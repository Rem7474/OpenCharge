package repository

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"opencharge/internal/domain"
)

type SourceStationRepository struct {
	pool *pgxpool.Pool
}

func NewSourceStationRepository(pool *pgxpool.Pool) *SourceStationRepository {
	return &SourceStationRepository{pool: pool}
}

// Upsert inserts or updates a station from an external source, keyed by
// (source, source_station_id), and returns its internal UUID.
func (r *SourceStationRepository) Upsert(ctx context.Context, s domain.SourceStation) (uuid.UUID, error) {
	raw, err := json.Marshal(s.Raw)
	if err != nil {
		return uuid.Nil, fmt.Errorf("marshal raw: %w", err)
	}

	const query = `
		INSERT INTO source_stations (
			source, source_station_id, name, operator_name,
			address_street, address_postal_code, address_city, address_country_code,
			location, raw, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8,
			ST_SetSRID(ST_MakePoint($9, $10), 4326), $11, now()
		)
		ON CONFLICT (source, source_station_id) DO UPDATE SET
			name = EXCLUDED.name,
			operator_name = EXCLUDED.operator_name,
			address_street = EXCLUDED.address_street,
			address_postal_code = EXCLUDED.address_postal_code,
			address_city = EXCLUDED.address_city,
			address_country_code = EXCLUDED.address_country_code,
			location = EXCLUDED.location,
			raw = EXCLUDED.raw,
			updated_at = now()
		RETURNING id`

	var id uuid.UUID
	err = r.pool.QueryRow(ctx, query,
		s.Source, s.SourceStationID, s.Name, s.OperatorName,
		s.AddressStreet, s.AddressPostal, s.AddressCity, s.AddressCountry,
		s.Lng, s.Lat, raw,
	).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("upsert source station %s:%s: %w", s.Source, s.SourceStationID, err)
	}
	return id, nil
}
