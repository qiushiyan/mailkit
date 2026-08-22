// Package cluster pulls together the messages that are about one real-world
// thing, across threads and senders.
//
// A provider's own grouping recovers less than you would hope: a run of
// notifications about one event scatters across several threads, and a
// confirmation from a different company never joins at all. What ties
// those together is a shared identifier -- an order number, a task id --
// printed in a body or buried in a URL. That is the strong signal and the
// only one that crosses senders. Domain and subject similarity are weak
// fallbacks, and every association comes back with the reason for it.
package cluster

import (
	"context"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/qiushiyan/mailkit/internal/mail"
	"github.com/qiushiyan/mailkit/internal/render"
)

// Order numbers, task ids, invoice references. Shorter than 6 digits is
// noise; longer than 14 is usually a tracking blob.
var identRe = regexp.MustCompile(`\b\d{6,14}\b`)

// A number introduced by a word like "order" is a key; a bare digit run in
// a footer is a PO box or an app-store link id. Preferring labelled numbers
// is what separates 1623209215 (the order) from 258595750 (onelink.me) and
// 530225 (a PO box) in the same message.
var keyedIdentRe = regexp.MustCompile(`(?i)(?:order|task|invoice|reference|ref|booking|confirmation|policy|claim|case|ticket|shipment|tracking)\W{0,12}(\d{6,14})`)

var subjectNoise = regexp.MustCompile(`(?i)^\s*((re|fwd|fw|aw)\s*:|updates?\s+to\s*:|heads\s+up!?|reminder\s*:|notification\s*:|automatic\s+reply\s*:)\s*`)
var wordRe = regexp.MustCompile(`[A-Za-z][A-Za-z0-9]{3,}`)

var stopwords = map[string]bool{
	"your": true, "you": true, "the": true, "and": true, "for": true, "with": true, "from": true,
	"this": true, "that": true, "has": true, "have": true, "been": true, "will": true, "are": true,
	"was": true, "our": true, "about": true, "info": true, "information": true, "please": true,
	"thanks": true, "thank": true, "email": true, "message": true, "update": true, "updates": true,
	"confirmed": true, "complete": true, "completed": true, "upcoming": true, "change": true, "changes": true,
}

const (
	// TooCommon: an identifier matching more than this many messages is a
	// date or a price, not a key. The mailbox answers the question; no
	// tuning needed.
	TooCommon       = 25
	MaxIdentifiers  = 5
	DefaultWindow   = 30
	DefaultLimit    = 40
	DefaultMinScore = 2

	weightIdentifier = 5
	weightThread     = 3
	weightDomain     = 2
	weightSubject    = 1
)

func plausible(m string) bool {
	if len(m) == 8 && (strings.HasPrefix(m, "19") || strings.HasPrefix(m, "20")) {
		return false // yyyymmdd
	}
	if len(m) == 6 && (strings.HasPrefix(m, "19") || strings.HasPrefix(m, "20")) {
		return false // yyyymm
	}
	return true
}

// Identifiers extracts digit runs that look like keys, labelled ones first.
// Bare runs are the fallback, used only when nothing is labelled -- a
// message that says "Order #1623209215" should not also drag in the PO box
// number from its own footer.
func Identifiers(text string) []string {
	for _, labelled := range []bool{true, false} {
		var out []string
		var matches []string
		if labelled {
			for _, m := range keyedIdentRe.FindAllStringSubmatch(text, -1) {
				matches = append(matches, m[1])
			}
		} else {
			matches = identRe.FindAllString(text, -1)
		}
		for _, m := range matches {
			if plausible(m) && !slices.Contains(out, m) {
				out = append(out, m)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return nil
}

// SubjectTokens are the up-to-four meaningful words of a subject.
func SubjectTokens(subject string) []string {
	core := subjectNoise.ReplaceAllString(subject, "")
	var out []string
	for _, w := range wordRe.FindAllString(strings.ToLower(core), -1) {
		if !stopwords[w] && !slices.Contains(out, w) {
			out = append(out, w)
		}
	}
	if len(out) > 4 {
		out = out[:4]
	}
	return out
}

// Options tune a run.
type Options struct {
	WindowDays int
	Limit      int
	MinScore   int
	// Raw keeps every hit: no minimum score, no too-common discard.
	Raw bool
}

// Hit is one related message and why it is here.
type Hit struct {
	ID             string    `json:"id"`
	ConversationID string    `json:"conversation_id"`
	Received       time.Time `json:"-"`
	Date           string    `json:"date"`
	From           string    `json:"from"`
	Subject        string    `json:"subject"`
	Score          int       `json:"score"`
	Why            []string  `json:"why"`
}

// Dropped records an identifier probe discarded as not distinctive.
type Dropped struct {
	Identifier string `json:"identifier"`
	Reason     string `json:"reason"`
	Hits       string `json:"hits"`
}

// Result is a cluster with its reasoning exposed.
type Result struct {
	Seed struct {
		ID, ConversationID, Date, From, Subject string
	} `json:"seed"`
	Signals struct {
		Identifiers   []string `json:"identifiers"`
		SubjectTokens []string `json:"subject_tokens"`
		Domain        string   `json:"domain"`
	} `json:"signals"`
	DroppedAsTooCommon []Dropped `json:"dropped_as_too_common"`
	BelowMinScore      int       `json:"below_min_score"`
	Related            []Hit     `json:"related"`
}

type probe struct {
	criteria mail.Criteria
	reason   string
	weight   int
	// want is how many hits to ask for. Identifier probes ask for more than
	// TooCommon regardless of the output limit, so the threshold can be
	// proven rather than truncated.
	want int
}

// Build clusters around the message seed.
func Build(ctx context.Context, box mail.Mailbox, seedID string, o Options) (Result, error) {
	if o.WindowDays <= 0 {
		o.WindowDays = DefaultWindow
	}
	if o.Limit <= 0 {
		o.Limit = DefaultLimit
	}
	seed, err := box.Fetch(ctx, seedID)
	if err != nil {
		return Result{}, err
	}
	var r Result
	r.Seed.ID, r.Seed.ConversationID, r.Seed.Date = seed.ID, seed.ConversationID, seed.DateHeader
	r.Seed.From, r.Seed.Subject = seed.From.String(), seed.Subject

	rendered := render.Convert(seed.Body)
	haystack := strings.Join([]string{seed.Subject, rendered.Text, strings.Join(rendered.RemoteImages, " ")}, " ")
	keys := Identifiers(haystack)
	if len(keys) > MaxIdentifiers {
		keys = keys[:MaxIdentifiers]
	}
	tokens := SubjectTokens(seed.Subject)
	domain := seed.From.Domain()
	r.Signals.Identifiers, r.Signals.SubjectTokens, r.Signals.Domain = keys, tokens, domain

	window := time.Duration(o.WindowDays) * 24 * time.Hour
	after, before := seed.Received.Add(-window), seed.Received.Add(window)
	var probes []probe
	for _, k := range keys {
		probes = append(probes, probe{mail.Criteria{Phrases: []string{k}}, "shares identifier " + k, weightIdentifier,
			max(o.Limit, TooCommon+1)})
	}
	if domain != "" {
		probes = append(probes, probe{mail.Criteria{From: domain, After: after, Before: before},
			"same sender domain (" + domain + ") within " + itoa(o.WindowDays) + " days", weightDomain, o.Limit})
	}
	if len(tokens) > 0 {
		// Bounded like the domain probe. Unbounded, "ikea OR assembly"
		// matched a food-hall newsletter from three years earlier.
		probes = append(probes, probe{mail.Criteria{SubjectTerms: tokens, After: after, Before: before},
			"subject overlap: " + strings.Join(tokens, ", "), weightSubject, o.Limit})
	}

	results := make([][]mail.Envelope, len(probes))
	g, gctx := errgroup.WithContext(ctx)
	for i, p := range probes {
		g.Go(func() error {
			hits, err := box.Search(gctx, p.criteria, p.want)
			if err != nil {
				return err
			}
			results[i] = hits
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return Result{}, err
	}

	scored := map[string]*Hit{}
	var order []string
	for i, p := range probes {
		hits := results[i]
		if p.weight == weightIdentifier && len(hits) > TooCommon && !o.Raw {
			r.DroppedAsTooCommon = append(r.DroppedAsTooCommon, Dropped{
				Identifier: p.criteria.Phrases[0], Reason: p.reason, Hits: "more than " + itoa(TooCommon)})
			continue
		}
		for _, h := range hits {
			if h.ID == seedID {
				continue
			}
			row, ok := scored[h.ID]
			if !ok {
				row = &Hit{ID: h.ID, ConversationID: h.ConversationID, Received: h.Received,
					Date: h.DateHeader, From: h.From.String(), Subject: h.Subject}
				scored[h.ID] = row
				order = append(order, h.ID)
			}
			row.Score += p.weight
			row.Why = append(row.Why, p.reason)
		}
	}
	for _, id := range order {
		row := scored[id]
		if row.ConversationID != "" && row.ConversationID == seed.ConversationID {
			row.Score += weightThread
			row.Why = append(row.Why, "same conversation")
		}
	}
	minScore := o.MinScore
	if o.Raw {
		minScore = 0
	}
	var kept []Hit
	for _, id := range order {
		if scored[id].Score >= minScore {
			kept = append(kept, *scored[id])
		}
	}
	r.BelowMinScore = len(order) - len(kept)
	slices.SortStableFunc(kept, func(a, b Hit) int { return a.Received.Compare(b.Received) })
	if len(kept) > o.Limit {
		kept = kept[:o.Limit]
	}
	r.Related = kept
	if r.Related == nil {
		r.Related = []Hit{}
	}
	if r.DroppedAsTooCommon == nil {
		r.DroppedAsTooCommon = []Dropped{}
	}
	return r, nil
}

func itoa(n int) string { return strconv.Itoa(n) }
