# Working on mailkit

mailkit lets agents read the person's mail (`mail-find`) and send mail only
after the person has seen it (`send-mail`); agents reach both through the
`read-email` and `write-email` skills in `skills/`. The two binaries are on
purpose: being allowed to look things up must not also grant sending as the
person. `docs/go-design.md` owns the design; read it before changing
behaviour.

- One maintainer's tool: commit on `main`, push at milestones, and keep
  changes proportional to demonstrated failures or requested features.
  Dependencies: the standard library, clikit and Kong, plus the provider
  SDKs, go-mail, goldmark and `x/net/html` that `docs/go-design.md`
  § Dependencies argues for.
- **Contracts other repositories depend on:** the skills, linked into
  `~/dotfiles/claude/.claude/skills/` by `make skills`, run these commands
  and read mail-find's JSON and `next_steps`, send-mail's compose lines and
  commit report. Pinned byte for byte by `TestContracts`
  (`cmd/<tool>/testdata/contract`, re-recorded by `make contract`);
  `TestTheJSONIsDescribed` holds the help pages to the JSON, and
  `TestPrintedCommandsParse` parses every command line the tools, their
  help and the skills hand an agent. A change to one updates the skills in
  the same pass.
- **The repository holds verbatim personal mail** as fixtures, so it stays
  private.
- **The port owns every invariant it states; adapters never re-implement
  one.** A loop, error literal or derivation that both adapters need belongs
  in `internal/mail`, beside `Narrow`, `Wrap`, `HasAttachments` and
  `LoginCommand`. Held by the contract suite in `internal/mailtest`, run
  against Gmail, Graph and memory.
- **A fold is verified, never assumed.** A quoted tail is removed only when
  its lines exist in earlier turns of the same conversation, in conversation
  order; Graph's `uniqueBody` is a hint checked the same way, and `read`
  always loads the conversation. Marker detection alone once destroyed a
  6390-character forward. Held by `render`'s tables and
  `TestRead_ForwardSurvivesWhole`.
- **The bytes previewed are the bytes sent, once.** `drafts.Draft` and
  `drafts.Commit` each run their whole sequence, so no caller can write a
  draft without its preview or send one without the claim and the recorded
  outcome. Held by `internal/drafts/commit_test.go` and
  `cmd/send-mail/send_test.go`.
- **A behavioural finding is pinned with a red test before it is fixed;**
  `cmd/*/review_test.go` are that record.
- Tests never touch live state: the memory adapter stands in for Gmail and
  Graph, a temporary `paths.Dirs` for `~/.config/mailkit` and
  `~/.local/state/mailkit`, a local server for remote images, and help pages
  run with an account opener that fails if called. Only `make test-live`
  reaches the real accounts (`RECORD=1` re-records cassettes); Outlook is
  blocked on tenant consent (`docs/outlook-status.md`).
- Ship: `make check`, then `make install`. Callers run
  `~/.local/bin/mail-find` and `~/.local/bin/send-mail` and keep the old
  build until then; the mini keeps its own until `twin sync mailkit`.
- The command line runs on clikit: how help renders and how argv errors read
  are clikit's (`~/dev/clikit`). Help, results and errors an agent reads
  follow `~/dotfiles/claude/.claude/skills/prompt-engineering/SKILL.md`;
  library questions go to `~/.agents/skills/find-docs/SKILL.md`.

Docs follow `~/dotfiles/docs/documentation-standards.md`, paths written from
the repository root.
