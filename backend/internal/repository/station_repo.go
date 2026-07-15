package repository

import (
	"context"
	"fmt"

	"github.com/Rem7474/opencharge/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type StationRepository struct {
	db *pgxpool.Pool
}

func NewStationRepository(db *pgxpool.Pool) *StationRepository {
	return &StationRepository{db: db}
}

// Upsert inserts or updates a station based on irve_id_pdc.
func (r *StationRepository) Upsert(ctx context.Context, s *domain.Station) error {
	query := `
		INSERT INTO stations (
			irve_id_station, irve_id_pdc, operator_name, amenageur, enseigne, name,
			address_street, address_postal_code, address_city, address_country_code,
			location, power_kw, connector_type, access_type, is_24_7, metadata, updated_at
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,
			ST_SetSRID(ST_MakePoint($11,$12),4326),
			$13,$14,$15,$16,$17,NOW()
		)
		ON CONFLICT (irve_id_pdc) DO UPDATE SET
			operator_name       = EXCLUDED.operator_name,
			amenageur           = EXCLUDED.amenageur,
			enseigne            = EXCLUDED.enseigne,
			name                = EXCLUDED.name,
			address_street      = EXCLUDED.address_street,
			address_postal_code = EXCLUDED.address_postal_code,
			address_city        = EXCLUDED.address_city,
			location            = EXCLUDED.location,
			power_kw            = EXCLUDED.power_kw,
			connector_type      = EXCLUDED.connector_type,
			access_type         = EXCLUDED.access_type,
			is_24_7             = EXCLUDED.is_24_7,
			metadata            = EXCLUDED.metadata,
			updated_at          = NOW()
	`
	_, err := r.db.Exec(ctx, query,
		s.IRVEIDStation, s.IRVEIDPDc, s.OperatorName, s.Amenageur, s.Enseigne, s.Name,
		s.AddressStreet, s.AddressPostalCode, s.AddressCity, s.AddressCountryCode,
		s.Lng, s.Lat, // ST_MakePoint(lng, lat)
		s.PowerKw, s.ConnectorType, s.AccessType, s.Is24_7, s.Metadata,
	)
	return err
}

// FindByBbox returns stations within a bounding box.
func (r *StationRepository) FindByBbox(ctx context.Context, minLng, minLat, maxLng, maxLat float64, operatorFilter *string, hasTariffs *bool, limit, offset int) ([]domain.StationListItem, error) {
	args := []any{minLng, minLat, maxLng, maxLat}
	conditions := ""
	argIdx := 5

	if operatorFilter != nil {
		conditions += fmt.Sprintf(" AND s.operator_name ILIKE $%d", argIdx)
		args = append(args, "%"+*operatorFilter+"%")
		argIdx++
	}
	if hasTariffs != nil && *hasTariffs {
		conditions += " AND EXISTS (SELECT 1 FROM station_tariffs t WHERE t.station_id = s.id)"
	}

	args = append(args, limit, offset)

	query := fmt.Sprintf(`
		SELECT
			s.id,
			s.name,
			ST_Y(s.location::geometry) AS lat,
			ST_X(s.location::geometry) AS lng,
			s.operator_name,
			s.address_street,
			s.address_city,
			s.address_postal_code,
			s.address_country_code,
			s.connector_type,
			s.power_kw,
			EXISTS (SELECT 1 FROM station_tariffs t WHERE t.station_id = s.id) AS has_tariffs,
			COALESCE(
				(SELECT array_agg(DISTINCT t.source) FROM station_tariffs t WHERE t.station_id = s.id),
				ARRAY[]::text[]
			) AS tariff_sources,
			(SELECT MIN(t.energy_price_cents_per_kwh) FROM station_tariffs t WHERE t.station_id = s.id AND t.kind = 'ac') AS ac_min,
			(SELECT MIN(t.energy_price_cents_per_kwh) FROM station_tariffs t WHERE t.station_id = s.id AND t.kind = 'dc') AS dc_min
		FROM stations s
		WHERE ST_Intersects(
			s.location,
			ST_MakeEnvelope($1, $2, $3, $4, 4326)
		)%s
		LIMIT $%d OFFSET $%d
	`, conditions, argIdx, argIdx+1)

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []domain.StationListItem
	for rows.Next() {
		var item domain.StationListItem
		var lat, lng float64
		var connType, powerKw, street, city, postal, country, opName, name *string
		var powerVal *float64
		var acMin, dcMin *float64
		var tariffSources []string
		var hasTariffsVal bool
		var idStr string

		err := rows.Scan(
			&idStr, &name, &lat, &lng, &opName,
			&street, &city, &postal, &country, &connType, &powerVal,
			&hasTariffsVal, &tariffSources, &acMin, &dcMin,
		)
		if err != nil {
			return nil, err
		}
		_ = powerKw

		item.ID = "irve:" + idStr
		item.Name = name
		item.Location = domain.LatLng{Lat: lat, Lng: lng}
		item.Operator = opName
		item.Address = domain.Address{
			Street:      street,
			City:        city,
			PostalCode:  postal,
			CountryCode: derefStr(country, "FR"),
		}
		item.HasTariffs = hasTariffsVal
		item.TariffSources = tariffSources
		if acMin != nil || dcMin != nil {
			item.PricingSummary = &domain.PricingSummary{
				ACMinCentsPerKwh: acMin,
				DCMinCentsPerKwh: dcMin,
			}
		}
		if connType != nil && powerVal != nil {
			kind := "ac"
			if *connType == "CCS" || *connType == "CHAdeMO" {
				kind = "dc"
			}
			item.Connectors = []domain.ConnectorStat{{Kind: kind, MaxPowerKw: *powerVal, Count: 1}}
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// GetByID retrieves a single station by its UUID.
func (r *StationRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.Station, error) {
	query := `
		SELECT
			id, irve_id_station, irve_id_pdc, operator_name, amenageur, enseigne, name,
			address_street, address_postal_code, address_city, address_country_code,
			ST_Y(location::geometry) AS lat,
			ST_X(location::geometry) AS lng,
			power_kw, connector_type, access_type, is_24_7, metadata,
			created_at, updated_at
		FROM stations
		WHERE id = $1
	`
	row := r.db.QueryRow(ctx, query, id)
	var s domain.Station
	err := row.Scan(
		&s.ID, &s.IRVEIDStation, &s.IRVEIDPDc, &s.OperatorName, &s.Amenageur, &s.Enseigne, &s.Name,
		&s.AddressStreet, &s.AddressPostalCode, &s.AddressCity, &s.AddressCountryCode,
		&s.Lat, &s.Lng,
		&s.PowerKw, &s.ConnectorType, &s.AccessType, &s.Is24_7, &s.Metadata,
		&s.CreatedAt, &s.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// GetByIRVEIDPDC retrieves a station by its IRVE PDC identifier.
func (r *StationRepository) GetByIRVEIDPDC(ctx context.Context, idPDC string) (*domain.Station, error) {
	query := `
		SELECT
			id, irve_id_station, irve_id_pdc, operator_name, amenageur, enseigne, name,
			address_street, address_postal_code, address_city, address_country_code,
			ST_Y(location::geometry) AS lat,
			ST_X(location::geometry) AS lng,
			power_kw, connector_type, access_type, is_24_7, metadata,
			created_at, updated_at
		FROM stations
		WHERE irve_id_pdc = $1
	`
	row := r.db.QueryRow(ctx, query, idPDC)
	var s domain.Station
	err := row.Scan(
		&s.ID, &s.IRVEIDStation, &s.IRVEIDPDc, &s.OperatorName, &s.Amenageur, &s.Enseigne, &s.Name,
		&s.AddressStreet, &s.AddressPostalCode, &s.AddressCity, &s.AddressCountryCode,
		&s.Lat, &s.Lng,
		&s.PowerKw, &s.ConnectorType, &s.AccessType, &s.Is24_7, &s.Metadata,
		&s.CreatedAt, &s.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// FindNearest finds the nearest IRVE station within maxDistanceDeg degrees.
func (r *StationRepository) FindNearest(ctx context.Context, lng, lat, maxDistanceDeg float64) (*domain.Station, error) {
	query := `
		SELECT
			id, irve_id_station, irve_id_pdc, operator_name, amenageur, enseigne, name,
			address_street, address_postal_code, address_city, address_country_code,
			ST_Y(location::geometry) AS lat,
			ST_X(location::geometry) AS lng,
			power_kw, connector_type, access_type, is_24_7, metadata,
			created_at, updated_at
		FROM stations
		WHERE ST_DWithin(
			location,
			ST_SetSRID(ST_MakePoint($1, $2), 4326),
			$3
		)
		ORDER BY location <-> ST_SetSRID(ST_MakePoint($1, $2), 4326)
		LIMIT 1
	`
	row := r.db.QueryRow(ctx, query, lng, lat, maxDistanceDeg)
	var s domain.Station
	err := row.Scan(
		&s.ID, &s.IRVEIDStation, &s.IRVEIDPDc, &s.OperatorName, &s.Amenageur, &s.Enseigne, &s.Name,
		&s.AddressStreet, &s.AddressPostalCode, &s.AddressCity, &s.AddressCountryCode,
		&s.Lat, &s.Lng,
		&s.PowerKw, &s.ConnectorType, &s.AccessType, &s.Is24_7, &s.Metadata,
		&s.CreatedAt, &s.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func derefStr(s *string, def string) string {
	if s == nil {
		return def
	}
	return *s
}
