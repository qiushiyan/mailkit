// Package attachments owns where downloaded bytes land and what they are
// called. Names come from someone else's mail, so they are sanitised
// exactly once, here; every path written is inside the directory asked for;
// and two parts that would collide get distinct names before any file is
// opened.
package attachments

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var unsafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// Safe derives a filename safe to write from one we did not author.
// "../../../etc/passwd" becomes "etc-passwd".
func Safe(name, fallback string) string {
	name = strings.Trim(unsafe.ReplaceAllString(strings.TrimSpace(name), "-"), "-.")
	if name == "" {
		name = fallback
	}
	if len(name) > 120 {
		name = name[:120]
	}
	return name
}

// Allocate creates dir and returns one destination path per name, in order.
// Names are sanitised; duplicates get -2, -3 suffixes; every path is
// verified to sit inside dir. Nothing is opened.
func Allocate(dir string, names []string) ([]string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	taken := map[string]bool{}
	out := make([]string, 0, len(names))
	for i, raw := range names {
		base := Safe(raw, fmt.Sprintf("part-%d", i+1))
		candidate := base
		ext := filepath.Ext(base)
		stem := strings.TrimSuffix(base, ext)
		for n := 2; taken[strings.ToLower(candidate)] || exists(filepath.Join(absDir, candidate)); n++ {
			candidate = fmt.Sprintf("%s-%d%s", stem, n, ext)
		}
		taken[strings.ToLower(candidate)] = true
		full := filepath.Join(absDir, candidate)
		if rel, err := filepath.Rel(absDir, full); err != nil || strings.HasPrefix(rel, "..") {
			return nil, fmt.Errorf("refusing to write outside %s: %q", dir, raw)
		}
		out = append(out, full)
	}
	return out, nil
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// DefaultDir is where downloads land when the caller names nowhere: under
// the state directory, never the cwd, so an agent cannot litter whatever
// directory it happens to be standing in.
func DefaultDir(messageID string) string {
	return filepath.Join(StateDir(), "attachments", Safe(messageID, "message"))
}

// StateDir is $XDG_STATE_HOME/mailkit or ~/.local/state/mailkit.
func StateDir() string {
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "mailkit")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "mailkit")
}
