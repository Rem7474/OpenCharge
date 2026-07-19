import { useEffect, useMemo, useState } from "react";
import { X, MapPin, Zap, Clock, Building2, Accessibility, Cable, Tag, Star } from "lucide-react";
import { fetchStationDetails } from "../api/stations.js";
import {
  connectorPriceKind,
  currentEnergyPriceCentsPerKWh,
  formatPrice,
  hasHourlyPricing,
  tariffCostBreakdown,
  PRICE_MODE_RECHARGE,
  SUBSCRIPTION_PLAN,
} from "../utils/pricing.js";
import { formatSourceLabel, formatPlanLabel, formatConnectorLabel, formatUpdatedAt, friendlyFetchErrorMessage } from "../utils/format.js";
import HourlyPriceChart from "./HourlyPriceChart.jsx";

// Pick, among a (source, plan)'s tariffs, the one matching the station's
// own connector kind (falling back to any tariff from that source/plan).
function bestTariffForSource(tariffs, source, plan, connectorKind) {
  const candidates = tariffs.filter(
    (t) => t.source === source && t.plan === plan && t.energy_price_cents_per_kwh != null
  );
  if (candidates.length === 0) return null;
  const matching = connectorKind ? candidates.find((t) => t.kind === connectorKind) : null;
  return matching ?? candidates[0];
}

// Ranks by currentEnergyPriceCentsPerKWh, not the raw
// energy_price_cents_per_kwh field: for a windowed tariff (Electra) that
// field is a snapshot fixed at the last ingestion run, so ranking by it
// directly can pick a tariff that was cheapest at ingestion time but isn't
// live right now.
function cheapestTariff(tariffs, connectorKind) {
  const candidates = tariffs.filter((t) => t.energy_price_cents_per_kwh != null);
  if (candidates.length === 0) return null;
  const pool = connectorKind ? candidates.filter((t) => t.kind === connectorKind) : candidates;
  const from = pool.length > 0 ? pool : candidates;
  return from.reduce((min, t) => (currentEnergyPriceCentsPerKWh(t) < currentEnergyPriceCentsPerKWh(min) ? t : min));
}

// TariffCost renders a tariff's price for the active mode: in "recharge"
// mode, a breakdown of every cost component the tariff actually carries
// (energy for chargeKWh, a per-minute rate for chargeMinutes, and any flat
// session fee) plus their total, since a session's real cost can combine
// all three (e.g. Izivia's "2,3€ la session de charge puis 0,51€/kWh").
// In "€/kWh" mode, just the headline energy rate — a blended total doesn't
// make sense as a per-unit figure.
function TariffCost({ tariff, priceMode, chargeKWh, chargeMinutes }) {
  if (priceMode !== PRICE_MODE_RECHARGE) {
    return tariff.energy_price_cents_per_kwh != null ? (
      <div className="price">{formatPrice(tariff.energy_price_cents_per_kwh, priceMode, chargeKWh)}</div>
    ) : null;
  }
  const { energy, time, fee, total } = tariffCostBreakdown(tariff, chargeKWh, chargeMinutes);
  if (total == null) return null;
  return (
    <div className="tariff-cost-breakdown">
      {energy != null && (
        <div>
          Énergie ({chargeKWh} kWh) : {energy.toFixed(2)} €
        </div>
      )}
      {time != null && (
        <div>
          Temps ({chargeMinutes} min) : {time.toFixed(2)} €
        </div>
      )}
      {fee != null && <div>Frais de session : {fee.toFixed(2)} €</div>}
      <div className="price">Total estimé : {total.toFixed(2)} €</div>
    </div>
  );
}

// Single place that decides how to render a tariff's price: an hourly chart
// for tariffs whose price varies across the day, a cost breakdown/headline
// rate otherwise. Used everywhere a tariff's price is shown (the per-source
// blocks, the "best overall" block, and each row of "Tous les tarifs")
// instead of repeating the same hasHourlyPricing(...) ? <A/> : <B/> check
// three times.
function TariffDisplay({ tariff, priceMode, chargeKWh, chargeMinutes }) {
  return hasHourlyPricing(tariff) ? (
    <HourlyPriceChart tariff={tariff} priceMode={priceMode} chargeKWh={chargeKWh} />
  ) : (
    <TariffCost tariff={tariff} priceMode={priceMode} chargeKWh={chargeKWh} chargeMinutes={chargeMinutes} />
  );
}

function TariffRow({ tariff, priceMode, chargeKWh, chargeMinutes }) {
  const updatedAt = formatUpdatedAt(tariff.updated_at);
  return (
    <div className="tariff-row">
      <div className="source">
        {tariff.source} · {formatPlanLabel(tariff.plan)} · {tariff.kind}
      </div>
      <TariffDisplay tariff={tariff} priceMode={priceMode} chargeKWh={chargeKWh} chargeMinutes={chargeMinutes} />
      {tariff.service_fee_percent != null && <div>Frais de service : {tariff.service_fee_percent}%</div>}
      {priceMode !== PRICE_MODE_RECHARGE && tariff.session_price_cents_per_min != null && (
        <div>{(tariff.session_price_cents_per_min / 100).toFixed(2)} € / min</div>
      )}
      {priceMode !== PRICE_MODE_RECHARGE && tariff.session_fee_cents != null && (
        <div>{(tariff.session_fee_cents / 100).toFixed(2)} € / session</div>
      )}
      {tariff.raw_text && <div className="raw-text">{tariff.raw_text}</div>}
      {updatedAt && <div className="updated-at">Mis à jour le {updatedAt}</div>}
    </div>
  );
}

export default function StationDetails({
  stationId,
  onClose,
  selectedSources,
  priceMode,
  chargeKWh,
  chargeMinutes,
  excludeSubscriptionPlans,
}) {
  const [data, setData] = useState(null);
  const [error, setError] = useState(null);

  useEffect(() => {
    if (!stationId) return;
    const controller = new AbortController();
    setData(null);
    setError(null);
    fetchStationDetails(stationId, { signal: controller.signal })
      .then(setData)
      .catch((err) => {
        if (err.name !== "AbortError") setError(err);
      });
    return () => controller.abort();
  }, [stationId]);

  // GET /stations/{id} always returns every known tariff, including
  // subscription-plan ones — the "Exclure les tarifs abonnés" filter is
  // applied client-side here so the detail panel matches what the map
  // marker already shows (the marker's price comes from the API's own
  // excludeSubscriptionPlans param — see api/stations.js).
  const tariffs = useMemo(() => {
    const all = data?.tariffs ?? [];
    return excludeSubscriptionPlans ? all.filter((t) => t.plan !== SUBSCRIPTION_PLAN) : all;
  }, [data, excludeSubscriptionPlans]);

  const connectorKind = data ? connectorPriceKind(data.station.connectors?.[0]?.kind) : null;
  const selectedEntries = Object.entries(selectedSources);
  const selectedTariffs = useMemo(
    () =>
      selectedEntries
        .map(([source, plan]) => ({ source, plan, tariff: bestTariffForSource(tariffs, source, plan, connectorKind) }))
        .filter((entry) => entry.tariff != null),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [tariffs, connectorKind, selectedSources]
  );
  const cheapestSelected = useMemo(
    () =>
      selectedTariffs.length > 0
        ? selectedTariffs.reduce((min, e) =>
            currentEnergyPriceCentsPerKWh(e.tariff) < currentEnergyPriceCentsPerKWh(min.tariff) ? e : min
          )
        : null,
    [selectedTariffs]
  );
  const overallBest = useMemo(() => (data ? cheapestTariff(tariffs, connectorKind) : null), [data, tariffs, connectorKind]);
  const overallBeatsSelection =
    overallBest &&
    (!cheapestSelected ||
      currentEnergyPriceCentsPerKWh(overallBest) < currentEnergyPriceCentsPerKWh(cheapestSelected.tariff));

  if (!stationId) return null;

  return (
    <div className="sidebar">
      <div aria-live="polite">
        {error && (
          <p role="alert">
            {friendlyFetchErrorMessage(error)}
          </p>
        )}
        {!data && !error && <p>Chargement…</p>}
      </div>
      {data && (
        <>
          <div className="station-header">
            <div className="station-header-text">
              <h2>{data.station.name || "Station sans nom"}</h2>
              <div className="station-header-sub">
                {data.station.operator || "Opérateur inconnu"}
                {data.station.enseigne ? ` · ${data.station.enseigne}` : ""}
              </div>
            </div>
            <button className="close-btn" onClick={onClose} aria-label="Fermer">
              <X size={15} strokeWidth={2.2} />
            </button>
          </div>

          <div className="station-meta-card">
            <div className="station-meta-row">
              <MapPin size={15} strokeWidth={2} className="station-meta-icon" />
              <span>
                {data.station.address.street}
                <br />
                {data.station.address.postalCode} {data.station.address.city}
              </span>
            </div>

            {data.station.connectors.length > 0 && (
              <div className="connector-badges">
                {data.station.connectors.map((c, i) => (
                  <span className="connector-badge" key={i}>
                    <Zap size={13} strokeWidth={2.2} />
                    {formatConnectorLabel(c.kind)}
                    {c.maxPowerKw ? ` · ${c.maxPowerKw}kW` : ""}
                  </span>
                ))}
              </div>
            )}

            <div className="station-meta-row">
              <Clock size={15} strokeWidth={2} className="station-meta-icon" />
              <span>
                {data.station.accessType || "Accès inconnu"} · {data.station.is24_7 ? "24/7" : "horaires limités"}
                {data.station.openingHours && data.station.openingHours !== "24/7" && ` (${data.station.openingHours})`}
              </span>
            </div>

            {data.station.pdcCount != null && (
              <div className="station-meta-row">
                <Building2 size={15} strokeWidth={2} className="station-meta-icon" />
                <span>{data.station.pdcCount} point(s) de charge sur site</span>
              </div>
            )}
            {data.station.accessibilityPmr && (
              <div className="station-meta-row">
                <Accessibility size={15} strokeWidth={2} className="station-meta-icon" />
                <span>{data.station.accessibilityPmr}</span>
              </div>
            )}
            {data.station.cableT2Attached != null && (
              <div className="station-meta-row">
                <Cable size={15} strokeWidth={2} className="station-meta-icon" />
                <span>Câble T2 {data.station.cableT2Attached ? "attaché" : "non attaché"}</span>
              </div>
            )}
          </div>

          <h3 className="section-heading">
            <Tag size={15} strokeWidth={2.2} /> Prix
          </h3>
          {selectedTariffs.length === 0 && selectedEntries.length > 0 && (
            <p>Aucun tarif connu à cette station pour les réseaux sélectionnés.</p>
          )}
          {selectedTariffs.map(({ source, plan, tariff }) => (
            <div className="station-price-block" key={`${source}:${plan}`}>
              <div className="source-name">
                {formatSourceLabel(source)} · {formatPlanLabel(plan)}
              </div>
              <TariffDisplay tariff={tariff} priceMode={priceMode} chargeKWh={chargeKWh} chargeMinutes={chargeMinutes} />
            </div>
          ))}
          {overallBest && overallBeatsSelection && (
            <div className="station-price-block best-overall">
              <div className="source-name">
                <Star size={12} strokeWidth={2.4} /> Meilleur prix toutes sources · {formatSourceLabel(overallBest.source)}{" "}
                · {formatPlanLabel(overallBest.plan)}
              </div>
              <TariffDisplay tariff={overallBest} priceMode={priceMode} chargeKWh={chargeKWh} chargeMinutes={chargeMinutes} />
            </div>
          )}
          {!overallBest && selectedEntries.length === 0 && <p>Aucun tarif connu pour cette station.</p>}

          <h3 className="section-heading">Tous les tarifs</h3>
          {tariffs.length === 0 && <p>Aucun tarif connu pour cette station.</p>}
          {tariffs.map((t, i) => (
            <TariffRow tariff={t} priceMode={priceMode} chargeKWh={chargeKWh} chargeMinutes={chargeMinutes} key={i} />
          ))}
        </>
      )}
    </div>
  );
}
