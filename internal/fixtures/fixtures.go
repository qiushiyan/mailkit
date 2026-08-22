// Package fixtures loads the recorded raw Gmail API JSON under testdata/
// through the real adapter's translation. The fixtures are the real
// writer's shape; the Message values are produced by the code under test,
// never hand-written, so a type change breaks compilation here rather than
// silently pinning an old shape.
package fixtures

import (
	"encoding/json/v2"
	"os"
	"path/filepath"
	"runtime"
	"testing"

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
