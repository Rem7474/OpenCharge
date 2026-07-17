package ingestion

import (
	"testing"

	"opencharge/internal/domain"
)

func TestIsOpenSupercharger(t *testing.T) {
	cases := []struct {
		name string
		loc  map[string]any
		want bool
	}{
		{
			name: "open supercharger",
			loc: map[string]any{
				"location_type":         []any{"supercharger"},
				"supercharger_function": map[string]any{"site_status": "open", "project_status": "Open"},
				"location_url_slug":     "29508",
			},
			want: true,
		},
		{
			name: "non-supercharger location type",
			loc:  map[string]any{"location_type": []any{"sales", "service"}},
			want: false,
		},
		{
			name: "coming soon supercharger is not an exact 'supercharger' type",
			loc:  map[string]any{"location_type": []any{"coming_soon_supercharger"}},
			want: false,
		},
		{
			name: "supercharger with closed site_status",
			loc: map[string]any{
				"location_type":         []any{"supercharger"},
				"supercharger_function": map[string]any{"site_status": "closed"},
			},
			want: false,
		},
		{
			name: "supercharger with non-open project_status",
			loc: map[string]any{
				"location_type":         []any{"supercharger"},
				"supercharger_function": map[string]any{"project_status": "Permitting"},
			},
			want: false,
		},
		{
			name: "supercharger with no status info is kept",
			loc:  map[string]any{"location_type": []any{"supercharger"}},
			want: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isOpenSupercharger(c.loc); got != c.want {
				t.Errorf("isOpenSupercharger(%+v) = %v, want %v", c.loc, got, c.want)
			}
		})
	}
}

func TestNormalizeTeslaStation(t *testing.T) {
	details := map[string]any{
		"name":           "Annecy, France",
		"commonSiteName": "La Galerie",
		"address": map[string]any{
			"street":      "1 rue du lac",
			"city":        "Seynod",
			"postalCode":  "74600",
			"countryCode": "fr",
		},
		"entryPoint": map[string]any{"latitude": 45.899247, "longitude": 6.129384},
		"centroid":   map[string]any{"latitude": 45.9, "longitude": 6.13},
	}

	src, ok := normalizeTeslaStation("29508", details)
	if !ok {
		t.Fatal("normalizeTeslaStation returned ok=false, want true")
	}
	if src.Source != "tesla" || src.SourceStationID != "29508" {
		t.Errorf("unexpected source station: %+v", src)
	}
	if src.OperatorName != "Tesla" {
		t.Errorf("OperatorName = %q, want Tesla", src.OperatorName)
	}
	if src.Name != "Annecy, France" {
		t.Errorf("Name = %q, want %q (details.name takes priority over commonSiteName)", src.Name, "Annecy, France")
	}
	if src.Lat != 45.899247 || src.Lng != 6.129384 {
		t.Errorf("unexpected location: (%v, %v), want entryPoint (45.899247, 6.129384)", src.Lat, src.Lng)
	}
	if src.AddressCity != "Seynod" || src.AddressPostal != "74600" || src.AddressCountry != "FR" {
		t.Errorf("unexpected address: %+v", src)
	}
}

func TestNormalizeTeslaStationFallsBackToCentroid(t *testing.T) {
	details := map[string]any{
		"commonSiteName": "Fallback Site",
		"centroid":       map[string]any{"latitude": 48.0, "longitude": 2.0},
	}
	src, ok := normalizeTeslaStation("slug-1", details)
	if !ok {
		t.Fatal("normalizeTeslaStation returned ok=false, want true")
	}
	if src.Lat != 48.0 || src.Lng != 2.0 {
		t.Errorf("unexpected fallback location: (%v, %v)", src.Lat, src.Lng)
	}
	if src.Name != "Fallback Site" {
		t.Errorf("Name = %q, want commonSiteName fallback %q", src.Name, "Fallback Site")
	}
}

func TestNormalizeTeslaStationNoLocation(t *testing.T) {
	if _, ok := normalizeTeslaStation("slug-2", map[string]any{"name": "No coords"}); ok {
		t.Error("normalizeTeslaStation returned ok=true for a station without any location")
	}
}

func TestNormalizeTeslaTariffsFourPlans(t *testing.T) {
	details := map[string]any{
		"effectivePricebooks": []any{
			map[string]any{"feeType": "CHARGING", "currencyCode": "EUR", "uom": "kwh", "rateBase": 0.50, "vehicleMakeType": "TSLA", "isMemberPricebook": true},
			map[string]any{"feeType": "CHARGING", "currencyCode": "EUR", "uom": "kwh", "rateBase": 0.55, "vehicleMakeType": "TSLA", "isMemberPricebook": false},
			map[string]any{"feeType": "CHARGING", "currencyCode": "EUR", "uom": "kwh", "rateBase": 0.60, "vehicleMakeType": "NTSLA", "isMemberPricebook": true},
			map[string]any{"feeType": "CHARGING", "currencyCode": "EUR", "uom": "kwh", "rateBase": 0.65, "vehicleMakeType": "NTSLA", "isMemberPricebook": false},
		},
	}

	tariffs := normalizeTeslaTariffs(details)
	if len(tariffs) != 4 {
		t.Fatalf("got %d tariffs, want 4", len(tariffs))
	}

	byPlan := map[string]domain.StationTariff{}
	for _, tariff := range tariffs {
		if tariff.Kind != domain.TariffKindDC {
			t.Errorf("tariff %s: Kind = %q, want dc", tariff.Plan, tariff.Kind)
		}
		if tariff.Source != "tesla" {
			t.Errorf("tariff %s: Source = %q, want tesla", tariff.Plan, tariff.Source)
		}
		byPlan[tariff.Plan] = tariff
	}

	wantPrices := map[string]float64{
		"tesla_member":     50.0,
		"tesla_public":     55.0,
		"non_tesla_member": 60.0,
		"non_tesla_public": 65.0,
	}
	for plan, wantCents := range wantPrices {
		tariff, ok := byPlan[plan]
		if !ok {
			t.Fatalf("missing plan %q", plan)
		}
		if tariff.EnergyPriceCentsPerKWh == nil || *tariff.EnergyPriceCentsPerKWh != wantCents {
			t.Errorf("%s.EnergyPriceCentsPerKWh = %v, want %v", plan, tariff.EnergyPriceCentsPerKWh, wantCents)
		}
	}
}

func TestNormalizeTeslaTariffsFreeUom(t *testing.T) {
	details := map[string]any{
		"effectivePricebooks": []any{
			map[string]any{"feeType": "CHARGING", "currencyCode": "EUR", "uom": "free", "vehicleMakeType": "TSLA", "isMemberPricebook": true},
		},
	}
	tariffs := normalizeTeslaTariffs(details)
	if len(tariffs) != 1 {
		t.Fatalf("got %d tariffs, want 1", len(tariffs))
	}
	if tariffs[0].EnergyPriceCentsPerKWh == nil || *tariffs[0].EnergyPriceCentsPerKWh != 0 {
		t.Errorf("EnergyPriceCentsPerKWh = %v, want 0 (free)", tariffs[0].EnergyPriceCentsPerKWh)
	}
}

func TestNormalizeTeslaTariffsParkingFeeMergedIntoCharging(t *testing.T) {
	details := map[string]any{
		"effectivePricebooks": []any{
			map[string]any{"feeType": "CHARGING", "currencyCode": "EUR", "uom": "kwh", "rateBase": 0.50, "vehicleMakeType": "TSLA", "isMemberPricebook": true},
			map[string]any{"feeType": "PARKING", "currencyCode": "EUR", "uom": "min", "rateBase": 0.10, "vehicleMakeType": "TSLA", "isMemberPricebook": true},
			// PARKING-only pair (no matching CHARGING entry): must not
			// produce a standalone tariff row.
			map[string]any{"feeType": "PARKING", "currencyCode": "EUR", "uom": "min", "rateBase": 0.20, "vehicleMakeType": "NTSLA", "isMemberPricebook": false},
		},
	}
	tariffs := normalizeTeslaTariffs(details)
	if len(tariffs) != 1 {
		t.Fatalf("got %d tariffs, want 1 (PARKING-only pair skipped)", len(tariffs))
	}
	tariff := tariffs[0]
	if tariff.Plan != "tesla_member" {
		t.Errorf("Plan = %q, want tesla_member", tariff.Plan)
	}
	if tariff.EnergyPriceCentsPerKWh == nil || *tariff.EnergyPriceCentsPerKWh != 50.0 {
		t.Errorf("EnergyPriceCentsPerKWh = %v, want 50.0", tariff.EnergyPriceCentsPerKWh)
	}
	if tariff.CongestionPriceCentsPerMin == nil || *tariff.CongestionPriceCentsPerMin != 10.0 {
		t.Errorf("CongestionPriceCentsPerMin = %v, want 10.0 (from the matching PARKING entry)", tariff.CongestionPriceCentsPerMin)
	}
	if _, hasParking := tariff.Extra["parking"]; !hasParking {
		t.Error("Extra[\"parking\"] missing, want the raw PARKING pricebook entry preserved")
	}
	if _, hasCharging := tariff.Extra["charging"]; !hasCharging {
		t.Error("Extra[\"charging\"] missing, want the raw CHARGING pricebook entry preserved")
	}
}

func TestNormalizeTeslaTariffsNoPricebooks(t *testing.T) {
	if got := normalizeTeslaTariffs(map[string]any{}); got != nil {
		t.Errorf("normalizeTeslaTariffs({}) = %v, want nil", got)
	}
}
