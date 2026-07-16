// IRVE connector type strings (see backend primaryConnectorType) mapped to
// the ac/dc "kind" vocabulary used by station_tariffs.kind, so we know
// whether to read ac_min_cents_per_kwh or dc_min_cents_per_kwh for a given
// station's own connector.
const DC_CONNECTOR_TYPES = new Set(["CCS", "CHAdeMO"]);
const AC_CONNECTOR_TYPES = new Set(["T2", "EF"]);

export function connectorPriceKind(connectorType) {
  if (DC_CONNECTOR_TYPES.has(connectorType)) return "dc";
  if (AC_CONNECTOR_TYPES.has(connectorType)) return "ac";
  return null;
}

/**
 * Pick the €/kWh price (in cents) most relevant to a station: prefer the
 * price matching its own connector kind, fall back to whichever of AC/DC
 * is available.
 */
export function pickPriceCentsPerKWh(pricingSummary, connectorType) {
  if (!pricingSummary) return null;
  const kind = connectorPriceKind(connectorType);
  if (kind === "ac" && pricingSummary.ac_min_cents_per_kwh != null) {
    return pricingSummary.ac_min_cents_per_kwh;
  }
  if (kind === "dc" && pricingSummary.dc_min_cents_per_kwh != null) {
    return pricingSummary.dc_min_cents_per_kwh;
  }
  return pricingSummary.ac_min_cents_per_kwh ?? pricingSummary.dc_min_cents_per_kwh ?? null;
}

/**
 * Turn a { sourceId: planId } selection map into the "source:plan" pairs
 * the API's `source` query param expects (see backend GET /stations docs).
 */
export function sourcePlanPairs(selectedSources) {
  return Object.entries(selectedSources).map(([source, plan]) => `${source}:${plan}`);
}

/** A tariff's windows are only worth charting when the price actually
 * varies across the day; a single window is just the flat price. */
export function hasHourlyPricing(tariff) {
  const windows = tariff?.extra?.windows;
  return Array.isArray(windows) && windows.length > 1;
}

export const PRICE_MODE_PER_KWH = "per_kwh";
export const PRICE_MODE_RECHARGE = "recharge";

/**
 * Format a €/kWh price (in cents) according to the active display mode.
 * In "recharge" mode, returns the total price for chargeKWh kWh of energy
 * (energy price only — session/congestion fees are not included).
 */
export function formatPrice(priceCentsPerKWh, mode, chargeKWh) {
  if (priceCentsPerKWh == null) return null;
  if (mode === PRICE_MODE_RECHARGE) {
    const total = (priceCentsPerKWh / 100) * chargeKWh;
    return `${total.toFixed(2)} €`;
  }
  return `${(priceCentsPerKWh / 100).toFixed(2)} €/kWh`;
}
