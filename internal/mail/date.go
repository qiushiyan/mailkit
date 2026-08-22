package mail

import (
	netmail "net/mail"
	"strings"
	"time"
)

// ParseDate parses an RFC 5322 Date header. The result is always zoned: a
// sender that omits the offset is read as UTC rather than producing a naive
// value that cannot be ordered against the others.
func ParseDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, " ("); i > 0 { // trailing "(UTC)" comments
		s = s[:i]
	}
	t, err := netmail.ParseDate(s)
	if err != nil {
		for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05Z", "Mon, 2 Jan 2006 15:04:05", "2 Jan 2006 15:04:05"} {
			if t2, err2 := time.Parse(layout, s); err2 == nil {
				return t2.UTC(), nil
			}
		}
		return time.Time{}, err
	}
	return t, nil
}
