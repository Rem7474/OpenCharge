package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"opencharge/internal/domain"
)

type TariffRepository struct {
	db dbtx
}

func NewTariffRepository(pool *pgxpool.Pool) *TariffRepository {
	return &TariffRepository{db: pool}
}

// WithTx returns a TariffRepository whose statements run inside tx instead
// of picking a connection from the pool per call.
func (r *TariffRepository) WithTx(tx pgx.Tx) *TariffRepository {
	return &TariffRepository{db: tx}
}

// Upsert inserts or refreshes a tariff for (station, source, kind, plan).
func (r *TariffRepository) Upsert(ctx context.Context, t domain.StationTariff) error {
	extra, err := json.Marshal(t.Extra)
	if err != nil {
		return fmt.Errorf("marshal extra: %w", err)
	}
	plan := t.Plan
	if plan == "" {
		plan = domain.TariffPlanStandard
	}

	const query = `
		INSERT INTO station_tariffs (
			station_id, source, plan, kind, model, currency,
			energy_price_cents_per_kwh, session_price_cents_per_min, congestion_price_cents_per_min,
			service_fee_percent, session_fee_cents, session_price_grace_minutes, connector_type, valid_from, valid_to, raw_text, extra, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, now())
		ON CONFLICT (station_id, source, kind, plan, connector_type) DO UPDATE SET
			model = EXCLUDED.model,
			currency = EXCLUDED.currency,
			energy_price_cents_per_kwh = EXCLUDED.energy_price_cents_per_kwh,
			session_price_cents_per_min = EXCLUDED.session_price_cents_per_min,
			congestion_price_cents_per_min = EXCLUDED.congestion_price_cents_per_min,
			service_fee_percent = EXCLUDED.service_fee_percent,
			session_fee_cents = EXCLUDED.session_fee_cents,
			session_price_grace_minutes = EXCLUDED.session_price_grace_minutes,
			valid_from = EXCLUDED.valid_from,
			valid_to = EXCLUDED.valid_to,
			raw_text = EXCLUDED.raw_text,
			extra = EXCLUDED.extra,
			updated_at = now()`

	_, err = r.db.Exec(ctx, query,
		t.StationID, t.Source, plan, t.Kind, t.Model, t.Currency,
		t.EnergyPriceCentsPerKWh, t.SessionPriceCentsPerMin, t.CongestionPriceCentsPerMin,
		t.ServiceFeePercent, t.SessionFeeCents, t.SessionPriceGraceMinutes, t.ConnectorType, t.ValidFrom, t.ValidTo, t.RawText, extra,
	)
	if err != nil {
		return fmt.Errorf("upsert station tariff: %w", err)
	}
	return nil
}

// tariffKey identifies a station_tariffs conflict key (station_id, source,
// kind, plan, connector_type).
func tariffKey(t domain.StationTariff) string {
	plan := t.Plan
	if plan == "" {
		plan = domain.TariffPlanStandard
	}
	return t.StationID.String() + "\x00" + t.Source + "\x00" + t.Kind + "\x00" + plan + "\x00" + t.ConnectorType
}

// BulkUpsert writes many tariffs in a single round trip instead of one
// Upsert call per tariff — the same batch of source stations can produce
// several tariffs each (e.g. Electra's 3 plans x 2 kinds), so this is
// where per-row round trips add up the most for large ingestion runs
// against a database with real network latency.
func (r *TariffRepository) BulkUpsert(ctx context.Context, tariffs []domain.StationTariff) error {
	if len(tariffs) == 0 {
		return nil
	}

	// Dedupe by conflict key, keeping the last occurrence: a single
	// multi-row INSERT ON CONFLICT DO UPDATE errors if two input rows
	// target the same key (e.g. two source stations correlated to the same
	// IRVE station produce the same (station_id, source, kind, plan)).
	deduped := dedupeTariffs(tariffs)

	n := len(deduped)
	stationIDs := make([]uuid.UUID, n)
	sources := make([]string, n)
	plans := make([]string, n)
	kinds := make([]string, n)
	models := make([]string, n)
	currencies := make([]string, n)
	energyPrices := make([]*float64, n)
	sessionPrices := make([]*float64, n)
	congestionPrices := make([]*float64, n)
	serviceFees := make([]*float64, n)
	sessionFees := make([]*float64, n)
	sessionGraceMinutes := make([]*float64, n)
	connectorTypes := make([]string, n)
	validFroms := make([]*time.Time, n)
	validTos := make([]*time.Time, n)
	rawTexts := make([]string, n)
	extras := make([]string, n)
	for i, t := range deduped {
		plan := t.Plan
		if plan == "" {
			plan = domain.TariffPlanStandard
		}
		extra, err := json.Marshal(t.Extra)
		if err != nil {
			return fmt.Errorf("marshal extra for bulk tariff %d: %w", i, err)
		}
		stationIDs[i] = t.StationID
		sources[i] = t.Source
		plans[i] = plan
		kinds[i] = t.Kind
		models[i] = t.Model
		currencies[i] = t.Currency
		energyPrices[i] = t.EnergyPriceCentsPerKWh
		sessionPrices[i] = t.SessionPriceCentsPerMin
		congestionPrices[i] = t.CongestionPriceCentsPerMin
		serviceFees[i] = t.ServiceFeePercent
		sessionFees[i] = t.SessionFeeCents
		sessionGraceMinutes[i] = t.SessionPriceGraceMinutes
		connectorTypes[i] = t.ConnectorType
		validFroms[i] = t.ValidFrom
		validTos[i] = t.ValidTo
		rawTexts[i] = t.RawText
		extras[i] = string(extra)
	}

	const query = `
		INSERT INTO station_tariffs (
			station_id, source, plan, kind, model, currency,
			energy_price_cents_per_kwh, session_price_cents_per_min, congestion_price_cents_per_min,
			service_fee_percent, session_fee_cents, session_price_grace_minutes, connector_type, valid_from, valid_to, raw_text, extra, updated_at
		)
		SELECT s.station_id, s.source, s.plan, s.kind, s.model, s.currency,
			s.energy_price_cents_per_kwh, s.session_price_cents_per_min, s.congestion_price_cents_per_min,
			s.service_fee_percent, s.session_fee_cents, s.session_price_grace_minutes, s.connector_type, s.valid_from, s.valid_to, s.raw_text, s.extra::jsonb, now()
		FROM unnest(
			$1::uuid[], $2::text[], $3::text[], $4::text[], $5::text[], $6::text[],
			$7::float8[], $8::float8[], $9::float8[],
			$10::float8[], $11::float8[], $12::float8[], $13::text[], $14::timestamptz[], $15::timestamptz[], $16::text[], $17::text[]
		) AS s(station_id, source, plan, kind, model, currency,
			energy_price_cents_per_kwh, session_price_cents_per_min, congestion_price_cents_per_min,
			service_fee_percent, session_fee_cents, session_price_grace_minutes, connector_type, valid_from, valid_to, raw_text, extra)
		ON CONFLICT (station_id, source, kind, plan, connector_type) DO UPDATE SET
			model = EXCLUDED.model,
			currency = EXCLUDED.currency,
			energy_price_cents_per_kwh = EXCLUDED.energy_price_cents_per_kwh,
			session_price_cents_per_min = EXCLUDED.session_price_cents_per_min,
			congestion_price_cents_per_min = EXCLUDED.congestion_price_cents_per_min,
			service_fee_percent = EXCLUDED.service_fee_percent,
			session_fee_cents = EXCLUDED.session_fee_cents,
			session_price_grace_minutes = EXCLUDED.session_price_grace_minutes,
			valid_from = EXCLUDED.valid_from,
			valid_to = EXCLUDED.valid_to,
			raw_text = EXCLUDED.raw_text,
			extra = EXCLUDED.extra,
			updated_at = now()`

	_, err := r.db.Exec(ctx, query,
		stationIDs, sources, plans, kinds, models, currencies,
		energyPrices, sessionPrices, congestionPrices,
		serviceFees, sessionFees, sessionGraceMinutes, connectorTypes, validFroms, validTos, rawTexts, extras,
	)
	if err != nil {
		return fmt.Errorf("bulk upsert station tariffs: %w", err)
	}
	return nil
}

func dedupeTariffs(tariffs []domain.StationTariff) []domain.StationTariff {
	byKey := make(map[string]int, len(tariffs))
	deduped := make([]domain.StationTariff, 0, len(tariffs))
	for _, t := range tariffs {
		key := tariffKey(t)
		if idx, ok := byKey[key]; ok {
			deduped[idx] = t
			continue
		}
		byKey[key] = len(deduped)
		deduped = append(deduped, t)
	}
	return deduped
}

// ListDistinctSourcesWithPlans returns every tariff source currently
// ingested along with its available price plans (e.g. "electra" ->
// ["app", "public", "subscription"]), so the frontend can build its
// operator filter and plan selector from what actually exists instead of a
// hardcoded list.
func (r *TariffRepository) ListDistinctSourcesWithPlans(ctx context.Context) ([]domain.SourcePlans, error) {
	rows, err := r.db.Query(ctx, `SELECT DISTINCT source, plan FROM station_tariffs ORDER BY source, plan`)
	if err != nil {
		return nil, fmt.Errorf("list distinct tariff sources: %w", err)
	}
	defer rows.Close()

	// Rows are ordered by source, so appending to the last entry's Plans
	// whenever the source repeats keeps this a single pass with no map
	// needed (and no risk of aliasing a slice element across reallocations).
	result := []domain.SourcePlans{}
	for rows.Next() {
		var source, plan string
		if err := rows.Scan(&source, &plan); err != nil {
			return nil, fmt.Errorf("scan tariff source/plan: %w", err)
		}
		if len(result) == 0 || result[len(result)-1].Source != source {
			result = append(result, domain.SourcePlans{Source: source})
		}
		last := &result[len(result)-1]
		last.Plans = append(last.Plans, plan)
	}
	return result, rows.Err()
}

// ListByStation returns all tariffs attached to an IRVE station.
func (r *TariffRepository) ListByStation(ctx context.Context, stationID uuid.UUID) ([]domain.StationTariff, error) {
	const query = `
		SELECT id, station_id, source, plan, kind, model, currency,
			energy_price_cents_per_kwh, session_price_cents_per_min, congestion_price_cents_per_min,
			service_fee_percent, session_fee_cents, session_price_grace_minutes, connector_type, valid_from, valid_to, raw_text, extra, created_at, updated_at
		FROM station_tariffs WHERE station_id = $1 ORDER BY source, plan, kind`

	rows, err := r.db.Query(ctx, query, stationID)
	if err != nil {
		return nil, fmt.Errorf("list tariffs for station %s: %w", stationID, err)
	}
	defer rows.Close()

	var tariffs []domain.StationTariff
	for rows.Next() {
		var t domain.StationTariff
		var extra []byte
		if err := rows.Scan(
			&t.ID, &t.StationID, &t.Source, &t.Plan, &t.Kind, &t.Model, &t.Currency,
			&t.EnergyPriceCentsPerKWh, &t.SessionPriceCentsPerMin, &t.CongestionPriceCentsPerMin,
			&t.ServiceFeePercent, &t.SessionFeeCents, &t.SessionPriceGraceMinutes, &t.ConnectorType, &t.ValidFrom, &t.ValidTo, &t.RawText, &extra, &t.CreatedAt, &t.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan tariff: %w", err)
		}
		_ = json.Unmarshal(extra, &t.Extra)
		tariffs = append(tariffs, t)
	}
	return tariffs, rows.Err()
}
