// Tariff sources come from the API as lowercase ids (e.g. "izivia",
// "electra"); capitalize for display since the API has no separate label.
export function formatSourceLabel(sourceId) {
  if (!sourceId) return "";
  return sourceId.charAt(0).toUpperCase() + sourceId.slice(1);
}
