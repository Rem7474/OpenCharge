package ingestion

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"opencharge/internal/domain"
	"opencharge/internal/repository"
)

// Sowatt Solutions' own published flat rate: a single "standard" price
// regardless of connector (AC or DC) or time of day. Confirmed manually,
// not scraped — same reasoning as fastned.go/lidl.go: Sowatt exposes no
// station-list/pricing API, but its network is already present in the IRVE
// referential, so there is nothing to fetch.
const (
	sowattSourceName      = "sowatt"
	sowattOperatorNeedle  = "sowatt"
	sowattFlatCentsPerKWh = 54.0
	sowattTariffModel     = "sowatt_fixed"
)

// SowattIngester tags IRVE stations operated by Sowatt Solutions with a
// single flat tariff. Same shape as LidlIngester: no external source to
// fetch or correlate by geolocation, writes station_tariffs directly
// against station_id.
type SowattIngester struct {
	Pool     *pgxpool.Pool
	Stations *repository.StationRepository
	Tariffs  *repository.TariffRepository
}

func NewSowattIngester(pool *pgxpool.Pool, stations *repository.StationRepository, tariffs *repository.TariffRepository) *SowattIngester {
	return &SowattIngester{Pool: pool, Stations: stations, Tariffs: tariffs}
}

func (ing *SowattIngester) Run(ctx context.Context) (int, error) {
	runStart := time.Now()

	stations, err := ing.Stations.ListByOperatorLike(ctx, sowattOperatorNeedle)
	if err != nil {
		return 0, fmt.Errorf("list sowatt stations: %w", err)
	}
	slog.Info("IRVE stations found", "source", sowattSourceName, "count", len(stations))

	tariffs := make([]domain.StationTariff, 0, len(stations))
	for _, s := range stations {
		price := sowattFlatCentsPerKWh
		tariffs = append(tariffs, domain.StationTariff{
			// kind=mixed (not derived from connector_type): the price is
			// the same for AC and DC alike, so this must feed both the ac
			// and dc aggregates (see station_repo.go's "kind IN ('ac',
			// 'mixed')" / "kind IN ('dc', 'mixed')" FILTER clauses) rather
			// than being pinned to whichever kind this station happens to
			// expose.
			StationID: s.ID, Source: sowattSourceName, Plan: domain.TariffPlanStandard, Kind: domain.TariffKindMixed,
			Model: sowattTariffModel, Currency: "EUR", EnergyPriceCentsPerKWh: &price, Extra: map[string]any{},
		})
	}

	if err := ing.Tariffs.BulkUpsert(ctx, tariffs); err != nil {
		return 0, fmt.Errorf("bulk upsert sowatt tariffs: %w", err)
	}
	slog.Info("ingestion done", "source", sowattSourceName, "tariffs", len(tariffs), "stations", len(stations))

	// Only sweep after actually finding stations this run — see the same
	// guard (and the incident that motivated it) in izivia.go.
	if len(stations) > 0 {
		if err := repository.SweepStaleSourceData(ctx, ing.Pool, sowattSourceName, runStart.Add(-repository.StaleSourceDataGracePeriod)); err != nil {
			return len(stations), err
		}
	}
	return len(stations), nil
}
