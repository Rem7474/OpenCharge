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

type StationRepository struct {
	pool *pgxpool.Pool
}

func NewStationRepository(pool *pgxpool.Pool) *StationRepository {
	return &StationRepository{pool: pool}
}

// UpsertStation inserts or updates an IRVE point of charge, keyed by
// irve_id_pdc, and returns its internal UUID.
func (r *StationRepository) UpsertStation(ctx context.Context, s domain.Station) (uuid.UUID, error) {
	metadata, err := json.Marshal(s.Metadata)
	if err != nil {
		return uuid.Nil, fmt.Errorf("marshal metadata: %w", err)
	}

	const query = `
		INSERT INTO stations (
			irve_id_station, irve_id_pdc, operator_name, amenageur, enseigne, name,
			address_street, address_postal_code, address_city, address_country_code,
			location, power_kw, connector_type, access_type, is_24_7, metadata, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10,
			ST_SetSRID(ST_MakePoint($11, $12), 4326), $13, $14, $15, $16, $17, now()
		)
		ON CONFLICT (irve_id_pdc) DO UPDATE SET
			irve_id_station = EXCLUDED.irve_id_station,
			operator_name = EXCLUDED.operator_name,
			amenageur = EXCLUDED.amenageur,
			enseigne = EXCLUDED.enseigne,
			name = EXCLUDED.name,
			address_street = EXCLUDED.address_street,
			address_postal_code = EXCLUDED.address_postal_code,
			address_city = EXCLUDED.address_city,
			address_country_code = EXCLUDED.address_country_code,
			location = EXCLUDED.location,
			power_kw = EXCLUDED.power_kw,
			connector_type = EXCLUDED.connector_type,
			access_type = EXCLUDED.access_type,
			is_24_7 = EXCLUDED.is_24_7,
			metadata = EXCLUDED.metadata,
			updated_at = now()
		RETURNING id`

	var id uuid.UUID
	err = r.pool.QueryRow(ctx, query,
		s.IRVEIDStation, s.IRVEIDPDC, s.OperatorName, s.Amenageur, s.Enseigne, s.Name,
		s.AddressStreet, s.AddressPostal, s.AddressCity, s.AddressCountry,
		s.Lng, s.Lat, s.PowerKW, s.ConnectorType, s.AccessType, s.Is24_7, metadata,
	).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("upsert station %s: %w", s.IRVEIDPDC, err)
	}
	return id, nil
}

const stationListSelectPrefix = `
	SELECT
		s.id, s.irve_id_station, s.irve_id_pdc, s.operator_name, s.amenageur, s.enseigne, s.name,
		s.address_street, s.address_postal_code, s.address_city, s.address_country_code,
		ST_Y(s.location), ST_X(s.location), s.power_kw, s.connector_type, s.access_type, s.is_24_7,
		s.metadata, s.created_at, s.updated_at,
		COALESCE(array_agg(DISTINCT t.source) FILTER (WHERE t.source IS NOT NULL), '{}'),
		MIN(t.energy_price_cents_per_kwh) FILTER (WHERE t.kind = 'ac'),
		MIN(t.energy_price_cents_per_kwh) FILTER (WHERE t.kind = 'dc')`

const stationListFrom = `
	FROM stations s
	LEFT JOIN station_tariffs t ON t.station_id = s.id`
// BulkUpsertStations upserts a slice of IRVE stations in a single transaction.
func (r *StationRepository) BulkUpsertStations(ctx context.Context, stations []domain.Station) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin bulk upsert tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	const query = `
		INSERT INTO stations (
			irve_id_station, irve_id_pdc, operator_name, amenageur, enseigne, name,
			address_street, address_postal_code, address_city, address_country_code,
			location, power_kw, connector_type, access_type, is_24_7, metadata, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10,
			ST_SetSRID(ST_MakePoint($11, $12), 4326), $13, $14, $15, $16, $17, now()
		)
		ON CONFLICT (irve_id_pdc) DO UPDATE SET
			irve_id_station = EXCLUDED.irve_id_station,
			operator_name = EXCLUDED.operator_name,
			amenageur = EXCLUDED.amenageur,
			enseigne = EXCLUDED.enseigne,
			name = EXCLUDED.name,
			address_street = EXCLUDED.address_street,
			address_postal_code = EXCLUDED.address_postal_code,
			address_city = EXCLUDED.address_city,
			address_country_code = EXCLUDED.address_country_code,
			location = EXCLUDED.location,
			power_kw = EXCLUDED.power_kw,
			connector_type = EXCLUDED.connector_type,
			access_type = EXCLUDED.access_type,
			is_24_7 = EXCLUDED.is_24_7,
			metadata = EXCLUDED.metadata,
			updated_at = now()`

	for _, s := range stations {
		metadata, err := json.Marshal(s.Metadata)
		if err != nil {
			return fmt.Errorf("marshal metadata for %s: %w", s.IRVEIDPDC, err)
		}
		_, err = tx.Exec(ctx, query,
			s.IRVEIDStation, s.IRVEIDPDC, s.OperatorName, s.Amenageur, s.Enseigne, s.Name,
			s.AddressStreet, s.AddressPostal, s.AddressCity, s.AddressCountry,
			s.Lng, s.Lat, s.PowerKW, s.ConnectorType, s.AccessType, s.Is24_7, metadata,
		)
		if err != nil {
			return fmt.Errorf("exec upsert %s: %w", s.IRVEIDPDC, err)
		}
	}

	return tx.Commit(ctx)
}
// ListByBBox returns stations intersecting the given bounding box, with an
// aggregated tariff summary per station. It never scans the whole table:
// callers must always provide a bbox.
func (r *StationRepository) ListByBBox(ctx context.Context, f domain.StationFilter) ([]domain.StationSummary, error) {
	limit := f.Limit
	if limit <= 0 || limit > 2000 {
		limit = 500
	}

	args := []any{f.MinLng, f.MinLat, f.MaxLng, f.MaxLat}

	// SelectedSourcesPricing never filters the result set (a station
	// without a tariff from f.Sources must still be returned, so the map
	// can gray it out instead of hiding it) — it's purely an extra
	// aggregate computed alongside the global min price. f.Sources holds
	// "source:plan" pairs (e.g. "electra:subscription"), matched against
	// the tariff's own source/plan joined the same way.
	args = append(args, f.Sources)
	sourcesParamIdx := len(args)
	query := stationListSelectPrefix + fmt.Sprintf(`,
		MIN(t.energy_price_cents_per_kwh) FILTER (WHERE t.kind = 'ac' AND (t.source || ':' || t.plan) = ANY($%d::text[])),
		MIN(t.energy_price_cents_per_kwh) FILTER (WHERE t.kind = 'dc' AND (t.source || ':' || t.plan) = ANY($%d::text[]))`, sourcesParamIdx, sourcesParamIdx)
	query += stationListFrom + `
		WHERE s.location && ST_MakeEnvelope($1, $2, $3, $4, 4326)`

	if f.Operator != "" {
		args = append(args, f.Operator)
		query += fmt.Sprintf(" AND s.operator_name = $%d", len(args))
	}

	query += `
		GROUP BY s.id`

	if f.HasTariffs != nil && *f.HasTariffs {
		query += ` HAVING COUNT(t.id) > 0`
	}

	args = append(args, limit, f.Offset)
	query += fmt.Sprintf(" ORDER BY s.id LIMIT $%d OFFSET $%d", len(args)-1, len(args))

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list stations by bbox: %w", err)
	}
	defer rows.Close()

	hasSourcesFilter := len(f.Sources) > 0
	var results []domain.StationSummary
	for rows.Next() {
		summary, err := scanStationSummary(rows, hasSourcesFilter)
		if err != nil {
			return nil, err
		}
		results = append(results, summary)
	}
	return results, rows.Err()
}

func (r *StationRepository) GetByIRVEID(ctx context.Context, irveID string) (*domain.Station, error) {
	const query = `
		SELECT id, irve_id_station, irve_id_pdc, operator_name, amenageur, enseigne, name,
			address_street, address_postal_code, address_city, address_country_code,
			ST_Y(location), ST_X(location), power_kw, connector_type, access_type, is_24_7,
			metadata, created_at, updated_at
		FROM stations WHERE irve_id_pdc = $1`

	var s domain.Station
	var metadata []byte
	err := r.pool.QueryRow(ctx, query, irveID).Scan(
		&s.ID, &s.IRVEIDStation, &s.IRVEIDPDC, &s.OperatorName, &s.Amenageur, &s.Enseigne, &s.Name,
		&s.AddressStreet, &s.AddressPostal, &s.AddressCity, &s.AddressCountry,
		&s.Lat, &s.Lng, &s.PowerKW, &s.ConnectorType, &s.AccessType, &s.Is24_7,
		&metadata, &s.CreatedAt, &s.UpdatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get station %s: %w", irveID, err)
	}
	_ = json.Unmarshal(metadata, &s.Metadata)
	return &s, nil
}

func scanStationSummary(rows pgx.Rows, hasSourcesFilter bool) (domain.StationSummary, error) {
	var s domain.Station
	var metadata []byte
	var tariffSources []string
	var acMin, dcMin, selectedAcMin, selectedDcMin *float64

	err := rows.Scan(
		&s.ID, &s.IRVEIDStation, &s.IRVEIDPDC, &s.OperatorName, &s.Amenageur, &s.Enseigne, &s.Name,
		&s.AddressStreet, &s.AddressPostal, &s.AddressCity, &s.AddressCountry,
		&s.Lat, &s.Lng, &s.PowerKW, &s.ConnectorType, &s.AccessType, &s.Is24_7,
		&metadata, &s.CreatedAt, &s.UpdatedAt,
		&tariffSources, &acMin, &dcMin, &selectedAcMin, &selectedDcMin,
	)
	if err != nil {
		return domain.StationSummary{}, fmt.Errorf("scan station summary: %w", err)
	}
	_ = json.Unmarshal(metadata, &s.Metadata)

	connectors := []domain.Connector{}
	if s.ConnectorType != "" {
		connectors = append(connectors, domain.Connector{
			Kind:       s.ConnectorType,
			MaxPowerKW: s.PowerKW,
			Count:      1,
		})
	}

	summary := domain.StationSummary{
		Station:       s,
		Connectors:    connectors,
		HasTariffs:    len(tariffSources) > 0,
		TariffSources: tariffSources,
		PricingSummary: domain.PricingSummary{
			ACMinCentsPerKWh: acMin,
			DCMinCentsPerKWh: dcMin,
		},
	}
	if hasSourcesFilter {
		summary.SelectedSourcesPricing = &domain.PricingSummary{
			ACMinCentsPerKWh: selectedAcMin,
			DCMinCentsPerKWh: selectedDcMin,
		}
	}
	return summary, nil
}
