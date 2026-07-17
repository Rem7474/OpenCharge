import OperatorFilter from "./OperatorFilter.jsx";
import ConnectorFilter from "./ConnectorFilter.jsx";
import { PRICE_MODE_PER_KWH, PRICE_MODE_RECHARGE } from "../utils/pricing.js";

/**
 * Floating "Filtrer par" card grouping every station-list filter (networks,
 * connector/power, price display, show-all toggle) in one place, styled
 * like the station detail card rather than the old row of separate
 * dropdowns spread across the top bar.
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
  selectedConnectorTypes,
  onToggleConnectorType,
  minPowerKw,
  onChangeMinPowerKw,
  onClose,
}) {
  return (
    <div className="filter-panel-overlay" onClick={onClose}>
      <div className="filter-panel" onClick={(e) => e.stopPropagation()}>
        <div className="filter-panel-header">
          <h3>Filtrer par</h3>
          <button type="button" className="close-btn" onClick={onClose} aria-label="Fermer">
            ✕
          </button>
        </div>

        <div className="filter-panel-body">
          <section className="filter-section">
            <div className="filter-section-label">Réseaux</div>
            <OperatorFilter selectedSources={selectedSources} onToggleSource={onToggleSource} onSelectPlan={onSelectPlan} />
          </section>

          <section className="filter-section">
            <ConnectorFilter
              selectedConnectorTypes={selectedConnectorTypes}
              onToggleConnectorType={onToggleConnectorType}
              minPowerKw={minPowerKw}
              onChangeMinPowerKw={onChangeMinPowerKw}
            />
          </section>

          <section className="filter-section">
            <div className="filter-section-label">Prix</div>
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
      </div>
    </div>
  );
}
