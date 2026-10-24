package main

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alecthomas/kong"
	"github.com/qiushiyan/clikit"
	"github.com/qiushiyan/clikit/clikittest"
	"github.com/qiushiyan/mailkit/internal/drafts"
	"github.com/qiushiyan/mailkit/internal/fixtures"
	"github.com/qiushiyan/mailkit/internal/mail"
	"github.com/qiushiyan/mailkit/internal/memory"
	"github.com/qiushiyan/mailkit/internal/paths"
	"github.com/qiushiyan/mailkit/internal/render"
)

// Scenario messages are built, not loaded: see fixtures.Msg.
var msg, day, para = fixtures.Msg, fixtures.Day, fixtures.Para

// harness runs send-mail exactly as main does, over the memory adapter, a
// temporary draft store and a fixed clock. It is the only way these tests
// reach the code: the command line is the test surface.
type harness struct {
	t     *testing.T
	box   *memory.Mailbox
	root  string // the temporary directory everything lives under
	dirs  paths.Dirs
	stdin string
	// open is the account opener; tests swap it for a provider that places
	// or refuses a send its own way.
	open mail.Opener
}

func newHarness(t *testing.T, msgs ...mail.Message) *harness {
	t.Helper()
	box := memory.New(msgs...)
	box.TextOf = func(m mail.Message) string { return render.Text(m.Body) }
	dir := t.TempDir()
	h := &harness{t: t, box: box, root: dir, dirs: paths.Dirs{Config: dir + "/config", State: dir + "/state"}}
	h.open = func(ctx context.Context, account string) (mail.Mailbox, error) {
		if account != "gmail" && account != "outlook" {
			return nil, fmt.Errorf("unknown account %q; pick one of gmail, outlook", account)
		}
		return h.box, nil
	}
	return h
}

// drafts is the store the harness's runs write to.
func (h *harness) drafts() drafts.Store { return drafts.Store{Dir: h.dirs.Drafts()} }

// now is the harness's clock.
var now = time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)

type result struct {
	code   int
	stdout string
	stderr string
}

// app is the production App with the harness's stand-ins bound over the
// real ones: Kong keeps one binding per type, and the later one wins.
func (h *harness) app() clikit.App {
	a := app
	a.Stdin = strings.NewReader(h.stdin)
	a.Options = append(slices.Clone(app.Options),
		kong.BindTo(h.t.Context(), (*context.Context)(nil)),
		kong.Bind(h.open),
		kong.Bind(h.dirs),
		kong.Bind(clock(func() time.Time { return now })),
		kong.Bind(previewer(func(string) error { return nil })),
	)
	return a
}

func (h *harness) send(args ...string) result {
	h.t.Helper()
	r := clikittest.Run(h.app(), args...)
	return result{r.Code, r.Stdout, r.Stderr}
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
