package drafts_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qiushiyan/mailkit/internal/drafts"
	"github.com/qiushiyan/mailkit/internal/mail"
	"github.com/qiushiyan/mailkit/internal/memory"
)

var now = time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)

// opener hands out box, or fails the way an adapter that cannot start does.
func opener(box mail.Mailbox, err error) mail.Opener {
	return func(context.Context, string) (mail.Mailbox, error) { return box, err }
}

// pending writes a fresh draft and returns the store and its id.
func pending(t *testing.T, box *memory.Mailbox) (drafts.Store, string) {
	t.Helper()
	s := drafts.Store{Dir: t.TempDir()}
	d, err := s.Draft(t.Context(), opener(box, nil), drafts.Request{
		Account: "gmail", To: []string{"a@example.com"}, Subject: "rent", Body: drafts.PlainBody("hello there"),
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	return s, d.ID
}

func state(t *testing.T, s drafts.Store, id string) drafts.State {
	t.Helper()
	r, err := s.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	return r.State
}

// Rule: the draft is written with the preview of its own bytes; a caller
// cannot have one without the other.
func TestDraft_WritesThePreviewOfItsBytes(t *testing.T) {
	box := memory.New()
	box.SendLimit = 10
	s, id := pending(t, box)
	page, err := os.ReadFile(filepath.Join(s.Dir, id+".html"))
	if err != nil {
		t.Fatalf("no preview beside the draft: %v", err)
	}
	if !strings.Contains(string(page), "hello there") || !strings.Contains(string(page), "a@example.com") {
		t.Errorf("preview does not show the draft:\n%s", page)
	}
}

// Rule: everything before Send is local or read-only, so a failure there
// returns the draft to pending; once Send may have transmitted, a failure
// is an unknown outcome and the draft is never sent again blind.
func TestCommit_OutcomeByWhereItFailed(t *testing.T) {
	for name, tc := range map[string]struct {
		fault func(box *memory.Mailbox) mail.Opener
		want  drafts.State
		says  string
	}{
		"adapter cannot start": {func(box *memory.Mailbox) mail.Opener { return opener(nil, mail.ErrAuth) },
			drafts.Pending, mail.LoginCommand("gmail")},
		"signed out": {func(box *memory.Mailbox) mail.Opener { box.AuthErr = mail.ErrAuth; return opener(box, nil) },
			drafts.Pending, mail.LoginCommand("gmail")},
		"over the limit": {func(box *memory.Mailbox) mail.Opener { box.SendLimit = 10; return opener(box, nil) },
			drafts.Pending, "over the"},
		"refused before transmission": {func(box *memory.Mailbox) mail.Opener {
			return opener(refusing{box, fmt.Errorf("%w: recipients differ", mail.ErrNotSent)}, nil)
		}, drafts.Pending, "recipients differ"},
		"lost after transmission": {func(box *memory.Mailbox) mail.Opener {
			box.SendErr = errors.New("connection reset by peer")
			return opener(box, nil)
		}, drafts.Unknown, "connection reset"},
	} {
		t.Run(name, func(t *testing.T) {
			box := memory.New()
			s, id := pending(t, box)
			_, err := s.Commit(t.Context(), id, tc.fault(box), now)
			if err == nil || !strings.Contains(err.Error(), tc.says) {
				t.Fatalf("error %v, want one saying %q", err, tc.says)
			}
			if got := state(t, s, id); got != tc.want {
				t.Errorf("draft is %s, want %s", got, tc.want)
			}
			if len(box.Sent) != 0 && tc.want == drafts.Pending {
				t.Error("a draft returned to pending was sent")
			}
		})
	}
}

func TestCommit_EditedBytesStayPending(t *testing.T) {
	box := memory.New()
	s, id := pending(t, box)
	eml := filepath.Join(s.Dir, id+".eml")
	b, _ := os.ReadFile(eml)
	os.WriteFile(eml, []byte(strings.Replace(string(b), "hello", "jello", 1)), 0o600)
	if _, err := s.Commit(t.Context(), id, opener(box, nil), now); err == nil || !strings.Contains(err.Error(), "re-draft") {
		t.Fatalf("an edited draft must be refused with the way out: %v", err)
	}
	if state(t, s, id) != drafts.Pending || len(box.Sent) != 0 {
		t.Error("an edited draft must stay pending and unsent")
	}
}

func TestCommit_ReceiptSaysWhatWasSent(t *testing.T) {
	box := memory.New()
	s, id := pending(t, box)
	r, err := s.Commit(t.Context(), id, opener(box, nil), now)
	if err != nil {
		t.Fatal(err)
	}
	if r.State != drafts.Sent || r.SentAs != "me@example.com" || r.Sent.ID == "" || !r.SentAt.Equal(now) {
		t.Errorf("receipt: state %s, as %q, provider id %q, at %v", r.State, r.SentAs, r.Sent.ID, r.SentAt)
	}
	if state(t, s, id) != drafts.Sent {
		t.Error("the record on disk is not sent")
	}
	if _, err := s.Commit(t.Context(), id, opener(box, nil), now); err == nil || len(box.Sent) != 1 {
		t.Error("a sent draft committed again must be refused, unsent")
	}
}

// refusing is a provider that refuses every send before transmitting.
type refusing struct {
	*memory.Mailbox
	err error
}

func (r refusing) Send(context.Context, *mail.Prepared, *mail.Parent) (mail.Sent, error) {
	return mail.Sent{}, r.err
}
