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

// Lidl's own published flat rate: a single price regardless of connector
// (AC or DC) or time of day, and a single plan (no subscription tier).
// Confirmed manually, not scraped — same reasoning as fastned.go: Lidl
// exposes no station-list/pricing API, but its network is already present
// in the IRVE referential, so there is nothing to fetch.
const (
	lidlSourceName      = "lidl"
	lidlOperatorNeedle  = "lidl"
	lidlFlatCentsPerKWh = 29.0
	lidlTariffModel     = "lidl_fixed"
)

// LidlIngester tags IRVE stations operated by Lidl with a single flat
// tariff. Same shape as FastnedIngester: no external source to fetch or
// correlate by geolocation, writes station_tariffs directly against
// station_id.
type LidlIngester struct {
	Pool     *pgxpool.Pool
	Stations *repository.StationRepository
	Tariffs  *repository.TariffRepository
}

func NewLidlIngester(pool *pgxpool.Pool, stations *repository.StationRepository, tariffs *repository.TariffRepository) *LidlIngester {
	return &LidlIngester{Pool: pool, Stations: stations, Tariffs: tariffs}
}

func (ing *LidlIngester) Run(ctx context.Context) (int, error) {
	runStart := time.Now()

	stations, err := ing.Stations.ListByOperatorLike(ctx, lidlOperatorNeedle)
	if err != nil {
		return 0, fmt.Errorf("list lidl stations: %w", err)
	}
	log.Printf("lidl: %d IRVE stations found", len(stations))

	tariffs := make([]domain.StationTariff, 0, len(stations))
	for _, s := range stations {
		price := lidlFlatCentsPerKWh
		tariffs = append(tariffs, domain.StationTariff{
			// kind=mixed (not derived from connector_type): the price is
			// the same for AC and DC alike, so this must feed both the ac
			// and dc aggregates (see station_repo.go's "kind IN ('ac',
			// 'mixed')" / "kind IN ('dc', 'mixed')" FILTER clauses) rather
			// than being pinned to whichever kind this station happens to
			// expose.
			StationID: s.ID, Source: lidlSourceName, Plan: domain.TariffPlanStandard, Kind: domain.TariffKindMixed,
			Model: lidlTariffModel, Currency: "EUR", EnergyPriceCentsPerKWh: &price, Extra: map[string]any{},
		})
	}

	if err := ing.Tariffs.BulkUpsert(ctx, tariffs); err != nil {
		return 0, fmt.Errorf("bulk upsert lidl tariffs: %w", err)
	}
	log.Printf("lidl: done, %d tariffs written for %d stations", len(tariffs), len(stations))

	// Only sweep after actually finding stations this run — see the same
	// guard (and the incident that motivated it) in izivia.go.
	if len(stations) > 0 {
		if err := repository.SweepStaleSourceData(ctx, ing.Pool, lidlSourceName, runStart.Add(-repository.StaleSourceDataGracePeriod)); err != nil {
			return len(stations), err
		}
	}
	return len(stations), nil
}
