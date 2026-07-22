// A backend "station" (see api/stations.js#fetchStationsInBBox) is really a
// single connector (point of charge): a physical site with several
// connectors comes back as several rows sharing the exact same coordinates
// (see backend/internal/domain/station.go's IRVEIDStation vs IRVEIDPDC).
// Left ungrouped, that's several map markers stacked exactly on top of each
// other for what a driver sees as one charging site. groupStationsByLocation
// turns the flat list back into one entry per site, each holding every
// connector-row at that location, so the UI can show one marker and one
// detail card per site instead.
//
// Coordinates are rounded to 6 decimal places (~11cm) rather than compared
// exactly: two connectors of the same real site are expected to carry the
// identical stored value, but rounding guards against any float
// round-tripping (e.g. through JSON) perturbing the last bit, and 11cm is
// far tighter than any two distinct real charging sites would ever be.
function locationKey(station) {
  return `${station.location.lat.toFixed(6)},${station.location.lng.toFixed(6)}`;
}

export function groupStationsByLocation(stations) {
  const byKey = new Map();
  for (const station of stations) {
    const key = locationKey(station);
    if (!byKey.has(key)) {
      byKey.set(key, { key, location: station.location, stations: [] });
    }
    byKey.get(key).stations.push(station);
  }
  return Array.from(byKey.values());
}
