---
name: read-email
description: "Read the user's mail with the mail-find CLI — find messages by sender, subject, date or phrase; read one in full incl. attachments and the pictures it links to; reassemble a thread or everything about one order/booking/tenancy across senders; turn a Message-ID or message:// link into a message."
---

# Read email with `mail-find`

`mail-find` is read-only and returns JSON. Every result carries `next_steps`: runnable commands for what the tool saw but did not do (pictures not fetched, a thread this message belongs to, an attachment that is itself a message). **Run the `next_steps` that bear on the question** — they are the protocol, not advice.

## 1. Find the message

```sh
# portable grammar: same meaning on gmail and outlook
mail-find search 'from:taskrabbit.co.uk newer_than:30d'
mail-find search 'subject:invoice has:attachment after:2026/07/01 before:2026/08/01'
mail-find search '"order 1623209215"'          # exact phrase; bare words are phrases too
mail-find search 'from:alice@example.com' --limit 5
```

```
from:ADDR|DOMAIN  to:ADDR|DOMAIN  subject:WORD|"PHRASE"  has:attachment
after:YYYY/MM/DD  before:YYYY/MM/DD  newer_than:Nd  older_than:Nd  "exact phrase"  word
```

Anything outside that grammar is an error naming the escape hatch: `--native` passes the query to the provider untouched (`mail-find search 'label:inbox' --native`). A search that says *narrow the query* scanned a thousand messages without filling its limit — add a date or sender.

Each hit: `id`, `conversation_id`, `message_id`, `date`, `from`, `to`, `subject`, `snippet`. `id` is what every other command takes.

When the user is looking at a mail in their client, resolve it instead of searching:

```sh
mail-find resolve '<6a8976c5...@mail>'                 # an RFC 822 Message-ID
mail-find resolve 'message://%3C6a8976c5...@mail%3E'   # a link dragged out of Apple Mail
```

## 2. Read it

```sh
mail-find read 1a02340ea531858a
```

`body` is the message as text with quoted history folded away — only history that verifiably appears earlier in the same conversation; `quoted_chars` says how much. `fold_rejected`, when present, explains why a detected quote was kept (its text exists nowhere else). `attachments` lists every part with `kind`:

| kind | what it is | bytes |
|---|---|---|
| `stored` | a real attachment, or an inline image (`inline: true`) | `fetch` |
| `cloud_link` | a OneDrive/Drive link | `url` — follow it yourself |
| `embedded_message` | a mail attached as a mail | its text is in `embedded` |

Most HTML mail carries its pictures as links, not attachments. A screenshot someone sent arrives that way, so when the question is about what a picture shows:

```sh
mail-find read 1a02340ea531858a --fetch-remote     # downloads into ~/.local/state/mailkit/attachments/<id>/remote/
```

`remote_images[]` come back labelled, never filtered: `pixel` (tracking beacon), `small` (logo, avatar, badge), or `image` — the one worth opening with the Read tool. Fetching tells the sender the mail was opened; that is accepted.

`--raw` gives the body as sent, quotes and all. Use it when a fold looks wrong or the user asks what exactly was written.

## 3. Go wider

Two different questions, two commands:

```sh
mail-find thread 1a02340ea531858a    # what the provider grouped: the reply chain as one transcript
mail-find context 1a02367a18eae254   # what is about the same thing: across threads and senders
```

`thread` returns `turns[]` in order, each with `said` (what that message added, history folded) — read this instead of each message. `context` is a heuristic: it extracts identifiers (order numbers, references), the sender's domain and subject words from the seed, searches on them, and returns `related[]` where every hit carries `why`. Too-common identifiers and weak matches are held back and named; `--min-score 1` or `--raw` shows them. Trust a hit by its `why`, not its rank.

## 4. Get the files

```sh
mail-find attachments 1a01c98dd4e47691               # manifest only
mail-find fetch 1a01c98dd4e47691 --attachment report.pdf --attachment data.csv
mail-find fetch 1a01c98dd4e47691 --all --out ./downloads
```

Files land under `~/.local/state/mailkit/attachments/<id>/` unless `--out` says otherwise; names are sanitised and collisions get `-2`. `fetch` on a message whose attachments are links or embedded mails says so and points back at `attachments`.

## Accounts and output

`--account gmail` is the default; `--account outlook` for the other mailbox. `--text` anywhere gives the human form. An authentication error prescribes the command that fixes it (`mail-find auth login --account …`) — relay it to the user rather than retrying.
