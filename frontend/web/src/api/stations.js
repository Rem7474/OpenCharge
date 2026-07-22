const API_BASE = import.meta.env.VITE_API_BASE_URL || "http://localhost:8080";

/**
 * Throws a structured error for a failed response: callers can branch on
 * `err.status` (e.g. friendlyFetchErrorMessage in utils/format.js) instead
 * of parsing the message string, and a missing status (network failure,
 * CORS, DNS) stays distinguishable from a real HTTP error status.
 */
async function throwForStatus(res, what) {
  const err = new Error(`${what} failed: ${res.status}`);
  err.status = res.status;
  throw err;
}

/**
 * Fetch stations intersecting a map viewport. bbox is mandatory: the map
 * never loads the whole IRVE dataset, only what's currently visible.
 *
 * `sources` (array of "source:plan" pairs, e.g. ["izivia:standard",
 * "electra:subscription"] — see utils/pricing.js#sourcePlanPairs) never
 * filters stations out server-side: it only asks the API to compute
 * `selectedSourcesPricing` for those (source, plan) pairs, so stations
 * without a tariff from the selection can still be shown, just without a
 * price.
 *
 * excludeSubscriptionPlans, when true, drops subscription-plan tariffs from
 * both pricingSummary and selectedSourcesPricing server-side (see backend
 * GET /stations docs), so the price used for the map marker never assumes
 * a paid subscription.
 *
 * chargeKWh/chargeMinutes, when given alongside min/maxPriceCentsPerKwh,
 * switch the price-range filter server-side to the total cost of a session
 * of that size (energy + time + flat fee) instead of a plain €/kWh rate —
 * see backend GET /stations docs.
 */
export async function fetchStationsInBBox(
  bbox,
  {
    operator,
    hasTariffs,
    sources,
    connectorTypes,
    minPowerKw,
    minPriceCentsPerKwh,
    maxPriceCentsPerKwh,
    chargeKWh,
    chargeMinutes,
    excludeSubscriptionPlans,
    limit,
    signal,
  } = {}
) {
  const params = new URLSearchParams();
  params.set("bbox", bbox.join(","));
  if (operator) params.set("operator", operator);
  if (hasTariffs !== undefined) params.set("hasTariffs", String(hasTariffs));
  if (sources && sources.length > 0) params.set("source", sources.join(","));
  if (connectorTypes && connectorTypes.length > 0) params.set("connectorType", connectorTypes.join(","));
  if (minPowerKw != null) params.set("minPowerKw", String(minPowerKw));
  if (minPriceCentsPerKwh != null) params.set("minPriceCentsPerKwh", String(minPriceCentsPerKwh));
  if (maxPriceCentsPerKwh != null) params.set("maxPriceCentsPerKwh", String(maxPriceCentsPerKwh));
  if (chargeKWh != null) params.set("chargeKWh", String(chargeKWh));
  if (chargeMinutes != null) params.set("chargeMinutes", String(chargeMinutes));
  if (excludeSubscriptionPlans) params.set("excludeSubscriptionPlans", "true");
  params.set("limit", String(limit ?? 500));

  const res = await fetch(`${API_BASE}/stations?${params.toString()}`, { signal });
  if (!res.ok) await throwForStatus(res, "GET /stations");
  return res.json();
}

export async function fetchStationDetails(id, { signal } = {}) {
  const res = await fetch(`${API_BASE}/stations/${encodeURIComponent(id)}`, { signal });
  if (!res.ok) await throwForStatus(res, `GET /stations/${id}`);
  return res.json();
}

/**
 * Fetch every tariff source currently known to the backend (e.g.
 * ["electra", "izivia"]), so the operator filter never needs a hardcoded
 * list and picks up new sources as soon as they're ingested.
 */
export async function fetchSources({ signal } = {}) {
  const res = await fetch(`${API_BASE}/sources`, { signal });
  if (!res.ok) await throwForStatus(res, "GET /sources");
  return res.json();
}

/**
 * A site's live is_available status, via our own backend (see
 * components/FreshmileAvailability.jsx) rather than calling Freshmile's API
 * directly from the browser: that direct call is blocked by CORS in
 * production (Freshmile's API sends no Access-Control-Allow-Origin header —
 * confirmed against the real deployment), so the backend proxies it
 * (GET /freshmile/availability/{locationId} — see backend api/freshmile.go).
 */
export async function fetchFreshmileAvailability(locationId, { signal } = {}) {
  const res = await fetch(`${API_BASE}/freshmile/availability/${encodeURIComponent(locationId)}`, { signal });
  if (!res.ok) await throwForStatus(res, `GET /freshmile/availability/${locationId}`);
  return res.json();
}

/**
 * A nationwide-average fuel price (SP95-E10 today), for the
 * essence/électrique cost comparison (see utils/pricing.js#
 * thermalEquivalentCost) — already averaged and cached server-side (see
 * backend api/fuelprice.go), so this is cheap to call once per session.
 */
export async function fetchFuelPrice({ signal } = {}) {
  const res = await fetch(`${API_BASE}/fuel-price`, { signal });
  if (!res.ok) await throwForStatus(res, "GET /fuel-price");
  return res.json();
}
