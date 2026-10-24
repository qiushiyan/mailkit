// Package pintest pins what callers read from a command: its stdout, stderr
// and exit code, byte for byte, in a file under testdata/contract. A pinned
// file that changes is a change every caller of that command sees, so it is
// reviewed as a diff. MAILKIT_CONTRACT_UPDATE=1 rewrites the files; it is a
// switch of its own so that accepting a help-page change (CLIKIT_UPDATE) can
// never rewrite a contract.
package pintest

import (
	"encoding/json/v2"
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

// Commands collects the command lines for tool that the files under dirs
// show a reader: in the pinned streams, every line or JSON string from the
// tool's name on (an instruction such as "To see them: mail-find context X
// --min-score 1"), and in help pages, every line of an Examples section.
// The pinned invocations themselves, usage lines and the "Run ... --help"
// pointer are left out: they are the parser's own output.
func Commands(t testing.TB, tool string, dirs ...string) []string {
	t.Helper()
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		_, after, ok := strings.Cut(s, tool+" ")
		if !ok || seen[after] {
			return
		}
		seen[after] = true
		out = append(out, tool+" "+after)
	}
	for _, dir := range dirs {
		files, _ := filepath.Glob(filepath.Join(dir, "*.txt"))
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			examples := false
			for line := range strings.SplitSeq(string(b), "\n") {
				switch {
				case strings.HasPrefix(line, "Examples:"):
					examples = true
					continue
				case !strings.HasPrefix(line, " "):
					examples = false
				}
				switch {
				case examples:
					add(strings.TrimSpace(line))
				case strings.HasPrefix(line, "$ "), strings.HasPrefix(line, "Usage: "), strings.Contains(line, `Run "`):
				case strings.HasPrefix(line, "{"):
					var v any
					if json.Unmarshal([]byte(line), &v) == nil {
						for _, s := range stringsIn(v) {
							for l := range strings.SplitSeq(s, "\n") {
								add(strings.TrimSpace(l))
							}
						}
					}
				case strings.Contains(string(b), "--- stdout"):
					add(strings.TrimSpace(line))
				}
			}
		}
	}
	return out
}

func stringsIn(v any) []string {
	switch v := v.(type) {
	case string:
		return []string{v}
	case []any:
		var out []string
		for _, e := range v {
			out = append(out, stringsIn(e)...)
		}
		return out
	case map[string]any:
		var out []string
		for _, e := range v {
			out = append(out, stringsIn(e)...)
		}
		return out
	}
	return nil
}

// Split breaks a command line into its arguments as a shell would: single
// and double quotes group, a backslash outside single quotes escapes, and
// an unquoted redirection, pipe or comment ends the command.
func Split(line string) []string {
	var args []string
	var cur strings.Builder
	inArg, quote := false, rune(0)
	runes := []rune(line)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case quote == '\'':
			if r == '\'' {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case quote == '"':
			switch {
			case r == '"':
				quote = 0
			case r == '\\' && i+1 < len(runes):
				i++
				cur.WriteRune(runes[i])
			default:
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, inArg = r, true
		case r == '\\' && i+1 < len(runes):
			i++
			cur.WriteRune(runes[i])
			inArg = true
		case r == '<' || r == '>' || r == '|' || (r == '#' && !inArg):
			i = len(runes)
		case r == ' ' || r == '\t':
			if inArg {
				args = append(args, cur.String())
				cur.Reset()
				inArg = false
			}
		default:
			cur.WriteRune(r)
			inArg = true
		}
	}
	if inArg {
		args = append(args, cur.String())
	}
	return args
}

// Fenced collects the command lines for tool in the sh code blocks of
// Markdown files, a skill's instructions, with backslash continuations
// joined.
func Fenced(t testing.TB, tool string, files ...string) []string {
	t.Helper()
	var out []string
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		inSh, cont := false, ""
		for line := range strings.SplitSeq(string(b), "\n") {
			switch {
			case strings.HasPrefix(line, "```sh"):
				inSh = true
			case strings.HasPrefix(line, "```"):
				inSh = false
			case inSh:
				line = cont + strings.TrimSpace(line)
				if before, ok := strings.CutSuffix(line, "\\"); ok {
					cont = before
					continue
				}
				cont = ""
				if strings.HasPrefix(line, tool+" ") {
					out = append(out, line)
				}
			}
		}
	}
	return out
}
