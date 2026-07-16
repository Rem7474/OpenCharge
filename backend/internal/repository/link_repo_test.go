package repository

import (
	"context"
	"testing"

	"opencharge/internal/domain"
)

func TestLinkRepository_FindNearestStation(t *testing.T) {
	pool := setupTestPool(t)
	ctx := context.Background()
	stationRepo := NewStationRepository(pool)
	linkRepo := NewLinkRepository(pool)

	near := testStation("FRLINK0001", 45.9000, 6.1000)
	near.OperatorName = "Izivia"
	far := testStation("FRLINK0002", 46.5000, 6.9000)

	if _, err := stationRepo.UpsertStation(ctx, near); err != nil {
		t.Fatalf("UpsertStation near: %v", err)
	}
	if _, err := stationRepo.UpsertStation(ctx, far); err != nil {
		t.Fatalf("UpsertStation far: %v", err)
	}

	// A point ~11m from "near" (roughly 0.0001 degrees of latitude).
	candidate, err := linkRepo.FindNearestStation(ctx, 45.9001, 6.1000, 150)
	if err != nil {
		t.Fatalf("FindNearestStation: %v", err)
	}
	if candidate == nil {
		t.Fatal("FindNearestStation returned nil, want the nearby station")
	}
	if candidate.OperatorName != "Izivia" {
		t.Errorf("OperatorName = %q, want Izivia", candidate.OperatorName)
	}
	if candidate.DistanceMeters <= 0 || candidate.DistanceMeters > 150 {
		t.Errorf("DistanceMeters = %v, want a small positive value under 150", candidate.DistanceMeters)
	}

	// Same point, but with a radius too small to reach anything.
	none, err := linkRepo.FindNearestStation(ctx, 45.9001, 6.1000, 1)
	if err != nil {
		t.Fatalf("FindNearestStation (tiny radius): %v", err)
	}
	if none != nil {
		t.Errorf("FindNearestStation with radius=1m = %+v, want nil", none)
	}
}

func TestLinkRepository_Upsert(t *testing.T) {
	pool := setupTestPool(t)
	ctx := context.Background()
	stationRepo := NewStationRepository(pool)
	sourceRepo := NewSourceStationRepository(pool)
	linkRepo := NewLinkRepository(pool)

	stationID, err := stationRepo.UpsertStation(ctx, testStation("FRLINKUP01", 45.9, 6.1))
	if err != nil {
		t.Fatalf("UpsertStation: %v", err)
	}
	sourceStationID, err := sourceRepo.Upsert(ctx, domain.SourceStation{
		Source: "electra", SourceStationID: "elec-xyz", Lat: 45.9, Lng: 6.1,
	})
	if err != nil {
		t.Fatalf("SourceStations.Upsert: %v", err)
	}

	distance := 42.0
	if err := linkRepo.Upsert(ctx, stationID, sourceStationID, "electra", domain.LinkQualityByGeolocation, &distance); err != nil {
		t.Fatalf("Links.Upsert: %v", err)
	}

	// Re-upserting the same pair updates the row instead of erroring on the
	// unique (station_id, source_station_id) constraint.
	distance = 10.0
	if err := linkRepo.Upsert(ctx, stationID, sourceStationID, "electra", domain.LinkQualityByOperatorName, &distance); err != nil {
		t.Fatalf("Links.Upsert (update): %v", err)
	}

	var quality string
	var storedDistance float64
	err = pool.QueryRow(ctx, `SELECT link_quality, distance_meters FROM station_links WHERE station_id = $1 AND source_station_id = $2`, stationID, sourceStationID).
		Scan(&quality, &storedDistance)
	if err != nil {
		t.Fatalf("query link: %v", err)
	}
	if quality != domain.LinkQualityByOperatorName {
		t.Errorf("link_quality = %q, want %q", quality, domain.LinkQualityByOperatorName)
	}
	if storedDistance != 10.0 {
		t.Errorf("distance_meters = %v, want 10.0", storedDistance)
	}
}
