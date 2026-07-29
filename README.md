<div align="center">

# 🔌 OpenCharge

**Quelle borne de recharge est vraiment la moins chère, près de vous ?**

Carte des bornes de recharge en France (bientôt en Europe), avec les prix
réels de chaque opérateur superposés au référentiel officiel IRVE —
Electra, Izivia, Freshmile, Ionity, Fastned et d'autres.

### 👉 [**opencharge.remcorp.fr**](https://opencharge.remcorp.fr) 👈

</div>

<br>

## Pourquoi cet outil ?

Le référentiel officiel IRVE liste ~132 000 points de charge en France,
mais sans les prix. Chaque opérateur (Electra, Izivia, Tesla...) publie
les siens séparément, souvent selon plusieurs paliers (avec ou sans appli,
abonné ou non). OpenCharge corrèle ces deux mondes géographiquement, pour
afficher sur une seule carte : où sont les bornes, ce qu'elles coûtent
réellement selon votre mode de paiement, et via quel opérateur.

## Ce que vous pouvez faire

- 🗺️ **Carte dynamique** qui ne charge jamais l'intégralité des bornes :
  tout est piloté par ce que vous regardez (zoom/déplacement)
- 🔍 **Filtrer par opérateur**, avec sélection multiple — le prix affiché
  est le moins cher parmi les sources cochées
- 💳 **Choisir votre palier tarifaire** quand un opérateur en propose
  plusieurs (ex. Electra : sans l'appli / avec l'appli / abonné Smart)
- 🔋 **Basculer entre €/kWh et prix pour une recharge donnée** (nombre de
  kWh configurable)
- 📊 **Voir le détail par connecteur** d'une station : prix par source,
  meilleur prix toutes sources confondues, et pour les tarifs variables
  dans la journée, un graphique horaire
- 📍 **Disponibilité en temps réel** (bornes libres/total) pour les
  stations Freshmile
- 📱 **Applications mobiles** (Android/iOS) en plus de la version web

## Sources de données

- **IRVE** (Etalab, consolidé par transport.data.gouv.fr) — le
  référentiel canonique des points de charge
- **Tarifs** : Electra, Izivia, Freshmile, Fastned,
  Lidl, Ionity, ChargeNow, eborn, Sowatt Solutions

Le détail de la méthodologie de corrélation et les limites de fiabilité
des prix affichés sont expliqués dans la page **À propos** de
l'application.

## Envie de contribuer ou de faire tourner le projet en local ?

Toute la partie technique (architecture backend/frontend, modèle de
données, ingestion par source, API, Docker, build mobile) est documentée
séparément dans [`docs/DEVELOPMENT.md`](docs/DEVELOPMENT.md).
