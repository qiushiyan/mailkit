# Design rules for what this tool returns

The consumer of every line `mail-find` prints is a model, so the output is
prompt surface and gets engineered as such. Two standing rules, then the
specifics.

## Rule 1 — the processing layer is the default

Common cases arrive already handled. `thread` renders a transcript, `read`
folds a reply's quoted tail and undoes the `text<mailto:text>` duplication,
`context` filters weak matches. A caller that has to opt into the readable
form will, sooner or later, not opt in — and then reads a chain whose
attribution it cannot resolve.

## Rule 2 — `--raw` bypasses it, everywhere

Every processing layer has edge cases, and this one has already been caught
losing 6390 characters of a forwarded message. `--raw` returns exactly what the
provider gave: unfolded, untidied, unfiltered.

| command | default | `--raw` |
|---|---|---|
| `read` | quoted tail folded, links de-duplicated | body exactly as returned |
| `thread` | one chronological transcript | every message as sent |
| `context` | weak and over-common matches filtered | every probe hit, scores intact |

So the processed form is never the *only* form. When it looks wrong, there is
somewhere to go that is not "trust it anyway".

## Results say what to do next

The highest-leverage surface is the result itself, read at the moment the next
action is chosen. Where a result changes what should happen next, it says so as
a runnable command:

```
no file attachments

6 image(s) referenced by URL in the body. To fetch them:
mail-find read 1a02367a18eae254 --fetch-remote
```

That message exists because of a real failure: `attachments` reported "no
attachments" for a message whose only substantive content was a linked
screenshot, and the answer given from it was confidently wrong. A count with no
command attached would not have fixed it — the fix is that the result names the
command that recovers what it could not return.

Each nudge carries a skip condition, so it fires only when true: no remote
images, no line; a message that is not part of an exchange gets no thread
pointer, and the check for one is skipped entirely unless the subject looks
like a reply — so the common case costs no extra call.

## Assert only what this layer saw

`read` reports remote images as *referenced*, not as read. A fold reports how
much was folded and by which marker. A `context` hit carries the reasons it was
included. Everything the tool cannot verify is left for the caller to check,
with the means to check it.

## Errors prescribe the command

Not "see README" — a model cannot run a document:

```
not authenticated for gmail. Run:
    gws auth login --scopes=.../gmail.readonly,.../gmail.send
```
