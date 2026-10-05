---
name: write-email
description: "Draft and send mail with the send-mail CLI, new or as a reply in its thread: preview first, send only on an explicit commit."
disable-model-invocation: true
---

# Write email with `send-mail`

You are sending mail as the user: a new message, or a reply to one they
received. Done is a draft the user has seen and approved, committed once,
with the commit reporting it sent — for a reply, in the original's thread.

Nothing leaves until `--commit`. Composing builds the exact message that
would go out, writes an HTML preview of it, and stops; a commit sends those
bytes and nothing else, and a draft sends once.

## Write the body first

Write it for the recipient, in the user's voice, and show it to the user
before composing — the preview is the second look, not the first. For a
reply, read the original first (`mail-find read ID`) and quote only what the
answer needs; the tool adds no quote of the original.

Bodies are markdown by default: write naturally and the recipient gets a
formatted mail (bold, lists, links, headings) with a plain-text alternative
generated from the same source. A newline is a line break, so sign-offs and
address blocks come out exactly as written. Prefer `--body-file` or stdin
over inline `--body` for anything with backticks or `$`.

Two other formats, both explicit:

- **`--format text`:** the bytes go out verbatim as plain text. Use it when
  `*`, `[`, `#` or `<angle brackets>` are content rather than formatting —
  pasted logs, code. Markdown compose refuses raw HTML, images, and
  non-http/mailto links with an error naming this escape; backtick-fencing a
  snippet also keeps it literal.
- **`--format html`:** the body is finished HTML, sent as-is.

## Compose a new message

```sh
send-mail --to alice@example.com --subject "Meter reading" --body "Taken on the 1st: **04512**."
send-mail --to alice@example.com,bob@example.com --cc ops@example.com --bcc me@example.com \
          --subject "Q3 numbers" --body-file note.md --attach report.pdf --attach data.csv
send-mail --to alice@example.com --subject "Hi" --body-file - < note.md         # body from stdin
send-mail --to alice@example.com --subject "Hi" --body "…" --sender me@alias.example   # send-as alias
```

`--account outlook` composes from the other mailbox.

## Reply to a message

When the user is answering a mail, reply to it rather than writing a new
message with a `Re:` subject. A new message only looks threaded in clients
that group by subject; Gmail files it as a conversation of its own, and a
helpdesk cannot attach it to its ticket.

```sh
send-mail --reply 1a10bfe41519179c --body-file reply.md                               # to the sender
send-mail --reply-all 1a10bfe41519179c --cc lee@example.com --body-file reply.md      # to everyone on it, and Lee
```

`ID` is the `id` that `mail-find` returns, from the same `--account`. The
reply goes to the original's sender — its Reply-To when it names one — and
`--reply-all` adds everyone else on it except the user; `--cc` and `--bcc`
add more. Choose reply-all only when the user means everyone on the original
to read the answer. The `Re:` subject and the threading come from the
original, so `--to` and `--subject` are refused with a reply: Gmail threads
a reply only when the subjects match.

The tool chose the recipients, so tell the user who the reply goes to:
compose prints them, and the preview opens with the message the reply
answers.

## Show the preview, then commit

What the user approves is the composed draft — markdown compiles, so the
sent mail is the preview's rendering, not the source you drafted. The
preview opens in the browser by default. With `--no-open`, surface the
preview file named in the compose output: open it for the user, or relay
what it shows — recipients, subject, attachments, the rendered body, and for
a reply the message it answers. A message over the provider's size limit is
flagged at compose time; send a share link instead of the file.

Only after the user approves, commit the draft id that compose printed:

```sh
send-mail --commit 20260822-115120-meter-reading-305f
```

Changing anything means a new draft: an edited draft is refused because its
bytes no longer match the preview, and a sent draft is refused a second
time. `send-mail --list` shows recent drafts and their state.

A reply's commit names the thread the provider put it in. When it warns
that the reply landed in another conversation, or says the provider does
not report one, run the `mail-find thread` command it prints and tell the
user what it shows.

## When a commit fails

The draft's state says what happened:

- **`pending`:** refused before anything left — too large, not signed in,
  changed since its preview, or on Outlook a reply whose recipients its
  reply actions could not match. Fix what the error names and commit again;
  a fix that changes the message means a new draft.
- **`unknown`:** the provider may have accepted it and the result was lost.
  Check sent mail for the subject around that time
  (`mail-find search 'subject:"…" newer_than:1d'`) before re-drafting, and
  never re-send blind: a duplicate cannot be recalled.
