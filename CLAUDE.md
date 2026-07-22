# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

### Backend (`cd backend`, Go 1.25)

```bash
go build ./...
go vet ./...
gofmt -l .                      # must be empty; CI fails otherwise
golangci-lint run ./...         # v2.5.0 in CI (golangci-lint-action@v7)

go test ./internal/ingestion/...          # pure unit tests, no DB needed
go test ./internal/... -p 1               # single test file/pkg needs TEST_DATABASE_URL too
go test ./internal/repository/... -run TestStationRepository_ListByBBox -v   # single test

# internal/repository and internal/api have integration tests against real
# Postgres/PostGIS; they self-skip if neither var is set:
TEST_DATABASE_URL=postgres://opencharge:opencharge@localhost:5432/opencharge?sslmode=disable go test ./internal/... -p 1 -race
```

`-p 1` is mandatory whenever repository/api tests run: each test truncates
shared tables on setup, so packages must not run concurrently against the
same database. Migrations must already be applied (`golang-migrate`, see
`db/migrations/`).

```bash
go run ./cmd/opencharge-api                        # HTTP API, :8080 by default
go run ./cmd/opencharge-ingest -source <name>       # see "Ingestion" below
```

### Frontend (`cd frontend/web`, Node 20)

```bash
npm install
npm run dev      # vite dev server, defaults VITE_API_BASE_URL to http://localhost:8080
npm run lint     # eslint src --ext .js,.jsx
npm test         # vitest run (non-watch)
npm run build
```

### Full stack via Docker

```bash
cp .env.example .env
docker compose up -d db migrate api web   # single exposed port: nginx (web) reverse-proxies /api/* to api
```

## Architecture

OpenCharge shows EV charging stations + prices on a map by combining:
IRVE (France's canonical charge-point referential, ~132k points, loaded
as-is) with external tariff sources correlated to it geographically.

### Data model (`backend/internal/domain`, PostgreSQL/PostGIS)

- `stations` — the IRVE referential, **one row per physical connector**
  (`irve_id_pdc` is the upsert key), not per site. A site with a T2 and a
  CCS plug is two rows at the same coordinates. `irve_id_station` groups
  rows belonging to the same site when populated.
- `source_stations` — an external source's own view of a station, before
  correlation (raw payload kept in `Raw`).
- `station_links` — the `source_stations` → `stations` correlation
  (quality: `exact`, `by_geolocation`, `by_operator+name`, `manual`; plus
  distance in meters).
- `station_tariffs` — normalized tariffs attached to an IRVE station, one
  per `(station, source, kind, plan)`. `kind` is `ac`/`dc`/`mixed`.

Correlation for geo-scanned sources uses PostGIS (`ST_DWithin` / `<->` KNN
operator): nearest IRVE station within `-link-max-distance-m` (150m
default). See `backend/internal/ingestion/linking.go`'s
`writeSourceStationChunk` and `backend/internal/repository/link_repo.go`'s
`FindNearestStationsForKind`.

**Same-kind co-location gotcha**: a single physical site can have two IRVE
rows of the *same* kind (e.g. a CHAdeMO and a CCS row, both `dc`).
Correlation resolves nearest-of-kind, which can only pick one winner per
`(source station, kind)` — so when a source's own tariffs already
distinguish connector type (`domain.StationTariff.ConnectorType`, today
only Freshmile sets this), correlation is additionally scoped per
`(kind, connectorType)`, not just kind, or one of the two co-located rows
silently gets nothing from that source. `FindNearestStationsForKind`'s
`connectorType` param handles this (exact match outranks everything else,
including the power-based tie-break used when the source instead exposes
a target power — see Izivia).

**Connector-type vocabulary** (`backend/internal/domain/connector.go`:
`ConnectorTypeCCS/CHAdeMO/T2/EF`) is the single source of truth in Go, but
is duplicated by hand in three other places that can't share Go code:
`frontend/web/src/utils/pricing.js`'s own AC/DC sets, `ingestion/izivia.go`'s
`iziviaConnectorKind` (classifies straight from Izivia's own raw strings,
never translated to IRVE's vocabulary), and the SQL fragment
`candidateKindFilterFragment` in `link_repo.go`. Changing the vocabulary
means updating all four.

### Tariff plans (`plan` column)

A source can expose several prices for the same station by payment method.
Single-tier sources (Izivia, Freshmile, ...) use `standard`. Electra has
`public` (fixed 0.64€/kWh constant)/`app` (scraped, can vary by time
window)/`subscription` (app price − 0.20€/kWh flat discount). Tesla has up
to 4 plans per Supercharger from its own `effectivePricebooks`. A tariff
carries `extra.windows` (per-time-of-day price breakdown) when it varies
during the day — that's what feeds the frontend's hourly bar chart
(`current_window_price` SQL function, `utils/pricing.js#currentEnergyPriceCentsPerKWh`
on the client side — both must independently pick the window covering
"now", kept in sync by hand same as the connector vocabulary).

### Two families of ingesters (`backend/internal/ingestion`)

1. **Geo-scan + correlate**: Electra, Izivia, Tesla, Freshmile, ChargeNow.
   Each fetches its own station list/prices from a scraped or private API
   and correlates to IRVE via `linking.go`. Freshmile and ChargeNow stream
   discover→correlate→price→write incrementally in fixed-size batches
   (see `freshmile.go`'s `runPipeline`/ChargeNow's `runPipeline`) rather
   than three all-or-nothing phases, so a run cut short (Ctrl+C,
   `docker stop`, idle-timeout) keeps whatever batch already completed
   instead of losing everything gathered so far.
2. **Direct-tag, fixed price**: Fastned, Lidl, Ionity, eborn, Sowatt. Their
   stations are already IRVE rows (`ListByOperatorLike`, matched by
   `operator_name`/`enseigne`) — no external station list, no
   `source_stations`/`station_links` involved, no network request at all.
   Prices are hardcoded constants, updated by hand when a network changes
   its rates. eborn is the one exception with actual per-kind/per-power
   pricing tiers instead of one flat number.

IRVE must always be ingested first — everything else correlates against
or tags it.

### Idle watchdog, not a flat timeout (`ingestion/idle.go`)

Izivia/Tesla/Freshmile/ChargeNow have no fixed wall-clock timeout —
scanning all of France can legitimately take over an hour. `-idle-timeout`
(default 5m) instead tracks time since the *last successful request*
across the whole run; it only aborts when nothing has succeeded in that
window (`context.Cause` surfaces the reason, e.g. "no successful request
in the last 5m0s"). A run stopped this way (or by Ctrl+C) never triggers
`repository.SweepStaleSourceData` — only a run that finished normally is
trusted to declare "anything not seen this run is actually gone".

### Failure tracking + targeted retry (`ingestion/failures.go`)

The same four fan-out sources record every permanently-failed request
(HTTP retries exhausted) to `<failed-dir>/<source>.json` (default
`ingest-failures/`, env `INGEST_FAILED_DIR`), with enough params to replay
it directly. `-retry-failed` replays only those, without a full rescan;
the file is rewritten each pass with whatever's still failing, deleted
once everything converges.

### ChargeNow's WAF (`ingestion/chargenow.go`) — hard-won, don't re-break

`/api/map/v1/fr/...` is a single facade that dispatches to different
internal microservices purely by the `rest-api-path` header value
(confirmed against real traffic): `clusters` for bbox discovery,
`charge-points` for live status, `pools` for lookup-by-id — but
`/tariffs/CHARGENOW_PRIME/prices` takes **no such header at all**; sending
one (even a plausible guess) silently misroutes it to the wrong
microservice and breaks every price fetch. Separately, and more
importantly: the WAF blocks on request *volume*, not content — a burst of
concurrent requests (old `chargenowScanWorkers: 16`) or a large batched
price request (dozens of items; real browser traffic never sends more
than 1-3) gets the whole run's IP rejected outright (403 HTML page), even
though the identical request succeeds instantly in isolation. Every
request (discovery and pricing) is paced through one shared rate limiter
(`chargenowMinRequestInterval`, 150ms — confirmed safe in production), and
price requests are kept deliberately small (`chargenowPriceBatchSize`, 3
items) — decoupled from `chargenowPoolBatchSize` (100), which is purely
about our own DB-write batching and has nothing to do with what
ChargeNow's API tolerates per request.

### Tesla needs a real, non-headless browser

`tesla.com/api/findus/*` is behind Akamai bot-mitigation that also
detects and blocks headless Chrome. `ingestion/tesla.go` drives Chromium
via `chromedp` with `headless=false`, so it needs an actual display (real
or `xvfb-run`). The `ingest` Docker image already wraps its entrypoint in
`xvfb-run` and installs Chromium — no extra setup there; locally, either
have a display or run under `xvfb-run -a`.

### API (`backend/internal/api`)

Three-ish endpoints: `GET /stations` (bbox-required, list+aggregate
pricing — see query params in `stations.go`'s doc comment, including the
recharge-mode total-cost filter via `chargeKWh`/`chargeMinutes`), `GET
/stations/{id}` (full tariff detail for one connector), `GET /sources`
(drives the frontend's operator filter, no hardcoded list). `GET
/freshmile/availability/{locationId}` (`api/freshmile.go`) proxies
Freshmile's live per-EVSE status server-side, since a direct
browser→Freshmile call is CORS-blocked in production — returns
availability both site-wide and broken down per connector type, since one
physical EVSE's `is_available` flag can cover several connectors at once
(e.g. a Type 2 + a domestic socket sharing one plug).

### Frontend (`frontend/web/src`, React + Leaflet)

The map is entirely bbox-driven (`GET /stations` refetches on every
pan/zoom; nothing loads the full dataset; below zoom 10 it just asks the
user to zoom in — `StationMarkers.jsx`'s `MIN_ZOOM_TO_LOAD`). Since
`stations` rows are per-connector, `utils/stationGrouping.js` groups
same-coordinate rows client-side into one site: one marker (priced at
whichever connector is cheapest, or cheapest among selected sources when a
filter is active), one detail card with a "price per connector" section
per connector (`StationDetails.jsx`'s `ConnectorPriceSection`). Selecting
specific sources never filters stations out — it only changes what
`selectedSourcesPricing` is computed from, so an unpriced-for-selection
station shows grayed out rather than disappearing.

## Testing conventions

- Backend integration tests (repository, api, and some ingestion tests)
  need `TEST_DATABASE_URL` or `DATABASE_URL` pointing at a real
  Postgres/PostGIS instance with migrations applied; they silently skip
  otherwise. Always run with `-p 1`.
- Frontend has no component tests, only pure-logic unit tests
  (`utils/*.test.js`) via Vitest.
