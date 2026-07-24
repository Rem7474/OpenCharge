package ingestion

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"opencharge/internal/domain"
	"opencharge/internal/repository"
)

// DefaultChargenowBaseURL is ChargeNow's (Digital Charging Solutions,
// "DCS") public map API.
const DefaultChargenowBaseURL = "https://chargenow.com/api/map/v1/fr"

const (
	chargenowQueryPath  = "/query"
	chargenowPricesPath = "/tariffs/CHARGENOW_PRIME/prices"
)

// chargenowHeaders mirrors the browser request ChargeNow's WAF accepts —
// same reasoning as izivia.go's iziviaHeaders: plain net/http requests
// without these get rejected outright. rest-api-path is deliberately NOT
// here even though it's a real header ChargeNow's WAF checks: it's a
// per-request routing key (see doRequest's doc comment — its value picks
// which internal microservice actually handles the request), set
// per-request there instead. A previous version of this map set it to
// "clusters" here as a blanket default, which — once doRequest's own
// per-request value became conditional (empty means "send no header at
// all", needed for /tariffs/.../prices) — silently won back via this
// map's own for-range loop before the per-request logic ran, defeating
// the whole fix.
var chargenowHeaders = map[string]string{
	"accept":          "application/json, text/plain, */*",
	"accept-language": "fr,fr-FR;q=0.9,en;q=0.8",
	"content-type":    "application/json",
	"cookie":          "CN_ALLOW_FUNCTIONAL_COOKIES_2=false; CN_ALLOW_GOOGLE_MAPS=true",
	"dnt":             "1",
	"origin":          "https://chargenow.com",
	"referer":         "https://chargenow.com/web/fr/cn-fr/map",
	"sec-fetch-dest":  "empty",
	"sec-fetch-mode":  "cors",
	"sec-fetch-site":  "same-origin",
	"User-Agent":      "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
}

const (
	chargenowSourceName  = "chargenow"
	chargenowTariffModel = "chargenow_ocpi"

	// Same France bounding box and grid step every other grid-scan
	// ingester (Izivia, Freshmile) uses.
	chargenowGridStep            = 2.0
	chargenowPrecision           = 3
	chargenowMaxSubdivisionDepth = 8
	// See freshmileMaxTilesVisited's comment for why this needs to be a
	// generous backstop, not a routine constraint: chargenowMaxSubdivisionDepth
	// and subdivideChargenowBBox's strict halving already bound worst-case
	// blowup per initial tile.
	chargenowMaxTilesVisited = 500000
	// chargenowScanWorkers was 16 — confirmed in production that firing
	// that many requests at once (the natural result of subdividing a
	// cluster into 4 sub-tiles, recursed 16-wide) gets ChargeNow's WAF to
	// reject every single one of them, discovery and pricing alike, with
	// an opaque 403 HTML page — not a per-request rejection, a blanket
	// one, on requests that succeed individually and instantly from a
	// browser or a lone curl. A real visitor's browser never has anywhere
	// near this many requests in flight; see chargenowMinRequestInterval,
	// the actual fix, for why a lower worker count alone doesn't fully
	// solve this (it only bounds how many requests can be queued up
	// waiting, not how fast they're actually sent).
	chargenowScanWorkers = 4
	// chargenowMinRequestInterval paces every single request this
	// ingester makes to chargenow.com (discovery and pricing alike — see
	// doRequest) to no faster than one every this long, regardless of how
	// many workers/goroutines are trying to fire at once. This is the
	// actual fix for the WAF block described above: chargenowScanWorkers
	// alone only bounds concurrency, not request rate, and a WAF blocking
	// on burst volume doesn't care whether 16 requests arrived from 16
	// goroutines or 1 goroutine firing 16 times fast — both look nothing
	// like a human panning/zooming a map. 150ms (~6.7 req/s) confirmed
	// against production as safe — a real full-France run at this rate
	// didn't get IP-blocked, unlike the previous unthrottled/16-worker
	// burst.
	chargenowMinRequestInterval = 150 * time.Millisecond
	// chargenowProgressLogInterval controls how often scanPoolsFrom logs
	// progress while it scans — same rationale as freshmile.go's
	// freshmileProgressLogInterval, and more important than ever now that
	// chargenowMinRequestInterval throttles every request: consumePools'
	// own "N processed so far" log only fires once a full
	// chargenowPoolBatchSize batch has been discovered, correlated,
	// priced, and written, which can now take minutes — without this
	// heartbeat, a slow-but-healthy throttled run looks identical to a
	// hung one for that whole stretch.
	chargenowProgressLogInterval = 10 * time.Second
	// chargenowPriceBatchSize is how many (charge_point, power_type, power)
	// triples go in one POST to the prices endpoint. Deliberately small and
	// close to real traffic (every captured real request has 1-3 items,
	// for a single pool's charge points) rather than batching many pools
	// together: confirmed in production that a single much larger request
	// (74 items, from batching a whole chargenowPoolBatchSize worth of
	// pools into it) gets a 403 from ChargeNow's WAF even once request
	// *rate* is already throttled to something safe (chargenowMinRequestInterval)
	// — request *size* looks like an independent signal it also checks.
	// processPoolBatch's own price-fetch loop already slices a pool
	// batch's full item list into as many chargenowPriceBatchSize-sized
	// requests as it takes, so shrinking this doesn't lose anything, just
	// trades fewer/bigger requests for more/smaller ones.
	chargenowPriceBatchSize = 3
	// chargenowPoolBatchSize bounds how many pools are correlated, priced,
	// and written together as one unit before the pipeline moves on to the
	// next. No longer tied to chargenowPriceBatchSize (seeing prices sent
	// in small batches): this is purely about our own database-write
	// efficiency (one bulk round trip per batch — see
	// writeSourceStationChunk), which has nothing to do with what
	// ChargeNow's API tolerates per request. Pools are fed into a batch as
	// they're discovered (see scanPools), interleaved with pricing and
	// writing, rather than only starting once the whole map has been
	// scanned — same durability rationale as freshmile.go's streaming
	// pipeline: a run cut short (SIGINT, the idle watchdog giving up)
	// keeps whatever has already been priced and written instead of
	// losing everything gathered so far.
	chargenowPoolBatchSize = 100

	// chargenowFlushTimeout bounds how long a single batch write is
	// allowed to take — same rationale and value as izivia.go's
	// iziviaFlushTimeout/freshmile.go's freshmileFlushTimeout.
	chargenowFlushTimeout = 2 * time.Minute

	// Fallback power (kW) used only for a pool with no IRVE match within
	// MaxLinkDistanceM to read a real power_kw from — see Run's
	// correlate-before-price comment for why a real value is normally
	// available.
	chargenowDefaultACPowerKW = 22.0
	chargenowDefaultDCPowerKW = 50.0
)

type ChargenowConfig struct {
	Workers int
}

func DefaultChargenowConfig() ChargenowConfig {
	return ChargenowConfig{Workers: chargenowScanWorkers}
}

type ChargenowIngester struct {
	Pool             *pgxpool.Pool
	SourceStations   *repository.SourceStationRepository
	Tariffs          *repository.TariffRepository
	Links            *repository.LinkRepository
	BaseURL          string
	Config           ChargenowConfig
	MaxLinkDistanceM float64
	// Failures, when set, records every request that failed for good
	// (discovery query for a bbox, price batch — recorded per affected
	// pool) so a later -retry-failed pass can replay just those — see
	// FailureLog.
	Failures *FailureLog
	// IdleTimeout bounds how long Run/RetryFailed goes without a single
	// successful request before giving up on the whole run — see
	// idleWatchdog. Defaults to DefaultIdleTimeout; <= 0 disables it.
	IdleTimeout time.Duration
	idle        *idleWatchdog // set by Run/RetryFailed, read by doRequest
	client      *http.Client
	// limiter paces every request (see chargenowMinRequestInterval);
	// overridden by tests to a no-op/faster interval to keep them fast.
	limiter      *chargenowRateLimiter
	retryBackoff time.Duration // unexported: overridden by tests to keep them fast
}

// chargenowRateLimiter hands out one token every interval via a shared
// ticker channel — any number of goroutines can wait on it concurrently,
// but only one proceeds per tick, which is exactly the property needed
// here: cap chargenow.com's request rate regardless of how many workers
// are trying to fire at once (see chargenowMinRequestInterval's doc
// comment for why that, not just a lower worker count, is the actual fix
// for the WAF block observed in production).
type chargenowRateLimiter struct {
	ticker *time.Ticker
}

func newChargenowRateLimiter(interval time.Duration) *chargenowRateLimiter {
	return &chargenowRateLimiter{ticker: time.NewTicker(interval)}
}

func (rl *chargenowRateLimiter) Wait(ctx context.Context) error {
	select {
	case <-rl.ticker.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func NewChargenowIngester(pool *pgxpool.Pool, sourceStations *repository.SourceStationRepository, tariffs *repository.TariffRepository, links *repository.LinkRepository, baseURL string, cfg ChargenowConfig) *ChargenowIngester {
	if baseURL == "" {
		baseURL = DefaultChargenowBaseURL
	}
	workers := effectiveWorkers(cfg.Workers, chargenowScanWorkers)
	// Same MaxIdleConnsPerHost fix as izivia.go's newIziviaHTTPClient:
	// http.DefaultTransport's default of 2 would otherwise serialize most
	// of a single-host worker fan-out behind repeated TCP/TLS handshakes.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = workers
	transport.MaxConnsPerHost = 0
	return &ChargenowIngester{
		Pool: pool, SourceStations: sourceStations, Tariffs: tariffs, Links: links,
		BaseURL: baseURL, Config: cfg, MaxLinkDistanceM: DefaultLinkMaxDistanceMeters,
		IdleTimeout:  DefaultIdleTimeout,
		client:       &http.Client{Timeout: 20 * time.Second, Transport: transport},
		limiter:      newChargenowRateLimiter(chargenowMinRequestInterval),
		retryBackoff: 2 * time.Second,
	}
}

// startIdleWatchdog wraps ctx with this ingester's idle watchdog (see
// idleWatchdog) and records it on ing.idle so doRequest can Ping it. The
// returned cancel must be deferred immediately by the caller.
func (ing *ChargenowIngester) startIdleWatchdog(ctx context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel, watchdog := startIdleWatchdog(ctx, ing.IdleTimeout)
	ing.idle = watchdog
	return ctx, cancel
}

// Run scans ChargeNow's map for pools (physical charging locations)
// covering metropolitan France and, as pools are discovered, correlates
// each with the nearest IRVE station(s), fetches its price, and writes the
// result — streamed in batches of chargenowPoolBatchSize as discovery
// finds them, rather than only starting once the whole map has been
// scanned (same discover+fetch+write-concurrently shape as freshmile.go's
// pipeline, and for the same reason: a run cut short keeps whatever it's
// already priced and written instead of losing everything). Correlation
// happens before pricing (not after, as in every other ingester's
// writeSourceStationChunk) because ChargeNow's own discovery API returns
// only a pool's location and its charge points' bare ids, never their
// power/connector type, while its pricing API requires exactly that
// (charge_point, power_type, power) to price anything — IRVE already
// knows the connector type/power for the physical location a pool
// corresponds to, so that's what's used to build the price query.
func (ing *ChargenowIngester) Run(ctx context.Context) (int, error) {
	defer ing.Failures.saveAndLog()
	ctx, cancelIdle := ing.startIdleWatchdog(ctx)
	defer cancelIdle()
	runStart := time.Now()

	processed, err := ing.runPipeline(ctx, ing.scanPools)
	if err != nil {
		return processed, err
	}
	slog.Info("ingestion done", "source", "chargenow", "processed", processed)

	// Only sweep after actually finding stations this run — see the same
	// guard (and the incident that motivated it) in izivia.go.
	if processed > 0 {
		if err := repository.SweepStaleSourceData(ctx, ing.Pool, chargenowSourceName, runStart.Add(-repository.StaleSourceDataGracePeriod)); err != nil {
			return processed, err
		}
	}
	return processed, nil
}

// RetryFailed replays only the requests a previous run recorded as failed
// (see FailureLog): failed discovery bboxes are re-scanned to recover
// their pools, and pools whose price batch failed are re-priced directly
// from the pool identity saved in the failure's params. Requests that
// fail again are re-recorded, so the failure file always reflects what's
// still outstanding. No stale-data sweep happens here: a retry pass only
// touches the previously-failed subset, so most known stations
// legitimately go un-refreshed.
func (ing *ChargenowIngester) RetryFailed(ctx context.Context, failures []FailedFetch) (int, error) {
	defer ing.Failures.saveAndLog()
	ctx, cancelIdle := ing.startIdleWatchdog(ctx)
	defer cancelIdle()

	var bboxes []chargenowBBox
	var directPools []chargenowPool
	for _, f := range failures {
		switch f.Kind {
		case failKindChargenowBBox:
			var bbox chargenowBBox
			if err := json.Unmarshal(f.Params, &bbox); err != nil {
				slog.Warn("skipping unreadable failure", "source", "chargenow", "kind", f.Kind, "error", err)
				continue
			}
			bboxes = append(bboxes, bbox)
		case failKindChargenowPool:
			var pool chargenowPool
			if err := json.Unmarshal(f.Params, &pool); err != nil || pool.ID == "" {
				slog.Warn("skipping unreadable failure", "source", "chargenow", "kind", f.Kind, "error", err)
				continue
			}
			directPools = append(directPools, pool)
		default:
			slog.Warn("skipping failure of unknown kind", "source", "chargenow", "kind", f.Kind)
		}
	}

	slog.Info("retrying recorded failures", "source", "chargenow", "directPools", len(directPools), "bboxes", len(bboxes), "failures", len(failures))

	// A pool fed directly may also be re-discovered by a retried bbox's
	// own scan; the resulting duplicate send is harmless (the write path
	// upserts) and rare enough not to be worth deduplicating across the
	// two feeds.
	feed := func(feedCtx context.Context, poolCh chan<- chargenowPool) {
		for _, p := range directPools {
			select {
			case poolCh <- p:
			case <-feedCtx.Done():
				return
			}
		}
		if len(bboxes) > 0 {
			ing.scanPoolsFrom(feedCtx, bboxes, poolCh)
		}
	}
	processed, err := ing.runPipeline(ctx, feed)
	slog.Info("retry done", "source", "chargenow", "processed", processed)
	return processed, err
}

// runPipeline runs the shared discover→correlate→price→write pipeline
// behind both a full Run and a RetryFailed pass: feed streams discovered
// pools onto poolCh (a full run's feed is scanPools; a retry pass feeds
// recorded pools directly and re-scans failed bboxes), and a single
// consumer batches them by chargenowPoolBatchSize, correlating, pricing,
// and writing each batch as it fills — same streaming shape as
// freshmile.go's runPipeline.
func (ing *ChargenowIngester) runPipeline(ctx context.Context, feed func(ctx context.Context, poolCh chan<- chargenowPool)) (int, error) {
	// pipelineCtx (not ctx directly) governs feed, so that once
	// consumePools returns below — whether poolCh closed normally or a
	// batch's correlate/price/write failed early — feed's own goroutine,
	// if still blocked trying to send a pool, unblocks via
	// pipelineCtx.Done() instead of leaking forever. Same rationale as
	// freshmile.go's identical pipelineCtx.
	pipelineCtx, cancelPipeline := context.WithCancel(ctx)
	defer cancelPipeline()

	poolCh := make(chan chargenowPool, chargenowPoolBatchSize)

	var feedWG sync.WaitGroup
	feedWG.Add(1)
	go func() {
		defer feedWG.Done()
		defer close(poolCh)
		feed(pipelineCtx, poolCh)
	}()

	processed, err := ing.consumePools(ctx, poolCh)
	// Whether consumePools drained poolCh to completion or returned early
	// on a batch error, cancel pipelineCtx so feed unblocks instead of
	// leaking (see the comment above pipelineCtx's declaration).
	cancelPipeline()
	feedWG.Wait()

	if err == nil {
		err = context.Cause(ctx)
	}
	return processed, err
}

// consumePools drains poolCh, batching by chargenowPoolBatchSize, and
// correlates+prices+writes each batch via processPoolBatch as it fills —
// so an interruption only loses the batch currently in flight, not
// everything discovered so far. Kept single-threaded (one batch at a
// time, not a worker pool): each batch already correlates/prices up to
// chargenowPoolBatchSize pools in one bulk round trip / one price
// request, so the per-batch unit of work is large enough that added
// concurrency here would mostly just mean several such bulk operations
// competing for the same downstream resources, not meaningfully faster
// throughput.
func (ing *ChargenowIngester) consumePools(ctx context.Context, poolCh <-chan chargenowPool) (int, error) {
	processed := 0
	batch := make([]chargenowPool, 0, chargenowPoolBatchSize)

	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		n, err := ing.processPoolBatch(ctx, batch)
		processed += n
		batch = batch[:0]
		if err != nil {
			return err
		}
		slog.Info("processing progress", "source", "chargenow", "processed", processed)
		return nil
	}

	for p := range poolCh {
		batch = append(batch, p)
		if len(batch) >= chargenowPoolBatchSize {
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

// processPoolBatch correlates one batch of pools with the IRVE
// referential, fetches their prices and writes the resulting source
// stations/tariffs — called once per chargenowPoolBatchSize-sized batch
// by consumePools as pools are discovered, rather than once for the whole
// dataset.
func (ing *ChargenowIngester) processPoolBatch(ctx context.Context, pools []chargenowPool) (int, error) {
	points := make([]repository.NearestStationQuery, len(pools))
	for i, p := range pools {
		points[i] = repository.NearestStationQuery{Lat: p.Lat, Lng: p.Lng}
	}
	nearestAC, err := ing.Links.FindNearestStationsForKind(ctx, points, domain.TariffKindAC, "", ing.MaxLinkDistanceM)
	if err != nil {
		return 0, fmt.Errorf("correlate chargenow pools (ac): %w", err)
	}
	nearestDC, err := ing.Links.FindNearestStationsForKind(ctx, points, domain.TariffKindDC, "", ing.MaxLinkDistanceM)
	if err != nil {
		return 0, fmt.Errorf("correlate chargenow pools (dc): %w", err)
	}

	type priceTarget struct {
		poolIdx int
		kind    string
	}
	var items []chargenowPriceQueryItem
	var targets []priceTarget
	for i, p := range pools {
		if len(p.ChargePointIDs) == 0 {
			continue
		}
		// One representative charge point per pool is enough: ChargeNow
		// prices by (power_type, power) for a given tariff plan, not by
		// individual physical charge point (confirmed by the sample
		// response — two different charge points at the same power/type
		// came back with the identical tariff id and price), and the API
		// still requires a valid charge_point id in the request payload.
		chargePointID := p.ChargePointIDs[0]
		if ac, ok := nearestAC[i]; ok {
			power := chargenowDefaultACPowerKW
			if ac.PowerKW != nil {
				power = *ac.PowerKW
			}
			items = append(items, chargenowPriceQueryItem{ChargePoint: chargePointID, PowerType: "AC", Power: power})
			targets = append(targets, priceTarget{poolIdx: i, kind: domain.TariffKindAC})
		}
		if dc, ok := nearestDC[i]; ok {
			power := chargenowDefaultDCPowerKW
			if dc.PowerKW != nil {
				power = *dc.PowerKW
			}
			items = append(items, chargenowPriceQueryItem{ChargePoint: chargePointID, PowerType: "DC", Power: power})
			targets = append(targets, priceTarget{poolIdx: i, kind: domain.TariffKindDC})
		}
	}

	targetByKey := make(map[string]priceTarget, len(items))
	for i, it := range items {
		targetByKey[chargenowPriceKey(it.ChargePoint, it.PowerType, it.Power)] = targets[i]
	}

	type poolPrice struct {
		energyCents *float64
		flatCents   *float64
	}
	pricesByPoolKind := make(map[string]poolPrice)

	// A failed price batch loses the tariffs of every pool it covered:
	// record each affected pool (dedup'd — a pool contributes one AC and
	// one DC item to the same batch) so a -retry-failed pass can re-price
	// exactly those, without re-scanning the whole map. Skipped when the
	// run is just being canceled — see the same guard in izivia.go.
	recordFailedBatch := func(targets []priceTarget, start, end int, err error) {
		if ctx.Err() != nil {
			return
		}
		recorded := map[int]struct{}{}
		for _, target := range targets[start:end] {
			if _, dup := recorded[target.poolIdx]; dup {
				continue
			}
			recorded[target.poolIdx] = struct{}{}
			ing.Failures.Record(failKindChargenowPool, ing.BaseURL+chargenowPricesPath, pools[target.poolIdx], err)
		}
	}

	for start := 0; start < len(items); start += chargenowPriceBatchSize {
		end := start + chargenowPriceBatchSize
		if end > len(items) {
			end = len(items)
		}
		body, err := json.Marshal(items[start:end])
		if err != nil {
			return 0, fmt.Errorf("marshal chargenow price batch: %w", err)
		}
		respBody, err := ing.withRetries(ctx, ing.BaseURL+chargenowPricesPath, func() ([]byte, int, error) {
			// No rest-api-path header on this request — see doRequest's doc
			// comment for why setting one here previously broke every price
			// fetch.
			return ing.doRequest(ctx, ing.BaseURL+chargenowPricesPath, "", body)
		})
		if err != nil {
			slog.Warn("price batch failed, skipping", "source", "chargenow", "start", start, "end", end, "error", err)
			recordFailedBatch(targets, start, end, err)
			continue
		}
		var results []chargenowPriceResult
		if err := json.Unmarshal(respBody, &results); err != nil {
			slog.Warn("decode price batch failed, skipping", "source", "chargenow", "start", start, "end", end, "error", err)
			recordFailedBatch(targets, start, end, err)
			continue
		}
		for _, res := range results {
			key := chargenowPriceKey(res.PriceIdentifier.ChargePoint, res.PriceIdentifier.PowerType, res.PriceIdentifier.Power)
			target, ok := targetByKey[key]
			if !ok {
				continue
			}
			energyCents, flatCents := chargenowExtractPrices(res)
			pricesByPoolKind[fmt.Sprintf("%d|%s", target.poolIdx, target.kind)] = poolPrice{energyCents, flatCents}
		}
		slog.Info("price queries progress", "source", "chargenow", "done", end, "total", len(items))
	}

	var normalized []normalizedSourceStation
	for i, p := range pools {
		var tariffs []domain.StationTariff
		for _, kind := range [2]string{domain.TariffKindAC, domain.TariffKindDC} {
			pp, ok := pricesByPoolKind[fmt.Sprintf("%d|%s", i, kind)]
			if !ok || pp.energyCents == nil {
				continue
			}
			tariffs = append(tariffs, domain.StationTariff{
				Source: chargenowSourceName, Plan: domain.TariffPlanStandard, Kind: kind,
				Model: chargenowTariffModel, Currency: "EUR",
				EnergyPriceCentsPerKWh: pp.energyCents, SessionFeeCents: pp.flatCents,
				Extra: map[string]any{},
			})
		}
		if len(tariffs) == 0 {
			continue
		}
		normalized = append(normalized, normalizedSourceStation{
			Station: domain.SourceStation{
				Source: chargenowSourceName, SourceStationID: p.ID,
				Name: "ChargeNow", OperatorName: "ChargeNow",
				Lat: p.Lat, Lng: p.Lng,
				Raw: map[string]any{"chargePointIds": p.ChargePointIDs},
			},
			Tariffs: tariffs,
		})
	}

	processed := 0
	for i := 0; i < len(normalized); i += ingestionBulkChunkSize {
		end := i + ingestionBulkChunkSize
		if end > len(normalized) {
			end = len(normalized)
		}
		// Decoupled from ctx (context.WithoutCancel), same as
		// izivia.go/freshmile.go's flush: this chunk is already fully
		// priced and collected in memory by this point, so once we've
		// committed to writing it, a SIGINT or the idle watchdog giving
		// up landing mid-query shouldn't be able to abort it.
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), chargenowFlushTimeout)
		n, err := writeSourceStationChunk(writeCtx, ing.Pool, ing.SourceStations, ing.Tariffs, ing.Links, ing.MaxLinkDistanceM, normalized[i:end])
		cancel()
		processed += n
		if err != nil {
			return processed, err
		}
		slog.Info("processing progress", "source", "chargenow", "processed", processed, "total", len(normalized))
	}
	// A batch's price fetches can fail on a mid-run ctx expiry without
	// that ever producing a non-nil err above (each failed price batch is
	// only logged and recorded, see recordFailedBatch) — so without
	// surfacing this, a truncated run could look fully successful to
	// consumePools/runPipeline, which would then let Run() sweep stations
	// this run never got to re-price. context.Cause (not plain ctx.Err())
	// so the caller sees *why* — e.g. "no successful request in the last
	// 5m0s..." — see idleWatchdog.
	if err := context.Cause(ctx); err != nil {
		return processed, err
	}
	return processed, nil
}

func chargenowPriceKey(chargePoint, powerType string, power float64) string {
	return fmt.Sprintf("%s|%s|%g", chargePoint, powerType, power)
}

// chargenowExtractPrices reads a price result's elements for the ENERGY
// (€/kWh, converted to cents) and FLAT (a one-time session fee, same
// concept as domain.StationTariff.SessionFeeCents — already used for
// Izivia's "2,3€ la session de charge") price components. Other OCPI
// component types (TIME, PARKING_TIME, ...) aren't modeled by
// StationTariff yet and are ignored rather than causing an error — that's
// display-lossy, not wrong: the energy price is still captured correctly.
func chargenowExtractPrices(result chargenowPriceResult) (energyCents, flatCents *float64) {
	for _, el := range result.Elements {
		for _, pc := range el.PriceComponents {
			switch pc.Type {
			case "ENERGY":
				v := pc.Price * 100
				energyCents = &v
			case "FLAT":
				v := pc.Price * 100
				flatCents = &v
			}
		}
	}
	return energyCents, flatCents
}

// chargenowPool's and chargenowBBox's JSON tags matter: a failed price
// fetch is persisted per affected pool, and a failed discovery query per
// bbox, in the failure log (see FailureLog) and read back by RetryFailed.
type chargenowPool struct {
	ID             string   `json:"id"`
	Lat            float64  `json:"lat"`
	Lng            float64  `json:"lng"`
	ChargePointIDs []string `json:"chargePointIds"`
}

type chargenowBBox struct {
	MinLng float64 `json:"minLng"`
	MinLat float64 `json:"minLat"`
	MaxLng float64 `json:"maxLng"`
	MaxLat float64 `json:"maxLat"`
}

// subdivideChargenowBBox splits a bbox into 4 quadrants — identical shape
// to freshmile.go's subdivideBBox, kept as its own small copy rather than
// sharing a type across files for two unrelated sources.
func subdivideChargenowBBox(b chargenowBBox) []chargenowBBox {
	midLng := (b.MinLng + b.MaxLng) / 2
	midLat := (b.MinLat + b.MaxLat) / 2
	return []chargenowBBox{
		{MinLng: b.MinLng, MinLat: b.MinLat, MaxLng: midLng, MaxLat: midLat},
		{MinLng: midLng, MinLat: b.MinLat, MaxLng: b.MaxLng, MaxLat: midLat},
		{MinLng: b.MinLng, MinLat: midLat, MaxLng: midLng, MaxLat: b.MaxLat},
		{MinLng: midLng, MinLat: midLat, MaxLng: b.MaxLng, MaxLat: b.MaxLat},
	}
}

// padDegenerateChargenowBBox guards against a zero-area bbox the same way
// freshmile.go's padDegenerateBBox does — a cluster's own reported
// bounding box can collapse to a single point on one axis (several
// stations at the exact same coordinate).
func padDegenerateChargenowBBox(b chargenowBBox) chargenowBBox {
	const minSpan = 0.0005
	if b.MaxLng-b.MinLng < minSpan {
		mid := (b.MinLng + b.MaxLng) / 2
		b.MinLng, b.MaxLng = mid-minSpan/2, mid+minSpan/2
	}
	if b.MaxLat-b.MinLat < minSpan {
		mid := (b.MinLat + b.MaxLat) / 2
		b.MinLat, b.MaxLat = mid-minSpan/2, mid+minSpan/2
	}
	return b
}

// scanPools grid-scans metropolitan France for ChargeNow pools, recursing
// into any cluster the API returns still-too-coarse (chargePointCount
// above what's resolvable at the requested precision) — same
// discover-then-subdivide shape as freshmile.go's scanLocationIDs, using
// chargePointCount/boundingBox* instead of location_count/properties.bbox.
// Discovered pools are sent onto poolCh as they're found (closing poolCh
// is the caller's job, once this returns), same streaming contract as
// freshmile.go's scanLocationIDs, so consumePools can start
// correlating/pricing/writing the first batch long before the whole map
// has been scanned.
func (ing *ChargenowIngester) scanPools(ctx context.Context, poolCh chan<- chargenowPool) {
	const step = chargenowGridStep
	minLng, maxLng := -5.5, 9.8
	minLat, maxLat := 41.0, 51.5

	var initial []chargenowBBox
	for lat := minLat; lat < maxLat; lat += step {
		for lng := minLng; lng < maxLng; lng += step {
			initial = append(initial, chargenowBBox{
				MinLng: lng, MinLat: lat,
				MaxLng: min(lng+step, maxLng), MaxLat: min(lat+step, maxLat),
			})
		}
	}
	// See izivia.go/freshmile.go's identical shuffle: without this, a
	// chronically-timing-out run always gets cut off at the same tail end
	// of this fixed grid order.
	rand.Shuffle(len(initial), func(i, j int) { initial[i], initial[j] = initial[j], initial[i] })

	ing.scanPoolsFrom(ctx, initial, poolCh)
}

// scanPoolsFrom is scanPools' engine, starting from an arbitrary list of
// initial boxes rather than always the full-France grid — RetryFailed
// reuses it to re-scan just the bboxes a previous run recorded as failed.
func (ing *ChargenowIngester) scanPoolsFrom(ctx context.Context, initial []chargenowBBox, poolCh chan<- chargenowPool) {
	var (
		mu   sync.Mutex
		seen = map[string]struct{}{}
	)
	var visited int64
	sem := make(chan struct{}, ing.workers())
	var wg sync.WaitGroup

	logDone := make(chan struct{})
	defer close(logDone)
	go func() {
		ticker := time.NewTicker(chargenowProgressLogInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				mu.Lock()
				found := len(seen)
				mu.Unlock()
				slog.Info("scanning map", "source", "chargenow", "tilesVisited", atomic.LoadInt64(&visited), "poolsFound", found)
			case <-logDone:
				return
			}
		}
	}()

	var scan func(bbox chargenowBBox, depth int)
	scan = func(bbox chargenowBBox, depth int) {
		defer wg.Done()
		if ctx.Err() != nil || atomic.LoadInt64(&visited) >= chargenowMaxTilesVisited {
			return
		}
		atomic.AddInt64(&visited, 1)

		bbox = padDegenerateChargenowBBox(bbox)
		resp, err := ing.fetchQuery(ctx, bbox, sem)
		if err != nil {
			slog.Warn("query bbox failed", "source", "chargenow", "bbox", fmt.Sprintf("%+v", bbox), "error", err)
			// A failed query drops its whole branch of the subdivision
			// tree — record it for a targeted retry, unless the scan is
			// just being canceled (see the same guard in izivia.go).
			if ctx.Err() == nil {
				ing.Failures.Record(failKindChargenowBBox, ing.BaseURL+chargenowQueryPath, bbox, err)
			}
			return
		}

		for _, p := range resp.Pools {
			mu.Lock()
			_, dup := seen[p.ID]
			if !dup {
				seen[p.ID] = struct{}{}
			}
			mu.Unlock()
			if dup {
				continue
			}
			ids := make([]string, len(p.ChargePoints))
			for i, cp := range p.ChargePoints {
				ids[i] = cp.ID
			}
			select {
			case poolCh <- chargenowPool{ID: p.ID, Lat: p.Latitude, Lng: p.Longitude, ChargePointIDs: ids}:
			case <-ctx.Done():
				return
			}
		}

		if depth >= chargenowMaxSubdivisionDepth {
			return
		}
		for _, c := range resp.PoolClusters {
			sub := chargenowBBox{
				MinLng: c.BoundingBoxLongitudeNW, MaxLat: c.BoundingBoxLatitudeNW,
				MaxLng: c.BoundingBoxLongitudeSE, MinLat: c.BoundingBoxLatitudeSE,
			}
			for _, q := range subdivideChargenowBBox(sub) {
				wg.Add(1)
				go scan(q, depth+1)
			}
		}
	}

	for _, tile := range initial {
		wg.Add(1)
		go scan(tile, 0)
	}
	wg.Wait()
}

func (ing *ChargenowIngester) workers() int {
	return effectiveWorkers(ing.Config.Workers, chargenowScanWorkers)
}

type chargenowQueryResponse struct {
	PoolClusters []chargenowPoolCluster `json:"poolClusters"`
	Pools        []chargenowRawPool     `json:"pools"`
}

type chargenowPoolCluster struct {
	ChargePointCount       int     `json:"chargePointCount"`
	Longitude              float64 `json:"longitude"`
	Latitude               float64 `json:"latitude"`
	BoundingBoxLongitudeNW float64 `json:"boundingBoxLongitudeNW"`
	BoundingBoxLatitudeNW  float64 `json:"boundingBoxLatitudeNW"`
	BoundingBoxLongitudeSE float64 `json:"boundingBoxLongitudeSE"`
	BoundingBoxLatitudeSE  float64 `json:"boundingBoxLatitudeSE"`
}

type chargenowRawPool struct {
	ID           string                    `json:"id"`
	Longitude    float64                   `json:"longitude"`
	Latitude     float64                   `json:"latitude"`
	ChargePoints []chargenowRawChargePoint `json:"chargePoints"`
}

type chargenowRawChargePoint struct {
	ID string `json:"id"`
}

type chargenowPriceQueryItem struct {
	ChargePoint string  `json:"charge_point"`
	PowerType   string  `json:"power_type"`
	Power       float64 `json:"power"`
}

type chargenowPriceResult struct {
	Currency        string                  `json:"currency"`
	Elements        []chargenowPriceElement `json:"elements"`
	PriceIdentifier chargenowPriceQueryItem `json:"price_identifier"`
}

type chargenowPriceElement struct {
	PriceComponents []chargenowPriceComponent `json:"price_components"`
}

type chargenowPriceComponent struct {
	Type  string  `json:"type"`
	Price float64 `json:"price"`
}

// fetchQuery POSTs the discovery payload for one bbox — searchCriteria
// field names/shape and filterCriteria's empty-array defaults match the
// working reference request exactly (precision 3, unpackSolitudeCluster
// false, unpackClustersWithSinglePool true).
func (ing *ChargenowIngester) fetchQuery(ctx context.Context, bbox chargenowBBox, sem chan struct{}) (*chargenowQueryResponse, error) {
	payload := map[string]any{
		"searchCriteria": map[string]any{
			"latitudeNW":                   bbox.MaxLat,
			"longitudeNW":                  bbox.MinLng,
			"latitudeSE":                   bbox.MinLat,
			"longitudeSE":                  bbox.MaxLng,
			"precision":                    chargenowPrecision,
			"unpackSolitudeCluster":        false,
			"unpackClustersWithSinglePool": true,
		},
		"withChargePointIds": true,
		"filterCriteria": map[string]any{
			"authenticationMethods": []any{},
			"cableAttachedTypes":    []any{},
			"paymentMethods":        []any{},
			"plugTypes":             []any{},
			"poolLocationTypes":     []any{},
			"valueAddedServices":    []any{},
			"dcsTcpoIds":            []any{},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal chargenow query payload: %w", err)
	}

	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	respBody, err := ing.withRetries(ctx, ing.BaseURL+chargenowQueryPath, func() ([]byte, int, error) {
		return ing.doRequest(ctx, ing.BaseURL+chargenowQueryPath, "clusters", body)
	})
	if err != nil {
		return nil, err
	}
	var resp chargenowQueryResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("decode chargenow query response: %w", err)
	}
	return &resp, nil
}

// withRetries retries do (one HTTP attempt) on a transient failure — see
// the shared withRetries in common.go.
func (ing *ChargenowIngester) withRetries(ctx context.Context, label string, do func() ([]byte, int, error)) ([]byte, error) {
	return withRetries(ctx, "chargenow", label, defaultMaxRetries, ing.retryBackoff, do)
}

// doRequest performs a single HTTP request with ChargeNow's WAF-required
// headers. restAPIPath is a routing key, not a path-derived label: the
// single /api/map/v1/fr/... facade dispatches to a completely different
// internal microservice (and body/response schema) depending on its value
// — confirmed against real traffic: "clusters" routes searchCriteria bbox
// queries to /poi-static-data/v1/poi-cluster-search (what /query uses for
// discovery, see fetchQuery), "charge-points" routes
// DCSChargePointDynStatusRequest queries to
// /poi-availability/v2/charge-points/query, and "pools" routes dcsPoolIds
// lookups elsewhere — none of which this ingester needs beyond discovery.
//
// /tariffs/CHARGENOW_PRIME/prices takes no rest-api-path header at all — a
// previous guess set it to "prices" (following the same "last meaningful
// path segment" pattern as "clusters"), which in fact routed every price
// request to the cluster-search microservice instead, which rejected it
// (its payload has no searchCriteria to validate) and quietly broke price
// fetching for every run. restAPIPath == "" (see the prices call site in
// processPoolBatch) leaves the header unset entirely.
func (ing *ChargenowIngester) doRequest(ctx context.Context, url, restAPIPath string, body []byte) ([]byte, int, error) {
	// Paces this request against every other one this ingester is making
	// concurrently — see chargenowMinRequestInterval's doc comment: this,
	// not chargenowScanWorkers, is what actually stops a burst of parallel
	// requests from getting the whole run's IP blocked by ChargeNow's WAF.
	if err := ing.limiter.Wait(ctx); err != nil {
		return nil, 0, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, 0, fmt.Errorf("build request for %s: %w", url, err)
	}
	for k, v := range chargenowHeaders {
		req.Header.Set(k, v)
	}
	if restAPIPath != "" {
		req.Header.Set("rest-api-path", restAPIPath)
	}
	resp, err := ing.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("chargenow request to %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		// errorBodySummary (izivia.go) trims to the first non-empty line,
		// capped — generically useful for any non-2xx body, not specific
		// to Izivia's Java stack traces.
		return nil, resp.StatusCode, fmt.Errorf("chargenow http %d for %s: %s", resp.StatusCode, url, errorBodySummary(data))
	}
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("chargenow read body from %s: %w", url, err)
	}
	ing.idle.Ping()
	return respBody, resp.StatusCode, nil
}
