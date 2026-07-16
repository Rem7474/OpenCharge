import { useState } from "react";
import { MapContainer, TileLayer } from "react-leaflet";
import StationMarkers from "../components/StationMarkers.jsx";
import StationDetails from "../components/StationDetails.jsx";

const FRANCE_CENTER = [46.8, 2.5];

export default function MapPage() {
  const [selectedStationId, setSelectedStationId] = useState(null);
  const [filters] = useState({ hasTariffs: true });

  return (
    <div className="app-layout">
      <div className="map-container">
        <MapContainer center={FRANCE_CENTER} zoom={6} minZoom={5} maxZoom={19}>
          <TileLayer
            attribution="&copy; OpenStreetMap contributors"
            url="https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png"
          />
          <StationMarkers onSelect={setSelectedStationId} filters={filters} />
        </MapContainer>
      </div>
      {selectedStationId && (
        <StationDetails
          stationId={selectedStationId}
          onClose={() => setSelectedStationId(null)}
        />
      )}
    </div>
  );
}
