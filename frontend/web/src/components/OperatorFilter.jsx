import { useEffect, useRef, useState } from "react";
import { fetchSources } from "../api/stations.js";
import { formatSourceLabel } from "../utils/format.js";

/**
 * Multi-select, searchable list of tariff sources ("operators" in the UI).
 * The list itself comes from GET /sources so new sources show up as soon
 * as they're ingested, with no frontend code change.
 */
export default function OperatorFilter({ selectedSources, onToggleSource }) {
  const [allSources, setAllSources] = useState([]);
  const [query, setQuery] = useState("");
  const [open, setOpen] = useState(false);
  const containerRef = useRef(null);

  useEffect(() => {
    const controller = new AbortController();
    fetchSources({ signal: controller.signal })
      .then((sources) => setAllSources(sources ?? []))
      .catch((err) => {
        if (err.name !== "AbortError") console.error(err);
      });
    return () => controller.abort();
  }, []);

  useEffect(() => {
    function handleClickOutside(e) {
      if (containerRef.current && !containerRef.current.contains(e.target)) {
        setOpen(false);
      }
    }
    document.addEventListener("mousedown", handleClickOutside);
    return () => document.removeEventListener("mousedown", handleClickOutside);
  }, []);

  const filtered = allSources.filter((s) => s.toLowerCase().includes(query.trim().toLowerCase()));

  const summary =
    selectedSources.length === 0
      ? "Tous les réseaux"
      : selectedSources.map(formatSourceLabel).join(", ");

  return (
    <div className="operator-filter" ref={containerRef}>
      <button
        type="button"
        className="operator-filter-toggle"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
      >
        {summary} ▾
      </button>
      {open && (
        <div className="operator-filter-panel">
          <input
            type="search"
            className="operator-filter-search"
            placeholder="Rechercher un réseau…"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            autoFocus
          />
          <ul className="operator-filter-list">
            {filtered.length === 0 && <li className="operator-filter-empty">Aucun réseau trouvé</li>}
            {filtered.map((source) => (
              <li key={source}>
                <label>
                  <input
                    type="checkbox"
                    checked={selectedSources.includes(source)}
                    onChange={() => onToggleSource(source)}
                  />
                  {formatSourceLabel(source)}
                </label>
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}
