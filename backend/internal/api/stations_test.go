package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"opencharge/internal/domain"
)

func newRouter(h *StationsHandler) http.Handler {
	r := chi.NewRouter()
	r.Get("/stations", h.ListStations)
	r.Get("/stations/{id}", h.GetStation)
	r.Get("/sources", h.ListSources)
	return r
}

func seedStation(t *testing.T, h *StationsHandler, irveID string, lat, lng float64) domain.Station {
	t.Helper()
	power := 150.0
	station := domain.Station{
		IRVEIDPDC:      irveID,
		OperatorName:   "Izivia",
		Enseigne:       "Izivia",
		Name:           "Station " + irveID,
		AddressCity:    "Annecy",
		AddressCountry: "FR",
		Lat:            lat,
		Lng:            lng,
		PowerKW:        &power,
		ConnectorType:  "CCS",
		AccessType:     "paid",
		Metadata:       map[string]any{},
	}
	id, err := h.Stations.UpsertStation(context.Background(), station)
	if err != nil {
		t.Fatalf("seed station %s: %v", irveID, err)
	}
	station.ID = id
	return station
}

func TestListStations_RequiresBBox(t *testing.T) {
	h := setupHandler(t)
	router := newRouter(h)

	req := httptest.NewRequest(http.MethodGet, "/stations", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestListStations_RejectsMalformedBBox(t *testing.T) {
	h := setupHandler(t)
	router := newRouter(h)

	for _, bbox := range []string{"1,2,3", "a,b,c,d", ""} {
		req := httptest.NewRequest(http.MethodGet, "/stations?bbox="+bbox, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("bbox=%q: status = %d, want %d", bbox, rec.Code, http.StatusBadRequest)
		}
	}
}

func TestListStations_ReturnsStationsInBBox(t *testing.T) {
	h := setupHandler(t)
	seedStation(t, h, "FRAPI0001", 45.90, 6.10)
	seedStation(t, h, "FRAPI0002", 48.85, 2.35) // outside the bbox below
	router := newRouter(h)

	req := httptest.NewRequest(http.MethodGet, "/stations?bbox=6.0,45.8,6.3,46.0", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var items []stationListItemDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d stations, want 1: %+v", len(items), items)
	}
	if items[0].ID != "irve:FRAPI0001" {
		t.Errorf("ID = %q, want irve:FRAPI0001", items[0].ID)
	}
	if items[0].Location.Lat != 45.90 || items[0].Location.Lng != 6.10 {
		t.Errorf("Location = %+v, want (45.90, 6.10)", items[0].Location)
	}
	if len(items[0].Connectors) != 1 || items[0].Connectors[0].Kind != "CCS" {
		t.Errorf("Connectors = %+v, want a single CCS connector", items[0].Connectors)
	}
}

func TestListStations_FiltersByConnectorTypeAndMinPower(t *testing.T) {
	h := setupHandler(t)
	router := newRouter(h)
	ctx := context.Background()

	fastCCS := domain.Station{
		IRVEIDPDC: "FRAPICONN01", OperatorName: "Izivia", Name: "Fast CCS",
		AddressCountry: "FR", Lat: 45.90, Lng: 6.10,
		PowerKW: floatPtr(150), ConnectorType: "CCS", AccessType: "paid", Metadata: map[string]any{},
	}
	slowT2 := domain.Station{
		IRVEIDPDC: "FRAPICONN02", OperatorName: "Izivia", Name: "Slow T2",
		AddressCountry: "FR", Lat: 45.91, Lng: 6.11,
		PowerKW: floatPtr(22), ConnectorType: "T2", AccessType: "paid", Metadata: map[string]any{},
	}
	for _, s := range []domain.Station{fastCCS, slowT2} {
		if _, err := h.Stations.UpsertStation(ctx, s); err != nil {
			t.Fatalf("seed station %s: %v", s.IRVEIDPDC, err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/stations?bbox=6.0,45.8,6.3,46.0&connectorType=CCS", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var items []stationListItemDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(items) != 1 || items[0].ID != "irve:FRAPICONN01" {
		t.Errorf("connectorType=CCS returned %+v, want only FRAPICONN01", items)
	}

	req = httptest.NewRequest(http.MethodGet, "/stations?bbox=6.0,45.8,6.3,46.0&minPowerKw=50", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	items = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(items) != 1 || items[0].ID != "irve:FRAPICONN01" {
		t.Errorf("minPowerKw=50 returned %+v, want only FRAPICONN01", items)
	}
}

func floatPtr(v float64) *float64 { return &v }

func TestGetStation_NotFound(t *testing.T) {
	h := setupHandler(t)
	router := newRouter(h)

	req := httptest.NewRequest(http.MethodGet, "/stations/irve:does-not-exist", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestGetStation_HandlesPercentEncodedColon(t *testing.T) {
	h := setupHandler(t)
	seedStation(t, h, "FRAPI0004", 45.90, 6.10)
	router := newRouter(h)

	// Real browser clients build this URL via encodeURIComponent("irve:FRAPI0004"),
	// which percent-encodes the colon; chi does not decode route params, so
	// the handler must do it itself.
	req := httptest.NewRequest(http.MethodGet, "/stations/irve%3AFRAPI0004", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}

func TestGetStation_ReturnsStationWithTariffs(t *testing.T) {
	h := setupHandler(t)
	station := seedStation(t, h, "FRAPI0003", 45.90, 6.10)

	price := 45.0
	err := h.Tariffs.Upsert(context.Background(), domain.StationTariff{
		StationID:              station.ID,
		Source:                 "izivia",
		Kind:                   domain.TariffKindMixed,
		Model:                  "izivia_text",
		Currency:               "EUR",
		EnergyPriceCentsPerKWh: &price,
		RawText:                "0,45€/kWh",
		Extra:                  map[string]any{},
	})
	if err != nil {
		t.Fatalf("seed tariff: %v", err)
	}

	router := newRouter(h)
	req := httptest.NewRequest(http.MethodGet, "/stations/irve:FRAPI0003", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var resp stationDetailResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Station.ID != "irve:FRAPI0003" {
		t.Errorf("Station.ID = %q, want irve:FRAPI0003", resp.Station.ID)
	}
	if len(resp.Tariffs) != 1 {
		t.Fatalf("got %d tariffs, want 1", len(resp.Tariffs))
	}
	if resp.Tariffs[0].Source != "izivia" || resp.Tariffs[0].RawText != "0,45€/kWh" {
		t.Errorf("unexpected tariff: %+v", resp.Tariffs[0])
	}
}

func TestGetStation_ExposesIRVEMetadataFields(t *testing.T) {
	h := setupHandler(t)
	station := domain.Station{
		IRVEIDPDC: "FRAPIMETA01", OperatorName: "TotalEnergies", Name: "Paris | Rue Sorbier 40",
		AddressCountry: "FR", Lat: 48.865684, Lng: 2.392972,
		ConnectorType: "T2", AccessType: "paid",
		Metadata: map[string]any{
			"nbre_pdc":          "5",
			"accessibilite_pmr": "Non accessible",
			"cable_t2_attache":  "False",
			"horaires":          "24/7",
		},
	}
	if _, err := h.Stations.UpsertStation(context.Background(), station); err != nil {
		t.Fatalf("seed station: %v", err)
	}

	router := newRouter(h)
	req := httptest.NewRequest(http.MethodGet, "/stations/irve:FRAPIMETA01", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var resp stationDetailResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Station.PDCCount == nil || *resp.Station.PDCCount != 5 {
		t.Errorf("PDCCount = %v, want 5", resp.Station.PDCCount)
	}
	if resp.Station.AccessibilityPMR != "Non accessible" {
		t.Errorf("AccessibilityPMR = %q, want %q", resp.Station.AccessibilityPMR, "Non accessible")
	}
	if resp.Station.CableT2Attached == nil || *resp.Station.CableT2Attached != false {
		t.Errorf("CableT2Attached = %v, want a pointer to false (field present, explicitly False)", resp.Station.CableT2Attached)
	}
	if resp.Station.OpeningHours != "24/7" {
		t.Errorf("OpeningHours = %q, want %q", resp.Station.OpeningHours, "24/7")
	}
}

func TestGetStation_MissingIRVEMetadataFieldsOmitted(t *testing.T) {
	h := setupHandler(t)
	seedStation(t, h, "FRAPIMETA02", 45.90, 6.10) // seedStation sets Metadata: map[string]any{}

	router := newRouter(h)
	req := httptest.NewRequest(http.MethodGet, "/stations/irve:FRAPIMETA02", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var resp stationDetailResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Station.PDCCount != nil {
		t.Errorf("PDCCount = %v, want nil (field absent from metadata)", resp.Station.PDCCount)
	}
	if resp.Station.CableT2Attached != nil {
		t.Errorf("CableT2Attached = %v, want nil (field absent from metadata, distinct from an explicit false)", resp.Station.CableT2Attached)
	}
}

func TestListSources(t *testing.T) {
	h := setupHandler(t)
	station := seedStation(t, h, "FRAPI0005", 45.90, 6.10)

	price := 40.0
	seeds := []domain.StationTariff{
		{Source: "electra", Plan: "app", Kind: domain.TariffKindAC},
		{Source: "electra", Plan: "public", Kind: domain.TariffKindAC},
		{Source: "izivia", Plan: domain.TariffPlanStandard, Kind: domain.TariffKindMixed},
	}
	for _, tariff := range seeds {
		tariff.StationID = station.ID
		tariff.Model = "test"
		tariff.Currency = "EUR"
		tariff.EnergyPriceCentsPerKWh = &price
		if err := h.Tariffs.Upsert(context.Background(), tariff); err != nil {
			t.Fatalf("seed tariff %s/%s: %v", tariff.Source, tariff.Plan, err)
		}
	}

	router := newRouter(h)
	req := httptest.NewRequest(http.MethodGet, "/sources", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var sources []struct {
		ID    string   `json:"id"`
		Plans []string `json:"plans"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &sources); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(sources) != 2 {
		t.Fatalf("got %d sources, want 2: %+v", len(sources), sources)
	}
	if sources[0].ID != "electra" || len(sources[0].Plans) != 2 || sources[0].Plans[0] != "app" || sources[0].Plans[1] != "public" {
		t.Errorf("sources[0] = %+v, want electra with plans [app public]", sources[0])
	}
	if sources[1].ID != "izivia" || len(sources[1].Plans) != 1 || sources[1].Plans[0] != "standard" {
		t.Errorf("sources[1] = %+v, want izivia with plans [standard]", sources[1])
	}
}
