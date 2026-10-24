package main

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/alecthomas/kong"
	"github.com/qiushiyan/clikit"
	"github.com/qiushiyan/clikit/clikittest"
	"github.com/qiushiyan/mailkit/internal/fixtures"
	"github.com/qiushiyan/mailkit/internal/images"
	"github.com/qiushiyan/mailkit/internal/mail"
	"github.com/qiushiyan/mailkit/internal/memory"
	"github.com/qiushiyan/mailkit/internal/paths"
	"github.com/qiushiyan/mailkit/internal/render"
)

// Scenario messages are built, not loaded: see fixtures.Msg.
var msg, day, para = fixtures.Msg, fixtures.Day, fixtures.Para

// harness runs mail-find exactly as main does, over the memory adapter, a
// temporary state directory and a fixed clock. It is the only way these
// tests reach the code: the command line is the test surface.
type harness struct {
	t    *testing.T
	box  *memory.Mailbox
	root string // the temporary directory everything lives under
	dirs paths.Dirs
	// images fetches remote pictures; nil fetches nothing.
	images *images.Fetcher
}

func newHarness(t *testing.T, msgs ...mail.Message) *harness {
	t.Helper()
	box := memory.New(msgs...)
	box.TextOf = func(m mail.Message) string { return render.Text(m.Body) }
	dir := t.TempDir()
	return &harness{t: t, box: box, root: dir, dirs: paths.Dirs{Config: dir + "/config", State: dir + "/state"},
		images: &images.Fetcher{Cap: 1 << 20, UserAgent: "test"}}
}

// now is the harness's clock.
var now = time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)

func (h *harness) open(ctx context.Context, account string) (mail.Mailbox, error) {
	if account != "gmail" && account != "outlook" {
		return nil, fmt.Errorf("unknown account %q; pick one of gmail, outlook", account)
	}
	return h.box, nil
}

func fakeLogin(ctx context.Context, account string, out func(string)) error {
	out("logged in " + account)
	return nil
}

type result struct {
	code   int
	stdout string
	stderr string
}

// app is the production App with the harness's stand-ins bound over the
// real ones: Kong keeps one binding per type, and the later one wins.
func (h *harness) app() clikit.App {
	a := app
	a.Options = append(slices.Clone(app.Options),
		kong.BindTo(h.t.Context(), (*context.Context)(nil)),
		kong.Bind(mail.Opener(h.open)),
		kong.Bind(h.dirs),
		kong.Bind(clock(func() time.Time { return now })),
		kong.Bind(h.images),
		kong.Bind(login(fakeLogin)),
	)
	return a
}

func (h *harness) find(args ...string) result {
	h.t.Helper()
	r := clikittest.Run(h.app(), args...)
	return result{r.Code, r.Stdout, r.Stderr}
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

func unmarshal(s string, v any) error { return json.Unmarshal([]byte(s), v) }

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
