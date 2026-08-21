# What actually reaches the model

Notes from working the tool against a real mailbox. Kept separate from the
README because this is findings, not usage -- and it is the part most likely to
change when this becomes a Go project.

## Downloading is not seeing

The single most important property, and the easiest one to forget:

**`mail-find` puts bytes on disk. It does not put them in the model's context.**

Text comes back on stdout and is read. Every image and PDF comes back as a
*path*, and something has to decide to open it. Two steps, not one. A model that
treats "fetch succeeded" as "I have the content" will confidently answer from a
message whose entire payload was a screenshot.

This is why downloaded images are labelled rather than filtered. The label is
not tidiness -- it is the signal for which file is worth the second step.

## The three stages, and what each yields

| stage | cost | what the model gets |
|---|---|---|
| `search` | 1 list call + 1 metadata call per hit | envelope only: id, date, from/to/cc, subject, ~200-char snippet, labels. **No body.** |
| `read` | 1 full-message call | headers, body as plain text, attachment manifest |
| `fetch` / `read --fetch-remote` | 1 call per blob | bytes on disk, plus dimensions for images |

The snippet from `search` is HTML-escaped by Gmail (`-&gt;` for `->`). It is a
preview, never a source to quote from.

## The body is converted, and the conversion loses things

`read` returns what `gws gmail +read` produces: the `text/plain` part, or the
HTML flattened to text. Three losses matter.

- **Links** become `anchor text ( https://url )`, inline in the prose.
- **Tables** flatten to sequential lines. Which value belonged to which column
  is often gone.
- **Images vanish entirely.** No placeholder, no alt text, no marker. An
  embedded picture leaves behind a bare parenthesised URL that reads exactly
  like a link:

  ```
  George D.

  ( https://cdn.asset-services.taskrabbit.com/chat_resources/...png )
  Friday, 21/08/26, 9:36am
  ```

  Nothing in that says "this is a picture". This is the failure that motivated
  `--fetch-remote`: a Taskrabbit thread where the one load-bearing message was a
  screenshot, and the tool reported "no attachments".

## By content type

| type | visible? | how |
|---|---|---|
| text/plain, text/html | yes | converted to text by `+read` |
| PNG / JPEG / GIF | yes, genuinely | download, then open the file. Verified: read a phone screenshot's map labels, station names and address bar |
| PDF | yes, rendered per page | download, then open. Verified on an invoice: line items, VAT breakdown, totals, last 4 of the card |
| docx / xlsx / pptx | **no** | needs conversion first -- `textutil -convert txt` (built into macOS) or an unzip+XML parse. Not automated here |
| anything else binary | no | -- |

Scanned documents are read as pictures, by looking. There is no OCR step; that
is usually fine and occasionally worse than fine on dense scans.

## Inline vs remote, and why the distinction is real

Both end up as images on disk, but they differ in where the bytes live:

- **inline (Content-ID)** -- stored by Gmail as a part with an `attachmentId`.
  Retrieved over the authenticated API. No third party learns anything.
  Frequently has *no filename*, which is why `attachments()` keys on
  `attachmentId` and not on filename.
- **remote** -- referenced by URL; the bytes sit on the sender's CDN. Getting
  them means an outbound request, which is exactly what a tracking pixel counts.
  Accepted deliberately here: the information in the picture outweighs the
  sender learning the mail was opened.

## Labels on downloaded images

| label | rule | typically |
|---|---|---|
| `pixel` | either edge <= 2px | tracking beacons |
| `small` | min edge < 100px or area < 40000px | wordmarks, avatars, store badges |
| *(none)* | everything else | worth opening |

Both tests are needed: a wordmark is wide and short (319x43), an avatar is
square and tiny (108x108); either test alone misses one of them. On a typical
marketing mail this leaves one unlabelled image out of six.

The rule is heuristic and will be wrong sometimes -- a genuinely small but
meaningful image (a QR code, a tiny chart) gets labelled `small`. Nothing is
withheld, so the cost of a wrong label is a missed second step, not lost data.

## Known gaps

- Office documents are not converted, so their content is invisible.
- Nothing decides *which* image to open; that judgement sits with the caller.
- `search` fans out one metadata call per hit (8 in parallel). Fine at
  `--limit 25`, wasteful at a few hundred.
- No thread view. Reconstructing a conversation means searching, then reading
  each message; `threadId` is returned but unused.
