package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/qiushiyan/mailkit/internal/cli"
	"github.com/qiushiyan/mailkit/internal/drafts"
	"github.com/qiushiyan/mailkit/internal/fixtures"
	"github.com/qiushiyan/mailkit/internal/mail"
	"github.com/qiushiyan/mailkit/internal/memory"
	"github.com/qiushiyan/mailkit/internal/pintest"
	"github.com/qiushiyan/mailkit/internal/render"
)

// The output callers read, byte for byte. skills/write-email runs these
// commands, reads the draft id and preview path compose prints, relays a
// commit's report, and follows the recovery a failed commit names. The
// cases run in order against one draft store, because a draft's state is
// what a later command reports. A file under testdata/contract that
// changes is a change those callers see (MAILKIT_CONTRACT_UPDATE=1
// rewrites them).
func TestContracts(t *testing.T) {
	w := newContractWorld(t)
	pdf := w.file("invoice.pdf", bytes.Repeat([]byte("PDF"), 500))
	note := w.file("note.txt", []byte("keep **stars** and <angles> as they are\n"))
	big := w.file("big.bin", bytes.Repeat([]byte{1}, 9<<10))
	steps := []struct {
		name  string
		args  []string
		stdin string
		setup func()
	}{
		{name: "compose", args: []string{"--to", "a@example.com,b@example.com", "--cc", "c@example.com", "--bcc", "d@example.com",
			"--subject", "Invoice — August", "--body", "Please find the invoice attached.\n\nThanks", "--attach", pdf, "--no-open"}},
		{name: "list-pending", args: []string{"--list"}},
		{name: "commit", args: []string{"--commit", "{draft}"}},
		{name: "commit-again", args: []string{"--commit", "{draft}"}},
		{name: "compose-spaced-recipients", args: []string{"-t", "a@example.com, b@example.com", "-s", "Spaced", "--body", "hello there", "--no-open"}},
		{name: "compose-stdin-html", stdin: "<p>Hello <b>there</b></p>", args: []string{"--to", "a@example.com", "--subject", "html", "--body-file", "-", "--format", "html", "--no-open"}},
		{name: "compose-file-text", args: []string{"--to", "a@example.com", "--subject", "verbatim", "--body-file", note, "--format", "text", "--no-open"}},
		{name: "compose-markdown-list", args: []string{"--to", "a@example.com", "--subject", "groceries", "--body", "- milk\n- eggs", "--no-open"}},
		{name: "compose-sender", args: []string{"--to", "a@example.com", "--subject", "alias", "--body", "hello there", "--sender", "alias@example.com", "--no-open"}},
		{name: "reply", args: []string{"--reply", "liam", "--body", "The refund works for me, thanks.", "--no-open"}},
		{name: "commit-reply", args: []string{"--commit", "{draft}"}},
		{name: "reply-all", args: []string{"--reply-all", "liam", "--cc", "boss@example.com", "--body", "Refund, please.", "--no-open"}},
		{name: "commit-reply-all", args: []string{"--commit", "{draft}"}},
		{name: "compose-over-limit", setup: func() { w.box.SendLimit = 10 << 10 },
			args: []string{"--to", "a@example.com", "--subject", "big", "--body", "see attached", "--attach", big, "--no-open"}},
		{name: "commit-over-limit", args: []string{"--commit", "{draft}"}},
		{name: "compose-flaky", setup: func() { w.box.SendLimit = 25 << 20 },
			args: []string{"--to", "a@example.com", "--subject", "flaky", "--body", "hello there", "--no-open"}},
		{name: "commit-lost", setup: func() { w.box.SendErr = errors.New("connection reset by peer") }, args: []string{"--commit", "{draft}"}},
		{name: "commit-unknown", setup: func() { w.box.SendErr = nil }, args: []string{"--commit", "{draft}"}},
		{name: "list", args: []string{"--list"}},

		{name: "bare"},
		{name: "commit-with-compose-flag", args: []string{"--commit", "{draft}", "--subject", "changed"}},
		{name: "commit-with-account", args: []string{"--commit", "{draft}", "--account", "outlook"}},
		{name: "commit-and-list", args: []string{"--commit", "{draft}", "--list"}},
		{name: "list-with-compose-flag", args: []string{"--list", "--to", "a@example.com"}},
		{name: "commit-missing", args: []string{"--commit", "20260822-120000-nope-0000"}},
		{name: "reply-with-to", args: []string{"--reply", "liam", "--to", "x@example.com", "--body", "hi"}},
		{name: "reply-with-subject", args: []string{"--reply", "liam", "--subject", "Keys", "--body", "hi"}},
		{name: "reply-and-reply-all", args: []string{"--reply", "liam", "--reply-all", "liam", "--body", "hi"}},
		{name: "reply-unknown", args: []string{"--reply", "nope", "--body", "hi", "--no-open"}},
		{name: "format-unknown", args: []string{"--to", "a@example.com", "--subject", "x", "--body", "hi", "--format", "rtf"}},
		{name: "two-bodies", args: []string{"--to", "a@example.com", "--subject", "x", "--body", "a", "--body-file", note}},
		{name: "no-subject", args: []string{"--to", "a@example.com", "--body", "hi"}},
		{name: "no-body", args: []string{"--to", "a@example.com", "--subject", "x"}},
		{name: "no-recipient", args: []string{"--subject", "x", "--body", "hi", "--no-open"}},
		{name: "bad-address", args: []string{"--to", "not-an-address", "--subject", "x", "--body", "hi", "--no-open"}},
		{name: "markdown-image", args: []string{"--to", "a@example.com", "--subject", "img", "--body", "![shot](https://example.com/x.png)", "--no-open"}},
		{name: "markdown-raw-html", args: []string{"--to", "a@example.com", "--subject", "log", "--body", "the error was <div>boom</div>", "--no-open"}},
		{name: "missing-attachment", args: []string{"--to", "a@example.com", "--subject", "x", "--body", "hi", "--attach", w.dir + "/nope.pdf", "--no-open"}},
		{name: "missing-body-file", args: []string{"--to", "a@example.com", "--subject", "x", "--body-file", w.dir + "/nope.md", "--no-open"}},
		{name: "unknown-flag", args: []string{"--bogus"}},
		{name: "signed-out", setup: func() { w.box.AuthErr = mail.ErrAuth },
			args: []string{"--to", "a@example.com", "--subject", "x", "--body", "hi", "--no-open"}},
	}
	var names []string
	draft := ""
	for _, s := range steps {
		names = append(names, s.name)
		if s.setup != nil {
			s.setup()
		}
		args := make([]string, len(s.args))
		for i, a := range s.args {
			args[i] = strings.ReplaceAll(a, "{draft}", draft)
		}
		r := w.run(s.stdin, args...)
		if m := commitLine.FindStringSubmatch(r.Stdout); m != nil {
			draft = m[1]
		}
		pintest.Check(t, "testdata/contract", s.name, w.norm(r))
	}
	pintest.Stale(t, "testdata/contract", names)
}

// commitLine is how compose names the draft it wrote.
var commitLine = regexp.MustCompile(`send-mail (?:--)?commit (\S+)`)

// draftSuffix is the random part of a draft id, the one thing about it
// that differs between runs.
var draftSuffix = regexp.MustCompile(`(\d{8}-\d{6}-[a-z0-9-]*?)-[0-9a-f]{4}\b`)

type contractWorld struct {
	t    *testing.T
	box  *memory.Mailbox
	dir  string
	deps cli.Deps
}

func newContractWorld(t *testing.T) *contractWorld {
	t.Helper()
	msg, day, para := fixtures.Msg, fixtures.Day, fixtures.Para
	ask := msg("ask", "ticket", day(0), "Me <me@example.com>", "Refund request", para("I was charged for a renewal I did not want; can it be refunded?"))
	ask.MessageID = "ask@mail.example"
	ask.To = []mail.Address{{Email: "help@vendor.example"}}
	answer := msg("liam", "ticket", day(1), "Liam <liam@vendor.example>", "Re: Refund request", para("We can refund the unused part of the quarter, or upgrade you to annual."))
	answer.MessageID = "CALag-refund@mail.example"
	answer.InReplyTo = []mail.MessageID{"ask@mail.example"}
	answer.References = []mail.MessageID{"ask@mail.example"}
	answer.Cc = []mail.Address{{Email: "billing@vendor.example"}, {Email: "me@example.com"}}

	w := &contractWorld{t: t, dir: t.TempDir()}
	w.box = memory.New(ask, answer)
	w.box.TextOf = func(m mail.Message) string { return render.Text(m.Body) }
	w.deps = cli.Deps{
		Open: func(ctx context.Context, account string) (mail.Mailbox, error) {
			if account != "gmail" && account != "outlook" {
				return nil, fmt.Errorf("unknown account %q; pick one of gmail, outlook", account)
			}
			return w.box, nil
		},
		Drafts:   drafts.Store{Dir: w.dir + "/drafts"},
		Now:      func() time.Time { return time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC) },
		Accounts: []string{"gmail", "outlook"},
	}
	return w
}

func (w *contractWorld) file(name string, b []byte) string {
	p := filepath.Join(w.dir, name)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		w.t.Fatal(err)
	}
	return p
}

func (w *contractWorld) run(stdin string, args ...string) pintest.Run {
	var out, errOut bytes.Buffer
	root := cli.SendMail(w.deps, &out, &errOut, strings.NewReader(stdin))
	root.SetContext(w.t.Context())
	code := cli.Run(root, args, &errOut)
	return pintest.Run{Tool: "send-mail", Args: args, Code: code, Stdout: out.String(), Stderr: errOut.String()}
}

// norm replaces what differs between runs: the temporary directory and
// the random suffix of each draft id.
func (w *contractWorld) norm(r pintest.Run) string {
	s := strings.ReplaceAll(r.String(), w.dir, "$TMP")
	return draftSuffix.ReplaceAllString(s, "${1}-XXXX")
}
