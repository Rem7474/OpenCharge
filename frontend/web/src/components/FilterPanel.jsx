import { useEffect, useRef } from "react";
import { SlidersHorizontal, Radio, Zap, Euro, X, RotateCcw } from "lucide-react";
import OperatorFilter from "./OperatorFilter.jsx";
import ConnectorFilter from "./ConnectorFilter.jsx";
import { PRICE_MODE_PER_KWH, PRICE_MODE_RECHARGE } from "../utils/pricing.js";
import { useDialogA11y } from "../hooks/useDialogA11y.js";

/**
 * Floating "Filtrer par" card grouping every station-list filter (networks,
 * connector/power, price display, show-all toggle) in one place. Docked to
 * the top-right of the map — same side/positioning treatment as
 * StationDetails' sidebar — rather than a centered modal covering the map:
 * a filter panel is something you keep glancing at while panning the map
 * underneath it. It still behaves like a dialog for keyboard/screen-reader
 * users (Escape closes it, focus is trapped inside and returned to the
 * "Filtrer" toggle on close — see docs/audit-ux-2026-07.md §1.2), even
 * though visually it's docked rather than a centered overlay.
 */
export default function FilterPanel({
  selectedSources,
  onToggleSource,
  onSelectPlan,
  priceMode,
  onChangePriceMode,
  chargeKWh,
  onChangeChargeKWh,
  chargeMinutes,
  onChangeChargeMinutes,
  showAllStations,
  onChangeShowAllStations,
  excludeSubscriptionPlans,
  onChangeExcludeSubscriptionPlans,
  selectedConnectorTypes,
  onToggleConnectorType,
  minPowerKw,
  onChangeMinPowerKw,
  minPriceCentsPerKwh,
  onChangeMinPriceCentsPerKwh,
  maxPriceCentsPerKwh,
  onChangeMaxPriceCentsPerKwh,
  onClose,
  onResetFilters,
}) {
  const panelRef = useRef(null);

  useEffect(() => {
    function handleClickOutside(e) {
      if (panelRef.current && !panelRef.current.contains(e.target)) {
        onClose();
      }
    }
    document.addEventListener("mousedown", handleClickOutside);
    return () => document.removeEventListener("mousedown", handleClickOutside);
  }, [onClose]);

  useDialogA11y(panelRef, true, onClose);

  // In "recharge" mode, minPriceCentsPerKwh/maxPriceCentsPerKwh hold a total
  // session cost (in cents) rather than a €/kWh rate — the backend switches
  // what it filters by based on whether chargeKWh/chargeMinutes are sent
  // alongside them (see StationMarkers, api/stations.js, and the backend's
  // GET /stations docs), so this panel never needs to convert units itself:
  // both modes are just plain cents, displayed as euros.
  const isRecharge = priceMode === PRICE_MODE_RECHARGE;
  const centsToDisplay = (cents) => (cents == null ? "" : cents / 100);
  const displayToCents = (value) => (value === "" ? null : Math.round(Number(value) * 100));

  return (
    <div className="filter-panel" role="dialog" aria-modal="true" aria-label="Filtrer par" ref={panelRef}>
      <div className="filter-panel-header">
        <h3>
          <SlidersHorizontal size={18} strokeWidth={2.2} />
          Filtrer par
        </h3>
        <button type="button" className="close-btn" onClick={onClose} aria-label="Fermer">
          <X size={15} strokeWidth={2.2} />
        </button>
      </div>

      <div className="filter-panel-body">
        <section className="filter-section">
          <div className="filter-section-label">
            <Radio size={14} strokeWidth={2.2} /> Réseaux
          </div>
          <OperatorFilter selectedSources={selectedSources} onToggleSource={onToggleSource} onSelectPlan={onSelectPlan} />
        </section>

        <section className="filter-section">
          <div className="filter-section-label">
            <Zap size={14} strokeWidth={2.2} /> Connecteurs &amp; puissance
          </div>
          <ConnectorFilter
            selectedConnectorTypes={selectedConnectorTypes}
            onToggleConnectorType={onToggleConnectorType}
            minPowerKw={minPowerKw}
            onChangeMinPowerKw={onChangeMinPowerKw}
          />
        </section>

        <section className="filter-section">
          <div className="filter-section-label">
            <Euro size={14} strokeWidth={2.2} /> Prix
          </div>
          <div className="price-mode-toggle" role="group" aria-label="Mode d'affichage du prix">
            <button
              type="button"
              aria-pressed={priceMode === PRICE_MODE_PER_KWH}
              onClick={() => onChangePriceMode(PRICE_MODE_PER_KWH)}
            >
              €/kWh
            </button>
            <button
              type="button"
              aria-pressed={priceMode === PRICE_MODE_RECHARGE}
              onClick={() => onChangePriceMode(PRICE_MODE_RECHARGE)}
            >
              Recharge
            </button>
          </div>
          {priceMode === PRICE_MODE_RECHARGE && (
            <div className="filter-price-inputs">
              <label>
                <input
                  type="number"
                  min={1}
                  max={200}
                  className="kwh-input"
                  value={chargeKWh}
                  onChange={(e) => onChangeChargeKWh(Number(e.target.value))}
                />{" "}
                kWh
              </label>
              <label>
                <input
                  type="number"
                  min={1}
                  max={1440}
                  className="minutes-input"
                  value={chargeMinutes}
                  onChange={(e) => onChangeChargeMinutes(Number(e.target.value))}
                />{" "}
                min de charge
              </label>
            </div>
          )}

          <div className="filter-price-range">
            <span className="filter-price-range-label">
              Fourchette de prix ({isRecharge ? `total pour ${chargeKWh} kWh / ${chargeMinutes} min` : "€/kWh"})
            </span>
            <div className="filter-price-range-inputs">
              <input
                type="number"
                min={0}
                step={0.01}
                placeholder="Min"
                aria-label={isRecharge ? "Prix minimum pour la recharge" : "Prix minimum en €/kWh"}
                value={centsToDisplay(minPriceCentsPerKwh)}
                onChange={(e) => onChangeMinPriceCentsPerKwh(displayToCents(e.target.value))}
              />
              <span>–</span>
              <input
                type="number"
                min={0}
                step={0.01}
                placeholder="Max"
                aria-label={isRecharge ? "Prix maximum pour la recharge" : "Prix maximum en €/kWh"}
                value={centsToDisplay(maxPriceCentsPerKwh)}
                onChange={(e) => onChangeMaxPriceCentsPerKwh(displayToCents(e.target.value))}
              />
            </div>
          </div>

          <label className="show-all-stations-toggle">
            <input
              type="checkbox"
              checked={excludeSubscriptionPlans}
              onChange={(e) => onChangeExcludeSubscriptionPlans(e.target.checked)}
            />
            Exclure les tarifs abonnés
          </label>
        </section>

        <section className="filter-section">
          <label className="show-all-stations-toggle">
            <input
              type="checkbox"
              checked={showAllStations}
              onChange={(e) => onChangeShowAllStations(e.target.checked)}
            />
            Toutes les bornes IRVE (même sans prix)
          </label>
        </section>
      </div>

      <div className="filter-panel-footer">
        <button type="button" className="filter-reset-btn" onClick={onResetFilters}>
          <RotateCcw size={13} strokeWidth={2.2} /> Réinitialiser les filtres
        </button>
      </div>
    </div>
  );
}
