# Audit UI/UX & Fonctionnalités — OpenCharge (juillet 2026)

Périmètre audité : `frontend/web/` (React 18 + Leaflet, JS pur, pas de
TypeScript), avec vérification croisée sur `backend/internal/api` et
`backend/internal/domain` pour les questions de disponibilité temps réel.

Légende de criticité : 🔴 bloquant · 🟠 important · 🟡 mineur

---

## Mission 1 — Audit UI/UX existant

### 1.1 Ergonomie & Expérience utilisateur

**🟠 Aucun état d'erreur visible sur le flux de données le plus critique**
`StationMarkers.jsx` (fetch des marqueurs sur la carte) avale silencieusement
les erreurs réseau :

```js
// src/components/StationMarkers.jsx:59-61
.catch((err) => {
  if (err.name !== "AbortError") console.error(err);
});
```

Si l'API est injoignable (backend down, 500, CORS), l'utilisateur voit une
carte vide ou figée sans aucun message. Seule la console navigateur reçoit
l'erreur. Correction : ajouter un état `error` (sur le modèle de
`OnboardingScreen.jsx`, qui gère correctement trois états distincts —
chargement / erreur / vide) et un bandeau "Impossible de charger les
bornes, réessayer".

**🟠 État "aucune borne trouvée" non distingué de l'état "en cours de
chargement"**
Dans `StationMarkers.jsx`, une fois `loading` à `false`, une carte
correctement zoomée mais sans stations correspondant aux filtres actifs
affiche exactement le même rendu (carte vide) qu'un chargement en cours ou
qu'une erreur silencieuse. Aucun message "Aucune borne ne correspond à vos
filtres dans cette zone" n'existe.

**🟠 Message d'erreur technique brut exposé à l'utilisateur**
`StationDetails.jsx:137` affiche `Erreur : {error}` où `error` provient
directement de `err.message` (`src/api/stations.js:39`, ex.
`"GET /stations/xyz failed: 500"`), un message anglophone/technique dans une
interface autrement entièrement en français. Il faudrait mapper les erreurs
réseau vers un message utilisateur générique.

**🟡 Aucune indication de fraîcheur des tarifs**
`StationDetails.jsx` affiche la date de mise à jour d'un tarif en italique
sans distinction visuelle entre une donnée d'hier et une donnée vieille d'un
an — un simple badge ("mis à jour il y a 8 mois") apporterait un signal de
confiance.

**🟡 Paliers de puissance encodés par un magic number**
`ConnectorFilter.jsx:14` : `POWER_STEPS = [3, 22, 50, 150, 350]` utilise
`3 kW` comme valeur "pas de filtre", documenté uniquement par un commentaire,
sans constante partagée nommée (`NO_MIN_POWER`) ni synchronisation avec le
backend.

### 1.2 Panneaux flottants : comportement de faux-modal sans l'être vraiment

**🔴 `OperatorFilter.jsx` — panneau à overlay sans focus trap ni `Escape`**
Ce composant affiche un vrai overlay/backdrop (`operator-panel-overlay` /
`operator-panel`, ligne 73) qui se comporte visuellement comme une boîte de
dialogue modale, mais :
- aucun `role="dialog"` / `aria-modal="true"` ;
- aucune gestion de la touche `Échap` ;
- aucun piège de focus (le focus clavier peut sortir du panneau vers le
  contenu masqué derrière) ;
- au moment de la fermeture, le focus n'est jamais rendu au bouton qui a
  ouvert le panneau.

Un utilisateur clavier ou lecteur d'écran peut se retrouver "perdu" derrière
l'overlay. Correction : ajouter `role="dialog"` + `aria-modal`, un
gestionnaire `onKeyDown` pour `Escape`, et un focus trap simple (ou la
balise native `<dialog>`).

**🟠 `FilterPanel.jsx` — même défaut, en version moins visuelle**
Panneau ancré (pas d'overlay plein écran) mais avec la même sémantique de
fermeture au clic extérieur (`useEffect` + `mousedown`, lignes 40-48) sans
gestion de focus à l'ouverture (le focus n'est jamais déplacé vers le
premier champ) ni au retour.

### 1.3 Mobile / tactile

Le zoom minimum (10) et le pilotage par viewport (bbox) sont bien pensés
pour limiter la charge sur mobile. Cependant :

**🟠 Aucun test mobile réel dans le dépôt** — pas de configuration Cypress
ni Playwright mobile, et aucun test automatisé du tout côté frontend
(`package.json` ne liste ni Vitest, ni Jest, ni React Testing Library). Les
comportements tactiles (taille des cibles sur les pastilles de connecteur,
double-tap sur la carte Leaflet vs sélection d'un marqueur) ne sont donc
validés que manuellement.

**🟡 Tailles de police non standardisées** — `index.css` (954 lignes)
mélange des valeurs de `11px` à `19px` sans échelle typographique
cohérente ; sur petits écrans mobiles, certains libellés à `11px`/`12px`
(ex. légendes de filtres) sont proches du seuil de lisibilité recommandé.

### 1.4 Cohérence du design system

**🟠 Design tokens partiels** — `index.css` définit des variables CSS pour
les couleurs (`--color-bg`, `--color-accent`, etc., y compris un thème
sombre aux lignes 20-33 et 920-938) mais **pas** pour les espacements, les
rayons de bordure ou les z-index. Résultat : `border-radius: 999px / 14px /
18px` et des `z-index: 1150 / 1300 / 1000` répétés en dur à plusieurs
endroits (lignes 117, 134, 236, 344) sans échelle partagée — tout
changement de superposition (ouvrir un nouveau panneau) oblige à relire
tout le fichier pour trouver la bonne valeur de `z-index`.

**🟡 Couleur non tokenisée** — `color: #fff` écrit en dur sur les boutons
d'accent (plusieurs occurrences, ex. lignes ~530, ~601, ~768, ~902) plutôt
que via une variable `--color-on-accent` ; fonctionne aujourd'hui car les
fonds d'accent sont toujours sombres, mais cassera silencieusement si la
couleur d'accent change.

### 1.5 Performance UI

**🟠 Pas de clustering des marqueurs Leaflet**
Ni `react-leaflet-cluster` ni `leaflet.markercluster` ne figurent dans
`package.json`. `StationMarkers.jsx:83-112` rend un `<Marker>` Leaflet
individuel par station, avec une icône `L.divIcon` reconstruite à partir
d'une chaîne HTML (lignes 13-20) sans mémoïsation. En zone urbaine dense,
juste au-dessus du zoom minimum (10), cela peut produire un chevauchement
important de marqueurs et un DOM volumineux sans decluttering. Un
clustering (`react-leaflet-cluster`) réduirait à la fois le bruit visuel et
le nombre de nœuds DOM.

**🟡 Pas de debounce sur pan/zoom**
`StationMarkers.jsx:65-68` déclenche un fetch à chaque `moveend`/`zoomend`
Leaflet sans délai de debounce. Un `AbortController` (lignes 42-44) annule
les requêtes obsolètes, ce qui limite les dégâts, mais un debounce de
200-300 ms réduirait encore le nombre de requêtes lors d'un pan continu.

**🟡 Aucune mémoïsation (`useMemo`/`useCallback`/`React.memo`) nulle part
dans le code base** — `MapPage.jsx` recrée à chaque rendu tous ses
callbacks et l'objet `filters`, ce qui cascade des re-rendus vers
`FilterBar` → `FilterPanel` (21 props) et `StationMarkers`/`StationDetails`.
Sur le volume actuel de données (viewport limité), l'impact mesuré est
probablement faible, mais c'est un point de vigilance si la densité de
marqueurs ou la fréquence de mise à jour des filtres augmente.

### 1.6 Accessibilité (a11y)

Le point fort du projet : `HourlyPriceChart.jsx` est bien construit sur le
plan a11y — `role="img"` avec un `aria-label` calculé décrivant min/max/prix
courant (ligne 74), doublé d'un `<table className="visually-hidden">` avec
`<caption>` et `<th scope="col">` (lignes 111-129) pour les lecteurs
d'écran. `ConnectorFilter.jsx` et `FilterPanel.jsx` utilisent correctement
`role="group"`, `aria-label`, `aria-pressed`, `aria-expanded`.

**🔴 Absence de focus management / focus trap** sur les deux panneaux qui se
comportent comme des modales (`OperatorFilter`, `FilterPanel`) — voir 1.2.

**🟠 États de chargement/erreur sans `aria-live`**
`StationDetails.jsx:137-138` : `Chargement…` et `Erreur : ...` sont du texte
brut, sans `aria-live="polite"` ni `role="alert"`, donc un lecteur d'écran
n'annonce pas ces changements d'état — l'utilisateur doit re-parcourir la
page pour les découvrir.

**🟡 Pas de framework de test d'accessibilité automatisé** (axe-core, etc.)
dans les scripts CI/npm — les bons réflexes ARIA constatés dans le code
reposent uniquement sur la discipline manuelle, sans garde-fou.

### 1.7 Qualité de code UI

**🟠 Aucun TypeScript, aucun PropTypes** — projet 100% JS/JSX, aucune
validation de forme de props à la compilation ni à l'exécution
(`tsconfig.json` absent, `prop-types` absent des dépendances). Risque
concret : `utils/pricing.js` documente explicitement (lignes 5-8, 64-69)
que ses ensembles `DC_CONNECTOR_TYPES`/`AC_CONNECTOR_TYPES` et sa fonction
`timeInWindow` doivent être *manuellement* synchronisés avec le code Go et
une fonction SQL équivalente côté backend — une désynchronisation
silencieuse est possible, sans qu'aucun test ni type ne l'empêche.

**🟠 Prop drilling important** — `MapPage.jsx` passe 19 props à `FilterBar`
(lignes 109-131), qui les retransmet quasi intégralement (`{...props}`,
`FilterBar.jsx:45`) à `FilterPanel`, qui en reçoit 21 au total
(`FilterPanel.jsx:15-37`). Une faute de frappe dans un nom de prop échoue
silencieusement, sans avertissement au build. Suggestion : un contexte React
`FiltersContext` ou un state manager léger (Zustand) pour ce sous-arbre.

**🟠 Composant monolithique : `StationDetails.jsx` (246 lignes)**
Combine deux fonctions pures (`bestTariffForSource`, `cheapestTariff`,
lignes 17-37), deux sous-composants (`TariffCost`, `TariffRow`, lignes
46-95) et le composant principal qui recalcule ses données dérivées
(`connectorKind`, `selectedTariffs`, `cheapestSelected`, `overallBest`,
`overallBeatsSelection`, lignes 116-133) **en ligne dans le rendu, sans
`useMemo`**, à chaque re-rendu. Le même bloc conditionnel
`hasHourlyPricing(tariff) ? <HourlyPriceChart/> : <TariffCost/>` est
dupliqué **verbatim trois fois** (lignes ~79-83, ~215-219, ~228-232) au
lieu d'être factorisé en un seul sous-composant `<TariffDisplay>`.

**🟡 Logique de fetch dupliquée : `OperatorFilter.jsx` vs
`OnboardingScreen.jsx`** — les deux composants réimplémentent
indépendamment le fetch des sources tarifaires, la sélection de plan
(`toggleSource`, `selectPlan`) et le rendu des pastilles de palier
(`role="group" aria-label="Palier tarifaire ..."`, boutons `aria-pressed`)
avec une logique quasiment identique. Un hook partagé
(`useOperatorSources()`) éliminerait la duplication.

**🟡 `OperatorFilter.jsx` a deux `useEffect` quasi identiques** (lignes
25-36 et 42-55) qui appellent tous deux `fetchSources` avec le même
traitement succès/erreur, plutôt qu'une fonction unique invoquée à deux
moments.

---

## Mission 2 — Benchmark vs applications de référence

| Application | Points forts UI/UX | Fonctionnalités différenciantes | Présent dans OpenCharge ? |
|---|---|---|---|
| Chargemap | Compte utilisateur, reviews, navigation GPS | Communauté, check-ins, disponibilité temps réel | ❌ (aucun compte, aucun statut temps réel dans le schéma) |
| ABRP | Planification porte-à-porte | Intégration véhicule, calcul SoC | ❌ (aucun planificateur d'itinéraire) |
| PlugShare | Feed communautaire, photos | Rapports d'utilisation, tags | ❌ |
| Electromaps | Filtre avancé type connecteur | Statistiques d'utilisation | ✅ partiel (filtre connecteur existe, pas de stats) |
| Zap-Map | Carte épurée, filtre réseau | Alertes réseau, statut temps réel | ✅ carte épurée / ❌ alertes, ❌ statut temps réel |
| ChargePoint | Design system cohérent | Réservation, historique de charge | 🟡 design system partiel (tokens couleur seulement) |

### Gaps fonctionnels identifiés

1. **Disponibilité temps réel** — absente du modèle de données. Vérifié
   côté backend : `domain.Station` (`backend/internal/domain/station.go`)
   ne contient **aucun** champ `Status`/`Available`/`OperationalStatus` —
   uniquement des attributs statiques IRVE (puissance, type de connecteur,
   accès, 24/7, adresse). Aucune des sources tarifaires ingérées
   (Electra, Izivia, Tesla, Freshmile, Fastned, Lidl, Ionity, eborn,
   Sowatt, ChargeNow) n'expose de statut d'occupation dans le pipeline
   d'ingestion actuel. C'est l'écart le plus structurant avec Chargemap/
   PlugShare/Zap-Map.
2. **Aucun compte utilisateur** — donc aucun favori, aucun historique de
   recharge, aucune alerte personnalisée possible aujourd'hui.
3. **Aucune planification d'itinéraire** — l'app est une carte de
   consultation, pas un planificateur (contrairement à ABRP).
4. **Filtres avancés partiels** — le filtre connecteur/puissance existe
   (`ConnectorFilter.jsx`) mais pas de filtre par disponibilité (puisqu'il
   n'y a pas de donnée de disponibilité), ni de recherche par adresse.
5. **Comparaison de prix interopérateurs** — déjà un point fort réel de
   l'existant (`StationDetails.jsx` calcule `cheapestTariff`/`overallBest`
   par station), plus abouti que la plupart des concurrents cités sur ce
   point précis. Marge de progression : historique de prix dans le temps
   (absent), et mise en avant visuelle plus forte du "meilleur prix" au
   niveau de la carte elle-même (aujourd'hui uniquement dans le panneau de
   détail).
6. **Pas de mode sombre indépendant du thème système** — le CSS a des
   variables dark-mode (`index.css:20-33, 920-938`) mais elles suivent
   `prefers-color-scheme`, sans bouton de bascule manuelle dans l'UI.
7. **Pas de partage/deep-link de station** — `HashRouter` avec deux routes
   seulement (`/`, `/about`), aucune route `/station/:id` ; impossible de
   partager un lien direct vers une station précise.

---

## Mission 3 — Suggestions de nouvelles fonctionnalités

### 3.1 Deep-link vers une station (partage d'URL)
**Valeur** : permet de partager une borne précise par lien, d'ouvrir
directement une station depuis un moteur de recherche/réseau social.
**Complexité** : S. **Impact UX** : élevé.
**Point d'entrée** : `App.jsx` (ajouter une route `/station/:id` dans le
`HashRouter`), `MapPage.jsx` (lire le param d'URL au montage pour
pré-sélectionner `selectedStationId` et centrer la carte via `GetStation`
côté `api/stations.js`), `StationDetails.jsx` (bouton "Copier le lien").
Ne nécessite aucune modification backend.

### 3.2 Historique des prix par station
**Valeur** : voir l'évolution tarifaire dans le temps, détecter les
hausses ; complète naturellement le point fort actuel de comparaison
interopérateurs.
**Complexité** : M (nécessite de conserver un historique plutôt qu'un
upsert écrasant, côté `station_tariffs`).
**Impact UX** : moyen-élevé.
**Point d'entrée** : migration SQL ajoutant une table
`station_tariff_history` (ou passage de `station_tariffs` en append-only
avec `valid_from`/`valid_to`), nouvel endpoint
`GET /stations/{id}/tariffs/history`, nouveau composant frontend
`TariffHistoryChart.jsx` (réutilisable avec le style de
`HourlyPriceChart.jsx`).

### 3.3 Recherche par adresse
**Valeur** : "trouver les bornes autour de [adresse]" est un point d'entrée
plus naturel que le pan/zoom manuel, surtout sur mobile.
**Complexité** : S-M (géocodage via l'API Adresse du gouvernement français,
gratuite, pas de dépendance externe payante).
**Impact UX** : élevé.
**Point d'entrée** : nouveau composant `AddressSearch.jsx` dans
`FilterBar.jsx`/`MapPage.jsx`, appel à `api-adresse.data.gouv.fr` (aucun
backend Go à modifier — recentre simplement la carte Leaflet existante).

### 3.4 Filtres avancés étendus (connecteur multi-sélection, tri par prix)
**Valeur** : rapprocher OpenCharge d'Electromaps sur la granularité de
filtrage.
**Complexité** : S (le filtre connecteur existe déjà côté
`ConnectorFilter.jsx` et backend `connectorType` ; il s'agit d'étendre
l'UI, pas de créer un nouveau système).
**Impact UX** : moyen.
**Point d'entrée** : `ConnectorFilter.jsx`, `FilterPanel.jsx`.

### 3.5 Mode sombre avec bascule manuelle
**Valeur** : confort visuel de conduite nocturne, cohérence avec les
attentes utilisateur modernes (ChargePoint, Zap-Map).
**Complexité** : S (les tokens CSS dark existent déjà,
`index.css:20-33, 920-938` — il ne manque qu'un état contrôlé + un
attribut `data-theme` sur `<html>` plutôt qu'une dépendance à
`prefers-color-scheme` seule).
**Impact UX** : moyen.
**Point d'entrée** : `App.jsx` ou nouveau hook `useTheme.js` +
`utils/storage.js` (persistance du choix), toggle dans le header.

### 3.6 Widget de prix embarquable (iframe)
**Valeur** : distribution/visibilité pour OpenCharge, cas d'usage évoqué
explicitement dans la mission.
**Complexité** : M.
**Impact UX** : élevé (pour les sites tiers), faible sur l'app elle-même.
**Point d'entrée** : nouvelle route frontend minimaliste
`/embed/station/:id` (composant allégé réutilisant `StationDetails.jsx`
sans le chrome de l'app), pas de changement backend au-delà de CORS déjà
géré par l'API existante.

### 3.7 Onboarding / tutoriel des paliers tarifaires
**Valeur** : les "paliers tarifaires" (plans) sont un concept propre à
OpenCharge et non trivial pour un nouvel utilisateur.
**Complexité** : S — `OnboardingScreen.jsx` existe déjà comme socle,
il s'agit de l'étendre avec 1-2 écrans explicatifs plutôt que de tout
recréer.
**Impact UX** : moyen.
**Point d'entrée** : `OnboardingScreen.jsx`, `utils/storage.js` (flag
"a vu l'explication des paliers").

### 3.8 Compte utilisateur (favoris uniquement, sans historique de recharge)
**Valeur** : première brique d'un compte, sans complexité de tracking de
sessions de recharge réelles.
**Complexité** : L (auth, table utilisateurs, endpoints protégés).
**Impact UX** : élevé mais dépend d'une infrastructure nouvelle.
**Point d'entrée** : backend — nouvelle table `users` + `favorites`,
middleware d'auth (`backend/internal/api`) ; frontend — nouveau contexte
`AuthContext`, bouton favori sur `StationDetails.jsx`.

### 3.9 Alertes de prix sur favoris
**Valeur** : notifier si le prix d'une station favorite passe sous un
seuil — nécessite 3.8 au préalable.
**Complexité** : XL (auth + job de vérification périodique + canal de
notification push/email).
**Impact UX** : élevé pour les utilisateurs engagés, faible en adoption
initiale (fonctionnalité de niche tant qu'il n'y a pas de compte).
**Point d'entrée** : dépend entièrement de 3.8 ; côté infra, un worker Go
planifié comparant `station_tariffs` à des seuils utilisateurs.

### 3.10 Disponibilité temps réel (OCPI/OCPP)
**Valeur** : l'écart fonctionnel le plus important vs Chargemap/PlugShare/
Zap-Map (voir Mission 2).
**Complexité** : XL — aucune des sources actuellement ingérées
(IRVE/Electra/Izivia/Tesla/Freshmile/Fastned/Lidl/Ionity/eborn/Sowatt/
ChargeNow) n'expose de statut temps réel dans le pipeline existant ; il
faudrait soit intégrer des flux OCPI `Status`/`StatusSchedule` par
opérateur (accords d'accès variables selon l'opérateur), soit consommer
l'API nationale IRVE dynamique si elle expose un statut (à vérifier —
IRVE statique actuel n'en a pas). Nécessite un nouveau modèle de données
(`domain.Station` + colonne/table `station_status`), un système
d'actualisation (websocket ou polling par lot), et une UI de statut sur
les marqueurs.
**Impact UX** : élevé.
**Point d'entrée (si lancé)** : nouvelle table SQL `station_status`
(migration), nouveau module d'ingestion `backend/internal/ingestion/ocpi`,
endpoint `GET /stations/{id}/status` ou champ enrichi sur `/stations`,
puis `StationMarkers.jsx` (icône colorée par statut) et
`StationDetails.jsx`.

### 3.11 Planification de trajet avec étapes de recharge
**Valeur** : rapprocherait OpenCharge d'ABRP.
**Complexité** : XL — nécessite un moteur de calcul SoC/autonomie par
modèle de véhicule, un algorithme de routage multi-étapes, une base de
données de véhicules. Hors du périmètre naturel de la stack actuelle
(React + Leaflet + Go + PostGIS) sans un investissement conséquent.
**Impact UX** : élevé mais coût très disproportionné par rapport aux
autres items — à ne considérer qu'après la disponibilité temps réel et un
compte utilisateur.
**Point d'entrée** : nouveau service backend dédié, hors de
`backend/internal/api` existant ; probablement à isoler dans son propre
module pour ne pas alourdir l'API de lecture actuelle.

---

## Mission 4 — Feuille de route priorisée

### Phase 1 — Quick wins (< 1 semaine, aucune infra nouvelle)

| Item | Fichiers concernés |
|---|---|
| Ajouter un état d'erreur visible sur `StationMarkers` (fetch API) | `src/components/StationMarkers.jsx` |
| Distinguer "chargement" / "aucune borne" / "erreur" dans `StationMarkers` | `src/components/StationMarkers.jsx` |
| Traduire/simplifier les messages d'erreur de `StationDetails` | `src/components/StationDetails.jsx`, `src/api/stations.js` |
| `role="dialog"` + `aria-modal` + `Escape` + focus trap sur `OperatorFilter` et `FilterPanel` | `src/components/OperatorFilter.jsx`, `src/components/FilterPanel.jsx` |
| `aria-live="polite"` sur les zones de chargement/erreur | `src/components/StationDetails.jsx` |
| Factoriser le bloc dupliqué `HourlyPriceChart`/`TariffCost` en un seul sous-composant | `src/components/StationDetails.jsx` |
| Extraire un hook `useOperatorSources()` partagé | `src/components/OperatorFilter.jsx`, `src/components/OnboardingScreen.jsx` |
| Introduire des tokens CSS pour espacement/rayon/z-index | `src/index.css` |
| Toggle de mode sombre manuel (les tokens dark existent déjà) | `src/App.jsx`, `src/utils/storage.js`, `src/index.css` |

### Phase 2 — Moyen terme (1 à 4 semaines, faisable seul, sans nouvelle infra lourde)

| Item | Fichiers/endpoints concernés |
|---|---|
| Deep-link `/station/:id` + partage d'URL | `src/App.jsx`, `src/pages/MapPage.jsx`, `src/components/StationDetails.jsx` |
| Recherche par adresse (géocodage API Adresse gouv.fr) | nouveau `src/components/AddressSearch.jsx`, `src/components/FilterBar.jsx` |
| Clustering des marqueurs Leaflet (`react-leaflet-cluster`) | `src/components/StationMarkers.jsx`, `package.json` |
| Debounce sur pan/zoom | `src/components/StationMarkers.jsx` |
| `useMemo`/`useCallback` sur `MapPage`/`StationDetails` pour réduire les re-rendus | `src/pages/MapPage.jsx`, `src/components/StationDetails.jsx` |
| Extension de l'onboarding (explication des paliers) | `src/components/OnboardingScreen.jsx` |
| Filtres avancés étendus (multi-sélection connecteur, tri par prix) | `src/components/ConnectorFilter.jsx`, `src/components/FilterPanel.jsx` |
| Widget de prix embarquable (`/embed/station/:id`) | `src/App.jsx`, nouveau composant allégé réutilisant `StationDetails.jsx` |
| Mise en place de tests frontend (Vitest + React Testing Library, a minima sur `utils/pricing.js` et `StationMarkers.jsx`) | `frontend/web/package.json`, nouveaux fichiers `*.test.jsx` |

### Phase 3 — Long terme (> 1 mois, infrastructure additionnelle : auth, websockets, données temps réel)

| Item | Fichiers/endpoints concernés |
|---|---|
| Historique des prix par station (table append-only + endpoint) | migration SQL `backend/db/migrations/`, `backend/internal/api/stations.go`, nouveau `TariffHistoryChart.jsx` |
| Disponibilité temps réel (OCPI/OCPP par opérateur) | nouveau module `backend/internal/ingestion/ocpi`, migration `station_status`, `backend/internal/api/stations.go`, `src/components/StationMarkers.jsx`, `src/components/StationDetails.jsx` |
| Compte utilisateur (favoris) | backend : auth + tables `users`/`favorites` ; frontend : nouveau `AuthContext`, bouton favori sur `StationDetails.jsx` |
| Alertes de prix sur favoris | dépend du compte utilisateur ; worker Go planifié + canal de notification |
| Planification de trajet avec étapes de recharge | nouveau service dédié, hors de `backend/internal/api` existant |

---

## Contraintes respectées dans ces recommandations

- Aucune suggestion ne charge le dataset complet côté carte : le clustering
  (3.4/Phase 2) et le debounce s'appliquent *au-dessus* du pilotage par
  viewport existant, sans le remettre en cause.
- Aucune modification proposée aux modules d'ingestion existants
  (Electra/Izivia/Tesla/Freshmile/Fastned/Lidl/Ionity/eborn/Sowatt/
  ChargeNow) — la disponibilité temps réel (3.10) est décrite comme un
  nouveau module additif (`ingestion/ocpi`), pas une modification des
  sources fragiles actuelles.
- Les items de Phase 1 et la majorité de la Phase 2 ne nécessitent aucune
  authentification, conformément à la priorité demandée.
- Compatibilité Capacitor (Android/iOS) préservée : toutes les suggestions
  restent dans le shell web existant (`frontend/web/`), aucune ne
  nécessite d'API native Capacitor supplémentaire.
