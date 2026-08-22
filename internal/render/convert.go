// Package render turns a provider's message into a form a model will not
// misread: HTML to text shaped for the fold rules, quote folding that is
// verified rather than assumed, tidying of conversion artefacts, and a
// chronological transcript.
//
// The converter is the calibration point. Every folding and tidying rule
// was measured against this exact text shape, so both providers go through
// it; outsourcing the conversion to a provider's CLI was the single biggest
// thing the first implementation got away with.
package render

import (
	"html"
	"regexp"
	"strings"

	xhtml "golang.org/x/net/html"

	"github.com/qiushiyan/mailkit/internal/mail"
)

// Rendered is one pass over the HTML: the text, and the images it only
// references by URL. One parse, because both come from the same tree and a
// second tokenisation of the same bytes is a hop that adds nothing.
type Rendered struct {
	Text string
	// RemoteImages are http(s) <img src> values in document order, deduped.
	RemoteImages []string
}

var (
	skipContainer = map[string]bool{"script": true, "style": true, "head": true, "title": true}
	skipVoid      = map[string]bool{"meta": true, "link": true, "base": true}
	// Split from skipContainer on purpose: a void element counted as a
	// container would push the depth counter and never pop it, muting the
	// rest of the document -- which is exactly what happened once.
	blockTags = map[string]bool{"p": true, "div": true, "table": true, "ul": true, "ol": true,
		"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
		"blockquote": true, "section": true, "article": true, "header": true, "footer": true, "pre": true}
	// tr is absent: its end tag already emits the row break.

	wsRun          = regexp.MustCompile(`[ \t\r\n]+`)
	blankRun       = regexp.MustCompile(`\n{3,}`)
	emptyQuoteLine = regexp.MustCompile(`(?m)^[>\s]*>[>\s]*$`)
	trailingWS     = regexp.MustCompile(`(?m)[ \t]+$`)
	schemeRe       = regexp.MustCompile(`^(https?://|mailto:)`)
)

type converter struct {
	out        strings.Builder
	skipDepth  int
	quoteDepth int
	inLink     bool
	href       string
	linkText   strings.Builder
	cellOpen   bool
	images     []string
	seen       map[string]bool
}

// emit prefixes quoted lines with "> " per level at emit time. Done here
// rather than as a post-pass: once the document is one string, which lines
// were quoted is exactly the information that has been lost.
func (c *converter) emit(s string) {
	if c.inLink {
		c.linkText.WriteString(s)
		return
	}
	if c.quoteDepth > 0 {
		s = strings.ReplaceAll(s, "\n", "\n"+strings.Repeat("> ", c.quoteDepth))
	}
	c.out.WriteString(s)
}

func (c *converter) newline(n int) { c.emit(strings.Repeat("\n", n)) }

func attr(t xhtml.Token, key string) string {
	for _, a := range t.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// sameTarget is true when the anchor text adds nothing to the href.
func sameTarget(text, href string) bool {
	a := strings.ToLower(strings.TrimRight(strings.TrimSpace(text), "/"))
	b := strings.ToLower(strings.TrimRight(strings.TrimSpace(href), "/"))
	b = schemeRe.ReplaceAllString(b, "")
	return a == b || (strings.HasSuffix(b, a) && len(a) > 6)
}

func (c *converter) start(t xhtml.Token) {
	tag := t.Data
	if skipVoid[tag] {
		return
	}
	if skipContainer[tag] {
		c.skipDepth++
		return
	}
	if c.skipDepth > 0 {
		return
	}
	switch tag {
	case "br":
		c.newline(1)
	case "img":
		// A picture that converts to a bare URL is indistinguishable from a
		// link, which is how a screenshot carrying a message's only real
		// content got read as absence. So it gets a marker.
		alt := strings.TrimSpace(attr(t, "alt"))
		src := strings.TrimSpace(attr(t, "src"))
		label := "[image]"
		if alt != "" {
			label = "[image: " + alt + "]"
		}
		if src != "" {
			c.emit(label + " ( " + src + " )")
			if lower := strings.ToLower(src); strings.HasPrefix(lower, "http") && !c.seen[src] {
				c.seen[src] = true
				c.images = append(c.images, src)
			}
		} else {
			c.emit(label)
		}
	case "a":
		c.inLink = true
		c.href = strings.TrimSpace(attr(t, "href"))
		c.linkText.Reset()
	case "li":
		c.newline(1)
		c.emit("- ")
	case "td", "th":
		if c.cellOpen {
			c.emit(" | ")
		}
		c.cellOpen = true
	case "blockquote":
		c.quoteDepth++
		c.newline(2)
	default:
		if blockTags[tag] {
			c.newline(1)
		}
	}
}

func (c *converter) end(tag string) {
	if skipContainer[tag] {
		c.skipDepth = max(0, c.skipDepth-1)
		return
	}
	if skipVoid[tag] || c.skipDepth > 0 {
		return
	}
	switch tag {
	case "a":
		text := strings.TrimSpace(c.linkText.String())
		href := c.href
		c.inLink = false
		c.linkText.Reset()
		switch {
		case text == "":
			c.emit(href)
		case href == "" || sameTarget(text, href):
			c.emit(text)
		default:
			c.emit(text + " ( " + href + " )")
		}
	case "td", "th":
	case "tr":
		c.cellOpen = false
		c.newline(1)
	case "blockquote":
		c.quoteDepth = max(0, c.quoteDepth-1)
		c.newline(2)
	default:
		if blockTags[tag] {
			c.newline(1)
		}
	}
}

func (c *converter) text(data string) {
	if c.skipDepth > 0 {
		return
	}
	// Collapse whitespace runs but keep the fact that there was some.
	s := wsRun.ReplaceAllString(data, " ")
	if strings.TrimSpace(s) != "" || s == " " {
		c.emit(s)
	}
}

// Convert renders a body to text, the HTML when there is one. Quoted
// history comes back prefixed "> ".
// The tokenizer, not the tree parser, is the primitive: it is the streaming
// analogue of the parser the rules were calibrated on, and a spike showed
// the output byte-identical on ten real bodies.
func Convert(b mail.Body) Rendered {
	if b.HTML == "" {
		return Rendered{Text: strings.TrimSpace(b.Text)}
	}
	c := &converter{seen: map[string]bool{}}
	z := xhtml.NewTokenizer(strings.NewReader(b.HTML))
loop:
	for {
		switch z.Next() {
		case xhtml.ErrorToken:
			break loop
		case xhtml.StartTagToken, xhtml.SelfClosingTagToken:
			c.start(z.Token())
		case xhtml.EndTagToken:
			c.end(z.Token().Data)
		case xhtml.TextToken:
			c.text(z.Token().Data)
		}
	}
	out := c.out.String()
	out = html.UnescapeString(out)
	out = strings.ReplaceAll(out, " ", " ")
	out = trailingWS.ReplaceAllString(out, "")
	out = emptyQuoteLine.ReplaceAllString(out, "")
	out = blankRun.ReplaceAllString(out, "\n\n")
	return Rendered{Text: strings.TrimSpace(out), RemoteImages: c.images}
}

// Text is the body as text: the HTML converted, or the plain part when
// there was no HTML.
func Text(b mail.Body) string { return Convert(b).Text }
