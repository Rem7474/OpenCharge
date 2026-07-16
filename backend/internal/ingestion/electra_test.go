package ingestion

import (
	"testing"

	"opencharge/internal/domain"
)

func TestNormalizeElectraStation(t *testing.T) {
	raw := map[string]any{
		"id":           "elec-123",
		"name":         "Electra Paris Bercy",
		"latitude":     48.8,
		"longitude":    2.3,
		"address":      "1 rue de Bercy",
		"country_code": "fr",
		"pricings": map[string]any{
			"dc_combo": map[string]any{
				"currency": "EUR",
				"windows": []any{
					map[string]any{
						"start_time":                           "00:00",
						"end_time":                             "23:59",
						"energy_price_cents_per_kwh":           48.0,
						"session_duration_price_cents_per_min": 0.0,
						"congestion_price_cents_per_min":       12.0,
					},
				},
			},
		},
	}

	src, tariffs, ok := normalizeElectraStation(raw)
	if !ok {
		t.Fatal("normalizeElectraStation returned ok=false, want true")
	}
	if src.Source != "electra" || src.SourceStationID != "elec-123" {
		t.Errorf("unexpected source station: %+v", src)
	}
	if src.Lat != 48.8 || src.Lng != 2.3 {
		t.Errorf("unexpected location: (%v, %v)", src.Lat, src.Lng)
	}
	if src.AddressCountry != "FR" {
		t.Errorf("AddressCountry = %q, want FR", src.AddressCountry)
	}

	// One connector kind (dc_combo) now yields 3 plans: public, app, subscription.
	if len(tariffs) != 3 {
		t.Fatalf("got %d tariffs, want 3 (public/app/subscription)", len(tariffs))
	}
	byPlan := map[string]domain.StationTariff{}
	for _, tariff := range tariffs {
		if tariff.Kind != domain.TariffKindDC {
			t.Errorf("tariff %s: Kind = %q, want dc", tariff.Plan, tariff.Kind)
		}
		if tariff.CongestionPriceCentsPerMin == nil || *tariff.CongestionPriceCentsPerMin != 12.0 {
			t.Errorf("tariff %s: CongestionPriceCentsPerMin = %v, want 12.0", tariff.Plan, tariff.CongestionPriceCentsPerMin)
		}
		byPlan[tariff.Plan] = tariff
	}

	public, ok := byPlan["public"]
	if !ok {
		t.Fatal("missing public plan tariff")
	}
	if public.EnergyPriceCentsPerKWh == nil || *public.EnergyPriceCentsPerKWh != electraPublicPriceCentsPerKWh {
		t.Errorf("public.EnergyPriceCentsPerKWh = %v, want %v", public.EnergyPriceCentsPerKWh, electraPublicPriceCentsPerKWh)
	}

	app, ok := byPlan["app"]
	if !ok {
		t.Fatal("missing app plan tariff")
	}
	if app.EnergyPriceCentsPerKWh == nil || *app.EnergyPriceCentsPerKWh != 48.0 {
		t.Errorf("app.EnergyPriceCentsPerKWh = %v, want 48.0 (the scraped price)", app.EnergyPriceCentsPerKWh)
	}
	appWindows, _ := app.Extra["windows"].([]map[string]any)
	if len(appWindows) != 1 || appWindows[0]["energyPriceCentsPerKwh"] != 48.0 {
		t.Errorf("app.Extra[windows] = %+v, want a single window at 48.0", app.Extra["windows"])
	}

	subscription, ok := byPlan["subscription"]
	if !ok {
		t.Fatal("missing subscription plan tariff")
	}
	if subscription.EnergyPriceCentsPerKWh == nil || *subscription.EnergyPriceCentsPerKWh != 28.0 {
		t.Errorf("subscription.EnergyPriceCentsPerKWh = %v, want 28.0 (48.0 - 20cts)", subscription.EnergyPriceCentsPerKWh)
	}
}

func TestNormalizeElectraTariffsMultiWindow(t *testing.T) {
	pricing := map[string]any{
		"dc_combo": map[string]any{
			"currency": "EUR",
			"windows": []any{
				map[string]any{"start_time": "00:00", "end_time": "07:00", "energy_price_cents_per_kwh": 35.0},
				map[string]any{"start_time": "07:00", "end_time": "23:59", "energy_price_cents_per_kwh": 55.0},
			},
		},
	}

	tariffs := normalizeElectraTariffs(pricing)
	var app domain.StationTariff
	for _, t := range tariffs {
		if t.Plan == "app" {
			app = t
		}
	}
	if app.EnergyPriceCentsPerKWh == nil || *app.EnergyPriceCentsPerKWh != 35.0 {
		t.Errorf("app.EnergyPriceCentsPerKWh = %v, want 35.0 (cheapest window)", app.EnergyPriceCentsPerKWh)
	}
	windows, _ := app.Extra["windows"].([]map[string]any)
	if len(windows) != 2 {
		t.Fatalf("got %d windows, want 2", len(windows))
	}
	if windows[0]["energyPriceCentsPerKwh"] != 35.0 || windows[1]["energyPriceCentsPerKwh"] != 55.0 {
		t.Errorf("windows = %+v, want per-window prices preserved", windows)
	}
}

func TestNormalizeElectraStationMissingID(t *testing.T) {
	if _, _, ok := normalizeElectraStation(map[string]any{"latitude": 48.8, "longitude": 2.3}); ok {
		t.Error("normalizeElectraStation returned ok=true for a record without id/uuid")
	}
}

func TestNormalizeElectraStationMissingLocation(t *testing.T) {
	if _, _, ok := normalizeElectraStation(map[string]any{"id": "elec-1"}); ok {
		t.Error("normalizeElectraStation returned ok=true for a record without coordinates")
	}
}

func TestElectraKind(t *testing.T) {
	cases := map[string]string{
		"dc_combo":  domain.TariffKindDC,
		"combo_ccs": domain.TariffKindDC,
		"ac_type2":  domain.TariffKindAC,
		"unknown":   domain.TariffKindMixed,
	}
	for input, want := range cases {
		if got := electraKind(input); got != want {
			t.Errorf("electraKind(%q) = %q, want %q", input, got, want)
		}
	}
}
