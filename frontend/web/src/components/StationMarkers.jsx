import { useEffect, useRef, useState } from "react";
import { Marker, Popup, useMapEvents } from "react-leaflet";
import L from "leaflet";
import { fetchStationsInBBox } from "../api/stations.js";
import { pickPriceCentsPerKWh, formatPrice, priceTier, sourcePlanPairs } from "../utils/pricing.js";

const MIN_ZOOM_TO_LOAD = 10;

function boundsToBBox(bounds) {
  return [bounds.getWest(), bounds.getSouth(), bounds.getEast(), bounds.getNorth()];
}

function priceIcon(label, hasPrice, tier) {
  const tierClass = hasPrice && tier ? ` price-marker--${tier}` : "";
  return L.divIcon({
    className: "",
    html: `<div class="price-marker${hasPrice ? "" : " no-price"}${tierClass}">${label}</div>`,
    iconSize: null,
  });
}

export default function StationMarkers({
  onSelect,
  selectedSources,
  priceMode,
  chargeKWh,
  showAllStations,
  selectedConnectorTypes,
  minPowerKw,
  minPriceCentsPerKwh,
  maxPriceCentsPerKwh,
}) {
  const [stations, setStations] = useState([]);
  const [loading, setLoading] = useState(false);
  const abortRef = useRef(null);

  const load = (map) => {
    if (map.getZoom() < MIN_ZOOM_TO_LOAD) {
      setStations([]);
      return;
    }
    abortRef.current?.abort();
    const controller = new AbortController();
    abortRef.current = controller;
    setLoading(true);
    fetchStationsInBBox(boundsToBBox(map.getBounds()), {
      sources: sourcePlanPairs(selectedSources),
      // Only ever sends hasTariffs=true or omits it — the backend only
      // special-cases the true value (see ListStations), so leaving it
      // undefined here returns every station regardless of pricing.
      hasTariffs: showAllStations ? undefined : true,
      connectorTypes: selectedConnectorTypes,
      minPowerKw,
      minPriceCentsPerKwh,
      maxPriceCentsPerKwh,
      signal: controller.signal,
    })
      .then((data) => setStations(data ?? []))
      .catch((err) => {
        if (err.name !== "AbortError") console.error(err);
      })
      .finally(() => setLoading(false));
  };

  const map = useMapEvents({
    moveend: () => load(map),
    zoomend: () => load(map),
  });

  const sourcesKey = sourcePlanPairs(selectedSources).join(",");
  const connectorTypesKey = selectedConnectorTypes.join(",");
  useEffect(() => {
    load(map);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sourcesKey, showAllStations, connectorTypesKey, minPowerKw, minPriceCentsPerKwh, maxPriceCentsPerKwh]);

  const belowMinZoom = map.getZoom() < MIN_ZOOM_TO_LOAD;

  return (
    <>
      {loading && <div className="status-banner">Chargement des bornes…</div>}
      {belowMinZoom && <div className="status-banner">Zoomez pour afficher les bornes</div>}
      {stations.map((station) => {
        const connectorType = station.connectors?.[0]?.kind;
        const hasSelection = Object.keys(selectedSources).length > 0;
        const pricing = hasSelection ? station.selectedSourcesPricing : station.pricingSummary;
        const priceCents = pickPriceCentsPerKWh(pricing, connectorType);
        // With a sources selection active, a station with no tariff for any
        // selected source/plan isn't relevant to what the user is looking
        // for — hide it instead of showing a dead "—" marker they'd have to
        // click through to learn nothing from.
        if (hasSelection && priceCents == null) return null;
        const label = priceCents != null ? formatPrice(priceCents, priceMode, chargeKWh) : "—";
        const tier = priceTier(priceCents);

        return (
          <Marker
            key={station.id}
            position={[station.location.lat, station.location.lng]}
            icon={priceIcon(label, priceCents != null, tier)}
            eventHandlers={{ click: () => onSelect(station.id) }}
          >
            <Popup>
              <strong>{station.name || "Station"}</strong>
              <br />
              {station.operator}
              <br />
              {priceCents != null ? label : "Pas de tarif pour la sélection"}
            </Popup>
          </Marker>
        );
      })}
    </>
  );
}
