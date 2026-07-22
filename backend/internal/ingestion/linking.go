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
	// ACPowerKW/DCPowerKW, when known, are the source's own rated power for
	// its AC/DC connectors — used as FindNearestStationsForKind's power-aware
	// tie-break target so that, when several IRVE rows of the same kind sit
	// at essentially the same coordinates (e.g. two DC rows of different
	// power at one physical location), the tariff lands on the row whose own
	// power actually matches rather than just whichever is nearest. Left nil
	// by sources that don't expose a usable power figure (most of them),
	// which keeps their existing nearest-by-distance-only behavior.
	ACPowerKW *float64
	DCPowerKW *float64
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
	//
	// Grouped by (kind, ConnectorType) rather than just kind: a single
	// source station's tariffs of the same kind can themselves span
	// several distinct connector types (today: only Freshmile sets
	// ConnectorType, e.g. a dc/CCS tariff and a dc/CHAdeMO tariff for the
	// same physical site). Since FindNearestStationsForKind can only
	// resolve ONE station per (point, kind), lumping both into a single
	// "dc" lookup would make BOTH tariffs target the same single IRVE row
	// — confirmed in production: a co-located CHAdeMO row kept the dc
	// tariff while its sibling CCS row got none. A separate lookup per
	// (kind, ConnectorType) lets each land on its own matching row instead;
	// sources that never set ConnectorType (Electra, Izivia, ChargeNow,
	// Tesla) end up with exactly one group per kind, same as before.
	type kindConnKey struct{ kind, connectorType string }
	groupIdxs := map[kindConnKey][]int{}
	groupPoints := map[kindConnKey][]repository.NearestStationQuery{}
	for i, item := range items {
		seen := map[kindConnKey]bool{}
		for _, t := range item.Tariffs {
			if t.Kind != domain.TariffKindAC && t.Kind != domain.TariffKindDC {
				continue
			}
			key := kindConnKey{kind: t.Kind, connectorType: t.ConnectorType}
			if seen[key] {
				continue
			}
			seen[key] = true

			point := points[i]
			if t.Kind == domain.TariffKindAC {
				point.TargetPowerKW = item.ACPowerKW
			} else {
				point.TargetPowerKW = item.DCPowerKW
			}
			groupIdxs[key] = append(groupIdxs[key], i)
			groupPoints[key] = append(groupPoints[key], point)
		}
	}

	resolved := map[kindConnKey]map[int]repository.NearestStationCandidate{}
	for key, pts := range groupPoints {
		byLocalIdx, err := txLinks.FindNearestStationsForKind(ctx, pts, key.kind, key.connectorType, maxDistanceM)
		if err != nil {
			return 0, err
		}
		idxs := groupIdxs[key]
		byIdx := make(map[int]repository.NearestStationCandidate, len(byLocalIdx))
		for local, candidate := range byLocalIdx {
			byIdx[idxs[local]] = candidate
		}
		resolved[key] = byIdx
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
		// actually resolved to gets its own link row — usually one (every
		// tariff lands on the same station), occasionally more (co-located
		// but distinct rows per kind and/or connector type).
		usedStations := map[uuid.UUID]repository.NearestStationCandidate{}
		if hasAny {
			usedStations[anyCandidate.StationID] = anyCandidate
		}

		for _, t := range item.Tariffs {
			candidate, ok := anyCandidate, hasAny
			if t.Kind == domain.TariffKindAC || t.Kind == domain.TariffKindDC {
				key := kindConnKey{kind: t.Kind, connectorType: t.ConnectorType}
				if c, kindOK := resolved[key][i]; kindOK {
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
