package ingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"opencharge/internal/domain"
	"opencharge/internal/repository"
)

// chargenowQueryFixture is the exact response shape from a real ChargeNow
// /query call (trimmed to the fields this ingester reads), used to pin
// decoding against the real API's field names/casing.
const chargenowQueryFixture = `{
    "poolClusters": [
        {
            "chargePointCount": 30,
            "longitude": 6.13212906382978,
            "latitude": 45.90709455125034,
            "boundingBoxLongitudeNW": 6.13037109375,
            "boundingBoxLatitudeNW": 45.911865234375,
            "boundingBoxLongitudeSE": 6.141357421875,
            "boundingBoxLatitudeSE": 45.9063720703125
        }
    ],
    "pools": [
        {
            "longitude": 6.125093936920166,
            "latitude": 45.92610168457031,
            "id": "FR:DCS:POOL:7f89d307-1798-3637-8c42-050dc6a29a08",
            "dcsTechnicalChargePointOperatorId": "FR:DCS:TECH_CHARGE_POINT_OPERATOR:3113ad27-2eea-3cc5-816f-80cdef10a3d2",
            "preferredPartnerStatus": false,
            "chargePointCount": 2,
            "chargePoints": [
                {"id": "FR:DCS:CHARGE_POINT:122b1d09-e012-3acc-9141-76dc44b0cf85"},
                {"id": "FR:DCS:CHARGE_POINT:27831a7e-d0cb-3d7c-9c0e-9ad85b66bfb3"}
            ]
        }
    ]
}`

func TestChargenowQueryResponseDecode(t *testing.T) {
	var resp chargenowQueryResponse
	if err := json.Unmarshal([]byte(chargenowQueryFixture), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.PoolClusters) != 1 {
		t.Fatalf("got %d poolClusters, want 1", len(resp.PoolClusters))
	}
	c := resp.PoolClusters[0]
	if c.ChargePointCount != 30 {
		t.Errorf("ChargePointCount = %d, want 30", c.ChargePointCount)
	}
	if c.BoundingBoxLongitudeNW != 6.13037109375 || c.BoundingBoxLatitudeSE != 45.9063720703125 {
		t.Errorf("cluster bbox = %+v, want NW/SE corners from fixture", c)
	}

	if len(resp.Pools) != 1 {
		t.Fatalf("got %d pools, want 1", len(resp.Pools))
	}
	p := resp.Pools[0]
	if p.ID != "FR:DCS:POOL:7f89d307-1798-3637-8c42-050dc6a29a08" {
		t.Errorf("pool id = %q, want the fixture's id", p.ID)
	}
	if len(p.ChargePoints) != 2 {
		t.Fatalf("got %d chargePoints, want 2", len(p.ChargePoints))
	}
	if p.ChargePoints[0].ID != "FR:DCS:CHARGE_POINT:122b1d09-e012-3acc-9141-76dc44b0cf85" {
		t.Errorf("chargePoints[0].ID = %q, want the fixture's first id", p.ChargePoints[0].ID)
	}
}

// chargenowPriceFixture is a real /tariffs/CHARGENOW_PRIME/prices response
// element: an ENERGY component (€/kWh) and a FLAT component (a one-time
// session fee) as two separate elements.
const chargenowPriceFixture = `{
    "id": "FR505",
    "power_type": "AC_1_PHASE",
    "currency": "EUR",
    "elements": [
        {
            "price_components": [
                {"type": "ENERGY", "price": 0.32, "step_size": 1}
            ],
            "restrictions": {}
        },
        {
            "price_components": [
                {"type": "FLAT", "price": 0.27, "step_size": 0}
            ],
            "restrictions": {}
        }
    ],
    "last_updated": 1784299583087,
    "price_identifier": {
        "charge_point": "FR:DCS:CHARGE_POINT:a0ed2cbc-33d7-3f05-ba4d-3d54df1b2cb8",
        "power_type": "AC",
        "power": 11
    }
}`

func TestChargenowExtractPrices(t *testing.T) {
	var result chargenowPriceResult
	if err := json.Unmarshal([]byte(chargenowPriceFixture), &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.PriceIdentifier.ChargePoint != "FR:DCS:CHARGE_POINT:a0ed2cbc-33d7-3f05-ba4d-3d54df1b2cb8" {
		t.Errorf("price_identifier.charge_point = %q, want the fixture's charge point", result.PriceIdentifier.ChargePoint)
	}

	energyCents, flatCents := chargenowExtractPrices(result)
	if energyCents == nil || *energyCents != 32.0 {
		t.Errorf("energyCents = %v, want 32.0 (0.32€ * 100)", energyCents)
	}
	if flatCents == nil || *flatCents != 27.0 {
		t.Errorf("flatCents = %v, want 27.0 (0.27€ * 100)", flatCents)
	}
}

func TestChargenowExtractPrices_NoComponents(t *testing.T) {
	energyCents, flatCents := chargenowExtractPrices(chargenowPriceResult{})
	if energyCents != nil || flatCents != nil {
		t.Errorf("extractPrices(empty) = (%v, %v), want (nil, nil)", energyCents, flatCents)
	}
}

func TestSubdivideChargenowBBox(t *testing.T) {
	quads := subdivideChargenowBBox(chargenowBBox{MinLng: 0, MinLat: 0, MaxLng: 2, MaxLat: 2})
	if len(quads) != 4 {
		t.Fatalf("got %d quadrants, want 4", len(quads))
	}
	// Union of the 4 quadrants must reconstruct the original bbox exactly,
	// and each must be a strict quarter of the original area (no overlap,
	// no gap) — this is what bounds worst-case subdivision depth.
	for _, q := range quads {
		width := q.MaxLng - q.MinLng
		height := q.MaxLat - q.MinLat
		if width != 1 || height != 1 {
			t.Errorf("quadrant %+v has size %gx%g, want 1x1", q, width, height)
		}
	}
}

func TestChargenowPriceKey_DistinguishesPowerType(t *testing.T) {
	acKey := chargenowPriceKey("FR:DCS:CHARGE_POINT:x", "AC", 11)
	dcKey := chargenowPriceKey("FR:DCS:CHARGE_POINT:x", "DC", 11)
	if acKey == dcKey {
		t.Errorf("chargenowPriceKey should differ by power_type, got %q for both", acKey)
	}
}

// TestChargenowIngester_Run exercises the full pipeline against a fake
// ChargeNow server and a real Postgres test database: discovery (a single
// pool, no clusters to subdivide), correlation to a co-located IRVE
// station (kind-aware, like the AC/DC co-location fix in linking_test.go),
// price fetch, and the resulting tariff written to the correlated station.
func TestChargenowIngester_Run(t *testing.T) {
	pool := setupLinkingTestPool(t)
	ctx := context.Background()
	stationRepo := repository.NewStationRepository(pool)
	sourceStationRepo := repository.NewSourceStationRepository(pool)
	tariffRepo := repository.NewTariffRepository(pool)
	linkRepo := repository.NewLinkRepository(pool)

	dcStation := testIRVEStation("FRCNDC0001", 45.9000, 6.1000, domain.ConnectorTypeCCS)
	dcStationID, err := stationRepo.UpsertStation(ctx, dcStation)
	if err != nil {
		t.Fatalf("UpsertStation dc: %v", err)
	}

	const poolLat, poolLng = 45.9000, 6.1000
	const chargePointID = "FR:DCS:CHARGE_POINT:test0001"

	queryResp := chargenowQueryResponse{
		Pools: []chargenowRawPool{
			{
				ID: "FR:DCS:POOL:test0001", Latitude: poolLat, Longitude: poolLng,
				ChargePoints: []chargenowRawChargePoint{{ID: chargePointID}},
			},
		},
	}

	var priceRequests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case chargenowQueryPath:
			// Confirmed against real traffic: ChargeNow's /query facade
			// routes a searchCriteria (bbox) discovery request to its
			// cluster-search backend only when rest-api-path is exactly
			// "clusters" — anything else (or a different endpoint's own
			// value bleeding in here) routes to the wrong microservice.
			if got := r.Header.Get("rest-api-path"); got != "clusters" {
				t.Errorf("query request rest-api-path = %q, want clusters", got)
			}
			_ = json.NewEncoder(w).Encode(queryResp)
		case chargenowPricesPath:
			// Pins a real production bug: /tariffs/.../prices takes no
			// rest-api-path header at all. Setting one (previously
			// "prices", following the same pattern as the query
			// endpoint's "clusters") in fact routed every price request
			// to the cluster-search backend instead, which rejected it
			// outright — silently breaking price fetching on every run.
			if got := r.Header.Get("rest-api-path"); got != "" {
				t.Errorf("prices request rest-api-path = %q, want no header at all", got)
			}
			priceRequests++
			var items []chargenowPriceQueryItem
			if err := json.NewDecoder(r.Body).Decode(&items); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			results := make([]chargenowPriceResult, 0, len(items))
			for _, it := range items {
				if it.PowerType != "DC" {
					continue // this pool only correlates to a dc IRVE station
				}
				results = append(results, chargenowPriceResult{
					Currency: "EUR",
					Elements: []chargenowPriceElement{
						{PriceComponents: []chargenowPriceComponent{{Type: "ENERGY", Price: 0.45}}},
						{PriceComponents: []chargenowPriceComponent{{Type: "FLAT", Price: 0.20}}},
					},
					PriceIdentifier: it,
				})
			}
			_ = json.NewEncoder(w).Encode(results)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	ing := NewChargenowIngester(pool, sourceStationRepo, tariffRepo, linkRepo, srv.URL, ChargenowConfig{Workers: 2})
	ing.limiter = newChargenowRateLimiter(time.Microsecond)
	ing.retryBackoff = time.Millisecond

	n, err := ing.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n != 1 {
		t.Fatalf("Run() = %d stations processed, want 1", n)
	}
	if priceRequests == 0 {
		t.Fatal("no price requests were made")
	}

	tariffs, err := tariffRepo.ListByStation(ctx, dcStationID)
	if err != nil {
		t.Fatalf("ListByStation: %v", err)
	}
	if len(tariffs) != 1 {
		t.Fatalf("got %d tariffs, want 1 (dc only — no ac IRVE match nearby)", len(tariffs))
	}
	tf := tariffs[0]
	if tf.Source != "chargenow" {
		t.Errorf("tariff source = %q, want chargenow", tf.Source)
	}
	if tf.Kind != domain.TariffKindDC {
		t.Errorf("tariff kind = %q, want dc", tf.Kind)
	}
	if tf.EnergyPriceCentsPerKWh == nil || *tf.EnergyPriceCentsPerKWh != 45.0 {
		t.Errorf("energy price = %v, want 45.0", tf.EnergyPriceCentsPerKWh)
	}
	if tf.SessionFeeCents == nil || *tf.SessionFeeCents != 20.0 {
		t.Errorf("session fee = %v, want 20.0", tf.SessionFeeCents)
	}
}

// TestChargenowIngester_RunStreamsMultipleBatches pins the streaming
// pipeline refactor (see runPipeline/consumePools/processPoolBatch): more
// pools than fit in a single chargenowPoolBatchSize batch must still all
// get correlated, priced, and written — not silently dropped or blocked —
// and doing so must take more than one price request, proving pools are
// processed incrementally in batches as they're discovered rather than
// only once the whole map has been scanned.
func TestChargenowIngester_RunStreamsMultipleBatches(t *testing.T) {
	pool := setupLinkingTestPool(t)
	ctx := context.Background()
	stationRepo := repository.NewStationRepository(pool)
	sourceStationRepo := repository.NewSourceStationRepository(pool)
	tariffRepo := repository.NewTariffRepository(pool)
	linkRepo := repository.NewLinkRepository(pool)

	const numPools = chargenowPoolBatchSize + 50 // forces at least 2 batches

	var queryResp chargenowQueryResponse
	stationIDs := make([]uuid.UUID, numPools)
	for i := 0; i < numPools; i++ {
		// 0.02 degrees apart (~2km at these latitudes) keeps every station
		// far enough from its neighbors that a pool only ever links to its
		// own station within MaxLinkDistanceM.
		lat := 45.0 + float64(i)*0.02
		lng := 6.0
		irveID := fmt.Sprintf("FRCNBATCH%04d", i)
		id, err := stationRepo.UpsertStation(ctx, testIRVEStation(irveID, lat, lng, domain.ConnectorTypeCCS))
		if err != nil {
			t.Fatalf("UpsertStation %d: %v", i, err)
		}
		stationIDs[i] = id

		poolID := fmt.Sprintf("FR:DCS:POOL:batch%04d", i)
		chargePointID := fmt.Sprintf("FR:DCS:CHARGE_POINT:batch%04d", i)
		queryResp.Pools = append(queryResp.Pools, chargenowRawPool{
			ID: poolID, Latitude: lat, Longitude: lng,
			ChargePoints: []chargenowRawChargePoint{{ID: chargePointID}},
		})
	}

	var priceBatches int32
	var mu sync.Mutex
	pricedItems := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case chargenowQueryPath:
			_ = json.NewEncoder(w).Encode(queryResp)
		case chargenowPricesPath:
			atomic.AddInt32(&priceBatches, 1)
			var items []chargenowPriceQueryItem
			if err := json.NewDecoder(r.Body).Decode(&items); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			results := make([]chargenowPriceResult, 0, len(items))
			for _, it := range items {
				if it.PowerType != "DC" {
					continue
				}
				mu.Lock()
				pricedItems[it.ChargePoint] = true
				mu.Unlock()
				results = append(results, chargenowPriceResult{
					Currency: "EUR",
					Elements: []chargenowPriceElement{
						{PriceComponents: []chargenowPriceComponent{{Type: "ENERGY", Price: 0.40}}},
					},
					PriceIdentifier: it,
				})
			}
			_ = json.NewEncoder(w).Encode(results)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	ing := NewChargenowIngester(pool, sourceStationRepo, tariffRepo, linkRepo, srv.URL, ChargenowConfig{Workers: 4})
	ing.limiter = newChargenowRateLimiter(time.Microsecond)
	ing.retryBackoff = time.Millisecond

	n, err := ing.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n != numPools {
		t.Fatalf("Run() = %d stations processed, want %d", n, numPools)
	}
	if got := atomic.LoadInt32(&priceBatches); got < 2 {
		t.Errorf("price batches = %d, want >= 2 (proving %d pools split across multiple chargenowPoolBatchSize=%d batches)", got, numPools, chargenowPoolBatchSize)
	}
	if len(pricedItems) != numPools {
		t.Errorf("priced %d distinct charge points, want %d (none dropped/duplicated across batches)", len(pricedItems), numPools)
	}

	for i, sid := range stationIDs {
		tariffs, err := tariffRepo.ListByStation(ctx, sid)
		if err != nil {
			t.Fatalf("ListByStation %d: %v", i, err)
		}
		if len(tariffs) != 1 {
			t.Fatalf("station %d: got %d tariffs, want 1", i, len(tariffs))
		}
	}
}

// TestChargenowIngester_RunKeepsAlreadyWrittenBatchesOnCancellation pins
// the durability property the streaming pipeline exists for: a run
// canceled partway through (SIGINT, the idle watchdog giving up) must
// keep whatever batch(es) were already correlated/priced/written, not
// lose everything gathered so far the way the old discover-everything-
// then-price-everything-then-write-everything pipeline would have.
//
// consumePools is single-threaded (one batch at a time), so by the time
// the second batch's price request reaches the test server, the first
// batch's database write has already completed synchronously — this test
// relies on that ordering to cancel ctx at exactly that point, deterministically.
func TestChargenowIngester_RunKeepsAlreadyWrittenBatchesOnCancellation(t *testing.T) {
	pool := setupLinkingTestPool(t)
	bgCtx := context.Background()
	stationRepo := repository.NewStationRepository(pool)
	sourceStationRepo := repository.NewSourceStationRepository(pool)
	tariffRepo := repository.NewTariffRepository(pool)
	linkRepo := repository.NewLinkRepository(pool)

	const numPools = chargenowPoolBatchSize + 50 // forces at least 2 batches

	var queryResp chargenowQueryResponse
	stationIDs := make([]uuid.UUID, numPools)
	for i := 0; i < numPools; i++ {
		lat := 46.0 + float64(i)*0.02
		lng := 6.0
		irveID := fmt.Sprintf("FRCNCANCEL%04d", i)
		id, err := stationRepo.UpsertStation(bgCtx, testIRVEStation(irveID, lat, lng, domain.ConnectorTypeCCS))
		if err != nil {
			t.Fatalf("UpsertStation %d: %v", i, err)
		}
		stationIDs[i] = id

		poolID := fmt.Sprintf("FR:DCS:POOL:cancel%04d", i)
		chargePointID := fmt.Sprintf("FR:DCS:CHARGE_POINT:cancel%04d", i)
		queryResp.Pools = append(queryResp.Pools, chargenowRawPool{
			ID: poolID, Latitude: lat, Longitude: lng,
			ChargePoints: []chargenowRawChargePoint{{ID: chargePointID}},
		})
	}

	runCtx, cancelRun := context.WithCancel(bgCtx)
	defer cancelRun()

	var priceBatches int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case chargenowQueryPath:
			_ = json.NewEncoder(w).Encode(queryResp)
		case chargenowPricesPath:
			n := atomic.AddInt32(&priceBatches, 1)
			if n >= 2 {
				// consumePools only reaches a second batch's price
				// request after the first batch's write has already
				// completed (single-threaded consumer) — cancel now and
				// answer nothing, simulating a SIGINT/idle-timeout
				// landing exactly between batches.
				cancelRun()
				time.Sleep(50 * time.Millisecond)
				return
			}
			var items []chargenowPriceQueryItem
			if err := json.NewDecoder(r.Body).Decode(&items); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			results := make([]chargenowPriceResult, 0, len(items))
			for _, it := range items {
				if it.PowerType != "DC" {
					continue
				}
				results = append(results, chargenowPriceResult{
					Currency: "EUR",
					Elements: []chargenowPriceElement{
						{PriceComponents: []chargenowPriceComponent{{Type: "ENERGY", Price: 0.40}}},
					},
					PriceIdentifier: it,
				})
			}
			_ = json.NewEncoder(w).Encode(results)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	ing := NewChargenowIngester(pool, sourceStationRepo, tariffRepo, linkRepo, srv.URL, ChargenowConfig{Workers: 4})
	ing.limiter = newChargenowRateLimiter(time.Microsecond)
	ing.retryBackoff = time.Millisecond

	n, err := ing.Run(runCtx)
	if err == nil {
		t.Fatal("Run() = nil error, want an error surfacing the mid-run cancellation")
	}
	if n != chargenowPoolBatchSize {
		t.Fatalf("Run() = %d stations processed, want exactly %d (the first batch only)", n, chargenowPoolBatchSize)
	}

	// The first batch's stations must be durably written despite the
	// cancellation — verified with a fresh, non-canceled context since
	// runCtx is dead by now.
	for i := 0; i < chargenowPoolBatchSize; i++ {
		tariffs, tErr := tariffRepo.ListByStation(bgCtx, stationIDs[i])
		if tErr != nil {
			t.Fatalf("ListByStation %d (first batch): %v", i, tErr)
		}
		if len(tariffs) != 1 {
			t.Errorf("station %d (first batch): got %d tariffs, want 1 (should have survived the cancellation)", i, len(tariffs))
		}
	}
}

// TestChargenowIngester_RetryFailedCombinesDirectPoolsAndRescannedBBoxes
// pins RetryFailed's two recovery paths together: a pool recorded
// directly (its price batch failed last run) is fed straight into the
// pipeline, and a bbox recorded as failed (its discovery query failed
// last run) is re-scanned to recover the pool(s) it covers — both must
// end up correlated, priced, and written by the same streamed pipeline
// RetryFailed shares with Run.
func TestChargenowIngester_RetryFailedCombinesDirectPoolsAndRescannedBBoxes(t *testing.T) {
	pool := setupLinkingTestPool(t)
	ctx := context.Background()
	stationRepo := repository.NewStationRepository(pool)
	sourceStationRepo := repository.NewSourceStationRepository(pool)
	tariffRepo := repository.NewTariffRepository(pool)
	linkRepo := repository.NewLinkRepository(pool)

	directStationID, err := stationRepo.UpsertStation(ctx, testIRVEStation("FRCNRETRYDIRECT", 45.9000, 6.1000, domain.ConnectorTypeCCS))
	if err != nil {
		t.Fatalf("UpsertStation direct: %v", err)
	}
	rescannedStationID, err := stationRepo.UpsertStation(ctx, testIRVEStation("FRCNRETRYSCAN", 46.2000, 6.3000, domain.ConnectorTypeCCS))
	if err != nil {
		t.Fatalf("UpsertStation rescanned: %v", err)
	}

	directPool := chargenowPool{
		ID: "FR:DCS:POOL:retry-direct", Lat: 45.9000, Lng: 6.1000,
		ChargePointIDs: []string{"FR:DCS:CHARGE_POINT:retry-direct"},
	}
	rescanBBox := chargenowBBox{MinLng: 6.2, MinLat: 46.1, MaxLng: 6.4, MaxLat: 46.3}
	rescanQueryResp := chargenowQueryResponse{
		Pools: []chargenowRawPool{
			{
				ID: "FR:DCS:POOL:retry-rescanned", Latitude: 46.2000, Longitude: 6.3000,
				ChargePoints: []chargenowRawChargePoint{{ID: "FR:DCS:CHARGE_POINT:retry-rescanned"}},
			},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case chargenowQueryPath:
			// Only the re-scanned bbox's own discovery query should ever
			// hit this ingester during RetryFailed — a direct pool must
			// not trigger a fresh full-France scan.
			_ = json.NewEncoder(w).Encode(rescanQueryResp)
		case chargenowPricesPath:
			var items []chargenowPriceQueryItem
			if err := json.NewDecoder(r.Body).Decode(&items); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			results := make([]chargenowPriceResult, 0, len(items))
			for _, it := range items {
				if it.PowerType != "DC" {
					continue
				}
				results = append(results, chargenowPriceResult{
					Currency: "EUR",
					Elements: []chargenowPriceElement{
						{PriceComponents: []chargenowPriceComponent{{Type: "ENERGY", Price: 0.50}}},
					},
					PriceIdentifier: it,
				})
			}
			_ = json.NewEncoder(w).Encode(results)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	ing := NewChargenowIngester(pool, sourceStationRepo, tariffRepo, linkRepo, srv.URL, ChargenowConfig{Workers: 2})
	ing.limiter = newChargenowRateLimiter(time.Microsecond)
	ing.retryBackoff = time.Millisecond

	directPoolJSON, err := json.Marshal(directPool)
	if err != nil {
		t.Fatalf("marshal directPool: %v", err)
	}
	bboxJSON, err := json.Marshal(rescanBBox)
	if err != nil {
		t.Fatalf("marshal rescanBBox: %v", err)
	}
	failures := []FailedFetch{
		{Source: "chargenow", Kind: failKindChargenowPool, Params: directPoolJSON, Error: "http 502"},
		{Source: "chargenow", Kind: failKindChargenowBBox, Params: bboxJSON, Error: "http 504"},
	}

	n, err := ing.RetryFailed(ctx, failures)
	if err != nil {
		t.Fatalf("RetryFailed: %v", err)
	}
	if n != 2 {
		t.Fatalf("RetryFailed() = %d stations processed, want 2", n)
	}

	for name, sid := range map[string]uuid.UUID{"direct": directStationID, "rescanned": rescannedStationID} {
		tariffs, tErr := tariffRepo.ListByStation(ctx, sid)
		if tErr != nil {
			t.Fatalf("ListByStation %s: %v", name, tErr)
		}
		if len(tariffs) != 1 {
			t.Fatalf("station %s: got %d tariffs, want 1", name, len(tariffs))
		}
		if tariffs[0].EnergyPriceCentsPerKWh == nil || *tariffs[0].EnergyPriceCentsPerKWh != 50.0 {
			t.Errorf("station %s: energy price = %v, want 50.0", name, tariffs[0].EnergyPriceCentsPerKWh)
		}
	}
}
