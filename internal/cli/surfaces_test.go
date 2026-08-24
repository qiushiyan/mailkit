package cli_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/qiushiyan/mailkit/internal/fixtures"
	"github.com/qiushiyan/mailkit/internal/mail"
)

// Rule: results say what to do next, at the moment the decision is made,
// and say only what this layer can vouch for. These pin the text the
// agent acts on.

func TestSurface_EmptySearchSaysHowToWiden(t *testing.T) {
	h := newHarness(t)
	steps := mustOK(t, h.find("search", "from:nobody.example")).json(t)["next_steps"].([]any)
	if len(steps) != 1 || !strings.Contains(steps[0].(string), "--native") || !strings.Contains(steps[0].(string), "domain") {
		t.Fatalf("an empty search must say how to widen and name the escape hatch: %v", steps)
	}
	hit := msg("a", "c", day(1), "a@example.com", "x", para("A message that matches so there is nothing to widen here."))
	if steps := mustOK(t, newHarness(t, hit).find("search", "matches")).json(t)["next_steps"].([]any); len(steps) != 0 {
		t.Errorf("a search with hits carries no widening advice: %v", steps)
	}
}

func TestSurface_RemoteImageNudgeCarriesItsCost(t *testing.T) {
	h := newHarness(t, fixtures.Message(t, fixtures.RemoteOnly))
	steps := mustOK(t, h.find("read", fixtures.RemoteOnly)).json(t)["next_steps"].([]any)
	found := false
	for _, s := range steps {
		found = found || (strings.Contains(s.(string), "--fetch-remote") && strings.Contains(s.(string), "tells the sender"))
	}
	if !found {
		t.Fatalf("the fetch-remote nudge must state that fetching is visible to the sender: %v", steps)
	}
}

func TestSurface_EmbeddedNudgeNamesWhereTheTextIsForThisOutput(t *testing.T) {
	inner := msg("inner", "ci", day(0), "boss@corp.example", "Budget", para("Approved as submitted, please proceed with the plan."))
	outer := msg("outer", "co", day(1), "a@example.com", "FW", para("Forwarding the approval as an attachment."))
	outer.Parts = []mail.Part{{Name: "Budget", MIME: "message/rfc822", Content: mail.EmbeddedPart{Item: &inner}}}
	h := newHarness(t, outer)
	j := mustOK(t, h.find("read", "outer")).json(t)["next_steps"].([]any)[0].(string)
	if !strings.Contains(j, "attachments[].embedded") || strings.Contains(j, "--text") {
		t.Errorf("JSON nudge points at the JSON field only: %s", j)
	}
	if tx := mustOK(t, h.find("read", "outer", "--text")).stdout; !strings.Contains(tx, "printed below") || strings.Contains(tx, "attachments[]") {
		t.Errorf("text nudge points at the printed block only:\n%s", tx)
	}
}

func TestSurface_FoldMarkerIsNotOutput(t *testing.T) {
	thread := fixtures.Thread(t, fixtures.TenancyThread)
	h := newHarness(t, thread...)
	for _, r := range []result{h.find("read", thread[len(thread)-1].ID), h.find("thread", thread[0].ID)} {
		if strings.Contains(r.stdout, "quote_marker") || strings.Contains(r.stdout, "caret") {
			t.Errorf("which regex matched is internal: %s", r.stdout[:200])
		}
	}
}

func TestSurface_ContextJSONCarriesTheHeldBackCommand(t *testing.T) {
	seed := msg("seed", "c1", day(5), "shop@store.example", "Order 1623209215 confirmed", para("Your order 1623209215 will ship soon."))
	weak := msg("weak", "c2", day(6), "news@store.example", "Newsletter", para("Unrelated newsletter from the same domain, a weak signal only."))
	h := newHarness(t, seed, weak)
	out := mustOK(t, h.find("context", "seed", "--min-score", "3")).json(t)
	steps := out["next_steps"].([]any)
	if len(steps) != 1 || !strings.Contains(steps[0].(string), "--min-score 1") {
		t.Fatalf("held-back hits must come with the command that shows them: %v", steps)
	}
	if seed := out["seed"].(map[string]any); seed["id"] != "seed" || seed["conversation_id"] != "c1" {
		t.Errorf("seed fields are named like every other envelope: %v", seed)
	}
}

func TestSurface_UnknownOutcomeErrorCarriesTheSearch(t *testing.T) {
	h := newHarness(t)
	id := draftIDOf(t, mustOK(t, h.send("--to", "a@example.com", "--subject", "Lease renewal", "--body", "hello there", "--no-open")))
	h.box.SendErr = errors.New("connection reset")
	mustFail(t, h.send("--commit", id))
	h.box.SendErr = nil
	r := mustFail(t, h.send("--commit", id))
	if !strings.Contains(r.stderr, `mail-find search 'subject:"Lease renewal"`) {
		t.Errorf("the recovery must be a runnable command: %s", r.stderr)
	}
}

func TestSurface_NotSignedInForDraftingNamesTheLogin(t *testing.T) {
	h := newHarness(t)
	h.box.AuthErr = mail.ErrAuth
	r := mustFail(t, h.send("--to", "a@example.com", "--subject", "x", "--body", "hello there", "--no-open"))
	if !strings.Contains(r.stderr, "mail-find auth login --account gmail") {
		t.Errorf("drafting without credentials must prescribe the login: %s", r.stderr)
	}
}

func TestSurface_BareSendMailListsItsThreeUses(t *testing.T) {
	r := mustFail(t, newHarness(t).send())
	for _, want := range []string{"--commit", "--list", "--to"} {
		if !strings.Contains(r.stderr, want) {
			t.Errorf("bare send-mail must name %s: %s", want, r.stderr)
		}
	}
}

func TestSurface_ErrorsNameTheSubcommand(t *testing.T) {
	r := mustFail(t, newHarness(t).find("fetch", "nope"))
	if !strings.HasPrefix(r.stderr, "mail-find fetch:") {
		t.Errorf("the failing subcommand is the prefix: %s", r.stderr)
	}
}
