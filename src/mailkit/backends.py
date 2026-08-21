"""Transport backends. Everything that actually touches a mail provider lives here.

The rest of mailkit is provider-agnostic on purpose: drafts, previews and the
approval gate work the same whichever account a message goes out on. A backend
only has to know how to search, read, pull an attachment, and send.

Gmail goes through `gws` (Google Workspace CLI, OAuth, creds in the OS keyring).
Outlook would go through `m365` (Graph API); that leg is deliberately not wired
up yet -- see README.
"""

import base64
import json
import subprocess
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path


class BackendError(Exception):
    pass


# ------------------------------------------------------------------ gmail


def _gws(args, timeout=180):
    try:
        r = subprocess.run(["gws"] + args, capture_output=True, text=True, timeout=timeout)
    except FileNotFoundError:
        raise BackendError("`gws` not found -- npm i -g @googleworkspace/cli")
    except subprocess.TimeoutExpired:
        raise BackendError(f"gws timed out: {' '.join(args[:3])}")
    if r.returncode != 0:
        msg = (r.stderr or r.stdout).strip().splitlines()
        raise BackendError(msg[-1] if msg else f"gws exited {r.returncode}")
    return r.stdout


def _gws_json(args):
    out = _gws(args).strip()
    if not out:
        return None
    try:
        return json.loads(out)
    except json.JSONDecodeError:
        # gws prints the "not an officially supported product" banner on some
        # paths; the payload is the last JSON-looking line.
        for line in reversed(out.splitlines()):
            line = line.strip()
            if line.startswith(("{", "[")):
                return json.loads(line)
        raise BackendError(f"could not parse gws output: {out[:200]}")


def _params(**kw):
    return ["--params", json.dumps({k: v for k, v in kw.items() if v is not None})]


def _walk(part):
    """Depth-first over a Gmail MIME part tree."""
    stack = [part]
    while stack:
        p = stack.pop()
        yield p
        stack.extend(p.get("parts") or [])


def _headers(msg):
    hs = ((msg.get("payload") or {}).get("headers")) or []
    return {h["name"].lower(): h["value"] for h in hs if "name" in h}


class GmailBackend:
    name = "gmail"

    _account_cache = None

    def account(self):
        """The address gws is authenticated as, or None if logged out."""
        if self._account_cache is not None:
            return self._account_cache or None
        try:
            status = _gws_json(["auth", "status"]) or {}
        except BackendError:
            return None
        # `auth status` reports storage but not identity, so ask Gmail who we
        # are -- and that doubles as proof the token actually works.
        if status.get("auth_method") in (None, "none"):
            self._account_cache = ""
            return None
        try:
            profile = _gws_json(["gmail", "users", "getProfile"] + _params(userId="me")) or {}
        except BackendError:
            self._account_cache = ""
            return None
        self._account_cache = profile.get("emailAddress") or ""
        return self._account_cache or None

    def search(self, query, limit=25):
        """Gmail's own search syntax -- `from:`, `has:attachment`, `newer_than:7d`."""
        listing = _gws_json(
            ["gmail", "users", "messages", "list"]
            + _params(userId="me", q=query, maxResults=limit)
        ) or {}
        ids = [m["id"] for m in (listing.get("messages") or [])][:limit]
        if not ids:
            return []
        # One metadata fetch per hit; Gmail's list only returns bare ids.
        with ThreadPoolExecutor(max_workers=8) as pool:
            msgs = list(pool.map(self._meta, ids))
        return [m for m in msgs if m]

    def _meta(self, msg_id):
        try:
            msg = _gws_json(
                ["gmail", "users", "messages", "get"]
                + _params(userId="me", id=msg_id, format="metadata")
            )
        except BackendError:
            return None
        h = _headers(msg)
        return {
            "id": msg_id,
            "thread_id": msg.get("threadId"),
            "date": h.get("date"),
            "from": h.get("from"),
            "to": h.get("to"),
            "cc": h.get("cc"),
            "subject": h.get("subject"),
            "snippet": msg.get("snippet"),
            "labels": msg.get("labelIds"),
        }

    def message(self, msg_id):
        """Full message: headers, plain-text body, attachment manifest."""
        msg = _gws_json(
            ["gmail", "users", "messages", "get"]
            + _params(userId="me", id=msg_id, format="full")
        )
        if not msg:
            raise BackendError(f"no such message: {msg_id}")
        h = _headers(msg)
        return {
            "id": msg_id,
            "thread_id": msg.get("threadId"),
            "date": h.get("date"),
            "from": h.get("from"),
            "to": h.get("to"),
            "cc": h.get("cc"),
            "subject": h.get("subject"),
            "snippet": msg.get("snippet"),
            "body": self._body(msg_id),
            "attachments": self.attachments(msg_id, _msg=msg),
        }

    def _body(self, msg_id):
        # +read already handles multipart/alternative, base64 and html->text.
        try:
            return _gws(["gmail", "+read", "--id", msg_id]).strip()
        except BackendError as e:
            return f"(body unavailable: {e})"

    def attachments(self, msg_id, _msg=None):
        msg = _msg or _gws_json(
            ["gmail", "users", "messages", "get"]
            + _params(userId="me", id=msg_id, format="full")
        )
        out = []
        for part in _walk((msg or {}).get("payload") or {}):
            filename = part.get("filename") or ""
            body = part.get("body") or {}
            if filename and body.get("attachmentId"):
                out.append({
                    "id": body["attachmentId"],
                    "name": filename,
                    "size": body.get("size", 0),
                    "mime_type": part.get("mimeType"),
                })
        return out

    def fetch_attachment(self, msg_id, attachment_id, dest: Path):
        data = _gws_json(
            ["gmail", "users", "messages", "attachments", "get"]
            + _params(userId="me", messageId=msg_id, id=attachment_id)
        )
        raw = (data or {}).get("data")
        if not raw:
            raise BackendError(f"attachment {attachment_id} returned no data")
        # Gmail uses base64url without padding.
        blob = base64.urlsafe_b64decode(raw + "=" * (-len(raw) % 4))
        dest.parent.mkdir(parents=True, exist_ok=True)
        dest.write_bytes(blob)
        return len(blob)

    # Gmail caps a message at 25 MB including MIME overhead.
    attachment_limit = 25 * 1024 * 1024

    def send(self, draft):
        cmd = ["gmail", "+send",
               "--to", ",".join(draft["to"]),
               "--subject", draft["subject"],
               "--body", draft["body"]]
        if draft.get("cc"):
            cmd += ["--cc", ",".join(draft["cc"])]
        if draft.get("bcc"):
            cmd += ["--bcc", ",".join(draft["bcc"])]
        if draft.get("body_type") == "HTML":
            cmd += ["--html"]
        if draft.get("sender_override"):
            cmd += ["--from", draft["sender_override"]]
        for att in draft.get("attachments") or []:
            cmd += ["-a", att["path"]]
        return _gws(cmd).strip()


# ---------------------------------------------------------------- outlook


class OutlookBackend:
    """Placeholder. The Graph path is designed but blocked on an Entra app
    registration in the planlab tenant -- see README, "Outlook status"."""

    name = "outlook"
    attachment_limit = 3 * 1024 * 1024  # Graph inlines attachments; ~4 MB request cap

    def _blocked(self, *_a, **_kw):
        raise BackendError(
            "the outlook backend is not wired up yet -- it needs an Entra app "
            "registration first. Use --account gmail."
        )

    account = search = message = attachments = fetch_attachment = send = _blocked


BACKENDS = {"gmail": GmailBackend, "outlook": OutlookBackend}


def get_backend(name):
    if name not in BACKENDS:
        raise BackendError(f"unknown account {name!r}; pick one of {', '.join(BACKENDS)}")
    return BACKENDS[name]()
