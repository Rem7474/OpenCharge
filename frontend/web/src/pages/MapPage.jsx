import { useEffect, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { MapContainer, TileLayer } from "react-leaflet";
import FilterBar from "../components/FilterBar.jsx";
import StationMarkers from "../components/StationMarkers.jsx";
import StationDetails from "../components/StationDetails.jsx";
import OnboardingScreen from "../components/OnboardingScreen.jsx";
import GeolocateControl from "../components/GeolocateControl.jsx";
import AddressSearch from "../components/AddressSearch.jsx";
import StationDeepLink from "../components/StationDeepLink.jsx";
import {
  PRICE_MODE_PER_KWH,
  DEFAULT_EV_CONSUMPTION_KWH_PER_100KM,
  DEFAULT_THERMAL_CONSUMPTION_L_PER_100KM,
} from "../utils/pricing.js";
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
  const navigate = useNavigate();
  const { id: stationIdParam } = useParams();

  // The site (group of same-location connectors) whose detail card is open
  // — see StationMarkers/StationDetails and utils/stationGrouping.js.
  const [selectedSite, setSelectedSite] = useState(null);
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
  // Assumptions for the essence/électrique comparison (see utils/pricing.js#
  // thermalEquivalentCost) — same treatment as chargeKWh/chargeMinutes:
  // editable, session-only, not persisted. This app doesn't model a real
  // vehicle profile, so these are just plausible round numbers.
  const [evConsumptionKWhPer100Km, setEvConsumptionKWhPer100Km] = useState(DEFAULT_EV_CONSUMPTION_KWH_PER_100KM);
  const [thermalConsumptionLPer100Km, setThermalConsumptionLPer100Km] = useState(DEFAULT_THERMAL_CONSUMPTION_L_PER_100KM);

  const toggleConnectorType = (type) => {
    setFilters((prev) => ({
      ...prev,
      connectorTypes: prev.connectorTypes.includes(type)
        ? prev.connectorTypes.filter((t) => t !== type)
        : [...prev.connectorTypes, type],
    }));
  };

  // Switching price mode also resets any active price-range filter: its
  // unit/meaning changes with the mode (a plain €/kWh rate vs. a total for
  // the configured session — see FilterPanel's label and the backend's
  // chargeKWh/chargeMinutes params), so a bound set in one mode is
  // meaningless carried over into the other.
  const changePriceMode = (mode) => {
    setPriceMode(mode);
    setFilters((prev) => ({ ...prev, minPriceCentsPerKwh: null, maxPriceCentsPerKwh: null }));
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

  // Selecting a marker both opens its detail card and writes a shareable
  // URL for it (see StationDeepLink, the counterpart that reads this same
  // route back into a selected site on load/back-forward). A site can group
  // several co-located connectors (see utils/stationGrouping.js) — the
  // first one is the link's canonical id, same as StationDetails already
  // treats it as "the" station for header info.
  const selectSite = (site) => {
    setSelectedSite(site);
    const firstId = site?.stations?.[0]?.id;
    if (firstId) navigate(`/station/${encodeURIComponent(firstId)}`);
  };

  const closeSite = () => {
    setSelectedSite(null);
    navigate("/", { replace: true });
  };

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
        onChangePriceMode={changePriceMode}
        chargeKWh={chargeKWh}
        onChangeChargeKWh={setChargeKWh}
        chargeMinutes={chargeMinutes}
        onChangeChargeMinutes={setChargeMinutes}
        evConsumptionKWhPer100Km={evConsumptionKWhPer100Km}
        onChangeEvConsumptionKWhPer100Km={setEvConsumptionKWhPer100Km}
        thermalConsumptionLPer100Km={thermalConsumptionLPer100Km}
        onChangeThermalConsumptionLPer100Km={setThermalConsumptionLPer100Km}
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
              onSelect={selectSite}
              selectedSources={filters.sources}
              priceMode={priceMode}
              chargeKWh={chargeKWh}
              chargeMinutes={chargeMinutes}
              showAllStations={filters.showAllStations}
              selectedConnectorTypes={filters.connectorTypes}
              minPowerKw={filters.minPowerKw}
              minPriceCentsPerKwh={filters.minPriceCentsPerKwh}
              maxPriceCentsPerKwh={filters.maxPriceCentsPerKwh}
              excludeSubscriptionPlans={filters.excludeSubscriptionPlans}
            />
            <GeolocateControl autoLocate={!stationIdParam} />
            <AddressSearch />
            <StationDeepLink selectedSite={selectedSite} onSelect={setSelectedSite} />
          </MapContainer>
        </div>
        {selectedSite && (
          <StationDetails
            site={selectedSite}
            onClose={closeSite}
            selectedSources={filters.sources}
            priceMode={priceMode}
            chargeKWh={chargeKWh}
            chargeMinutes={chargeMinutes}
            evConsumptionKWhPer100Km={evConsumptionKWhPer100Km}
            thermalConsumptionLPer100Km={thermalConsumptionLPer100Km}
            excludeSubscriptionPlans={filters.excludeSubscriptionPlans}
          />
        )}
      </div>
    </div>
  );
}
