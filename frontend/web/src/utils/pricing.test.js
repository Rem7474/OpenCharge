import { describe, it, expect, vi, afterEach } from "vitest";
import {
  connectorPriceKind,
  tariffAppliesToBucket,
  preferConnectorMatch,
  bestTariffForSource,
  cheapestTariff,
  pickPriceCentsPerKWh,
  cheapestPriceAcrossStations,
  priceTier,
  offPeakRecommendation,
  fuelPriceComparison,
  tariffCostBreakdown,
} from "./pricing.js";
import { groupStationsByLocation } from "./stationGrouping.js";

function station(overrides) {
  return {
    id: "irve:test",
    name: "Test",
    location: { lat: 45.9, lng: 6.1 },
    connectors: [{ kind: "T2" }],
    pricingSummary: {},
    ...overrides,
  };
}

function tariff(overrides) {
  return {
    source: "test",
    plan: "standard",
    kind: "dc",
    energy_price_cents_per_kwh: 40,
    ...overrides,
  };
}

describe("connectorPriceKind", () => {
  it("maps DC connector types to 'dc'", () => {
    expect(connectorPriceKind("CCS")).toBe("dc");
    expect(connectorPriceKind("CHAdeMO")).toBe("dc");
  });
  it("maps AC connector types to 'ac'", () => {
    expect(connectorPriceKind("T2")).toBe("ac");
    expect(connectorPriceKind("EF")).toBe("ac");
  });
  it("returns null for an unknown connector type", () => {
    expect(connectorPriceKind("other")).toBeNull();
    expect(connectorPriceKind(undefined)).toBeNull();
  });
});

describe("tariffAppliesToBucket", () => {
  it("matches a tariff whose own kind equals the bucket", () => {
    expect(tariffAppliesToBucket(tariff({ kind: "dc" }), "dc")).toBe(true);
    expect(tariffAppliesToBucket(tariff({ kind: "ac" }), "dc")).toBe(false);
  });
  it("treats 'mixed' as applying to every bucket", () => {
    expect(tariffAppliesToBucket(tariff({ kind: "mixed" }), "ac")).toBe(true);
    expect(tariffAppliesToBucket(tariff({ kind: "mixed" }), "dc")).toBe(true);
  });
});

describe("preferConnectorMatch", () => {
  it("keeps only the rows matching the station's own connector type", () => {
    const generic = tariff({ connector_type: undefined });
    const specific = tariff({ connector_type: "CCS" });
    expect(preferConnectorMatch([generic, specific], "CCS")).toEqual([specific]);
  });
  it("falls back to every row when none match", () => {
    const rows = [tariff({ connector_type: "T2" })];
    expect(preferConnectorMatch(rows, "CCS")).toEqual(rows);
  });
  it("falls back to every row when the station's connector type is unknown", () => {
    const rows = [tariff({ connector_type: "T2" }), tariff({ connector_type: "CCS" })];
    expect(preferConnectorMatch(rows, null)).toEqual(rows);
  });
});

describe("bestTariffForSource", () => {
  it("picks the cheaper of two same-source/plan/kind tariffs when neither is connector-specific", () => {
    const cheap = tariff({ energy_price_cents_per_kwh: 10 });
    const pricier = tariff({ energy_price_cents_per_kwh: 40 });
    expect(bestTariffForSource([cheap, pricier], "test", "standard", "dc", "CCS")).toBe(cheap);
  });

  it("prefers a connector-specific tariff from the same source over a cheaper generic one", () => {
    // Reproduces the reported bug's same-source scenario: Freshmile's own
    // CCS-specific tariff must win over its own cheaper generic tariff.
    const genericCheap = tariff({ source: "freshmile", energy_price_cents_per_kwh: 10, connector_type: undefined });
    const specificPricier = tariff({ source: "freshmile", energy_price_cents_per_kwh: 40, connector_type: "CCS" });
    const got = bestTariffForSource([genericCheap, specificPricier], "freshmile", "standard", "dc", "CCS");
    expect(got).toBe(specificPricier);
  });

  it("only considers tariffs from the requested source and plan", () => {
    const wrongSource = tariff({ source: "izivia", energy_price_cents_per_kwh: 5 });
    const rightSource = tariff({ source: "freshmile", energy_price_cents_per_kwh: 40 });
    const got = bestTariffForSource([wrongSource, rightSource], "freshmile", "standard", "dc", null);
    expect(got).toBe(rightSource);
  });

  it("includes mixed-kind tariffs when filtering by bucket", () => {
    const mixed = tariff({ kind: "mixed", energy_price_cents_per_kwh: 29 });
    expect(bestTariffForSource([mixed], "test", "standard", "dc", null)).toBe(mixed);
  });

  it("returns null when no tariff matches the source/plan", () => {
    expect(bestTariffForSource([tariff({ source: "other" })], "test", "standard", "dc", null)).toBeNull();
  });
});

describe("cheapestTariff", () => {
  it("is the regression test for the reported bug: a connector-specific tariff from one source must never suppress a cheaper tariff from a different, unrelated source", () => {
    const freshmileSpecific = tariff({ source: "freshmile", plan: "standard", kind: "dc", energy_price_cents_per_kwh: 51, connector_type: "CCS" });
    const freshmileGeneric = tariff({ source: "freshmile", plan: "standard", kind: "dc", energy_price_cents_per_kwh: 32, connector_type: undefined });
    const izivia = tariff({ source: "izivia", plan: "standard", kind: "dc", energy_price_cents_per_kwh: 45 });
    const lidl = tariff({ source: "lidl", plan: "standard", kind: "mixed", energy_price_cents_per_kwh: 29 });

    const got = cheapestTariff([freshmileSpecific, freshmileGeneric, izivia, lidl], "dc", "CCS");
    expect(got).toBe(lidl);
    expect(got.energy_price_cents_per_kwh).toBe(29);
  });

  it("still prefers the connector-specific tariff within a single source", () => {
    const specific = tariff({ source: "freshmile", energy_price_cents_per_kwh: 40, connector_type: "CCS" });
    const genericCheaper = tariff({ source: "freshmile", energy_price_cents_per_kwh: 10, connector_type: undefined });
    const got = cheapestTariff([specific, genericCheaper], "dc", "CCS");
    expect(got).toBe(specific);
  });

  it("keeps subscription and standard plans from the same source in separate dedup groups", () => {
    const subscriptionSpecific = tariff({ source: "freshmile", plan: "subscription", energy_price_cents_per_kwh: 15, connector_type: "CCS" });
    const standardGeneric = tariff({ source: "freshmile", plan: "standard", energy_price_cents_per_kwh: 40, connector_type: undefined });
    // Simulates the frontend having already filtered out subscription-plan
    // tariffs (see StationDetails.jsx's excludeSubscriptionPlans handling):
    // only the standard tariff should remain in the candidate list.
    const got = cheapestTariff([standardGeneric], "dc", "CCS");
    expect(got).toBe(standardGeneric);
    expect([subscriptionSpecific]).not.toContain(got);
  });

  it("returns null when no tariff has a known price", () => {
    expect(cheapestTariff([tariff({ energy_price_cents_per_kwh: null })], "dc", null)).toBeNull();
  });
});

describe("pickPriceCentsPerKWh", () => {
  it("prefers the price matching the station's own connector kind", () => {
    const pricing = { ac_min_cents_per_kwh: 20, dc_min_cents_per_kwh: 40 };
    expect(pickPriceCentsPerKWh(pricing, "CCS")).toBe(40);
    expect(pickPriceCentsPerKWh(pricing, "T2")).toBe(20);
  });
  it("falls back to whichever price is available for an unknown connector", () => {
    expect(pickPriceCentsPerKWh({ ac_min_cents_per_kwh: 20, dc_min_cents_per_kwh: null }, "other")).toBe(20);
  });
  it("returns null when there's no pricing summary at all", () => {
    expect(pickPriceCentsPerKWh(null, "CCS")).toBeNull();
  });
});

describe("priceTier", () => {
  it("buckets prices into the four map-marker tiers", () => {
    expect(priceTier(20)).toBe("low");
    expect(priceTier(30)).toBe("mid");
    expect(priceTier(45)).toBe("high");
    expect(priceTier(60)).toBe("extreme");
  });
  it("returns null for an unknown price", () => {
    expect(priceTier(null)).toBeNull();
  });
});

describe("cheapestPriceAcrossStations", () => {
  it("picks the cheapest connector's price, not the first one", () => {
    const stations = [
      station({ connectors: [{ kind: "T2" }], pricingSummary: { ac_min_cents_per_kwh: 40 } }),
      station({ connectors: [{ kind: "CCS" }], pricingSummary: { dc_min_cents_per_kwh: 25 } }),
    ];
    expect(cheapestPriceAcrossStations(stations, false)).toBe(25);
  });
  it("reads selectedSourcesPricing instead when hasSelection is true", () => {
    const stations = [
      station({
        connectors: [{ kind: "T2" }],
        pricingSummary: { ac_min_cents_per_kwh: 10 },
        selectedSourcesPricing: { ac_min_cents_per_kwh: 50 },
      }),
    ];
    expect(cheapestPriceAcrossStations(stations, true)).toBe(50);
  });
  it("ignores connectors with no known price and returns null if none have one", () => {
    const stations = [station({ pricingSummary: {} }), station({ pricingSummary: {} })];
    expect(cheapestPriceAcrossStations(stations, false)).toBeNull();
  });
});

describe("offPeakRecommendation", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  function windowedTariff(windows) {
    return tariff({ energy_price_cents_per_kwh: null, extra: { windows } });
  }

  it("recommends the cheapest window when it meaningfully beats the current price", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date(2026, 0, 1, 10, 0)); // 10:00, inside the peak window
    const t = windowedTariff([
      { startTime: "07:00", endTime: "22:00", energyPriceCentsPerKwh: 45 },
      { startTime: "22:00", endTime: "07:00", energyPriceCentsPerKwh: 20 },
    ]);
    const rec = offPeakRecommendation(t);
    expect(rec).not.toBeNull();
    expect(rec.startTime).toBe("22:00");
    expect(rec.endTime).toBe("07:00");
    expect(rec.priceCentsPerKWh).toBe(20);
    expect(rec.currentPriceCentsPerKWh).toBe(45);
    expect(Math.round(rec.savingsPercent)).toBe(56);
  });

  it("returns null when the current window is already the cheapest one", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date(2026, 0, 1, 23, 0)); // 23:00, inside the cheap window
    const t = windowedTariff([
      { startTime: "07:00", endTime: "22:00", energyPriceCentsPerKwh: 45 },
      { startTime: "22:00", endTime: "07:00", energyPriceCentsPerKwh: 20 },
    ]);
    expect(offPeakRecommendation(t)).toBeNull();
  });

  it("returns null when the gap is too small to be worth recommending", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date(2026, 0, 1, 10, 0));
    const t = windowedTariff([
      { startTime: "07:00", endTime: "22:00", energyPriceCentsPerKwh: 30 },
      { startTime: "22:00", endTime: "07:00", energyPriceCentsPerKwh: 29.5 },
    ]);
    expect(offPeakRecommendation(t)).toBeNull();
  });

  it("returns null for a tariff with a single (non-varying) window", () => {
    expect(offPeakRecommendation(tariff({ extra: { windows: [{ startTime: "00:00", endTime: "24:00", energyPriceCentsPerKwh: 30 }] } }))).toBeNull();
  });

  it("returns null for a flat tariff with no windows at all", () => {
    expect(offPeakRecommendation(tariff({ energy_price_cents_per_kwh: 30 }))).toBeNull();
  });
});

describe("fuelPriceComparison", () => {
  // Regression numbers straight from a user-provided spreadsheet: 0,35
  // €/kWh, 7 L/100km thermal, 1,90 €/L, at 15 kWh/100km (min) and 22
  // kWh/100km (max) EV consumption.
  it("matches the reference spreadsheet at the low end of EV consumption", () => {
    const got = fuelPriceComparison({
      evPriceCentsPerKWh: 35,
      evConsumptionKWhPer100Km: 15,
      thermalConsumptionLPer100Km: 7,
      fuelPriceCentsPerLiter: 190,
    });
    expect(got.evCostCentsPer100Km).toBeCloseTo(525); // 5,25 €/100km
    expect(got.thermalCostCentsPer100Km).toBeCloseTo(1330); // 13,3 €/100km
    expect(got.savingsCentsPer100Km).toBeCloseTo(805); // 8,05 €/100km
    expect(got.savingsPercent).toBeCloseTo(60.5, 0);
  });

  it("matches the reference spreadsheet at the high end of EV consumption", () => {
    const got = fuelPriceComparison({
      evPriceCentsPerKWh: 35,
      evConsumptionKWhPer100Km: 22,
      thermalConsumptionLPer100Km: 7,
      fuelPriceCentsPerLiter: 190,
    });
    expect(got.evCostCentsPer100Km).toBeCloseTo(770); // 7,7 €/100km
    expect(got.savingsCentsPer100Km).toBeCloseTo(560); // 5,6 €/100km
    expect(got.savingsPercent).toBeCloseTo(42.1, 1);
  });

  it("derives the fuel price this electricity rate is equivalent to", () => {
    const got = fuelPriceComparison({
      evPriceCentsPerKWh: 35,
      evConsumptionKWhPer100Km: 15,
      thermalConsumptionLPer100Km: 7,
      fuelPriceCentsPerLiter: 190,
    });
    // 5,25 €/100km / 7 L/100km = 0,75 €/L — independent of the real fuel price.
    expect(got.equivalentFuelPriceCentsPerLiter).toBeCloseTo(75);
  });

  it("can report a negative saving for an unusually expensive tariff, rather than pretending it's a win", () => {
    const got = fuelPriceComparison({
      evPriceCentsPerKWh: 90,
      evConsumptionKWhPer100Km: 20,
      thermalConsumptionLPer100Km: 6,
      fuelPriceCentsPerLiter: 180,
    });
    expect(got.savingsCentsPer100Km).toBeLessThan(0);
  });

  it("returns null when the fuel price hasn't loaded yet", () => {
    expect(
      fuelPriceComparison({
        evPriceCentsPerKWh: 35,
        evConsumptionKWhPer100Km: 20,
        thermalConsumptionLPer100Km: 6,
        fuelPriceCentsPerLiter: null,
      })
    ).toBeNull();
  });

  it("returns null for a non-positive price or consumption", () => {
    expect(
      fuelPriceComparison({ evPriceCentsPerKWh: 0, evConsumptionKWhPer100Km: 20, thermalConsumptionLPer100Km: 6, fuelPriceCentsPerLiter: 180 })
    ).toBeNull();
    expect(
      fuelPriceComparison({ evPriceCentsPerKWh: 35, evConsumptionKWhPer100Km: 0, thermalConsumptionLPer100Km: 6, fuelPriceCentsPerLiter: 180 })
    ).toBeNull();
  });
});

describe("tariffCostBreakdown", () => {
  // Regression for the real Izivia wording: "Surcoût de 0,30€/min après 1h
  // de charge" — the per-minute rate must not apply to the first 60
  // minutes at all.
  function graceTariff(overrides) {
    return tariff({
      energy_price_cents_per_kwh: 35,
      session_price_cents_per_min: 30,
      session_price_grace_minutes: 60,
      ...overrides,
    });
  }

  it("charges nothing for time when the session stays within the grace period", () => {
    const got = tariffCostBreakdown(graceTariff(), 10, 45);
    expect(got.time).toBe(0);
    expect(got.total).toBeCloseTo(3.5); // energy only: 0,35€ x 10 kWh
  });

  it("only bills the minutes beyond the grace period", () => {
    const got = tariffCostBreakdown(graceTariff(), 10, 90);
    expect(got.time).toBeCloseTo(9); // (90 - 60) min x 0,30€
    expect(got.total).toBeCloseTo(3.5 + 9);
  });

  it("behaves exactly as before when a tariff has no grace period", () => {
    const got = tariffCostBreakdown(tariff({ energy_price_cents_per_kwh: 35, session_price_cents_per_min: 30 }), 10, 45);
    expect(got.time).toBeCloseTo(13.5); // full 45 min x 0,30€, from minute 1
  });
});

describe("groupStationsByLocation", () => {
  it("groups connectors sharing the exact same coordinates into one site", () => {
    const a = station({ id: "irve:a", location: { lat: 45.9, lng: 6.1 } });
    const b = station({ id: "irve:b", location: { lat: 45.9, lng: 6.1 } });
    const c = station({ id: "irve:c", location: { lat: 46.0, lng: 6.2 } });
    const sites = groupStationsByLocation([a, b, c]);
    expect(sites).toHaveLength(2);
    const siteAB = sites.find((s) => s.stations.length === 2);
    expect(siteAB.stations.map((s) => s.id).sort()).toEqual(["irve:a", "irve:b"]);
  });
});
