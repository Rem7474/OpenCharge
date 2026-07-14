package repository

import (
	"context"
	"encoding/json"
	"fmt"

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

func (r *TariffRepository) Insert(ctx context.Context, t *domain.StationTariff) error {
	extra, _ := json.Marshal(t.Extra)
	_, err := r.db.Exec(ctx, `
		INSERT INTO station_tariffs (
			station_id, source, kind, model, currency,
			energy_price_cents_per_kwh, session_price_cents_per_min, congestion_price_cents_per_min,
			service_fee_percent, valid_from, valid_to, raw_text, extra
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13::jsonb)
		ON CONFLICT DO NOTHING
	`,
		t.StationID, t.Source, t.Kind, t.Model, t.Currency,
		t.EnergyPriceCentsPerKwh, t.SessionPriceCentsPerMin, t.CongestionPriceCentsPerMin,
		t.ServiceFeePercent, t.ValidFrom, t.ValidTo, t.RawText, string(extra),
	)
	return err
}

func (r *TariffRepository) FindByStationID(ctx context.Context, stationID uuid.UUID) ([]*domain.StationTariff, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id, station_id, source, kind, model, currency,
		       energy_price_cents_per_kwh, session_price_cents_per_min, congestion_price_cents_per_min,
		       service_fee_percent, valid_from, valid_to, raw_text, extra, created_at, updated_at
		FROM station_tariffs WHERE station_id = $1
	`, stationID)
	if err != nil {
		return nil, fmt.Errorf("FindByStationID: %w", err)
	}
	defer rows.Close()
	var tariffs []*domain.StationTariff
	for rows.Next() {
		var t domain.StationTariff
		var extra []byte
		err := rows.Scan(
			&t.ID, &t.StationID, &t.Source, &t.Kind, &t.Model, &t.Currency,
			&t.EnergyPriceCentsPerKwh, &t.SessionPriceCentsPerMin, &t.CongestionPriceCentsPerMin,
			&t.ServiceFeePercent, &t.ValidFrom, &t.ValidTo, &t.RawText, &extra,
			&t.CreatedAt, &t.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		t.Extra = extra
		tariffs = append(tariffs, &t)
	}
	return tariffs, rows.Err()
}

// PricingSummary contient le prix minimum AC/DC pour une station.
type PricingSummary struct {
	HasTariffs         bool      `json:"hasTariffs"`
	TariffSources      []string  `json:"tariffSources"`
	ACMinCentsPerKwh   *float64  `json:"ac_min_cents_per_kwh,omitempty"`
	DCMinCentsPerKwh   *float64  `json:"dc_min_cents_per_kwh,omitempty"`
}

func (r *TariffRepository) SummaryByStationID(ctx context.Context, stationID uuid.UUID) (*PricingSummary, error) {
	rows, err := r.db.Query(ctx, `
		SELECT kind, source, MIN(energy_price_cents_per_kwh)
		FROM station_tariffs
		WHERE station_id = $1 AND energy_price_cents_per_kwh IS NOT NULL
		GROUP BY kind, source
	`, stationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	summary := &PricingSummary{}
	sourceSet := map[string]bool{}
	for rows.Next() {
		var kind, source string
		var minPrice float64
		if err := rows.Scan(&kind, &source, &minPrice); err != nil {
			continue
		}
		summary.HasTariffs = true
		sourceSet[source] = true
		switch kind {
		case "ac":
			if summary.ACMinCentsPerKwh == nil || minPrice < *summary.ACMinCentsPerKwh {
				v := minPrice
				summary.ACMinCentsPerKwh = &v
			}
		case "dc":
			if summary.DCMinCentsPerKwh == nil || minPrice < *summary.DCMinCentsPerKwh {
				v := minPrice
				summary.DCMinCentsPerKwh = &v
			}
		}
	}
	for s := range sourceSet {
		summary.TariffSources = append(summary.TariffSources, s)
	}
	return summary, rows.Err()
}
