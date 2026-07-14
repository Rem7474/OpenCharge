-- +migrate Up
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS postgis;

-- Table principale des points de charge IRVE (référentiel canonique)
CREATE TABLE IF NOT EXISTS stations (
    id                   UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    irve_id_station      VARCHAR,
    irve_id_pdc          VARCHAR UNIQUE NOT NULL,
    operator_name        VARCHAR,
    amenageur            VARCHAR,
    enseigne             VARCHAR,
    name                 VARCHAR,
    address_street       VARCHAR,
    address_postal_code  VARCHAR,
    address_city         VARCHAR,
    address_country_code VARCHAR DEFAULT 'FR',
    location             geometry(Point, 4326) NOT NULL,
    power_kw             FLOAT,
    connector_type       VARCHAR,
    access_type          VARCHAR,
    is_24_7              BOOLEAN DEFAULT FALSE,
    metadata             JSONB,
    created_at           TIMESTAMPTZ DEFAULT now(),
    updated_at           TIMESTAMPTZ DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_stations_location  ON stations USING GIST (location);
CREATE INDEX IF NOT EXISTS idx_stations_irve_pdc  ON stations (irve_id_pdc);
CREATE INDEX IF NOT EXISTS idx_stations_operator  ON stations (operator_name);
CREATE INDEX IF NOT EXISTS idx_stations_enseigne  ON stations (enseigne);

-- Table des stations sources externes (Izivia, Electra, …)
CREATE TABLE IF NOT EXISTS source_stations (
    id                UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    source            VARCHAR NOT NULL,
    source_station_id VARCHAR NOT NULL,
    name              VARCHAR,
    operator_name     VARCHAR,
    address_street    VARCHAR,
    address_postal_code VARCHAR,
    address_city      VARCHAR,
    address_country_code VARCHAR,
    location          geometry(Point, 4326),
    raw               JSONB,
    created_at        TIMESTAMPTZ DEFAULT now(),
    updated_at        TIMESTAMPTZ DEFAULT now(),
    UNIQUE (source, source_station_id)
);

CREATE INDEX IF NOT EXISTS idx_src_stations_location ON source_stations USING GIST (location);
CREATE INDEX IF NOT EXISTS idx_src_stations_source   ON source_stations (source);
