package ingestion

import (
	"context"
	"testing"

	"opencharge/internal/domain"
	"opencharge/internal/repository"
)

func TestSowattIngester_Run(t *testing.T) {
	pool := setupLinkingTestPool(t)
	ctx := context.Background()
	stationRepo := repository.NewStationRepository(pool)
	tariffRepo := repository.NewTariffRepository(pool)

	sowattAC := testIRVEStation("FRSOWATT0001", 45.9000, 6.1000, domain.ConnectorTypeT2)
	sowattAC.OperatorName = "Sowatt Solutions"
	sowattAC.Enseigne = "Sowatt Solutions"
	sowattACID, err := stationRepo.UpsertStation(ctx, sowattAC)
	if err != nil {
		t.Fatalf("UpsertStation sowatt ac: %v", err)
	}

	sowattDC := testIRVEStation("FRSOWATT0002", 45.9100, 6.1100, domain.ConnectorTypeCCS)
	sowattDC.OperatorName = "Some Legal Entity SAS"
	sowattDC.Enseigne = "Sowatt Solutions"
	sowattDCID, err := stationRepo.UpsertStation(ctx, sowattDC)
	if err != nil {
		t.Fatalf("UpsertStation sowatt dc: %v", err)
	}

	other := testIRVEStation("FROTHER0003", 45.9200, 6.1200, domain.ConnectorTypeCCS)
	other.OperatorName = "Electra"
	other.Enseigne = "Electra"
	otherID, err := stationRepo.UpsertStation(ctx, other)
	if err != nil {
		t.Fatalf("UpsertStation other: %v", err)
	}

	ing := NewSowattIngester(pool, stationRepo, tariffRepo)
	n, err := ing.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n != 2 {
		t.Fatalf("Run() = %d stations, want 2", n)
	}

	acTariffs, err := tariffRepo.ListByStation(ctx, sowattACID)
	if err != nil {
		t.Fatalf("ListByStation sowattAC: %v", err)
	}
	assertSowattTariff(t, acTariffs)

	dcTariffs, err := tariffRepo.ListByStation(ctx, sowattDCID)
	if err != nil {
		t.Fatalf("ListByStation sowattDC: %v", err)
	}
	assertSowattTariff(t, dcTariffs)

	otherTariffs, err := tariffRepo.ListByStation(ctx, otherID)
	if err != nil {
		t.Fatalf("ListByStation other: %v", err)
	}
	if len(otherTariffs) != 0 {
		t.Errorf("other station got %d sowatt tariffs, want 0 (not a sowatt station)", len(otherTariffs))
	}
}

func assertSowattTariff(t *testing.T, tariffs []domain.StationTariff) {
	t.Helper()
	if len(tariffs) != 1 {
		t.Fatalf("got %d tariffs, want 1 (single flat plan)", len(tariffs))
	}
	tf := tariffs[0]
	if tf.Source != "sowatt" {
		t.Errorf("tariff source = %q, want sowatt", tf.Source)
	}
	if tf.Plan != domain.TariffPlanStandard {
		t.Errorf("tariff plan = %q, want %q", tf.Plan, domain.TariffPlanStandard)
	}
	if tf.Kind != domain.TariffKindMixed {
		t.Errorf("tariff kind = %q, want mixed (same price for ac and dc)", tf.Kind)
	}
	if tf.EnergyPriceCentsPerKWh == nil || *tf.EnergyPriceCentsPerKWh != sowattFlatCentsPerKWh {
		t.Errorf("tariff price = %v, want %v", tf.EnergyPriceCentsPerKWh, sowattFlatCentsPerKWh)
	}
}
