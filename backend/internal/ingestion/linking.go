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

// writeSourceStationChunk writes a chunk of source stations and their
// tariffs in a small, fixed number of round trips regardless of chunk
// size: one bulk nearest-IRVE-station lookup, one bulk source-station
// upsert, one bulk link upsert, one bulk tariff upsert — instead of ~1-8
// round trips per station (nearest lookup + source station + link + up to
// 6 tariffs). That difference is negligible on a local database (sub-
// millisecond round trips) but dominates wall-clock time against a
// database with real network latency, where it was measured to make
// large batches ~100-200x slower than this bulk form.
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
	stations := make([]domain.SourceStation, len(items))
	for i, item := range items {
		points[i] = repository.NearestStationQuery{Lat: item.Station.Lat, Lng: item.Station.Lng}
		stations[i] = item.Station
	}

	nearest, err := txLinks.FindNearestStations(ctx, points, maxDistanceM)
	if err != nil {
		return 0, err
	}
	sourceStationIDs, err := txSourceStations.BulkUpsert(ctx, stations)
	if err != nil {
		return 0, err
	}

	var linkUpserts []repository.LinkUpsert
	var tariffUpserts []domain.StationTariff
	for i, item := range items {
		candidate, ok := nearest[i]
		if !ok {
			continue
		}
		sourceStationID, ok := sourceStationIDs[repository.SourceStationKey(item.Station.Source, item.Station.SourceStationID)]
		if !ok {
			continue
		}

		quality := domain.LinkQualityByGeolocation
		if strings.EqualFold(candidate.OperatorName, item.Station.OperatorName) || strings.EqualFold(candidate.Name, item.Station.Name) {
			quality = domain.LinkQualityByOperatorName
		}
		linkUpserts = append(linkUpserts, repository.LinkUpsert{
			StationID: candidate.StationID, SourceStationID: sourceStationID,
			Source: item.Station.Source, LinkQuality: quality, DistanceMeters: candidate.DistanceMeters,
		})
		for _, t := range item.Tariffs {
			t.StationID = candidate.StationID
			tariffUpserts = append(tariffUpserts, t)
		}
	}

	if err := txLinks.BulkUpsert(ctx, linkUpserts); err != nil {
		return 0, err
	}
	if err := txTariffs.BulkUpsert(ctx, tariffUpserts); err != nil {
		return 0, err
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit source station chunk tx: %w", err)
	}
	return len(items), nil
}
