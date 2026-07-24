import { useEffect, useState } from "react";
import { fetchFuelPrice } from "../api/stations.js";

/**
 * Fetches the nationwide-average fuel price once per app session — the
 * backend already caches it for hours (see api/fuelprice.go), so there's no
 * reason to refetch here beyond once. Returns null while loading or on any
 * failure: the essence/électrique comparison is a nice-to-have over the
 * tariff's own known price, so callers should just omit it rather than show
 * an error.
 */
export function useFuelPrice() {
  const [data, setData] = useState(null);

  useEffect(() => {
    const controller = new AbortController();
    fetchFuelPrice({ signal: controller.signal })
      .then(setData)
      .catch((err) => {
        if (err.name !== "AbortError") setData(null);
      });
    return () => controller.abort();
  }, []);

  return data;
}
