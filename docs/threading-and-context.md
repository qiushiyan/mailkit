# Reassembling a story from a mailbox

Two commands, because there are two different problems and only one of them
has an exact answer.

## `thread` -- what the provider already grouped

`mail-find thread <id>` returns Gmail's own thread in one `threads.get` call.
Exact, cheap, no heuristics. Right answer for a human reply chain.

It is also much weaker than it sounds. Gmail groups a reply chain, plus
same-subject mail inside a short window. Measured on a real run of
notifications about one furniture delivery:

```
1a02367a18eae254  thread=self      Heads up! A change to your upcoming IKEA Assembly
1a02340ea531858a  thread=grouped   Updates to: IKEA Furniture Assembly
1a0233fbb408f2b5  thread=self      Updates to: IKEA Furniture Assembly
1a016b39c7cc3992  thread=self      You've been matched! ...
1a013c77cffb5e55  thread=self      Updates to: IKEA Furniture Assembly
1a011d4a2302fadc  thread=self      Your Upcoming IKEA Assembly
```

Six messages, five threads. Two of the three identically-titled messages were
grouped; the third was three days earlier and fell outside the window. And no
threading scheme in the world would have pulled in the order confirmation from
the *retailer*, which is a different company entirely.

## `context` -- what is actually about the same thing

The signal that works is a **shared identifier**: an order number, a task id, a
reference, printed in a body or buried in a URL. It is the only signal that
crosses senders, which is the whole point -- the retailer and the assembly
service never reply to each other, but both quote order 1623209215.

### Extraction

Digit runs of 6-14. Preferring *labelled* numbers is what makes this usable:

```
"IKEA Order #1623209215 ... PO Box 530225 ... onelink.me/258595750?utm"
   bare runs  -> 1623209215, 530225, 258595750
   labelled   -> 1623209215
```

Bare runs are the fallback, used only when a message labels nothing.

### Probes and weights

| probe | weight | bounded by time? |
|---|---|---|
| shared identifier | 5 | no -- an order number is distinctive whenever it appears |
| same Gmail thread | 3 | n/a |
| same sender domain | 2 | yes, +/- `--window` days |
| subject token overlap | 1 | yes, +/- `--window` days |

Two self-calibrating guards, both learned by getting it wrong first:

- **An identifier matching more than 25 messages is discarded.** A number that
  common is a date, a price or a boilerplate id, not a key. No tuning needed --
  the mailbox answers the question.
- **Weak probes must be time-bounded.** Unbounded, `subject:(ikea OR assembly)`
  returned a food-hall newsletter from 2023 and an "Inspecting Loop Assembly"
  programming course. Subject tokens only mean anything near the event.

### Minimum score

Default `--min-score 2`: one weak signal alone is not evidence, two are. On the
test case this held back six matches -- IKEA login codes, an insurance ad, an
unrelated proof of payment -- while keeping all eight genuinely relevant ones.
`--min-score 1` shows them.

### Every hit carries its reason

```
[ 8] 1a011d4a2302fadc  Mon, 17 Aug 2026 22:25:42
     Your Upcoming IKEA Assembly
     why : shares identifier 1623209215
     why : same sender domain (taskrabbit.co.uk) within 30 days
     why : subject overlap: ikea, assembly
```

This is not decoration. A heuristic cluster whose members cannot be challenged
is worse than no cluster: it invites a confident answer built on a message that
was never related. The reasons are what make a wrong grouping survivable.

## Measured result

Seeded on the last message of the run: **8 related messages, spanning two
companies and five threads, in correct chronological order, in 3.6s.**

The whole sequence comes back -- order placed, VAT invoice, assembly booked,
tasker assigned, tasker's messages, cancellation -- which is the story, and none
of it is reachable from `thread`.

## Known imperfections

- One marketing mail ("Improving your space makes everyday wonderful!") scores 5
  because it genuinely quotes the order number. A real link, useless narratively.
  The `why` line makes it discardable; suppressing it would need content
  judgement this layer does not have.
- Cost is one metadata call per candidate hit, 8 in parallel. Fine at these
  sizes, wasteful if `--limit` is raised far.
- Identifier keywords are English-only.
- Nothing dedupes near-identical notifications; three "Updates to:" messages
  come back as three rows.

---

# Reading a reply chain

`mail-find thread <id> --transcript` renders the thread as one chronological
transcript. Nothing is reworded; text is rearranged and repeated copies are
dropped.

## What the raw form looks like

Measured on a real six-message tenancy thread:

| problem | why it matters |
|---|---|
| Outlook replies carry **no quote markers at all** | depth is signalled only by a `From:/Sent:/To:` block or an `On <date>, X wrote:` line. Misread that one line and a statement gets attributed to the wrong person -- the failure that yields a confident wrong answer |
| one message mixes **two quoting conventions** | the participants use different clients, so an Outlook header block and an Apple `On ... wrote:` appear in the same body |
| newest-first | reading top to bottom means reading backwards in time |
| `text<url>` duplication | `FHashim@quintainliving.com<mailto:FHashim@quintainliving.com>` -- every address and link doubled by the HTML-to-text step |
| platform banners inside the quote | Microsoft's "You don't often get email from..." sits in the quoted body and reads like something the sender wrote |
| signatures and legal footers repeat per level | six copies of the same contact block |

Body length grows monotonically down the chain -- 942, 1205, 1578, 2185, 2610
characters -- because each reply re-includes everything before it. 8982
characters for roughly 2000 characters of conversation.

## Folding is verified, not assumed

Dropping a quoted block is only safe when that text is *elsewhere in the
thread* -- it is an earlier turn, still present, just not repeated. A forward is
the opposite: what it quotes is usually the only copy.

Marker detection alone does not establish which case you are in, and trusting it
destroyed data on the first real forward tried: Apple Mail prefixes
`Begin forwarded message:` with the same `> ` it uses for replies, so a
reply-shaped pattern matched and all 6390 characters of a single-message thread
were folded into nothing.

So the check is now empirical. Before folding, the quoted block's prose lines
are compared against everything already seen in the thread; below 80% coverage
the block stays, with the reason recorded in `fold_rejected`. Structural lines
(attribution, header blocks, `E:`/`T:` contact rows) are excluded from the
comparison -- they are format, and their absence upstream never means content
was lost.

## Result

```
turn 1: no quote
turn 2: folded 1024 via caret
turn 3: folded 1324 via caret
turn 4: folded 1769 via outlook-header
turn 5: folded 2123 via outlook-header
turn 6: folded 374 via caret

transcript 2116 chars from 8982 raw (6866 folded as repetition)
```

Independently verified afterwards: of every prose line folded away, **zero**
could not be found in an earlier turn. Both forwards tested come back whole.

Each turn still shows what was folded and by which marker, so a fold can be
challenged the same way a `context` match can.
