package ingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
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

const iziviaBaseURL = "https://fronts-map.izivia.com/api"

var iziviaHeaders = map[string]string{
	"Accept":          "application/json",
	"Accept-Language": "fr",
	"User-Agent":      "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
	"Referer":         "https://fronts-map.izivia.com/",
	"Origin":          "https://fronts-map.izivia.com",
	"x-device-id":     "opencharge-ingest",
	"Content-Type":    "application/json",
}

// IziviaConfig tunes the marker grid scan (Izivia's map API is
// viewport-based, so the whole of France is scanned as a grid of squares).
type IziviaConfig struct {
	Workers  int
	GridStep float64
	Zoom     int
}

// DefaultIziviaConfig: Workers=40 because every marker costs two sequential
// HTTP round trips (station detail, then pricing) and the source has tens
// of thousands of markers — this is a pure I/O-bound fan-out to a single
// host, so it needs a much larger pool than a CPU-bound worker count. It
// only pays off paired with newIziviaHTTPClient's raised per-host
// connection limits below; without that, Go's default transport caps
// concurrent connections to one host at 2 regardless of goroutine count.
func DefaultIziviaConfig() IziviaConfig {
	return IziviaConfig{Workers: 40, GridStep: 2.0, Zoom: 7}
}

// iziviaFlushTimeout bounds how long a single batch write is allowed to
// take — same rationale and value as Freshmile's freshmileFlushTimeout.
const iziviaFlushTimeout = 2 * time.Minute

// newIziviaHTTPClient returns a client whose transport allows enough
// concurrent connections to fronts-map.izivia.com to actually make worker
// concurrency effective. http.DefaultTransport's MaxIdleConnsPerHost is 2:
// with a single-host fan-out like this ingester's, that silently
// serializes most requests behind repeated TCP/TLS handshakes no matter
// how many goroutines are launched.
func newIziviaHTTPClient(maxConnsPerHost int) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = maxConnsPerHost
	transport.MaxConnsPerHost = 0 // unlimited in-flight; idle pool is what mattered
	return &http.Client{Timeout: 20 * time.Second, Transport: transport}
}

type IziviaIngester struct {
	Pool             *pgxpool.Pool
	SourceStations   *repository.SourceStationRepository
	Tariffs          *repository.TariffRepository
	Links            *repository.LinkRepository
	Config           IziviaConfig
	MaxLinkDistanceM float64
	// Failures, when set, records every request that failed for good
	// (station detail fetch, marker square scan) so a later
	// -retry-failed pass can replay just those — see FailureLog.
	Failures     *FailureLog
	client       *http.Client
	retryBackoff time.Duration // unexported: overridden by tests to keep them fast
}

func NewIziviaIngester(pool *pgxpool.Pool, sourceStations *repository.SourceStationRepository, tariffs *repository.TariffRepository, links *repository.LinkRepository, cfg IziviaConfig) *IziviaIngester {
	workers := cfg.Workers
	if workers <= 0 {
		workers = 40
	}
	return &IziviaIngester{
		Pool:             pool,
		SourceStations:   sourceStations,
		Tariffs:          tariffs,
		Links:            links,
		Config:           cfg,
		MaxLinkDistanceM: DefaultLinkMaxDistanceMeters,
		client:           newIziviaHTTPClient(workers),
		retryBackoff:     2 * time.Second,
	}
}

// iziviaMaxRetries is how many times a transient failure (network error,
// timeout, or 5xx) is retried before giving up on that request. Every
// square-scan and marker fetch used to have zero retry at all: a single
// fronts-map.izivia.com timeout (observed in practice under load — the API
// has no documented SLA) permanently dropped whatever that request covered
// (an entire grid square during discovery, or one station during detail
// fetch), logged and skipped, with no way to recover it within the run.
// 4xx responses are never retried — they won't succeed on a second try.
const iziviaMaxRetries = 5

// Run scans Izivia's map for markers covering metropolitan France, fetches
// station details and pricing for each, then correlates every station with
// the nearest IRVE point of charge. Fetching stays concurrent (the workers
// below are I/O-bound, so parallelism helps there), but database writes are
// funneled through a single consumer that batches them via
// writeSourceStationChunk — same bulk correlation + single-transaction
// pattern as Electra, instead of one uncommitted round trip per marker.
func (ing *IziviaIngester) Run(ctx context.Context) (int, error) {
	defer ing.Failures.saveAndLog()
	runStart := time.Now()

	markers, err := ing.fetchMarkers(ctx)
	if err != nil {
		return 0, err
	}
	log.Printf("izivia: %d unique markers found", len(markers))

	result, firstErr := ing.processMarkers(ctx, markers)

	log.Printf("izivia: done, %d stations processed", result)

	// Only sweep after a fully successful run (see repository.SweepStaleSourceData) —
	// firstErr covers both ctx cancellation and a write failure. result
	// > 0 is a second, independent guard: every healthy run re-touches
	// thousands of stations, so zero processed with no error is itself a
	// sign something upstream failed silently (e.g. every marker's detail
	// fetch failing while the marker scan itself reported success) —
	// sweeping in that case would wipe the entire known dataset instead of
	// just skipping a bad run. See also fetchMarkers' own all-squares-failed
	// check, which catches the total-scan-failure case earlier.
	if firstErr == nil && result > 0 {
		if err := repository.SweepStaleSourceData(ctx, ing.Pool, "izivia", runStart.Add(-repository.StaleSourceDataGracePeriod)); err != nil {
			return result, err
		}
	}
	return result, firstErr
}

// RetryFailed replays only the requests a previous run recorded as failed
// (see FailureLog): failed marker squares are re-scanned to recover their
// markers, and failed station details are re-fetched from the marker saved
// in the failure's params. Stations that fail again are re-recorded, so
// the failure file always reflects what's still outstanding. No stale-data
// sweep happens here: a retry pass only touches the previously-failed
// subset, so most known stations legitimately go un-refreshed.
func (ing *IziviaIngester) RetryFailed(ctx context.Context, failures []FailedFetch) (int, error) {
	defer ing.Failures.saveAndLog()

	var markers []map[string]any
	seen := map[string]struct{}{}
	addMarker := func(m map[string]any) {
		id, _ := m["id"].(string)
		if id == "" {
			return
		}
		if _, dup := seen[id]; dup {
			return
		}
		seen[id] = struct{}{}
		markers = append(markers, m)
	}

	for _, f := range failures {
		switch f.Kind {
		case failKindIziviaSquare:
			var square iziviaSquare
			if err := json.Unmarshal(f.Params, &square); err != nil {
				log.Printf("izivia: skipping unreadable %s failure: %v", f.Kind, err)
				continue
			}
			squareMarkers, err := ing.fetchSquareMarkers(ctx, square)
			if err != nil {
				log.Printf("izivia: markers square still failing: centerLng=%g centerLat=%g zoom=%d: %v", square.CenterLng, square.CenterLat, square.Zoom, err)
				if ctx.Err() == nil {
					ing.Failures.Record(failKindIziviaSquare, iziviaBaseURL+"/map/markers", square, err)
				}
				continue
			}
			for _, m := range squareMarkers {
				addMarker(m)
			}
		case failKindIziviaStation:
			var marker map[string]any
			if err := json.Unmarshal(f.Params, &marker); err != nil {
				log.Printf("izivia: skipping unreadable %s failure: %v", f.Kind, err)
				continue
			}
			addMarker(marker)
		default:
			log.Printf("izivia: skipping failure of unknown kind %q", f.Kind)
		}
	}

	log.Printf("izivia: retrying %d markers from %d recorded failure(s)", len(markers), len(failures))
	processed, err := ing.processMarkers(ctx, markers)
	log.Printf("izivia: retry done, %d stations processed", processed)
	return processed, err
}

// processMarkers fetches details/pricing for every marker and writes the
// results, concurrently — the shared pipeline behind both a full Run and a
// RetryFailed pass.
func (ing *IziviaIngester) processMarkers(ctx context.Context, markers []map[string]any) (int, error) {
	// pipelineCtx governs the marker-feeding loop below and the fetch
	// workers. writeResults runs concurrently in its own goroutine and can
	// return early on a flush error, well before every marker has been fed
	// in — at that point nobody is left to drain resultsCh (workers) or
	// markerCh (the feeding loop below), and neither is watching plain ctx
	// (which is still perfectly valid; only the write side gave up). Without
	// an explicit cancel here, both sides block forever instead of Run()
	// returning the real error — that's the "context deadline exceeded" /
	// unrecoverable-crash failure mode this once produced, except with a
	// write error instead of a timeout it wouldn't even print anything, it
	// would just hang.
	pipelineCtx, cancelPipeline := context.WithCancel(ctx)
	defer cancelPipeline()

	markerCh := make(chan map[string]any)
	resultsCh := make(chan normalizedSourceStation)
	var wg sync.WaitGroup

	worker := func() {
		defer wg.Done()
		for marker := range markerCh {
			item, ok, err := ing.fetchAndNormalizeMarker(pipelineCtx, marker)
			if err != nil {
				stationID, _ := marker["id"].(string)
				log.Printf("izivia: station %s failed: %v", stationID, err)
				// Not recorded when the pipeline itself is shutting down
				// (timeout/SIGINT/write error): those markers didn't fail on
				// their own, and recording the cancellation-error flood
				// would drown the genuinely-failed ones.
				if pipelineCtx.Err() == nil {
					ing.Failures.Record(failKindIziviaStation, fmt.Sprintf("%s/charging-locations/%s", iziviaBaseURL, stationID), marker, err)
				}
				continue
			}
			if !ok {
				continue
			}
			select {
			case resultsCh <- item:
			case <-pipelineCtx.Done():
				return
			}
		}
	}

	workers := ing.Config.Workers
	if workers <= 0 {
		workers = 40
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
		processed, err := ing.writeResults(ctx, resultsCh, len(markers))
		writeDone <- struct {
			processed int
			err       error
		}{processed, err}
		// Whether writeResults finished normally or bailed out early on a
		// flush error, cancel the pipeline so the feeding loop and any
		// worker still blocked sending unblock instead of leaking forever.
		cancelPipeline()
	}()

feedLoop:
	for _, marker := range markers {
		select {
		case markerCh <- marker:
		case <-pipelineCtx.Done():
			break feedLoop
		}
	}
	close(markerCh)

	result := <-writeDone
	firstErr := ctx.Err()
	if firstErr == nil {
		firstErr = result.err
	}
	return result.processed, firstErr
}

// writeResults drains resultsCh, batching writes by ingestionBulkChunkSize
// through writeSourceStationChunk.
func (ing *IziviaIngester) writeResults(ctx context.Context, resultsCh <-chan normalizedSourceStation, total int) (int, error) {
	processed := 0
	batch := make([]normalizedSourceStation, 0, ingestionBulkChunkSize)

	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		// Decoupled from ctx (context.WithoutCancel), same as Freshmile's
		// writeResults: this batch is already fully collected in memory by
		// this point, so once we've committed to writing it, the run's own
		// -timeout or a SIGINT landing mid-query shouldn't be able to abort
		// it. Without this, a deadline expiring right at (or just before) a
		// flush silently drops that whole batch — already-fetched stations
		// that never make it to the database — instead of just ending the
		// run one batch earlier.
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), iziviaFlushTimeout)
		defer cancel()
		n, err := writeSourceStationChunk(writeCtx, ing.Pool, ing.SourceStations, ing.Tariffs, ing.Links, ing.MaxLinkDistanceM, batch)
		processed += n
		batch = batch[:0]
		if err != nil {
			return err
		}
		log.Printf("izivia: %d/%d processed", processed, total)
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

// fetchAndNormalizeMarker does the I/O-bound work for one marker (station
// detail + pricing HTTP calls, then normalization) without touching the
// database — writes are batched separately, see Run.
func (ing *IziviaIngester) fetchAndNormalizeMarker(ctx context.Context, marker map[string]any) (normalizedSourceStation, bool, error) {
	stationID, _ := marker["id"].(string)
	if stationID == "" {
		return normalizedSourceStation{}, false, fmt.Errorf("marker without id")
	}

	detailsURL := fmt.Sprintf("%s/charging-locations/%s", iziviaBaseURL, stationID)
	stationBody, err := ing.postJSON(ctx, detailsURL, detailsURL, map[string]any{})
	if err != nil {
		return normalizedSourceStation{}, false, fmt.Errorf("fetch station details: %w", err)
	}
	var station map[string]any
	if err := json.Unmarshal(stationBody, &station); err != nil {
		return normalizedSourceStation{}, false, fmt.Errorf("decode station details: %w", err)
	}

	var pricing []any
	if stationEmipID, _ := station["firstStationEmipId"].(string); stationEmipID != "" {
		pricingBody, err := ing.getJSON(ctx, fmt.Sprintf("%s/charging-locations/%s/pricing-info-items?stationEmipId=%s", iziviaBaseURL, stationID, stationEmipID))
		if err == nil && len(strings.TrimSpace(string(pricingBody))) > 0 {
			_ = json.Unmarshal(pricingBody, &pricing)
		}
	}

	src, tariffs, ok := normalizeIziviaStation(marker, station, pricing)
	if !ok {
		return normalizedSourceStation{}, false, fmt.Errorf("station without usable location")
	}
	acPowerKW, dcPowerKW := iziviaMaxPowerKWByKind(station)
	return normalizedSourceStation{Station: src, Tariffs: tariffs, ACPowerKW: acPowerKW, DCPowerKW: dcPowerKW}, true, nil
}

func normalizeIziviaStation(marker, station map[string]any, pricing []any) (domain.SourceStation, []domain.StationTariff, bool) {
	stationID := stringValue(station["id"])
	if stationID == "" {
		stationID = stringValue(marker["id"])
	}
	if stationID == "" {
		return domain.SourceStation{}, nil, false
	}

	coords, _ := station["coordinates"].([]any)
	var lat, lng *float64
	if len(coords) >= 2 {
		lng, _ = floatValue(coords[0])
		lat, _ = floatValue(coords[1])
	}
	if lat == nil || lng == nil {
		lat, _ = floatValue(marker["lat"])
		lng, _ = floatValue(marker["lng"])
	}
	if lat == nil || lng == nil {
		return domain.SourceStation{}, nil, false
	}

	addressMap, _ := station["address"].(map[string]any)

	src := domain.SourceStation{
		Source:          "izivia",
		SourceStationID: stationID,
		Name:            stringValue(station["name"]),
		OperatorName:    "Izivia",
		AddressStreet:   stringValue(addressMap["street"]),
		AddressPostal:   stringValue(addressMap["postalCode"]),
		AddressCity:     stringValue(addressMap["city"]),
		AddressCountry:  normalizeCountry(stringValue(addressMap["country"])),
		Lat:             *lat,
		Lng:             *lng,
		Raw:             map[string]any{"marker": marker, "station": station, "pricing": pricing},
	}

	return src, normalizeIziviaTariffs(station, pricing), true
}

// iziviaPowerPattern extracts a connector's power rating (e.g. "24kW" in
// "Connecteurs : CCS 24kW") from Izivia's free-text pricing description.
var iziviaPowerPattern = regexp.MustCompile(`(?i)([0-9]+(?:[.,][0-9]+)?)\s*kW`)

// iziviaTariffKind decides a station's tariff Kind (ac/dc/mixed). It prefers
// the station's structured chargingConnectorsStats — far more reliable than
// guessing from the pricing text — and only falls back to the free-text
// heuristic (inferIziviaKind) when no usable connector data is present.
//
// A station exposing only AC (or only DC) connectors gets that kind; one
// exposing both gets "mixed" (a single Izivia price applies to every
// connector, and since ListByBBox feeds 'mixed' into both the AC and DC
// minimums, the marker is still priced whatever the connector).
func iziviaTariffKind(station map[string]any, text string) string {
	stats, _ := station["chargingConnectorsStats"].([]any)
	hasAC, hasDC := false, false
	for _, raw := range stats {
		c, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch iziviaConnectorKind(stringValue(c["standard"]), c["maxPowerInW"]) {
		case domain.TariffKindAC:
			hasAC = true
		case domain.TariffKindDC:
			hasDC = true
		}
	}
	switch {
	case hasAC && !hasDC:
		return domain.TariffKindAC
	case hasDC && !hasAC:
		return domain.TariffKindDC
	case hasAC && hasDC:
		return domain.TariffKindMixed
	default:
		// No usable structured connector data — fall back to the free-text
		// heuristic (which may itself return "mixed").
		return inferIziviaKind(text)
	}
}

// iziviaConnectorKind maps one chargingConnectorsStats entry to ac/dc. The
// connector standard is authoritative: Type 2 / Type 3 / domestic sockets
// are AC even when rated above 22kW (observed: "t2" up to 25kW), while
// CCS Combo ("combo_t2") and CHAdeMO are DC. Only when the standard is
// unrecognized does it fall back to power (>22kW ⇒ DC). Returns "" when
// neither the standard nor the power is usable.
func iziviaConnectorKind(standard string, maxPowerInW any) string {
	switch strings.ToLower(strings.TrimSpace(standard)) {
	case "combo_t2", "combo_ccs", "ccs", "chademo":
		return domain.TariffKindDC
	case "t2", "type2", "iec_62196_t2", "t3", "type3", "standard_household", "domestic", "ef", "e", "type_e":
		return domain.TariffKindAC
	}
	if w, ok := floatValue(maxPowerInW); ok && w != nil && *w > 0 {
		if *w/1000.0 > 22 {
			return domain.TariffKindDC
		}
		return domain.TariffKindAC
	}
	return ""
}

// iziviaMaxPowerKWByKind reads a station's chargingConnectorsStats and
// returns the highest rated power (in kW) among its AC connectors and among
// its DC connectors, respectively — nil for a kind the station has none of.
// Used as the target power for FindNearestStationsForKind's power-aware
// tie-break: IRVE frequently models the same physical Izivia location as
// several rows at effectively the same coordinates (e.g. a 22kW AC and a
// 150kW DC row, or even two DC rows of different power), so distance and
// kind alone aren't always enough to disambiguate which row a given
// AC/DC-kind tariff should land on.
func iziviaMaxPowerKWByKind(station map[string]any) (acKW, dcKW *float64) {
	stats, _ := station["chargingConnectorsStats"].([]any)
	for _, raw := range stats {
		c, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		w, ok := floatValue(c["maxPowerInW"])
		if !ok || w == nil || *w <= 0 {
			continue
		}
		kw := *w / 1000.0
		switch iziviaConnectorKind(stringValue(c["standard"]), c["maxPowerInW"]) {
		case domain.TariffKindAC:
			if acKW == nil || kw > *acKW {
				acKW = &kw
			}
		case domain.TariffKindDC:
			if dcKW == nil || kw > *dcKW {
				dcKW = &kw
			}
		}
	}
	return acKW, dcKW
}

// inferIziviaKind derives a tariff's Kind (ac/dc) from the power rating
// mentioned in its pricing text, used only as a fallback when a station has
// no usable structured connector data (see iziviaTariffKind). Above 22kW
// (the ceiling for three-phase AC charging) is treated as DC; at or below is
// AC. When no power figure is found in the text, Kind falls back to Mixed
// rather than guessing.
func inferIziviaKind(text string) string {
	match := iziviaPowerPattern.FindStringSubmatch(text)
	if len(match) != 2 {
		return domain.TariffKindMixed
	}
	power, err := strconv.ParseFloat(strings.ReplaceAll(match[1], ",", "."), 64)
	if err != nil {
		return domain.TariffKindMixed
	}
	if power > 22 {
		return domain.TariffKindDC
	}
	return domain.TariffKindAC
}

// iziviaPricingTexts gathers every candidate pricing-description string from
// an Izivia pricing-info-items payload, across BOTH shapes seen in
// production. The dominant one (observed in ~90% of stations) is a
// "charging_location / leaf" entry carrying pricingInfos/rawPricingInfos at
// its own top level; the rarer one nests them under chargingStations[] (with
// a subscriptionNames sibling). Earlier code only read the nested shape, so
// the vast majority of stations produced no tariff at all — they then either
// vanished from the map (the frontend requests hasTariffs=true) or showed no
// Izivia price. pricingInfos is preferred over rawPricingInfos (same text,
// but pricingInfos uses "\n" line breaks rather than "<br>").
func iziviaPricingTexts(pricing []any) []string {
	var texts []string
	add := func(m map[string]any) {
		got := extractStringList(m["pricingInfos"])
		if len(got) == 0 {
			got = extractStringList(m["rawPricingInfos"])
		}
		texts = append(texts, got...)
	}
	for _, item := range pricing {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		// Top-level shape.
		add(entry)
		// Nested shape.
		chargingStations, _ := entry["chargingStations"].([]any)
		for _, cs := range chargingStations {
			if csMap, ok := cs.(map[string]any); ok {
				add(csMap)
			}
		}
	}
	return texts
}

// normalizeIziviaTariffs turns Izivia's free-text pricing entries into
// StationTariff rows, deriving Kind (ac/dc/mixed) from the station's
// structured connector data (see iziviaTariffKind), and only from the
// pricing text as a fallback. It scans every candidate text (across both
// payload shapes) and returns the first that yields a usable price, instead
// of only ever looking at the first nested entry.
func normalizeIziviaTariffs(station map[string]any, pricing []any) []domain.StationTariff {
	for _, rawText := range iziviaPricingTexts(pricing) {
		price, sessionPrice, sessionFee, fee := parsePriceText(rawText)
		if price == nil && sessionPrice == nil && sessionFee == nil && fee == nil {
			continue
		}
		return []domain.StationTariff{{
			Source:                  "izivia",
			Plan:                    domain.TariffPlanStandard,
			Kind:                    iziviaTariffKind(station, rawText),
			Model:                   "izivia_text",
			Currency:                "EUR",
			EnergyPriceCentsPerKWh:  price,
			SessionPriceCentsPerMin: sessionPrice,
			SessionFeeCents:         sessionFee,
			ServiceFeePercent:       fee,
			RawText:                 rawText,
			Extra:                   map[string]any{},
		}}
	}
	return nil
}

func extractStringList(value any) []string {
	list, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(list))
	for _, item := range list {
		if text := stringValue(item); text != "" {
			result = append(result, text)
		}
	}
	return result
}

func normalizeCountry(code string) string {
	switch strings.ToUpper(strings.TrimSpace(code)) {
	case "FRA", "FRANCE":
		return "FR"
	default:
		return code
	}
}

type iziviaSquare struct {
	CenterLng float64 `json:"centerLng"`
	CenterLat float64 `json:"centerLat"`
	Zoom      int     `json:"zoom"`
}

func (ing *IziviaIngester) fetchMarkers(ctx context.Context) ([]map[string]any, error) {
	step := ing.Config.GridStep
	zoom := ing.Config.Zoom
	if step <= 0 {
		step = 2.0
	}
	if zoom <= 0 {
		zoom = 7
	}

	minLng, maxLng := -5.5, 9.8
	minLat, maxLat := 41.0, 51.5
	var squares []iziviaSquare
	for lat := minLat; lat <= maxLat; lat += step {
		for lng := minLng; lng <= maxLng; lng += step {
			squares = append(squares, iziviaSquare{CenterLng: lng, CenterLat: lat, Zoom: zoom})
		}
	}
	// The whole run has a hard overall timeout (see cmd/opencharge-ingest's
	// -timeout), and squares used to always be fed to workers in this same
	// fixed south-to-north, west-to-east order every run. A run that's
	// chronically running behind (a slow API, lots of retries) always gets
	// cut off at the same tail end of that fixed list — i.e. it's always
	// the same geographic region (the far north of France, last in this
	// order) that goes stale, run after run, rather than a timeout costing
	// a different, rotating slice of coverage each time. Shuffling once per
	// run means a recurring timeout still costs roughly the same *amount*
	// of coverage, but spreads *which* region gets missed across runs
	// instead of permanently starving one.
	rand.Shuffle(len(squares), func(i, j int) { squares[i], squares[j] = squares[j], squares[i] })

	// The grid scan is a small, fixed number of squares (tens, not
	// thousands), but each is its own HTTP round trip: fan it out with a
	// bounded pool instead of one request at a time.
	squareCh := make(chan iziviaSquare)
	resultsCh := make(chan []map[string]any)
	var wg sync.WaitGroup

	scanWorkers := ing.Config.Workers
	if scanWorkers <= 0 {
		scanWorkers = 40
	}
	if scanWorkers > len(squares) {
		scanWorkers = len(squares)
	}
	// succeeded/failed count per-square outcomes so a total scan failure
	// (every square erroring, e.g. Izivia's backend returning gRPC 500s for
	// the whole run) can be told apart from a scan that genuinely found zero
	// markers. Without this, fetchMarkers used to return an empty slice with
	// a nil error either way — indistinguishable from Run()'s point of view,
	// which then went on to sweep every previously-known Izivia
	// source_station/tariff as "stale", wiping the entire dataset on what
	// was actually a total upstream outage, not an empty France.
	var succeeded, failed int64
	for i := 0; i < scanWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for square := range squareCh {
				markers, err := ing.fetchSquareMarkers(ctx, square)
				if err != nil {
					atomic.AddInt64(&failed, 1)
					log.Printf("izivia: markers square failed: centerLng=%g centerLat=%g zoom=%d: %v", square.CenterLng, square.CenterLat, square.Zoom, err)
					if ctx.Err() == nil {
						ing.Failures.Record(failKindIziviaSquare, iziviaBaseURL+"/map/markers", square, err)
					}
					continue
				}
				atomic.AddInt64(&succeeded, 1)
				select {
				case resultsCh <- markers:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		wg.Wait()
		close(resultsCh)
	}()
	go func() {
		defer close(squareCh)
		for _, square := range squares {
			select {
			case squareCh <- square:
			case <-ctx.Done():
				return
			}
		}
	}()

	all := make([]map[string]any, 0)
	seen := map[string]struct{}{}
	for markers := range resultsCh {
		for _, marker := range markers {
			id, _ := marker["id"].(string)
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			all = append(all, marker)
		}
	}

	if succeeded == 0 && len(squares) > 0 {
		return nil, fmt.Errorf("izivia: all %d marker-scan squares failed (%d errors) — aborting run rather than treating a total scan failure as \"France has zero stations\"", len(squares), failed)
	}

	return all, nil
}

// fetchSquareMarkers fetches /map/markers for one grid square. The URL is
// the same constant for every square — only the payload distinguishes one
// request from another — so the label passed to postJSON (used in retry
// logs) must spell out the square explicitly. Without this, a page of
// retry logs (or the final failure) is indistinguishable noise: no way to
// tell which of the many in-flight squares actually failed.
func (ing *IziviaIngester) fetchSquareMarkers(ctx context.Context, square iziviaSquare) ([]map[string]any, error) {
	payload := map[string]any{"square": square, "filters": map[string]any{}}
	markersURL := iziviaBaseURL + "/map/markers"
	label := fmt.Sprintf("%s (square centerLng=%g centerLat=%g zoom=%d)", markersURL, square.CenterLng, square.CenterLat, square.Zoom)
	body, err := ing.postJSON(ctx, markersURL, label, payload)
	if err != nil {
		return nil, err
	}
	var markers []map[string]any
	if err := json.Unmarshal(body, &markers); err != nil {
		return nil, fmt.Errorf("decode markers for square centerLng=%g centerLat=%g zoom=%d: %w", square.CenterLng, square.CenterLat, square.Zoom, err)
	}
	return markers, nil
}

// postJSON POSTs payload to url. label identifies this specific request in
// retry/failure logs — for endpoints like /map/markers where the URL is
// the same constant for every call (the request is distinguished only by
// its payload, e.g. which grid square is being queried), passing just url
// as the label makes every retry/failure log line indistinguishable from
// any other in-flight request, exactly the "which POST is this?" problem
// observed in production. Callers with a genuinely unique URL per request
// (e.g. /charging-locations/{id}) can just pass url again as label.
func (ing *IziviaIngester) postJSON(ctx context.Context, url, label string, payload map[string]any) ([]byte, error) {
	return ing.withRetries(ctx, label, func() ([]byte, int, error) {
		body, _ := json.Marshal(payload)
		return ing.doRequest(ctx, http.MethodPost, url, strings.NewReader(string(body)))
	})
}

func (ing *IziviaIngester) getJSON(ctx context.Context, url string) ([]byte, error) {
	return ing.withRetries(ctx, url, func() ([]byte, int, error) {
		return ing.doRequest(ctx, http.MethodGet, url, nil)
	})
}

// withRetries retries do (one HTTP attempt) on a transient failure —
// network error, timeout, or 5xx — up to iziviaMaxRetries times with
// exponential backoff, same pattern as Freshmile's getJSON. do's int
// return is the HTTP status (0 if the request never got a response at
// all); a non-zero status below 500 is a definitive client error and
// stops retrying immediately. label is only used for the retry log line
// (see postJSON's comment on why it can differ from the request's actual
// URL); the error doRequest returns always carries the real URL.
func (ing *IziviaIngester) withRetries(ctx context.Context, label string, do func() ([]byte, int, error)) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt <= iziviaMaxRetries; attempt++ {
		if attempt > 0 {
			backoff := (1 << (attempt - 1)) * ing.retryBackoff
			log.Printf("izivia: retrying %s in %v (attempt %d/%d) after: %v", label, backoff, attempt+1, iziviaMaxRetries+1, lastErr)
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

// doRequest performs a single HTTP request, always including the full URL
// and status in any returned error so a failure is directly reproducible
// without reconstructing it from a marker/square logged separately.
func (ing *IziviaIngester) doRequest(ctx context.Context, method, url string, body io.Reader) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, 0, fmt.Errorf("build request for %s: %w", url, err)
	}
	for k, v := range iziviaHeaders {
		req.Header.Set(k, v)
	}
	resp, err := ing.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("izivia request to %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return nil, resp.StatusCode, fmt.Errorf("izivia http %d for %s: %s", resp.StatusCode, url, errorBodySummary(data))
	}
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("izivia read body from %s: %w", url, err)
	}
	return respBody, resp.StatusCode, nil
}

// errorBodySummary trims a non-2xx response body down to its first
// non-empty line, capped to a sane length. Izivia's own 500s come back as a
// full Kotlin/Java stack trace (exception class, then a "\tat ..." line per
// frame) — every frame after the first is redundant for our purposes (we
// don't control that backend and can't act on which line of Izivia's code
// threw), but withRetries logs the full error on every retry attempt, so
// keeping the whole trace multiplied a single 500 into a wall of repeated
// stack frames across every attempt. The exception class/message on the
// first line is already enough to tell "what kind of failure is this"
// without reproducing the whole trace log after log after log.
func errorBodySummary(body []byte) string {
	const maxLen = 200
	line := strings.TrimSpace(string(body))
	if idx := strings.IndexAny(line, "\r\n"); idx >= 0 {
		line = strings.TrimSpace(line[:idx])
	}
	if len(line) > maxLen {
		line = line[:maxLen] + "…"
	}
	if line == "" {
		return "(empty body)"
	}
	return line
}
