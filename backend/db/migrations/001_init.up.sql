CREATE EXTENSION IF NOT EXISTS postgis;
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

-- stations: the IRVE referential (canonical points of charge)
CREATE TABLE stations (
    id                  UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    irve_id_station     TEXT,
    irve_id_pdc         TEXT NOT NULL UNIQUE,
    operator_name       TEXT NOT NULL DEFAULT '',
    amenageur           TEXT NOT NULL DEFAULT '',
    enseigne            TEXT NOT NULL DEFAULT '',
    name                TEXT NOT NULL DEFAULT '',
    address_street      TEXT NOT NULL DEFAULT '',
    address_postal_code TEXT NOT NULL DEFAULT '',
    address_city        TEXT NOT NULL DEFAULT '',
    address_country_code TEXT NOT NULL DEFAULT 'FR',
    location            geometry(Point, 4326) NOT NULL,
    power_kw            DOUBLE PRECISION,
    connector_type      TEXT NOT NULL DEFAULT '',
    access_type         TEXT NOT NULL DEFAULT '',
    is_24_7             BOOLEAN NOT NULL DEFAULT FALSE,
    metadata            JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX stations_location_gist_idx ON stations USING GIST (location);
CREATE INDEX stations_irve_id_pdc_idx ON stations (irve_id_pdc);
CREATE INDEX stations_operator_name_idx ON stations (operator_name);
CREATE INDEX stations_enseigne_idx ON stations (enseigne);
