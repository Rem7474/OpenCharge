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
