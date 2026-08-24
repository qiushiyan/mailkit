# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

Personal Go CLI that lets an agent read my mail (`mail-find`) and send mail only after I have seen it (`send-mail`). Two binaries on purpose: the ability to look things up must not also grant the ability to send as me. The repo holds verbatim personal mail as fixtures; it stays private.

## Commands

```
make check          # vet + `go fix -diff` must be empty + `go test -race ./...`
make install        # builds to ~/.local/bin/{mail-find,send-mail} (atomic rename)
go test ./internal/cli -run TestRead_ForwardSurvivesWhole
make test-live      # real Gmail/Outlook; RECORD=1 re-records testdata cassettes
```

Go 1.27 is assumed from the start: `encoding/json/v2`, `errors.AsType`, `strings.SplitSeq`, `t.Context()`, and `go fix` modernizers are part of the gate, not optional.

## Architecture: one port, adapters below, rules above

`internal/mail` is the port. `Mailbox` (Account/Resolve/Search/Fetch/Conversation/Open/Send) is the whole provider surface; `Criteria` plus `Match` is the one definition of "this message matches"; `Part.Content` is a sealed sum (`StoredPart` / `LinkedPart` / `EmbeddedPart`) because where the bytes live decides what fetching them costs and tells the sender. `Prepared` is an unforgeable send token: the only thing `Send` accepts is the bytes that were previewed.

**The port owns every invariant it states; adapters never re-implement one.** `mail.Narrow` is the exact-search walk (page, narrow with `MatchEnvelope`, stop at limit or exhaustion, `ErrTooBroad` past 1000 rows). `mail.Wrap` labels errors once at an adapter's public boundary — inner helpers return plain errors. `mail.HasAttachments` is the one definition of "has an attachment" (a non-inline part). If you find yourself writing a loop, an error literal or a derivation in `gmail/` or `graph/` that the other adapter also needs, it belongs in `mail/`.

Adapters (`internal/gmail` via the official SDK, `internal/graph` hand-typed over Graph REST, `internal/memory` for tests) only compile a coarse request and translate wire shapes. Two provider facts shape `Search`: Gmail's date operators are day-granular and `from:` matches display names, so the envelope re-checks those; Graph cannot combine `$search` and `$filter`, so phrases force `$search` and everything else is narrowed locally. Phrases are the one predicate left to the provider's full-text match in both — verifying them would mean fetching every candidate body. `has:attachment` is left to Gmail because a metadata envelope cannot see parts.

`internal/render` holds the rules that cost real failures to learn (see `docs/go-migration.md` for the list). They are provider-independent by design: `Convert(mail.Body)` is the single HTML→text entry (byte-identical to the Python it replaced), `Build` folds a conversation, and `Read` folds one message *exactly as `Build` would fold its turn* — never write a second pool definition in the CLI. `internal/norm` is the single text normaliser (Unicode, with byte offsets); `Coverage` and `prefixEnd` must agree on what "same text" means, so neither gets its own.

`internal/cli` is Cobra trees over a `Deps` struct; `internal/wiring` is the composition root (a third provider is one package plus one case there). JSON is the default output and `--text` the human form; `--raw` bypasses the processing layer everywhere (`docs/output-design.md`).

## The fold: verified, never assumed

A quoted tail is removed only when its lines already exist in *earlier* turns of the same conversation — earlier in conversation order, not by timestamp. Marker detection alone once destroyed a 6390-character Apple Mail forward that sat under the same `> ` a reply uses. Graph's `uniqueBody` (`ProviderFolded`) is a *hint* verified the same way, with the boundary found in the body's own bytes. A quote with no 40-char prose line is checked line by line, not waved through. `read` therefore always loads the conversation.

## The send gate

`send-mail` composes a complete `.eml` at draft time (go-mail; Bcc via `SetGenHeader` because go-mail never writes a Bcc header), previews from that file, pins its sha256, and `--commit` sends those bytes or nothing. Draft state is the single-use token: `pending → sending → sent|unknown`. `Claim` serialises the transition with a kernel `flock` — ownership, never file age, decides; a process that dies after claiming leaves `sending`, reported as `unknown` with a recovery instruction. Failures before transmission `Release` to pending; anything after is `unknown`. Gmail rewrites the Message-ID on send, so recovery searches by subject and time, not by the id we set.

## Tests: the rules are the tests

Tiers (`docs/go-design.md` §6): T1 adapter cassettes replay recorded raw JSON through the real adapters via `httptest`; one contract suite (`internal/mailtest`) runs against Gmail, Graph and memory. T2 drives the real Cobra trees over the memory adapter (`internal/cli/harness_test.go`) and asserts behaviour — tokens and relations, never exact output strings. T3 tables for pure rules. T4 `-tags live`.

Fixtures are the real writer's shape: `testdata/gmail/*.json` is verbatim API output loaded through `gmail.Translate` (`internal/fixtures` names each by what it proves). `testdata/graph` is doc-derived until tenant consent lands. Cassettes model provider semantics deliberately (Gmail newest-first paging, KQL subject stemming, `$expand` dropping `sourceUrl`); extend them loudly, never silently accept a query you cannot evaluate.

A behavioural finding is pinned with a red test before it is fixed; `internal/cli/review_test.go` is that record.

## State and config

`~/.config/mailkit/` holds `gmail-client.json`, `gmail-token.json`, `outlook.json` (or `MAILKIT_OUTLOOK_CLIENT_ID` / `_TENANT_ID`). Downloads land under `~/.local/state/mailkit/attachments/<message-id>/`, never the cwd; names are sanitised once, in `internal/attachments`. Drafts live beside them.

## Known state

Outlook is blocked on tenant admin consent, not code (`docs/outlook-status.md`); when it lands, `go test -tags live ./internal/graph -run Live -record` replaces the doc-derived cassettes. Graph expands one level of embedded message; deeper ones are marked truncated. Remote images are labelled by size (`pixel` / `small` / image), never filtered — fetching one tells the sender the mail was opened, and that is accepted.
