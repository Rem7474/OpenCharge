import { useEffect, useState } from "react";
import { fetchStationDetails } from "../api/stations.js";

function TariffRow({ tariff }) {
  return (
    <div className="tariff-row">
      <div className="source">
        {tariff.source} · {tariff.kind}
      </div>
      {tariff.energy_price_cents_per_kwh != null && (
        <div className="price">
          {(tariff.energy_price_cents_per_kwh / 100).toFixed(2)} € / kWh
        </div>
      )}
      {tariff.service_fee_percent != null && (
        <div>Frais de service : {tariff.service_fee_percent}%</div>
      )}
      {tariff.session_price_cents_per_min != null && (
        <div>{(tariff.session_price_cents_per_min / 100).toFixed(2)} € / min</div>
      )}
      {tariff.raw_text && <div className="raw-text">{tariff.raw_text}</div>}
    </div>
  );
}

export default function StationDetails({ stationId, onClose }) {
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
          <p>Accès : {data.station.accessType || "inconnu"} · 24/7 : {data.station.is24_7 ? "oui" : "non"}</p>

          <h3>Tarifs</h3>
          {data.tariffs.length === 0 && <p>Aucun tarif connu pour cette station.</p>}
          {data.tariffs.map((t, i) => (
            <TariffRow tariff={t} key={i} />
          ))}
        </>
      )}
    </div>
  );
}
