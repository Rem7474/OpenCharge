-- A per-minute session rate (session_price_cents_per_min) that only
-- applies to charging minutes beyond this threshold, e.g. Izivia's
-- "surcoût de 0,30€/min après 1h de charge" — the first 60 minutes carry
-- no per-minute charge at all. NULL (the default, and every existing row)
-- means the rate applies from minute 1, i.e. no grace period — this
-- doesn't change behavior for any tariff that doesn't set it.
ALTER TABLE station_tariffs ADD COLUMN session_price_grace_minutes DOUBLE PRECISION;
