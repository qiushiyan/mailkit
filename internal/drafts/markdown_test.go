package drafts

import (
	"strings"
	"testing"
)

// The compiler's promise: both parts render the same message, and anything
// that cannot is refused loudly at compose time.

func TestMarkdown_BothPartsSayTheSameThing(t *testing.T) {
	cases := []struct {
		name, src   string
		plain, html []string
	}{
		{
			name:  "emphasis keeps the plain-mail convention",
			src:   "This is **important** and *subtle*.",
			plain: []string{"This is *important* and *subtle*."},
			html:  []string{"<strong>important</strong>", "<em>subtle</em>"},
		},
		{
			name:  "a newline is a line break in both parts",
			src:   "Best,\nQiushi",
			plain: []string{"Best,\nQiushi"},
			html:  []string{"Best,<br>\nQiushi"},
		},
		{
			name:  "bullets",
			src:   "- first\n- second",
			plain: []string{"- first\n- second"},
			html:  []string{"<ul>", "<li>first</li>", "<li>second</li>"},
		},
		{
			name:  "ordered list keeps its numbering",
			src:   "3. third\n4. fourth",
			plain: []string{"3. third\n4. fourth"},
			html:  []string{`<ol start="3">`, "<li>third</li>"},
		},
		{
			name:  "nested list keeps its nesting",
			src:   "- outer\n  - inner",
			plain: []string{"- outer\n  - inner"},
			html:  []string{"<ul>\n<li>outer\n<ul>\n<li>inner</li>"},
		},
		{
			name:  "link carries label and destination in both",
			src:   "see [the plan](https://example.com/plan)",
			plain: []string{"see the plan (https://example.com/plan)"},
			html:  []string{`<a href="https://example.com/plan">the plan</a>`},
		},
		{
			name:  "autolink is just the address",
			src:   "write to <https://example.com>",
			plain: []string{"write to https://example.com"},
			html:  []string{`<a href="https://example.com">https://example.com</a>`},
		},
		{
			name:  "heading and paragraph separate as blocks",
			src:   "# Update\n\nAll good.",
			plain: []string{"Update\n\nAll good."},
			html:  []string{"<h1>Update</h1>", "<p>All good.</p>"},
		},
		{
			name:  "blockquote keeps its prefix in plain",
			src:   "> quoted words",
			plain: []string{"> quoted words"},
			html:  []string{"<blockquote>"},
		},
		{
			name:  "backslash escapes resolve in both parts",
			src:   `2 \* 3 = 6`,
			plain: []string{"2 * 3 = 6"},
			html:  []string{"2 * 3 = 6"},
		},
		{
			name:  "entities resolve in both parts",
			src:   "AT&amp;T and &copy; 2026",
			plain: []string{"AT&T and © 2026"},
			html:  []string{"AT&amp;T and © 2026"},
		},
		{
			name:  "an escaped ampersand never becomes an entity",
			src:   `\&copy; literal`,
			plain: []string{"&copy; literal"},
			html:  []string{"&amp;copy; literal"},
		},
		{
			name:  "code span newlines normalise to spaces in both parts",
			src:   "run `alpha\nbeta` now",
			plain: []string{"run alpha beta now"},
			html:  []string{"<code>alpha beta</code>"},
		},
		{
			name:  "link title reaches both parts",
			src:   `[docs](https://example.com "draft title")`,
			plain: []string{`docs (https://example.com "draft title")`},
			html:  []string{`title="draft title"`},
		},
		{
			name:  "a label that is the destination stays as written",
			src:   "[mailto:a@example.com](mailto:a@example.com)",
			plain: []string{"mailto:a@example.com"},
			html:  []string{`<a href="mailto:a@example.com">mailto:a@example.com</a>`},
		},
		{
			name:  "code block survives verbatim",
			src:   "```\nmake check\n```",
			plain: []string{"make check"},
			html:  []string{"<pre><code>make check"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			plain, html, err := compileMarkdown(c.src)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range c.plain {
				if !strings.Contains(plain, want) {
					t.Errorf("plain part lacks %q:\n%s", want, plain)
				}
			}
			for _, want := range c.html {
				if !strings.Contains(html, want) {
					t.Errorf("html part lacks %q:\n%s", want, html)
				}
			}
		})
	}
}

func TestMarkdown_RefusesWhatCannotRenderEquivalently(t *testing.T) {
	cases := []struct{ name, src, wantErr string }{
		{"inline raw HTML", "hello <b>there</b>", "raw HTML"},
		{"block raw HTML", "<div>\nblock\n</div>", "raw HTML"},
		{"image", "![logo](https://example.com/logo.png)", "image"},
		{"local image", "![shot](./shot.png)", "image"},
		{"javascript link", "[click](javascript:alert(1))", "must be http(s) or mailto"},
		{"data link", "[blob](data:text/html,x)", "must be http(s) or mailto"},
		{"renders to nothing", "[foo]: https://example.com\n", "nothing visible"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := compileMarkdown(c.src)
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("want refusal mentioning %q, got %v", c.wantErr, err)
			}
		})
	}
}

func TestMarkdown_MailtoLinksPass(t *testing.T) {
	plain, html, err := compileMarkdown("mail [me](mailto:me@example.com)")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plain, "me (mailto:me@example.com)") {
		t.Errorf("plain: %s", plain)
	}
	if !strings.Contains(html, `href="mailto:me@example.com"`) {
		t.Errorf("html: %s", html)
	}
}
