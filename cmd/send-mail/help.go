package main

import "github.com/qiushiyan/clikit"

// The long help of each command: what an agent holding only the page needs
// to use it. Synopses, flags, defaults and exit codes are generated.

func (CLI) Help() string {
	return `
Mail goes out as the person, and a sent mail cannot be recalled, so writing
and sending are separate acts. compose and reply build the exact message
that would go out, write it with an HTML preview, and stop. Only commit
sends, and it sends those bytes once: a draft that changed since its
preview is refused, and a sent one cannot be sent again. Show the person the
preview, and commit only once they have approved it.

send-mail sends from the accounts that mail-find auth login signs in; one
sign-in covers reading and sending. An error that says the account is not
authenticated names that command, which needs the person at the keyboard.`
}

func (CLI) HelpSections() []clikit.Section {
	return []clikit.Section{{Title: "Examples", Body: `
send-mail compose --to alice@example.com --subject "Meter reading" --body-file note.md
send-mail reply 1a10bfe41519179c --body-file reply.md
send-mail commit 20260822-115120-meter-reading-305f`}}
}

// body is the section of the pages that take a body.
const body = `
The body is markdown unless --format says otherwise: bold, lists, links and
headings come out formatted, with a plain-text part generated from the same
source, and a newline is a line break, so sign-offs keep their lines.
Markdown that cannot look the same in both parts is refused, naming the way
out: raw HTML (--format html for a finished HTML body, --format text for
the bytes as written), images (--attach the file), and links that are not
http, https or mailto. Use --format text for a body whose *, [ or # are
content, such as a pasted log: markdown would format them.`

// preview is the section of the pages that write one.
const preview = `
The preview is read back from the draft's bytes, so it shows what will go
out: the account, From, To, Cc, Bcc, the format and the size; the HTML part
as it renders, and the plain-text part; the attachments; and for a reply,
the message it answers. With --no-open, open the file for the person or
relay those to them.`

func (ComposeCmd) Help() string {
	return `
Builds the message from the flags and the account; it goes out from the
account's own address unless --sender names another. It writes the draft
and an HTML preview, opens the preview in the browser unless --no-open,
and prints, as labelled lines of text, the draft id, the account, the
preview's path, its size, and the commit command. A message over the account's size limit is flagged here and
refused at commit: send a share link instead of the file.`
}

func (ComposeCmd) HelpSections() []clikit.Section {
	return []clikit.Section{
		{Title: "Body", Body: body},
		{Title: "Preview", Body: preview},
		{Title: "Examples", Body: `
send-mail compose --to alice@example.com --subject "Meter reading" --body "Taken on the 1st: **04512**."
send-mail compose --to alice@example.com,bob@example.com --cc ops@example.com --subject "Q3 numbers" --body-file note.md --attach report.pdf
send-mail compose --to alice@example.com --subject "Hi" --body-file - < note.md`},
	}
}

func (ReplyCmd) Help() string {
	return `
Answers message ID inside its conversation, so the answer threads in Gmail
and on a helpdesk, not only in clients that group by subject. The
recipients and the subject come from the original: it goes to the
original's Reply-To, or else its sender (for a message the account sent, to
that message's own recipients), and --all adds everyone else on it except
you. There is no --to, because the original decides who receives it, and
no --subject, because Gmail threads a reply only when the subject matches;
--cc and --bcc add people.

Because the tool chose the recipients, the output says whom the reply
answers and who receives it, and the preview opens with the message it
answers. Tell the person who it goes to.`
}

func (ReplyCmd) HelpSections() []clikit.Section {
	return []clikit.Section{
		{Title: "Body", Body: body},
		{Title: "Preview", Body: preview},
		{Title: "Examples", Body: `
send-mail reply 1a10bfe41519179c --body-file reply.md
send-mail reply 1a10bfe41519179c --all --cc lee@example.com --body-file reply.md`},
	}
}

func (CommitCmd) Help() string {
	return `
Sends the draft's bytes exactly as previewed, from the account it was
written on, into the conversation its record names, and prints the address
it went out as. For a reply it also
says which conversation the provider put it in; when that is not the
original's, or the provider does not say, it prints the mail-find thread
command that shows where it landed.

A failure before anything left returns the draft to pending: fix what the
error names and commit again, or write a new draft if the fix changes the
message. A failure after the bytes may have left makes the outcome unknown:
check sent mail with the mail-find search the error prints before writing
the draft again, because a duplicate cannot be recalled. Committing that
draft again sends nothing and prints the search again.`
}

func (CommitCmd) HelpSections() []clikit.Section {
	return []clikit.Section{
		{Title: "Examples", Body: `
send-mail commit 20260822-115120-meter-reading-305f`},
		{Title: "States", Body: states},
	}
}

func (ListCmd) HelpSections() []clikit.Section {
	return []clikit.Section{{Title: "States", Body: states}}
}

// states is the section of the pages that report a draft's state.
const states = `
pending   written and not sent; commit sends it, and one never committed
          is never sent
sending   a commit is under way; if none is, it died before recording the
          outcome, and commit reports the draft as unknown
sent      sent once, with the time; it cannot be sent again
unknown   the provider may have accepted it and the result was lost`
