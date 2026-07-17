package repository

import (
	"context"
	"testing"
	"time"

	"opencharge/internal/domain"
)

// TestSweepStaleSourceData exercises the mark-and-sweep pattern each
// ingester's Run() now applies at the end of a fully successful run: a
// source_station/tariff whose updated_at predates the sweep threshold (as
// if written by a previous run and never touched since — the source no
// longer reports it) is deleted, along with its cascaded station_link,
// while a row touched after the threshold survives.
func TestSweepStaleSourceData(t *testing.T) {
	pool := setupTestPool(t)
	ctx := context.Background()
	stationRepo := NewStationRepository(pool)
	sourceStationRepo := NewSourceStationRepository(pool)
	tariffRepo := NewTariffRepository(pool)
	linkRepo := NewLinkRepository(pool)

	stationID, err := stationRepo.UpsertStation(ctx, testStation("FRSWEEP001", 45.9, 6.1))
	if err != nil {
		t.Fatalf("UpsertStation: %v", err)
	}

	staleID, err := sourceStationRepo.Upsert(ctx, domain.SourceStation{
		Source: "izivia", SourceStationID: "izv-stale", Name: "Stale", OperatorName: "Izivia",
		Lat: 45.9, Lng: 6.1, Raw: map[string]any{},
	})
	if err != nil {
		t.Fatalf("Upsert stale source station: %v", err)
	}
	freshID, err := sourceStationRepo.Upsert(ctx, domain.SourceStation{
		Source: "izivia", SourceStationID: "izv-fresh", Name: "Fresh", OperatorName: "Izivia",
		Lat: 45.91, Lng: 6.11, Raw: map[string]any{},
	})
	if err != nil {
		t.Fatalf("Upsert fresh source station: %v", err)
	}

	if err := linkRepo.Upsert(ctx, stationID, staleID, "izivia", domain.LinkQualityByGeolocation, nil); err != nil {
		t.Fatalf("Upsert stale link: %v", err)
	}

	stalePrice := 10.0
	if err := tariffRepo.Upsert(ctx, domain.StationTariff{
		StationID: stationID, Source: "izivia", Plan: domain.TariffPlanStandard, Kind: domain.TariffKindAC,
		Model: "izivia_text", Currency: "EUR", EnergyPriceCentsPerKWh: &stalePrice, Extra: map[string]any{},
	}); err != nil {
		t.Fatalf("Upsert stale tariff: %v", err)
	}

	// Backdate the "stale" rows, as if they were written by a previous run
	// and never observed again — exactly what the sweep is meant to catch.
	if _, err := pool.Exec(ctx, `UPDATE source_stations SET updated_at = now() - interval '1 hour' WHERE id = $1`, staleID); err != nil {
		t.Fatalf("backdate stale source_station: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE station_tariffs SET updated_at = now() - interval '1 hour' WHERE station_id = $1 AND kind = $2`, stationID, domain.TariffKindAC); err != nil {
		t.Fatalf("backdate stale tariff: %v", err)
	}

	sweepThreshold := time.Now()

	// Re-touch both "fresh" rows after the threshold, simulating this run
	// having actually seen them (a real ingestion run's own writes all land
	// after runStart, which is what sweepThreshold stands in for here).
	if _, err := sourceStationRepo.Upsert(ctx, domain.SourceStation{
		Source: "izivia", SourceStationID: "izv-fresh", Name: "Fresh", OperatorName: "Izivia",
		Lat: 45.91, Lng: 6.11, Raw: map[string]any{},
	}); err != nil {
		t.Fatalf("re-Upsert fresh source station: %v", err)
	}
	freshPrice := 20.0
	if err := tariffRepo.Upsert(ctx, domain.StationTariff{
		StationID: stationID, Source: "izivia", Plan: domain.TariffPlanStandard, Kind: domain.TariffKindDC,
		Model: "izivia_text", Currency: "EUR", EnergyPriceCentsPerKWh: &freshPrice, Extra: map[string]any{},
	}); err != nil {
		t.Fatalf("Upsert fresh tariff: %v", err)
	}

	if err := SweepStaleSourceData(ctx, pool, "izivia", sweepThreshold); err != nil {
		t.Fatalf("SweepStaleSourceData: %v", err)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM source_stations WHERE id = $1`, staleID).Scan(&count); err != nil {
		t.Fatalf("count stale source_station: %v", err)
	}
	if count != 0 {
		t.Error("stale source_station survived the sweep")
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM station_links WHERE source_station_id = $1`, staleID).Scan(&count); err != nil {
		t.Fatalf("count stale link: %v", err)
	}
	if count != 0 {
		t.Error("stale link survived the sweep (should cascade from source_stations)")
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM source_stations WHERE id = $1`, freshID).Scan(&count); err != nil {
		t.Fatalf("count fresh source_station: %v", err)
	}
	if count != 1 {
		t.Error("fresh source_station was wrongly swept")
	}

	tariffs, err := tariffRepo.ListByStation(ctx, stationID)
	if err != nil {
		t.Fatalf("ListByStation: %v", err)
	}
	if len(tariffs) != 1 {
		t.Fatalf("got %d tariffs, want 1 (only the fresh dc tariff should survive)", len(tariffs))
	}
	if tariffs[0].Kind != domain.TariffKindDC {
		t.Errorf("surviving tariff kind = %q, want dc", tariffs[0].Kind)
	}
}

// TestSweepStaleSourceData_OnlyTargetsGivenSource confirms the sweep never
// touches another source's data even if it's equally "stale" by updated_at
// — each ingester only ever sweeps its own source.
func TestSweepStaleSourceData_OnlyTargetsGivenSource(t *testing.T) {
	pool := setupTestPool(t)
	ctx := context.Background()
	sourceStationRepo := NewSourceStationRepository(pool)

	otherID, err := sourceStationRepo.Upsert(ctx, domain.SourceStation{
		Source: "electra", SourceStationID: "elec-untouched", Name: "Other source", OperatorName: "Electra",
		Lat: 45.9, Lng: 6.1, Raw: map[string]any{},
	})
	if err != nil {
		t.Fatalf("Upsert other-source station: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE source_stations SET updated_at = now() - interval '1 hour' WHERE id = $1`, otherID); err != nil {
		t.Fatalf("backdate other-source station: %v", err)
	}

	if err := SweepStaleSourceData(ctx, pool, "izivia", time.Now()); err != nil {
		t.Fatalf("SweepStaleSourceData: %v", err)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM source_stations WHERE id = $1`, otherID).Scan(&count); err != nil {
		t.Fatalf("count other-source station: %v", err)
	}
	if count != 1 {
		t.Error("sweeping izivia deleted an electra row")
	}
}
