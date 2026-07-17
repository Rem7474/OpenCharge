// Persists the MapPage `filters` object (see DEFAULT_FILTERS in
// pages/MapPage.jsx) across reloads, and doubles as the signal for whether
// the onboarding screen has ever been completed: a missing/invalid entry
// means "first visit", not just "nothing filtered yet".
const FILTERS_KEY = "opencharge:filters";

/**
 * Reads the persisted filters object, or null if nothing valid is stored
 * (first visit, corrupted JSON, or an unexpected shape from an older
 * version of the app).
 */
export function readStoredFilters() {
  let raw;
  try {
    raw = window.localStorage.getItem(FILTERS_KEY);
  } catch {
    return null;
  }
  if (!raw) return null;
  try {
    const parsed = JSON.parse(raw);
    if (!parsed || typeof parsed !== "object" || typeof parsed.sources !== "object") {
      return null;
    }
    return parsed;
  } catch {
    return null;
  }
}

/** Persists the filters object; failures (e.g. private browsing, quota) are non-fatal. */
export function writeStoredFilters(filters) {
  try {
    window.localStorage.setItem(FILTERS_KEY, JSON.stringify(filters));
  } catch {
    // Persistence is a nice-to-have, not required for the app to function.
  }
}
