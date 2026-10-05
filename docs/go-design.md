# The Go rewrite: design

Settled 2026-08-22 after three independent seam designs, two spikes, and a
Codex consult (`~/.local/state/envoy/jobs/go-migration-design-909f8956/20260822-024753-consult`).
This is the spec the build follows. It rests on `go-migration.md` (why),
`operations.md` (the seam's evidence), `output-design.md` (the CLI rules).

## 1. Where the project lives, and how the Python falls away

In place, on this repo. `~/dev/MailKit` and `~/dev/mailkit` are the same
directory on macOS's case-insensitive filesystem, so the "new blank project"
was never a separate place; and the commit log (`e5367f9 Restore
fetch_attachment…`) is the evidence the rewrite rests on.

1. `git mv src bin scripts reference/python/` in one commit.
2. Go lands at the root: `go.mod` (go 1.27.0), `cmd/`, `internal/`, `testdata/`.
3. Symlinks in `~/.local/bin` repoint when the Gmail live check passes.
4. `reference/python/` is deleted in the commit that records that pass.

The Go binary does its own OAuth with the existing `client_secret.json`;
token at `~/.config/mailkit/`. One re-login; the 7-day Testing-mode expiry
carries over.

## 2. Dependencies

Use a library where it is better tested than what we would write; stdlib
where the job is genuinely small.

| need | choice | why |
|---|---|---|
| CLI | `spf13/cobra` | persistent flags make `search q --text` work; help from the builder |
| Gmail | `google.golang.org/api/gmail/v1`, `golang.org/x/oauth2` | official; `option.WithHTTPClient` for cassette tests |
| Graph | hand-typed `net/http` over five endpoints, `azidentity` + `azidentity/cache` | the SDK was measured affordable (9.8 MB, 2 s cached build) but its models do not express the MIME form of `sendMail`, and the gate requires the prepared bytes to go out unchanged; five endpoints are small to type by hand |
| MIME for send | `wneessen/go-mail` v0.8 | `Msg.WriteTo` emits RFC 5322 without SMTP; attachments verified byte-exact in a spike. **Never writes Bcc** (`msgwriter.go:121`) — set it via `SetGenHeader`; `WithNoDefaultUserAgent()` |
| HTML parsing | `golang.org/x/net/html` **Tokenizer** | a port of our converter on it is byte-identical to the Python on ten real bodies; the tree-building `Parse` is not the right primitive |
| fan-out | `golang.org/x/sync/errgroup` | first error cancels siblings; `WaitGroup.Go` cannot |
| JSON | `encoding/json/v2` deliberately | strict decoding of cassettes and API payloads |
| stdlib | `image.DecodeConfig`, `crypto/sha256`, `net/mail`, `mime/multipart`, `uuid` not needed | small |

Rejected: `jaytaylor/html2text` drops `<img>` unless it is an `<a>`'s only
child — the exact "images vanish" failure our converter fixes.

## 3. The port

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
func (c Criteria) Match(m Message) bool   // the one definition of "matches"; the memory adapter IS this

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
type PartContent interface{ isPart() }
type StoredPart   struct{ Handle Handle }          // bytes in the message; Open works
type LinkedPart   struct{ URL string }             // referenceAttachment; no bytes here
type EmbeddedPart struct{ Item *Message; Truncated bool } // itemAttachment; adapter recursion, depth budget 2
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
case each.

What the adapters hide: Gmail `q=` compilation and `rfc822msgid:`; Graph's
`$search`/`$filter` choice and local narrowing; paging; MIME walking and
base64url; `attachmentId` keying; Content-ID naming; OData type mapping;
how each provider places a reply (`threadId`, Graph's reply actions); auth
and token storage.

## 4. The shared layer

```
cmd/mail-find, cmd/send-mail   Cobra; persistent --account/--text/--raw
internal/mail                  port, types, Criteria.Match, the search grammar → Criteria, reply derivation, sentinel errors
internal/gmail, internal/graph adapters
internal/memory                test adapter: []Message + part bytes; Search = filter by Criteria.Match
internal/render                HTML → text AND remote-image references in one parse; tidy; quote split; coverage; transcript
internal/cluster               identifier extraction, probes, scoring, too-common (context.py)
internal/images                remote fetch with deadline; classify by min edge and area
internal/attachments           destination allocation (batch, -2 suffixes, filepath.Rel containment), sanitisation — the one owner
internal/drafts                compose → .eml (markdown → text+HTML alternative, reply headers), preview from the .eml, state machine, commit
```

Rules carried, and where each now lives:

- **Fold only what is verified upstream.** `render.Transcript` compares each
  quoted block's prose lines to earlier turns; < 80 % keeps it, recorded in
  `fold_rejected`. **`read` is no longer exempt**: when a boundary is found,
  load `Conversation` and apply the same check; if it cannot be loaded or
  holds no earlier copy, keep the quote. `ProviderFolded` is a candidate
  boundary checked the same way.
- **`--raw`** = converted text with no fold, tidy, `ProviderFolded`, or
  context filtering. Conversion runs in both branches. HTML is not what
  `--raw` means.
- **Search grammar.** `internal/mail` parses the portable subset the model
  already writes (`from:`, `to:`, `subject:`, `has:attachment`, `after:`,
  `before:`, `newer_than:Nd`, quoted phrases, bare words → phrases) into
  `Criteria`; unknown operators are an error naming the grammar. `--native`
  passes the string to the provider untouched, is tagged in the output as
  provider-specific, and is tested on Gmail only.
- **Too-common.** Identifier probes request at least 26 post-match hits
  regardless of `--limit`; > 25 is reported as "more than 25", never a
  truncated count.
- **Fetch by name** succeeds only when the name is unique on the message;
  otherwise an ambiguity error listing handles. Destinations are allocated
  as a batch before any file opens.
- **Nudges** unchanged: results name the recovery command; each has a skip
  condition.

## 5. The send gate

The reviewed bytes are the sent bytes, literally:

1. `send-mail` compose builds the complete RFC 5322 message with go-mail —
   recipients incl. Bcc header, subject, body, attachment bytes, a
   deterministic `Message-ID` — and writes `<id>.eml` plus `<id>.json`
   (state, account, sha256 and size of the `.eml`, and for a reply the
   original it answers). The body is markdown by default, compiled before
   the bytes are pinned into a text+HTML `multipart/alternative`;
   `--format text|html` sends one part verbatim. The compile rules and
   their refusals: `docs/markdown-compose.md`.
2. The preview is rendered by parsing the `.eml`, not from the flags; a
   reply's leads with the original it answers.
3. `--commit <id>`: load; refuse unless state is `pending`; verify sha256
   and, for a reply, that the record's original is the message the bytes'
   `In-Reply-To` names; move to `sending` (atomic rename) *before* any
   network I/O; `Send`, with the record's parent; move to `sent` with the
   provider id. A refusal marked `ErrNotSent` (or `ErrTooLarge`, `ErrAuth`)
   returns the draft to `pending`; any other failure after the provider may
   have accepted leaves `unknown`, never retryable silently. Recovery is by
   subject and time: the live send showed **Gmail rewrites the Message-ID**
   (`…@mail.gmail.com`), so the deterministic id is not a handle there; the
   provider id is stored once known.
4. `SendLimit` is checked against the `.eml`'s size, not attachment sums.
5. Draft id: `<yyyymmdd-hhmmss>-<slug>-<4 hex>` — readable for the model to
   type back, collision-free within a second.

`Prepared` is a constructor-backed type exposing a reader, digest and size;
`Send` transmits nothing else. Gmail encodes it as `raw`; Graph posts it as
MIME.

### Replies

`send-mail --reply ID` (or `--reply-all ID`) answers the message `mail-find`
returned as `ID`, inside its conversation. A reply that only looks threaded
is the failure this exists for: a new message under a `Re:` subject is
grouped by subject in Apple Mail, while Gmail files it as a thread of its
own and a helpdesk cannot attach it to its ticket.

Everything a reply derives comes from `mail.NewReply`, used by compose and
by the contract suite alike:

- **Recipients:** the original's Reply-To, else its sender; a message the
  account sent itself is answered to its own recipients. Reply-all adds
  everyone else on it, never the account. Reply-to-sender is the default
  because an extra recipient cannot be unsent, while a missing one shows in
  the preview and `--cc` adds it.
- **Subject:** the original's with `Re: ` once. Gmail threads only on a
  matching subject, so `--to` and `--subject` are refused with a reply.
- **Headers:** `In-Reply-To` names the original; `References` continues
  its chain (RFC 5322 §3.6.4).
- **Thread:** the provider's placement (`mail.Parent`) lives on the draft
  record and the `Send` call, never in the `.eml`, so a commit sends into
  the conversation that was previewed. The commit reports the conversation
  the provider says it used, and names `mail-find thread` when that differs
  or the provider does not say.

How each provider places it:

- **Gmail** files a sent message in a thread only when the request carries
  the `threadId`, the message's `In-Reply-To`/`References` name a message
  in that thread, and the subjects match; otherwise it silently starts a
  new thread. The send response names the thread it used.
- **Graph** threads by Thread-Index, not by the RFC headers, so `sendMail`
  with them starts a conversation of its own, and `createReply` needs
  `Mail.ReadWrite`, which the app does not request. Replies go through `/messages/{id}/reply` or `/replyAll`, which take the
  MIME bytes under `Mail.Send` but address the reply themselves — the
  original's sender, or everyone on it — and the reference does not say
  whether the MIME To/Cc count too. `replyAction` sends only when every
  reading reaches exactly the previewed recipients and refuses the rest
  with `ErrNotSent`; on Outlook that refuses an extra `--cc`, any `--bcc`,
  an original with a separate Reply-To, and a reply to one's own message
  until first contact settles the question (`docs/outlook-status.md`).

Quoting the original body is out of scope: a reply carries what the author
writes.

## 6. Tests

Tiers, each with one job:

- **T1 adapter cassettes.** `httptest.NewTestServer` replays recorded raw
  API JSON into the real adapters. Assert the golden `Message` and the
  *decoded* request predicates (method, path, parsed `q`/`$filter`/
  `$search`, paging), never URL strings. One **adapter contract suite**
  runs against Gmail, Graph (doc-derived cassettes, labelled), and memory.
- **T2 behaviour through the CLI.** Real Cobra tree, memory adapter.
  Scenarios built with domain constructors from HTML fixture files, so a
  type change breaks compilation, not fixtures. Assert tokens and relations.
- **T3 tables** for pure rules: classification boundaries, identifier
  extraction, sanitisation. Owners; T2 proves only the wiring.
- **T4 live** behind `-tags live`, with `-record` writing cassettes.

| rule | tier | the assertion |
|---|---|---|
| fold verified upstream | T2 | tenancy thread: every prose line absent from a turn exists in an earlier turn (computed); transcript < raw. Forward: `read` contains the forwarded body. **Unrecognised forward** (Re: subject, unique quoted prose, no marker): kept, `fold_rejected` set. **Bad `uniqueBody`** (removed text not upstream): not trusted |
| labelled identifiers | T3 + T2 | table; CLI `signals.identifiers` excludes the PO box and link id |
| > 25 too common | T2 | 26 sharing a number → dropped and named; 25 → kept; `--limit 10` still detects 26; `--raw` keeps |
| weak probes bounded | T2 | same-domain and subject-only messages placed one day inside and one day outside the window: included / excluded |
| min edge and area | T3 + T2 | table; a `--fetch-remote` result carries the label |
| attachmentId keying | T1 + T2 | golden has the nameless CID part, inline; CLI `fetch` by its handle writes it |
| sanitise | T3 + T2 | `../../../etc/passwd` → `etc-passwd`; every written path is inside the out-dir by `filepath.Rel`; duplicate names get `-2` |
| processing default, `--raw` | T2 | `read --raw` == converted text; `thread --raw` contains every body; `context --raw` ≥ default; `--raw` after the subcommand on all three |
| recovery command | T2 | `attachments` on remote-only message names `read <id> --fetch-remote`; absent otherwise; `ErrAuth` names the login command |
| send gate | T2 | bytes the fake `Send` receives parse back with every recipient incl. Bcc and every attachment hash; edited `.eml` refused; second commit refused; concurrent commits → one send; crash between `sending` and `sent` → `unknown` |
| reply derivation | T3 | recipients (sender, Reply-To, own message, reply-all minus the account, each once); `Re:` once; `References` continues the recorded tenancy chain |
| reply in the gate | T2 | `--reply` → commit: the parent reaches `Send`, the bytes carry the threading headers, `thread` shows the reply as a turn; a record whose original disagrees with the bytes is refused; `--to`/`--subject` refused; the commit names a conversation the provider moved it to |
| reply threading | T1 contract | on memory, Gmail and Graph, a send with a parent joins the parent's conversation; Gmail's cassette files by the threads guide's criteria, Graph's by the reply action's documented recipients |
| markdown compose | T3 + T2 | both renderings of one AST carry the same tokens; raw HTML / images / non-mail links / renders-to-nothing refused naming the escape; sent tree asserted with parentage (alternative under mixed, plain before HTML); `--format text` bytes survive untouched |
| contract | T2 | each of the seven subcommands + send-mail has one happy-path case |

Fixtures: raw Gmail JSON already captured (tenancy thread `19fd6b394c784f9b`,
forward `1989fc5ed9469c6c`, IKEA run incl. `1a02367a18eae254`, remote-only
`1a02340ea531858a`, nameless CID `1a01c98dd4e47691`); verbatim, repo private.

## 7. Sequence

1. Fixtures into `testdata/`; `internal/mail` types and `Criteria.Match`;
   `internal/memory`.
2. **Walking skeleton**: every port method through Gmail and doc-derived
   Graph cassettes, adapter contract suite green on all three adapters.
   This is the guard against a Gmail-shaped port while Graph cannot run.
3. Shared rules, one slice per rule, T2/T3 red → green.
4. Send gate with go-mail; T2 gate cases.
5. Live Gmail check incl. one real send; symlinks; delete Python.
6. When consent lands: `-tags live -record`, replace Graph cassettes, run
   the first-contact checks.

## 8. Known limits

Go does not unblock Outlook; Graph stays doc-derived until the admin
clicks. `LinkedPart` is surfaced, not followed. `uniqueBody` agreement has
no fixture until a recording exists. Outlook refuses the replies whose
recipients its reply actions might choose differently (§5, Replies).
