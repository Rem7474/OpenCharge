package ingestion

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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
			_ = json.NewEncoder(w).Encode(queryResp)
		case chargenowPricesPath:
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
