-- 002_add_tariffs.sql

CREATE TABLE IF NOT EXISTS station_tariffs (
    id                              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    station_id                      UUID        NOT NULL REFERENCES stations(id) ON DELETE CASCADE,
    source                          TEXT        NOT NULL,
    kind                            TEXT        NOT NULL CHECK (kind IN ('ac', 'dc', 'mixed')),
    model                           TEXT        NOT NULL,
    currency                        TEXT        NOT NULL DEFAULT 'EUR',
    energy_price_cents_per_kwh      NUMERIC,
    session_price_cents_per_min     NUMERIC,
    congestion_price_cents_per_min  NUMERIC,
    service_fee_percent             NUMERIC,
    valid_from                      DATE,
    valid_to                        DATE,
    raw_text                        TEXT,
    extra                           JSONB,
    created_at                      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at                      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS station_tariffs_station_id_idx ON station_tariffs (station_id);
CREATE INDEX IF NOT EXISTS station_tariffs_source_idx     ON station_tariffs (source);
