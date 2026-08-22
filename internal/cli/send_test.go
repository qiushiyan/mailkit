package cli_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
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
	id := draftIDOf(t, mustOK(t, h.send("--to", "a@example.com", "--subject", "html", "--body-file", "-", "--html", "--no-open")))
	mustOK(t, h.send("--commit", id))
	hdr, body, _ := parts(t, h.box.Sent[0])
	if !strings.Contains(hdr.Get("Content-Type"), "text/html") {
		t.Errorf("HTML body should be sent as text/html: %q", hdr.Get("Content-Type"))
	}
	if !strings.Contains(body, "<b>there</b>") {
		t.Errorf("body lost: %q", body)
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
