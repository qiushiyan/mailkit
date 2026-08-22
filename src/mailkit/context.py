"""Pulling together the messages that are about one real-world thing.

Gmail's own threading recovers less than you would hope. It groups a reply
chain, plus same-subject mail inside a short window -- so a run of automated
notifications about a single event scatters across several threads, and a
confirmation from a *different* company about the same order never joins at
all. Measured on one real case: six messages, five threads.

What actually ties those together is a shared identifier -- an order number, a
task id, a reference -- printed in the body or buried in a URL. That is the
strong signal here, and the only one that crosses senders. Domain and subject
similarity are weak fallbacks for messages that carry no such number.

Every association is returned with the reason for it. A cluster whose members
cannot be challenged is worse than no cluster: it invites confident answers
built on a message that was never related.
"""

import re
from concurrent.futures import ThreadPoolExecutor
from datetime import timedelta, timezone
from email.utils import parsedate_to_datetime

# Order numbers, task ids, invoice references. Shorter than 6 digits is noise;
# longer than 14 is usually a tracking blob.
IDENT_RE = re.compile(r"\b\d{6,14}\b")

# A number introduced by a word like "order" is a key; a bare digit run in a
# footer is a PO box or an app-store link id. Preferring labelled numbers is
# what separates 1623209215 (the order) from 258595750 (onelink.me) and
# 530225 (a PO box) in the same message.
KEYED_IDENT_RE = re.compile(
    r"(?:order|task|invoice|reference|ref|booking|confirmation|"
    r"policy|claim|case|ticket|shipment|tracking)\W{0,12}(\d{6,14})",
    re.I,
)

# Boilerplate that says nothing about which event a message concerns.
SUBJECT_NOISE = re.compile(
    r"^\s*((re|fwd|fw|aw)\s*:|updates?\s+to\s*:|heads\s+up!?|reminder\s*:|"
    r"notification\s*:|automatic\s+reply\s*:)\s*",
    re.I,
)

STOPWORDS = {
    "your", "you", "the", "and", "for", "with", "from", "this", "that", "has",
    "have", "been", "will", "are", "was", "our", "about", "info", "information",
    "please", "thanks", "thank", "email", "message", "update", "updates",
    "confirmed", "complete", "completed", "upcoming", "change", "changes",
}

# An identifier that matches half the mailbox is a date or a price, not a key.
TOO_COMMON = 25
MAX_IDENTIFIERS = 5
DEFAULT_WINDOW_DAYS = 30
DEFAULT_LIMIT = 40


def _parse_date(value):
    """Always timezone-aware. Some senders omit the offset, and a mailbox will
    happily mix both kinds inside one cluster -- sorting them together throws."""
    try:
        parsed = parsedate_to_datetime(value)
    except (TypeError, ValueError):
        return None
    if parsed is not None and parsed.tzinfo is None:
        return parsed.replace(tzinfo=timezone.utc)
    return parsed


def _plausible(match):
    if len(match) == 8 and match[:2] in ("19", "20"):
        return False                      # yyyymmdd
    if len(match) == 6 and match.startswith(("19", "20")):
        return False                      # yyyymm
    return True


def identifiers(text):
    """Digit runs that look like keys, labelled ones first.

    Falls back to bare runs only when nothing in the message is labelled --
    a message that says "Order #1623209215" should not also drag in the PO box
    number from its own footer.
    """
    text = text or ""
    for pattern in (KEYED_IDENT_RE, IDENT_RE):
        out = []
        for match in pattern.findall(text):
            if _plausible(match) and match not in out:
                out.append(match)
        if out:
            return out
    return []


def subject_tokens(subject):
    core = SUBJECT_NOISE.sub("", subject or "")
    words = re.findall(r"[A-Za-z][A-Za-z0-9]{3,}", core.lower())
    seen = []
    for w in words:
        if w not in STOPWORDS and w not in seen:
            seen.append(w)
    return seen[:4]


def _domain(address):
    m = re.search(r"@([A-Za-z0-9.-]+)", address or "")
    if not m:
        return None
    host = m.group(1).lower().strip(">").rstrip(".")
    parts = host.split(".")
    # Keep the registrable-ish tail so no-reply@mail.corp.co.uk still matches
    # notifications@corp.co.uk.
    if len(parts) > 2 and parts[-2] in {"co", "com", "org", "net", "ac", "gov"}:
        return ".".join(parts[-3:])
    return ".".join(parts[-2:])


def build(backend, msg_id, window_days=DEFAULT_WINDOW_DAYS, limit=DEFAULT_LIMIT,
          min_score=2):
    seed = backend.message(msg_id, include_remote=True)
    seed_when = _parse_date(seed.get("date"))

    haystack = " ".join(filter(None, [
        seed.get("subject"), seed.get("body"),
        " ".join(seed.get("remote_image_urls") or []),
    ]))
    keys = identifiers(haystack)[:MAX_IDENTIFIERS]
    tokens = subject_tokens(seed.get("subject"))
    domain = _domain(seed.get("from"))

    # Each probe is (query, reason, weight). Identifier probes are checked for
    # distinctiveness by how many hits they return, which needs no tuning.
    probes = [(backend.q_exact(k), f"shares identifier {k}", 5) for k in keys]
    if domain and seed_when:
        probes.append((
            backend.q_from_domain_between(
                domain, seed_when - timedelta(days=window_days),
                seed_when + timedelta(days=window_days)),
            f"same sender domain ({domain}) within {window_days} days", 2))
    if tokens and seed_when:
        # Bounded like the domain probe. Unbounded, "ikea OR assembly" matches
        # a food hall newsletter from three years ago -- the tokens are only
        # meaningful near the event they describe.
        probes.append((
            backend.q_subject_tokens(tokens)
            + f" after:{(seed_when - timedelta(days=window_days)):%Y/%m/%d}"
            + f" before:{(seed_when + timedelta(days=window_days)):%Y/%m/%d}",
            "subject overlap: " + ", ".join(tokens), 1))

    def run(probe):
        query, reason, weight = probe
        try:
            return probe, backend.search(query, limit=limit)
        except Exception:
            return probe, []

    with ThreadPoolExecutor(max_workers=6) as pool:
        results = list(pool.map(run, probes))

    scored = {}
    dropped = []
    for (query, reason, weight), hits in results:
        if weight == 5 and len(hits) > TOO_COMMON:
            dropped.append({"query": query, "reason": reason, "hits": len(hits)})
            continue
        for hit in hits:
            if hit["id"] == msg_id:
                continue
            row = scored.setdefault(hit["id"], {**hit, "score": 0, "why": []})
            row["score"] += weight
            row["why"].append(reason)

    for row in scored.values():
        if row.get("thread_id") and row["thread_id"] == seed.get("thread_id"):
            row["score"] += 3
            row["why"].append("same Gmail thread")

    # One weak signal on its own is not evidence. Same-domain-within-a-window
    # scores 2, a shared identifier 5; a bare subject-token match scores 1 and
    # is held back unless the caller lowers the bar.
    kept = [r for r in scored.values() if r["score"] >= min_score]
    kept.sort(key=lambda r: (_parse_date(r.get("date")) or seed_when))
    return {
        "seed": {k: seed.get(k) for k in
                 ("id", "thread_id", "date", "from", "subject")},
        "signals": {"identifiers": keys, "subject_tokens": tokens,
                    "domain": domain},
        "dropped_as_too_common": dropped,
        "below_min_score": len(scored) - len(kept),
        "related": kept[:limit],
    }
