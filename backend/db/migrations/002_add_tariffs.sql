-- +migrate Up
CREATE TABLE IF NOT EXISTS station_tariffs (
    id                              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    station_id                      UUID NOT NULL REFERENCES stations(id) ON DELETE CASCADE,
    source                          VARCHAR NOT NULL,
    kind                            VARCHAR NOT NULL CHECK (kind IN ('ac', 'dc', 'mixed')),
    model                           VARCHAR,
    currency                        VARCHAR DEFAULT 'EUR',
    energy_price_cents_per_kwh      FLOAT,
    session_price_cents_per_min     FLOAT,
    congestion_price_cents_per_min  FLOAT,
    service_fee_percent             FLOAT,
    valid_from                      DATE,
    valid_to                        DATE,
    raw_text                        TEXT,
    extra                           JSONB,
    created_at                      TIMESTAMPTZ DEFAULT now(),
    updated_at                      TIMESTAMPTZ DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_tariffs_station_id ON station_tariffs (station_id);
CREATE INDEX IF NOT EXISTS idx_tariffs_source     ON station_tariffs (source);
