// Package drafts is the approval gate: compose writes a complete message to
// disk and a preview of it; commit sends exactly those bytes, once.
//
// Sending is irreversible and an agent is composing on someone's behalf, so
// composing and sending are separate acts. What makes the gate honest is
// that the draft IS the wire message: the RFC 5322 bytes are built at
// compose time, the preview is rendered from them, their digest is pinned,
// and Send receives them unchanged. A draft moves pending -> sending ->
// sent; the move to sending happens before any network I/O, so a crash
// can never leave a sent message looking pending.
package drafts

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	netmail "net/mail"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	gomail "github.com/wneessen/go-mail"

	"github.com/qiushiyan/mailkit/internal/attachments"
	"github.com/qiushiyan/mailkit/internal/mail"
)

// State is where a draft is in its single life.
type State string

const (
	Pending State = "pending"
	Sending State = "sending"
	Sent    State = "sent"
	// Unknown: the provider may have accepted the message but the result
	// was lost. Never retried silently; recover by checking sent mail for
	// the subject -- Gmail rewrites the Message-ID on send, so it is not
	// the handle it looks like.
	Unknown State = "unknown"
)

// Record is the draft's metadata, beside its .eml.
type Record struct {
	ID          string       `json:"id"`
	CreatedAt   time.Time    `json:"created_at"`
	Account     string       `json:"account"`
	From        string       `json:"from"`
	To          []string     `json:"to"`
	Cc          []string     `json:"cc,omitzero"`
	Bcc         []string     `json:"bcc,omitzero"`
	Subject     string       `json:"subject"`
	MessageID   string       `json:"message_id"`
	Size        int64        `json:"size"`
	SHA256      string       `json:"sha256"`
	Attachments []Attachment `json:"attachments"`
	State       State        `json:"state"`
	SentAt      time.Time    `json:"sent_at,omitzero"`
	SentAs      string       `json:"sent_as,omitzero"`
	ProviderID  string       `json:"provider_id,omitzero"`
	Error       string       `json:"error,omitzero"`
}

// Attachment is one file folded into the draft at compose time.
type Attachment struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// Compose is what the user asked to send.
type Compose struct {
	Account     string
	From        string // the authenticated address, or a send-as override
	To, Cc, Bcc []string
	Subject     string
	Body        string
	HTML        bool
	Attach      []string
}

// Store is a directory of drafts.
type Store struct{ Dir string }

// DefaultStore is under the state directory.
func DefaultStore() Store { return Store{Dir: filepath.Join(attachments.StateDir(), "drafts")} }

var emailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// Create builds the wire message and writes the draft. Nothing is sent.
func (s Store) Create(c Compose, now time.Time) (Record, *mail.Prepared, error) {
	if len(c.To) == 0 {
		return Record{}, nil, errors.New("at least one --to recipient is required")
	}
	for _, a := range append(append(append([]string{}, c.To...), c.Cc...), c.Bcc...) {
		if !emailRe.MatchString(a) {
			return Record{}, nil, fmt.Errorf("%q does not look like an email address", a)
		}
	}
	if strings.TrimSpace(c.Body) == "" {
		return Record{}, nil, errors.New("refusing to draft an empty body")
	}
	if c.From == "" {
		return Record{}, nil, mail.Wrap(c.Account, "draft", "mail-find auth login --account "+c.Account, mail.ErrAuth)
	}

	m := gomail.NewMsg(gomail.WithNoDefaultUserAgent())
	if err := m.From(c.From); err != nil {
		return Record{}, nil, err
	}
	if err := m.To(c.To...); err != nil {
		return Record{}, nil, err
	}
	if len(c.Cc) > 0 {
		if err := m.Cc(c.Cc...); err != nil {
			return Record{}, nil, err
		}
	}
	if len(c.Bcc) > 0 {
		// go-mail never writes Bcc into the message (SMTP envelope
		// semantics); the Gmail and Graph APIs take it from the header.
		m.SetGenHeader(gomail.Header("Bcc"), strings.Join(c.Bcc, ", "))
	}
	m.Subject(c.Subject)
	if c.HTML {
		m.SetBodyString(gomail.TypeTextHTML, c.Body)
	} else {
		m.SetBodyString(gomail.TypeTextPlain, c.Body)
	}
	var atts []Attachment
	for _, raw := range c.Attach {
		p, err := filepath.Abs(expand(raw))
		if err != nil {
			return Record{}, nil, err
		}
		st, err := os.Stat(p)
		if err != nil || !st.Mode().IsRegular() {
			return Record{}, nil, fmt.Errorf("attachment not found: %s", p)
		}
		m.AttachFile(p, gomail.WithFileName(filepath.Base(p)))
		atts = append(atts, Attachment{Name: filepath.Base(p), Path: p, Size: st.Size()})
	}
	id := draftID(now, c.Subject)
	msgID := fmt.Sprintf("<%s@mailkit>", id)
	m.SetMessageIDWithValue(msgID)

	var buf bytes.Buffer
	if _, err := m.WriteTo(&buf); err != nil {
		return Record{}, nil, err
	}
	prepared, err := mail.NewPrepared(&buf)
	if err != nil {
		return Record{}, nil, err
	}
	rec := Record{
		ID: id, CreatedAt: now, Account: c.Account, From: c.From,
		To: c.To, Cc: c.Cc, Bcc: c.Bcc, Subject: c.Subject, MessageID: msgID,
		Size: prepared.Size(), SHA256: prepared.Digest(), Attachments: atts, State: Pending,
	}
	if rec.Attachments == nil {
		rec.Attachments = []Attachment{}
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return Record{}, nil, err
	}
	if err := os.WriteFile(s.emlPath(id), prepared.Bytes(), 0o600); err != nil {
		return Record{}, nil, err
	}
	if err := s.write(rec); err != nil {
		return Record{}, nil, err
	}
	return rec, prepared, nil
}

func expand(p string) string {
	if after, ok := strings.CutPrefix(p, "~/"); ok {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, after)
	}
	return p
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// draftID is readable -- the model types it back -- and collision-free
// within a second.
func draftID(now time.Time, subject string) string {
	slug := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(subject), "-"), "-")
	if slug == "" {
		slug = "no-subject"
	}
	if len(slug) > 40 {
		slug = slug[:40]
	}
	var b [2]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%s-%s-%s", now.Format("20060102-150405"), slug, hex.EncodeToString(b[:]))
}

func (s Store) emlPath(id string) string  { return filepath.Join(s.Dir, id+".eml") }
func (s Store) jsonPath(id string) string { return filepath.Join(s.Dir, id+".json") }

// PreviewPath is where the HTML preview of a draft lives.
func (s Store) PreviewPath(id string) string { return filepath.Join(s.Dir, id+".html") }

func (s Store) write(r Record) error {
	b, err := json.Marshal(r, json.Deterministic(true))
	if err != nil {
		return err
	}
	tmp := s.jsonPath(r.ID) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.jsonPath(r.ID))
}

// Load reads a draft's record.
func (s Store) Load(id string) (Record, error) {
	b, err := os.ReadFile(s.jsonPath(id))
	if err != nil {
		return Record{}, fmt.Errorf("no such draft: %s", id)
	}
	var r Record
	if err := json.Unmarshal(b, &r); err != nil {
		return Record{}, fmt.Errorf("draft %s is unreadable: %w", id, err)
	}
	return r, nil
}

// Open returns the prepared bytes, verified against the pinned digest.
func (s Store) Open(r Record) (*mail.Prepared, error) {
	fh, err := os.Open(s.emlPath(r.ID))
	if err != nil {
		return nil, fmt.Errorf("draft %s has no message file; re-draft", r.ID)
	}
	defer fh.Close()
	p, err := mail.NewPrepared(fh)
	if err != nil {
		return nil, err
	}
	if p.Digest() != r.SHA256 {
		return nil, fmt.Errorf("draft %s changed since its preview -- the bytes no longer match what was reviewed; re-draft", r.ID)
	}
	return p, nil
}

// Claim moves a pending draft to sending. The record's state is the
// single-use token; the lock only serialises the read-modify-write so two
// concurrent commits cannot both see "pending". It is a kernel advisory
// lock on a file that stays on disk: the kernel releases it when the holder
// exits, so a process that dies mid-transition cannot hold it, and a live
// process that merely stalled still owns it -- age says nothing. A process
// that dies after claiming leaves the record in sending, and the next claim
// reports that as an unknown outcome rather than retrying it.
func (s Store) Claim(id string) (Record, error) {
	fh, err := os.OpenFile(filepath.Join(s.Dir, id+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return Record{}, err
	}
	defer fh.Close()
	if err := syscall.Flock(int(fh.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return Record{}, fmt.Errorf("draft %s is being sent by another process right now", id)
	}
	defer syscall.Flock(int(fh.Fd()), syscall.LOCK_UN)
	r, err := s.Load(id)
	if err != nil {
		return Record{}, err
	}
	switch r.State {
	case Pending:
	case Sent:
		return Record{}, fmt.Errorf("draft %s already sent at %s; drafts are single-use", id, r.SentAt.Format(time.RFC3339))
	case Sending, Unknown:
		// Sending with no process holding the lock is a send whose outcome
		// was never recorded: the same thing as unknown.
		return Record{}, fmt.Errorf("draft %s has an unknown outcome: the provider may have accepted it. Before re-drafting, check sent mail (the Message-ID is rewritten on send, so search by subject):\n    mail-find search 'subject:%q newer_than:1d' --account %s", id, r.Subject, r.Account)
	default:
		return Record{}, fmt.Errorf("draft %s is %s; drafts are single-use", id, r.State)
	}
	r.State = Sending
	if err := s.write(r); err != nil {
		return Record{}, err
	}
	return r, nil
}

// Release returns a claimed draft to pending: nothing was transmitted, so
// the draft can be committed again once the reason is fixed.
func (s Store) Release(r Record, reason error) error {
	r.State, r.Error = Pending, reason.Error()
	return s.write(r)
}

// Finish records the outcome of a claimed send. A provider that refused the
// message before accepting any of it (too large, not authenticated) leaves
// the draft pending; any other failure is an unknown outcome, because the
// bytes may have left.
func (s Store) Finish(r Record, providerID, sentAs string, sendErr error, now time.Time) error {
	switch {
	case sendErr == nil:
		r.State, r.SentAt, r.SentAs, r.ProviderID = Sent, now, sentAs, providerID
	case errors.Is(sendErr, mail.ErrTooLarge) || errors.Is(sendErr, mail.ErrAuth):
		return s.Release(r, sendErr)
	default:
		r.State, r.Error = Unknown, sendErr.Error()
	}
	return s.write(r)
}

// Recent lists drafts, newest first.
func (s Store) Recent(limit int) ([]Record, error) {
	entries, err := filepath.Glob(filepath.Join(s.Dir, "*.json"))
	if err != nil {
		return nil, err
	}
	sort.Sort(sort.Reverse(sort.StringSlice(entries)))
	var out []Record
	for _, p := range entries {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var r Record
		if json.Unmarshal(b, &r) == nil {
			out = append(out, r)
		}
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

// Parsed is the prepared message read back for the preview: what will
// actually go out, not what the flags said.
type Parsed struct {
	From, To, Cc, Bcc, Subject, Date string
	BodyText                         string
	BodyHTML                         string
	Attachments                      []Attachment
}

// Parse reads the wire message. The preview renders from this so the page
// cannot disagree with the bytes.
func Parse(p *mail.Prepared) (Parsed, error) {
	m, err := netmail.ReadMessage(p.Reader())
	if err != nil {
		return Parsed{}, err
	}
	dec := new(mime.WordDecoder)
	hdr := func(k string) string {
		v, err := dec.DecodeHeader(m.Header.Get(k))
		if err != nil {
			return m.Header.Get(k)
		}
		return v
	}
	out := Parsed{From: hdr("From"), To: hdr("To"), Cc: hdr("Cc"), Bcc: hdr("Bcc"), Subject: hdr("Subject"), Date: hdr("Date")}
	if err := walk(m.Header.Get("Content-Type"), m.Header.Get("Content-Transfer-Encoding"), m.Body, &out); err != nil {
		return Parsed{}, err
	}
	return out, nil
}

func walk(ctype, cte string, body io.Reader, out *Parsed) error {
	mt, params, err := mime.ParseMediaType(ctype)
	if err != nil {
		mt = "text/plain"
	}
	if strings.HasPrefix(mt, "multipart/") {
		mr := multipart.NewReader(body, params["boundary"])
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}
			if err := walk(part.Header.Get("Content-Type"), part.Header.Get("Content-Transfer-Encoding"), part, out); err != nil {
				return err
			}
		}
	}
	b, err := io.ReadAll(decode(cte, body))
	if err != nil {
		return err
	}
	if name := params["name"]; name != "" || !strings.HasPrefix(mt, "text/") {
		out.Attachments = append(out.Attachments, Attachment{Name: name, Size: int64(len(b))})
		return nil
	}
	if mt == "text/html" {
		out.BodyHTML = string(b)
	} else {
		out.BodyText = string(b)
	}
	return nil
}
