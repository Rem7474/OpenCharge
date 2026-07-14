package domain

import (
	"time"

	"github.com/google/uuid"
)

// LinkQuality décrit la fiabilité du lien IRVE ↔ source externe.
type LinkQuality string

const (
	LinkQualityExact           LinkQuality = "exact"
	LinkQualityByGeolocation   LinkQuality = "by_geolocation"
	LinkQualityByOperatorName  LinkQuality = "by_operator_name"
	LinkQualityManual          LinkQuality = "manual"
)

// StationLink représente la corrélation entre une station IRVE et une source externe.
type StationLink struct {
	ID              uuid.UUID   `json:"id"`
	StationID       uuid.UUID   `json:"station_id"`
	SourceStationID uuid.UUID   `json:"source_station_id"`
	Source          string      `json:"source"`
	LinkQuality     LinkQuality `json:"link_quality"`
	CreatedAt       time.Time   `json:"created_at"`
}
