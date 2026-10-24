package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/qiushiyan/mailkit/internal/mail"
)

// Rule: results say what to do next, at the moment the decision is made.
// These pin the text the agent acts on.

func TestSurface_UnknownOutcomeErrorCarriesTheSearch(t *testing.T) {
	h := newHarness(t)
	id := draftIDOf(t, mustOK(t, h.send("compose", "--to", "a@example.com", "--subject", "Lease renewal", "--body", "hello there", "--no-open")))
	h.box.SendErr = errors.New("connection reset")
	mustFail(t, h.send("commit", id))
	h.box.SendErr = nil
	r := mustFail(t, h.send("commit", id))
	if !strings.Contains(r.stderr, `mail-find search 'subject:"Lease renewal"`) {
		t.Errorf("the recovery must be a runnable command: %s", r.stderr)
	}
}

func TestSurface_NotSignedInForDraftingNamesTheLogin(t *testing.T) {
	h := newHarness(t)
	h.box.AuthErr = mail.ErrAuth
	r := mustFail(t, h.send("compose", "--to", "a@example.com", "--subject", "x", "--body", "hello there", "--no-open"))
	if !strings.Contains(r.stderr, "mail-find auth login --account gmail") {
		t.Errorf("drafting without credentials must prescribe the login: %s", r.stderr)
	}
}

func TestSurface_BareSendMailListsItsCommands(t *testing.T) {
	r := mustFail(t, newHarness(t).send())
	for _, want := range []string{"compose", "reply", "commit", "list"} {
		if !strings.Contains(r.stderr, want) {
			t.Errorf("bare send-mail must name %s: %s", want, r.stderr)
		}
	}
}
