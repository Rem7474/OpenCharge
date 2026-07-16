package ingestion

import (
	"testing"

	"opencharge/internal/domain"
)

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
	if got := normalizeIziviaTariffs(nil); got != nil {
		t.Errorf("normalizeIziviaTariffs(nil) = %v, want nil", got)
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
	tariffs := normalizeIziviaTariffs(pricing)
	if len(tariffs) != 1 {
		t.Fatalf("got %d tariffs, want 1", len(tariffs))
	}
	if tariffs[0].Kind != domain.TariffKindMixed {
		t.Errorf("Kind = %q, want mixed", tariffs[0].Kind)
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
