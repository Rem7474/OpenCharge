package ingestion

import (
	"context"
	"testing"

	"opencharge/internal/domain"
	"opencharge/internal/repository"
)

func TestFastnedIngester_Run(t *testing.T) {
	pool := setupLinkingTestPool(t)
	ctx := context.Background()
	stationRepo := repository.NewStationRepository(pool)
	tariffRepo := repository.NewTariffRepository(pool)

	fastnedDC := testIRVEStation("FRFASTNED1", 45.9000, 6.1000, domain.ConnectorTypeCCS)
	fastnedDC.OperatorName = "FASTNED"
	fastnedDC.Enseigne = "FASTNED"
	fastnedDCID, err := stationRepo.UpsertStation(ctx, fastnedDC)
	if err != nil {
		t.Fatalf("UpsertStation fastned dc: %v", err)
	}

	// Case-insensitive, and matched via enseigne rather than operator_name
	// — IRVE data isn't consistent about which column carries a network's
	// brand name for a given station.
	fastnedViaEnseigne := testIRVEStation("FRFASTNED2", 45.9100, 6.1100, domain.ConnectorTypeOther)
	fastnedViaEnseigne.OperatorName = "Some Legal Entity SAS"
	fastnedViaEnseigne.Enseigne = "Fastned"
	fastnedViaEnseigneID, err := stationRepo.UpsertStation(ctx, fastnedViaEnseigne)
	if err != nil {
		t.Fatalf("UpsertStation fastned via enseigne: %v", err)
	}

	other := testIRVEStation("FROTHER0001", 45.9200, 6.1200, domain.ConnectorTypeCCS)
	other.OperatorName = "Electra"
	other.Enseigne = "Electra"
	otherID, err := stationRepo.UpsertStation(ctx, other)
	if err != nil {
		t.Fatalf("UpsertStation other: %v", err)
	}

	ing := NewFastnedIngester(pool, stationRepo, tariffRepo)
	n, err := ing.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n != 2 {
		t.Fatalf("Run() = %d stations, want 2", n)
	}

	dcTariffs, err := tariffRepo.ListByStation(ctx, fastnedDCID)
	if err != nil {
		t.Fatalf("ListByStation fastnedDC: %v", err)
	}
	assertFastnedTariffs(t, dcTariffs, domain.TariffKindDC)

	fallbackTariffs, err := tariffRepo.ListByStation(ctx, fastnedViaEnseigneID)
	if err != nil {
		t.Fatalf("ListByStation fastnedViaEnseigne: %v", err)
	}
	// ConnectorTypeOther doesn't map to ac/dc via TariffKindForConnector,
	// so Run must fall back to dc (Fastned is rapid-charging only).
	assertFastnedTariffs(t, fallbackTariffs, domain.TariffKindDC)

	otherTariffs, err := tariffRepo.ListByStation(ctx, otherID)
	if err != nil {
		t.Fatalf("ListByStation other: %v", err)
	}
	if len(otherTariffs) != 0 {
		t.Errorf("other station got %d fastned tariffs, want 0 (not a fastned station)", len(otherTariffs))
	}
}

func assertFastnedTariffs(t *testing.T, tariffs []domain.StationTariff, wantKind string) {
	t.Helper()
	if len(tariffs) != 2 {
		t.Fatalf("got %d tariffs, want 2 (standard + subscription)", len(tariffs))
	}
	byPlan := map[string]domain.StationTariff{}
	for _, tf := range tariffs {
		if tf.Source != "fastned" {
			t.Errorf("tariff source = %q, want fastned", tf.Source)
		}
		if tf.Kind != wantKind {
			t.Errorf("tariff kind = %q, want %q", tf.Kind, wantKind)
		}
		byPlan[tf.Plan] = tf
	}
	standard, ok := byPlan[domain.TariffPlanStandard]
	if !ok || standard.EnergyPriceCentsPerKWh == nil || *standard.EnergyPriceCentsPerKWh != fastnedStandardCentsPerKWh {
		t.Errorf("standard tariff = %+v, want energy_price_cents_per_kwh=%v", standard, fastnedStandardCentsPerKWh)
	}
	subscription, ok := byPlan[fastnedSubscriptionPlan]
	if !ok || subscription.EnergyPriceCentsPerKWh == nil || *subscription.EnergyPriceCentsPerKWh != fastnedSubscriptionCentsPerKWh {
		t.Errorf("subscription tariff = %+v, want energy_price_cents_per_kwh=%v", subscription, fastnedSubscriptionCentsPerKWh)
	}
}
