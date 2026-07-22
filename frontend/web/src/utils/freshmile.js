// A site's Freshmile-sourced extras (a Street View preview image, and the
// numeric location id needed to poll real-time availability — see
// components/FreshmileAvailability.jsx) live in every connector's own
// "freshmile" tariff Extra, not on the station itself (see backend
// ingestion/freshmile.go's normalizeFreshmileTariffs doc comment: a single
// Freshmile location's connectors can end up correlated to several
// different IRVE station rows — one per connector kind — so there's no
// single shared place to attach a location-level field to instead).
//
// Scans every connector's tariffs for the first non-null value of each
// field independently, rather than stopping at the first tariff that has
// either one — they're set independently in the backend (a location can
// have one without the other), and are identical across every connector
// of the same site regardless of which one happens to carry them.
export function findFreshmileSiteMeta(details) {
  let imgPreviewUrl = null;
  let locationId = null;
  for (const detail of details ?? []) {
    for (const tariff of detail?.tariffs ?? []) {
      const extra = tariff.extra;
      if (!extra) continue;
      if (imgPreviewUrl == null && extra.img_preview_url) imgPreviewUrl = extra.img_preview_url;
      if (locationId == null && extra.freshmile_location_id != null) locationId = extra.freshmile_location_id;
    }
  }
  return { imgPreviewUrl, locationId };
}
