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
      opencharge-api/      # API HTTP (GET /stations, GET /stations/{id})
      opencharge-ingest/   # CLI d'ingestion (irve, electra, izivia, all)
    internal/
      api/                 # handlers HTTP + DTOs JSON
      domain/               # modèle métier (Station, SourceStation, Tariff, Link)
      repository/           # accès PostgreSQL/PostGIS (pgx)
      ingestion/             # import + normalisation IRVE/Electra/Izivia + corrélation
    db/migrations/          # migrations SQL (golang-migrate)
  frontend/web/             # React + Leaflet, carte pilotée par bbox
  docker-compose.yml        # Postgres+PostGIS, migrations, API
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
    "pricingSummary": { "ac_min_cents_per_kwh": 45, "dc_min_cents_per_kwh": 54 }
  }
]
```

### `GET /stations/{id}`

`id` est l'identifiant IRVE, ex. `irve:FR-123456`. Retourne la station et
la liste de ses tarifs normalisés (un par source/kind), avec le texte brut
d'origine quand la source est textuelle (ex. Izivia).

## Frontend

```bash
cd frontend/web
npm install
npm run dev
```

`VITE_API_BASE_URL` (défaut `http://localhost:8080`) pointe vers l'API. La
carte (Leaflet) recharge `GET /stations` à chaque déplacement/zoom, jamais
le dataset complet.

## Sources

- IRVE (Etalab, consolidé) : GeoJSON republié par transport.data.gouv.fr
- Electra : `https://stations.go-electra.com/stations.js`
- Izivia : API front `https://fronts-map.izivia.com/api` (markers, détails,
  tarifs), scannée par grille sur la métropole
