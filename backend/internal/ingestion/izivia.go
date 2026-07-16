package ingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
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

func DefaultIziviaConfig() IziviaConfig {
	return IziviaConfig{Workers: 12, GridStep: 2.0, Zoom: 7}
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
	return &IziviaIngester{
		Pool:             pool,
		SourceStations:   sourceStations,
		Tariffs:          tariffs,
		Links:            links,
		Config:           cfg,
		MaxLinkDistanceM: DefaultLinkMaxDistanceMeters,
		client:           &http.Client{Timeout: 20 * time.Second},
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

	markerCh := make(chan map[string]any)
	resultsCh := make(chan normalizedSourceStation)
	var wg sync.WaitGroup

	worker := func() {
		defer wg.Done()
		for marker := range markerCh {
			item, ok, err := ing.fetchAndNormalizeMarker(ctx, marker)
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
			case <-ctx.Done():
				return
			}
		}
	}

	workers := ing.Config.Workers
	if workers <= 0 {
		workers = 12
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
	}()

	var firstErr error
	for _, marker := range markers {
		select {
		case markerCh <- marker:
		case <-ctx.Done():
			firstErr = ctx.Err()
		}
	}
	close(markerCh)

	result := <-writeDone
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
		n, err := writeSourceStationChunk(ctx, ing.Pool, ing.SourceStations, ing.Tariffs, ing.Links, ing.MaxLinkDistanceM, batch)
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

	return src, normalizeIziviaTariffs(pricing), true
}

// normalizeIziviaTariffs turns Izivia's free-text pricing entries into
// StationTariff rows. Izivia pricing isn't split cleanly by connector kind
// in the public API, so every parsed price is stored as "mixed".
func normalizeIziviaTariffs(pricing []any) []domain.StationTariff {
	for _, item := range pricing {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		chargingStations, _ := entry["chargingStations"].([]any)
		for _, cs := range chargingStations {
			csMap, ok := cs.(map[string]any)
			if !ok {
				continue
			}
			texts := extractStringList(csMap["pricingInfos"])
			if len(texts) == 0 {
				texts = extractStringList(csMap["rawPricingInfos"])
			}
			if len(texts) == 0 {
				continue
			}
			rawText := texts[0]
			price, sessionPrice, fee := parsePriceText(rawText)
			if price == nil && sessionPrice == nil && fee == nil {
				continue
			}
			return []domain.StationTariff{{
				Source:                  "izivia",
				Plan:                    domain.TariffPlanStandard,
				Kind:                    domain.TariffKindMixed,
				Model:                   "izivia_text",
				Currency:                "EUR",
				EnergyPriceCentsPerKWh:  price,
				SessionPriceCentsPerMin: sessionPrice,
				ServiceFeePercent:       fee,
				RawText:                 rawText,
				Extra:                   map[string]any{},
			}}
		}
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

	all := make([]map[string]any, 0)
	seen := map[string]struct{}{}
	for i, square := range squares {
		payload := map[string]any{"square": square, "filters": map[string]any{}}
		body, err := ing.postJSON(ctx, iziviaBaseURL+"/map/markers", payload)
		if err != nil {
			log.Printf("izivia: markers square %d/%d failed: %v", i+1, len(squares), err)
			continue
		}
		var markers []map[string]any
		if err := json.Unmarshal(body, &markers); err != nil {
			continue
		}
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
