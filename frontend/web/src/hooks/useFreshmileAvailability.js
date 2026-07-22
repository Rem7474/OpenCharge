import { useEffect, useState } from "react";
import { fetchFreshmileAvailability } from "../api/stations.js";

/**
 * Fetches a Freshmile site's live availability once per locationId (via our
 * own backend proxy — see api/stations.js#fetchFreshmileAvailability and
 * backend api/freshmile.go — a direct browser -> Freshmile call is blocked
 * by CORS in production). Shared by StationDetails, which needs the same
 * fetched data twice over: once for the site-wide "X/Y bornes disponibles"
 * summary, and again per connector (see ConnectorPriceSection) to show
 * each connector kind's own available/total count — fetching it once here
 * instead of once per consumer avoids duplicate requests for the exact
 * same locationId.
 *
 * Returns `data: null` while loading or on any failure (network, CORS,
 * upstream error) — this is a nice-to-have over the station's already-known
 * (ingested) data, so callers should just omit it rather than show an
 * error when it's unavailable.
 */
export function useFreshmileAvailability(locationId) {
  const [data, setData] = useState(null);

  useEffect(() => {
    if (locationId == null) {
      setData(null);
      return undefined;
    }
    const controller = new AbortController();
    setData(null);
    fetchFreshmileAvailability(locationId, { signal: controller.signal })
      .then(setData)
      .catch((err) => {
        if (err.name !== "AbortError") setData(null);
      });
    return () => controller.abort();
  }, [locationId]);

  return data;
}
