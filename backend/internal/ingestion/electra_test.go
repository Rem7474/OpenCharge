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

	if len(tariffs) != 1 {
		t.Fatalf("got %d tariffs, want 1", len(tariffs))
	}
	tariff := tariffs[0]
	if tariff.Kind != domain.TariffKindDC {
		t.Errorf("Kind = %q, want dc", tariff.Kind)
	}
	if tariff.EnergyPriceCentsPerKWh == nil || *tariff.EnergyPriceCentsPerKWh != 48.0 {
		t.Errorf("EnergyPriceCentsPerKWh = %v, want 48.0", tariff.EnergyPriceCentsPerKWh)
	}
	if tariff.CongestionPriceCentsPerMin == nil || *tariff.CongestionPriceCentsPerMin != 12.0 {
		t.Errorf("CongestionPriceCentsPerMin = %v, want 12.0", tariff.CongestionPriceCentsPerMin)
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
