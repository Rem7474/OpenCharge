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
	transport.MaxIdleConnsPerHost = workers
	return &FreshmileIngester{
		Pool:             pool,
		SourceStations:   sourceStations,
		Tariffs:          tariffs,
		Links:            links,
		BaseURL:          baseURL,
		Config:           cfg,
		MaxLinkDistanceM: DefaultLinkMaxDistanceMeters,
		client:           &http.Client{Timeout: 60 * time.Second, Transport: transport},
	}
}

// Run discovers every unique Freshmile location (recursively resolving map
// clusters down to individual points), fetches each one's details/tariffs
// concurrently, then correlates every station with the nearest IRVE point
// of charge. Detail fetches use the same fetch/normalize-then-batch-write
// split as Izivia/Tesla: workers stay concurrent for the I/O-bound part,
// writes are funneled through a single consumer batching via
// writeSourceStationChunk.
func (ing *FreshmileIngester) Run(ctx context.Context) (int, error) {
	ids, err := ing.fetchAllLocationIDs(ctx)
	if err != nil {
		return 0, err
	}
	log.Printf("freshmile: %d unique locations to fetch", len(ids))

	idCh := make(chan int)
	resultsCh := make(chan normalizedSourceStation)
	var wg sync.WaitGroup

	worker := func() {
		defer wg.Done()
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
		wg.Add(1)
		go worker()
	}
	go func() {
		wg.Wait()
		close(resultsCh)
	}()

	writeDone := make(chan struct {
		processed int
		err       error
	}, 1)
	go func() {
		processed, err := ing.writeResults(ctx, resultsCh, len(ids))
		writeDone <- struct {
			processed int
			err       error
		}{processed, err}
	}()

	var firstErr error
	for _, id := range ids {
		select {
		case idCh <- id:
		case <-ctx.Done():
			firstErr = ctx.Err()
		}
	}
	close(idCh)

	result := <-writeDone
	if firstErr == nil {
		firstErr = result.err
	}

	log.Printf("freshmile: done, %d locations processed", result.processed)
	return result.processed, firstErr
}

// writeResults drains resultsCh, batching writes by ingestionBulkChunkSize
// through writeSourceStationChunk.
func (ing *FreshmileIngester) writeResults(ctx context.Context, resultsCh <-chan normalizedSourceStation, total int) (int, error) {
	processed := 0
	batch := make([]normalizedSourceStation, 0, ingestionBulkChunkSize)

	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		n, err := writeSourceStationChunk(ctx, ing.Pool, ing.SourceStations, ing.Tariffs, ing.Links, ing.MaxLinkDistanceM, batch)
		processed += n
		batch = batch[:0]
		if err != nil {
			return err
		}
		log.Printf("freshmile: %d/%d processed", processed, total)
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
	body, err := ing.getJSON(ctx, fmt.Sprintf("%s/locations/%d", ing.BaseURL, id))
	if err != nil {
		return normalizedSourceStation{}, false, fmt.Errorf("fetch location details: %w", err)
	}
	var details map[string]any
	if err := json.Unmarshal(body, &details); err != nil {
		return normalizedSourceStation{}, false, fmt.Errorf("decode location details: %w", err)
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

// fetchAllLocationIDs scans map-locations across metropolitan France,
// starting from a coarse grid of tiles. Any feature that's a cluster
// (location_count > 1) is resolved by recursively subdividing its own
// bbox — never fetched via /locations directly — until only unique points
// remain or freshmileMaxSubdivisionDepth is hit, in which case that
// (persistently clustered) branch is dropped rather than recursed into
// forever. Network errors on one tile are logged and skipped, not fatal.
func (ing *FreshmileIngester) fetchAllLocationIDs(ctx context.Context) ([]int, error) {
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

	ids := map[int]struct{}{}
	visited := 0

	var scan func(bbox freshmileBBox, depth int)
	scan = func(bbox freshmileBBox, depth int) {
		if ctx.Err() != nil || visited >= freshmileMaxTilesVisited {
			return
		}
		visited++

		features, err := ing.fetchMapLocations(ctx, bbox)
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
				if locID, ok := floatValue(props["location_id"]); ok && locID != nil {
					ids[int(*locID)] = struct{}{}
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
				scan(sub, depth+1)
			}
		}
	}

	for _, tile := range initial {
		scan(tile, 0)
	}

	result := make([]int, 0, len(ids))
	for id := range ids {
		result = append(result, id)
	}
	log.Printf("freshmile: %d unique locations discovered across %d map-locations tiles visited", len(result), visited)
	return result, nil
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

func (ing *FreshmileIngester) fetchMapLocations(ctx context.Context, bbox freshmileBBox) ([]map[string]any, error) {
	url := fmt.Sprintf("%s/map-locations?bbox=%g,%g,%g,%g&zoom=%d",
		ing.BaseURL, bbox.MinLng, bbox.MinLat, bbox.MaxLng, bbox.MaxLat, freshmileZoom)
	body, err := ing.getJSON(ctx, url)
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

func (ing *FreshmileIngester) getJSON(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")

	resp, err := ing.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return nil, fmt.Errorf("freshmile http %d: %s", resp.StatusCode, string(data))
	}
	return io.ReadAll(resp.Body)
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
// decimal separator.
var freshmilePricePattern = regexp.MustCompile(`(?:([\d.,]+)\s*€|€\s*([\d.,]+))\s*/\s*(?:started\s+)?kWh`)

// freshmilePriceFromDescription extracts a €/kWh price (in cents) from a
// Freshmile tariff's JSON-encoded multi-language description (e.g.
// {"fr": "0,70 € / kWh entamé.", "en": "€ 0.70 / started kWh."}),
// preferring French then falling back to English.
func freshmilePriceFromDescription(raw any) (priceCents *float64, lang, text string, ok bool) {
	descText := stringValue(raw)
	if descText == "" {
		return nil, "", "", false
	}
	var byLang map[string]string
	if err := json.Unmarshal([]byte(descText), &byLang); err != nil {
		return nil, "", "", false
	}
	for _, l := range []string{"fr", "en"} {
		t := byLang[l]
		if t == "" {
			continue
		}
		match := freshmilePricePattern.FindStringSubmatch(t)
		if len(match) != 3 {
			continue
		}
		amount := firstNonEmpty(match[1], match[2])
		euros, err := strconv.ParseFloat(strings.ReplaceAll(amount, ",", "."), 64)
		if err != nil {
			continue
		}
		// Round to avoid float64 noise from the euro->cents multiplication
		// (e.g. 0.55 * 100 = 55.00000000000001).
		cents := math.Round(euros*10000) / 100
		return &cents, l, t, true
	}
	return nil, "", "", false
}
