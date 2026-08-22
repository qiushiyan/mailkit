//go:build live

package graph_test

import (
	"context"
	"encoding/json/v2"
	"flag"
	"os"
	"path/filepath"
	"testing"

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
		text := render.Text(m.Body.HTML, m.Body.Text)
		spoken, quoted, marker := render.Split(text)
		t.Logf("%q: body %d -> spoken %d, quoted %d, marker=%s", m.Subject, len(text), len(spoken), len(quoted), marker)
		if quoted == "" && marker == "" && len(text) > 2*len(spoken) {
			t.Error("quoted text present but no boundary recognised")
		}

		t.Run("3 uniqueBody and our own folding agree", func(t *testing.T) {
			if m.ProviderFolded == nil {
				t.Skip("uniqueBody not returned")
			}
			theirs := render.Text(m.ProviderFolded.HTML, m.ProviderFolded.Text)
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
