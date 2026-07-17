package domain

import "testing"

func TestTariffKindForConnector(t *testing.T) {
	cases := map[string]string{
		ConnectorTypeCCS:     TariffKindDC,
		ConnectorTypeCHAdeMO: TariffKindDC,
		ConnectorTypeT2:      TariffKindAC,
		ConnectorTypeEF:      TariffKindAC,
		ConnectorTypeOther:   "",
		ConnectorTypeUnknown: "",
		"":                   "",
		"garbage":            "",
	}
	for connectorType, want := range cases {
		if got := TariffKindForConnector(connectorType); got != want {
			t.Errorf("TariffKindForConnector(%q) = %q, want %q", connectorType, got, want)
		}
	}
}
