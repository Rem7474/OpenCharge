-- station_tariffs.energy_price_cents_per_kwh is a snapshot: for tariffs
-- with time-of-day windows (currently only Electra, see
-- ingestion/electra.go's withPlan), it's set once per ingestion run to
-- whichever window covered "now" at that moment, then never touched again
-- until the next run. Between runs, the real current time moves into a
-- different window with a different price, so the map/list aggregates
-- (which read this column straight via MIN()) can show a stale price —
-- confirmed in production: a map marker showing a peak-hour price hours
-- after the peak window ended, while the station detail's per-hour chart
-- (computed live from the browser's clock) correctly showed the current
-- off-peak price.
--
-- current_window_price re-derives the live price directly from the raw
-- per-window data already stored in station_tariffs.extra->'windows'
-- (startTime/endTime "HH:MM", energyPriceCentsPerKwh), evaluated against
-- the real current time in Europe/Paris — the same timezone assumption
-- ingestion already makes for window boundaries (see electra.go's
-- electraLocation). Falls back to the snapshot column when a tariff has
-- no windows (every non-Electra source) or none match (malformed data).
CREATE OR REPLACE FUNCTION current_window_price(extra jsonb, fallback float8)
RETURNS float8
LANGUAGE sql
STABLE
AS $$
    SELECT COALESCE(
        (
            SELECT (w->>'energyPriceCentsPerKwh')::float8
            FROM jsonb_array_elements(extra->'windows') AS w
            WHERE w->>'energyPriceCentsPerKwh' IS NOT NULL
                AND w->>'startTime' IS NOT NULL
                AND w->>'endTime' IS NOT NULL
                AND (
                    -- window within a single day (start <= end)
                    (
                        (w->>'startTime') <= (w->>'endTime')
                        AND to_char(now() AT TIME ZONE 'Europe/Paris', 'HH24:MI') >= (w->>'startTime')
                        AND to_char(now() AT TIME ZONE 'Europe/Paris', 'HH24:MI') < (w->>'endTime')
                    )
                    OR
                    -- window wraps past midnight (start > end, e.g. 22:00-06:00)
                    (
                        (w->>'startTime') > (w->>'endTime')
                        AND (
                            to_char(now() AT TIME ZONE 'Europe/Paris', 'HH24:MI') >= (w->>'startTime')
                            OR to_char(now() AT TIME ZONE 'Europe/Paris', 'HH24:MI') < (w->>'endTime')
                        )
                    )
                )
            LIMIT 1
        ),
        fallback
    );
$$;
