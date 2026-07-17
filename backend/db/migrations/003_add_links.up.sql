-- station_links: correlation between an IRVE station and a source station.
CREATE TABLE station_links (
    id                UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    station_id        UUID NOT NULL REFERENCES stations(id) ON DELETE CASCADE,
    source_station_id UUID NOT NULL REFERENCES source_stations(id) ON DELETE CASCADE,
    source            TEXT NOT NULL,
    link_quality      TEXT NOT NULL DEFAULT 'by_geolocation',
    distance_meters   DOUBLE PRECISION,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (station_id, source_station_id)
);

CREATE INDEX station_links_station_id_idx ON station_links (station_id);
CREATE INDEX station_links_source_station_id_idx ON station_links (source_station_id);
CREATE INDEX station_links_source_idx ON station_links (source);
