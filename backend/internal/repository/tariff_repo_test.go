package repository

import (
	"context"
	"testing"

	"opencharge/internal/domain"
)

func TestTariffRepository_UpsertAndListByStation(t *testing.T) {
	pool := setupTestPool(t)
	ctx := context.Background()
	stationRepo := NewStationRepository(pool)
	tariffRepo := NewTariffRepository(pool)

	stationID, err := stationRepo.UpsertStation(ctx, testStation("FRTARIFFX1", 45.9, 6.1))
	if err != nil {
		t.Fatalf("UpsertStation: %v", err)
	}

	acPrice := 45.0
	dcPrice := 54.0
	acTariff := domain.StationTariff{
		StationID:              stationID,
		Source:                 "izivia",
		Kind:                   domain.TariffKindAC,
		Model:                  "izivia_text",
		Currency:               "EUR",
		EnergyPriceCentsPerKWh: &acPrice,
		RawText:                "0,45€/kWh",
		Extra:                  map[string]any{},
	}
	dcTariff := domain.StationTariff{
		StationID:              stationID,
		Source:                 "electra",
		Kind:                   domain.TariffKindDC,
		Model:                  "electra_fixed",
		Currency:               "EUR",
		EnergyPriceCentsPerKWh: &dcPrice,
		Extra:                  map[string]any{"windows": []any{}},
	}

	if err := tariffRepo.Upsert(ctx, acTariff); err != nil {
		t.Fatalf("Upsert ac: %v", err)
	}
	if err := tariffRepo.Upsert(ctx, dcTariff); err != nil {
		t.Fatalf("Upsert dc: %v", err)
	}

	tariffs, err := tariffRepo.ListByStation(ctx, stationID)
	if err != nil {
		t.Fatalf("ListByStation: %v", err)
	}
	if len(tariffs) != 2 {
		t.Fatalf("got %d tariffs, want 2", len(tariffs))
	}

	bySource := map[string]domain.StationTariff{}
	for _, t := range tariffs {
		bySource[t.Source] = t
	}
	if bySource["izivia"].RawText != "0,45€/kWh" {
		t.Errorf("izivia RawText = %q", bySource["izivia"].RawText)
	}
	if bySource["electra"].EnergyPriceCentsPerKWh == nil || *bySource["electra"].EnergyPriceCentsPerKWh != 54.0 {
		t.Errorf("electra EnergyPriceCentsPerKWh = %v, want 54.0", bySource["electra"].EnergyPriceCentsPerKWh)
	}

	// Upserting the same (station, source, kind) updates in place rather
	// than creating a second row.
	newPrice := 39.0
	acTariff.EnergyPriceCentsPerKWh = &newPrice
	acTariff.RawText = "0,39€/kWh"
	if err := tariffRepo.Upsert(ctx, acTariff); err != nil {
		t.Fatalf("Upsert ac (update): %v", err)
	}

	tariffs, err = tariffRepo.ListByStation(ctx, stationID)
	if err != nil {
		t.Fatalf("ListByStation after update: %v", err)
	}
	if len(tariffs) != 2 {
		t.Fatalf("got %d tariffs after update, want 2 (upsert must not duplicate)", len(tariffs))
	}
	for _, tar := range tariffs {
		if tar.Source == "izivia" && (tar.EnergyPriceCentsPerKWh == nil || *tar.EnergyPriceCentsPerKWh != 39.0) {
			t.Errorf("izivia EnergyPriceCentsPerKWh after update = %v, want 39.0", tar.EnergyPriceCentsPerKWh)
		}
	}
}

func TestTariffRepository_ListDistinctSourcesWithPlans(t *testing.T) {
	pool := setupTestPool(t)
	ctx := context.Background()
	stationRepo := NewStationRepository(pool)
	tariffRepo := NewTariffRepository(pool)

	empty, err := tariffRepo.ListDistinctSourcesWithPlans(ctx)
	if err != nil {
		t.Fatalf("ListDistinctSourcesWithPlans (empty): %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("ListDistinctSourcesWithPlans on an empty table = %v, want []", empty)
	}

	stationID, err := stationRepo.UpsertStation(ctx, testStation("FRSOURCES1", 45.9, 6.1))
	if err != nil {
		t.Fatalf("UpsertStation: %v", err)
	}
	price := 40.0
	tariffs := []domain.StationTariff{
		{Source: "electra", Plan: "app", Kind: domain.TariffKindAC},
		{Source: "electra", Plan: "public", Kind: domain.TariffKindAC},
		{Source: "electra", Plan: "app", Kind: domain.TariffKindDC}, // same source+plan, different kind: no new plan entry
		{Source: "izivia", Plan: domain.TariffPlanStandard, Kind: domain.TariffKindMixed},
	}
	for _, tariff := range tariffs {
		tariff.StationID = stationID
		tariff.Model = "test"
		tariff.Currency = "EUR"
		tariff.EnergyPriceCentsPerKWh = &price
		if err := tariffRepo.Upsert(ctx, tariff); err != nil {
			t.Fatalf("Upsert %s/%s: %v", tariff.Source, tariff.Plan, err)
		}
	}

	sources, err := tariffRepo.ListDistinctSourcesWithPlans(ctx)
	if err != nil {
		t.Fatalf("ListDistinctSourcesWithPlans: %v", err)
	}
	if len(sources) != 2 {
		t.Fatalf("got %d sources, want 2: %+v", len(sources), sources)
	}
	if sources[0].Source != "electra" || len(sources[0].Plans) != 2 || sources[0].Plans[0] != "app" || sources[0].Plans[1] != "public" {
		t.Errorf("sources[0] = %+v, want electra with plans [app public]", sources[0])
	}
	if sources[1].Source != "izivia" || len(sources[1].Plans) != 1 || sources[1].Plans[0] != "standard" {
		t.Errorf("sources[1] = %+v, want izivia with plans [standard]", sources[1])
	}
}

func TestTariffRepository_BulkUpsert(t *testing.T) {
	pool := setupTestPool(t)
	ctx := context.Background()
	stationRepo := NewStationRepository(pool)
	tariffRepo := NewTariffRepository(pool)

	stationID, err := stationRepo.UpsertStation(ctx, testStation("FRBULKTAR1", 45.9, 6.1))
	if err != nil {
		t.Fatalf("UpsertStation: %v", err)
	}

	acPrice := 30.0
	dcPrice := 50.0
	dcPriceDup := 55.0
	tariffs := []domain.StationTariff{
		{StationID: stationID, Source: "electra", Plan: "app", Kind: domain.TariffKindAC, Model: "m", Currency: "EUR", EnergyPriceCentsPerKWh: &acPrice, Extra: map[string]any{}},
		{StationID: stationID, Source: "electra", Plan: "app", Kind: domain.TariffKindDC, Model: "m", Currency: "EUR", EnergyPriceCentsPerKWh: &dcPrice, Extra: map[string]any{}},
		// Same conflict key (station, source, kind, plan) as the previous
		// row: the batch must dedupe (last one wins) instead of erroring
		// with "ON CONFLICT DO UPDATE command cannot affect row a second
		// time".
		{StationID: stationID, Source: "electra", Plan: "app", Kind: domain.TariffKindDC, Model: "m", Currency: "EUR", EnergyPriceCentsPerKWh: &dcPriceDup, Extra: map[string]any{}},
	}

	if err := tariffRepo.BulkUpsert(ctx, tariffs); err != nil {
		t.Fatalf("BulkUpsert: %v", err)
	}

	got, err := tariffRepo.ListByStation(ctx, stationID)
	if err != nil {
		t.Fatalf("ListByStation: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d tariffs, want 2 (deduped)", len(got))
	}
	byKind := map[string]domain.StationTariff{}
	for _, tar := range got {
		byKind[tar.Kind] = tar
	}
	if byKind["dc"].EnergyPriceCentsPerKWh == nil || *byKind["dc"].EnergyPriceCentsPerKWh != 55.0 {
		t.Errorf("dc EnergyPriceCentsPerKWh = %v, want 55.0 (last occurrence in the batch wins)", byKind["dc"].EnergyPriceCentsPerKWh)
	}

	// Re-upserting via BulkUpsert must update in place, not duplicate.
	newACPrice := 35.0
	if err := tariffRepo.BulkUpsert(ctx, []domain.StationTariff{
		{StationID: stationID, Source: "electra", Plan: "app", Kind: domain.TariffKindAC, Model: "m", Currency: "EUR", EnergyPriceCentsPerKWh: &newACPrice, Extra: map[string]any{}},
	}); err != nil {
		t.Fatalf("BulkUpsert (update): %v", err)
	}
	got, err = tariffRepo.ListByStation(ctx, stationID)
	if err != nil {
		t.Fatalf("ListByStation after update: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d tariffs after update, want 2 (upsert must not duplicate)", len(got))
	}
}

func TestTariffRepository_BulkUpsert_Empty(t *testing.T) {
	pool := setupTestPool(t)
	tariffRepo := NewTariffRepository(pool)
	if err := tariffRepo.BulkUpsert(context.Background(), nil); err != nil {
		t.Errorf("BulkUpsert(nil) = %v, want nil", err)
	}
}

func TestTariffRepository_ListByStation_Empty(t *testing.T) {
	pool := setupTestPool(t)
	ctx := context.Background()
	stationRepo := NewStationRepository(pool)
	tariffRepo := NewTariffRepository(pool)

	stationID, err := stationRepo.UpsertStation(ctx, testStation("FRNOTARIFF", 45.9, 6.1))
	if err != nil {
		t.Fatalf("UpsertStation: %v", err)
	}

	tariffs, err := tariffRepo.ListByStation(ctx, stationID)
	if err != nil {
		t.Fatalf("ListByStation: %v", err)
	}
	if len(tariffs) != 0 {
		t.Errorf("got %d tariffs, want 0", len(tariffs))
	}
}
