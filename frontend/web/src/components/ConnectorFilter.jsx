import { formatConnectorLabel } from "../utils/format.js";

// Mirrors backend/internal/domain/connector.go's canonical vocabulary.
// Unlike OperatorFilter's source list, this is a small, fixed set — no
// endpoint to fetch it from. "unknown" is deliberately excluded: filtering
// *for* "we don't know the connector" isn't a meaningful user choice.
const CONNECTOR_TYPES = ["CCS", "CHAdeMO", "T2", "EF", "other"];

// Discrete power steps for the slider (kW), matching the app's real
// power-tier boundaries rather than an arbitrary numeric range. The first
// step (3 kW) means "no filter" — nearly every station is at least that
// powerful, so it reads as a baseline/"all" position rather than a real
// threshold; the last step (350 kW) is open-ended ("350+").
const POWER_STEPS = [3, 22, 50, 150, 350];

function powerStepIndex(minPowerKw) {
  if (minPowerKw == null) return 0;
  const idx = POWER_STEPS.indexOf(minPowerKw);
  return idx === -1 ? 0 : idx;
}

/**
 * Inline (not a dropdown — meant to live inside FilterPanel) connector-type
 * multi-select, styled as pill toggle buttons, plus a discrete-step power
 * slider.
 */
export default function ConnectorFilter({
  selectedConnectorTypes,
  onToggleConnectorType,
  minPowerKw,
  onChangeMinPowerKw,
}) {
  const stepIdx = powerStepIndex(minPowerKw);

  return (
    <div className="connector-filter-inline">
      <div className="filter-section-label">Puissance de charge (kW)</div>
      <div className="power-slider">
        <input
          type="range"
          min={0}
          max={POWER_STEPS.length - 1}
          step={1}
          value={stepIdx}
          onChange={(e) => {
            const idx = Number(e.target.value);
            onChangeMinPowerKw(idx === 0 ? null : POWER_STEPS[idx]);
          }}
          aria-label="Puissance de charge minimale"
        />
        <div className="power-slider-labels">
          {POWER_STEPS.map((v, i) => (
            <span key={v} className={i === stepIdx ? "active" : ""}>
              {i === POWER_STEPS.length - 1 ? `${v}+` : v}
            </span>
          ))}
        </div>
      </div>

      <div className="filter-section-label">Type de connecteurs</div>
      <div className="connector-pills" role="group" aria-label="Type de connecteurs">
        {CONNECTOR_TYPES.map((type) => (
          <button
            key={type}
            type="button"
            className="connector-pill"
            aria-pressed={selectedConnectorTypes.includes(type)}
            onClick={() => onToggleConnectorType(type)}
          >
            {formatConnectorLabel(type)}
          </button>
        ))}
      </div>
    </div>
  );
}
