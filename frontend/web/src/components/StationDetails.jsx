import { useEffect, useState } from "react";
import { fetchStationDetails } from "../api/stations.js";
import { connectorPriceKind, formatPrice, hasHourlyPricing } from "../utils/pricing.js";
import { formatSourceLabel, formatPlanLabel } from "../utils/format.js";
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

function cheapestTariff(tariffs, connectorKind) {
  const candidates = tariffs.filter((t) => t.energy_price_cents_per_kwh != null);
  if (candidates.length === 0) return null;
  const pool = connectorKind ? candidates.filter((t) => t.kind === connectorKind) : candidates;
  const from = pool.length > 0 ? pool : candidates;
  return from.reduce((min, t) => (t.energy_price_cents_per_kwh < min.energy_price_cents_per_kwh ? t : min));
}

function TariffRow({ tariff, priceMode, chargeKWh }) {
  return (
    <div className="tariff-row">
      <div className="source">
        {tariff.source} · {formatPlanLabel(tariff.plan)} · {tariff.kind}
      </div>
      {hasHourlyPricing(tariff) ? (
        <HourlyPriceChart tariff={tariff} priceMode={priceMode} chargeKWh={chargeKWh} />
      ) : (
        tariff.energy_price_cents_per_kwh != null && (
          <div className="price">{formatPrice(tariff.energy_price_cents_per_kwh, priceMode, chargeKWh)}</div>
        )
      )}
      {tariff.service_fee_percent != null && <div>Frais de service : {tariff.service_fee_percent}%</div>}
      {tariff.session_price_cents_per_min != null && (
        <div>{(tariff.session_price_cents_per_min / 100).toFixed(2)} € / min</div>
      )}
      {tariff.raw_text && <div className="raw-text">{tariff.raw_text}</div>}
    </div>
  );
}

export default function StationDetails({ stationId, onClose, selectedSources, priceMode, chargeKWh }) {
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
        if (err.name !== "AbortError") setError(err.message);
      });
    return () => controller.abort();
  }, [stationId]);

  if (!stationId) return null;

  const connectorKind = data ? connectorPriceKind(data.station.connectors?.[0]?.kind) : null;
  const selectedEntries = Object.entries(selectedSources);
  const selectedTariffs = data
    ? selectedEntries
        .map(([source, plan]) => ({ source, plan, tariff: bestTariffForSource(data.tariffs, source, plan, connectorKind) }))
        .filter((entry) => entry.tariff != null)
    : [];
  const cheapestSelected =
    selectedTariffs.length > 0
      ? selectedTariffs.reduce((min, e) =>
          e.tariff.energy_price_cents_per_kwh < min.tariff.energy_price_cents_per_kwh ? e : min
        )
      : null;
  const overallBest = data ? cheapestTariff(data.tariffs, connectorKind) : null;
  const overallBeatsSelection =
    overallBest &&
    (!cheapestSelected || overallBest.energy_price_cents_per_kwh < cheapestSelected.tariff.energy_price_cents_per_kwh);

  return (
    <div className="sidebar">
      <button className="close-btn" onClick={onClose} aria-label="Fermer">
        ✕
      </button>
      {error && <p>Erreur : {error}</p>}
      {!data && !error && <p>Chargement…</p>}
      {data && (
        <>
          <h2>{data.station.name || "Station sans nom"}</h2>
          <p>
            {data.station.address.street}
            <br />
            {data.station.address.postalCode} {data.station.address.city}
          </p>
          <p>
            Opérateur : {data.station.operator || "—"}
            {data.station.enseigne ? ` (${data.station.enseigne})` : ""}
          </p>
          <p>
            Connecteurs :{" "}
            {data.station.connectors
              .map((c) => `${c.kind}${c.maxPowerKw ? ` ${c.maxPowerKw}kW` : ""}`)
              .join(", ") || "inconnu"}
          </p>
          <p>
            Accès : {data.station.accessType || "inconnu"} · 24/7 : {data.station.is24_7 ? "oui" : "non"}
          </p>

          <h3>Prix</h3>
          {selectedTariffs.length === 0 && selectedEntries.length > 0 && (
            <p>Aucun tarif connu à cette station pour les réseaux sélectionnés.</p>
          )}
          {selectedTariffs.map(({ source, plan, tariff }) => (
            <div className="station-price-block" key={`${source}:${plan}`}>
              <div className="source-name">
                {formatSourceLabel(source)} · {formatPlanLabel(plan)}
              </div>
              {hasHourlyPricing(tariff) ? (
                <HourlyPriceChart tariff={tariff} priceMode={priceMode} chargeKWh={chargeKWh} />
              ) : (
                <div className="price">{formatPrice(tariff.energy_price_cents_per_kwh, priceMode, chargeKWh)}</div>
              )}
            </div>
          ))}
          {overallBest && overallBeatsSelection && (
            <div className="station-price-block best-overall">
              <div className="source-name">
                Meilleur prix toutes sources · {formatSourceLabel(overallBest.source)} ·{" "}
                {formatPlanLabel(overallBest.plan)}
              </div>
              {hasHourlyPricing(overallBest) ? (
                <HourlyPriceChart tariff={overallBest} priceMode={priceMode} chargeKWh={chargeKWh} />
              ) : (
                <div className="price">{formatPrice(overallBest.energy_price_cents_per_kwh, priceMode, chargeKWh)}</div>
              )}
            </div>
          )}
          {!overallBest && selectedEntries.length === 0 && <p>Aucun tarif connu pour cette station.</p>}

          <h3>Tous les tarifs</h3>
          {data.tariffs.length === 0 && <p>Aucun tarif connu pour cette station.</p>}
          {data.tariffs.map((t, i) => (
            <TariffRow tariff={t} priceMode={priceMode} chargeKWh={chargeKWh} key={i} />
          ))}
        </>
      )}
    </div>
  );
}
