const GEOCODE_BASE = "https://data.geopf.fr/geocodage/search";

/**
 * Address/city search via the French government's public geocoding API
 * (IGN Géoplateforme — the continuation of the old api-adresse.data.gouv.fr
 * BAN service, decommissioned end of January 2026; same query params and
 * GeoJSON response shape). No API key, free, French-address-focused, which
 * matches this app's scope (the IRVE referential is France-only).
 *
 * Returns a flat array of { label, city, type, lat, lng } — type is one of
 * "housenumber"/"street"/"locality"/"municipality" (see AddressSearch.jsx's
 * zoomForResultType, which picks a map zoom level from it).
 */
export async function searchAddress(query, { signal, limit = 5 } = {}) {
  const params = new URLSearchParams({ q: query, limit: String(limit) });
  const res = await fetch(`${GEOCODE_BASE}?${params.toString()}`, { signal });
  if (!res.ok) {
    const err = new Error(`geocode search failed: ${res.status}`);
    err.status = res.status;
    throw err;
  }
  const data = await res.json();
  const features = Array.isArray(data?.features) ? data.features : [];
  return features.map((f) => ({
    label: f.properties?.label,
    city: f.properties?.city,
    type: f.properties?.type,
    lat: f.geometry?.coordinates?.[1],
    lng: f.geometry?.coordinates?.[0],
  }));
}
