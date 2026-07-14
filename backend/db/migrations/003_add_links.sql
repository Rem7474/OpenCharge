-- +migrate Up
CREATE TABLE IF NOT EXISTS station_links (
    id                UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    station_id        UUID NOT NULL REFERENCES stations(id) ON DELETE CASCADE,
    source_station_id UUID NOT NULL REFERENCES source_stations(id) ON DELETE CASCADE,
    source            VARCHAR NOT NULL,
    link_quality      VARCHAR NOT NULL CHECK (link_quality IN ('exact', 'by_geolocation', 'by_operator_name', 'manual')),
    created_at        TIMESTAMPTZ DEFAULT now(),
    UNIQUE (station_id, source_station_id)
);

CREATE INDEX IF NOT EXISTS idx_links_station_id ON station_links (station_id);
CREATE INDEX IF NOT EXISTS idx_links_source     ON station_links (source);
