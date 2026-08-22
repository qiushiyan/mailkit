package mail

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	netmail "net/mail"
)

// Prepared is a complete RFC 5322 message, the bytes that were reviewed and
// the only thing Send accepts. It is constructed, never assembled by hand,
// so a caller cannot send something that was not prepared.
type Prepared struct {
	raw    []byte
	digest string
}

// NewPrepared reads a full message and validates that it parses as one.
func NewPrepared(r io.Reader) (*Prepared, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, errors.New("prepared message is empty")
	}
	if _, err := netmail.ReadMessage(bytes.NewReader(raw)); err != nil {
		return nil, errors.New("prepared message does not parse as RFC 5322: " + err.Error())
	}
	sum := sha256.Sum256(raw)
	return &Prepared{raw: raw, digest: hex.EncodeToString(sum[:])}, nil
}

// Bytes is the message exactly as prepared. It is a copy: what was reviewed
// cannot be changed through the value handed out.
func (p *Prepared) Bytes() []byte { return bytes.Clone(p.raw) }

// Reader streams the message.
func (p *Prepared) Reader() io.Reader { return bytes.NewReader(p.raw) }

// Size in bytes; the provider's SendLimit is checked against this.
func (p *Prepared) Size() int64 { return int64(len(p.raw)) }

// Digest is the sha256 of the bytes, hex.
func (p *Prepared) Digest() string { return p.digest }

// Header reads one header from the prepared message.
func (p *Prepared) Header(name string) string {
	m, err := netmail.ReadMessage(bytes.NewReader(p.raw))
	if err != nil {
		return ""
	}
	return m.Header.Get(name)
}
