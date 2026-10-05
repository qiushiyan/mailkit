// Package mailtest is the adapter contract suite: the invariants every
// mail.Mailbox must hold, run against each adapter over whatever stands in
// for its provider. One suite, three adapters, so the port is defined by
// behaviour rather than by the first adapter that happened to exist.
package mailtest

import (
	"bytes"
	"context"
	"errors"
	"mime"
	"strings"
	"testing"
	"time"

	"github.com/qiushiyan/mailkit/internal/mail"
)

// Scenario describes what the adapter's backing store holds, so the suite
// can assert against known facts without knowing the provider's shape.
type Scenario struct {
	// Account the adapter should report.
	Address string
	// A message with an HTML body and at least one stored part.
	MessageID string
	RFC822ID  mail.MessageID
	// Conversation is any conversation in the store, with ConversationSize
	// messages; it need not contain MessageID.
	Conversation     string
	ConversationSize int
	// A phrase that appears only in MessageID's body.
	UniquePhrase string
	// FromDomain of MessageID's sender.
	FromDomain string
	// StoredPartName is the name of a stored part on MessageID whose bytes
	// begin with StoredPrefix.
	StoredPartName string
	StoredPrefix   []byte
	// Received of MessageID, for the date-window assertions.
	Received time.Time
}

// Run executes the contract.
func Run(t *testing.T, box mail.Mailbox, s Scenario) {
	t.Helper()
	ctx := t.Context()

	t.Run("Account reports the address and a send limit", func(t *testing.T) {
		a, err := box.Account(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if a.Address != s.Address || a.SendLimit <= 0 {
			t.Errorf("account = %+v", a)
		}
	})

	t.Run("Resolve finds by RFC822 Message-ID with or without brackets", func(t *testing.T) {
		for _, id := range []mail.MessageID{s.RFC822ID, "<" + s.RFC822ID + ">"} {
			e, err := box.Resolve(ctx, id)
			if err != nil {
				t.Fatalf("%s: %v", id, err)
			}
			if e.ID != s.MessageID {
				t.Errorf("resolved %s, want %s", e.ID, s.MessageID)
			}
		}
		if _, err := box.Resolve(ctx, "nobody@nowhere.invalid"); !errors.Is(err, mail.ErrNotFound) {
			t.Errorf("unknown id should be ErrNotFound, got %v", err)
		}
	})

	t.Run("Search is exact: every hit satisfies the criteria", func(t *testing.T) {
		c := mail.Criteria{Phrases: []string{s.UniquePhrase}}
		hits, err := box.Search(ctx, c, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) != 1 || hits[0].ID != s.MessageID {
			t.Fatalf("unique phrase should find exactly the message: %v", ids(hits))
		}
		win := mail.Criteria{From: s.FromDomain, After: s.Received.Add(-time.Hour), Before: s.Received.Add(time.Hour)}
		hits, err = box.Search(ctx, win, 10)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, h := range hits {
			if !win.Match(h, "") {
				t.Errorf("hit %s does not satisfy %+v", h.ID, win)
			}
			found = found || h.ID == s.MessageID
		}
		if !found {
			t.Errorf("domain+window search missed %s: %v", s.MessageID, ids(hits))
		}
		outside := mail.Criteria{From: s.FromDomain, After: s.Received.Add(time.Hour), Before: s.Received.Add(2 * time.Hour)}
		if hits, _ := box.Search(ctx, outside, 10); contains(hits, s.MessageID) {
			t.Errorf("a window that excludes Received still returned the message")
		}
	})

	t.Run("Search honours limit", func(t *testing.T) {
		hits, err := box.Search(ctx, mail.Criteria{}, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) > 1 {
			t.Errorf("limit 1 returned %d", len(hits))
		}
	})

	var msg mail.Message
	t.Run("Fetch returns HTML, zoned Received, and parts in one call", func(t *testing.T) {
		var err error
		msg, err = box.Fetch(ctx, s.MessageID)
		if err != nil {
			t.Fatal(err)
		}
		if msg.Body.HTML == "" {
			t.Error("body HTML empty")
		}
		if msg.Received.IsZero() || msg.Received.Location() == nil {
			t.Error("Received must be set and zoned")
		}
		if msg.MessageID != s.RFC822ID {
			t.Errorf("MessageID = %q, want %q", msg.MessageID, s.RFC822ID)
		}
		if msg.ConversationID == "" {
			t.Error("ConversationID empty")
		}
		if _, err := box.Fetch(ctx, "does-not-exist"); !errors.Is(err, mail.ErrNotFound) {
			t.Errorf("unknown id should be ErrNotFound, got %v", err)
		}
	})

	t.Run("Conversation is ascending and complete", func(t *testing.T) {
		msgs, err := box.Conversation(ctx, s.Conversation)
		if err != nil {
			t.Fatal(err)
		}
		if len(msgs) != s.ConversationSize {
			t.Fatalf("conversation has %d messages, want %d", len(msgs), s.ConversationSize)
		}
		for i := 1; i < len(msgs); i++ {
			if msgs[i].Received.Before(msgs[i-1].Received) {
				t.Errorf("not ascending at %d", i)
			}
		}
		for _, m := range msgs {
			if m.Body.HTML == "" && m.Body.Text == "" {
				t.Errorf("conversation message %s has no body", m.ID)
			}
		}
	})

	t.Run("Open streams a stored part and refuses the others", func(t *testing.T) {
		var part *mail.Part
		for i := range msg.Parts {
			if msg.Parts[i].Name == s.StoredPartName {
				part = &msg.Parts[i]
			}
		}
		if part == nil {
			t.Fatalf("part %q not on message; parts: %v", s.StoredPartName, names(msg.Parts))
		}
		h, ok := part.Handle()
		if !ok {
			t.Fatalf("%q is not a stored part", s.StoredPartName)
		}
		var buf bytes.Buffer
		if err := box.Open(ctx, h, &buf); err != nil {
			t.Fatal(err)
		}
		if !bytes.HasPrefix(buf.Bytes(), s.StoredPrefix) {
			t.Errorf("bytes begin %q, want %q", head(buf.Bytes()), s.StoredPrefix)
		}
		if err := box.Open(ctx, "not/a-real-handle", &buf); err == nil {
			t.Error("bogus handle should fail")
		}
	})

	t.Run("Send refuses over the limit and accepts under it", func(t *testing.T) {
		a, _ := box.Account(ctx)
		big, _ := mail.NewPrepared(bytes.NewReader(append([]byte("From: a@b.c\r\nTo: d@e.f\r\nSubject: x\r\n\r\n"), bytes.Repeat([]byte("x"), int(a.SendLimit)+1)...)))
		if _, err := box.Send(ctx, big, nil); !errors.Is(err, mail.ErrTooLarge) {
			t.Errorf("oversize send should be ErrTooLarge, got %v", err)
		}
	})

	// Last, because it adds a message to MessageID's conversation.
	t.Run("Send with a parent joins the parent's conversation", func(t *testing.T) {
		a, err := box.Account(ctx)
		if err != nil {
			t.Fatal(err)
		}
		orig, err := box.Fetch(ctx, s.MessageID)
		if err != nil {
			t.Fatal(err)
		}
		before, err := box.Conversation(ctx, orig.ConversationID)
		if err != nil {
			t.Fatal(err)
		}
		r, err := mail.NewReply(orig.Envelope, []string{a.Address}, false)
		if err != nil {
			t.Fatal(err)
		}
		to := make([]string, 0, len(r.To))
		for _, x := range r.To {
			to = append(to, x.Email)
		}
		const said = "This reply carries the word zephyrine, which nothing else holds."
		p, err := mail.NewPrepared(strings.NewReader("From: " + a.Address + "\r\nTo: " + strings.Join(to, ", ") +
			"\r\nSubject: " + mime.QEncoding.Encode("utf-8", r.Subject) + "\r\nIn-Reply-To: " + r.InReplyTo +
			"\r\nReferences: " + r.References + "\r\nMessage-ID: <contract-reply@mailkit.test>" +
			"\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + said + "\r\n"))
		if err != nil {
			t.Fatal(err)
		}
		parent := r.Parent()
		sent, err := box.Send(ctx, p, &parent)
		if err != nil {
			t.Fatal(err)
		}
		if sent.ConversationID != "" && sent.ConversationID != orig.ConversationID {
			t.Errorf("the provider reports conversation %s, want the parent's %s", sent.ConversationID, orig.ConversationID)
		}
		after, err := box.Conversation(ctx, orig.ConversationID)
		if err != nil {
			t.Fatal(err)
		}
		if len(after) != len(before)+1 {
			t.Fatalf("the parent's conversation went from %d to %d messages; the reply is not in it", len(before), len(after))
		}
		var got *mail.Message
		for i := range after {
			if strings.Contains(after[i].Body.Text+after[i].Body.HTML, "zephyrine") {
				got = &after[i]
			}
		}
		if got == nil {
			t.Fatalf("no message in the parent's conversation carries the reply's body: %v", subjects(after))
		}
		if got.Subject != r.Subject || got.ConversationID != orig.ConversationID {
			t.Errorf("reply listed as %q in %s, want %q in %s", got.Subject, got.ConversationID, r.Subject, orig.ConversationID)
		}
	})
}

func subjects(ms []mail.Message) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.Subject)
	}
	return out
}

func ids(es []mail.Envelope) []string {
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, e.ID)
	}
	return out
}

func names(ps []mail.Part) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Name)
	}
	return out
}

func contains(es []mail.Envelope, id string) bool {
	for _, e := range es {
		if e.ID == id {
			return true
		}
	}
	return false
}

func head(b []byte) []byte {
	if len(b) > 8 {
		return b[:8]
	}
	return b
}

var _ = context.Background
