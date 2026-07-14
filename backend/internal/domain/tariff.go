package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// StationTariff représente un tarif normalisé attaché à une station IRVE.
type StationTariff struct {
	ID                           uuid.UUID       `json:"id"`
	StationID                    uuid.UUID       `json:"station_id"`
	Source                       string          `json:"source"`
	Kind                         string          `json:"kind"` // ac | dc | mixed
	Model                        string          `json:"model"` // fixed | time_based | izivia_text | electra_fixed
	Currency                     string          `json:"currency"`
	EnergyPriceCentsPerKwh       *float64        `json:"energy_price_cents_per_kwh,omitempty"`
	SessionPriceCentsPerMin      *float64        `json:"session_price_cents_per_min,omitempty"`
	CongestionPriceCentsPerMin   *float64        `json:"congestion_price_cents_per_min,omitempty"`
	ServiceFeePercent            *float64        `json:"service_fee_percent,omitempty"`
	ValidFrom                    *time.Time      `json:"valid_from,omitempty"`
	ValidTo                      *time.Time      `json:"valid_to,omitempty"`
	RawText                      *string         `json:"raw_text,omitempty"`
	Extra                        json.RawMessage `json:"extra,omitempty"`
	CreatedAt                    time.Time       `json:"created_at"`
	UpdatedAt                    time.Time       `json:"updated_at"`
}
