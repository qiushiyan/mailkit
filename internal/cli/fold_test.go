package cli_test

import (
	"strings"
	"testing"

	"github.com/qiushiyan/mailkit/internal/fixtures"
	"github.com/qiushiyan/mailkit/internal/mail"
	"github.com/qiushiyan/mailkit/internal/norm"
	"github.com/qiushiyan/mailkit/internal/render"
)

// Rule: fold only what is verified to exist upstream.
//
// The failure it names: an Apple Mail forward was folded whole -- 6390
// characters, the only copy -- because "Begin forwarded message:" sat under
// the same "> " a reply uses and marker detection alone decided.

// proseLinesOf mirrors the coverage unit: prose-length, non-structural
// lines, so the invariant is checked with the same notion of "line".
func proseLinesOf(text string) []string {
	var out []string
	for l := range strings.SplitSeq(text, "\n") {
		l = strings.TrimSpace(strings.TrimLeft(l, "> "))
		if len(l) >= 40 {
			out = append(out, l)
		}
	}
	return out
}

// structuralLine mirrors the rule's own exclusion: attribution, header
// blocks and platform banners are format, and their absence upstream never
// means content was lost.
func structuralLine(line string) bool {
	l := strings.ToLower(line)
	for _, p := range []string{"on ", "from:", "to:", "sent:", "cc:", "subject:", "date:", "you don't often get email"} {
		if strings.HasPrefix(l, p) {
			return true
		}
	}
	return false
}

func normal(s string) string { return norm.Text(s) }

func TestThread_FoldsOnlyWhatEarlierTurnsAlreadySaid(t *testing.T) {
	thread := fixtures.Thread(t, fixtures.TenancyThread)
	h := newHarness(t, thread...)
	r := mustOK(t, h.find("thread", thread[len(thread)-1].ID))
	var tr render.Transcript
	if err := unmarshal(r.stdout, &tr); err != nil {
		t.Fatal(err)
	}
	if len(tr.Turns) != len(thread) {
		t.Fatalf("turns = %d, want %d", len(tr.Turns), len(thread))
	}
	// The invariant: every prose line the transcript dropped from a turn
	// exists in an earlier turn's raw text. Zero exceptions.
	var earlier strings.Builder
	folded := 0
	for i, turn := range tr.Turns {
		raw := render.Text(thread[i].Body)
		if turn.QuotedChars > 0 {
			folded++
			pool := normal(earlier.String())
			for _, line := range proseLinesOf(raw) {
				if strings.Contains(normal(turn.Said), normal(line)) {
					continue // kept, not folded
				}
				if structuralLine(line) {
					continue
				}
				if !strings.Contains(pool, normal(line)) {
					t.Errorf("turn %d folded a line that no earlier turn contains:\n  %q", i+1, line)
				}
			}
		}
		earlier.WriteString("\n" + raw)
	}
	if folded < 4 {
		t.Errorf("only %d turns folded; the fixture has five replies", folded)
	}
	if tr.TranscriptChars >= tr.RawChars/2 {
		t.Errorf("transcript %d chars from %d raw: folding did not happen", tr.TranscriptChars, tr.RawChars)
	}
	markers := map[string]bool{}
	for _, turn := range tr.Turns {
		if turn.QuoteMarker != "" {
			markers[turn.QuoteMarker] = true
		}
	}
	for _, want := range []string{"caret", "outlook-header"} {
		if !markers[want] {
			t.Errorf("marker %q never used; the fixture mixes both conventions", want)
		}
	}
}

func TestRead_ForwardSurvivesWhole(t *testing.T) {
	fwd := fixtures.Message(t, fixtures.AppleForward)
	h := newHarness(t, fwd)
	r := mustOK(t, h.find("read", fwd.ID))
	body := r.json(t)["body"].(string)
	raw := render.Text(fwd.Body)
	if len(body) < len(raw)*9/10 {
		t.Fatalf("forward was folded: %d of %d chars survived", len(body), len(raw))
	}
	// Deleting the forward short-circuit AND the coverage check in
	// render.Split/FoldOne must fail this: the raw text carries a "> "
	// caret marker that would otherwise fold everything after it.
	if !strings.Contains(raw, "\n>") {
		t.Fatalf("fixture no longer carries the caret-quoted forward this test exists for")
	}
}

func TestRead_UnrecognisedForwardWithReplySubjectIsKept(t *testing.T) {
	// A Re: subject, a caret-quoted block whose prose exists nowhere else,
	// and no forward marker. Only the coverage check can save it.
	unique := "The landlord agreed to replace the boiler before the fifteenth and will confirm the contractor by Friday morning."
	m := msg("m1", "c1", day(1), "a@example.com", "Re: boiler",
		"<p>See below.</p><blockquote><p>"+unique+"</p><p>Second unique paragraph that also only exists inside this quoted block of text.</p></blockquote>")
	h := newHarness(t, m)
	r := mustOK(t, h.find("read", "m1"))
	out := r.json(t)
	if !strings.Contains(out["body"].(string), unique) {
		t.Fatalf("the only copy of the quoted text was folded away:\n%s", out["body"])
	}
	if out["fold_rejected"] == nil || out["fold_rejected"] == "" {
		t.Errorf("a kept quote must say why it was kept (fold_rejected)")
	}
}

func TestRead_FoldsAReplyWhoseQuoteIsUpstream(t *testing.T) {
	said := "Could you confirm the meter reading was taken on the first of the month as agreed with the agent?"
	earlier := msg("m1", "c1", day(0), "a@example.com", "Meter", para(said))
	reply := msg("m2", "c1", day(1), "b@example.com", "Re: Meter",
		"<p>Yes, confirmed.</p><blockquote><p>"+said+"</p></blockquote>")
	h := newHarness(t, earlier, reply)
	r := mustOK(t, h.find("read", "m2"))
	out := r.json(t)
	body := out["body"].(string)
	if strings.Contains(body, said) {
		t.Fatalf("quote that exists upstream was not folded:\n%s", body)
	}
	if out["quoted_chars"].(float64) <= 0 {
		t.Errorf("quoted_chars should report what was folded")
	}
	raw := mustOK(t, h.find("read", "m2", "--raw")).json(t)
	if !strings.Contains(raw["body"].(string), said) {
		t.Errorf("--raw must return the quote")
	}
}

func TestThread_ProviderFoldedIsVerifiedNotTrusted(t *testing.T) {
	// Graph's uniqueBody claims the tail is history, but the tail exists in
	// no earlier turn. Trusting it would lose the only copy.
	tail := "This paragraph is what the provider says is quoted history, but nothing earlier in the thread contains it at all."
	first := msg("m1", "c1", day(0), "a@example.com", "Hello", para("A short opener that is long enough to count as prose here."))
	second := msg("m2", "c1", day(1), "b@example.com", "Re: Hello", para("My reply is here and it is long enough to be a prose line.", tail))
	second.ProviderFolded = &mail.Body{HTML: para("My reply is here and it is long enough to be a prose line.")}
	h := newHarness(t, first, second)
	var tr render.Transcript
	if err := unmarshal(mustOK(t, h.find("thread", "m2")).stdout, &tr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tr.Turns[1].Said, tail) {
		t.Fatalf("uniqueBody was trusted without upstream evidence:\n%s", tr.Turns[1].Said)
	}

	// And when the tail IS upstream, the provider hint is accepted.
	first2 := msg("m1", "c1", day(0), "a@example.com", "Hello", para(tail))
	h2 := newHarness(t, first2, second)
	if err := unmarshal(mustOK(t, h2.find("thread", "m2")).stdout, &tr); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(tr.Turns[1].Said, tail) || tr.Turns[1].QuoteMarker != "provider" {
		t.Errorf("verified provider fold should be used: marker=%q said=%q", tr.Turns[1].QuoteMarker, tr.Turns[1].Said)
	}
	if !strings.HasSuffix(tr.Turns[1].Said, "prose line.") {
		t.Errorf("the boundary must keep the spoken text's own punctuation: %q", tr.Turns[1].Said)
	}
}

func TestThread_RawContainsEveryBodyAsSent(t *testing.T) {
	thread := fixtures.Thread(t, fixtures.TenancyThread)
	h := newHarness(t, thread...)
	raw := mustOK(t, h.find("thread", thread[0].ID, "--raw"))
	rows := raw.jsonList(t, "messages")
	if len(rows) != len(thread) {
		t.Fatalf("%d messages, want %d", len(rows), len(thread))
	}
	for i, m := range thread {
		if rows[i]["body"].(string) != render.Text(m.Body) {
			t.Errorf("message %d body altered under --raw", i)
		}
	}
}
