import { useEffect, useState } from "react";
import { fetchSources } from "../api/stations.js";
import { formatSourceLabel, formatPlanLabel } from "../utils/format.js";

/**
 * Full-page operator/plan picker — the same selection this app already
 * exposes as a dropdown in OperatorFilter.jsx, presented as a standalone
 * step. Shown automatically on first visit (see MapPage's showOnboarding
 * state) and re-openable later from the filter bar, in which case
 * `initialSources` carries the current selection so re-opening doesn't
 * reset it to empty.
 */
export default function OnboardingScreen({ initialSources, onComplete, onSkip }) {
  const [allSources, setAllSources] = useState([]);
  const [loading, setLoading] = useState(true);
  const [selected, setSelected] = useState(initialSources ?? {});

  useEffect(() => {
    const controller = new AbortController();
    fetchSources({ signal: controller.signal })
      .then((sources) => setAllSources(sources ?? []))
      .catch((err) => {
        if (err.name !== "AbortError") console.error(err);
      })
      .finally(() => setLoading(false));
    return () => controller.abort();
  }, []);

  const toggleSource = (source) => {
    setSelected((prev) => {
      const next = { ...prev };
      if (source.id in next) {
        delete next[source.id];
      } else {
        next[source.id] = source.plans[0];
      }
      return next;
    });
  };

  const selectPlan = (sourceId, planId) => {
    setSelected((prev) => ({ ...prev, [sourceId]: planId }));
  };

  const selectedCount = Object.keys(selected).length;

  return (
    <div className="onboarding-screen">
      <div className="onboarding-header">
        <h2>Vos opérateurs</h2>
        <p>Sélectionnez vos opérateurs de recharge et abonnements pour comparer les tarifs en temps réel sur la carte.</p>
      </div>

      <div className="onboarding-list">
        {loading && <p className="onboarding-empty">Chargement des réseaux…</p>}
        {!loading && allSources.length === 0 && <p className="onboarding-empty">Aucun réseau disponible.</p>}
        {allSources.map((source) => {
          const checked = source.id in selected;
          return (
            <div key={source.id} className="onboarding-item">
              <label className="onboarding-item-row" onClick={() => toggleSource(source)}>
                <span className="onboarding-item-name">{formatSourceLabel(source.id)}</span>
                <input type="checkbox" checked={checked} readOnly />
              </label>
              {checked && source.plans.length > 1 && (
                <div className="plan-selector" role="group" aria-label={`Palier tarifaire ${source.id}`}>
                  {source.plans.map((plan) => (
                    <button
                      key={plan}
                      type="button"
                      aria-pressed={selected[source.id] === plan}
                      onClick={() => selectPlan(source.id, plan)}
                    >
                      {formatPlanLabel(plan)}
                    </button>
                  ))}
                </div>
              )}
            </div>
          );
        })}
      </div>

      <div className="onboarding-footer">
        {onSkip && (
          <button type="button" className="onboarding-skip" onClick={onSkip}>
            Passer
          </button>
        )}
        <button type="button" className="onboarding-continue" onClick={() => onComplete(selected)}>
          {selectedCount > 0 ? `${selectedCount} opérateur(s) sélectionné(s) — Continuer` : "Continuer sans sélection"}
        </button>
      </div>
    </div>
  );
}
