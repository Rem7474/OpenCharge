package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"chargingbackend/internal/model"
)

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL;`); err != nil {
		_ = db.Close()
		return nil, err
	}

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS stations (
			id TEXT PRIMARY KEY,
			source TEXT NOT NULL,
			operator TEXT NOT NULL,
			name TEXT NOT NULL,
			status TEXT,
			latitude REAL,
			longitude REAL,
			street TEXT,
			postal_code TEXT,
			city TEXT,
			country_code TEXT,
			is_24_7 INTEGER NOT NULL DEFAULT 0,
			accessible_for_disabled INTEGER NOT NULL DEFAULT 0,
			parking_type TEXT,
			best_price_cents_per_kwh REAL,
			currency TEXT,
			pricing_model TEXT,
			connector_count INTEGER NOT NULL DEFAULT 0,
			raw_json TEXT NOT NULL,
			normalized_json TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_stations_source_operator ON stations(source, operator);`,
		`CREATE INDEX IF NOT EXISTS idx_stations_city ON stations(city);`,
		`CREATE INDEX IF NOT EXISTS idx_stations_price ON stations(best_price_cents_per_kwh);`,
	}

	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) UpsertStation(ctx context.Context, station model.Station) error {
	rawJSON, err := json.Marshal(station.Raw)
	if err != nil {
		return err
	}
	normalizedJSON, err := json.Marshal(station)
	if err != nil {
		return err
	}

	var latitude, longitude sql.NullFloat64
	if station.Location.Lat != nil {
		latitude = sql.NullFloat64{Float64: *station.Location.Lat, Valid: true}
	}
	if station.Location.Lng != nil {
		longitude = sql.NullFloat64{Float64: *station.Location.Lng, Valid: true}
	}

	var bestPrice sql.NullFloat64
	if station.BestPriceCentsPerKwh != nil {
		bestPrice = sql.NullFloat64{Float64: *station.BestPriceCentsPerKwh, Valid: true}
	}

	updatedAt := station.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}

	_, err = s.db.ExecContext(ctx, `
INSERT INTO stations (
	id, source, operator, name, status, latitude, longitude, street, postal_code, city, country_code,
	is_24_7, accessible_for_disabled, parking_type, best_price_cents_per_kwh, currency, pricing_model,
	connector_count, raw_json, normalized_json, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
	source=excluded.source,
	operator=excluded.operator,
	name=excluded.name,
	status=excluded.status,
	latitude=excluded.latitude,
	longitude=excluded.longitude,
	street=excluded.street,
	postal_code=excluded.postal_code,
	city=excluded.city,
	country_code=excluded.country_code,
	is_24_7=excluded.is_24_7,
	accessible_for_disabled=excluded.accessible_for_disabled,
	parking_type=excluded.parking_type,
	best_price_cents_per_kwh=excluded.best_price_cents_per_kwh,
	currency=excluded.currency,
	pricing_model=excluded.pricing_model,
	connector_count=excluded.connector_count,
	raw_json=excluded.raw_json,
	normalized_json=excluded.normalized_json,
	updated_at=excluded.updated_at
`, station.ID, station.Source, station.Operator, station.Name, station.Status, latitude, longitude,
		station.Address.Street, station.Address.PostalCode, station.Address.City, station.Address.CountryCode,
		boolToInt(station.Is24_7), boolToInt(station.AccessibleForDisabled), station.ParkingType,
		bestPrice, station.Currency, station.Pricing.Model, len(station.Connectors), string(rawJSON), string(normalizedJSON), updatedAt.UTC().Format(time.RFC3339Nano))
	return err
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (s *Store) ListStations(ctx context.Context, filter model.StationFilter) ([]model.Station, error) {
	query := `SELECT normalized_json FROM stations WHERE 1=1`
	args := []any{}

	if filter.Source != "" {
		query += ` AND source = ?`
		args = append(args, filter.Source)
	}
	if filter.Operator != "" {
		query += ` AND operator = ?`
		args = append(args, filter.Operator)
	}
	if filter.City != "" {
		query += ` AND city = ?`
		args = append(args, filter.City)
	}
	if filter.MinPrice != nil {
		query += ` AND best_price_cents_per_kwh IS NOT NULL AND best_price_cents_per_kwh <= ?`
		args = append(args, *filter.MinPrice)
	}

	if filter.Sort == "name" {
		query += ` ORDER BY name ASC, id ASC`
	} else {
		query += ` ORDER BY best_price_cents_per_kwh IS NULL, best_price_cents_per_kwh ASC, name ASC`
	}

	limit := filter.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query += ` LIMIT ?`
	args = append(args, limit)
	if filter.Offset > 0 {
		query += ` OFFSET ?`
		args = append(args, filter.Offset)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var stations []model.Station
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var station model.Station
		if err := json.Unmarshal([]byte(payload), &station); err != nil {
			return nil, err
		}
		stations = append(stations, station)
	}
	return stations, rows.Err()
}

func (s *Store) GetStation(ctx context.Context, id string) (*model.Station, error) {
	var payload string
	if err := s.db.QueryRowContext(ctx, `SELECT normalized_json FROM stations WHERE id = ?`, id).Scan(&payload); err != nil {
		return nil, err
	}
	var station model.Station
	if err := json.Unmarshal([]byte(payload), &station); err != nil {
		return nil, fmt.Errorf("decode station %s: %w", id, err)
	}
	return &station, nil
}

func (s *Store) CountStations(ctx context.Context) (int, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM stations`).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}
