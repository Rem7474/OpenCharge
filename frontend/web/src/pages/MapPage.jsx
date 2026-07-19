import { useEffect, useState } from "react";
import { MapContainer, TileLayer } from "react-leaflet";
import FilterBar from "../components/FilterBar.jsx";
import StationMarkers from "../components/StationMarkers.jsx";
import StationDetails from "../components/StationDetails.jsx";
import OnboardingScreen from "../components/OnboardingScreen.jsx";
import GeolocateControl from "../components/GeolocateControl.jsx";
import { PRICE_MODE_PER_KWH } from "../utils/pricing.js";
import { readStoredFilters, writeStoredFilters } from "../utils/storage.js";

const FRANCE_CENTER = [46.8, 2.5];
const DEFAULT_CHARGE_KWH = 50;
const DEFAULT_CHARGE_MINUTES = 60;

// Every piece of state here determines *which stations get fetched*
// (see StationMarkers' fetchStationsInBBox call) — grouped into one object
// with one setter instead of a separate useState per field, so each new
// station-list filter (this one added a connector-type list and a min-power
// value on top of the existing source selection) doesn't mean yet another
// pair of props threaded through FilterBar/StationMarkers. priceMode/
// chargeKWh/chargeMinutes stay separate: they're display/computation
// settings that never change which stations come back from the API.
const DEFAULT_FILTERS = {
  // { sourceId: planId }, e.g. { electra: "subscription", izivia: "standard" }.
  sources: {},
  connectorTypes: [],
  minPowerKw: null,
  // In cents/kWh, matching the API's minPriceCentsPerKwh/maxPriceCentsPerKwh
  // params and every other price value in this app (see utils/pricing.js) —
  // the filter UI itself shows/accepts euros and converts at the edge.
  minPriceCentsPerKwh: null,
  maxPriceCentsPerKwh: null,
  // Off by default (only priced stations shown): the IRVE referential has
  // many more stations than ones we've matched a price for, and showing
  // every one by default would bury the priced ones a user is here to
  // compare.
  showAllStations: false,
  // When true, subscription-plan tariffs (Electra/Fastned/eborn's
  // "subscription" plan) are excluded from the price shown on markers
  // (server-side, via the API's excludeSubscriptionPlans param) and from
  // the station detail panel (client-side — see StationDetails.jsx).
  excludeSubscriptionPlans: false,
};

export default function MapPage() {
  const [selectedStationId, setSelectedStationId] = useState(null);
  const [filters, setFilters] = useState(() => readStoredFilters() ?? DEFAULT_FILTERS);
  // Onboarding is shown automatically only the first time the app is
  // opened (nothing persisted yet) — every later visit goes straight to
  // the map, using whatever was saved. It stays reachable afterwards via
  // a button in the filter bar (see onReopenOnboarding below).
  const [showOnboarding, setShowOnboarding] = useState(() => readStoredFilters() == null);
  const [priceMode, setPriceMode] = useState(PRICE_MODE_PER_KWH);
  const [chargeKWh, setChargeKWh] = useState(DEFAULT_CHARGE_KWH);
  // How long the charging session lasts, in minutes — feeds a tariff's
  // per-minute rate and any flat session fee (see utils/pricing.js#
  // tariffCostBreakdown), alongside chargeKWh for the energy cost.
  const [chargeMinutes, setChargeMinutes] = useState(DEFAULT_CHARGE_MINUTES);

  const toggleConnectorType = (type) => {
    setFilters((prev) => ({
      ...prev,
      connectorTypes: prev.connectorTypes.includes(type)
        ? prev.connectorTypes.filter((t) => t !== type)
        : [...prev.connectorTypes, type],
    }));
  };

  const setMinPowerKw = (minPowerKw) => setFilters((prev) => ({ ...prev, minPowerKw }));
  const setMinPriceCentsPerKwh = (minPriceCentsPerKwh) => setFilters((prev) => ({ ...prev, minPriceCentsPerKwh }));
  const setMaxPriceCentsPerKwh = (maxPriceCentsPerKwh) => setFilters((prev) => ({ ...prev, maxPriceCentsPerKwh }));
  const setShowAllStations = (showAllStations) => setFilters((prev) => ({ ...prev, showAllStations }));
  const setExcludeSubscriptionPlans = (excludeSubscriptionPlans) =>
    setFilters((prev) => ({ ...prev, excludeSubscriptionPlans }));

  const toggleSource = (source, wasChecked) => {
    setFilters((prev) => {
      const sources = { ...prev.sources };
      if (wasChecked) {
        delete sources[source.id];
      } else {
        sources[source.id] = source.plans[0];
      }
      return { ...prev, sources };
    });
  };

  const selectPlan = (sourceId, planId) => {
    setFilters((prev) => ({ ...prev, sources: { ...prev.sources, [sourceId]: planId } }));
  };

  const resetFilters = () => setFilters(DEFAULT_FILTERS);

  // Mirrors every filter change to localStorage, not just the onboarding
  // step, so later adjustments made from FilterBar also survive a reload.
  useEffect(() => {
    writeStoredFilters(filters);
  }, [filters]);

  const completeOnboarding = (sources) => {
    setFilters((prev) => ({ ...prev, sources }));
    setShowOnboarding(false);
  };

  if (showOnboarding) {
    return (
      <OnboardingScreen
        initialSources={filters.sources}
        onComplete={completeOnboarding}
        onSkip={() => setShowOnboarding(false)}
      />
    );
  }

  return (
    <div className="map-page">
      <FilterBar
        selectedSources={filters.sources}
        onToggleSource={toggleSource}
        onSelectPlan={selectPlan}
        priceMode={priceMode}
        onChangePriceMode={setPriceMode}
        chargeKWh={chargeKWh}
        onChangeChargeKWh={setChargeKWh}
        chargeMinutes={chargeMinutes}
        onChangeChargeMinutes={setChargeMinutes}
        showAllStations={filters.showAllStations}
        onChangeShowAllStations={setShowAllStations}
        excludeSubscriptionPlans={filters.excludeSubscriptionPlans}
        onChangeExcludeSubscriptionPlans={setExcludeSubscriptionPlans}
        selectedConnectorTypes={filters.connectorTypes}
        onToggleConnectorType={toggleConnectorType}
        minPowerKw={filters.minPowerKw}
        onChangeMinPowerKw={setMinPowerKw}
        minPriceCentsPerKwh={filters.minPriceCentsPerKwh}
        onChangeMinPriceCentsPerKwh={setMinPriceCentsPerKwh}
        maxPriceCentsPerKwh={filters.maxPriceCentsPerKwh}
        onChangeMaxPriceCentsPerKwh={setMaxPriceCentsPerKwh}
        onReopenOnboarding={() => setShowOnboarding(true)}
        onResetFilters={resetFilters}
      />
      <div className="app-body">
        <div className="map-container">
          <MapContainer center={FRANCE_CENTER} zoom={6} minZoom={5} maxZoom={19}>
            <TileLayer
              attribution="&copy; OpenStreetMap contributors"
              url="https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png"
            />
            <StationMarkers
              onSelect={setSelectedStationId}
              selectedSources={filters.sources}
              priceMode={priceMode}
              chargeKWh={chargeKWh}
              showAllStations={filters.showAllStations}
              selectedConnectorTypes={filters.connectorTypes}
              minPowerKw={filters.minPowerKw}
              minPriceCentsPerKwh={filters.minPriceCentsPerKwh}
              maxPriceCentsPerKwh={filters.maxPriceCentsPerKwh}
              excludeSubscriptionPlans={filters.excludeSubscriptionPlans}
            />
            <GeolocateControl />
          </MapContainer>
        </div>
        {selectedStationId && (
          <StationDetails
            stationId={selectedStationId}
            onClose={() => setSelectedStationId(null)}
            selectedSources={filters.sources}
            priceMode={priceMode}
            chargeKWh={chargeKWh}
            chargeMinutes={chargeMinutes}
            excludeSubscriptionPlans={filters.excludeSubscriptionPlans}
          />
        )}
      </div>
    </div>
  );
}
