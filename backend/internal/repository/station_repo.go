package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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

// subscriptionExclusionSQL returns the extra WHERE-clause fragment that
// drops subscription-plan tariffs from a price aggregate's FILTER (...)
// when exclude is true, or "" (no-op) otherwise. domain.TariffPlanSubscription
// is a fixed Go constant, never user input, so splicing it into the query
// text directly (rather than a bind parameter) is safe.
func subscriptionExclusionSQL(exclude bool) string {
	if !exclude {
		return ""
	}
	return " AND t.plan <> '" + domain.TariffPlanSubscription + "'"
}

// stationListSelectPrefix returns the shared SELECT list every ListByBBox
// query starts from. exclude mirrors StationFilter.ExcludeSubscriptionPlans:
// when true, both the ac_min/dc_min aggregates drop subscription-plan
// tariffs, so a station whose only price requires a paid subscription comes
// back with a null price instead of one the caller can't actually get.
func stationListSelectPrefix(exclude bool) string {
	excl := subscriptionExclusionSQL(exclude)
	return fmt.Sprintf(`
	SELECT
		s.id, s.irve_id_station, s.irve_id_pdc, s.operator_name, s.amenageur, s.enseigne, s.name,
		s.address_street, s.address_postal_code, s.address_city, s.address_country_code,
		ST_Y(s.location), ST_X(s.location), s.power_kw, s.connector_type, s.access_type, s.is_24_7,
		s.metadata, s.created_at, s.updated_at,
		COALESCE(array_agg(DISTINCT t.source) FILTER (WHERE t.source IS NOT NULL), '{}'),
		-- 'mixed' means the price applies whatever the connector kind (a
		-- single-price source like Izivia's free text with no power figure to
		-- infer ac/dc from), so it must feed BOTH the AC and DC minimums —
		-- otherwise these stations come back with null ac/dc and get grayed
		-- out on the map even though a tariff exists (visible in the detail
		-- view, which lists every kind).
		--
		-- current_window_price(t.extra, t.energy_price_cents_per_kwh) instead
		-- of the bare column: for tariffs with time-of-day windows (Electra),
		-- the column is a snapshot fixed at the last ingestion run, not live —
		-- see migration 008 for why that goes stale between runs. Non-windowed
		-- tariffs (everything else) fall through to the same column value.
		--
		-- No connector-type disambiguation needed here: stationListFrom's
		-- LATERAL join already dedupes each (source, plan, kind) down to a
		-- single row per station, preferring an exact connector_type match —
		-- so this MIN is already a true minimum across every applicable
		-- source, not just whichever source happens to have a connector-
		-- specific tariff on file (see stationListFrom's comment for why that
		-- distinction matters).
		MIN(current_window_price(t.extra, t.energy_price_cents_per_kwh)) FILTER (WHERE t.kind IN ('ac', 'mixed')%[1]s),
		MIN(current_window_price(t.extra, t.energy_price_cents_per_kwh)) FILTER (WHERE t.kind IN ('dc', 'mixed')%[1]s)`, excl)
}

// stationListFrom dedupes station_tariffs per (source, plan, kind) before
// any aggregation happens: some sources (currently only Freshmile) can
// attach both a connector-specific tariff (t.connector_type set, e.g.
// "CCS") and a generic one (t.connector_type = ”) to the very same
// station row — see the correlation note below. Only the row that matches
// (or, absent a match, an arbitrary row from) each (source, plan, kind)
// group survives, via ROW_NUMBER() OVER (... ORDER BY exact-match DESC).
//
// This dedup has to happen per source/plan, *before* taking a MIN across
// all sources: a naive "prefer any exact-connector-match row over the
// coarse min" at the aggregate level (the previous approach) let a single
// source's connector-specific tariff suppress every other, unrelated
// source's cheaper price for the whole station — e.g. a Freshmile
// CCS-specific tariff at 0.51€/kWh would hide a plain Lidl tariff at
// 0.29€/kWh entirely, even though Lidl's price has nothing to do with
// Freshmile's connector granularity. Deduping first means the MIN below
// is computed over one representative price per source, so it's a true
// global minimum again.
//
// (The connector-specific-vs-generic split arises because Freshmile
// correlation happens at the site level: a CCS-specific Freshmile tariff
// meant for one physical connector can end up attached to a *different*
// IRVE station row at the same location that has, say, a T2 connector —
// see source_station correlation in ingestion/freshmile.go.)
const stationListFrom = `
	FROM stations s
	LEFT JOIN LATERAL (
		SELECT t2.*,
			ROW_NUMBER() OVER (
				PARTITION BY t2.source, t2.plan, t2.kind
				ORDER BY (t2.connector_type = s.connector_type AND t2.connector_type <> '') DESC
			) AS rn
		FROM station_tariffs t2
		WHERE t2.station_id = s.id
	) t ON t.rn = 1`

// stationBestPriceACFragment/stationBestPriceDCFragment mirror
// stationListSelectPrefix's own ac_min/dc_min expressions exactly (kept in
// sync by hand). Factored out here so stationBestPriceFragment (below)
// doesn't hand-duplicate the whole thing a second time.
func stationBestPriceACFragment(exclude bool) string {
	excl := subscriptionExclusionSQL(exclude)
	return fmt.Sprintf(`MIN(current_window_price(t.extra, t.energy_price_cents_per_kwh)) FILTER (WHERE t.kind IN ('ac', 'mixed')%[1]s)`, excl)
}

func stationBestPriceDCFragment(exclude bool) string {
	excl := subscriptionExclusionSQL(exclude)
	return fmt.Sprintf(`MIN(current_window_price(t.extra, t.energy_price_cents_per_kwh)) FILTER (WHERE t.kind IN ('dc', 'mixed')%[1]s)`, excl)
}

// stationBestPriceFragment picks the same price a client would display for
// a station via utils/pricing.js#pickPriceCentsPerKWh: prefer whichever of
// ac/dc matches the station's own connector kind, falling back to
// whichever is available. Used by ListByBBox's price-range filter when no
// f.Sources selection is active, since without one there is no "filtered"
// price to filter on — unlike ORDER BY/GROUP BY, HAVING can't reference a
// SELECT-list alias and must repeat the aggregate expression verbatim.
func stationBestPriceFragment(exclude bool) string {
	return stationBestPriceExprFor(stationBestPriceACFragment(exclude), stationBestPriceDCFragment(exclude))
}

// stationBestPriceExprFor builds the same "prefer the station's own
// connector kind, else whichever is available" COALESCE/CASE shape as
// stationBestPriceFragment, but over caller-supplied AC/DC fragments — used
// both for the unfiltered global best price and, with sourcesParamIdx
// spliced into ac/dcFragment, for the price among only the caller's
// selected sources (see stationSelectedPriceFragment).
func stationBestPriceExprFor(acFragment, dcFragment string) string {
	return fmt.Sprintf(`COALESCE(
		CASE
			WHEN s.connector_type IN ('CCS', 'CHAdeMO') THEN (%[2]s)
			WHEN s.connector_type IN ('T2', 'EF') THEN (%[1]s)
			ELSE NULL
		END,
		(%[1]s), (%[2]s)
	)`, acFragment, dcFragment)
}

// stationSelectedPriceFragment mirrors stationBestPriceFragment but scoped
// to only the tariffs matching f.Sources (the same (source||':'||plan) =
// ANY($sourcesParamIdx) filter ListByBBox's SELECT list already applies for
// SelectedSourcesPricing) — the price a user with that specific
// source/plan selection would actually see for the station, as opposed to
// the station's cheapest tariff overall. Used by ListByBBox's price-range
// filter whenever a sources selection is active: the min/max price fields
// filter what the user would pay, not an unrelated cheaper plan they
// haven't selected.
func stationSelectedPriceFragment(sourcesParamIdx int, exclude bool) string {
	excl := subscriptionExclusionSQL(exclude)
	acFragment := fmt.Sprintf(
		`MIN(current_window_price(t.extra, t.energy_price_cents_per_kwh)) FILTER (WHERE t.kind IN ('ac', 'mixed') AND (t.source || ':' || t.plan) = ANY($%[1]d::text[])%[2]s)`,
		sourcesParamIdx, excl,
	)
	dcFragment := fmt.Sprintf(
		`MIN(current_window_price(t.extra, t.energy_price_cents_per_kwh)) FILTER (WHERE t.kind IN ('dc', 'mixed') AND (t.source || ':' || t.plan) = ANY($%[1]d::text[])%[2]s)`,
		sourcesParamIdx, excl,
	)
	return stationBestPriceExprFor(acFragment, dcFragment)
}

// stationTotalCentsExpr estimates the total cost (in cents) of a session
// delivering the caller's chosen kWh over the caller's chosen minutes, for a
// single tariff row: energy price × kWh, plus any per-minute rate × minutes,
// plus any flat one-time session fee. chargeKWhIdx/chargeMinutesIdx are the
// positional $N args holding those two session parameters. Mirrors
// utils/pricing.js#tariffCostBreakdown's total, so the price-range filter's
// "recharge" mode matches what the frontend actually displays for a station.
func stationTotalCentsExpr(chargeKWhIdx, chargeMinutesIdx int) string {
	return fmt.Sprintf(
		`(current_window_price(t.extra, t.energy_price_cents_per_kwh) * $%[1]d + COALESCE(t.session_price_cents_per_min, 0) * $%[2]d + COALESCE(t.session_fee_cents, 0))`,
		chargeKWhIdx, chargeMinutesIdx,
	)
}

// stationBestTotalFragment mirrors stationBestPriceFragment but ranks/filters
// tariffs by stationTotalCentsExpr's total-for-this-session cost instead of
// the bare €/kWh rate — used by ListByBBox's price-range filter in "recharge"
// mode (f.ChargeKWh set) when no f.Sources selection is active.
func stationBestTotalFragment(exclude bool, chargeKWhIdx, chargeMinutesIdx int) string {
	excl := subscriptionExclusionSQL(exclude)
	totalExpr := stationTotalCentsExpr(chargeKWhIdx, chargeMinutesIdx)
	acFragment := fmt.Sprintf(`MIN(%s) FILTER (WHERE t.kind IN ('ac', 'mixed')%s)`, totalExpr, excl)
	dcFragment := fmt.Sprintf(`MIN(%s) FILTER (WHERE t.kind IN ('dc', 'mixed')%s)`, totalExpr, excl)
	return stationBestPriceExprFor(acFragment, dcFragment)
}

// stationSelectedTotalFragment mirrors stationSelectedPriceFragment but, like
// stationBestTotalFragment, ranks/filters by total session cost instead of
// the bare €/kWh rate — used by ListByBBox's price-range filter in "recharge"
// mode when a sources selection is active.
func stationSelectedTotalFragment(sourcesParamIdx int, exclude bool, chargeKWhIdx, chargeMinutesIdx int) string {
	excl := subscriptionExclusionSQL(exclude)
	totalExpr := stationTotalCentsExpr(chargeKWhIdx, chargeMinutesIdx)
	acFragment := fmt.Sprintf(
		`MIN(%[1]s) FILTER (WHERE t.kind IN ('ac', 'mixed') AND (t.source || ':' || t.plan) = ANY($%[2]d::text[])%[3]s)`,
		totalExpr, sourcesParamIdx, excl,
	)
	dcFragment := fmt.Sprintf(
		`MIN(%[1]s) FILTER (WHERE t.kind IN ('dc', 'mixed') AND (t.source || ':' || t.plan) = ANY($%[2]d::text[])%[3]s)`,
		totalExpr, sourcesParamIdx, excl,
	)
	return stationBestPriceExprFor(acFragment, dcFragment)
}

// BulkUpsertStations upserts a slice of IRVE stations in a single round trip
// via a multi-row INSERT ... SELECT FROM unnest(...), the same bulk form as
// SourceStationRepository.BulkUpsert / TariffRepository.BulkUpsert — instead
// of one Exec per station. A full IRVE run is ~132k rows; per-row round
// trips there dominate wall-clock time against a database with real network
// latency far more than the query cost itself.
func (r *StationRepository) BulkUpsertStations(ctx context.Context, stations []domain.Station) error {
	if len(stations) == 0 {
		return nil
	}

	// Dedupe by irve_id_pdc, keeping the last occurrence: a single multi-row
	// INSERT ON CONFLICT DO UPDATE errors ("command cannot affect row a
	// second time") if two input rows target the same conflict key, which a
	// per-row Exec loop tolerated.
	deduped := dedupeStations(stations)

	n := len(deduped)
	irveIDStations := make([]*string, n)
	irveIDPDCs := make([]string, n)
	operatorNames := make([]string, n)
	amenageurs := make([]string, n)
	enseignes := make([]string, n)
	names := make([]string, n)
	addressStreets := make([]string, n)
	addressPostals := make([]string, n)
	addressCities := make([]string, n)
	addressCountries := make([]string, n)
	lngs := make([]float64, n)
	lats := make([]float64, n)
	powers := make([]*float64, n)
	connectorTypes := make([]string, n)
	accessTypes := make([]string, n)
	is247s := make([]bool, n)
	metadatas := make([]string, n)
	for i, s := range deduped {
		metadata, err := json.Marshal(s.Metadata)
		if err != nil {
			return fmt.Errorf("marshal metadata for %s: %w", s.IRVEIDPDC, err)
		}
		irveIDStations[i] = s.IRVEIDStation
		irveIDPDCs[i] = s.IRVEIDPDC
		operatorNames[i] = s.OperatorName
		amenageurs[i] = s.Amenageur
		enseignes[i] = s.Enseigne
		names[i] = s.Name
		addressStreets[i] = s.AddressStreet
		addressPostals[i] = s.AddressPostal
		addressCities[i] = s.AddressCity
		addressCountries[i] = s.AddressCountry
		lngs[i] = s.Lng
		lats[i] = s.Lat
		powers[i] = s.PowerKW
		connectorTypes[i] = s.ConnectorType
		accessTypes[i] = s.AccessType
		is247s[i] = s.Is24_7
		metadatas[i] = string(metadata)
	}

	const query = `
		INSERT INTO stations (
			irve_id_station, irve_id_pdc, operator_name, amenageur, enseigne, name,
			address_street, address_postal_code, address_city, address_country_code,
			location, power_kw, connector_type, access_type, is_24_7, metadata, updated_at
		)
		SELECT s.irve_id_station, s.irve_id_pdc, s.operator_name, s.amenageur, s.enseigne, s.name,
			s.address_street, s.address_postal_code, s.address_city, s.address_country_code,
			ST_SetSRID(ST_MakePoint(s.lng, s.lat), 4326), s.power_kw, s.connector_type, s.access_type, s.is_24_7, s.metadata::jsonb, now()
		FROM unnest(
			$1::text[], $2::text[], $3::text[], $4::text[], $5::text[], $6::text[],
			$7::text[], $8::text[], $9::text[], $10::text[],
			$11::float8[], $12::float8[], $13::float8[], $14::text[], $15::text[], $16::bool[], $17::text[]
		) AS s(irve_id_station, irve_id_pdc, operator_name, amenageur, enseigne, name,
			address_street, address_postal_code, address_city, address_country_code,
			lng, lat, power_kw, connector_type, access_type, is_24_7, metadata)
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

	_, err := r.pool.Exec(ctx, query,
		irveIDStations, irveIDPDCs, operatorNames, amenageurs, enseignes, names,
		addressStreets, addressPostals, addressCities, addressCountries,
		lngs, lats, powers, connectorTypes, accessTypes, is247s, metadatas,
	)
	if err != nil {
		return fmt.Errorf("bulk upsert stations: %w", err)
	}
	return nil
}

func dedupeStations(stations []domain.Station) []domain.Station {
	byKey := make(map[string]int, len(stations))
	deduped := make([]domain.Station, 0, len(stations))
	for _, s := range stations {
		if idx, ok := byKey[s.IRVEIDPDC]; ok {
			deduped[idx] = s
			continue
		}
		byKey[s.IRVEIDPDC] = len(deduped)
		deduped = append(deduped, s)
	}
	return deduped
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
	subscriptionExcl := subscriptionExclusionSQL(f.ExcludeSubscriptionPlans)
	query := stationListSelectPrefix(f.ExcludeSubscriptionPlans) + fmt.Sprintf(`,
		MIN(current_window_price(t.extra, t.energy_price_cents_per_kwh)) FILTER (WHERE t.kind IN ('ac', 'mixed') AND (t.source || ':' || t.plan) = ANY($%[1]d::text[])%[2]s),
		MIN(current_window_price(t.extra, t.energy_price_cents_per_kwh)) FILTER (WHERE t.kind IN ('dc', 'mixed') AND (t.source || ':' || t.plan) = ANY($%[1]d::text[])%[2]s)`, sourcesParamIdx, subscriptionExcl)
	query += stationListFrom + `
		WHERE s.location && ST_MakeEnvelope($1, $2, $3, $4, 4326)`

	if f.Operator != "" {
		args = append(args, f.Operator)
		query += fmt.Sprintf(" AND s.operator_name = $%d", len(args))
	}

	if len(f.ConnectorTypes) > 0 {
		args = append(args, f.ConnectorTypes)
		query += fmt.Sprintf(" AND s.connector_type = ANY($%d::text[])", len(args))
	}

	if f.MinPowerKW != nil {
		args = append(args, *f.MinPowerKW)
		query += fmt.Sprintf(" AND s.power_kw >= $%d", len(args))
	}

	query += `
		GROUP BY s.id`

	// Every condition here depends on an aggregate (COUNT/MIN ... FILTER),
	// so it must live in HAVING, not WHERE — and unlike ORDER BY/GROUP BY,
	// HAVING can't reference a SELECT-list output alias, so
	// stationBestPriceFragment's aggregate expression has to be repeated
	// verbatim rather than referenced by name.
	// When a sources selection is active, the price range filters against
	// the price for that selection specifically (stationSelectedPriceFragment)
	// rather than the station's cheapest tariff overall — a user filtering
	// "0.20-0.30 €/kWh" while only Electra is selected expects that bound to
	// apply to Electra's price, not some unrelated cheaper source they
	// haven't picked.
	priceFragment := stationBestPriceFragment(f.ExcludeSubscriptionPlans)
	if len(f.Sources) > 0 {
		priceFragment = stationSelectedPriceFragment(sourcesParamIdx, f.ExcludeSubscriptionPlans)
	}

	// When the caller supplied a session size (f.ChargeKWh), the price-range
	// filter switches from a bare €/kWh rate to the total cost of that
	// session — see domain.StationFilter.MinPriceCentsPerKWh's doc comment.
	wantsPriceFilter := f.MinPriceCentsPerKWh != nil || f.MaxPriceCentsPerKWh != nil
	if wantsPriceFilter && f.ChargeKWh != nil {
		args = append(args, *f.ChargeKWh)
		chargeKWhIdx := len(args)
		chargeMinutes := 0.0
		if f.ChargeMinutes != nil {
			chargeMinutes = *f.ChargeMinutes
		}
		args = append(args, chargeMinutes)
		chargeMinutesIdx := len(args)
		if len(f.Sources) > 0 {
			priceFragment = stationSelectedTotalFragment(sourcesParamIdx, f.ExcludeSubscriptionPlans, chargeKWhIdx, chargeMinutesIdx)
		} else {
			priceFragment = stationBestTotalFragment(f.ExcludeSubscriptionPlans, chargeKWhIdx, chargeMinutesIdx)
		}
	}

	var having []string
	if f.HasTariffs != nil && *f.HasTariffs {
		having = append(having, "COUNT(t.id) > 0")
	}
	if f.MinPriceCentsPerKWh != nil {
		args = append(args, *f.MinPriceCentsPerKWh)
		having = append(having, fmt.Sprintf("%s >= $%d", priceFragment, len(args)))
	}
	if f.MaxPriceCentsPerKWh != nil {
		args = append(args, *f.MaxPriceCentsPerKWh)
		having = append(having, fmt.Sprintf("%s <= $%d", priceFragment, len(args)))
	}
	if len(having) > 0 {
		query += " HAVING " + strings.Join(having, " AND ")
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

// ListByOperatorLike returns every IRVE station whose operator_name or
// enseigne case-insensitively contains needle — for ingesters (Fastned)
// whose whole network is already present in the IRVE referential, so there
// is no external station list to fetch/correlate: the "source" is just a
// tag applied directly to matching IRVE rows, no source_stations/
// station_links involved at all. Matches on either column since IRVE data
// entry isn't consistent about which of the two carries a network's brand
// name for a given station.
func (r *StationRepository) ListByOperatorLike(ctx context.Context, needle string) ([]domain.Station, error) {
	const query = `
		SELECT id, irve_id_station, irve_id_pdc, operator_name, amenageur, enseigne, name,
			address_street, address_postal_code, address_city, address_country_code,
			ST_Y(location), ST_X(location), power_kw, connector_type, access_type, is_24_7,
			metadata, created_at, updated_at
		FROM stations
		WHERE operator_name ILIKE '%' || $1 || '%' OR enseigne ILIKE '%' || $1 || '%'`

	rows, err := r.pool.Query(ctx, query, needle)
	if err != nil {
		return nil, fmt.Errorf("list stations by operator like %q: %w", needle, err)
	}
	defer rows.Close()

	var stations []domain.Station
	for rows.Next() {
		var s domain.Station
		var metadata []byte
		if err := rows.Scan(
			&s.ID, &s.IRVEIDStation, &s.IRVEIDPDC, &s.OperatorName, &s.Amenageur, &s.Enseigne, &s.Name,
			&s.AddressStreet, &s.AddressPostal, &s.AddressCity, &s.AddressCountry,
			&s.Lat, &s.Lng, &s.PowerKW, &s.ConnectorType, &s.AccessType, &s.Is24_7,
			&metadata, &s.CreatedAt, &s.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan station by operator like %q: %w", needle, err)
		}
		_ = json.Unmarshal(metadata, &s.Metadata)
		stations = append(stations, s)
	}
	return stations, rows.Err()
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
