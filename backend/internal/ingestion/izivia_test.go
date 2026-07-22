package ingestion

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"opencharge/internal/domain"
)

func newTestIziviaIngester() *IziviaIngester {
	ing := NewIziviaIngester(nil, nil, nil, nil, IziviaConfig{})
	ing.retryBackoff = time.Millisecond // keep retry tests fast
	return ing
}

func TestIziviaGetJSONRetriesOn5xxThenSucceeds(t *testing.T) {
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&requests, 1)
		if n < 3 {
			w.WriteHeader(http.StatusGatewayTimeout)
			_, _ = fmt.Fprint(w, "<html>504 Gateway Time-out</html>")
			return
		}
		_, _ = fmt.Fprint(w, `{"ok": true}`)
	}))
	defer srv.Close()

	ing := newTestIziviaIngester()
	body, err := ing.getJSON(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("getJSON: %v", err)
	}
	if string(body) != `{"ok": true}` {
		t.Errorf("body = %q, want ok:true", body)
	}
	if got := atomic.LoadInt32(&requests); got != 3 {
		t.Errorf("requests = %d, want 3 (2 failures + 1 success)", got)
	}
}

// TestIziviaPostJSONRetryLogUsesLabelNotJustURL pins the fix for a real
// production observability gap: /map/markers has the same URL for every
// grid square, so a retry/failure log keyed only on the URL is
// indistinguishable from any other in-flight square's log line — there's
// no way to tell which one actually failed. postJSON's label parameter
// (distinct from the request URL) must be what shows up in the retry log.
func TestIziviaPostJSONRetryLogUsesLabelNotJustURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = fmt.Fprint(w, "bad gateway")
	}))
	defer srv.Close()

	var logBuf bytes.Buffer
	prevOutput := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(prevOutput)

	ing := newTestIziviaIngester()
	label := srv.URL + " (square centerLng=2.2 centerLat=46.2 zoom=7)"
	_, err := ing.postJSON(context.Background(), srv.URL, label, map[string]any{"square": map[string]any{}})
	if err == nil {
		t.Fatal("postJSON = nil error, want an error after exhausting retries")
	}

	logged := logBuf.String()
	if !strings.Contains(logged, "centerLng=2.2 centerLat=46.2") {
		t.Errorf("retry log doesn't mention the square, got: %s", logged)
	}
}

func TestIziviaPostJSONRetriesOnNetworkErrorThenSucceeds(t *testing.T) {
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&requests, 1)
		if n < 2 {
			// Simulate a connection-level failure (no HTTP response at all,
			// status == 0 in withRetries) by closing the connection instead
			// of writing a status line.
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("ResponseWriter does not support hijacking")
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				t.Fatalf("hijack: %v", err)
			}
			_ = conn.Close()
			return
		}
		_, _ = fmt.Fprint(w, `{"markers": []}`)
	}))
	defer srv.Close()

	ing := newTestIziviaIngester()
	body, err := ing.postJSON(context.Background(), srv.URL, srv.URL, map[string]any{"square": map[string]any{}})
	if err != nil {
		t.Fatalf("postJSON: %v", err)
	}
	if string(body) != `{"markers": []}` {
		t.Errorf("body = %q, want markers:[]", body)
	}
	if got := atomic.LoadInt32(&requests); got != 2 {
		t.Errorf("requests = %d, want 2 (1 network failure + 1 success)", got)
	}
}

func TestIziviaGetJSONDoesNotRetryOn4xx(t *testing.T) {
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, "not found")
	}))
	defer srv.Close()

	ing := newTestIziviaIngester()
	_, err := ing.getJSON(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("getJSON = nil error, want an error for a 404")
	}
	if !strings.Contains(err.Error(), srv.URL) {
		t.Errorf("error %q does not contain the request URL %q", err.Error(), srv.URL)
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error %q does not contain the status code", err.Error())
	}
	if got := atomic.LoadInt32(&requests); got != 1 {
		t.Errorf("requests = %d, want 1 (4xx must not be retried)", got)
	}
}

func TestIziviaGetJSONGivesUpAfterMaxRetries(t *testing.T) {
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = fmt.Fprint(w, "bad gateway")
	}))
	defer srv.Close()

	ing := newTestIziviaIngester()
	_, err := ing.getJSON(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("getJSON = nil error, want an error after exhausting retries")
	}
	if !strings.Contains(err.Error(), srv.URL) {
		t.Errorf("error %q does not contain the request URL %q", err.Error(), srv.URL)
	}
	if got := atomic.LoadInt32(&requests); got != defaultMaxRetries+1 {
		t.Errorf("requests = %d, want %d (initial attempt + %d retries)", got, defaultMaxRetries+1, defaultMaxRetries)
	}
}

func TestNormalizeIziviaStation(t *testing.T) {
	marker := map[string]any{"id": "izv-1"}
	station := map[string]any{
		"id":          "izv-1",
		"name":        "Izivia Annecy",
		"coordinates": []any{6.1213, 45.9123},
		"address": map[string]any{
			"street":     "1 rue du lac",
			"postalCode": "74000",
			"city":       "Annecy",
			"country":    "FRA",
		},
	}
	pricing := []any{
		map[string]any{
			"chargingStations": []any{
				map[string]any{
					"pricingInfos": []any{"0,45€/kWh (Dont 15% de frais de service)"},
				},
			},
		},
	}

	src, tariffs, ok := normalizeIziviaStation(marker, station, pricing)
	if !ok {
		t.Fatal("normalizeIziviaStation returned ok=false, want true")
	}
	if src.Source != "izivia" || src.SourceStationID != "izv-1" {
		t.Errorf("unexpected source station: %+v", src)
	}
	if src.Lat != 45.9123 || src.Lng != 6.1213 {
		t.Errorf("unexpected location: (%v, %v)", src.Lat, src.Lng)
	}
	if src.AddressCountry != "FR" {
		t.Errorf("AddressCountry = %q, want FR", src.AddressCountry)
	}

	if len(tariffs) != 1 {
		t.Fatalf("got %d tariffs, want 1", len(tariffs))
	}
	tariff := tariffs[0]
	if tariff.Model != "izivia_text" {
		t.Errorf("Model = %q, want izivia_text", tariff.Model)
	}
	if tariff.EnergyPriceCentsPerKWh == nil || *tariff.EnergyPriceCentsPerKWh != 45.0 {
		t.Errorf("EnergyPriceCentsPerKWh = %v, want 45.0 (0,45€ in cents)", tariff.EnergyPriceCentsPerKWh)
	}
	if tariff.ServiceFeePercent == nil || *tariff.ServiceFeePercent != 15 {
		t.Errorf("ServiceFeePercent = %v, want 15", tariff.ServiceFeePercent)
	}
	if tariff.RawText != "0,45€/kWh (Dont 15% de frais de service)" {
		t.Errorf("RawText = %q", tariff.RawText)
	}
}

func TestNormalizeIziviaStationFallsBackToMarkerCoordinates(t *testing.T) {
	marker := map[string]any{"id": "izv-2", "lat": 45.0, "lng": 5.0}
	station := map[string]any{"id": "izv-2", "name": "Izivia sans coords station"}

	src, _, ok := normalizeIziviaStation(marker, station, nil)
	if !ok {
		t.Fatal("normalizeIziviaStation returned ok=false, want true")
	}
	if src.Lat != 45.0 || src.Lng != 5.0 {
		t.Errorf("unexpected fallback location: (%v, %v)", src.Lat, src.Lng)
	}
}

func TestNormalizeIziviaStationNoLocation(t *testing.T) {
	if _, _, ok := normalizeIziviaStation(map[string]any{"id": "izv-3"}, map[string]any{"id": "izv-3"}, nil); ok {
		t.Error("normalizeIziviaStation returned ok=true for a station without any location")
	}
}

func TestNormalizeIziviaTariffsNoPricing(t *testing.T) {
	if got := normalizeIziviaTariffs(nil, nil); got != nil {
		t.Errorf("normalizeIziviaTariffs(nil, nil) = %v, want nil", got)
	}
}

func TestNormalizeIziviaTariffsFallsBackToRawPricingInfos(t *testing.T) {
	pricing := []any{
		map[string]any{
			"chargingStations": []any{
				map[string]any{
					"rawPricingInfos": []any{"0,30€/kWh"},
				},
			},
		},
	}
	tariffs := normalizeIziviaTariffs(nil, pricing)
	if len(tariffs) != 1 {
		t.Fatalf("got %d tariffs, want 1", len(tariffs))
	}
	if tariffs[0].Kind != domain.TariffKindMixed {
		t.Errorf("Kind = %q, want mixed", tariffs[0].Kind)
	}
}

func TestNormalizeIziviaTariffsTopLevelPricingInfos(t *testing.T) {
	// The dominant production shape: itemType "charging_location", leaf, with
	// pricingInfos at the entry's own top level (not nested under
	// chargingStations). This must yield a tariff — the earlier code only
	// looked inside chargingStations and produced nothing here.
	pricing := []any{
		map[string]any{
			"rawPricingInfos": []any{"0,207€/kWh  <br> (Dont 15% de frais de service)"},
			"pricingInfos":    []any{"0,207€/kWh  \n (Dont 15% de frais de service)"},
			"itemType":        "charging_location",
			"structureType":   "leaf",
		},
	}
	tariffs := normalizeIziviaTariffs(nil, pricing)
	if len(tariffs) != 1 {
		t.Fatalf("got %d tariffs, want 1", len(tariffs))
	}
	if tariffs[0].EnergyPriceCentsPerKWh == nil || *tariffs[0].EnergyPriceCentsPerKWh != 20.7 {
		t.Errorf("EnergyPriceCentsPerKWh = %v, want 20.7 (0,207€ in cents)", tariffs[0].EnergyPriceCentsPerKWh)
	}
	if tariffs[0].ServiceFeePercent == nil || *tariffs[0].ServiceFeePercent != 15 {
		t.Errorf("ServiceFeePercent = %v, want 15", tariffs[0].ServiceFeePercent)
	}
}

func TestInferIziviaKind(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{"above 22kW is dc", "Connecteurs : CCS 24kW", domain.TariffKindDC},
		{"at 22kW is ac (boundary)", "Connecteurs : Type 2 22kW", domain.TariffKindAC},
		{"below 22kW is ac", "Connecteurs : Type 2 7kW", domain.TariffKindAC},
		{"comma decimal power", "Connecteurs : Type 2 3,7kW", domain.TariffKindAC},
		{"no power mentioned falls back to mixed", "0,45€/kWh (Dont 15% de frais de service)", domain.TariffKindMixed},
		{"empty text falls back to mixed", "", domain.TariffKindMixed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := inferIziviaKind(c.text); got != c.want {
				t.Errorf("inferIziviaKind(%q) = %q, want %q", c.text, got, c.want)
			}
		})
	}
}

func TestNormalizeIziviaTariffsInfersKindAndSkipsZeroPrice(t *testing.T) {
	// The exact style of problem text reported: a leading placeholder
	// "0.00 €/kWh" before the real price, and a connector power rating
	// used to infer DC/AC instead of always falling back to "mixed".
	rawText := "Connecteurs : CCS 24kW\n0.00 €/kWh\nFrais de service : 15%\n" +
		"0,391€/kWh Une fois la charge terminée : 15 min à 0,0€/min puis 0,23€/min (Dont 15% de frais de service)"
	pricing := []any{
		map[string]any{
			"chargingStations": []any{
				map[string]any{
					"pricingInfos": []any{rawText},
				},
			},
		},
	}

	tariffs := normalizeIziviaTariffs(nil, pricing)
	if len(tariffs) != 1 {
		t.Fatalf("got %d tariffs, want 1", len(tariffs))
	}
	tariff := tariffs[0]
	if tariff.Kind != domain.TariffKindDC {
		t.Errorf("Kind = %q, want dc (24kW > 22kW)", tariff.Kind)
	}
	if tariff.EnergyPriceCentsPerKWh == nil || *tariff.EnergyPriceCentsPerKWh != 39.1 {
		t.Errorf("EnergyPriceCentsPerKWh = %v, want 39.1 (skipping the leading 0.00)", tariff.EnergyPriceCentsPerKWh)
	}
	if tariff.SessionPriceCentsPerMin == nil || *tariff.SessionPriceCentsPerMin != 23.0 {
		t.Errorf("SessionPriceCentsPerMin = %v, want 23.0 (skipping the 0,0€/min grace period)", tariff.SessionPriceCentsPerMin)
	}
	if tariff.ServiceFeePercent == nil || *tariff.ServiceFeePercent != 15.0 {
		t.Errorf("ServiceFeePercent = %v, want 15.0", tariff.ServiceFeePercent)
	}
}

func TestIziviaTariffKindFromConnectorStats(t *testing.T) {
	acStation := map[string]any{"chargingConnectorsStats": []any{
		map[string]any{"standard": "t2", "maxPowerInW": 25000.0}, // AC even >22kW
		map[string]any{"standard": "standard_household", "maxPowerInW": 7260.0},
	}}
	dcStation := map[string]any{"chargingConnectorsStats": []any{
		map[string]any{"standard": "combo_t2", "maxPowerInW": 150000.0},
		map[string]any{"standard": "chademo", "maxPowerInW": 50000.0},
	}}
	mixedStation := map[string]any{"chargingConnectorsStats": []any{
		map[string]any{"standard": "t2", "maxPowerInW": 22000.0},
		map[string]any{"standard": "combo_t2", "maxPowerInW": 120000.0},
	}}

	// Text says "CCS 24kW" (would infer DC), but structured data is
	// authoritative: an AC-only station stays AC.
	if got := iziviaTariffKind(acStation, "Connecteurs : CCS 24kW"); got != domain.TariffKindAC {
		t.Errorf("AC-only station kind = %q, want ac", got)
	}
	if got := iziviaTariffKind(dcStation, ""); got != domain.TariffKindDC {
		t.Errorf("DC-only station kind = %q, want dc", got)
	}
	if got := iziviaTariffKind(mixedStation, ""); got != domain.TariffKindMixed {
		t.Errorf("AC+DC station kind = %q, want mixed", got)
	}
	// No structured data at all: fall back to the text heuristic.
	if got := iziviaTariffKind(map[string]any{}, "Connecteurs : CCS 50kW"); got != domain.TariffKindDC {
		t.Errorf("no-stats station with DC text = %q, want dc (text fallback)", got)
	}
	if got := iziviaTariffKind(map[string]any{}, "0,45€/kWh"); got != domain.TariffKindMixed {
		t.Errorf("no-stats station with no power in text = %q, want mixed", got)
	}
}

func TestNormalizeCountry(t *testing.T) {
	cases := map[string]string{
		"FRA":    "FR",
		"france": "FR",
		"FR":     "FR",
		"BE":     "BE",
	}
	for input, want := range cases {
		if got := normalizeCountry(input); got != want {
			t.Errorf("normalizeCountry(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestErrorBodySummary(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			"multi-line Java stack trace is cut to its first line",
			"com.izivia.emobility.fronts.commons.exception.ExternalTechnicalProblemException: \n\tat com.izivia.emobility.api.impl.mbp.MbpClientHttpHandlerKt.checkOk(MbpClientHttpHandler.kt:55)\n\tat com.izivia.emobility.api.impl.mbp.MbpClientHttpHandlerKt.check(MbpClientHttpHandler.kt:65)",
			"com.izivia.emobility.fronts.commons.exception.ExternalTechnicalProblemException:",
		},
		{"single-line body is kept as-is", "not found", "not found"},
		{"empty body", "", "(empty body)"},
		{"whitespace-only body", "   \n\n  ", "(empty body)"},
		{
			"a single line longer than the cap is truncated",
			strings.Repeat("x", 250),
			strings.Repeat("x", 200) + "…",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := errorBodySummary([]byte(c.body)); got != c.want {
				t.Errorf("errorBodySummary(%q) = %q, want %q", c.body, got, c.want)
			}
		})
	}
}
