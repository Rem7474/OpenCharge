import { useState } from "react";
import { useMap, CircleMarker } from "react-leaflet";
import { LocateFixed, LoaderCircle } from "lucide-react";

// Zoom level to fly to on a successful locate — well above MIN_ZOOM_TO_LOAD
// (see StationMarkers.jsx) so nearby stations load immediately, and close
// enough to actually read individual marker prices rather than a whole
// region.
const LOCATE_ZOOM = 14;

function geolocationErrorMessage(err) {
  switch (err.code) {
    case err.PERMISSION_DENIED:
      return "Localisation refusée. Autorisez l'accès à votre position dans les réglages du navigateur pour l'utiliser.";
    case err.POSITION_UNAVAILABLE:
      return "Position indisponible pour le moment.";
    case err.TIMEOUT:
      return "La localisation a pris trop de temps. Réessayez.";
    default:
      return "Impossible de vous localiser.";
  }
}

/**
 * Floating "locate me" control (bottom-right of the map, clear of the
 * status banners StationMarkers docks at the top): flies the map to the
 * browser's geolocation at a zoom level that immediately loads nearby
 * stations, since the app is entirely viewport-driven (see StationMarkers'
 * MIN_ZOOM_TO_LOAD) — there's no separate "nearby stations" endpoint to
 * call, moving the viewport there is enough.
 */
export default function GeolocateControl() {
  const map = useMap();
  const [status, setStatus] = useState("idle"); // idle | locating | error
  const [error, setError] = useState(null);
  const [position, setPosition] = useState(null);

  const locate = () => {
    if (!("geolocation" in navigator)) {
      setStatus("error");
      setError("La géolocalisation n'est pas disponible sur cet appareil.");
      return;
    }
    setStatus("locating");
    setError(null);
    navigator.geolocation.getCurrentPosition(
      (pos) => {
        const { latitude, longitude, accuracy } = pos.coords;
        setPosition({ lat: latitude, lng: longitude, accuracy });
        map.flyTo([latitude, longitude], Math.max(map.getZoom(), LOCATE_ZOOM));
        setStatus("idle");
      },
      (err) => {
        setStatus("error");
        setError(geolocationErrorMessage(err));
      },
      { enableHighAccuracy: true, timeout: 10000, maximumAge: 60000 }
    );
  };

  return (
    <>
      <button
        type="button"
        className="geolocate-btn"
        onClick={locate}
        disabled={status === "locating"}
        aria-label="Me localiser et afficher les bornes à proximité"
        title="Me localiser"
      >
        {status === "locating" ? (
          <LoaderCircle size={18} strokeWidth={2.2} className="geolocate-spinner" />
        ) : (
          <LocateFixed size={18} strokeWidth={2.2} />
        )}
      </button>
      {status === "error" && error && (
        <div className="status-banner status-banner--error geolocate-error" role="alert">
          {error}
        </div>
      )}
      {position && (
        <CircleMarker
          center={[position.lat, position.lng]}
          radius={8}
          pathOptions={{ color: "#2a78d6", fillColor: "#2a78d6", fillOpacity: 0.5 }}
        />
      )}
    </>
  );
}
