import OperatorFilter from "./OperatorFilter.jsx";
import { PRICE_MODE_PER_KWH, PRICE_MODE_RECHARGE } from "../utils/pricing.js";

export default function FilterBar({
  selectedSources,
  onToggleSource,
  onSelectPlan,
  priceMode,
  onChangePriceMode,
  chargeKWh,
  onChangeChargeKWh,
  chargeMinutes,
  onChangeChargeMinutes,
}) {
  return (
    <div className="filter-bar">
      <div className="filter-group">
        <span className="filter-label">Réseaux</span>
        <OperatorFilter selectedSources={selectedSources} onToggleSource={onToggleSource} onSelectPlan={onSelectPlan} />
      </div>

      <div className="filter-group">
        <span className="filter-label">Prix</span>
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
          <>
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
          </>
        )}
      </div>
    </div>
  );
}
