package ingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/jackc/pgx/v5/pgxpool"

	"opencharge/internal/domain"
	"opencharge/internal/repository"
)

// DefaultTeslaLocationsURL lists every Tesla location (Superchargers,
// stores, service centers, ...); Run filters it down to open Superchargers.
const DefaultTeslaLocationsURL = "https://www.tesla.com/api/findus/get-locations"

const teslaDetailsURLFmt = "https://www.tesla.com/api/findus/get-charger-details?locationSlug=%s"

// TeslaConfig tunes the per-slug detail-fetch worker pool.
type TeslaConfig struct {
	Workers int
}

// DefaultTeslaConfig: Workers=4. Each fetch here is a real browser tab
// (see the package comment on chromedp below), not a plain HTTP round
// trip, so a smaller pool than Electra/Izivia/Freshmile's is deliberate —
// tabs are much heavier (memory, CPU) than idle HTTP connections.
func DefaultTeslaConfig() TeslaConfig {
	return TeslaConfig{Workers: 4}
}

// tesla.com/api/findus/* sits behind Akamai's bot mitigation, which
// rejects plain net/http requests outright (even with a full set of
// browser-like headers — verified: still a 403 "Access Denied" edge page)
// because it fingerprints the TLS/JS environment, not just headers. A real
// browser is required, so this ingester drives Chromium via chromedp
// instead of net/http for every fetch (both get-locations and
// get-charger-details). It also must run in "headed" mode (against a
// virtual display, not --headless) — Akamai fingerprints headless Chrome
// too and denies it just the same.
type TeslaIngester struct {
	Pool             *pgxpool.Pool
	SourceStations   *repository.SourceStationRepository
	Tariffs          *repository.TariffRepository
	Links            *repository.LinkRepository
	URL              string // get-locations endpoint, overridable for tests
	Config           TeslaConfig
	MaxLinkDistanceM float64
	// ChromeExecPath overrides the Chromium/Chrome binary chromedp
	// launches. Empty uses chromedp's own PATH lookup (google-chrome,
	// chromium, etc.) — set this when the binary lives somewhere
	// non-standard (e.g. the ingest Docker image's /usr/bin/chromium, or
	// a local dev machine's Playwright-managed Chromium).
	ChromeExecPath string
	detailsURLFmt  string // get-charger-details endpoint, derived from URL's host
}

func NewTeslaIngester(pool *pgxpool.Pool, sourceStations *repository.SourceStationRepository, tariffs *repository.TariffRepository, links *repository.LinkRepository, teslaURL string, cfg TeslaConfig) *TeslaIngester {
	if teslaURL == "" {
		teslaURL = DefaultTeslaLocationsURL
	}
	return &TeslaIngester{
		Pool:             pool,
		SourceStations:   sourceStations,
		Tariffs:          tariffs,
		Links:            links,
		URL:              teslaURL,
		Config:           cfg,
		MaxLinkDistanceM: DefaultLinkMaxDistanceMeters,
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
// the nearest IRVE point of charge. A single headless Chromium process is
// launched for the whole run and reused (one tab per fetch, via
// chromedp.NewContext off the shared allocator) rather than starting a new
// browser per request — that would be prohibitively slow. Detail fetches
// still run concurrently across worker tabs, with writes funneled through
// a single consumer batching via writeSourceStationChunk — same pattern as
// Electra/Izivia/Freshmile, just with browser tabs instead of HTTP
// connections as the concurrency unit.
func (ing *TeslaIngester) Run(ctx context.Context) (int, error) {
	allocOpts := append(chromedp.DefaultExecAllocatorOptions[:],
		// Deliberately NOT headless: Akamai's bot mitigation fingerprints
		// headless Chrome and serves an "Access Denied" page instead of
		// JSON (confirmed empirically — --headless=true and --headless=new
		// both get denied; the working Playwright prototype this was
		// ported from used headless=False for the same reason). Run a real
		// "headed" Chrome instead, pointed at a virtual display (Xvfb) in
		// the ingest Docker image — see its entrypoint.
		chromedp.Flag("headless", false),
		// Most container runtimes (including this project's Docker image)
		// run as root, where Chromium's sandbox refuses to start at all.
		chromedp.Flag("no-sandbox", true),
	)
	if ing.ChromeExecPath != "" {
		allocOpts = append(allocOpts, chromedp.ExecPath(ing.ChromeExecPath))
	}
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, allocOpts...)
	defer cancelAlloc()

	locations, err := ing.fetchLocations(allocCtx)
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
			item, ok, err := ing.fetchAndNormalizeSupercharger(allocCtx, slug)
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
		workers = 4
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
// (charger-details fetch via a browser tab, then normalization) without
// touching the database — writes are batched separately, see Run.
func (ing *TeslaIngester) fetchAndNormalizeSupercharger(allocCtx context.Context, slug string) (normalizedSourceStation, bool, error) {
	detailsURL := fmt.Sprintf(ing.detailsURLFmt, url.QueryEscape(slug))
	body, err := fetchViaChrome(allocCtx, detailsURL)
	if err != nil {
		return normalizedSourceStation{}, false, fmt.Errorf("fetch charger details: %w", err)
	}

	var envelope struct {
		Data struct {
			Data map[string]any `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return normalizedSourceStation{}, false, fmt.Errorf("decode charger details (got %d bytes from %s): %w", len(body), detailsURL, err)
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

func (ing *TeslaIngester) fetchLocations(allocCtx context.Context) ([]map[string]any, error) {
	log.Printf("tesla: downloading %s", ing.URL)
	body, err := fetchViaChrome(allocCtx, ing.URL)
	if err != nil {
		return nil, fmt.Errorf("download tesla locations: %w", err)
	}

	var envelope struct {
		Data struct {
			Data []map[string]any `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("decode tesla locations (got %d bytes from %s): %w", len(body), ing.URL, err)
	}
	return envelope.Data.Data, nil
}

// teslaFetchTimeout bounds a single page fetch: long enough for Akamai's
// bot-mitigation JS challenge (if any) to resolve, short enough that one
// stuck tab can't stall a whole ingestion run indefinitely.
const teslaFetchTimeout = 45 * time.Second

// fetchViaChrome navigates a fresh tab (its own chromedp.NewContext off
// the shared allocator — cheap compared to launching a new browser) to
// url and returns document.body.innerText, mirroring how a real browser
// hitting tesla.com/api/findus/* renders a raw JSON response as plain text
// in a <pre> tag. This is what actually gets past Akamai: plain net/http
// requests get a 403 "Access Denied" edge page regardless of headers sent,
// because the mitigation fingerprints the TLS/JS environment, not just
// request headers — a real browser engine is required, not just a
// convincing User-Agent.
func fetchViaChrome(allocCtx context.Context, url string) ([]byte, error) {
	tabCtx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()
	tabCtx, cancelTimeout := context.WithTimeout(tabCtx, teslaFetchTimeout)
	defer cancelTimeout()

	var text string
	if err := chromedp.Run(tabCtx,
		chromedp.Navigate(url),
		chromedp.Evaluate(`document.body.innerText`, &text),
	); err != nil {
		return nil, fmt.Errorf("chromedp fetch %s: %w", url, err)
	}
	return []byte(text), nil
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
