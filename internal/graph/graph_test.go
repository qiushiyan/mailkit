package graph_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime"
	"net/http"
	"net/http/httptest"
	netmail "net/mail"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/qiushiyan/mailkit/internal/graph"
	"github.com/qiushiyan/mailkit/internal/mail"
	"github.com/qiushiyan/mailkit/internal/mailtest"
)

// cassette is a fake Graph built from testdata/graph -- DOC-DERIVED shapes
// until a recording exists (see testdata/graph/README.txt). It evaluates
// only the $filter/$search forms the adapter emits, loudly.
type cassette struct {
	t        *testing.T
	messages []map[string]any
	item     map[string]any
	requests []*http.Request
	sendBody string
	sends    int
	// pageSize, when set, overrides $top so paging is exercised on a small fixture.
	pageSize int
}

func load(t *testing.T, name string, v any) {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	b, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "testdata", "graph", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatal(err)
	}
}

func newCassette(t *testing.T) (*cassette, *graph.Mailbox) {
	c := &cassette{t: t}
	load(t, "messages.json", &c.messages)
	load(t, "item-attachment.json", &c.item)
	srv := httptest.NewServer(c)
	t.Cleanup(srv.Close)
	box := graph.New(srv.Client())
	box.BaseURL = srv.URL
	return c, box
}

func atts(m map[string]any) []map[string]any {
	raw, _ := m["attachments"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, a := range raw {
		if am, ok := a.(map[string]any); ok {
			out = append(out, am)
		}
	}
	return out
}

func str(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}

func (c *cassette) received(m map[string]any) time.Time {
	t, _ := time.Parse(time.RFC3339, str(m, "receivedDateTime"))
	return t
}

// filterMatch evaluates the $filter forms the adapter emits.
func (c *cassette) filterMatch(m map[string]any, filter string) bool {
	for clause := range strings.SplitSeq(filter, " and ") {
		clause = strings.TrimSpace(clause)
		switch {
		case strings.HasPrefix(clause, "internetMessageId eq '"):
			want := strings.TrimSuffix(strings.TrimPrefix(clause, "internetMessageId eq '"), "'")
			if str(m, "internetMessageId") != want {
				return false
			}
		case strings.HasPrefix(clause, "conversationId eq '"):
			want := strings.TrimSuffix(strings.TrimPrefix(clause, "conversationId eq '"), "'")
			if str(m, "conversationId") != want {
				return false
			}
		case strings.HasPrefix(clause, "receivedDateTime ge "):
			t, err := time.Parse(time.RFC3339, strings.TrimPrefix(clause, "receivedDateTime ge "))
			if err != nil || c.received(m).Before(t) {
				return false
			}
		case strings.HasPrefix(clause, "receivedDateTime lt "):
			t, err := time.Parse(time.RFC3339, strings.TrimPrefix(clause, "receivedDateTime lt "))
			if err != nil || !c.received(m).Before(t) {
				return false
			}
		case clause == "hasAttachments eq true":
			if m["hasAttachments"] != true {
				return false
			}
		case strings.HasPrefix(clause, "from/emailAddress/address eq '"):
			want := strings.TrimSuffix(strings.TrimPrefix(clause, "from/emailAddress/address eq '"), "'")
			from, _ := m["from"].(map[string]any)
			ea, _ := from["emailAddress"].(map[string]any)
			if !strings.EqualFold(str(ea, "address"), want) {
				return false
			}
		default:
			c.t.Errorf("cassette cannot evaluate $filter clause %q", clause)
			return false
		}
	}
	return true
}

// searchMatch evaluates the $search forms: quoted phrases joined by AND,
// and (subject:"a" OR subject:"b").
func (c *cassette) searchMatch(m map[string]any, search string) bool {
	body, _ := m["body"].(map[string]any)
	text := strings.ToLower(str(body, "content") + " " + str(m, "subject"))
	subject := strings.ToLower(str(m, "subject"))
	for term := range strings.SplitSeq(search, " AND ") {
		term = strings.TrimSpace(term)
		switch {
		case strings.HasPrefix(term, "(subject:"):
			ok := false
			for alt := range strings.SplitSeq(strings.Trim(term, "()"), " OR ") {
				// KQL matches word stems: subject:"moves" finds "move".
				term := strings.TrimSuffix(strings.ToLower(strings.Trim(strings.TrimPrefix(alt, "subject:"), `"`)), "s")
				ok = ok || strings.Contains(subject, term)
			}
			if !ok {
				return false
			}
		case strings.HasPrefix(term, `"`):
			if !strings.Contains(text, strings.ToLower(strings.Trim(term, `"`))) {
				return false
			}
		default:
			c.t.Errorf("cassette cannot evaluate $search term %q", term)
			return false
		}
	}
	return true
}

func (c *cassette) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.requests = append(c.requests, r)
	q := r.URL.Query()
	writeJSON := func(v any) {
		w.Header().Set("Content-Type", "application/json")
		b, _ := json.Marshal(v)
		w.Write(b)
	}
	path := r.URL.Path
	switch {
	case path == "/me":
		writeJSON(map[string]any{"mail": "qiushi@planlab.example", "userPrincipalName": "qiushi@planlab.example"})
	case path == "/me/messages":
		var out []map[string]any
		for _, m := range c.messages {
			if f := q.Get("$filter"); f != "" && !c.filterMatch(m, f) {
				continue
			}
			if s := q.Get("$search"); s != "" && !c.searchMatch(m, s) {
				continue
			}
			out = append(out, m)
		}
		if out == nil {
			out = []map[string]any{}
		}
		size := c.pageSize
		if size == 0 {
			size, _ = strconv.Atoi(q.Get("$top"))
		}
		if size <= 0 {
			size = 10
		}
		skip, _ := strconv.Atoi(q.Get("$skiptoken"))
		page := map[string]any{"value": []map[string]any{}}
		if skip < len(out) {
			page["value"] = out[skip:min(skip+size, len(out))]
		}
		if skip+size < len(out) {
			next := *r.URL
			nq := next.Query()
			nq.Set("$skiptoken", strconv.Itoa(skip+size))
			next.RawQuery = nq.Encode()
			page["@odata.nextLink"] = "http://" + r.Host + next.String()
		}
		writeJSON(page)
	case path == "/me/sendMail":
		b, _ := io.ReadAll(r.Body)
		c.sendBody = string(b)
		if r.Header.Get("Content-Type") != "text/plain" {
			http.Error(w, `{"error":{"code":"BadRequest","message":"MIME send needs text/plain"}}`, 400)
			return
		}
		c.file(b, nil, "")
		w.WriteHeader(http.StatusAccepted)
	case r.Method == http.MethodPost && (strings.HasSuffix(path, "/reply") || strings.HasSuffix(path, "/replyAll")):
		rest, action, _ := strings.Cut(strings.TrimPrefix(path, "/me/messages/"), "/")
		id, _ := url.PathUnescape(rest)
		b, _ := io.ReadAll(r.Body)
		c.sendBody = string(b)
		if r.Header.Get("Content-Type") != "text/plain" {
			http.Error(w, `{"error":{"code":"BadRequest","message":"MIME reply needs text/plain"}}`, 400)
			return
		}
		for _, m := range c.messages {
			if str(m, "id") == id {
				c.file(b, m, action)
				w.WriteHeader(http.StatusAccepted)
				return
			}
		}
		http.Error(w, `{"error":{"code":"ErrorItemNotFound","message":"The specified object was not found in the store."}}`, 404)
	case strings.HasSuffix(path, "/$value"):
		known := false
		for _, m := range c.messages {
			known = known || strings.Contains(path, url.PathEscape(str(m, "id")))
		}
		if !known {
			http.Error(w, `{"error":{"code":"ErrorItemNotFound","message":"not found"}}`, 404)
			return
		}
		w.Write([]byte("%PDF-1.7 fake"))
	case strings.Contains(path, "/attachments/"):
		attID, _ := url.PathUnescape(path[strings.LastIndex(path, "/")+1:])
		if attID == str(c.item, "id") {
			writeJSON(c.item)
			return
		}
		for _, m := range c.messages {
			for _, a := range atts(m) {
				if str(a, "id") == attID {
					writeJSON(a)
					return
				}
			}
		}
		http.Error(w, `{"error":{"code":"ErrorItemNotFound","message":"not found"}}`, 404)
	case strings.HasPrefix(path, "/me/messages/"):
		id, _ := url.PathUnescape(strings.TrimPrefix(path, "/me/messages/"))
		for _, m := range c.messages {
			if str(m, "id") == id {
				// The adapter's $expand selects base properties only; Graph
				// returns nothing else, so a referenceAttachment's sourceUrl
				// is absent here and needs its own GET.
				projected := map[string]any{}
				maps.Copy(projected, m)
				var stripped []map[string]any
				for _, a := range atts(m) {
					b := map[string]any{}
					for k, v := range a {
						if k != "sourceUrl" {
							b[k] = v
						}
					}
					stripped = append(stripped, b)
				}
				projected["attachments"] = stripped
				writeJSON(projected)
				return
			}
		}
		http.Error(w, `{"error":{"code":"ErrorItemNotFound","message":"The specified object was not found in the store."}}`, 404)
	default:
		http.Error(w, "unexpected "+r.URL.String(), 500)
	}
}

const me = "qiushi@planlab.example"

// file models where Graph puts a message it sends. sendMail starts a
// conversation of its own -- Exchange groups by Thread-Index, never by
// In-Reply-To/References -- addressed as the MIME says. A reply action puts
// the reply in the original's conversation and addresses it as the
// reference says, ignoring the MIME To and Cc: /reply to the original's
// sender, /replyAll to the sender and everyone on it but the account. That
// is the narrowest reading of the reference, so an adapter that reaches the
// previewed recipients here reaches them under every reading. Single-part
// bodies only; anything else is a loud failure.
func (c *cassette) file(b64 []byte, orig map[string]any, action string) {
	raw, err := base64.StdEncoding.DecodeString(string(b64))
	if err != nil {
		c.t.Errorf("MIME body is not base64: %v", err)
		return
	}
	m, err := netmail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		c.t.Errorf("MIME body is not RFC 5322: %v", err)
		return
	}
	if strings.HasPrefix(strings.ToLower(m.Header.Get("Content-Type")), "multipart/") {
		c.t.Errorf("cassette files single-part sends only -- extend it deliberately")
	}
	body, _ := io.ReadAll(m.Body)
	subject, _ := new(mime.WordDecoder).DecodeHeader(m.Header.Get("Subject"))
	recips := func(as []mail.Address) []any {
		out := []any{}
		for _, a := range as {
			if !strings.EqualFold(a.Email, me) {
				out = append(out, map[string]any{"emailAddress": map[string]any{"name": a.Name, "address": a.Email}})
			}
		}
		return out
	}
	listed := func(v any) []any {
		l, _ := v.([]any)
		return l
	}
	c.sends++
	id := fmt.Sprintf("sent-%d", c.sends)
	conv, to, cc := "AAQk-"+id, recips(mail.ParseAddressList(m.Header.Get("To"))), recips(mail.ParseAddressList(m.Header.Get("Cc")))
	if orig != nil {
		conv, to, cc = str(orig, "conversationId"), []any{orig["from"]}, []any{}
		if action == "replyAll" {
			to = append(to, listed(orig["toRecipients"])...)
			cc = listed(orig["ccRecipients"])
		}
		mine := func(r any) bool {
			ea, _ := r.(map[string]any)["emailAddress"].(map[string]any)
			return strings.EqualFold(str(ea, "address"), me)
		}
		to, cc = slices.DeleteFunc(slices.Clone(to), mine), slices.DeleteFunc(slices.Clone(cc), mine)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	c.messages = append(c.messages, map[string]any{
		"@odata.type": "#microsoft.graph.message", "id": id, "internetMessageId": m.Header.Get("Message-ID"),
		"conversationId": conv, "receivedDateTime": now, "sentDateTime": now, "subject": subject,
		"bodyPreview": "", "hasAttachments": false, "attachments": []any{},
		"from":         map[string]any{"emailAddress": map[string]any{"name": "Qiushi", "address": me}},
		"toRecipients": to, "ccRecipients": cc,
		"body": map[string]any{"contentType": "text", "content": string(body)},
	})
}

func TestGraph_Contract(t *testing.T) {
	_, box := newCassette(t)
	mailtest.Run(t, box, mailtest.Scenario{
		Address: "qiushi@planlab.example", MessageID: "AAMkAGI1-first", RFC822ID: "first.0001@contoso.example",
		Conversation: "AAQkAGI1-conv", ConversationSize: 2, UniquePhrase: "parking passes", FromDomain: "contoso.example",
		StoredPartName: "floorplan.pdf", StoredPrefix: []byte("%PDF"),
		Received: time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC),
	})
}

func TestGraph_ThreeAttachmentKindsAndOneLevelOfEmbedding(t *testing.T) {
	_, box := newCassette(t)
	m, err := box.Fetch(t.Context(), "AAMkAGI1-first")
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for _, p := range m.Parts {
		kinds[p.Name] = p.Kind()
	}
	want := map[string]string{"floorplan.pdf": "stored", "image001.png": "stored", "Move checklist.xlsx": "cloud_link", "FW: Lease terms": "embedded_message"}
	for name, kind := range want {
		if kinds[name] != kind {
			t.Errorf("%s: kind %q, want %q", name, kinds[name], kind)
		}
	}
	for _, p := range m.Parts {
		switch c := p.Content.(type) {
		case mail.LinkedPart:
			if !strings.Contains(c.URL, "sharepoint") {
				t.Errorf("cloud link lost its URL: %+v", c)
			}
		case mail.EmbeddedPart:
			if c.Item == nil || c.Item.Subject != "Lease terms" || c.Item.Body.HTML == "" {
				t.Errorf("embedded message not expanded: %+v", c)
			}
		}
		if p.Name == "image001.png" && (!p.Inline || p.ContentID == "") {
			t.Errorf("inline part flags lost: %+v", p)
		}
	}
	if m.ProviderFolded == nil {
		t.Error("uniqueBody should be passed up as ProviderFolded")
	}
}

func TestGraph_UnknownAttachmentTypeIsLoud(t *testing.T) {
	c, box := newCassette(t)
	atts := c.messages[0]["attachments"].([]any)
	atts[0].(map[string]any)["@odata.type"] = "#microsoft.graph.somethingNew"
	if _, err := box.Fetch(t.Context(), "AAMkAGI1-first"); err == nil || !strings.Contains(err.Error(), "somethingNew") {
		t.Errorf("unknown @odata.type must fail loudly, got %v", err)
	}
}

func TestGraph_SearchPlansFilterOrSearchNeverBoth(t *testing.T) {
	c, box := newCassette(t)
	after := time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)
	// Phrases force $search; the date window must then be narrowed locally.
	hits, err := box.Search(t.Context(), mail.Criteria{Phrases: []string{"office move"}, After: after, Before: after.AddDate(0, 0, 2)}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Errorf("phrase+window should find both thread messages, got %d", len(hits))
	}
	for _, r := range c.requests {
		if r.URL.Path != "/me/messages" {
			continue
		}
		q := r.URL.Query()
		if q.Get("$search") != "" && q.Get("$filter") != "" {
			t.Fatalf("$search and $filter in one request: %s", r.URL.RawQuery)
		}
	}
	// The window excludes the third message even though the phrase would not.
	hits, _ = box.Search(t.Context(), mail.Criteria{Phrases: []string{"invoice"}, Before: after}, 10)
	if len(hits) != 0 {
		t.Errorf("local narrowing failed: %d hits", len(hits))
	}
	// Dates alone go to $filter with an orderby.
	c.requests = nil
	if _, err := box.Search(t.Context(), mail.Criteria{After: after, HasAttachment: true}, 10); err != nil {
		t.Fatal(err)
	}
	last := c.requests[len(c.requests)-1].URL.Query()
	if last.Get("$search") != "" || !strings.Contains(last.Get("$filter"), "hasAttachments eq true") || !strings.Contains(last.Get("$filter"), "receivedDateTime ge") {
		t.Errorf("expected a $filter with both predicates: %s", last.Encode())
	}
}

func TestGraph_SendPostsPreparedBytesAsMIME(t *testing.T) {
	c, box := newCassette(t)
	p, err := mail.NewPrepared(strings.NewReader("From: a@b.c\r\nTo: d@e.f\r\nMessage-ID: <x@mailkit>\r\nSubject: hi\r\n\r\nbody\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	sent, err := box.Send(t.Context(), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sent != (mail.Sent{}) {
		t.Errorf("MIME sendMail returns 202 and nothing else; inventing an id or a conversation misleads recovery, got %+v", sent)
	}
	raw, err := base64.StdEncoding.DecodeString(c.sendBody)
	if err != nil || string(raw) != string(p.Bytes()) {
		t.Errorf("MIME body does not decode to the prepared bytes: %v %q", err, raw)
	}
}

func TestGraph_TextBodyComesBackAsText(t *testing.T) {
	_, box := newCassette(t)
	m, err := box.Fetch(t.Context(), "AAMkAGI1-third")
	if err != nil {
		t.Fatal(err)
	}
	if m.Body.HTML != "" || !strings.Contains(m.Body.Text, "invoice 4471") {
		t.Errorf("text body: %+v", m.Body)
	}
	if m.ProviderFolded != nil {
		t.Error("null uniqueBody must be nil")
	}
}

// Rule: Search is exact -- limit hits or exhaustion, never a silently
// truncated page walk.
func TestGraph_SearchPagesUntilLimitOrExhaustion(t *testing.T) {
	c, box := newCassette(t)
	c.pageSize = 1
	before := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	// "office move" is in two messages; the newer (10:30) fails the bound
	// and sits on the first page alone.
	hits, err := box.Search(t.Context(), mail.Criteria{Phrases: []string{"office move"}, Before: before}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ID != "AAMkAGI1-first" {
		t.Fatalf("want the 09:00 message alone, got %+v", hits)
	}
}

func TestGraph_SearchThatCannotFillRefusesToPretend(t *testing.T) {
	c, box := newCassette(t)
	c.pageSize = 50
	second := c.messages[1]
	for i := range 1200 {
		clone := map[string]any{}
		maps.Copy(clone, second)
		clone["id"] = fmt.Sprintf("clone-%04d", i)
		c.messages = append(c.messages, clone)
	}
	// Every clone is at 10:30; a local bound before 10:00 rejects all of them.
	_, err := box.Search(t.Context(), mail.Criteria{Phrases: []string{"printers"}, Before: time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)}, 5)
	if err == nil || !strings.Contains(err.Error(), "narrow") {
		t.Fatalf("scanning past the cap must be an error that says to narrow the query, got %v", err)
	}
}

func TestGraph_ConversationFollowsEveryPage(t *testing.T) {
	c, box := newCassette(t)
	c.pageSize = 1
	msgs, err := box.Conversation(t.Context(), "AAQkAGI1-conv")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("conversation has 2 messages across 2 pages, got %d", len(msgs))
	}
	// Parts are completed the same way Fetch completes them.
	for _, p := range msgs[0].Parts {
		switch c := p.Content.(type) {
		case mail.LinkedPart:
			if c.URL == "" {
				t.Errorf("conversation lost the cloud link's URL: %+v", p)
			}
		case mail.EmbeddedPart:
			if c.Item == nil && !c.Truncated {
				t.Errorf("conversation left an embedded message neither expanded nor truncated: %+v", p)
			}
		}
	}
}

func TestGraph_SubjectTermsAreNarrowedLocallyNotLeftToKQL(t *testing.T) {
	_, box := newCassette(t)
	hits, err := box.Search(t.Context(), mail.Criteria{SubjectTerms: []string{"moves"}}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("KQL's stemmed subject match is not the port's subject match: %+v", hits)
	}
}

func TestGraph_NestedEmbeddedMessageIsExpandedOrMarkedTruncated(t *testing.T) {
	c, box := newCassette(t)
	item := c.item["item"].(map[string]any)
	item["attachments"] = []any{map[string]any{
		"@odata.type": "#microsoft.graph.itemAttachment", "id": "nested-1", "name": "RE: Lease terms", "contentType": "message/rfc822", "size": 2048,
	}}
	m, err := box.Fetch(t.Context(), "AAMkAGI1-first")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range m.Parts {
		e, ok := p.Content.(mail.EmbeddedPart)
		if !ok {
			continue
		}
		for _, inner := range e.Item.Parts {
			if ie, ok := inner.Content.(mail.EmbeddedPart); ok && ie.Item == nil && !ie.Truncated {
				t.Fatalf("a nested embedded message that was not fetched must say so: %+v", inner)
			}
		}
	}
}

// --- replies: Graph's reply actions address a reply themselves ------------

// reply builds the prepared bytes a reply draft would carry, addressed as
// given, and the parent it is sent under.
func reply(t *testing.T, box *graph.Mailbox, id string, to, cc, bcc string) (*mail.Prepared, *mail.Parent) {
	t.Helper()
	orig, err := box.Fetch(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	r, err := mail.NewReply(orig.Envelope, []string{me}, false)
	if err != nil {
		t.Fatal(err)
	}
	hdr := "From: " + me + "\r\nTo: " + to + "\r\n"
	if cc != "" {
		hdr += "Cc: " + cc + "\r\n"
	}
	if bcc != "" {
		hdr += "Bcc: " + bcc + "\r\n"
	}
	p, err := mail.NewPrepared(strings.NewReader(hdr + "Subject: " + r.Subject + "\r\nIn-Reply-To: " + r.InReplyTo +
		"\r\nReferences: " + r.References + "\r\nMessage-ID: <r@mailkit>\r\n\r\nYes, the printers move too.\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	parent := r.Parent()
	return p, &parent
}

func posts(c *cassette) []string {
	var out []string
	for _, r := range c.requests {
		if r.Method == http.MethodPost {
			out = append(out, r.URL.Path)
		}
	}
	return out
}

func recipients(m map[string]any, key string) []string {
	var out []string
	for _, r := range m[key].([]any) {
		ea := r.(map[string]any)["emailAddress"].(map[string]any)
		out = append(out, str(ea, "address"))
	}
	slices.Sort(out)
	return out
}

// sendMail would start a conversation of its own, so a reply goes through
// the original's reply action -- the prepared bytes, as MIME -- and reaches
// exactly the recipients it was previewed with.
func TestGraph_ReplyGoesThroughTheReplyAction(t *testing.T) {
	c, box := newCassette(t)
	p, parent := reply(t, box, "AAMkAGI1-first", "dana@contoso.example", "", "")
	if _, err := box.Send(t.Context(), p, parent); err != nil {
		t.Fatal(err)
	}
	if got := posts(c); len(got) != 1 || got[0] != "/me/messages/AAMkAGI1-first/reply" {
		t.Fatalf("POSTs = %v, want the original's reply action alone", got)
	}
	raw, err := base64.StdEncoding.DecodeString(c.sendBody)
	if err != nil || !bytes.Equal(raw, p.Bytes()) {
		t.Errorf("reply body does not decode to the prepared bytes: %v", err)
	}
	filed := c.messages[len(c.messages)-1]
	if str(filed, "conversationId") != "AAQkAGI1-conv" || !slices.Equal(recipients(filed, "toRecipients"), []string{"dana@contoso.example"}) {
		t.Errorf("reply filed in %s to %v", str(filed, "conversationId"), recipients(filed, "toRecipients"))
	}
}

func TestGraph_ReplyAllUsesReplyAll(t *testing.T) {
	c, box := newCassette(t)
	first := c.messages[0]
	first["toRecipients"] = append(first["toRecipients"].([]any), map[string]any{"emailAddress": map[string]any{"name": "Lee", "address": "lee@contoso.example"}})
	first["ccRecipients"] = []any{map[string]any{"emailAddress": map[string]any{"name": "Facilities", "address": "facilities@contoso.example"}}}
	p, parent := reply(t, box, "AAMkAGI1-first", "dana@contoso.example, lee@contoso.example", "facilities@contoso.example", "")
	if _, err := box.Send(t.Context(), p, parent); err != nil {
		t.Fatal(err)
	}
	if got := posts(c); len(got) != 1 || got[0] != "/me/messages/AAMkAGI1-first/replyAll" {
		t.Fatalf("POSTs = %v, want replyAll", got)
	}
	filed := c.messages[len(c.messages)-1]
	got := append(recipients(filed, "toRecipients"), recipients(filed, "ccRecipients")...)
	slices.Sort(got)
	if want := []string{"dana@contoso.example", "facilities@contoso.example", "lee@contoso.example"}; !slices.Equal(got, want) {
		t.Errorf("reply-all reached %v, want %v", got, want)
	}
}

// Whatever Graph would address differently from the preview, under any
// reading of its reference, is refused before a byte leaves -- and refused
// as not sent, so the draft returns to pending rather than unknown.
func TestGraph_ReplyGraphCannotAddressAsPreviewedIsRefusedBeforeSending(t *testing.T) {
	for name, tc := range map[string]struct {
		id, to, cc, bcc string
		setup           func(c *cassette)
	}{
		"an extra cc":           {id: "AAMkAGI1-first", to: "dana@contoso.example", cc: "boss@contoso.example"},
		"a bcc":                 {id: "AAMkAGI1-first", to: "dana@contoso.example", bcc: "me@planlab.example"},
		"my own message":        {id: "AAMkAGI1-second", to: "dana@contoso.example"},
		"a Reply-To, addressed": {id: "AAMkAGI1-first", to: "moves@contoso.example", setup: withReplyTo},
		"a Reply-To, ignored":   {id: "AAMkAGI1-first", to: "dana@contoso.example", setup: withReplyTo},
	} {
		t.Run(name, func(t *testing.T) {
			c, box := newCassette(t)
			if tc.setup != nil {
				tc.setup(c)
			}
			p, parent := reply(t, box, tc.id, tc.to, tc.cc, tc.bcc)
			_, err := box.Send(t.Context(), p, parent)
			if !errors.Is(err, mail.ErrNotSent) {
				t.Fatalf("want a refusal marked not sent, got %v", err)
			}
			if got := posts(c); len(got) != 0 {
				t.Errorf("a refused reply still POSTed %v", got)
			}
		})
	}
}

// A note to self is answered to self under every reading, so it goes.
func TestGraph_ReplyToANoteToSelfIsSent(t *testing.T) {
	c, box := newCassette(t)
	note := maps.Clone(c.messages[2])
	note["id"], note["conversationId"] = "AAMkAGI1-note", "AAQkAGI1-note"
	note["from"] = map[string]any{"emailAddress": map[string]any{"name": "Qiushi", "address": me}}
	note["toRecipients"] = []any{note["from"]}
	c.messages = append(c.messages, note)
	p, parent := reply(t, box, "AAMkAGI1-note", me, "", "")
	if _, err := box.Send(t.Context(), p, parent); err != nil {
		t.Fatal(err)
	}
	if got := posts(c); len(got) != 1 || got[0] != "/me/messages/AAMkAGI1-note/reply" {
		t.Errorf("POSTs = %v", got)
	}
}

// One reading of the reference sends a reply to the Reply-To, the other to
// the sender; with both on the original, no recipient set is safe.
func withReplyTo(c *cassette) {
	c.messages[0]["replyTo"] = []any{map[string]any{"emailAddress": map[string]any{"name": "Moves", "address": "moves@contoso.example"}}}
}

// A reply continues the chain its original carries, so a fetched message
// brings its transport headers and its Reply-To.
func TestGraph_FetchCarriesTheThreadingHeaders(t *testing.T) {
	c, box := newCassette(t)
	withReplyTo(c)
	c.messages[1]["internetMessageHeaders"] = []any{
		map[string]any{"name": "In-Reply-To", "value": "<first.0001@contoso.example>"},
		map[string]any{"name": "References", "value": "<first.0001@contoso.example>"},
	}
	second, err := box.Fetch(t.Context(), "AAMkAGI1-second")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(second.InReplyTo, []mail.MessageID{"first.0001@contoso.example"}) || !slices.Equal(second.References, second.InReplyTo) {
		t.Errorf("threading headers: in-reply-to %v references %v", second.InReplyTo, second.References)
	}
	last := c.requests[len(c.requests)-1].URL.Query().Get("$select")
	if !strings.Contains(last, "internetMessageHeaders") {
		t.Errorf("fetch must select the transport headers: %s", last)
	}
	first, err := box.Fetch(t.Context(), "AAMkAGI1-first")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.ReplyTo) != 1 || first.ReplyTo[0].Email != "moves@contoso.example" {
		t.Errorf("ReplyTo = %v", first.ReplyTo)
	}
}
