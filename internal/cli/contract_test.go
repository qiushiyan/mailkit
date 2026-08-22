package cli_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/qiushiyan/mailkit/internal/fixtures"
	"github.com/qiushiyan/mailkit/internal/mail"
)

// One happy-path case per subcommand, plus the two friction rules: flags
// work on either side of the subcommand, and errors prescribe the command.

func TestContract_Search(t *testing.T) {
	a := msg("a", "ca", day(1), "alice@shop.example", "Your order 123456789", para("Order 123456789 confirmed and will ship within two working days."))
	b := msg("b", "cb", day(5), "bob@other.example", "Lunch", para("Shall we get lunch on Thursday at the usual place near the office?"))
	h := newHarness(t, a, b)
	hits := mustOK(t, h.find("search", `from:shop.example "order 123456789"`)).jsonList(t, "hits")
	if len(hits) != 1 || hits[0]["id"] != "a" {
		t.Fatalf("want only a, got %v", hits)
	}
	if r := mustFail(t, h.find("search", "label:inbox")); !strings.Contains(r.stderr, "--native") {
		t.Errorf("unknown operator must name the grammar and the escape hatch: %s", r.stderr)
	}
	// Flags after the subcommand.
	r := mustOK(t, h.find("search", "lunch", "--text"))
	if !strings.Contains(r.stdout, "Lunch") || strings.HasPrefix(r.stdout, "{") {
		t.Errorf("--text after the subcommand should give the human form:\n%s", r.stdout)
	}
}

func TestContract_Resolve(t *testing.T) {
	m := msg("m1", "c1", day(1), "a@example.com", "hello", para("A message with a known Message-ID for resolving by reference."))
	m.MessageID = "abc.123@mail.example"
	h := newHarness(t, m)
	for _, ref := range []string{"<abc.123@mail.example>", "abc.123@mail.example", "message://%3Cabc.123@mail.example%3E"} {
		if got := mustOK(t, h.find("resolve", ref)).json(t)["id"]; got != "m1" {
			t.Errorf("resolve %q = %v", ref, got)
		}
	}
	if r := mustFail(t, h.find("resolve", "nope@nowhere")); !strings.Contains(r.stderr, "Message-ID") {
		t.Errorf("not-found should explain what a Message-ID is: %s", r.stderr)
	}
}

func TestContract_ReadNamesTheThreadWhenPartOfOne(t *testing.T) {
	thread := fixtures.Thread(t, fixtures.TenancyThread)
	h := newHarness(t, thread...)
	last := thread[len(thread)-1]
	steps := mustOK(t, h.find("read", last.ID)).json(t)["next_steps"].([]any)
	want := "mail-find thread " + last.ID
	found := false
	for _, s := range steps {
		found = found || strings.Contains(s.(string), want)
	}
	if !found {
		t.Errorf("a reply in a 6-message thread should point at %q: %v", want, steps)
	}
	lone := msg("lone", "cl", day(1), "a@example.com", "Solo", para("A single message with no conversation around it at all."))
	if steps := mustOK(t, newHarness(t, lone).find("read", "lone")).json(t)["next_steps"].([]any); len(steps) != 0 {
		t.Errorf("a lone message gets no thread pointer: %v", steps)
	}
}

func TestContract_EmbeddedMessageIsSurfacedNotAbsent(t *testing.T) {
	inner := msg("inner", "ci", day(0), "boss@corp.example", "Budget approval", para("The budget for the next quarter is approved as submitted, please proceed."))
	outer := msg("outer", "co", day(1), "a@example.com", "FW: see attached", para("Forwarding the approval as an attachment rather than inline."))
	outer.Parts = []mail.Part{{Name: "Budget approval", MIME: "message/rfc822", Content: mail.EmbeddedPart{Item: &inner}}}
	outer.HasAttachments = true
	h := newHarness(t, outer)
	r := mustOK(t, h.find("attachments", "outer"))
	rows := r.jsonList(t, "attachments")
	if len(rows) != 1 || rows[0]["kind"] != "embedded_message" {
		t.Fatalf("embedded message must be listed with its kind: %v", rows)
	}
	read := mustOK(t, h.find("read", "outer"))
	if !strings.Contains(read.stdout, "Budget approval") {
		t.Errorf("read should tell the caller about the embedded message")
	}
	if r := mustFail(t, h.find("fetch", "outer", "--all")); !strings.Contains(r.stderr, "none hold bytes") {
		t.Errorf("fetch must explain why there is nothing to download: %s", r.stderr)
	}
}

func TestContract_CloudLinkIsSurfacedWithItsURL(t *testing.T) {
	m := msg("m1", "c1", day(1), "a@example.com", "shared", para("Sharing a file from the company drive with you for review."))
	m.Parts = []mail.Part{{Name: "deck.pptx", Content: mail.LinkedPart{URL: "https://drive.example/deck"}}}
	rows := mustOK(t, newHarness(t, m).find("attachments", "m1")).jsonList(t, "attachments")
	if rows[0]["kind"] != "cloud_link" || rows[0]["url"] != "https://drive.example/deck" {
		t.Errorf("cloud link must carry its URL: %v", rows)
	}
}

func TestContract_NotAuthenticatedNamesTheLoginCommand(t *testing.T) {
	h := newHarness(t)
	h.box.AuthErr = &mail.ProviderError{Provider: "gmail", Op: "account", Err: mail.ErrAuth, Hint: "mail-find auth login"}
	r := mustFail(t, h.find("search", "anything"))
	if !strings.Contains(r.stderr, "mail-find auth login") {
		t.Errorf("error must prescribe the command: %s", r.stderr)
	}
	if !errors.Is(h.box.AuthErr, mail.ErrAuth) {
		t.Fatal("test setup")
	}
}

func TestContract_RawAcceptedAfterSubcommandEverywhere(t *testing.T) {
	m := msg("m1", "c1", day(1), "a@example.com", "x", para("A body long enough to be prose for the raw flag acceptance test."))
	h := newHarness(t, m)
	for _, cmd := range []string{"read", "thread", "context"} {
		if r := h.find(cmd, "m1", "--raw"); r.code != 0 {
			t.Errorf("%s --raw after the subcommand failed: %s", cmd, r.stderr)
		}
		if r := h.find("--text", cmd, "m1", "--raw"); r.code != 0 {
			t.Errorf("--text before and --raw after failed for %s: %s", cmd, r.stderr)
		}
	}
}

func TestContract_AuthStatusAndLogin(t *testing.T) {
	h := newHarness(t)
	if r := mustOK(t, h.find("auth", "status")); !strings.Contains(r.stdout, "me@example.com") {
		t.Errorf("status should show the address: %s", r.stdout)
	}
	if r := mustOK(t, h.find("auth", "login", "--account", "outlook")); !strings.Contains(r.stdout, "outlook") {
		t.Errorf("login should run for the named account: %s", r.stdout)
	}
}
