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
