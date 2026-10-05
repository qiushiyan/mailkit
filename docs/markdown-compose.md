# Markdown compose

How `send-mail` turns an authored body into what email clients render. The
compiler: `internal/drafts/markdown.go`. Markdown is only the input language;
the real job is compiling one reviewed, semantic message into two
recipient-facing representations that stay equivalent — `text/plain` for
clients that display characters literally, `text/html` for clients that
interpret markup. The message is the parsed AST; both parts are renderings of
it, and anything that cannot render equivalently in both is refused at
compose time. A divergence between alternatives is a lie to one set of
recipients: `multipart/alternative` promises interchangeable versions of the
same information (RFC 2046 §5.1.4).

This is entirely a compose-time concern. Both adapters transmit the prepared
bytes untouched (Gmail raw send, Graph MIME `sendMail`), and the compile runs
before `mail.NewPrepared` pins the digest — the port and adapters never learn
markdown exists.

## Parse once, render twice

goldmark (github.com/yuin/goldmark, CommonMark, zero transitive dependencies)
parses the source to an AST; the stock HTML renderer produces the `text/html`
part and a small AST walker in the same file produces the `text/plain` part:
strong/emphasis as `*text*` (the standing plain-mail convention), links as
`label (URL)`, list nesting preserved with markers, headings as their text,
blockquotes under `> `. Rendering the plain part from the source verbatim
fails the equivalence bar — plain recipients would see authoring syntax
(`[label](url)`, backslash escapes, indentation grammar), and go-mail's
quoted-printable writer canonicalises line endings anyway, so "verbatim" was
never byte-true.

**A newline is a line break in both parts** (`WithHardWraps`). Authors here
are agents whose markdown dialect is the chat one, where `Best,\nQiushi` is
two lines; spec CommonMark soft-break folding would silently collapse the
sign-offs and address blocks that occur constantly in real mail, while the
cost — a paragraph hard-wrapped at a column keeping its wrap points — almost
never occurs in agent-authored bodies. Either failure shows in the preview
before anything sends.

No extensions: CommonMark core covers bold, emphasis, lists, and links, and
gives headings, blockquotes, and code for free. GFM extras are one line each
when a real mail needs them.

## Refusals

Compose-time errors, each naming the offending construct and its way out:

- **Raw HTML**, inline or block — it would be visible in the plain part and
  omitted from the HTML part. The escapes: `--format html` for a finished
  HTML body, `--format text` for bytes verbatim.
- **Images** — a remote `<img>` fires a network request the moment the
  preview auto-opens, a read receipt leaked before the user ever commits; a
  local path is a broken link for the recipient. `--attach` is the way out.
- **Link destinations outside http(s)/mailto** — an unclickable or dangerous
  scheme in a mail is never intended.
- **Source whose rendered output is empty** (a body of only link-reference
  definitions parses to nothing) — otherwise go-mail would emit an empty
  `text/html` part that HTML clients *prefer* over the visible plain one.

## The HTML is a bare semantic fragment

`<strong>`, `<em>`, `<ul>`, `<a>`, `<p>` render correctly in every client
including Outlook's Word engine with no CSS. No wrapper, no inline styles:
personal mail should look like mail in the recipient's client, not a
designed page.

## Where it sits

`Compose.Body` is a constructed sum — `PlainBody(s)` / `HTMLBody(s)` /
`MarkdownBody(s)` — coupling the source with its interpretation the way
`Part.Content` seals storage location; there is no format enum value to get
wrong. The compile is an implementation file inside `internal/drafts`, not a
package or a renderer port: one implementation, no second adapter on the
horizon. The MIME shape go-mail produces:

```
multipart/mixed            # only when attachments exist
  multipart/alternative
    text/plain             # first
    text/html              # last: clients prefer the last part they support
  attachment(s)
```

## CLI surface

One `--format markdown|text|html` flag, markdown the default. The failure
asymmetry decides the default: an agent that never read the skill gets
formatting instead of sending literal `**bold**` to a human, and the
default's own failures are louder — a transformation is visible in the
preview, a refused construct is a compose-time error naming `--format text`
as the verbatim escape. Processed-by-default with an explicit bypass is also
the read side's shape (`--raw`). The cost accepted: pasted content with
markdown-significant characters transforms or is refused, so every refusal
points at the escape.

The three modes refuse to mix — `--commit`/`--list` with compose-only flags,
`--commit` with `--list`, `--body` with `--body-file` are errors, not silent
precedence. No extension sniffing on `--body-file`: stdin has no extension,
and the format is not a property of a file name.

## Preview

Rich bodies (compiled markdown and `--format html` alike) render inside a
sandboxed iframe — the `sandbox` attribute blocks script and same-origin
access, a CSP meta blocks every network load — so author-controlled HTML can
neither run nor phone home from a page that auto-opens. The Format row states
the actual MIME shape (`Text + HTML`): the wire retains no markdown
provenance, and the page must not claim metadata the bytes do not carry.
`Parsed` distinguishes an absent part from an empty one, and the plain
alternative stays inspectable in a collapsed section.

## Out of scope, deliberately

- **Inline images** — the natural follow-up is `![](local-path)` as a `cid:`
  attachment (AST walk → `EmbedFile` → rewrite the src); it waits until
  asked for.
- **GFM tables / strikethrough / task lists** — one-line extensions when
  needed.
- **Quoting the original in a reply** — `--reply` threads the answer
  (`docs/go-design.md` § Replies) and carries only what the author writes;
  a markdown blockquote of the original waits until asked.
- **Read-side markdown** (HTML→markdown for `mail-find`) — different
  feature; `render.Convert` stays the calibrated read form and is
  deliberately not reused on the write path, since its folding rules are
  lossy by design for received mail.
