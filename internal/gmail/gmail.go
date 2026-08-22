// Package gmail is the Mailbox adapter for Gmail, over the official Go SDK.
//
// Everything Gmail-shaped lives here: the q= syntax, the MIME part tree,
// base64url, attachmentId keying, threadId. Nothing above this package
// knows any of it.
package gmail

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"
	gm "google.golang.org/api/gmail/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"

	"github.com/qiushiyan/mailkit/internal/mail"
)

const (
	providerName = "gmail"
	// Gmail caps a message at 25 MB including MIME overhead.
	sendLimit = 25 << 20
	// fanOut is the parallelism of the per-hit metadata fetch.
	fanOut = 8
)

// Mailbox is a Gmail account.
type Mailbox struct {
	svc *gm.Service
	// LoginHint is the command that recovers from ErrAuth.
	LoginHint string
}

// New builds a Mailbox over an authenticated HTTP client. opts may add an
// endpoint (tests point it at a cassette server).
func New(ctx context.Context, client *http.Client, opts ...option.ClientOption) (*Mailbox, error) {
	svc, err := gm.NewService(ctx, append([]option.ClientOption{option.WithHTTPClient(client)}, opts...)...)
	if err != nil {
		return nil, err
	}
	return &Mailbox{svc: svc, LoginHint: LoginHint}, nil
}

// wrap labels an error at the public boundary, after mapping Google's
// status codes to the port's sentinels.
func (m *Mailbox) wrap(op string, err error) error {
	if ge, ok := errors.AsType[*googleapi.Error](err); ok {
		if base := mail.Sentinel(ge.Code); base != nil {
			err = base
		}
	}
	return mail.Wrap(providerName, op, m.LoginHint, err)
}

func (m *Mailbox) Account(ctx context.Context) (mail.Account, error) {
	p, err := m.svc.Users.GetProfile("me").Context(ctx).Do()
	if err != nil {
		return mail.Account{}, m.wrap("account", err)
	}
	return mail.Account{Address: p.EmailAddress, SendLimit: sendLimit}, nil
}

func (m *Mailbox) Resolve(ctx context.Context, id mail.MessageID) (mail.Envelope, error) {
	id = mail.MessageID(strings.Trim(string(id), "<> "))
	hits, err := m.listIDs(ctx, "rfc822msgid:"+string(id), 1)
	if err != nil {
		return mail.Envelope{}, m.wrap("resolve", err)
	}
	if len(hits) == 0 {
		return mail.Envelope{}, m.wrap("resolve", fmt.Errorf("message-id %s: %w", id, mail.ErrNotFound))
	}
	msg, err := m.svc.Users.Messages.Get("me", hits[0]).Format("metadata").Context(ctx).Do()
	if err != nil {
		return mail.Envelope{}, m.wrap("resolve", err)
	}
	return envelope(msg), nil
}

// compile turns Criteria into Gmail's own query syntax. Every field is
// expressed, as the coarse request; Search narrows the fields whose Gmail
// meaning is looser than the port's.
func compile(c mail.Criteria) string {
	var parts []string
	for _, p := range c.Phrases {
		parts = append(parts, fmt.Sprintf("%q", p))
	}
	if len(c.SubjectTerms) > 0 {
		terms := make([]string, 0, len(c.SubjectTerms))
		for _, t := range c.SubjectTerms {
			if strings.ContainsAny(t, " \t") {
				t = fmt.Sprintf("%q", t)
			}
			terms = append(terms, t)
		}
		parts = append(parts, "subject:("+strings.Join(terms, " OR ")+")")
	}
	if c.From != "" {
		parts = append(parts, "from:"+c.From)
	}
	if c.To != "" {
		parts = append(parts, "to:"+c.To)
	}
	if c.HasAttachment {
		parts = append(parts, "has:attachment")
	}
	if !c.After.IsZero() {
		parts = append(parts, "after:"+c.After.Format("2006/01/02"))
	}
	if !c.Before.IsZero() {
		// Gmail's before: is exclusive of the day; round up so a bound at
		// 13:00 still admits that day's earlier mail.
		parts = append(parts, "before:"+c.Before.Add(24*time.Hour).Format("2006/01/02"))
	}
	return strings.Join(parts, " ")
}

// Search compiles the criteria to one Gmail query and narrows the listing
// with the port's walk, fetching one metadata envelope per hit in parallel
// -- Gmail's list returns bare ids. Gmail's operators are coarser than the
// port in two ways the envelope corrects: date operators are day-granular,
// and from:/to: also match display names. has:attachment is left to Gmail
// because a metadata envelope cannot see parts.
func (m *Mailbox) Search(ctx context.Context, c mail.Criteria, limit int) ([]mail.Envelope, error) {
	residual := c
	residual.HasAttachment = false
	q := compile(c)
	page := ""
	first := true
	hits, err := mail.Narrow(limit, residual, func() ([]mail.Envelope, bool, error) {
		if !first && page == "" {
			return nil, false, nil
		}
		first = false
		call := m.svc.Users.Messages.List("me").Q(q).MaxResults(int64(min(max(limit, mail.DefaultLimit), 500))).Context(ctx)
		if page != "" {
			call = call.PageToken(page)
		}
		resp, err := call.Do()
		if err != nil {
			return nil, false, err
		}
		ids := make([]string, 0, len(resp.Messages))
		for _, r := range resp.Messages {
			ids = append(ids, r.Id)
		}
		envs, err := m.envelopes(ctx, ids)
		page = resp.NextPageToken
		return envs, page != "", err
	})
	if err != nil {
		return nil, m.wrap("search", err)
	}
	return hits, nil
}

// listIDs returns up to limit ids for a query in Gmail's own semantics --
// Resolve and NativeSearch, where the provider's answer is the answer.
func (m *Mailbox) listIDs(ctx context.Context, q string, limit int) ([]string, error) {
	var ids []string
	page := ""
	for {
		call := m.svc.Users.Messages.List("me").Q(q).MaxResults(int64(min(max(limit, 1), 500))).Context(ctx)
		if page != "" {
			call = call.PageToken(page)
		}
		resp, err := call.Do()
		if err != nil {
			return nil, err
		}
		for _, r := range resp.Messages {
			ids = append(ids, r.Id)
			if len(ids) >= limit {
				return ids, nil
			}
		}
		if resp.NextPageToken == "" {
			return ids, nil
		}
		page = resp.NextPageToken
	}
}

// envelopes fetches metadata for ids in parallel, in order.
func (m *Mailbox) envelopes(ctx context.Context, ids []string) ([]mail.Envelope, error) {
	out := make([]mail.Envelope, len(ids))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(fanOut)
	for i, id := range ids {
		g.Go(func() error {
			msg, err := m.svc.Users.Messages.Get("me", id).Format("metadata").Context(gctx).Do()
			if err != nil {
				return err
			}
			out[i] = envelope(msg)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return out, nil
}

func (m *Mailbox) Fetch(ctx context.Context, id string) (mail.Message, error) {
	msg, err := m.svc.Users.Messages.Get("me", id).Format("full").Context(ctx).Do()
	if err != nil {
		return mail.Message{}, m.wrap("fetch", err)
	}
	return message(msg), nil
}

func (m *Mailbox) Conversation(ctx context.Context, convID string) ([]mail.Message, error) {
	t, err := m.svc.Users.Threads.Get("me", convID).Format("full").Context(ctx).Do()
	if err != nil {
		return nil, m.wrap("conversation", err)
	}
	out := make([]mail.Message, 0, len(t.Messages))
	for _, msg := range t.Messages {
		out = append(out, message(msg))
	}
	// threads.get already returns ascending; sort defensively anyway.
	mail.SortByReceived(out)
	return out, nil
}

// A handle is "<messageId>/<attachmentId>": Gmail scopes attachment ids
// to their message.
func (m *Mailbox) Open(ctx context.Context, h mail.Handle, w io.Writer) error {
	msgID, attID, ok := strings.Cut(string(h), "/")
	if !ok {
		return m.wrap("open", fmt.Errorf("handle %q: %w", h, mail.ErrNoBytes))
	}
	body, err := m.svc.Users.Messages.Attachments.Get("me", msgID, attID).Context(ctx).Do()
	if err != nil {
		return m.wrap("open", err)
	}
	raw, err := base64.URLEncoding.WithPadding(base64.NoPadding).DecodeString(strings.TrimRight(body.Data, "="))
	if err != nil {
		return m.wrap("open", err)
	}
	_, err = w.Write(raw)
	return err
}

func (m *Mailbox) Send(ctx context.Context, p *mail.Prepared) (string, error) {
	if p.Size() > sendLimit {
		return "", m.wrap("send", mail.ErrTooLarge)
	}
	sent, err := m.svc.Users.Messages.Send("me", &gm.Message{
		Raw: base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(p.Bytes()),
	}).Context(ctx).Do()
	if err != nil {
		return "", m.wrap("send", err)
	}
	return sent.Id, nil
}

// --- translation: Gmail JSON -> mail types ---------------------------------

func headers(p *gm.MessagePart) map[string]string {
	h := map[string]string{}
	if p == nil {
		return h
	}
	for _, hd := range p.Headers {
		h[strings.ToLower(hd.Name)] = hd.Value
	}
	return h
}

func envelope(msg *gm.Message) mail.Envelope {
	h := headers(msg.Payload)
	e := mail.Envelope{
		ID:             msg.Id,
		ConversationID: msg.ThreadId,
		MessageID:      mail.MessageID(strings.Trim(h["message-id"], "<> ")),
		DateHeader:     h["date"],
		From:           mail.ParseAddress(h["from"]),
		To:             mail.ParseAddressList(h["to"]),
		Cc:             mail.ParseAddressList(h["cc"]),
		Subject:        h["subject"],
		Snippet:        msg.Snippet,
	}
	if msg.InternalDate > 0 {
		e.Received = time.UnixMilli(msg.InternalDate).UTC()
	} else if t, err := mail.ParseDate(h["date"]); err == nil {
		e.Received = t
	}
	for _, p := range walk(msg.Payload) {
		if p.Body != nil && p.Body.AttachmentId != "" && p.Filename != "" {
			e.HasAttachments = true
			break
		}
	}
	return e
}

// walk is depth-first in document order, so "the first attachment" means
// the first one in the message.
func walk(p *gm.MessagePart) []*gm.MessagePart {
	if p == nil {
		return nil
	}
	out := []*gm.MessagePart{p}
	for _, c := range p.Parts {
		out = append(out, walk(c)...)
	}
	return out
}

func decodeBody(data string) string {
	if data == "" {
		return ""
	}
	raw, err := base64.URLEncoding.WithPadding(base64.NoPadding).DecodeString(strings.TrimRight(data, "="))
	if err != nil {
		return ""
	}
	return string(bytes.ToValidUTF8(raw, []byte("�")))
}

func message(msg *gm.Message) mail.Message {
	out := mail.Message{Envelope: envelope(msg)}
	for i, p := range walk(msg.Payload) {
		mt := strings.ToLower(p.MimeType)
		if p.Body != nil && p.Body.AttachmentId != "" {
			// A part is an attachment if it has an attachmentId -- not if it
			// has a filename. Images embedded by Content-ID often carry no
			// filename, and keying on filename silently dropped them.
			ph := headers(p)
			cid := strings.Trim(ph["content-id"], "<> ")
			name := p.Filename
			if name == "" {
				ext := ".bin"
				if exts, _ := mime.ExtensionsByType(strings.Split(mt, ";")[0]); len(exts) > 0 {
					ext = exts[0]
				}
				base := cid
				if base == "" {
					base = fmt.Sprintf("inline-%d", i)
				}
				name = base + ext
			}
			out.Parts = append(out.Parts, mail.Part{
				Name:      name,
				MIME:      p.MimeType,
				Size:      p.Body.Size,
				Inline:    cid != "" || strings.Contains(strings.ToLower(ph["content-disposition"]), "inline"),
				ContentID: cid,
				Content:   mail.StoredPart{Handle: mail.Handle(msg.Id + "/" + p.Body.AttachmentId)},
			})
			continue
		}
		if p.Body == nil || p.Body.Data == "" {
			continue
		}
		switch {
		case mt == "text/html" && out.Body.HTML == "":
			out.Body.HTML = decodeBody(p.Body.Data)
		case mt == "text/plain" && out.Body.Text == "":
			out.Body.Text = decodeBody(p.Body.Data)
		}
	}
	out.HasAttachments = mail.HasAttachments(out.Parts)
	return out
}

// Translate exposes the Gmail JSON -> Message translation so tests can
// round-trip recorded API responses through the real adapter.
func Translate(msg *gm.Message) mail.Message { return message(msg) }

// NativeSearch passes a query in Gmail's own syntax through untouched. It
// is the --native escape hatch, provider-specific by definition.
func (m *Mailbox) NativeSearch(ctx context.Context, q string, limit int) ([]mail.Envelope, error) {
	if limit <= 0 {
		limit = 25
	}
	ids, err := m.listIDs(ctx, q, limit)
	if err != nil {
		return nil, m.wrap("search", err)
	}
	out := make([]mail.Envelope, len(ids))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(fanOut)
	for i, id := range ids {
		g.Go(func() error {
			msg, err := m.svc.Users.Messages.Get("me", id).Format("metadata").Context(gctx).Do()
			if err != nil {
				return err
			}
			out[i] = envelope(msg)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, m.wrap("search", err)
	}
	return out, nil
}

// RawMessage returns the API's own JSON for a message (format=full), for
// recording fixtures from the real writer.
func (m *Mailbox) RawMessage(ctx context.Context, id string) (*gm.Message, error) {
	msg, err := m.svc.Users.Messages.Get("me", id).Format("full").Context(ctx).Do()
	if err != nil {
		return nil, m.wrap("fetch", err)
	}
	return msg, nil
}

// RawThread returns the API's own JSON for a thread (format=full).
func (m *Mailbox) RawThread(ctx context.Context, id string) (*gm.Thread, error) {
	t, err := m.svc.Users.Threads.Get("me", id).Format("full").Context(ctx).Do()
	if err != nil {
		return nil, m.wrap("conversation", err)
	}
	return t, nil
}
