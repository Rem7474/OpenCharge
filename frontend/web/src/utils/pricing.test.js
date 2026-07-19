import { describe, it, expect } from "vitest";
import {
  connectorPriceKind,
  tariffAppliesToBucket,
  preferConnectorMatch,
  bestTariffForSource,
  cheapestTariff,
  pickPriceCentsPerKWh,
  priceTier,
} from "./pricing.js";

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
