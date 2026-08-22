# Notes for a Go rewrite

Written while the Python version was working, from what building it actually
cost. Not a decision to migrate -- the inputs to that decision.

## The finding that matters

**Most of the value on offer is in leaving the wrap-a-CLI architecture, not in
changing language.** Go is what makes leaving it cheap, because the official
SDKs compile into the binary.

Today the chain is: our code -> parse the JSON output of `gws` -> a Rust binary
that is explicitly not an officially supported Google product, pre-1.0, and
says to expect breaking changes. Adding Outlook the same way means a second
wrapper around `m365`, a second JSON dialect, and a second auth story.

Python has official SDKs too (`google-api-python-client`, `msgraph-sdk`), so
this is not a capability Go uniquely has. The honest difference: adding pip
dependencies to a Python CLI means a venv or `uv tool install`, and the
"it's just a script" property is gone. Go compiles them in.

## Evidence: the bugs this thing actually produced

Of the seven real defects hit while building it, six came from the architecture
or from dynamic typing, one was pure logic.

| defect | Go + SDK |
|---|---|
| `gws` rejects any attachment path outside the working directory -- the first real send failed | gone; no cwd involved |
| `gws auth status` rejects `--format json` | gone |
| a banner on stderr, JSON on stdout, and code deciding which is which | gone |
| `--params` requires hand-assembling a JSON string | gone; pass a struct |
| comparing timezone-naive and timezone-aware timestamps threw | gone; `time.Time` always carries a zone |
| an over-wide edit deleted `fetch_attachment`; only a runtime `AttributeError` revealed it | `go build` catches it |
| folding destroyed a forwarded message whole | **not caught by any of this.** Only a test catches it |

That last row is the argument for the test suite below, and the reason a
compiler is not the point.

## What the code is made of

1624 lines, in two halves with opposite characteristics:

| part | lines | character | effect of Go |
|---|---|---|---|
| `backends.py` | 464 | 17 subprocess sites, wrapping `gws` | large win -- most of it deletes |
| `transcript.py`, `context.py` | 396 | 31 regex sites, zero subprocess | **worse in Go** |
| `drafts.py`, `preview.py` | 241 | file IO, hashing, an HTML template | a wash |
| the two CLI entry points | 523 | flags and presentation | slightly better (Cobra) |

## The one concrete incompatibility

Go's `regexp` is RE2: no backreferences, no lookaround. All eleven patterns
were checked. Exactly one fails:

```python
MAILTO_DUP = re.compile(r"([^\s<>]+@[^\s<>]+?)<mailto:\1>")
```

The `\1` has no RE2 equivalent. Port it as a match-then-compare: capture both
sides, compare in code. Arguably clearer than the regex was.

Nothing else uses lookahead or lookbehind, so the text-processing port is
verbose but not blocked.

Standard-library parity is fine, and one item improves: `image.DecodeConfig`
replaces the hand-rolled PNG/GIF/JPEG header parsing in `_image_dims`.

## What would earn the migration

A line-by-line translation would not. These four would:

1. **A typed domain model.** `msg` is a dict and `inline` is a key. An inline
   attachment and a remote image differ in where the bytes live, whether
   fetching them tells the sender, and whether they work offline -- differences
   currently held by convention. Put them in the type system.
2. **Table-driven tests.** The highest-value item. The fold-coverage rule, the
   identifier preference, the image-classification boundaries, the too-common
   threshold -- all are input-to-expected-output, which is exactly the shape Go
   tests take. Right now they are verified by throwaway scripts run once by
   hand, and the one defect a compiler cannot catch lives here.
3. **`context.Context`.** Timeouts are scattered `timeout=180` arguments with
   no cancellation propagation. Fetching, downloading and the parallel probes
   should all cancel together.
4. **`errgroup`** for the search fan-out (list, then one metadata call per hit).
   Cleaner than `ThreadPoolExecutor`, and the first error aborts the rest.
   The weakest of the four -- Python threads are adequate for IO-bound work.
   Not a reason to migrate on its own.

## Port the decisions, not the code

The product is the rules, each of which cost a real failure to learn. They live
in the other files here and should be carried over as **test cases first**:

- fold only what is verified to exist upstream, at 80% prose-line coverage --
  `threading-and-context.md`
- prefer labelled identifiers over bare digit runs -- `threading-and-context.md`
- discard an identifier matching more than 25 messages -- same
- weak probes must be time-bounded -- same
- classify images on both minimum edge and area -- `what-the-model-sees.md`
- attachments key on `attachmentId`, never on filename -- same
- sanitise attachment filenames against path traversal -- same
- processing layer on by default, `--raw` on every layer -- `output-design.md`
- results name the command that recovers what they could not return -- same

Translating the Python without carrying these across turns hard-won rules back
into implicit behaviour.

## Sequencing, if it happens

Outlook is the natural trigger. Doing it in Python means building the second
CLI wrapper and paying the table above twice; doing it in Go means both
providers are SDK calls and the backend interface gets its first real second
implementation -- which is when an interface is actually tested.

So: turn the rules above into a language-neutral test-case list, then build
Outlook in Go and bring Gmail across with it. Not Gmail first, then Outlook.

## The case for not migrating

If this stays a personal tool on one machine, the Python version already works.
The gain would be that it becomes maintainable and testable, not that it becomes
more useful. That is a real gain and a modest one; weigh it against a rewrite of
a thing that currently does its job.
