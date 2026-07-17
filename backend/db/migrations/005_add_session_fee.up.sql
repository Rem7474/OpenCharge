-- A flat, one-time fee charged just for starting a charging session,
-- independent of energy consumed or duration (e.g. Izivia's "2,3€ la
-- session de charge"). Distinct from session_price_cents_per_min, which is
-- a per-minute *rate* despite the similar name.
ALTER TABLE station_tariffs ADD COLUMN session_fee_cents DOUBLE PRECISION;
