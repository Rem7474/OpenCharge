package ingestion

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"opencharge/internal/domain"
	"opencharge/internal/repository"
)

// DefaultChargenowBaseURL is ChargeNow's (Digital Charging Solutions,
// "DCS") public map API.
const DefaultChargenowBaseURL = "https://chargenow.com/api/map/v1/fr"

const (
	chargenowQueryPath  = "/query"
	chargenowPricesPath = "/tariffs/CHARGENOW_PRIME/prices"
)

// chargenowHeaders mirrors the browser request ChargeNow's WAF accepts —
// same reasoning as izivia.go's iziviaHeaders: plain net/http requests
// without these get rejected outright. rest-api-path is the load-bearing
// one and is set per-request (see doRequest), not here, since its value
// differs between /query ("clusters", confirmed against a real response)
// and /tariffs/.../prices (unverified — see doRequest's comment).
var chargenowHeaders = map[string]string{
	"accept":          "application/json, text/plain, */*",
	"accept-language": "fr,fr-FR;q=0.9,en;q=0.8",
	"content-type":    "application/json",
	"cookie":          "CN_ALLOW_FUNCTIONAL_COOKIES_2=false; CN_ALLOW_GOOGLE_MAPS=true",
	"dnt":             "1",
	"origin":          "https://chargenow.com",
	"referer":         "https://chargenow.com/web/fr/cn-fr/map",
	"rest-api-path":   "clusters",
	"sec-fetch-dest":  "empty",
	"sec-fetch-mode":  "cors",
	"sec-fetch-site":  "same-origin",
	"User-Agent":      "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
}

const (
	chargenowSourceName  = "chargenow"
	chargenowTariffModel = "chargenow_ocpi"

	// Same France bounding box and grid step every other grid-scan
	// ingester (Izivia, Freshmile) uses.
	chargenowGridStep            = 2.0
	chargenowPrecision           = 3
	chargenowMaxSubdivisionDepth = 8
	// See freshmileMaxTilesVisited's comment for why this needs to be a
	// generous backstop, not a routine constraint: chargenowMaxSubdivisionDepth
	// and subdivideChargenowBBox's strict halving already bound worst-case
	// blowup per initial tile.
	chargenowMaxTilesVisited = 500000
	chargenowScanWorkers     = 16
	chargenowMaxRetries      = 5
	// How many (charge_point, power_type, power) triples go in one POST to
	// the prices endpoint — same reasoning as ingestionBulkChunkSize:
	// bound request/response size without turning thousands of pools into
	// thousands of round trips.
	chargenowPriceBatchSize = 200

	// Fallback power (kW) used only for a pool with no IRVE match within
	// MaxLinkDistanceM to read a real power_kw from — see Run's
	// correlate-before-price comment for why a real value is normally
	// available.
	chargenowDefaultACPowerKW = 22.0
	chargenowDefaultDCPowerKW = 50.0
)

type ChargenowConfig struct {
	Workers int
}

func DefaultChargenowConfig() ChargenowConfig {
	return ChargenowConfig{Workers: chargenowScanWorkers}
}

type ChargenowIngester struct {
	Pool             *pgxpool.Pool
	SourceStations   *repository.SourceStationRepository
	Tariffs          *repository.TariffRepository
	Links            *repository.LinkRepository
	BaseURL          string
	Config           ChargenowConfig
	MaxLinkDistanceM float64
	// Failures, when set, records every request that failed for good
	// (discovery query for a bbox, price batch — recorded per affected
	// pool) so a later -retry-failed pass can replay just those — see
	// FailureLog.
	Failures     *FailureLog
	client       *http.Client
	retryBackoff time.Duration // unexported: overridden by tests to keep them fast
}

func NewChargenowIngester(pool *pgxpool.Pool, sourceStations *repository.SourceStationRepository, tariffs *repository.TariffRepository, links *repository.LinkRepository, baseURL string, cfg ChargenowConfig) *ChargenowIngester {
	if baseURL == "" {
		baseURL = DefaultChargenowBaseURL
	}
	workers := cfg.Workers
	if workers <= 0 {
		workers = chargenowScanWorkers
	}
	// Same MaxIdleConnsPerHost fix as izivia.go's newIziviaHTTPClient:
	// http.DefaultTransport's default of 2 would otherwise serialize most
	// of a single-host worker fan-out behind repeated TCP/TLS handshakes.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = workers
	transport.MaxConnsPerHost = 0
	return &ChargenowIngester{
		Pool: pool, SourceStations: sourceStations, Tariffs: tariffs, Links: links,
		BaseURL: baseURL, Config: cfg, MaxLinkDistanceM: DefaultLinkMaxDistanceMeters,
		client:       &http.Client{Timeout: 20 * time.Second, Transport: transport},
		retryBackoff: 2 * time.Second,
	}
}

// Run scans ChargeNow's map for pools (physical charging locations)
// covering metropolitan France, then, for each pool, correlates it with
// the nearest IRVE station(s) up front — before fetching any price — since
// ChargeNow's own discovery API returns only a pool's location and its
// charge points' bare ids, never their power/connector type, while its
// pricing API requires exactly that (charge_point, power_type, power) to
// price anything. IRVE already knows the connector type/power for the
// physical location a pool corresponds to, so that's what's used to build
// the price query — every other ingester correlates only once, at write
// time in writeSourceStationChunk, but ChargeNow's price fetch itself
// depends on the correlation, not just the final station_links row.
func (ing *ChargenowIngester) Run(ctx context.Context) (int, error) {
	defer ing.Failures.saveAndLog()
	runStart := time.Now()

	pools, err := ing.scanPools(ctx)
	if err != nil {
		return 0, err
	}
	log.Printf("chargenow: %d pools found", len(pools))

	processed, err := ing.processPools(ctx, pools)
	if err != nil {
		return processed, err
	}
	log.Printf("chargenow: done, %d source stations processed", processed)

	// Only sweep after actually finding stations this run — see the same
	// guard (and the incident that motivated it) in izivia.go.
	if processed > 0 {
		if err := repository.SweepStaleSourceData(ctx, ing.Pool, chargenowSourceName, runStart.Add(-repository.StaleSourceDataGracePeriod)); err != nil {
			return processed, err
		}
	}
	return processed, nil
}

// RetryFailed replays only the requests a previous run recorded as failed
// (see FailureLog): failed discovery bboxes are re-scanned to recover
// their pools, and pools whose price batch failed are re-priced directly
// from the pool identity saved in the failure's params. Requests that
// fail again are re-recorded, so the failure file always reflects what's
// still outstanding. No stale-data sweep happens here: a retry pass only
// touches the previously-failed subset, so most known stations
// legitimately go un-refreshed.
func (ing *ChargenowIngester) RetryFailed(ctx context.Context, failures []FailedFetch) (int, error) {
	defer ing.Failures.saveAndLog()

	var bboxes []chargenowBBox
	poolsByID := map[string]chargenowPool{}
	for _, f := range failures {
		switch f.Kind {
		case failKindChargenowBBox:
			var bbox chargenowBBox
			if err := json.Unmarshal(f.Params, &bbox); err != nil {
				log.Printf("chargenow: skipping unreadable %s failure: %v", f.Kind, err)
				continue
			}
			bboxes = append(bboxes, bbox)
		case failKindChargenowPool:
			var pool chargenowPool
			if err := json.Unmarshal(f.Params, &pool); err != nil || pool.ID == "" {
				log.Printf("chargenow: skipping unreadable %s failure: %v", f.Kind, err)
				continue
			}
			poolsByID[pool.ID] = pool
		default:
			log.Printf("chargenow: skipping failure of unknown kind %q", f.Kind)
		}
	}

	if len(bboxes) > 0 {
		scanned, err := ing.scanPoolsFrom(ctx, bboxes)
		if err != nil {
			return 0, err
		}
		for _, p := range scanned {
			poolsByID[p.ID] = p
		}
	}

	pools := make([]chargenowPool, 0, len(poolsByID))
	for _, p := range poolsByID {
		pools = append(pools, p)
	}

	log.Printf("chargenow: retrying %d pools from %d recorded failure(s)", len(pools), len(failures))
	processed, err := ing.processPools(ctx, pools)
	log.Printf("chargenow: retry done, %d source stations processed", processed)
	return processed, err
}

// processPools correlates every pool with the IRVE referential, fetches
// its prices and writes the resulting source stations/tariffs — the
// shared pipeline behind both a full Run and a RetryFailed pass.
func (ing *ChargenowIngester) processPools(ctx context.Context, pools []chargenowPool) (int, error) {
	points := make([]repository.NearestStationQuery, len(pools))
	for i, p := range pools {
		points[i] = repository.NearestStationQuery{Lat: p.Lat, Lng: p.Lng}
	}
	nearestAC, err := ing.Links.FindNearestStationsForKind(ctx, points, domain.TariffKindAC, ing.MaxLinkDistanceM)
	if err != nil {
		return 0, fmt.Errorf("correlate chargenow pools (ac): %w", err)
	}
	nearestDC, err := ing.Links.FindNearestStationsForKind(ctx, points, domain.TariffKindDC, ing.MaxLinkDistanceM)
	if err != nil {
		return 0, fmt.Errorf("correlate chargenow pools (dc): %w", err)
	}

	type priceTarget struct {
		poolIdx int
		kind    string
	}
	var items []chargenowPriceQueryItem
	var targets []priceTarget
	for i, p := range pools {
		if len(p.ChargePointIDs) == 0 {
			continue
		}
		// One representative charge point per pool is enough: ChargeNow
		// prices by (power_type, power) for a given tariff plan, not by
		// individual physical charge point (confirmed by the sample
		// response — two different charge points at the same power/type
		// came back with the identical tariff id and price), and the API
		// still requires a valid charge_point id in the request payload.
		chargePointID := p.ChargePointIDs[0]
		if ac, ok := nearestAC[i]; ok {
			power := chargenowDefaultACPowerKW
			if ac.PowerKW != nil {
				power = *ac.PowerKW
			}
			items = append(items, chargenowPriceQueryItem{ChargePoint: chargePointID, PowerType: "AC", Power: power})
			targets = append(targets, priceTarget{poolIdx: i, kind: domain.TariffKindAC})
		}
		if dc, ok := nearestDC[i]; ok {
			power := chargenowDefaultDCPowerKW
			if dc.PowerKW != nil {
				power = *dc.PowerKW
			}
			items = append(items, chargenowPriceQueryItem{ChargePoint: chargePointID, PowerType: "DC", Power: power})
			targets = append(targets, priceTarget{poolIdx: i, kind: domain.TariffKindDC})
		}
	}

	targetByKey := make(map[string]priceTarget, len(items))
	for i, it := range items {
		targetByKey[chargenowPriceKey(it.ChargePoint, it.PowerType, it.Power)] = targets[i]
	}

	type poolPrice struct {
		energyCents *float64
		flatCents   *float64
	}
	pricesByPoolKind := make(map[string]poolPrice)

	// A failed price batch loses the tariffs of every pool it covered:
	// record each affected pool (dedup'd — a pool contributes one AC and
	// one DC item to the same batch) so a -retry-failed pass can re-price
	// exactly those, without re-scanning the whole map. Skipped when the
	// run is just being canceled — see the same guard in izivia.go.
	recordFailedBatch := func(targets []priceTarget, start, end int, err error) {
		if ctx.Err() != nil {
			return
		}
		recorded := map[int]struct{}{}
		for _, target := range targets[start:end] {
			if _, dup := recorded[target.poolIdx]; dup {
				continue
			}
			recorded[target.poolIdx] = struct{}{}
			ing.Failures.Record(failKindChargenowPool, ing.BaseURL+chargenowPricesPath, pools[target.poolIdx], err)
		}
	}

	for start := 0; start < len(items); start += chargenowPriceBatchSize {
		end := start + chargenowPriceBatchSize
		if end > len(items) {
			end = len(items)
		}
		body, err := json.Marshal(items[start:end])
		if err != nil {
			return 0, fmt.Errorf("marshal chargenow price batch: %w", err)
		}
		respBody, err := ing.withRetries(ctx, ing.BaseURL+chargenowPricesPath, func() ([]byte, int, error) {
			return ing.doRequest(ctx, ing.BaseURL+chargenowPricesPath, "prices", body)
		})
		if err != nil {
			log.Printf("chargenow: price batch [%d:%d] failed, skipping: %v", start, end, err)
			recordFailedBatch(targets, start, end, err)
			continue
		}
		var results []chargenowPriceResult
		if err := json.Unmarshal(respBody, &results); err != nil {
			log.Printf("chargenow: decode price batch [%d:%d] failed, skipping: %v", start, end, err)
			recordFailedBatch(targets, start, end, err)
			continue
		}
		for _, res := range results {
			key := chargenowPriceKey(res.PriceIdentifier.ChargePoint, res.PriceIdentifier.PowerType, res.PriceIdentifier.Power)
			target, ok := targetByKey[key]
			if !ok {
				continue
			}
			energyCents, flatCents := chargenowExtractPrices(res)
			pricesByPoolKind[fmt.Sprintf("%d|%s", target.poolIdx, target.kind)] = poolPrice{energyCents, flatCents}
		}
		log.Printf("chargenow: %d/%d price queries done", end, len(items))
	}

	var normalized []normalizedSourceStation
	for i, p := range pools {
		var tariffs []domain.StationTariff
		for _, kind := range [2]string{domain.TariffKindAC, domain.TariffKindDC} {
			pp, ok := pricesByPoolKind[fmt.Sprintf("%d|%s", i, kind)]
			if !ok || pp.energyCents == nil {
				continue
			}
			tariffs = append(tariffs, domain.StationTariff{
				Source: chargenowSourceName, Plan: domain.TariffPlanStandard, Kind: kind,
				Model: chargenowTariffModel, Currency: "EUR",
				EnergyPriceCentsPerKWh: pp.energyCents, SessionFeeCents: pp.flatCents,
				Extra: map[string]any{},
			})
		}
		if len(tariffs) == 0 {
			continue
		}
		normalized = append(normalized, normalizedSourceStation{
			Station: domain.SourceStation{
				Source: chargenowSourceName, SourceStationID: p.ID,
				Name: "ChargeNow", OperatorName: "ChargeNow",
				Lat: p.Lat, Lng: p.Lng,
				Raw: map[string]any{"chargePointIds": p.ChargePointIDs},
			},
			Tariffs: tariffs,
		})
	}

	processed := 0
	for i := 0; i < len(normalized); i += ingestionBulkChunkSize {
		end := i + ingestionBulkChunkSize
		if end > len(normalized) {
			end = len(normalized)
		}
		n, err := writeSourceStationChunk(ctx, ing.Pool, ing.SourceStations, ing.Tariffs, ing.Links, ing.MaxLinkDistanceM, normalized[i:end])
		processed += n
		if err != nil {
			return processed, err
		}
		log.Printf("chargenow: %d/%d processed", processed, len(normalized))
	}
	// Same guard as freshmile's runPipeline: price batches that failed on
	// a mid-run ctx expiry are only logged and skipped above, so without
	// surfacing ctx.Err() a truncated run could look fully successful to
	// Run(), which would then sweep stations this run never re-priced.
	if err := ctx.Err(); err != nil {
		return processed, err
	}
	return processed, nil
}

func chargenowPriceKey(chargePoint, powerType string, power float64) string {
	return fmt.Sprintf("%s|%s|%g", chargePoint, powerType, power)
}

// chargenowExtractPrices reads a price result's elements for the ENERGY
// (€/kWh, converted to cents) and FLAT (a one-time session fee, same
// concept as domain.StationTariff.SessionFeeCents — already used for
// Izivia's "2,3€ la session de charge") price components. Other OCPI
// component types (TIME, PARKING_TIME, ...) aren't modeled by
// StationTariff yet and are ignored rather than causing an error — that's
// display-lossy, not wrong: the energy price is still captured correctly.
func chargenowExtractPrices(result chargenowPriceResult) (energyCents, flatCents *float64) {
	for _, el := range result.Elements {
		for _, pc := range el.PriceComponents {
			switch pc.Type {
			case "ENERGY":
				v := pc.Price * 100
				energyCents = &v
			case "FLAT":
				v := pc.Price * 100
				flatCents = &v
			}
		}
	}
	return energyCents, flatCents
}

// chargenowPool's and chargenowBBox's JSON tags matter: a failed price
// fetch is persisted per affected pool, and a failed discovery query per
// bbox, in the failure log (see FailureLog) and read back by RetryFailed.
type chargenowPool struct {
	ID             string   `json:"id"`
	Lat            float64  `json:"lat"`
	Lng            float64  `json:"lng"`
	ChargePointIDs []string `json:"chargePointIds"`
}

type chargenowBBox struct {
	MinLng float64 `json:"minLng"`
	MinLat float64 `json:"minLat"`
	MaxLng float64 `json:"maxLng"`
	MaxLat float64 `json:"maxLat"`
}

// subdivideChargenowBBox splits a bbox into 4 quadrants — identical shape
// to freshmile.go's subdivideBBox, kept as its own small copy rather than
// sharing a type across files for two unrelated sources.
func subdivideChargenowBBox(b chargenowBBox) []chargenowBBox {
	midLng := (b.MinLng + b.MaxLng) / 2
	midLat := (b.MinLat + b.MaxLat) / 2
	return []chargenowBBox{
		{MinLng: b.MinLng, MinLat: b.MinLat, MaxLng: midLng, MaxLat: midLat},
		{MinLng: midLng, MinLat: b.MinLat, MaxLng: b.MaxLng, MaxLat: midLat},
		{MinLng: b.MinLng, MinLat: midLat, MaxLng: midLng, MaxLat: b.MaxLat},
		{MinLng: midLng, MinLat: midLat, MaxLng: b.MaxLng, MaxLat: b.MaxLat},
	}
}

// padDegenerateChargenowBBox guards against a zero-area bbox the same way
// freshmile.go's padDegenerateBBox does — a cluster's own reported
// bounding box can collapse to a single point on one axis (several
// stations at the exact same coordinate).
func padDegenerateChargenowBBox(b chargenowBBox) chargenowBBox {
	const minSpan = 0.0005
	if b.MaxLng-b.MinLng < minSpan {
		mid := (b.MinLng + b.MaxLng) / 2
		b.MinLng, b.MaxLng = mid-minSpan/2, mid+minSpan/2
	}
	if b.MaxLat-b.MinLat < minSpan {
		mid := (b.MinLat + b.MaxLat) / 2
		b.MinLat, b.MaxLat = mid-minSpan/2, mid+minSpan/2
	}
	return b
}

// scanPools grid-scans metropolitan France for ChargeNow pools, recursing
// into any cluster the API returns still-too-coarse (chargePointCount
// above what's resolvable at the requested precision) — same
// discover-then-subdivide shape as freshmile.go's scanLocationIDs, using
// chargePointCount/boundingBox* instead of location_count/properties.bbox.
func (ing *ChargenowIngester) scanPools(ctx context.Context) ([]chargenowPool, error) {
	const step = chargenowGridStep
	minLng, maxLng := -5.5, 9.8
	minLat, maxLat := 41.0, 51.5

	var initial []chargenowBBox
	for lat := minLat; lat < maxLat; lat += step {
		for lng := minLng; lng < maxLng; lng += step {
			initial = append(initial, chargenowBBox{
				MinLng: lng, MinLat: lat,
				MaxLng: min(lng+step, maxLng), MaxLat: min(lat+step, maxLat),
			})
		}
	}
	// See izivia.go/freshmile.go's identical shuffle: without this, a
	// chronically-timing-out run always gets cut off at the same tail end
	// of this fixed grid order.
	rand.Shuffle(len(initial), func(i, j int) { initial[i], initial[j] = initial[j], initial[i] })

	return ing.scanPoolsFrom(ctx, initial)
}

// scanPoolsFrom is scanPools' engine, starting from an arbitrary list of
// initial boxes rather than always the full-France grid — RetryFailed
// reuses it to re-scan just the bboxes a previous run recorded as failed.
func (ing *ChargenowIngester) scanPoolsFrom(ctx context.Context, initial []chargenowBBox) ([]chargenowPool, error) {
	var (
		mu   sync.Mutex
		seen = map[string]chargenowPool{}
	)
	var visited int64
	sem := make(chan struct{}, ing.workers())
	var wg sync.WaitGroup

	var scan func(bbox chargenowBBox, depth int)
	scan = func(bbox chargenowBBox, depth int) {
		defer wg.Done()
		if ctx.Err() != nil || atomic.LoadInt64(&visited) >= chargenowMaxTilesVisited {
			return
		}
		atomic.AddInt64(&visited, 1)

		bbox = padDegenerateChargenowBBox(bbox)
		resp, err := ing.fetchQuery(ctx, bbox, sem)
		if err != nil {
			log.Printf("chargenow: query bbox %+v failed: %v", bbox, err)
			// A failed query drops its whole branch of the subdivision
			// tree — record it for a targeted retry, unless the scan is
			// just being canceled (see the same guard in izivia.go).
			if ctx.Err() == nil {
				ing.Failures.Record(failKindChargenowBBox, ing.BaseURL+chargenowQueryPath, bbox, err)
			}
			return
		}

		mu.Lock()
		for _, p := range resp.Pools {
			ids := make([]string, len(p.ChargePoints))
			for i, cp := range p.ChargePoints {
				ids[i] = cp.ID
			}
			seen[p.ID] = chargenowPool{ID: p.ID, Lat: p.Latitude, Lng: p.Longitude, ChargePointIDs: ids}
		}
		mu.Unlock()

		if depth >= chargenowMaxSubdivisionDepth {
			return
		}
		for _, c := range resp.PoolClusters {
			sub := chargenowBBox{
				MinLng: c.BoundingBoxLongitudeNW, MaxLat: c.BoundingBoxLatitudeNW,
				MaxLng: c.BoundingBoxLongitudeSE, MinLat: c.BoundingBoxLatitudeSE,
			}
			for _, q := range subdivideChargenowBBox(sub) {
				wg.Add(1)
				go scan(q, depth+1)
			}
		}
	}

	for _, tile := range initial {
		wg.Add(1)
		go scan(tile, 0)
	}
	wg.Wait()

	pools := make([]chargenowPool, 0, len(seen))
	for _, p := range seen {
		pools = append(pools, p)
	}
	return pools, nil
}

func (ing *ChargenowIngester) workers() int {
	if ing.Config.Workers > 0 {
		return ing.Config.Workers
	}
	return chargenowScanWorkers
}

type chargenowQueryResponse struct {
	PoolClusters []chargenowPoolCluster `json:"poolClusters"`
	Pools        []chargenowRawPool     `json:"pools"`
}

type chargenowPoolCluster struct {
	ChargePointCount       int     `json:"chargePointCount"`
	Longitude              float64 `json:"longitude"`
	Latitude               float64 `json:"latitude"`
	BoundingBoxLongitudeNW float64 `json:"boundingBoxLongitudeNW"`
	BoundingBoxLatitudeNW  float64 `json:"boundingBoxLatitudeNW"`
	BoundingBoxLongitudeSE float64 `json:"boundingBoxLongitudeSE"`
	BoundingBoxLatitudeSE  float64 `json:"boundingBoxLatitudeSE"`
}

type chargenowRawPool struct {
	ID           string                    `json:"id"`
	Longitude    float64                   `json:"longitude"`
	Latitude     float64                   `json:"latitude"`
	ChargePoints []chargenowRawChargePoint `json:"chargePoints"`
}

type chargenowRawChargePoint struct {
	ID string `json:"id"`
}

type chargenowPriceQueryItem struct {
	ChargePoint string  `json:"charge_point"`
	PowerType   string  `json:"power_type"`
	Power       float64 `json:"power"`
}

type chargenowPriceResult struct {
	Currency        string                  `json:"currency"`
	Elements        []chargenowPriceElement `json:"elements"`
	PriceIdentifier chargenowPriceQueryItem `json:"price_identifier"`
}

type chargenowPriceElement struct {
	PriceComponents []chargenowPriceComponent `json:"price_components"`
}

type chargenowPriceComponent struct {
	Type  string  `json:"type"`
	Price float64 `json:"price"`
}

// fetchQuery POSTs the discovery payload for one bbox — searchCriteria
// field names/shape and filterCriteria's empty-array defaults match the
// working reference request exactly (precision 3, unpackSolitudeCluster
// false, unpackClustersWithSinglePool true).
func (ing *ChargenowIngester) fetchQuery(ctx context.Context, bbox chargenowBBox, sem chan struct{}) (*chargenowQueryResponse, error) {
	payload := map[string]any{
		"searchCriteria": map[string]any{
			"latitudeNW":                   bbox.MaxLat,
			"longitudeNW":                  bbox.MinLng,
			"latitudeSE":                   bbox.MinLat,
			"longitudeSE":                  bbox.MaxLng,
			"precision":                    chargenowPrecision,
			"unpackSolitudeCluster":        false,
			"unpackClustersWithSinglePool": true,
		},
		"withChargePointIds": true,
		"filterCriteria": map[string]any{
			"authenticationMethods": []any{},
			"cableAttachedTypes":    []any{},
			"paymentMethods":        []any{},
			"plugTypes":             []any{},
			"poolLocationTypes":     []any{},
			"valueAddedServices":    []any{},
			"dcsTcpoIds":            []any{},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal chargenow query payload: %w", err)
	}

	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	respBody, err := ing.withRetries(ctx, ing.BaseURL+chargenowQueryPath, func() ([]byte, int, error) {
		return ing.doRequest(ctx, ing.BaseURL+chargenowQueryPath, "clusters", body)
	})
	if err != nil {
		return nil, err
	}
	var resp chargenowQueryResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("decode chargenow query response: %w", err)
	}
	return &resp, nil
}

// withRetries retries do (one HTTP attempt) on a transient failure —
// network error, timeout, or 5xx — up to chargenowMaxRetries times with
// exponential backoff. Same pattern as izivia.go's withRetries.
func (ing *ChargenowIngester) withRetries(ctx context.Context, label string, do func() ([]byte, int, error)) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt <= chargenowMaxRetries; attempt++ {
		if attempt > 0 {
			backoff := (1 << (attempt - 1)) * ing.retryBackoff
			log.Printf("chargenow: retrying %s in %v (attempt %d/%d) after: %v", label, backoff, attempt+1, chargenowMaxRetries+1, lastErr)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		body, status, err := do()
		if err == nil {
			return body, nil
		}
		lastErr = err
		if status != 0 && status < 500 {
			break
		}
	}
	return nil, lastErr
}

// doRequest performs a single HTTP request with ChargeNow's WAF-required
// headers. restAPIPath is the load-bearing one: confirmed "clusters" for
// /query against a real response; "prices" for /tariffs/.../prices is an
// educated guess (same "last meaningful path segment" pattern) that has
// NOT been verified against a live response — this project's sandbox has
// no network access to chargenow.com to confirm it. If the price-fetch
// phase starts failing in production with a WAF-shaped rejection (opaque
// 403/406, not an ordinary "unknown charge_point" or auth error from
// ChargeNow's own API), check this value first — capture the real header
// from a browser DevTools request to /tariffs/CHARGENOW_PRIME/prices and
// update this constant.
func (ing *ChargenowIngester) doRequest(ctx context.Context, url, restAPIPath string, body []byte) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, 0, fmt.Errorf("build request for %s: %w", url, err)
	}
	for k, v := range chargenowHeaders {
		req.Header.Set(k, v)
	}
	req.Header.Set("rest-api-path", restAPIPath)
	resp, err := ing.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("chargenow request to %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		// errorBodySummary (izivia.go) trims to the first non-empty line,
		// capped — generically useful for any non-2xx body, not specific
		// to Izivia's Java stack traces.
		return nil, resp.StatusCode, fmt.Errorf("chargenow http %d for %s: %s", resp.StatusCode, url, errorBodySummary(data))
	}
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("chargenow read body from %s: %w", url, err)
	}
	return respBody, resp.StatusCode, nil
}
