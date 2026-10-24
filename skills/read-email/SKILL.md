---
name: read-email
description: "Read the user's mail with the mail-find CLI — find messages by sender, subject, date or phrase; read one in full incl. attachments and the pictures it links to; reassemble a thread or everything about one order/booking/tenancy across senders; turn a Message-ID or message:// link into a message."
---

# Read email with `mail-find`

You are answering a question from the user's mail. Done is an answer
grounded in what the mail says, including what it attaches and the pictures
it only links to. `mail-find` is read-only and prints JSON; each command's
page, `mail-find <command> --help`, has its grammar, flags, JSON fields and
examples, so run it when you need an operator or a field this skill does not
show.

Search, read, context and attachments results carry `next_steps`: runnable
commands for what the tool saw but did not do, such as pictures it did not
fetch, the conversation a message belongs to, or matches it held back.
**Run the `next_steps` that bear on the question**; they are the protocol,
not advice.

## 1. Find the message

```sh
mail-find search 'from:taskrabbit.co.uk newer_than:30d'
mail-find search 'subject:invoice has:attachment after:2026/07/01 before:2026/08/01'
mail-find search '"order 1623209215"' --limit 5
```

The grammar means the same on Gmail and Outlook, and a term outside it is an
error naming the grammar; `--native` passes provider syntax through. A search
that says to narrow the query scanned a thousand messages without filling
its limit: add a date or a sender. A hit's `id` is what every other command
takes, and it belongs to its account; `--account outlook` reads the other
mailbox.

When the user is looking at a mail in their client, resolve it instead of
searching:

```sh
mail-find resolve 'message://%3C6a8976c5...@mail%3E'   # a link dragged out of Apple Mail, or a bare Message-ID
```

## 2. Read it

```sh
mail-find read 1a02340ea531858a
```

The body has quoted history folded away, but only history that verifiably
appears earlier in the same conversation; `fold_rejected` says why a quote
was kept, and `--raw` gives the body unfolded when a fold looks wrong or the
exact words matter. Check `attachments` by `kind`: an `embedded_message`
carries its text in `embedded`, and a `cloud_link` is a URL to follow
yourself.

Most HTML mail carries its pictures as links, not attachments. When the
question is about what a picture shows, fetch them and open the ones
labelled `image` with the Read tool; fetching tells the sender the mail was
opened, and that is accepted:

```sh
mail-find read 1a02340ea531858a --fetch-remote
```

## 3. Go wider

Two different questions, two commands:

```sh
mail-find thread 1a02340ea531858a    # what the provider grouped: the reply chain as one transcript
mail-find context 1a02367a18eae254   # what is about the same thing: across threads and senders
```

Read a thread's `turns`, each `said` being what that message added, instead
of each message. `context` is a heuristic built on the seed's order and
reference numbers, its sender's domain and its subject words: trust a hit by
its `why`, not its rank.

## 4. Get the files

```sh
mail-find fetch 1a01c98dd4e47691 --attachment report.pdf --attachment data.csv
mail-find fetch 1a01c98dd4e47691 --all --out ./downloads
```

A saved file is a path, not its content: open it before answering from it.
Files land under `~/.local/state/mailkit/attachments/<id>/` unless `--out`
names another directory.

An error that says an account is not authenticated names the command that
signs it in. Relay it to the user rather than running it: signing in needs
them at the keyboard.
