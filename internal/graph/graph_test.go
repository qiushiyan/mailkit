package graph_test

import (
	"encoding/base64"
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
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
				ok = ok || strings.Contains(subject, strings.ToLower(strings.Trim(strings.TrimPrefix(alt, "subject:"), `"`)))
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
		writeJSON(map[string]any{"value": out})
	case path == "/me/sendMail":
		b, _ := io.ReadAll(r.Body)
		c.sendBody = string(b)
		if r.Header.Get("Content-Type") != "text/plain" {
			http.Error(w, `{"error":{"code":"BadRequest","message":"MIME send needs text/plain"}}`, 400)
			return
		}
		w.WriteHeader(http.StatusAccepted)
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
		writeJSON(c.item)
	case strings.HasPrefix(path, "/me/messages/"):
		id, _ := url.PathUnescape(strings.TrimPrefix(path, "/me/messages/"))
		for _, m := range c.messages {
			if str(m, "id") == id {
				writeJSON(m)
				return
			}
		}
		http.Error(w, `{"error":{"code":"ErrorItemNotFound","message":"The specified object was not found in the store."}}`, 404)
	default:
		http.Error(w, "unexpected "+r.URL.String(), 500)
	}
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

func TestGraph_ThreeAttachmentKindsAndEmbeddedRecursion(t *testing.T) {
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
	id, err := box.Send(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	if id != "<x@mailkit>" {
		t.Errorf("send should return the Message-ID as the handle, got %q", id)
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
