// send-mail: compose mail as an agent; send only what a human previewed.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/alecthomas/kong"
	"github.com/qiushiyan/clikit"

	"github.com/qiushiyan/mailkit/internal/drafts"
	"github.com/qiushiyan/mailkit/internal/mail"
	"github.com/qiushiyan/mailkit/internal/paths"
	"github.com/qiushiyan/mailkit/internal/render"
	"github.com/qiushiyan/mailkit/internal/wiring"
)

type CLI struct {
	Compose ComposeCmd `cmd:"" help:"Write a draft of a new message and a preview of its exact bytes. Nothing is sent."`
	Reply   ReplyCmd   `cmd:"" help:"Write a draft that answers a message, in its thread, and a preview. Nothing is sent."`
	Commit  CommitCmd  `cmd:"" help:"Send a previewed draft, exactly as previewed, once."`
	List    ListCmd    `cmd:"" help:"List the 30 most recent drafts, newest first, with their state."`
}

// The seams a test replaces: the clock, and what opens a preview page.
type (
	clock     func() time.Time
	previewer func(path string) error
)

var app = clikit.App{
	Name:        "send-mail",
	Description: "send-mail writes a mail draft and a preview of it; only commit sends, and only the previewed bytes.",
	New:         func() any { return &CLI{} },
	Options: []kong.Option{
		kong.Vars{"accounts": strings.Join(wiring.Accounts, ",")},
		kong.BindTo(context.Background(), (*context.Context)(nil)),
		kong.Bind(clock(time.Now)),
		kong.Bind(previewer(func(path string) error { return exec.Command("open", path).Start() })),
		kong.BindSingletonProvider(func() (paths.Dirs, error) { return paths.Default(), nil }),
		kong.BindToProvider(func(d paths.Dirs) (mail.Opener, error) { return wiring.Opener(d), nil }),
		kong.BindToProvider(func(d paths.Dirs) (drafts.Store, error) { return drafts.Store{Dir: d.Drafts()}, nil }),
	},
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	a := app
	a.Options = append(slices.Clone(app.Options), kong.BindTo(ctx, (*context.Context)(nil)))
	code := a.Run(os.Args[1:])
	stop()
	os.Exit(code)
}

// DraftFlags are what a new message and a reply both take.
type DraftFlags struct {
	Cc       []string `placeholder:"ADDRESS" help:"Also send a copy to this address."`
	Bcc      []string `placeholder:"ADDRESS" help:"Send a blind copy to this address; the other recipients do not see it."`
	Body     string   `xor:"body" required:"" help:"The body, inline. A body that starts with - must be written --body=TEXT; for anything long, or with backticks or $, prefer --body-file."`
	BodyFile string   `xor:"body" required:"" placeholder:"FILE" help:"Read the body from this file, or from stdin when it is -."`
	Format   string   `enum:"markdown,text,html" default:"markdown" help:"How to read the body: markdown compiles to a plain-text and an HTML part that say the same thing; text sends the bytes verbatim; html sends finished HTML as it is."`
	Attach   []string `sep:"none" placeholder:"FILE" help:"Attach this file."`
	Sender   string   `placeholder:"ADDRESS" help:"Send as this alias or send-as address of the account instead of its own address."`
	NoOpen   bool     `help:"Write the preview without opening it in the browser, to show it to the person some other way."`
	Account  string   `enum:"${accounts}" default:"gmail" help:"Which account to send from."`
}

// write drafts req with the body and options f carries, opens its
// preview, and prints what the author must check and the commit command.
func (f *DraftFlags) write(w io.Writer, ctx context.Context, store drafts.Store, open mail.Opener, now clock, preview previewer, stdin io.Reader, req drafts.Request) error {
	text, err := readBody(f.BodyFile, f.Body, stdin)
	if err != nil {
		return err
	}
	switch f.Format {
	case "html":
		req.Body = drafts.HTMLBody(text)
	case "text":
		req.Body = drafts.PlainBody(text)
	default:
		req.Body = drafts.MarkdownBody(text)
	}
	req.Account, req.Sender, req.Cc, req.Bcc, req.Attach = f.Account, f.Sender, addresses(f.Cc), addresses(f.Bcc), f.Attach
	rec, err := store.Draft(ctx, open, req, now())
	if err != nil {
		return err
	}
	if !f.NoOpen && preview != nil {
		_ = preview(rec.Preview)
	}
	fmt.Fprintf(w, "draft   %s\naccount %s (%s)\n", rec.ID, rec.Account, rec.From)
	if o := rec.InReplyTo; o != nil {
		// The recipients were chosen here, not typed: say who they are.
		fmt.Fprintf(w, "answers %s, %q, %s\nto      %s\n", o.From, o.Subject, o.Date, strings.Join(rec.To, ", "))
		if len(rec.Cc) > 0 {
			fmt.Fprintf(w, "cc      %s\n", strings.Join(rec.Cc, ", "))
		}
	}
	fmt.Fprintf(w, "preview %s\nsize    %s\n", rec.Preview, render.Size(rec.Size))
	if rec.Size > rec.SendLimit {
		fmt.Fprintf(w, "warning: %s is over the %s limit for %s -- the send will be refused\n", render.Size(rec.Size), render.Size(rec.SendLimit), rec.Account)
	}
	fmt.Fprintf(w, "\nnothing has been sent. to send exactly this draft:\n    send-mail commit %s\n", rec.ID)
	return nil
}

// --- compose ---------------------------------------------------------------

type ComposeCmd struct {
	To      []string `short:"t" required:"" placeholder:"ADDRESS" help:"Send to this address."`
	Subject string   `short:"s" required:"" help:"The subject line."`
	DraftFlags
}

func (c *ComposeCmd) Run(k *kong.Context, ctx context.Context, store drafts.Store, open mail.Opener, now clock, preview previewer, stdin io.Reader) error {
	if strings.TrimSpace(c.Subject) == "" {
		return clikit.Usage(errors.New("--subject is empty; a message needs a subject"))
	}
	return c.write(k.Stdout, ctx, store, open, now, preview, stdin, drafts.Request{To: addresses(c.To), Subject: c.Subject})
}

// --- reply -----------------------------------------------------------------

type ReplyCmd struct {
	ID  string `arg:"" help:"The id of the message to answer, as mail-find prints it, from the same --account."`
	All bool   `help:"Reply to everyone on the original except you, not only to its sender."`
	DraftFlags
}

func (c *ReplyCmd) Run(k *kong.Context, ctx context.Context, store drafts.Store, open mail.Opener, now clock, preview previewer, stdin io.Reader) error {
	return c.write(k.Stdout, ctx, store, open, now, preview, stdin, drafts.Request{Reply: c.ID, ReplyAll: c.All})
}

// --- commit ----------------------------------------------------------------

type CommitCmd struct {
	DraftID string `arg:"" help:"The draft id that compose or reply printed."`
}

func (c *CommitCmd) Run(k *kong.Context, ctx context.Context, store drafts.Store, open mail.Opener, now clock) error {
	rec, err := store.Commit(ctx, c.DraftID, open, now())
	if err != nil {
		return err
	}
	w := k.Stdout
	fmt.Fprintf(w, "sent as %s -> %s\nsubject: %s\n", rec.SentAs, strings.Join(rec.To, ", "), rec.Subject)
	parent := rec.Parent()
	if parent == nil {
		return nil
	}
	// Report the thread only as far as the provider said where the message
	// went; a mismatch is the failure replies exist to prevent, so it is
	// named, with the command that shows it.
	check := fmt.Sprintf("mail-find thread %s --account %s", parent.ID, rec.Account)
	switch rec.Sent.ConversationID {
	case parent.ConversationID:
		fmt.Fprintf(w, "in reply to %s, in its thread %s\n", rec.InReplyTo.From, parent.ConversationID)
	case "":
		fmt.Fprintf(w, "in reply to %s; %s does not report the thread it used. To check:\n    %s\n", rec.InReplyTo.From, rec.Account, check)
	default:
		fmt.Fprintf(w, "warning: %s put the reply in conversation %s, not the original's %s. To see the original's thread:\n    %s\n", rec.Account, rec.Sent.ConversationID, parent.ConversationID, check)
	}
	return nil
}

// --- list ------------------------------------------------------------------

type ListCmd struct{}

func (c *ListCmd) Run(k *kong.Context, store drafts.Store) error {
	rows, err := store.Recent(30)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Fprintf(k.Stdout, "no drafts\n")
		return nil
	}
	for _, r := range rows {
		state := string(r.State)
		if r.State == drafts.Sent {
			state = "sent " + r.SentAt.Format("2006-01-02 15:04")
		}
		fmt.Fprintf(k.Stdout, "%-56s %-8s %-22s -> %s\n", r.ID, r.Account, state, strings.Join(r.To, ", "))
	}
	return nil
}

// addresses trims what the shell left around each comma-separated address.
func addresses(vals []string) []string {
	var out []string
	for _, v := range vals {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func readBody(file, inline string, stdin io.Reader) (string, error) {
	switch {
	case file == "-":
		b, err := io.ReadAll(stdin)
		return string(b), err
	case file != "":
		if after, ok := strings.CutPrefix(file, "~/"); ok {
			home, _ := os.UserHomeDir()
			file = filepath.Join(home, after)
		}
		b, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("body file not found: %s", file)
		}
		return string(b), nil
	case inline != "":
		return inline, nil
	}
	return "", errors.New("give a body with --body or --body-file (use - for stdin)")
}
