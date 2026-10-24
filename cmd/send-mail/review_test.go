package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/qiushiyan/mailkit/internal/drafts"
	"github.com/qiushiyan/mailkit/internal/mail"
	"github.com/qiushiyan/mailkit/internal/render"
)

// --- send gate: the states a crash leaves behind ----------------------------

func TestSend_DraftClaimedByADeadProcessIsReportedUnknown(t *testing.T) {
	h := newHarness(t)
	id := draftIDOf(t, mustOK(t, h.send("compose", "--to", "a@example.com", "--subject", "crash", "--body", "hello there", "--no-open")))
	// A process that claimed the draft and died before recording an outcome.
	if _, err := h.drafts().Claim(id); err != nil {
		t.Fatal(err)
	}
	r := mustFail(t, h.send("commit", id))
	if !strings.Contains(r.stderr, "unknown") || !strings.Contains(r.stderr, "crash") {
		t.Fatalf("a draft left in sending must be reported as unknown, with the subject to search for: %s", r.stderr)
	}
	if len(h.box.Sent) != 0 {
		t.Errorf("a draft of unknown outcome was sent again")
	}
}

func TestSend_ProviderFailureAfterTransmissionIsUnknownNotPending(t *testing.T) {
	h := newHarness(t)
	id := draftIDOf(t, mustOK(t, h.send("compose", "--to", "a@example.com", "--subject", "flaky", "--body", "hello there", "--no-open")))
	h.box.SendErr = errors.New("connection reset by peer")
	mustFail(t, h.send("commit", id))
	rec, _ := h.drafts().Load(id)
	if rec.State != drafts.Unknown {
		t.Fatalf("a failure once bytes may have left must be unknown, got %s", rec.State)
	}
	h.box.SendErr = nil
	r := mustFail(t, h.send("commit", id))
	if !strings.Contains(r.stderr, "unknown") || !strings.Contains(r.stderr, "flaky") {
		t.Errorf("second commit must refuse and name the subject to check for: %s", r.stderr)
	}
}

func TestSend_PreSendRefusalReturnsToPending(t *testing.T) {
	h := newHarness(t)
	id := draftIDOf(t, mustOK(t, h.send("compose", "--to", "a@example.com", "--subject", "big", "--body", "hello there", "--no-open")))
	h.box.SendLimit = 1
	mustFail(t, h.send("commit", id))
	if rec, _ := h.drafts().Load(id); rec.State != drafts.Pending {
		t.Errorf("a refusal before transmission should return to pending, got %s", rec.State)
	}
}

// --- replies: an answer must join the conversation it answers -------------

// Finding 2026-10-05: with no reply flow, the answer to a support mail went
// out as a new message under a "Re:" subject. Apple Mail grouped it by
// subject, so it looked threaded; Gmail gave it a thread of its own and its
// draft carried no In-Reply-To or References, so the helpdesk could not
// attach it to the ticket either.
func TestSend_ReplyJoinsTheConversationItAnswers(t *testing.T) {
	orig := msg("liam", "ticket", day(1), "Liam <liam@help.example>", "Refund request", para("We can refund the unused part of the quarter, or upgrade you to annual."))
	orig.MessageID = "CALag-refund@mail.example"
	orig.References = []mail.MessageID{"first-ask@mail.example"}
	h := newHarness(t, orig)
	id := draftIDOf(t, mustOK(t, h.send("reply", "liam", "--body", "The refund works for me, thanks.", "--no-open")))
	mustOK(t, h.send("commit", id))

	hdr, _, _ := parts(t, h.box.Sent[0])
	if got := hdr.Get("In-Reply-To"); got != "<CALag-refund@mail.example>" {
		t.Errorf("In-Reply-To = %q, want the original's Message-ID", got)
	}
	if got := strings.Fields(hdr.Get("References")); len(got) != 2 || got[0] != "<first-ask@mail.example>" || got[1] != "<CALag-refund@mail.example>" {
		t.Errorf("References = %q, want the original's chain then the original", got)
	}
	// What mail-find thread renders: the original's conversation, with the
	// reply as a turn of it.
	conv, err := h.box.Conversation(t.Context(), "ticket")
	if err != nil {
		t.Fatal(err)
	}
	turns := render.Build(conv).Turns
	if len(turns) != 2 || !strings.Contains(turns[1].Said, "refund works for me") {
		t.Fatalf("the reply is not a turn of the original's thread: %+v", turns)
	}
	if reply := conv[1]; reply.ConversationID != "ticket" || reply.Subject != "Re: Refund request" {
		t.Errorf("reply has conversation %q and subject %q", reply.ConversationID, reply.Subject)
	}
}

// Finding 2026-10-05: drafts were written with "Message-ID: <<id@mailkit>>"
// -- an id already in brackets, bracketed again -- which is not a msg-id
// at all. Gmail rewrote it on send and hid the defect.
func TestSend_MessageIDIsOneMsgID(t *testing.T) {
	h := newHarness(t)
	id := draftIDOf(t, mustOK(t, h.send("compose", "--to", "a@example.com", "--subject", "id", "--body", "hello there", "--no-open")))
	mustOK(t, h.send("commit", id))
	hdr, _, _ := parts(t, h.box.Sent[0])
	got := hdr.Get("Message-ID")
	if want := "<" + id + "@mailkit>"; got != want {
		t.Errorf("Message-ID = %q, want %q", got, want)
	}
}
