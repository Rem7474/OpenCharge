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
