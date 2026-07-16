package ingestion

import "testing"

func TestNormalizeIRVEFeature(t *testing.T) {
	feature := geoJSONFeature{
		Geometry: geoJSONGeometry{Type: "Point", Coordinates: []float64{6.1213, 45.9123}},
		Properties: map[string]any{
			"id_pdc_itinerance":     "FRIRVEPDC123",
			"id_station_itinerance": "FRIRVESTA456",
			"nom_operateur":         "Izivia",
			"nom_station":           "Station Annecy",
			"puissance_nominale":    "150",
			"prise_type_combo_ccs":  "true",
			"gratuit":               "false",
			"paiement_cb":           "true",
			"horaires":              "24/7",
		},
	}

	station, ok := normalizeIRVEFeature(feature)
	if !ok {
		t.Fatal("normalizeIRVEFeature returned ok=false, want true")
	}
	if station.IRVEIDPDC != "FRIRVEPDC123" {
		t.Errorf("IRVEIDPDC = %q, want FRIRVEPDC123", station.IRVEIDPDC)
	}
	if station.Lat != 45.9123 || station.Lng != 6.1213 {
		t.Errorf("location = (%v, %v), want (45.9123, 6.1213)", station.Lat, station.Lng)
	}
	if station.ConnectorType != "CCS" {
		t.Errorf("ConnectorType = %q, want CCS", station.ConnectorType)
	}
	if station.AccessType != "paid" {
		t.Errorf("AccessType = %q, want paid", station.AccessType)
	}
	if !station.Is24_7 {
		t.Error("Is24_7 = false, want true")
	}
	if station.PowerKW == nil || *station.PowerKW != 150 {
		t.Errorf("PowerKW = %v, want 150", station.PowerKW)
	}
}

func TestNormalizeIRVEFeatureMissingID(t *testing.T) {
	feature := geoJSONFeature{
		Geometry:   geoJSONGeometry{Type: "Point", Coordinates: []float64{6.1213, 45.9123}},
		Properties: map[string]any{},
	}
	if _, ok := normalizeIRVEFeature(feature); ok {
		t.Error("normalizeIRVEFeature returned ok=true for a feature without id_pdc_itinerance")
	}
}
