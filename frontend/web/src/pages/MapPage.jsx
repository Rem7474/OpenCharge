import { useState } from "react";
import { MapContainer, TileLayer } from "react-leaflet";
import FilterBar from "../components/FilterBar.jsx";
import StationMarkers from "../components/StationMarkers.jsx";
import StationDetails from "../components/StationDetails.jsx";
import { PRICE_MODE_PER_KWH } from "../utils/pricing.js";

const FRANCE_CENTER = [46.8, 2.5];
const DEFAULT_CHARGE_KWH = 50;
const DEFAULT_CHARGE_MINUTES = 60;

export default function MapPage() {
  const [selectedStationId, setSelectedStationId] = useState(null);
  // { sourceId: planId }, e.g. { electra: "subscription", izivia: "standard" }.
  const [selectedSources, setSelectedSources] = useState({});
  const [priceMode, setPriceMode] = useState(PRICE_MODE_PER_KWH);
  const [chargeKWh, setChargeKWh] = useState(DEFAULT_CHARGE_KWH);
  // How long the charging session lasts, in minutes — feeds a tariff's
  // per-minute rate and any flat session fee (see utils/pricing.js#
  // tariffCostBreakdown), alongside chargeKWh for the energy cost.
  const [chargeMinutes, setChargeMinutes] = useState(DEFAULT_CHARGE_MINUTES);
  // Off by default (only priced stations shown, today's behavior): the
  // IRVE referential has many more stations than ones we've matched a
  // price for, and showing every one of them by default would bury the
  // priced ones a user is actually here to compare.
  const [showAllStations, setShowAllStations] = useState(false);

  const toggleSource = (source, wasChecked) => {
    setSelectedSources((prev) => {
      const next = { ...prev };
      if (wasChecked) {
        delete next[source.id];
      } else {
        next[source.id] = source.plans[0];
      }
      return next;
    });
  };

  const selectPlan = (sourceId, planId) => {
    setSelectedSources((prev) => ({ ...prev, [sourceId]: planId }));
  };

  return (
    <div style={{ display: "flex", flexDirection: "column", flex: 1, minHeight: 0 }}>
      <FilterBar
        selectedSources={selectedSources}
        onToggleSource={toggleSource}
        onSelectPlan={selectPlan}
        priceMode={priceMode}
        onChangePriceMode={setPriceMode}
        chargeKWh={chargeKWh}
        onChangeChargeKWh={setChargeKWh}
        chargeMinutes={chargeMinutes}
        onChangeChargeMinutes={setChargeMinutes}
        showAllStations={showAllStations}
        onChangeShowAllStations={setShowAllStations}
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
              showAllStations={showAllStations}
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
            chargeMinutes={chargeMinutes}
          />
        )}
      </div>
    </div>
  );
}
