package repository

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// setupTestPool connects to a real Postgres/PostGIS instance for
// integration tests. It looks up TEST_DATABASE_URL first (CI sets it
// explicitly), falling back to DATABASE_URL. The target database must
// already have the migrations in db/migrations applied. Tests are skipped
// when neither variable is set, so `go test ./...` stays usable without a
// database around.
func setupTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("skipping repository integration test: set TEST_DATABASE_URL (or DATABASE_URL) to a Postgres/PostGIS instance with migrations applied")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect to test database: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("ping test database: %v", err)
	}

	if _, err := pool.Exec(ctx, `TRUNCATE TABLE station_links, station_tariffs, source_stations, stations RESTART IDENTITY CASCADE`); err != nil {
		pool.Close()
		t.Fatalf("truncate test database: %v", err)
	}

	t.Cleanup(pool.Close)
	return pool
}
