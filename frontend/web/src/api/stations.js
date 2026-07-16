const API_BASE = import.meta.env.VITE_API_BASE_URL || "http://localhost:8080";

/**
 * Fetch stations intersecting a map viewport. bbox is mandatory: the map
 * never loads the whole IRVE dataset, only what's currently visible.
 */
export async function fetchStationsInBBox(bbox, { operator, hasTariffs, source, limit, signal } = {}) {
  const params = new URLSearchParams();
  params.set("bbox", bbox.join(","));
  if (operator) params.set("operator", operator);
  if (hasTariffs !== undefined) params.set("hasTariffs", String(hasTariffs));
  if (source) params.set("source", source);
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
