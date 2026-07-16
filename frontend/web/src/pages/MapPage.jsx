import { useState } from "react";
import { MapContainer, TileLayer } from "react-leaflet";
import FilterBar from "../components/FilterBar.jsx";
import StationMarkers from "../components/StationMarkers.jsx";
import StationDetails from "../components/StationDetails.jsx";
import { PRICE_MODE_PER_KWH } from "../utils/pricing.js";

const FRANCE_CENTER = [46.8, 2.5];
const DEFAULT_CHARGE_KWH = 50;

export default function MapPage() {
  const [selectedStationId, setSelectedStationId] = useState(null);
  const [selectedSources, setSelectedSources] = useState([]);
  const [priceMode, setPriceMode] = useState(PRICE_MODE_PER_KWH);
  const [chargeKWh, setChargeKWh] = useState(DEFAULT_CHARGE_KWH);

  const toggleSource = (sourceId) => {
    setSelectedSources((prev) =>
      prev.includes(sourceId) ? prev.filter((s) => s !== sourceId) : [...prev, sourceId]
    );
  };

  return (
    <div style={{ display: "flex", flexDirection: "column", flex: 1, minHeight: 0 }}>
      <FilterBar
        selectedSources={selectedSources}
        onToggleSource={toggleSource}
        priceMode={priceMode}
        onChangePriceMode={setPriceMode}
        chargeKWh={chargeKWh}
        onChangeChargeKWh={setChargeKWh}
      />
      <div className="app-body" style={{ flex: 1 }}>
        <div className="map-container">
          <MapContainer center={FRANCE_CENTER} zoom={6} minZoom={5} maxZoom={19}>
            <TileLayer
              attribution="&copy; OpenStreetMap contributors"
              url="https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png"
            />
            <StationMarkers
              onSelect={setSelectedStationId}
              selectedSources={selectedSources}
              priceMode={priceMode}
              chargeKWh={chargeKWh}
            />
          </MapContainer>
        </div>
        {selectedStationId && (
          <StationDetails
            stationId={selectedStationId}
            onClose={() => setSelectedStationId(null)}
            selectedSources={selectedSources}
            priceMode={priceMode}
            chargeKWh={chargeKWh}
          />
        )}
      </div>
    </div>
  );
}
