package repository

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"opencharge/internal/domain"
)

type TariffRepository struct {
	pool *pgxpool.Pool
}

func NewTariffRepository(pool *pgxpool.Pool) *TariffRepository {
	return &TariffRepository{pool: pool}
}

// Upsert inserts or refreshes a tariff for (station, source, kind).
func (r *TariffRepository) Upsert(ctx context.Context, t domain.StationTariff) error {
	extra, err := json.Marshal(t.Extra)
	if err != nil {
		return fmt.Errorf("marshal extra: %w", err)
	}

	const query = `
		INSERT INTO station_tariffs (
			station_id, source, kind, model, currency,
			energy_price_cents_per_kwh, session_price_cents_per_min, congestion_price_cents_per_min,
			service_fee_percent, valid_from, valid_to, raw_text, extra, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, now())
		ON CONFLICT (station_id, source, kind) DO UPDATE SET
			model = EXCLUDED.model,
			currency = EXCLUDED.currency,
			energy_price_cents_per_kwh = EXCLUDED.energy_price_cents_per_kwh,
			session_price_cents_per_min = EXCLUDED.session_price_cents_per_min,
			congestion_price_cents_per_min = EXCLUDED.congestion_price_cents_per_min,
			service_fee_percent = EXCLUDED.service_fee_percent,
			valid_from = EXCLUDED.valid_from,
			valid_to = EXCLUDED.valid_to,
			raw_text = EXCLUDED.raw_text,
			extra = EXCLUDED.extra,
			updated_at = now()`

	_, err = r.pool.Exec(ctx, query,
		t.StationID, t.Source, t.Kind, t.Model, t.Currency,
		t.EnergyPriceCentsPerKWh, t.SessionPriceCentsPerMin, t.CongestionPriceCentsPerMin,
		t.ServiceFeePercent, t.ValidFrom, t.ValidTo, t.RawText, extra,
	)
	if err != nil {
		return fmt.Errorf("upsert station tariff: %w", err)
	}
	return nil
}

// ListDistinctSources returns every tariff source currently ingested (e.g.
// "izivia", "electra"), so the frontend can build its operator filter from
// what actually exists instead of a hardcoded list.
func (r *TariffRepository) ListDistinctSources(ctx context.Context) ([]string, error) {
	rows, err := r.pool.Query(ctx, `SELECT DISTINCT source FROM station_tariffs ORDER BY source`)
	if err != nil {
		return nil, fmt.Errorf("list distinct tariff sources: %w", err)
	}
	defer rows.Close()

	sources := []string{}
	for rows.Next() {
		var source string
		if err := rows.Scan(&source); err != nil {
			return nil, fmt.Errorf("scan tariff source: %w", err)
		}
		sources = append(sources, source)
	}
	return sources, rows.Err()
}

// ListByStation returns all tariffs attached to an IRVE station.
func (r *TariffRepository) ListByStation(ctx context.Context, stationID uuid.UUID) ([]domain.StationTariff, error) {
	const query = `
		SELECT id, station_id, source, kind, model, currency,
			energy_price_cents_per_kwh, session_price_cents_per_min, congestion_price_cents_per_min,
			service_fee_percent, valid_from, valid_to, raw_text, extra, created_at, updated_at
		FROM station_tariffs WHERE station_id = $1 ORDER BY source, kind`

	rows, err := r.pool.Query(ctx, query, stationID)
	if err != nil {
		return nil, fmt.Errorf("list tariffs for station %s: %w", stationID, err)
	}
	defer rows.Close()

	var tariffs []domain.StationTariff
	for rows.Next() {
		var t domain.StationTariff
		var extra []byte
		if err := rows.Scan(
			&t.ID, &t.StationID, &t.Source, &t.Kind, &t.Model, &t.Currency,
			&t.EnergyPriceCentsPerKWh, &t.SessionPriceCentsPerMin, &t.CongestionPriceCentsPerMin,
			&t.ServiceFeePercent, &t.ValidFrom, &t.ValidTo, &t.RawText, &extra, &t.CreatedAt, &t.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan tariff: %w", err)
		}
		_ = json.Unmarshal(extra, &t.Extra)
		tariffs = append(tariffs, t)
	}
	return tariffs, rows.Err()
}
