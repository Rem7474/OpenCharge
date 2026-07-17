const API_BASE = import.meta.env.VITE_API_BASE_URL || "http://localhost:8080";

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
 */
export async function fetchStationsInBBox(
  bbox,
  { operator, hasTariffs, sources, connectorTypes, minPowerKw, limit, signal } = {}
) {
  const params = new URLSearchParams();
  params.set("bbox", bbox.join(","));
  if (operator) params.set("operator", operator);
  if (hasTariffs !== undefined) params.set("hasTariffs", String(hasTariffs));
  if (sources && sources.length > 0) params.set("source", sources.join(","));
  if (connectorTypes && connectorTypes.length > 0) params.set("connectorType", connectorTypes.join(","));
  if (minPowerKw != null) params.set("minPowerKw", String(minPowerKw));
  params.set("limit", String(limit ?? 500));

  const res = await fetch(`${API_BASE}/stations?${params.toString()}`, { signal });
  if (!res.ok) {
    throw new Error(`GET /stations failed: ${res.status}`);
  }
  return res.json();
}

export async function fetchStationDetails(id, { signal } = {}) {
  const res = await fetch(`${API_BASE}/stations/${encodeURIComponent(id)}`, { signal });
  if (!res.ok) {
    throw new Error(`GET /stations/${id} failed: ${res.status}`);
  }
  return res.json();
}

/**
 * Fetch every tariff source currently known to the backend (e.g.
 * ["electra", "izivia"]), so the operator filter never needs a hardcoded
 * list and picks up new sources as soon as they're ingested.
 */
export async function fetchSources({ signal } = {}) {
  const res = await fetch(`${API_BASE}/sources`, { signal });
  if (!res.ok) {
    throw new Error(`GET /sources failed: ${res.status}`);
  }
  return res.json();
}
