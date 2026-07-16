package ingestion

import "testing"

func TestParsePriceText(t *testing.T) {
	cases := []struct {
		name      string
		text      string
		wantPrice *float64
		wantFee   *float64
	}{
		{"price and fee", "0,45€/kWh (Dont 15% de frais de service)", ptr(0.45), ptr(15.0)},
		{"price only", "0.30€/kWh", ptr(0.30), nil},
		{"empty", "", nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			price, fee := parsePriceText(c.text)
			if !floatPtrEqual(price, c.wantPrice) {
				t.Errorf("price = %v, want %v", deref(price), deref(c.wantPrice))
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
