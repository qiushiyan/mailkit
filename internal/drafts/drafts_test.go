package drafts_test

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/qiushiyan/mailkit/internal/drafts"
)

// Rule: a draft is sent once. The claim is decided by who holds the lock,
// never by how old the lock file is -- a live process that stalled for a
// minute still owns its transition.
func TestClaim_IsDecidedByOwnershipNotAge(t *testing.T) {
	s := drafts.Store{Dir: t.TempDir()}
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	rec, _, err := s.Create(drafts.Compose{Account: "memory", From: "me@example.com", To: []string{"a@example.com"}, Subject: "once", Body: drafts.PlainBody("hello there")}, now)
	if err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(s.Dir, rec.ID+".lock")
	fh, err := os.OpenFile(lock, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	if err := syscall.Flock(int(fh.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Minute)
	os.Chtimes(lock, old, old)
	if _, err := s.Claim(rec.ID); err == nil {
		t.Fatal("a claim succeeded while another process held the lock")
	}
	if r, _ := s.Load(rec.ID); r.State != drafts.Pending {
		t.Fatalf("the contested draft changed state to %s", r.State)
	}
	syscall.Flock(int(fh.Fd()), syscall.LOCK_UN)
	if _, err := s.Claim(rec.ID); err != nil {
		t.Fatalf("once released, the claim must go through: %v", err)
	}
}
