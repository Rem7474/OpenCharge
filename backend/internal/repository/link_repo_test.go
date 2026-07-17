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

func TestLinkRepository_FindNearestStations(t *testing.T) {
	pool := setupTestPool(t)
	ctx := context.Background()
	stationRepo := NewStationRepository(pool)
	linkRepo := NewLinkRepository(pool)

	near := testStation("FRBULKN001", 45.9000, 6.1000)
	near.OperatorName = "Izivia"
	far := testStation("FRBULKN002", 46.5000, 6.9000)

	if _, err := stationRepo.UpsertStation(ctx, near); err != nil {
		t.Fatalf("UpsertStation near: %v", err)
	}
	if _, err := stationRepo.UpsertStation(ctx, far); err != nil {
		t.Fatalf("UpsertStation far: %v", err)
	}

	points := []NearestStationQuery{
		{Lat: 45.9001, Lng: 6.1000},  // index 0: close to "near"
		{Lat: 10.0000, Lng: 10.0000}, // index 1: nowhere near anything
	}
	results, err := linkRepo.FindNearestStations(ctx, points, 150)
	if err != nil {
		t.Fatalf("FindNearestStations: %v", err)
	}

	candidate, ok := results[0]
	if !ok {
		t.Fatal("results[0] missing, want the nearby station")
	}
	if candidate.OperatorName != "Izivia" {
		t.Errorf("results[0].OperatorName = %q, want Izivia", candidate.OperatorName)
	}
	if candidate.DistanceMeters <= 0 || candidate.DistanceMeters > 150 {
		t.Errorf("results[0].DistanceMeters = %v, want a small positive value under 150", candidate.DistanceMeters)
	}

	if _, ok := results[1]; ok {
		t.Errorf("results[1] = %+v, want absent (no station within range)", results[1])
	}

	if empty, err := linkRepo.FindNearestStations(ctx, nil, 150); err != nil || empty != nil {
		t.Errorf("FindNearestStations(nil) = (%v, %v), want (nil, nil)", empty, err)
	}
}

func TestLinkRepository_FindNearestStationsForKind(t *testing.T) {
	pool := setupTestPool(t)
	ctx := context.Background()
	stationRepo := NewStationRepository(pool)
	linkRepo := NewLinkRepository(pool)

	// Two co-located IRVE rows (same physical address, different PDC):
	// a DC (CCS) connector and an AC (T2) connector, close enough to each
	// other that pure nearest-by-distance can't tell them apart reliably.
	dc := testStation("FRKINDDC01", 45.9000, 6.1000)
	dc.ConnectorType = "CCS"
	dc.OperatorName = "KindTest"
	ac := testStation("FRKINDAC01", 45.90001, 6.10001)
	ac.ConnectorType = "T2"
	ac.OperatorName = "KindTest"
	far := testStation("FRKINDFAR1", 46.5000, 6.9000)
	far.ConnectorType = "T2"

	if _, err := stationRepo.UpsertStation(ctx, dc); err != nil {
		t.Fatalf("UpsertStation dc: %v", err)
	}
	if _, err := stationRepo.UpsertStation(ctx, ac); err != nil {
		t.Fatalf("UpsertStation ac: %v", err)
	}
	if _, err := stationRepo.UpsertStation(ctx, far); err != nil {
		t.Fatalf("UpsertStation far: %v", err)
	}

	points := []NearestStationQuery{
		{Lat: 45.9000, Lng: 6.1000},  // index 0: sits exactly on "dc"
		{Lat: 10.0000, Lng: 10.0000}, // index 1: nowhere near anything
	}

	dcResults, err := linkRepo.FindNearestStationsForKind(ctx, points, domain.TariffKindDC, 150)
	if err != nil {
		t.Fatalf("FindNearestStationsForKind(dc): %v", err)
	}
	dcCandidate, ok := dcResults[0]
	if !ok {
		t.Fatal("dcResults[0] missing, want the dc station")
	}
	if dcCandidate.DistanceMeters < 0 {
		t.Errorf("dcResults[0].DistanceMeters = %v, want >= 0", dcCandidate.DistanceMeters)
	}

	acResults, err := linkRepo.FindNearestStationsForKind(ctx, points, domain.TariffKindAC, 150)
	if err != nil {
		t.Fatalf("FindNearestStationsForKind(ac): %v", err)
	}
	acCandidate, ok := acResults[0]
	if !ok {
		t.Fatal("acResults[0] missing, want the ac station")
	}

	if dcCandidate.StationID == acCandidate.StationID {
		t.Errorf("dc and ac candidates resolved to the same station %v, want distinct co-located stations", dcCandidate.StationID)
	}

	if _, ok := dcResults[1]; ok {
		t.Errorf("dcResults[1] = %+v, want absent (no station within range)", dcResults[1])
	}

	if empty, err := linkRepo.FindNearestStationsForKind(ctx, nil, domain.TariffKindDC, 150); err != nil || empty != nil {
		t.Errorf("FindNearestStationsForKind(nil) = (%v, %v), want (nil, nil)", empty, err)
	}
}

func TestLinkRepository_BulkUpsert(t *testing.T) {
	pool := setupTestPool(t)
	ctx := context.Background()
	stationRepo := NewStationRepository(pool)
	sourceRepo := NewSourceStationRepository(pool)
	linkRepo := NewLinkRepository(pool)

	station1, err := stationRepo.UpsertStation(ctx, testStation("FRBULKLK01", 45.9, 6.1))
	if err != nil {
		t.Fatalf("UpsertStation 1: %v", err)
	}
	station2, err := stationRepo.UpsertStation(ctx, testStation("FRBULKLK02", 46.0, 6.2))
	if err != nil {
		t.Fatalf("UpsertStation 2: %v", err)
	}
	src1, err := sourceRepo.Upsert(ctx, domain.SourceStation{Source: "electra", SourceStationID: "bulk-1", Lat: 45.9, Lng: 6.1})
	if err != nil {
		t.Fatalf("SourceStations.Upsert 1: %v", err)
	}
	src2, err := sourceRepo.Upsert(ctx, domain.SourceStation{Source: "electra", SourceStationID: "bulk-2", Lat: 46.0, Lng: 6.2})
	if err != nil {
		t.Fatalf("SourceStations.Upsert 2: %v", err)
	}

	err = linkRepo.BulkUpsert(ctx, []LinkUpsert{
		{StationID: station1, SourceStationID: src1, Source: "electra", LinkQuality: domain.LinkQualityByGeolocation, DistanceMeters: 12.0},
		{StationID: station2, SourceStationID: src2, Source: "electra", LinkQuality: domain.LinkQualityByOperatorName, DistanceMeters: 8.0},
	})
	if err != nil {
		t.Fatalf("BulkUpsert: %v", err)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM station_links WHERE source = 'electra'`).Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if count != 2 {
		t.Errorf("found %d links, want 2", count)
	}

	// Re-upserting via BulkUpsert must update in place, not duplicate.
	err = linkRepo.BulkUpsert(ctx, []LinkUpsert{
		{StationID: station1, SourceStationID: src1, Source: "electra", LinkQuality: domain.LinkQualityByOperatorName, DistanceMeters: 5.0},
	})
	if err != nil {
		t.Fatalf("BulkUpsert (update): %v", err)
	}
	var quality string
	var distance float64
	err = pool.QueryRow(ctx, `SELECT link_quality, distance_meters FROM station_links WHERE station_id = $1 AND source_station_id = $2`, station1, src1).
		Scan(&quality, &distance)
	if err != nil {
		t.Fatalf("query link: %v", err)
	}
	if quality != domain.LinkQualityByOperatorName || distance != 5.0 {
		t.Errorf("link = (%q, %v), want (%q, 5.0)", quality, distance, domain.LinkQualityByOperatorName)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM station_links WHERE source = 'electra'`).Scan(&count); err != nil {
		t.Fatalf("count rows after update: %v", err)
	}
	if count != 2 {
		t.Errorf("found %d links after update, want 2 (upsert must not duplicate)", count)
	}
}

func TestLinkRepository_BulkUpsert_Empty(t *testing.T) {
	pool := setupTestPool(t)
	linkRepo := NewLinkRepository(pool)
	if err := linkRepo.BulkUpsert(context.Background(), nil); err != nil {
		t.Errorf("BulkUpsert(nil) = %v, want nil", err)
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
