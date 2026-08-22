"""Presenting a reply chain so attribution survives reading.

Nothing here changes information. It rearranges and de-duplicates what the
provider already returned, because the raw form is actively misleading to read:

  - Replies from Outlook carry no quote markers at all. Depth is signalled only
    by a `From:/Sent:/To:` block or an `On <date>, X wrote:` line, so the
    boundary between what this sender is saying now and what they are quoting
    is a single line of plain text. Misreading it attributes a statement to the
    wrong person -- the one failure here that produces a confident wrong answer.
  - A single message often mixes two quoting conventions, because the two
    participants use different clients.
  - Each reply re-includes every earlier message, so a six-message thread costs
    several times what its actual content does, and reads newest-first.

Folding a quote is only safe because the quoted text is itself a message in the
same thread -- it is still available, just not repeated. A *forward* is the
opposite case: what it quotes usually is not in the thread, so folding it would
destroy the only copy. Forwards are therefore left intact.
"""

import re

# Where a message stops speaking and starts quoting. Ordered: the Outlook
# header block is checked before the looser attribution line so a message
# carrying both folds at the earlier of the two.
QUOTE_BOUNDARIES = [
    ("outlook-header", re.compile(
        r"^\s*From:\s*.+?$\n^\s*Sent:\s*.+?$", re.M | re.S)),
    ("original-message", re.compile(
        r"^\s*-{2,}\s*Original Message\s*-{2,}\s*$", re.M | re.I)),
    ("attribution", re.compile(
        r"^\s*On\s+.{4,80}?\s+wrote:\s*$", re.M)),
    ("attribution-inline", re.compile(
        r"^\s*On\s+.{4,120},\s*.{2,80}<[^>]+>\s*wrote:\s*$", re.M)),
    ("caret", re.compile(r"^>", re.M)),
]

# A forwarded body is usually the only copy of what it contains.
FORWARD_MARKERS = re.compile(
    r"^[>\s]*(-{3,}\s*Forwarded message\s*-{3,}|Begin forwarded message:)",
    re.M | re.I,
)

# The HTML-to-text step renders every link as `text<url>`, which doubles every
# address and URL in the message.
MAILTO_DUP = re.compile(r"([^\s<>]+@[^\s<>]+?)<mailto:\1>", re.I)
MAILTO_ANY = re.compile(r"([^\s<>]+)<mailto:[^>]+>", re.I)
LINK_DUP = re.compile(r"([^\s<>]+)<(https?://[^>]+)>")

# Tenancy-platform chrome that is inserted into the body and reads like prose.
BANNERS = [
    re.compile(r"^You don't often get email from .*?$", re.M | re.I),
    re.compile(r"^\s*Learn why this is important\s*$", re.M | re.I),
    re.compile(r"^\s*\[?External( Email)?\]?:?\s*$", re.M | re.I),
]

BLANK_RUN = re.compile(r"\n{3,}")

# Lines that carry no prose: attribution, header blocks, contact chrome. They
# are structure, so their absence upstream never means content was lost.
STRUCTURAL = re.compile(
    r"^(On\s.+wrote:|From:|To:|Sent:|Cc:|Bcc:|Subject:|Date:|E:|T:|W:|"
    r"You don't often get email)", re.I)
MIN_PROSE_LINE = 40
FOLD_COVERAGE = 0.8
SEPARATOR_RUN = re.compile(r"^[_\-=]{8,}\s*$", re.M)


def split_quote(body):
    """(spoken, quoted, marker) -- the part written now, and the part quoted.

    Returns the whole body as `spoken` when the message is a forward, or when
    no boundary is recognised.
    """
    if not body:
        return "", "", None
    if FORWARD_MARKERS.search(body):
        return body, "", "forward"

    earliest = None
    for name, pattern in QUOTE_BOUNDARIES:
        match = pattern.search(body)
        if match and (earliest is None or match.start() < earliest[1]):
            earliest = (name, match.start())
    if earliest is None:
        return body, "", None
    name, index = earliest
    return body[:index].rstrip(), body[index:].strip(), name


def tidy(text):
    """Undo the artefacts of the HTML-to-text conversion. Content only moves or
    loses a duplicate copy of itself; nothing is reworded."""
    if not text:
        return ""
    text = MAILTO_DUP.sub(r"\1", text)
    text = MAILTO_ANY.sub(r"\1", text)
    # Keep a link's target only when it says something the anchor text does not.
    def _link(m):
        anchor, url = m.group(1), m.group(2)
        stripped = url.rstrip("/").replace("https://", "").replace("http://", "")
        return anchor if stripped.endswith(anchor.rstrip("/")) else f"{anchor} ({url})"
    text = LINK_DUP.sub(_link, text)
    for banner in BANNERS:
        text = banner.sub("", text)
    text = SEPARATOR_RUN.sub("", text)
    return BLANK_RUN.sub("\n\n", text).strip()


def _prose_lines(text):
    for line in (text or "").splitlines():
        line = re.sub(r"^[>\s]+", "", line).strip()
        if len(line) >= MIN_PROSE_LINE and not STRUCTURAL.match(line):
            yield line


def _normalise(text):
    return re.sub(r"[^a-z0-9]+", " ", (text or "").lower())


def coverage(quoted, pool):
    """How much of a quoted block genuinely repeats something said earlier."""
    lines = list(_prose_lines(quoted))
    if not lines:
        return 1.0, 0
    flat = _normalise(pool)
    found = sum(1 for line in lines if _normalise(line).strip() in flat)
    return found / len(lines), len(lines)


def build(messages):
    """A chronological transcript, one turn per message, quotes folded.

    A quote is only folded once it has been *checked* to repeat earlier turns.
    Marker detection alone is not enough to make that safe: an Apple Mail
    forward prefixes "Begin forwarded message:" with the same "> " it uses for
    replies, and folding one destroys the only copy of its contents. So the
    text is compared against what came before, and a block that does not
    already exist upstream stays where it is.
    """
    turns = []
    pool = ""
    for m in messages:
        body = m.get("body") or ""
        spoken, quoted, marker = split_quote(body)
        rejected = None
        if quoted:
            ratio, n_lines = coverage(quoted, pool)
            if ratio < FOLD_COVERAGE:
                rejected = (f"kept: {int(ratio * 100)}% of {n_lines} quoted "
                            f"lines are not in earlier turns")
                spoken, quoted = body, ""
        pool += "\n" + body
        turns.append({
            "fold_rejected": rejected,
            "id": m.get("id"),
            "date": m.get("date"),
            "from": m.get("from"),
            "to": m.get("to"),
            "subject": m.get("subject"),
            "said": tidy(spoken),
            "quoted_chars": len(quoted),
            "quote_marker": marker,
            "attachments": m.get("attachments") or [],
        })
    raw = sum(len(m.get("body") or "") for m in messages)
    kept = sum(len(t["said"]) for t in turns)
    return {
        "turns": turns,
        "raw_chars": raw,
        "transcript_chars": kept,
        "folded_chars": raw - kept,
    }


def render(result):
    out = []
    for i, t in enumerate(result["turns"], 1):
        out.append(f"--- {i}. {t.get('date') or ''}")
        out.append(f"    {t.get('from')}")
        if t.get("to"):
            out.append(f"    to: {t['to']}")
        if t["attachments"]:
            out.append("    attachments: "
                       + ", ".join(a["name"] for a in t["attachments"]))
        out.append("")
        out.append(t["said"] or "(no new text -- quoting only)")
        if t["quoted_chars"]:
            out.append(f"\n    [folded {t['quoted_chars']} chars quoted via "
                       f"{t['quote_marker']}; those turns appear above]")
        out.append("")
    out.append(f"transcript {result['transcript_chars']} chars "
               f"from {result['raw_chars']} raw "
               f"({result['folded_chars']} folded as repetition)")
    return "\n".join(out)
