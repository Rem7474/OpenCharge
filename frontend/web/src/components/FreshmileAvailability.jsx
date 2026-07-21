import { useEffect, useState } from "react";

const FRESHMILE_LOCATION_API_BASE = "https://prod-driver-api.freshmile.com/charge/api/v2/locations";

/**
 * Polls Freshmile's own public API directly from the browser for a site's
 * live is_available status. The ingested value (see backend
 * ingestion/freshmile.go, station_tariffs.extra) is only as fresh as the
 * last ingestion run and goes stale between runs — this reflects right
 * now, at the cost of one extra request per station the user actually
 * opens (never during map browsing/listing).
 *
 * This is a direct frontend -> Freshmile call, tried first as the simplest
 * option. If it turns out to be blocked by CORS in production, the planned
 * fallback is a small backend proxy endpoint forwarding the same GET — not
 * built yet, since it's only worth adding once the direct call is actually
 * confirmed not to work. Any failure here (CORS, network, non-2xx) quietly
 * hides the badge rather than showing an error: this is a nice-to-have on
 * top of the station's already-known (ingested) data, not something the
 * rest of the page depends on.
 */
export default function FreshmileAvailability({ locationId }) {
  const [status, setStatus] = useState("loading"); // loading | available | unavailable | unknown

  useEffect(() => {
    if (locationId == null) {
      setStatus("unknown");
      return undefined;
    }
    const controller = new AbortController();
    setStatus("loading");
    fetch(`${FRESHMILE_LOCATION_API_BASE}/${locationId}`, { signal: controller.signal })
      .then((res) => (res.ok ? res.json() : Promise.reject(new Error(`http ${res.status}`))))
      .then((body) => {
        const isAvailable = body?.data?.is_available;
        setStatus(isAvailable == null ? "unknown" : isAvailable ? "available" : "unavailable");
      })
      .catch((err) => {
        if (err.name !== "AbortError") setStatus("unknown");
      });
    return () => controller.abort();
  }, [locationId]);

  if (status === "unknown") return null;
  if (status === "loading") {
    return <span className="freshmile-availability freshmile-availability--loading">Statut en direct…</span>;
  }
  return (
    <span className={`freshmile-availability freshmile-availability--${status}`}>
      {status === "available" ? "● Disponible maintenant" : "● Indisponible actuellement"}
    </span>
  );
}
