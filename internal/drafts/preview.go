package drafts

import (
	"encoding/base64"
	"fmt"
	"html"
	"io"
	"mime/quotedprintable"
	"strings"
)

func decode(cte string, r io.Reader) io.Reader {
	switch strings.ToLower(strings.TrimSpace(cte)) {
	case "base64":
		return base64.NewDecoder(base64.StdEncoding, r)
	case "quoted-printable":
		return quotedprintable.NewReader(r)
	}
	return r
}

// HumanSize renders bytes for people.
func HumanSize(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
}

const previewCSS = `
:root{--bg:#f6f6f4;--card:#fff;--ink:#1c1c1a;--muted:#6b6b66;--line:#e2e2dd;--accent:#7a5cff;--warn-bg:#fff4e0;--warn-ink:#7a4a00}
@media (prefers-color-scheme:dark){:root{--bg:#16161a;--card:#1e1e23;--ink:#e8e8e4;--muted:#9a9a94;--line:#2e2e35;--accent:#a68bff;--warn-bg:#3a2c12;--warn-ink:#f0c987}}
*{box-sizing:border-box}body{margin:0;padding:2.5rem 1.25rem 4rem;background:var(--bg);color:var(--ink);font:15px/1.6 ui-sans-serif,-apple-system,system-ui,sans-serif}
main{max-width:44rem;margin:0 auto}.tag{display:inline-block;font-size:11px;letter-spacing:.09em;text-transform:uppercase;font-weight:600;color:var(--accent);margin-bottom:.5rem}
h1{font-size:1.5rem;line-height:1.3;margin:0 0 1.5rem;font-weight:600}h2{font-size:12px;letter-spacing:.08em;text-transform:uppercase;color:var(--muted);margin:0 0 .85rem;font-weight:600}
.card{background:var(--card);border:1px solid var(--line);border-radius:12px;padding:1.25rem 1.5rem;margin-bottom:1.25rem}
dl{display:grid;grid-template-columns:5.5rem 1fr;gap:.45rem 1rem;margin:0}dt{color:var(--muted);font-size:13px}dd{margin:0;word-break:break-word}
.body{white-space:pre-wrap;word-wrap:break-word}
iframe.body-frame{width:100%;height:24rem;border:1px solid var(--line);border-radius:8px;background:#fff}
details.alt{margin-top:.85rem}details.alt summary{cursor:pointer;color:var(--muted);font-size:13px}details.alt .body{margin-top:.6rem}
ul.att{list-style:none;margin:0;padding:0}ul.att li{display:flex;justify-content:space-between;gap:1rem;padding:.4rem 0;border-bottom:1px solid var(--line)}ul.att li:last-child{border-bottom:0}ul.att .size{color:var(--muted);font-size:13px;white-space:nowrap}
pre.cmd{background:var(--card);border:1px solid var(--line);border-radius:10px;padding:.9rem 1.1rem;overflow-x:auto;margin:0;font:13px/1.5 ui-monospace,SFMono-Regular,Menlo,monospace}
.foot{color:var(--muted);font-size:13px;margin-top:.75rem}
`

// RenderPreview is the page a person looks at before committing. It is
// rendered from the parsed wire message, so it shows what will be sent.
func RenderPreview(rec Record, p Parsed) string {
	e := html.EscapeString
	row := func(label, value string) string {
		if value == "" {
			return ""
		}
		return fmt.Sprintf("<dt>%s</dt><dd>%s</dd>", label, e(value))
	}
	// The format row states the actual MIME shape, never the input language:
	// the wire retains no markdown provenance and the page must not claim
	// metadata the bytes do not carry.
	format := "Text"
	switch {
	case p.HasHTML && p.HasText:
		format = "Text + HTML"
	case p.HasHTML:
		format = "HTML"
	}
	meta := row("Account", rec.Account) + row("From", p.From) + row("To", p.To) + row("Cc", p.Cc) +
		row("Bcc", p.Bcc) + row("Format", format) + row("Size", HumanSize(rec.Size))

	att := ""
	if len(p.Attachments) > 0 {
		var items strings.Builder
		var total int64
		for _, a := range p.Attachments {
			total += a.Size
			fmt.Fprintf(&items, `<li><span>%s</span><span class="size">%s</span></li>`, e(a.Name), HumanSize(a.Size))
		}
		att = fmt.Sprintf(`<div class="card"><h2>Attachments &middot; %s total</h2><ul class="att">%s</ul></div>`, HumanSize(total), items.String())
	}
	// Rich bodies render inside a sandboxed iframe: the sandbox attribute
	// (no tokens) blocks script and same-origin access, the CSP meta blocks
	// every network load, so author-controlled HTML can neither run nor
	// phone home from a page that auto-opens. The plain alternative, when
	// there is one, stays inspectable beside it.
	body := fmt.Sprintf(`<div class="body">%s</div>`, e(p.BodyText))
	if p.HasHTML {
		frame := `<!doctype html><html><head><meta charset="utf-8">` +
			`<meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'">` +
			`<style>body{margin:0;font:15px/1.6 ui-sans-serif,-apple-system,system-ui,sans-serif}</style></head><body>` +
			p.BodyHTML + `</body></html>`
		body = fmt.Sprintf(`<iframe class="body-frame" sandbox srcdoc="%s"></iframe>`, e(frame))
		if p.HasText {
			body += fmt.Sprintf(`<details class="alt"><summary>Plain-text part</summary><div class="body">%s</div></details>`, e(p.BodyText))
		}
	}
	return fmt.Sprintf(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Draft — %s</title><style>%s</style></head>
<body><main>
  <div class="tag">Draft &middot; not sent</div>
  <h1>%s</h1>
  <div class="card"><dl>%s</dl></div>
  <div class="card"><h2>Body</h2>%s</div>
  %s
  <h2>Send it</h2>
  <pre class="cmd">send-mail --commit %s</pre>
  <p class="foot">Drafted %s. Single-use: committing sends exactly these bytes (sha256 %s…) and marks the draft sent.</p>
</main></body></html>
`, e(p.Subject), previewCSS, e(p.Subject), meta, body, att, e(rec.ID), e(rec.CreatedAt.Format("2006-01-02 15:04:05")), e(rec.SHA256[:12]))
}
