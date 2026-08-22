package cli_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qiushiyan/mailkit/internal/fixtures"
	"github.com/qiushiyan/mailkit/internal/images"
	"github.com/qiushiyan/mailkit/internal/mail"
)

// Rules: attachments key on attachmentId, never on filename; sanitise
// filenames against path traversal; classify images on both minimum edge
// and area; results name the command that recovers what they could not
// return.

func TestAttachments_InlineImageWithoutFilenameIsListedAndFetchable(t *testing.T) {
	m := fixtures.Message(t, fixtures.InlineCID)
	h := newHarness(t, m)
	var inline *mail.Part
	for i := range m.Parts {
		if m.Parts[i].Inline && m.Parts[i].ContentID != "" {
			inline = &m.Parts[i]
		}
	}
	if inline == nil {
		t.Fatalf("fixture %s no longer carries an inline CID part", fixtures.InlineCID)
	}
	handle, _ := inline.Handle()
	h.box.Store(handle, []byte("PNG-BYTES"))

	rows := mustOK(t, h.find("attachments", m.ID)).jsonList(t, "attachments")
	found := false
	for _, r := range rows {
		if r["handle"] == string(handle) {
			found = true
			if r["inline"] != true {
				t.Errorf("inline part not flagged inline: %v", r)
			}
			if r["name"] == "" {
				t.Errorf("nameless inline part must be given a name from its Content-ID")
			}
		}
	}
	if !found {
		t.Fatalf("inline part %s missing from attachments listing", handle)
	}

	// Fetch by handle writes the bytes under the attachment dir.
	saved := mustOK(t, h.find("fetch", m.ID, "--attachment", string(handle))).jsonList(t, "saved")
	if len(saved) != 1 {
		t.Fatalf("saved %d files, want 1", len(saved))
	}
	b, err := os.ReadFile(saved[0]["path"].(string))
	if err != nil || string(b) != "PNG-BYTES" {
		t.Errorf("fetched bytes wrong: %q %v", b, err)
	}
}

func TestFetch_NamesAreSanitisedAndContainedAndDistinct(t *testing.T) {
	m := msg("m1", "c1", day(1), "a@example.com", "files", para("Three files attached to this message for the sanitisation test."))
	m.Parts = []mail.Part{
		{Name: "../../../etc/passwd", MIME: "text/plain", Size: 5, Content: mail.StoredPart{Handle: "h1"}},
		{Name: "report.pdf", MIME: "application/pdf", Size: 5, Content: mail.StoredPart{Handle: "h2"}},
		{Name: "report.pdf", MIME: "application/pdf", Size: 5, Content: mail.StoredPart{Handle: "h3"}},
	}
	m.HasAttachments = true
	h := newHarness(t, m)
	for _, hd := range []string{"h1", "h2", "h3"} {
		h.box.Store(mail.Handle(hd), []byte(hd+"-data"))
	}
	saved := mustOK(t, h.find("fetch", "m1", "--all")).jsonList(t, "saved")
	if len(saved) != 3 {
		t.Fatalf("saved %d, want 3", len(saved))
	}
	dir := h.deps.AttachmentDir("m1")
	seen := map[string]bool{}
	for _, s := range saved {
		p := s["path"].(string)
		rel, err := filepath.Rel(dir, p)
		if err != nil || strings.HasPrefix(rel, "..") {
			t.Errorf("written outside the attachment dir: %s", p)
		}
		if seen[p] {
			t.Errorf("two attachments written to the same path: %s", p)
		}
		seen[p] = true
		if _, err := os.Stat(p); err != nil {
			t.Errorf("not written: %s", p)
		}
	}
	if _, ok := seen[filepath.Join(dir, "etc-passwd")]; !ok {
		t.Errorf("traversal name should land as etc-passwd; got %v", seen)
	}
	// Same name twice: selecting by name is ambiguous and says so with handles.
	r := mustFail(t, h.find("fetch", "m1", "--attachment", "report.pdf"))
	if !strings.Contains(r.stderr, "h2") || !strings.Contains(r.stderr, "h3") {
		t.Errorf("ambiguity error must list the handles to pick from: %s", r.stderr)
	}
}

func TestAttachments_RemoteOnlyMessageNamesTheRecoveryCommand(t *testing.T) {
	m := fixtures.Message(t, fixtures.RemoteOnly)
	h := newHarness(t, m)
	r := mustOK(t, h.find("attachments", m.ID))
	steps := r.json(t)["next_steps"].([]any)
	want := "mail-find read " + m.ID + " --fetch-remote"
	if len(steps) == 0 || !strings.Contains(steps[0].(string), want) {
		t.Fatalf("a message whose only content is a linked image must name %q; got %v", want, steps)
	}
	// The absence flip: no remote images, no nudge.
	plain := msg("p1", "c1", day(1), "a@example.com", "plain", para("No pictures here, just a sentence long enough to be prose."))
	r = mustOK(t, newHarness(t, plain).find("attachments", "p1"))
	if steps := r.json(t)["next_steps"].([]any); len(steps) != 0 {
		t.Errorf("no remote images, yet a nudge fired: %v", steps)
	}
	// read reports the same, and stops once they are fetched.
	r = mustOK(t, h.find("read", m.ID))
	if !strings.Contains(r.stdout, want) {
		t.Errorf("read must also name the recovery command")
	}
}

func TestRead_FetchRemoteLabelsImagesByMinEdgeAndArea(t *testing.T) {
	// Real images from the recorded Taskrabbit mail are not in the repo;
	// serve synthetic PNGs of the shapes the rule was calibrated on.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/wordmark.png":
			w.Write(pngOf(319, 43))
		case "/avatar.png":
			w.Write(pngOf(108, 108))
		case "/pixel.png":
			w.Write(pngOf(1, 1))
		case "/photo.png":
			w.Write(pngOf(640, 480))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	html := `<p>A message whose pictures carry the content.</p>` +
		`<img src="` + srv.URL + `/wordmark.png"><img src="` + srv.URL + `/avatar.png">` +
		`<img src="` + srv.URL + `/pixel.png"><img src="` + srv.URL + `/photo.png">`
	m := msg("m1", "c1", day(1), "a@example.com", "pics", html)
	h := newHarness(t, m)
	h.deps.Images = &images.Fetcher{Client: srv.Client(), Cap: 1 << 20, UserAgent: "test"}
	r := mustOK(t, h.find("read", "m1", "--fetch-remote"))
	got := map[string]string{}
	for _, img := range r.jsonList(t, "remote_images") {
		got[filepath.Base(img["url"].(string))] = img["kind"].(string)
		if img["path"] == nil {
			t.Errorf("image not downloaded: %v", img)
		}
	}
	want := map[string]string{"wordmark.png": "small", "avatar.png": "small", "pixel.png": "pixel", "photo.png": "image"}
	for name, kind := range want {
		if got[name] != kind {
			t.Errorf("%s labelled %q, want %q", name, got[name], kind)
		}
	}
	if steps := r.json(t)["next_steps"].([]any); len(steps) != 0 {
		t.Errorf("after --fetch-remote the nudge must not fire: %v", steps)
	}
}
