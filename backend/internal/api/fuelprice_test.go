package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestExtractSP95PriceEuros_PrefersE10Field(t *testing.T) {
	rec := map[string]any{
		"sp95_prix":     1.85,
		"sp95_e10_prix": 1.79,
		"gazole_prix":   1.70,
		"sp95_maj":      "2026-07-22T10:00:00+02:00",
	}
	got, ok := extractSP95PriceEuros(rec)
	if !ok {
		t.Fatal("expected a match")
	}
	if got != 1.79 {
		t.Errorf("price = %v, want 1.79 (the e10 field, not the plain sp95 or the update-date field)", got)
	}
}

func TestExtractSP95PriceEuros_FallsBackToBareSP95FieldWhenNoE10Variant(t *testing.T) {
	// Also exercises the string-typed-number case: some exports encode
	// prices as strings rather than JSON numbers.
	rec := map[string]any{"sp95_prix": "1.85", "gazole_prix": 1.70}
	got, ok := extractSP95PriceEuros(rec)
	if !ok || got != 1.85 {
		t.Errorf("got (%v, %v); want (1.85, true)", got, ok)
	}
}

func TestExtractSP95PriceEuros_NoMatchWithoutAnySP95Field(t *testing.T) {
	rec := map[string]any{"gazole_prix": 1.70, "e85_prix": 0.99}
	if _, ok := extractSP95PriceEuros(rec); ok {
		t.Error("expected no match: record has no sp95 field at all")
	}
}

func TestFuelPriceHandler_GetFuelPrice_LiveAverage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"sp95_e10_prix":1.80},{"sp95_e10_prix":1.70},{"gazole_prix":1.65}]}`))
	}))
	defer upstream.Close()

	h := &FuelPriceHandler{APIURL: upstream.URL, client: upstream.Client()}
	rr := httptest.NewRecorder()
	h.GetFuelPrice(rr, httptest.NewRequest(http.MethodGet, "/fuel-price", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	// Average of 1.80 and 1.65 -> 1.75€ -> 175 cents; the gazole-only record
	// contributes nothing since it has no sp95 field.
	want := `{"fuelType":"SP95-E10","pricePerLiterCents":175,"live":true}` + "\n"
	if got := rr.Body.String(); got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
}

func TestFuelPriceHandler_GetFuelPrice_FallsBackOnUpstreamError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()

	h := &FuelPriceHandler{APIURL: upstream.URL, client: upstream.Client()}
	rr := httptest.NewRecorder()
	h.GetFuelPrice(rr, httptest.NewRequest(http.MethodGet, "/fuel-price", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (fallback, not an error)", rr.Code)
	}
	want := fmt.Sprintf(`{"fuelType":"SP95-E10","pricePerLiterCents":%v,"live":false}`, fallbackSP95PriceCentsPerLiter) + "\n"
	if got := rr.Body.String(); got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
}

func TestFuelPriceHandler_GetFuelPrice_FallsBackWhenSchemaDoesntMatch(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"results":[{"gazole_prix":1.65},{"e85_prix":0.99}]}`))
	}))
	defer upstream.Close()

	h := &FuelPriceHandler{APIURL: upstream.URL, client: upstream.Client()}
	rr := httptest.NewRecorder()
	h.GetFuelPrice(rr, httptest.NewRequest(http.MethodGet, "/fuel-price", nil))

	want := fmt.Sprintf(`{"fuelType":"SP95-E10","pricePerLiterCents":%v,"live":false}`, fallbackSP95PriceCentsPerLiter) + "\n"
	if got := rr.Body.String(); got != want {
		t.Errorf("body = %s, want %s (no sp95-looking field anywhere in the response)", got, want)
	}
}

func TestFuelPriceHandler_GetFuelPrice_CachesAcrossCalls(t *testing.T) {
	var requests int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		_, _ = w.Write([]byte(`{"results":[{"sp95_e10_prix":1.80}]}`))
	}))
	defer upstream.Close()

	h := &FuelPriceHandler{APIURL: upstream.URL, client: upstream.Client()}
	for i := 0; i < 3; i++ {
		rr := httptest.NewRecorder()
		h.GetFuelPrice(rr, httptest.NewRequest(http.MethodGet, "/fuel-price", nil))
	}
	if got := atomic.LoadInt32(&requests); got != 1 {
		t.Errorf("upstream requests = %d, want 1 (subsequent calls served from cache)", got)
	}
}
