package main

import (
	"context"
	"encoding/json/v2"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/qiushiyan/clikit/clikittest"

	"github.com/qiushiyan/mailkit/internal/fixtures"
	"github.com/qiushiyan/mailkit/internal/mail"
	"github.com/qiushiyan/mailkit/internal/pintest"
	"github.com/qiushiyan/mailkit/internal/wiring"
)

// Every help page, pinned; make golden accepts an intended change. Help
// must never reach an account: the opener here fails the test if it is
// called, so a page that loads credentials cannot pass.
func TestHelpPages(t *testing.T) {
	h := newHarness(t)
	h.open = func(context.Context, string) (mail.Mailbox, error) {
		t.Error("a help page opened an account")
		return nil, context.Canceled
	}
	clikittest.Golden(t, h.app(), "testdata/help")
}

// Every mail-find command line an agent is handed -- in a result's
// next_steps, in an error's recovery, in a help page's examples, as the
// login hint, in the skills' instructions -- parses against the grammar, so
// a renamed flag cannot leave an instruction that fails when it is run.
func TestPrintedCommandsParse(t *testing.T) {
	lines := pintest.Commands(t, "mail-find", "testdata/contract", "../send-mail/testdata/contract", "testdata/help", "../send-mail/testdata/help")
	lines = append(lines, pintest.Fenced(t, "mail-find", "../../skills/read-email/SKILL.md", "../../skills/write-email/SKILL.md")...)
	for _, a := range wiring.Accounts {
		lines = append(lines, mail.LoginCommand(a))
	}
	if len(lines) < 10 {
		t.Fatalf("found only %d command lines; the extraction is broken: %q", len(lines), lines)
	}
	for _, line := range lines {
		k, err := kong.New(app.New(), append(slices.Clone(app.Options), kong.Exit(func(int) { t.Errorf("%s: exited", line) }))...)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := k.Parse(pintest.Split(line)[1:]); err != nil {
			t.Errorf("%s: %v", line, err)
		}
	}
}

// Each command's JSON section names every key its output carries, so the
// page an agent learns the shape from cannot fall behind the shape.
func TestTheJSONIsDescribed(t *testing.T) {
	w := newContractWorld(t)
	thread := fixtures.Thread(t, fixtures.TenancyThread)
	for cmd, runs := range map[string][][]string{
		"search":      {{"search", "from:shop.example"}, {"search", "lunch"}},
		"resolve":     {{"resolve", "abc.123@mail.example"}},
		"read":        {{"read", "pics", "--fetch-remote"}, {"read", "kinds"}, {"read", "unverified"}, {"read", fixtures.InlineCID}, {"read", thread[len(thread)-1].ID}},
		"thread":      {{"thread", thread[0].ID}, {"thread", thread[0].ID, "--raw"}},
		"context":     {{"context", "seed"}, {"context", fixtures.IKEASeed}},
		"attachments": {{"attachments", "kinds"}, {"attachments", fixtures.InlineCID}, {"attachments", fixtures.RemoteOnly}},
		"fetch":       {{"fetch", "files", "--all"}},
		"auth status": {{"auth", "status"}},
	} {
		page := mustOK(t, w.find(append(strings.Fields(cmd), "--help")...)).stdout
		_, section, ok := strings.Cut(page, "\nJSON:\n")
		if !ok {
			t.Errorf("%s --help has no JSON section", cmd)
			continue
		}
		section, _, _ = strings.Cut(section, "\n\n")
		for _, args := range runs {
			var v any
			if err := json.Unmarshal([]byte(mustOK(t, w.find(args...)).stdout), &v); err != nil {
				t.Fatalf("%v: %v", args, err)
			}
			keys := keysIn(v)
			slices.Sort(keys)
			for _, key := range slices.Compact(keys) {
				if !regexp.MustCompile(`\b` + key + `\b`).MatchString(section) {
					t.Errorf("%s --help's JSON section does not name %q (from %v)", cmd, key, args)
				}
			}
		}
	}
}

func keysIn(v any) []string {
	var out []string
	switch v := v.(type) {
	case map[string]any:
		for k, e := range v {
			out = append(out, k)
			out = append(out, keysIn(e)...)
		}
	case []any:
		for _, e := range v {
			out = append(out, keysIn(e)...)
		}
	}
	return out
}
