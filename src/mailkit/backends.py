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
import mimetypes
import os
import re
import shutil
import struct
import subprocess
import tempfile
import urllib.error
import urllib.request
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

IMG_SRC_RE = re.compile(r'<img[^>]+src\s*=\s*["\']([^"\']+)["\']', re.I)

# A browser-ish agent: some CDNs refuse python-urllib outright.
USER_AGENT = ("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 "
              "(KHTML, like Gecko) Chrome/126.0 Safari/537.36")

REMOTE_IMAGE_CAP = 25 * 1024 * 1024


class BackendError(Exception):
    pass


# ------------------------------------------------------------------ gmail

def _gws(args, timeout=180, cwd=None):
    # gws reads its own credentials from ~/.config/gws. An explicitly exported
    # GOOGLE_WORKSPACE_CLI_TOKEN still wins, which is the escape hatch if the
    # keyring bug (googleworkspace/cli#361, closed in 0.9.x) ever resurfaces --
    # but we no longer inject one, because an ADC token cannot carry Gmail
    # scopes: Google blocks gcloud's OAuth client from restricted scopes.
    env = os.environ.copy()
    if not env.get("GOOGLE_WORKSPACE_CLI_TOKEN"):
        env.pop("GOOGLE_WORKSPACE_CLI_TOKEN", None)
    try:
        r = subprocess.run(["gws"] + args, capture_output=True, text=True,
                           timeout=timeout, env=env, cwd=cwd)
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
        # Reversed, so popping restores document order: "the first attachment"
        # has to mean the first one in the message.
        stack.extend(reversed(p.get("parts") or []))


def _headers(msg):
    hs = ((msg.get("payload") or {}).get("headers")) or []
    return {h["name"].lower(): h["value"] for h in hs if "name" in h}


def _safe_name(name, fallback="file"):
    """A filename safe to write, derived from something we did not author."""
    name = re.sub(r"[^A-Za-z0-9._-]+", "-", (name or "").strip()).strip("-.")
    return (name or fallback)[:120]


def _image_dims(path):
    """(width, height) for the common formats, without pulling in Pillow.

    Used only to label an image, never to decide whether to keep it -- the
    caller downloads everything and filters afterwards.
    """
    try:
        with path.open("rb") as fh:
            head = fh.read(32)
    except OSError:
        return None
    if head[:8] == b"\x89PNG\r\n\x1a\n" and head[12:16] == b"IHDR":
        return struct.unpack(">II", head[16:24])
    if head[:6] in (b"GIF87a", b"GIF89a"):
        return struct.unpack("<HH", head[6:10])
    if head[:2] == b"\xff\xd8":  # JPEG: walk the segment chain to an SOF
        try:
            with path.open("rb") as fh:
                fh.seek(2)
                while True:
                    marker = fh.read(2)
                    if len(marker) < 2 or marker[0] != 0xFF:
                        return None
                    if 0xC0 <= marker[1] <= 0xCF and marker[1] not in (0xC4, 0xC8, 0xCC):
                        fh.read(3)
                        h, w = struct.unpack(">HH", fh.read(4))
                        return (w, h)
                    size = struct.unpack(">H", fh.read(2))[0]
                    fh.seek(size - 2, 1)
        except (OSError, struct.error):
            return None
    return None


def classify_image(width, height):
    """Tracking pixels and UI chrome are still downloaded -- just labelled, so
    a real screenshot does not have to be picked out of five store badges."""
    if not width or not height:
        return "image"
    if width <= 2 or height <= 2:
        return "pixel"
    # Both a minimum edge and a minimum area: a wordmark is wide but short
    # (319x43) and an avatar is square but tiny (108x108); neither carries
    # anything worth reading, and only one of those two tests catches each.
    if min(width, height) < 100 or width * height < 40_000:
        return "small"
    return "image"


class GmailBackend:
    name = "gmail"

    _account_cache = None

    def account(self):
        """The address gws is authenticated as, or None if not usable.

        Asking Gmail who we are doubles as proof the token actually works,
        which `gws auth status` cannot tell us under token injection.
        """
        if self._account_cache is not None:
            return self._account_cache or None
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

    def message(self, msg_id, include_remote=False):
        """Full message: headers, plain-text body, attachment manifest.

        include_remote adds the URLs of images the body only references; they
        are listed, not downloaded, so the caller decides whether to reach out
        to the sender's server."""
        msg = _gws_json(
            ["gmail", "users", "messages", "get"]
            + _params(userId="me", id=msg_id, format="full")
        )
        if not msg:
            raise BackendError(f"no such message: {msg_id}")
        h = _headers(msg)
        result = {
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
        if include_remote:
            result["remote_image_urls"] = self.remote_images(msg_id, _msg=msg)
        return result

    def _body(self, msg_id):
        # +read already handles multipart/alternative, base64 and html->text.
        try:
            return _gws(["gmail", "+read", "--id", msg_id]).strip()
        except BackendError as e:
            return f"(body unavailable: {e})"

    def attachments(self, msg_id, _msg=None):
        """Every part Gmail stores as a separate blob.

        A part is an attachment if it has an attachmentId -- not if it has a
        filename. Images embedded in the body by Content-ID often carry no
        filename at all, and those were being dropped, which is exactly the
        kind of part that turns out to hold the actual information.
        """
        msg = _msg or _gws_json(
            ["gmail", "users", "messages", "get"]
            + _params(userId="me", id=msg_id, format="full")
        )
        out = []
        for i, part in enumerate(_walk((msg or {}).get("payload") or {})):
            body = part.get("body") or {}
            if not body.get("attachmentId"):
                continue
            headers = {h["name"].lower(): h.get("value", "")
                       for h in (part.get("headers") or [])}
            cid = (headers.get("content-id") or "").strip("<>")
            disposition = (headers.get("content-disposition") or "").lower()
            mime = part.get("mimeType") or "application/octet-stream"
            name = part.get("filename") or ""
            if not name:
                ext = mimetypes.guess_extension(mime.split(";")[0]) or ".bin"
                name = _safe_name(cid, f"inline-{i}") + ext
            out.append({
                "id": body["attachmentId"],
                "name": _safe_name(name, f"part-{i}"),
                "size": body.get("size", 0),
                "mime_type": mime,
                "inline": bool(cid) or "inline" in disposition,
                "content_id": cid or None,
            })
        return out

    def html_body(self, msg_id, _msg=None):
        msg = _msg or _gws_json(
            ["gmail", "users", "messages", "get"]
            + _params(userId="me", id=msg_id, format="full")
        )
        for part in _walk((msg or {}).get("payload") or {}):
            if part.get("mimeType") == "text/html":
                data = (part.get("body") or {}).get("data") or ""
                if data:
                    raw = base64.urlsafe_b64decode(data + "=" * (-len(data) % 4))
                    return raw.decode("utf-8", "replace")
        return ""

    def remote_images(self, msg_id, _msg=None):
        """http(s) <img> sources in the HTML body, in document order."""
        html = self.html_body(msg_id, _msg=_msg)
        urls = [u for u in IMG_SRC_RE.findall(html) if u.lower().startswith("http")]
        return list(dict.fromkeys(urls))

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

    def fetch_remote(self, url, out_dir: Path):
        """Download one remote image. Fetching it tells the sender the mail was
        opened -- that is inherent to remote images, and accepted here; the
        information in the picture is the point."""
        out_dir.mkdir(parents=True, exist_ok=True)
        base = _safe_name(url.split("?")[0].rsplit("/", 1)[-1], "image")
        dest = out_dir / base
        n = 1
        while dest.exists():
            dest = out_dir / f"{dest.stem}-{n}{dest.suffix}"
            n += 1
        req = urllib.request.Request(url, headers={"User-Agent": USER_AGENT})
        try:
            with urllib.request.urlopen(req, timeout=30) as resp:
                blob = resp.read(REMOTE_IMAGE_CAP + 1)
        except (urllib.error.URLError, OSError, ValueError) as e:
            return {"url": url, "error": str(e)}
        if len(blob) > REMOTE_IMAGE_CAP:
            return {"url": url, "error": "larger than 25 MB, skipped"}
        if not dest.suffix:
            dest = dest.with_suffix(".bin")
        dest.write_bytes(blob)
        dims = _image_dims(dest)
        width, height = dims if dims else (None, None)
        return {
            "url": url,
            "path": str(dest),
            "size": len(blob),
            "width": width,
            "height": height,
            "kind": classify_image(width, height),
        }

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

        attachments = draft.get("attachments") or []
        if not attachments:
            return _gws(cmd).strip()

        # gws refuses any -a path that resolves outside the working directory.
        # Drafts hold absolute paths (they have to: the sha256 pin is checked
        # against the real file), so stage copies under one temp root and run
        # from there. Each file gets its own numbered subdirectory so two
        # attachments sharing a basename cannot collide.
        with tempfile.TemporaryDirectory(prefix="mailkit-send-") as staging:
            for i, att in enumerate(attachments):
                sub = Path(staging) / str(i)
                sub.mkdir()
                shutil.copy2(att["path"], sub / att["name"])
                cmd += ["-a", f"{i}/{att['name']}"]
            return _gws(cmd, cwd=staging).strip()


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
    html_body = remote_images = fetch_remote = _blocked


BACKENDS = {"gmail": GmailBackend, "outlook": OutlookBackend}


def get_backend(name):
    if name not in BACKENDS:
        raise BackendError(f"unknown account {name!r}; pick one of {', '.join(BACKENDS)}")
    return BACKENDS[name]()
