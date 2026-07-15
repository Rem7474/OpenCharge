package repository

import (
	"context"

	"github.com/Rem7474/opencharge/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type TariffRepository struct {
	db *pgxpool.Pool
}

func NewTariffRepository(db *pgxpool.Pool) *TariffRepository {
	return &TariffRepository{db: db}
}

// Upsert inserts a tariff (no natural unique key, so we delete+insert by station+source+kind).
func (r *TariffRepository) Upsert(ctx context.Context, t *domain.StationTariff) error {
	query := `
		INSERT INTO station_tariffs (
			station_id, source, kind, model, currency,
			energy_price_cents_per_kwh, session_price_cents_per_min,
			congestion_price_cents_per_min, service_fee_percent,
			valid_from, valid_to, raw_text, extra, updated_at
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,NOW()
		)
	`
	_, err := r.db.Exec(ctx, query,
		t.StationID, t.Source, t.Kind, t.Model, t.Currency,
		t.EnergyPriceCentsPerKwh, t.SessionPriceCentsPerMin,
		t.CongestionPriceCentsPerMin, t.ServiceFeePercent,
		t.ValidFrom, t.ValidTo, t.RawText, t.Extra,
	)
	return err
}

// DeleteByStationAndSource removes existing tariffs before re-inserting (idempotent ingestion).
func (r *TariffRepository) DeleteByStationAndSource(ctx context.Context, stationID uuid.UUID, source string) error {
	_, err := r.db.Exec(ctx,
		"DELETE FROM station_tariffs WHERE station_id = $1 AND source = $2",
		stationID, source,
	)
	return err
}

// GetByStationID returns all tariffs for a given station.
func (r *TariffRepository) GetByStationID(ctx context.Context, stationID uuid.UUID) ([]domain.StationTariff, error) {
	query := `
		SELECT
			id, station_id, source, kind, model, currency,
			energy_price_cents_per_kwh, session_price_cents_per_min,
			congestion_price_cents_per_min, service_fee_percent,
			valid_from, valid_to, raw_text, extra, created_at, updated_at
		FROM station_tariffs
		WHERE station_id = $1
		ORDER BY source, kind
	`
	rows, err := r.db.Query(ctx, query, stationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tariffs []domain.StationTariff
	for rows.Next() {
		var t domain.StationTariff
		err := rows.Scan(
			&t.ID, &t.StationID, &t.Source, &t.Kind, &t.Model, &t.Currency,
			&t.EnergyPriceCentsPerKwh, &t.SessionPriceCentsPerMin,
			&t.CongestionPriceCentsPerMin, &t.ServiceFeePercent,
			&t.ValidFrom, &t.ValidTo, &t.RawText, &t.Extra, &t.CreatedAt, &t.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		tariffs = append(tariffs, t)
	}
	return tariffs, rows.Err()
}
