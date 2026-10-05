package cli_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
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

	"github.com/qiushiyan/mailkit/internal/drafts"
	"github.com/qiushiyan/mailkit/internal/mail"
	"github.com/qiushiyan/mailkit/internal/memory"
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

// parts flattens a prepared message: attachment bytes by filename, the last
// bodily text leaf as body. Structural assertions belong to mimeTree.
func parts(t *testing.T, p *mail.Prepared) (hdr netmail.Header, body string, atts map[string][]byte) {
	t.Helper()
	m, err := netmail.ReadMessage(p.Reader())
	if err != nil {
		t.Fatal(err)
	}
	atts = map[string][]byte{}
	var flatten func(n mimePart)
	flatten = func(n mimePart) {
		for _, c := range n.children {
			flatten(c)
		}
		switch {
		case len(n.children) > 0:
		case n.filename != "":
			atts[n.filename] = n.body
		default:
			body = n.text()
		}
	}
	flatten(mimeTree(t, p))
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
	body      []byte
	children  []mimePart
}

// text is the leaf body with wire line endings normalised for asserting.
func (p mimePart) text() string { return strings.ReplaceAll(string(p.body), "\r\n", "\n") }

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
	node.body = b
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

// Markdown is the default format: an agent writing naturally gets the
// text+HTML pair without knowing a flag exists.
func TestSend_MarkdownIsTheDefaultAndCompilesToAlternative(t *testing.T) {
	h := newHarness(t)
	h.stdin = "Hi **team**,\n\n- first\n- second\n\nsee [the plan](https://example.com/plan)\n\nBest,\nQiushi\n"
	id := draftIDOf(t, mustOK(t, h.send("--to", "a@example.com", "--subject", "plan", "--body-file", "-", "--no-open")))
	mustOK(t, h.send("--commit", id))
	root := mimeTree(t, h.box.Sent[0])
	if root.mediaType != "multipart/alternative" {
		t.Fatalf("root is %s, want multipart/alternative", root.mediaType)
	}
	if len(root.children) != 2 || root.children[0].mediaType != "text/plain" || root.children[1].mediaType != "text/html" {
		t.Fatalf("alternative must hold text/plain then text/html (the preferred part last): %+v", root.children)
	}
	plain, html := root.children[0].text(), root.children[1].text()
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
	r := mustFail(t, h.send("--to", "a@example.com", "--subject", "img", "--body", "![shot](https://example.com/x.png)", "--no-open"))
	if !strings.Contains(r.stderr, "image") || !strings.Contains(r.stderr, "--attach") {
		t.Errorf("image refusal must name the construct and the alternative: %s", r.stderr)
	}
	// Pasted content that trips the compiler must be pointed at the escape.
	r = mustFail(t, h.send("--to", "a@example.com", "--subject", "log", "--body", "the error was <div>boom</div>", "--no-open"))
	if !strings.Contains(r.stderr, "--format text") {
		t.Errorf("raw-HTML refusal must name --format text as the way to send verbatim: %s", r.stderr)
	}
	if len(h.box.Sent) != 0 {
		t.Error("refused draft must not exist to send")
	}
}

// --format text is the escape hatch: the bytes go out as text/plain exactly
// as authored, markdown syntax and all.
func TestSend_FormatTextSendsBytesVerbatim(t *testing.T) {
	h := newHarness(t)
	body := "keep **stars**, [brackets](x) and <angles> as they are"
	id := draftIDOf(t, mustOK(t, h.send("--to", "a@example.com", "--subject", "verbatim", "--body", body, "--format", "text", "--no-open")))
	mustOK(t, h.send("--commit", id))
	root := mimeTree(t, h.box.Sent[0])
	if root.mediaType != "text/plain" || len(root.children) != 0 {
		t.Fatalf("text format must send a single text/plain part, got %s with %d children", root.mediaType, len(root.children))
	}
	if got := strings.TrimRight(root.text(), "\n"); got != body {
		t.Errorf("body transformed:\n got %q\nwant %q", got, body)
	}
}

func TestSend_FlagAndModeConflictsAreRefused(t *testing.T) {
	h := newHarness(t)
	id := draftIDOf(t, mustOK(t, h.send("--to", "a@example.com", "--subject", "modes", "--body", "hello there", "--no-open")))
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"unknown format":             {[]string{"--to", "a@example.com", "--subject", "x", "--body", "hi", "--format", "rtf"}, "text, html, or markdown"},
		"commit with compose":        {[]string{"--commit", id, "--subject", "changed"}, "--subject"},
		"commit with account":        {[]string{"--commit", id, "--account", "outlook"}, "--account"},
		"commit with no-open":        {[]string{"--commit", id, "--no-open"}, "--no-open"},
		"commit with list":           {[]string{"--commit", id, "--list"}, "different modes"},
		"list with compose":          {[]string{"--list", "--to", "a@example.com"}, "--to"},
		"two sources for body":       {[]string{"--to", "a@example.com", "--subject", "x", "--body", "a", "--body-file", "b"}, "use one"},
		"two sources, one set empty": {[]string{"--to", "a@example.com", "--subject", "x", "--body", "", "--body-file", "b"}, "use one"},
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

// --- replies: the gate holds for an answer, thread included ---------------

// ticket is a support conversation: the customer's question, and the
// helpdesk's answer that the user replies to.
func ticket() (ask, answer mail.Message) {
	ask = msg("ask", "ticket", day(0), "Me <me@example.com>", "Refund request", para("I was charged for a renewal I did not want; can it be refunded?"))
	ask.MessageID = "ask@mail.example"
	ask.To = []mail.Address{{Email: "help@vendor.example"}}
	answer = msg("liam", "ticket", day(1), "Liam <liam@vendor.example>", "Re: Refund request", para("We can refund the unused part of the quarter, or upgrade you to annual."))
	answer.MessageID = "CALag-refund@mail.example"
	answer.InReplyTo = []mail.MessageID{"ask@mail.example"}
	answer.References = []mail.MessageID{"ask@mail.example"}
	answer.Cc = []mail.Address{{Email: "billing@vendor.example"}, {Email: "me@example.com"}}
	return ask, answer
}

func TestSend_ReplyRoundTripSendsTheReviewedBytesIntoTheReviewedThread(t *testing.T) {
	ask, answer := ticket()
	h := newHarness(t, ask, answer)
	r := mustOK(t, h.send("--reply", "liam", "--body", "The refund works for me, thanks.", "--no-open"))
	for _, want := range []string{"answers Liam <liam@vendor.example>", `"Re: Refund request"`, "to      liam@vendor.example"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("compose output must say what the reply answers and who it goes to; lacks %q:\n%s", want, r.stdout)
		}
	}
	if strings.Contains(r.stdout, "billing@") {
		t.Errorf("a reply goes to the sender alone:\n%s", r.stdout)
	}
	id := draftIDOf(t, r)
	rec, err := h.deps.Drafts.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if o := rec.InReplyTo; o == nil || o.ID != "liam" || o.ConversationID != "ticket" || o.MessageID != "CALag-refund@mail.example" {
		t.Fatalf("the record must keep the original and its thread: %+v", rec.InReplyTo)
	}
	page, _ := os.ReadFile(h.deps.Drafts.PreviewPath(id))
	for _, want := range []string{"In reply to", "Liam &lt;liam@vendor.example&gt;", "Re: Refund request", answer.DateHeader, "&lt;CALag-refund@mail.example&gt;"} {
		if !strings.Contains(string(page), want) {
			t.Errorf("preview must show what the draft answers; lacks %q", want)
		}
	}
	eml, _ := os.ReadFile(filepath.Join(h.deps.Drafts.Dir, id+".eml"))
	if bytes.Contains(eml, []byte("ticket")) {
		t.Error("the provider thread id travels on the send call, never in the message")
	}

	c := mustOK(t, h.send("--commit", id))
	if len(h.box.Sent) != 1 || !bytes.Equal(h.box.Sent[0].Bytes(), eml) {
		t.Fatal("bytes sent differ from the .eml that was previewed")
	}
	if p := h.box.Parents[0]; p == nil || p.ID != "liam" || p.ConversationID != "ticket" {
		t.Fatalf("Send must receive the previewed thread: %+v", p)
	}
	if !strings.Contains(c.stdout, "in its thread ticket") {
		t.Errorf("commit must report the thread the provider used:\n%s", c.stdout)
	}
	hdr, _, _ := parts(t, h.box.Sent[0])
	if hdr.Get("In-Reply-To") != "<CALag-refund@mail.example>" || hdr.Get("References") != "<ask@mail.example> <CALag-refund@mail.example>" {
		t.Errorf("threading headers: In-Reply-To %q References %q", hdr.Get("In-Reply-To"), hdr.Get("References"))
	}
	if r := mustFail(t, h.send("--commit", id)); !strings.Contains(r.stderr, "single-use") {
		t.Errorf("a sent reply is single-use like any draft: %s", r.stderr)
	}
}

func TestSend_ReplyAllAddsEveryoneButMeAndCcAddsMore(t *testing.T) {
	ask, answer := ticket()
	h := newHarness(t, ask, answer)
	id := draftIDOf(t, mustOK(t, h.send("--reply-all", "liam", "--cc", "boss@example.com", "--body", "Refund, please.", "--no-open")))
	mustOK(t, h.send("--commit", id))
	hdr, _, _ := parts(t, h.box.Sent[0])
	if to := hdr.Get("To"); !strings.Contains(to, "liam@vendor.example") || strings.Contains(to, "me@example.com") {
		t.Errorf("To = %q", to)
	}
	cc := hdr.Get("Cc")
	if !strings.Contains(cc, "billing@vendor.example") || !strings.Contains(cc, "boss@example.com") || strings.Contains(cc, "me@example.com") {
		t.Errorf("Cc = %q, want the original's other recipients and --cc, never me", cc)
	}
}

func TestSend_ReplyRefusesWhatWouldReaddressOrRetitleIt(t *testing.T) {
	ask, answer := ticket()
	h := newHarness(t, ask, answer)
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"to with reply":        {[]string{"--reply", "liam", "--to", "x@example.com", "--body", "hi"}, "--cc"},
		"subject with reply":   {[]string{"--reply", "liam", "--subject", "Keys", "--body", "hi"}, "subjects match"},
		"reply and reply-all":  {[]string{"--reply", "liam", "--reply-all", "liam", "--body", "hi"}, "use one"},
		"commit with reply":    {[]string{"--commit", "x", "--reply", "liam"}, "--reply"},
		"unknown original":     {[]string{"--reply", "nope", "--body", "hi"}, "not found"},
		"original without ids": {[]string{"--reply", "noid", "--body", "hi"}, "Message-ID"},
	} {
		t.Run(name, func(t *testing.T) {
			if name == "original without ids" {
				m := msg("noid", "c9", day(2), "a@example.com", "x", para("A message whose sender's client wrote no Message-ID at all."))
				m.MessageID = ""
				h.box.Add(m)
			}
			r := mustFail(t, h.send(tc.args...))
			if !strings.Contains(r.stderr, tc.want) {
				t.Errorf("refusal must mention %q: %s", tc.want, r.stderr)
			}
		})
	}
	if len(h.box.Sent) != 0 {
		t.Error("no refused reply may send")
	}
}

// The record names the thread; the bytes name the original. A record that
// no longer agrees with its bytes is refused before anything leaves.
func TestSend_ReplyWhoseRecordNamesAnotherOriginalIsRefused(t *testing.T) {
	ask, answer := ticket()
	h := newHarness(t, ask, answer)
	id := draftIDOf(t, mustOK(t, h.send("--reply", "liam", "--body", "The refund works for me.", "--no-open")))
	path := filepath.Join(h.deps.Drafts.Dir, id+".json")
	b, _ := os.ReadFile(path)
	os.WriteFile(path, bytes.Replace(b, []byte("CALag-refund@mail.example"), []byte("ask@mail.example"), 1), 0o600)
	if r := mustFail(t, h.send("--commit", id)); !strings.Contains(r.stderr, "re-draft") {
		t.Errorf("a record that disagrees with its bytes must be refused: %s", r.stderr)
	}
	if len(h.box.Sent) != 0 {
		t.Error("a reply whose thread no longer matches its message was sent")
	}
	if rec, _ := h.deps.Drafts.Load(id); rec.State != drafts.Pending {
		t.Errorf("nothing left, so the draft returns to pending, got %s", rec.State)
	}
}

// placing reports a conversation of its choosing, or refuses, the way a
// provider can that ignores or cannot honour the requested thread.
type placing struct {
	*memory.Mailbox
	conv string
	err  error
}

func (p placing) Send(ctx context.Context, m *mail.Prepared, parent *mail.Parent) (mail.Sent, error) {
	if p.err != nil {
		return mail.Sent{}, p.err
	}
	sent, err := p.Mailbox.Send(ctx, m, parent)
	sent.ConversationID = p.conv
	return sent, err
}

func TestSend_CommitSaysWhereTheProviderPutTheReply(t *testing.T) {
	for name, tc := range map[string]struct {
		conv string
		want []string
	}{
		"elsewhere": {"stray-9", []string{"warning", "stray-9", "not the original's ticket", "mail-find thread liam"}},
		"unsaid":    {"", []string{"does not report the thread", "mail-find thread liam"}},
	} {
		t.Run(name, func(t *testing.T) {
			ask, answer := ticket()
			h := newHarness(t, ask, answer)
			h.deps.Open = func(context.Context, string) (mail.Mailbox, error) { return placing{h.box, tc.conv, nil}, nil }
			id := draftIDOf(t, mustOK(t, h.send("--reply", "liam", "--body", "The refund works for me.", "--no-open")))
			r := mustOK(t, h.send("--commit", id))
			for _, want := range tc.want {
				if !strings.Contains(r.stdout, want) {
					t.Errorf("commit output lacks %q:\n%s", want, r.stdout)
				}
			}
		})
	}
}

// A provider that refuses a reply before transmitting (Graph, when it
// cannot address the reply as previewed) leaves the draft pending.
func TestSend_ReplyRefusedBeforeTransmissionReturnsToPending(t *testing.T) {
	ask, answer := ticket()
	h := newHarness(t, ask, answer)
	h.deps.Open = func(context.Context, string) (mail.Mailbox, error) {
		return placing{h.box, "", fmt.Errorf("%w: recipients differ", mail.ErrNotSent)}, nil
	}
	id := draftIDOf(t, mustOK(t, h.send("--reply", "liam", "--body", "The refund works for me.", "--no-open")))
	mustFail(t, h.send("--commit", id))
	if rec, _ := h.deps.Drafts.Load(id); rec.State != drafts.Pending {
		t.Errorf("a refusal before transmission must leave the draft pending, got %s", rec.State)
	}
}
