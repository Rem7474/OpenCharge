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

func TestTariffRepository_ListDistinctSources(t *testing.T) {
	pool := setupTestPool(t)
	ctx := context.Background()
	stationRepo := NewStationRepository(pool)
	tariffRepo := NewTariffRepository(pool)

	empty, err := tariffRepo.ListDistinctSources(ctx)
	if err != nil {
		t.Fatalf("ListDistinctSources (empty): %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("ListDistinctSources on an empty table = %v, want []", empty)
	}

	stationID, err := stationRepo.UpsertStation(ctx, testStation("FRSOURCES1", 45.9, 6.1))
	if err != nil {
		t.Fatalf("UpsertStation: %v", err)
	}
	price := 40.0
	for _, source := range []string{"electra", "izivia", "electra"} {
		if err := tariffRepo.Upsert(ctx, domain.StationTariff{
			StationID: stationID, Source: source, Kind: domain.TariffKindAC,
			Model: "test", Currency: "EUR", EnergyPriceCentsPerKWh: &price,
		}); err != nil {
			t.Fatalf("Upsert %s: %v", source, err)
		}
	}

	sources, err := tariffRepo.ListDistinctSources(ctx)
	if err != nil {
		t.Fatalf("ListDistinctSources: %v", err)
	}
	if len(sources) != 2 || sources[0] != "electra" || sources[1] != "izivia" {
		t.Errorf("ListDistinctSources = %v, want [electra izivia] (deduped, sorted)", sources)
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
