package repository

import (
	"context"
	"fmt"

	"github.com/Rem7474/opencharge/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type LinkRepository struct {
	db *pgxpool.Pool
}

func NewLinkRepository(db *pgxpool.Pool) *LinkRepository {
	return &LinkRepository{db: db}
}

func (r *LinkRepository) Upsert(ctx context.Context, l *domain.StationLink) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO station_links (station_id, source_station_id, source, link_quality)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT (station_id, source_station_id) DO UPDATE SET
			link_quality = EXCLUDED.link_quality
	`, l.StationID, l.SourceStationID, l.Source, string(l.LinkQuality))
	return err
}

func (r *LinkRepository) FindByStationID(ctx context.Context, stationID uuid.UUID) ([]*domain.StationLink, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id, station_id, source_station_id, source, link_quality, created_at
		FROM station_links WHERE station_id = $1
	`, stationID)
	if err != nil {
		return nil, fmt.Errorf("FindByStationID links: %w", err)
	}
	defer rows.Close()
	var links []*domain.StationLink
	for rows.Next() {
		var l domain.StationLink
		if err := rows.Scan(&l.ID, &l.StationID, &l.SourceStationID, &l.Source, &l.LinkQuality, &l.CreatedAt); err != nil {
			return nil, err
		}
		links = append(links, &l)
	}
	return links, rows.Err()
}

// UpsertSourceStation insère ou met à jour une source_station et retourne son UUID interne.
func (r *LinkRepository) UpsertSourceStation(ctx context.Context, ss *domain.SourceStation) (uuid.UUID, error) {
	var id uuid.UUID
	err := r.db.QueryRow(ctx, `
		INSERT INTO source_stations (
			source, source_station_id, name, operator_name,
			address_street, address_postal_code, address_city, address_country_code,
			location, raw
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,ST_SetSRID(ST_MakePoint($9,$10),4326),$11::jsonb)
		ON CONFLICT (source, source_station_id) DO UPDATE SET
			name              = EXCLUDED.name,
			operator_name     = EXCLUDED.operator_name,
			location          = EXCLUDED.location,
			raw               = EXCLUDED.raw,
			updated_at        = now()
		RETURNING id
	`, ss.Source, ss.SourceStationID, ss.Name, ss.OperatorName,
		ss.AddressStreet, ss.AddressPostalCode, ss.AddressCity, ss.AddressCountryCode,
		ss.Lng, ss.Lat, string(ss.Raw),
	).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("UpsertSourceStation: %w", err)
	}
	return id, nil
}

// FindNearestStation retourne l'UUID et l'irve_id_pdc de la station IRVE la plus proche
// dans un rayon en mètres.
func (r *LinkRepository) FindNearestStation(ctx context.Context, lng, lat, radiusMeters float64) (uuid.UUID, string, error) {
	var id uuid.UUID
	var irvePDC string
	err := r.db.QueryRow(ctx, `
		SELECT id, irve_id_pdc
		FROM stations
		WHERE ST_DWithin(
			location::geography,
			ST_SetSRID(ST_MakePoint($1,$2),4326)::geography,
			$3
		)
		ORDER BY location::geography <-> ST_SetSRID(ST_MakePoint($1,$2),4326)::geography
		LIMIT 1
	`, lng, lat, radiusMeters).Scan(&id, &irvePDC)
	if err != nil {
		return uuid.Nil, "", err
	}
	return id, irvePDC, nil
}
