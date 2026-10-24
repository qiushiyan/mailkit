package norm

import (
	"slices"
	"testing"
)

// Owner of "the same text": the normalised form, and the offsets that map a
// match in it back to a cut in the original bytes.
func TestFold(t *testing.T) {
	for in, want := range map[string]struct {
		text string
		ends []int
	}{
		"Order #16": {"order 16", []int{1, 2, 3, 4, 5, 7, 8, 9}},
		"Ä b":       {"ä b", []int{2, 2, 3, 4}},
		"--x--":     {"x", []int{3}},
		"":          {"", []int{}},
	} {
		text, ends := Fold(in)
		if text != want.text || !slices.Equal(ends, want.ends) {
			t.Errorf("Fold(%q) = %q %v, want %q %v", in, text, ends, want.text, want.ends)
		}
		if len(ends) != len(text) {
			t.Errorf("Fold(%q): %d offsets for %d bytes", in, len(ends), len(text))
		}
	}
	if Text("Order #1623209215") != Text("order 1623209215") {
		t.Error("punctuation and case must not make two texts differ")
	}
}
