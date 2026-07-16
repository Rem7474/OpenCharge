package ingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"opencharge/internal/domain"
)

func TestNormalizeFreshmileStation(t *testing.T) {
	details := map[string]any{
		"ref":  "FREBNBBNXM1",
		"name": "MARCELLAZ ALBANAIS - Rue des ecoles",
		"coordinates": map[string]any{
			"latitude":  45.873704,
			"longitude": 5.998856,
		},
		"address": map[string]any{
			"fullname":    "14 Route de Peignat",
			"city":        "Marcellaz-Albanais",
			"postal_code": "74150",
			"country":     "FRA",
		},
	}

	src, ok := normalizeFreshmileStation(details)
	if !ok {
		t.Fatal("normalizeFreshmileStation returned ok=false, want true")
	}
	if src.Source != "freshmile" || src.SourceStationID != "FREBNBBNXM1" {
		t.Errorf("unexpected source station: %+v", src)
	}
	if src.OperatorName != "Freshmile" {
		t.Errorf("OperatorName = %q, want Freshmile", src.OperatorName)
	}
	if src.Lat != 45.873704 || src.Lng != 5.998856 {
		t.Errorf("unexpected location: (%v, %v)", src.Lat, src.Lng)
	}
	if src.AddressCity != "Marcellaz-Albanais" || src.AddressPostal != "74150" {
		t.Errorf("unexpected address: %+v", src)
	}
	if src.AddressCountry != "FR" {
		t.Errorf("AddressCountry = %q, want FR (mapped from FRA)", src.AddressCountry)
	}
}

func TestNormalizeFreshmileStationNoRef(t *testing.T) {
	if _, ok := normalizeFreshmileStation(map[string]any{"coordinates": map[string]any{"latitude": 45.0, "longitude": 6.0}}); ok {
		t.Error("normalizeFreshmileStation returned ok=true for a location without ref")
	}
}

func TestNormalizeFreshmileStationNoLocation(t *testing.T) {
	if _, ok := normalizeFreshmileStation(map[string]any{"ref": "X"}); ok {
		t.Error("normalizeFreshmileStation returned ok=true for a location without coordinates")
	}
}

func TestFreshmilePriceFromDescription(t *testing.T) {
	cases := []struct {
		name      string
		raw       any
		wantOK    bool
		wantCents float64
		wantLang  string
	}{
		{
			name:      "french comma decimal",
			raw:       `{"fr": "0,70 € / kWh entamé.", "en": "€ 0.70 / started kWh."}`,
			wantOK:    true,
			wantCents: 70.0,
			wantLang:  "fr",
		},
		{
			name:      "falls back to english when french missing",
			raw:       `{"en": "€ 0.51 / started kWh."}`,
			wantOK:    true,
			wantCents: 51.0,
			wantLang:  "en",
		},
		{
			name:      "plain text, not a per-language JSON blob (production format)",
			raw:       "0,70 € / kWh entamé.",
			wantOK:    true,
			wantCents: 70.0,
			wantLang:  "",
		},
		{
			name:      `plain text with "par" separator (production format)`,
			raw:       "Le prix dépend de l'énergie délivrée\n0,60 € par kWh entamé",
			wantOK:    true,
			wantCents: 60.0,
			wantLang:  "",
		},
		{
			name:      `plain text with "par" separator, more text after the price`,
			raw:       "Le prix dépend de l'énergie délivrée et du temps de branchement\n0,25 € par kWh entamé et 0,025 € par minute\nLa tarification continue tant que le véhicule reste branché",
			wantOK:    true,
			wantCents: 25.0,
			wantLang:  "",
		},
		{
			name:   "not valid json and no price pattern in it either",
			raw:    "Tarif sur demande",
			wantOK: false,
		},
		{
			name:   "no matching pattern in either language",
			raw:    `{"fr": "Gratuit", "en": "Free"}`,
			wantOK: false,
		},
		{
			name:   "empty",
			raw:    "",
			wantOK: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			price, lang, _, ok := freshmilePriceFromDescription(c.raw)
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v", ok, c.wantOK)
			}
			if !ok {
				return
			}
			if price == nil || *price != c.wantCents {
				t.Errorf("price = %v, want %v", price, c.wantCents)
			}
			if lang != c.wantLang {
				t.Errorf("lang = %q, want %q", lang, c.wantLang)
			}
		})
	}
}

func TestNormalizeFreshmileTariffs(t *testing.T) {
	details := map[string]any{
		"connectors": map[string]any{
			"best_power": map[string]any{"category": "fast", "kw": 50},
		},
		"evses": []any{
			map[string]any{
				"id": 937463,
				"connectors": []any{
					map[string]any{
						"id":       1022655,
						"power":    50.0,
						"standard": "IEC_62196_T2_COMBO",
						"tariff": map[string]any{
							"id":              4692,
							"currency":        "EUR",
							"is_free":         false,
							"is_preferential": false,
							"custom_ref":      "normal-k-wh-interop-20",
							"description":     `{"fr": "0,70 € / kWh entamé.", "en": "€ 0.70 / started kWh."}`,
						},
					},
					map[string]any{
						"id":       1022656,
						"power":    22.0,
						"standard": "IEC_62196_T2",
						"tariff": map[string]any{
							"id":              4693,
							"currency":        "EUR",
							"is_free":         false,
							"is_preferential": true,
							"custom_ref":      "normal-k-wh-interop-20",
							"description":     `{"fr": "0,51 € / kWh entamé.", "en": "€ 0.51 / started kWh."}`,
						},
					},
					map[string]any{
						"id":       1022657,
						"power":    22.0,
						"standard": "IEC_62196_T2",
						"tariff": map[string]any{
							"id":         4694,
							"currency":   "EUR",
							"is_free":    true,
							"custom_ref": "free-charging",
						},
					},
				},
			},
		},
	}

	tariffs := normalizeFreshmileTariffs(details)
	if len(tariffs) != 3 {
		t.Fatalf("got %d tariffs, want 3", len(tariffs))
	}

	byPlan := map[string]domain.StationTariff{}
	for _, tariff := range tariffs {
		if tariff.Source != "freshmile" {
			t.Errorf("tariff %s: Source = %q, want freshmile", tariff.Plan, tariff.Source)
		}
		byPlan[tariff.Plan] = tariff
	}

	standard, ok := byPlan["normal-k-wh-interop-20"]
	if !ok {
		t.Fatal("missing plan normal-k-wh-interop-20 (non-preferential)")
	}
	if standard.Kind != domain.TariffKindDC {
		t.Errorf("standard.Kind = %q, want dc (best_power_category=fast)", standard.Kind)
	}
	if standard.EnergyPriceCentsPerKWh == nil || *standard.EnergyPriceCentsPerKWh != 70.0 {
		t.Errorf("standard.EnergyPriceCentsPerKWh = %v, want 70.0", standard.EnergyPriceCentsPerKWh)
	}
	if standard.Extra["connectorType"] != "CCS" {
		t.Errorf("standard.Extra[connectorType] = %v, want CCS", standard.Extra["connectorType"])
	}

	preferential, ok := byPlan["normal-k-wh-interop-20:preferential"]
	if !ok {
		t.Fatal("missing plan normal-k-wh-interop-20:preferential")
	}
	if preferential.EnergyPriceCentsPerKWh == nil || *preferential.EnergyPriceCentsPerKWh != 51.0 {
		t.Errorf("preferential.EnergyPriceCentsPerKWh = %v, want 51.0", preferential.EnergyPriceCentsPerKWh)
	}
	if preferential.Extra["connectorType"] != "T2" {
		t.Errorf("preferential.Extra[connectorType] = %v, want T2", preferential.Extra["connectorType"])
	}

	free, ok := byPlan["free-charging"]
	if !ok {
		t.Fatal("missing plan free-charging")
	}
	if free.EnergyPriceCentsPerKWh != nil {
		t.Errorf("free.EnergyPriceCentsPerKWh = %v, want nil (is_free)", free.EnergyPriceCentsPerKWh)
	}
	if free.Extra["is_free"] != true {
		t.Errorf("free.Extra[is_free] = %v, want true", free.Extra["is_free"])
	}
}

func TestNormalizeFreshmileTariffsUnparsablePriceKeepsNilNotDropped(t *testing.T) {
	details := map[string]any{
		"evses": []any{
			map[string]any{
				"connectors": []any{
					map[string]any{
						"power":    22.0,
						"standard": "IEC_62196_T2",
						"tariff": map[string]any{
							"currency":    "EUR",
							"custom_ref":  "mystery-tariff",
							"description": `{"fr": "Tarif sur demande"}`,
						},
					},
				},
			},
		},
	}
	tariffs := normalizeFreshmileTariffs(details)
	if len(tariffs) != 1 {
		t.Fatalf("got %d tariffs, want 1 (kept even though price couldn't be parsed)", len(tariffs))
	}
	if tariffs[0].EnergyPriceCentsPerKWh != nil {
		t.Errorf("EnergyPriceCentsPerKWh = %v, want nil", tariffs[0].EnergyPriceCentsPerKWh)
	}
}

func TestNormalizeFreshmileTariffsNoEvses(t *testing.T) {
	if got := normalizeFreshmileTariffs(map[string]any{}); got != nil {
		t.Errorf("normalizeFreshmileTariffs({}) = %v, want nil", got)
	}
}

func TestFreshmileConnectorType(t *testing.T) {
	cases := map[string]string{
		"IEC_62196_T2_COMBO": "CCS",
		"CHAdeMO":            "CHAdeMO",
		"chademo":            "CHAdeMO",
		"IEC_62196_T2":       "T2",
		"UNKNOWN_STANDARD":   "UNKNOWN_STANDARD",
	}
	for input, want := range cases {
		if got := freshmileConnectorType(input); got != want {
			t.Errorf("freshmileConnectorType(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestPadDegenerateBBox(t *testing.T) {
	// Observed in the wild: a cluster's reported bbox.sw/bbox.ne collapse
	// to the same longitude, producing a zero-width query bbox that
	// Freshmile's API 500s on.
	degenerate := freshmileBBox{MinLng: -1.579401008784771, MinLat: 43.313418, MaxLng: -1.579401008784771, MaxLat: 43.313419}
	padded := padDegenerateBBox(degenerate)
	// Allow for ordinary float64 rounding in the center +/- half-width
	// arithmetic (e.g. 0.0009999999999998899 instead of exactly 0.001).
	if padded.MaxLng-padded.MinLng < freshmileMinBBoxDegrees-1e-9 {
		t.Errorf("padded width = %v, want ~>= %v", padded.MaxLng-padded.MinLng, freshmileMinBBoxDegrees)
	}
	if padded.MaxLng <= padded.MinLng {
		t.Errorf("padded bbox still degenerate: %+v", padded)
	}
	wantCenterLng := -1.579401008784771
	gotCenterLng := (padded.MinLng + padded.MaxLng) / 2
	if gotCenterLng != wantCenterLng {
		t.Errorf("padding shifted the center: got %v, want %v", gotCenterLng, wantCenterLng)
	}

	// A normal, already-valid bbox must be left untouched.
	normal := freshmileBBox{MinLng: 6.0, MinLat: 45.8, MaxLng: 6.3, MaxLat: 46.0}
	if got := padDegenerateBBox(normal); got != normal {
		t.Errorf("padDegenerateBBox(%+v) = %+v, want unchanged", normal, got)
	}
}

func TestSubdivideBBox(t *testing.T) {
	b := freshmileBBox{MinLng: 0, MinLat: 0, MaxLng: 2, MaxLat: 2}
	subs := subdivideBBox(b)
	if len(subs) != 4 {
		t.Fatalf("got %d sub-boxes, want 4", len(subs))
	}
	// Each quadrant should be a quarter of the original area, and their
	// union should reconstruct the original box exactly (no gaps/overlap
	// beyond shared edges).
	wantMinLng, wantMinLat := b.MinLng, b.MinLat
	wantMaxLng, wantMaxLat := b.MaxLng, b.MaxLat
	gotMinLng, gotMinLat := subs[0].MinLng, subs[0].MinLat
	gotMaxLng, gotMaxLat := subs[0].MaxLng, subs[0].MaxLat
	for _, s := range subs[1:] {
		if s.MinLng < gotMinLng {
			gotMinLng = s.MinLng
		}
		if s.MinLat < gotMinLat {
			gotMinLat = s.MinLat
		}
		if s.MaxLng > gotMaxLng {
			gotMaxLng = s.MaxLng
		}
		if s.MaxLat > gotMaxLat {
			gotMaxLat = s.MaxLat
		}
	}
	if gotMinLng != wantMinLng || gotMinLat != wantMinLat || gotMaxLng != wantMaxLng || gotMaxLat != wantMaxLat {
		t.Errorf("subdivided bounds = (%v,%v,%v,%v), want (%v,%v,%v,%v)", gotMinLng, gotMinLat, gotMaxLng, gotMaxLat, wantMinLng, wantMinLat, wantMaxLng, wantMaxLat)
	}
}

func TestFreshmileClusterBBox(t *testing.T) {
	props := map[string]any{
		"bbox": map[string]any{
			"sw": []any{6.109077958390117, 45.88170299772173},
			"ne": []any{6.1492969281971455, 45.92181698419154},
		},
	}
	bbox, ok := freshmileClusterBBox(props)
	if !ok {
		t.Fatal("freshmileClusterBBox returned ok=false, want true")
	}
	if bbox.MinLng != 6.109077958390117 || bbox.MinLat != 45.88170299772173 {
		t.Errorf("unexpected sw: (%v, %v)", bbox.MinLng, bbox.MinLat)
	}
	if bbox.MaxLng != 6.1492969281971455 || bbox.MaxLat != 45.92181698419154 {
		t.Errorf("unexpected ne: (%v, %v)", bbox.MaxLng, bbox.MaxLat)
	}
}

func TestFreshmileClusterBBoxMissing(t *testing.T) {
	if _, ok := freshmileClusterBBox(map[string]any{}); ok {
		t.Error("freshmileClusterBBox returned ok=true for properties without bbox")
	}
}

func newTestFreshmileIngester(baseURL string) *FreshmileIngester {
	ing := NewFreshmileIngester(nil, nil, nil, nil, baseURL, FreshmileConfig{})
	ing.retryBackoff = time.Millisecond // keep retry tests fast
	return ing
}

func TestGetJSONRetriesOn5xxThenSucceeds(t *testing.T) {
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

	ing := newTestFreshmileIngester(srv.URL)
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

func TestGetJSONDoesNotRetryOn4xx(t *testing.T) {
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, "not found")
	}))
	defer srv.Close()

	ing := newTestFreshmileIngester(srv.URL)
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

func TestGetJSONGivesUpAfterMaxRetries(t *testing.T) {
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = fmt.Fprint(w, "bad gateway")
	}))
	defer srv.Close()

	ing := newTestFreshmileIngester(srv.URL)
	_, err := ing.getJSON(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("getJSON = nil error, want an error after exhausting retries")
	}
	if !strings.Contains(err.Error(), srv.URL) {
		t.Errorf("error %q does not contain the request URL %q", err.Error(), srv.URL)
	}
	if got := atomic.LoadInt32(&requests); got != freshmileMaxRetries+1 {
		t.Errorf("requests = %d, want %d (initial attempt + %d retries)", got, freshmileMaxRetries+1, freshmileMaxRetries)
	}
}

// realFreshmileLocationPayload is an actual /locations/{id} response
// (production, captured 2026-07-16). Two things about it don't match
// what the rest of this file's fixtures assume: the location object is
// wrapped in a "data" envelope, and the tariff description is a single
// plain-text string ("0,25 € / kWh entamé + 0,05 € / min...") rather than
// the {"fr":...,"en":...} JSON blob normalizeFreshmileTariffs was
// originally written against.
const realFreshmileLocationPayload = `{"data":{"id":2999,"ref":"AEC485D47D","name":"Cannes Parking Braille","coordinates":{"latitude":43.554817,"longitude":7.022205},"address":{"fullname":"5 Rue Louis Braille","city":"Cannes","postal_code":"06400","country":"FRA"},"evses":[{"id":5926,"connectors":[{"id":11298,"power":4,"standard":"DOMESTIC_F","tariff":{"id":2998,"description":"0,25 € \/ kWh entamé + 0,05 € \/ min.\r\nLa tarification continue tant que le véhicule reste branché.","is_free":false,"currency":"EUR","custom_ref":"normal-interop","is_preferential":false}}]}],"connectors":{"best_power":{"category":"normal","kw":22},"types":["DOMESTIC_F","IEC_62196_T2"]}}}`

func TestNormalizeFreshmileRealPayloadWithDataEnvelopeAndPlainTextDescription(t *testing.T) {
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(realFreshmileLocationPayload), &envelope); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	details := envelope.Data
	if details == nil {
		t.Fatal("fixture missing data envelope")
	}

	src, ok := normalizeFreshmileStation(details)
	if !ok {
		t.Fatal("normalizeFreshmileStation = false, want true")
	}
	if src.SourceStationID != "AEC485D47D" {
		t.Errorf("SourceStationID = %q, want AEC485D47D", src.SourceStationID)
	}
	if src.Lat != 43.554817 || src.Lng != 7.022205 {
		t.Errorf("coords = %v,%v, want 43.554817,7.022205", src.Lat, src.Lng)
	}

	tariffs := normalizeFreshmileTariffs(details)
	if len(tariffs) != 1 {
		t.Fatalf("got %d tariffs, want 1", len(tariffs))
	}
	price := tariffs[0].EnergyPriceCentsPerKWh
	if price == nil || *price != 25.0 {
		t.Errorf("EnergyPriceCentsPerKWh = %v, want 25.0 (0,25 € plain-text description)", price)
	}
}

func TestFreshmilePriceFromDescriptionPlainTextEnglishFallback(t *testing.T) {
	price, lang, text, ok := freshmilePriceFromDescription("€ 0.30 / started kWh + € 0.30 / min\nThe pricing continues as long as the vehicle remains plugged in.")
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if lang != "" {
		t.Errorf("lang = %q, want empty (not a per-language JSON blob)", lang)
	}
	if price == nil || *price != 30.0 {
		t.Errorf("price = %v, want 30.0", price)
	}
	if text == "" {
		t.Error("text is empty, want the raw description")
	}
}
