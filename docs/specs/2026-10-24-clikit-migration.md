# mailkit on clikit

Shipped 2026-10-24 on `main`, with every recommendation under **Decisions
for you** taken; its decisions live in `docs/go-design.md`. This is the
build record: what the proposal argued, the order the work landed in, and
what callers saw change. It followed the house standards
(`~/.claude-accounts/qiushiyan@planlab.ai/skills/go-cli/SKILL.md`,
`MIGRATE.md` beside it). Left to do outside this repository: commit
`[tools.mailkit]` in `~/dotfiles/twin/.config/twin/twin.toml`, then
`twin tools install mailkit` on the mini.

## Done when

- `mail-find` and `send-mail` run on clikit v0.3.2 (Kong v1.16.1), the latest
  tag; `spf13/cobra` and `spf13/pflag` leave `go.mod`.
- For every invocation a caller uses (the two skills, and every command the
  tools print themselves), stdout, stderr and the exit code are the bytes the
  Cobra build produced, except the differences listed under **What callers
  will see change**, each one approved.
- Every help page is clikit's, pinned under `cmd/<tool>/testdata/help/`, and a
  fresh agent given only the help pages completes the real tasks listed under
  step 4.
- `cmd/<tool>` only translates. Every rule the CLI layer holds today has one
  owning package below it.
- `make check` passes, `make install` has run, AGENTS.md follows the
  template, and mailkit is a twin tool.

## Where it stands

`internal/cli` holds both command trees as Cobra builders: `app.go` (761
lines, mail-find) and `sendmail.go` (300 lines, send-mail), over a `Deps`
struct. `internal/wiring` imports `cli` to fill that struct, and the two mains
are three lines each. Every failure, usage errors included, exits 1 with
`<command path>: <err>`. Help is Cobra's: positionals exist only in `Use`
strings, and send-mail's `Long` is a hand-aligned table of its modes.

The gate is green (`make check`, 41 s), `go fix -diff` prints nothing, and
startup costs 4.6 ms of package init, so nothing here is about speed.

Callers: `skills/read-email` and `skills/write-email`, which are linked into
`~/dotfiles/claude/.claude/skills/`, plus the commands the tools print in
`next_steps`, hints and compose output. `~/dev/gazette` and
`~/dev/notes/go-cli-frameworks-survey.md` mention the tools in prose only. No
script parses an exit code or an error line. mailkit is a twin repository
(`[repos.mailkit]`) but not a twin tool.

## Shape after the move

```
cmd/mail-find/
  main.go              the App, the root (--account, --text), the commands, main
  output.go            JSON shapes, text rendering, next_steps
  testdata/help/       golden help pages
  testdata/contract/   pinned stdout, stderr and exit codes
  *_test.go            the read-side T2 suite
cmd/send-mail/
  main.go, output.go, testdata/help/, testdata/contract/, *_test.go
internal/wiring        account → Mailbox, login, the account list; no longer imports cli
internal/paths         new: the config and state directories (see refactor 4)
internal/cli           deleted
```

The grammar moves into `cmd/<tool>` rather than staying in a shared
`internal/cli` because the two trees share almost nothing: the `--account`
flag, the opener and the clock. Their tests split along the same line. One
test crosses both binaries: `TestSend_ReplyJoinsTheConversationItAnswers`,
which composes a reply and then checks it with `mail-find thread`. Its thread
half becomes a check through `memory` and `render.Build`, and the contract
suite (`internal/mailtest`) already proves on every adapter that a send with
a parent joins the parent's conversation.

Two binaries stay. The split is the permission boundary: an agent can be
allowed `mail-find` and not `send-mail`.

## The command lines

### mail-find keeps its shape

Every invocation keeps working. The declarations:

- **Root:** `--account` takes `enum:"${accounts}" default:"gmail"`, with
  `${accounts}` set by `kong.Vars` from `wiring`, so adding a provider is
  still one case there. `--text` stays a root flag. Kong accepts a parent's
  flags after the subcommand, so `search q --text` still works, and
  `TestContract_RawAcceptedAfterSubcommandEverywhere` keeps holding it.
- **Commands:** `search QUERY [--limit] [--native]`,
  `resolve REF`, `read ID [--fetch-remote] [--out] [--raw]`,
  `thread ID [--raw]`, `context ID [--window] [--limit] [--min-score] [--raw]`,
  `attachments ID`, `fetch ID (--attachment NAME… | --all) [--out]`,
  `auth login`, `auth status`.
- **`fetch`:** `--attachment` and `--all` become a required `xor` group, so
  the hand-written "pick attachments with…" error disappears and help states
  the rule. `--out` gets `type:"path"`.

### send-mail: its modes become commands

```
send-mail compose --to A[,B] --subject S (--body B | --body-file F) [--cc] [--bcc] [--attach F]… [--format] [--sender] [--no-open] [--account]
send-mail reply ID [--all] (--body B | --body-file F) [--cc] [--bcc] [--attach F]… [--format] [--sender] [--no-open] [--account]
send-mail commit DRAFT-ID
send-mail list
```

Today one command carries three modes. `sendmail.go:60-75` refuses any flag
the active mode does not use by walking `Flags().Visit`, and help cannot
state that rule: the page lists fifteen flags as if they all combine. As
commands, the grammar carries the refusals. `commit` takes no flags that
could change the send, `reply` has no `--to` or `--subject`, `--reply` and
`--reply-all` become one `--all` switch, and the body source is a required
`xor` group that help prints under Constraints. The hand-written refusals in
`TestSend_FlagAndModeConflictsAreRefused` and
`TestSend_ReplyRefusesWhatWouldReaddressOrRetitleIt` become Kong's. Both
tests keep the assertion that a refused invocation sends nothing.

The cost is wording. "A reply goes to the original's sender…; drop --to, and
add other people with --cc" becomes `unknown flag --to` followed by reply's
synopsis. `reply`'s `Help()` says why the flags are absent.

The alternative, keeping `--commit` and `--list` as flags, would need a hook
that re-implements the `Visit` check, and help would still show every flag
as combinable. clikit exists to stop help from saying things the parser
does not do.

Flag details, verified with a scratch module against clikit v0.3.2:

- **`--to`, `--cc`, `--bcc`:** `[]string`. Kong splits on commas and also
  accepts the flag repeated. Each value is trimmed in `Run`: Kong keeps the
  space after a comma (`-t 'a@x, b@y'` yields `" b@y"`), which address
  validation would refuse.
- **`--attach`:** `sep:"none"`, so a comma inside a file name survives.
- **`--format`:** `enum:"markdown,text,html" default:"markdown"`.
- **`--body-file`:** a plain string, not `type:"path"`, because `-` means
  stdin.
- **Shared body flags:** an embedded struct used by both `compose` and
  `reply`. clikit accepts the same `xor` key on sibling commands.

### Exit codes

0, 1 for a failure, and 2 for invalid argv (clikit's default; today it is 1).
No codes of the tool's own: nothing branches on a status, and the skills tell
the agent to relay stderr.

### Bindings and seams

`Deps` and its `defaults()` are replaced by bindings in `App.Options`.
Anything that can fail loads through `kong.BindToProvider`, so `--help` never
touches a token:

- **`context.Context`:** the App binds `context.Background()`, `main` appends
  the `signal.NotifyContext` context, and tests append `t.Context()`. The
  later binding wins.
- **`mail.Opener`** (`func(ctx, account) (Mailbox, error)`, declared in
  `mail`): from `wiring`; tests bind one that returns `memory`.
- **`mail.Mailbox`:** a provider in mail-find built from the root's
  `--account` and the opener. send-mail binds the opener alone, because
  `commit` opens the account named in the draft record, not the one in a
  flag.
- **`paths.Dirs`:** the one value that says where live state is. The drafts
  store, the attachment directory and the token paths all derive from it.
  Tests bind temporary directories, so no test depends on `XDG_*` being
  unset.
- **The rest:** the clock (`func() time.Time`), `*images.Fetcher`, the
  previewer that opens the preview page, and login.

mailkit is the first clikit consumer that does network I/O and needs
cancellation. Binding the context is the tool's own job and needs no change
to clikit. If a second networked tool (slackkit, envoy) migrates, a
`RunContext` in clikit may then earn a place in its public surface.

## Refactor: rules the CLI layer holds move to their owners

Ordered by what is at stake:

1. **The send gate's sequence goes to `drafts`.** The order that makes a
   commit crash-safe lives in the CLI (`sendmail.go:94-145`): claim, open and
   verify the bytes, open the record's account, check the limit, send,
   finish, and release on every failure before transmission. The `drafts`
   package doc claims the gate, and `drafts.Finish` separately decides which
   errors release. `Store.Commit(ctx, id, open, now) (Receipt, error)` owns
   all of it. `Receipt` carries what the report needs: the record, the
   address it was sent as, the conversation the provider reported, and the
   parent. Each release and unknown edge gets a direct `drafts` test; the T2
   gate cases stay.
2. **Compose's derivation goes to `drafts`.** `sendmail.go:147-240` derives
   From (the account address or `--sender`) and the reply (Fetch, then
   `mail.NewReply`), creates the draft, and then the CLI itself parses the
   bytes and writes the `.html`. "The preview is rendered from the `.eml`"
   therefore lives half in the CLI. `Store.Compose(ctx, box, Request, now)
   (Draft, error)` writes the `.eml`, `.json` and `.html` together. The
   command opens the preview and prints the result.
3. **Attachment selection goes to `attachments`.** The rule from
   `docs/go-design.md` § The shared layer, that a name must be unique on the message or the
   error lists the handles, and the loop that streams parts into their
   allocated paths both live in the `fetch` command (`app.go:628-710`).
   `attachments.Select(parts, tokens)` and `attachments.Save(ctx, box, dir,
   parts)` take them. The package doc already claims where downloaded bytes
   land.
4. **One owner for where mailkit keeps files.** The config directory is
   computed twice with the same XDG logic (`internal/gmail/auth.go:32`,
   `internal/graph/auth.go:24`). The state directory lives in
   `internal/attachments/attachments.go:75`, and `drafts` imports
   `attachments` only to find it. The new `internal/paths` owns `Dirs`,
   which the binding above carries.
5. **One owner for the login command.** It is written out five times in
   two forms: `internal/gmail/auth.go:44` has no `--account`, while
   `internal/graph/auth.go:22`, `internal/graph/graph.go:61`,
   `internal/drafts/drafts.go:186` and `internal/cli/sendmail.go:299` include
   it. Once the grammar is generated, renaming a flag would leave these
   strings stale with no failure. `mail.LoginCommand(account)` owns it, and
   `TestPrintedCommandsParse` (below) checks it.
6. **Message references go to `mail`.** `resolve` decodes a `message://`
   link and trims brackets inside the command (`app.go:406-414`). That is
   the input rule of the Identify operation, and
   `TestContract_ResolveDecodesAnyPercentEncoding` pins a review finding
   about it. `mail.ParseRef(s) (MessageID, error)` takes it, with a T3
   table.
7. **Tests go to their owners.** `render` and `norm` have no tests of their
   own: every fold rule is proven through the CLI. `Split`, `Coverage`,
   `FoldOne`, `Tidy` and `prefixEnd` get T3 tables in `render`, and
   `norm.Fold` gets an offsets table. T2 keeps the end-to-end rules: `read`
   loads the conversation, a forward survives, and `uniqueBody` is verified
   rather than trusted. The T2 files move anyway, so this costs little now.
8. **Scenario constructors go to `internal/fixtures`.** `msg`, `day`, `para`
   and `ticket` leave `internal/cli/harness_test.go`, because both `cmd`
   packages need them.
9. **Small items.** Declare the `NativeSearch` capability as a named
   interface in its consumer (`app.go:150`). `drafts.HumanSize` is used by
   mail-find's text output and belongs beside `render`'s other text for
   people. Replace `sort.*` with `slices` (`internal/memory/memory.go:118`,
   `internal/drafts/drafts.go:434`) and `strings.LastIndex` with
   `strings.CutLast` (`internal/images/images.go:94`). Delete the
   `var _ = context.Background` placeholder
   (`internal/mailtest/contract.go:291`).

Left as they are: the port, both adapters, the rules inside `render`,
`cluster`, and the markdown compile. Each already sits with its owner.

## What clikit makes testable

- **`TestPrintedCommandsParse`:** every command line the tools print
  (`next_steps`, compose's commit line, the unknown-outcome search, login
  hints) is parsed against its App's grammar. Today those strings are spread
  across four packages, and nothing fails when one goes stale.
- **`TestTheJSONIsDescribed`** (claude-steps' pattern): each mail-find
  command's help carries a JSON section that names every key its output
  holds. The field documentation now in `skills/read-email/SKILL.md` moves
  there, so the skill can point at `--help`.
- **Golden pages per binary** through `clikittest.Golden`.

## Sequence

Commits on `main`, as one body of work. Each step is what makes the next one
checkable.

1. **Pin today's behaviour.** `cmd/mail-find/contract_test.go` and
   `cmd/send-mail/contract_test.go` drive today's `cli.MailFind` and
   `cli.SendMail` over the memory adapter in a temporary home. They record
   stdout, stderr and the exit code under `testdata/contract/`, with the
   update switch `MAILKIT_CONTRACT_UPDATE=1`, kept apart from `make golden`.
   Temporary paths and the draft id's random suffix are normalised. The
   cases cover every example in both skills, every command the tools print,
   and the error shapes: an unknown operator, not found, `ErrAuth`, too
   broad, and each send refusal and outcome state.
2. **Move the rules down** (refactor 1–9). The pins replay byte for byte.
3. **Switch the grammar.** Run `go get github.com/qiushiyan/clikit@v0.3.2`,
   put the Apps in `cmd/`, delete `internal/cli`, and port the T2 harness to
   `clikittest.Run` with appended bindings. Then `go mod tidy`, which drops
   cobra and pflag. The pins are re-recorded, and the diff contains only the
   changes listed below.
4. **Write the help.** Each `Help()` starts from the old `Long` text and the
   skills' field documentation. Sections: Examples per command; the search
   grammar on `search`; JSON on every mail-find command; the gate's states
   on `commit` and `list`. Run `make golden`. Then give a fresh agent only
   the help pages and these tasks: find an invoice from last month, read a
   thread, fetch a nameless inline image, reply to all with one extra Cc,
   commit it, and recover from an unknown outcome. Fix whatever it gets
   wrong.
5. **Update the callers in the same pass.** Every command line in
   `skills/write-email`; `skills/read-email` only where the help now owns
   what it repeats; compose's printed line. Record the differences in the
   design doc, with the line the migrated tools carry: how help renders,
   how an argv error reads and which errors are usage errors belong to
   clikit, and a change there is a clikit release plus a `go.mod` bump.
6. **Repository shape.** In the Makefile: `golden` runs only the help tests
   with `CLIKIT_UPDATE=1`; `check` gains the gofmt test; `install` uses
   claude-steps' `mktemp`-and-rename for each binary. AGENTS.md follows
   `AGENTS-TEMPLATE.md`, with `CLAUDE.md` a symlink to it. Its architecture
   tour and commands list move to the design doc, and README.md, which does
   not exist yet, is written. This is the docs pass you run yourself; it is
   listed here because the template is one of the standards.
7. **Install and register.** Run `make install`, add `[tools.mailkit]
   method = "make-install"` to `~/dotfiles/twin/.config/twin/twin.toml`,
   then `twin tools install mailkit` on the mini.

## What callers will see change

Each needs your approval:

1. **Help pages:** every page is clikit's, with a synopsis, Constraints
   and Exit codes sections.
2. **Failure line:** `mail-find fetch: <err>` becomes
   `mail-find: error: <err>`, so the subcommand is no longer named.
   `TestSurface_ErrorsNameTheSubcommand` is replaced.
3. **Usage errors** exit 2 instead of 1, in Kong's wording, followed by the
   synopsis and a pointer to `--help`.
4. **send-mail's grammar,** as above. Compose prints
   `send-mail commit <id>`, and a bare `send-mail` lists the four commands
   (`TestSurface_BareSendMailListsItsThreeUses` is rewritten).
5. **Inline values that start with `-` need `=`.** `--body "- milk"` is
   refused with exit 2 and Kong's "perhaps try --body=…", where pflag
   accepted it, and a markdown list starts exactly that way. The `--body`
   help says so. The skill already prefers `--body-file` and stdin.
6. **Prescriptive refusals become Kong's:** `--format rtf`, flags given to
   `commit`, and `--to` given to `reply` (Kong adds "did you mean
   \"--cc\"?").
7. **Bare `mail-find`** printed the root help and exited 0; it is now a
   usage error that lists the commands, exit 2, as every clikit tool's is.
8. **Gmail's login hint** names `--account gmail`, as Outlook's always
   named its account (one owner, `mail.LoginCommand`).
9. **Two argv shapes that used to pass are refused:** `fetch` with both
   `--attachment` and `--all` (`--all` used to win), and `compose
   --subject " "` (used to draft a subject-less message).

## Found along the way

- **A kept quote's reason was inverted.** `fold_rejected` gave the share
  of quoted lines that *are* upstream as the share that are not, so a quote
  found nowhere read "0% … are not in earlier turns". Pinned by
  `TestFoldOne_FoldsOnlyWhatThePoolRepeats`, fixed, and a contract pin
  added for a kept quote.
- **A commit's login hint named the wrong account.** It used the
  `--account` flag's value, which a commit never takes; it now names the
  account the draft was composed on.

## Kept on purpose

- **JSON is mail-find's default and `--text` the human form**, rather than
  the house `--json` opt-in. Its reader is a model, and the processed form
  has to be the default (`docs/output-design.md` Rule 1). AGENTS.md records
  this as an invariant.
- **send-mail's output stays text.** Nothing has failed because it is text.
- **Dependencies beyond clikit and Kong** (the Google API, azidentity,
  go-mail, goldmark, `x/net/html`, `x/oauth2`, `x/sync`) each earned their
  place in `docs/go-design.md` § Dependencies.

## Decisions for you

1. **send-mail as four commands.** Recommended; the alternative is
   argued above.
2. **Their names.** `compose`, `reply`, `commit` and `list` match the
   vocabulary the docs and skills already use.
3. **No default command.** `default:"withargs"` on `compose` would keep
   `send-mail --to …` working, but `--commit` and `--list` break either way.
   With no default, each act has a page of its own, and bare `send-mail`
   names all four. Recommended.
4. **clikit's error line.** It drops the subcommand name. Recommended,
   because it is the house line and the agent knows which command it ran.
