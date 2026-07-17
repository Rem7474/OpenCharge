package api

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"opencharge/internal/repository"
)

// setupHandler wires a StationsHandler against a real Postgres/PostGIS
// instance so tests exercise the actual SQL, not a mock. It looks up
// TEST_DATABASE_URL first, falling back to DATABASE_URL, and skips when
// neither is set (the target database must already have migrations
// applied, exactly like the repository package's integration tests).
func setupHandler(t *testing.T) *StationsHandler {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("skipping API integration test: set TEST_DATABASE_URL (or DATABASE_URL) to a Postgres/PostGIS instance with migrations applied")
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

	return NewStationsHandler(repository.NewStationRepository(pool), repository.NewTariffRepository(pool))
}
