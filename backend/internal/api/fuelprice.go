package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultFuelPriceAPIURL is the French government's open-data feed for fuel
// prices (data.economie.gouv.fr, dataset
// "prix-des-carburants-en-france-flux-instantane-v2", refreshed every ~10
// minutes from real service-station reports) — see FuelPriceHandler's doc
// comment for why this only ever reads a sample of records to average,
// never a single station.
const DefaultFuelPriceAPIURL = "https://data.economie.gouv.fr/api/explore/v2.1/catalog/datasets/prix-des-carburants-en-france-flux-instantane-v2/records"

// fallbackSP95PriceCentsPerLiter is used whenever the live government feed
// can't be reached, or its field names have changed since this was written
// (this handler was built without the ability to verify the live schema
// against a real request — see extractSP95PriceEuros) — a recent real
// French SP95-E10 average, updated by hand occasionally, same "fixed
// constant updated by hand" tradeoff as fastned.go/lidl.go's own tariffs,
// rather than the essence/électrique comparison silently disappearing.
const fallbackSP95PriceCentsPerLiter = 178.0

const fuelPriceCacheTTL = 6 * time.Hour
const fuelPriceRequestTimeout = 8 * time.Second

// fuelPriceSampleSize caps how many station records are read to compute the
// average — this is a rough nationwide estimate for an "essence/électrique"
// cost-comparison argument, not a precision instrument, so a sample is
// enough and keeps the upstream request small.
const fuelPriceSampleSize = 200

// FuelPriceHandler proxies a nationwide-average SP95-E10 (unleaded) price
// for the essence/électrique cost comparison (see StationDetails.jsx /
// utils/fuelComparison.js). It deliberately averages a sample of stations
// rather than looking up the one nearest a given point: matching a
// specific station would need the same kind of geographic correlation
// machinery the IRVE/source-station linking already does for charge
// points, which isn't worth it for a number whose whole point is "roughly
// how much" — the comparison is an argument-maker, not a precise quote.
// Results are cached in memory (fuelPriceCacheTTL): the average barely
// moves within a day, and there's no reason to hit the government feed on
// every single page load.
type FuelPriceHandler struct {
	APIURL string
	client *http.Client

	mu       sync.Mutex
	cached   *fuelPriceResult
	cachedAt time.Time
}

type fuelPriceResult struct {
	PricePerLiterCents float64
	Live               bool
}

func NewFuelPriceHandler() *FuelPriceHandler {
	return &FuelPriceHandler{
		APIURL: DefaultFuelPriceAPIURL,
		client: &http.Client{Timeout: fuelPriceRequestTimeout},
	}
}

type fuelPriceDTO struct {
	FuelType           string  `json:"fuelType"`
	PricePerLiterCents float64 `json:"pricePerLiterCents"`
	Live               bool    `json:"live"`
}

// GetFuelPrice handles GET /fuel-price: the current nationwide-average
// SP95-E10 price, in cents/liter, plus whether it came from the live feed
// or the hardcoded fallback (live=false) — StationDetails shows a lighter
// "estimation" wording in the latter case rather than presenting a stale
// or approximate number as if it were exact.
func (h *FuelPriceHandler) GetFuelPrice(w http.ResponseWriter, r *http.Request) {
	result := h.currentPrice(r.Context())
	writeJSON(w, http.StatusOK, fuelPriceDTO{
		FuelType:           "SP95-E10",
		PricePerLiterCents: result.PricePerLiterCents,
		Live:               result.Live,
	})
}

func (h *FuelPriceHandler) currentPrice(ctx context.Context) fuelPriceResult {
	h.mu.Lock()
	if h.cached != nil && time.Since(h.cachedAt) < fuelPriceCacheTTL {
		cached := *h.cached
		h.mu.Unlock()
		return cached
	}
	h.mu.Unlock()

	result := fuelPriceResult{PricePerLiterCents: fallbackSP95PriceCentsPerLiter, Live: false}
	if avgEuros, err := h.fetchLiveAverage(ctx); err != nil {
		slog.Warn("fuel price: falling back to hardcoded estimate", "error", err)
	} else {
		result = fuelPriceResult{PricePerLiterCents: avgEuros * 100, Live: true}
	}

	h.mu.Lock()
	h.cached = &result
	h.cachedAt = time.Now()
	h.mu.Unlock()
	return result
}

// fetchLiveAverage downloads a sample of station records and averages
// whichever field looks like an SP95/SP95-E10 price (see
// extractSP95PriceEuros) — no `where`/`select` server-side filter, so a
// wrong guess at the exact field name can't turn into a rejected query;
// worst case, extractSP95PriceEuros finds nothing on every record and this
// returns an error, which currentPrice already treats as "use the fallback".
func (h *FuelPriceHandler) fetchLiveAverage(ctx context.Context) (float64, error) {
	url := fmt.Sprintf("%s?limit=%d", h.APIURL, fuelPriceSampleSize)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := h.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("upstream http %d", resp.StatusCode)
	}

	var body struct {
		Results []map[string]any `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return 0, fmt.Errorf("decode: %w", err)
	}

	var sum float64
	var count int
	for _, rec := range body.Results {
		if price, ok := extractSP95PriceEuros(rec); ok {
			sum += price
			count++
		}
	}
	if count == 0 {
		return 0, fmt.Errorf("no sp95 price field found in %d record(s)", len(body.Results))
	}
	return sum / float64(count), nil
}

// extractSP95PriceEuros scans a raw record's fields for a plausible SP95
// price, tolerant of the exact field name/casing (e.g. "sp95_prix",
// "Sp95_Prix", "prix_sp95_e10") rather than assuming one literal key — this
// was written without the ability to verify the live API's schema against
// a real request (see DefaultFuelPriceAPIURL's doc comment), so being
// loose here is what lets fetchLiveAverage degrade to the fallback
// constant instead of erroring outright if the exact names differ.
// Prefers a field naming both "sp95" and "e10" (the far more common pump
// grade today) over a bare "sp95" match, and skips anything that looks
// like a date/update-timestamp/outage field sharing the same prefix.
func extractSP95PriceEuros(rec map[string]any) (float64, bool) {
	var best float64
	var bestIsE10 bool
	found := false
	for key, v := range rec {
		lower := strings.ToLower(key)
		if !strings.Contains(lower, "sp95") {
			continue
		}
		if strings.Contains(lower, "maj") || strings.Contains(lower, "date") || strings.Contains(lower, "rupture") {
			continue
		}
		price, ok := numericValue(v)
		if !ok || price <= 0 {
			continue
		}
		isE10 := strings.Contains(lower, "e10")
		if !found || (isE10 && !bestIsE10) {
			best, bestIsE10, found = price, isE10, true
		}
	}
	return best, found
}

func numericValue(v any) (float64, bool) {
	switch val := v.(type) {
	case float64:
		return val, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(val), 64)
		if err != nil {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}
