package main

import (
	"context"
	"slices"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/qiushiyan/clikit/clikittest"

	"github.com/qiushiyan/mailkit/internal/mail"
	"github.com/qiushiyan/mailkit/internal/pintest"
)

// Every help page, pinned; make golden accepts an intended change. Help
// must never reach an account: the opener here fails the test if it is
// called.
func TestHelpPages(t *testing.T) {
	h := newHarness(t)
	h.open = func(context.Context, string) (mail.Mailbox, error) {
		t.Error("a help page opened an account")
		return nil, context.Canceled
	}
	clikittest.Golden(t, h.app(), "testdata/help")
}

// Every send-mail command line an agent is handed -- compose's commit line,
// a help page's examples, the skills' instructions -- parses against the
// grammar, so a renamed flag cannot leave an instruction that fails when it
// is run.
func TestPrintedCommandsParse(t *testing.T) {
	lines := pintest.Commands(t, "send-mail", "testdata/contract", "../mail-find/testdata/contract", "testdata/help", "../mail-find/testdata/help")
	lines = append(lines, pintest.Fenced(t, "send-mail", "../../skills/read-email/SKILL.md", "../../skills/write-email/SKILL.md")...)
	if len(lines) < 5 {
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
