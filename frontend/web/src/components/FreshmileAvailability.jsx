import { useEffect, useState } from "react";
import { fetchFreshmileAvailability } from "../api/stations.js";

/**
 * Shows a site's live is_available status, fetched through our own backend
 * (see api/stations.js#fetchFreshmileAvailability and backend
 * api/freshmile.go) rather than calling Freshmile's API directly from the
 * browser — a direct call was tried first, but is blocked by CORS in
 * production (Freshmile's API sends no Access-Control-Allow-Origin header,
 * confirmed against the real deployment).
 *
 * The ingested value (see backend ingestion/freshmile.go,
 * station_tariffs.extra) is only as fresh as the last ingestion run and
 * goes stale between runs — this reflects right now, at the cost of one
 * extra request per station the user actually opens (never during map
 * browsing/listing). Any failure here (network, upstream error) quietly
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
    fetchFreshmileAvailability(locationId, { signal: controller.signal })
      .then((body) => {
        const isAvailable = body?.isAvailable;
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
