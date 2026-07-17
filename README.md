# OpenCharge

OpenCharge visualise les bornes de recharge en France (puis en Europe) en
combinant :

- **IRVE** comme référentiel canonique des points de charge (données déjà
  consolidées par Etalab, chargées telles quelles puis enrichies) ;
- des **sources tarifaires externes** (Izivia, Electra, ...) corrélées à
  IRVE par géolocalisation (et, quand c'est fiable, par opérateur/enseigne) ;
- une **carte dynamique** qui ne charge jamais l'intégralité des ~132 000
  points de charge : tout est piloté par le viewport (bounding box).

## Architecture

```
opencharge/
  backend/
    cmd/
      opencharge-api/      # API HTTP (GET /stations, GET /stations/{id}, GET /sources)
      opencharge-ingest/   # CLI d'ingestion (irve, electra, izivia, tesla, freshmile, fastned, lidl, chargenow, all)
    internal/
      api/                 # handlers HTTP + DTOs JSON
      domain/               # modèle métier (Station, SourceStation, Tariff, Link)
      repository/           # accès PostgreSQL/PostGIS (pgx)
      ingestion/             # import + normalisation IRVE/Electra/Izivia/Tesla/Freshmile + corrélation
    db/migrations/          # migrations SQL (golang-migrate)
  frontend/
    web/                     # React + Leaflet, carte pilotée par bbox
      android/, ios/         # shells natifs Capacitor (générés, cf. section Mobile)
  docker-compose.yml        # Postgres+PostGIS, migrations, API, frontend web
```

### Modèle de données

- `stations` : le référentiel IRVE, à la granularité **point de charge**
  (`irve_id_pdc` est la clé d'upsert). Géométrie `Point(4326)` indexée GIST.
- `source_stations` : stations telles que vues par une source externe
  (Izivia, Electra), avant corrélation.
- `station_links` : corrélation `source_stations` → `stations`, avec une
  qualité de lien (`exact`, `by_geolocation`, `by_operator+name`, `manual`)
  et la distance en mètres.
- `station_tariffs` : tarifs normalisés attachés à une station IRVE, un par
  `(station, source, kind, plan)`.

La corrélation se fait via PostGIS (`ST_DWithin` / opérateur KNN `<->`) :
pour chaque station externe, on cherche la station IRVE la plus proche dans
un rayon configurable (150 m par défaut).

### Paliers tarifaires (`plan`)

Une source peut exposer plusieurs prix pour la même station selon le moyen
de paiement (ex. Electra : `public` sans l'appli, `app` avec l'appli,
`subscription` avec l'abonnement Smart). Les sources à palier unique
(Izivia, ...) utilisent le plan `standard`. Règle Electra actuellement
implémentée (`backend/internal/ingestion/electra.go`) :
- `public` : tarif fixe 0,64 €/kWh (constante non scrapée, à mettre à jour
  à la main si Electra change ce prix) ;
- `app` : le prix scrapé tel quel, éventuellement variable par plage horaire ;
- `subscription` : le prix `app`, chaque plage horaire réduite de 20 cts/kWh.

Tesla (`backend/internal/ingestion/tesla.go`) expose jusqu'à 4 paliers par
Supercharger, un par combinaison véhicule/abonnement issue de ses
`effectivePricebooks` : `tesla_member`, `tesla_public`,
`non_tesla_member`, `non_tesla_public`. Un éventuel frais de stationnement
(`feeType: "PARKING"`) pour la même combinaison alimente
`congestion_price_cents_per_min` du tarif correspondant plutôt que de
créer une ligne séparée.

Freshmile (`backend/internal/ingestion/freshmile.go`) peut exposer
plusieurs tarifs distincts par station, un par produit tarifaire
(`custom_ref`, ex. `normal-k-wh-interop-20`), chacun devenant son propre
`Plan` — avec un suffixe `:preferential` quand le tarif est marqué
`is_preferential` (abonnement/partenaire). Le prix €/kWh est extrait par
regex depuis le texte de description multilingue du tarif (FR en priorité,
sinon EN) ; un tarif dont le prix n'a pas pu être extrait est quand même
conservé (`energy_price_cents_per_kwh` à `null`, brut dans `extra.tariff`)
plutôt que d'être jeté, pour audit/futur raffinement de la regex.

Chaque tarif porte aussi `extra.windows`, la liste de ses plages horaires
avec leur propre prix (`{"startTime","endTime","energyPriceCentsPerKwh"}`) —
c'est cette donnée qui alimente le graphique horaire du frontend.

## Lancer l'environnement

```bash
cp .env.example .env   # ajuster APP_PORT si 8081 est déjà pris

docker compose up -d db migrate
# ou, sans docker : appliquer backend/db/migrations/*.sql avec golang-migrate
# migrate -path backend/db/migrations -database "$DATABASE_URL" up

# environnement complet (API + frontend, un seul port exposé) :
docker compose up -d db migrate api web
```

`web` (nginx) est le seul point d'entrée HTTP : il sert le frontend sur `/`
et fait reverse-proxy de `/api/*` vers le conteneur `api`, qui n'est jamais
publié sur l'hôte. Une fois lancé : `http://localhost:8081/` (frontend) et
`http://localhost:8081/api/stations?...` (API). `docker compose` lit `.env`
automatiquement : `DB_PORT` et `APP_PORT` contrôlent les ports exposés sur
l'hôte (défauts 5432/8081), et `POSTGRES_USER`/`POSTGRES_PASSWORD`/
`POSTGRES_DB`/`CORS_ORIGIN`/`VITE_API_BASE_URL` les autres réglages. Voir
`.env.example` pour la liste complète.

## Ingestion

```bash
cd backend
go run ./cmd/opencharge-ingest -source irve       # référentiel IRVE (GeoJSON)
go run ./cmd/opencharge-ingest -source electra    # stations + tarifs Electra, corrélation
go run ./cmd/opencharge-ingest -source izivia     # stations + tarifs Izivia, corrélation
go run ./cmd/opencharge-ingest -source tesla      # Superchargers Tesla, corrélation
go run ./cmd/opencharge-ingest -source freshmile  # stations + tarifs Freshmile, corrélation
go run ./cmd/opencharge-ingest -source fastned    # tarifs fixes Fastned sur les stations IRVE déjà taguées
go run ./cmd/opencharge-ingest -source lidl       # tarif fixe Lidl sur les stations IRVE déjà taguées
go run ./cmd/opencharge-ingest -source chargenow  # stations + tarifs ChargeNow (DCS), corrélation
go run ./cmd/opencharge-ingest -source all        # les huit, dans cet ordre
```

Variables utiles : `-dsn` (DSN Postgres, ou `DATABASE_URL`), `-irve-url`,
`-electra-url`, `-tesla-url`, `-freshmile-url`, `-chargenow-url`,
`-link-max-distance-m`.

IRVE doit toujours être ingéré en premier : c'est le référentiel contre
lequel Electra, Izivia, Tesla, Freshmile et ChargeNow sont corrélés, et
que Fastned/Lidl tagguent directement (leurs stations sont déjà les lignes
IRVE elles-mêmes, identifiées par `operator_name`/`enseigne` contenant
"fastned"/"lidl" — voir `backend/internal/ingestion/fastned.go` et
`lidl.go`).

**Fastned et Lidl n'ont pas d'API de tarifs publique scrapable** : leurs
tarifs (Fastned : 0,61 €/kWh standard, 0,43 €/kWh abonné ; Lidl : 0,29 €/kWh
unique, AC comme DC) sont des constantes fixes dans le code, à mettre à
jour manuellement si l'un de ces réseaux change ses prix. Aucune requête
réseau n'est faite pour ces deux runs.

**ChargeNow** (`backend/internal/ingestion/chargenow.go`) scanne toute la
France via son API de clusters/pools (`/api/map/v1/fr/query`, même logique
de subdivision par bounding box que Freshmile), puis interroge son API de
tarifs (`/tariffs/CHARGENOW_PRIME/prices`) pour chaque pool trouvé.
Particularité : l'API de découverte de ChargeNow ne renvoie ni le type de
connecteur ni la puissance de chaque point de charge (seulement son id),
alors que l'API de tarifs a besoin de `power_type`/`power` pour répondre —
l'ingester corrèle donc chaque pool avec la station IRVE la plus proche
*avant* même d'interroger les tarifs, uniquement pour lire ce
`connector_type`/`power_kw` déjà connu d'IRVE. Nécessite le header WAF
`rest-api-path` sur chaque requête (`clusters` pour `/query`, confirmé ;
`prices` pour `/tariffs/.../prices` est une supposition non vérifiée en
conditions réelles — voir le commentaire sur `doRequest` si ce endpoint se
met à échouer de façon suspecte).

**Freshmile scanne toute la France puis récupère le détail de chaque site
— découverte et récupération/écriture tournent en pipeline, pas en deux
phases séparées.** Le scan des tuiles `map-locations` (avec subdivision
récursive des clusters) est parallélisé sur 16 requêtes concurrentes, et
chaque emplacement découvert est immédiatement envoyé aux workers de
détail (8 par défaut, `FreshmileConfig.Workers`) puis écrit en base par
paquets de 200 au fur et à mesure — sans attendre la fin du scan complet.
Un arrêt en cours de route (Ctrl+C, `docker stop`, ou le flag `-timeout`)
n'efface donc pas le travail déjà fait : ce qui a été récupéré avant
l'arrêt reste écrit en base, et le run suivant repart pour compléter.

**Tesla nécessite Chromium — en mode "headed", pas headless.**
`tesla.com/api/findus/*` est protégé par un bot-mitigation (Akamai) qui
rejette toute requête HTTP classique quels que soient les en-têtes envoyés
— un vrai moteur de navigateur est nécessaire. Mais Akamai détecte
également Chrome lancé en `--headless` et le bloque de la même façon
(vérifié empiriquement : `Access Denied`) ; il faut donc un Chrome
"headed" classique, tourné vers un display (réel ou virtuel).
`backend/internal/ingestion/tesla.go` pilote donc Chromium via
[chromedp](https://github.com/chromedp/chromedp) plutôt que `net/http`
pour cette source uniquement, avec `headless=false`. En local sur un poste
avec écran, il faut un binaire Chrome/Chromium installé et accessible :
soit dans le `PATH` (`google-chrome`, `chromium`, ...), soit désigné
explicitement via `-tesla-chrome-path` ou la variable `TESLA_CHROME_PATH`.
Sur un serveur/CI sans display, il faut lancer la commande sous un display
virtuel, ex. `xvfb-run -a go run ./cmd/opencharge-ingest -source tesla`.
L'image Docker `ingest` (`backend/Dockerfile`, cible `ingest`) installe
Chromium + `xvfb`, positionne `TESLA_CHROME_PATH=/usr/bin/chromium`, et son
`ENTRYPOINT` est déjà enveloppé dans `xvfb-run` — rien de plus à faire
côté Docker. C'est pour ça que cette image est nettement plus lourde que
`api` (base `debian:bookworm-slim` + Chromium + xvfb, au lieu de
distroless).

## Tests

```bash
cd backend
go test ./internal/ingestion/...          # tests unitaires purs (parsing, normalisation)
TEST_DATABASE_URL=postgres://opencharge:opencharge@localhost:5432/opencharge?sslmode=disable \
  go test ./internal/... -p 1
```

Les tests de `internal/repository` et `internal/api` sont des tests
d'intégration : ils s'exécutent contre une vraie base Postgres/PostGIS
(migrations déjà appliquées) et sont **skippés automatiquement** si
`TEST_DATABASE_URL` (ou `DATABASE_URL`) n'est pas défini. Chaque test
tronque les tables au démarrage, donc `-p 1` est nécessaire pour éviter
que deux packages ne se marchent dessus sur la même base. C'est exactement
ce que fait la CI (`.github/workflows/backend.yml`), avec un service
`postgis/postgis` éphémère.

## API

```bash
cd backend
go run ./cmd/opencharge-api
```

### `GET /stations`

Query params : `bbox=minLng,minLat,maxLng,maxLat` (obligatoire),
`operator`, `hasTariffs`, `source`, `limit`, `offset`.

`source` accepte une liste de paires `source:plan` séparées par des virgules
(ex. `source=izivia:standard,electra:subscription`) ; une source sans `:plan`
est traitée comme `standard`. **Il ne filtre jamais les stations** : il
contrôle uniquement pour quelles paires (source, plan) `selectedSourcesPricing`
est calculé, afin que la carte puisse griser une station sans tarif pour la
sélection au lieu de la masquer.

```json
[
  {
    "id": "irve:FR-123456",
    "name": "Station X",
    "location": { "lat": 45.9123, "lng": 6.1213 },
    "operator": "Izivia",
    "address": { "city": "Annecy", "postalCode": "74000", "countryCode": "FR" },
    "connectors": [{ "kind": "CCS", "maxPowerKw": 150, "count": 1 }],
    "hasTariffs": true,
    "tariffSources": ["izivia", "electra"],
    "pricingSummary": { "ac_min_cents_per_kwh": 45, "dc_min_cents_per_kwh": 54 },
    "selectedSourcesPricing": { "dc_min_cents_per_kwh": 48 }
  }
]
```

`pricingSummary` est le prix minimum toutes sources confondues.
`selectedSourcesPricing` n'apparaît que si `source` était fourni : c'est le
prix minimum parmi uniquement les sources demandées (absent ou champs à
`null` si aucune des sources sélectionnées n'a de tarif pour cette station).

### `GET /stations/{id}`

`id` est l'identifiant IRVE, ex. `irve:FR-123456`. Retourne la station et
la liste de ses tarifs normalisés (un par source/kind), avec le texte brut
d'origine quand la source est textuelle (ex. Izivia). Le frontend calcule
côté client, à partir de cette liste complète, le prix par source
sélectionnée et le meilleur prix toutes sources — aucun paramètre `source`
n'est nécessaire ici.

### `GET /sources`

Retourne chaque source tarifaire actuellement ingérée avec ses paliers
disponibles, ex. :

```json
[
  { "id": "electra", "plans": ["app", "public", "subscription"] },
  { "id": "izivia", "plans": ["standard"] }
]
```

Le frontend construit son filtre d'opérateurs (et le sélecteur de palier
quand une source en a plusieurs) à partir de cet endpoint : aucune liste
n'est codée en dur côté client, une nouvelle source ou un nouveau palier
apparaît automatiquement dès qu'il est ingéré.

## Frontend

```bash
cd frontend/web
npm install
npm run dev
```

`VITE_API_BASE_URL` (défaut `http://localhost:8080`) pointe vers l'API
lancée en local (`go run ./cmd/opencharge-api`, cf. section API ci-dessus).
Ce mode (frontend et API lancés séparément, hors Docker) cible l'API
directement par une URL absolue ; il ne passe pas par le reverse-proxy
nginx décrit dans la section Docker ci-dessous.

### Pages et parcours

- **Carte (`/`)** : la carte (Leaflet) recharge `GET /stations` à chaque
  déplacement/zoom, jamais le dataset complet ; en dessous du zoom 10, un
  message invite à zoomer plutôt que de charger des milliers de marqueurs.
  - **Filtre opérateurs** : liste à cocher, avec recherche, alimentée par
    `GET /sources` (aucune liste codée en dur — une nouvelle source
    ingérée apparaît automatiquement). Sélection multiple : le prix affiché
    sur un marqueur est le moins cher parmi les sources cochées. Aucune
    sélection = comportement par défaut (prix le moins cher toutes sources
    confondues). Quand une source a plusieurs paliers tarifaires (ex.
    Electra : sans l'appli / avec l'appli / abonné), un petit sélecteur
    apparaît sous son libellé pour choisir celui qui s'applique à
    l'utilisateur — entièrement piloté par `GET /sources`, aucun palier
    codé en dur.
  - **Mode de prix** : bascule entre €/kWh et prix pour une recharge d'un
    nombre de kWh configurable (calcul client, aucun appel réseau
    supplémentaire au changement de mode).
  - Une station sans tarif pour la sélection reste visible sur la carte,
    grisée sans prix (jamais masquée).
  - Clic sur une station : panneau de détail avec le prix par source et
    palier sélectionnés, le meilleur prix toutes sources en comparaison si
    différent, et la liste complète des tarifs pour audit. Un tarif dont
    le prix varie dans la journée (plusieurs plages horaires) s'affiche
    sous forme de petit graphique en barres prix/heure plutôt qu'un prix
    unique.
- **À propos (`/about`)** : sources de données, méthodologie de
  corrélation, limites de fiabilité des prix affichés.

### Docker

```bash
docker compose up -d db migrate api web
```

Le `Dockerfile` (`frontend/web/Dockerfile`) est un build multi-stage
`node:20-alpine` → `nginx:alpine`. `VITE_API_BASE_URL` est un argument de
build (les variables Vite sont figées à la compilation, pas au runtime) :
par défaut `/api`, un chemin relatif résolu contre l'origine de la page —
c'est ce même conteneur `web` (nginx) qui sert le frontend sur `/` **et**
fait reverse-proxy de `/api/*` vers le conteneur `api` (`frontend/web/nginx.conf`),
qui lui n'est jamais exposé sur l'hôte. Un seul port est donc publié :
`http://localhost:${APP_PORT:-8081}/` pour le frontend,
`http://localhost:${APP_PORT:-8081}/api/*` pour l'API.

### Mobile (Capacitor)

Le web app est encapsulé tel quel dans une coquille native iOS/Android via
Capacitor — aucune logique dupliquée. Config et projets natifs
(`android/`, `ios/`) vivent dans `frontend/web/` (convention standard de
l'outil : Capacitor a besoin d'être co-localisé avec `package.json`).

```bash
cd frontend/web
npm run cap:sync      # build web + copie dans android/ et ios/
npm run cap:android   # + ouvre Android Studio
npm run cap:ios       # + ouvre Xcode (macOS uniquement)
```

Build et publication sur les stores nécessitent Android Studio/Gradle ou
Xcode en local — hors périmètre de ce dépôt/CI pour l'instant.

## Sources

- IRVE (Etalab, consolidé) : GeoJSON republié par transport.data.gouv.fr
- Electra : `https://stations.go-electra.com/stations.js`
- Izivia : API front `https://fronts-map.izivia.com/api` (markers, détails,
  tarifs), scannée par grille sur la métropole
- Tesla : API front `https://www.tesla.com/api/findus/*` (liste des sites,
  détails/tarifs par Supercharger)
- Freshmile : API carto `https://prod-driver-api.freshmile.com/charge/api/v2`
  (`map-locations` en clusters/points, `locations/{id}` pour le détail),
  clusters résolus par subdivision récursive de bbox jusqu'aux points
  unitaires
