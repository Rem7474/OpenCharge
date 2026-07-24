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

// eborn's own published flat rates: three plans (standard, a discounted
// "carte" tier for cardholders, and a "forfait" monthly subscription that
// makes charging itself free), each with three power brackets — AC, DC
// 25-60kW, and DC >60kW. Confirmed manually, not scraped — same reasoning
// as fastned.go: eborn exposes no station-list/pricing API, but its
// network is already present in the IRVE referential, so there is nothing
// to fetch.
const (
	ebornSourceName     = "eborn"
	ebornOperatorNeedle = "eborn"
	ebornTariffModel    = "eborn_fixed"

	ebornCardPlan         = "card"
	ebornSubscriptionPlan = "subscription"

	// eborn's own subscription ("forfait") makes energy itself free, but
	// costs a flat monthly fee — there's no field on domain.StationTariff
	// for a recurring (as opposed to per-session) cost, so it's recorded
	// as RawText instead of silently showing "0,00 €/kWh" with no context.
	ebornSubscriptionMonthlyFeeText = "Abonnement forfait eborn : 49 €/mois, recharge incluse"

	ebornACStandardCentsPerKWh     = 43.3
	ebornACCardCentsPerKWh         = 31.0
	ebornDCMidStandardCentsPerKWh  = 57.3
	ebornDCMidCardCentsPerKWh      = 43.3
	ebornDCHighStandardCentsPerKWh = 65.0
	ebornDCHighCardCentsPerKWh     = 58.8
	// DC power bracket boundary (kW): eborn's own tariff table splits at
	// 60kW ("entre 25 et 60 kW" vs "> 60"); a dc station below 25kW isn't
	// a bracket eborn publishes a distinct price for, so it falls into the
	// same "mid" bracket as 25-60kW rather than being dropped.
	ebornDCPowerBracketKW = 60.0
)

// EbornIngester tags IRVE stations operated by eborn with fixed tariffs
// selected per-station from its own connector kind (ac/dc) and power_kw —
// unlike Fastned/Lidl's single flat rate, eborn's price genuinely depends
// on which of the two properties IRVE already records for that specific
// station, so each station gets exactly one price per plan, not every
// bracket. Same shape otherwise: no external source to fetch or correlate
// by geolocation, writes station_tariffs directly against station_id.
type EbornIngester struct {
	Pool     *pgxpool.Pool
	Stations *repository.StationRepository
	Tariffs  *repository.TariffRepository
}

func NewEbornIngester(pool *pgxpool.Pool, stations *repository.StationRepository, tariffs *repository.TariffRepository) *EbornIngester {
	return &EbornIngester{Pool: pool, Stations: stations, Tariffs: tariffs}
}

func (ing *EbornIngester) Run(ctx context.Context) (int, error) {
	runStart := time.Now()

	stations, err := ing.Stations.ListByOperatorLike(ctx, ebornOperatorNeedle)
	if err != nil {
		return 0, fmt.Errorf("list eborn stations: %w", err)
	}
	slog.Info("IRVE stations found", "source", ebornSourceName, "count", len(stations))

	tariffs := make([]domain.StationTariff, 0, len(stations)*3)
	for _, s := range stations {
		kind := domain.TariffKindForConnector(s.ConnectorType)
		if kind == "" {
			// Unlike Fastned (HPC-only, so "dc" is always a safe
			// fallback), eborn genuinely operates both ac and dc
			// stations, so a connector_type this project can't classify
			// isn't safe to price at all — skip rather than guess.
			continue
		}

		var standard, card float64
		if kind == domain.TariffKindAC {
			standard, card = ebornACStandardCentsPerKWh, ebornACCardCentsPerKWh
		} else if s.PowerKW != nil && *s.PowerKW > ebornDCPowerBracketKW {
			standard, card = ebornDCHighStandardCentsPerKWh, ebornDCHighCardCentsPerKWh
		} else {
			standard, card = ebornDCMidStandardCentsPerKWh, ebornDCMidCardCentsPerKWh
		}
		free := 0.0

		tariffs = append(tariffs,
			domain.StationTariff{
				StationID: s.ID, Source: ebornSourceName, Plan: domain.TariffPlanStandard, Kind: kind,
				Model: ebornTariffModel, Currency: "EUR", EnergyPriceCentsPerKWh: &standard, Extra: map[string]any{},
			},
			domain.StationTariff{
				StationID: s.ID, Source: ebornSourceName, Plan: ebornCardPlan, Kind: kind,
				Model: ebornTariffModel, Currency: "EUR", EnergyPriceCentsPerKWh: &card, Extra: map[string]any{},
			},
			domain.StationTariff{
				StationID: s.ID, Source: ebornSourceName, Plan: ebornSubscriptionPlan, Kind: kind,
				Model: ebornTariffModel, Currency: "EUR", EnergyPriceCentsPerKWh: &free,
				RawText: ebornSubscriptionMonthlyFeeText, Extra: map[string]any{},
			},
		)
	}

	if err := ing.Tariffs.BulkUpsert(ctx, tariffs); err != nil {
		return 0, fmt.Errorf("bulk upsert eborn tariffs: %w", err)
	}
	slog.Info("ingestion done", "source", ebornSourceName, "tariffs", len(tariffs), "stations", len(stations))

	if len(stations) > 0 {
		if err := repository.SweepStaleSourceData(ctx, ing.Pool, ebornSourceName, runStart.Add(-repository.StaleSourceDataGracePeriod)); err != nil {
			return len(stations), err
		}
	}
	return len(stations), nil
}
