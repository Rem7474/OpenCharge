import { useState } from "react";
import FilterPanel from "./FilterPanel.jsx";

/**
 * Top bar: a single "Filtrer" toggle that opens the floating FilterPanel
 * (grouping every station-list filter), plus the onboarding re-open
 * shortcut. All the actual filter props are just forwarded to FilterPanel.
 */
export default function FilterBar(props) {
  const {
    selectedSources,
    selectedConnectorTypes,
    minPowerKw,
    minPriceCentsPerKwh,
    maxPriceCentsPerKwh,
    showAllStations,
    excludeSubscriptionPlans,
    onReopenOnboarding,
  } = props;
  const [open, setOpen] = useState(false);

  const activeCount =
    Object.keys(selectedSources).length +
    selectedConnectorTypes.length +
    (minPowerKw != null ? 1 : 0) +
    (minPriceCentsPerKwh != null ? 1 : 0) +
    (maxPriceCentsPerKwh != null ? 1 : 0) +
    (showAllStations ? 1 : 0) +
    (excludeSubscriptionPlans ? 1 : 0);

  return (
    <div className="filter-bar">
      <button type="button" className="filter-toggle-btn" onClick={() => setOpen(true)} aria-expanded={open}>
        Filtrer{activeCount > 0 ? ` · ${activeCount}` : ""}
      </button>
      {onReopenOnboarding && (
        <button
          type="button"
          className="onboarding-reopen-btn"
          onClick={onReopenOnboarding}
          aria-label="Configurer mes opérateurs"
          title="Configurer mes opérateurs"
        >
          ⚙
        </button>
      )}
      {open && <FilterPanel {...props} onClose={() => setOpen(false)} />}
    </div>
  );
}
