# The operations, independent of provider

Written before adding a second provider, from what building the first one
taught. The question this answers: which parts of what we built are about
*mail*, and which are about *Gmail*.

The short version: **every rule that cost a real failure to learn lives on the
provider-independent side.** The Gmail-specific half produced only mechanical
bugs -- a rejected path, an unparsed flag, a banner on the wrong stream. The
expensive lessons were all about presenting a message to a model without it
missing something, and none of them mention Gmail.

That is the argument for where the seam goes.

## Five operations

### 1. Identify — "this one, the one in front of me"

Turn something a person can hand over into a handle. The only universal
identifier is the RFC822 `Message-ID`: it is in the message itself, so every
client and every provider sees the same value.

| | |
|---|---|
| Gmail | `rfc822msgid:<id>` search operator |
| Graph | `$filter=internetMessageId eq '<id>'` |

Both work, so this ports directly. It matters more than its size: it is the
only operation that answers "read the mail I am looking at right now", and the
answer cannot come from a provider-specific id, because the person is looking
at a mail client, not an API.

### 2. Find — "the ones matching a description"

**This is where the abstraction actually has to do work.** Gmail takes one query
string in its own syntax. Graph splits the job between `$search` and `$filter`
and **will not accept both in one request** -- so a query that Gmail expresses
in one line ("to this person, last week, with attachments") is, on Graph, a
coarse request plus client-side narrowing.

So the shared layer cannot pass a query string through. It needs a **structured
query** the backend translates and, where the provider cannot express it,
completes locally:

```
Query{ participants, exact_phrases, has_attachment, after, before, subject_terms }
```

The `q_exact` / `q_from_domain_between` / `q_subject_tokens` / `q_rfc822`
builders already on the backend were the right instinct, but they cover only
what the clustering needs. The user-facing `search` command still takes raw
Gmail syntax, and that is a leak to close before a second provider exists.

### 3. Fetch — "everything this message contains"

Return one normalised message. Two findings shape what "normalised" means.

**The body must arrive as HTML, not as text.** Today `gws` converts
HTML to text for us and we never wrote a converter. But the folding and tidying
rules are calibrated against *that converter's* output -- its `>` quoting, its
`text ( url )` link rendering. Graph hands back `body.content` as raw HTML, and
a different conversion produces differently-shaped text, on which the fold
detection silently misfires. **The converter has to move into the shared layer
so both providers produce identically-shaped text.** This is the single biggest
thing the Gmail implementation got away with.

**The attachment model needs more slots than Gmail required.** Ours is
`{stored attachment (inline or not), remote image}`. Graph has three kinds:

| Graph type | what it is | our model |
|---|---|---|
| `fileAttachment` | bytes in the message | maps to what we have |
| `referenceAttachment` | a link to OneDrive/SharePoint | **no slot** -- bytes are not in the mail |
| `itemAttachment` | an entire embedded message or event | **no slot** -- and it is a message, so reading it is a recursive fetch |

Both providers mark inline parts the same way in spirit (`isInline` + `contentId`
on Graph, `Content-ID` on Gmail), so the inline/attached distinction survives.
`itemAttachment` is exactly the shape of thing an agent misses -- a forwarded
mail that *is* an attachment rather than quoted text.

Normalised shape, then:

```
Message{ id, rfc822_id, conversation_id, date, from, to, cc, subject,
         body_html, body_text?, parts[], remote_image_urls[] }
Part{ kind: stored | cloud_link | embedded_message,
      inline: bool, name, mime_type, size, content_id, handle }
```

### 4. Relate — "the other messages about this same thing"

Two layers, and both are needed on both providers:

- **Provider grouping** — Gmail `threadId`, Graph `conversationId`. Exact, one
  call, and weaker than it sounds on both: a run of notifications about one
  event scatters across several, and a confirmation from a second company never
  joins at all (measured on Gmail: six messages, five threads).
- **Identifier clustering** — the layer that crosses senders. Entirely
  provider-independent: it needs only the structured query from operation 2 and
  the message fields from operation 3. Its rules -- prefer labelled numbers,
  discard an identifier matching more than 25 messages, time-bound the weak
  probes, attach the reason to every hit -- port unchanged.

### 5. Render — "a form the model will not misread"

Fully provider-independent, and where all the hard-won rules live: fold a
reply's quoted tail only when verified to exist upstream, de-duplicate
`text<url>`, decode entities, classify images by minimum edge and area, order
the transcript chronologically, name the recovery command in the result.

One provider difference is worth designing for rather than ignoring: **Graph
offers `uniqueBody`**, the message body with the conversation history already
removed -- provider-native folding. Treat it as a capability, not a shortcut:
use it when present, fall back to our text folding when not, and **run the
same verification either way**. The invariant that makes folding safe is that
what was removed still exists elsewhere in the conversation. Microsoft asserting
it is not evidence of it, and `--raw` must bypass `uniqueBody` exactly as it
bypasses ours.

## Where the seam goes

The backend does four things and no more: authenticate, resolve, search, fetch.
Everything downstream operates on the normalised message.

```
backend        account() resolve() search(Query) fetch() conversation()
               fetch_part()
capabilities   attachment_limit, native_unique_body, can_combine_search_filter

shared         html→text · quote folding + verification · tidy · image
               classification · identifier clustering · transcript · drafts,
               preview, send gate · result nudges
```

Evidence that this line is in the right place: `transcript.py` and `preview.py`
already import nothing from the backend, and the clustering touches it only
through query builders. The parts that leak today are the two named above --
the user-facing query syntax, and the outsourced HTML conversion.

## What to verify on Outlook before trusting any of it

The Gmail work was only correct because it was checked against a real mailbox.
The same checks, restated as questions for the second provider:

- Does a reply chain from Outlook fold correctly, when the quoting convention is
  the `From:/Sent:/To:` header block rather than `>`? (Gmail's mailbox contained
  both, so the parser has seen it -- but not as the *only* form.)
- Does a forwarded message survive intact, given that folding one destroyed
  6390 characters the first time it was tried?
- Do `uniqueBody` and our own folding agree? Where they differ, which is right?
- Does an `itemAttachment` get surfaced at all, or read as "no attachments" --
  the failure this project already had once, with a linked screenshot?
- Does the 3 MB Graph attachment ceiling get hit where Gmail's 25 MB never did?
