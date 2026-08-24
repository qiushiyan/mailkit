package cli_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	netmail "net/mail"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/qiushiyan/mailkit/internal/mail"
)

// The send gate's promise: the bytes reviewed are the bytes sent, once.

var draftIDRe = regexp.MustCompile(`send-mail --commit (\S+)`)

func draftIDOf(t *testing.T, r result) string {
	t.Helper()
	m := draftIDRe.FindStringSubmatch(r.stdout)
	if m == nil {
		t.Fatalf("compose output names no commit command:\n%s", r.stdout)
	}
	return m[1]
}

func writeFile(t *testing.T, name string, content []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, content, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// parts decodes a prepared message into its attachment bytes by filename.
func parts(t *testing.T, p *mail.Prepared) (hdr netmail.Header, body string, atts map[string][]byte) {
	t.Helper()
	m, err := netmail.ReadMessage(p.Reader())
	if err != nil {
		t.Fatal(err)
	}
	atts = map[string][]byte{}
	mt, params, _ := mime.ParseMediaType(m.Header.Get("Content-Type"))
	if !strings.HasPrefix(mt, "multipart/") {
		b, _ := io.ReadAll(m.Body)
		return m.Header, string(b), atts
	}
	mr := multipart.NewReader(m.Body, params["boundary"])
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(part)
		if part.Header.Get("Content-Transfer-Encoding") == "base64" {
			b, _ = io.ReadAll(base64.NewDecoder(base64.StdEncoding, bytes.NewReader(b)))
		}
		if name := part.FileName(); name != "" {
			atts[name] = b
		} else {
			body = string(b)
		}
	}
	return m.Header, body, atts
}

func TestSend_CommitSendsExactlyTheReviewedBytes(t *testing.T) {
	h := newHarness(t)
	pdf := writeFile(t, "invoice.pdf", bytes.Repeat([]byte("PDF"), 5000))
	r := mustOK(t, h.send("--to", "a@example.com,b@example.com", "--cc", "c@example.com", "--bcc", "d@example.com",
		"--subject", "Invoice — August", "--body", "Please find the invoice attached.\n\nThanks", "--attach", pdf, "--no-open"))
	id := draftIDOf(t, r)
	if len(h.box.Sent) != 0 {
		t.Fatal("compose must not send")
	}
	eml, err := os.ReadFile(filepath.Join(h.deps.Drafts.Dir, id+".eml"))
	if err != nil {
		t.Fatalf("draft has no .eml on disk: %v", err)
	}

	mustOK(t, h.send("--commit", id))
	if len(h.box.Sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(h.box.Sent))
	}
	sent := h.box.Sent[0]
	if !bytes.Equal(sent.Bytes(), eml) {
		t.Fatal("bytes sent differ from the .eml that was previewed")
	}
	hdr, body, atts := parts(t, sent)
	for name, want := range map[string]string{"To": "a@example.com", "Cc": "c@example.com", "Bcc": "d@example.com"} {
		if !strings.Contains(hdr.Get(name), want) {
			t.Errorf("%s header lost %s: %q", name, want, hdr.Get(name))
		}
	}
	if !strings.Contains(hdr.Get("To"), "b@example.com") {
		t.Errorf("second To recipient lost")
	}
	if got := sha256.Sum256(atts["invoice.pdf"]); got != sha256.Sum256(bytes.Repeat([]byte("PDF"), 5000)) {
		t.Errorf("attachment bytes differ after the round trip (%d bytes)", len(atts["invoice.pdf"]))
	}
	if !strings.Contains(body, "invoice attached") {
		t.Errorf("body lost: %q", body)
	}
	if hdr.Get("Message-ID") == "" {
		t.Errorf("a deterministic Message-ID is the recovery handle; none set")
	}
	// The preview was rendered from the same bytes.
	page, _ := os.ReadFile(h.deps.Drafts.PreviewPath(id))
	for _, want := range []string{"invoice.pdf", "d@example.com", "Invoice — August"} {
		if !strings.Contains(string(page), want) {
			t.Errorf("preview does not show %q", want)
		}
	}
}

func TestSend_DraftIsSingleUse(t *testing.T) {
	h := newHarness(t)
	id := draftIDOf(t, mustOK(t, h.send("--to", "a@example.com", "--subject", "once", "--body", "hello there", "--no-open")))
	mustOK(t, h.send("--commit", id))
	r := mustFail(t, h.send("--commit", id))
	if !strings.Contains(r.stderr, "single-use") {
		t.Errorf("second commit must be refused as single-use: %s", r.stderr)
	}
	if len(h.box.Sent) != 1 {
		t.Errorf("sent %d times", len(h.box.Sent))
	}
}

func TestSend_ConcurrentCommitsSendOnce(t *testing.T) {
	h := newHarness(t)
	id := draftIDOf(t, mustOK(t, h.send("--to", "a@example.com", "--subject", "race", "--body", "hello there", "--no-open")))
	var wg sync.WaitGroup
	ok := 0
	var mu sync.Mutex
	for range 8 {
		wg.Go(func() {
			if r := h.send("--commit", id); r.code == 0 {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if ok != 1 || len(h.box.Sent) != 1 {
		t.Fatalf("%d commits succeeded, %d sends happened; want exactly 1", ok, len(h.box.Sent))
	}
}

func TestSend_EditedDraftIsRefused(t *testing.T) {
	h := newHarness(t)
	id := draftIDOf(t, mustOK(t, h.send("--to", "a@example.com", "--subject", "edit", "--body", "original body", "--no-open")))
	eml := filepath.Join(h.deps.Drafts.Dir, id+".eml")
	b, _ := os.ReadFile(eml)
	os.WriteFile(eml, bytes.Replace(b, []byte("original"), []byte("tampered"), 1), 0o600)
	r := mustFail(t, h.send("--commit", id))
	if !strings.Contains(r.stderr, "re-draft") {
		t.Errorf("a changed message must be refused and tell the user to re-draft: %s", r.stderr)
	}
	if len(h.box.Sent) != 0 {
		t.Errorf("tampered draft was sent")
	}
}

func TestSend_OverLimitIsRefusedOnPreparedBytes(t *testing.T) {
	h := newHarness(t)
	h.box.SendLimit = 10 << 10
	big := writeFile(t, "big.bin", bytes.Repeat([]byte{1}, 9<<10)) // under the limit raw, over it once base64 expands
	r := mustOK(t, h.send("--to", "a@example.com", "--subject", "big", "--body", "see attached", "--attach", big, "--no-open"))
	if !strings.Contains(r.stdout, "over the") {
		t.Errorf("compose should warn that the prepared message exceeds the limit:\n%s", r.stdout)
	}
	id := draftIDOf(t, r)
	rr := mustFail(t, h.send("--commit", id))
	if !strings.Contains(rr.stderr, "limit") {
		t.Errorf("commit must refuse on the limit: %s", rr.stderr)
	}
	if len(h.box.Sent) != 0 {
		t.Errorf("oversize draft was sent")
	}
}

func TestSend_BodyFromStdinAndHTML(t *testing.T) {
	h := newHarness(t)
	h.stdin = "<p>Hello <b>there</b></p>"
	id := draftIDOf(t, mustOK(t, h.send("--to", "a@example.com", "--subject", "html", "--body-file", "-", "--format", "html", "--no-open")))
	mustOK(t, h.send("--commit", id))
	hdr, body, _ := parts(t, h.box.Sent[0])
	if !strings.Contains(hdr.Get("Content-Type"), "text/html") {
		t.Errorf("HTML body should be sent as text/html: %q", hdr.Get("Content-Type"))
	}
	if !strings.Contains(body, "<b>there</b>") {
		t.Errorf("body lost: %q", body)
	}
}

// mimePart is one node of the sent message's MIME tree. The markdown tests
// assert on the tree with parentage intact: a flat walk would still pass if
// the alternative parts sat as multipart/mixed siblings.
type mimePart struct {
	mediaType string
	filename  string
	body      string
	children  []mimePart
}

func mimeTree(t *testing.T, p *mail.Prepared) mimePart {
	t.Helper()
	m, err := netmail.ReadMessage(p.Reader())
	if err != nil {
		t.Fatal(err)
	}
	return mimeNode(t, m.Header.Get("Content-Type"), m.Header.Get("Content-Transfer-Encoding"), m.Body)
}

func mimeNode(t *testing.T, ctype, cte string, r io.Reader) mimePart {
	t.Helper()
	mt, params, err := mime.ParseMediaType(ctype)
	if err != nil {
		mt = "text/plain"
	}
	node := mimePart{mediaType: mt}
	if strings.HasPrefix(mt, "multipart/") {
		mr := multipart.NewReader(r, params["boundary"])
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				return node
			}
			if err != nil {
				t.Fatal(err)
			}
			// NextPart auto-decodes quoted-printable; base64 it leaves.
			child := mimeNode(t, part.Header.Get("Content-Type"), part.Header.Get("Content-Transfer-Encoding"), part)
			child.filename = part.FileName()
			node.children = append(node.children, child)
		}
	}
	b, err := io.ReadAll(decodeCTE(cte, r))
	if err != nil {
		t.Fatal(err)
	}
	node.body = strings.ReplaceAll(string(b), "\r\n", "\n")
	return node
}

func decodeCTE(cte string, r io.Reader) io.Reader {
	switch strings.ToLower(strings.TrimSpace(cte)) {
	case "base64":
		return base64.NewDecoder(base64.StdEncoding, r)
	case "quoted-printable":
		return quotedprintable.NewReader(r)
	}
	return r
}

func TestSend_MarkdownCompilesToAlternative(t *testing.T) {
	h := newHarness(t)
	h.stdin = "Hi **team**,\n\n- first\n- second\n\nsee [the plan](https://example.com/plan)\n\nBest,\nQiushi\n"
	id := draftIDOf(t, mustOK(t, h.send("--to", "a@example.com", "--subject", "plan", "--body-file", "-", "--format", "markdown", "--no-open")))
	mustOK(t, h.send("--commit", id))
	root := mimeTree(t, h.box.Sent[0])
	if root.mediaType != "multipart/alternative" {
		t.Fatalf("root is %s, want multipart/alternative", root.mediaType)
	}
	if len(root.children) != 2 || root.children[0].mediaType != "text/plain" || root.children[1].mediaType != "text/html" {
		t.Fatalf("alternative must hold text/plain then text/html (the preferred part last): %+v", root.children)
	}
	plain, html := root.children[0].body, root.children[1].body
	for _, want := range []string{"*team*", "- first\n- second", "the plan (https://example.com/plan)", "Best,\nQiushi"} {
		if !strings.Contains(plain, want) {
			t.Errorf("plain part lacks %q:\n%s", want, plain)
		}
	}
	for _, want := range []string{"<strong>team</strong>", "<li>first</li>", `<a href="https://example.com/plan">the plan</a>`, "Best,<br>"} {
		if !strings.Contains(html, want) {
			t.Errorf("html part lacks %q:\n%s", want, html)
		}
	}
	page, _ := os.ReadFile(h.deps.Drafts.PreviewPath(id))
	for _, want := range []string{"<iframe", "sandbox", "Plain-text part", "Text + HTML"} {
		if !strings.Contains(string(page), want) {
			t.Errorf("preview lacks %q", want)
		}
	}
}

func TestSend_MarkdownWithAttachmentNestsAlternativeUnderMixed(t *testing.T) {
	h := newHarness(t)
	pdf := writeFile(t, "invoice.pdf", []byte("%PDF fake"))
	id := draftIDOf(t, mustOK(t, h.send("--to", "a@example.com", "--subject", "mixed", "--body", "see **attached**", "--format", "markdown", "--attach", pdf, "--no-open")))
	mustOK(t, h.send("--commit", id))
	root := mimeTree(t, h.box.Sent[0])
	if root.mediaType != "multipart/mixed" || len(root.children) != 2 {
		t.Fatalf("root is %s with %d children, want multipart/mixed with 2", root.mediaType, len(root.children))
	}
	alt := root.children[0]
	if alt.mediaType != "multipart/alternative" || len(alt.children) != 2 {
		t.Fatalf("first child is %s with %d children, want the alternative pair", alt.mediaType, len(alt.children))
	}
	if root.children[1].filename != "invoice.pdf" {
		t.Errorf("attachment lost: %+v", root.children[1])
	}
}

func TestSend_MarkdownRefusalNamesTheConstruct(t *testing.T) {
	h := newHarness(t)
	r := mustFail(t, h.send("--to", "a@example.com", "--subject", "img", "--body", "![shot](https://example.com/x.png)", "--format", "markdown", "--no-open"))
	if !strings.Contains(r.stderr, "image") || !strings.Contains(r.stderr, "--attach") {
		t.Errorf("image refusal must name the construct and the alternative: %s", r.stderr)
	}
	if len(h.box.Sent) != 0 {
		t.Error("refused draft must not exist to send")
	}
}

func TestSend_FlagAndModeConflictsAreRefused(t *testing.T) {
	h := newHarness(t)
	id := draftIDOf(t, mustOK(t, h.send("--to", "a@example.com", "--subject", "modes", "--body", "hello there", "--no-open")))
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"unknown format":       {[]string{"--to", "a@example.com", "--subject", "x", "--body", "hi", "--format", "rtf"}, "text, html, or markdown"},
		"commit with compose":  {[]string{"--commit", id, "--subject", "changed"}, "--subject"},
		"commit with list":     {[]string{"--commit", id, "--list"}, "different modes"},
		"list with compose":    {[]string{"--list", "--to", "a@example.com"}, "--to"},
		"two sources for body": {[]string{"--to", "a@example.com", "--subject", "x", "--body", "a", "--body-file", "b"}, "use one"},
	} {
		t.Run(name, func(t *testing.T) {
			r := mustFail(t, h.send(tc.args...))
			if !strings.Contains(r.stderr, tc.want) {
				t.Errorf("refusal must mention %q: %s", tc.want, r.stderr)
			}
		})
	}
	if len(h.box.Sent) != 0 {
		t.Error("no conflicted invocation may send")
	}
}

func TestSend_ListShowsState(t *testing.T) {
	h := newHarness(t)
	id := draftIDOf(t, mustOK(t, h.send("--to", "a@example.com", "--subject", "listed", "--body", "hello there", "--no-open")))
	r := mustOK(t, h.send("--list"))
	if !strings.Contains(r.stdout, id) || !strings.Contains(r.stdout, "pending") {
		t.Errorf("list should show the pending draft:\n%s", r.stdout)
	}
	mustOK(t, h.send("--commit", id))
	if r := mustOK(t, h.send("--list")); !strings.Contains(r.stdout, "sent") {
		t.Errorf("list should show it sent:\n%s", r.stdout)
	}
}
