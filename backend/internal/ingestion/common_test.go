package ingestion

import "testing"

func TestParsePriceText(t *testing.T) {
	cases := []struct {
		name        string
		text        string
		wantPrice   *float64
		wantSession *float64
		wantFee     *float64
	}{
		{"price and fee", "0,45€/kWh (Dont 15% de frais de service)", ptr(45.0), nil, ptr(15.0)},
		{"price only, dot decimal", "0.30€/kWh", ptr(30.0), nil, nil},
		{"price with TTC and spacing", "0,50 € TTC / kWh", ptr(50.0), nil, nil},
		{"price with 'du kWh' wording", "Prix : 0,45€ du kWh", ptr(45.0), nil, nil},
		{"per-minute price", "0,05€/min", nil, ptr(5.0), nil},
		{"per-minute price, 'la minute' wording", "0,08 € la minute", nil, ptr(8.0), nil},
		{"fee before price wording", "frais de service : 10%", nil, nil, ptr(10.0)},
		{"empty", "", nil, nil, nil},
		{
			"skips a leading zero price, takes the first non-zero one",
			"0.00 €/kWh\nFrais de service : 15%\n0,391€/kWh Une fois la charge terminée : 15 min à 0,0€/min puis 0,23€/min (Dont 15% de frais de service)",
			ptr(39.1), ptr(23.0), ptr(15.0),
		},
		{"only a zero price present", "0.00 €/kWh", nil, nil, nil},
		{"only a zero per-minute price present", "0,0€/min", nil, nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			price, session, fee := parsePriceText(c.text)
			if !floatPtrEqual(price, c.wantPrice) {
				t.Errorf("price = %v, want %v", deref(price), deref(c.wantPrice))
			}
			if !floatPtrEqual(session, c.wantSession) {
				t.Errorf("session = %v, want %v", deref(session), deref(c.wantSession))
			}
			if !floatPtrEqual(fee, c.wantFee) {
				t.Errorf("fee = %v, want %v", deref(fee), deref(c.wantFee))
			}
		})
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if got := firstNonEmpty("", "  ", "b", "c"); got != "b" {
		t.Errorf("firstNonEmpty = %q, want %q", got, "b")
	}
	if got := firstNonEmpty("", ""); got != "" {
		t.Errorf("firstNonEmpty = %q, want empty", got)
	}
}

func TestParseBooleanLoose(t *testing.T) {
	truthy := []string{"true", "1", "yes", "oui", "vrai", "  Oui  "}
	for _, v := range truthy {
		if !parseBooleanLoose(v) {
			t.Errorf("parseBooleanLoose(%q) = false, want true", v)
		}
	}
	falsy := []string{"false", "0", "non", "", "n/a"}
	for _, v := range falsy {
		if parseBooleanLoose(v) {
			t.Errorf("parseBooleanLoose(%q) = true, want false", v)
		}
	}
}

func ptr(v float64) *float64 { return &v }

func deref(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}

func floatPtrEqual(a, b *float64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
