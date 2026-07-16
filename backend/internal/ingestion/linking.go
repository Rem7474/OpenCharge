package ingestion

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"opencharge/internal/domain"
	"opencharge/internal/repository"
)

// ingestionBulkChunkSize is the number of source stations processed per
// database transaction, shared by every source that correlates external
// stations against the IRVE referential (Electra, Izivia, ...).
const ingestionBulkChunkSize = 200

// normalizedSourceStation is a source station and its tariffs, ready to be
// correlated with the IRVE referential and written to the database.
type normalizedSourceStation struct {
	Station domain.SourceStation
	Tariffs []domain.StationTariff
}

// writeSourceStationChunk upserts a chunk's source stations, resolves all
// of their nearest IRVE station in a single bulk query (instead of one
// geospatial query per station), then upserts links and tariffs — all
// inside one transaction.
func writeSourceStationChunk(ctx context.Context, pool *pgxpool.Pool, sourceStations *repository.SourceStationRepository, tariffs *repository.TariffRepository, links *repository.LinkRepository, maxDistanceM float64, items []normalizedSourceStation) (int, error) {
	if len(items) == 0 {
		return 0, nil
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin source station chunk tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	txSourceStations := sourceStations.WithTx(tx)
	txTariffs := tariffs.WithTx(tx)
	txLinks := links.WithTx(tx)

	points := make([]repository.NearestStationQuery, len(items))
	for i, item := range items {
		points[i] = repository.NearestStationQuery{Lat: item.Station.Lat, Lng: item.Station.Lng}
	}
	nearest, err := txLinks.FindNearestStations(ctx, points, maxDistanceM)
	if err != nil {
		return 0, err
	}

	processed := 0
	for i, item := range items {
		sourceStationID, err := txSourceStations.Upsert(ctx, item.Station)
		if err != nil {
			return processed, err
		}

		candidate, ok := nearest[i]
		if ok {
			quality := domain.LinkQualityByGeolocation
			if strings.EqualFold(candidate.OperatorName, item.Station.OperatorName) || strings.EqualFold(candidate.Name, item.Station.Name) {
				quality = domain.LinkQualityByOperatorName
			}
			distance := candidate.DistanceMeters
			if err := txLinks.Upsert(ctx, candidate.StationID, sourceStationID, item.Station.Source, quality, &distance); err != nil {
				return processed, err
			}
			for _, t := range item.Tariffs {
				t.StationID = candidate.StationID
				if err := txTariffs.Upsert(ctx, t); err != nil {
					return processed, err
				}
			}
		}
		processed++
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit source station chunk tx: %w", err)
	}
	return processed, nil
}
