package repository

import (
	"context"
	"testing"

	"opencharge/internal/domain"
)

func testStation(irveIDPDC string, lat, lng float64) domain.Station {
	power := 50.0
	return domain.Station{
		IRVEIDPDC:      irveIDPDC,
		OperatorName:   "Izivia",
		Enseigne:       "Izivia",
		Name:           "Station " + irveIDPDC,
		AddressCity:    "Annecy",
		AddressCountry: "FR",
		Lat:            lat,
		Lng:            lng,
		PowerKW:        &power,
		ConnectorType:  "CCS",
		AccessType:     "paid",
		Metadata:       map[string]any{"raw_field": "value"},
	}
}

func TestStationRepository_UpsertAndGet(t *testing.T) {
	pool := setupTestPool(t)
	ctx := context.Background()
	repo := NewStationRepository(pool)

	station := testStation("FRTEST0001", 45.9, 6.1)
	id, err := repo.UpsertStation(ctx, station)
	if err != nil {
		t.Fatalf("UpsertStation: %v", err)
	}
	if id.String() == "" {
		t.Fatal("UpsertStation returned a zero UUID")
	}

	got, err := repo.GetByIRVEID(ctx, "FRTEST0001")
	if err != nil {
		t.Fatalf("GetByIRVEID: %v", err)
	}
	if got == nil {
		t.Fatal("GetByIRVEID returned nil, want the upserted station")
	}
	if got.Name != "Station FRTEST0001" || got.OperatorName != "Izivia" {
		t.Errorf("unexpected station: %+v", got)
	}
	if got.Lat != 45.9 || got.Lng != 6.1 {
		t.Errorf("unexpected location: (%v, %v)", got.Lat, got.Lng)
	}
	if got.Metadata["raw_field"] != "value" {
		t.Errorf("Metadata = %v, want raw_field=value", got.Metadata)
	}

	// Upserting again with the same irve_id_pdc must update in place, not
	// create a duplicate row.
	updated := testStation("FRTEST0001", 45.9, 6.1)
	updated.Name = "Station Renamed"
	updatedID, err := repo.UpsertStation(ctx, updated)
	if err != nil {
		t.Fatalf("UpsertStation (update): %v", err)
	}
	if updatedID != id {
		t.Errorf("UpsertStation changed the id on update: got %v, want %v", updatedID, id)
	}
	got, err = repo.GetByIRVEID(ctx, "FRTEST0001")
	if err != nil {
		t.Fatalf("GetByIRVEID after update: %v", err)
	}
	if got.Name != "Station Renamed" {
		t.Errorf("Name = %q, want %q", got.Name, "Station Renamed")
	}
}

func TestStationRepository_BulkUpsertStations(t *testing.T) {
	pool := setupTestPool(t)
	ctx := context.Background()
	repo := NewStationRepository(pool)

	stations := []domain.Station{
		testStation("FRBULK0001", 45.90, 6.10),
		testStation("FRBULK0002", 45.91, 6.11),
		testStation("FRBULK0003", 45.92, 6.12),
	}
	if err := repo.BulkUpsertStations(ctx, stations); err != nil {
		t.Fatalf("BulkUpsertStations: %v", err)
	}

	for _, s := range stations {
		got, err := repo.GetByIRVEID(ctx, s.IRVEIDPDC)
		if err != nil {
			t.Fatalf("GetByIRVEID(%s): %v", s.IRVEIDPDC, err)
		}
		if got == nil {
			t.Fatalf("GetByIRVEID(%s) = nil, want the bulk-upserted station", s.IRVEIDPDC)
		}
	}

	// Re-upserting the same rows (e.g. a re-ingestion run) must update in
	// place, not create duplicates or error out.
	renamed := stations
	renamed[0].Name = "Renamed via bulk upsert"
	if err := repo.BulkUpsertStations(ctx, renamed); err != nil {
		t.Fatalf("BulkUpsertStations (update): %v", err)
	}
	got, err := repo.GetByIRVEID(ctx, "FRBULK0001")
	if err != nil {
		t.Fatalf("GetByIRVEID after update: %v", err)
	}
	if got.Name != "Renamed via bulk upsert" {
		t.Errorf("Name = %q, want %q", got.Name, "Renamed via bulk upsert")
	}
}

func TestStationRepository_BulkUpsertStations_Empty(t *testing.T) {
	pool := setupTestPool(t)
	repo := NewStationRepository(pool)
	if err := repo.BulkUpsertStations(context.Background(), nil); err != nil {
		t.Errorf("BulkUpsertStations(nil) = %v, want no error", err)
	}
}

func TestStationRepository_GetByIRVEID_NotFound(t *testing.T) {
	pool := setupTestPool(t)
	repo := NewStationRepository(pool)

	got, err := repo.GetByIRVEID(context.Background(), "does-not-exist")
	if err != nil {
		t.Fatalf("GetByIRVEID: %v", err)
	}
	if got != nil {
		t.Errorf("GetByIRVEID = %+v, want nil", got)
	}
}

func TestStationRepository_ListByBBox(t *testing.T) {
	pool := setupTestPool(t)
	ctx := context.Background()
	repo := NewStationRepository(pool)

	// Inside the bbox we query below.
	insideA := testStation("FRBBOX0001", 45.90, 6.10)
	insideA.OperatorName = "Izivia"
	insideB := testStation("FRBBOX0002", 45.91, 6.12)
	insideB.OperatorName = "Electra"
	// Far outside.
	outside := testStation("FRBBOX0003", 48.85, 2.35)

	for _, s := range []domain.Station{insideA, insideB, outside} {
		if _, err := repo.UpsertStation(ctx, s); err != nil {
			t.Fatalf("UpsertStation(%s): %v", s.IRVEIDPDC, err)
		}
	}

	bbox := domain.StationFilter{MinLng: 6.0, MinLat: 45.8, MaxLng: 6.3, MaxLat: 46.0}
	results, err := repo.ListByBBox(ctx, bbox)
	if err != nil {
		t.Fatalf("ListByBBox: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d stations in bbox, want 2", len(results))
	}
	ids := map[string]bool{}
	for _, r := range results {
		ids[r.Station.IRVEIDPDC] = true
	}
	if !ids["FRBBOX0001"] || !ids["FRBBOX0002"] {
		t.Errorf("unexpected stations in bbox result: %v", ids)
	}

	// Operator filter narrows it down to one.
	withOperator := bbox
	withOperator.Operator = "Electra"
	results, err = repo.ListByBBox(ctx, withOperator)
	if err != nil {
		t.Fatalf("ListByBBox with operator filter: %v", err)
	}
	if len(results) != 1 || results[0].Station.IRVEIDPDC != "FRBBOX0002" {
		t.Errorf("operator filter returned %+v, want only FRBBOX0002", results)
	}

	// Outside the bbox, nothing should come back.
	farAway := domain.StationFilter{MinLng: 2.2, MinLat: 48.8, MaxLng: 2.2001, MaxLat: 48.8001}
	results, err = repo.ListByBBox(ctx, farAway)
	if err != nil {
		t.Fatalf("ListByBBox far away: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("got %d stations far from any inserted point, want 0", len(results))
	}
}

func TestStationRepository_ListByBBox_HasTariffs(t *testing.T) {
	pool := setupTestPool(t)
	ctx := context.Background()
	stationRepo := NewStationRepository(pool)
	tariffRepo := NewTariffRepository(pool)

	withTariff := testStation("FRTARIFF01", 45.90, 6.10)
	withoutTariff := testStation("FRTARIFF02", 45.91, 6.11)

	idWith, err := stationRepo.UpsertStation(ctx, withTariff)
	if err != nil {
		t.Fatalf("UpsertStation: %v", err)
	}
	if _, err := stationRepo.UpsertStation(ctx, withoutTariff); err != nil {
		t.Fatalf("UpsertStation: %v", err)
	}

	price := 45.0
	if err := tariffRepo.Upsert(ctx, domain.StationTariff{
		StationID:              idWith,
		Source:                 "izivia",
		Kind:                   domain.TariffKindMixed,
		Model:                  "izivia_text",
		Currency:               "EUR",
		EnergyPriceCentsPerKWh: &price,
	}); err != nil {
		t.Fatalf("Tariffs.Upsert: %v", err)
	}

	bbox := domain.StationFilter{MinLng: 6.0, MinLat: 45.8, MaxLng: 6.3, MaxLat: 46.0}
	hasTariffs := true
	bbox.HasTariffs = &hasTariffs

	results, err := stationRepo.ListByBBox(ctx, bbox)
	if err != nil {
		t.Fatalf("ListByBBox hasTariffs: %v", err)
	}
	if len(results) != 1 || results[0].Station.IRVEIDPDC != "FRTARIFF01" {
		t.Fatalf("ListByBBox hasTariffs = %+v, want only FRTARIFF01", results)
	}
	if !results[0].HasTariffs {
		t.Error("HasTariffs = false, want true")
	}
	if len(results[0].TariffSources) != 1 || results[0].TariffSources[0] != "izivia" {
		t.Errorf("TariffSources = %v, want [izivia]", results[0].TariffSources)
	}
}

func TestStationRepository_ListByBBox_MixedKindFeedsBothACAndDC(t *testing.T) {
	pool := setupTestPool(t)
	ctx := context.Background()
	stationRepo := NewStationRepository(pool)
	tariffRepo := NewTariffRepository(pool)

	// A single-price source (Izivia free text with no power figure) stores a
	// "mixed" tariff. It must surface as BOTH the AC and DC minimum so the map
	// can price the marker regardless of the station's own connector kind —
	// this is the regression that grayed out Izivia stations even though their
	// tariff showed in the detail view.
	station := testStation("FRMIXED001", 45.90, 6.10)
	id, err := stationRepo.UpsertStation(ctx, station)
	if err != nil {
		t.Fatalf("UpsertStation: %v", err)
	}
	price := 39.1
	if err := tariffRepo.Upsert(ctx, domain.StationTariff{
		StationID: id, Source: "izivia", Kind: domain.TariffKindMixed,
		Model: "izivia_text", Currency: "EUR", EnergyPriceCentsPerKWh: &price,
	}); err != nil {
		t.Fatalf("Tariffs.Upsert: %v", err)
	}

	bbox := domain.StationFilter{MinLng: 6.0, MinLat: 45.8, MaxLng: 6.3, MaxLat: 46.0}
	results, err := stationRepo.ListByBBox(ctx, bbox)
	if err != nil {
		t.Fatalf("ListByBBox: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d stations, want 1", len(results))
	}
	ps := results[0].PricingSummary
	if ps.ACMinCentsPerKWh == nil || *ps.ACMinCentsPerKWh != 39.1 {
		t.Errorf("ACMinCentsPerKWh = %v, want 39.1 (mixed tariff must feed AC)", ps.ACMinCentsPerKWh)
	}
	if ps.DCMinCentsPerKWh == nil || *ps.DCMinCentsPerKWh != 39.1 {
		t.Errorf("DCMinCentsPerKWh = %v, want 39.1 (mixed tariff must feed DC)", ps.DCMinCentsPerKWh)
	}

	// And it must feed SelectedSourcesPricing the same way when its
	// source:plan pair is selected.
	bbox.Sources = []string{"izivia:standard"}
	results, err = stationRepo.ListByBBox(ctx, bbox)
	if err != nil {
		t.Fatalf("ListByBBox with sources: %v", err)
	}
	sp := results[0].SelectedSourcesPricing
	if sp == nil || sp.ACMinCentsPerKWh == nil || *sp.ACMinCentsPerKWh != 39.1 || sp.DCMinCentsPerKWh == nil || *sp.DCMinCentsPerKWh != 39.1 {
		t.Errorf("SelectedSourcesPricing = %+v, want AC=DC=39.1 for selected mixed izivia tariff", sp)
	}
}

func TestStationRepository_ListByBBox_SelectedSourcesPricing(t *testing.T) {
	pool := setupTestPool(t)
	ctx := context.Background()
	stationRepo := NewStationRepository(pool)
	tariffRepo := NewTariffRepository(pool)

	withIzivia := testStation("FRSRC0001", 45.90, 6.10)
	withElectra := testStation("FRSRC0002", 45.91, 6.11)

	idIzivia, err := stationRepo.UpsertStation(ctx, withIzivia)
	if err != nil {
		t.Fatalf("UpsertStation: %v", err)
	}
	idElectra, err := stationRepo.UpsertStation(ctx, withElectra)
	if err != nil {
		t.Fatalf("UpsertStation: %v", err)
	}

	iziviaPrice := 45.0
	if err := tariffRepo.Upsert(ctx, domain.StationTariff{
		StationID: idIzivia, Source: "izivia", Kind: domain.TariffKindMixed,
		Model: "izivia_text", Currency: "EUR", EnergyPriceCentsPerKWh: &iziviaPrice,
	}); err != nil {
		t.Fatalf("Tariffs.Upsert izivia: %v", err)
	}
	electraPrice := 48.0
	if err := tariffRepo.Upsert(ctx, domain.StationTariff{
		StationID: idElectra, Source: "electra", Kind: domain.TariffKindDC,
		Model: "electra_fixed", Currency: "EUR", EnergyPriceCentsPerKWh: &electraPrice,
	}); err != nil {
		t.Fatalf("Tariffs.Upsert electra: %v", err)
	}

	bbox := domain.StationFilter{MinLng: 6.0, MinLat: 45.8, MaxLng: 6.3, MaxLat: 46.0}

	// Without a Sources filter, SelectedSourcesPricing must stay nil — it's
	// an opt-in field, not always computed.
	results, err := stationRepo.ListByBBox(ctx, bbox)
	if err != nil {
		t.Fatalf("ListByBBox no sources: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d stations, want 2", len(results))
	}
	for _, r := range results {
		if r.SelectedSourcesPricing != nil {
			t.Errorf("station %s: SelectedSourcesPricing = %+v, want nil without a Sources filter", r.Station.IRVEIDPDC, r.SelectedSourcesPricing)
		}
	}

	// Selecting "electra:standard" must never drop the izivia-only station
	// (grayed out on the map, not hidden) but only the electra station gets
	// a SelectedSourcesPricing price. The repository deals in opaque
	// "source:plan" pairs; pairing them up is the API handler's job.
	bbox.Sources = []string{"electra:standard"}
	results, err = stationRepo.ListByBBox(ctx, bbox)
	if err != nil {
		t.Fatalf("ListByBBox sources=electra:standard: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d stations with Sources=[electra], want 2 (never filtered out)", len(results))
	}

	bySourceStation := map[string]domain.StationSummary{}
	for _, r := range results {
		bySourceStation[r.Station.IRVEIDPDC] = r
	}

	izivia := bySourceStation["FRSRC0001"]
	if izivia.SelectedSourcesPricing == nil {
		t.Fatal("izivia station: SelectedSourcesPricing = nil, want a non-nil (empty) pricing struct")
	}
	if izivia.SelectedSourcesPricing.ACMinCentsPerKWh != nil || izivia.SelectedSourcesPricing.DCMinCentsPerKWh != nil {
		t.Errorf("izivia station: SelectedSourcesPricing = %+v, want both nil (no electra tariff here)", izivia.SelectedSourcesPricing)
	}

	electra := bySourceStation["FRSRC0002"]
	if electra.SelectedSourcesPricing == nil {
		t.Fatal("electra station: SelectedSourcesPricing = nil, want a populated pricing struct")
	}
	if electra.SelectedSourcesPricing.DCMinCentsPerKWh == nil || *electra.SelectedSourcesPricing.DCMinCentsPerKWh != 48.0 {
		t.Errorf("electra station: DCMinCentsPerKWh = %v, want 48.0", electra.SelectedSourcesPricing.DCMinCentsPerKWh)
	}

	// Selecting a plan the station doesn't have a tariff for ("subscription"
	// vs the "standard" plan actually stored) must not match: the matching
	// is plan-aware, not just source-aware.
	bbox.Sources = []string{"electra:subscription"}
	results, err = stationRepo.ListByBBox(ctx, bbox)
	if err != nil {
		t.Fatalf("ListByBBox sources=electra:subscription: %v", err)
	}
	for _, r := range results {
		if r.Station.IRVEIDPDC == "FRSRC0002" && r.SelectedSourcesPricing != nil &&
			(r.SelectedSourcesPricing.ACMinCentsPerKWh != nil || r.SelectedSourcesPricing.DCMinCentsPerKWh != nil) {
			t.Errorf("electra station with sources=electra:subscription = %+v, want no price (only the standard plan is stored)", r.SelectedSourcesPricing)
		}
	}
}
