// Package memory is the Mailbox adapter tests run against: a slice of
// messages and a map of part bytes. It has no branches of its own -- Search
// is Criteria.Match over everything it holds, which is what makes it the
// reference semantics rather than a second opinion.
package memory

import (
	"context"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/qiushiyan/mailkit/internal/mail"
)

// Mailbox is an in-memory mail.Mailbox.
type Mailbox struct {
	Address   string
	SendLimit int64
	// TextOf converts a message body to text for phrase matching. The
	// shared converter is the natural value; tests that set Body.Text
	// only can leave it nil.
	TextOf func(mail.Message) string
	// AuthErr, when set, is returned by every operation -- the not-logged-in path.
	AuthErr error
	// SendErr, when set, is returned by Send after the message was accepted for
	// transmission -- the outcome-unknown path.
	SendErr error

	mu       sync.Mutex
	messages []mail.Message
	parts    map[mail.Handle][]byte
	// Sent records every prepared message Send received, in order.
	Sent []*mail.Prepared
	// Searches records every Criteria Search received, for tests that
	// assert what a caller asked for.
	Searches []mail.Criteria
}

// New builds a mailbox holding msgs.
func New(msgs ...mail.Message) *Mailbox {
	m := &Mailbox{Address: "me@example.com", SendLimit: 25 << 20, parts: map[mail.Handle][]byte{}}
	m.Add(msgs...)
	return m
}

// Add appends messages.
func (m *Mailbox) Add(msgs ...mail.Message) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = append(m.messages, msgs...)
}

// Store registers bytes for a handle.
func (m *Mailbox) Store(h mail.Handle, b []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.parts[h] = b
}

func (m *Mailbox) Account(context.Context) (mail.Account, error) {
	if m.AuthErr != nil {
		return mail.Account{}, m.AuthErr
	}
	return mail.Account{Address: m.Address, SendLimit: m.SendLimit}, nil
}

func (m *Mailbox) Resolve(_ context.Context, id mail.MessageID) (mail.Envelope, error) {
	if m.AuthErr != nil {
		return mail.Envelope{}, m.AuthErr
	}
	id = mail.MessageID(strings.Trim(string(id), "<> "))
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, msg := range m.messages {
		if msg.MessageID == id {
			return msg.Envelope, nil
		}
	}
	return mail.Envelope{}, fmt.Errorf("message-id %s: %w", id, mail.ErrNotFound)
}

func (m *Mailbox) text(msg mail.Message) string {
	if m.TextOf != nil {
		return m.TextOf(msg)
	}
	if msg.Body.Text != "" {
		return msg.Body.Text
	}
	return msg.Body.HTML
}

func (m *Mailbox) Search(_ context.Context, c mail.Criteria, limit int) ([]mail.Envelope, error) {
	if m.AuthErr != nil {
		return nil, m.AuthErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Searches = append(m.Searches, c)
	var out []mail.Envelope
	for _, msg := range m.messages {
		if c.Match(msg.Envelope, m.text(msg)) {
			out = append(out, msg.Envelope)
		}
	}
	// Newest first, like a provider listing.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Received.After(out[j].Received) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *Mailbox) Fetch(_ context.Context, id string) (mail.Message, error) {
	if m.AuthErr != nil {
		return mail.Message{}, m.AuthErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, msg := range m.messages {
		if msg.ID == id {
			return msg, nil
		}
	}
	return mail.Message{}, fmt.Errorf("message %s: %w", id, mail.ErrNotFound)
}

func (m *Mailbox) Conversation(_ context.Context, convID string) ([]mail.Message, error) {
	if m.AuthErr != nil {
		return nil, m.AuthErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []mail.Message
	for _, msg := range m.messages {
		if msg.ConversationID == convID {
			out = append(out, msg)
		}
	}
	slices.SortStableFunc(out, func(a, b mail.Message) int { return a.Received.Compare(b.Received) })
	return out, nil
}

func (m *Mailbox) Open(_ context.Context, h mail.Handle, w io.Writer) error {
	if m.AuthErr != nil {
		return m.AuthErr
	}
	m.mu.Lock()
	b, ok := m.parts[h]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("handle %s: %w", h, mail.ErrNoBytes)
	}
	_, err := w.Write(b)
	return err
}

func (m *Mailbox) Send(_ context.Context, p *mail.Prepared) (string, error) {
	if p.Size() > m.SendLimit {
		return "", mail.ErrTooLarge
	}
	if m.SendErr != nil {
		return "", m.SendErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Sent = append(m.Sent, p)
	return fmt.Sprintf("memory-%d", len(m.Sent)), nil
}

var _ mail.Mailbox = (*Mailbox)(nil)
