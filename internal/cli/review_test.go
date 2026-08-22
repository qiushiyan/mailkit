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
