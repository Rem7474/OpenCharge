package domain

import (
	"time"

	"github.com/google/uuid"
)

const (
	TariffKindAC    = "ac"
	TariffKindDC    = "dc"
	TariffKindMixed = "mixed"
)

// StationTariff is a normalized tariff attached to an IRVE station,
// coming from a given source (electra, izivia, irve_text, ...).
type StationTariff struct {
	ID                         uuid.UUID
	StationID                  uuid.UUID
	Source                     string
	Kind                       string
	Model                      string
	Currency                   string
	EnergyPriceCentsPerKWh     *float64
	SessionPriceCentsPerMin    *float64
	CongestionPriceCentsPerMin *float64
	ServiceFeePercent          *float64
	ValidFrom                  *time.Time
	ValidTo                    *time.Time
	RawText                    string
	Extra                      map[string]any
	CreatedAt                  time.Time
	UpdatedAt                  time.Time
}
