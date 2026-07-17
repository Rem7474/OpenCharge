package ingestion

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"opencharge/internal/domain"
	"opencharge/internal/repository"
)

func TestIonityIngester_Run(t *testing.T) {
	pool := setupLinkingTestPool(t)
	ctx := context.Background()
	stationRepo := repository.NewStationRepository(pool)
	tariffRepo := repository.NewTariffRepository(pool)

	ionity := testIRVEStation("FRIONITY001", 45.9000, 6.1000, domain.ConnectorTypeCCS)
	ionity.OperatorName = "IONITY"
	ionity.Enseigne = "IONITY"
	ionityID, err := stationRepo.UpsertStation(ctx, ionity)
	if err != nil {
		t.Fatalf("UpsertStation ionity: %v", err)
	}

	// Matched via enseigne with different casing — same IRVE-consistency
	// concern as fastned/lidl.
	ionityViaEnseigne := testIRVEStation("FRIONITY002", 45.9100, 6.1100, domain.ConnectorTypeT2)
	ionityViaEnseigne.OperatorName = "Some Legal Entity SAS"
	ionityViaEnseigne.Enseigne = "Ionity"
	ionityViaEnseigneID, err := stationRepo.UpsertStation(ctx, ionityViaEnseigne)
	if err != nil {
		t.Fatalf("UpsertStation ionity via enseigne: %v", err)
	}

	other := testIRVEStation("FROTHER0003", 45.9200, 6.1200, domain.ConnectorTypeCCS)
	other.OperatorName = "Electra"
	other.Enseigne = "Electra"
	otherID, err := stationRepo.UpsertStation(ctx, other)
	if err != nil {
		t.Fatalf("UpsertStation other: %v", err)
	}

	ing := NewIonityIngester(pool, stationRepo, tariffRepo)
	n, err := ing.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n != 2 {
		t.Fatalf("Run() = %d stations, want 2", n)
	}

	assertIonityTariffs(t, ctx, tariffRepo, ionityID)
	// kind is always dc, even for a station whose own IRVE connector_type
	// is T2 (Ionity is HPC-only — its own tariff doesn't depend on
	// whatever connector_type this particular IRVE row happens to carry).
	assertIonityTariffs(t, ctx, tariffRepo, ionityViaEnseigneID)

	otherTariffs, err := tariffRepo.ListByStation(ctx, otherID)
	if err != nil {
		t.Fatalf("ListByStation other: %v", err)
	}
	if len(otherTariffs) != 0 {
		t.Errorf("other station got %d ionity tariffs, want 0 (not an ionity station)", len(otherTariffs))
	}
}

func assertIonityTariffs(t *testing.T, ctx context.Context, tariffRepo *repository.TariffRepository, stationID uuid.UUID) {
	t.Helper()
	tariffs, err := tariffRepo.ListByStation(ctx, stationID)
	if err != nil {
		t.Fatalf("ListByStation: %v", err)
	}
	if len(tariffs) != 2 {
		t.Fatalf("got %d tariffs, want 2 (public + app)", len(tariffs))
	}
	byPlan := map[string]domain.StationTariff{}
	for _, tf := range tariffs {
		if tf.Source != "ionity" {
			t.Errorf("tariff source = %q, want ionity", tf.Source)
		}
		if tf.Kind != domain.TariffKindDC {
			t.Errorf("tariff kind = %q, want dc", tf.Kind)
		}
		byPlan[tf.Plan] = tf
	}
	public, ok := byPlan["public"]
	if !ok || public.EnergyPriceCentsPerKWh == nil || *public.EnergyPriceCentsPerKWh != ionityPublicCentsPerKWh {
		t.Errorf("public tariff = %+v, want energy_price_cents_per_kwh=%v", public, ionityPublicCentsPerKWh)
	}
	app, ok := byPlan["app"]
	if !ok || app.EnergyPriceCentsPerKWh == nil || *app.EnergyPriceCentsPerKWh != ionityAppCentsPerKWh {
		t.Errorf("app tariff = %+v, want energy_price_cents_per_kwh=%v", app, ionityAppCentsPerKWh)
	}
}
