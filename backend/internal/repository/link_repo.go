package repository

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type LinkRepository struct {
	db dbtx
}

func NewLinkRepository(pool *pgxpool.Pool) *LinkRepository {
	return &LinkRepository{db: pool}
}

// WithTx returns a LinkRepository whose statements run inside tx instead of
// picking a connection from the pool per call.
func (r *LinkRepository) WithTx(tx pgx.Tx) *LinkRepository {
	return &LinkRepository{db: tx}
}

// NearestStationCandidate is the closest IRVE station to a source station,
// used to decide how a StationLink should be created.
type NearestStationCandidate struct {
	StationID      uuid.UUID
	OperatorName   string
	Name           string
	DistanceMeters float64
}

// FindNearestStation returns the closest IRVE station within maxDistanceMeters
// of the given source station location, using PostGIS ST_DWithin/KNN.
func (r *LinkRepository) FindNearestStation(ctx context.Context, lat, lng, maxDistanceMeters float64) (*NearestStationCandidate, error) {
	const query = `
		SELECT id, operator_name, name,
			ST_Distance(location::geography, ST_SetSRID(ST_MakePoint($1, $2), 4326)::geography) AS distance_m
		FROM stations
		WHERE ST_DWithin(location::geography, ST_SetSRID(ST_MakePoint($1, $2), 4326)::geography, $3)
		ORDER BY location <-> ST_SetSRID(ST_MakePoint($1, $2), 4326)
		LIMIT 1`

	var c NearestStationCandidate
	err := r.db.QueryRow(ctx, query, lng, lat, maxDistanceMeters).Scan(&c.StationID, &c.OperatorName, &c.Name, &c.DistanceMeters)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("find nearest station: %w", err)
	}
	return &c, nil
}

// NearestStationQuery is one point to resolve in a FindNearestStations
// bulk call.
type NearestStationQuery struct {
	Lat, Lng float64
}

// FindNearestStations resolves the closest IRVE station within
// maxDistanceMeters for every point in a single round trip, instead of one
// FindNearestStation call (and one geospatial index scan) per point. The
// result is keyed by the point's index in points; a point with no
// candidate within range is simply absent from the map.
func (r *LinkRepository) FindNearestStations(ctx context.Context, points []NearestStationQuery, maxDistanceMeters float64) (map[int]NearestStationCandidate, error) {
	if len(points) == 0 {
		return nil, nil
	}

	lngs := make([]float64, len(points))
	lats := make([]float64, len(points))
	idxs := make([]int32, len(points))
	for i, p := range points {
		lngs[i] = p.Lng
		lats[i] = p.Lat
		idxs[i] = int32(i)
	}

	// LEFT JOIN LATERAL (not CROSS JOIN) so a point with no candidate in
	// range still produces a row (with NULL station columns) instead of
	// disappearing from the result — otherwise "no neighbor" would be
	// indistinguishable from "never queried".
	const query = `
		SELECT q.idx, s.id, s.operator_name, s.name, s.distance_m
		FROM unnest($1::float8[], $2::float8[], $3::int[]) AS q(lng, lat, idx)
		LEFT JOIN LATERAL (
			SELECT st.id, st.operator_name, st.name,
				ST_Distance(st.location::geography, ST_SetSRID(ST_MakePoint(q.lng, q.lat), 4326)::geography) AS distance_m
			FROM stations st
			WHERE ST_DWithin(st.location::geography, ST_SetSRID(ST_MakePoint(q.lng, q.lat), 4326)::geography, $4)
			ORDER BY st.location <-> ST_SetSRID(ST_MakePoint(q.lng, q.lat), 4326)
			LIMIT 1
		) s ON true`

	rows, err := r.db.Query(ctx, query, lngs, lats, idxs, maxDistanceMeters)
	if err != nil {
		return nil, fmt.Errorf("find nearest stations (bulk): %w", err)
	}
	defer rows.Close()

	result := map[int]NearestStationCandidate{}
	for rows.Next() {
		var idx int32
		var id *uuid.UUID
		var operatorName, name *string
		var distance *float64
		if err := rows.Scan(&idx, &id, &operatorName, &name, &distance); err != nil {
			return nil, fmt.Errorf("scan nearest station (bulk): %w", err)
		}
		if id == nil {
			continue
		}
		c := NearestStationCandidate{StationID: *id, DistanceMeters: *distance}
		if operatorName != nil {
			c.OperatorName = *operatorName
		}
		if name != nil {
			c.Name = *name
		}
		result[int(idx)] = c
	}
	return result, rows.Err()
}

// Upsert creates or refreshes the correlation between an IRVE station and a
// source station.
func (r *LinkRepository) Upsert(ctx context.Context, stationID, sourceStationID uuid.UUID, source, linkQuality string, distanceMeters *float64) error {
	const query = `
		INSERT INTO station_links (station_id, source_station_id, source, link_quality, distance_meters)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (station_id, source_station_id) DO UPDATE SET
			link_quality = EXCLUDED.link_quality,
			distance_meters = EXCLUDED.distance_meters`

	if _, err := r.db.Exec(ctx, query, stationID, sourceStationID, source, linkQuality, distanceMeters); err != nil {
		return fmt.Errorf("upsert station link: %w", err)
	}
	return nil
}

// LinkUpsert is one link to write in a BulkUpsert call.
type LinkUpsert struct {
	StationID       uuid.UUID
	SourceStationID uuid.UUID
	Source          string
	LinkQuality     string
	DistanceMeters  float64
}

// BulkUpsert writes many links in a single round trip. Each
// (StationID, SourceStationID) pair is unique per call by construction
// (SourceStationID is a freshly upserted source station's own UUID), so
// unlike tariffs there's no same-batch conflict-key collision to dedupe.
func (r *LinkRepository) BulkUpsert(ctx context.Context, links []LinkUpsert) error {
	if len(links) == 0 {
		return nil
	}

	n := len(links)
	stationIDs := make([]uuid.UUID, n)
	sourceStationIDs := make([]uuid.UUID, n)
	sources := make([]string, n)
	qualities := make([]string, n)
	distances := make([]float64, n)
	for i, l := range links {
		stationIDs[i] = l.StationID
		sourceStationIDs[i] = l.SourceStationID
		sources[i] = l.Source
		qualities[i] = l.LinkQuality
		distances[i] = l.DistanceMeters
	}

	const query = `
		INSERT INTO station_links (station_id, source_station_id, source, link_quality, distance_meters)
		SELECT * FROM unnest($1::uuid[], $2::uuid[], $3::text[], $4::text[], $5::float8[])
		ON CONFLICT (station_id, source_station_id) DO UPDATE SET
			link_quality = EXCLUDED.link_quality,
			distance_meters = EXCLUDED.distance_meters`

	if _, err := r.db.Exec(ctx, query, stationIDs, sourceStationIDs, sources, qualities, distances); err != nil {
		return fmt.Errorf("bulk upsert station links: %w", err)
	}
	return nil
}
