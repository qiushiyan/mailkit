"""The approval gate: draft on disk, preview, then a pinned single-use send.

Sending is irreversible and an agent is composing on my behalf, so composing and
sending are separate acts. Composing writes a draft here and renders a preview;
nothing leaves the machine. Sending takes a draft *id*, never a fresh set of
flags, so the bytes that go out are the bytes that were reviewed.

Three things keep that promise honest:
  - every attachment's sha256 is recorded at draft time and re-checked at send
  - a draft carries a sent_at stamp and is refused once it is set
  - the account is fixed at draft time, so the preview's From line cannot lie
"""

import hashlib
import json
import os
import re
from datetime import datetime
from pathlib import Path

STATE = Path(os.environ.get("XDG_STATE_HOME", Path.home() / ".local/state")) / "mailkit/drafts"

EMAIL_RE = re.compile(r"^[^@\s]+@[^@\s]+\.[^@\s]+$")


class DraftError(Exception):
    pass


def sha256(path):
    h = hashlib.sha256()
    with open(path, "rb") as fh:
        for chunk in iter(lambda: fh.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def human_size(n):
    if n < 1024:
        return f"{n} B"
    if n < 1024 ** 2:
        return f"{n / 1024:.1f} KB"
    return f"{n / 1024 ** 2:.1f} MB"


def split_addrs(value):
    return [a.strip() for a in (value or "").split(",") if a.strip()]


def _slug(text):
    s = re.sub(r"[^a-z0-9]+", "-", (text or "").lower()).strip("-")
    return s[:40] or "no-subject"


def create(*, account, backend_account, to, cc, bcc, subject, body,
           body_type="Text", attachments=(), sender_override=None):
    to, cc, bcc = split_addrs(to), split_addrs(cc), split_addrs(bcc)
    if not to:
        raise DraftError("at least one --to recipient is required")
    for addr in to + cc + bcc:
        if not EMAIL_RE.match(addr):
            raise DraftError(f"{addr!r} does not look like an email address")
    if not (body or "").strip():
        raise DraftError("refusing to draft an empty body")

    files = []
    for raw in attachments:
        p = Path(raw).expanduser().resolve()
        if not p.is_file():
            raise DraftError(f"attachment not found: {p}")
        files.append({"path": str(p), "name": p.name,
                      "size": p.stat().st_size, "sha256": sha256(p)})

    now = datetime.now()
    draft = {
        "id": f"{now:%Y%m%d-%H%M%S}-{_slug(subject)}",
        "created_at": now.isoformat(timespec="seconds"),
        "account": account,
        "from": sender_override or backend_account,
        "sender_override": sender_override,
        "to": to, "cc": cc, "bcc": bcc,
        "subject": subject or "",
        "body": body,
        "body_type": body_type,
        "attachments": files,
        "sent_at": None,
    }
    STATE.mkdir(parents=True, exist_ok=True)
    path(draft["id"]).write_text(json.dumps(draft, indent=2, ensure_ascii=False))
    return draft


def path(draft_id):
    return STATE / f"{draft_id}.json"


def preview_path(draft_id):
    return STATE / f"{draft_id}.html"


def load(draft_id):
    p = path(draft_id)
    if not p.is_file():
        raise DraftError(f"no such draft: {draft_id}")
    return json.loads(p.read_text())


def verify_sendable(draft):
    """Everything that must still hold between preview and send."""
    if draft.get("sent_at"):
        raise DraftError(f"draft already sent at {draft['sent_at']}; drafts are single-use")
    for att in draft.get("attachments") or []:
        p = Path(att["path"])
        if not p.is_file():
            raise DraftError(f"attachment vanished since preview: {p}")
        if sha256(p) != att["sha256"]:
            raise DraftError(f"attachment changed since preview: {p} -- re-draft to review it")


def mark_sent(draft, sent_as):
    draft["sent_at"] = datetime.now().isoformat(timespec="seconds")
    draft["sent_as"] = sent_as
    path(draft["id"]).write_text(json.dumps(draft, indent=2, ensure_ascii=False))


def recent(limit=30):
    if not STATE.is_dir():
        return []
    out = []
    for p in sorted(STATE.glob("*.json"), reverse=True)[:limit]:
        try:
            out.append(json.loads(p.read_text()))
        except json.JSONDecodeError:
            continue
    return out
