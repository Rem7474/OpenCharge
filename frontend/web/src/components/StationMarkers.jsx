import { useEffect, useRef, useState } from "react";
import { CircleMarker, Popup, useMapEvents } from "react-leaflet";
import { fetchStationsInBBox } from "../api/stations.js";

function boundsToBBox(bounds) {
  return [
    bounds.getWest(),
    bounds.getSouth(),
    bounds.getEast(),
    bounds.getNorth(),
  ];
}

function markerColor(station) {
  if (!station.hasTariffs) return "#999";
  return "#1a7f37";
}

export default function StationMarkers({ onSelect, filters }) {
  const [stations, setStations] = useState([]);
  const [loading, setLoading] = useState(false);
  const abortRef = useRef(null);

  const load = (map) => {
    if (map.getZoom() < 10) {
      // Below this zoom the viewport covers too much of France: ask the
      // user to zoom in instead of loading thousands of markers at once.
      setStations([]);
      return;
    }
    abortRef.current?.abort();
    const controller = new AbortController();
    abortRef.current = controller;
    setLoading(true);
    fetchStationsInBBox(boundsToBBox(map.getBounds()), {
      ...filters,
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

  useEffect(() => {
    load(map);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [filters.operator, filters.hasTariffs, filters.source]);

  return (
    <>
      {loading && <div className="status-banner">Chargement des bornes…</div>}
      {map.getZoom() < 10 && (
        <div className="status-banner">Zoomez pour afficher les bornes</div>
      )}
      {stations.map((station) => (
        <CircleMarker
          key={station.id}
          center={[station.location.lat, station.location.lng]}
          radius={6}
          pathOptions={{ color: markerColor(station), fillOpacity: 0.8 }}
          eventHandlers={{ click: () => onSelect(station.id) }}
        >
          <Popup>
            <strong>{station.name || "Station"}</strong>
            <br />
            {station.operator}
            <br />
            {station.hasTariffs ? "Tarifs disponibles" : "Pas de tarif connu"}
          </Popup>
        </CircleMarker>
      ))}
    </>
  );
}
