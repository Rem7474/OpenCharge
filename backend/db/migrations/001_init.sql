-- 001_init.sql
-- Requires PostGIS extension

CREATE EXTENSION IF NOT EXISTS postgis;
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- Referential IRVE stations (one row = one charging point / PDC)
CREATE TABLE IF NOT EXISTS stations (
    id                   UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    irve_id_station      TEXT,
    irve_id_pdc          TEXT        UNIQUE,
    operator_name        TEXT,
    amenageur            TEXT,
    enseigne             TEXT,
    name                 TEXT,
    address_street       TEXT,
    address_postal_code  TEXT,
    address_city         TEXT,
    address_country_code TEXT        NOT NULL DEFAULT 'FR',
    location             geometry(Point, 4326) NOT NULL,
    power_kw             NUMERIC,
    connector_type       TEXT,
    access_type          TEXT,
    is_24_7              BOOLEAN,
    metadata             JSONB,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS stations_location_gist   ON stations USING GIST (location);
CREATE INDEX IF NOT EXISTS stations_irve_id_pdc_idx ON stations (irve_id_pdc);
CREATE INDEX IF NOT EXISTS stations_operator_idx    ON stations (operator_name);
CREATE INDEX IF NOT EXISTS stations_enseigne_idx    ON stations (enseigne);

-- External-source stations (Izivia, Electra, …)
CREATE TABLE IF NOT EXISTS source_stations (
    id                   UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    source               TEXT        NOT NULL,
    source_station_id    TEXT        NOT NULL,
    name                 TEXT,
    operator_name        TEXT,
    address_street       TEXT,
    address_postal_code  TEXT,
    address_city         TEXT,
    address_country_code TEXT        NOT NULL DEFAULT 'FR',
    location             geometry(Point, 4326),
    raw                  JSONB,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (source, source_station_id)
);

CREATE INDEX IF NOT EXISTS source_stations_location_gist ON source_stations USING GIST (location);
CREATE INDEX IF NOT EXISTS source_stations_source_idx    ON source_stations (source);
