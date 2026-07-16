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

func TestListSources(t *testing.T) {
	h := setupHandler(t)
	station := seedStation(t, h, "FRAPI0005", 45.90, 6.10)

	price := 40.0
	for _, source := range []string{"electra", "izivia"} {
		err := h.Tariffs.Upsert(context.Background(), domain.StationTariff{
			StationID: station.ID, Source: source, Kind: domain.TariffKindAC,
			Model: "test", Currency: "EUR", EnergyPriceCentsPerKWh: &price,
		})
		if err != nil {
			t.Fatalf("seed tariff %s: %v", source, err)
		}
	}

	router := newRouter(h)
	req := httptest.NewRequest(http.MethodGet, "/sources", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var sources []string
	if err := json.Unmarshal(rec.Body.Bytes(), &sources); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(sources) != 2 || sources[0] != "electra" || sources[1] != "izivia" {
		t.Errorf("sources = %v, want [electra izivia]", sources)
	}
}
