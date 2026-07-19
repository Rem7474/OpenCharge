import { useRef, useState } from "react";
import { Radio, X } from "lucide-react";
import { formatSourceLabel, formatPlanLabel } from "../utils/format.js";
import { useOperatorSources } from "../hooks/useOperatorSources.js";
import { useDialogA11y } from "../hooks/useDialogA11y.js";

/**
 * Multi-select, searchable list of tariff sources ("operators" in the UI).
 * The list itself, and each source's available price plans (public/app/
 * subscription for Electra, a single "standard" plan for most sources),
 * comes from GET /sources so new sources/plans show up as soon as they're
 * ingested, with no frontend code change.
 *
 * Opens as its own centered floating overlay (backdrop + modal card, same
 * treatment FilterPanel itself used before it got docked to a corner) —
 * a full network picker warrants more room and more attention than a
 * small anchored dropdown, especially once there are many sources to
 * search through. Behaves like a real modal dialog (role="dialog",
 * Escape to close, focus trapped inside, focus returned to the toggle
 * button on close) via useDialogA11y.
 */
export default function OperatorFilter({ selectedSources, onToggleSource, onSelectPlan }) {
  const [query, setQuery] = useState("");
  const [open, setOpen] = useState(false);
  const panelRef = useRef(null);
  const searchRef = useRef(null);
  const close = () => setOpen(false);
  // Re-fetches every time the picker opens (refetchKey=open): an ingestion
  // run can add a new source (or plan) mid-session, and a mount-time-only
  // fetch would never reflect that until a full page reload.
  const { sources: allSources, error } = useOperatorSources(open);
  useDialogA11y(panelRef, open, close, searchRef);

  const filtered = allSources.filter((s) => s.id.toLowerCase().includes(query.trim().toLowerCase()));
  const selectedIds = Object.keys(selectedSources);

  const summary = selectedIds.length === 0 ? "Tous les réseaux" : selectedIds.map(formatSourceLabel).join(", ");

  return (
    <div className="operator-filter">
      <button type="button" className="operator-filter-toggle" onClick={() => setOpen(true)} aria-expanded={open}>
        {summary} ▾
      </button>
      {open && (
        <div className="operator-panel-overlay" onClick={close}>
          <div
            className="operator-panel"
            role="dialog"
            aria-modal="true"
            aria-label="Réseaux"
            ref={panelRef}
            onClick={(e) => e.stopPropagation()}
          >
            <div className="operator-panel-header">
              <h3>
                <Radio size={18} strokeWidth={2.2} /> Réseaux
              </h3>
              <button type="button" className="close-btn" onClick={close} aria-label="Fermer">
                <X size={15} strokeWidth={2.2} />
              </button>
            </div>
            <div className="operator-panel-body">
              <input
                ref={searchRef}
                type="search"
                className="operator-filter-search"
                placeholder="Rechercher un réseau…"
                value={query}
                onChange={(e) => setQuery(e.target.value)}
              />
              <ul className="operator-filter-list">
                {error && <li className="operator-filter-empty">Impossible de contacter le serveur ({error}).</li>}
                {!error && filtered.length === 0 && <li className="operator-filter-empty">Aucun réseau trouvé</li>}
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
          </div>
        </div>
      )}
    </div>
  );
}
