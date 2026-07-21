package ingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"opencharge/internal/domain"
	"opencharge/internal/repository"
)

// DefaultFreshmileBaseURL is Freshmile's map/charging-location API.
const DefaultFreshmileBaseURL = "https://prod-driver-api.freshmile.com/charge/api/v2"

// freshmileZoom is passed to map-locations on every tile request. Since we
// control tile size ourselves via bbox subdivision, the exact value mostly
// just needs to be "detailed enough" for the API to return real points
// rather than an even coarser cluster than our bbox implies.
const freshmileZoom = 14

// freshmileMaxSubdivisionDepth bounds cluster subdivision: a cluster that's
// still a cluster after this many splits is dropped rather than recursed
// into forever (e.g. many stations occupying the exact same coordinates).
const freshmileMaxSubdivisionDepth = 8

// freshmileMaxTilesVisited is a hard safety cap on the total number of
// map-locations calls in one Run, in case pathological cluster geometry
// causes an explosion in sibling tiles. It must stay well above what a real
// full-France scan needs: freshmileMaxSubdivisionDepth (8) and
// subdivideBBox's strict quadrant-halving already bound the worst case per
// initial grid tile to a fixed geometric series (sum of 4^0..4^8 ≈ 87k), so
// this counter's only job is to catch true runaway growth, not to throttle
// legitimate coverage. It used to be 20000 — a single shared counter across
// every initial grid tile — which meant dense clusters (e.g. Île-de-France)
// exhausted the whole budget through deep subdivision before goroutines for
// other, later-scheduled regions of France got to visit a single tile,
// silently truncating national coverage rather than slowing it down.
const freshmileMaxTilesVisited = 500000

// freshmileScanWorkers bounds how many map-locations requests the
// discovery scan (fetchAllLocationIDs) has in flight at once. The scan is
// a tree of tile/cluster-subdivision fetches, not a flat list, so it's
// parallelized with a semaphore-bounded recursive fan-out rather than a
// simple worker-pool-over-a-channel like the detail-fetch phase.
const freshmileScanWorkers = 16

// freshmileFlushTimeout bounds how long a single batch write is allowed
// to take. It's deliberately generous (this isn't a "how long is a
// healthy write" tuning knob, just a backstop against a truly hung
// query) since writeResults always runs the write decoupled from the
// run's own ctx — see there for why.
const freshmileFlushTimeout = 2 * time.Minute

// freshmileProgressLogInterval controls how often scanLocationIDs logs
// progress while it scans, on a wall-clock ticker rather than a tile
// count — the discovery phase's tile count can advance in bursts (a big
// cluster subdivides into many quick sibling requests) or stall for a
// while (a dense area recursing deeply, or several workers retrying at
// once), so a fixed "every N tiles" interval leaves long silent stretches
// where a slow-but-healthy run is indistinguishable from a hung one. A
// wall-clock heartbeat guarantees a log line on a predictable cadence
// regardless of how the tile count is moving.
const freshmileProgressLogInterval = 10 * time.Second

// freshmileMinBBoxDegrees is the minimum width/height a map-locations
// query bbox is padded out to (~100m at French latitudes). Freshmile's API
// returns a 500 for a zero-area bbox, which happens in practice when a
// cluster's own reported bbox.sw/bbox.ne collapse to the same coordinate
// on one axis (observed: several stations at the exact same longitude).
const freshmileMinBBoxDegrees = 0.001

// padDegenerateBBox widens bbox around its center on any axis narrower
// than freshmileMinBBoxDegrees, leaving a normal-sized bbox untouched.
func padDegenerateBBox(b freshmileBBox) freshmileBBox {
	if b.MaxLng-b.MinLng < freshmileMinBBoxDegrees {
		centerLng := (b.MinLng + b.MaxLng) / 2
		b.MinLng = centerLng - freshmileMinBBoxDegrees/2
		b.MaxLng = centerLng + freshmileMinBBoxDegrees/2
	}
	if b.MaxLat-b.MinLat < freshmileMinBBoxDegrees {
		centerLat := (b.MinLat + b.MaxLat) / 2
		b.MinLat = centerLat - freshmileMinBBoxDegrees/2
		b.MaxLat = centerLat + freshmileMinBBoxDegrees/2
	}
	return b
}

// FreshmileConfig tunes the per-location detail-fetch worker pool.
type FreshmileConfig struct {
	Workers int
}

// freshmileDefaultWorkers: 24. Each location costs one HTTP round trip
// (GET /locations/{id}, unlike Izivia's two), so this is the same kind of
// I/O-bound single-host fan-out as Izivia's worker count, just tuned down
// a bit given how retry-heavy this API already is (see defaultMaxRetries
// in common.go) — too high a worker count would only mean more requests
// piling up in backoff at once against the same flaky gateway.
const freshmileDefaultWorkers = 24

func DefaultFreshmileConfig() FreshmileConfig {
	return FreshmileConfig{Workers: freshmileDefaultWorkers}
}

type FreshmileIngester struct {
	Pool             *pgxpool.Pool
	SourceStations   *repository.SourceStationRepository
	Tariffs          *repository.TariffRepository
	Links            *repository.LinkRepository
	BaseURL          string
	Config           FreshmileConfig
	MaxLinkDistanceM float64
	// Failures, when set, records every request that failed for good
	// (location detail fetch, map-locations tile scan) so a later
	// -retry-failed pass can replay just those — see FailureLog.
	Failures *FailureLog
	// IdleTimeout bounds how long Run/RetryFailed goes without a single
	// successful request before giving up on the whole run — see
	// idleWatchdog. Defaults to DefaultIdleTimeout; <= 0 disables it.
	IdleTimeout  time.Duration
	idle         *idleWatchdog // set by Run/RetryFailed, read by doGet
	client       *http.Client
	retryBackoff time.Duration // unexported: overridden by tests to keep them fast
}

func NewFreshmileIngester(pool *pgxpool.Pool, sourceStations *repository.SourceStationRepository, tariffs *repository.TariffRepository, links *repository.LinkRepository, baseURL string, cfg FreshmileConfig) *FreshmileIngester {
	if baseURL == "" {
		baseURL = DefaultFreshmileBaseURL
	}
	workers := effectiveWorkers(cfg.Workers, freshmileDefaultWorkers)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = max(workers, freshmileScanWorkers)
	return &FreshmileIngester{
		Pool:             pool,
		SourceStations:   sourceStations,
		Tariffs:          tariffs,
		Links:            links,
		BaseURL:          baseURL,
		Config:           cfg,
		MaxLinkDistanceM: DefaultLinkMaxDistanceMeters,
		IdleTimeout:      DefaultIdleTimeout,
		client:           &http.Client{Timeout: 60 * time.Second, Transport: transport},
		retryBackoff:     2 * time.Second,
	}
}

// startIdleWatchdog wraps ctx with this ingester's idle watchdog (see
// idleWatchdog) and records it on ing.idle so doGet can Ping it. The
// returned cancel must be deferred immediately by the caller.
func (ing *FreshmileIngester) startIdleWatchdog(ctx context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel, watchdog := startIdleWatchdog(ctx, ing.IdleTimeout)
	ing.idle = watchdog
	return ctx, cancel
}

// Run discovers Freshmile locations (recursively resolving map clusters
// down to individual points) and fetches/writes them in a single streaming
// pipeline, rather than discovering the whole of France before fetching
// anything: the discovery scan feeds location IDs onto idCh as it finds
// them, detail-fetch workers consume idCh concurrently, and a single
// writer batches results onto the database via writeSourceStationChunk as
// they arrive. That way a run stopped partway through (ctx canceled,
// timeout hit) keeps whatever's already been written instead of losing
// everything — discovery alone used to take the vast majority of a run's
// wall-clock time with nothing durable to show for it yet.
func (ing *FreshmileIngester) Run(ctx context.Context) (int, error) {
	defer ing.Failures.saveAndLog()
	ctx, cancelIdle := ing.startIdleWatchdog(ctx)
	defer cancelIdle()
	runStart := time.Now()

	processed, err := ing.runPipeline(ctx, ing.scanLocationIDs)

	log.Printf("freshmile: done, %d locations processed", processed)

	// Only sweep after a fully successful run (see repository.SweepStaleSourceData).
	// processed > 0 guards against a scan that silently found/fetched
	// nothing (e.g. Freshmile's map-locations API down for the whole run)
	// looking identical to "France has zero stations" and wiping the
	// entire known dataset — see the same guard in izivia.go for the
	// production incident that motivated it.
	if err == nil && processed > 0 {
		if sweepErr := repository.SweepStaleSourceData(ctx, ing.Pool, "freshmile", runStart.Add(-repository.StaleSourceDataGracePeriod)); sweepErr != nil {
			return processed, sweepErr
		}
	}
	return processed, err
}

// RetryFailed replays only the requests a previous run recorded as failed
// (see FailureLog): failed map tiles are re-scanned (re-discovering and
// fetching whatever locations they cover) and failed location details are
// re-fetched directly by id. Requests that fail again are re-recorded, so
// the failure file always reflects what's still outstanding. No stale-data
// sweep happens here: a retry pass only touches the previously-failed
// subset, so most known locations legitimately go un-refreshed.
func (ing *FreshmileIngester) RetryFailed(ctx context.Context, failures []FailedFetch) (int, error) {
	defer ing.Failures.saveAndLog()
	ctx, cancelIdle := ing.startIdleWatchdog(ctx)
	defer cancelIdle()

	var ids []int
	var tiles []freshmileBBox
	seenIDs := map[int]struct{}{}
	for _, f := range failures {
		switch f.Kind {
		case failKindFreshmileLocation:
			var params struct {
				ID int `json:"id"`
			}
			if err := json.Unmarshal(f.Params, &params); err != nil || params.ID == 0 {
				log.Printf("freshmile: skipping unreadable %s failure: %v", f.Kind, err)
				continue
			}
			if _, dup := seenIDs[params.ID]; dup {
				continue
			}
			seenIDs[params.ID] = struct{}{}
			ids = append(ids, params.ID)
		case failKindFreshmileTile:
			var bbox freshmileBBox
			if err := json.Unmarshal(f.Params, &bbox); err != nil {
				log.Printf("freshmile: skipping unreadable %s failure: %v", f.Kind, err)
				continue
			}
			tiles = append(tiles, bbox)
		default:
			log.Printf("freshmile: skipping failure of unknown kind %q", f.Kind)
		}
	}

	log.Printf("freshmile: retrying %d locations and %d map tiles from %d recorded failure(s)", len(ids), len(tiles), len(failures))

	// A location fed directly by id may also be re-discovered by a retried
	// tile's scan; the resulting duplicate fetch is harmless (the write
	// path upserts) and rare enough not to be worth threading the seen-set
	// across the two feeds.
	feed := func(feedCtx context.Context, idCh chan<- int) {
		for _, id := range ids {
			select {
			case idCh <- id:
			case <-feedCtx.Done():
				return
			}
		}
		if len(tiles) > 0 {
			ing.scanBBoxes(feedCtx, idCh, tiles)
		}
	}
	processed, err := ing.runPipeline(ctx, feed)
	log.Printf("freshmile: retry done, %d locations processed", processed)
	return processed, err
}

// runPipeline runs the shared discover→fetch→write pipeline behind both a
// full Run and a RetryFailed pass: feed streams location IDs onto idCh
// (a full run's feed is scanLocationIDs; a retry pass feeds recorded IDs
// and re-scans failed tiles), detail-fetch workers consume them, and a
// single writer batches results onto the database.
func (ing *FreshmileIngester) runPipeline(ctx context.Context, feed func(ctx context.Context, idCh chan<- int)) (int, error) {
	// pipelineCtx (not ctx directly) governs the feed and the
	// detail-fetch workers, so that once writeResults returns — whether
	// because the pipeline finished normally or because a flush failed —
	// we can force those two stages to unwind via cancelPipeline below.
	// Without this, a write error ends writeResults early while workers
	// are still blocked sending to resultsCh (nobody left to read it) and
	// scan goroutines are still blocked sending to idCh (workers stopped
	// draining it): neither side has any reason tied to ctx alone to give
	// up, since ctx itself hasn't been canceled — only the write side
	// has stopped consuming. That leaks every remaining goroutine forever
	// and, worse, wedges Run() itself on scanWG.Wait() below (scanLocationIDs
	// can never return while its goroutines are stuck), so the real error
	// from flush() never makes it back to the caller to be logged.
	pipelineCtx, cancelPipeline := context.WithCancel(ctx)
	defer cancelPipeline()

	// Sized well above ingestionBulkChunkSize so a slow write-side flush
	// (up to freshmileFlushTimeout) doesn't immediately start backing up
	// into the scan goroutines trying to send discovered IDs (see
	// scanLocationIDs) — it's slack, not a correctness requirement.
	idCh := make(chan int, 500)
	resultsCh := make(chan normalizedSourceStation)

	var fetchedOK, fetchFailed int64

	var scanWG sync.WaitGroup
	scanWG.Add(1)
	go func() {
		defer scanWG.Done()
		defer close(idCh)
		feed(pipelineCtx, idCh)
	}()

	var workerWG sync.WaitGroup
	worker := func() {
		defer workerWG.Done()
		for id := range idCh {
			item, ok, err := ing.fetchAndNormalizeLocation(pipelineCtx, id)
			if err != nil {
				atomic.AddInt64(&fetchFailed, 1)
				log.Printf("freshmile: location %d failed: %v", id, err)
				// Not recorded when the pipeline itself is shutting down:
				// those locations didn't fail on their own — see the same
				// guard in izivia.go's worker.
				if pipelineCtx.Err() == nil {
					ing.Failures.Record(failKindFreshmileLocation, fmt.Sprintf("%s/locations/%d", ing.BaseURL, id), map[string]int{"id": id}, err)
				}
				continue
			}
			if !ok {
				continue
			}
			atomic.AddInt64(&fetchedOK, 1)
			select {
			case resultsCh <- item:
			case <-pipelineCtx.Done():
				return
			}
		}
	}

	workers := effectiveWorkers(ing.Config.Workers, freshmileDefaultWorkers)
	for i := 0; i < workers; i++ {
		workerWG.Add(1)
		go worker()
	}
	go func() {
		workerWG.Wait()
		close(resultsCh)
	}()

	// writeResults only logs when a batch actually flushes, so a fetch
	// phase that's just slow (or backed up behind a flaky API) looks
	// identical to a hung one in between flushes — this heartbeat gives a
	// wall-clock signal regardless of whether anything's flushed yet.
	heartbeatDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(freshmileProgressLogInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				log.Printf("freshmile: fetch/write in progress, %d fetched ok, %d failed so far", atomic.LoadInt64(&fetchedOK), atomic.LoadInt64(&fetchFailed))
			case <-heartbeatDone:
				return
			}
		}
	}()

	processed, err := ing.writeResults(ctx, resultsCh)
	close(heartbeatDone)
	// Whether writeResults finished normally (resultsCh closed once
	// discovery+fetch both ran dry) or bailed out early on a flush error,
	// nothing is going to read resultsCh or drain idCh from here on — cancel
	// pipelineCtx so any worker/scan goroutine still blocked sending to one
	// of them unblocks via its ctx.Done() case instead of leaking forever
	// (see the comment above pipelineCtx's declaration).
	cancelPipeline()
	scanWG.Wait()
	workerWG.Wait()

	// A run cut short by ctx (a SIGINT, or the idle watchdog giving up —
	// see idleWatchdog) can still end with a nil write error: flushes are
	// deliberately decoupled from ctx (see writeResults), so the last
	// collected batch commits fine and the pipeline just drains early.
	// Without surfacing this here, such a truncated run looked fully
	// successful to Run(), which then attempted the stale-data sweep with
	// an already-expired ctx — the sweep query's own "context deadline
	// exceeded" failure was the only thing that stopped it from wiping
	// every location the run never got to visit. context.Cause (not plain
	// ctx.Err()) so the caller sees *why* — e.g. "no successful request in
	// the last 5m0s..." — rather than the generic "context canceled".
	if err == nil {
		err = context.Cause(ctx)
	}
	return processed, err
}

// writeResults drains resultsCh, batching writes by ingestionBulkChunkSize
// through writeSourceStationChunk. The total location count isn't known
// upfront (discovery and fetching now run concurrently), so progress is
// logged as a running count rather than "x/y".
func (ing *FreshmileIngester) writeResults(ctx context.Context, resultsCh <-chan normalizedSourceStation) (int, error) {
	processed := 0
	batch := make([]normalizedSourceStation, 0, ingestionBulkChunkSize)

	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		// Always decoupled from ctx (context.WithoutCancel), not just once
		// ctx has already ended: this batch is fully collected in memory
		// by this point, so once we've committed to writing it, a SIGINT
		// or the idle watchdog giving up landing mid-query shouldn't be
		// able to abort it —
		// checking ctx.Err() only at the top of flush() left a race where
		// cancellation arriving after that check but before the query
		// finished still aborted an in-progress write
		// ("canceling statement due to user request" observed in
		// practice). The bound is generous (freshmileFlushTimeout, not
		// the short shutdown-specific grace period an earlier version of
		// this used) so it doesn't interfere with a legitimately slow
		// write under concurrent load — it's just a backstop against a
		// truly hung query, not a race with the run's own cancellation.
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), freshmileFlushTimeout)
		defer cancel()
		n, err := writeSourceStationChunk(writeCtx, ing.Pool, ing.SourceStations, ing.Tariffs, ing.Links, ing.MaxLinkDistanceM, batch)
		processed += n
		batch = batch[:0]
		if err != nil {
			return err
		}
		log.Printf("freshmile: %d processed so far", processed)
		return nil
	}

	for item := range resultsCh {
		batch = append(batch, item)
		if len(batch) >= ingestionBulkChunkSize {
			if err := flush(); err != nil {
				return processed, err
			}
		}
	}
	if err := flush(); err != nil {
		return processed, err
	}
	return processed, nil
}

// fetchAndNormalizeLocation does the I/O-bound work for one location id
// (GET /locations/{id}, then normalization) without touching the
// database — writes are batched separately, see Run.
func (ing *FreshmileIngester) fetchAndNormalizeLocation(ctx context.Context, id int) (normalizedSourceStation, bool, error) {
	body, err := ing.getJSON(ctx, fmt.Sprintf("%s/locations/%d", ing.BaseURL, id), nil)
	if err != nil {
		return normalizedSourceStation{}, false, fmt.Errorf("fetch location details: %w", err)
	}
	// Unlike map-locations (a bare {"features":[...]}), /locations/{id}
	// wraps the actual location object in a "data" envelope.
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return normalizedSourceStation{}, false, fmt.Errorf("decode location details: %w", err)
	}
	details := envelope.Data
	if details == nil {
		return normalizedSourceStation{}, false, fmt.Errorf("location details response missing \"data\" envelope")
	}

	src, ok := normalizeFreshmileStation(details)
	if !ok {
		return normalizedSourceStation{}, false, fmt.Errorf("station without usable location")
	}
	return normalizedSourceStation{Station: src, Tariffs: normalizeFreshmileTariffs(details)}, true, nil
}

// freshmileBBox is a [minLng,minLat,maxLng,maxLat] map-locations query
// box. The JSON tags matter: a failed tile is persisted as this struct in
// the failure log (see FailureLog) and read back by RetryFailed.
type freshmileBBox struct {
	MinLng float64 `json:"minLng"`
	MinLat float64 `json:"minLat"`
	MaxLng float64 `json:"maxLng"`
	MaxLat float64 `json:"maxLat"`
}

// scanLocationIDs scans map-locations across metropolitan France, starting
// from a coarse grid of tiles, and sends each newly-discovered unique
// location ID onto idCh as it's found (closing idCh is the caller's job,
// once this returns). Any feature that's a cluster (location_count > 1) is
// resolved by recursively subdividing its own bbox — never fetched via
// /locations directly — until only unique points remain or
// freshmileMaxSubdivisionDepth is hit, in which case that (persistently
// clustered) branch is dropped rather than recursed into forever. Network
// errors on one tile are logged and skipped, not fatal.
//
// The scan tree is fanned out across goroutines rather than walked
// sequentially: discovery used to take the vast majority of a run's
// wall-clock time while producing nothing durable, so both scanning
// itself and (via idCh) the detail-fetch/write phase that consumes it now
// overlap. Concurrency is bounded by freshmileScanWorkers via a
// semaphore, but the semaphore only wraps each individual HTTP attempt
// (see getJSON) — never the retry backoff sleep, nor feature processing,
// nor the idCh send below — so a tile that's retrying or a scan()
// blocked handing an ID to a slow write side doesn't tie up one of the
// limited concurrent-request slots while it isn't actually using the
// network.
func (ing *FreshmileIngester) scanLocationIDs(ctx context.Context, idCh chan<- int) {
	const step = 2.0
	minLng, maxLng := -5.5, 9.8
	minLat, maxLat := 41.0, 51.5

	var initial []freshmileBBox
	for lat := minLat; lat < maxLat; lat += step {
		for lng := minLng; lng < maxLng; lng += step {
			initial = append(initial, freshmileBBox{
				MinLng: lng, MinLat: lat,
				MaxLng: min(lng+step, maxLng), MaxLat: min(lat+step, maxLat),
			})
		}
	}
	// See the identical comment in izivia.go's fetchMarkers: without this,
	// a chronically-timing-out run always gets cut off at the same tail end
	// of this fixed south-to-north, west-to-east grid, so the same region
	// of France goes stale run after run instead of the miss rotating.
	rand.Shuffle(len(initial), func(i, j int) { initial[i], initial[j] = initial[j], initial[i] })

	ing.scanBBoxes(ctx, idCh, initial)
}

// scanBBoxes is scanLocationIDs' engine, starting from an arbitrary list
// of initial boxes rather than always the full-France grid — RetryFailed
// reuses it to re-scan just the tiles a previous run recorded as failed.
func (ing *FreshmileIngester) scanBBoxes(ctx context.Context, idCh chan<- int, initial []freshmileBBox) {
	var (
		mu   sync.Mutex
		seen = map[int]struct{}{}
	)
	var visited int64
	sem := make(chan struct{}, freshmileScanWorkers)
	var wg sync.WaitGroup

	logDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(freshmileProgressLogInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				mu.Lock()
				found := len(seen)
				mu.Unlock()
				log.Printf("freshmile: scanning map-locations, %d tiles visited so far, %d locations found", atomic.LoadInt64(&visited), found)
			case <-logDone:
				return
			}
		}
	}()

	var scan func(bbox freshmileBBox, depth int)
	scan = func(bbox freshmileBBox, depth int) {
		defer wg.Done()
		if ctx.Err() != nil || atomic.LoadInt64(&visited) >= freshmileMaxTilesVisited {
			return
		}

		atomic.AddInt64(&visited, 1)

		// A cluster's own reported bbox can collapse to a single point on
		// one axis (real data: e.g. several stations at the exact same
		// longitude) — querying map-locations with a zero-area bbox gets a
		// 500 from Freshmile's API, so always pad before querying,
		// regardless of whether bbox came from the initial grid or a
		// cluster subdivision.
		bbox = padDegenerateBBox(bbox)

		features, err := ing.fetchMapLocations(ctx, bbox, sem)
		if err != nil {
			log.Printf("freshmile: map-locations bbox %+v failed: %v", bbox, err)
			// A failed tile drops its whole branch of the subdivision tree
			// (a region can go missing from discovery entirely), so it's
			// worth recording for a targeted retry — unless the scan is
			// just being canceled, see the same guard in izivia.go.
			if ctx.Err() == nil {
				ing.Failures.Record(failKindFreshmileTile, ing.mapLocationsURL(bbox), bbox, err)
			}
			return
		}
		for _, f := range features {
			props, _ := f["properties"].(map[string]any)
			if props == nil {
				continue
			}
			count, countOK := floatValue(props["location_count"])
			if !countOK || count == nil {
				continue
			}
			if *count <= 1 {
				locID, ok := floatValue(props["location_id"])
				if !ok || locID == nil {
					continue
				}
				id := int(*locID)
				mu.Lock()
				_, dup := seen[id]
				if !dup {
					seen[id] = struct{}{}
				}
				mu.Unlock()
				if !dup {
					select {
					case idCh <- id:
					case <-ctx.Done():
					}
				}
				continue
			}
			if depth >= freshmileMaxSubdivisionDepth {
				continue
			}
			clusterBBox, ok := freshmileClusterBBox(props)
			if !ok {
				continue
			}
			for _, sub := range subdivideBBox(clusterBBox) {
				wg.Add(1)
				go scan(sub, depth+1)
			}
		}
	}

	for _, tile := range initial {
		wg.Add(1)
		go scan(tile, 0)
	}
	wg.Wait()
	close(logDone)

	log.Printf("freshmile: discovery done, %d unique locations across %d map-locations tiles visited", len(seen), atomic.LoadInt64(&visited))
}

// freshmileClusterBBox reads properties.bbox.{sw,ne} off a cluster feature.
func freshmileClusterBBox(props map[string]any) (freshmileBBox, bool) {
	bboxRaw, _ := props["bbox"].(map[string]any)
	if bboxRaw == nil {
		return freshmileBBox{}, false
	}
	sw, _ := bboxRaw["sw"].([]any)
	ne, _ := bboxRaw["ne"].([]any)
	if len(sw) < 2 || len(ne) < 2 {
		return freshmileBBox{}, false
	}
	swLng, ok1 := floatValue(sw[0])
	swLat, ok2 := floatValue(sw[1])
	neLng, ok3 := floatValue(ne[0])
	neLat, ok4 := floatValue(ne[1])
	if !ok1 || !ok2 || !ok3 || !ok4 || swLng == nil || swLat == nil || neLng == nil || neLat == nil {
		return freshmileBBox{}, false
	}
	return freshmileBBox{MinLng: *swLng, MinLat: *swLat, MaxLng: *neLng, MaxLat: *neLat}, true
}

// subdivideBBox splits a bbox into 4 quadrants.
func subdivideBBox(b freshmileBBox) []freshmileBBox {
	midLng := (b.MinLng + b.MaxLng) / 2
	midLat := (b.MinLat + b.MaxLat) / 2
	return []freshmileBBox{
		{MinLng: b.MinLng, MinLat: b.MinLat, MaxLng: midLng, MaxLat: midLat},
		{MinLng: midLng, MinLat: b.MinLat, MaxLng: b.MaxLng, MaxLat: midLat},
		{MinLng: b.MinLng, MinLat: midLat, MaxLng: midLng, MaxLat: b.MaxLat},
		{MinLng: midLng, MinLat: midLat, MaxLng: b.MaxLng, MaxLat: b.MaxLat},
	}
}

func (ing *FreshmileIngester) mapLocationsURL(bbox freshmileBBox) string {
	return fmt.Sprintf("%s/map-locations?bbox=%g,%g,%g,%g&zoom=%d",
		ing.BaseURL, bbox.MinLng, bbox.MinLat, bbox.MaxLng, bbox.MaxLat, freshmileZoom)
}

func (ing *FreshmileIngester) fetchMapLocations(ctx context.Context, bbox freshmileBBox, sem chan struct{}) ([]map[string]any, error) {
	url := ing.mapLocationsURL(bbox)
	body, err := ing.getJSON(ctx, url, sem)
	if err != nil {
		return nil, err
	}
	var collection struct {
		Features []map[string]any `json:"features"`
	}
	if err := json.Unmarshal(body, &collection); err != nil {
		return nil, fmt.Errorf("decode map-locations: %w", err)
	}
	return collection.Features, nil
}

// getJSON performs a GET with retry/backoff on transient failures — see
// the shared withRetries in common.go, which this uses the same
// defaultMaxRetries budget as. Freshmile's map-locations failures make
// that budget earn its keep more than most: a failure here isn't like a
// single location's detail fetch failing (that just skips one station) —
// scanLocationIDs drops the whole tile/cluster branch and never revisits
// it, so a region can go permanently missing from discovery. That, plus
// scanLocationIDs running freshmileScanWorkers requests concurrently
// (more simultaneous load on the same gateway than a sequential scan
// would produce), makes a short retry budget more likely to be exhausted
// by the Azure Application Gateway's occasional 504s than it would be
// elsewhere.
//
// sem, if non-nil, bounds how many of these requests run concurrently
// across all callers sharing it (see scanLocationIDs) — it's acquired and
// released around each individual attempt in doGet, not around the whole
// retry loop, so a request that's sleeping between retries gives up its
// slot instead of holding it idle for up to defaultMaxRetries backoffs.
func (ing *FreshmileIngester) getJSON(ctx context.Context, url string, sem chan struct{}) ([]byte, error) {
	return withRetries(ctx, "freshmile", url, defaultMaxRetries, ing.retryBackoff, func() ([]byte, int, error) {
		return ing.doGet(ctx, url, sem)
	})
}

// doGet performs a single GET, always including the full URL in any
// returned error so a failure is directly reproducible (e.g. via curl)
// without having to reconstruct it from a bbox or id logged separately.
// sem, if non-nil, is acquired for the duration of this one attempt only.
func (ing *FreshmileIngester) doGet(ctx context.Context, url string, sem chan struct{}) ([]byte, int, error) {
	if sem != nil {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return nil, 0, ctx.Err()
		}
		defer func() { <-sem }()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("build request for %s: %w", url, err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")

	resp, err := ing.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("freshmile request to %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return nil, resp.StatusCode, fmt.Errorf("freshmile http %d for %s: %s", resp.StatusCode, url, string(data))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("freshmile read body from %s: %w", url, err)
	}
	ing.idle.Ping()
	return body, resp.StatusCode, nil
}

func normalizeFreshmileStation(details map[string]any) (domain.SourceStation, bool) {
	ref := stringValue(details["ref"])
	if ref == "" {
		return domain.SourceStation{}, false
	}

	coords, _ := details["coordinates"].(map[string]any)
	lat, latOK := floatValue(coords["latitude"])
	lng, lngOK := floatValue(coords["longitude"])
	if !latOK || !lngOK || lat == nil || lng == nil {
		return domain.SourceStation{}, false
	}

	addressMap, _ := details["address"].(map[string]any)

	return domain.SourceStation{
		Source:          "freshmile",
		SourceStationID: ref,
		Name:            stringValue(details["name"]),
		OperatorName:    "Freshmile",
		AddressStreet:   stringValue(addressMap["fullname"]),
		AddressPostal:   stringValue(addressMap["postal_code"]),
		AddressCity:     stringValue(addressMap["city"]),
		AddressCountry:  normalizeCountry(stringValue(addressMap["country"])),
		Lat:             *lat,
		Lng:             *lng,
		Raw:             details,
	}, true
}

// freshmileConnectorType maps a Freshmile connector "standard" to the
// vocabulary used elsewhere (IRVE's connector_type, roughly) so the
// frontend can pick a consistent icon — stored in a tariff's Extra since
// domain.StationTariff has no connector-type field of its own (Freshmile
// is a source_station, not the IRVE referential).
func freshmileConnectorType(standard string) string {
	switch strings.ToUpper(standard) {
	case "IEC_62196_T2_COMBO":
		return domain.ConnectorTypeCCS
	case "CHADEMO":
		return domain.ConnectorTypeCHAdeMO
	case "IEC_62196_T2":
		return domain.ConnectorTypeT2
	default:
		return standard
	}
}

// freshmileTariffCandidate pairs a normalized tariff with whether it came
// from a preferential (partner/member-only) price tier, so
// normalizeFreshmileTariffs can rank candidates for the same Kind without
// re-deriving that from Extra.
type freshmileTariffCandidate struct {
	tariff         domain.StationTariff
	isPreferential bool
}

// normalizeFreshmileTariffs walks every evse/connector's tariff and keeps,
// per (Kind, ConnectorType) pair, a single representative StationTariff —
// see freshmileBetterCandidate for how "representative" is chosen within a
// pair. Earlier versions kept every connector's tariff as its own Plan,
// using Freshmile's per-tariff custom_ref (e.g. "lidl-interop-hastobe") as
// the Plan value. In production that turned out to be a partner/venue
// contract identifier, not a small, meaningful set of price tiers the way
// Electra's public/app/subscription is — every newly-ingested partner
// contract added another entry to the network-wide plan selector, growing
// it unbounded. Freshmile tariffs now always use the single "standard"
// Plan, matching how every other single-tier source (Izivia, IRVE text,
// ...) behaves; ConnectorType (ingestion/freshmile.go's own
// freshmileConnectorType, mapped to domain.ConnectorType* where possible)
// carries the per-connector precision instead, as a real column rather
// than folded into Plan.
func normalizeFreshmileTariffs(details map[string]any) []domain.StationTariff {
	bestPowerCategory := ""
	if connectorsSummary, ok := details["connectors"].(map[string]any); ok {
		if bestPower, ok := connectorsSummary["best_power"].(map[string]any); ok {
			bestPowerCategory = strings.ToLower(stringValue(bestPower["category"]))
		}
	}

	best := map[string]freshmileTariffCandidate{}
	evses, _ := details["evses"].([]any)
	for _, rawEvse := range evses {
		evse, ok := rawEvse.(map[string]any)
		if !ok {
			continue
		}
		connectors, _ := evse["connectors"].([]any)
		for _, rawConn := range connectors {
			conn, ok := rawConn.(map[string]any)
			if !ok {
				continue
			}
			tariffRaw, ok := conn["tariff"].(map[string]any)
			if !ok {
				continue
			}
			candidate := freshmileTariffCandidate{
				tariff:         normalizeFreshmileConnectorTariff(conn, tariffRaw, bestPowerCategory),
				isPreferential: parseBooleanLoose(stringValue(tariffRaw["is_preferential"])),
			}
			key := candidate.tariff.Kind + "\x00" + candidate.tariff.ConnectorType
			current, exists := best[key]
			if !exists || freshmileBetterCandidate(candidate, current) {
				best[key] = candidate
			}
		}
	}

	if len(best) == 0 {
		return nil
	}
	tariffs := make([]domain.StationTariff, 0, len(best))
	for _, c := range best {
		tariffs = append(tariffs, c.tariff)
	}
	return tariffs
}

// freshmileBetterCandidate decides which of two same-Kind tariff
// candidates should represent a Freshmile station now that every
// connector's tariff collapses onto the single "standard" Plan: a
// publicly-available (non-preferential) price always wins over a
// partner/member-only preferential one — showing a discount most visitors
// can't actually get would be misleading — and within the same tier, the
// cheaper known price wins. A candidate with no parseable price never
// displaces one that has a price, but is kept if it's the only one seen
// for that Kind so far.
func freshmileBetterCandidate(candidate, current freshmileTariffCandidate) bool {
	if candidate.isPreferential != current.isPreferential {
		return !candidate.isPreferential
	}
	if candidate.tariff.EnergyPriceCentsPerKWh == nil && candidate.tariff.SessionPriceCentsPerMin == nil {
		return false
	}
	if current.tariff.EnergyPriceCentsPerKWh == nil && current.tariff.SessionPriceCentsPerMin == nil {
		return true
	}
	if candidate.tariff.EnergyPriceCentsPerKWh != nil && current.tariff.EnergyPriceCentsPerKWh != nil {
		return *candidate.tariff.EnergyPriceCentsPerKWh < *current.tariff.EnergyPriceCentsPerKWh
	}
	// One priced per kWh, the other per minute (or the comparison is
	// otherwise not apples-to-apples): keep the incumbent rather than
	// guess which is actually cheaper.
	return false
}

func normalizeFreshmileConnectorTariff(conn, tariffRaw map[string]any, bestPowerCategory string) domain.StationTariff {
	power, _ := floatValue(conn["power"])
	kind := domain.TariffKindAC
	if (power != nil && *power >= 50) || bestPowerCategory == "fast" || bestPowerCategory == "superfast" {
		kind = domain.TariffKindDC
	}

	extra := map[string]any{
		"tariff": tariffRaw,
	}

	t := domain.StationTariff{
		Source:        "freshmile",
		Plan:          domain.TariffPlanStandard,
		Kind:          kind,
		Model:         "freshmile_kwh",
		Currency:      firstNonEmpty(stringValue(tariffRaw["currency"]), "EUR"),
		ConnectorType: freshmileConnectorType(stringValue(conn["standard"])),
		Extra:         extra,
	}

	if parseBooleanLoose(stringValue(tariffRaw["is_free"])) {
		extra["is_free"] = true
		// A free tariff is a real 0 €/kWh price, not an unknown one: set it
		// explicitly so it feeds the AC/DC minimums (and shows as "0,00 €")
		// instead of being indistinguishable from a tariff whose price
		// couldn't be parsed (left nil below).
		zero := 0.0
		t.EnergyPriceCentsPerKWh = &zero
		return t
	}

	// Try both a €/kWh price and a €/min rate rather than stopping at the
	// first match: some Freshmile tariffs genuinely combine the two in one
	// description (e.g. "0,50 € par kwh et 0,05 € par minute" — real
	// production text), and a tariff that's actually per-minute-only would
	// never even reach the session-price check if €/kWh matching returned
	// early. Genuinely tiered/packaged descriptions ("6 € les 5 premières
	// heures puis 2 € toutes les 15 minutes", "forfait de 2 € par 6 kWh")
	// don't reduce to a single €/kWh or €/min figure and are deliberately
	// left with both prices nil — both patterns require their unit word
	// directly adjacent to the amount, which keeps them from latching onto
	// an unrelated number in a tiered/packaged description.
	price, lang, priceText, priceOK := freshmilePriceFromDescription(tariffRaw["description"])
	sessionPrice, sessionText, sessionOK := freshmileSessionPriceFromDescription(tariffRaw["description"])
	if !priceOK && !sessionOK {
		log.Printf("freshmile: could not extract a €/kWh or €/min price from tariff %v description: %v", tariffRaw["id"], tariffRaw["description"])
		return t
	}
	if priceOK {
		t.EnergyPriceCentsPerKWh = price
		extra["parsedLang"] = lang
		extra["parsedText"] = priceText
		extra["energyPriceEuro"] = *price / 100
	}
	if sessionOK {
		t.SessionPriceCentsPerMin = sessionPrice
		if priceOK {
			t.Model = "freshmile_kwh_and_per_minute"
		} else {
			t.Model = "freshmile_per_minute"
			extra["parsedText"] = sessionText
		}
	}
	return t
}

// freshmilePricePattern matches "<amount> € / [started ]kWh" style text in
// either currency-symbol ordering — French puts the amount first ("0,70 €
// / kWh entamé."), English puts € first ("€ 0.70 / started kWh.") — via two
// alternative capture groups; the amount can use either a comma or dot
// decimal separator. The separator between amount and "kWh" also varies:
// "/" ("0,70 € / kWh"), or the word "par"/"per" ("0,25 € par kWh entamé").
// Case-insensitive: production text isn't consistent about capitalizing
// "kWh" (e.g. "0,50 € par kwh" — lowercase — was silently unmatched before).
var freshmilePricePattern = mustCompileFrenchWS(`(?i)(?:([\d.,]+)\s*€|€\s*([\d.,]+))\s*(?:/|par|per)\s*(?:started\s+)?kwh`)

// freshmilePriceFromDescription extracts a €/kWh price (in cents) from a
// Freshmile tariff's description field. Two shapes have been observed in
// the wild: a JSON-encoded multi-language object (e.g. {"fr": "0,70 € /
// kWh entamé.", "en": "€ 0.70 / started kWh."}) and — in current
// production responses — a single plain-text string in whatever language
// the API happened to answer in (e.g. "€ 0.30 / started kWh + € 0.30 /
// min\nThe pricing continues..."). The JSON form is tried first (and
// prefers French then English); a plain string that isn't valid JSON
// falls back to matching the price pattern directly against it.
func freshmilePriceFromDescription(raw any) (priceCents *float64, lang, text string, ok bool) {
	descText := stringValue(raw)
	if descText == "" {
		return nil, "", "", false
	}

	var byLang map[string]string
	if err := json.Unmarshal([]byte(descText), &byLang); err == nil {
		for _, l := range []string{"fr", "en"} {
			t := byLang[l]
			if t == "" {
				continue
			}
			if cents, ok := extractFreshmilePriceCents(t); ok {
				return cents, l, t, true
			}
		}
		return nil, "", "", false
	}

	if cents, ok := extractFreshmilePriceCents(descText); ok {
		return cents, "", descText, true
	}
	return nil, "", "", false
}

// extractFreshmilePriceCents matches freshmilePricePattern against a
// single piece of text and converts the amount to cents.
func extractFreshmilePriceCents(t string) (*float64, bool) {
	match := freshmilePricePattern.FindStringSubmatch(t)
	if len(match) != 3 {
		return nil, false
	}
	amount := firstNonEmpty(match[1], match[2])
	euros, err := strconv.ParseFloat(strings.ReplaceAll(amount, ",", "."), 64)
	if err != nil {
		return nil, false
	}
	// Round to avoid float64 noise from the euro->cents multiplication
	// (e.g. 0.55 * 100 = 55.00000000000001).
	cents := math.Round(euros*10000) / 100
	return &cents, true
}

// freshmileSessionPriceFromDescription extracts a simple €/minute price
// (in cents) from a Freshmile tariff's description, for tariffs priced by
// time rather than energy (e.g. "0,40 € par minute"). Reuses
// pricePerMinutePattern (common.go, shared with Izivia's free-text price
// parsing) and the same JSON-vs-plain-text description shapes as
// freshmilePriceFromDescription. A genuinely tiered time tariff (a flat
// fee for the first N hours, then a rate per subsequent block — e.g. "6 €
// les 5 premières heures puis 2 € toutes les 15 minutes") doesn't reduce
// to a single €/min figure; pricePerMinutePattern requires the amount and
// "par minute"/"/min" to be directly adjacent, so it correctly leaves
// those unmatched instead of latching onto an unrelated trailing rate.
func freshmileSessionPriceFromDescription(raw any) (cents *float64, text string, ok bool) {
	descText := stringValue(raw)
	if descText == "" {
		return nil, "", false
	}

	var byLang map[string]string
	if err := json.Unmarshal([]byte(descText), &byLang); err == nil {
		for _, l := range []string{"fr", "en"} {
			t := byLang[l]
			if t == "" {
				continue
			}
			if c := matchEuroCentsFirstNonZero(pricePerMinutePattern, t); c != nil {
				return c, t, true
			}
		}
		return nil, "", false
	}

	if c := matchEuroCentsFirstNonZero(pricePerMinutePattern, descText); c != nil {
		return c, descText, true
	}
	return nil, "", false
}
