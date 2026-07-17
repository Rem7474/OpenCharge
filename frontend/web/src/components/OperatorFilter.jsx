import { useEffect, useRef, useState } from "react";
import { fetchSources } from "../api/stations.js";
import { formatSourceLabel, formatPlanLabel } from "../utils/format.js";

/**
 * Multi-select, searchable list of tariff sources ("operators" in the UI).
 * The list itself, and each source's available price plans (public/app/
 * subscription for Electra, a single "standard" plan for most sources),
 * comes from GET /sources so new sources/plans show up as soon as they're
 * ingested, with no frontend code change.
 */
export default function OperatorFilter({ selectedSources, onToggleSource, onSelectPlan }) {
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

  // Re-fetch every time the dropdown opens: an ingestion run can add a new
  // source (or a new plan on an existing one) at any point during a long
  // browsing session, and the initial mount-time fetch above would
  // otherwise never reflect that until a full page reload.
  useEffect(() => {
    if (!open) return;
    const controller = new AbortController();
    fetchSources({ signal: controller.signal })
      .then((sources) => setAllSources(sources ?? []))
      .catch((err) => {
        if (err.name !== "AbortError") console.error(err);
      });
    return () => controller.abort();
  }, [open]);

  useEffect(() => {
    function handleClickOutside(e) {
      if (containerRef.current && !containerRef.current.contains(e.target)) {
        setOpen(false);
      }
    }
    document.addEventListener("mousedown", handleClickOutside);
    return () => document.removeEventListener("mousedown", handleClickOutside);
  }, []);

  const filtered = allSources.filter((s) => s.id.toLowerCase().includes(query.trim().toLowerCase()));
  const selectedIds = Object.keys(selectedSources);

  const summary = selectedIds.length === 0 ? "Tous les réseaux" : selectedIds.map(formatSourceLabel).join(", ");

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
            {filtered.map((source) => {
              const checked = source.id in selectedSources;
              return (
                <li key={source.id}>
                  <label>
                    <input type="checkbox" checked={checked} onChange={() => onToggleSource(source, checked)} />
                    {formatSourceLabel(source.id)}
                  </label>
                  {checked && source.plans.length > 1 && (
                    <div className="plan-selector" role="group" aria-label={`Palier tarifaire ${source.id}`}>
                      {source.plans.map((plan) => (
                        <button
                          key={plan}
                          type="button"
                          aria-pressed={selectedSources[source.id] === plan}
                          onClick={() => onSelectPlan(source.id, plan)}
                        >
                          {formatPlanLabel(plan)}
                        </button>
                      ))}
                    </div>
                  )}
                </li>
              );
            })}
          </ul>
        </div>
      )}
    </div>
  );
}
