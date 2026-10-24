# mailkit

Two command-line tools that let an agent work with your mail. `mail-find`
searches it, reads a message or a whole conversation, and downloads what is
attached; it changes nothing. `send-mail` writes a draft and an HTML preview
of the exact message that would go out, and sends it only when that draft
is committed. They are separate binaries so that an agent can be allowed to
look things up without being able to send as you.

## Install

```sh
make install   # builds both into ~/.local/bin
make skills    # links skills/read-email and skills/write-email into the dotfiles skills directory
```

Sign in once per account; the one sign-in covers reading and sending:

```sh
mail-find auth login                     # Gmail; the OAuth client goes in ~/.config/mailkit/gmail-client.json
mail-find auth login --account outlook   # Outlook; see docs/outlook-status.md
```

## Use

`mail-find --help` and `send-mail --help` are the manual: every command's
page has what it does, its JSON or output, and examples. Agents use the
tools through the skills: `read-email` to find and read, `write-email` to
draft, show the preview, and send once it is approved.

```sh
mail-find search 'from:taskrabbit.co.uk newer_than:30d'
mail-find read 1a02340ea531858a
send-mail compose --to alice@example.com --subject "Meter reading" --body-file note.md
send-mail commit 20260822-115120-meter-reading-305f
```

Credentials live under `~/.config/mailkit/`, drafts and downloads under
`~/.local/state/mailkit/` (or the `XDG_CONFIG_HOME` and `XDG_STATE_HOME`
equivalents).

## Design

`docs/go-design.md`; working on mailkit: `AGENTS.md`.
