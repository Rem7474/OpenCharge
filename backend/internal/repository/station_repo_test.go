package repository

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"opencharge/internal/domain"
)

func testStation(irveIDPDC string, lat, lng float64) domain.Station {
	power := 50.0
	return domain.Station{
		IRVEIDPDC:      irveIDPDC,
		OperatorName:   "Izivia",
		Enseigne:       "Izivia",
		Name:           "Station " + irveIDPDC,
		AddressCity:    "Annecy",
		AddressCountry: "FR",
		Lat:            lat,
		Lng:            lng,
		PowerKW:        &power,
		ConnectorType:  "CCS",
		AccessType:     "paid",
		Metadata:       map[string]any{"raw_field": "value"},
	}
}

func TestStationRepository_UpsertAndGet(t *testing.T) {
	pool := setupTestPool(t)
	ctx := context.Background()
	repo := NewStationRepository(pool)

	station := testStation("FRTEST0001", 45.9, 6.1)
	id, err := repo.UpsertStation(ctx, station)
	if err != nil {
		t.Fatalf("UpsertStation: %v", err)
	}
	if id.String() == "" {
		t.Fatal("UpsertStation returned a zero UUID")
	}

	got, err := repo.GetByIRVEID(ctx, "FRTEST0001")
	if err != nil {
		t.Fatalf("GetByIRVEID: %v", err)
	}
	if got == nil {
		t.Fatal("GetByIRVEID returned nil, want the upserted station")
	}
	if got.Name != "Station FRTEST0001" || got.OperatorName != "Izivia" {
		t.Errorf("unexpected station: %+v", got)
	}
	if got.Lat != 45.9 || got.Lng != 6.1 {
		t.Errorf("unexpected location: (%v, %v)", got.Lat, got.Lng)
	}
	if got.Metadata["raw_field"] != "value" {
		t.Errorf("Metadata = %v, want raw_field=value", got.Metadata)
	}

	// Upserting again with the same irve_id_pdc must update in place, not
	// create a duplicate row.
	updated := testStation("FRTEST0001", 45.9, 6.1)
	updated.Name = "Station Renamed"
	updatedID, err := repo.UpsertStation(ctx, updated)
	if err != nil {
		t.Fatalf("UpsertStation (update): %v", err)
	}
	if updatedID != id {
		t.Errorf("UpsertStation changed the id on update: got %v, want %v", updatedID, id)
	}
	got, err = repo.GetByIRVEID(ctx, "FRTEST0001")
	if err != nil {
		t.Fatalf("GetByIRVEID after update: %v", err)
	}
	if got.Name != "Station Renamed" {
		t.Errorf("Name = %q, want %q", got.Name, "Station Renamed")
	}
}

func TestStationRepository_BulkUpsertStations(t *testing.T) {
	pool := setupTestPool(t)
	ctx := context.Background()
	repo := NewStationRepository(pool)

	stations := []domain.Station{
		testStation("FRBULK0001", 45.90, 6.10),
		testStation("FRBULK0002", 45.91, 6.11),
		testStation("FRBULK0003", 45.92, 6.12),
	}
	if err := repo.BulkUpsertStations(ctx, stations); err != nil {
		t.Fatalf("BulkUpsertStations: %v", err)
	}

	for _, s := range stations {
		got, err := repo.GetByIRVEID(ctx, s.IRVEIDPDC)
		if err != nil {
			t.Fatalf("GetByIRVEID(%s): %v", s.IRVEIDPDC, err)
		}
		if got == nil {
			t.Fatalf("GetByIRVEID(%s) = nil, want the bulk-upserted station", s.IRVEIDPDC)
		}
	}

	// Re-upserting the same rows (e.g. a re-ingestion run) must update in
	// place, not create duplicates or error out.
	renamed := stations
	renamed[0].Name = "Renamed via bulk upsert"
	if err := repo.BulkUpsertStations(ctx, renamed); err != nil {
		t.Fatalf("BulkUpsertStations (update): %v", err)
	}
	got, err := repo.GetByIRVEID(ctx, "FRBULK0001")
	if err != nil {
		t.Fatalf("GetByIRVEID after update: %v", err)
	}
	if got.Name != "Renamed via bulk upsert" {
		t.Errorf("Name = %q, want %q", got.Name, "Renamed via bulk upsert")
	}
}

func TestStationRepository_BulkUpsertStations_Empty(t *testing.T) {
	pool := setupTestPool(t)
	repo := NewStationRepository(pool)
	if err := repo.BulkUpsertStations(context.Background(), nil); err != nil {
		t.Errorf("BulkUpsertStations(nil) = %v, want no error", err)
	}
}

func TestStationRepository_GetByIRVEID_NotFound(t *testing.T) {
	pool := setupTestPool(t)
	repo := NewStationRepository(pool)

	got, err := repo.GetByIRVEID(context.Background(), "does-not-exist")
	if err != nil {
		t.Fatalf("GetByIRVEID: %v", err)
	}
	if got != nil {
		t.Errorf("GetByIRVEID = %+v, want nil", got)
	}
}

func TestStationRepository_ListByOperatorLike(t *testing.T) {
	pool := setupTestPool(t)
	ctx := context.Background()
	repo := NewStationRepository(pool)

	viaOperator := testStation("FROPLIKE01", 45.9, 6.1)
	viaOperator.OperatorName = "FASTNED"
	viaOperator.Enseigne = "FASTNED"
	if _, err := repo.UpsertStation(ctx, viaOperator); err != nil {
		t.Fatalf("UpsertStation viaOperator: %v", err)
	}

	// Matched via enseigne, not operator_name, and with different casing —
	// IRVE data isn't consistent about which column carries a network's
	// brand name for a given station.
	viaEnseigne := testStation("FROPLIKE02", 45.91, 6.11)
	viaEnseigne.OperatorName = "Some Legal Entity SAS"
	viaEnseigne.Enseigne = "Fastned"
	if _, err := repo.UpsertStation(ctx, viaEnseigne); err != nil {
		t.Fatalf("UpsertStation viaEnseigne: %v", err)
	}

	other := testStation("FROPLIKE03", 45.92, 6.12)
	other.OperatorName = "Electra"
	other.Enseigne = "Electra"
	if _, err := repo.UpsertStation(ctx, other); err != nil {
		t.Fatalf("UpsertStation other: %v", err)
	}

	got, err := repo.ListByOperatorLike(ctx, "fastned")
	if err != nil {
		t.Fatalf("ListByOperatorLike: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListByOperatorLike(\"fastned\") = %d stations, want 2", len(got))
	}
	ids := map[string]bool{}
	for _, s := range got {
		ids[s.IRVEIDPDC] = true
	}
	if !ids["FROPLIKE01"] || !ids["FROPLIKE02"] {
		t.Errorf("ListByOperatorLike(\"fastned\") = %v, want FROPLIKE01 and FROPLIKE02", ids)
	}
}

func TestStationRepository_ListByBBox(t *testing.T) {
	pool := setupTestPool(t)
	ctx := context.Background()
	repo := NewStationRepository(pool)

	// Inside the bbox we query below.
	insideA := testStation("FRBBOX0001", 45.90, 6.10)
	insideA.OperatorName = "Izivia"
	insideB := testStation("FRBBOX0002", 45.91, 6.12)
	insideB.OperatorName = "Electra"
	// Far outside.
	outside := testStation("FRBBOX0003", 48.85, 2.35)

	for _, s := range []domain.Station{insideA, insideB, outside} {
		if _, err := repo.UpsertStation(ctx, s); err != nil {
			t.Fatalf("UpsertStation(%s): %v", s.IRVEIDPDC, err)
		}
	}

	bbox := domain.StationFilter{MinLng: 6.0, MinLat: 45.8, MaxLng: 6.3, MaxLat: 46.0}
	results, err := repo.ListByBBox(ctx, bbox)
	if err != nil {
		t.Fatalf("ListByBBox: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d stations in bbox, want 2", len(results))
	}
	ids := map[string]bool{}
	for _, r := range results {
		ids[r.Station.IRVEIDPDC] = true
	}
	if !ids["FRBBOX0001"] || !ids["FRBBOX0002"] {
		t.Errorf("unexpected stations in bbox result: %v", ids)
	}

	// Operator filter narrows it down to one.
	withOperator := bbox
	withOperator.Operator = "Electra"
	results, err = repo.ListByBBox(ctx, withOperator)
	if err != nil {
		t.Fatalf("ListByBBox with operator filter: %v", err)
	}
	if len(results) != 1 || results[0].Station.IRVEIDPDC != "FRBBOX0002" {
		t.Errorf("operator filter returned %+v, want only FRBBOX0002", results)
	}

	// Outside the bbox, nothing should come back.
	farAway := domain.StationFilter{MinLng: 2.2, MinLat: 48.8, MaxLng: 2.2001, MaxLat: 48.8001}
	results, err = repo.ListByBBox(ctx, farAway)
	if err != nil {
		t.Fatalf("ListByBBox far away: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("got %d stations far from any inserted point, want 0", len(results))
	}
}

func TestStationRepository_ListByBBox_ConnectorTypeAndMinPower(t *testing.T) {
	pool := setupTestPool(t)
	ctx := context.Background()
	repo := NewStationRepository(pool)

	ccsFast := testStation("FRCONN0001", 45.90, 6.10)
	ccsFast.ConnectorType = "CCS"
	power := 150.0
	ccsFast.PowerKW = &power

	t2Slow := testStation("FRCONN0002", 45.91, 6.12)
	t2Slow.ConnectorType = "T2"
	slowPower := 22.0
	t2Slow.PowerKW = &slowPower

	unknownPower := testStation("FRCONN0003", 45.92, 6.13)
	unknownPower.ConnectorType = "CHAdeMO"
	unknownPower.PowerKW = nil

	for _, s := range []domain.Station{ccsFast, t2Slow, unknownPower} {
		if _, err := repo.UpsertStation(ctx, s); err != nil {
			t.Fatalf("UpsertStation(%s): %v", s.IRVEIDPDC, err)
		}
	}

	bbox := domain.StationFilter{MinLng: 6.0, MinLat: 45.8, MaxLng: 6.3, MaxLat: 46.0}

	// Connector type filter, single value.
	ccsOnly := bbox
	ccsOnly.ConnectorTypes = []string{"CCS"}
	results, err := repo.ListByBBox(ctx, ccsOnly)
	if err != nil {
		t.Fatalf("ListByBBox connectorType=CCS: %v", err)
	}
	if len(results) != 1 || results[0].Station.IRVEIDPDC != "FRCONN0001" {
		t.Errorf("connectorType=CCS returned %+v, want only FRCONN0001", results)
	}

	// Connector type filter, multiple values.
	ccsOrChademo := bbox
	ccsOrChademo.ConnectorTypes = []string{"CCS", "CHAdeMO"}
	results, err = repo.ListByBBox(ctx, ccsOrChademo)
	if err != nil {
		t.Fatalf("ListByBBox connectorType=CCS,CHAdeMO: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("got %d stations for connectorType=CCS,CHAdeMO, want 2", len(results))
	}

	// Min power filter: excludes the slow T2 and the unknown-power station.
	fastOnly := bbox
	minPower := 50.0
	fastOnly.MinPowerKW = &minPower
	results, err = repo.ListByBBox(ctx, fastOnly)
	if err != nil {
		t.Fatalf("ListByBBox minPowerKw=50: %v", err)
	}
	if len(results) != 1 || results[0].Station.IRVEIDPDC != "FRCONN0001" {
		t.Errorf("minPowerKw=50 returned %+v, want only FRCONN0001 (unknown power must not pass)", results)
	}

	// Combined: connector type AND min power.
	combined := bbox
	combined.ConnectorTypes = []string{"CCS", "T2"}
	combined.MinPowerKW = &minPower
	results, err = repo.ListByBBox(ctx, combined)
	if err != nil {
		t.Fatalf("ListByBBox combined filter: %v", err)
	}
	if len(results) != 1 || results[0].Station.IRVEIDPDC != "FRCONN0001" {
		t.Errorf("combined filter returned %+v, want only FRCONN0001", results)
	}
}

// TestStationRepository_ListByBBox_PrefersExactConnectorMatch_SameSource
// pins stationListFrom's LATERAL dedup: when a single source (Freshmile is
// currently the only one that populates connector_type) has *both* a
// connector-specific tariff and a generic one for the very same station,
// source, plan and kind, the connector-specific one must win — even when
// it's pricier — because it's the accurate price for this station's actual
// connector, and the generic one is a coarser fallback meant for stations
// Freshmile didn't have connector-level data for.
func TestStationRepository_ListByBBox_PrefersExactConnectorMatch_SameSource(t *testing.T) {
	pool := setupTestPool(t)
	ctx := context.Background()
	stationRepo := NewStationRepository(pool)
	tariffRepo := NewTariffRepository(pool)

	station := testStation("FRPREFCONN1", 45.90, 6.10)
	station.ConnectorType = "T2"
	stationID, err := stationRepo.UpsertStation(ctx, station)
	if err != nil {
		t.Fatalf("UpsertStation: %v", err)
	}

	t2Price := 40.0
	genericPrice := 10.0 // cheaper, but not connector-specific — must still lose to the exact match from the same source
	if err := tariffRepo.Upsert(ctx, domain.StationTariff{
		StationID: stationID, Source: "freshmile", Plan: domain.TariffPlanStandard, Kind: domain.TariffKindAC,
		Model: "freshmile_kwh", Currency: "EUR", EnergyPriceCentsPerKWh: &t2Price,
		ConnectorType: "T2", Extra: map[string]any{},
	}); err != nil {
		t.Fatalf("Upsert T2 tariff: %v", err)
	}
	if err := tariffRepo.Upsert(ctx, domain.StationTariff{
		StationID: stationID, Source: "freshmile", Plan: domain.TariffPlanStandard, Kind: domain.TariffKindAC,
		Model: "freshmile_kwh", Currency: "EUR", EnergyPriceCentsPerKWh: &genericPrice, Extra: map[string]any{},
	}); err != nil {
		t.Fatalf("Upsert generic tariff: %v", err)
	}

	bbox := domain.StationFilter{MinLng: 6.0, MinLat: 45.8, MaxLng: 6.3, MaxLat: 46.0}
	results, err := stationRepo.ListByBBox(ctx, bbox)
	if err != nil {
		t.Fatalf("ListByBBox: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d stations, want 1", len(results))
	}
	if got := results[0].PricingSummary.ACMinCentsPerKWh; got == nil || *got != 40.0 {
		t.Errorf("ACMinCentsPerKWh = %v, want 40.0 (Freshmile's own T2-specific tariff, not its cheaper generic 10.0)", got)
	}
}

// TestStationRepository_ListByBBox_ConnectorMatchNeverSuppressesOtherSources
// is the regression test for the "gros bug sur le meilleur tarif" report: a
// connector-specific tariff from one source (Freshmile) must never hide a
// cheaper, unrelated tariff from a completely different source (Izivia) —
// the connector-type exact-match preference only makes sense *within* one
// source's own tariffs (see the SameSource test above), never across
// sources. Before stationListFrom's LATERAL dedup, a single global COALESCE
// let any exact-connector-match row anywhere suppress every other source's
// price entirely, so this used to return 40.0 (Freshmile) instead of the
// true cheapest, 10.0 (Izivia).
func TestStationRepository_ListByBBox_ConnectorMatchNeverSuppressesOtherSources(t *testing.T) {
	pool := setupTestPool(t)
	ctx := context.Background()
	stationRepo := NewStationRepository(pool)
	tariffRepo := NewTariffRepository(pool)

	station := testStation("FRPREFCONN2", 45.90, 6.10)
	station.ConnectorType = "T2"
	stationID, err := stationRepo.UpsertStation(ctx, station)
	if err != nil {
		t.Fatalf("UpsertStation: %v", err)
	}

	t2Price := 40.0
	cheaperOtherSource := 10.0
	if err := tariffRepo.Upsert(ctx, domain.StationTariff{
		StationID: stationID, Source: "freshmile", Plan: domain.TariffPlanStandard, Kind: domain.TariffKindAC,
		Model: "freshmile_kwh", Currency: "EUR", EnergyPriceCentsPerKWh: &t2Price,
		ConnectorType: "T2", Extra: map[string]any{},
	}); err != nil {
		t.Fatalf("Upsert T2 tariff: %v", err)
	}
	if err := tariffRepo.Upsert(ctx, domain.StationTariff{
		StationID: stationID, Source: "izivia", Plan: domain.TariffPlanStandard, Kind: domain.TariffKindAC,
		Model: "izivia_text", Currency: "EUR", EnergyPriceCentsPerKWh: &cheaperOtherSource, Extra: map[string]any{},
	}); err != nil {
		t.Fatalf("Upsert generic tariff: %v", err)
	}

	bbox := domain.StationFilter{MinLng: 6.0, MinLat: 45.8, MaxLng: 6.3, MaxLat: 46.0}
	results, err := stationRepo.ListByBBox(ctx, bbox)
	if err != nil {
		t.Fatalf("ListByBBox: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d stations, want 1", len(results))
	}
	if got := results[0].PricingSummary.ACMinCentsPerKWh; got == nil || *got != 10.0 {
		t.Errorf("ACMinCentsPerKWh = %v, want 10.0 (Izivia's cheaper price, not suppressed by Freshmile's unrelated connector-specific tariff)", got)
	}
}

func TestStationRepository_ListByBBox_HasTariffs(t *testing.T) {
	pool := setupTestPool(t)
	ctx := context.Background()
	stationRepo := NewStationRepository(pool)
	tariffRepo := NewTariffRepository(pool)

	withTariff := testStation("FRTARIFF01", 45.90, 6.10)
	withoutTariff := testStation("FRTARIFF02", 45.91, 6.11)

	idWith, err := stationRepo.UpsertStation(ctx, withTariff)
	if err != nil {
		t.Fatalf("UpsertStation: %v", err)
	}
	if _, err := stationRepo.UpsertStation(ctx, withoutTariff); err != nil {
		t.Fatalf("UpsertStation: %v", err)
	}

	price := 45.0
	if err := tariffRepo.Upsert(ctx, domain.StationTariff{
		StationID:              idWith,
		Source:                 "izivia",
		Kind:                   domain.TariffKindMixed,
		Model:                  "izivia_text",
		Currency:               "EUR",
		EnergyPriceCentsPerKWh: &price,
	}); err != nil {
		t.Fatalf("Tariffs.Upsert: %v", err)
	}

	bbox := domain.StationFilter{MinLng: 6.0, MinLat: 45.8, MaxLng: 6.3, MaxLat: 46.0}
	hasTariffs := true
	bbox.HasTariffs = &hasTariffs

	results, err := stationRepo.ListByBBox(ctx, bbox)
	if err != nil {
		t.Fatalf("ListByBBox hasTariffs: %v", err)
	}
	if len(results) != 1 || results[0].Station.IRVEIDPDC != "FRTARIFF01" {
		t.Fatalf("ListByBBox hasTariffs = %+v, want only FRTARIFF01", results)
	}
	if !results[0].HasTariffs {
		t.Error("HasTariffs = false, want true")
	}
	if len(results[0].TariffSources) != 1 || results[0].TariffSources[0] != "izivia" {
		t.Errorf("TariffSources = %v, want [izivia]", results[0].TariffSources)
	}
}

func TestStationRepository_ListByBBox_PriceRange(t *testing.T) {
	pool := setupTestPool(t)
	ctx := context.Background()
	stationRepo := NewStationRepository(pool)
	tariffRepo := NewTariffRepository(pool)

	cheap := testStation("FRPRICE001", 45.90, 6.10)
	mid := testStation("FRPRICE002", 45.91, 6.11)
	expensive := testStation("FRPRICE003", 45.92, 6.12)
	noPrice := testStation("FRPRICE004", 45.93, 6.13)

	cheapID, err := stationRepo.UpsertStation(ctx, cheap)
	if err != nil {
		t.Fatalf("UpsertStation cheap: %v", err)
	}
	midID, err := stationRepo.UpsertStation(ctx, mid)
	if err != nil {
		t.Fatalf("UpsertStation mid: %v", err)
	}
	expensiveID, err := stationRepo.UpsertStation(ctx, expensive)
	if err != nil {
		t.Fatalf("UpsertStation expensive: %v", err)
	}
	if _, err := stationRepo.UpsertStation(ctx, noPrice); err != nil {
		t.Fatalf("UpsertStation noPrice: %v", err)
	}

	cheapPrice, midPrice, expensivePrice := 20.0, 30.0, 60.0
	for id, price := range map[uuid.UUID]*float64{cheapID: &cheapPrice, midID: &midPrice, expensiveID: &expensivePrice} {
		if err := tariffRepo.Upsert(ctx, domain.StationTariff{
			StationID: id, Source: "izivia", Kind: domain.TariffKindMixed, Model: "izivia_text",
			Currency: "EUR", EnergyPriceCentsPerKWh: price,
		}); err != nil {
			t.Fatalf("Tariffs.Upsert: %v", err)
		}
	}

	bbox := domain.StationFilter{MinLng: 6.0, MinLat: 45.8, MaxLng: 6.3, MaxLat: 46.0}
	minPrice, maxPrice := 25.0, 40.0
	bbox.MinPriceCentsPerKWh = &minPrice
	bbox.MaxPriceCentsPerKWh = &maxPrice

	results, err := stationRepo.ListByBBox(ctx, bbox)
	if err != nil {
		t.Fatalf("ListByBBox price range: %v", err)
	}
	if len(results) != 1 || results[0].Station.IRVEIDPDC != "FRPRICE002" {
		t.Fatalf("ListByBBox price range [25,40] = %+v, want only FRPRICE002 (30 cts)", results)
	}

	// A station with no known price must never match a price-range filter,
	// same as it never matches HasTariffs=true.
	minOnly := domain.StationFilter{MinLng: 6.0, MinLat: 45.8, MaxLng: 6.3, MaxLat: 46.0}
	zero := 0.0
	minOnly.MinPriceCentsPerKWh = &zero
	results, err = stationRepo.ListByBBox(ctx, minOnly)
	if err != nil {
		t.Fatalf("ListByBBox minPrice=0: %v", err)
	}
	for _, r := range results {
		if r.Station.IRVEIDPDC == "FRPRICE004" {
			t.Error("station with no known price matched a minPrice filter, want excluded")
		}
	}
}

func TestStationRepository_ListByBBox_MixedKindFeedsBothACAndDC(t *testing.T) {
	pool := setupTestPool(t)
	ctx := context.Background()
	stationRepo := NewStationRepository(pool)
	tariffRepo := NewTariffRepository(pool)

	// A single-price source (Izivia free text with no power figure) stores a
	// "mixed" tariff. It must surface as BOTH the AC and DC minimum so the map
	// can price the marker regardless of the station's own connector kind —
	// this is the regression that grayed out Izivia stations even though their
	// tariff showed in the detail view.
	station := testStation("FRMIXED001", 45.90, 6.10)
	id, err := stationRepo.UpsertStation(ctx, station)
	if err != nil {
		t.Fatalf("UpsertStation: %v", err)
	}
	price := 39.1
	if err := tariffRepo.Upsert(ctx, domain.StationTariff{
		StationID: id, Source: "izivia", Kind: domain.TariffKindMixed,
		Model: "izivia_text", Currency: "EUR", EnergyPriceCentsPerKWh: &price,
	}); err != nil {
		t.Fatalf("Tariffs.Upsert: %v", err)
	}

	bbox := domain.StationFilter{MinLng: 6.0, MinLat: 45.8, MaxLng: 6.3, MaxLat: 46.0}
	results, err := stationRepo.ListByBBox(ctx, bbox)
	if err != nil {
		t.Fatalf("ListByBBox: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d stations, want 1", len(results))
	}
	ps := results[0].PricingSummary
	if ps.ACMinCentsPerKWh == nil || *ps.ACMinCentsPerKWh != 39.1 {
		t.Errorf("ACMinCentsPerKWh = %v, want 39.1 (mixed tariff must feed AC)", ps.ACMinCentsPerKWh)
	}
	if ps.DCMinCentsPerKWh == nil || *ps.DCMinCentsPerKWh != 39.1 {
		t.Errorf("DCMinCentsPerKWh = %v, want 39.1 (mixed tariff must feed DC)", ps.DCMinCentsPerKWh)
	}

	// And it must feed SelectedSourcesPricing the same way when its
	// source:plan pair is selected.
	bbox.Sources = []string{"izivia:standard"}
	results, err = stationRepo.ListByBBox(ctx, bbox)
	if err != nil {
		t.Fatalf("ListByBBox with sources: %v", err)
	}
	sp := results[0].SelectedSourcesPricing
	if sp == nil || sp.ACMinCentsPerKWh == nil || *sp.ACMinCentsPerKWh != 39.1 || sp.DCMinCentsPerKWh == nil || *sp.DCMinCentsPerKWh != 39.1 {
		t.Errorf("SelectedSourcesPricing = %+v, want AC=DC=39.1 for selected mixed izivia tariff", sp)
	}
}

func TestStationRepository_ListByBBox_SelectedSourcesPricing(t *testing.T) {
	pool := setupTestPool(t)
	ctx := context.Background()
	stationRepo := NewStationRepository(pool)
	tariffRepo := NewTariffRepository(pool)

	withIzivia := testStation("FRSRC0001", 45.90, 6.10)
	withElectra := testStation("FRSRC0002", 45.91, 6.11)

	idIzivia, err := stationRepo.UpsertStation(ctx, withIzivia)
	if err != nil {
		t.Fatalf("UpsertStation: %v", err)
	}
	idElectra, err := stationRepo.UpsertStation(ctx, withElectra)
	if err != nil {
		t.Fatalf("UpsertStation: %v", err)
	}

	iziviaPrice := 45.0
	if err := tariffRepo.Upsert(ctx, domain.StationTariff{
		StationID: idIzivia, Source: "izivia", Kind: domain.TariffKindMixed,
		Model: "izivia_text", Currency: "EUR", EnergyPriceCentsPerKWh: &iziviaPrice,
	}); err != nil {
		t.Fatalf("Tariffs.Upsert izivia: %v", err)
	}
	electraPrice := 48.0
	if err := tariffRepo.Upsert(ctx, domain.StationTariff{
		StationID: idElectra, Source: "electra", Kind: domain.TariffKindDC,
		Model: "electra_fixed", Currency: "EUR", EnergyPriceCentsPerKWh: &electraPrice,
	}); err != nil {
		t.Fatalf("Tariffs.Upsert electra: %v", err)
	}

	bbox := domain.StationFilter{MinLng: 6.0, MinLat: 45.8, MaxLng: 6.3, MaxLat: 46.0}

	// Without a Sources filter, SelectedSourcesPricing must stay nil — it's
	// an opt-in field, not always computed.
	results, err := stationRepo.ListByBBox(ctx, bbox)
	if err != nil {
		t.Fatalf("ListByBBox no sources: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d stations, want 2", len(results))
	}
	for _, r := range results {
		if r.SelectedSourcesPricing != nil {
			t.Errorf("station %s: SelectedSourcesPricing = %+v, want nil without a Sources filter", r.Station.IRVEIDPDC, r.SelectedSourcesPricing)
		}
	}

	// Selecting "electra:standard" must never drop the izivia-only station
	// (grayed out on the map, not hidden) but only the electra station gets
	// a SelectedSourcesPricing price. The repository deals in opaque
	// "source:plan" pairs; pairing them up is the API handler's job.
	bbox.Sources = []string{"electra:standard"}
	results, err = stationRepo.ListByBBox(ctx, bbox)
	if err != nil {
		t.Fatalf("ListByBBox sources=electra:standard: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d stations with Sources=[electra], want 2 (never filtered out)", len(results))
	}

	bySourceStation := map[string]domain.StationSummary{}
	for _, r := range results {
		bySourceStation[r.Station.IRVEIDPDC] = r
	}

	izivia := bySourceStation["FRSRC0001"]
	if izivia.SelectedSourcesPricing == nil {
		t.Fatal("izivia station: SelectedSourcesPricing = nil, want a non-nil (empty) pricing struct")
	}
	if izivia.SelectedSourcesPricing.ACMinCentsPerKWh != nil || izivia.SelectedSourcesPricing.DCMinCentsPerKWh != nil {
		t.Errorf("izivia station: SelectedSourcesPricing = %+v, want both nil (no electra tariff here)", izivia.SelectedSourcesPricing)
	}

	electra := bySourceStation["FRSRC0002"]
	if electra.SelectedSourcesPricing == nil {
		t.Fatal("electra station: SelectedSourcesPricing = nil, want a populated pricing struct")
	}
	if electra.SelectedSourcesPricing.DCMinCentsPerKWh == nil || *electra.SelectedSourcesPricing.DCMinCentsPerKWh != 48.0 {
		t.Errorf("electra station: DCMinCentsPerKWh = %v, want 48.0", electra.SelectedSourcesPricing.DCMinCentsPerKWh)
	}

	// Selecting a plan the station doesn't have a tariff for ("subscription"
	// vs the "standard" plan actually stored) must not match: the matching
	// is plan-aware, not just source-aware.
	bbox.Sources = []string{"electra:subscription"}
	results, err = stationRepo.ListByBBox(ctx, bbox)
	if err != nil {
		t.Fatalf("ListByBBox sources=electra:subscription: %v", err)
	}
	for _, r := range results {
		if r.Station.IRVEIDPDC == "FRSRC0002" && r.SelectedSourcesPricing != nil &&
			(r.SelectedSourcesPricing.ACMinCentsPerKWh != nil || r.SelectedSourcesPricing.DCMinCentsPerKWh != nil) {
			t.Errorf("electra station with sources=electra:subscription = %+v, want no price (only the standard plan is stored)", r.SelectedSourcesPricing)
		}
	}
}

func TestStationRepository_ListByBBox_ExcludeSubscriptionPlans(t *testing.T) {
	pool := setupTestPool(t)
	ctx := context.Background()
	stationRepo := NewStationRepository(pool)
	tariffRepo := NewTariffRepository(pool)

	// A station whose only known price is a subscription-plan tariff, and
	// one with both a standard and a (pricier) subscription tariff, so
	// excluding subscription plans changes the global best price for the
	// second station rather than just removing it entirely.
	subscriptionOnly := testStation("FREXCL001", 45.90, 6.10)
	both := testStation("FREXCL002", 45.91, 6.11)

	idSubOnly, err := stationRepo.UpsertStation(ctx, subscriptionOnly)
	if err != nil {
		t.Fatalf("UpsertStation subscriptionOnly: %v", err)
	}
	idBoth, err := stationRepo.UpsertStation(ctx, both)
	if err != nil {
		t.Fatalf("UpsertStation both: %v", err)
	}

	subOnlyPrice := 30.0
	if err := tariffRepo.Upsert(ctx, domain.StationTariff{
		StationID: idSubOnly, Source: "electra", Plan: domain.TariffPlanSubscription, Kind: domain.TariffKindDC,
		Model: "electra_fixed", Currency: "EUR", EnergyPriceCentsPerKWh: &subOnlyPrice,
	}); err != nil {
		t.Fatalf("Tariffs.Upsert subscriptionOnly: %v", err)
	}

	standardPrice := 40.0
	subscriptionPrice := 25.0 // cheaper, so it would otherwise win the global MIN()
	if err := tariffRepo.Upsert(ctx, domain.StationTariff{
		StationID: idBoth, Source: "electra", Plan: domain.TariffPlanStandard, Kind: domain.TariffKindDC,
		Model: "electra_fixed", Currency: "EUR", EnergyPriceCentsPerKWh: &standardPrice,
	}); err != nil {
		t.Fatalf("Tariffs.Upsert both/standard: %v", err)
	}
	if err := tariffRepo.Upsert(ctx, domain.StationTariff{
		StationID: idBoth, Source: "electra", Plan: domain.TariffPlanSubscription, Kind: domain.TariffKindDC,
		Model: "electra_fixed", Currency: "EUR", EnergyPriceCentsPerKWh: &subscriptionPrice,
	}); err != nil {
		t.Fatalf("Tariffs.Upsert both/subscription: %v", err)
	}

	bbox := domain.StationFilter{MinLng: 6.0, MinLat: 45.8, MaxLng: 6.3, MaxLat: 46.0}

	// Without the filter: the subscription-only station has a price, and the
	// "both" station's price is the cheaper subscription rate.
	results, err := stationRepo.ListByBBox(ctx, bbox)
	if err != nil {
		t.Fatalf("ListByBBox (no exclusion): %v", err)
	}
	byID := map[string]domain.StationSummary{}
	for _, r := range results {
		byID[r.Station.IRVEIDPDC] = r
	}
	if p := byID["FREXCL001"].PricingSummary.DCMinCentsPerKWh; p == nil || *p != subOnlyPrice {
		t.Errorf("FREXCL001 DCMinCentsPerKWh = %v, want %v", p, subOnlyPrice)
	}
	if p := byID["FREXCL002"].PricingSummary.DCMinCentsPerKWh; p == nil || *p != subscriptionPrice {
		t.Errorf("FREXCL002 DCMinCentsPerKWh = %v, want %v (cheapest overall, including subscription)", p, subscriptionPrice)
	}

	// With ExcludeSubscriptionPlans: the subscription-only station has no
	// price at all, and the "both" station falls back to its standard price.
	bbox.ExcludeSubscriptionPlans = true
	results, err = stationRepo.ListByBBox(ctx, bbox)
	if err != nil {
		t.Fatalf("ListByBBox (exclude subscription): %v", err)
	}
	byID = map[string]domain.StationSummary{}
	for _, r := range results {
		byID[r.Station.IRVEIDPDC] = r
	}
	if p := byID["FREXCL001"].PricingSummary.DCMinCentsPerKWh; p != nil {
		t.Errorf("FREXCL001 DCMinCentsPerKWh = %v, want nil (only tariff is subscription-plan)", *p)
	}
	if p := byID["FREXCL002"].PricingSummary.DCMinCentsPerKWh; p == nil || *p != standardPrice {
		t.Errorf("FREXCL002 DCMinCentsPerKWh = %v, want %v (falls back to the standard plan)", p, standardPrice)
	}

	// Same exclusion must also apply to SelectedSourcesPricing when a
	// sources selection is active.
	bbox.Sources = []string{"electra:subscription"}
	results, err = stationRepo.ListByBBox(ctx, bbox)
	if err != nil {
		t.Fatalf("ListByBBox (exclude subscription + sources=electra:subscription): %v", err)
	}
	for _, r := range results {
		if r.SelectedSourcesPricing != nil && r.SelectedSourcesPricing.DCMinCentsPerKWh != nil {
			t.Errorf("station %s: SelectedSourcesPricing.DCMinCentsPerKWh = %v, want nil (the only matching tariff is the excluded subscription plan)",
				r.Station.IRVEIDPDC, *r.SelectedSourcesPricing.DCMinCentsPerKWh)
		}
	}
}

// TestStationRepository_ListByBBox_SelectedSourcesDedupWithinSource covers
// the interaction between stationListFrom's connector-match dedup and a
// f.Sources selection: when a user selects one specific source that itself
// has both a connector-specific and a generic tariff for the same
// station/plan (Freshmile's case), SelectedSourcesPricing must reflect the
// connector-specific one — the dedup has to apply consistently whether or
// not a sources filter is active, since stationSelectedPriceFragment reuses
// the same deduped `t` rows as the unfiltered aggregate.
func TestStationRepository_ListByBBox_SelectedSourcesDedupWithinSource(t *testing.T) {
	pool := setupTestPool(t)
	ctx := context.Background()
	stationRepo := NewStationRepository(pool)
	tariffRepo := NewTariffRepository(pool)

	station := testStation("FRSELDEDUP1", 45.90, 6.10)
	station.ConnectorType = "CCS"
	stationID, err := stationRepo.UpsertStation(ctx, station)
	if err != nil {
		t.Fatalf("UpsertStation: %v", err)
	}

	genericPrice := 32.0
	specificPrice := 51.0
	if err := tariffRepo.Upsert(ctx, domain.StationTariff{
		StationID: stationID, Source: "freshmile", Plan: domain.TariffPlanStandard, Kind: domain.TariffKindDC,
		Model: "freshmile_kwh", Currency: "EUR", EnergyPriceCentsPerKWh: &genericPrice, Extra: map[string]any{},
	}); err != nil {
		t.Fatalf("Upsert generic tariff: %v", err)
	}
	if err := tariffRepo.Upsert(ctx, domain.StationTariff{
		StationID: stationID, Source: "freshmile", Plan: domain.TariffPlanStandard, Kind: domain.TariffKindDC,
		Model: "freshmile_kwh", Currency: "EUR", EnergyPriceCentsPerKWh: &specificPrice,
		ConnectorType: "CCS", Extra: map[string]any{},
	}); err != nil {
		t.Fatalf("Upsert connector-specific tariff: %v", err)
	}

	bbox := domain.StationFilter{
		MinLng: 6.0, MinLat: 45.8, MaxLng: 6.3, MaxLat: 46.0,
		Sources: []string{"freshmile:standard"},
	}
	results, err := stationRepo.ListByBBox(ctx, bbox)
	if err != nil {
		t.Fatalf("ListByBBox: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d stations, want 1", len(results))
	}
	got := results[0].SelectedSourcesPricing
	if got == nil || got.DCMinCentsPerKWh == nil || *got.DCMinCentsPerKWh != specificPrice {
		t.Errorf("SelectedSourcesPricing = %+v, want DCMinCentsPerKWh = %v (the connector-specific tariff)", got, specificPrice)
	}
}

// TestStationRepository_ListByBBox_DedupRespectsPlanBoundary covers the
// interaction between stationListFrom's dedup (partitioned by source,
// plan, kind — so a subscription-plan tariff and a standard-plan tariff
// from the same source never get grouped together) and
// ExcludeSubscriptionPlans: a cheap connector-specific *subscription*
// tariff must not "steal" the dedup slot from a pricier but excludable
// generic *standard* tariff from the same source — they're different plans
// and must be deduped (and filtered) independently.
func TestStationRepository_ListByBBox_DedupRespectsPlanBoundary(t *testing.T) {
	pool := setupTestPool(t)
	ctx := context.Background()
	stationRepo := NewStationRepository(pool)
	tariffRepo := NewTariffRepository(pool)

	station := testStation("FRPLANBOUND1", 45.90, 6.10)
	station.ConnectorType = "CCS"
	stationID, err := stationRepo.UpsertStation(ctx, station)
	if err != nil {
		t.Fatalf("UpsertStation: %v", err)
	}

	cheapSubscriptionSpecific := 15.0
	standardGeneric := 40.0
	if err := tariffRepo.Upsert(ctx, domain.StationTariff{
		StationID: stationID, Source: "freshmile", Plan: domain.TariffPlanSubscription, Kind: domain.TariffKindDC,
		Model: "freshmile_kwh", Currency: "EUR", EnergyPriceCentsPerKWh: &cheapSubscriptionSpecific,
		ConnectorType: "CCS", Extra: map[string]any{},
	}); err != nil {
		t.Fatalf("Upsert subscription tariff: %v", err)
	}
	if err := tariffRepo.Upsert(ctx, domain.StationTariff{
		StationID: stationID, Source: "freshmile", Plan: domain.TariffPlanStandard, Kind: domain.TariffKindDC,
		Model: "freshmile_kwh", Currency: "EUR", EnergyPriceCentsPerKWh: &standardGeneric, Extra: map[string]any{},
	}); err != nil {
		t.Fatalf("Upsert standard tariff: %v", err)
	}

	bbox := domain.StationFilter{
		MinLng: 6.0, MinLat: 45.8, MaxLng: 6.3, MaxLat: 46.0,
		ExcludeSubscriptionPlans: true,
	}
	results, err := stationRepo.ListByBBox(ctx, bbox)
	if err != nil {
		t.Fatalf("ListByBBox: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d stations, want 1", len(results))
	}
	got := results[0].PricingSummary.DCMinCentsPerKWh
	if got == nil || *got != standardGeneric {
		t.Errorf("DCMinCentsPerKWh = %v, want %v (the standard plan's price, subscription plan excluded — not the cheaper subscription price)", got, standardGeneric)
	}
}
