// Package attachments owns what downloaded bytes are called. Names come
// from someone else's mail, so they are sanitised exactly once, here; every
// path written is inside the directory asked for; and two parts that would
// collide get distinct names before any file is opened. Which directory
// that is by default belongs to internal/paths.
package attachments

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/qiushiyan/mailkit/internal/mail"
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

// Select picks the parts of msg to download: each part named by its name or
// its handle, or every part with bytes when names is empty. A name has to
// pick one part, so a name two parts share is refused with the handles that
// tell them apart. Only stored parts have bytes here; a message whose
// attachments are all links or embedded messages says so.
func Select(msg mail.Message, names []string) ([]mail.Part, error) {
	var stored []mail.Part
	for _, p := range msg.Parts {
		if _, ok := p.Handle(); ok {
			stored = append(stored, p)
		}
	}
	if len(stored) == 0 {
		if len(msg.Parts) > 0 {
			return nil, fmt.Errorf("message %s has %d attachment(s) but none hold bytes here (cloud links or embedded messages); see mail-find attachments %s", msg.ID, len(msg.Parts), msg.ID)
		}
		return nil, fmt.Errorf("message %s has no attachments", msg.ID)
	}
	if len(names) == 0 {
		return stored, nil
	}
	var wanted []mail.Part
	for _, token := range names {
		var matches []mail.Part
		for _, p := range stored {
			if h, _ := p.Handle(); token == p.Name || token == string(h) {
				matches = append(matches, p)
			}
		}
		switch len(matches) {
		case 0:
			avail := make([]string, 0, len(stored))
			for _, p := range stored {
				avail = append(avail, p.Name)
			}
			return nil, fmt.Errorf("no attachment %q on that message; has: %s", token, strings.Join(avail, ", "))
		case 1:
			wanted = append(wanted, matches[0])
		default:
			handles := make([]string, 0, len(matches))
			for _, p := range matches {
				h, _ := p.Handle()
				handles = append(handles, string(h))
			}
			return nil, fmt.Errorf("%q names %d attachments; pick one by handle: %s", token, len(matches), strings.Join(handles, ", "))
		}
	}
	return wanted, nil
}

// Source is where stored parts' bytes come from; a mail.Mailbox is one.
type Source interface {
	Open(ctx context.Context, h mail.Handle, w io.Writer) error
}

// Saved is one downloaded part: its name as the mail gave it, and where it
// landed.
type Saved struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// Save streams each part's bytes into dir, under the names Allocate gives
// them, and says where each landed. A part that fails to stream leaves no
// file behind.
func Save(ctx context.Context, src Source, dir string, parts []mail.Part) ([]Saved, error) {
	names := make([]string, 0, len(parts))
	for _, p := range parts {
		names = append(names, p.Name)
	}
	paths, err := Allocate(dir, names)
	if err != nil {
		return nil, err
	}
	var out []Saved
	for i, p := range parts {
		h, _ := p.Handle()
		fh, err := os.Create(paths[i])
		if err != nil {
			return nil, err
		}
		err = src.Open(ctx, h, fh)
		fh.Close()
		if err != nil {
			os.Remove(paths[i])
			return nil, err
		}
		var size int64
		if st, err := os.Stat(paths[i]); err == nil {
			size = st.Size()
		}
		out = append(out, Saved{p.Name, paths[i], size})
	}
	return out, nil
}
