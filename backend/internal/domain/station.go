package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Station représente un point de charge IRVE (référentiel canonique).
type Station struct {
	ID                  uuid.UUID       `json:"id"`
	IRVEIDStation       string          `json:"irve_id_station"`
	IRVEIDPDC           string          `json:"irve_id_pdc"`
	OperatorName        string          `json:"operator_name"`
	Amenageur           string          `json:"amenageur"`
	Enseigne            string          `json:"enseigne"`
	Name                string          `json:"name"`
	AddressStreet       string          `json:"address_street"`
	AddressPostalCode   string          `json:"address_postal_code"`
	AddressCity         string          `json:"address_city"`
	AddressCountryCode  string          `json:"address_country_code"`
	Lat                 float64         `json:"lat"`
	Lng                 float64         `json:"lng"`
	PowerKw             float64         `json:"power_kw"`
	ConnectorType       string          `json:"connector_type"`
	AccessType          string          `json:"access_type"`
	Is247               bool            `json:"is_24_7"`
	Metadata            json.RawMessage `json:"metadata,omitempty"`
	CreatedAt           time.Time       `json:"created_at"`
	UpdatedAt           time.Time       `json:"updated_at"`
}

// SourceStation représente une station issue d'une source externe (Izivia, Electra).
type SourceStation struct {
	ID                uuid.UUID       `json:"id"`
	Source            string          `json:"source"`
	SourceStationID   string          `json:"source_station_id"`
	Name              string          `json:"name"`
	OperatorName      string          `json:"operator_name"`
	AddressStreet     string          `json:"address_street"`
	AddressPostalCode string          `json:"address_postal_code"`
	AddressCity       string          `json:"address_city"`
	AddressCountryCode string         `json:"address_country_code"`
	Lat               float64         `json:"lat"`
	Lng               float64         `json:"lng"`
	Raw               json.RawMessage `json:"raw,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
}

// Connector est utilisé dans les réponses API (calculé depuis metadata ou connector_type).
type Connector struct {
	Kind       string  `json:"kind"` // ac | dc
	MaxPowerKw float64 `json:"maxPowerKw"`
	Count      int     `json:"count"`
}
