"""Renders a draft as a local HTML page -- the thing I actually look at.

Deliberately a file on disk opened with `open`, not a provider-side draft: a
Gmail draft would mean opening a browser, which is the chore this whole tool
exists to remove, and it would not be pinned to what finally gets sent.
"""

import html

from .drafts import human_size

CSS = """
:root {
  --bg:#f6f6f4; --card:#fff; --ink:#1c1c1a; --muted:#6b6b66;
  --line:#e2e2dd; --accent:#7a5cff; --warn-bg:#fff4e0; --warn-ink:#7a4a00;
}
@media (prefers-color-scheme: dark) {
  :root {
    --bg:#16161a; --card:#1e1e23; --ink:#e8e8e4; --muted:#9a9a94;
    --line:#2e2e35; --accent:#a68bff; --warn-bg:#3a2c12; --warn-ink:#f0c987;
  }
}
*{box-sizing:border-box}
body{margin:0;padding:2.5rem 1.25rem 4rem;background:var(--bg);color:var(--ink);
  font:15px/1.6 ui-sans-serif,-apple-system,system-ui,sans-serif}
main{max-width:44rem;margin:0 auto}
.tag{display:inline-block;font-size:11px;letter-spacing:.09em;text-transform:uppercase;
  font-weight:600;color:var(--accent);margin-bottom:.5rem}
h1{font-size:1.5rem;line-height:1.3;margin:0 0 1.5rem;font-weight:600}
h2{font-size:12px;letter-spacing:.08em;text-transform:uppercase;color:var(--muted);
  margin:0 0 .85rem;font-weight:600}
.card{background:var(--card);border:1px solid var(--line);border-radius:12px;
  padding:1.25rem 1.5rem;margin-bottom:1.25rem}
dl{display:grid;grid-template-columns:5.5rem 1fr;gap:.45rem 1rem;margin:0}
dt{color:var(--muted);font-size:13px}
dd{margin:0;word-break:break-word}
.body{white-space:pre-wrap;word-wrap:break-word}
.body.html-mode{white-space:normal}
ul.att{list-style:none;margin:0;padding:0}
ul.att li{display:flex;justify-content:space-between;gap:1rem;padding:.4rem 0;
  border-bottom:1px solid var(--line)}
ul.att li:last-child{border-bottom:0}
ul.att .size{color:var(--muted);font-size:13px;white-space:nowrap}
.warn{background:var(--warn-bg);color:var(--warn-ink);border-radius:8px;
  padding:.7rem 1rem;margin-bottom:1.25rem;font-size:14px}
pre.cmd{background:var(--card);border:1px solid var(--line);border-radius:10px;
  padding:.9rem 1.1rem;overflow-x:auto;margin:0;
  font:13px/1.5 ui-monospace,SFMono-Regular,Menlo,monospace}
.foot{color:var(--muted);font-size:13px;margin-top:.75rem}
"""


def render(draft):
    e = html.escape

    def row(label, value):
        return f"<dt>{label}</dt><dd>{e(value)}</dd>" if value else ""

    meta = "".join([
        row("Account", draft["account"]),
        row("From", draft["from"] or "(not logged in)"),
        row("To", ", ".join(draft["to"])),
        row("Cc", ", ".join(draft["cc"])),
        row("Bcc", ", ".join(draft["bcc"])),
        row("Format", draft["body_type"]),
    ])

    att_block = ""
    if draft["attachments"]:
        items = "".join(
            f'<li><span>{e(a["name"])}</span>'
            f'<span class="size">{human_size(a["size"])}</span></li>'
            for a in draft["attachments"]
        )
        total = sum(a["size"] for a in draft["attachments"])
        att_block = (f'<div class="card"><h2>Attachments &middot; {human_size(total)} total</h2>'
                     f'<ul class="att">{items}</ul></div>')

    if draft["body_type"] == "HTML":
        body_html = f'<div class="body html-mode">{draft["body"]}</div>'
    else:
        body_html = f'<div class="body">{e(draft["body"])}</div>'

    warn = ""
    if not draft["from"]:
        warn = ('<div class="warn">Not authenticated for this account &mdash; the send '
                'will be refused until you log in.</div>')

    return f"""<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Draft — {e(draft['subject'])}</title>
<style>{CSS}</style></head>
<body><main>
  <div class="tag">Draft &middot; not sent</div>
  <h1>{e(draft['subject'])}</h1>
  {warn}
  <div class="card"><dl>{meta}</dl></div>
  <div class="card"><h2>Body</h2>{body_html}</div>
  {att_block}
  <h2>Send it</h2>
  <pre class="cmd">send-mail --commit {e(draft['id'])}</pre>
  <p class="foot">Drafted {e(draft['created_at'])}. Single-use: committing marks this
  draft sent, and an attachment edited after this preview blocks the send.</p>
</main></body></html>
"""
