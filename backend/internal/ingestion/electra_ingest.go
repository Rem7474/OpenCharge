package ingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Rem7474/opencharge/internal/domain"
	"github.com/Rem7474/opencharge/internal/repository"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const electraStationsURL = "https://stations.go-electra.com/stations.js"

type electraStation struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Address    string         `json:"address"`
	City       string         `json:"city"`
	PostalCode string         `json:"postalCode"`
	Country    string         `json:"country"`
	Latitude   float64        `json:"latitude"`
	Longitude  float64        `json:"longitude"`
	Operator   string         `json:"operator"`
	Pricings   electraPricings `json:"pricings"`
}

type electraPricings struct {
	AC []electraPricing `json:"ac"`
	DC []electraPricing `json:"dc"`
}

type electraPricing struct {
	EnergyPricePerKwh          *float64        `json:"energyPricePerKwh"`
	SessionDurationPricePerMin *float64        `json:"sessionDurationPricePerMin"`
	CongestionPricePerMin      *float64        `json:"congestionPricePerMin"`
	Windows                    []electraWindow `json:"windows"`
}

type electraWindow struct {
	StartTime string `json:"startTime"`
	EndTime   string `json:"endTime"`
}

// IngestElectra downloads stations.js, upserts source stations + tariffs, and links to IRVE.
func IngestElectra(
	ctx context.Context,
	stationRepo *repository.StationRepository,
	tariffRepo *repository.TariffRepository,
	linkRepo *repository.LinkRepository,
	logger *zap.Logger,
	linkDistDeg float64,
) error {
	logger.Info("Starting Electra ingestion", zap.String("url", electraStationsURL))

	resp, err := http.Get(electraStationsURL)
	if err != nil {
		return fmt.Errorf("download Electra stations.js: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read Electra body: %w", err)
	}

	js := strings.TrimSpace(string(body))
	js = strings.TrimPrefix(js, "export default ")
	js = strings.TrimSuffix(js, ";")

	var stations []electraStation
	if err := json.Unmarshal([]byte(js), &stations); err != nil {
		return fmt.Errorf("parse Electra JSON: %w", err)
	}

	logger.Info("Parsed Electra stations", zap.Int("count", len(stations)))

	var processed, failed int
	for _, es := range stations {
		if err := processElectraStation(ctx, es, stationRepo, tariffRepo, linkRepo, linkDistDeg); err != nil {
			logger.Warn("Process Electra station failed", zap.String("id", es.ID), zap.Error(err))
			failed++
			continue
		}
		processed++
	}

	logger.Info("Electra ingestion complete", zap.Int("processed", processed), zap.Int("failed", failed))
	return nil
}

func processElectraStation(
	ctx context.Context,
	es electraStation,
	stationRepo *repository.StationRepository,
	tariffRepo *repository.TariffRepository,
	linkRepo *repository.LinkRepository,
	linkDistDeg float64,
) error {
	country := es.Country
	if country == "" {
		country = "FR"
	}

	rawBytes, _ := json.Marshal(es)
	ss := &domain.SourceStation{
		Source:             "electra",
		SourceStationID:    es.ID,
		Name:               strPtr(es.Name),
		OperatorName:       strPtr(es.Operator),
		AddressStreet:      strPtr(es.Address),
		AddressPostalCode:  strPtr(es.PostalCode),
		AddressCity:        strPtr(es.City),
		AddressCountryCode: country,
		Lat:                &es.Latitude,
		Lng:                &es.Longitude,
		Raw:                rawBytes,
	}

	ssID, err := linkRepo.UpsertSourceStation(ctx, ss)
	if err != nil {
		return fmt.Errorf("upsert source station: %w", err)
	}

	irveStation, err := stationRepo.FindNearest(ctx, es.Longitude, es.Latitude, linkDistDeg)
	if err != nil {
		// No nearby IRVE station found — still saved the source station
		return nil
	}

	_ = tariffRepo.DeleteByStationAndSource(ctx, irveStation.ID, "electra")

	for _, pricing := range es.Pricings.AC {
		_ = tariffRepo.Upsert(ctx, buildElectraTariff(irveStation.ID, "ac", pricing))
	}
	for _, pricing := range es.Pricings.DC {
		_ = tariffRepo.Upsert(ctx, buildElectraTariff(irveStation.ID, "dc", pricing))
	}

	link := &domain.StationLink{
		StationID:       irveStation.ID,
		SourceStationID: ssID,
		Source:          "electra",
		LinkQuality:     "by_geolocation",
	}
	return linkRepo.UpsertLink(ctx, link)
}

func buildElectraTariff(stationID uuid.UUID, kind string, p electraPricing) *domain.StationTariff {
	var energyCents, sessionCents, congestionCents *float64
	if p.EnergyPricePerKwh != nil {
		v := *p.EnergyPricePerKwh * 100
		energyKwh := v
		energyKwh = v
		_ = energyKwh
		energyKwh = v
		energyCents = &v
	}
	if p.SessionDurationPricePerMin != nil {
		v := *p.SessionDurationPricePerMin * 100
		sessionCents = &v
	}
	if p.CongestionPricePerMin != nil {
		v := *p.CongestionPricePerMin * 100
		congestionCents = &v
	}

	extraBytes, _ := json.Marshal(map[string]interface{}{"windows": p.Windows})

	return &domain.StationTariff{
		StationID:                  stationID,
		Source:                     "electra",
		Kind:                       kind,
		Model:                      "electra_fixed",
		Currency:                   "EUR",
		EnergyPriceCentsPerKwh:     energyCents,
		SessionPriceCentsPerMin:    sessionCents,
		CongestionPriceCentsPerMin: congestionCents,
		Extra:                      extraBytes,
	}
}
