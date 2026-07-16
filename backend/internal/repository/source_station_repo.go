package repository

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"opencharge/internal/domain"
)

type SourceStationRepository struct {
	db dbtx
}

func NewSourceStationRepository(pool *pgxpool.Pool) *SourceStationRepository {
	return &SourceStationRepository{db: pool}
}

// WithTx returns a SourceStationRepository whose statements run inside tx
// instead of picking a connection from the pool per call.
func (r *SourceStationRepository) WithTx(tx pgx.Tx) *SourceStationRepository {
	return &SourceStationRepository{db: tx}
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
	err = r.db.QueryRow(ctx, query,
		s.Source, s.SourceStationID, s.Name, s.OperatorName,
		s.AddressStreet, s.AddressPostal, s.AddressCity, s.AddressCountry,
		s.Lng, s.Lat, raw,
	).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("upsert source station %s:%s: %w", s.Source, s.SourceStationID, err)
	}
	return id, nil
}

// SourceStationKey returns the natural key used to look up a station's UUID
// in BulkUpsert's result map.
func SourceStationKey(source, sourceStationID string) string {
	return source + "\x00" + sourceStationID
}

// BulkUpsert upserts many source stations in a single round trip and
// returns their UUIDs keyed by SourceStationKey(source, sourceStationID) —
// used instead of one Upsert call per station when ingesting large batches
// against a database with real network latency, where per-row round trips
// dominate wall-clock time far more than the query cost itself.
func (r *SourceStationRepository) BulkUpsert(ctx context.Context, stations []domain.SourceStation) (map[string]uuid.UUID, error) {
	if len(stations) == 0 {
		return nil, nil
	}

	// Dedupe by (source, source_station_id), keeping the last occurrence:
	// a single multi-row INSERT ON CONFLICT DO UPDATE errors ("command
	// cannot affect row a second time") if two input rows target the same
	// conflict key, unlike a loop of individual Upsert calls.
	deduped := dedupeSourceStations(stations)

	n := len(deduped)
	sources := make([]string, n)
	sourceStationIDs := make([]string, n)
	names := make([]string, n)
	operatorNames := make([]string, n)
	addressStreets := make([]string, n)
	addressPostals := make([]string, n)
	addressCities := make([]string, n)
	addressCountries := make([]string, n)
	lngs := make([]float64, n)
	lats := make([]float64, n)
	raws := make([]string, n)
	for i, s := range deduped {
		raw, err := json.Marshal(s.Raw)
		if err != nil {
			return nil, fmt.Errorf("marshal raw for %s:%s: %w", s.Source, s.SourceStationID, err)
		}
		sources[i] = s.Source
		sourceStationIDs[i] = s.SourceStationID
		names[i] = s.Name
		operatorNames[i] = s.OperatorName
		addressStreets[i] = s.AddressStreet
		addressPostals[i] = s.AddressPostal
		addressCities[i] = s.AddressCity
		addressCountries[i] = s.AddressCountry
		lngs[i] = s.Lng
		lats[i] = s.Lat
		raws[i] = string(raw)
	}

	const query = `
		INSERT INTO source_stations (
			source, source_station_id, name, operator_name,
			address_street, address_postal_code, address_city, address_country_code,
			location, raw, updated_at
		)
		SELECT s.source, s.source_station_id, s.name, s.operator_name,
			s.address_street, s.address_postal_code, s.address_city, s.address_country_code,
			ST_SetSRID(ST_MakePoint(s.lng, s.lat), 4326), s.raw::jsonb, now()
		FROM unnest(
			$1::text[], $2::text[], $3::text[], $4::text[],
			$5::text[], $6::text[], $7::text[], $8::text[],
			$9::float8[], $10::float8[], $11::text[]
		) AS s(source, source_station_id, name, operator_name,
			address_street, address_postal_code, address_city, address_country_code,
			lng, lat, raw)
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
		RETURNING id, source, source_station_id`

	rows, err := r.db.Query(ctx, query,
		sources, sourceStationIDs, names, operatorNames,
		addressStreets, addressPostals, addressCities, addressCountries,
		lngs, lats, raws,
	)
	if err != nil {
		return nil, fmt.Errorf("bulk upsert source stations: %w", err)
	}
	defer rows.Close()

	result := make(map[string]uuid.UUID, n)
	for rows.Next() {
		var id uuid.UUID
		var source, sourceStationID string
		if err := rows.Scan(&id, &source, &sourceStationID); err != nil {
			return nil, fmt.Errorf("scan bulk upsert source station: %w", err)
		}
		result[SourceStationKey(source, sourceStationID)] = id
	}
	return result, rows.Err()
}

func dedupeSourceStations(stations []domain.SourceStation) []domain.SourceStation {
	byKey := make(map[string]int, len(stations))
	deduped := make([]domain.SourceStation, 0, len(stations))
	for _, s := range stations {
		key := SourceStationKey(s.Source, s.SourceStationID)
		if idx, ok := byKey[key]; ok {
			deduped[idx] = s
			continue
		}
		byKey[key] = len(deduped)
		deduped = append(deduped, s)
	}
	return deduped
}
