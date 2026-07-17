DROP INDEX station_tariffs_station_source_kind_plan_connector_idx;
CREATE UNIQUE INDEX station_tariffs_station_source_kind_plan_idx
    ON station_tariffs (station_id, source, kind, plan);
ALTER TABLE station_tariffs DROP COLUMN connector_type;
