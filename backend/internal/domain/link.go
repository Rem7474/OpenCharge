package domain

import (
	"time"

	"github.com/google/uuid"
)

// SourceStation is a raw station record from an external source (Izivia, Electra, …).
type SourceStation struct {
	ID                 uuid.UUID  `json:"id"`
	Source             string     `json:"source"`
	SourceStationID    string     `json:"source_station_id"`
	Name               *string    `json:"name,omitempty"`
	OperatorName       *string    `json:"operator_name,omitempty"`
	AddressStreet      *string    `json:"address_street,omitempty"`
	AddressPostalCode  *string    `json:"address_postal_code,omitempty"`
	AddressCity        *string    `json:"address_city,omitempty"`
	AddressCountryCode string     `json:"address_country_code"`
	Lat                *float64   `json:"lat,omitempty"`
	Lng                *float64   `json:"lng,omitempty"`
	Raw                []byte     `json:"raw,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

// StationLink correlates an IRVE station to an external source station.
type StationLink struct {
	ID               uuid.UUID `json:"id"`
	StationID        uuid.UUID `json:"station_id"`
	SourceStationID  uuid.UUID `json:"source_station_id"`
	Source           string    `json:"source"`
	LinkQuality      string    `json:"link_quality"` // exact | by_geolocation | by_operator_name | manual
	CreatedAt        time.Time `json:"created_at"`
}
