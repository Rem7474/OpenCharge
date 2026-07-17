package ingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
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
	client           *http.Client
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
	}
}

// Run scans Izivia's map for markers covering metropolitan France, fetches
// station details and pricing for each, then correlates every station with
// the nearest IRVE point of charge. Fetching stays concurrent (the workers
// below are I/O-bound, so parallelism helps there), but database writes are
// funneled through a single consumer that batches them via
// writeSourceStationChunk — same bulk correlation + single-transaction
// pattern as Electra, instead of one uncommitted round trip per marker.
func (ing *IziviaIngester) Run(ctx context.Context) (int, error) {
	markers, err := ing.fetchMarkers(ctx)
	if err != nil {
		return 0, err
	}
	log.Printf("izivia: %d unique markers found", len(markers))

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

	log.Printf("izivia: done, %d stations processed", result.processed)
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

	stationBody, err := ing.postJSON(ctx, fmt.Sprintf("%s/charging-locations/%s", iziviaBaseURL, stationID), map[string]any{})
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
	return normalizedSourceStation{Station: src, Tariffs: tariffs}, true, nil
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
	for i := 0; i < scanWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for square := range squareCh {
				payload := map[string]any{"square": square, "filters": map[string]any{}}
				body, err := ing.postJSON(ctx, iziviaBaseURL+"/map/markers", payload)
				if err != nil {
					log.Printf("izivia: markers square failed: %v", err)
					continue
				}
				var markers []map[string]any
				if err := json.Unmarshal(body, &markers); err != nil {
					continue
				}
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
	return all, nil
}

func (ing *IziviaIngester) postJSON(ctx context.Context, url string, payload map[string]any) ([]byte, error) {
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	for k, v := range iziviaHeaders {
		req.Header.Set(k, v)
	}
	resp, err := ing.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return nil, fmt.Errorf("izivia http %d: %s", resp.StatusCode, string(data))
	}
	return io.ReadAll(resp.Body)
}

func (ing *IziviaIngester) getJSON(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range iziviaHeaders {
		req.Header.Set(k, v)
	}
	resp, err := ing.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return nil, fmt.Errorf("izivia http %d: %s", resp.StatusCode, string(data))
	}
	return io.ReadAll(resp.Body)
}
