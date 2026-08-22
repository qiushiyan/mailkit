// Package mail is the seam between a mail provider and everything that is
// about mail rather than about a provider.
//
// A Mailbox does seven things and no more: prove who it is, resolve a
// Message-ID, search, fetch one message, fetch a conversation, stream a
// stored part, and send bytes that were prepared elsewhere. Everything
// downstream -- HTML to text, quote folding, clustering, the send gate --
// operates on the types here and never sees a provider.
package mail

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// Mailbox is one authenticated account on one provider.
//
// Search is exact: every returned envelope satisfies the Criteria, and the
// result is either limit long or the mailbox had no more matches. An adapter
// whose provider cannot express a predicate pages a coarser request and
// narrows locally; the caller never learns which.
type Mailbox interface {
	// Account proves the credentials work. ErrAuth carries the command that
	// would fix it.
	Account(ctx context.Context) (Account, error)
	// Resolve turns an RFC 822 Message-ID -- the one identifier every client
	// and provider agree on -- into this provider's envelope.
	Resolve(ctx context.Context, id MessageID) (Envelope, error)
	// Search returns up to limit envelopes, each matching c.
	Search(ctx context.Context, c Criteria, limit int) ([]Envelope, error)
	// Fetch returns one message with its body and parts in one call.
	Fetch(ctx context.Context, id string) (Message, error)
	// Conversation returns every message the provider groups with convID,
	// ascending by Received.
	Conversation(ctx context.Context, convID string) ([]Message, error)
	// Open streams a StoredPart's bytes. Other part kinds return ErrNoBytes.
	Open(ctx context.Context, h Handle, w io.Writer) error
	// Send transmits p exactly as prepared. The gate is upstream; this is
	// transport.
	Send(ctx context.Context, p *Prepared) (providerID string, err error)
}

// Account is the address a Mailbox acts as, and the ceiling it sends under.
type Account struct {
	Address   string
	SendLimit int64 // bytes of the prepared message
}

// MessageID is an RFC 822 Message-ID without its angle brackets.
type MessageID string

// Handle identifies a stored part, scoped to the adapter that issued it.
type Handle string

// Address is one mailbox participant.
type Address struct {
	Name  string
	Email string
}

// Envelope is what a listing returns: everything about a message except
// its body.
type Envelope struct {
	ID             string
	ConversationID string
	MessageID      MessageID
	// Received is the provider's own timestamp and the ordering key. It is
	// always zoned; a mailbox happily mixes senders who omit the offset, and
	// sorting naive and aware values together is a defect this project had.
	Received time.Time
	// DateHeader is the Date header as sent, for display only.
	DateHeader     string
	From           Address
	To, Cc         []Address
	Subject        string
	Snippet        string
	HasAttachments bool
}

// Body is a message body as the provider holds it. HTML is preferred; Text
// is set only when the message had no HTML part.
type Body struct {
	HTML string
	Text string
}

// Message is one fetched message.
type Message struct {
	Envelope
	Body Body
	// ProviderFolded is the provider's own quote-stripped body (Graph's
	// uniqueBody). It is a hint for the fold, verified like any other
	// boundary, and never output on its own.
	ProviderFolded *Body
	// Parts in document order, so "the first attachment" is stable.
	Parts []Part
}

// Part is one attachment-like thing on a message. Which of the three kinds
// it is, and therefore where its bytes live, is the Content.
type Part struct {
	// Name exactly as the provider gave it, or synthesised from the
	// Content-ID for a nameless inline image. Not safe to write to disk;
	// the attachments store owns that.
	Name      string
	MIME      string
	Size      int64
	Inline    bool
	ContentID string
	Content   PartContent
}

// PartContent is sealed: a part is stored, linked, or embedded.
type PartContent interface{ isPartContent() }

// StoredPart has its bytes in the message; Open works.
type StoredPart struct{ Handle Handle }

// LinkedPart is a reference to bytes elsewhere (OneDrive, SharePoint).
type LinkedPart struct{ URL string }

// EmbeddedPart is a whole message attached as an item. Truncated is set when
// the adapter stopped recursing at its depth budget.
type EmbeddedPart struct {
	Item      *Message
	Truncated bool
}

func (StoredPart) isPartContent()   {}
func (LinkedPart) isPartContent()   {}
func (EmbeddedPart) isPartContent() {}

// Handle returns the part's handle when it is stored, and false otherwise.
func (p Part) Handle() (Handle, bool) {
	s, ok := p.Content.(StoredPart)
	return s.Handle, ok
}

// Kind names the content variant for output.
func (p Part) Kind() string {
	switch p.Content.(type) {
	case StoredPart:
		return "stored"
	case LinkedPart:
		return "cloud_link"
	case EmbeddedPart:
		return "embedded_message"
	}
	return "unknown"
}

// Sentinel errors. Adapters wrap them in a *ProviderError.
var (
	ErrAuth     = errors.New("not authenticated")
	ErrNotFound = errors.New("not found")
	ErrNoBytes  = errors.New("part has no bytes to fetch from the message")
	ErrTooLarge = errors.New("message exceeds the provider's size limit")
)

// ProviderError says which provider and operation failed, and why.
type ProviderError struct {
	Provider string
	Op       string
	Err      error
	// Hint is a runnable command that recovers, when one exists.
	Hint string
}

func (e *ProviderError) Error() string {
	if e.Hint != "" {
		return fmt.Sprintf("%s %s: %v. Run:\n    %s", e.Provider, e.Op, e.Err, e.Hint)
	}
	return fmt.Sprintf("%s %s: %v", e.Provider, e.Op, e.Err)
}

func (e *ProviderError) Unwrap() error { return e.Err }
