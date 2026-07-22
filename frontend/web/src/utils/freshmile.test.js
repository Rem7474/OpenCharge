import { describe, it, expect } from "vitest";
import { findFreshmileSiteMeta } from "./freshmile.js";

function detailWith(tariffs) {
  return { station: {}, tariffs };
}

describe("findFreshmileSiteMeta", () => {
  it("finds img_preview_url and freshmile_location_id from any connector's tariffs", () => {
    const details = [
      detailWith([{ source: "izivia", extra: {} }]),
      detailWith([
        {
          source: "freshmile",
          extra: { img_preview_url: "https://example.com/preview.jpg", freshmile_location_id: 829320 },
        },
      ]),
    ];
    expect(findFreshmileSiteMeta(details)).toEqual({
      imgPreviewUrl: "https://example.com/preview.jpg",
      locationId: 829320,
    });
  });

  it("combines the two fields even when they come from different connectors/tariffs", () => {
    const details = [
      detailWith([{ source: "freshmile", extra: { freshmile_location_id: 829320 } }]),
      detailWith([{ source: "freshmile", extra: { img_preview_url: "https://example.com/preview.jpg" } }]),
    ];
    expect(findFreshmileSiteMeta(details)).toEqual({
      imgPreviewUrl: "https://example.com/preview.jpg",
      locationId: 829320,
    });
  });

  it("returns nulls when no connector has freshmile data", () => {
    const details = [detailWith([{ source: "izivia", extra: {} }])];
    expect(findFreshmileSiteMeta(details)).toEqual({ imgPreviewUrl: null, locationId: null });
  });

  it("handles a missing/empty details array", () => {
    expect(findFreshmileSiteMeta(null)).toEqual({ imgPreviewUrl: null, locationId: null });
    expect(findFreshmileSiteMeta([])).toEqual({ imgPreviewUrl: null, locationId: null });
  });
});
