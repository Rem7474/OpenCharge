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
		-- 'mixed' means the price applies whatever the connector kind (a
		-- single-price source like Izivia's free text with no power figure to
		-- infer ac/dc from), so it must feed BOTH the AC and DC minimums —
		-- otherwise these stations come back with null ac/dc and get grayed
		-- out on the map even though a tariff exists (visible in the detail
		-- view, which lists every kind).
		--
		-- COALESCE prefers a tariff whose connector_type exactly matches this
		-- station's own s.connector_type (Freshmile is currently the only
		-- source that populates it) over the coarse kind-only minimum: e.g. a
		-- station with a T2 connector and a Freshmile CCS-specific tariff on
		-- file for the *same physical location* (a different PDC row sharing
		-- source_station correlation) shouldn't have that CCS price bleed into
		-- this station's summary. "t.connector_type <> ''" guards against
		-- s.connector_type also being '' (unknown) — that must never look like
		-- an "exact match" against every generic (non-connector-specific)
		-- tariff row.
		--
		-- current_window_price(t.extra, t.energy_price_cents_per_kwh) instead
		-- of the bare column: for tariffs with time-of-day windows (Electra),
		-- the column is a snapshot fixed at the last ingestion run, not live —
		-- see migration 008 for why that goes stale between runs. Non-windowed
		-- tariffs (everything else) fall through to the same column value.
		COALESCE(
			MIN(current_window_price(t.extra, t.energy_price_cents_per_kwh)) FILTER (WHERE t.kind IN ('ac', 'mixed') AND t.connector_type = s.connector_type AND t.connector_type <> ''),
			MIN(current_window_price(t.extra, t.energy_price_cents_per_kwh)) FILTER (WHERE t.kind IN ('ac', 'mixed'))
		),
		COALESCE(
			MIN(current_window_price(t.extra, t.energy_price_cents_per_kwh)) FILTER (WHERE t.kind IN ('dc', 'mixed') AND t.connector_type = s.connector_type AND t.connector_type <> ''),
			MIN(current_window_price(t.extra, t.energy_price_cents_per_kwh)) FILTER (WHERE t.kind IN ('dc', 'mixed'))
		)`

const stationListFrom = `
	FROM stations s
	LEFT JOIN station_tariffs t ON t.station_id = s.id`

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
	query := stationListSelectPrefix + fmt.Sprintf(`,
		COALESCE(
			MIN(current_window_price(t.extra, t.energy_price_cents_per_kwh)) FILTER (WHERE t.kind IN ('ac', 'mixed') AND (t.source || ':' || t.plan) = ANY($%[1]d::text[]) AND t.connector_type = s.connector_type AND t.connector_type <> ''),
			MIN(current_window_price(t.extra, t.energy_price_cents_per_kwh)) FILTER (WHERE t.kind IN ('ac', 'mixed') AND (t.source || ':' || t.plan) = ANY($%[1]d::text[]))
		),
		COALESCE(
			MIN(current_window_price(t.extra, t.energy_price_cents_per_kwh)) FILTER (WHERE t.kind IN ('dc', 'mixed') AND (t.source || ':' || t.plan) = ANY($%[1]d::text[]) AND t.connector_type = s.connector_type AND t.connector_type <> ''),
			MIN(current_window_price(t.extra, t.energy_price_cents_per_kwh)) FILTER (WHERE t.kind IN ('dc', 'mixed') AND (t.source || ':' || t.plan) = ANY($%[1]d::text[]))
		)`, sourcesParamIdx)
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
