import { useEffect, useState } from "react";
import { fetchSources } from "../api/stations.js";

/**
 * Fetches GET /sources (every tariff source + its price plans) and keeps it
 * up to date. Shared by OperatorFilter and OnboardingScreen, which used to
 * each reimplement this fetch/loading/error dance independently.
 *
 * refetchKey lets a caller force a re-fetch (OperatorFilter re-fetches every
 * time its picker opens, since an ingestion run can add a new source or
 * plan mid-session) without duplicating the effect itself — pass a value
 * that changes when a re-fetch should happen (e.g. `open`), or omit it to
 * only fetch once on mount.
 */
export function useOperatorSources(refetchKey) {
  const [sources, setSources] = useState([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(null);

  useEffect(() => {
    const controller = new AbortController();
    setError(null);
    setLoading(true);
    fetchSources({ signal: controller.signal })
      .then((data) => setSources(data ?? []))
      .catch((err) => {
        if (err.name !== "AbortError") {
          console.error(err);
          setError(err.message);
        }
      })
      .finally(() => setLoading(false));
    return () => controller.abort();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [refetchKey]);

  return { sources, loading, error };
}
