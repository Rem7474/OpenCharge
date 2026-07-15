package repository

import (
	"context"

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

// UpsertSourceStation inserts or updates a source station.
func (r *LinkRepository) UpsertSourceStation(ctx context.Context, ss *domain.SourceStation) (uuid.UUID, error) {
	var id uuid.UUID
	query := `
		INSERT INTO source_stations (
			source, source_station_id, name, operator_name,
			address_street, address_postal_code, address_city, address_country_code,
			location, raw, updated_at
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,
			CASE WHEN $9::float IS NOT NULL AND $10::float IS NOT NULL
				THEN ST_SetSRID(ST_MakePoint($9,$10),4326)
				ELSE NULL END,
			$11, NOW()
		)
		ON CONFLICT (source, source_station_id) DO UPDATE SET
			name               = EXCLUDED.name,
			operator_name      = EXCLUDED.operator_name,
			address_street     = EXCLUDED.address_street,
			address_postal_code = EXCLUDED.address_postal_code,
			address_city       = EXCLUDED.address_city,
			location           = EXCLUDED.location,
			raw                = EXCLUDED.raw,
			updated_at         = NOW()
		RETURNING id
	`
	err := r.db.QueryRow(ctx, query,
		ss.Source, ss.SourceStationID, ss.Name, ss.OperatorName,
		ss.AddressStreet, ss.AddressPostalCode, ss.AddressCity, ss.AddressCountryCode,
		ss.Lng, ss.Lat, ss.Raw,
	).Scan(&id)
	return id, err
}

// UpsertLink inserts or ignores a correlation link.
func (r *LinkRepository) UpsertLink(ctx context.Context, link *domain.StationLink) error {
	query := `
		INSERT INTO station_links (station_id, source_station_id, source, link_quality)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (station_id, source_station_id) DO UPDATE SET
			link_quality = EXCLUDED.link_quality
	`
	_, err := r.db.Exec(ctx, query, link.StationID, link.SourceStationID, link.Source, link.LinkQuality)
	return err
}

// GetLinksByStationID returns all external links for a given IRVE station.
func (r *LinkRepository) GetLinksByStationID(ctx context.Context, stationID uuid.UUID) ([]domain.StationLink, error) {
	rows, err := r.db.Query(ctx,
		"SELECT id, station_id, source_station_id, source, link_quality, created_at FROM station_links WHERE station_id = $1",
		stationID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var links []domain.StationLink
	for rows.Next() {
		var l domain.StationLink
		if err := rows.Scan(&l.ID, &l.StationID, &l.SourceStationID, &l.Source, &l.LinkQuality, &l.CreatedAt); err != nil {
			return nil, err
		}
		links = append(links, l)
	}
	return links, rows.Err()
}
