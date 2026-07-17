import { useEffect, useRef, useState } from "react";
import { formatConnectorLabel } from "../utils/format.js";

// Mirrors backend/internal/domain/connector.go's canonical vocabulary.
// Unlike OperatorFilter's source list, this is a small, fixed set — no
// endpoint to fetch it from, no dropdown-open refetch needed. "unknown" is
// deliberately excluded: filtering *for* "we don't know the connector"
// isn't a meaningful user choice.
const CONNECTOR_TYPES = ["CCS", "CHAdeMO", "T2", "EF", "other"];

/**
 * Multi-select connector-type filter plus a minimum-power input, styled
 * and structured like OperatorFilter's dropdown/checkbox pattern.
 */
export default function ConnectorFilter({
  selectedConnectorTypes,
  onToggleConnectorType,
  minPowerKw,
  onChangeMinPowerKw,
}) {
  const [open, setOpen] = useState(false);
  const containerRef = useRef(null);

  useEffect(() => {
    function handleClickOutside(e) {
      if (containerRef.current && !containerRef.current.contains(e.target)) {
        setOpen(false);
      }
    }
    document.addEventListener("mousedown", handleClickOutside);
    return () => document.removeEventListener("mousedown", handleClickOutside);
  }, []);

  const summary =
    selectedConnectorTypes.length === 0 && minPowerKw == null
      ? "Tous les connecteurs"
      : [...selectedConnectorTypes.map(formatConnectorLabel), minPowerKw != null ? `≥${minPowerKw}kW` : null]
          .filter(Boolean)
          .join(", ");

  return (
    <div className="connector-filter" ref={containerRef}>
      <button
        type="button"
        className="connector-filter-toggle"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
      >
        {summary} ▾
      </button>
      {open && (
        <div className="connector-filter-panel">
          <ul className="connector-filter-list">
            {CONNECTOR_TYPES.map((type) => (
              <li key={type}>
                <label>
                  <input
                    type="checkbox"
                    checked={selectedConnectorTypes.includes(type)}
                    onChange={() => onToggleConnectorType(type)}
                  />
                  {formatConnectorLabel(type)}
                </label>
              </li>
            ))}
          </ul>
          <label className="min-power-input">
            Puissance min.
            <input
              type="number"
              min={0}
              max={400}
              placeholder="kW"
              value={minPowerKw ?? ""}
              onChange={(e) => onChangeMinPowerKw(e.target.value === "" ? null : Number(e.target.value))}
            />
            kW
          </label>
        </div>
      )}
    </div>
  );
}
