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

// Fastned's own published flat rates: no time-of-day variation, no
// per-connector difference. Confirmed manually against fastned.com, not
// scraped — Fastned exposes no station-list/pricing API the way Electra or
// Izivia do, but its whole network is already present in the IRVE
// referential (matched by operator_name/enseigne), so there is nothing to
// fetch: this ingester just tags every already-known Fastned station with
// these two tariffs.
const (
	fastnedSourceName              = "fastned"
	fastnedOperatorNeedle          = "fastned"
	fastnedStandardCentsPerKWh     = 61.0
	fastnedSubscriptionCentsPerKWh = 43.0
	fastnedSubscriptionPlan        = "subscription"
	fastnedTariffModel             = "fastned_fixed"
)

// FastnedIngester tags IRVE stations operated by Fastned with fixed
// tariffs. Unlike every other ingester, there is no external source to
// fetch or correlate by geolocation: Fastned's stations already are the
// IRVE rows themselves, so this writes station_tariffs directly against
// station_id — no source_stations/station_links involved at all.
type FastnedIngester struct {
	Pool     *pgxpool.Pool
	Stations *repository.StationRepository
	Tariffs  *repository.TariffRepository
}

func NewFastnedIngester(pool *pgxpool.Pool, stations *repository.StationRepository, tariffs *repository.TariffRepository) *FastnedIngester {
	return &FastnedIngester{Pool: pool, Stations: stations, Tariffs: tariffs}
}

func (ing *FastnedIngester) Run(ctx context.Context) (int, error) {
	runStart := time.Now()

	stations, err := ing.Stations.ListByOperatorLike(ctx, fastnedOperatorNeedle)
	if err != nil {
		return 0, fmt.Errorf("list fastned stations: %w", err)
	}
	slog.Info("IRVE stations found", "source", fastnedSourceName, "count", len(stations))

	tariffs := make([]domain.StationTariff, 0, len(stations)*2)
	for _, s := range stations {
		// Fastned is a rapid-charging-only network (CCS/CHAdeMO, no AC
		// destination charging) — fall back to dc for any station whose
		// IRVE connector_type doesn't map cleanly (e.g. "other"/"unknown"),
		// rather than dropping its tariff entirely.
		kind := domain.TariffKindForConnector(s.ConnectorType)
		if kind == "" {
			kind = domain.TariffKindDC
		}
		standard := fastnedStandardCentsPerKWh
		subscription := fastnedSubscriptionCentsPerKWh
		tariffs = append(tariffs,
			domain.StationTariff{
				StationID: s.ID, Source: fastnedSourceName, Plan: domain.TariffPlanStandard, Kind: kind,
				Model: fastnedTariffModel, Currency: "EUR", EnergyPriceCentsPerKWh: &standard, Extra: map[string]any{},
			},
			domain.StationTariff{
				StationID: s.ID, Source: fastnedSourceName, Plan: fastnedSubscriptionPlan, Kind: kind,
				Model: fastnedTariffModel, Currency: "EUR", EnergyPriceCentsPerKWh: &subscription, Extra: map[string]any{},
			},
		)
	}

	if err := ing.Tariffs.BulkUpsert(ctx, tariffs); err != nil {
		return 0, fmt.Errorf("bulk upsert fastned tariffs: %w", err)
	}
	slog.Info("ingestion done", "source", fastnedSourceName, "tariffs", len(tariffs), "stations", len(stations))

	// Only sweep after actually finding stations this run — see the same
	// guard (and the incident that motivated it) in izivia.go. Sweeping
	// only ever affects station_tariffs here (there are no source_stations
	// rows for fastned to begin with), removing tariffs for a station that
	// no longer matches fastnedOperatorNeedle (renamed/removed from IRVE).
	if len(stations) > 0 {
		if err := repository.SweepStaleSourceData(ctx, ing.Pool, fastnedSourceName, runStart.Add(-repository.StaleSourceDataGracePeriod)); err != nil {
			return len(stations), err
		}
	}
	return len(stations), nil
}
