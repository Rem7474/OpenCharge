package api

import (
	"reflect"
	"testing"
)

func TestParseSourcePlanPairs(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"bare source defaults to standard plan", "izivia", []string{"izivia:standard"}},
		{"multiple bare sources", "izivia,electra", []string{"izivia:standard", "electra:standard"}},
		{"explicit plan preserved", "electra:subscription", []string{"electra:subscription"}},
		{"mixed bare and explicit", "izivia,electra:subscription", []string{"izivia:standard", "electra:subscription"}},
		{"whitespace and empty entries", " izivia , ,electra:app ,", []string{"izivia:standard", "electra:app"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseSourcePlanPairs(c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("parseSourcePlanPairs(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}
