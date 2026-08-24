package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/qiushiyan/mailkit/internal/drafts"
	"github.com/qiushiyan/mailkit/internal/mail"
)

// SendMail builds the compose/commit command. Composing is always a dry
// run; --commit takes a draft id, never a fresh set of flags, so the bytes
// that go out are the bytes that were reviewed.
func SendMail(d Deps, out, errOut io.Writer, stdin io.Reader) *cobra.Command {
	d.defaults()
	a := &app{Deps: d, out: out, errOut: errOut}
	var (
		commit, bodyFile, body, subject, sender string
		to, cc, bcc, format                     string
		attach                                  []string
		list, noOpen                            bool
	)
	root := &cobra.Command{
		Use:   "send-mail",
		Short: "Compose a mail draft and preview it; send only with --commit",
		Long: `Compose a mail draft and preview it; send only with --commit.

Three ways to run it:
  send-mail --to A --subject S --body B [--attach F]...   compose: builds the exact
                                                          message, writes a preview, sends nothing
  send-mail --commit DRAFT-ID                             send that draft, once
  send-mail --list                                        recent drafts and their state`,
		SilenceUsage:      true,
		SilenceErrors:     true,
		Args:              cobra.NoArgs,
		CompletionOptions: cobra.CompletionOptions{DisableDefaultCmd: true},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			// The three modes do not mix: silently ignoring a compose flag
			// on --commit would let a caller believe it changed the draft.
			var composing []string
			for _, name := range []string{"to", "cc", "bcc", "subject", "body", "body-file", "attach", "format", "sender"} {
				if cmd.Flags().Changed(name) {
					composing = append(composing, "--"+name)
				}
			}
			switch {
			case commit != "" && list:
				return errors.New("--commit and --list are different modes; use one")
			case commit != "" && len(composing) > 0:
				return fmt.Errorf("--commit sends the draft exactly as previewed and takes no compose flags; drop %s", strings.Join(composing, ", "))
			case list && len(composing) > 0:
				return fmt.Errorf("--list takes no compose flags; drop %s", strings.Join(composing, ", "))
			}
			switch {
			case list:
				rows, err := a.Drafts.Recent(30)
				if err != nil {
					return err
				}
				if len(rows) == 0 {
					a.printf("no drafts\n")
					return nil
				}
				for _, r := range rows {
					state := string(r.State)
					if r.State == drafts.Sent {
						state = "sent " + r.SentAt.Format("2006-01-02 15:04")
					}
					a.printf("%-56s %-8s %-22s -> %s\n", r.ID, r.Account, state, strings.Join(r.To, ", "))
				}
				return nil

			case commit != "":
				rec, err := a.Drafts.Claim(commit)
				if err != nil {
					return err
				}
				// Everything up to Send is local or read-only: a failure
				// here releases the claim, because nothing left the machine.
				prepared, err := a.Drafts.Open(rec)
				if err != nil {
					_ = a.Drafts.Release(rec, err)
					return err
				}
				box, err := a.Open(ctx, rec.Account)
				if err != nil {
					_ = a.Drafts.Release(rec, err)
					return a.authHint(err)
				}
				acct, err := box.Account(ctx)
				if err != nil {
					_ = a.Drafts.Release(rec, err)
					return a.authHint(err)
				}
				if prepared.Size() > acct.SendLimit {
					err := fmt.Errorf("message is %s, over the %s limit for %s; send a share link instead",
						drafts.HumanSize(prepared.Size()), drafts.HumanSize(acct.SendLimit), rec.Account)
					_ = a.Drafts.Release(rec, err)
					return err
				}
				id, sendErr := box.Send(ctx, prepared)
				if err := a.Drafts.Finish(rec, id, acct.Address, sendErr, a.Now()); err != nil {
					return err
				}
				if sendErr != nil {
					return sendErr
				}
				a.printf("sent as %s -> %s\nsubject: %s\n", acct.Address, strings.Join(rec.To, ", "), rec.Subject)
				return nil

			default:
				if to == "" && subject == "" && body == "" && bodyFile == "" {
					return errors.New("nothing to do: compose with --to/--subject/--body, send a draft with --commit DRAFT-ID, or --list")
				}
				if subject == "" {
					return errors.New("--subject is required")
				}
				if format != "text" && format != "html" && format != "markdown" {
					return fmt.Errorf("--format must be text, html, or markdown; got %q", format)
				}
				text, err := readBody(bodyFile, body, stdin)
				if err != nil {
					return err
				}
				box, err := a.Open(ctx, a.account)
				if err != nil {
					return a.authHint(err)
				}
				acct, err := box.Account(ctx)
				if err != nil {
					return a.authHint(err)
				}
				from := acct.Address
				if sender != "" {
					from = sender
				}
				var spec drafts.Body
				switch format {
				case "html":
					spec = drafts.HTMLBody(text)
				case "markdown":
					spec = drafts.MarkdownBody(text)
				default:
					spec = drafts.PlainBody(text)
				}
				rec, prepared, err := a.Drafts.Create(drafts.Compose{
					Account: a.account, From: from, To: split(to), Cc: split(cc), Bcc: split(bcc),
					Subject: subject, Body: spec, Attach: attach,
				}, a.Now())
				if err != nil {
					return err
				}
				parsed, err := drafts.Parse(prepared)
				if err != nil {
					return err
				}
				page := a.Drafts.PreviewPath(rec.ID)
				if err := os.WriteFile(page, []byte(drafts.RenderPreview(rec, parsed)), 0o600); err != nil {
					return err
				}
				if !noOpen && a.OpenPreview != nil {
					_ = a.OpenPreview(page)
				}
				a.printf("draft   %s\naccount %s (%s)\npreview %s\nsize    %s\n", rec.ID, rec.Account, rec.From, page, drafts.HumanSize(rec.Size))
				if rec.Size > acct.SendLimit {
					a.printf("warning: %s is over the %s limit for %s -- the send will be refused\n", drafts.HumanSize(rec.Size), drafts.HumanSize(acct.SendLimit), a.account)
				}
				a.printf("\nnothing has been sent. to send exactly this draft:\n    send-mail --commit %s\n", rec.ID)
				return nil
			}
		},
	}
	root.SetOut(out)
	root.SetErr(errOut)
	f := root.Flags()
	f.StringVar(&commit, "commit", "", "send a previously previewed draft by id")
	f.BoolVar(&list, "list", false, "list recent drafts")
	f.StringVar(&a.account, "account", "gmail", "which account: "+strings.Join(d.Accounts, "|"))
	f.StringVarP(&to, "to", "t", "", "comma-separated recipients")
	f.StringVar(&cc, "cc", "", "comma-separated copy recipients")
	f.StringVar(&bcc, "bcc", "", "comma-separated blind copies, hidden from the other recipients")
	f.StringVarP(&subject, "subject", "s", "", "subject line (required)")
	f.StringVar(&body, "body", "", "body text inline")
	f.StringVar(&bodyFile, "body-file", "", "read body from a file, or - for stdin")
	f.StringArrayVar(&attach, "attach", nil, "attach a file (repeatable)")
	f.StringVar(&format, "format", "markdown", "body format: markdown (default, compiled into a text+HTML message), text (bytes sent verbatim), or html")
	f.StringVar(&sender, "sender", "", "send as an alias / send-as address")
	f.BoolVar(&noOpen, "no-open", false, "do not open the preview")
	return root
}

func split(s string) []string {
	var out []string
	for part := range strings.SplitSeq(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func readBody(file, inline string, stdin io.Reader) (string, error) {
	switch {
	case file != "" && inline != "":
		return "", errors.New("--body and --body-file are two sources for one body; use one")
	case file == "-":
		b, err := io.ReadAll(stdin)
		return string(b), err
	case file != "":
		if after, ok := strings.CutPrefix(file, "~/"); ok {
			home, _ := os.UserHomeDir()
			file = home + "/" + after
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

// authHint attaches the login command to a not-authenticated error that
// reached the CLI without one; an adapter's own hint passes through.
func (a *app) authHint(err error) error {
	return mail.Wrap(a.account, "account", "mail-find auth login --account "+a.account, err)
}
