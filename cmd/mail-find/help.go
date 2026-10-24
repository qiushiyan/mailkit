package main

import (
	"fmt"

	"github.com/qiushiyan/clikit"

	"github.com/qiushiyan/mailkit/internal/cluster"
	"github.com/qiushiyan/mailkit/internal/mail"
)

// The long help of each command: what an agent holding only the page needs
// to use it. Synopses, flags, defaults and exit codes are generated.

func (CLI) Help() string {
	return `
Reads the person's mail: find a message, read it, reassemble its
conversation or everything about the same order, booking or tenancy, and
download what is attached. Every command prints one JSON document; --text
prints the form for people instead.

An ID is the "id" field that search, resolve and thread print, and it
belongs to the account it came from. A Message-ID (the RFC 822 header, or a
message:// link dragged out of a mail client) is not an ID: resolve turns
one into an ID.

search, read, context and attachments results carry next_steps: runnable
commands for what the tool saw but did not do, such as pictures it did not
fetch, the conversation a message belongs to, or matches it held back. Run
the ones that bear on the question.

An error that says the account is not authenticated names the command that
signs it in again. Signing in needs the person at the keyboard: relay the
command to them rather than running it.`
}

func (CLI) HelpSections() []clikit.Section {
	return []clikit.Section{{Title: "Examples", Body: `
mail-find search 'from:taskrabbit.co.uk newer_than:30d'
mail-find read 1a02340ea531858a
mail-find thread 1a02340ea531858a
mail-find context 1a02367a18eae254`}}
}

func (SearchCmd) Help() string {
	return `
Searches the account in a grammar that means the same on Gmail and Outlook.
Every hit matches every term of the query, a phrase as the provider's
full-text search reads it. A result that fills --limit may have more
behind it: raise --limit or narrow the query. A query that scans a thousand
messages without filling --limit fails and says to narrow it, rather than
returning a short list that looks complete: add a date or a sender.

The terms of the grammar below must all hold, except subject: terms, of
which any one may. from: and to: take an address or a bare domain. after:
includes its day and before: does not, each day in the local time zone;
newer_than: and older_than: take Nd, Nm (N times 30 days) or Ny (N times
365). A bare word is a phrase. A term outside the grammar is an error that
names the grammar; --native passes the query to the provider untouched
instead.

A hit's snippet is the provider's preview, HTML-escaped by Gmail: read the
message before quoting it.`
}

func (SearchCmd) HelpSections() []clikit.Section {
	return []clikit.Section{
		{Title: "Grammar", Body: mail.Grammar},
		{Title: "Examples", Body: `
mail-find search 'from:taskrabbit.co.uk newer_than:30d'
mail-find search 'subject:invoice has:attachment after:2026/07/01 before:2026/08/01'
mail-find search '"order 1623209215"' --limit 5
mail-find search 'label:inbox' --native`},
		{Title: "JSON", Body: `
{"hits": [...], "next_steps": [...]}, with native naming the provider whose
syntax --native used. A hit has id, conversation_id, message_id (the RFC
822 Message-ID), date (as sent), from, to, cc, subject and snippet; an
empty one is left out.`},
	}
}

func (ResolveCmd) Help() string {
	return `
Finds the message a person is looking at in their mail client. The
Message-ID is the one identifier every client and provider agree on. A
message dragged out of Apple Mail arrives as a message:// link holding it,
percent-encoded, and resolve decodes that.`
}

func (ResolveCmd) HelpSections() []clikit.Section {
	return []clikit.Section{
		{Title: "Examples", Body: `
mail-find resolve '<6a8976c5.4a0b@mail.example>'
mail-find resolve 'message://%3C6a8976c5.4a0b@mail.example%3E'`},
		{Title: "JSON", Body: `
One message's envelope, as search prints a hit: id, conversation_id,
message_id, date, from, to, cc, subject and snippet.`},
	}
}

func (ReadCmd) Help() string {
	return `
Prints the message with its quoted history folded away, but only history
that verifiably appears earlier in the same conversation, which read loads
to check. A quote found nowhere earlier is kept whole, and fold_rejected
says why. --raw prints the body without folding.

The body is the HTML converted to text: a link reads "text ( URL )", a
table's rows become lines, and a picture is only its marker,
"[image] ( URL )" or its alt text. Most HTML mail carries its pictures as
links rather than attachments, so when the question is what a picture
shows, --fetch-remote downloads them and labels each: pixel (a tracking
beacon), small (a logo, avatar or badge) or image, the one worth opening. A
downloaded picture is a path; open it to see it. Fetching tells the sender
the mail was opened, which is accepted when the picture bears on the
question.`
}

func (ReadCmd) HelpSections() []clikit.Section {
	return []clikit.Section{
		{Title: "Examples", Body: `
mail-find read 1a02340ea531858a
mail-find read 1a02340ea531858a --fetch-remote`},
		{Title: "JSON", Body: `
The envelope as search prints it (id, conversation_id, message_id, date,
from, to, cc, subject, snippet), then:
  body            the text, folded unless --raw
  quoted_chars    how many characters of quoted history were folded
  fold_rejected   why a detected quote was kept
  attachments     every part: name, kind, handle, url, size, mime_type,
                  inline, content_id, truncated; an embedded_message carries
                  its text in embedded (from, to, date, subject, body,
                  attachments)
  remote_images   with --fetch-remote, each picture: url, path, size,
                  width, height, kind, or error
  next_steps`},
		{Title: "Attachment kinds", Body: kinds},
	}
}

// kinds is the section of the pages that list a message's parts.
const kinds = `
stored            a file or inline picture in the message; fetch downloads it
cloud_link        a OneDrive or Drive link; the bytes are at its url, which
                  fetch does not follow
embedded_message  a whole mail attached as a mail; its text is in embedded`

func (ThreadCmd) Help() string {
	return `
Reassembles the conversation the provider grouped this message with, oldest
first, one turn per message. A turn's said is what that message added, its
quoted history folded the way read folds it, so the turns can be read in
place of every message. --raw prints every message whole, its quoted
history included.

A conversation is the provider's grouping: Gmail's thread, Outlook's
conversation. Mail about the same thing from another sender, or a
notification that started a thread of its own, is not in it; context finds
those.`
}

func (ThreadCmd) HelpSections() []clikit.Section {
	return []clikit.Section{
		{Title: "Examples", Body: `
mail-find thread 1a02340ea531858a
mail-find thread 1a02340ea531858a --raw`},
		{Title: "JSON", Body: `
turns, oldest first, each with id, date, from, to, subject, said,
quoted_chars, fold_rejected and attachments (name, kind, inline); then
raw_chars, transcript_chars and folded_chars, the sizes before and after
folding. With --raw, messages instead, each with the envelope as search
prints it (id, conversation_id, message_id, date, from, to, cc, subject,
snippet), its whole body, and attachments as read prints them.`},
	}
}

func (ContextCmd) Help() string {
	return fmt.Sprintf(`
Gathers what is about the same real-world thing as the seed message, such
as an order, a booking or a tenancy, across threads and senders. It takes
the seed's identifiers (order and reference numbers; one after a word such
as order, invoice or ref is preferred to a bare run of digits), its
sender's domain and its subject words, searches on each, and scores what
comes back. An identifier is the strong signal; the domain and
the subject words are weak, and reach only --window days either side of the
seed.

It is a heuristic: every hit carries why, and is trusted by its why, not by
its rank. A hit's score is the sum of what it shares with the seed: an
identifier %d, the seed's own conversation %d, the sender's domain %d, its
subject words %d. Hits scoring under --min-score are held back, and an
identifier shared by more than %d messages is dropped as not distinctive
(it is a date or a price). Both are reported, and next_steps names the
command that shows them.`, cluster.WeightIdentifier, cluster.WeightThread, cluster.WeightDomain, cluster.WeightSubject, cluster.TooCommon)
}

func (ContextCmd) HelpSections() []clikit.Section {
	return []clikit.Section{
		{Title: "Examples", Body: `
mail-find context 1a02367a18eae254
mail-find context 1a02367a18eae254 --min-score 1`},
		{Title: "JSON", Body: `
  seed                    id, conversation_id, date, from, subject
  signals                 what was searched on: identifiers, subject_tokens,
                          domain
  dropped_as_too_common   each identifier dropped, with reason and hits
  below_min_score         how many hits were held back
  related                 oldest first, each with id, conversation_id, date,
                          from, subject, score and why
  next_steps`},
	}
}

func (AttachmentsCmd) Help() string {
	return `
Lists every part of the message without downloading anything, and the
pictures its body only links to. A message whose only content is a linked
screenshot has no attachments: remote_image_urls, and the next step naming
read --fetch-remote, are where that shows.`
}

func (AttachmentsCmd) HelpSections() []clikit.Section {
	return []clikit.Section{
		{Title: "JSON", Body: `
attachments, each part as read prints it: name, kind, handle, url, size,
mime_type, inline, content_id, truncated, and for an embedded_message its
text in embedded (from, to, date, subject, body, attachments). Then
remote_image_urls, in the order the body has them, and next_steps.`},
		{Title: "Attachment kinds", Body: kinds},
	}
}

func (FetchCmd) Help() string {
	return `
Writes the chosen attachments to disk: into --out, or else the message's
own directory under $XDG_STATE_HOME/mailkit/attachments/ (by default
~/.local/state/mailkit/attachments/), never the working directory. Names
are made safe for the disk, and two files that would collide get -2, -3.
Only stored parts have bytes; for a cloud link or an embedded message fetch
says there is nothing to download, and attachments shows what each holds.

A downloaded file is a path, not yet read: open it to see what it says.`
}

func (FetchCmd) HelpSections() []clikit.Section {
	return []clikit.Section{
		{Title: "Examples", Body: `
mail-find fetch 1a01c98dd4e47691 --attachment report.pdf --attachment data.csv
mail-find fetch 1a01c98dd4e47691 --all --out ./downloads`},
		{Title: "JSON", Body: `
saved, each file with name (as the mail gave it), path and size.`},
	}
}

func (LoginCmd) Help() string {
	return `
Signs --account in. Gmail opens a browser and waits for its redirect;
Outlook shows a device code to enter at the address it gives. The token is
kept under $XDG_CONFIG_HOME/mailkit (by default ~/.config/mailkit) for
later runs. It needs the person at the keyboard.`
}

func (StatusCmd) HelpSections() []clikit.Section {
	return []clikit.Section{{Title: "JSON", Body: `
account, address (the one --account sends as), and send_limit in bytes.`}}
}
