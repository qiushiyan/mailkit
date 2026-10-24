// mail-find: read-only access to mail history, for an agent to consume.
//
// Read and send are separate binaries on purpose: wanting an agent to look
// something up in my mail should not also hand it the ability to send as me.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/alecthomas/kong"
	"github.com/qiushiyan/clikit"

	"github.com/qiushiyan/mailkit/internal/attachments"
	"github.com/qiushiyan/mailkit/internal/cluster"
	"github.com/qiushiyan/mailkit/internal/images"
	"github.com/qiushiyan/mailkit/internal/mail"
	"github.com/qiushiyan/mailkit/internal/paths"
	"github.com/qiushiyan/mailkit/internal/render"
	"github.com/qiushiyan/mailkit/internal/wiring"
)

type CLI struct {
	Account string `enum:"${accounts}" default:"gmail" help:"Which account's mailbox to read. An id from one account means nothing in the other."`
	Text    bool   `help:"Print the human form instead of one JSON document."`

	Search      SearchCmd      `cmd:"" help:"Find messages by sender, recipient, subject, date or phrase."`
	Resolve     ResolveCmd     `cmd:"" help:"Turn a Message-ID, or a message:// link from a mail client, into the message's id."`
	Read        ReadCmd        `cmd:"" help:"Print one message: headers, the body with verified quoted history folded, and its attachments."`
	Thread      ThreadCmd      `cmd:"" help:"Print the conversation a message belongs to as one transcript, each turn saying only what it added."`
	Context     ContextCmd     `cmd:"" help:"Find messages about the same real-world thing across threads and senders; each hit says why."`
	Attachments AttachmentsCmd `cmd:"" help:"List a message's attachments and the pictures its body only links to."`
	Fetch       FetchCmd       `cmd:"" help:"Download a message's attachments and print where each landed."`
	Auth        AuthCmd        `cmd:"" help:"Sign in, or check which address an account is signed in as."`
}

// The seams a test replaces: the clock, and the interactive sign-in.
type (
	clock func() time.Time
	login func(ctx context.Context, account string, out func(string)) error
)

// nativeSearcher is an adapter that can pass a query to its provider
// untouched; Gmail is one.
type nativeSearcher interface {
	NativeSearch(ctx context.Context, q string, limit int) ([]mail.Envelope, error)
}

var app = clikit.App{
	Name:        "mail-find",
	Description: "mail-find searches, reads and downloads from mail history. Nothing here sends or changes mail.",
	New:         func() any { return &CLI{} },
	Options: []kong.Option{
		kong.Vars{
			"accounts":      strings.Join(wiring.Accounts, ","),
			"limit":         strconv.Itoa(mail.DefaultLimit),
			"window":        strconv.Itoa(cluster.DefaultWindow),
			"context_limit": strconv.Itoa(cluster.DefaultLimit),
			"min_score":     strconv.Itoa(cluster.DefaultMinScore),
		},
		kong.BindTo(context.Background(), (*context.Context)(nil)),
		kong.Bind(clock(time.Now)),
		kong.Bind(images.Default),
		kong.BindSingletonProvider(func() (paths.Dirs, error) { return paths.Default(), nil }),
		kong.BindToProvider(func(d paths.Dirs) (mail.Opener, error) { return wiring.Opener(d), nil }),
		kong.BindToProvider(func(d paths.Dirs) (login, error) { return login(wiring.Login(d)), nil }),
		// No credential check is made here: the first real operation fails
		// with ErrAuth and the login command if it must, and a working
		// account pays nothing extra.
		kong.BindToProvider(func(ctx context.Context, cli *CLI, open mail.Opener) (mail.Mailbox, error) {
			return open(ctx, cli.Account)
		}),
		kong.BindToProvider(func(k *kong.Context, cli *CLI) (output, error) { return output{k.Stdout, cli.Text}, nil }),
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

// --- search ----------------------------------------------------------------

type SearchCmd struct {
	Query  string `arg:"" help:"The query in the portable grammar below, as one shell argument: quote it."`
	Limit  int    `default:"${limit}" help:"Stop after this many hits."`
	Native bool   `help:"Pass QUERY to the provider untouched, in its own syntax; the hits are not portable to the other account."`
}

func (c *SearchCmd) Run(ctx context.Context, cli *CLI, box mail.Mailbox, now clock, out output) error {
	var hits []mail.Envelope
	var err error
	if c.Native {
		n, ok := box.(nativeSearcher)
		if !ok {
			return fmt.Errorf("--native is not supported on %s", cli.Account)
		}
		hits, err = n.NativeSearch(ctx, c.Query, c.Limit)
	} else {
		q, perr := mail.ParseQuery(c.Query, now())
		if perr != nil {
			return perr
		}
		hits, err = box.Search(ctx, q, c.Limit)
	}
	if err != nil {
		return err
	}
	var steps []string
	if len(hits) == 0 && !c.Native {
		steps = append(steps, "0 hits. Widen one bound at a time: drop the date, use the sender's domain instead of the address, or use a shorter phrase. Provider syntax goes through untouched with: mail-find search QUERY --native")
	}
	if !out.text {
		rows := make([]envelopeOut, 0, len(hits))
		for _, h := range hits {
			rows = append(rows, envOut(h))
		}
		v := map[string]any{"hits": rows, "next_steps": orEmpty(steps)}
		if c.Native {
			v["native"] = cli.Account + " syntax, not portable"
		}
		return out.json(v)
	}
	if len(hits) == 0 {
		out.printf("no matches\n")
		for _, s := range steps {
			out.printf("\n%s\n", s)
		}
		return nil
	}
	for _, h := range hits {
		out.printf("%s  %s\n  from: %s\n  subj: %s\n", h.ID, h.DateHeader, h.From, h.Subject)
		if h.Snippet != "" {
			out.printf("  %s\n", truncate(h.Snippet, 120))
		}
		out.printf("\n")
	}
	return nil
}

// --- resolve ---------------------------------------------------------------

type ResolveCmd struct {
	Ref string `arg:"" help:"A Message-ID header value, with or without its angle brackets, or a message:// link."`
}

// Run parses the reference before opening the account, so a malformed one
// is reported as such whatever state the credentials are in.
func (c *ResolveCmd) Run(ctx context.Context, cli *CLI, open mail.Opener, out output) error {
	ref, err := mail.ParseRef(c.Ref)
	if err != nil {
		return err
	}
	box, err := open(ctx, cli.Account)
	if err != nil {
		return err
	}
	e, err := box.Resolve(ctx, ref)
	if err != nil {
		if errors.Is(err, mail.ErrNotFound) {
			return fmt.Errorf("no message in this mailbox has Message-ID %s. Check it came from this account, and that it is the Message-ID header rather than a thread or conversation id", ref)
		}
		return err
	}
	if !out.text {
		return out.json(envOut(e))
	}
	out.printf("%s  %s\n  from: %s\n  subj: %s\n\nRead it:  mail-find read %s\n", e.ID, e.DateHeader, e.From, e.Subject, e.ID)
	return nil
}

// --- read ------------------------------------------------------------------

type ReadCmd struct {
	ID          string `arg:"" help:"The message's id: the \"id\" field that search, resolve or thread prints."`
	FetchRemote bool   `help:"Also download the pictures the body only links to, each labelled by size. Fetching tells the sender the mail was opened."`
	Out         string `type:"path" placeholder:"DIR" help:"Directory for the downloaded pictures, in place of the message's state directory."`
	Raw         bool   `help:"Print the body as converted, quoted history included: for when a fold looks wrong, or the exact words are wanted."`
}

func (c *ReadCmd) Run(ctx context.Context, box mail.Mailbox, dirs paths.Dirs, fetcher *images.Fetcher, out output) error {
	msg, err := box.Fetch(ctx, c.ID)
	if err != nil {
		return err
	}
	rendered := render.Convert(msg.Body)
	r := readOut{envelopeOut: envOut(msg.Envelope), Body: rendered.Text, Attachments: partsOut(msg.Parts), NextSteps: []string{}}

	// The conversation is what a fold is verified against and what the
	// thread pointer counts; a message that has none, or whose
	// conversation cannot be loaded, is read alone.
	conv, _ := box.Conversation(ctx, msg.ConversationID)
	if !c.Raw {
		r.Body, r.Fold = render.Read(msg, conv)
	}

	if c.FetchRemote && len(rendered.RemoteImages) > 0 {
		dir := c.Out
		if dir == "" {
			dir = dirs.Attachments(msg.ID) + "/remote"
		}
		r.RemoteImages = fetcher.FetchAll(ctx, rendered.RemoteImages, dir)
	}

	// Nudges: each fires only when true, as a runnable command.
	if !c.FetchRemote && len(rendered.RemoteImages) > 0 {
		r.NextSteps = append(r.NextSteps, fmt.Sprintf("%d image(s) referenced by URL, not fetched. To include them (fetching tells the sender the mail was opened): mail-find read %s --fetch-remote", len(rendered.RemoteImages), msg.ID))
	}
	if len(conv) > 1 {
		r.NextSteps = append(r.NextSteps, fmt.Sprintf("1 of %d messages in this conversation. For the whole exchange: mail-find thread %s", len(conv), msg.ID))
	}
	for _, p := range msg.Parts {
		if e, ok := p.Content.(mail.EmbeddedPart); ok && e.Item != nil {
			where := "attachments[].embedded"
			if out.text {
				where = "printed below its attachment line"
			}
			r.NextSteps = append(r.NextSteps, fmt.Sprintf("attachment %q is an embedded message from %s: %q -- its text is %s, not in the body", p.Name, e.Item.From, e.Item.Subject, where))
		}
	}

	if !out.text {
		return out.json(r)
	}
	out.read(r)
	return nil
}

// --- thread ----------------------------------------------------------------

type ThreadCmd struct {
	ID  string `arg:"" help:"The id of any message in the conversation."`
	Raw bool   `help:"Print every message whole, its quoted history included, instead of the transcript."`
}

func (c *ThreadCmd) Run(ctx context.Context, box mail.Mailbox, out output) error {
	msg, err := box.Fetch(ctx, c.ID)
	if err != nil {
		return err
	}
	msgs, err := box.Conversation(ctx, msg.ConversationID)
	if err != nil {
		return err
	}
	if c.Raw {
		rows := make([]rawMsg, 0, len(msgs))
		for _, m := range msgs {
			rows = append(rows, rawMsg{envOut(m.Envelope), render.Text(m.Body), partsOut(m.Parts)})
		}
		if !out.text {
			return out.json(map[string]any{"messages": rows})
		}
		for _, r := range rows {
			marker := " "
			if r.ID == c.ID {
				marker = ">"
			}
			out.printf("%s %s  %s\n    from: %s\n    subj: %s\n\n%s\n\n", marker, r.ID, r.Date, r.From, r.Subject, r.Body)
		}
		return nil
	}
	t := render.Build(msgs)
	if !out.text {
		return out.json(t)
	}
	out.printf("%s\n\n--raw for every message exactly as sent, quotes and all\n", render.Render(t))
	return nil
}

// --- context ---------------------------------------------------------------

type ContextCmd struct {
	ID       string `arg:"" help:"The id of the message to start from: the seed."`
	Window   int    `default:"${window}" help:"Days either side of the seed that the weak signals, sender domain and subject words, reach."`
	Limit    int    `default:"${context_limit}" help:"Stop after this many related messages."`
	MinScore int    `default:"${min_score}" help:"Hold back hits that score below this; 1 admits a single weak signal."`
	Raw      bool   `help:"Keep every hit: the weak ones, and those of an identifier too common to be distinctive."`
}

func (c *ContextCmd) Run(ctx context.Context, box mail.Mailbox, out output) error {
	r, err := cluster.Build(ctx, box, c.ID, cluster.Options{WindowDays: c.Window, Limit: c.Limit, MinScore: c.MinScore, Raw: c.Raw})
	if err != nil {
		return err
	}
	var steps []string
	if r.BelowMinScore > 0 {
		steps = append(steps, fmt.Sprintf("%d weaker match(es) held back. To see them: mail-find context %s --min-score 1", r.BelowMinScore, c.ID))
	}
	if len(r.DroppedAsTooCommon) > 0 {
		steps = append(steps, fmt.Sprintf("an identifier shared by too many messages was dropped as not distinctive. To include its hits anyway: mail-find context %s --raw", c.ID))
	}
	if !out.text {
		return out.json(struct {
			cluster.Result
			NextSteps []string `json:"next_steps"`
		}{r, orEmpty(steps)})
	}
	out.context(r)
	return nil
}

// --- attachments -----------------------------------------------------------

type AttachmentsCmd struct {
	ID string `arg:"" help:"The message's id."`
}

func (c *AttachmentsCmd) Run(ctx context.Context, box mail.Mailbox, out output) error {
	msg, err := box.Fetch(ctx, c.ID)
	if err != nil {
		return err
	}
	remote := render.Convert(msg.Body).RemoteImages
	steps := []string{}
	if len(remote) > 0 {
		steps = append(steps, fmt.Sprintf("%d image(s) referenced by URL in the body. To fetch them: mail-find read %s --fetch-remote", len(remote), msg.ID))
	}
	if !out.text {
		return out.json(map[string]any{"attachments": partsOut(msg.Parts), "remote_image_urls": remote, "next_steps": steps})
	}
	if len(msg.Parts) == 0 {
		out.printf("no file attachments\n")
	}
	for _, p := range partsOut(msg.Parts) {
		tag := ""
		if p.Inline {
			tag = "  [inline]"
		}
		if p.Kind != "stored" {
			tag += "  [" + p.Kind + ": no bytes in the message]"
		}
		out.printf("%s  %s  %s%s\n", p.Name, render.Size(p.Size), p.MIME, tag)
	}
	for _, s := range steps {
		out.printf("\n%s\n", s)
	}
	return nil
}

// --- fetch -----------------------------------------------------------------

type FetchCmd struct {
	ID         string   `arg:"" help:"The message's id."`
	Attachment []string `xor:"pick" required:"" sep:"none" placeholder:"NAME" help:"Download the attachment with this name or handle. A name two attachments share needs its handle."`
	All        bool     `xor:"pick" required:"" help:"Download every attachment that has bytes in the message."`
	Out        string   `type:"path" placeholder:"DIR" help:"Directory to write into, in place of the message's state directory."`
}

func (c *FetchCmd) Run(ctx context.Context, box mail.Mailbox, dirs paths.Dirs, out output) error {
	msg, err := box.Fetch(ctx, c.ID)
	if err != nil {
		return err
	}
	wanted, err := attachments.Select(msg, c.Attachment)
	if err != nil {
		return err
	}
	dir := c.Out
	if dir == "" {
		dir = dirs.Attachments(msg.ID)
	}
	saved, err := attachments.Save(ctx, box, dir, wanted)
	if err != nil {
		return err
	}
	if !out.text {
		return out.json(map[string]any{"saved": saved})
	}
	for _, s := range saved {
		out.printf("%s  (%s)\n", s.Path, render.Size(s.Size))
	}
	return nil
}

// --- auth ------------------------------------------------------------------

type AuthCmd struct {
	Login  LoginCmd  `cmd:"" help:"Sign in to --account interactively: a browser for Gmail, a device code for Outlook."`
	Status StatusCmd `cmd:"" help:"Print the address --account is signed in as, and the largest message it can send."`
}

type LoginCmd struct{}

func (c *LoginCmd) Run(ctx context.Context, cli *CLI, signIn login, out output) error {
	return signIn(ctx, cli.Account, func(s string) { out.printf("%s\n", s) })
}

type StatusCmd struct{}

func (c *StatusCmd) Run(ctx context.Context, cli *CLI, box mail.Mailbox, out output) error {
	acct, err := box.Account(ctx)
	if err != nil {
		return err
	}
	if !out.text {
		return out.json(map[string]any{"account": cli.Account, "address": acct.Address, "send_limit": acct.SendLimit})
	}
	out.printf("%s: %s (send limit %s)\n", cli.Account, acct.Address, render.Size(acct.SendLimit))
	return nil
}
