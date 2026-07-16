package api

import (
	"reflect"
	"testing"
)

func TestParseSources(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"single", "izivia", []string{"izivia"}},
		{"multiple", "izivia,electra", []string{"izivia", "electra"}},
		{"whitespace and empty entries", " izivia , ,electra ,", []string{"izivia", "electra"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseSources(c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("parseSources(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}
