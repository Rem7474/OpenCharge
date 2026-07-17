package ingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"opencharge/internal/domain"
	"opencharge/internal/repository"
)

// DefaultFreshmileBaseURL is Freshmile's map/charging-location API.
const DefaultFreshmileBaseURL = "https://prod-driver-api.freshmile.com/charge/api/v2"

// freshmileZoom is passed to map-locations on every tile request. Since we
// control tile size ourselves via bbox subdivision, the exact value mostly
// just needs to be "detailed enough" for the API to return real points
// rather than an even coarser cluster than our bbox implies.
const freshmileZoom = 14

// freshmileMaxSubdivisionDepth bounds cluster subdivision: a cluster that's
// still a cluster after this many splits is dropped rather than recursed
// into forever (e.g. many stations occupying the exact same coordinates).
const freshmileMaxSubdivisionDepth = 8

// freshmileMaxTilesVisited is a hard safety cap on the total number of
// map-locations calls in one Run, independent of depth, in case pathological
// cluster geometry causes an explosion in sibling tiles.
const freshmileMaxTilesVisited = 20000

// freshmileScanWorkers bounds how many map-locations requests the
// discovery scan (fetchAllLocationIDs) has in flight at once. The scan is
// a tree of tile/cluster-subdivision fetches, not a flat list, so it's
// parallelized with a semaphore-bounded recursive fan-out rather than a
// simple worker-pool-over-a-channel like the detail-fetch phase.
const freshmileScanWorkers = 16

// freshmileFlushTimeout bounds how long a single batch write is allowed
// to take. It's deliberately generous (this isn't a "how long is a
// healthy write" tuning knob, just a backstop against a truly hung
// query) since writeResults always runs the write decoupled from the
// run's own ctx — see there for why.
const freshmileFlushTimeout = 2 * time.Minute

// freshmileProgressLogInterval controls how often scanLocationIDs logs
// progress while it scans, on a wall-clock ticker rather than a tile
// count — the discovery phase's tile count can advance in bursts (a big
// cluster subdivides into many quick sibling requests) or stall for a
// while (a dense area recursing deeply, or several workers retrying at
// once), so a fixed "every N tiles" interval leaves long silent stretches
// where a slow-but-healthy run is indistinguishable from a hung one. A
// wall-clock heartbeat guarantees a log line on a predictable cadence
// regardless of how the tile count is moving.
const freshmileProgressLogInterval = 10 * time.Second

// freshmileMinBBoxDegrees is the minimum width/height a map-locations
// query bbox is padded out to (~100m at French latitudes). Freshmile's API
// returns a 500 for a zero-area bbox, which happens in practice when a
// cluster's own reported bbox.sw/bbox.ne collapse to the same coordinate
// on one axis (observed: several stations at the exact same longitude).
const freshmileMinBBoxDegrees = 0.001

// padDegenerateBBox widens bbox around its center on any axis narrower
// than freshmileMinBBoxDegrees, leaving a normal-sized bbox untouched.
func padDegenerateBBox(b freshmileBBox) freshmileBBox {
	if b.MaxLng-b.MinLng < freshmileMinBBoxDegrees {
		centerLng := (b.MinLng + b.MaxLng) / 2
		b.MinLng = centerLng - freshmileMinBBoxDegrees/2
		b.MaxLng = centerLng + freshmileMinBBoxDegrees/2
	}
	if b.MaxLat-b.MinLat < freshmileMinBBoxDegrees {
		centerLat := (b.MinLat + b.MaxLat) / 2
		b.MinLat = centerLat - freshmileMinBBoxDegrees/2
		b.MaxLat = centerLat + freshmileMinBBoxDegrees/2
	}
	return b
}

// FreshmileConfig tunes the per-location detail-fetch worker pool.
type FreshmileConfig struct {
	Workers int
}

func DefaultFreshmileConfig() FreshmileConfig {
	return FreshmileConfig{Workers: 8}
}

type FreshmileIngester struct {
	Pool             *pgxpool.Pool
	SourceStations   *repository.SourceStationRepository
	Tariffs          *repository.TariffRepository
	Links            *repository.LinkRepository
	BaseURL          string
	Config           FreshmileConfig
	MaxLinkDistanceM float64
	client           *http.Client
	retryBackoff     time.Duration // unexported: overridden by tests to keep them fast
}

func NewFreshmileIngester(pool *pgxpool.Pool, sourceStations *repository.SourceStationRepository, tariffs *repository.TariffRepository, links *repository.LinkRepository, baseURL string, cfg FreshmileConfig) *FreshmileIngester {
	if baseURL == "" {
		baseURL = DefaultFreshmileBaseURL
	}
	workers := cfg.Workers
	if workers <= 0 {
		workers = 8
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = max(workers, freshmileScanWorkers)
	return &FreshmileIngester{
		Pool:             pool,
		SourceStations:   sourceStations,
		Tariffs:          tariffs,
		Links:            links,
		BaseURL:          baseURL,
		Config:           cfg,
		MaxLinkDistanceM: DefaultLinkMaxDistanceMeters,
		client:           &http.Client{Timeout: 60 * time.Second, Transport: transport},
		retryBackoff:     2 * time.Second,
	}
}

// Run discovers Freshmile locations (recursively resolving map clusters
// down to individual points) and fetches/writes them in a single streaming
// pipeline, rather than discovering the whole of France before fetching
// anything: the discovery scan feeds location IDs onto idCh as it finds
// them, detail-fetch workers consume idCh concurrently, and a single
// writer batches results onto the database via writeSourceStationChunk as
// they arrive. That way a run stopped partway through (ctx canceled,
// timeout hit) keeps whatever's already been written instead of losing
// everything — discovery alone used to take the vast majority of a run's
// wall-clock time with nothing durable to show for it yet.
func (ing *FreshmileIngester) Run(ctx context.Context) (int, error) {
	idCh := make(chan int, 100)
	resultsCh := make(chan normalizedSourceStation)

	var scanWG sync.WaitGroup
	scanWG.Add(1)
	go func() {
		defer scanWG.Done()
		defer close(idCh)
		ing.scanLocationIDs(ctx, idCh)
	}()

	var workerWG sync.WaitGroup
	worker := func() {
		defer workerWG.Done()
		for id := range idCh {
			item, ok, err := ing.fetchAndNormalizeLocation(ctx, id)
			if err != nil {
				log.Printf("freshmile: location %d failed: %v", id, err)
				continue
			}
			if !ok {
				continue
			}
			select {
			case resultsCh <- item:
			case <-ctx.Done():
				return
			}
		}
	}

	workers := ing.Config.Workers
	if workers <= 0 {
		workers = 8
	}
	for i := 0; i < workers; i++ {
		workerWG.Add(1)
		go worker()
	}
	go func() {
		workerWG.Wait()
		close(resultsCh)
	}()

	processed, err := ing.writeResults(ctx, resultsCh)
	scanWG.Wait()

	log.Printf("freshmile: done, %d locations processed", processed)
	return processed, err
}

// writeResults drains resultsCh, batching writes by ingestionBulkChunkSize
// through writeSourceStationChunk. The total location count isn't known
// upfront (discovery and fetching now run concurrently), so progress is
// logged as a running count rather than "x/y".
func (ing *FreshmileIngester) writeResults(ctx context.Context, resultsCh <-chan normalizedSourceStation) (int, error) {
	processed := 0
	batch := make([]normalizedSourceStation, 0, ingestionBulkChunkSize)

	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		// Always decoupled from ctx (context.WithoutCancel), not just once
		// ctx has already ended: this batch is fully collected in memory
		// by this point, so once we've committed to writing it, a SIGINT
		// or -timeout landing mid-query shouldn't be able to abort it —
		// checking ctx.Err() only at the top of flush() left a race where
		// cancellation arriving after that check but before the query
		// finished still aborted an in-progress write
		// ("canceling statement due to user request" observed in
		// practice). The bound is generous (freshmileFlushTimeout, not
		// the short shutdown-specific grace period an earlier version of
		// this used) so it doesn't interfere with a legitimately slow
		// write under concurrent load — it's just a backstop against a
		// truly hung query, not a race with the run's own cancellation.
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), freshmileFlushTimeout)
		defer cancel()
		n, err := writeSourceStationChunk(writeCtx, ing.Pool, ing.SourceStations, ing.Tariffs, ing.Links, ing.MaxLinkDistanceM, batch)
		processed += n
		batch = batch[:0]
		if err != nil {
			return err
		}
		log.Printf("freshmile: %d processed so far", processed)
		return nil
	}

	for item := range resultsCh {
		batch = append(batch, item)
		if len(batch) >= ingestionBulkChunkSize {
			if err := flush(); err != nil {
				return processed, err
			}
		}
	}
	if err := flush(); err != nil {
		return processed, err
	}
	return processed, nil
}

// fetchAndNormalizeLocation does the I/O-bound work for one location id
// (GET /locations/{id}, then normalization) without touching the
// database — writes are batched separately, see Run.
func (ing *FreshmileIngester) fetchAndNormalizeLocation(ctx context.Context, id int) (normalizedSourceStation, bool, error) {
	body, err := ing.getJSON(ctx, fmt.Sprintf("%s/locations/%d", ing.BaseURL, id), nil)
	if err != nil {
		return normalizedSourceStation{}, false, fmt.Errorf("fetch location details: %w", err)
	}
	// Unlike map-locations (a bare {"features":[...]}), /locations/{id}
	// wraps the actual location object in a "data" envelope.
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return normalizedSourceStation{}, false, fmt.Errorf("decode location details: %w", err)
	}
	details := envelope.Data
	if details == nil {
		return normalizedSourceStation{}, false, fmt.Errorf("location details response missing \"data\" envelope")
	}

	src, ok := normalizeFreshmileStation(details)
	if !ok {
		return normalizedSourceStation{}, false, fmt.Errorf("station without usable location")
	}
	return normalizedSourceStation{Station: src, Tariffs: normalizeFreshmileTariffs(details)}, true, nil
}

// freshmileBBox is a [minLng,minLat,maxLng,maxLat] map-locations query box.
type freshmileBBox struct {
	MinLng, MinLat, MaxLng, MaxLat float64
}

// scanLocationIDs scans map-locations across metropolitan France, starting
// from a coarse grid of tiles, and sends each newly-discovered unique
// location ID onto idCh as it's found (closing idCh is the caller's job,
// once this returns). Any feature that's a cluster (location_count > 1) is
// resolved by recursively subdividing its own bbox — never fetched via
// /locations directly — until only unique points remain or
// freshmileMaxSubdivisionDepth is hit, in which case that (persistently
// clustered) branch is dropped rather than recursed into forever. Network
// errors on one tile are logged and skipped, not fatal.
//
// The scan tree is fanned out across goroutines rather than walked
// sequentially: discovery used to take the vast majority of a run's
// wall-clock time while producing nothing durable, so both scanning
// itself and (via idCh) the detail-fetch/write phase that consumes it now
// overlap. Concurrency is bounded by freshmileScanWorkers via a
// semaphore, but the semaphore only wraps each individual HTTP attempt
// (see getJSON) — never the retry backoff sleep, nor feature processing,
// nor the idCh send below — so a tile that's retrying or a scan()
// blocked handing an ID to a slow write side doesn't tie up one of the
// limited concurrent-request slots while it isn't actually using the
// network.
func (ing *FreshmileIngester) scanLocationIDs(ctx context.Context, idCh chan<- int) {
	const step = 2.0
	minLng, maxLng := -5.5, 9.8
	minLat, maxLat := 41.0, 51.5

	var initial []freshmileBBox
	for lat := minLat; lat < maxLat; lat += step {
		for lng := minLng; lng < maxLng; lng += step {
			initial = append(initial, freshmileBBox{
				MinLng: lng, MinLat: lat,
				MaxLng: min(lng+step, maxLng), MaxLat: min(lat+step, maxLat),
			})
		}
	}

	var (
		mu   sync.Mutex
		seen = map[int]struct{}{}
	)
	var visited int64
	sem := make(chan struct{}, freshmileScanWorkers)
	var wg sync.WaitGroup

	logDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(freshmileProgressLogInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				mu.Lock()
				found := len(seen)
				mu.Unlock()
				log.Printf("freshmile: scanning map-locations, %d tiles visited so far, %d locations found", atomic.LoadInt64(&visited), found)
			case <-logDone:
				return
			}
		}
	}()

	var scan func(bbox freshmileBBox, depth int)
	scan = func(bbox freshmileBBox, depth int) {
		defer wg.Done()
		if ctx.Err() != nil || atomic.LoadInt64(&visited) >= freshmileMaxTilesVisited {
			return
		}

		atomic.AddInt64(&visited, 1)

		// A cluster's own reported bbox can collapse to a single point on
		// one axis (real data: e.g. several stations at the exact same
		// longitude) — querying map-locations with a zero-area bbox gets a
		// 500 from Freshmile's API, so always pad before querying,
		// regardless of whether bbox came from the initial grid or a
		// cluster subdivision.
		bbox = padDegenerateBBox(bbox)

		features, err := ing.fetchMapLocations(ctx, bbox, sem)
		if err != nil {
			log.Printf("freshmile: map-locations bbox %+v failed: %v", bbox, err)
			return
		}
		for _, f := range features {
			props, _ := f["properties"].(map[string]any)
			if props == nil {
				continue
			}
			count, countOK := floatValue(props["location_count"])
			if !countOK || count == nil {
				continue
			}
			if *count <= 1 {
				locID, ok := floatValue(props["location_id"])
				if !ok || locID == nil {
					continue
				}
				id := int(*locID)
				mu.Lock()
				_, dup := seen[id]
				if !dup {
					seen[id] = struct{}{}
				}
				mu.Unlock()
				if !dup {
					select {
					case idCh <- id:
					case <-ctx.Done():
					}
				}
				continue
			}
			if depth >= freshmileMaxSubdivisionDepth {
				continue
			}
			clusterBBox, ok := freshmileClusterBBox(props)
			if !ok {
				continue
			}
			for _, sub := range subdivideBBox(clusterBBox) {
				wg.Add(1)
				go scan(sub, depth+1)
			}
		}
	}

	for _, tile := range initial {
		wg.Add(1)
		go scan(tile, 0)
	}
	wg.Wait()
	close(logDone)

	log.Printf("freshmile: discovery done, %d unique locations across %d map-locations tiles visited", len(seen), atomic.LoadInt64(&visited))
}

// freshmileClusterBBox reads properties.bbox.{sw,ne} off a cluster feature.
func freshmileClusterBBox(props map[string]any) (freshmileBBox, bool) {
	bboxRaw, _ := props["bbox"].(map[string]any)
	if bboxRaw == nil {
		return freshmileBBox{}, false
	}
	sw, _ := bboxRaw["sw"].([]any)
	ne, _ := bboxRaw["ne"].([]any)
	if len(sw) < 2 || len(ne) < 2 {
		return freshmileBBox{}, false
	}
	swLng, ok1 := floatValue(sw[0])
	swLat, ok2 := floatValue(sw[1])
	neLng, ok3 := floatValue(ne[0])
	neLat, ok4 := floatValue(ne[1])
	if !ok1 || !ok2 || !ok3 || !ok4 || swLng == nil || swLat == nil || neLng == nil || neLat == nil {
		return freshmileBBox{}, false
	}
	return freshmileBBox{MinLng: *swLng, MinLat: *swLat, MaxLng: *neLng, MaxLat: *neLat}, true
}

// subdivideBBox splits a bbox into 4 quadrants.
func subdivideBBox(b freshmileBBox) []freshmileBBox {
	midLng := (b.MinLng + b.MaxLng) / 2
	midLat := (b.MinLat + b.MaxLat) / 2
	return []freshmileBBox{
		{MinLng: b.MinLng, MinLat: b.MinLat, MaxLng: midLng, MaxLat: midLat},
		{MinLng: midLng, MinLat: b.MinLat, MaxLng: b.MaxLng, MaxLat: midLat},
		{MinLng: b.MinLng, MinLat: midLat, MaxLng: midLng, MaxLat: b.MaxLat},
		{MinLng: midLng, MinLat: midLat, MaxLng: b.MaxLng, MaxLat: b.MaxLat},
	}
}

func (ing *FreshmileIngester) fetchMapLocations(ctx context.Context, bbox freshmileBBox, sem chan struct{}) ([]map[string]any, error) {
	url := fmt.Sprintf("%s/map-locations?bbox=%g,%g,%g,%g&zoom=%d",
		ing.BaseURL, bbox.MinLng, bbox.MinLat, bbox.MaxLng, bbox.MaxLat, freshmileZoom)
	body, err := ing.getJSON(ctx, url, sem)
	if err != nil {
		return nil, err
	}
	var collection struct {
		Features []map[string]any `json:"features"`
	}
	if err := json.Unmarshal(body, &collection); err != nil {
		return nil, fmt.Errorf("decode map-locations: %w", err)
	}
	return collection.Features, nil
}

// freshmileMaxRetries is how many times a transient failure (network error
// or 5xx, e.g. the Azure Application Gateway fronting Freshmile's API
// occasionally returning 504 Gateway Timeout) is retried before giving up
// on that request. 4xx responses are never retried — they won't succeed
// on a second try.
//
// A map-locations failure here isn't like a single location's detail
// fetch failing (that just skips one station): scanLocationIDs drops the
// whole tile/cluster branch and never revisits it, so a region can go
// permanently missing from discovery. That, plus scanLocationIDs now
// running freshmileScanWorkers requests concurrently (more simultaneous
// load on the same gateway than the old sequential scan ever produced),
// makes a short retry budget more likely to be exhausted by transient
// 504s than it used to be — so this is deliberately more generous than a
// single-request retry would normally need, with exponential backoff
// instead of linear.
const freshmileMaxRetries = 5

// getJSON performs a GET with retry/backoff on transient failures. sem, if
// non-nil, bounds how many of these requests run concurrently across all
// callers sharing it (see scanLocationIDs) — it's acquired and released
// around each individual attempt in doGet, not around the whole retry
// loop, so a request that's sleeping between retries gives up its slot
// instead of holding it idle for up to freshmileMaxRetries backoffs.
func (ing *FreshmileIngester) getJSON(ctx context.Context, url string, sem chan struct{}) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt <= freshmileMaxRetries; attempt++ {
		if attempt > 0 {
			backoff := (1 << (attempt - 1)) * ing.retryBackoff
			log.Printf("freshmile: retrying %s in %v (attempt %d/%d) after: %v", url, backoff, attempt+1, freshmileMaxRetries+1, lastErr)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		body, status, err := ing.doGet(ctx, url, sem)
		if err == nil {
			return body, nil
		}
		lastErr = err
		// status == 0 means no HTTP response at all (network/timeout
		// error) — treat that as transient too, same as a 5xx.
		if status != 0 && status < 500 {
			break
		}
	}
	return nil, lastErr
}

// doGet performs a single GET, always including the full URL in any
// returned error so a failure is directly reproducible (e.g. via curl)
// without having to reconstruct it from a bbox or id logged separately.
// sem, if non-nil, is acquired for the duration of this one attempt only.
func (ing *FreshmileIngester) doGet(ctx context.Context, url string, sem chan struct{}) ([]byte, int, error) {
	if sem != nil {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return nil, 0, ctx.Err()
		}
		defer func() { <-sem }()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("build request for %s: %w", url, err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")

	resp, err := ing.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("freshmile request to %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return nil, resp.StatusCode, fmt.Errorf("freshmile http %d for %s: %s", resp.StatusCode, url, string(data))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("freshmile read body from %s: %w", url, err)
	}
	return body, resp.StatusCode, nil
}

func normalizeFreshmileStation(details map[string]any) (domain.SourceStation, bool) {
	ref := stringValue(details["ref"])
	if ref == "" {
		return domain.SourceStation{}, false
	}

	coords, _ := details["coordinates"].(map[string]any)
	lat, latOK := floatValue(coords["latitude"])
	lng, lngOK := floatValue(coords["longitude"])
	if !latOK || !lngOK || lat == nil || lng == nil {
		return domain.SourceStation{}, false
	}

	addressMap, _ := details["address"].(map[string]any)

	return domain.SourceStation{
		Source:          "freshmile",
		SourceStationID: ref,
		Name:            stringValue(details["name"]),
		OperatorName:    "Freshmile",
		AddressStreet:   stringValue(addressMap["fullname"]),
		AddressPostal:   stringValue(addressMap["postal_code"]),
		AddressCity:     stringValue(addressMap["city"]),
		AddressCountry:  normalizeCountry(stringValue(addressMap["country"])),
		Lat:             *lat,
		Lng:             *lng,
		Raw:             details,
	}, true
}

// freshmileConnectorType maps a Freshmile connector "standard" to the
// vocabulary used elsewhere (IRVE's connector_type, roughly) so the
// frontend can pick a consistent icon — stored in a tariff's Extra since
// domain.StationTariff has no connector-type field of its own (Freshmile
// is a source_station, not the IRVE referential).
func freshmileConnectorType(standard string) string {
	switch strings.ToUpper(standard) {
	case "IEC_62196_T2_COMBO":
		return "CCS"
	case "CHADEMO":
		return "CHAdeMO"
	case "IEC_62196_T2":
		return "T2"
	default:
		return standard
	}
}

// normalizeFreshmileTariffs walks every evse/connector's tariff and turns
// each into a StationTariff. Unlike Electra/Tesla (a handful of fixed
// plans per station), Freshmile can expose many distinct tariff products
// per station — one per connector's pricebook, identified by
// custom_ref/origin_ref — so each becomes its own Plan rather than being
// collapsed: they're genuinely different prices depending which connector/
// contract applies, not variations of the same handful of plans.
func normalizeFreshmileTariffs(details map[string]any) []domain.StationTariff {
	bestPowerCategory := ""
	if connectorsSummary, ok := details["connectors"].(map[string]any); ok {
		if bestPower, ok := connectorsSummary["best_power"].(map[string]any); ok {
			bestPowerCategory = strings.ToLower(stringValue(bestPower["category"]))
		}
	}

	var tariffs []domain.StationTariff
	evses, _ := details["evses"].([]any)
	for _, rawEvse := range evses {
		evse, ok := rawEvse.(map[string]any)
		if !ok {
			continue
		}
		connectors, _ := evse["connectors"].([]any)
		for _, rawConn := range connectors {
			conn, ok := rawConn.(map[string]any)
			if !ok {
				continue
			}
			tariffRaw, ok := conn["tariff"].(map[string]any)
			if !ok {
				continue
			}
			tariffs = append(tariffs, normalizeFreshmileConnectorTariff(conn, tariffRaw, bestPowerCategory))
		}
	}
	return tariffs
}

func normalizeFreshmileConnectorTariff(conn, tariffRaw map[string]any, bestPowerCategory string) domain.StationTariff {
	customRef := firstNonEmpty(stringValue(tariffRaw["custom_ref"]), domain.TariffPlanStandard)
	plan := customRef
	if parseBooleanLoose(stringValue(tariffRaw["is_preferential"])) {
		plan = customRef + ":preferential"
	}

	power, _ := floatValue(conn["power"])
	kind := domain.TariffKindAC
	if (power != nil && *power >= 50) || bestPowerCategory == "fast" || bestPowerCategory == "superfast" {
		kind = domain.TariffKindDC
	}

	extra := map[string]any{
		"tariff":        tariffRaw,
		"connectorType": freshmileConnectorType(stringValue(conn["standard"])),
	}

	t := domain.StationTariff{
		Source:   "freshmile",
		Plan:     plan,
		Kind:     kind,
		Model:    "freshmile_kwh",
		Currency: firstNonEmpty(stringValue(tariffRaw["currency"]), "EUR"),
		Extra:    extra,
	}

	if parseBooleanLoose(stringValue(tariffRaw["is_free"])) {
		extra["is_free"] = true
		// A free tariff is a real 0 €/kWh price, not an unknown one: set it
		// explicitly so it feeds the AC/DC minimums (and shows as "0,00 €")
		// instead of being indistinguishable from a tariff whose price
		// couldn't be parsed (left nil below).
		zero := 0.0
		t.EnergyPriceCentsPerKWh = &zero
		return t
	}

	price, lang, text, ok := freshmilePriceFromDescription(tariffRaw["description"])
	if !ok {
		log.Printf("freshmile: could not extract a €/kWh price from tariff %v description: %v", tariffRaw["id"], tariffRaw["description"])
		return t
	}
	t.EnergyPriceCentsPerKWh = price
	extra["parsedLang"] = lang
	extra["parsedText"] = text
	extra["energyPriceEuro"] = *price / 100
	return t
}

// freshmilePricePattern matches "<amount> € / [started ]kWh" style text in
// either currency-symbol ordering — French puts the amount first ("0,70 €
// / kWh entamé."), English puts € first ("€ 0.70 / started kWh.") — via two
// alternative capture groups; the amount can use either a comma or dot
// decimal separator. The separator between amount and "kWh" also varies:
// "/" ("0,70 € / kWh"), or the word "par"/"per" ("0,25 € par kWh entamé").
var freshmilePricePattern = regexp.MustCompile(`(?:([\d.,]+)\s*€|€\s*([\d.,]+))\s*(?:/|par|per)\s*(?:started\s+)?kWh`)

// freshmilePriceFromDescription extracts a €/kWh price (in cents) from a
// Freshmile tariff's description field. Two shapes have been observed in
// the wild: a JSON-encoded multi-language object (e.g. {"fr": "0,70 € /
// kWh entamé.", "en": "€ 0.70 / started kWh."}) and — in current
// production responses — a single plain-text string in whatever language
// the API happened to answer in (e.g. "€ 0.30 / started kWh + € 0.30 /
// min\nThe pricing continues..."). The JSON form is tried first (and
// prefers French then English); a plain string that isn't valid JSON
// falls back to matching the price pattern directly against it.
func freshmilePriceFromDescription(raw any) (priceCents *float64, lang, text string, ok bool) {
	descText := stringValue(raw)
	if descText == "" {
		return nil, "", "", false
	}

	var byLang map[string]string
	if err := json.Unmarshal([]byte(descText), &byLang); err == nil {
		for _, l := range []string{"fr", "en"} {
			t := byLang[l]
			if t == "" {
				continue
			}
			if cents, ok := extractFreshmilePriceCents(t); ok {
				return cents, l, t, true
			}
		}
		return nil, "", "", false
	}

	if cents, ok := extractFreshmilePriceCents(descText); ok {
		return cents, "", descText, true
	}
	return nil, "", "", false
}

// extractFreshmilePriceCents matches freshmilePricePattern against a
// single piece of text and converts the amount to cents.
func extractFreshmilePriceCents(t string) (*float64, bool) {
	match := freshmilePricePattern.FindStringSubmatch(t)
	if len(match) != 3 {
		return nil, false
	}
	amount := firstNonEmpty(match[1], match[2])
	euros, err := strconv.ParseFloat(strings.ReplaceAll(amount, ",", "."), 64)
	if err != nil {
		return nil, false
	}
	// Round to avoid float64 noise from the euro->cents multiplication
	// (e.g. 0.55 * 100 = 55.00000000000001).
	cents := math.Round(euros*10000) / 100
	return &cents, true
}
