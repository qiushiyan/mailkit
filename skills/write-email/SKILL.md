---
name: write-email
description: "Draft and send mail with the send-mail CLI: preview first, send only on an explicit commit."
disable-model-invocation: true
---

# Write email with `send-mail`

Nothing is sent until `--commit`. Composing builds the exact message that
would go out, writes an HTML preview, and stops. The user looks at the
preview; only then does a commit send those bytes — and a draft sends once.

## 1. Compose

Bodies are markdown by default: write naturally and the recipient gets a
formatted mail (bold, lists, links, headings) with a plain-text alternative
generated from the same source. A newline is a line break — sign-offs and
address blocks come out exactly as written. Prefer `--body-file` or stdin
over inline `--body` for anything with backticks or `$`.

```sh
send-mail --to alice@example.com --subject "Meter reading" --body "Taken on the 1st: **04512**."
send-mail --to alice@example.com,bob@example.com --cc ops@example.com --bcc me@example.com \
          --subject "Q3 numbers" --body-file note.md --attach report.pdf --attach data.csv
send-mail --to alice@example.com --subject "Hi" --body-file - < reply.md       # body from stdin
send-mail --to alice@example.com --subject "Hi" --body "…" --sender me@alias.example   # send-as alias
```

Two other formats, both explicit:

- `--format text` — the bytes go out verbatim as plain text. Use it when the
  body must survive untouched: pasted logs, code, anything where `*`, `[`,
  `#` or `<angle brackets>` are content rather than formatting. (Markdown
  compose refuses raw HTML, images, and non-http/mailto links with an error
  naming this escape; backtick-fencing a snippet also keeps it literal.)
- `--format html` — the body is finished HTML, sent as-is.

The preview opens in the browser by default; `--no-open` suppresses that
(use it when the user will review the text in the conversation instead).
`--account outlook` composes from the other mailbox. A message over the
provider's size limit is reported at compose time: send a share link instead
of the file.

Write the body for the recipient, in the user's voice, and show it to the
user before composing — the preview is the second look, not the first.

Output:

```
draft   20260822-115120-meter-reading-305f
account gmail (qiushi.yann@gmail.com)
preview /Users/qiushi/.local/state/mailkit/drafts/20260822-115120-meter-reading-305f.html
size    301 B

nothing has been sent. to send exactly this draft:
    send-mail --commit 20260822-115120-meter-reading-305f
```

## 2. Commit

Only after the user has approved the draft:

```sh
send-mail --commit 20260822-115120-meter-reading-305f
```

```
sent as qiushi.yann@gmail.com -> alice@example.com
subject: Meter reading
```

Changing anything means a new draft: an edited `.eml` is refused (the bytes
no longer match the preview), and a sent draft is refused a second time.
`send-mail --list` shows recent drafts with their state.

## If a commit fails

| state | meaning | do |
|---|---|---|
| `pending` | refused before anything left (too large, not signed in) | fix the cause, commit again |
| `unknown` | the provider may have accepted it; the result was lost | check sent mail for the subject around that time (`mail-find search 'subject:"…" newer_than:1d'`) before re-drafting — never re-send blind |

## Replies

`send-mail` composes new messages; it does not thread a reply onto an
existing conversation. To answer a mail: read it with `mail-find read`, write
the reply with the same subject prefixed `Re:` and address the original
sender, and quote only what the answer needs.
