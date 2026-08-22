package cli_test

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/qiushiyan/mailkit/internal/cli"
	"github.com/qiushiyan/mailkit/internal/drafts"
	"github.com/qiushiyan/mailkit/internal/images"
	"github.com/qiushiyan/mailkit/internal/mail"
	"github.com/qiushiyan/mailkit/internal/memory"
	"github.com/qiushiyan/mailkit/internal/render"
)

// harness drives the real command trees against the memory adapter. It is
// the only way the tests reach the code: the interface is the test surface.
type harness struct {
	t     *testing.T
	box   *memory.Mailbox
	deps  cli.Deps
	stdin string
}

func newHarness(t *testing.T, msgs ...mail.Message) *harness {
	t.Helper()
	box := memory.New(msgs...)
	box.TextOf = func(m mail.Message) string { return render.Text(m.Body.HTML, m.Body.Text) }
	dir := t.TempDir()
	h := &harness{t: t, box: box}
	h.deps = cli.Deps{
		Open: func(ctx context.Context, account string) (mail.Mailbox, error) {
			if account != "gmail" && account != "outlook" && account != "memory" {
				return nil, errors.New("unknown account " + account)
			}
			return box, nil
		},
		Login: func(ctx context.Context, account string, out func(string)) error {
			out("logged in " + account)
			return nil
		},
		Images:        &images.Fetcher{Client: nil, Cap: 1 << 20, UserAgent: "test"},
		Drafts:        drafts.Store{Dir: dir + "/drafts"},
		AttachmentDir: func(id string) string { return dir + "/attachments/" + id },
		Now:           func() time.Time { return time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC) },
		Accounts:      []string{"gmail", "outlook"},
	}
	return h
}

type result struct {
	code   int
	stdout string
	stderr string
}

func (r result) json(t *testing.T) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &v); err != nil {
		t.Fatalf("stdout is not a JSON object: %v\n%s", err, r.stdout)
	}
	return v
}

func (r result) jsonList(t *testing.T, key string) []map[string]any {
	t.Helper()
	raw, ok := r.json(t)[key]
	if !ok {
		t.Fatalf("no %q in output:\n%s", key, r.stdout)
	}
	var out []map[string]any
	for _, item := range raw.([]any) {
		out = append(out, item.(map[string]any))
	}
	return out
}

func (h *harness) find(args ...string) result {
	h.t.Helper()
	var out, errOut bytes.Buffer
	root := cli.MailFind(h.deps, &out, &errOut)
	root.SetContext(h.t.Context())
	code := cli.Run(root, args, &errOut)
	return result{code, out.String(), errOut.String()}
}

func (h *harness) send(args ...string) result {
	h.t.Helper()
	var out, errOut bytes.Buffer
	root := cli.SendMail(h.deps, &out, &errOut, strings.NewReader(h.stdin))
	root.SetContext(h.t.Context())
	code := cli.Run(root, args, &errOut)
	return result{code, out.String(), errOut.String()}
}

func mustOK(t *testing.T, r result) result {
	t.Helper()
	if r.code != 0 {
		t.Fatalf("exit %d\nstderr: %s\nstdout: %s", r.code, r.stderr, r.stdout)
	}
	return r
}

func mustFail(t *testing.T, r result) result {
	t.Helper()
	if r.code == 0 {
		t.Fatalf("expected failure, got exit 0\nstdout: %s", r.stdout)
	}
	return r
}

// msg is a constructor for scenario messages; the fold and clustering
// tests build conversations from it rather than from serialised fixtures.
func msg(id, conv string, received time.Time, from, subject, html string) mail.Message {
	return mail.Message{
		ID: id, ConversationID: conv, MessageID: mail.MessageID(id + "@test"),
		Received: received, DateHeader: received.Format(time.RFC1123Z),
		From: mail.ParseAddress(from), To: []mail.Address{{Email: "me@example.com"}}, Subject: subject,
		Body: mail.Body{HTML: html},
	}
}

func day(n int) time.Time { return time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC).AddDate(0, 0, n) }

func para(lines ...string) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString("<p>" + l + "</p>")
	}
	return b.String()
}
