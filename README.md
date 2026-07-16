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
      opencharge-ingest/   # CLI d'ingestion (irve, electra, izivia, all)
    internal/
      api/                 # handlers HTTP + DTOs JSON
      domain/               # modèle métier (Station, SourceStation, Tariff, Link)
      repository/           # accès PostgreSQL/PostGIS (pgx)
      ingestion/             # import + normalisation IRVE/Electra/Izivia + corrélation
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
  `(station, source, kind)`.

La corrélation se fait via PostGIS (`ST_DWithin` / opérateur KNN `<->`) :
pour chaque station externe, on cherche la station IRVE la plus proche dans
un rayon configurable (150 m par défaut).

## Lancer l'environnement

```bash
docker compose up -d db migrate
# ou, sans docker : appliquer backend/db/migrations/*.sql avec golang-migrate
# migrate -path backend/db/migrations -database "$DATABASE_URL" up

# environnement complet (API + frontend web servi par nginx) :
docker compose up -d db migrate api web
```

## Ingestion

```bash
cd backend
go run ./cmd/opencharge-ingest -source irve      # référentiel IRVE (GeoJSON)
go run ./cmd/opencharge-ingest -source electra   # stations + tarifs Electra, corrélation
go run ./cmd/opencharge-ingest -source izivia    # stations + tarifs Izivia, corrélation
go run ./cmd/opencharge-ingest -source all       # les trois, dans cet ordre
```

Variables utiles : `-dsn` (DSN Postgres, ou `DATABASE_URL`), `-irve-url`,
`-electra-url`, `-link-max-distance-m`.

IRVE doit toujours être ingéré en premier : c'est le référentiel contre
lequel Electra et Izivia sont corrélés.

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

`source` accepte une liste séparée par des virgules (ex.
`source=izivia,electra`). **Il ne filtre jamais les stations** : il contrôle
uniquement pour quelles sources `selectedSourcesPricing` est calculé, afin
que la carte puisse griser une station sans tarif pour la sélection au lieu
de la masquer.

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

Retourne la liste des sources tarifaires actuellement ingérées, ex.
`["electra", "izivia"]`. Le frontend construit son filtre d'opérateurs à
partir de cet endpoint : aucune liste n'est codée en dur côté client, une
nouvelle source apparaît automatiquement dès qu'elle est ingérée.

## Frontend

```bash
cd frontend/web
npm install
npm run dev
```

`VITE_API_BASE_URL` (défaut `http://localhost:8080`) pointe vers l'API.

### Pages et parcours

- **Carte (`/`)** : la carte (Leaflet) recharge `GET /stations` à chaque
  déplacement/zoom, jamais le dataset complet ; en dessous du zoom 10, un
  message invite à zoomer plutôt que de charger des milliers de marqueurs.
  - **Filtre opérateurs** : liste à cocher, avec recherche, alimentée par
    `GET /sources` (aucune liste codée en dur — une nouvelle source
    ingérée apparaît automatiquement). Sélection multiple : le prix affiché
    sur un marqueur est le moins cher parmi les sources cochées. Aucune
    sélection = comportement par défaut (prix le moins cher toutes sources
    confondues).
  - **Mode de prix** : bascule entre €/kWh et prix pour une recharge d'un
    nombre de kWh configurable (calcul client, aucun appel réseau
    supplémentaire au changement de mode).
  - Une station sans tarif pour la sélection reste visible sur la carte,
    grisée sans prix (jamais masquée).
  - Clic sur une station : panneau de détail avec le prix par source
    sélectionnée, le meilleur prix toutes sources en comparaison si
    différent, et la liste complète des tarifs pour audit.
- **À propos (`/about`)** : sources de données, méthodologie de
  corrélation, limites de fiabilité des prix affichés.

### Docker

```bash
docker compose build web
docker compose up -d web
```

Le `Dockerfile` (`frontend/web/Dockerfile`) est un build multi-stage
`node:20-alpine` → `nginx:alpine`. `VITE_API_BASE_URL` est un argument de
build (les variables Vite sont figées à la compilation, pas au runtime) :
adaptez-le à l'URL publique de l'API en production.

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
