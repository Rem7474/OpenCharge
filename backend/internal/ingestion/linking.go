package ingestion

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
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

	// A source station's tariffs can span both ac and dc kinds (e.g.
	// Electra), but IRVE models one row per physical connector: a
	// co-located CCS row and T2 row for what's really one charging point
	// are indistinguishable by distance alone. So beyond the plain
	// nearest-overall lookup above (used as the fallback and for the
	// station_links row when a source station has no kind-tagged
	// tariffs), resolve a kind-preferring nearest separately for the
	// subset of items that actually have an ac or dc tariff to place.
	var acIdxs, dcIdxs []int
	var acPoints, dcPoints []repository.NearestStationQuery
	for i, item := range items {
		hasAC, hasDC := false, false
		for _, t := range item.Tariffs {
			switch t.Kind {
			case domain.TariffKindAC:
				hasAC = true
			case domain.TariffKindDC:
				hasDC = true
			}
		}
		if hasAC {
			acIdxs = append(acIdxs, i)
			acPoints = append(acPoints, points[i])
		}
		if hasDC {
			dcIdxs = append(dcIdxs, i)
			dcPoints = append(dcPoints, points[i])
		}
	}

	nearestAC := map[int]repository.NearestStationCandidate{}
	if len(acPoints) > 0 {
		byLocalIdx, err := txLinks.FindNearestStationsForKind(ctx, acPoints, domain.TariffKindAC, maxDistanceM)
		if err != nil {
			return 0, err
		}
		for local, candidate := range byLocalIdx {
			nearestAC[acIdxs[local]] = candidate
		}
	}
	nearestDC := map[int]repository.NearestStationCandidate{}
	if len(dcPoints) > 0 {
		byLocalIdx, err := txLinks.FindNearestStationsForKind(ctx, dcPoints, domain.TariffKindDC, maxDistanceM)
		if err != nil {
			return 0, err
		}
		for local, candidate := range byLocalIdx {
			nearestDC[dcIdxs[local]] = candidate
		}
	}

	sourceStationIDs, err := txSourceStations.BulkUpsert(ctx, stations)
	if err != nil {
		return 0, err
	}

	var linkUpserts []repository.LinkUpsert
	var tariffUpserts []domain.StationTariff
	for i, item := range items {
		anyCandidate, hasAny := nearest[i]
		sourceStationID, ok := sourceStationIDs[repository.SourceStationKey(item.Station.Source, item.Station.SourceStationID)]
		if !ok {
			continue
		}

		// Every distinct IRVE station this source station's tariffs
		// actually resolved to gets its own link row — usually one (ac
		// and dc land on the same station), occasionally two (co-located
		// but distinct ac/dc rows).
		usedStations := map[uuid.UUID]repository.NearestStationCandidate{}
		if hasAny {
			usedStations[anyCandidate.StationID] = anyCandidate
		}

		for _, t := range item.Tariffs {
			candidate, ok := anyCandidate, hasAny
			switch t.Kind {
			case domain.TariffKindAC:
				if c, kindOK := nearestAC[i]; kindOK {
					candidate, ok = c, true
				}
			case domain.TariffKindDC:
				if c, kindOK := nearestDC[i]; kindOK {
					candidate, ok = c, true
				}
			}
			if !ok {
				continue
			}
			t.StationID = candidate.StationID
			tariffUpserts = append(tariffUpserts, t)
			usedStations[candidate.StationID] = candidate
		}

		for _, candidate := range usedStations {
			quality := domain.LinkQualityByGeolocation
			if strings.EqualFold(candidate.OperatorName, item.Station.OperatorName) || strings.EqualFold(candidate.Name, item.Station.Name) {
				quality = domain.LinkQualityByOperatorName
			}
			linkUpserts = append(linkUpserts, repository.LinkUpsert{
				StationID: candidate.StationID, SourceStationID: sourceStationID,
				Source: item.Station.Source, LinkQuality: quality, DistanceMeters: candidate.DistanceMeters,
			})
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
