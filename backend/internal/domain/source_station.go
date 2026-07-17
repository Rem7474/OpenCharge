package domain

import (
	"time"

	"github.com/google/uuid"
)

// SourceStation is a station as seen by an external source (Izivia,
// Electra, ...), before correlation with the IRVE referential.
type SourceStation struct {
	ID              uuid.UUID
	Source          string
	SourceStationID string
	Name            string
	OperatorName    string
	AddressStreet   string
	AddressPostal   string
	AddressCity     string
	AddressCountry  string
	Lat             float64
	Lng             float64
	Raw             map[string]any
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

const (
	LinkQualityExact          = "exact"
	LinkQualityByGeolocation  = "by_geolocation"
	LinkQualityByOperatorName = "by_operator+name"
	LinkQualityManual         = "manual"
)

// StationLink correlates an IRVE station with a source station.
type StationLink struct {
	ID              uuid.UUID
	StationID       uuid.UUID
	SourceStationID uuid.UUID
	Source          string
	LinkQuality     string
	DistanceMeters  *float64
	CreatedAt       time.Time
}
