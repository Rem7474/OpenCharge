package repository

import (
	"context"
	"encoding/json"
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

// Upsert insère ou met à jour une station IRVE (clé = irve_id_pdc).
func (r *StationRepository) Upsert(ctx context.Context, s *domain.Station) error {
	meta, err := json.Marshal(s.Metadata)
	if err != nil {
		meta = []byte("{}")
	}
	_, err = r.db.Exec(ctx, `
		INSERT INTO stations (
			irve_id_station, irve_id_pdc, operator_name, amenageur, enseigne, name,
			address_street, address_postal_code, address_city, address_country_code,
			location, power_kw, connector_type, access_type, is_24_7, metadata, updated_at
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,
			ST_SetSRID(ST_MakePoint($11,$12),4326),
			$13,$14,$15,$16,$17::jsonb,now()
		)
		ON CONFLICT (irve_id_pdc) DO UPDATE SET
			irve_id_station      = EXCLUDED.irve_id_station,
			operator_name        = EXCLUDED.operator_name,
			amenageur            = EXCLUDED.amenageur,
			enseigne             = EXCLUDED.enseigne,
			name                 = EXCLUDED.name,
			address_street       = EXCLUDED.address_street,
			address_postal_code  = EXCLUDED.address_postal_code,
			address_city         = EXCLUDED.address_city,
			address_country_code = EXCLUDED.address_country_code,
			location             = EXCLUDED.location,
			power_kw             = EXCLUDED.power_kw,
			connector_type       = EXCLUDED.connector_type,
			access_type          = EXCLUDED.access_type,
			is_24_7              = EXCLUDED.is_24_7,
			metadata             = EXCLUDED.metadata,
			updated_at           = now()
	`,
		s.IRVEIDStation, s.IRVEIDPDCc, s.OperatorName, s.Amenageur, s.Enseigne, s.Name,
		s.AddressStreet, s.AddressPostalCode, s.AddressCity, s.AddressCountryCode,
		s.Lng, s.Lat,
		s.PowerKw, s.ConnectorType, s.AccessType, s.Is247, string(meta),
	)
	return err
}

// FindByBbox retourne les stations dans une bounding box (minLng, minLat, maxLng, maxLat).
func (r *StationRepository) FindByBbox(ctx context.Context, minLng, minLat, maxLng, maxLat float64, limit, offset int) ([]*domain.Station, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id, irve_id_station, irve_id_pdc, operator_name, amenageur, enseigne, name,
		       address_street, address_postal_code, address_city, address_country_code,
		       ST_Y(location::geometry) AS lat, ST_X(location::geometry) AS lng,
		       power_kw, connector_type, access_type, is_24_7, metadata, created_at, updated_at
		FROM stations
		WHERE ST_Within(location, ST_MakeEnvelope($1,$2,$3,$4,4326))
		LIMIT $5 OFFSET $6
	`, minLng, minLat, maxLng, maxLat, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("FindByBbox query: %w", err)
	}
	defer rows.Close()

	var stations []*domain.Station
	for rows.Next() {
		var s domain.Station
		var meta []byte
		err := rows.Scan(
			&s.ID, &s.IRVEIDStation, &s.IRVEIDPDCc, &s.OperatorName, &s.Amenageur, &s.Enseigne, &s.Name,
			&s.AddressStreet, &s.AddressPostalCode, &s.AddressCity, &s.AddressCountryCode,
			&s.Lat, &s.Lng,
			&s.PowerKw, &s.ConnectorType, &s.AccessType, &s.Is247, &meta,
			&s.CreatedAt, &s.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("FindByBbox scan: %w", err)
		}
		s.Metadata = meta
		stations = append(stations, &s)
	}
	return stations, rows.Err()
}

// FindByIRVEPDC retourne une station par son identifiant irve_id_pdc.
func (r *StationRepository) FindByIRVEPDC(ctx context.Context, irvePDC string) (*domain.Station, error) {
	var s domain.Station
	var meta []byte
	err := r.db.QueryRow(ctx, `
		SELECT id, irve_id_station, irve_id_pdc, operator_name, amenageur, enseigne, name,
		       address_street, address_postal_code, address_city, address_country_code,
		       ST_Y(location::geometry) AS lat, ST_X(location::geometry) AS lng,
		       power_kw, connector_type, access_type, is_24_7, metadata, created_at, updated_at
		FROM stations WHERE irve_id_pdc = $1
	`, irvePDC).Scan(
		&s.ID, &s.IRVEIDStation, &s.IRVEIDPDCc, &s.OperatorName, &s.Amenageur, &s.Enseigne, &s.Name,
		&s.AddressStreet, &s.AddressPostalCode, &s.AddressCity, &s.AddressCountryCode,
		&s.Lat, &s.Lng,
		&s.PowerKw, &s.ConnectorType, &s.AccessType, &s.Is247, &meta,
		&s.CreatedAt, &s.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("FindByIRVEPDC: %w", err)
	}
	s.Metadata = meta
	return &s, nil
}

// FindByID retourne une station par son UUID interne.
func (r *StationRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.Station, error) {
	var s domain.Station
	var meta []byte
	err := r.db.QueryRow(ctx, `
		SELECT id, irve_id_station, irve_id_pdc, operator_name, amenageur, enseigne, name,
		       address_street, address_postal_code, address_city, address_country_code,
		       ST_Y(location::geometry) AS lat, ST_X(location::geometry) AS lng,
		       power_kw, connector_type, access_type, is_24_7, metadata, created_at, updated_at
		FROM stations WHERE id = $1
	`, id).Scan(
		&s.ID, &s.IRVEIDStation, &s.IRVEIDPDCc, &s.OperatorName, &s.Amenageur, &s.Enseigne, &s.Name,
		&s.AddressStreet, &s.AddressPostalCode, &s.AddressCity, &s.AddressCountryCode,
		&s.Lat, &s.Lng,
		&s.PowerKw, &s.ConnectorType, &s.AccessType, &s.Is247, &meta,
		&s.CreatedAt, &s.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("FindByID: %w", err)
	}
	s.Metadata = meta
	return &s, nil
}
