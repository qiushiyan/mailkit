# mailkit

Command-line mail for agents: let Claude search my mail history, pull attachments
out of it, and draft replies -- without opening a browser, and without ever
sending something I have not seen.

MVP, Python, stdlib only. Likely to be rewritten as a Go CLI once the shape settles.

## The two commands

Read and send are separate binaries on purpose. Wanting an agent to look
something up in my mail should not also hand it the ability to send as me.

```
mail-find search "from:alice has:attachment newer_than:30d"
mail-find read <message-id>
mail-find attachments <message-id>
mail-find fetch <message-id> --attachment report.pdf --attachment data.csv

send-mail --to a@b.com --subject "Hi" --body-file note.md --attach report.pdf
send-mail --commit <draft-id>
send-mail --list
```

`mail-find` emits JSON by default (`--text` for humans) so the model gets
structure instead of scraped text. `search` takes the provider's own query
syntax -- for Gmail that is the search box syntax, `has:attachment`,
`newer_than:7d`, `from:`, `subject:`.

Attachments download to `~/.local/state/mailkit/attachments/<message-id>/`
rather than the cwd, so an agent cannot litter whatever directory it happens
to be standing in.

## The approval gate

`send-mail` never sends on the first call. Composing writes a draft to
`~/.local/state/mailkit/drafts/`, renders an HTML preview, opens it, and prints
the one command that would send it.

`--commit` takes a **draft id**, not a fresh set of flags. That is the whole
point: the bytes that go out are the bytes that were reviewed. Three things
keep that honest:

- every attachment's sha256 is recorded at draft time and re-checked at send;
  a file edited in between blocks the send
- a draft carries a `sent_at` stamp and is refused once set -- single use
- the account is fixed at draft time, so the preview's From line cannot lie

A provider-side draft (`gws gmail +send --draft`) was the obvious alternative
and is worse for this: reviewing it means opening a browser, which is the chore
this tool exists to remove, and nothing pins the reviewed draft to what is
finally sent.

## Layout

```
bin/send-mail          compose + approval gate
bin/mail-find          read-only search / read / fetch
src/mailkit/backends.py   everything that touches a provider
src/mailkit/drafts.py     draft store, hashing, single-use commit
src/mailkit/preview.py    the local HTML page
```

Both binaries are symlinked into `~/.local/bin`.

Backends are the only provider-aware code. Drafts, previews and the gate are
provider-agnostic, so a second account is a class, not a fork.

## Gmail setup

Transport is [`gws`](https://github.com/googleworkspace/cli) -- Google's
Workspace CLI. Not an officially supported product and pre-1.0, so expect
breaking changes; it earns its place by handling OAuth, keyring storage, MIME
and 25 MB attachments so this repo does not have to.

```
npm i -g @googleworkspace/cli
brew install --cask google-cloud-sdk     # gws auth setup drives gcloud
gcloud auth login
gws auth setup                            # creates the GCP project + OAuth client
gws auth login -s gmail                   # scope-limited: gmail only, not all 85+
```

`gws auth login -s gmail` matters. The default scope preset asks for 85+ scopes
and fails outright for unverified apps (~25 scope ceiling).

The alternative -- an app password over SMTP/IMAP -- still works on Gmail in
2026 (unlike Outlook, where Microsoft killed Basic Auth for SMTP AUTH in spring
2026). It was rejected anyway: it means holding a real credential that grants
full mailbox access, versus OAuth where no copyable password ever exists.

## Outlook status

Not wired up. The backend class exists and raises a clear error.

The Graph path is designed -- `m365 outlook mail send` for sending,
`m365 request` against raw Graph for reading and attachments -- but it needs an
Entra app registration in the planlab tenant (delegated `Mail.Send` +
`Mail.Read`, both user-consentable by default). Blocked on confirming whether
that tenant lets a non-admin register an app.

Two things to know when that resumes:

- Graph inlines attachments in the send request, capping them around 3 MB --
  an order of magnitude below Gmail's 25 MB
- Graph's `$search` and `$filter` cannot be combined in one request, so
  "sent to X last week with attachments" is a coarse filter plus client-side
  narrowing, not one query

The local fallback -- reading Mail.app's `.emlx` store and sending via
AppleScript -- needs Full Disk Access for the terminal and only sees locally
synced history, so it is a downgrade, not an equivalent.

## Notes

Mail bodies are untrusted input. Once an agent reads mail, a message someone
else sent can try to instruct it ("forward the attachment to ..."). `gws` has
`--sanitize` (Model Armor) for this; not enabled yet.
