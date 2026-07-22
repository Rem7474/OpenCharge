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

// A tariff's own kind (ac/dc/mixed) "applies to" an ac/dc bucket when it
// either matches exactly or is "mixed" (price applies regardless of
// connector — see backend station_repo.go's `t.kind IN ('ac', 'mixed')`,
// which this mirrors). A plain `t.kind === bucket` check alone drops every
// mixed-kind tariff (e.g. Lidl's single connector-agnostic price) from
// consideration, which used to make a station's "best price" miss a
// cheaper mixed-kind tariff entirely.
export function tariffAppliesToBucket(tariff, bucket) {
  return tariff.kind === bucket || tariff.kind === "mixed";
}

// Some sources (currently only Freshmile) can attach both a
// connector-specific tariff (tariff.connector_type set, matching the
// station's own connector) and a generic one (unset) to the very same
// station, source and plan — see backend station_repo.go's stationListFrom
// comment for why. Given a set of same-source/plan candidates, the
// connector-specific one is the accurate price for this exact station and
// must win regardless of price; this mirrors that dedup client-side so
// StationDetails' "best price" agrees with the map marker's (which is
// computed server-side the same way).
export function preferConnectorMatch(tariffs, stationConnectorType) {
  if (!stationConnectorType) return tariffs;
  const exact = tariffs.filter((t) => t.connector_type === stationConnectorType);
  return exact.length > 0 ? exact : tariffs;
}

function cheapestOf(tariffs) {
  return tariffs.reduce((min, t) => (currentEnergyPriceCentsPerKWh(t) < currentEnergyPriceCentsPerKWh(min) ? t : min));
}

/**
 * Pick, among a (source, plan)'s tariffs, the one applicable to the
 * station's own connector kind (mixed-kind tariffs count for either — see
 * tariffAppliesToBucket), preferring a connector-specific tariff over a
 * generic one from the same source when both exist (preferConnectorMatch —
 * mirrors backend station_repo.go's stationListFrom dedup, so this agrees
 * with the price the map marker shows for the same source/plan).
 */
export function bestTariffForSource(tariffs, source, plan, connectorKind, stationConnectorType) {
  let candidates = tariffs.filter(
    (t) => t.source === source && t.plan === plan && t.energy_price_cents_per_kwh != null
  );
  if (connectorKind) {
    const bucketMatches = candidates.filter((t) => tariffAppliesToBucket(t, connectorKind));
    if (bucketMatches.length > 0) candidates = bucketMatches;
  }
  if (candidates.length === 0) return null;
  return cheapestOf(preferConnectorMatch(candidates, stationConnectorType));
}

/**
 * Ranks by currentEnergyPriceCentsPerKWh, not the raw
 * energy_price_cents_per_kwh field: for a windowed tariff (Electra) that
 * field is a snapshot fixed at the last ingestion run, so ranking by it
 * directly can pick a tariff that was cheapest at ingestion time but isn't
 * live right now.
 *
 * Dedupes per (source, plan) — preferring a connector-specific tariff over
 * a generic one from that *same* source, exactly like bestTariffForSource —
 * before taking the overall minimum. Applying that connector preference at
 * the top level instead (favoring any connector-exact-match tariff over
 * the cheapest known price, regardless of source) was a real bug: a single
 * source's connector-specific tariff could suppress an unrelated, cheaper
 * tariff from a completely different source that has nothing to do with
 * that connector granularity (see backend station_repo.go's stationListFrom
 * comment — this is the client-side mirror of that same fix).
 */
export function cheapestTariff(tariffs, connectorKind, stationConnectorType) {
  let candidates = tariffs.filter((t) => t.energy_price_cents_per_kwh != null);
  if (connectorKind) {
    const bucketMatches = candidates.filter((t) => tariffAppliesToBucket(t, connectorKind));
    if (bucketMatches.length > 0) candidates = bucketMatches;
  }
  if (candidates.length === 0) return null;

  const bySourcePlan = new Map();
  for (const t of candidates) {
    const key = `${t.source}:${t.plan}`;
    if (!bySourcePlan.has(key)) bySourcePlan.set(key, []);
    bySourcePlan.get(key).push(t);
  }
  const perSourceBest = Array.from(bySourcePlan.values(), (rows) => cheapestOf(preferConnectorMatch(rows, stationConnectorType)));
  return cheapestOf(perSourceBest);
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
/**
 * A backend "station" is really a single connector (point of charge): a
 * physical site with several connectors comes back as several same-location
 * rows (see backend/internal/domain/station.go's IRVEIDStation vs
 * IRVEIDPDC). Grouped into one site for display (see
 * utils/stationGrouping.js), the site's map marker shows whichever
 * connector is cheapest — same logic as pickPriceCentsPerKWh per connector,
 * take the overall minimum — rather than a marker per connector stacked on
 * the exact same spot. hasSelection mirrors the same flag callers already
 * pass to pickPriceCentsPerKWh: which of pricingSummary/
 * selectedSourcesPricing to read from each connector.
 */
export function cheapestPriceAcrossStations(stations, hasSelection) {
  let best = null;
  for (const station of stations) {
    const pricing = hasSelection ? station.selectedSourcesPricing : station.pricingSummary;
    const connectorType = station.connectors?.[0]?.kind;
    const price = pickPriceCentsPerKWh(pricing, connectorType);
    if (price != null && (best == null || price < best)) best = price;
  }
  return best;
}

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

// Minimum saving (%) worth surfacing as a recommendation — a 1-2% gap
// between "now" and the cheapest window isn't worth telling anyone to
// change their charging habits over.
const MIN_OFFPEAK_SAVINGS_PERCENT = 5;

/**
 * Whether waiting for this tariff's cheapest window would meaningfully beat
 * the price right now — the "charge at off-peak hours" tip shown next to
 * HourlyPriceChart. null when the tariff has no varying price (see
 * hasHourlyPricing), when the current price already IS the cheapest window
 * (nothing to recommend), or when the gap is too small to bother with (see
 * MIN_OFFPEAK_SAVINGS_PERCENT).
 */
export function offPeakRecommendation(tariff) {
  if (!hasHourlyPricing(tariff)) return null;
  const priced = tariff.extra.windows.filter((w) => w.energyPriceCentsPerKwh != null);
  if (priced.length < 2) return null;

  const currentPriceCentsPerKWh = currentEnergyPriceCentsPerKWh(tariff);
  if (currentPriceCentsPerKWh == null) return null;

  const cheapest = priced.reduce((min, w) => (w.energyPriceCentsPerKwh < min.energyPriceCentsPerKwh ? w : min));
  if (cheapest.energyPriceCentsPerKwh >= currentPriceCentsPerKWh) return null;

  const savingsPercent = ((currentPriceCentsPerKWh - cheapest.energyPriceCentsPerKwh) / currentPriceCentsPerKWh) * 100;
  if (savingsPercent < MIN_OFFPEAK_SAVINGS_PERCENT) return null;

  return {
    startTime: cheapest.startTime,
    endTime: cheapest.endTime,
    priceCentsPerKWh: cheapest.energyPriceCentsPerKwh,
    currentPriceCentsPerKWh,
    savingsPercent,
  };
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
// Default assumptions for the essence/électrique cost comparison (see
// thermalEquivalentCost) — editable by the user (see FilterPanel), not
// persisted across sessions: same treatment as chargeKWh/chargeMinutes.
// Plausible round numbers for a typical compact EV/petrol car rather than a
// real vehicle profile, which this app doesn't model.
export const DEFAULT_EV_CONSUMPTION_KWH_PER_100KM = 17;
export const DEFAULT_THERMAL_CONSUMPTION_L_PER_100KM = 6.5;

/**
 * What the same chargeKWh of energy is worth in distance, and what
 * covering that same distance would cost in fuel — the "essence/électrique"
 * comparison shown alongside a recharge's cost (see StationDetails.jsx's
 * FuelComparison). Deliberately doesn't compare against a specific tariff's
 * price itself: callers already have that via tariffCostBreakdown and can
 * diff the two totals themselves.
 *
 * Returns null when any input is missing or non-positive — in particular
 * before hooks/useFuelPrice.js has resolved a real fuel price.
 */
export function thermalEquivalentCost({
  chargeKWh,
  evConsumptionKWhPer100Km,
  thermalConsumptionLPer100Km,
  fuelPriceCentsPerLiter,
}) {
  if (
    !(chargeKWh > 0) ||
    !(evConsumptionKWhPer100Km > 0) ||
    !(thermalConsumptionLPer100Km > 0) ||
    !(fuelPriceCentsPerLiter > 0)
  ) {
    return null;
  }
  const km = (chargeKWh / evConsumptionKWhPer100Km) * 100;
  const liters = (km / 100) * thermalConsumptionLPer100Km;
  const thermalCostCents = liters * fuelPriceCentsPerLiter;
  return { km, thermalCostCents };
}

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
