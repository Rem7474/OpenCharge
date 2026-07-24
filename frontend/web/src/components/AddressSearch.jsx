import { useEffect, useRef, useState } from "react";
import { useMap } from "react-leaflet";
import { Search, X, LoaderCircle } from "lucide-react";
import { searchAddress } from "../api/geocode.js";

const MIN_QUERY_LENGTH = 3;
const DEBOUNCE_MS = 300;

// Zoom level to fly to on selecting a result — tighter for a precise
// housenumber/street match, wider for a whole town, mirroring
// GeolocateControl's own LOCATE_ZOOM for the "close enough to read
// individual marker prices" precise case.
function zoomForResultType(type) {
  switch (type) {
    case "housenumber":
    case "street":
      return 16;
    case "locality":
      return 14;
    default:
      return 12; // municipality, or anything unrecognized
  }
}

/**
 * Floating address/city search (top-left of the map, mirroring
 * GeolocateControl's bottom-right "locate me" button): flies the map to a
 * selected result at a zoom level that immediately loads nearby stations,
 * same reasoning as GeolocateControl — the app is entirely viewport-driven
 * (see StationMarkers' MIN_ZOOM_TO_LOAD), so moving the viewport is enough.
 */
export default function AddressSearch() {
  const map = useMap();
  const [query, setQuery] = useState("");
  const [results, setResults] = useState([]);
  const [open, setOpen] = useState(false);
  const [loading, setLoading] = useState(false);
  const [activeIndex, setActiveIndex] = useState(-1);
  const abortRef = useRef(null);
  const debounceRef = useRef(null);

  useEffect(() => {
    return () => {
      clearTimeout(debounceRef.current);
      abortRef.current?.abort();
    };
  }, []);

  const runSearch = (value) => {
    abortRef.current?.abort();
    if (value.trim().length < MIN_QUERY_LENGTH) {
      setResults([]);
      setLoading(false);
      return;
    }
    const controller = new AbortController();
    abortRef.current = controller;
    setLoading(true);
    searchAddress(value, { signal: controller.signal })
      .then((found) => {
        setResults(found);
        setActiveIndex(-1);
      })
      .catch((err) => {
        if (err.name !== "AbortError") setResults([]);
      })
      .finally(() => setLoading(false));
  };

  const onChange = (e) => {
    const value = e.target.value;
    setQuery(value);
    setOpen(true);
    clearTimeout(debounceRef.current);
    debounceRef.current = setTimeout(() => runSearch(value), DEBOUNCE_MS);
  };

  const selectResult = (result) => {
    if (!result) return;
    map.flyTo([result.lat, result.lng], Math.max(map.getZoom(), zoomForResultType(result.type)));
    setQuery(result.label ?? "");
    setResults([]);
    setOpen(false);
    setActiveIndex(-1);
  };

  const clear = () => {
    setQuery("");
    setResults([]);
    setOpen(false);
    setActiveIndex(-1);
    abortRef.current?.abort();
  };

  const onKeyDown = (e) => {
    if (!open || results.length === 0) return;
    if (e.key === "ArrowDown") {
      e.preventDefault();
      setActiveIndex((i) => (i + 1) % results.length);
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setActiveIndex((i) => (i <= 0 ? results.length - 1 : i - 1));
    } else if (e.key === "Enter") {
      e.preventDefault();
      selectResult(results[activeIndex] ?? results[0]);
    } else if (e.key === "Escape") {
      setOpen(false);
    }
  };

  const showResults = open && (loading || results.length > 0 || query.trim().length >= MIN_QUERY_LENGTH);

  return (
    <div className="address-search" role="combobox" aria-expanded={showResults} aria-haspopup="listbox" aria-owns="address-search-results">
      <div className="address-search-input-wrap">
        <Search size={15} strokeWidth={2.2} className="address-search-icon" aria-hidden="true" />
        <input
          type="text"
          value={query}
          onChange={onChange}
          onKeyDown={onKeyDown}
          onFocus={() => setOpen(true)}
          placeholder="Rechercher une adresse ou une ville"
          aria-label="Rechercher une adresse ou une ville"
          aria-autocomplete="list"
          aria-controls="address-search-results"
        />
        {loading && <LoaderCircle size={15} strokeWidth={2.2} className="address-search-spinner" aria-hidden="true" />}
        {!loading && query && (
          <button type="button" className="address-search-clear" onClick={clear} aria-label="Effacer la recherche">
            <X size={14} strokeWidth={2.2} />
          </button>
        )}
      </div>
      {showResults && (
        <ul className="address-search-results" id="address-search-results" role="listbox">
          {results.length === 0 && !loading && query.trim().length >= MIN_QUERY_LENGTH && (
            <li className="address-search-empty" role="presentation">
              Aucun résultat
            </li>
          )}
          {results.map((r, i) => (
            <li key={`${r.lat},${r.lng}`} role="option" aria-selected={i === activeIndex}>
              <button
                type="button"
                className={i === activeIndex ? "active" : ""}
                onMouseEnter={() => setActiveIndex(i)}
                onClick={() => selectResult(r)}
              >
                {r.label}
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
