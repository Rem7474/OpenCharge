/**
 * Site-wide "X/Y bornes disponibles" badge — purely presentational, fed by
 * useFreshmileAvailability (see StationDetails.jsx, which fetches once and
 * shares the result with each connector's own badge too — see
 * ConnectorPriceSection). evsesTotalCount/evsesAvailableCount count
 * physical charge points (evses), not connectors: a single evse can expose
 * several connectors sharing the same plug/power (e.g. a Type 2 and a
 * domestic socket on the same pedestal — real production data), so
 * "available" only ever means the whole evse is free, never one of its
 * connectors independently of the other.
 */
export default function FreshmileAvailability({ availability }) {
  if (!availability || availability.evsesTotalCount === 0) return null;
  const { evsesAvailableCount, evsesTotalCount } = availability;
  const allAvailable = evsesAvailableCount === evsesTotalCount;
  return (
    <span className={`freshmile-availability ${allAvailable ? "freshmile-availability--available" : evsesAvailableCount > 0 ? "freshmile-availability--partial" : "freshmile-availability--unavailable"}`}>
      ● {evsesAvailableCount}/{evsesTotalCount} borne{evsesTotalCount > 1 ? "s" : ""} disponible{evsesAvailableCount > 1 ? "s" : ""}
    </span>
  );
}
