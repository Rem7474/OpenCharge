DROP INDEX station_tariffs_station_source_kind_plan_idx;
CREATE UNIQUE INDEX station_tariffs_station_source_kind_idx ON station_tariffs (station_id, source, kind);
ALTER TABLE station_tariffs DROP COLUMN plan;
