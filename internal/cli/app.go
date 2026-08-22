// Package cli is the two command trees, built over an injected Mailbox so
// tests drive the real commands against the memory adapter.
//
// Two standing rules from output-design.md: the processing layer is on by
// default, and --raw bypasses it on every command that has one. And every
// result that changes what should happen next says so as a runnable command.
package cli

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/qiushiyan/mailkit/internal/attachments"
	"github.com/qiushiyan/mailkit/internal/cluster"
	"github.com/qiushiyan/mailkit/internal/drafts"
	"github.com/qiushiyan/mailkit/internal/images"
	"github.com/qiushiyan/mailkit/internal/mail"
	"github.com/qiushiyan/mailkit/internal/render"
)

// Opener returns the Mailbox for an account name. The composition root
// wires the real adapters; tests wire memory.
type Opener func(ctx context.Context, account string) (mail.Mailbox, error)

// Login runs a provider's interactive sign-in.
type Login func(ctx context.Context, account string, out func(string)) error

// Deps is everything the commands reach outside themselves.
type Deps struct {
	Open   Opener
	Login  Login
	Images *images.Fetcher
	Drafts drafts.Store
	// AttachmentDir overrides where downloads land (tests).
	AttachmentDir func(messageID string) string
	Now           func() time.Time
	// OpenPreview opens the draft preview page; nil means do not.
	OpenPreview func(path string) error
	Accounts    []string
}

func (d *Deps) defaults() {
	if d.Images == nil {
		d.Images = images.Default
	}
	if d.AttachmentDir == nil {
		d.AttachmentDir = attachments.DefaultDir
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if len(d.Accounts) == 0 {
		d.Accounts = []string{"gmail", "outlook"}
	}
}

type app struct {
	Deps
	out, errOut io.Writer
	account     string
	text        bool
}

func (a *app) emit(v any) error {
	b, err := json.Marshal(v, json.Deterministic(true))
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(a.out, string(b))
	return err
}

func (a *app) printf(format string, args ...any) { fmt.Fprintf(a.out, format, args...) }

// mailbox opens the account. No credential check is made here: the first
// real operation fails with ErrAuth and the login hint if it must, and a
// working account pays nothing extra.
func (a *app) mailbox(ctx context.Context) (mail.Mailbox, error) {
	return a.Open(ctx, a.account)
}

// MailFind builds the read-only command tree.
func MailFind(d Deps, out, errOut io.Writer) *cobra.Command {
	d.defaults()
	a := &app{Deps: d, out: out, errOut: errOut}
	root := &cobra.Command{
		Use:           "mail-find",
		Short:         "Search, read and download from mail history (read-only)",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetOut(out)
	root.SetErr(errOut)
	// Persistent, so they are accepted on either side of the subcommand --
	// `mail-find search q --text` is what anyone writes first.
	root.PersistentFlags().StringVar(&a.account, "account", "gmail", "which account: "+strings.Join(d.Accounts, "|"))
	root.PersistentFlags().BoolVar(&a.text, "text", false, "human-readable output instead of JSON")
	root.PersistentFlags().Bool("json", true, "JSON output (default)")

	root.AddCommand(a.searchCmd(), a.readCmd(), a.resolveCmd(), a.threadCmd(), a.contextCmd(),
		a.attachmentsCmd(), a.fetchCmd(), a.authCmd())
	return root
}

// Run executes a command tree and maps errors to the exit convention.
func Run(cmd *cobra.Command, args []string, errOut io.Writer) int {
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		fmt.Fprintf(errOut, "%s: %v\n", cmd.Name(), err)
		return 1
	}
	return 0
}

// --- search ----------------------------------------------------------------

func (a *app) searchCmd() *cobra.Command {
	var limit int
	var native bool
	c := &cobra.Command{
		Use:   "search QUERY",
		Short: "search mail history in the portable grammar",
		Long:  "Portable grammar, same meaning on every account:\n" + mail.Grammar,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			box, err := a.mailbox(ctx)
			if err != nil {
				return err
			}
			var hits []mail.Envelope
			if native {
				n, ok := box.(interface {
					NativeSearch(context.Context, string, int) ([]mail.Envelope, error)
				})
				if !ok {
					return fmt.Errorf("--native is not supported on %s", a.account)
				}
				hits, err = n.NativeSearch(ctx, args[0], limit)
			} else {
				c, perr := mail.ParseQuery(args[0], a.Now())
				if perr != nil {
					return perr
				}
				hits, err = box.Search(ctx, c, limit)
			}
			if err != nil {
				return err
			}
			if !a.text {
				rows := make([]envelopeOut, 0, len(hits))
				for _, h := range hits {
					rows = append(rows, envOut(h))
				}
				out := map[string]any{"hits": rows}
				if native {
					out["native"] = a.account + " syntax, not portable"
				}
				return a.emit(out)
			}
			if len(hits) == 0 {
				a.printf("no matches\n")
				return nil
			}
			for _, h := range hits {
				a.printf("%s  %s\n  from: %s\n  subj: %s\n", h.ID, h.DateHeader, h.From, h.Subject)
				if h.Snippet != "" {
					a.printf("  %s\n", truncate(h.Snippet, 120))
				}
				a.printf("\n")
			}
			return nil
		},
	}
	c.Flags().IntVar(&limit, "limit", 25, "maximum hits")
	c.Flags().BoolVar(&native, "native", false, "pass QUERY to the provider untouched (provider-specific syntax)")
	return c
}

type envelopeOut struct {
	ID             string `json:"id"`
	ConversationID string `json:"conversation_id"`
	MessageID      string `json:"message_id,omitempty"`
	Date           string `json:"date"`
	From           string `json:"from"`
	To             string `json:"to,omitempty"`
	Cc             string `json:"cc,omitempty"`
	Subject        string `json:"subject"`
	Snippet        string `json:"snippet,omitempty"`
}

func envOut(e mail.Envelope) envelopeOut {
	return envelopeOut{ID: e.ID, ConversationID: e.ConversationID, MessageID: string(e.MessageID),
		Date: e.DateHeader, From: e.From.String(), To: mail.Joined(e.To), Cc: mail.Joined(e.Cc),
		Subject: e.Subject, Snippet: e.Snippet}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// --- read ------------------------------------------------------------------

type partOut struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Handle    string `json:"handle,omitempty"`
	URL       string `json:"url,omitempty"`
	Size      int64  `json:"size"`
	MIME      string `json:"mime_type"`
	Inline    bool   `json:"inline"`
	ContentID string `json:"content_id,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	// Embedded is the attached message itself, for an embedded_message
	// part: this output is the caller's only copy of its text.
	Embedded *embeddedOut `json:"embedded,omitempty"`
}

type embeddedOut struct {
	From        string    `json:"from"`
	To          string    `json:"to,omitempty"`
	Date        string    `json:"date"`
	Subject     string    `json:"subject"`
	Body        string    `json:"body"`
	Attachments []partOut `json:"attachments"`
}

func partsOut(parts []mail.Part) []partOut {
	out := make([]partOut, 0, len(parts))
	for _, p := range parts {
		o := partOut{Name: p.Name, Kind: p.Kind(), Size: p.Size, MIME: p.MIME, Inline: p.Inline, ContentID: p.ContentID}
		switch c := p.Content.(type) {
		case mail.StoredPart:
			o.Handle = string(c.Handle)
		case mail.LinkedPart:
			o.URL = c.URL
		case mail.EmbeddedPart:
			o.Truncated = c.Truncated
			if c.Item != nil {
				o.Embedded = &embeddedOut{
					From: c.Item.From.String(), To: mail.Joined(c.Item.To), Date: c.Item.DateHeader, Subject: c.Item.Subject,
					Body: render.Text(c.Item.Body), Attachments: partsOut(c.Item.Parts),
				}
			}
		}
		out = append(out, o)
	}
	return out
}

type readOut struct {
	envelopeOut
	Body string `json:"body"`
	render.Fold
	Attachments  []partOut        `json:"attachments"`
	RemoteImages []images.Fetched `json:"remote_images,omitempty"`
	NextSteps    []string         `json:"next_steps"`
}

func (a *app) readCmd() *cobra.Command {
	var fetchRemote, raw bool
	var outDir string
	c := &cobra.Command{
		Use:   "read MESSAGE_ID",
		Short: "full message: headers, body, attachment manifest",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			box, err := a.mailbox(ctx)
			if err != nil {
				return err
			}
			msg, err := box.Fetch(ctx, args[0])
			if err != nil {
				return err
			}
			rendered := render.Convert(msg.Body)
			out := readOut{envelopeOut: envOut(msg.Envelope), Body: rendered.Text, Attachments: partsOut(msg.Parts), NextSteps: []string{}}

			// The conversation is what a fold is verified against and what
			// the thread pointer counts; a message that has none, or whose
			// conversation cannot be loaded, is read alone.
			conv, _ := box.Conversation(ctx, msg.ConversationID)
			if !raw {
				out.Body, out.Fold = render.Read(msg, conv)
			}

			if fetchRemote && len(rendered.RemoteImages) > 0 {
				dir := outDir
				if dir == "" {
					dir = a.AttachmentDir(msg.ID) + "/remote"
				}
				out.RemoteImages = a.Images.FetchAll(ctx, rendered.RemoteImages, dir)
			}

			// Nudges: each fires only when true, as a runnable command.
			if !fetchRemote && len(rendered.RemoteImages) > 0 {
				out.NextSteps = append(out.NextSteps, fmt.Sprintf("%d image(s) referenced by URL, not fetched. To include them: mail-find read %s --fetch-remote", len(rendered.RemoteImages), msg.ID))
			}
			if len(conv) > 1 {
				out.NextSteps = append(out.NextSteps, fmt.Sprintf("1 of %d messages in this conversation. For the whole exchange: mail-find thread %s", len(conv), msg.ID))
			}
			for _, p := range msg.Parts {
				if e, ok := p.Content.(mail.EmbeddedPart); ok && e.Item != nil {
					out.NextSteps = append(out.NextSteps, fmt.Sprintf("attachment %q is an embedded message from %s: %q -- its text is in attachments[].embedded (after the attachment line with --text); it is not quoted in the body", p.Name, e.Item.From, e.Item.Subject))
				}
			}

			if !a.text {
				return a.emit(out)
			}
			for _, kv := range [][2]string{{"Date", out.Date}, {"From", out.From}, {"To", out.To}, {"Cc", out.Cc}, {"Subject", out.Subject}} {
				if kv[1] != "" {
					a.printf("%-9s %s\n", kv[0], kv[1])
				}
			}
			for _, p := range out.Attachments {
				tag := ""
				if p.Inline {
					tag = " [inline]"
				}
				if p.Kind != "stored" {
					tag += " [" + p.Kind + "]"
				}
				a.printf("%-9s %s (%s)%s\n", "Attach", p.Name, drafts.HumanSize(p.Size), tag)
				if e := p.Embedded; e != nil {
					a.printf("\n--- embedded message: %s\n    From: %s\n    Date: %s\n    Subject: %s\n\n%s\n--- end embedded message\n", p.Name, e.From, e.Date, e.Subject, e.Body)
				}
			}
			for _, img := range out.RemoteImages {
				if img.Error != "" {
					a.printf("%-9s %s -- FAILED: %s\n", "Remote", truncate(img.URL, 70), img.Error)
					continue
				}
				note := ""
				if img.Kind != images.Image {
					note = "  <- " + string(img.Kind)
				}
				a.printf("%-9s %s  %dx%d%s\n", "Remote", img.Path, img.Width, img.Height, note)
			}
			a.printf("\n%s\n", out.Body)
			if out.QuotedChars > 0 {
				a.printf("\n[folded %d chars quoted via %s; --raw for the message as sent]\n", out.QuotedChars, out.QuoteMarker)
			}
			if out.FoldRejected != "" {
				a.printf("\n[quote %s]\n", out.FoldRejected)
			}
			for _, s := range out.NextSteps {
				a.printf("\n%s\n", s)
			}
			return nil
		},
	}
	c.Flags().BoolVar(&fetchRemote, "fetch-remote", false, "also download images the body only links to (tells the sender the mail was opened)")
	c.Flags().StringVar(&outDir, "out", "", "destination for downloaded images")
	c.Flags().BoolVar(&raw, "raw", false, "the body as converted, with no quote folding or tidying")
	return c
}

func (a *app) resolveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "resolve MESSAGE-ID",
		Short: "find a message from a Message-ID or a message:// URL dragged out of a mail client",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			ref := strings.TrimSpace(args[0])
			if strings.HasPrefix(strings.ToLower(ref), "message://") {
				decoded, err := url.PathUnescape(ref[len("message://"):])
				if err != nil {
					return fmt.Errorf("message:// URL is not decodable: %w", err)
				}
				ref = decoded
			}
			ref = strings.Trim(ref, "<> ")
			if ref == "" {
				return errors.New("nothing to resolve -- pass a Message-ID or a message:// URL")
			}
			box, err := a.mailbox(ctx)
			if err != nil {
				return err
			}
			e, err := box.Resolve(ctx, mail.MessageID(ref))
			if err != nil {
				if errors.Is(err, mail.ErrNotFound) {
					return fmt.Errorf("no message in this mailbox has Message-ID %s. Check it came from this account, and that it is the Message-ID header rather than a thread or conversation id", ref)
				}
				return err
			}
			if !a.text {
				return a.emit(envOut(e))
			}
			a.printf("%s  %s\n  from: %s\n  subj: %s\n\nRead it:  mail-find read %s\n", e.ID, e.DateHeader, e.From, e.Subject, e.ID)
			return nil
		},
	}
}

// --- thread ----------------------------------------------------------------

func (a *app) threadCmd() *cobra.Command {
	var raw bool
	c := &cobra.Command{
		Use:   "thread MESSAGE_ID",
		Short: "every message the provider grouped with this one, as one transcript",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			box, err := a.mailbox(ctx)
			if err != nil {
				return err
			}
			msg, err := box.Fetch(ctx, args[0])
			if err != nil {
				return err
			}
			msgs, err := box.Conversation(ctx, msg.ConversationID)
			if err != nil {
				return err
			}
			if raw {
				type rawMsg struct {
					envelopeOut
					Body        string    `json:"body"`
					Attachments []partOut `json:"attachments"`
				}
				rows := make([]rawMsg, 0, len(msgs))
				for _, m := range msgs {
					rows = append(rows, rawMsg{envOut(m.Envelope), render.Text(m.Body), partsOut(m.Parts)})
				}
				if !a.text {
					return a.emit(map[string]any{"messages": rows})
				}
				for _, r := range rows {
					marker := " "
					if r.ID == args[0] {
						marker = ">"
					}
					a.printf("%s %s  %s\n    from: %s\n    subj: %s\n\n%s\n\n", marker, r.ID, r.Date, r.From, r.Subject, r.Body)
				}
				return nil
			}
			t := render.Build(msgs)
			if !a.text {
				return a.emit(t)
			}
			a.printf("%s\n\n--raw for every message exactly as sent, quotes and all\n", render.Render(t))
			return nil
		},
	}
	c.Flags().BoolVar(&raw, "raw", false, "every message exactly as sent, quoted tails included")
	return c
}

// --- context ---------------------------------------------------------------

func (a *app) contextCmd() *cobra.Command {
	var window, limit, minScore int
	var raw bool
	c := &cobra.Command{
		Use:   "context MESSAGE_ID",
		Short: "messages about the same real-world thing, across threads and senders (heuristic; each hit says why)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			box, err := a.mailbox(ctx)
			if err != nil {
				return err
			}
			r, err := cluster.Build(ctx, box, args[0], cluster.Options{WindowDays: window, Limit: limit, MinScore: minScore, Raw: raw})
			if err != nil {
				return err
			}
			if !a.text {
				return a.emit(r)
			}
			a.printf("seed  %s  %s\n      %s\n", r.Seed.ID, r.Seed.Date, r.Seed.Subject)
			a.printf("signals  identifiers=%v  domain=%s  tokens=%v\n", r.Signals.Identifiers, orDash(r.Signals.Domain), r.Signals.SubjectTokens)
			for _, d := range r.DroppedAsTooCommon {
				a.printf("dropped  %s -- %s hits, not distinctive\n", d.Reason, d.Hits)
			}
			if r.BelowMinScore > 0 {
				a.printf("held back  %d weaker match(es); --min-score 1 to see them\n", r.BelowMinScore)
			}
			a.printf("\n")
			if len(r.Related) == 0 {
				a.printf("nothing related found\n")
				return nil
			}
			for _, h := range r.Related {
				a.printf("[%2d] %s  %s\n     %s\n     from: %s\n", h.Score, h.ID, h.Date, truncate(h.Subject, 70), h.From)
				for _, w := range h.Why {
					a.printf("     why : %s\n", w)
				}
				a.printf("\n")
			}
			return nil
		},
	}
	c.Flags().IntVar(&window, "window", cluster.DefaultWindow, "days either side of the seed for the weak signals")
	c.Flags().IntVar(&limit, "limit", cluster.DefaultLimit, "maximum related messages")
	c.Flags().IntVar(&minScore, "min-score", cluster.DefaultMinScore, "drop hits below this confidence (1 admits single weak signals)")
	c.Flags().BoolVar(&raw, "raw", false, "include hits filtered as weak or as matching a too-common identifier")
	return c
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// --- attachments -----------------------------------------------------------

func (a *app) attachmentsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "attachments MESSAGE_ID",
		Short: "list a message's attachments",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			box, err := a.mailbox(ctx)
			if err != nil {
				return err
			}
			msg, err := box.Fetch(ctx, args[0])
			if err != nil {
				return err
			}
			remote := render.Convert(msg.Body).RemoteImages
			steps := []string{}
			if len(remote) > 0 {
				steps = append(steps, fmt.Sprintf("%d image(s) referenced by URL in the body. To fetch them: mail-find read %s --fetch-remote", len(remote), msg.ID))
			}
			if !a.text {
				return a.emit(map[string]any{"attachments": partsOut(msg.Parts), "remote_image_urls": remote, "next_steps": steps})
			}
			if len(msg.Parts) == 0 {
				a.printf("no file attachments\n")
			}
			for _, p := range partsOut(msg.Parts) {
				tag := ""
				if p.Inline {
					tag = "  [inline]"
				}
				if p.Kind != "stored" {
					tag += "  [" + p.Kind + ": no bytes in the message]"
				}
				a.printf("%s  %s  %s%s\n", p.Name, drafts.HumanSize(p.Size), p.MIME, tag)
			}
			for _, s := range steps {
				a.printf("\n%s\n", s)
			}
			return nil
		},
	}
}

// --- fetch -----------------------------------------------------------------

func (a *app) fetchCmd() *cobra.Command {
	var names []string
	var all bool
	var outDir string
	c := &cobra.Command{
		Use:   "fetch MESSAGE_ID",
		Short: "download attachments to a local directory",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			box, err := a.mailbox(ctx)
			if err != nil {
				return err
			}
			msg, err := box.Fetch(ctx, args[0])
			if err != nil {
				return err
			}
			var stored []mail.Part
			for _, p := range msg.Parts {
				if _, ok := p.Content.(mail.StoredPart); ok {
					stored = append(stored, p)
				}
			}
			if len(stored) == 0 {
				if len(msg.Parts) > 0 {
					return fmt.Errorf("message %s has %d attachment(s) but none hold bytes here (cloud links or embedded messages); see mail-find attachments %s", msg.ID, len(msg.Parts), msg.ID)
				}
				return fmt.Errorf("message %s has no attachments", msg.ID)
			}
			var wanted []mail.Part
			switch {
			case all:
				wanted = stored
			case len(names) > 0:
				for _, token := range names {
					var matches []mail.Part
					for _, p := range stored {
						h, _ := p.Handle()
						if token == p.Name || token == string(h) {
							matches = append(matches, p)
						}
					}
					switch len(matches) {
					case 0:
						avail := make([]string, 0, len(stored))
						for _, p := range stored {
							avail = append(avail, p.Name)
						}
						return fmt.Errorf("no attachment %q on that message; has: %s", token, strings.Join(avail, ", "))
					case 1:
						wanted = append(wanted, matches[0])
					default:
						handles := make([]string, 0, len(matches))
						for _, p := range matches {
							h, _ := p.Handle()
							handles = append(handles, string(h))
						}
						return fmt.Errorf("%q names %d attachments; pick one by handle: %s", token, len(matches), strings.Join(handles, ", "))
					}
				}
			default:
				return errors.New("pick attachments with --attachment NAME (repeatable) or --all")
			}
			dir := outDir
			if dir == "" {
				dir = a.AttachmentDir(msg.ID)
			}
			names := make([]string, 0, len(wanted))
			for _, p := range wanted {
				names = append(names, p.Name)
			}
			paths, err := attachments.Allocate(dir, names)
			if err != nil {
				return err
			}
			type saved struct {
				Name string `json:"name"`
				Path string `json:"path"`
				Size int64  `json:"size"`
			}
			var out []saved
			for i, p := range wanted {
				h, _ := p.Handle()
				fh, err := os.Create(paths[i])
				if err != nil {
					return err
				}
				err = box.Open(ctx, h, fh)
				fh.Close()
				if err != nil {
					os.Remove(paths[i])
					return err
				}
				st, _ := os.Stat(paths[i])
				var size int64
				if st != nil {
					size = st.Size()
				}
				out = append(out, saved{p.Name, paths[i], size})
			}
			if !a.text {
				return a.emit(map[string]any{"saved": out})
			}
			for _, s := range out {
				a.printf("%s  (%s)\n", s.Path, drafts.HumanSize(s.Size))
			}
			return nil
		},
	}
	c.Flags().StringArrayVar(&names, "attachment", nil, "attachment name or handle (repeatable)")
	c.Flags().BoolVar(&all, "all", false, "download every attachment")
	c.Flags().StringVar(&outDir, "out", "", "destination directory")
	return c
}

// --- auth ------------------------------------------------------------------

func (a *app) authCmd() *cobra.Command {
	auth := &cobra.Command{Use: "auth", Short: "sign in and check credentials"}
	auth.AddCommand(&cobra.Command{
		Use:   "login",
		Short: "interactive sign-in for --account",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if a.Login == nil {
				return errors.New("login is not available here")
			}
			return a.Login(cmd.Context(), a.account, func(s string) { a.printf("%s\n", s) })
		},
	}, &cobra.Command{
		Use:   "status",
		Short: "who --account is signed in as",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			box, err := a.Open(cmd.Context(), a.account)
			if err != nil {
				return err
			}
			acct, err := box.Account(cmd.Context())
			if err != nil {
				return err
			}
			if !a.text {
				return a.emit(map[string]any{"account": a.account, "address": acct.Address, "send_limit": acct.SendLimit})
			}
			a.printf("%s: %s (send limit %s)\n", a.account, acct.Address, drafts.HumanSize(acct.SendLimit))
			return nil
		},
	})
	return auth
}
