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
)

const irveGeoJSONURL = "https://hydra.s3.rbx.io.cloud.ovh.net/geojson/eb76d20a-8501-400e-b336-d85724de5435.geojson"

type irveFeatureCollection struct {
	Type     string        `json:"type"`
	Features []irveFeature `json:"features"`
}

type irveFeature struct {
	Type       string          `json:"type"`
	Geometry   irveGeometry    `json:"geometry"`
	Properties json.RawMessage `json:"properties"`
}

type irveGeometry struct {
	Type        string    `json:"type"`
	Coordinates []float64 `json:"coordinates"`
}

type irveProps struct {
	IDStation        string  `json:"id_station_itinerance"`
	IDPDC            string  `json:"id_pdc_itinerance"`
	NomStationItinerance string `json:"nom_station"`
	Adresse          string  `json:"adresse_station"`
	CodePostal       string  `json:"code_postal"`
	Commune          string  `json:"commune"`
	NomOperateur     string  `json:"nom_operateur"`
	NomEnseigneOp    string  `json:"nom_enseigne"`
	NomAmenageur     string  `json:"nom_amenageur"`
	PuissanceNominale float64 `json:"puissance_nominale"`
	PriseType        string  `json:"prise_type_ef"`
	PriseTypeCombo   string  `json:"prise_type_combo_ccs"`
	PriseTypeChademo string  `json:"prise_type_chademo"`
	PriseTypeT2      string  `json:"prise_type_2"`
	AccesRecharge    string  `json:"acces_recharge"`
	HorairesOuverture string `json:"horaires"`
}

func IngestIRVE(ctx context.Context, repo *repository.StationRepository) error {
	log.Println("[IRVE] Téléchargement du GeoJSON IRVE...")
	resp, err := http.Get(irveGeoJSONURL)
	if err != nil {
		return fmt.Errorf("IRVE download: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("IRVE read body: %w", err)
	}
	log.Printf("[IRVE] GeoJSON téléchargé: %.1f MB", float64(len(body))/1e6)

	var fc irveFeatureCollection
	if err := json.Unmarshal(body, &fc); err != nil {
		return fmt.Errorf("IRVE unmarshal: %w", err)
	}
	log.Printf("[IRVE] %d features à traiter", len(fc.Features))

	done, skipped := 0, 0
	for _, f := range fc.Features {
		var props irveProps
		if err := json.Unmarshal(f.Properties, &props); err != nil {
			skipped++
			continue
		}
		if props.IDPDC == "" {
			skipped++
			continue
		}
		if len(f.Geometry.Coordinates) < 2 {
			skipped++
			continue
		}

		lng := f.Geometry.Coordinates[0]
		lat := f.Geometry.Coordinates[1]

		// Détermine le type de connecteur principal
		connectorType := "unknown"
		switch {
		case strings.EqualFold(props.PriseTypeChademo, "true"):
			connectorType = "CHAdeMO"
		case strings.EqualFold(props.PriseTypeCombo, "true"):
			connectorType = "CCS"
		case strings.EqualFold(props.PriseTypeT2, "true"):
			connectorType = "T2"
		case strings.EqualFold(props.PriseType, "true"):
			connectorType = "EF"
		}

		is247 := strings.Contains(strings.ToLower(props.HorairesOuverture), "24/7") ||
			strings.Contains(strings.ToLower(props.HorairesOuverture), "24h/24")

		s := &domain.Station{
			IRVEIDStation:      props.IDStation,
			IRVEIDPDC:          props.IDPDC,
			OperatorName:       props.NomOperateur,
			Amenageur:          props.NomAmenageur,
			Enseigne:           props.NomEnseigneOp,
			Name:               props.NomStationItinerance,
			AddressStreet:      props.Adresse,
			AddressPostalCode:  props.CodePostal,
			AddressCity:        props.Commune,
			AddressCountryCode: "FR",
			Lat:                lat,
			Lng:                lng,
			PowerKw:            props.PuissanceNominale,
			ConnectorType:      connectorType,
			AccessType:         props.AccesRecharge,
			Is247:              is247,
			Metadata:           f.Properties,
		}

		if err := repo.Upsert(ctx, s); err != nil {
			log.Printf("[IRVE] Upsert error %s: %v", props.IDPDC, err)
			skipped++
			continue
		}
		done++
		if done%5000 == 0 {
			log.Printf("[IRVE] %d upserts effectués...", done)
		}
	}
	log.Printf("[IRVE] Terminé: %d upserts, %d ignorés", done, skipped)
	return nil
}
