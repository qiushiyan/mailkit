#!/usr/bin/env python3
"""First contact with a real Outlook mailbox.

The Gmail implementation was only ever correct because it was checked against
real mail; this runs the same checks on the second provider. Each one names a
failure that already happened once on Gmail, so a pass here is evidence and not
optimism.

    python3 scripts/verify-outlook.py
"""

import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "src"))

from mailkit import transcript                        # noqa: E402
from mailkit.backends import BackendError, get_backend  # noqa: E402


def head(n, text):
    print(f"\n{'=' * 62}\n{n}. {text}\n{'=' * 62}")


def main():
    b = get_backend("outlook")
    who = b.account()
    if not who:
        print("not logged in -- run:  m365 login --appId <id> --tenant <id>")
        return 1
    print(f"connected as {who}")

    head(1, "Search returns anything at all")
    hits = b.search("search:mail", limit=5)
    for h in hits:
        print(f"  {h['id'][:24]}  {(h.get('subject') or '')[:50]}")
    if not hits:
        print("  no results -- check Mail.Read consent")
        return 1

    head(2, "A reply chain folds when the only quoting is a From:/Sent: block")
    replies = b.search("search:subject:RE", limit=5)
    if not replies:
        print("  no reply found to test")
    else:
        msg = b.message(replies[0]["id"])
        spoken, quoted, marker = transcript.split_quote(msg["body"])
        print(f"  {(msg.get('subject') or '')[:52]}")
        print(f"  body {len(msg['body'])} chars -> spoken {len(spoken)}, "
              f"quoted {len(quoted)}, marker={marker}")
        if quoted and marker is None:
            print("  FAIL: quoted text present but no boundary recognised")

    head(3, "uniqueBody and our own folding agree")
    if replies:
        msg = b.message(replies[0]["id"])
        ours, _, _ = transcript.split_quote(msg["body"])
        theirs = msg.get("provider_unique_body")
        if not theirs:
            print("  uniqueBody not returned")
        else:
            print(f"  ours {len(ours)} chars vs uniqueBody {len(theirs)} chars")
            print("  agree" if abs(len(ours) - len(theirs)) < 80
                  else "  DIVERGE -- read both before trusting either")

    head(4, "A forward survives whole")
    fwd = b.search("search:subject:FW", limit=3)
    if not fwd:
        print("  no forward found to test")
    for f in fwd[:1]:
        msg = b.message(f["id"])
        r = transcript.build([msg])
        t = r["turns"][0]
        print(f"  raw {r['raw_chars']} -> kept {r['transcript_chars']}")
        if t["fold_rejected"]:
            print(f"  held: {t['fold_rejected']}")
        elif t["quoted_chars"]:
            print("  FAIL: a forward was folded; its content has no copy upstream")

    head(5, "Attachment kinds Gmail never produced")
    withatt = b.search("search:hasAttachments:true", limit=8)
    seen = {}
    for h in withatt:
        for a in b.attachments(h["id"]):
            seen.setdefault(a["kind"], []).append((h["id"], a["name"]))
    for kind, rows in seen.items():
        print(f"  {kind:<18} {len(rows)}  e.g. {rows[0][1][:40]}")
    for kind in ("cloud_link", "embedded_message"):
        if kind in seen:
            print(f"  NOTE: {kind} present -- no bytes fetchable from the message; "
                  f"the shared layer must not report it as absent")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except BackendError as e:
        print(f"backend error: {e}")
        sys.exit(1)
