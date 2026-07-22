import { useEffect, useMemo, useState } from "react";
import { X, MapPin, Zap, Clock, Building2, Accessibility, Cable, Tag, Star, Copy, Check, Fuel } from "lucide-react";
import { fetchStationDetails } from "../api/stations.js";
import {
  connectorPriceKind,
  currentEnergyPriceCentsPerKWh,
  formatPrice,
  hasHourlyPricing,
  tariffCostBreakdown,
  fuelPriceComparison,
  bestTariffForSource,
  cheapestTariff,
  PRICE_MODE_RECHARGE,
  SUBSCRIPTION_PLAN,
} from "../utils/pricing.js";
import { formatSourceLabel, formatPlanLabel, formatConnectorLabel, formatUpdatedAt, friendlyFetchErrorMessage } from "../utils/format.js";
import { findFreshmileSiteMeta } from "../utils/freshmile.js";
import { useFreshmileAvailability } from "../hooks/useFreshmileAvailability.js";
import { useFuelPrice } from "../hooks/useFuelPrice.js";
import HourlyPriceChart from "./HourlyPriceChart.jsx";
import FreshmileAvailability from "./FreshmileAvailability.jsx";

// Shared by TariffCost and TariffTimeAndFee so the grace-period wording
// (e.g. Izivia's "après 1h de charge") can't drift between the two places
// a tariff's time cost renders.
function timeLine(tariff, chargeMinutes, time) {
  return (
    <div>
      Temps ({chargeMinutes} min
      {tariff.session_price_grace_minutes ? `, ${tariff.session_price_grace_minutes} min offertes` : ""}) : {time.toFixed(2)} €
    </div>
  );
}

// TariffCost renders a tariff's price for the active mode: in "recharge"
// mode, a breakdown of every cost component the tariff actually carries
// (energy for chargeKWh, a per-minute rate for chargeMinutes, and any flat
// session fee) plus their total, since a session's real cost can combine
// all three (e.g. Izivia's "2,3€ la session de charge puis 0,51€/kWh").
// In "€/kWh" mode, just the headline energy rate — a blended total doesn't
// make sense as a per-unit figure.
function TariffCost({ tariff, priceMode, chargeKWh, chargeMinutes }) {
  if (priceMode !== PRICE_MODE_RECHARGE) {
    return tariff.energy_price_cents_per_kwh != null ? (
      <div className="price">{formatPrice(tariff.energy_price_cents_per_kwh, priceMode, chargeKWh)}</div>
    ) : null;
  }
  const { energy, time, fee, total } = tariffCostBreakdown(tariff, chargeKWh, chargeMinutes);
  if (total == null) return null;
  return (
    <div className="tariff-cost-breakdown">
      {energy != null && (
        <div>
          Énergie ({chargeKWh} kWh) : {energy.toFixed(2)} €
        </div>
      )}
      {time != null && timeLine(tariff, chargeMinutes, time)}
      {fee != null && <div>Frais de session : {fee.toFixed(2)} €</div>}
      <div className="price">Total estimé : {total.toFixed(2)} €</div>
    </div>
  );
}

// HourlyPriceChart only ever shows a windowed tariff's energy/kWh side —
// but the same tariff can independently carry a per-minute rate and/or a
// flat session fee (e.g. Izivia's day/night pricing plus its "surcoût
// après 1h de charge"), which would otherwise never be shown anywhere:
// TariffCost, the only other place these render, is entirely skipped for
// a windowed tariff (see TariffDisplay). Passing chargeKWh=0 to
// tariffCostBreakdown here is deliberate — this only ever reads its
// time/fee fields, never energy (already covered by the chart) or total
// (a single blended figure doesn't make sense when energy varies by hour).
function TariffTimeAndFee({ tariff, chargeMinutes }) {
  const { time, fee } = tariffCostBreakdown(tariff, 0, chargeMinutes);
  if (time == null && fee == null) return null;
  return (
    <div className="tariff-cost-breakdown">
      {time != null && timeLine(tariff, chargeMinutes, time)}
      {fee != null && <div>Frais de session : {fee.toFixed(2)} €</div>}
    </div>
  );
}

// Single place that decides how to render a tariff's price: an hourly chart
// for tariffs whose price varies across the day, a cost breakdown/headline
// rate otherwise. Used everywhere a tariff's price is shown (the per-source
// blocks, the "best overall" block, and each row of "Tous les tarifs")
// instead of repeating the same hasHourlyPricing(...) ? <A/> : <B/> check
// three times.
function TariffDisplay({ tariff, priceMode, chargeKWh, chargeMinutes }) {
  if (hasHourlyPricing(tariff)) {
    return (
      <>
        <HourlyPriceChart tariff={tariff} priceMode={priceMode} chargeKWh={chargeKWh} />
        {priceMode === PRICE_MODE_RECHARGE && <TariffTimeAndFee tariff={tariff} chargeMinutes={chargeMinutes} />}
      </>
    );
  }
  return <TariffCost tariff={tariff} priceMode={priceMode} chargeKWh={chargeKWh} chargeMinutes={chargeMinutes} />;
}

function TariffRow({ tariff, priceMode, chargeKWh, chargeMinutes }) {
  const updatedAt = formatUpdatedAt(tariff.updated_at);
  return (
    <div className="tariff-row">
      <div className="source">
        {tariff.source} · {formatPlanLabel(tariff.plan)} · {tariff.kind}
      </div>
      <TariffDisplay tariff={tariff} priceMode={priceMode} chargeKWh={chargeKWh} chargeMinutes={chargeMinutes} />
      {tariff.service_fee_percent != null && <div>Frais de service : {tariff.service_fee_percent}%</div>}
      {priceMode !== PRICE_MODE_RECHARGE && tariff.session_price_cents_per_min != null && (
        <div>
          {(tariff.session_price_cents_per_min / 100).toFixed(2)} € / min
          {tariff.session_price_grace_minutes ? ` après ${tariff.session_price_grace_minutes} min de charge` : ""}
        </div>
      )}
      {priceMode !== PRICE_MODE_RECHARGE && tariff.session_fee_cents != null && (
        <div>{(tariff.session_fee_cents / 100).toFixed(2)} € / session</div>
      )}
      {tariff.raw_text && <div className="raw-text">{tariff.raw_text}</div>}
      {updatedAt && <div className="updated-at">Mis à jour le {updatedAt}</div>}
    </div>
  );
}

// Shown once per connector, alongside whichever price is already
// highlighted there — a €/kWh-vs-€/L rate comparison, meaningful
// regardless of priceMode (no chargeKWh/session size involved) or of which
// tariff happens to be cheapest, so this doesn't repeat per tariff row the
// way TariffCost does. tariff is whichever one ConnectorPriceSection is
// already treating as "the" price for this connector (its best
// selected-source tariff, or the overall best); fuelPrice is the
// nationwide-average SP95-E10 price from hooks/useFuelPrice.js (null while
// loading, in which case this renders nothing rather than guessing).
//
// evConsumptionMin/MaxKWhPer100Km is a range, not one number: real EV
// consumption swings a lot with conditions (highway vs. city, cold
// weather, driving style), so a single assumed figure would claim a
// precision this app has no way to back up — this computes the comparison
// at both ends and shows the resulting range instead.
function FuelComparison({ tariff, evConsumptionMinKWhPer100Km, evConsumptionMaxKWhPer100Km, thermalConsumptionLPer100Km, fuelPrice }) {
  if (!tariff || !fuelPrice) return null;
  const evPriceCentsPerKWh = currentEnergyPriceCentsPerKWh(tariff);
  if (evPriceCentsPerKWh == null) return null;

  const shared = { evPriceCentsPerKWh, thermalConsumptionLPer100Km, fuelPriceCentsPerLiter: fuelPrice.pricePerLiterCents };
  const atMin = fuelPriceComparison({ ...shared, evConsumptionKWhPer100Km: evConsumptionMinKWhPer100Km });
  const atMax = fuelPriceComparison({ ...shared, evConsumptionKWhPer100Km: evConsumptionMaxKWhPer100Km });
  if (!atMin || !atMax) return null;

  // Lower EV consumption always means a cheaper equivalent and bigger
  // savings, but this sorts by value rather than assuming that — so the
  // range still reads low-to-high even if the user enters the two fields
  // the "wrong" way round.
  const equivLow = Math.min(atMin.equivalentFuelPriceCentsPerLiter, atMax.equivalentFuelPriceCentsPerLiter) / 100;
  const equivHigh = Math.max(atMin.equivalentFuelPriceCentsPerLiter, atMax.equivalentFuelPriceCentsPerLiter) / 100;
  const savingsLow = Math.min(atMin.savingsPercent, atMax.savingsPercent);
  const savingsHigh = Math.max(atMin.savingsPercent, atMax.savingsPercent);
  const bothCheaper = atMin.savingsCentsPer100Km > 0 && atMax.savingsCentsPer100Km > 0;
  const bothPricier = atMin.savingsCentsPer100Km <= 0 && atMax.savingsCentsPer100Km <= 0;

  return (
    <p className="fuel-comparison">
      <Fuel size={13} strokeWidth={2.2} />
      Équivaut à {equivLow.toFixed(2)}–{equivHigh.toFixed(2)} €/L d&rsquo;essence selon la conso
      {bothCheaper && (
        <>
          {" "}
          — {Math.round(savingsLow)} à {Math.round(savingsHigh)} % moins cher
        </>
      )}
      {bothPricier && (
        <>
          {" "}
          — {Math.round(Math.abs(savingsHigh))} à {Math.round(Math.abs(savingsLow))} % plus cher que l&rsquo;essence
        </>
      )}
      {!bothCheaper && !bothPricier && <> — moins cher ou plus cher selon la conso réelle</>}
      {!fuelPrice.live && " (prix carburant estimé)"}
    </p>
  );
}

// One physical site can expose several connectors (points of charge) —
// each with its own independent set of tariffs, since a source can price
// e.g. a CCS plug differently from a T2 plug at the same location (see
// backend station_repo.go's connector-kind bucketing). This renders one
// connector's full price section — everything StationDetails used to show
// for "the" station, now scoped to a single connector — so the parent can
// stack one per connector on the same card (see StationDetails' render).
function ConnectorPriceSection({
  connectorSummary,
  detail,
  connectorAvailability,
  selectedSources,
  priceMode,
  chargeKWh,
  chargeMinutes,
  evConsumptionMinKWhPer100Km,
  evConsumptionMaxKWhPer100Km,
  thermalConsumptionLPer100Km,
  fuelPrice,
  excludeSubscriptionPlans,
}) {
  // GET /stations/{id} always returns every known tariff, including
  // subscription-plan ones — the "Exclure les tarifs abonnés" filter is
  // applied client-side here so the detail panel matches what the map
  // marker already shows (the marker's price comes from the API's own
  // excludeSubscriptionPlans param — see api/stations.js).
  const tariffs = useMemo(() => {
    const all = detail?.tariffs ?? [];
    return excludeSubscriptionPlans ? all.filter((t) => t.plan !== SUBSCRIPTION_PLAN) : all;
  }, [detail, excludeSubscriptionPlans]);

  const stationConnectorType = detail ? detail.station.connectors?.[0]?.kind : null;
  const connectorKind = connectorPriceKind(stationConnectorType);
  const selectedEntries = Object.entries(selectedSources);
  const selectedTariffs = useMemo(
    () =>
      selectedEntries
        .map(([source, plan]) => ({
          source,
          plan,
          tariff: bestTariffForSource(tariffs, source, plan, connectorKind, stationConnectorType),
        }))
        .filter((entry) => entry.tariff != null),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [tariffs, connectorKind, stationConnectorType, selectedSources]
  );
  const cheapestSelected = useMemo(
    () =>
      selectedTariffs.length > 0
        ? selectedTariffs.reduce((min, e) =>
            currentEnergyPriceCentsPerKWh(e.tariff) < currentEnergyPriceCentsPerKWh(min.tariff) ? e : min
          )
        : null,
    [selectedTariffs]
  );
  const overallBest = useMemo(
    () => (detail ? cheapestTariff(tariffs, connectorKind, stationConnectorType) : null),
    [detail, tariffs, connectorKind, stationConnectorType]
  );
  const overallBeatsSelection =
    overallBest &&
    (!cheapestSelected ||
      currentEnergyPriceCentsPerKWh(overallBest) < currentEnergyPriceCentsPerKWh(cheapestSelected.tariff));

  return (
    <div className="connector-price-section">
      <h4 className="connector-price-section-heading">
        <Zap size={13} strokeWidth={2.2} />
        {formatConnectorLabel(connectorSummary.connectors?.[0]?.kind) || "Connecteur"}
        {connectorSummary.connectors?.[0]?.maxPowerKw ? ` · ${connectorSummary.connectors[0].maxPowerKw}kW` : ""}
        {connectorAvailability && (
          <span className={`connector-availability${connectorAvailability.available === 0 ? " connector-availability--none" : ""}`}>
            {connectorAvailability.available}/{connectorAvailability.total} disponible
            {connectorAvailability.available > 1 ? "s" : ""}
          </span>
        )}
      </h4>

      {selectedTariffs.length === 0 && selectedEntries.length > 0 && (
        <p>Aucun tarif connu pour ce connecteur pour les réseaux sélectionnés.</p>
      )}
      {selectedTariffs.map(({ source, plan, tariff }) => (
        <div className="station-price-block" key={`${source}:${plan}`}>
          <div className="source-name">
            {formatSourceLabel(source)} · {formatPlanLabel(plan)}
          </div>
          <TariffDisplay tariff={tariff} priceMode={priceMode} chargeKWh={chargeKWh} chargeMinutes={chargeMinutes} />
        </div>
      ))}
      {overallBest && overallBeatsSelection && (
        <div className="station-price-block best-overall">
          <div className="source-name">
            <Star size={12} strokeWidth={2.4} /> Meilleur prix toutes sources · {formatSourceLabel(overallBest.source)}{" "}
            · {formatPlanLabel(overallBest.plan)}
          </div>
          <TariffDisplay tariff={overallBest} priceMode={priceMode} chargeKWh={chargeKWh} chargeMinutes={chargeMinutes} />
        </div>
      )}
      {!overallBest && selectedEntries.length === 0 && <p>Aucun tarif connu pour ce connecteur.</p>}

      <FuelComparison
        tariff={overallBeatsSelection ? overallBest : (cheapestSelected?.tariff ?? overallBest)}
        evConsumptionMinKWhPer100Km={evConsumptionMinKWhPer100Km}
        evConsumptionMaxKWhPer100Km={evConsumptionMaxKWhPer100Km}
        thermalConsumptionLPer100Km={thermalConsumptionLPer100Km}
        fuelPrice={fuelPrice}
      />

      {tariffs.length > 0 && (
        <details className="connector-all-tariffs">
          <summary>Tous les tarifs ({tariffs.length})</summary>
          {tariffs.map((t, i) => (
            <TariffRow tariff={t} priceMode={priceMode} chargeKWh={chargeKWh} chargeMinutes={chargeMinutes} key={i} />
          ))}
        </details>
      )}
    </div>
  );
}

export default function StationDetails({
  site,
  onClose,
  selectedSources,
  priceMode,
  chargeKWh,
  chargeMinutes,
  evConsumptionMinKWhPer100Km,
  evConsumptionMaxKWhPer100Km,
  thermalConsumptionLPer100Km,
  excludeSubscriptionPlans,
}) {
  const [details, setDetails] = useState(null);
  const [error, setError] = useState(null);
  const [linkCopied, setLinkCopied] = useState(false);
  const fuelPrice = useFuelPrice();

  useEffect(() => {
    if (!site) return undefined;
    const controller = new AbortController();
    setDetails(null);
    setError(null);
    Promise.all(site.stations.map((s) => fetchStationDetails(s.id, { signal: controller.signal })))
      .then(setDetails)
      .catch((err) => {
        if (err.name !== "AbortError") setError(err);
      });
    return () => controller.abort();
    // Re-fetches only when the site itself changes (site.key, its stable
    // location-based identity — see utils/stationGrouping.js), not on every
    // re-render that hands down a structurally-equal but new `site` object
    // reference (StationMarkers recomputes the grouping on every station
    // list refresh).
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [site?.key]);

  // findFreshmileSiteMeta tolerates details being null (still loading) or
  // site being null (nothing selected) — computed unconditionally, ahead of
  // the early return below, since useFreshmileAvailability is a hook and
  // must run on every render regardless of whether site is null.
  const { imgPreviewUrl, locationId } = findFreshmileSiteMeta(details);
  const freshmileAvailability = useFreshmileAvailability(locationId);

  // Shares the current URL as-is rather than rebuilding it from site.key:
  // MapPage's selectSite already wrote /station/<id> here on selection (or
  // StationDeepLink resolved it from one on load), so location.href is
  // already the right link to hand out.
  const copyLink = () => {
    navigator.clipboard.writeText(window.location.href).then(() => {
      setLinkCopied(true);
      setTimeout(() => setLinkCopied(false), 1500);
    });
  };

  if (!site) return null;

  // Name/address/operator are identical for every connector of a site (same
  // physical location) — the list response already has them, so the header
  // renders immediately without waiting on the per-connector detail fetch.
  const first = site.stations[0];
  // Access type/24-7/PMR/etc. are metadata-only fields the list endpoint
  // doesn't carry (see api/dto.go's stationDetailDTO vs stationListItemDTO);
  // take them from whichever connector's detail loaded first — in practice
  // identical across every connector of the same site.
  const firstDetail = details?.[0]?.station;
  const connectors = site.stations.map((s) => s.connectors?.[0]).filter(Boolean);

  return (
    <div className="sidebar">
      <div aria-live="polite">
        {error && <p role="alert">{friendlyFetchErrorMessage(error)}</p>}
        {!details && !error && <p>Chargement…</p>}
      </div>
      <div className="station-header">
        <div className="station-header-text">
          <h2>{first.name || "Station sans nom"}</h2>
          <div className="station-header-sub">
            {first.operator || "Opérateur inconnu"}
            {first.enseigne ? ` · ${first.enseigne}` : ""}
          </div>
        </div>
        <div className="station-header-actions">
          <button
            type="button"
            className="copy-link-btn"
            onClick={copyLink}
            aria-label="Copier le lien de cette borne"
            title="Copier le lien"
          >
            {linkCopied ? <Check size={14} strokeWidth={2.4} /> : <Copy size={14} strokeWidth={2.2} />}
          </button>
          <button className="close-btn" onClick={onClose} aria-label="Fermer">
            <X size={15} strokeWidth={2.2} />
          </button>
        </div>
      </div>

      {imgPreviewUrl ? (
        <div className="station-preview">
          <img src={imgPreviewUrl} alt="" className="station-preview-image" loading="lazy" />
          <FreshmileAvailability availability={freshmileAvailability} />
        </div>
      ) : (
        freshmileAvailability && (
          <div className="station-preview station-preview--no-image">
            <FreshmileAvailability availability={freshmileAvailability} />
          </div>
        )
      )}

      <div className="station-meta-card">
        <div className="station-meta-row">
          <MapPin size={15} strokeWidth={2} className="station-meta-icon" />
          <span>
            {first.address.street}
            <br />
            {first.address.postalCode} {first.address.city}
          </span>
        </div>

        {connectors.length > 0 && (
          <div className="connector-badges">
            {connectors.map((c, i) => (
              <span className="connector-badge" key={i}>
                <Zap size={13} strokeWidth={2.2} />
                {formatConnectorLabel(c.kind)}
                {c.maxPowerKw ? ` · ${c.maxPowerKw}kW` : ""}
              </span>
            ))}
          </div>
        )}

        {firstDetail && (
          <>
            <div className="station-meta-row">
              <Clock size={15} strokeWidth={2} className="station-meta-icon" />
              <span>
                {firstDetail.accessType || "Accès inconnu"} · {firstDetail.is24_7 ? "24/7" : "horaires limités"}
                {firstDetail.openingHours && firstDetail.openingHours !== "24/7" && ` (${firstDetail.openingHours})`}
              </span>
            </div>

            {firstDetail.pdcCount != null && (
              <div className="station-meta-row">
                <Building2 size={15} strokeWidth={2} className="station-meta-icon" />
                <span>{firstDetail.pdcCount} point(s) de charge sur site</span>
              </div>
            )}
            {firstDetail.accessibilityPmr && (
              <div className="station-meta-row">
                <Accessibility size={15} strokeWidth={2} className="station-meta-icon" />
                <span>{firstDetail.accessibilityPmr}</span>
              </div>
            )}
            {firstDetail.cableT2Attached != null && (
              <div className="station-meta-row">
                <Cable size={15} strokeWidth={2} className="station-meta-icon" />
                <span>Câble T2 {firstDetail.cableT2Attached ? "attaché" : "non attaché"}</span>
              </div>
            )}
          </>
        )}
      </div>

      {details && (
        <>
          <h3 className="section-heading">
            <Tag size={15} strokeWidth={2.2} /> Prix par connecteur
          </h3>
          {site.stations.map((connectorSummary, i) => (
            <ConnectorPriceSection
              key={connectorSummary.id}
              connectorSummary={connectorSummary}
              detail={details[i]}
              connectorAvailability={freshmileAvailability?.connectorAvailability?.[connectorSummary.connectors?.[0]?.kind]}
              selectedSources={selectedSources}
              priceMode={priceMode}
              chargeKWh={chargeKWh}
              chargeMinutes={chargeMinutes}
              evConsumptionMinKWhPer100Km={evConsumptionMinKWhPer100Km}
              evConsumptionMaxKWhPer100Km={evConsumptionMaxKWhPer100Km}
              thermalConsumptionLPer100Km={thermalConsumptionLPer100Km}
              fuelPrice={fuelPrice}
              excludeSubscriptionPlans={excludeSubscriptionPlans}
            />
          ))}
        </>
      )}
    </div>
  );
}
