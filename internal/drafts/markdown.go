package drafts

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	gmhtml "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
)

// Markdown is compiled once into an AST and rendered twice: the HTML part by
// goldmark's stock renderer, the plain part by the walker below. Both parts
// of a multipart/alternative must say the same thing (RFC 2046 §5.1.4), so
// anything that cannot render equivalently in both -- raw HTML, images,
// non-mail link schemes -- is refused at compose time, never dropped from
// one part only.
//
// WithHardWraps: a newline is a line break in both parts. Authors here are
// agents whose markdown dialect is the chat one, where "Best,\nQiushi" is
// two lines; spec CommonMark would silently fold it to one.
var markdown = goldmark.New(goldmark.WithRendererOptions(gmhtml.WithHardWraps()))

func compileMarkdown(source string) (plain, html string, err error) {
	src := []byte(source)
	doc := markdown.Parser().Parse(text.NewReader(src))
	if err := refuseUnrenderable(doc, src); err != nil {
		return "", "", err
	}
	plain = plainDoc(doc, src)
	if strings.TrimSpace(plain) == "" {
		return "", "", errors.New("the markdown renders to nothing visible; a body of only definitions or blank lines would send an empty-looking mail")
	}
	var buf bytes.Buffer
	if err := markdown.Renderer().Render(&buf, src, doc); err != nil {
		return "", "", err
	}
	return plain, buf.String(), nil
}

func refuseUnrenderable(doc ast.Node, src []byte) error {
	return ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch v := n.(type) {
		case *ast.HTMLBlock, *ast.RawHTML:
			return ast.WalkStop, errors.New("raw HTML cannot render the same in both parts of the message; write markdown, or pass a finished HTML body with --format html")
		case *ast.Image:
			return ast.WalkStop, errors.New("images are not supported in markdown bodies: a remote image is a read receipt for the sender's preview, a local path is a broken link for the recipient; use --attach")
		case *ast.Link:
			if !mailableLink(string(v.Destination)) {
				return ast.WalkStop, fmt.Errorf("link destination %q must be http(s) or mailto", v.Destination)
			}
		case *ast.AutoLink:
			if v.AutoLinkType == ast.AutoLinkURL && !mailableLink(string(v.URL(src))) {
				return ast.WalkStop, fmt.Errorf("link destination %q must be http(s) or mailto", v.URL(src))
			}
		}
		return ast.WalkContinue, nil
	})
}

func mailableLink(dest string) bool {
	d := strings.ToLower(dest)
	return strings.HasPrefix(d, "http://") || strings.HasPrefix(d, "https://") || strings.HasPrefix(d, "mailto:")
}

func plainDoc(doc ast.Node, src []byte) string {
	return plainChildren(doc, src, "\n\n")
}

func plainChildren(n ast.Node, src []byte, sep string) string {
	var blocks []string
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		blocks = append(blocks, plainBlock(c, src))
	}
	return strings.Join(blocks, sep)
}

func plainBlock(n ast.Node, src []byte) string {
	switch v := n.(type) {
	case *ast.Paragraph, *ast.TextBlock, *ast.Heading:
		return plainInline(n, src)
	case *ast.Blockquote:
		return prefixLines(plainChildren(v, src, "\n\n"), "> ")
	case *ast.List:
		return plainList(v, src)
	case *ast.FencedCodeBlock, *ast.CodeBlock:
		return rawLines(n, src)
	case *ast.ThematicBreak:
		return "---"
	}
	return plainInline(n, src)
}

func plainList(v *ast.List, src []byte) string {
	sep := "\n"
	if !v.IsTight {
		sep = "\n\n"
	}
	num := v.Start
	if num == 0 {
		num = 1
	}
	var items []string
	for it := v.FirstChild(); it != nil; it = it.NextSibling() {
		marker := "- "
		if v.IsOrdered() {
			marker = fmt.Sprintf("%d. ", num)
			num++
		}
		lines := strings.Split(plainChildren(it, src, sep), "\n")
		for i, l := range lines {
			switch {
			case i == 0:
				lines[i] = marker + l
			case l != "":
				lines[i] = strings.Repeat(" ", len(marker)) + l
			}
		}
		items = append(items, strings.Join(lines, "\n"))
	}
	return strings.Join(items, sep)
}

func plainInline(n ast.Node, src []byte) string {
	var b strings.Builder
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch v := c.(type) {
		case *ast.Text:
			b.Write(v.Segment.Value(src))
			if v.SoftLineBreak() || v.HardLineBreak() {
				b.WriteByte('\n')
			}
		case *ast.String:
			b.Write(v.Value)
		case *ast.Emphasis:
			b.WriteString("*" + plainInline(v, src) + "*")
		case *ast.Link:
			label, url := plainInline(v, src), string(v.Destination)
			if label == "" || label == url || "mailto:"+label == url {
				b.WriteString(strings.TrimPrefix(url, "mailto:"))
			} else {
				fmt.Fprintf(&b, "%s (%s)", label, url)
			}
		case *ast.AutoLink:
			b.Write(v.Label(src))
		default:
			b.WriteString(plainInline(c, src))
		}
	}
	return b.String()
}

func rawLines(n ast.Node, src []byte) string {
	var b strings.Builder
	lines := n.Lines()
	for i := range lines.Len() {
		seg := lines.At(i)
		b.Write(seg.Value(src))
	}
	return strings.TrimRight(b.String(), "\n")
}

func prefixLines(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if l == "" {
			lines[i] = strings.TrimRight(prefix, " ")
		} else {
			lines[i] = prefix + l
		}
	}
	return strings.Join(lines, "\n")
}
