package render

import (
	"strings"
	"testing"
)

// The fold rules, tested at their owner. The CLI suites prove the wiring:
// read loads the conversation, thread folds turn by turn.

func TestSplit_FindsTheEarliestBoundaryAndKeepsForwardsWhole(t *testing.T) {
	for name, c := range map[string]struct {
		body, spoken, marker string
	}{
		"attribution":      {"Thanks!\n\nOn Mon, 3 Aug 2026, Bob wrote:\n> Yes.", "Thanks!", "attribution"},
		"caret":            {"Ok.\n> the earlier line", "Ok.", "caret"},
		"outlook header":   {"Reply here.\n\nFrom: Bob\nSent: Monday\nTo: me", "Reply here.", "outlook-header"},
		"original message": {"Reply.\n-----Original Message-----\nFrom: Bob\nSent: Monday", "Reply.", "original-message"},
		"forward":          {"FYI\n\n---------- Forwarded message ---------\nFrom: x\n> quoted", "FYI\n\n---------- Forwarded message ---------\nFrom: x\n> quoted", "forward"},
		"apple forward":    {"See below.\n> Begin forwarded message:\n> From: x", "See below.\n> Begin forwarded message:\n> From: x", "forward"},
		"no boundary":      {"Just a message.", "Just a message.", ""},
	} {
		spoken, quoted, marker := Split(c.body)
		if spoken != c.spoken || marker != c.marker {
			t.Errorf("%s: Split = %q, %q; want %q, %q", name, spoken, marker, c.spoken, c.marker)
		}
		if marker == "forward" && quoted != "" {
			t.Errorf("%s: a forward is the only copy of what it carries; nothing may be quoted off it", name)
		}
	}
}

func TestCoverage_ChecksShortLinesAndWavesThroughOnlyStructure(t *testing.T) {
	prose := "Could you confirm the meter reading was taken on the first of the month?"
	for name, c := range map[string]struct {
		quoted, pool string
		ratio        float64
		lines        int
	}{
		"prose upstream":         {"> " + prose, "earlier: " + prose, 1, 1},
		"prose nowhere":          {"> " + prose, "something else entirely", 0, 1},
		"same text, other shape": {"> ORDER 1623209215 shipped today, to your address in London!", "Order #1623209215 shipped today to your address in london", 1, 1},
		"short lines, no prose":  {"> Yes, 3pm works.\n> Room 4B.", "", 0, 2},
		"structure only":         {"On Mon, Bob wrote:\nFrom: Bob\nSent: Monday", "", 1, 0},
	} {
		ratio, n := Coverage(c.quoted, c.pool)
		if ratio != c.ratio || n != c.lines {
			t.Errorf("%s: Coverage = %v of %d lines, want %v of %d", name, ratio, n, c.ratio, c.lines)
		}
	}
}

func TestFoldOne_FoldsOnlyWhatThePoolRepeats(t *testing.T) {
	said := "Could you confirm the meter reading was taken on the first of the month as agreed?"
	other := "This second quoted paragraph exists nowhere in the conversation before this reply."
	body := "Yes, confirmed.\n\nOn Mon, Alice wrote:\n> " + said

	got, f := FoldOne(body, "earlier: "+said, "")
	if got != "Yes, confirmed." || f.QuotedChars == 0 || f.FoldRejected != "" || f.QuoteMarker != "attribution" {
		t.Errorf("an upstream quote must fold: said %q, fold %+v", got, f)
	}

	got, f = FoldOne(body+"\n> "+other, "earlier: "+said, "")
	if !strings.Contains(got, other) || f.QuotedChars != 0 {
		t.Fatalf("a quote half of which is nowhere upstream must be kept whole: %q", got)
	}
	if f.FoldRejected != "kept: 50% of 2 quoted lines are not in earlier turns" {
		t.Errorf("the reason must give the share that is missing upstream: %q", f.FoldRejected)
	}

	_, f = FoldOne(body, "", "")
	if f.FoldRejected != "kept: 100% of 1 quoted lines are not in earlier turns" {
		t.Errorf("a quote with nothing upstream is 100%% missing: %q", f.FoldRejected)
	}
}

func TestFoldOne_ProviderBoundaryIsVerifiedLikeOurs(t *testing.T) {
	history := "The earlier turn said this at length, so the fold may remove it from the reply."
	body := "Sure, see you at 3pm.\n\n" + history

	got, f := FoldOne(body, "earlier: "+history, "Sure, see you at 3pm!!")
	if got != "Sure, see you at 3pm." || f.QuoteMarker != "provider" {
		t.Errorf("a verified provider boundary is used, cut in the body's own bytes: %q, %+v", got, f)
	}
	got, f = FoldOne(body, "", "Sure, see you at 3pm!!")
	if !strings.Contains(got, history) || f.FoldRejected == "" {
		t.Errorf("an unverified provider boundary is a claim, not evidence: %q, %+v", got, f)
	}
}

func TestPrefixEnd_MapsTheNormalisedPrefixBackToTheBody(t *testing.T) {
	body := "Sure, see you at 3pm.\n\nmore"
	if end, ok := prefixEnd(body, "SURE see you at 3pm!!!"); !ok || body[:end] != "Sure, see you at 3pm." {
		t.Errorf("prefixEnd = %d, %v; want the cut after the closing punctuation", end, ok)
	}
	for _, p := range []string{"", "!!", "see you at 3pm"} {
		if _, ok := prefixEnd(body, p); ok {
			t.Errorf("%q is not a prefix of the body", p)
		}
	}
}

func TestTidy_UndoesConversionArtefactsWithoutRewording(t *testing.T) {
	for in, want := range map[string]string{
		"write to a@b.com<mailto:a@b.com>":       "write to a@b.com",
		"see example.com<https://example.com/>":  "see example.com",
		"click here<https://example.com/plan>":   "click here (https://example.com/plan)",
		"[External]\nHello":                      "Hello",
		"Hi\n__________\nBye":                    "Hi\n\nBye",
		"x&lt;https://x&gt;":                     "x<https://x>",
		"a b\n\n\n\nc":                           "a b\n\nc",
		"You don't often get email from x.\nHey": "Hey",
	} {
		if got := Tidy(in); got != want {
			t.Errorf("Tidy(%q) = %q, want %q", in, got, want)
		}
	}
}
