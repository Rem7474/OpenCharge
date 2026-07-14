package ingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/Rem7474/opencharge/internal/domain"
	"github.com/Rem7474/opencharge/internal/repository"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const iziviaBaseURL = "https://fronts-map.izivia.com/api"
const defaultIziviaRadiusMeters = 120.0

var (
	rePrice    = regexp.MustCompile(`([0-9]+,[0-9]+)\s*€`)
	reFee      = regexp.MustCompile(`([0-9]+)%\s+de frais de service`)
)

func newIziviaSession() *http.Client {
	return &http.Client{}
}

func iziviaRequest(client *http.Client, method, url string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", "fr")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/150.0.0.0 Safari/537.36 Edg/150.0.0.0")
	req.Header.Set("Referer", "https://fronts-map.izivia.com/")
	req.Header.Set("Origin", "https://fronts-map.izivia.com")
	req.Header.Set("x-device-id", "b1a5a1c8-68b4-41fb-a18f-78d53910878a")
	// Ne pas forcer Accept-Encoding: laisser net/http gérer la décompression
	return client.Do(req)
}

// IngestIziviaSquare ingeste toutes les stations Izivia pour un carré donné.
func IngestIziviaSquare(ctx context.Context,
	linkRepo *repository.LinkRepository,
	tariffRepo *repository.TariffRepository,
	centerLng, centerLat float64,
	zoom int,
	radiusMeters float64,
) error {
	if radiusMeters <= 0 {
		radiusMeters = defaultIziviaRadiusMeters
	}

	client := newIziviaSession()

	// 1. Récupération des markers
	payload, _ := json.Marshal(map[string]interface{}{
		"square": map[string]interface{}{
			"centerLng": centerLng,
			"centerLat": centerLat,
			"zoom":      zoom,
		},
		"filters": map[string]interface{}{},
	})

	resp, err := iziviaRequest(client, "POST", iziviaBaseURL+"/map/markers", strings.NewReader(string(payload)))
	if err != nil {
		return fmt.Errorf("izivia markers: %w", err)
	}
	defer resp.Body.Close()

	var markers []struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&markers); err != nil {
		return fmt.Errorf("izivia markers decode: %w", err)
	}
	log.Printf("[Izivia] %d markers récupérés pour (%.4f,%.4f) zoom=%d", len(markers), centerLng, centerLat, zoom)

	for _, m := range markers {
		if err := processIziviaStation(ctx, client, m.ID, linkRepo, tariffRepo, radiusMeters); err != nil {
			log.Printf("[Izivia] process %s: %v", m.ID, err)
		}
	}
	return nil
}

func processIziviaStation(
	ctx context.Context,
	client *http.Client,
	stationID string,
	linkRepo *repository.LinkRepository,
	tariffRepo *repository.TariffRepository,
	radiusMeters float64,
) error {
	// Détails de la station
	detailPayload := strings.NewReader("{}")
	resp, err := iziviaRequest(client, "POST", iziviaBaseURL+"/charging-locations/"+stationID, detailPayload)
	if err != nil {
		return fmt.Errorf("station detail: %w", err)
	}
	defer resp.Body.Close()

	var station map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&station); err != nil {
		return fmt.Errorf("station detail decode: %w", err)
	}

	// Coordonnées
	coords, _ := station["coordinates"].([]interface{})
	if len(coords) < 2 {
		return fmt.Errorf("coordonnées manquantes")
	}
	lng, _ := coords[0].(float64)
	lat, _ := coords[1].(float64)

	addr, _ := station["address"].(map[string]interface{})
	name, _ := station["name"].(string)

	raw, _ := json.Marshal(station)

	ss := &domain.SourceStation{
		Source:             "izivia",
		SourceStationID:    stationID,
		Name:               name,
		OperatorName:       "Izivia",
		AddressStreet:      stringField(addr, "street"),
		AddressPostalCode:  stringField(addr, "postalCode"),
		AddressCity:        stringField(addr, "city"),
		AddressCountryCode: stringField(addr, "country"),
		Lat:                lat,
		Lng:                lng,
		Raw:                raw,
	}

	ssID, err := linkRepo.UpsertSourceStation(ctx, ss)
	if err != nil {
		return fmt.Errorf("UpsertSourceStation: %w", err)
	}

	// Corrélation IRVE
	IRVEStationID, _, err := linkRepo.FindNearestStation(ctx, lng, lat, radiusMeters)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil // pas de station IRVE à proximité, on saute
		}
		return fmt.Errorf("FindNearestStation: %w", err)
	}

	link := &domain.StationLink{
		StationID:       IRVEStationID,
		SourceStationID: ssID,
		Source:          "izivia",
		LinkQuality:     domain.LinkQualityByGeolocation,
	}
	if err := linkRepo.Upsert(ctx, link); err != nil {
		log.Printf("[Izivia] link upsert: %v", err)
	}

	// Pricing
	firstEmip, _ := station["firstStationEmipId"].(string)
	if firstEmip == "" {
		return nil
	}

	pricingResp, err := iziviaRequest(client, "GET",
		fmt.Sprintf("%s/charging-locations/%s/pricing-info-items?stationEmipId=%s", iziviaBaseURL, stationID, firstEmip),
		nil,
	)
	if err != nil {
		return fmt.Errorf("pricing fetch: %w", err)
	}
	defer pricingResp.Body.Close()

	var pricingData interface{}
	if err := json.NewDecoder(pricingResp.Body).Decode(&pricingData); err != nil {
		return nil // pricing facultatif
	}

	tariffs := parseIziviaPricing(IRVEStationID, pricingData)
	for _, t := range tariffs {
		if err := tariffRepo.Insert(ctx, t); err != nil {
			log.Printf("[Izivia] tariff insert: %v", err)
		}
	}
	return nil
}

func parseIziviaPricing(stationID uuid.UUID, data interface{}) []*domain.StationTariff {
	items, ok := data.([]interface{})
	if !ok {
		return nil
	}

	var tariffs []*domain.StationTariff
	for _, item := range items {
		obj, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		chargingStations, _ := obj["chargingStations"].([]interface{})
		for _, cs := range chargingStations {
			csObj, ok := cs.(map[string]interface{})
			if !ok {
				continue
			}

			// texte brut du tarif
			var rawText string
			if pricingInfos, ok := csObj["pricingInfos"].([]interface{}); ok && len(pricingInfos) > 0 {
				rawText, _ = pricingInfos[0].(string)
			} else if rawInfos, ok := csObj["rawPricingInfos"].([]interface{}); ok && len(rawInfos) > 0 {
				rawText, _ = rawInfos[0].(string)
			}
			if rawText == "" {
				continue
			}

			var energyCents *float64
			if m := rePrice.FindStringSubmatch(rawText); len(m) > 1 {
				pStr := strings.ReplaceAll(m[1], ",", ".")
				if v, err := strconv.ParseFloat(pStr, 64); err == nil {
					c := v * 100
					energyEents = &c
				}
			}

			var feePct *float64
			if m := reFee.FindStringSubmatch(rawText); len(m) > 1 {
				if v, err := strconv.ParseFloat(m[1], 64); err == nil {
					feePct = &v
				}
			}

			t := &domain.StationTariff{
				StationID:              stationID,
				Source:                 "izivia",
				Kind:                   "ac", // Izivia est majoritairement AC
				Model:                  "izivia_text",
				Currency:               "EUR",
				EnergyPriceCentsPerKwh: energyCents,
				ServiceFeePercent:      feePct,
				RawText:                &rawText,
				Extra:                  []byte("{}"),
			}
			tariffs = append(tariffs, t)
		}
	}
	return tariffs
}

func stringField(m map[string]interface{}, key string) string {
	if m == nil {
		return ""
	}
	v, _ := m[key].(string)
	return v
}
