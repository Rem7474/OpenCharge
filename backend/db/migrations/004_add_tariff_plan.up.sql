-- A source can expose several price tiers for the same (station, kind),
-- e.g. Electra's public / app / subscription tariffs. "standard" is the
-- default plan for sources with a single tier (Izivia, IRVE text, ...).
ALTER TABLE station_tariffs ADD COLUMN plan TEXT NOT NULL DEFAULT 'standard';

DROP INDEX station_tariffs_station_source_kind_idx;
CREATE UNIQUE INDEX station_tariffs_station_source_kind_plan_idx ON station_tariffs (station_id, source, kind, plan);
