// Tariff sources come from the API as lowercase ids (e.g. "izivia",
// "electra"); capitalize for display since the API has no separate label.
export function formatSourceLabel(sourceId) {
  if (!sourceId) return "";
  return sourceId.charAt(0).toUpperCase() + sourceId.slice(1);
}

// Known price plan ids get a readable French label; anything else (a
// future plan id we don't know about yet) falls back to capitalize, same
// spirit as formatSourceLabel — never hides an unrecognized plan.
const PLAN_LABELS = {
  standard: "Standard",
  public: "Sans l'appli",
  app: "Avec l'appli",
  subscription: "Abonné",
};

export function formatPlanLabel(planId) {
  if (!planId) return "";
  return PLAN_LABELS[planId] ?? formatSourceLabel(planId);
}

// Connector type ids (see backend/internal/domain/connector.go) are mostly
// already display-ready acronyms (CCS, CHAdeMO, T2, EF); "other" is the
// one that needs a French label.
const CONNECTOR_LABELS = {
  other: "Autre",
};

export function formatConnectorLabel(connectorType) {
  if (!connectorType) return "";
  return CONNECTOR_LABELS[connectorType] ?? connectorType;
}

/**
 * Format a tariff's `updated_at` (RFC 3339, as sent by the API) for display.
 * Ingestion rewrites that timestamp on every run whether or not the price
 * moved, so this answers "how fresh is this data?", not "when did the price
 * last change?". Returns "" for a missing or unparseable date so callers can
 * skip the line entirely rather than render "Invalid Date".
 */
export function formatUpdatedAt(isoDate) {
  if (!isoDate) return "";
  const date = new Date(isoDate);
  if (Number.isNaN(date.getTime())) return "";
  return date.toLocaleString("fr-FR", {
    day: "2-digit",
    month: "2-digit",
    year: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}
