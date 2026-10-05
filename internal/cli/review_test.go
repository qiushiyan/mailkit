package cli_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/qiushiyan/mailkit/internal/drafts"
	"github.com/qiushiyan/mailkit/internal/mail"
)

// --- fold: what "verified upstream" must mean ----------------------------

func TestRead_ShortQuotedLinesAreNotFoldedWithoutUpstream(t *testing.T) {
	// A quote of only short lines has no prose line to check, which must
	// not read as "nothing to prove". With nothing upstream the quote stays.
	reply := msg("m1", "c1", day(1), "b@example.com", "Re: Time", "")
	reply.Body = mail.Body{Text: "Thanks!\n\nOn Mon, Bob wrote:\n> Yes, 3pm works.\n> Room 4B, second floor.\n"}
	h := newHarness(t, reply)
	body := mustOK(t, h.find("read", "m1")).json(t)["body"].(string)
	if !strings.Contains(body, "3pm works") || !strings.Contains(body, "Room 4B") {
		t.Fatalf("short quoted lines with no upstream copy were folded:\n%s", body)
	}
}

func TestRead_ProviderBoundaryIsTakenInTheBodysOwnCoordinates(t *testing.T) {
	// The provider's quote-stripped text matches ours after normalisation
	// but is longer in bytes. Slicing the body by the provider's length
	// would skip past the only copy of a line and never check it.
	unique := "This sentence exists nowhere else in the conversation and must survive the fold."
	history := "The earlier turn said this, at length, so the fold may remove it from the reply."
	first := msg("m1", "c1", day(0), "a@example.com", "Plan", para(history))
	reply := msg("m2", "c1", day(1), "b@example.com", "Re: Plan", "")
	reply.Body = mail.Body{Text: "Sure, see you at 3pm.\n\n" + unique + "\n" + history + "\n"}
	reply.ProviderFolded = &mail.Body{Text: "Sure, see you at 3pm." + strings.Repeat("!", 100)}
	h := newHarness(t, first, reply)
	body := mustOK(t, h.find("read", "m2")).json(t)["body"].(string)
	if !strings.Contains(body, unique) {
		t.Fatalf("provider boundary skipped an unchecked line:\n%s", body)
	}
}

func TestRead_UpstreamMeansEarlierInConversationOrderNotSameTimestamp(t *testing.T) {
	// read and thread must agree on what counts as upstream. A later message
	// that shares the target's timestamp is not upstream of it.
	line := "Our quoted line appears only in a message that comes after this one in the thread."
	first := msg("m1", "c1", day(0), "a@example.com", "Q", para("An opener long enough to be prose and nothing more than that here."))
	target := msg("m2", "c1", day(1), "b@example.com", "Re: Q", "")
	target.Body = mail.Body{Text: "Ok.\n\nOn Tue, Carol wrote:\n> " + line + "\n"}
	later := msg("m3", "c1", day(1), "c@example.com", "Re: Q", para(line))
	h := newHarness(t, first, target, later)
	body := mustOK(t, h.find("read", "m2")).json(t)["body"].(string)
	if !strings.Contains(body, line) {
		t.Fatalf("read folded against a message that is not upstream:\n%s", body)
	}
}

// --- embedded messages: the body must reach the caller ---------------------

func TestRead_EmbeddedMessageBodyIsInTheOutput(t *testing.T) {
	innerText := "The budget for the next quarter is approved as submitted, please proceed."
	inner := msg("inner", "ci", day(0), "boss@corp.example", "Budget approval", para(innerText))
	outer := msg("outer", "co", day(1), "a@example.com", "FW: see attached", para("Forwarding the approval as an attachment rather than inline."))
	outer.Parts = []mail.Part{{Name: "Budget approval", MIME: "message/rfc822", Content: mail.EmbeddedPart{Item: &inner}}}
	outer.HasAttachments = true
	h := newHarness(t, outer)
	rows := mustOK(t, h.find("read", "outer")).jsonList(t, "attachments")
	emb, _ := rows[0]["embedded"].(map[string]any)
	if emb == nil || !strings.Contains(emb["body"].(string), innerText) || emb["from"] != "boss@corp.example" {
		t.Fatalf("embedded message body must be in attachments[0].embedded: %v", rows[0])
	}
	if text := mustOK(t, h.find("read", "outer", "--text")).stdout; !strings.Contains(text, innerText) {
		t.Fatalf("--text must carry the embedded body too:\n%s", text)
	}
}

// --- send gate: the states a crash leaves behind ----------------------------

func TestSend_DraftClaimedByADeadProcessIsReportedUnknown(t *testing.T) {
	h := newHarness(t)
	id := draftIDOf(t, mustOK(t, h.send("--to", "a@example.com", "--subject", "crash", "--body", "hello there", "--no-open")))
	// A process that claimed the draft and died before recording an outcome.
	if _, err := h.deps.Drafts.Claim(id); err != nil {
		t.Fatal(err)
	}
	r := mustFail(t, h.send("--commit", id))
	if !strings.Contains(r.stderr, "unknown") || !strings.Contains(r.stderr, "crash") {
		t.Fatalf("a draft left in sending must be reported as unknown, with the subject to search for: %s", r.stderr)
	}
	if len(h.box.Sent) != 0 {
		t.Errorf("a draft of unknown outcome was sent again")
	}
}

func TestSend_ProviderFailureAfterTransmissionIsUnknownNotPending(t *testing.T) {
	h := newHarness(t)
	id := draftIDOf(t, mustOK(t, h.send("--to", "a@example.com", "--subject", "flaky", "--body", "hello there", "--no-open")))
	h.box.SendErr = errors.New("connection reset by peer")
	mustFail(t, h.send("--commit", id))
	rec, _ := h.deps.Drafts.Load(id)
	if rec.State != drafts.Unknown {
		t.Fatalf("a failure once bytes may have left must be unknown, got %s", rec.State)
	}
	h.box.SendErr = nil
	r := mustFail(t, h.send("--commit", id))
	if !strings.Contains(r.stderr, "unknown") || !strings.Contains(r.stderr, "flaky") {
		t.Errorf("second commit must refuse and name the subject to check for: %s", r.stderr)
	}
}

func TestSend_PreSendRefusalReturnsToPending(t *testing.T) {
	h := newHarness(t)
	id := draftIDOf(t, mustOK(t, h.send("--to", "a@example.com", "--subject", "big", "--body", "hello there", "--no-open")))
	h.box.SendLimit = 1
	mustFail(t, h.send("--commit", id))
	if rec, _ := h.deps.Drafts.Load(id); rec.State != drafts.Pending {
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
	id := draftIDOf(t, mustOK(t, h.send("--reply", "liam", "--body", "The refund works for me, thanks.", "--no-open")))
	mustOK(t, h.send("--commit", id))

	hdr, _, _ := parts(t, h.box.Sent[0])
	if got := hdr.Get("In-Reply-To"); got != "<CALag-refund@mail.example>" {
		t.Errorf("In-Reply-To = %q, want the original's Message-ID", got)
	}
	if got := strings.Fields(hdr.Get("References")); len(got) != 2 || got[0] != "<first-ask@mail.example>" || got[1] != "<CALag-refund@mail.example>" {
		t.Errorf("References = %q, want the original's chain then the original", got)
	}
	turns := mustOK(t, h.find("thread", "liam")).jsonList(t, "turns")
	if len(turns) != 2 || !strings.Contains(turns[1]["said"].(string), "refund works for me") {
		t.Fatalf("the reply is not a turn of the original's thread: %v", turns)
	}
	reply := mustOK(t, h.find("read", turns[1]["id"].(string))).json(t)
	if reply["conversation_id"] != "ticket" || reply["subject"] != "Re: Refund request" {
		t.Errorf("reply has conversation %v and subject %v", reply["conversation_id"], reply["subject"])
	}
}

// Finding 2026-10-05: drafts were written with "Message-ID: <<id@mailkit>>"
// -- an id already in brackets, bracketed again -- which is not a msg-id
// at all. Gmail rewrote it on send and hid the defect.
func TestSend_MessageIDIsOneMsgID(t *testing.T) {
	h := newHarness(t)
	id := draftIDOf(t, mustOK(t, h.send("--to", "a@example.com", "--subject", "id", "--body", "hello there", "--no-open")))
	mustOK(t, h.send("--commit", id))
	hdr, _, _ := parts(t, h.box.Sent[0])
	got := hdr.Get("Message-ID")
	if want := "<" + id + "@mailkit>"; got != want {
		t.Errorf("Message-ID = %q, want %q", got, want)
	}
}

// --- resolve: message:// URLs are percent-encoded, in either case ---------

func TestContract_ResolveDecodesAnyPercentEncoding(t *testing.T) {
	m := msg("m1", "c1", day(1), "a@example.com", "hello", para("A message with a known Message-ID for resolving by reference."))
	m.MessageID = "abc+123@mail.example"
	h := newHarness(t, m)
	for _, ref := range []string{"message://%3cabc+123@mail.example%3e", "message://%3Cabc%2B123%40mail.example%3E"} {
		if got := mustOK(t, h.find("resolve", ref)).json(t)["id"]; got != "m1" {
			t.Errorf("resolve %q = %v", ref, got)
		}
	}
}
