package attachments

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/qiushiyan/mailkit/internal/mail"
)

// Owner of filename sanitisation and destination allocation.
func TestSafe(t *testing.T) {
	for in, want := range map[string]string{
		"../../../etc/passwd":    "etc-passwd",
		"report.pdf":             "report.pdf",
		"weird name (1).PNG":     "weird-name-1-.PNG",
		"":                       "fallback",
		"....":                   "fallback",
		strings.Repeat("a", 200): strings.Repeat("a", 120),
	} {
		if got := Safe(in, "fallback"); got != want {
			t.Errorf("Safe(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAllocate_DistinctAndContained(t *testing.T) {
	dir := t.TempDir()
	paths, err := Allocate(dir, []string{"a.pdf", "a.pdf", "../a.pdf", "", "A.PDF"})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, p := range paths {
		rel, err := filepath.Rel(dir, p)
		if err != nil || strings.HasPrefix(rel, "..") {
			t.Errorf("outside dir: %s", p)
		}
		key := strings.ToLower(p)
		if seen[key] {
			t.Errorf("duplicate destination: %s", p)
		}
		seen[key] = true
	}
	if filepath.Base(paths[1]) != "a-2.pdf" {
		t.Errorf("second a.pdf should be a-2.pdf, got %s", filepath.Base(paths[1]))
	}
}

// Owner of the fetch-by-name rule: a name picks one part or none, a shared
// name is refused with the handles, and only stored parts have bytes.
func TestSelect(t *testing.T) {
	stored := func(name, h string) mail.Part {
		return mail.Part{Name: name, Content: mail.StoredPart{Handle: mail.Handle(h)}}
	}
	msg := mail.Message{ID: "m1", Parts: []mail.Part{
		stored("a.pdf", "h1"), stored("dup.pdf", "h2"), stored("dup.pdf", "h3"),
		{Name: "deck", Content: mail.LinkedPart{URL: "https://drive.example/deck"}},
	}}
	pick := func(names ...string) string {
		parts, err := Select(msg, names)
		if err != nil {
			return "error: " + err.Error()
		}
		var hs []string
		for _, p := range parts {
			h, _ := p.Handle()
			hs = append(hs, string(h))
		}
		return strings.Join(hs, ",")
	}
	for _, c := range []struct {
		names []string
		want  string
	}{
		{nil, "h1,h2,h3"},
		{[]string{"a.pdf"}, "h1"},
		{[]string{"h3", "a.pdf"}, "h3,h1"},
		{[]string{"dup.pdf"}, `error: "dup.pdf" names 2 attachments; pick one by handle: h2, h3`},
		{[]string{"deck"}, `error: no attachment "deck" on that message; has: a.pdf, dup.pdf, dup.pdf`},
	} {
		if got := pick(c.names...); got != c.want {
			t.Errorf("Select(%q) = %s, want %s", c.names, got, c.want)
		}
	}
	links := mail.Message{ID: "m2", Parts: msg.Parts[3:]}
	if _, err := Select(links, nil); err == nil || !strings.Contains(err.Error(), "none hold bytes") {
		t.Errorf("a message of links only must say none hold bytes: %v", err)
	}
	if _, err := Select(mail.Message{ID: "m3"}, nil); err == nil || !strings.Contains(err.Error(), "no attachments") {
		t.Errorf("a message with no parts must say so: %v", err)
	}
}
