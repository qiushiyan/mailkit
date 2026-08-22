package gmail_test

import (
	"context"
	"encoding/base64"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	gm "google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"

	"github.com/qiushiyan/mailkit/internal/fixtures"
	"github.com/qiushiyan/mailkit/internal/gmail"
	"github.com/qiushiyan/mailkit/internal/mail"
	"github.com/qiushiyan/mailkit/internal/mailtest"
)

// cassette is a fake Gmail API built from the recorded raw JSON: the real
// writer's shape, served over httptest into the real adapter. It also
// records every request so tests can assert what the adapter asked for.
type cassette struct {
	t           *testing.T
	messages    map[string]*gm.Message
	mu          sync.Mutex // the SDK fans out; handlers run concurrently
	requests    []*url.URL
	attachment  string // base64url bytes served for any attachments.get
	lastSendRaw string // the raw field of the last messages.send
}

func newCassette(t *testing.T) (*cassette, *gmail.Mailbox) {
	t.Helper()
	c := &cassette{t: t, messages: map[string]*gm.Message{}}
	var th gm.Thread
	must(t, json.Unmarshal(fixtures.RawJSON(t, "thread-"+fixtures.TenancyThread+".json"), &th))
	for _, m := range th.Messages {
		c.messages[m.Id] = m
	}
	for _, id := range []string{fixtures.AppleForward, fixtures.RemoteOnly, fixtures.InlineCID, fixtures.IKEASeed} {
		var m gm.Message
		must(t, json.Unmarshal(fixtures.RawJSON(t, "msg-"+id+".json"), &m))
		c.messages[id] = &m
	}
	srv := httptest.NewServer(c)
	t.Cleanup(srv.Close)
	box, err := gmail.New(context.Background(), srv.Client(), option.WithEndpoint(srv.URL), option.WithoutAuthentication())
	must(t, err)
	return c, box
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func header(m *gm.Message, name string) string {
	for _, h := range m.Payload.Headers {
		if strings.EqualFold(h.Name, name) {
			return h.Value
		}
	}
	return ""
}

// matches evaluates a Gmail q= string the way the tests need: the subset
// the adapter compiles. It is deliberately literal so a change in the
// compiled query shows up as a failing request, not a silently passing fake.
func (c *cassette) matches(m *gm.Message, q string) bool {
	text := strings.ToLower(decodeAll(m))
	subject := strings.ToLower(header(m, "Subject"))
	from := strings.ToLower(header(m, "From"))
	received := time.UnixMilli(m.InternalDate)
	for _, tok := range tokens(q) {
		switch {
		case strings.HasPrefix(tok, "rfc822msgid:"):
			if !strings.EqualFold(strings.Trim(header(m, "Message-ID"), "<>"), strings.TrimPrefix(tok, "rfc822msgid:")) {
				return false
			}
		case strings.HasPrefix(tok, "from:"):
			if !strings.Contains(from, strings.TrimPrefix(tok, "from:")) {
				return false
			}
		case strings.HasPrefix(tok, "to:"):
			if !strings.Contains(strings.ToLower(header(m, "To")), strings.TrimPrefix(tok, "to:")) {
				return false
			}
		case strings.HasPrefix(tok, "subject:("):
			ok := false
			for term := range strings.SplitSeq(strings.TrimSuffix(strings.TrimPrefix(tok, "subject:("), ")"), " or ") {
				ok = ok || strings.Contains(subject, strings.Trim(term, `"`))
			}
			if !ok {
				return false
			}
		case strings.HasPrefix(tok, "after:"):
			d, _ := time.Parse("2006/01/02", strings.TrimPrefix(tok, "after:"))
			if received.Before(d) {
				return false
			}
		case strings.HasPrefix(tok, "before:"):
			d, _ := time.Parse("2006/01/02", strings.TrimPrefix(tok, "before:"))
			if !received.Before(d) {
				return false
			}
		case tok == "has:attachment":
			if !strings.Contains(decodeStructure(m), "attachmentId") {
				return false
			}
		case strings.HasPrefix(tok, `"`):
			if !strings.Contains(text, strings.Trim(tok, `"`)) && !strings.Contains(subject, strings.Trim(tok, `"`)) {
				return false
			}
		default:
			c.t.Errorf("cassette cannot evaluate query token %q -- extend the fake deliberately", tok)
			return false
		}
	}
	return true
}

func tokens(q string) []string {
	var out []string
	var cur strings.Builder
	depth, quote := 0, false
	for _, r := range strings.ToLower(q) {
		switch {
		case r == '"':
			quote = !quote
			cur.WriteRune(r)
		case r == '(':
			depth++
			cur.WriteRune(r)
		case r == ')':
			depth--
			cur.WriteRune(r)
		case r == ' ' && depth == 0 && !quote:
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

func decodeAll(m *gm.Message) string {
	var b strings.Builder
	var walk func(p *gm.MessagePart)
	walk = func(p *gm.MessagePart) {
		if p == nil {
			return
		}
		if p.Body != nil && p.Body.Data != "" {
			raw, _ := base64.RawURLEncoding.DecodeString(strings.TrimRight(p.Body.Data, "="))
			b.Write(raw)
		}
		for _, c := range p.Parts {
			walk(c)
		}
	}
	walk(m.Payload)
	return b.String()
}

func decodeStructure(m *gm.Message) string {
	b, _ := json.Marshal(m.Payload)
	return string(b)
}

func (c *cassette) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	c.requests = append(c.requests, r.URL)
	c.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/gmail/v1/users/me")
	writeJSON := func(v any) {
		w.Header().Set("Content-Type", "application/json")
		b, _ := json.Marshal(v)
		w.Write(b)
	}
	switch {
	case path == "/profile":
		writeJSON(gm.Profile{EmailAddress: "me@gmail.test"})
	case path == "/messages":
		q := r.URL.Query().Get("q")
		// Gmail lists newest first and pages by maxResults; the token is
		// an offset here.
		var all []*gm.Message
		for _, m := range c.messages {
			if c.matches(m, q) {
				all = append(all, m)
			}
		}
		sort.Slice(all, func(i, j int) bool { return all[i].InternalDate > all[j].InternalDate })
		start, _ := strconv.Atoi(r.URL.Query().Get("pageToken"))
		size, _ := strconv.Atoi(r.URL.Query().Get("maxResults"))
		if size <= 0 {
			size = 100
		}
		var resp gm.ListMessagesResponse
		for i := start; i < len(all) && i < start+size; i++ {
			resp.Messages = append(resp.Messages, &gm.Message{Id: all[i].Id, ThreadId: all[i].ThreadId})
		}
		if start+size < len(all) {
			resp.NextPageToken = strconv.Itoa(start + size)
		}
		writeJSON(resp)
	case path == "/messages/send":
		var body gm.Message
		_ = json.UnmarshalRead(r.Body, &body)
		c.mu.Lock()
		c.lastSendRaw = body.Raw
		c.mu.Unlock()
		writeJSON(gm.Message{Id: "sent-1"})
	case strings.HasPrefix(path, "/messages/") && strings.Contains(path, "/attachments/"):
		msgID := strings.TrimPrefix(path, "/messages/")
		msgID = msgID[:strings.Index(msgID, "/")]
		if _, ok := c.messages[msgID]; c.attachment == "" || !ok {
			http.Error(w, `{"error":{"code":404,"message":"not in cassette"}}`, 404)
			return
		}
		writeJSON(gm.MessagePartBody{Data: c.attachment, Size: 12})
	case strings.HasPrefix(path, "/messages/"):
		m, ok := c.messages[strings.TrimPrefix(path, "/messages/")]
		if !ok {
			http.Error(w, `{"error":{"code":404,"message":"Requested entity was not found."}}`, 404)
			return
		}
		writeJSON(m)
	case strings.HasPrefix(path, "/threads/"):
		id := strings.TrimPrefix(path, "/threads/")
		var th gm.Thread
		th.Id = id
		for _, m := range c.messages {
			if m.ThreadId == id {
				th.Messages = append(th.Messages, m)
			}
		}
		if len(th.Messages) == 0 {
			http.Error(w, `{"error":{"code":404}}`, 404)
			return
		}
		writeJSON(th)
	default:
		http.Error(w, "unexpected "+r.URL.String(), 500)
	}
}

// --- the golden translation: recorded JSON -> Message ----------------------

func TestTranslate_TenancyThreadGolden(t *testing.T) {
	thread := fixtures.Thread(t, fixtures.TenancyThread)
	if len(thread) != 6 {
		t.Fatalf("thread has %d messages, want 6", len(thread))
	}
	first := thread[0]
	if first.From.Email != "FHashim@quintainliving.com" || first.From.Name == "" {
		t.Errorf("From = %+v", first.From)
	}
	if first.Subject != "Quintain Living - 819 Solar" {
		t.Errorf("Subject = %q", first.Subject)
	}
	if first.ConversationID != fixtures.TenancyThread {
		t.Errorf("ConversationID = %q", first.ConversationID)
	}
	if first.MessageID == "" || strings.ContainsAny(string(first.MessageID), "<>") {
		t.Errorf("MessageID = %q", first.MessageID)
	}
	if first.Received.Year() != 2026 || first.Received.Location() == nil {
		t.Errorf("Received = %v", first.Received)
	}
	if !strings.Contains(first.Body.HTML, "<") {
		t.Errorf("Body.HTML should be HTML")
	}
	for i := 1; i < len(thread); i++ {
		if thread[i].Received.Before(thread[i-1].Received) {
			t.Errorf("thread not ascending at %d", i)
		}
	}
}

func TestTranslate_InlineCIDPartKeysOnAttachmentID(t *testing.T) {
	m := fixtures.Message(t, fixtures.InlineCID)
	var inline []mail.Part
	for _, p := range m.Parts {
		if p.Inline && p.ContentID != "" {
			inline = append(inline, p)
		}
	}
	if len(inline) == 0 {
		t.Fatalf("no inline CID part translated; parts: %+v", m.Parts)
	}
	for _, p := range inline {
		if _, ok := p.Handle(); !ok {
			t.Errorf("inline part %q has no handle", p.Name)
		}
		if p.Name == "" || !strings.Contains(p.Name, ".") {
			t.Errorf("nameless inline part should get a name with an extension, got %q", p.Name)
		}
	}
	// Inline images are parts, and fetchable, but not attachments: the
	// port's HasAttachments means something beyond the rendered body.
	if m.HasAttachments {
		t.Error("a message whose only parts are inline images has no attachments")
	}
}

func TestTranslate_RemoteOnlyHasNoParts(t *testing.T) {
	m := fixtures.Message(t, fixtures.RemoteOnly)
	if len(m.Parts) != 0 {
		t.Errorf("remote-only message has %d parts: %v", len(m.Parts), m.Parts)
	}
	if !strings.Contains(m.Body.HTML, "<img") {
		t.Error("fixture should reference images in HTML")
	}
}

// --- the adapter over the cassette -----------------------------------------

func TestGmail_Contract(t *testing.T) {
	c, box := newCassette(t)
	// Handle bytes are not in the cassette (attachments.get is a separate
	// call never recorded), so the contract's Open case is served by a
	// synthetic response.
	m := fixtures.Message(t, fixtures.InlineCID)
	var part mail.Part
	for _, p := range m.Parts {
		if _, ok := p.Handle(); ok {
			part = p
			break
		}
	}
	mailtest.Run(t, withAttachments(box, c), mailtest.Scenario{
		Address:          "me@gmail.test",
		MessageID:        fixtures.InlineCID,
		RFC822ID:         m.MessageID,
		Conversation:     fixtures.TenancyThread,
		ConversationSize: 6,
		UniquePhrase:     "Inventory report complete",
		FromDomain:       "propertyreporting.co.uk",
		StoredPartName:   part.Name,
		StoredPrefix:     []byte("\x89PNG"),
		Received:         m.Received,
	})
}

// withAttachments adds a synthetic attachments.get to the cassette.
func withAttachments(box *gmail.Mailbox, c *cassette) mail.Mailbox {
	c.attachment = base64.RawURLEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nfake"))
	return box
}

func TestGmail_SearchCompilesEveryCriteriaField(t *testing.T) {
	c, box := newCassette(t)
	after := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	before := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	_, err := box.Search(t.Context(), mail.Criteria{
		Phrases: []string{"819 Solar"}, SubjectTerms: []string{"quintain", "solar"}, From: "quintainliving.com",
		To: "gmail.com", HasAttachment: false, After: after, Before: before,
	}, 5)
	must(t, err)
	var q string
	for _, u := range c.requests {
		if u.Path == "/gmail/v1/users/me/messages" {
			q = u.Query().Get("q")
		}
	}
	for _, want := range []string{`"819 Solar"`, "subject:(quintain OR solar)", "from:quintainliving.com", "to:gmail.com", "after:2026/08/01", "before:"} {
		if !strings.Contains(q, want) {
			t.Errorf("compiled query lacks %q: %s", want, q)
		}
	}
}

func TestGmail_SearchFansOutOneMetadataCallPerHit(t *testing.T) {
	c, box := newCassette(t)
	hits, err := box.Search(t.Context(), mail.Criteria{Phrases: []string{"Quintain"}}, 25)
	must(t, err)
	gets := 0
	for _, u := range c.requests {
		if strings.HasPrefix(u.Path, "/gmail/v1/users/me/messages/") && u.Query().Get("format") == "metadata" {
			gets++
		}
	}
	if gets != len(hits) || len(hits) < 6 {
		t.Errorf("%d hits, %d metadata calls", len(hits), gets)
	}
}

func TestGmail_SendEncodesPreparedBytesAsRaw(t *testing.T) {
	c, box := newCassette(t)
	p, err := mail.NewPrepared(strings.NewReader("From: a@b.c\r\nTo: d@e.f\r\nSubject: hi\r\n\r\nbody\r\n"))
	must(t, err)
	id, err := box.Send(t.Context(), p)
	must(t, err)
	if id != "sent-1" {
		t.Errorf("id = %q", id)
	}
	if c.lastSendRaw == "" {
		t.Fatal("no send body captured")
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(c.lastSendRaw, "="))
	must(t, err)
	if string(raw) != string(p.Bytes()) {
		t.Errorf("raw decodes to %q", raw)
	}
}

// The rule: a part is an attachment if it has an attachmentId, not if it
// has a filename. The recorded inline-CID message happens to carry
// filenames, so this case blanks them on the real JSON -- the writer's
// shape with one field removed -- to reach the rule's negative space.
// Deleting the attachmentId keying in message() must fail this.
func TestTranslate_PartWithoutFilenameIsStillAPart(t *testing.T) {
	var m gm.Message
	must(t, json.Unmarshal(fixtures.RawJSON(t, "msg-"+fixtures.InlineCID+".json"), &m))
	named := len(gmail.Translate(&m).Parts)
	var blank func(p *gm.MessagePart)
	blank = func(p *gm.MessagePart) {
		if p == nil {
			return
		}
		p.Filename = ""
		for _, c := range p.Parts {
			blank(c)
		}
	}
	blank(m.Payload)
	parts := gmail.Translate(&m).Parts
	if len(parts) != named || named == 0 {
		t.Fatalf("blanking filenames changed the part count: %d -> %d", named, len(parts))
	}
	for _, p := range parts {
		if p.Name == "" || p.ContentID == "" || !strings.HasPrefix(p.Name, p.ContentID) || !p.Inline {
			t.Errorf("nameless part should be named from its Content-ID and inline: %+v", p)
		}
		if _, ok := p.Handle(); !ok {
			t.Errorf("nameless part lost its handle: %+v", p)
		}
	}
}

// Rule: Search is exact -- limit hits or exhaustion, never a truncated
// coarse page. Gmail's after:/before: are day-granular, so the newest
// same-day message can pass the provider and fail the precise bound; the
// adapter must keep paging instead of returning fewer than limit.
func TestGmail_SearchPagesPastCoarseHitsTheBoundRejects(t *testing.T) {
	_, box := newCassette(t)
	// 2026-08-10 holds 09:46, 09:50 and 12:28 messages in the thread.
	hits, err := box.Search(t.Context(), mail.Criteria{
		After: time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC), Before: time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC),
	}, 1)
	must(t, err)
	if len(hits) != 1 || hits[0].ID != "19feb14eebb753de" {
		t.Fatalf("want the 09:50 message alone, got %v", ids(hits))
	}
	// Gmail's from: also matches display names; the port's From is an
	// address or domain. "quintain" is in the sender's name and domain,
	// so the provider returns hits the port must not.
	hits, err = box.Search(t.Context(), mail.Criteria{From: "quintain"}, 10)
	must(t, err)
	if len(hits) != 0 {
		t.Errorf("a bare word is neither an address nor a domain: %v", ids(hits))
	}
}

func TestGmail_SearchThatCannotFillRefusesToPretend(t *testing.T) {
	c, box := newCassette(t)
	base := c.messages["19feb14eebb753de"]
	for i := range 1200 {
		clone := *base
		clone.Id = fmt.Sprintf("clone-%04d", i)
		c.messages[clone.Id] = &clone
	}
	// Every clone is on 2026-08-10 09:50; a precise window after 10:00 on
	// that day admits none of them locally while the day operator admits all.
	_, err := box.Search(t.Context(), mail.Criteria{
		After: time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC), Before: time.Date(2026, 8, 10, 11, 0, 0, 0, time.UTC),
	}, 5)
	if err == nil || !strings.Contains(err.Error(), "narrow") {
		t.Fatalf("scanning past the cap must be an error that says to narrow the query, got %v", err)
	}
}

func ids(hits []mail.Envelope) []string {
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.ID)
	}
	return out
}
