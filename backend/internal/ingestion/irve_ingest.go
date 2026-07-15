package ingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/Rem7474/opencharge/internal/domain"
	"github.com/Rem7474/opencharge/internal/repository"
	"go.uber.org/zap"
)

const irveGeoJSONURL = "https://hydra.s3.rbx.io.cloud.ovh.net/geojson/eb76d20a-8501-400e-b336-d85724de5435.geojson"

type irveFeatureCollection struct {
	Type     string        `json:"type"`
	Features []irveFeature `json:"features"`
}

type irveFeature struct {
	Type       string                 `json:"type"`
	Geometry   irveGeometry           `json:"geometry"`
	Properties map[string]interface{} `json:"properties"`
}

type irveGeometry struct {
	Type        string    `json:"type"`
	Coordinates []float64 `json:"coordinates"`
}

// IngestIRVE downloads the IRVE GeoJSON and upserts all charging points.
func IngestIRVE(ctx context.Context, repo *repository.StationRepository, logger *zap.Logger) error {
	logger.Info("Starting IRVE ingestion", zap.String("url", irveGeoJSONURL))

	resp, err := http.Get(irveGeoJSONURL)
	if err != nil {
		return fmt.Errorf("download IRVE GeoJSON: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read IRVE GeoJSON body: %w", err)
	}

	var fc irveFeatureCollection
	if err := json.Unmarshal(body, &fc); err != nil {
		return fmt.Errorf("parse IRVE GeoJSON: %w", err)
	}

	logger.Info("Parsed GeoJSON", zap.Int("features", len(fc.Features)))

	var processed, failed int
	for i, feat := range fc.Features {
		if feat.Geometry.Type != "Point" || len(feat.Geometry.Coordinates) < 2 {
			failed++
			continue
		}

		s := featureToStation(feat)
		if err := repo.Upsert(ctx, s); err != nil {
			logger.Warn("Upsert failed", zap.Int("index", i), zap.Error(err))
			failed++
			continue
		}
		processed++
		if processed%1000 == 0 {
			logger.Info("Progress", zap.Int("processed", processed), zap.Int("total", len(fc.Features)))
		}
	}

	logger.Info("IRVE ingestion complete", zap.Int("processed", processed), zap.Int("failed", failed))
	return nil
}

func featureToStation(feat irveFeature) *domain.Station {
	p := feat.Properties
	s := &domain.Station{
		AddressCountryCode: "FR",
		Lng:                feat.Geometry.Coordinates[0],
		Lat:                feat.Geometry.Coordinates[1],
	}

	s.IRVEIDPDc = strPtr(propString(p, "id_pdc_itinerance"))
	s.IRVEIDStation = strPtr(propString(p, "id_station_itinerance"))
	s.OperatorName = strPtr(propString(p, "nom_operateur"))
	s.Amenageur = strPtr(propString(p, "nom_amenageur"))
	s.Enseigne = strPtr(propString(p, "nom_enseigne"))
	s.Name = strPtr(propString(p, "nom_station"))
	s.AddressStreet = strPtr(propString(p, "adresse_station"))
	s.AddressPostalCode = strPtr(propString(p, "code_postal"))
	s.AddressCity = strPtr(propString(p, "commune"))
	s.ConnectorType = strPtr(propString(p, "type_prise"))
	s.AccessType = strPtr(propString(p, "acces_recharge"))

	if pw := propString(p, "puissance_nominale"); pw != "" {
		if v, err := strconv.ParseFloat(strings.ReplaceAll(pw, ",", "."), 64); err == nil {
			s.PowerKw = &v
		}
	}

	if h := propString(p, "horaires"); h != "" {
		s.Is24_7 = boolPtr(strings.Contains(strings.ToLower(h), "24/7"))
	}

	// Keep remaining props as metadata JSONB
	metaKeys := []string{"id_pdc_itinerance", "id_station_itinerance", "nom_operateur",
		"nom_amenageur", "nom_enseigne", "nom_station", "adresse_station",
		"code_postal", "commune", "type_prise", "acces_recharge", "puissance_nominale", "horaires"}
	meta := map[string]interface{}{}
	for k, v := range p {
		if !contains(metaKeys, k) {
			meta[k] = v
		}
	}
	if raw, err := json.Marshal(meta); err == nil {
		s.Metadata = raw
	}

	return s
}

func propString(p map[string]interface{}, key string) string {
	v, ok := p[key]
	if !ok || v == nil {
		return ""
	}
	return fmt.Sprintf("%v", v)
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func boolPtr(b bool) *bool { return &b }

func contains(slice []string, val string) bool {
	for _, s := range slice {
		if s == val {
			return true
		}
	}
	return false
}
