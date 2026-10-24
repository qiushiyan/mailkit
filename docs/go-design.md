# mailkit design

mailkit lets an agent read the person's mail and send mail only after the
person has seen it, as two binaries: `mail-find` reads and changes nothing,
`send-mail` drafts and sends. The split is the permission boundary: an agent
can be allowed to look things up without being able to send as the person.
This doc is for changing mailkit: which package owns each rule, the command
line, the send gate, and how the tests hold them. The rules themselves were
each bought by a real failure, and the docs that argue them are the ones to
read before changing one:

- **What the output promises, and why the processed form is the default:**
  `docs/output-design.md`
- **Folding a reply chain, and `context` across senders:**
  `docs/threading-and-context.md`
- **What reaches the model, and the image labels:**
  `docs/what-the-model-sees.md`
- **Markdown bodies and their refusals:** `docs/markdown-compose.md`
- **Outlook's blocker and its first-contact checks:**
  `docs/outlook-status.md`
- **Records of why the seam sits where it does, and why Go:**
  `docs/operations.md`, `docs/go-migration.md`

## Dependencies

Use a library where it is better tested than what we would write; stdlib
where the job is genuinely small.

| need | choice | why |
|---|---|---|
| CLI | `qiushiyan/clikit` over `alecthomas/kong` | the house command line: help rendered from the declaration, and argv errors and exit codes shared with the other `~/dev` tools; Kong's grammar has the flags-anywhere, nested commands and xor groups both binaries use |
| Gmail | `google.golang.org/api/gmail/v1`, `golang.org/x/oauth2` | official; `option.WithHTTPClient` for cassette tests |
| Graph | hand-typed `net/http` over five endpoints, `azidentity` + `azidentity/cache` | the SDK was measured affordable (9.8 MB, 2 s cached build) but its models do not express the MIME form of `sendMail`, and the gate requires the prepared bytes to go out unchanged; five endpoints are small to type by hand |
| MIME for send | `wneessen/go-mail` v0.8 | `Msg.WriteTo` emits RFC 5322 without SMTP; attachments verified byte-exact in a spike. **Never writes Bcc** (`msgwriter.go:121`) — set it via `SetGenHeader`; `WithNoDefaultUserAgent()` |
| markdown | `yuin/goldmark` | CommonMark with no transitive dependencies; one AST renders both parts (`docs/markdown-compose.md`) |
| HTML parsing | `golang.org/x/net/html` **Tokenizer** | a port of our converter on it is byte-identical to the Python it replaced on ten real bodies; the tree-building `Parse` is not the right primitive |
| fan-out | `golang.org/x/sync/errgroup` | first error cancels siblings; `WaitGroup.Go` cannot |
| JSON | `encoding/json/v2` deliberately | strict decoding of cassettes and API payloads |
| stdlib | `image.DecodeConfig`, `crypto/sha256`, `net/mail`, `mime/multipart` | small |

Rejected: `jaytaylor/html2text` drops `<img>` unless it is an `<a>`'s only
child — the exact "images vanish" failure our converter fixes. cobra gives a
positional argument nowhere to carry a description and states no flag
constraint in help, so send-mail's mode rules were hand-written and
invisible on its page.

## The port

```go
package mail // domain types and the port; adapters import this, never the reverse

type Mailbox interface {
    Account(ctx) (Account, error)                           // live proof the token works; ErrAuth carries the login command
    Resolve(ctx, id MessageID) (Envelope, error)            // RFC822 Message-ID → envelope; ErrNotFound
    Search(ctx, c Criteria, limit int) ([]Envelope, error)  // EXACT: every hit satisfies c; len == limit or the mailbox is exhausted
    Fetch(ctx, id string) (Message, error)                  // one call: envelope, body, parts
    Conversation(ctx, convID string) ([]Message, error)     // ascending by Received; paginated inside
    Open(ctx, h Handle, w io.Writer) error                  // StoredPart only; ErrNoBytes otherwise
    Send(ctx, p *Prepared, parent *Parent) (Sent, error)   // the prepared bytes, encoded for the provider; ErrTooLarge, ErrNotSent
}

type Opener func(ctx, account string) (Mailbox, error) // account name → adapter; wiring implements it, tests bind memory

type Account struct{ Address string; SendLimit int64 } // bytes of the prepared message
type Parent  struct{ ID, ConversationID string; MessageID MessageID } // the message a reply answers; nil starts a conversation
type Sent    struct{ ID, ConversationID string }       // what the provider reported; empty where it says nothing

type Criteria struct {                // conjunction; zero fields unconstrained
    Phrases      []string             // exact, anywhere in the message
    SubjectTerms []string             // any-of
    From, To     string               // address or bare domain
    HasAttachment bool
    After, Before time.Time           // on Received; zero = unbounded
}
func (c Criteria) Match(e Envelope, text string) bool // the one definition of "matches"; the memory adapter IS this
func (c Criteria) MatchEnvelope(e Envelope) bool       // Match without the phrases: what an adapter narrows a listing with

type Envelope struct {
    ID, ConversationID string
    MessageID          MessageID   // RFC822
    Received           time.Time   // provider's canonical timestamp; ordering key; always zoned
    DateHeader         string      // as sent; display only
    From               Address
    To, Cc             []Address
    Subject, Snippet   string
    HasAttachments     bool
    ReplyTo            []Address   // where the sender asked replies to go
    InReplyTo, References []MessageID // its own threading headers, as sent
}

type Message struct {
    Envelope
    Body           Body    // HTML, or Text only when the message had no HTML part
    ProviderFolded *Body   // Graph uniqueBody, verbatim; nil when absent; a hint the fold verifies, never output
    Parts          []Part  // document order
}

type Part struct {
    Name      string      // verbatim from the provider, or synthesised from Content-ID; unsanitised
    MIME      string
    Size      int64
    Inline    bool
    ContentID string
    Content   PartContent // sealed: exactly one of the three
}
type PartContent interface{ isPartContent() }
type StoredPart   struct{ Handle Handle }          // bytes in the message; Open works
type LinkedPart   struct{ URL string }             // referenceAttachment; no bytes here
type EmbeddedPart struct{ Item *Message; Truncated bool } // itemAttachment; one level expanded, deeper ones Truncated
```

Invariants: `Search` is exact on every adapter — the adapter compiles a
coarse provider query, pages, fetches what its residual predicates need, and
narrows until it has `limit` matches or the provider is exhausted. `Send`
with a parent puts the message in the parent's conversation, addressed
exactly as its headers say, or refuses with `ErrNotSent` before a byte
leaves. `Parts` is document order. An unknown Graph `@odata.type` is a
loud error. Errors are sentinels (`ErrAuth`, `ErrNotFound`, `ErrNoBytes`,
`ErrTooLarge`, `ErrNotSent`) wrapped with provider and operation. Adapters
are registered in the composition root (`internal/wiring`), one explicit
case each, so a third provider is one package plus one case there.

**The port owns every invariant it states; adapters never re-implement
one.** `mail.Narrow` is the exact-search walk (page, narrow with
`MatchEnvelope`, stop at limit or exhaustion, `ErrTooBroad` past 1000 rows).
`mail.Wrap` labels errors once at an adapter's public boundary, and inner
helpers return plain errors. `mail.HasAttachments` is the one definition of
"has an attachment" (a non-inline part), and `mail.LoginCommand` the one
command that recovers from `ErrAuth`. A loop, an error literal or a
derivation that both adapters need belongs in `internal/mail`.

What the adapters hide: Gmail `q=` compilation and `rfc822msgid:`; Graph's
`$search`/`$filter` choice and local narrowing; paging; MIME walking and
base64url; `attachmentId` keying; Content-ID naming; OData type mapping;
how each provider places a reply (`threadId`, Graph's reply actions); auth
and token storage. Two provider facts shape `Search`. Gmail's date
operators are day-granular and its `from:` matches display names, so the
envelope re-checks both. Graph cannot combine `$search` and `$filter`, so
phrases force `$search` and everything else is narrowed locally. Phrases
are the one predicate left to the provider's full-text match in both,
because verifying them would mean fetching every candidate body;
`has:attachment` is left to Gmail because a metadata envelope cannot see
parts.

## The shared layer

```
cmd/mail-find, cmd/send-mail   the clikit Apps: grammar, help pages, output; one call into internal/ per command
internal/mail                  port, types, Criteria.Match, the search grammar → Criteria, ParseRef, reply derivation, sentinel errors
internal/gmail, internal/graph adapters, taking their config directory from the caller
internal/memory                test adapter: []Message + part bytes; Search = filter by Criteria.Match
internal/render                HTML → text AND remote-image references in one parse; tidy; quote split; coverage; transcript
internal/norm                  the one definition of "the same text", with byte offsets
internal/cluster               identifier extraction, probes, scoring, too-common
internal/images                remote fetch with deadline; classify by min edge and area
internal/attachments           which parts a fetch takes; their names on disk (sanitised, -2 suffixes, contained); the stream into them
internal/drafts                the send gate: Draft and Commit, the .eml, the preview from it, the state machine
internal/paths                 the config and state directories, one value tests replace
internal/wiring                the composition root: account name → adapter, sign-in, the account list
```

Rules carried, and where each lives:

- **Fold only what is verified upstream.** `render.Build` compares each
  quoted block's prose lines to earlier turns; < 80 % keeps it, recorded in
  `fold_rejected`. `read` loads the conversation and applies the same
  check through `render.Read`, which folds one message exactly as `Build`
  folds its turn, so there is one pool definition; if the conversation
  cannot be loaded or holds no earlier copy, the quote stays.
  `ProviderFolded` is a candidate boundary checked the same way, found in
  the body's own bytes. `render.Convert(mail.Body)` is the single HTML→text
  entry, and `norm` the single normaliser: `Coverage` and `prefixEnd` must
  agree on what "same text" means, so neither gets its own.
- **`--raw`** = converted text with no fold, tidy, `ProviderFolded`, or
  context filtering. Conversion runs in both branches. HTML is not what
  `--raw` means.
- **Search grammar.** `internal/mail` parses the portable subset the model
  already writes (`from:`, `to:`, `subject:`, `has:attachment`, `after:`,
  `before:`, `newer_than:`, `older_than:`, quoted phrases, bare words →
  phrases) into `Criteria`; unknown operators are an error naming the
  grammar. `--native` passes the string to the provider untouched, is
  tagged in the output as provider-specific, and is tested on Gmail only.
- **Too-common.** Identifier probes request at least 26 post-match hits
  regardless of `--limit`; > 25 is reported as "more than 25", never a
  truncated count.
- **Fetch by name** (`attachments.Select`) succeeds only when the name is
  unique on the message; otherwise an ambiguity error listing handles.
  Destinations are allocated as a batch before any file opens.
- **Nudges:** a result names the command that recovers what it could not
  return, and each nudge has a skip condition (`docs/output-design.md`).
- **Where files live** (`internal/paths`): credentials under
  `$XDG_CONFIG_HOME/mailkit` (`~/.config/mailkit`); drafts and downloads
  under `$XDG_STATE_HOME/mailkit` (`~/.local/state/mailkit`), never the
  working directory.

## The command line

Each binary is a `clikit.App` declared in `cmd/<tool>/main.go`: Kong structs
are the grammar, `help.go` holds each command's prose, and mail-find's
`output.go` holds its JSON shapes and text forms. A Run method translates:
flags in, one call into `internal/`, output out.

- **Bindings are the seams.** Dependencies arrive through `App.Options`: the
  account opener from `wiring`, `paths.Dirs`, the clock, the image fetcher,
  the previewer, and a context `main` cancels on interrupt. mail-find's
  mailbox is a provider over `--account`, so a command that never asks for
  one never opens an account, and `--help` never reaches a credential.
  `resolve` parses its reference before opening the account, inside Run.
  A test appends its own binding of the same type to a copy of the options;
  the later binding wins.
- **Output is a contract.** mail-find prints one JSON document by default
  and the human form with `--text`, because its reader is a model and the
  processed form has to be the one it gets unasked. Each command's help
  page names the JSON keys. send-mail prints text: labelled lines, then the
  command to run next.
- **Every printed instruction runs.** `next_steps`, error recoveries,
  compose's commit line, the login hint, the help examples and the skills'
  examples are command lines an agent will run, and the tests parse each
  against the grammar.
- **send-mail's acts are commands:** `compose`, `reply ID [--all]`,
  `commit DRAFT-ID`, `list`. Each takes only what it uses, so a flag that
  cannot change the send (any flag on `commit`, `--to` on `reply`) is
  refused by the parser and stated by help. A mode flag on one command
  could be refused only by a hook that help cannot describe.
- **What clikit owns:** how help renders, how an argv error reads, and
  which errors are usage errors (exit 2, with the synopsis and a pointer to
  `--help`) belong to clikit (`~/dev/clikit`). A change there is a clikit
  release plus a `go.mod` bump, reviewed through the golden help pages.
  Every other failure exits 1 as `<tool>: error: <err>`; neither tool adds
  codes of its own, since no caller branches on one.

A flag value that starts with `-` has to be written `--flag=VALUE`, because
Kong reads `--body "- milk"` as a short flag; compose's help says so.

## The send gate

The reviewed bytes are the sent bytes, literally. `drafts.Draft` and
`drafts.Commit` are the gate's acts, and each runs its whole sequence, so no
caller can write a draft without its preview or send one without the
claim, the checks and the recorded outcome:

1. `compose` and `reply` call `Draft`, which takes the sender from the
   account (or `--sender`), derives a reply from the original, and builds
   the complete RFC 5322 message with go-mail — recipients incl. Bcc
   header, subject, body, attachment bytes, a deterministic `Message-ID`.
   It writes `<id>.eml`, `<id>.json` (state, account, sha256 and size of
   the `.eml`, and for a reply the original it answers) and `<id>.html`.
   The body is markdown by default, compiled before the bytes are pinned
   into a text+HTML `multipart/alternative`; `--format text|html` sends one
   part verbatim. The compile rules and their refusals:
   `docs/markdown-compose.md`.
2. The preview is rendered by parsing the `.eml`, not from the flags; a
   reply's leads with the original it answers.
3. `commit` calls `Commit`: claim the draft, refusing unless it is
   `pending`; verify sha256 and, for a reply, that the record's original is
   the message the bytes' `In-Reply-To` names; open the account the draft
   was written on; check the limit; `Send`, with the record's parent; move
   to `sent` with the provider id. The move to `sending` happens *before*
   any network I/O. Every failure before `Send` returns the draft to
   `pending`, and so does a refusal marked `ErrNotSent` (or `ErrTooLarge`,
   `ErrAuth`); any other failure after the provider may have accepted
   leaves `unknown`, never retryable silently. Recovery is by subject and
   time: the live send showed **Gmail rewrites the Message-ID**
   (`…@mail.gmail.com`), so the deterministic id is not a handle there; the
   provider id is stored once known.
4. `Claim` serialises the move with a kernel `flock`: ownership, never file
   age, decides. A process that dies after claiming leaves `sending`, which
   the next commit reports as `unknown` with the search that checks it.
5. `SendLimit` is checked against the `.eml`'s size, not attachment sums.
6. Draft id: `<yyyymmdd-hhmmss>-<slug>-<4 hex>` — readable for the model to
   type back, collision-free within a second.

`Prepared` is a constructor-backed type exposing a reader, digest and size;
`Send` transmits nothing else. Gmail encodes it as `raw`; Graph posts it as
MIME.

### Replies

`send-mail reply ID` (with `--all`, reply-all) answers the message
`mail-find` returned as `ID`, inside its conversation. A reply that only
looks threaded is the failure this exists for: a new message under a `Re:`
subject is grouped by subject in Apple Mail, while Gmail files it as a
thread of its own and a helpdesk cannot attach it to its ticket.

Everything a reply derives comes from `mail.NewReply`, used by the gate and
by the contract suite alike:

- **Recipients:** the original's Reply-To, else its sender; a message the
  account sent itself is answered to its own recipients. Reply-all adds
  everyone else on it, never the account. Reply-to-sender is the default
  because an extra recipient cannot be unsent, while a missing one shows in
  the preview and `--cc` adds it.
- **Subject:** the original's with `Re: ` once. Gmail threads only on a
  matching subject, so `reply` takes no `--to` and no `--subject`.
- **Headers:** `In-Reply-To` names the original; `References` continues
  its chain (RFC 5322 §3.6.4).
- **Thread:** the provider's placement (`mail.Parent`) lives on the draft
  record and the `Send` call, never in the `.eml`, so a commit sends into
  the conversation that was previewed, and `Store.Open` refuses a record
  whose original disagrees with the bytes. The commit reports the
  conversation the provider says it used, and names `mail-find thread`
  when that differs or the provider does not say.

How each provider places it:

- **Gmail** files a sent message in a thread only when the request carries
  the `threadId`, the message's `In-Reply-To`/`References` name a message
  in that thread, and the subjects match; otherwise it silently starts a
  new thread. The send response names the thread it used.
- **Graph** threads by Thread-Index, not by the RFC headers, so `sendMail`
  with them starts a conversation of its own, and `createReply` needs
  `Mail.ReadWrite`, which the app does not request. Replies go through
  `/messages/{id}/reply` or `/replyAll`, which take the MIME bytes under
  `Mail.Send` but address the reply themselves — the original's sender, or
  everyone on it — and the reference does not say whether the MIME To/Cc
  count too. `replyAction` sends only when every reading reaches exactly
  the previewed recipients and refuses the rest with `ErrNotSent`; on
  Outlook that refuses an extra `--cc`, any `--bcc`, an original with a
  separate Reply-To, and a reply to one's own message until first contact
  settles the question (`docs/outlook-status.md`).

Quoting the original body is out of scope: a reply carries what the author
writes.

## Tests

Tiers, each with one job:

- **T1 adapter cassettes.** `httptest.NewTestServer` replays recorded raw
  API JSON into the real adapters. Assert the golden `Message` and the
  *decoded* request predicates (method, path, parsed `q`/`$filter`/
  `$search`, paging), never URL strings. One **adapter contract suite**
  (`internal/mailtest`) runs against Gmail, Graph (doc-derived cassettes,
  labelled), and memory. Cassettes model provider semantics deliberately
  (Gmail newest-first paging, KQL subject stemming, `$expand` dropping
  `sourceUrl`); extend them loudly, never silently accept a query they
  cannot evaluate.
- **T2 behaviour through the command line.** Each binary's App, run by
  `clikittest` exactly as `main` runs it, over the memory adapter, a
  temporary `paths.Dirs` and a fixed clock (`cmd/<tool>/harness_test.go`).
  Scenarios are built with `fixtures.Msg` and the recorded fixtures, so a
  type change breaks compilation, not fixtures. Assert tokens and relations.
- **Contract pins.** What callers read, byte for byte: every invocation the
  skills show, every command the tools print, every error shape, under
  `cmd/<tool>/testdata/contract` (`TestContracts`). `make contract`
  re-records them, apart from `make golden`, so accepting a help change can
  never rewrite a contract; a pin's diff is a change every caller sees.
- **Help pages.** Golden under `cmd/<tool>/testdata/help` (`TestHelpPages`,
  with an opener that fails if help reaches an account).
  `TestTheJSONIsDescribed` requires each mail-find command's JSON section to
  name every key its output holds; `TestPrintedCommandsParse` parses every
  command line the pins, the help pages and the skills hand an agent.
- **T3 tables** for pure rules at their owners: the fold and tidy rules in
  `render`, offsets in `norm`, classification boundaries, identifier
  extraction, sanitisation and selection, reply derivation, the gate's
  outcomes in `drafts`. T2 proves the wiring.
- **T4 live** behind `-tags live` (`make test-live`), with `-record`
  writing cassettes.

| rule | tier | the assertion |
|---|---|---|
| fold verified upstream | T3 + T2 | tenancy thread: every prose line absent from a turn exists in an earlier turn (computed); transcript < raw. Forward: `read` contains the forwarded body. **Unrecognised forward** (Re: subject, unique quoted prose, no marker): kept, `fold_rejected` gives the share missing upstream. **Bad `uniqueBody`** (removed text not upstream): not trusted |
| labelled identifiers | T3 + T2 | table; `signals.identifiers` excludes the PO box and link id |
| > 25 too common | T2 | 26 sharing a number → dropped and named; 25 → kept; `--limit 10` still detects 26; `--raw` keeps |
| weak probes bounded | T2 | same-domain and subject-only messages placed one day inside and one day outside the window: included / excluded |
| min edge and area | T3 + T2 | table; a `--fetch-remote` result carries the label |
| attachmentId keying | T1 + T2 | golden has the nameless CID part, inline; `fetch` by its handle writes it |
| sanitise | T3 + T2 | `../../../etc/passwd` → `etc-passwd`; every written path is inside the out-dir by `filepath.Rel`; duplicate names get `-2` |
| processing default, `--raw` | T2 | `read --raw` == converted text; `thread --raw` contains every body; `context --raw` ≥ default; `--raw` after the subcommand on all three |
| recovery command | T2 | `attachments` on remote-only message names `read <id> --fetch-remote`; absent otherwise; `ErrAuth` names the login command; every such command parses |
| send gate | T3 + T2 | bytes the fake `Send` receives parse back with every recipient incl. Bcc and every attachment hash; edited `.eml` refused; second commit refused; concurrent commits → one send; crash between `sending` and `sent` → `unknown`; each failure before transmission → `pending` |
| reply derivation | T3 | recipients (sender, Reply-To, own message, reply-all minus the account, each once); `Re:` once; `References` continues the recorded tenancy chain |
| reply in the gate | T2 | `reply` → `commit`: the parent reaches `Send`, the bytes carry the threading headers, the reply is a turn of the original's conversation; a record whose original disagrees with the bytes is refused; `--to`/`--subject` refused; the commit names a conversation the provider moved it to |
| reply threading | T1 contract | on memory, Gmail and Graph, a send with a parent joins the parent's conversation; Gmail's cassette files by the threads guide's criteria, Graph's by the reply action's documented recipients |
| markdown compose | T3 + T2 | both renderings of one AST carry the same tokens; raw HTML / images / non-mail links / renders-to-nothing refused naming the escape; sent tree asserted with parentage (alternative under mixed, plain before HTML); `--format text` bytes survive untouched |
| what callers read | pins | each command's output, error and exit code, byte for byte |

A behavioural finding is pinned with a red test before it is fixed;
`cmd/mail-find/review_test.go` and `cmd/send-mail/review_test.go` are that
record.

Fixtures: raw Gmail JSON captured from the live API (tenancy thread
`19fd6b394c784f9b`, forward `1989fc5ed9469c6c`, IKEA run incl.
`1a02367a18eae254`, remote-only `1a02340ea531858a`, nameless CID
`1a01c98dd4e47691`), loaded through `gmail.Translate`; verbatim, so the
repository stays private. `testdata/graph` is doc-derived until tenant
consent lands.

## Known limits

Outlook is blocked on tenant consent, not code (`docs/outlook-status.md`):
Graph stays doc-derived until it lands, `uniqueBody` agreement has no
fixture until a recording exists, and Outlook refuses the replies whose
recipients its reply actions might choose differently (§ Replies). Graph
expands one level of embedded message; deeper ones are marked truncated.
`LinkedPart` is surfaced, not followed. The Gmail OAuth client is in
Testing mode, so its token expires every 7 days and `mail-find auth login`
renews it.
