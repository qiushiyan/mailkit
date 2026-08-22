package mail

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Criteria is a conjunction of predicates over a mailbox. Zero fields are
// unconstrained. It is the one shape a search takes: the user-facing grammar
// parses into it, the clustering probes are built as it, and every adapter
// translates it -- completing locally what its provider cannot express.
type Criteria struct {
	// Phrases must each appear verbatim somewhere in the message.
	Phrases []string
	// SubjectTerms: any one of them in the subject.
	SubjectTerms []string
	// From and To are an exact address or a bare domain.
	From string
	To   string
	// HasAttachment requires at least one non-inline stored part.
	HasAttachment bool
	// After and Before bound Received; zero means unbounded.
	After  time.Time
	Before time.Time
}

// IsZero reports whether no predicate is set.
func (c Criteria) IsZero() bool {
	return len(c.Phrases) == 0 && len(c.SubjectTerms) == 0 && c.From == "" &&
		c.To == "" && !c.HasAttachment && c.After.IsZero() && c.Before.IsZero()
}

// Match is the definition of "this message matches". text is the message's
// body as text (any converter); phrases are checked against subject and
// text after normalisation. The memory adapter is exactly this function
// over its contents, and an adapter narrowing a coarse provider result uses
// it too, so the predicate has one home.
func (c Criteria) Match(e Envelope, text string) bool {
	if c.From != "" && !e.From.matchesSelector(c.From) {
		return false
	}
	if c.To != "" {
		hit := false
		for _, a := range e.To {
			if a.matchesSelector(c.To) {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	if c.HasAttachment && !e.HasAttachments {
		return false
	}
	if !c.After.IsZero() && e.Received.Before(c.After) {
		return false
	}
	if !c.Before.IsZero() && !e.Received.Before(c.Before) {
		return false
	}
	if len(c.SubjectTerms) > 0 {
		subj := normalise(e.Subject)
		hit := false
		for _, t := range c.SubjectTerms {
			if strings.Contains(subj, normalise(t)) {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	if len(c.Phrases) > 0 {
		doc := normalise(e.Subject + " " + text)
		for _, p := range c.Phrases {
			if !strings.Contains(doc, normalise(p)) {
				return false
			}
		}
	}
	return true
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// normalise lowercases and collapses punctuation so "Order #1623209215" and
// "order 1623209215" compare equal, and so a phrase match survives the
// provider's own tokenisation.
func normalise(s string) string {
	return " " + strings.TrimSpace(nonAlnum.ReplaceAllString(strings.ToLower(s), " ")) + " "
}

// Grammar is the portable subset of search syntax the CLI accepts. It is
// the form a model already writes for Gmail, and every operator in it means
// the same thing on every provider. Anything outside it is an error that
// names the grammar, rather than a string silently handed to one provider.
const Grammar = `from:ADDR|DOMAIN  to:ADDR|DOMAIN  subject:WORD|"PHRASE"  has:attachment
after:YYYY/MM/DD  before:YYYY/MM/DD  newer_than:Nd  older_than:Nd  "exact phrase"  word`

// ParseQuery turns a query string in the Grammar into Criteria. now anchors
// the relative date operators.
func ParseQuery(q string, now time.Time) (Criteria, error) {
	var c Criteria
	for _, tok := range tokenize(q) {
		op, val, hasOp := strings.Cut(tok, ":")
		if !hasOp || strings.HasPrefix(tok, `"`) {
			c.Phrases = append(c.Phrases, strings.Trim(tok, `"`))
			continue
		}
		val = strings.Trim(val, `"`)
		if val == "" {
			return Criteria{}, fmt.Errorf("%q needs a value; grammar:\n%s", tok, Grammar)
		}
		op = strings.ToLower(op)
		switch op {
		case "from":
			c.From = val
		case "to":
			c.To = val
		case "subject":
			c.SubjectTerms = append(c.SubjectTerms, val)
		case "has":
			if strings.ToLower(val) != "attachment" {
				return Criteria{}, fmt.Errorf("has:%s is not in the grammar; only has:attachment", val)
			}
			c.HasAttachment = true
		case "after", "before":
			t, err := parseDay(val)
			if err != nil {
				return Criteria{}, fmt.Errorf("%s: %w", tok, err)
			}
			if op == "after" {
				c.After = t
			} else {
				c.Before = t
			}
		case "newer_than", "older_than":
			d, err := parseRelative(val)
			if err != nil {
				return Criteria{}, fmt.Errorf("%s: %w", tok, err)
			}
			if op == "newer_than" {
				c.After = now.Add(-d)
			} else {
				c.Before = now.Add(-d)
			}
		default:
			return Criteria{}, fmt.Errorf("%q is not in the portable grammar:\n%s\nUse --native to pass provider syntax through untouched", tok, Grammar)
		}
	}
	return c, nil
}

// tokenize splits on whitespace, keeping quoted runs together, including
// after an operator: subject:"two words".
func tokenize(q string) []string {
	var out []string
	var cur strings.Builder
	inQuote := false
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range q {
		switch {
		case r == '"':
			inQuote = !inQuote
			cur.WriteRune(r)
		case !inQuote && (r == ' ' || r == '\t' || r == '\n'):
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}

func parseDay(s string) (time.Time, error) {
	for _, layout := range []string{"2006/01/02", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("%q is not a date (YYYY/MM/DD)", s)
}

func parseRelative(s string) (time.Duration, error) {
	if len(s) < 2 {
		return 0, fmt.Errorf("%q is not a span (Nd, Nm, Ny)", s)
	}
	n, err := strconv.Atoi(s[:len(s)-1])
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%q is not a span (Nd, Nm, Ny)", s)
	}
	day := 24 * time.Hour
	switch s[len(s)-1] {
	case 'd':
		return time.Duration(n) * day, nil
	case 'm':
		return time.Duration(n) * 30 * day, nil
	case 'y':
		return time.Duration(n) * 365 * day, nil
	}
	return 0, fmt.Errorf("%q is not a span (Nd, Nm, Ny)", s)
}
