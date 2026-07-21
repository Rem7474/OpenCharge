package ingestion

import (
	"context"
	"encoding/json"
	"errors"
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

// TestNormalizeFreshmileTariffsDedupesPerKindAndConnectorType exercises a
// three-connector fixture with two distinct connector types: one CCS
// (Kind=dc, from its own standard, regardless of the station-level
// best_power category also being "fast" here) and two T2 (Kind=ac — T2 is
// AC regardless of the station's best_power category; see
// freshmileTariffKind). Dedup groups by (Kind, ConnectorType), not Kind
// alone, so this must produce two tariffs: the lone CCS candidate
// survives as-is, and the two T2 candidates resolve via
// freshmileBetterCandidate (free non-preferential beats preferential
// regardless of price).
func TestNormalizeFreshmileTariffsDedupesPerKindAndConnectorType(t *testing.T) {
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
	if len(tariffs) != 2 {
		t.Fatalf("got %d tariffs, want 2 (CCS/dc and T2/ac stay separate)", len(tariffs))
	}

	byConnector := map[string]domain.StationTariff{}
	for _, tariff := range tariffs {
		if tariff.Source != "freshmile" {
			t.Errorf("tariff %s: Source = %q, want freshmile", tariff.ConnectorType, tariff.Source)
		}
		if tariff.Plan != domain.TariffPlanStandard {
			t.Errorf("tariff %s: Plan = %q, want %q (custom_ref is no longer surfaced as Plan)", tariff.ConnectorType, tariff.Plan, domain.TariffPlanStandard)
		}
		byConnector[tariff.ConnectorType] = tariff
	}

	ccs, ok := byConnector[domain.ConnectorTypeCCS]
	if !ok {
		t.Fatal("missing CCS tariff")
	}
	if ccs.Kind != domain.TariffKindDC {
		t.Errorf("CCS Kind = %q, want dc", ccs.Kind)
	}
	if ccs.EnergyPriceCentsPerKWh == nil || *ccs.EnergyPriceCentsPerKWh != 70.0 {
		t.Errorf("CCS EnergyPriceCentsPerKWh = %v, want 70.0 (only candidate for this connector type)", ccs.EnergyPriceCentsPerKWh)
	}

	t2, ok := byConnector[domain.ConnectorTypeT2]
	if !ok {
		t.Fatal("missing T2 tariff")
	}
	if t2.Kind != domain.TariffKindAC {
		t.Errorf("T2 Kind = %q, want ac", t2.Kind)
	}
	if t2.EnergyPriceCentsPerKWh == nil || *t2.EnergyPriceCentsPerKWh != 0 {
		t.Errorf("T2 EnergyPriceCentsPerKWh = %v, want 0 (the free, non-preferential candidate wins over the preferential 51 one)", t2.EnergyPriceCentsPerKWh)
	}
	if t2.Extra["is_free"] != true {
		t.Errorf("T2 Extra[is_free] = %v, want true", t2.Extra["is_free"])
	}
}

// TestNormalizeFreshmileTariffsNonPreferentialAlwaysWins pins that a
// non-preferential (publicly available) price is picked over a
// preferential (partner/member-only) one even when the preferential price
// is cheaper — showing a discount most visitors can't access would be
// misleading.
func TestNormalizeFreshmileTariffsNonPreferentialAlwaysWins(t *testing.T) {
	details := map[string]any{
		"evses": []any{
			map[string]any{
				"connectors": []any{
					map[string]any{
						"power":    22.0,
						"standard": "IEC_62196_T2",
						"tariff": map[string]any{
							"currency":        "EUR",
							"is_preferential": true,
							"custom_ref":      "cheap-partner-deal",
							"description":     `{"fr": "0,10 € / kWh entamé."}`,
						},
					},
					map[string]any{
						"power":    22.0,
						"standard": "IEC_62196_T2",
						"tariff": map[string]any{
							"currency":    "EUR",
							"custom_ref":  "public-price",
							"description": `{"fr": "0,80 € / kWh entamé."}`,
						},
					},
				},
			},
		},
	}

	tariffs := normalizeFreshmileTariffs(details)
	if len(tariffs) != 1 {
		t.Fatalf("got %d tariffs, want 1", len(tariffs))
	}
	if got := tariffs[0].EnergyPriceCentsPerKWh; got == nil || *got != 80.0 {
		t.Errorf("EnergyPriceCentsPerKWh = %v, want 80.0 (the more expensive but non-preferential price)", got)
	}
}

// TestNormalizeFreshmileTariffsSeparatesACAndDC confirms the per-Kind
// dedup keeps AC and DC as two distinct tariffs rather than collapsing
// everything down to a single row per station.
func TestNormalizeFreshmileTariffsSeparatesACAndDC(t *testing.T) {
	details := map[string]any{
		"evses": []any{
			map[string]any{
				"connectors": []any{
					map[string]any{
						"power":    22.0,
						"standard": "IEC_62196_T2",
						"tariff": map[string]any{
							"currency":    "EUR",
							"custom_ref":  "ac-tariff",
							"description": `{"fr": "0,40 € / kWh entamé."}`,
						},
					},
					map[string]any{
						"power":    100.0,
						"standard": "IEC_62196_T2_COMBO",
						"tariff": map[string]any{
							"currency":    "EUR",
							"custom_ref":  "dc-tariff",
							"description": `{"fr": "0,60 € / kWh entamé."}`,
						},
					},
				},
			},
		},
	}

	tariffs := normalizeFreshmileTariffs(details)
	if len(tariffs) != 2 {
		t.Fatalf("got %d tariffs, want 2 (one ac, one dc)", len(tariffs))
	}
	byKind := map[string]domain.StationTariff{}
	for _, tariff := range tariffs {
		byKind[tariff.Kind] = tariff
	}
	if ac, ok := byKind[domain.TariffKindAC]; !ok || ac.EnergyPriceCentsPerKWh == nil || *ac.EnergyPriceCentsPerKWh != 40.0 {
		t.Errorf("ac tariff = %+v, want EnergyPriceCentsPerKWh=40.0", ac)
	}
	if dc, ok := byKind[domain.TariffKindDC]; !ok || dc.EnergyPriceCentsPerKWh == nil || *dc.EnergyPriceCentsPerKWh != 60.0 {
		t.Errorf("dc tariff = %+v, want EnergyPriceCentsPerKWh=60.0", dc)
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

// TestNormalizeFreshmileTariffsPackagedPriceStaysUnparsed pins that a
// "forfait" (flat package for a fixed amount of energy, e.g. "forfait de 2
// € par 6 kWh") — real production text — is deliberately left with both
// prices nil rather than guessing a €/kWh figure from it. The pattern's
// tight "par <unit>" adjacency requirement already keeps this from
// matching (there's a number between "par" and "kWh"), so this is a
// regression test for that, not new behavior.
func TestNormalizeFreshmileTariffsPackagedPriceStaysUnparsed(t *testing.T) {
	details := map[string]any{
		"evses": []any{
			map[string]any{
				"connectors": []any{
					map[string]any{
						"power":    22.0,
						"standard": "IEC_62196_T2",
						"tariff": map[string]any{
							"currency":   "EUR",
							"custom_ref": "package-pricing",
							"description": "Le prix dépend de l'énergie délivrée.\n" +
								"- Recharge par badge : forfait de 2 € par 6 kWh\n" +
								"- Recharge par smartphone en mode anonyme : forfait de 3 € par 6 kWh",
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
		t.Errorf("EnergyPriceCentsPerKWh = %v, want nil (packaged pricing isn't a single €/kWh figure)", tariffs[0].EnergyPriceCentsPerKWh)
	}
	if tariffs[0].SessionPriceCentsPerMin != nil {
		t.Errorf("SessionPriceCentsPerMin = %v, want nil", tariffs[0].SessionPriceCentsPerMin)
	}
}

// TestNormalizeFreshmileTariffsCombinedKWhAndPerMinute pins two real
// production behaviors together: lowercase "kwh" must match (case was
// previously significant and silently dropped it), and a description
// combining both a €/kWh price and a €/min rate must populate both fields
// instead of stopping at whichever pattern matches first.
func TestNormalizeFreshmileTariffsCombinedKWhAndPerMinute(t *testing.T) {
	details := map[string]any{
		"evses": []any{
			map[string]any{
				"connectors": []any{
					map[string]any{
						"power":    22.0,
						"standard": "IEC_62196_T2",
						"tariff": map[string]any{
							"currency":   "EUR",
							"custom_ref": "kwh-and-per-minute",
							"description": "Le prix dépend de l'énergie délivrée et du temps de branchement\n" +
								"0,50 € par kwh et 0,05 € par minute",
						},
					},
				},
			},
		},
	}
	tariffs := normalizeFreshmileTariffs(details)
	if len(tariffs) != 1 {
		t.Fatalf("got %d tariffs, want 1", len(tariffs))
	}
	got := tariffs[0]
	if got.EnergyPriceCentsPerKWh == nil || *got.EnergyPriceCentsPerKWh != 50.0 {
		t.Errorf("EnergyPriceCentsPerKWh = %v, want 50.0", got.EnergyPriceCentsPerKWh)
	}
	if got.SessionPriceCentsPerMin == nil || *got.SessionPriceCentsPerMin != 5.0 {
		t.Errorf("SessionPriceCentsPerMin = %v, want 5.0", got.SessionPriceCentsPerMin)
	}
}

// TestNormalizeFreshmileTariffsKWhEntameAndSubCentPerMinute pins another
// real production description shape: single-line (spaces, no newlines),
// "kWh entamé" sitting between the energy price and the "et ..." per-minute
// clause, and a sub-cent per-minute amount with three decimals (0,025 € →
// 2.5 cents/min, which must survive the euro→cents conversion untruncated).
func TestNormalizeFreshmileTariffsKWhEntameAndSubCentPerMinute(t *testing.T) {
	details := map[string]any{
		"evses": []any{
			map[string]any{
				"connectors": []any{
					map[string]any{
						"power":    22.0,
						"standard": "IEC_62196_T2",
						"tariff": map[string]any{
							"currency":    "EUR",
							"description": "Le prix dépend de l'énergie délivrée et du temps de branchement 0,20 € par kWh entamé et 0,025 € par minute La tarification continue tant que le véhicule est branché",
						},
					},
				},
			},
		},
	}
	tariffs := normalizeFreshmileTariffs(details)
	if len(tariffs) != 1 {
		t.Fatalf("got %d tariffs, want 1", len(tariffs))
	}
	got := tariffs[0]
	if got.EnergyPriceCentsPerKWh == nil || *got.EnergyPriceCentsPerKWh != 20.0 {
		t.Errorf("EnergyPriceCentsPerKWh = %v, want 20.0", got.EnergyPriceCentsPerKWh)
	}
	if got.SessionPriceCentsPerMin == nil || *got.SessionPriceCentsPerMin != 2.5 {
		t.Errorf("SessionPriceCentsPerMin = %v, want 2.5", got.SessionPriceCentsPerMin)
	}
	if got.Model != "freshmile_kwh_and_per_minute" {
		t.Errorf("Model = %q, want freshmile_kwh_and_per_minute", got.Model)
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
	body, err := ing.getJSON(context.Background(), srv.URL, nil)
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
	_, err := ing.getJSON(context.Background(), srv.URL, nil)
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
	_, err := ing.getJSON(context.Background(), srv.URL, nil)
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

// realFreshmileEmptyLocationPayload is a real /locations/{id} response for
// a currently-unavailable location with no evses at all (production,
// captured 2026-07-16) — normalization must still succeed for the station
// itself (ref/coordinates are present) while producing zero tariffs,
// rather than erroring out.
const realFreshmileEmptyLocationPayload = `{"data":{"id":3658,"ref":"C1DF1E5BC8","name":"BP | Aire de Saint Léger OUEST","evses_statuses":[],"is_available":false,"coordinates":{"latitude":45.6116,"longitude":-0.60293},"address":{"fullname":"A10","city":"Saint-Léger","postal_code":"17800","country":"FRA"},"evses":[],"evses_available_count":0,"evses_total_count":0,"connectors":{"best_power":{"category":"superfast","kw":0},"types":["IEC_62196_T2_COMBO","IEC_62196_T2","CHADEMO"]}}}`

func TestNormalizeFreshmileRealPayloadEmptyEvses(t *testing.T) {
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(realFreshmileEmptyLocationPayload), &envelope); err != nil {
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
	if src.SourceStationID != "C1DF1E5BC8" {
		t.Errorf("SourceStationID = %q, want C1DF1E5BC8", src.SourceStationID)
	}
	if src.Lat != 45.6116 || src.Lng != -0.60293 {
		t.Errorf("coords = %v,%v, want 45.6116,-0.60293", src.Lat, src.Lng)
	}

	if tariffs := normalizeFreshmileTariffs(details); len(tariffs) != 0 {
		t.Errorf("got %d tariffs, want 0 (no evses)", len(tariffs))
	}
}

// TestFreshmileRunSurfacesContextErrorInsteadOfSweeping pins the fix for a
// production failure: a run cut short by the CLI's -timeout still ended
// with a nil pipeline error (writes are decoupled from ctx, so the last
// batches commit fine), making the truncated run look fully successful —
// Run() then attempted the stale-data sweep with an already-expired ctx
// ("sweep stale source_stations for freshmile: context deadline exceeded"),
// and only that query's own failure kept it from wiping every location the
// run never got to visit. Run must report the ctx error itself and never
// reach the sweep (the nil Pool here would panic if it did).
func TestFreshmileRunSurfacesContextErrorInsteadOfSweeping(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"features": []}`)
	}))
	defer srv.Close()

	ing := NewFreshmileIngester(nil, nil, nil, nil, srv.URL, FreshmileConfig{Workers: 2})
	ing.retryBackoff = time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	processed, err := ing.Run(ctx)
	if processed != 0 {
		t.Errorf("processed = %d, want 0", processed)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Run() error = %v, want context.Canceled", err)
	}
}

// realFreshmileAnnecyMixedConnectorsPayload is a real /locations/{id}
// response (production, captured 2026-07-21) for a site mixing a CCS
// connector (24kW, driving the whole location's best_power.category to
// "fast") with several genuinely-AC Type 2 (22kW) and domestic (4kW)
// connectors — see TestNormalizeFreshmileRealPayloadMixedConnectorKinds.
const realFreshmileAnnecyMixedConnectorsPayload = `{"data":{"id":829320,"ref":"FREBNBTHPN1","name":"ANNECY - Route De Vignieres","evses_statuses":["AVAILABLE"],"is_available":true,"has_free_tariff":false,"is_favorite":false,"is_open":false,"is_private":false,"has_access_control":0,"twentyfourseven":false,"regular_hours":[],"coordinates":{"latitude":45.9078,"longitude":6.13666},"address":{"fullname":"Route de Vigni\u00e8res","city":"Annecy","postal_code":"74000","country":"FRA"},"evses":[{"id":936864,"custom_ref":"OHC3YQ1","status":"AVAILABLE","is_available":true,"is_remote_capable":false,"created_at":"2025-04-01T17:13:38.000000Z","updated_at":"2026-06-11T13:24:28.000000Z","location_id":829320,"connectors":[{"id":1021614,"ref":null,"power":24,"standard":"IEC_62196_T2_COMBO","is_remote_capable":false,"is_attached":false,"created_at":"2025-04-01T17:13:38.000000Z","updated_at":"2025-04-01T17:13:38.000000Z","tariff_id":4692,"evse_id":936864,"tariff":{"id":4692,"name":"Interop sortante - Normal kWh (20)","description":"{\"de\": \"\u20ac 0.70 / started kWh.\", \"en\": \"\u20ac 0.70 / started kWh.\", \"es\": \"\u20ac 0.70 / started kWh.\", \"fr\": \"0,70 \u20ac / kWh entam\u00e9.\", \"nl\": \"\u20ac 0.70 / started kWh.\"}","is_free":false,"currency":"EUR","provision":{"amount":50,"currency":"EUR"},"payment_authorization_amount":{"amount":50,"currency":"EUR"},"max_price":{"amount":166.67,"currency":"EUR"},"custom_ref":"normal-k-wh-interop-20","is_hidden":false,"is_preferential":false,"origin_ref":"normal-k-wh-interop-20","commissioned_at":null}}]},{"id":936865,"custom_ref":"OCYIR91","status":"AVAILABLE","is_available":true,"is_remote_capable":false,"created_at":"2025-04-01T17:13:39.000000Z","updated_at":"2026-06-11T13:24:28.000000Z","location_id":829320,"connectors":[{"id":1021615,"ref":null,"power":22,"standard":"IEC_62196_T2","is_remote_capable":false,"is_attached":true,"created_at":"2025-04-01T17:13:39.000000Z","updated_at":"2025-04-01T17:13:39.000000Z","tariff_id":3001,"evse_id":936865,"tariff":{"id":3001,"name":"Interop sortante - Lidl Rapide (20)","description":"{\"de\": \"0.51 \u20ac / kWh started.\", \"en\": \"0.51 \u20ac / kWh started.\", \"es\": \"0.51 \u20ac / kWh started.\", \"fr\": \"0,51 \u20ac / kWh entam\u00e9.\", \"nl\": \"0.51 \u20ac / kWh started.\"}","is_free":false,"currency":"EUR","provision":{"amount":50,"currency":"EUR"},"payment_authorization_amount":{"amount":50,"currency":"EUR"},"max_price":{"amount":166.67,"currency":"EUR"},"custom_ref":"lidl-rapide-interop","is_hidden":false,"is_preferential":false,"origin_ref":"lidl-rapide-interop","commissioned_at":null}}]},{"id":936866,"custom_ref":"OE2G1I1","status":"AVAILABLE","is_available":true,"is_remote_capable":false,"created_at":"2025-04-01T17:13:39.000000Z","updated_at":"2026-06-11T13:24:28.000000Z","location_id":829320,"connectors":[{"id":1021616,"ref":null,"power":4,"standard":"DOMESTIC_F","is_remote_capable":false,"is_attached":true,"created_at":"2025-04-01T17:13:39.000000Z","updated_at":"2025-04-01T17:13:39.000000Z","tariff_id":3001,"evse_id":936866,"tariff":{"id":3001,"name":"Interop sortante - Lidl Rapide (20)","description":"{\"de\": \"0.51 \u20ac / kWh started.\", \"en\": \"0.51 \u20ac / kWh started.\", \"es\": \"0.51 \u20ac / kWh started.\", \"fr\": \"0,51 \u20ac / kWh entam\u00e9.\", \"nl\": \"0.51 \u20ac / kWh started.\"}","is_free":false,"currency":"EUR","provision":{"amount":50,"currency":"EUR"},"payment_authorization_amount":{"amount":50,"currency":"EUR"},"max_price":{"amount":166.67,"currency":"EUR"},"custom_ref":"lidl-rapide-interop","is_hidden":false,"is_preferential":false,"origin_ref":"lidl-rapide-interop","commissioned_at":null}},{"id":1021617,"ref":null,"power":22,"standard":"IEC_62196_T2","is_remote_capable":false,"is_attached":true,"created_at":"2025-04-01T17:13:39.000000Z","updated_at":"2025-04-01T17:13:39.000000Z","tariff_id":3001,"evse_id":936866,"tariff":{"id":3001,"name":"Interop sortante - Lidl Rapide (20)","description":"{\"de\": \"0.51 \u20ac / kWh started.\", \"en\": \"0.51 \u20ac / kWh started.\", \"es\": \"0.51 \u20ac / kWh started.\", \"fr\": \"0,51 \u20ac / kWh entam\u00e9.\", \"nl\": \"0.51 \u20ac / kWh started.\"}","is_free":false,"currency":"EUR","provision":{"amount":50,"currency":"EUR"},"payment_authorization_amount":{"amount":50,"currency":"EUR"},"max_price":{"amount":166.67,"currency":"EUR"},"custom_ref":"lidl-rapide-interop","is_hidden":false,"is_preferential":false,"origin_ref":"lidl-rapide-interop","commissioned_at":null}}]},{"id":936867,"custom_ref":"OSKSQ51","status":"AVAILABLE","is_available":true,"is_remote_capable":false,"created_at":"2025-04-01T17:13:39.000000Z","updated_at":"2026-06-11T13:24:28.000000Z","location_id":829320,"connectors":[{"id":1021618,"ref":null,"power":4,"standard":"DOMESTIC_F","is_remote_capable":false,"is_attached":true,"created_at":"2025-04-01T17:13:39.000000Z","updated_at":"2025-04-01T17:13:39.000000Z","tariff_id":3001,"evse_id":936867,"tariff":{"id":3001,"name":"Interop sortante - Lidl Rapide (20)","description":"{\"de\": \"0.51 \u20ac / kWh started.\", \"en\": \"0.51 \u20ac / kWh started.\", \"es\": \"0.51 \u20ac / kWh started.\", \"fr\": \"0,51 \u20ac / kWh entam\u00e9.\", \"nl\": \"0.51 \u20ac / kWh started.\"}","is_free":false,"currency":"EUR","provision":{"amount":50,"currency":"EUR"},"payment_authorization_amount":{"amount":50,"currency":"EUR"},"max_price":{"amount":166.67,"currency":"EUR"},"custom_ref":"lidl-rapide-interop","is_hidden":false,"is_preferential":false,"origin_ref":"lidl-rapide-interop","commissioned_at":null}},{"id":1021619,"ref":null,"power":22,"standard":"IEC_62196_T2","is_remote_capable":false,"is_attached":true,"created_at":"2025-04-01T17:13:39.000000Z","updated_at":"2025-04-01T17:13:39.000000Z","tariff_id":3001,"evse_id":936867,"tariff":{"id":3001,"name":"Interop sortante - Lidl Rapide (20)","description":"{\"de\": \"0.51 \u20ac / kWh started.\", \"en\": \"0.51 \u20ac / kWh started.\", \"es\": \"0.51 \u20ac / kWh started.\", \"fr\": \"0,51 \u20ac / kWh entam\u00e9.\", \"nl\": \"0.51 \u20ac / kWh started.\"}","is_free":false,"currency":"EUR","provision":{"amount":50,"currency":"EUR"},"payment_authorization_amount":{"amount":50,"currency":"EUR"},"max_price":{"amount":166.67,"currency":"EUR"},"custom_ref":"lidl-rapide-interop","is_hidden":false,"is_preferential":false,"origin_ref":"lidl-rapide-interop","commissioned_at":null}}]}],"evses_available_count":4,"evses_total_count":4,"evses_capabilities":["RFID_READER"],"connectors":{"best_power":{"category":"fast","kw":24},"types":["IEC_62196_T2_COMBO","IEC_62196_T2","DOMESTIC_F"]},"img_preview_url":"https://maps.googleapis.com/maps/api/streetview?size=640x640&location=45.9078,6.13666&key=AIzaSyBTzO4i4vUlvTN98zgI1ae8fMPVHXVZ-Uk","hotline":{"phone_number":null,"schedule":[]},"related_refs":["FREBNBNMAK1","FREBNBNMAK2","FREBNBTHPN2"],"created_at":"2025-04-01T17:13:39.000000Z","updated_at":"2026-01-26T15:17:01.000000Z"}}`

// TestNormalizeFreshmileRealPayloadMixedConnectorKinds pins the fix for a
// real production bug: a station whose best_power.category is
// "fast"/"superfast" (reported for the WHOLE location, driven by its
// fastest connector) used to force EVERY connector there to Kind=dc,
// including a plain Type 2 AC socket and a 4kW domestic plug — because
// freshmileTariffKind (then just power-based) never consulted the
// connector's own standard at all. This fixture is a real /locations/{id}
// response (production, captured 2026-07-21) for a site with exactly that
// shape: one CCS connector (best_power.category "fast", driven by its
// 24kW) plus several Type 2 (22kW) and domestic (4kW) connectors — both
// genuinely AC. Before the fix, all three ended up Kind=dc, and the T2/
// domestic tariffs (0,51€/kWh) could silently outcompete the CCS
// connector's own, higher, genuinely-DC price (0,70€/kWh) in a station's
// displayed DC price aggregate.
func TestNormalizeFreshmileRealPayloadMixedConnectorKinds(t *testing.T) {
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(realFreshmileAnnecyMixedConnectorsPayload), &envelope); err != nil {
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
	if src.SourceStationID != "FREBNBTHPN1" {
		t.Errorf("SourceStationID = %q, want FREBNBTHPN1", src.SourceStationID)
	}

	tariffs := normalizeFreshmileTariffs(details)
	if len(tariffs) != 3 {
		t.Fatalf("got %d tariffs, want 3 (CCS/dc, T2/ac, EF/ac) — got %+v", len(tariffs), tariffs)
	}

	byKey := map[string]domain.StationTariff{}
	for _, tf := range tariffs {
		byKey[tf.Kind+"/"+tf.ConnectorType] = tf
	}

	ccs, ok := byKey[domain.TariffKindDC+"/"+domain.ConnectorTypeCCS]
	if !ok {
		t.Fatalf("missing dc/CCS tariff, got %+v", byKey)
	}
	if ccs.EnergyPriceCentsPerKWh == nil || *ccs.EnergyPriceCentsPerKWh != 70.0 {
		t.Errorf("CCS price = %v, want 70.0 (0,70€/kWh)", ccs.EnergyPriceCentsPerKWh)
	}

	t2, ok := byKey[domain.TariffKindAC+"/"+domain.ConnectorTypeT2]
	if !ok {
		t.Fatalf("missing ac/T2 tariff (T2 must NOT be forced to dc by the station's fast CCS connector), got %+v", byKey)
	}
	if t2.EnergyPriceCentsPerKWh == nil || *t2.EnergyPriceCentsPerKWh != 51.0 {
		t.Errorf("T2 price = %v, want 51.0 (0,51€/kWh)", t2.EnergyPriceCentsPerKWh)
	}

	ef, ok := byKey[domain.TariffKindAC+"/"+domain.ConnectorTypeEF]
	if !ok {
		t.Fatalf("missing ac/EF tariff (DOMESTIC_F must map to EF and NOT be forced to dc), got %+v", byKey)
	}
	if ef.EnergyPriceCentsPerKWh == nil || *ef.EnergyPriceCentsPerKWh != 51.0 {
		t.Errorf("DOMESTIC_F (EF) price = %v, want 51.0 (0,51€/kWh)", ef.EnergyPriceCentsPerKWh)
	}

	// The location-level id/img_preview_url must land in every connector's
	// own tariff Extra (see normalizeFreshmileTariffs' doc comment) — this
	// site's three tariffs correlate to three different IRVE station rows
	// (one per connector kind), so there's no single shared place to store
	// them other than duplicating across each connector's own tariff.
	for key, tf := range byKey {
		if got := tf.Extra["freshmile_location_id"]; got != int64(829320) {
			t.Errorf("%s: freshmile_location_id = %v (%T), want int64(829320)", key, got, got)
		}
		wantImg := "https://maps.googleapis.com/maps/api/streetview?size=640x640&location=45.9078,6.13666&key=AIzaSyBTzO4i4vUlvTN98zgI1ae8fMPVHXVZ-Uk"
		if got := tf.Extra["img_preview_url"]; got != wantImg {
			t.Errorf("%s: img_preview_url = %v, want %v", key, got, wantImg)
		}
	}
}
