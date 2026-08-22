package attachments

import (
	"path/filepath"
	"strings"
	"testing"
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
