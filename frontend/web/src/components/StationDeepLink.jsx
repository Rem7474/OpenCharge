import { useEffect } from "react";
import { useParams } from "react-router-dom";
import { useMap } from "react-leaflet";
import { fetchStationDetails } from "../api/stations.js";

// Well above MIN_ZOOM_TO_LOAD (see StationMarkers.jsx) and matching
// GeolocateControl's own LOCATE_ZOOM, so a shared link lands the viewer
// close enough to read the linked station's own marker immediately.
const DEEP_LINK_ZOOM = 16;

// Builds the minimal "site" shape StationDetails/MapPage expect (see
// utils/stationGrouping.js) from a single GET /stations/{id} response.
// Deliberately just the one connector the link points at, not the full
// same-location group a bbox fetch would produce: without a station list
// endpoint keyed on this criterion this is the only site info we have.
// Once the map settles here, StationMarkers' own bbox load will still
// surface any sibling connectors at the same site as separate markers.
function siteFromDetail(detail) {
  const station = detail.station;
  return {
    key: station.id,
    location: station.location,
    stations: [
      {
        id: station.id,
        name: station.name,
        operator: station.operator,
        enseigne: station.enseigne,
        address: station.address,
        connectors: station.connectors,
      },
    ],
  };
}

/**
 * Resolves a /station/:id deep link into a selected site + map viewport, so
 * opening a shared station URL immediately shows that station's detail
 * panel — the counterpart to MapPage's own onSelect->navigate(), which
 * writes that same URL when a marker is clicked. Rendered inside
 * MapContainer (needs useMap) as a sibling of StationMarkers/
 * GeolocateControl; onSelect is the raw setSelectedSite setter, not
 * MapPage's navigate-wrapping handler, since the id is already in the URL —
 * that's why this ran in the first place.
 */
export default function StationDeepLink({ selectedSite, onSelect }) {
  const { id } = useParams();
  const map = useMap();

  useEffect(() => {
    if (!id) return undefined;
    // A marker click already sets selectedSite itself before/alongside
    // navigating (see MapPage's selectSite) — nothing to resolve in that
    // case, only a fresh deep link or a browser back/forward across two
    // different stations actually needs a fetch here.
    if (selectedSite?.stations?.some((s) => s.id === id)) return undefined;

    const controller = new AbortController();
    fetchStationDetails(id, { signal: controller.signal })
      .then((detail) => {
        const site = siteFromDetail(detail);
        onSelect(site);
        map.flyTo([site.location.lat, site.location.lng], Math.max(map.getZoom(), DEEP_LINK_ZOOM));
      })
      .catch(() => {
        // A real failure (station gone, network error) just leaves no site
        // selected — StationMarkers' own bbox load still proceeds normally.
      });
    return () => controller.abort();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id]);

  return null;
}
