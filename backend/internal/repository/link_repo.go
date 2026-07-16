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
