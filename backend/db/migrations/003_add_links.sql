-- 003_add_links.sql

CREATE TABLE IF NOT EXISTS station_links (
    id                UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    station_id        UUID        NOT NULL REFERENCES stations(id) ON DELETE CASCADE,
    source_station_id UUID        NOT NULL REFERENCES source_stations(id) ON DELETE CASCADE,
    source            TEXT        NOT NULL,
    link_quality      TEXT        NOT NULL CHECK (link_quality IN ('exact', 'by_geolocation', 'by_operator_name', 'manual')),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (station_id, source_station_id)
);

CREATE INDEX IF NOT EXISTS station_links_station_id_idx        ON station_links (station_id);
CREATE INDEX IF NOT EXISTS station_links_source_station_id_idx ON station_links (source_station_id);
