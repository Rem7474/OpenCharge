import { useEffect, useRef, useState } from "react";
import { Marker, Popup, useMapEvents } from "react-leaflet";
import L from "leaflet";
import { fetchStationsInBBox } from "../api/stations.js";
import {
  cheapestPriceAcrossStations,
  formatPrice,
  priceTier,
  sourcePlanPairs,
  PRICE_MODE_RECHARGE,
} from "../utils/pricing.js";
import { friendlyFetchErrorMessage } from "../utils/format.js";
import { groupStationsByLocation } from "../utils/stationGrouping.js";

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
  chargeMinutes,
  showAllStations,
  selectedConnectorTypes,
  minPowerKw,
  minPriceCentsPerKwh,
  maxPriceCentsPerKwh,
  excludeSubscriptionPlans,
}) {
  const [stations, setStations] = useState([]);
  const [loading, setLoading] = useState(false);
  // null = no error; otherwise the Error thrown by fetchStationsInBBox, so
  // the banner can distinguish a real failure (with a retry) from either a
  // load in progress or a load that succeeded with zero results.
  const [error, setError] = useState(null);
  const abortRef = useRef(null);

  const load = (map) => {
    if (map.getZoom() < MIN_ZOOM_TO_LOAD) {
      setStations([]);
      setError(null);
      return;
    }
    abortRef.current?.abort();
    const controller = new AbortController();
    abortRef.current = controller;
    setLoading(true);
    setError(null);
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
      // Only meaningful (and only sent) in "recharge" mode: that's the only
      // time the price-range filter means "total for this session" rather
      // than a plain €/kWh rate — see FilterPanel's price-range label.
      chargeKWh: priceMode === PRICE_MODE_RECHARGE ? chargeKWh : undefined,
      chargeMinutes: priceMode === PRICE_MODE_RECHARGE ? chargeMinutes : undefined,
      excludeSubscriptionPlans,
      signal: controller.signal,
    })
      .then((data) => setStations(data ?? []))
      .catch((err) => {
        if (err.name !== "AbortError") {
          console.error(err);
          setError(err);
        }
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
  }, [
    sourcesKey,
    showAllStations,
    connectorTypesKey,
    minPowerKw,
    minPriceCentsPerKwh,
    maxPriceCentsPerKwh,
    priceMode,
    chargeKWh,
    chargeMinutes,
    excludeSubscriptionPlans,
  ]);

  const belowMinZoom = map.getZoom() < MIN_ZOOM_TO_LOAD;
  const isEmpty = !loading && !error && !belowMinZoom && stations.length === 0;
  // Several connectors of the same physical site share the exact same
  // coordinates — one marker per site instead of one per connector, priced
  // at whichever connector is cheapest (see utils/pricing.js#
  // cheapestPriceAcrossStations).
  const sites = groupStationsByLocation(stations);

  return (
    <>
      <div aria-live="polite">
        {loading && <div className="status-banner">Chargement des bornes…</div>}
        {belowMinZoom && <div className="status-banner">Zoomez pour afficher les bornes</div>}
        {error && (
          <div className="status-banner status-banner--error" role="alert">
            {friendlyFetchErrorMessage(error)}{" "}
            <button type="button" className="status-banner-retry" onClick={() => load(map)}>
              Réessayer
            </button>
          </div>
        )}
        {isEmpty && <div className="status-banner">Aucune borne ne correspond à vos filtres dans cette zone.</div>}
      </div>
      {sites.map((site) => {
        const hasSelection = Object.keys(selectedSources).length > 0;
        const priceCents = cheapestPriceAcrossStations(site.stations, hasSelection);
        // With a sources selection active, a site with no tariff for any
        // selected source/plan on any of its connectors isn't relevant to
        // what the user is looking for — hide it instead of showing a dead
        // "—" marker they'd have to click through to learn nothing from.
        if (hasSelection && priceCents == null) return null;
        const label = priceCents != null ? formatPrice(priceCents, priceMode, chargeKWh) : "—";
        const tier = priceTier(priceCents);
        const first = site.stations[0];

        return (
          <Marker
            key={site.key}
            position={[site.location.lat, site.location.lng]}
            icon={priceIcon(label, priceCents != null, tier)}
            eventHandlers={{ click: () => onSelect(site) }}
          >
            <Popup>
              <strong>{first.name || "Station"}</strong>
              <br />
              {first.operator}
              <br />
              {priceCents != null ? label : "Pas de tarif pour la sélection"}
            </Popup>
          </Marker>
        );
      })}
    </>
  );
}
