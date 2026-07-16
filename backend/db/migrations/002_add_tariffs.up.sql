-- source_stations: raw stations as seen by external sources (Izivia, Electra, ...)
CREATE TABLE source_stations (
    id                   UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    source               TEXT NOT NULL,
    source_station_id    TEXT NOT NULL,
    name                 TEXT NOT NULL DEFAULT '',
    operator_name        TEXT NOT NULL DEFAULT '',
    address_street       TEXT NOT NULL DEFAULT '',
    address_postal_code  TEXT NOT NULL DEFAULT '',
    address_city         TEXT NOT NULL DEFAULT '',
    address_country_code TEXT NOT NULL DEFAULT '',
    location             geometry(Point, 4326) NOT NULL,
    raw                  JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (source, source_station_id)
);

CREATE INDEX source_stations_location_gist_idx ON source_stations USING GIST (location);
CREATE INDEX source_stations_source_idx ON source_stations (source);

-- station_tariffs: tariffs normalized from an external source and attached
-- to an IRVE station.
CREATE TABLE station_tariffs (
    id                             UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    station_id                     UUID NOT NULL REFERENCES stations(id) ON DELETE CASCADE,
    source                         TEXT NOT NULL,
    kind                           TEXT NOT NULL DEFAULT 'mixed',
    model                          TEXT NOT NULL DEFAULT '',
    currency                       TEXT NOT NULL DEFAULT 'EUR',
    energy_price_cents_per_kwh     DOUBLE PRECISION,
    session_price_cents_per_min    DOUBLE PRECISION,
    congestion_price_cents_per_min DOUBLE PRECISION,
    service_fee_percent            DOUBLE PRECISION,
    valid_from                     TIMESTAMPTZ,
    valid_to                       TIMESTAMPTZ,
    raw_text                       TEXT NOT NULL DEFAULT '',
    extra                          JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at                     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX station_tariffs_station_id_idx ON station_tariffs (station_id);
CREATE INDEX station_tariffs_source_idx ON station_tariffs (source);
-- one tariff row per (station, source, kind): re-ingestion updates in place
CREATE UNIQUE INDEX station_tariffs_station_source_kind_idx ON station_tariffs (station_id, source, kind);
