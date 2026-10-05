//go:build live

package graph_test

import (
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
	"github.com/qiushiyan/mailkit/internal/graph"
	"github.com/qiushiyan/mailkit/internal/mail"
	"github.com/qiushiyan/mailkit/internal/render"
)

// First contact with a real Outlook mailbox:
//
//	go test -tags live ./internal/graph -run Live -v [-record]
//
// Each check names a failure that already happened once on Gmail, so a
// pass is evidence and a skip is an open question. With -record, the raw
// responses replace the doc-derived cassettes under testdata/graph.
var record = flag.Bool("record", false, "re-record testdata/graph from the live API")

func liveBox(t *testing.T) *graph.Mailbox {
	t.Helper()
	client, err := graph.Client(context.Background())
	if err != nil {
		t.Skipf("no live credentials: %v", err)
	}
	box := graph.New(client)
	if _, err := box.Account(context.Background()); err != nil {
		t.Skipf("not signed in: %v", err)
	}
	return box
}

func TestLive_FirstContact(t *testing.T) {
	box := liveBox(t)
	ctx := t.Context()

	t.Run("1 search returns anything at all", func(t *testing.T) {
		hits, err := box.Search(ctx, mail.Criteria{}, 5)
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) == 0 {
			t.Fatal("no results -- check Mail.Read consent")
		}
		for _, h := range hits {
			t.Logf("%s  %q", h.ID, h.Subject)
		}
	})

	t.Run("2 a reply chain folds when the only quoting is a From:/Sent: block", func(t *testing.T) {
		replies, err := box.Search(ctx, mail.Criteria{SubjectTerms: []string{"RE:"}}, 5)
		if err != nil || len(replies) == 0 {
			t.Skipf("no reply found to test: %v", err)
		}
		m, err := box.Fetch(ctx, replies[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		text := render.Text(m.Body)
		spoken, quoted, marker := render.Split(text)
		t.Logf("%q: body %d -> spoken %d, quoted %d, marker=%s", m.Subject, len(text), len(spoken), len(quoted), marker)
		if quoted == "" && marker == "" && len(text) > 2*len(spoken) {
			t.Error("quoted text present but no boundary recognised")
		}

		t.Run("3 uniqueBody and our own folding agree", func(t *testing.T) {
			if m.ProviderFolded == nil {
				t.Skip("uniqueBody not returned")
			}
			theirs := render.Text(*m.ProviderFolded)
			diff := len(spoken) - len(theirs)
			if diff < 0 {
				diff = -diff
			}
			t.Logf("ours %d chars vs uniqueBody %d chars", len(spoken), len(theirs))
			if diff > 80 {
				t.Error("DIVERGE -- read both before trusting either")
			}
		})
	})

	t.Run("4 a forward survives whole", func(t *testing.T) {
		fwd, err := box.Search(ctx, mail.Criteria{SubjectTerms: []string{"FW:"}}, 3)
		if err != nil || len(fwd) == 0 {
			t.Skipf("no forward found to test: %v", err)
		}
		m, err := box.Fetch(ctx, fwd[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		tr := render.Build([]mail.Message{m})
		turn := tr.Turns[0]
		t.Logf("raw %d -> kept %d", tr.RawChars, tr.TranscriptChars)
		if turn.QuotedChars > 0 {
			t.Error("a forward was folded; its content has no copy upstream")
		}
	})

	t.Run("5 attachment kinds Gmail never produced", func(t *testing.T) {
		with, err := box.Search(ctx, mail.Criteria{HasAttachment: true}, 8)
		if err != nil {
			t.Fatal(err)
		}
		seen := map[string]int{}
		for _, h := range with {
			m, err := box.Fetch(ctx, h.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range m.Parts {
				seen[p.Kind()]++
			}
		}
		for kind, n := range seen {
			t.Logf("%-18s %d", kind, n)
		}
	})

	t.Run("6 a reply lands in the conversation, addressed as previewed", func(t *testing.T) {
		liveReply(t, box)
	})

	if *record {
		t.Run("record", func(t *testing.T) {
			hits, err := box.Search(ctx, mail.Criteria{}, 3)
			if err != nil {
				t.Fatal(err)
			}
			var raws []any
			for _, h := range hits {
				raw, err := box.RawMessage(ctx, h.ID)
				if err != nil {
					t.Fatal(err)
				}
				raws = append(raws, raw)
			}
			b, _ := json.Marshal(raws)
			p := filepath.Join("..", "..", "testdata", "graph", "messages.live.json")
			if err := os.WriteFile(p, b, 0o644); err != nil {
				t.Fatal(err)
			}
			t.Logf("recorded %s -- review, then replace messages.json and delete README.txt's doc-derived notice", p)
		})
	}
}

// liveReply answers a real message, so it runs only when told which one --
// MAILKIT_LIVE_REPLY_TO=<id>, in a conversation you own. It settles what
// the reference leaves open (graph.go, replyAction): the reply must join
// the original's conversation, and reach exactly the recipients the draft
// names -- not more, not fewer.
func liveReply(t *testing.T, box *graph.Mailbox) {
	id := os.Getenv("MAILKIT_LIVE_REPLY_TO")
	if id == "" {
		t.Skip("set MAILKIT_LIVE_REPLY_TO to a message id in a conversation you own; this sends a real reply")
	}
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
	token := "mailkit-live-" + time.Now().Format("20060102T150405")
	rec, p, err := drafts.Store{Dir: t.TempDir()}.Create(drafts.Compose{
		Account: "outlook", From: acct.Address, Reply: &r,
		Body: drafts.PlainBody("mailkit live check " + token + ": this reply should sit in the original's conversation."),
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := box.Send(ctx, p, rec.Parent()); err != nil {
		t.Fatal(err)
	}
	// 202 means accepted, not filed: poll Sent Items' conversation briefly.
	var got *mail.Message
	for range 15 {
		msgs, err := box.Conversation(ctx, orig.ConversationID)
		if err != nil {
			t.Fatal(err)
		}
		for i := range msgs {
			if strings.Contains(render.Text(msgs[i].Body), token) {
				got = &msgs[i]
			}
		}
		if got != nil {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if got == nil {
		t.Fatalf("no reply carrying %s joined conversation %s -- check Sent Items for where it went", token, orig.ConversationID)
	}
	want := append(slices.Clone(rec.To), rec.Cc...)
	var reached []string
	for _, a := range append(slices.Clone(got.To), got.Cc...) {
		reached = append(reached, a.Email)
	}
	slices.Sort(want)
	slices.Sort(reached)
	if !slices.EqualFunc(want, reached, strings.EqualFold) {
		t.Errorf("the reply reached %v; the draft named %v", reached, want)
	}
}
