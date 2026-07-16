package ingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"opencharge/internal/domain"
	"opencharge/internal/repository"
)

// DefaultTeslaLocationsURL lists every Tesla location (Superchargers,
// stores, service centers, ...); Run filters it down to open Superchargers.
const DefaultTeslaLocationsURL = "https://www.tesla.com/api/findus/get-locations"

const teslaDetailsURLFmt = "https://www.tesla.com/api/findus/get-charger-details?locationSlug=%s"

// teslaHeaders mirrors a real browser hitting tesla.com/findus: Akamai's
// edge bot mitigation (the "errors.edgesuite.net" 403 page) rejects
// requests missing these — a bare User-Agent isn't enough on its own.
var teslaHeaders = map[string]string{
	"Accept":                    "application/json",
	"Accept-Language":           "en-US,en;q=0.9",
	"Referer":                   "https://www.tesla.com/findus",
	"DNT":                       "1",
	"Upgrade-Insecure-Requests": "1",
	"Sec-Ch-Ua":                 `"Chromium";v="146", "Not-A.Brand";v="24", "Microsoft Edge";v="146"`,
	"Sec-Ch-Ua-Mobile":          "?0",
	"Sec-Ch-Ua-Platform":        `"Windows"`,
	"User-Agent":                "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36 Edg/146.0.0.0",
}

// TeslaConfig tunes the per-slug detail-fetch worker pool.
type TeslaConfig struct {
	Workers int
}

// DefaultTeslaConfig: Workers=8, within the 5-10 range asked for — this
// endpoint fetches one slug per station (no bulk list of tariffs like
// Electra), so it's a fan-out similar to Izivia's, but deliberately more
// conservative since Tesla's find-us API has no documented rate limits.
func DefaultTeslaConfig() TeslaConfig {
	return TeslaConfig{Workers: 8}
}

type TeslaIngester struct {
	Pool             *pgxpool.Pool
	SourceStations   *repository.SourceStationRepository
	Tariffs          *repository.TariffRepository
	Links            *repository.LinkRepository
	URL              string // get-locations endpoint, overridable for tests
	Config           TeslaConfig
	MaxLinkDistanceM float64
	client           *http.Client
	detailsURLFmt    string // get-charger-details endpoint, derived from URL's host
}

func NewTeslaIngester(pool *pgxpool.Pool, sourceStations *repository.SourceStationRepository, tariffs *repository.TariffRepository, links *repository.LinkRepository, teslaURL string, cfg TeslaConfig) *TeslaIngester {
	if teslaURL == "" {
		teslaURL = DefaultTeslaLocationsURL
	}
	workers := cfg.Workers
	if workers <= 0 {
		workers = 8
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = workers
	return &TeslaIngester{
		Pool:             pool,
		SourceStations:   sourceStations,
		Tariffs:          tariffs,
		Links:            links,
		URL:              teslaURL,
		Config:           cfg,
		MaxLinkDistanceM: DefaultLinkMaxDistanceMeters,
		client:           &http.Client{Timeout: 60 * time.Second, Transport: transport},
		detailsURLFmt:    teslaDetailsURLFmtFor(teslaURL),
	}
}

// teslaDetailsURLFmtFor derives the get-charger-details endpoint from the
// get-locations URL's own scheme+host, so pointing URL at a different host
// (tests, a future mirror/proxy) also redirects the per-station detail
// calls instead of always hitting the real tesla.com — falls back to the
// real endpoint if locationsURL doesn't parse.
func teslaDetailsURLFmtFor(locationsURL string) string {
	parsed, err := url.Parse(locationsURL)
	if err != nil || parsed.Host == "" {
		return teslaDetailsURLFmt
	}
	base := url.URL{Scheme: parsed.Scheme, Host: parsed.Host, Path: "/api/findus/get-charger-details"}
	return base.String() + "?locationSlug=%s"
}

// Run downloads Tesla's full locations list, keeps only open Superchargers,
// fetches each one's details/pricing, then correlates every station with
// the nearest IRVE point of charge. Detail fetches run concurrently (I/O
// bound, one HTTP round trip per Supercharger), but writes are funneled
// through a single consumer batching via writeSourceStationChunk — same
// pattern as Electra/Izivia.
func (ing *TeslaIngester) Run(ctx context.Context) (int, error) {
	locations, err := ing.fetchLocations(ctx)
	if err != nil {
		return 0, err
	}

	slugs := make([]string, 0, len(locations))
	for _, loc := range locations {
		if !isOpenSupercharger(loc) {
			continue
		}
		if slug := stringValue(loc["location_url_slug"]); slug != "" {
			slugs = append(slugs, slug)
		}
	}
	log.Printf("tesla: %d locations downloaded, %d open superchargers", len(locations), len(slugs))

	slugCh := make(chan string)
	resultsCh := make(chan normalizedSourceStation)
	var wg sync.WaitGroup

	worker := func() {
		defer wg.Done()
		for slug := range slugCh {
			item, ok, err := ing.fetchAndNormalizeSupercharger(ctx, slug)
			if err != nil {
				log.Printf("tesla: supercharger %s failed: %v", slug, err)
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
		processed, err := ing.writeResults(ctx, resultsCh, len(slugs))
		writeDone <- struct {
			processed int
			err       error
		}{processed, err}
	}()

	var firstErr error
	for _, slug := range slugs {
		select {
		case slugCh <- slug:
		case <-ctx.Done():
			firstErr = ctx.Err()
		}
	}
	close(slugCh)

	result := <-writeDone
	if firstErr == nil {
		firstErr = result.err
	}

	log.Printf("tesla: done, %d stations processed", result.processed)
	return result.processed, firstErr
}

// writeResults drains resultsCh, batching writes by ingestionBulkChunkSize
// through writeSourceStationChunk.
func (ing *TeslaIngester) writeResults(ctx context.Context, resultsCh <-chan normalizedSourceStation, total int) (int, error) {
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
		log.Printf("tesla: %d/%d processed", processed, total)
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

// fetchAndNormalizeSupercharger does the I/O-bound work for one slug
// (charger-details HTTP call, then normalization) without touching the
// database — writes are batched separately, see Run.
func (ing *TeslaIngester) fetchAndNormalizeSupercharger(ctx context.Context, slug string) (normalizedSourceStation, bool, error) {
	body, err := ing.getJSON(ctx, fmt.Sprintf(ing.detailsURLFmt, url.QueryEscape(slug)))
	if err != nil {
		return normalizedSourceStation{}, false, fmt.Errorf("fetch charger details: %w", err)
	}

	var envelope struct {
		Data struct {
			Data map[string]any `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return normalizedSourceStation{}, false, fmt.Errorf("decode charger details: %w", err)
	}
	details := envelope.Data.Data
	if details == nil {
		return normalizedSourceStation{}, false, fmt.Errorf("empty charger details")
	}

	src, ok := normalizeTeslaStation(slug, details)
	if !ok {
		return normalizedSourceStation{}, false, fmt.Errorf("station without usable location")
	}
	return normalizedSourceStation{Station: src, Tariffs: normalizeTeslaTariffs(details)}, true, nil
}

func (ing *TeslaIngester) fetchLocations(ctx context.Context) ([]map[string]any, error) {
	log.Printf("tesla: downloading %s", ing.URL)
	body, err := ing.getJSON(ctx, ing.URL)
	if err != nil {
		return nil, fmt.Errorf("download tesla locations: %w", err)
	}

	var envelope struct {
		Data struct {
			Data []map[string]any `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("decode tesla locations: %w", err)
	}
	return envelope.Data.Data, nil
}

func (ing *TeslaIngester) getJSON(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range teslaHeaders {
		req.Header.Set(k, v)
	}

	resp, err := ing.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return nil, fmt.Errorf("tesla http %d: %s", resp.StatusCode, string(data))
	}
	return io.ReadAll(resp.Body)
}

// isOpenSupercharger reports whether a get-locations entry is an
// operational Supercharger site: location_type must contain exactly
// "supercharger" (not "coming_soon_supercharger"/"winner_supercharger"),
// and if supercharger_function carries a site/project status, it must
// read "open". Entries with no status info are kept (status just isn't
// exposed for that entry) rather than excluded.
func isOpenSupercharger(loc map[string]any) bool {
	types, _ := loc["location_type"].([]any)
	hasSupercharger := false
	for _, t := range types {
		if stringValue(t) == "supercharger" {
			hasSupercharger = true
			break
		}
	}
	if !hasSupercharger {
		return false
	}

	fn, _ := loc["supercharger_function"].(map[string]any)
	if fn == nil {
		return true
	}
	if status := stringValue(fn["site_status"]); status != "" && !strings.EqualFold(status, "open") {
		return false
	}
	if status := stringValue(fn["project_status"]); status != "" && !strings.EqualFold(status, "open") {
		return false
	}
	return true
}

func normalizeTeslaStation(slug string, details map[string]any) (domain.SourceStation, bool) {
	lat, lng, ok := teslaCoordinates(details)
	if !ok {
		return domain.SourceStation{}, false
	}

	addressMap, _ := details["address"].(map[string]any)
	name := firstNonEmpty(stringValue(details["name"]), stringValue(details["commonSiteName"]))

	return domain.SourceStation{
		Source:          "tesla",
		SourceStationID: slug,
		Name:            name,
		OperatorName:    "Tesla",
		AddressStreet:   stringValue(addressMap["street"]),
		AddressPostal:   stringValue(addressMap["postalCode"]),
		AddressCity:     stringValue(addressMap["city"]),
		AddressCountry:  strings.ToUpper(stringValue(addressMap["countryCode"])),
		Lat:             lat,
		Lng:             lng,
		Raw:             details,
	}, true
}

// teslaCoordinates prefers entryPoint (where you'd actually navigate to)
// over centroid (the site's geometric center), falling back to centroid
// when entryPoint is absent.
func teslaCoordinates(details map[string]any) (lat, lng float64, ok bool) {
	if la, lo, ok := teslaLatLng(details["entryPoint"]); ok {
		return la, lo, true
	}
	if la, lo, ok := teslaLatLng(details["centroid"]); ok {
		return la, lo, true
	}
	return 0, 0, false
}

func teslaLatLng(value any) (lat, lng float64, ok bool) {
	point, isMap := value.(map[string]any)
	if !isMap {
		return 0, 0, false
	}
	la, laOK := floatValue(point["latitude"])
	lo, loOK := floatValue(point["longitude"])
	if !laOK || !loOK {
		return 0, 0, false
	}
	return *la, *lo, true
}

// teslaPricebookKey identifies a (vehicleMakeType, isMemberPricebook) pair
// from effectivePricebooks — each distinct pair becomes its own
// StationTariff (never merged), matching how Tesla actually prices a
// charge: Tesla vs. non-Tesla vehicles, member vs. public rate.
type teslaPricebookKey struct {
	vehicleMakeType   string
	isMemberPricebook bool
}

func teslaPlan(key teslaPricebookKey) string {
	tesla := key.vehicleMakeType == "TSLA"
	switch {
	case tesla && key.isMemberPricebook:
		return "tesla_member"
	case tesla && !key.isMemberPricebook:
		return "tesla_public"
	case !tesla && key.isMemberPricebook:
		return "non_tesla_member"
	default:
		return "non_tesla_public"
	}
}

// normalizeTeslaTariffs turns effectivePricebooks into one StationTariff
// per (vehicleMakeType, isMemberPricebook) pair that has a CHARGING entry.
// A PARKING entry for the same pair contributes its per-minute rate as
// CongestionPriceCentsPerMin on that same tariff (an idle/overstay fee,
// the closest existing concept to Electra's congestion fee) rather than
// becoming a separate row. The full raw pricebook entries are kept in
// Extra for future refinement (tiered/power-based rates aren't modeled).
func normalizeTeslaTariffs(details map[string]any) []domain.StationTariff {
	pricebooks, _ := details["effectivePricebooks"].([]any)

	tariffs := map[teslaPricebookKey]*domain.StationTariff{}
	var order []teslaPricebookKey

	for _, raw := range pricebooks {
		pb, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		key := teslaPricebookKey{
			vehicleMakeType:   strings.ToUpper(stringValue(pb["vehicleMakeType"])),
			isMemberPricebook: parseBooleanLoose(stringValue(pb["isMemberPricebook"])),
		}
		if key.vehicleMakeType == "" {
			continue
		}

		tariff, exists := tariffs[key]
		if !exists {
			tariff = &domain.StationTariff{
				Source:   "tesla",
				Plan:     teslaPlan(key),
				Kind:     domain.TariffKindDC,
				Model:    "tesla_pricebook",
				Currency: firstNonEmpty(stringValue(pb["currencyCode"]), "EUR"),
				Extra:    map[string]any{},
			}
			tariffs[key] = tariff
			order = append(order, key)
		}

		switch strings.ToUpper(stringValue(pb["feeType"])) {
		case "CHARGING":
			tariff.EnergyPriceCentsPerKWh = teslaEnergyPriceCents(pb)
			tariff.Extra["charging"] = pb
		case "PARKING":
			tariff.CongestionPriceCentsPerMin = teslaRateCents(pb)
			tariff.Extra["parking"] = pb
		default:
			tariff.Extra[strings.ToLower(stringValue(pb["feeType"]))] = pb
		}
	}

	var result []domain.StationTariff
	for _, key := range order {
		t := tariffs[key]
		// A PARKING-only pair (no matching CHARGING fee) isn't a
		// chargeable tariff — skip it rather than emit a row with no
		// energy price.
		if _, hasCharging := t.Extra["charging"]; !hasCharging {
			continue
		}
		result = append(result, *t)
	}
	return result
}

// teslaEnergyPriceCents derives EnergyPriceCentsPerKWh from a CHARGING
// pricebook entry's uom: "free" is a flat zero, "kwh" uses the entry's
// rate (see teslaRateCents); any other unit (e.g. per-session flat fees)
// isn't modeled here and is left nil — the raw entry is still in Extra.
func teslaEnergyPriceCents(pb map[string]any) *float64 {
	switch strings.ToLower(stringValue(pb["uom"])) {
	case "free":
		zero := 0.0
		return &zero
	case "kwh":
		return teslaRateCents(pb)
	default:
		return nil
	}
}

// teslaRateCents converts a pricebook entry's rate (euros, like the rest
// of the raw sources normalized here) to cents: rateBase is used first,
// falling back to rateTier1 when rateBase is absent/zero. Tiered/power-
// based pricing (rateTier2, rateTier3, thresholds) isn't modeled — this is
// a documented simplification, revisitable once real pricebook samples are
// available; the raw entry stays in Extra regardless.
func teslaRateCents(pb map[string]any) *float64 {
	rate, ok := floatValue(pb["rateBase"])
	if !ok || rate == nil || *rate == 0 {
		if tier1, tier1OK := floatValue(pb["rateTier1"]); tier1OK && tier1 != nil && *tier1 != 0 {
			rate = tier1
		}
	}
	if rate == nil {
		return nil
	}
	// Round to avoid float64 noise from the euro->cents multiplication
	// (e.g. 0.55 * 100 = 55.00000000000001): a rate never needs more than
	// hundredths-of-a-cent precision.
	cents := math.Round(*rate*10000) / 100
	return &cents
}
