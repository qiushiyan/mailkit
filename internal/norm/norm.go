// Package norm is the one definition of "the same text" used to compare
// what a message says with what a search asked or an earlier turn said:
// case-folded letters and digits, every other run collapsed to one space.
// "Order #1623209215" and "order 1623209215" are the same text.
package norm

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Fold returns the normalised form of s and, for every byte of it, the
// offset in s just past the rune that produced it -- so a match found in
// the normalised form can be mapped back to a cut in the original.
func Fold(s string) (text string, ends []int) {
	var b strings.Builder
	b.Grow(len(s))
	ends = make([]int, 0, len(s))
	pendingSpace := false
	for pos, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			pendingSpace = b.Len() > 0
			continue
		}
		end := pos + utf8.RuneLen(r)
		if pendingSpace {
			b.WriteByte(' ')
			ends = append(ends, pos)
			pendingSpace = false
		}
		lr := unicode.ToLower(r)
		n, _ := b.WriteRune(lr)
		for range n {
			ends = append(ends, end)
		}
	}
	return b.String(), ends
}

// Text is Fold without the offsets.
func Text(s string) string {
	t, _ := Fold(s)
	return t
}
