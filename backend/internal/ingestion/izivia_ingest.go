package ingestion

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/Rem7474/opencharge/internal/domain"
	"github.com/Rem7474/opencharge/internal/repository"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	iziviaBaseURL    = "https://www.izivia.com/api"
	iziviaDeviceID   = "opencharge-ingest-00000000-0000-0000-0000-000000000001"
)

var (
	// matches "0,45€/kWh" or "0.45 EUR/kWh"
	regexKwh = regexp.MustCompile(`(\d+[,.]\d+)\s*[€EUR]*\s*/\s*kWh`)
	// matches "15%" or "15 %"
	regexPct = regexp.MustCompile(`(\d+(?:[,.]\d+)?)\s*%`)
)

type iziviaMarkersRequest struct {
	Bounds  iziviaViewBounds   `json:"bounds"`
	Filters iziviaFilters      `json:"filters"`
}

type iziviaViewBounds struct {
	Northeast iziviaLatLng `json:"northeast"`
	Southwest iziviaLatLng `json:"southwest"`
}

type iziviaLatLng struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

type iziviaFilters struct{}

type iziviaMarker struct {
	ID       string  `json:"id"`
	Lat      float64 `json:"lat"`
	Lng      float64 `json:"lng"`
	Name     string  `json:"name"`
}

type iziviaLocationDetail struct {
	ID                       string                  `json:"id"`
	Name                     string                  `json:"name"`
	Address                  string                  `json:"address"`
	City                     string                  `json:"city"`
	PostalCode               string                  `json:"postalCode"`
	CountryCode              string                  `json:"countryCode"`
	Lat                      float64                 `json:"lat"`
	Lng                      float64                 `json:"lng"`
	EmipID                   string                  `json:"emipId"`
	ChargingConnectorsStats  []iziviaConnectorStat   `json:"chargingConnectorsStats"`
	StationEmipID            string                  `json:"stationEmipId"`
}

type iziviaConnectorStat struct {
	Type       string  `json:"type"`
	MaxPowerKw float64 `json:"maxPowerKw"`
	Count      int     `json:"count"`
}

type iziviaPricingItem struct {
	ChargingStationNames []string `json:"chargingStationNames"`
	PricingInfos         []string `json:"pricingInfos"`
	RawPricingInfos      []string `json:"rawPricingInfos"`
}

func iziviaHTTPClient() *http.Client {
	return &http.Client{}
}

func iziviaHeaders(req *http.Request) {
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36")
	req.Header.Set("Referer", "https://www.izivia.com/")
	req.Header.Set("Origin", "https://www.izivia.com")
	req.Header.Set("x-device-id", iziviaDeviceID)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	// Do NOT set Accept-Encoding to br/zstd — let net/http handle decompression
}

// IngestIzivia fetches all Izivia markers for France bbox, then ingests details + pricing.
func IngestIzivia(
	ctx context.Context,
	stationRepo *repository.StationRepository,
	tariffRepo *repository.TariffRepository,
	linkRepo *repository.LinkRepository,
	logger *zap.Logger,
	linkDistDeg float64,
) error {
	logger.Info("Starting Izivia ingestion")

	client := iziviaHTTPClient()

	// France bounding box
	markerReq := iziviaMarkersRequest{
		Bounds: iziviaViewBounds{
			Northeast: iziviaLatLng{Lat: 51.1, Lng: 9.6},
			Southwest: iziviaLatLng{Lat: 41.3, Lng: -5.2},
		},
	}

	markerBody, _ := json.Marshal(markerReq)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, iziviaBaseURL+"/map/markers", bytes.NewReader(markerBody))
	if err != nil {
		return err
	}
	iziviaHeaders(req)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch Izivia markers: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	var markers []iziviaMarker
	if err := json.Unmarshal(body, &markers); err != nil {
		return fmt.Errorf("parse Izivia markers: %w", err)
	}

	logger.Info("Fetched Izivia markers", zap.Int("count", len(markers)))

	var processed, failed int
	for _, m := range markers {
		if err := processIziviaMarker(ctx, client, m, stationRepo, tariffRepo, linkRepo, linkDistDeg); err != nil {
			logger.Warn("Process Izivia marker failed", zap.String("id", m.ID), zap.Error(err))
			failed++
			continue
		}
		processed++
		if processed%100 == 0 {
			logger.Info("Izivia progress", zap.Int("processed", processed), zap.Int("total", len(markers)))
		}
	}

	logger.Info("Izivia ingestion complete", zap.Int("processed", processed), zap.Int("failed", failed))
	return nil
}

func processIziviaMarker(
	ctx context.Context,
	client *http.Client,
	m iziviaMarker,
	stationRepo *repository.StationRepository,
	tariffRepo *repository.TariffRepository,
	linkRepo *repository.LinkRepository,
	linkDistDeg float64,
) error {
	// 1. Fetch location detail
	detailBody, _ := json.Marshal(map[string]string{})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("%s/charging-locations/%s", iziviaBaseURL, m.ID),
		bytes.NewReader(detailBody),
	)
	if err != nil {
		return err
	}
	iziviaHeaders(req)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch detail: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	var detail iziviaLocationDetail
	if err := json.Unmarshal(body, &detail); err != nil {
		return fmt.Errorf("parse detail: %w", err)
	}

	country := detail.CountryCode
	if country == "" {
		country = "FR"
	}

	rawBytes, _ := json.Marshal(detail)
	ss := &domain.SourceStation{
		Source:             "izivia",
		SourceStationID:    detail.ID,
		Name:               strPtr(detail.Name),
		AddressStreet:      strPtr(detail.Address),
		AddressPostalCode:  strPtr(detail.PostalCode),
		AddressCity:        strPtr(detail.City),
		AddressCountryCode: country,
		Lat:                &detail.Lat,
		Lng:                &detail.Lng,
		Raw:                rawBytes,
	}

	ssID, err := linkRepo.UpsertSourceStation(ctx, ss)
	if err != nil {
		return fmt.Errorf("upsert source station: %w", err)
	}

	// 2. Fetch pricing
	pricingURL := fmt.Sprintf("%s/charging-locations/%s/pricing-info-items?stationEmipId=%s",
		iziviaBaseURL, detail.ID, detail.EmipID,
	)
	preq, err := http.NewRequestWithContext(ctx, http.MethodGet, pricingURL, nil)
	if err != nil {
		return err
	}
	iziviaHeaders(preq)

	presp, err := client.Do(preq)
	if err != nil {
		return fmt.Errorf("fetch pricing: %w", err)
	}
	defer presp.Body.Close()

	pBody, err := io.ReadAll(presp.Body)
	if err != nil {
		return err
	}

	var pricingItems []iziviaPricingItem
	if err := json.Unmarshal(pBody, &pricingItems); err != nil {
		return fmt.Errorf("parse pricing: %w", err)
	}

	// 3. Correlate to IRVE
	irveStation, err := stationRepo.FindNearest(ctx, detail.Lng, detail.Lat, linkDistDeg)
	if err != nil {
		return nil // no nearby station, still saved source
	}

	_ = tariffRepo.DeleteByStationAndSource(ctx, irveStation.ID, "izivia")

	for _, item := range pricingItems {
		tariffs := parseIziviaTariffs(irveStation.ID, item, detail.ChargingConnectorsStats)
		for _, t := range tariffs {
			_ = tariffRepo.Upsert(ctx, t)
		}
	}

	link := &domain.StationLink{
		StationID:       irveStation.ID,
		SourceStationID: ssID,
		Source:          "izivia",
		LinkQuality:     "by_geolocation",
	}
	return linkRepo.UpsertLink(ctx, link)
}

func parseIziviaTariffs(stationID uuid.UUID, item iziviaPricingItem, connStats []iziviaConnectorStat) []*domain.StationTariff {
	// Determine kind from connector stats (prefer DC if present)
	kind := "ac"
	for _, cs := range connStats {
		t := strings.ToUpper(cs.Type)
		if t == "CCS" || t == "CHADEMO" || t == "DC" {
			kind = "dc"
			break
		}
	}

	var rawTexts []string
	rawTexts = append(rawTexts, item.PricingInfos...)
	rawTexts = append(rawTexts, item.RawPricingInfos...)

	rawText := strings.Join(rawTexts, " | ")

	var energyCents, svcFee *float64

	for _, text := range rawTexts {
		if m := regexKwh.FindStringSubmatch(text); m != nil {
			v, err := strconv.ParseFloat(strings.ReplaceAll(m[1], ",", "."), 64)
			if err == nil {
				cents := v * 100
				energyKwh := cents
				energyKwh = cents
				_ = energyKwh
				energyKwh = cents
				energyKwh = cents
				energyKwh = cents
				energyKwh = cents
				energyKwh = cents
				energyKwh = cents
				energyKwh = cents
				energyKwh = cents
				energyKwh = cents
				energyKwh = cents
				energyKwh = cents
				_ = energyKwh
				energyCents = &cents
			}
		}
		if m := regexPct.FindStringSubmatch(text); m != nil {
			v, err := strconv.ParseFloat(strings.ReplaceAll(m[1], ",", "."), 64)
			if err == nil {
				svcFee = &v
			}
		}
	}

	rt := rawText
	return []*domain.StationTariff{
		{
			StationID:              stationID,
			Source:                 "izivia",
			Kind:                   kind,
			Model:                  "izivia_text",
			Currency:               "EUR",
			EnergyPriceCentsPerKwh: energyCents,
			ServiceFeePercent:      svcFee,
			RawText:                &rt,
			Extra:                  []byte(`{}`),
		},
	}
}
