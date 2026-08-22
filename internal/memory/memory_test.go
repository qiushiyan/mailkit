package memory_test

import (
	"testing"
	"time"

	"github.com/qiushiyan/mailkit/internal/mail"
	"github.com/qiushiyan/mailkit/internal/mailtest"
	"github.com/qiushiyan/mailkit/internal/memory"
	"github.com/qiushiyan/mailkit/internal/render"
)

// The memory adapter is the reference semantics; it must pass the same
// contract the real adapters do.
func TestMemory_Contract(t *testing.T) {
	at := time.Date(2026, 8, 6, 9, 51, 55, 0, time.UTC)
	mk := func(id, conv string, when time.Time, from, subject, html string) mail.Message {
		return mail.Message{
			ID: id, ConversationID: conv, MessageID: mail.MessageID(id + "@example.test"), Received: when,
			DateHeader: when.Format(time.RFC1123Z), From: mail.ParseAddress(from), To: []mail.Address{{Email: "me@example.com"}}, Subject: subject, Body: mail.Body{HTML: html}}
	}
	first := mk("m1", "c1", at, "Agent <agent@mail.letting.co.uk>", "Flat 819", "<p>The unique phrase is heliotrope.</p>")
	first.Parts = []mail.Part{{Name: "lease.pdf", MIME: "application/pdf", Size: 4, Content: mail.StoredPart{Handle: "m1/att1"}}}
	first.HasAttachments = true
	second := mk("m2", "c1", at.Add(time.Hour), "me@example.com", "Re: Flat 819", "<p>Thanks, received.</p>")
	third := mk("m3", "c2", at.AddDate(0, 0, 3), "news@other.org", "Newsletter", "<p>Unrelated newsletter content.</p>")
	box := memory.New(first, second, third)
	box.TextOf = func(m mail.Message) string { return render.Text(m.Body) }
	box.Store("m1/att1", []byte("%PDF-1.4"))
	mailtest.Run(t, box, mailtest.Scenario{
		Address: "me@example.com", MessageID: "m1", RFC822ID: "m1@example.test", Conversation: "c1", ConversationSize: 2,
		UniquePhrase: "heliotrope", FromDomain: "letting.co.uk", StoredPartName: "lease.pdf", StoredPrefix: []byte("%PDF"), Received: at,
	})
}
