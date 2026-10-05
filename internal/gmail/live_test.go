//go:build live

package gmail_test

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/qiushiyan/mailkit/internal/drafts"
	"github.com/qiushiyan/mailkit/internal/fixtures"
	"github.com/qiushiyan/mailkit/internal/gmail"
	"github.com/qiushiyan/mailkit/internal/mail"
	"github.com/qiushiyan/mailkit/internal/render"
)

// Live, against the real mailbox:  go test -tags live ./internal/gmail -run Live
// With -record, the raw API responses for the fixture ids are re-captured
// into testdata/gmail so the cassettes track the real writer.
var record = flag.Bool("record", false, "re-record testdata/gmail from the live API")

func liveBox(t *testing.T) *gmail.Mailbox {
	t.Helper()
	ctx := context.Background()
	client, err := gmail.Client(ctx)
	if err != nil {
		t.Skipf("no live credentials: %v", err)
	}
	box, err := gmail.New(ctx, client)
	if err != nil {
		t.Fatal(err)
	}
	return box
}

func TestLive_AccountAndFixtureMessages(t *testing.T) {
	box := liveBox(t)
	ctx := t.Context()
	acct, err := box.Account(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("connected as %s", acct.Address)

	for _, id := range []string{fixtures.AppleForward, fixtures.RemoteOnly, fixtures.InlineCID, fixtures.IKEASeed} {
		m, err := box.Fetch(ctx, id)
		if err != nil {
			t.Fatalf("fetch %s: %v", id, err)
		}
		if m.Body.HTML == "" {
			t.Errorf("%s: no HTML body", id)
		}
		t.Logf("%s  %q  parts=%d", id, m.Subject, len(m.Parts))
	}
	thread, err := box.Conversation(ctx, fixtures.TenancyThread)
	if err != nil {
		t.Fatal(err)
	}
	if len(thread) != 6 {
		t.Errorf("tenancy thread has %d messages live, fixture has 6", len(thread))
	}
	tr := render.Build(thread)
	t.Logf("transcript %d from %d raw (%d folded)", tr.TranscriptChars, tr.RawChars, tr.FoldedChars)
	for _, turn := range tr.Turns {
		if turn.FoldRejected != "" {
			t.Logf("turn %s: %s", turn.ID, turn.FoldRejected)
		}
	}
}

func TestLive_SearchResolveOpen(t *testing.T) {
	box := liveBox(t)
	ctx := t.Context()
	hits, err := box.Search(ctx, mail.Criteria{From: "taskrabbit.co.uk", After: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("no taskrabbit mail since August 1 -- the fixture mailbox changed")
	}
	for _, h := range hits {
		if !strings.HasSuffix(strings.ToLower(h.From.Email), "taskrabbit.co.uk") {
			t.Errorf("search returned a non-matching sender: %s", h.From)
		}
	}
	e, err := box.Resolve(ctx, hits[0].MessageID)
	if err != nil || e.ID != hits[0].ID {
		t.Errorf("resolve round trip: %v %v", e.ID, err)
	}
	m, err := box.Fetch(ctx, fixtures.InlineCID)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range m.Parts {
		if h, ok := p.Handle(); ok {
			var buf bytes.Buffer
			if err := box.Open(ctx, h, &buf); err != nil {
				t.Fatal(err)
			}
			if !bytes.HasPrefix(buf.Bytes(), []byte("\x89PNG")) {
				t.Errorf("%s: not a PNG (%d bytes)", p.Name, buf.Len())
			}
			break
		}
	}
}

// TestLive_ReplyThreads sends a real reply, so it runs only when told which
// message to answer -- one in a thread you own, since the reply goes to its
// sender:
//
//	MAILKIT_LIVE_REPLY_TO=<id> go test -tags live ./internal/gmail -run LiveReply -v
//
// The reply is composed the way send-mail composes one and must land in
// the original's thread, as Gmail reports it and as the thread reads back.
func TestLive_ReplyThreads(t *testing.T) {
	id := os.Getenv("MAILKIT_LIVE_REPLY_TO")
	if id == "" {
		t.Skip("set MAILKIT_LIVE_REPLY_TO to a message id in a thread you own; this sends a real reply")
	}
	box := liveBox(t)
	ctx := t.Context()
	acct, err := box.Account(ctx)
	if err != nil {
		t.Fatal(err)
	}
	orig, err := box.Fetch(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	r, err := mail.NewReply(orig.Envelope, []string{acct.Address}, false)
	if err != nil {
		t.Fatal(err)
	}
	rec, p, err := drafts.Store{Dir: t.TempDir()}.Create(drafts.Compose{
		Account: "gmail", From: acct.Address, Reply: &r,
		Body: drafts.PlainBody("mailkit live check: this reply should sit in the original's thread (" + time.Now().Format(time.RFC3339) + ")."),
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("replying to %s %q from %s, to %v", orig.ID, orig.Subject, orig.From, rec.To)
	sent, err := box.Send(ctx, p, rec.Parent())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("sent %s into thread %s", sent.ID, sent.ConversationID)
	if sent.ConversationID != orig.ConversationID {
		t.Errorf("Gmail put the reply in thread %s, not the original's %s", sent.ConversationID, orig.ConversationID)
	}
	thread, err := box.Conversation(ctx, orig.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(thread, func(m mail.Message) bool { return m.ID == sent.ID }) {
		t.Errorf("thread %s does not hold the reply %s", orig.ConversationID, sent.ID)
	}
}

// TestLive_Record re-captures the fixtures. It runs only with -record.
func TestLive_Record(t *testing.T) {
	if !*record {
		t.Skip("-record not set")
	}
	box := liveBox(t)
	ctx := t.Context()
	dir := filepath.Join("..", "..", "testdata", "gmail")
	write := func(name string, v any) {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("recorded %s (%d bytes)", name, len(b))
	}
	for _, id := range []string{fixtures.AppleForward, fixtures.RemoteOnly, fixtures.InlineCID, fixtures.IKEASeed} {
		raw, err := box.RawMessage(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		write("msg-"+id+".json", raw)
	}
	raw, err := box.RawThread(ctx, fixtures.TenancyThread)
	if err != nil {
		t.Fatal(err)
	}
	write("thread-"+fixtures.TenancyThread+".json", raw)
}
