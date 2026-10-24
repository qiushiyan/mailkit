// Package fixtures loads the recorded raw Gmail API JSON under testdata/
// through the real adapter's translation. The fixtures are the real
// writer's shape; the Message values are produced by the code under test,
// never hand-written, so a type change breaks compilation here rather than
// silently pinning an old shape.
package fixtures

import (
	"bytes"
	"encoding/json/v2"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	gm "google.golang.org/api/gmail/v1"

	"github.com/qiushiyan/mailkit/internal/gmail"
	"github.com/qiushiyan/mailkit/internal/mail"
)

// Real message ids, so a test can name what it is asserting about.
const (
	TenancyThread = "19fd6b394c784f9b" // 6 messages, caret and Outlook-header quoting
	AppleForward  = "1989fc5ed9469c6c" // "Begin forwarded message:" under "> "
	RemoteOnly    = "1a02340ea531858a" // Taskrabbit: a screenshot referenced by URL, nothing attached
	InlineCID     = "1a01c98dd4e47691" // two inline images by Content-ID (Gmail happens to give them filenames)
	IKEASeed      = "1a02367a18eae254" // order 1623209215 labelled, PO box and onelink ids bare
)

func root() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "testdata", "gmail")
}

func read(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root(), name))
	if err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	return b
}

// Message loads one recorded messages.get?format=full response.
func Message(t testing.TB, id string) mail.Message {
	t.Helper()
	var m gm.Message
	if err := json.Unmarshal(read(t, "msg-"+id+".json"), &m); err != nil {
		t.Fatalf("decode %s: %v", id, err)
	}
	return gmail.Translate(&m)
}

// Thread loads one recorded threads.get?format=full response, ascending.
func Thread(t testing.TB, id string) []mail.Message {
	t.Helper()
	var th gm.Thread
	if err := json.Unmarshal(read(t, "thread-"+id+".json"), &th); err != nil {
		t.Fatalf("decode thread %s: %v", id, err)
	}
	out := make([]mail.Message, 0, len(th.Messages))
	for _, m := range th.Messages {
		out = append(out, gmail.Translate(m))
	}
	return out
}

// RawJSON returns a fixture's bytes for cassette servers.
func RawJSON(t testing.TB, name string) []byte { return read(t, name) }

// Msg builds a scenario message. The fold and clustering rules are tested
// on conversations built from it rather than from serialised fixtures, so a
// type change breaks compilation, not a fixture.
func Msg(id, conv string, received time.Time, from, subject, html string) mail.Message {
	return mail.Message{
		ID: id, ConversationID: conv, MessageID: mail.MessageID(id + "@test"),
		Received: received, DateHeader: received.Format(time.RFC1123Z),
		From: mail.ParseAddress(from), To: []mail.Address{{Email: "me@example.com"}}, Subject: subject,
		Body: mail.Body{HTML: html},
	}
}

// Day is n days after 2026-08-01 10:00 UTC, the scenarios' calendar.
func Day(n int) time.Time { return time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC).AddDate(0, 0, n) }

// Para is an HTML body of one paragraph per line.
func Para(lines ...string) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString("<p>" + l + "</p>")
	}
	return b.String()
}

// PNG is an image of the given size, for the size-label rules: the real
// pictures from recorded mail are not in the repository.
func PNG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 0, 255})
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}
