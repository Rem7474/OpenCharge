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
 * Bucket a €/kWh price (in cents) into a cheap/mid/expensive/extreme tier
 * for the map marker color-coding: <28 cts green, 28-35 orange, 35-50 red,
 * >50 black. Always computed from the raw per-kWh rate, never from a
 * formatted/mode-dependent display value — the tier must stay the same
 * regardless of whether the price is shown as €/kWh or as a total for a
 * chosen session size.
 */
export function priceTier(priceCentsPerKWh) {
  if (priceCentsPerKWh == null) return null;
  if (priceCentsPerKWh < 28) return "low";
  if (priceCentsPerKWh <= 35) return "mid";
  if (priceCentsPerKWh <= 50) return "high";
  return "extreme";
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

// Mirrors backend/db/migrations/008_add_current_window_price_fn.up.sql's
// current_window_price SQL function: half-open [start, end) in HH:MM,
// wrapping past midnight (e.g. 22:00-06:00). Kept in sync by hand — same
// reason as the connector-kind sets above, this can't share code with SQL
// or Go. Exported so HourlyPriceChart can reuse it (it needs the price at
// every hour of the day, not just "now").
export function timeInWindow(hm, start, end) {
  if (!start || !end) return false;
  if (start <= end) return hm >= start && hm < end;
  return hm >= start || hm < end;
}

/**
 * A tariff's live €/kWh price right now, for tariffs whose price varies by
 * time of day (currently only Electra — see hasHourlyPricing). Falls back
 * to the tariff's own energy_price_cents_per_kwh when it has no (or only
 * one) window: that field is already the live/only price for those.
 *
 * tariff.energy_price_cents_per_kwh itself is a snapshot the backend fixes
 * once per ingestion run (whichever window covered "now" at that moment —
 * see electra.go's withPlan) and never updates until the next run, so
 * comparing/ranking tariffs by that raw field can pick a stale "best" price
 * once real time has moved into a different window. Always prefer this
 * function over the raw field when a full tariff object (with .extra) is
 * available.
 */
export function currentEnergyPriceCentsPerKWh(tariff) {
  if (!hasHourlyPricing(tariff)) return tariff?.energy_price_cents_per_kwh ?? null;
  const priced = tariff.extra.windows.filter((w) => w.energyPriceCentsPerKwh != null);
  if (priced.length === 0) return tariff.energy_price_cents_per_kwh ?? null;
  const hm = new Date().toTimeString().slice(0, 5);
  const match = priced.find((w) => timeInWindow(hm, w.startTime, w.endTime));
  return (match ?? priced[0]).energyPriceCentsPerKwh;
}

export const PRICE_MODE_PER_KWH = "per_kwh";
export const PRICE_MODE_RECHARGE = "recharge";

// Mirrors backend/internal/domain/tariff.go's TariffPlanSubscription — the
// plan id used for subscriber-only price tiers (Electra, Fastned, eborn).
// Used client-side to filter StationDetails' own tariff list the same way
// the "Exclure les tarifs abonnés" filter already makes the API filter
// pricingSummary/selectedSourcesPricing server-side (see
// api/stations.js#fetchStationsInBBox's excludeSubscriptionPlans param).
export const SUBSCRIPTION_PLAN = "subscription";

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
