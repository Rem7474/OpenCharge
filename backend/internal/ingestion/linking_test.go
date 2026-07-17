package ingestion

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"opencharge/internal/domain"
	"opencharge/internal/repository"
)

func setupLinkingTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("skipping linking integration test: set TEST_DATABASE_URL (or DATABASE_URL) to a Postgres/PostGIS instance with migrations applied")
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

func testIRVEStation(irveIDPDC string, lat, lng float64, connectorType string) domain.Station {
	power := 50.0
	return domain.Station{
		IRVEIDPDC:      irveIDPDC,
		OperatorName:   "TestOperator",
		Enseigne:       "TestOperator",
		Name:           "Station " + irveIDPDC,
		AddressCity:    "Annecy",
		AddressCountry: "FR",
		Lat:            lat,
		Lng:            lng,
		PowerKW:        &power,
		ConnectorType:  connectorType,
		AccessType:     "paid",
		Metadata:       map[string]any{"raw_field": "value"},
	}
}

// TestWriteSourceStationChunk_KindAwareMatching pins the fix for a
// production bug: a single physical address can have two co-located IRVE
// rows (one DC/CCS PDC, one AC/T2 PDC), and a source station reporting
// both an ac and a dc tariff for "the same place" must have each tariff
// attached to the IRVE row of the matching kind — not both piled onto
// whichever row happened to be nearest by pure distance, which silently
// hid one kind's price behind the other's connector filter.
func TestWriteSourceStationChunk_KindAwareMatching(t *testing.T) {
	pool := setupLinkingTestPool(t)
	ctx := context.Background()

	stationRepo := repository.NewStationRepository(pool)
	sourceStationRepo := repository.NewSourceStationRepository(pool)
	tariffRepo := repository.NewTariffRepository(pool)
	linkRepo := repository.NewLinkRepository(pool)

	dcStationID, err := stationRepo.UpsertStation(ctx, testIRVEStation("FRCHUNKDC1", 45.9000, 6.1000, domain.ConnectorTypeCCS))
	if err != nil {
		t.Fatalf("UpsertStation dc: %v", err)
	}
	acStationID, err := stationRepo.UpsertStation(ctx, testIRVEStation("FRCHUNKAC1", 45.90001, 6.10001, domain.ConnectorTypeT2))
	if err != nil {
		t.Fatalf("UpsertStation ac: %v", err)
	}

	item := normalizedSourceStation{
		Station: domain.SourceStation{
			Source:          "electra",
			SourceStationID: "kind-aware-1",
			OperatorName:    "TestOperator",
			Name:            "Station FRCHUNKDC1",
			Lat:             45.9000,
			Lng:             6.1000,
		},
		Tariffs: []domain.StationTariff{
			{Source: "electra", Kind: domain.TariffKindDC, Plan: "standard", EnergyPriceCentsPerKWh: ptr(45.0)},
			{Source: "electra", Kind: domain.TariffKindAC, Plan: "standard", EnergyPriceCentsPerKWh: ptr(30.0)},
		},
	}

	n, err := writeSourceStationChunk(ctx, pool, sourceStationRepo, tariffRepo, linkRepo, 150, []normalizedSourceStation{item})
	if err != nil {
		t.Fatalf("writeSourceStationChunk: %v", err)
	}
	if n != 1 {
		t.Fatalf("writeSourceStationChunk wrote %d items, want 1", n)
	}

	dcTariffs, err := tariffRepo.ListByStation(ctx, dcStationID)
	if err != nil {
		t.Fatalf("ListByStation dc: %v", err)
	}
	if len(dcTariffs) != 1 || dcTariffs[0].Kind != domain.TariffKindDC {
		t.Fatalf("dc station tariffs = %+v, want exactly one dc tariff", dcTariffs)
	}

	acTariffs, err := tariffRepo.ListByStation(ctx, acStationID)
	if err != nil {
		t.Fatalf("ListByStation ac: %v", err)
	}
	if len(acTariffs) != 1 || acTariffs[0].Kind != domain.TariffKindAC {
		t.Fatalf("ac station tariffs = %+v, want exactly one ac tariff", acTariffs)
	}

	var linkCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM station_links WHERE source = 'electra'`).Scan(&linkCount); err != nil {
		t.Fatalf("count links: %v", err)
	}
	if linkCount != 2 {
		t.Errorf("station_links count = %d, want 2 (one per co-located kind-matched station)", linkCount)
	}
}
