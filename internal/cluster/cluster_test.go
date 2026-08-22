package cluster

import (
	"slices"
	"testing"
)

// Owner of identifier extraction: labelled numbers win; bare runs are the
// fallback; dates are never keys.
func TestIdentifiers(t *testing.T) {
	cases := []struct {
		name, text string
		want       []string
	}{
		{"labelled beats bare runs in the same message",
			"IKEA Order #1623209215 ... PO Box 530225 ... onelink.me/258595750?utm", []string{"1623209215"}},
		{"bare runs when nothing is labelled", "ref 123 ... 530225 ... 258595750", []string{"530225", "258595750"}},
		{"yyyymmdd excluded", "Invoice 20260817 and ticket 4455667", []string{"4455667"}},
		{"too short and too long ignored", "12345 and 123456789012345", nil},
		{"dedup", "order 777001 order 777001", []string{"777001"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Identifiers(tc.text); !slices.Equal(got, tc.want) {
				t.Errorf("Identifiers = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSubjectTokens(t *testing.T) {
	got := SubjectTokens("Re: Updates to: IKEA Furniture Assembly")
	if !slices.Equal(got, []string{"ikea", "furniture", "assembly"}) {
		t.Errorf("tokens = %v", got)
	}
}
