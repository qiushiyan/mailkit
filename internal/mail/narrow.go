package mail

import (
	"errors"
	"fmt"
)

// ErrTooBroad: a search walked maxScan coarse rows without filling its
// limit. The caller should narrow the query rather than read a short list
// as the whole answer.
var ErrTooBroad = errors.New("search too broad")

// maxScan bounds a narrowing walk.
const maxScan = 1000

// DefaultLimit is the search limit when the caller names none.
const DefaultLimit = 25

// Narrow is the exact-search walk every adapter shares: it pulls pages of
// coarse provider hits from next, keeps those residual accepts, and stops
// at limit exact matches or when next reports no more. Walking maxScan rows
// without filling limit is ErrTooBroad, never a short answer. next returns
// one page and whether another follows.
func Narrow(limit int, residual Criteria, next func() (page []Envelope, more bool, err error)) ([]Envelope, error) {
	if limit <= 0 {
		limit = DefaultLimit
	}
	var out []Envelope
	scanned := 0
	for {
		page, more, err := next()
		if err != nil {
			return nil, err
		}
		for _, e := range page {
			scanned++
			if !residual.MatchEnvelope(e) {
				continue
			}
			out = append(out, e)
			if len(out) >= limit {
				return out, nil
			}
		}
		if !more {
			return out, nil
		}
		if scanned >= maxScan {
			return nil, fmt.Errorf("%w: scanned %d messages and found %d matches; narrow the query (tighter dates, from:, subject:)", ErrTooBroad, scanned, len(out))
		}
	}
}
