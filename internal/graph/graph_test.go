package graph_test

import (
	"encoding/base64"
	"encoding/json/v2"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
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
	id, err := box.Send(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	if id != "" {
		t.Errorf("MIME sendMail returns 202 and no id; inventing one misleads recovery, got %q", id)
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
