// Package images fetches the pictures a message only references by URL, and
// labels every downloaded image by size so nothing is silently withheld.
//
// Fetching a remote image tells the sender the mail was opened. That is
// inherent to remote images and accepted here: the information in the
// picture is the point.
package images

import (
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"github.com/qiushiyan/mailkit/internal/attachments"
)

// Kind is the label a downloaded image carries.
type Kind string

const (
	// Pixel: either edge <= 2px. Tracking beacons.
	Pixel Kind = "pixel"
	// Small: minimum edge < 100px or area < 40000px. Wordmarks, avatars,
	// store badges. Both tests are needed: a wordmark is wide but short
	// (319x43), an avatar square but tiny (108x108); each test misses one.
	Small Kind = "small"
	// Image: everything else -- the picture actually worth reading.
	Image Kind = "image"
)

// Classify labels an image by its dimensions. Unknown dimensions are not
// filtered; they are labelled as an image so the second step is not skipped.
func Classify(width, height int) Kind {
	if width <= 0 || height <= 0 {
		return Image
	}
	if width <= 2 || height <= 2 {
		return Pixel
	}
	if min(width, height) < 100 || width*height < 40_000 {
		return Small
	}
	return Image
}

// Fetched is the result of one download.
type Fetched struct {
	URL    string `json:"url"`
	Path   string `json:"path,omitempty"`
	Size   int64  `json:"size,omitempty"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
	Kind   Kind   `json:"kind,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Fetcher downloads remote images with a bounded client.
type Fetcher struct {
	Client *http.Client
	// Cap is the largest body accepted; larger is skipped, not truncated.
	Cap int64
	// UserAgent: some CDNs refuse a non-browser agent outright.
	UserAgent string
}

// Default is the production fetcher: 30s per request, 25 MB cap.
var Default = &Fetcher{
	Client:    &http.Client{Timeout: 30 * time.Second},
	Cap:       25 << 20,
	UserAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36",
}

// FetchAll downloads every URL into dir. A failure on one URL is recorded in
// its result and does not stop the others.
func (f *Fetcher) FetchAll(ctx context.Context, urls []string, dir string) []Fetched {
	out := make([]Fetched, 0, len(urls))
	names := make([]string, 0, len(urls))
	for _, u := range urls {
		base := u
		if i := strings.Index(base, "?"); i >= 0 {
			base = base[:i]
		}
		if i := strings.LastIndex(base, "/"); i >= 0 {
			base = base[i+1:]
		}
		if filepath.Ext(base) == "" {
			base += ".bin"
		}
		names = append(names, base)
	}
	paths, err := attachments.Allocate(dir, names)
	if err != nil {
		for _, u := range urls {
			out = append(out, Fetched{URL: u, Error: err.Error()})
		}
		return out
	}
	for i, u := range urls {
		out = append(out, f.fetchOne(ctx, u, paths[i]))
	}
	return out
}

func (f *Fetcher) fetchOne(ctx context.Context, url, dest string) Fetched {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Fetched{URL: url, Error: err.Error()}
	}
	req.Header.Set("User-Agent", f.UserAgent)
	resp, err := f.Client.Do(req)
	if err != nil {
		return Fetched{URL: url, Error: err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Fetched{URL: url, Error: fmt.Sprintf("HTTP %d", resp.StatusCode)}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, f.Cap+1))
	if err != nil {
		return Fetched{URL: url, Error: err.Error()}
	}
	if int64(len(body)) > f.Cap {
		return Fetched{URL: url, Error: fmt.Sprintf("larger than %d MB, skipped", f.Cap>>20)}
	}
	if err := os.WriteFile(dest, body, 0o644); err != nil {
		return Fetched{URL: url, Error: err.Error()}
	}
	return Describe(url, dest, int64(len(body)))
}

// Describe labels a file already on disk.
func Describe(url, path string, size int64) Fetched {
	r := Fetched{URL: url, Path: path, Size: size}
	if w, h, err := Dimensions(path); err == nil {
		r.Width, r.Height = w, h
	}
	r.Kind = Classify(r.Width, r.Height)
	return r
}

// Dimensions reads only the header of a PNG, GIF or JPEG.
func Dimensions(path string) (int, int, error) {
	fh, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer fh.Close()
	cfg, _, err := image.DecodeConfig(fh)
	if err != nil {
		return 0, 0, errors.New("not a decodable image")
	}
	return cfg.Width, cfg.Height, nil
}
