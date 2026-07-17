package ingestion

import (
	"context"
	"testing"

	"opencharge/internal/domain"
	"opencharge/internal/repository"
)

func TestLidlIngester_Run(t *testing.T) {
	pool := setupLinkingTestPool(t)
	ctx := context.Background()
	stationRepo := repository.NewStationRepository(pool)
	tariffRepo := repository.NewTariffRepository(pool)

	lidlAC := testIRVEStation("FRLIDL0001", 45.9000, 6.1000, domain.ConnectorTypeT2)
	lidlAC.OperatorName = "LIDL"
	lidlAC.Enseigne = "LIDL"
	lidlACID, err := stationRepo.UpsertStation(ctx, lidlAC)
	if err != nil {
		t.Fatalf("UpsertStation lidl ac: %v", err)
	}

	lidlDC := testIRVEStation("FRLIDL0002", 45.9100, 6.1100, domain.ConnectorTypeCCS)
	lidlDC.OperatorName = "Some Legal Entity SAS"
	lidlDC.Enseigne = "Lidl"
	lidlDCID, err := stationRepo.UpsertStation(ctx, lidlDC)
	if err != nil {
		t.Fatalf("UpsertStation lidl dc: %v", err)
	}

	other := testIRVEStation("FROTHER0002", 45.9200, 6.1200, domain.ConnectorTypeCCS)
	other.OperatorName = "Electra"
	other.Enseigne = "Electra"
	otherID, err := stationRepo.UpsertStation(ctx, other)
	if err != nil {
		t.Fatalf("UpsertStation other: %v", err)
	}

	ing := NewLidlIngester(pool, stationRepo, tariffRepo)
	n, err := ing.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n != 2 {
		t.Fatalf("Run() = %d stations, want 2", n)
	}

	acTariffs, err := tariffRepo.ListByStation(ctx, lidlACID)
	if err != nil {
		t.Fatalf("ListByStation lidlAC: %v", err)
	}
	assertLidlTariff(t, acTariffs)

	dcTariffs, err := tariffRepo.ListByStation(ctx, lidlDCID)
	if err != nil {
		t.Fatalf("ListByStation lidlDC: %v", err)
	}
	assertLidlTariff(t, dcTariffs)

	otherTariffs, err := tariffRepo.ListByStation(ctx, otherID)
	if err != nil {
		t.Fatalf("ListByStation other: %v", err)
	}
	if len(otherTariffs) != 0 {
		t.Errorf("other station got %d lidl tariffs, want 0 (not a lidl station)", len(otherTariffs))
	}
}

func assertLidlTariff(t *testing.T, tariffs []domain.StationTariff) {
	t.Helper()
	if len(tariffs) != 1 {
		t.Fatalf("got %d tariffs, want 1 (single flat plan)", len(tariffs))
	}
	tf := tariffs[0]
	if tf.Source != "lidl" {
		t.Errorf("tariff source = %q, want lidl", tf.Source)
	}
	if tf.Plan != domain.TariffPlanStandard {
		t.Errorf("tariff plan = %q, want %q", tf.Plan, domain.TariffPlanStandard)
	}
	if tf.Kind != domain.TariffKindMixed {
		t.Errorf("tariff kind = %q, want mixed (same price for ac and dc)", tf.Kind)
	}
	if tf.EnergyPriceCentsPerKWh == nil || *tf.EnergyPriceCentsPerKWh != lidlFlatCentsPerKWh {
		t.Errorf("tariff price = %v, want %v", tf.EnergyPriceCentsPerKWh, lidlFlatCentsPerKWh)
	}
}
