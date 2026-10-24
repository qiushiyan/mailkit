// Package pintest pins what callers read from a command: its stdout, stderr
// and exit code, byte for byte, in a file under testdata/contract. A pinned
// file that changes is a change every caller of that command sees, so it is
// reviewed as a diff. MAILKIT_CONTRACT_UPDATE=1 rewrites the files; it is a
// switch of its own so that accepting a help-page change (CLIKIT_UPDATE) can
// never rewrite a contract.
package pintest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Run is one invocation and what it produced.
type Run struct {
	Tool           string
	Args           []string
	Code           int
	Stdout, Stderr string
}

// String is the pinned form: the command line, the exit code, then both
// streams under their own markers.
func (r Run) String() string {
	var b strings.Builder
	b.WriteString("$ " + r.Tool)
	for _, a := range r.Args {
		b.WriteString(" " + Quote(a))
	}
	fmt.Fprintf(&b, "\nexit %d\n--- stdout\n%s--- stderr\n%s", r.Code, r.Stdout, r.Stderr)
	return b.String()
}

// Quote renders one argument the way a shell would need it.
func Quote(a string) string {
	if a != "" && !strings.ContainsAny(a, " \t\n'\"\\$`*?[]{}()<>|&;#~!%") {
		return a
	}
	return "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
}

// Check compares got with dir/name.txt, or writes it there under
// MAILKIT_CONTRACT_UPDATE=1.
func Check(t testing.TB, dir, name, got string) {
	t.Helper()
	file := filepath.Join(dir, name+".txt")
	if os.Getenv("MAILKIT_CONTRACT_UPDATE") == "1" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(file)
	if err != nil {
		t.Errorf("%s: %v (MAILKIT_CONTRACT_UPDATE=1 writes it)", name, err)
		return
	}
	if got != string(want) {
		t.Errorf("%s differs from %s (MAILKIT_CONTRACT_UPDATE=1 accepts it)\n--- got\n%s--- want\n%s", name, file, got, want)
	}
}

// Stale reports files in dir that no case named, so a removed case shows up
// as well as a changed one.
func Stale(t testing.TB, dir string, names []string) {
	t.Helper()
	if os.Getenv("MAILKIT_CONTRACT_UPDATE") == "1" {
		return
	}
	known := map[string]bool{}
	for _, n := range names {
		known[n+".txt"] = true
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.txt"))
	for _, f := range files {
		if !known[filepath.Base(f)] {
			t.Errorf("%s belongs to no case; remove it", f)
		}
	}
}
