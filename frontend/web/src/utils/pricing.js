// IRVE connector type strings (see backend primaryConnectorType) mapped to
// the ac/dc "kind" vocabulary used by station_tariffs.kind, so we know
// whether to read ac_min_cents_per_kwh or dc_min_cents_per_kwh for a given
// station's own connector.
// Mirrors backend/internal/domain/connector.go's TariffKindForConnector —
// keep these two in sync by hand (JS can't import Go constants).
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
 * Bucket a €/kWh price (in cents) into a cheap/mid/expensive tier for the
 * map marker color-coding: <25 cts green, 25-35 orange, >35 red. Always
 * computed from the raw per-kWh rate, never from a formatted/mode-dependent
 * display value — the tier must stay the same regardless of whether the
 * price is shown as €/kWh or as a total for a chosen session size.
 */
export function priceTier(priceCentsPerKWh) {
  if (priceCentsPerKWh == null) return null;
  if (priceCentsPerKWh < 25) return "low";
  if (priceCentsPerKWh <= 35) return "mid";
  return "high";
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
 * (energy price only — session/congestion fees are not included; for a
 * tariff's full cost including its per-minute rate and flat session fee,
 * see tariffCostBreakdown).
 */
export function formatPrice(priceCentsPerKWh, mode, chargeKWh) {
  if (priceCentsPerKWh == null) return null;
  if (mode === PRICE_MODE_RECHARGE) {
    const total = (priceCentsPerKWh / 100) * chargeKWh;
    return `${total.toFixed(2)} €`;
  }
  return `${(priceCentsPerKWh / 100).toFixed(2)} €/kWh`;
}

/**
 * Break a tariff's estimated cost for a chargeKWh/chargeMinutes session
 * down into its known components — energy (€/kWh × kWh), time (a
 * per-minute rate × minutes charging, e.g. Freshmile's "0,40 € par
 * minute"), and a flat session fee (a one-time amount just for starting,
 * e.g. Izivia's "2,3€ la session de charge") — plus their sum. Each
 * component is null when the tariff doesn't carry that kind of price, so
 * callers can render only the lines that apply; total is null only when
 * none of the three are known at all.
 */
export function tariffCostBreakdown(tariff, chargeKWh, chargeMinutes) {
  const energy = tariff.energy_price_cents_per_kwh != null ? (tariff.energy_price_cents_per_kwh / 100) * chargeKWh : null;
  const time = tariff.session_price_cents_per_min != null ? (tariff.session_price_cents_per_min / 100) * chargeMinutes : null;
  const fee = tariff.session_fee_cents != null ? tariff.session_fee_cents / 100 : null;
  if (energy == null && time == null && fee == null) {
    return { energy, time, fee, total: null };
  }
  const total = (energy ?? 0) + (time ?? 0) + (fee ?? 0);
  return { energy, time, fee, total };
}
