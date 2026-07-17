-- Preserves per-connector-type pricing precision when a source's raw data
-- differentiates by actual connector (today: only Freshmile — see
-- freshmileConnectorType). '' (not NULL) for every other source/kind,
-- matching this schema's existing convention of empty-string over NULL for
-- dimensions that participate in uniqueness (a NULL column doesn't collide
-- with another NULL under a UNIQUE index, which would silently defeat the
-- point of including it).
ALTER TABLE station_tariffs ADD COLUMN connector_type TEXT NOT NULL DEFAULT '';

DROP INDEX station_tariffs_station_source_kind_plan_idx;
CREATE UNIQUE INDEX station_tariffs_station_source_kind_plan_connector_idx
    ON station_tariffs (station_id, source, kind, plan, connector_type);
