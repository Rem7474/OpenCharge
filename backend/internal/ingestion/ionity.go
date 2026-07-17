package ingestion

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"opencharge/internal/domain"
	"opencharge/internal/repository"
)

// Ionity's own published flat rates: no time-of-day variation, no
// per-connector-power difference — a public (pay-as-you-go, no app) price
// and a cheaper app price, same shape as Electra's "public"/"app" plans
// (reusing those plan ids so the frontend's existing plan-label mapping
// needs no changes). Confirmed manually, not scraped — same reasoning as
// fastned.go: Ionity exposes no station-list/pricing API, but its network
// is already present in the IRVE referential, so there is nothing to
// fetch.
const (
	ionitySourceName        = "ionity"
	ionityOperatorNeedle    = "ionity"
	ionityPublicCentsPerKWh = 55.0
	ionityAppCentsPerKWh    = 52.0
	ionityAppPlan           = "app"
	ionityPublicPlan        = "public"
	ionityTariffModel       = "ionity_fixed"
)

// IonityIngester tags IRVE stations operated by Ionity with two fixed
// tariffs. Same shape as FastnedIngester: no external source to fetch or
// correlate by geolocation, writes station_tariffs directly against
// station_id. Ionity is an HPC-only network (150-350kW CCS), so kind is
// always dc regardless of the matched station's own connector_type.
type IonityIngester struct {
	Pool     *pgxpool.Pool
	Stations *repository.StationRepository
	Tariffs  *repository.TariffRepository
}

func NewIonityIngester(pool *pgxpool.Pool, stations *repository.StationRepository, tariffs *repository.TariffRepository) *IonityIngester {
	return &IonityIngester{Pool: pool, Stations: stations, Tariffs: tariffs}
}

func (ing *IonityIngester) Run(ctx context.Context) (int, error) {
	runStart := time.Now()

	stations, err := ing.Stations.ListByOperatorLike(ctx, ionityOperatorNeedle)
	if err != nil {
		return 0, fmt.Errorf("list ionity stations: %w", err)
	}
	log.Printf("ionity: %d IRVE stations found", len(stations))

	tariffs := make([]domain.StationTariff, 0, len(stations)*2)
	for _, s := range stations {
		public := ionityPublicCentsPerKWh
		app := ionityAppCentsPerKWh
		tariffs = append(tariffs,
			domain.StationTariff{
				StationID: s.ID, Source: ionitySourceName, Plan: ionityPublicPlan, Kind: domain.TariffKindDC,
				Model: ionityTariffModel, Currency: "EUR", EnergyPriceCentsPerKWh: &public, Extra: map[string]any{},
			},
			domain.StationTariff{
				StationID: s.ID, Source: ionitySourceName, Plan: ionityAppPlan, Kind: domain.TariffKindDC,
				Model: ionityTariffModel, Currency: "EUR", EnergyPriceCentsPerKWh: &app, Extra: map[string]any{},
			},
		)
	}

	if err := ing.Tariffs.BulkUpsert(ctx, tariffs); err != nil {
		return 0, fmt.Errorf("bulk upsert ionity tariffs: %w", err)
	}
	log.Printf("ionity: done, %d tariffs written for %d stations", len(tariffs), len(stations))

	if len(stations) > 0 {
		if err := repository.SweepStaleSourceData(ctx, ing.Pool, ionitySourceName, runStart.Add(-repository.StaleSourceDataGracePeriod)); err != nil {
			return len(stations), err
		}
	}
	return len(stations), nil
}
