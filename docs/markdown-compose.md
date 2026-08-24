# Markdown compose: design scope

Status: implemented (codex consult round folded in, 2026-08-24). Still
deferred: the docs/go-design.md §6 row and the write-email skill update.

`send-mail` should let an agent author a body in markdown and have mailkit emit
what email actually supports. Markdown is only the input language: the real
job is compiling one reviewed, semantic message into two recipient-facing
representations that stay equivalent — `text/plain` for clients that display
characters literally, `text/html` for clients that interpret markup. The
message is the parsed AST; both parts are renderings of it. Anything that
cannot be rendered equivalently in both is refused at compose time, never
silently dropped from one part — a divergence between alternatives is a lie to
one set of recipients.

## Why this is compose-only

Both adapters transmit the prepared bytes untouched: Gmail's raw send
(`internal/gmail/gmail.go:269`) and Graph's MIME `sendMail`
(`internal/graph/graph.go:382`) base64 `p.Bytes()` and nothing else. The digest
pin, `Claim`, and the preview's MIME walker are format-agnostic. The whole
feature lives in `internal/drafts` and the `send-mail` flag surface.

## Decisions

**Parse once, render twice.** goldmark (github.com/yuin/goldmark, CommonMark
0.31.2, zero transitive dependencies) parses the source to an AST; the stock
HTML renderer produces the `text/html` part and a small write-side AST walker
produces the `text/plain` part. The source verbatim was considered and
discarded: `multipart/alternative` promises interchangeable versions of the
same information (RFC 2046 §5.1.4), and raw source shows plain-text recipients
authoring syntax — `[label](url)`, backslash escapes, list-indentation grammar
— while the killer cases actively diverge (raw HTML visible in plain, omitted
from HTML; a `javascript:` destination visible in plain, stripped in HTML).
go-mail's quoted-printable writer canonicalises line endings anyway, so
"verbatim" was never byte-true. The plain renderer: strong/emphasis as
`*text*` (the standing plain-mail convention), links as `label (URL)`, list
nesting preserved with markers, headings as their text, hard breaks kept, soft
breaks folded or kept per the open call below — always the same choice the
HTML renderer makes, so the two parts say the same thing line for line.

**Refuse what cannot render equivalently.** Compose-time errors, each naming
the offending construct: raw HTML (inline or block), link/image destinations
outside http/https/mailto, images of any kind in v1 (a remote `<img>` fires a
network request the moment the preview auto-opens — a read-receipt leaked
before the user ever commits — and a local path is a broken link for the
recipient), and source whose rendered output is empty (a body of only link
reference definitions parses to nothing; go-mail would emit an empty
`text/html` part that HTML clients *prefer*, while the preview's
`BodyHTML == ""` check would hide the problem). No extensions: CommonMark core
minus images covers the requirement — bold, emphasis, lists, links — plus
headings, blockquotes, and code for free. GFM extras are one line each when a
real mail needs them.

**`Compose.Body` becomes a constructed sum**, not a format enum:
`drafts.PlainBody(s)` / `drafts.HTMLBody(s)` / `drafts.MarkdownBody(s)`
replacing the `Body string` + `HTML bool` pair. It couples the source with its
interpretation the way `Part.Content` already seals storage location, and
leaves no unknown-enum-value state to check. Conversion happens inside
`Store.Create` before `NewPrepared` — a `markdown.go` implementation file in
`internal/drafts`, no new package, no renderer port (one implementation, no
second adapter on the horizon). The port and adapters never learn markdown
exists.

**MIME shape.** `SetBodyString(TypeTextPlain, plain)` then
`AddAlternativeString(TypeTextHTML, html)` — the last alternative is the
preferred one. With attachments, go-mail v0.8.1 nests correctly
(`msgwriter.go:143`: mixed root, alternative closed before attachments are
written):

    multipart/mixed
      multipart/alternative
        text/plain
        text/html
      attachment(s)

**The HTML is a bare semantic fragment.** `<strong>`, `<em>`, `<ul>`, `<a>`,
`<p>` render correctly in every client including Outlook's Word engine with no
CSS. No wrapper, no inline styles: personal mail should look like mail in the
recipient's client, not a designed page.

**CLI surface: one `--format markdown|text|html` flag, markdown the
default**, replacing `--html` — one enum instead of two mutually exclusive
booleans, and the skill is the only consumer to migrate. Markdown is the
default because the failure asymmetry favours it: an agent that never read
the skill gets formatting instead of sending literal `**bold**` to a human,
and the default's own failures are louder — a transformation is visible in
the preview, a refused construct is a compose-time error that names
`--format text` as the verbatim escape. Processed-by-default with an explicit
bypass is also the read side's shape (`--raw`). The cost accepted: pasted
content with markdown-significant characters transforms or is refused, so
every refusal points at the escape. The same change completes the refusal
surface around the modes: `--commit`/`--list` with compose-only flags,
`--commit` with `--list`, and `--body` with `--body-file` are refusals, not
silent precedence. No extension sniffing on `--body-file`: stdin has no
extension, and the format is not a property of a file name. The skill tells
agents to pass markdown via stdin or a file — inline `--body` invites shell
corruption of backticks and dollars.

**Decided: soft breaks are line breaks** (`WithHardWraps` stays). The goal is
that newlines just work without teaching agents an escape syntax; the two
semantics weighed:

- *Keep `WithHardWraps`*: every newline is a `<br>`. Matches the dialect
  agents already write (GitHub comments, Slack); a single-newline sign-off or
  address block survives. Cost: a paragraph hard-wrapped at a column keeps its
  wrap points forever.
- *Drop it* (codex's position): spec CommonMark — soft breaks reflow, hard
  breaks are spelled `\` at line end, and the skill teaches that. Cost: the
  most common agent-authored shape (`Best,\nQiushi`) silently collapses to one
  line unless the skill lesson sticks.

Both failures are visible in the preview before anything sends; hard wraps
won because the sign-off case occurs constantly in real mail and the
column-wrapped paragraph almost never does in agent-authored bodies. The
plain renderer makes the identical distinction: every break is a newline.

## Preview

Rich bodies (generated HTML and `--format html` passthrough alike) render
inside a sandboxed iframe — scripts off, external loads blocked — instead of
today's raw injection into the preview page (`preview.go:75-77`). goldmark's
output is escaped by construction, but the `--html` path never was: author
HTML in an auto-opened page could run script or restyle the recipient rows it
sits beside, and the iframe closes that for both paths at once. The Format
row derives from the actual MIME shape (`Text + HTML`) — the wire retains no
markdown provenance, and the preview must not claim metadata the bytes don't
carry. `Parsed` distinguishes an absent part from an empty one, and the plain
part appears in a collapsed section so both alternatives can be checked before
commit.

## Tests

Each adopted trap from the consult round is pinned as a planned case:

- **T3, rendering tables** (`internal/drafts`): bold/lists/links in both
  renderers; the soft/hard break choice applied identically in both parts;
  emphasis and link forms in the plain renderer.
- **T3, refusal tables**: raw HTML refused (inline and block); `javascript:`
  and `data:` destinations refused; `![image](...)` refused; a body of only
  `[foo]: https://example.com` refused as rendering to nothing.
- **T2** (`internal/cli/send_test.go`): compose `--format markdown` via
  stdin, commit, and assert the sent bytes with a **recursive MIME tree
  assertion that preserves parentage and order and decodes quoted-printable
  as well as base64** — the current `parts` helper flattens one level, and a
  flat two-string check would still pass if the alternatives sat as
  `multipart/mixed` siblings. No attachments: root is `multipart/alternative`.
  With an attachment: the nesting shown above, Bcc surviving.
- **T2, flags**: `--format` rejects unknown values; the completed refusal
  pairs (`--commit` + compose flags, `--body` + `--body-file`) each name both
  flags; `TestSurface_BareSendMailListsItsThreeUses` updated for the surface
  change.
- Extend the send-gate row in docs/go-design.md §6; rewrite the format section
  of skills/write-email/SKILL.md (`--format`, stdin for markdown, the
  hard-break rule if codex's soft-break position wins).

## Out of scope, deliberately

- **Images** — refused in v1, not merely undocumented. The natural follow-up
  is `![](local-path)` as a `cid:` inline attachment (AST walk → `EmbedFile`
  → rewrite the src); it waits until asked for.
- **GFM tables / strikethrough / task lists** — one-line extensions when
  needed.
- **Replies** — no reply flow exists to integrate with; markdown quoting
  waits for threading.
- **Read-side markdown** (HTML→markdown for `mail-find`) — different feature;
  `render.Convert` stays the calibrated read form and is deliberately not
  reused on the write path (its folding rules are lossy by design for
  received mail).
