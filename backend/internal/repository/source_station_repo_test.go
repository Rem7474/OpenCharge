package repository

import (
	"context"
	"testing"

	"opencharge/internal/domain"
)

func TestSourceStationRepository_UpsertDedupes(t *testing.T) {
	pool := setupTestPool(t)
	ctx := context.Background()
	repo := NewSourceStationRepository(pool)

	src := domain.SourceStation{
		Source:          "izivia",
		SourceStationID: "izv-abc",
		Name:            "Izivia Annecy",
		OperatorName:    "Izivia",
		Lat:             45.9,
		Lng:             6.1,
		Raw:             map[string]any{"foo": "bar"},
	}

	id, err := repo.Upsert(ctx, src)
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	src.Name = "Izivia Annecy Centre"
	id2, err := repo.Upsert(ctx, src)
	if err != nil {
		t.Fatalf("Upsert (update): %v", err)
	}
	if id != id2 {
		t.Errorf("Upsert changed id for the same (source, source_station_id): got %v, want %v", id2, id)
	}

	var name string
	err = pool.QueryRow(ctx, `SELECT name FROM source_stations WHERE id = $1`, id).Scan(&name)
	if err != nil {
		t.Fatalf("query name: %v", err)
	}
	if name != "Izivia Annecy Centre" {
		t.Errorf("name = %q, want %q", name, "Izivia Annecy Centre")
	}

	var count int
	err = pool.QueryRow(ctx, `SELECT count(*) FROM source_stations WHERE source = 'izivia' AND source_station_id = 'izv-abc'`).Scan(&count)
	if err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if count != 1 {
		t.Errorf("found %d rows for the same source station, want 1", count)
	}
}

func TestSourceStationRepository_BulkUpsert(t *testing.T) {
	pool := setupTestPool(t)
	ctx := context.Background()
	repo := NewSourceStationRepository(pool)

	stations := []domain.SourceStation{
		{Source: "electra", SourceStationID: "elec-1", Name: "A", OperatorName: "Electra", Lat: 45.9, Lng: 6.1, Raw: map[string]any{}},
		{Source: "electra", SourceStationID: "elec-2", Name: "B", OperatorName: "Electra", Lat: 46.0, Lng: 6.2, Raw: map[string]any{}},
		// Same (source, source_station_id) as the first entry: the batch
		// must dedupe, keeping this later value, not error on conflict.
		{Source: "electra", SourceStationID: "elec-1", Name: "A renamed", OperatorName: "Electra", Lat: 45.9, Lng: 6.1, Raw: map[string]any{}},
	}

	ids, err := repo.BulkUpsert(ctx, stations)
	if err != nil {
		t.Fatalf("BulkUpsert: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("got %d ids, want 2 (deduped)", len(ids))
	}
	id1, ok := ids[SourceStationKey("electra", "elec-1")]
	if !ok {
		t.Fatal("missing id for elec-1")
	}
	if _, ok := ids[SourceStationKey("electra", "elec-2")]; !ok {
		t.Fatal("missing id for elec-2")
	}

	var name string
	if err := pool.QueryRow(ctx, `SELECT name FROM source_stations WHERE id = $1`, id1).Scan(&name); err != nil {
		t.Fatalf("query name: %v", err)
	}
	if name != "A renamed" {
		t.Errorf("name = %q, want %q (last occurrence in the batch wins)", name, "A renamed")
	}

	// Re-upserting via BulkUpsert must update in place, not duplicate rows.
	ids2, err := repo.BulkUpsert(ctx, []domain.SourceStation{
		{Source: "electra", SourceStationID: "elec-1", Name: "A again", OperatorName: "Electra", Lat: 45.9, Lng: 6.1, Raw: map[string]any{}},
	})
	if err != nil {
		t.Fatalf("BulkUpsert (update): %v", err)
	}
	if ids2[SourceStationKey("electra", "elec-1")] != id1 {
		t.Errorf("BulkUpsert changed id on update: got %v, want %v", ids2[SourceStationKey("electra", "elec-1")], id1)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM source_stations WHERE source = 'electra'`).Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if count != 2 {
		t.Errorf("found %d electra rows, want 2", count)
	}
}

func TestSourceStationRepository_BulkUpsert_Empty(t *testing.T) {
	pool := setupTestPool(t)
	repo := NewSourceStationRepository(pool)
	ids, err := repo.BulkUpsert(context.Background(), nil)
	if err != nil {
		t.Errorf("BulkUpsert(nil) error = %v, want nil", err)
	}
	if ids != nil {
		t.Errorf("BulkUpsert(nil) = %v, want nil", ids)
	}
}
