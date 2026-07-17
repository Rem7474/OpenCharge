package ingestion

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"opencharge/internal/domain"
	"opencharge/internal/repository"
)

func TestEbornIngester_Run(t *testing.T) {
	pool := setupLinkingTestPool(t)
	ctx := context.Background()
	stationRepo := repository.NewStationRepository(pool)
	tariffRepo := repository.NewTariffRepository(pool)

	ac := testIRVEStation("FREBORN001", 45.9000, 6.1000, domain.ConnectorTypeT2)
	ac.OperatorName = "EASYCHARGE"
	ac.Enseigne = "eborn"
	acID, err := stationRepo.UpsertStation(ctx, ac)
	if err != nil {
		t.Fatalf("UpsertStation ac: %v", err)
	}

	dcMidPower := 50.0
	dcMid := testIRVEStation("FREBORN002", 45.9100, 6.1100, domain.ConnectorTypeCCS)
	dcMid.OperatorName = "EASYCHARGE"
	dcMid.Enseigne = "eborn"
	dcMid.PowerKW = &dcMidPower
	dcMidID, err := stationRepo.UpsertStation(ctx, dcMid)
	if err != nil {
		t.Fatalf("UpsertStation dcMid: %v", err)
	}

	dcHighPower := 150.0
	dcHigh := testIRVEStation("FREBORN003", 45.9200, 6.1200, domain.ConnectorTypeCCS)
	dcHigh.OperatorName = "EASYCHARGE"
	dcHigh.Enseigne = "eborn"
	dcHigh.PowerKW = &dcHighPower
	dcHighID, err := stationRepo.UpsertStation(ctx, dcHigh)
	if err != nil {
		t.Fatalf("UpsertStation dcHigh: %v", err)
	}

	// A dc station with no known power_kw must fall into the mid bracket
	// (>60 requires actually knowing the power is >60), not be dropped.
	dcUnknownPower := testIRVEStation("FREBORN004", 45.9300, 6.1300, domain.ConnectorTypeCHAdeMO)
	dcUnknownPower.OperatorName = "EASYCHARGE"
	dcUnknownPower.Enseigne = "eborn"
	dcUnknownPower.PowerKW = nil
	dcUnknownPowerID, err := stationRepo.UpsertStation(ctx, dcUnknownPower)
	if err != nil {
		t.Fatalf("UpsertStation dcUnknownPower: %v", err)
	}

	other := testIRVEStation("FROTHER0004", 45.9400, 6.1400, domain.ConnectorTypeCCS)
	other.OperatorName = "Electra"
	other.Enseigne = "Electra"
	otherID, err := stationRepo.UpsertStation(ctx, other)
	if err != nil {
		t.Fatalf("UpsertStation other: %v", err)
	}

	ing := NewEbornIngester(pool, stationRepo, tariffRepo)
	n, err := ing.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n != 4 {
		t.Fatalf("Run() = %d stations, want 4", n)
	}

	assertEbornTariffs(t, ctx, tariffRepo, acID, domain.TariffKindAC, ebornACStandardCentsPerKWh, ebornACCardCentsPerKWh)
	assertEbornTariffs(t, ctx, tariffRepo, dcMidID, domain.TariffKindDC, ebornDCMidStandardCentsPerKWh, ebornDCMidCardCentsPerKWh)
	assertEbornTariffs(t, ctx, tariffRepo, dcHighID, domain.TariffKindDC, ebornDCHighStandardCentsPerKWh, ebornDCHighCardCentsPerKWh)
	assertEbornTariffs(t, ctx, tariffRepo, dcUnknownPowerID, domain.TariffKindDC, ebornDCMidStandardCentsPerKWh, ebornDCMidCardCentsPerKWh)

	otherTariffs, err := tariffRepo.ListByStation(ctx, otherID)
	if err != nil {
		t.Fatalf("ListByStation other: %v", err)
	}
	if len(otherTariffs) != 0 {
		t.Errorf("other station got %d eborn tariffs, want 0 (not an eborn station)", len(otherTariffs))
	}
}

func TestEbornIngester_UnclassifiableConnectorSkipped(t *testing.T) {
	pool := setupLinkingTestPool(t)
	ctx := context.Background()
	stationRepo := repository.NewStationRepository(pool)
	tariffRepo := repository.NewTariffRepository(pool)

	unclassified := testIRVEStation("FREBORN005", 45.9000, 6.1000, domain.ConnectorTypeOther)
	unclassified.OperatorName = "EASYCHARGE"
	unclassified.Enseigne = "eborn"
	unclassifiedID, err := stationRepo.UpsertStation(ctx, unclassified)
	if err != nil {
		t.Fatalf("UpsertStation: %v", err)
	}

	ing := NewEbornIngester(pool, stationRepo, tariffRepo)
	if _, err := ing.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	tariffs, err := tariffRepo.ListByStation(ctx, unclassifiedID)
	if err != nil {
		t.Fatalf("ListByStation: %v", err)
	}
	if len(tariffs) != 0 {
		t.Errorf("got %d tariffs for an unclassifiable connector, want 0 (skip rather than guess)", len(tariffs))
	}
}

func assertEbornTariffs(t *testing.T, ctx context.Context, tariffRepo *repository.TariffRepository, stationID uuid.UUID, wantKind string, wantStandard, wantCard float64) {
	t.Helper()
	tariffs, err := tariffRepo.ListByStation(ctx, stationID)
	if err != nil {
		t.Fatalf("ListByStation: %v", err)
	}
	if len(tariffs) != 3 {
		t.Fatalf("got %d tariffs, want 3 (standard + card + subscription)", len(tariffs))
	}
	byPlan := map[string]domain.StationTariff{}
	for _, tf := range tariffs {
		if tf.Source != "eborn" {
			t.Errorf("tariff source = %q, want eborn", tf.Source)
		}
		if tf.Kind != wantKind {
			t.Errorf("tariff kind = %q, want %q", tf.Kind, wantKind)
		}
		byPlan[tf.Plan] = tf
	}
	standard, ok := byPlan[domain.TariffPlanStandard]
	if !ok || standard.EnergyPriceCentsPerKWh == nil || *standard.EnergyPriceCentsPerKWh != wantStandard {
		t.Errorf("standard tariff = %+v, want energy_price_cents_per_kwh=%v", standard, wantStandard)
	}
	card, ok := byPlan[ebornCardPlan]
	if !ok || card.EnergyPriceCentsPerKWh == nil || *card.EnergyPriceCentsPerKWh != wantCard {
		t.Errorf("card tariff = %+v, want energy_price_cents_per_kwh=%v", card, wantCard)
	}
	subscription, ok := byPlan[ebornSubscriptionPlan]
	if !ok || subscription.EnergyPriceCentsPerKWh == nil || *subscription.EnergyPriceCentsPerKWh != 0 {
		t.Errorf("subscription tariff = %+v, want energy_price_cents_per_kwh=0", subscription)
	}
	if subscription.RawText != ebornSubscriptionMonthlyFeeText {
		t.Errorf("subscription raw_text = %q, want the monthly fee note", subscription.RawText)
	}
}
