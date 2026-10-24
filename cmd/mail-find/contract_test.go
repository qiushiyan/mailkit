package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/qiushiyan/mailkit/internal/fixtures"
	"github.com/qiushiyan/mailkit/internal/images"
	"github.com/qiushiyan/mailkit/internal/mail"
	"github.com/qiushiyan/mailkit/internal/pintest"
)

// The output callers read, byte for byte. skills/read-email runs these
// commands and reads their JSON; every command a result prints is what an
// agent runs next. A file under testdata/contract that changes is a change
// those callers see, so it is reviewed as a diff
// (MAILKIT_CONTRACT_UPDATE=1 rewrites them).
func TestContracts(t *testing.T) {
	w := newContractWorld(t)
	thread := fixtures.Thread(t, fixtures.TenancyThread)
	first, last := thread[0].ID, thread[len(thread)-1].ID
	cases := []struct {
		name string
		args []string
	}{
		{"search", []string{"search", `from:shop.example "order 123456789"`}},
		{"search-text", []string{"search", "lunch", "--text"}},
		{"search-text-first", []string{"--text", "search", "lunch"}},
		{"search-attachment-dates", []string{"search", "subject:invoice has:attachment after:2026/07/01 before:2026/08/01"}},
		{"search-phrase", []string{"search", `"order 1623209215"`}},
		{"search-limit", []string{"search", "from:shop.example", "--limit", "1"}},
		{"search-empty", []string{"search", "from:nobody.example"}},
		{"search-empty-text", []string{"search", "from:nobody.example", "--text"}},
		{"search-unknown-operator", []string{"search", "label:inbox"}},
		{"search-native", []string{"search", "label:inbox", "--native"}},
		{"search-bad-limit", []string{"search", "lunch", "--limit", "x"}},
		{"search-no-query", []string{"search"}},
		{"search-unknown-account", []string{"search", "lunch", "--account", "yahoo"}},

		{"resolve-bracketed", []string{"resolve", "<abc.123@mail.example>"}},
		{"resolve-message-url", []string{"resolve", "message://%3Cabc.123@mail.example%3E"}},
		{"resolve-text", []string{"resolve", "abc.123@mail.example", "--text"}},
		{"resolve-not-found", []string{"resolve", "nope@nowhere"}},
		{"resolve-empty", []string{"resolve", "message://"}},

		{"read-reply", []string{"read", last}},
		{"read-reply-text", []string{"read", last, "--text"}},
		{"read-reply-raw", []string{"read", last, "--raw"}},
		{"read-forward", []string{"read", fixtures.AppleForward}},
		{"read-remote-only", []string{"read", fixtures.RemoteOnly}},
		{"read-fetch-remote", []string{"read", "pics", "--fetch-remote"}},
		{"read-fetch-remote-text", []string{"read", "pics", "--fetch-remote", "--text", "--out", w.root + "/pics"}},
		{"read-quote-kept", []string{"read", "unverified"}},
		{"read-quote-kept-text", []string{"read", "unverified", "--text"}},
		{"read-embedded", []string{"read", "kinds"}},
		{"read-embedded-text", []string{"read", "kinds", "--text"}},
		{"read-not-found", []string{"read", "nope"}},

		{"thread", []string{"thread", first}},
		{"thread-text", []string{"thread", first, "--text"}},
		{"thread-raw", []string{"thread", first, "--raw"}},
		{"thread-raw-text", []string{"thread", first, "--raw", "--text"}},

		{"context", []string{"context", "seed"}},
		{"context-min-score", []string{"context", "seed", "--min-score", "3"}},
		{"context-raw", []string{"context", "seed", "--raw"}},
		{"context-text", []string{"context", "seed", "--text"}},
		{"context-ikea", []string{"context", fixtures.IKEASeed, "--window", "3", "--limit", "5"}},

		{"attachments-inline", []string{"attachments", fixtures.InlineCID}},
		{"attachments-inline-text", []string{"attachments", fixtures.InlineCID, "--text"}},
		{"attachments-remote-only", []string{"attachments", fixtures.RemoteOnly}},
		{"attachments-remote-only-text", []string{"attachments", fixtures.RemoteOnly, "--text"}},
		{"attachments-kinds", []string{"attachments", "kinds"}},
		{"attachments-kinds-text", []string{"attachments", "kinds", "--text"}},
		{"attachments-none-text", []string{"attachments", "lunch", "--text"}},

		{"fetch-by-handle", []string{"fetch", fixtures.InlineCID, "--attachment", w.inlineHandle}},
		{"fetch-all", []string{"fetch", "files", "--all"}},
		{"fetch-all-out-text", []string{"fetch", "files", "--all", "--out", w.root + "/out", "--text"}},
		{"fetch-two-names", []string{"fetch", "files", "--attachment", "../../../etc/passwd", "--attachment", "h2"}},
		{"fetch-ambiguous", []string{"fetch", "files", "--attachment", "report.pdf"}},
		{"fetch-unknown-name", []string{"fetch", "files", "--attachment", "nope.pdf"}},
		{"fetch-no-selection", []string{"fetch", "files"}},
		{"fetch-no-bytes", []string{"fetch", "kinds", "--all"}},
		{"fetch-no-attachments", []string{"fetch", "lunch", "--all"}},

		{"auth-status", []string{"auth", "status"}},
		{"auth-status-text", []string{"auth", "status", "--text"}},
		{"auth-login", []string{"auth", "login", "--account", "outlook"}},

		{"bare", nil},
		{"unknown-command", []string{"bogus"}},
		{"read-no-id", []string{"read"}},
		{"read-two-ids", []string{"read", "lunch", "restock"}},
		{"unknown-flag", []string{"read", "lunch", "--bogus"}},
	}
	var names []string
	for _, c := range cases {
		names = append(names, c.name)
		pintest.Check(t, "testdata/contract", c.name, w.norm(w.run(c.args...)))
	}

	// Signed out: every operation names the command that signs back in.
	w.box.AuthErr = mail.Wrap("gmail", "search", "mail-find auth login --account gmail", mail.ErrAuth)
	for _, c := range []struct {
		name string
		args []string
	}{
		{"signed-out-search", []string{"search", "lunch"}},
		{"signed-out-status", []string{"auth", "status"}},
	} {
		names = append(names, c.name)
		pintest.Check(t, "testdata/contract", c.name, w.norm(w.run(c.args...)))
	}
	pintest.Stale(t, "testdata/contract", names)
}

// contractWorld is one mailbox holding the recorded fixtures and the
// scenario messages every case reads, under a temporary state directory,
// with remote images served locally.
type contractWorld struct {
	*harness
	server       *httptest.Server
	inlineHandle string
}

func newContractWorld(t *testing.T) *contractWorld {
	t.Helper()
	msg, day, para := fixtures.Msg, fixtures.Day, fixtures.Para
	w := &contractWorld{}
	w.server = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		sizes := map[string][2]int{"/wordmark.png": {319, 43}, "/avatar.png": {108, 108}, "/pixel.png": {1, 1}, "/photo.png": {640, 480}}
		s, ok := sizes[r.URL.Path]
		if !ok {
			http.NotFound(rw, r)
			return
		}
		rw.Write(fixtures.PNG(s[0], s[1]))
	}))
	t.Cleanup(w.server.Close)

	inline := fixtures.Message(t, fixtures.InlineCID)
	msgs := append(fixtures.Thread(t, fixtures.TenancyThread),
		fixtures.Message(t, fixtures.AppleForward),
		fixtures.Message(t, fixtures.RemoteOnly),
		fixtures.Message(t, fixtures.IKEASeed),
		inline,
		msg("order", "c-order", day(1), "Alice <alice@shop.example>", "Your order 123456789", para("Order 123456789 confirmed and will ship within two working days.")),
		msg("restock", "c-restock", day(3), "alice@shop.example", "Back in stock", para("The lamp you asked about is back in stock at the shop this week.")),
		msg("lunch", "c-lunch", day(5), "bob@other.example", "Lunch", para("Shall we get lunch on Thursday at the usual place near the office?")),
		msg("seed", "c-seed", day(10), "shop@store.example", "Order 555123987 confirmed", para("Your order 555123987 will ship soon from our warehouse.")),
		msg("courier", "c-courier", day(12), "courier@delivery.example", "Your parcel", para("Parcel for order 555123987 is out for delivery this afternoon.")),
		msg("newsletter", "c-news", day(11), "news@store.example", "Autumn newsletter", para("Unrelated newsletter from the same domain, a weak signal only.")),
		msg("pics", "c-pics", day(2), "a@example.com", "Pictures", `<p>A message whose pictures carry the content.</p>`+
			`<img src="`+w.server.URL+`/wordmark.png"><img src="`+w.server.URL+`/avatar.png">`+
			`<img src="`+w.server.URL+`/pixel.png"><img src="`+w.server.URL+`/photo.png">`),
	)
	invoice := msg("invoice", "c-invoice", day(-10), "billing@vendor.example", "Invoice for July", para("Your invoice for July is attached as a PDF for your records."))
	invoice.Parts = []mail.Part{{Name: "invoice-july.pdf", MIME: "application/pdf", Size: 5, Content: mail.StoredPart{Handle: "inv"}}}
	invoice.HasAttachments = true
	ref := msg("ref", "c-ref", day(4), "a@example.com", "hello", para("A message with a known Message-ID for resolving by reference."))
	ref.MessageID = "abc.123@mail.example"
	inner := msg("inner", "c-inner", day(0), "boss@corp.example", "Budget approval", para("The budget for the next quarter is approved as submitted, please proceed."))
	kinds := msg("kinds", "c-kinds", day(6), "a@example.com", "FW: see attached", para("Forwarding the approval as an attachment, and the deck as a link."))
	kinds.Parts = []mail.Part{
		{Name: "Budget approval", MIME: "message/rfc822", Content: mail.EmbeddedPart{Item: &inner}},
		{Name: "deck.pptx", MIME: "application/vnd.ms-powerpoint", Size: 2048, Content: mail.LinkedPart{URL: "https://drive.example/deck"}},
	}
	kinds.HasAttachments = true
	files := msg("files", "c-files", day(7), "a@example.com", "files", para("Three files attached to this message for the sanitisation test."))
	files.Parts = []mail.Part{
		{Name: "../../../etc/passwd", MIME: "text/plain", Size: 7, Content: mail.StoredPart{Handle: "h1"}},
		{Name: "report.pdf", MIME: "application/pdf", Size: 7, Content: mail.StoredPart{Handle: "h2"}},
		{Name: "report.pdf", MIME: "application/pdf", Size: 7, Content: mail.StoredPart{Handle: "h3"}},
	}
	files.HasAttachments = true
	unverified := msg("unverified", "c-unverified", day(8), "a@example.com", "Re: boiler",
		"<p>See below.</p><blockquote><p>The landlord agreed to replace the boiler before the fifteenth and will confirm by Friday.</p></blockquote>")
	msgs = append(msgs, invoice, ref, kinds, files, unverified)

	w.harness = newHarness(t, msgs...)
	for _, h := range []string{"inv", "h1", "h2", "h3"} {
		w.box.Store(mail.Handle(h), []byte(h+"-data"))
	}
	for _, p := range inline.Parts {
		if h, ok := p.Handle(); ok {
			w.box.Store(h, []byte("PNG-BYTES"))
			if p.Inline && w.inlineHandle == "" {
				w.inlineHandle = string(h)
			}
		}
	}

	// Remote images come from the local server only: a URL anywhere else
	// would be a request a test must never make.
	local := w.server.Client().Transport
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if !strings.HasPrefix(r.URL.String(), w.server.URL) {
			return nil, fmt.Errorf("test fetched %s", r.URL)
		}
		return local.RoundTrip(r)
	})}
	w.images = &images.Fetcher{Client: client, Cap: 1 << 20, UserAgent: "test"}
	return w
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func (w *contractWorld) run(args ...string) pintest.Run {
	r := w.find(args...)
	return pintest.Run{Tool: "mail-find", Args: args, Code: r.code, Stdout: r.stdout, Stderr: r.stderr}
}

// norm replaces what differs between runs: the temporary directory and
// the local server's port.
func (w *contractWorld) norm(r pintest.Run) string {
	return strings.NewReplacer(w.root, "$TMP", w.server.URL, "$SERVER").Replace(r.String())
}
