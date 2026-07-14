package ingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/Rem7474/opencharge/internal/domain"
	"github.com/Rem7474/opencharge/internal/repository"
	"github.com/jackc/pgx/v5"
)

const electraStationsURL = "https://stations.go-electra.com/stations.js"
const defaultElectraRadiusMeters = 150.0

type electraStation struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Address struct {
		Street     string `json:"street"`
		PostalCode string `json:"postalCode"`
		City       string `json:"city"`
		Country    string `json:"country"`
	} `json:"address"`
	Location struct {
		Lat float64 `json:"lat"`
		Lng float64 `json:"lng"`
	} `json:"location"`
	Pricings struct {
		AC []electraPricing `json:"ac"`
		DC []electraPricing `json:"dc"`
	} `json:"pricings"`
}

type electraPricing struct {
	EnergyPricePerKwh          *float64 `json:"energyPricePerKwh"`
	SessionDurationPricePerMin *float64 `json:"sessionDurationPricePerMin"`
	CongestionPricePerMin      *float64 `json:"congestionPricePerMin"`
	Windows                    []struct {
		StartTime string `json:"startTime"`
		EndTime   string `json:"endTime"`
	} `json:"windows"`
}

func IngestElectra(ctx context.Context, linkRepo *repository.LinkRepository, tariffRepo *repository.TariffRepository, radiusMeters float64) error {
	if radiusMeters <= 0 {
		radiusMeters = defaultElectraRadiusMeters
	}

	log.Println("[Electra] Téléchargement stations.js...")
	resp, err := http.Get(electraStationsURL)
	if err != nil {
		return fmt.Errorf("electra download: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("electra read body: %w", err)
	}

	// Nettoie le module JS : "export default [...];"
	src := strings.TrimSpace(string(body))
	src = strings.TrimPrefix(src, "export default")
	src = strings.TrimSpace(src)
	src = strings.TrimSuffix(src, ";")

	var stations []electraStation
	if err := json.Unmarshal([]byte(src), &stations); err != nil {
		return fmt.Errorf("electra unmarshal: %w", err)
	}
	log.Printf("[Electra] %d stations récupérées", len(stations))

	done, skipped := 0, 0
	for _, es := range stations {
		raw, _ := json.Marshal(es)

		ss := &domain.SourceStation{
			Source:             "electra",
			SourceStationID:    es.ID,
			Name:               es.Name,
			OperatorName:       "Electra",
			AddressStreet:      es.Address.Street,
			AddressPostalCode:  es.Address.PostalCode,
			AddressCity:        es.Address.City,
			AddressCountryCode: es.Address.Country,
			Lat:                es.Location.Lat,
			Lng:                es.Location.Lng,
			Raw:                raw,
		}

		ssID, err := linkRepo.UpsertSourceStation(ctx, ss)
		if err != nil {
			log.Printf("[Electra] UpsertSourceStation %s: %v", es.ID, err)
			skipped++
			continue
		}

		// Corrélation avec IRVE par géoloc
		IRVEStationID, _, err := linkRepo.FindNearestStation(ctx, es.Location.Lng, es.Location.Lat, radiusMeters)
		if err != nil {
			if err != pgx.ErrNoRows {
				log.Printf("[Electra] FindNearest %s: %v", es.ID, err)
			}
			skipped++
			continue
		}

		link := &domain.StationLink{
			StationID:       IRVEStationID,
			SourceStationID: ssID,
			Source:          "electra",
			LinkQuality:     domain.LinkQualityByGeolocation,
		}
		if err := linkRepo.Upsert(ctx, link); err != nil {
			log.Printf("[Electra] Link upsert %s: %v", es.ID, err)
		}

		// Tarifs AC
		for _, p := range es.Pricings.AC {
			t := buildElectraTariff(IRVEStationID, "ac", p)
			if err := tariffRepo.Insert(ctx, t); err != nil {
				log.Printf("[Electra] Tariff AC insert %s: %v", es.ID, err)
			}
		}
		// Tarifs DC
		for _, p := range es.Pricings.DC {
			t := buildElectraTariff(IRVEStationID, "dc", p)
			if err := tariffRepo.Insert(ctx, t); err != nil {
				log.Printf("[Electra] Tariff DC insert %s: %v", es.ID, err)
			}
		}

		done++
	}
	log.Printf("[Electra] Terminé: %d liés, %d ignorés/non corrélés", done, skipped)
	return nil
}

func buildElectraTariff(stationID interface{ ... }, kind string, p electraPricing) *domain.StationTariff {
	var energyCents, sessionCents, congestionCents *float64
	if p.EnergyPricePerKwh != nil {
		v := *p.EnergyPricePerKwh * 100
		energyKwh := v
		energyEents = &energyKwh
	}
	if p.SessionDurationPricePerMin != nil {
		v := *p.SessionDurationPricePerMin * 100
		sessionCents = &v
	}
	if p.CongestionPricePerMin != nil {
		v := *p.CongestionPricePerMin * 100
		congestionCents = &v
	}

	var extraWindows []map[string]string
	for _, w := range p.Windows {
		extraWindows = append(extraWindows, map[string]string{
			"startTime": w.StartTime,
			"endTime":   w.EndTime,
		})
	}
	extraJSON, _ := json.Marshal(map[string]interface{}{"windows": extraWindows})

	return &domain.StationTariff{
		StationID:                   stationID.(uuid.UUID),
		Source:                      "electra",
		Kind:                        kind,
		Model:                       "electra_fixed",
		Currency:                    "EUR",
		EnergyPriceCentsPerKwh:      energyCents,
		SessionPriceCentsPerMin:     sessionCents,
		CongestionPriceCentsPerMin:  congestionCents,
		Extra:                       extraJSON,
	}
}
