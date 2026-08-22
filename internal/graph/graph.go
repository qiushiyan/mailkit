// Package graph is the Mailbox adapter for Microsoft 365 over the Graph
// REST API, with azidentity for tokens.
//
// It is hand-typed over net/http rather than the generated SDK for one
// concrete reason: sending the prepared RFC 5322 bytes needs the MIME form
// of /me/sendMail (Content-Type: text/plain, base64 body), which the SDK's
// models do not express, and the gate's promise is that those bytes go out
// unchanged. Five endpoints are small enough to type by hand.
//
// Graph differs from Gmail in three ways this package absorbs: $search and
// $filter cannot appear in one request, so a query is compiled to one and
// the rest is narrowed locally over pages; attachments come in three kinds,
// two of which hold no bytes; and the API offers uniqueBody, its own quote
// folding, which is passed up as a hint.
//
// Unverified against a live mailbox (tenant consent pending). Every shape
// here comes from the Graph reference; the recording run replaces them.
package graph

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/qiushiyan/mailkit/internal/mail"
)

const (
	providerName = "outlook"
	// Graph base64-inlines attachments into the send request; the request
	// ceiling is 4 MB, so the prepared message must fit under it with the
	// base64 expansion.
	sendLimit = 3 << 20
	// pageSize for coarse requests that are narrowed locally.
	pageSize = 50
	// maxPages bounds a narrowing loop that never finds enough.
	maxPages = 8
	// embedDepth bounds recursion into itemAttachments.
	embedDepth = 2
)

// Mailbox is a Microsoft 365 account.
type Mailbox struct {
	Client  *http.Client // must add Authorization
	BaseURL string       // https://graph.microsoft.com/v1.0
	// LoginHint is the command that recovers from ErrAuth.
	LoginHint string
}

// New builds a Mailbox over a client that already authenticates.
func New(client *http.Client) *Mailbox {
	return &Mailbox{Client: client, BaseURL: "https://graph.microsoft.com/v1.0", LoginHint: "mail-find auth login --account outlook"}
}

type odataError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (m *Mailbox) do(ctx context.Context, op, method, path string, body io.Reader, contentType string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, m.BaseURL+path, body)
	if err != nil {
		return &mail.ProviderError{Provider: providerName, Op: op, Err: err}
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := m.Client.Do(req)
	if err != nil {
		if errors.Is(err, mail.ErrAuth) {
			return &mail.ProviderError{Provider: providerName, Op: op, Err: mail.ErrAuth, Hint: m.LoginHint}
		}
		return &mail.ProviderError{Provider: providerName, Op: op, Err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		var oe odataError
		_ = json.Unmarshal(raw, &oe)
		detail := oe.Error.Message
		if detail == "" {
			detail = strings.TrimSpace(string(raw))
		}
		switch resp.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return &mail.ProviderError{Provider: providerName, Op: op, Err: fmt.Errorf("%w: %s", mail.ErrAuth, detail), Hint: m.LoginHint}
		case http.StatusNotFound:
			return &mail.ProviderError{Provider: providerName, Op: op, Err: fmt.Errorf("%w: %s", mail.ErrNotFound, detail)}
		case http.StatusRequestEntityTooLarge:
			return &mail.ProviderError{Provider: providerName, Op: op, Err: fmt.Errorf("%w: %s", mail.ErrTooLarge, detail)}
		}
		return &mail.ProviderError{Provider: providerName, Op: op, Err: fmt.Errorf("HTTP %d %s: %s", resp.StatusCode, oe.Error.Code, detail)}
	}
	if out == nil {
		return nil
	}
	if w, ok := out.(io.Writer); ok {
		_, err := io.Copy(w, resp.Body)
		return err
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return &mail.ProviderError{Provider: providerName, Op: op, Err: err}
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return &mail.ProviderError{Provider: providerName, Op: op, Err: fmt.Errorf("decoding response: %w", err)}
	}
	return nil
}

// --- wire shapes, hand-typed from the Graph reference ---------------------

type recipient struct {
	EmailAddress struct {
		Name    string `json:"name"`
		Address string `json:"address"`
	} `json:"emailAddress"`
}

type itemBody struct {
	ContentType string `json:"contentType"`
	Content     string `json:"content"`
}

type message struct {
	ID                string       `json:"id"`
	InternetMessageID string       `json:"internetMessageId"`
	ConversationID    string       `json:"conversationId"`
	ReceivedDateTime  string       `json:"receivedDateTime"`
	SentDateTime      string       `json:"sentDateTime"`
	Subject           string       `json:"subject"`
	BodyPreview       string       `json:"bodyPreview"`
	HasAttachments    bool         `json:"hasAttachments"`
	From              *recipient   `json:"from"`
	ToRecipients      []recipient  `json:"toRecipients"`
	CcRecipients      []recipient  `json:"ccRecipients"`
	Body              *itemBody    `json:"body"`
	UniqueBody        *itemBody    `json:"uniqueBody"`
	Attachments       []attachment `json:"attachments"`
}

type attachment struct {
	ODataType   string   `json:"@odata.type"`
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	ContentType string   `json:"contentType"`
	Size        int64    `json:"size"`
	IsInline    bool     `json:"isInline"`
	ContentID   string   `json:"contentId"`
	SourceURL   string   `json:"sourceUrl"` // referenceAttachment
	Item        *message `json:"item"`      // itemAttachment, when expanded
}

type listing struct {
	Value    []message `json:"value"`
	NextLink string    `json:"@odata.nextLink"`
}

const selectFields = "id,internetMessageId,conversationId,receivedDateTime,sentDateTime,subject,from,toRecipients,ccRecipients,bodyPreview,hasAttachments"

// --- Mailbox ---------------------------------------------------------------

func (m *Mailbox) Account(ctx context.Context) (mail.Account, error) {
	var me struct {
		Mail              string `json:"mail"`
		UserPrincipalName string `json:"userPrincipalName"`
	}
	if err := m.do(ctx, "account", http.MethodGet, "/me?$select=mail,userPrincipalName", nil, "", &me); err != nil {
		return mail.Account{}, err
	}
	addr := me.Mail
	if addr == "" {
		addr = me.UserPrincipalName
	}
	return mail.Account{Address: addr, SendLimit: sendLimit}, nil
}

func (m *Mailbox) Resolve(ctx context.Context, id mail.MessageID) (mail.Envelope, error) {
	id = mail.MessageID(strings.Trim(string(id), "<> "))
	q := url.Values{}
	q.Set("$filter", fmt.Sprintf("internetMessageId eq '%s'", strings.ReplaceAll("<"+string(id)+">", "'", "''")))
	q.Set("$select", selectFields)
	q.Set("$top", "1")
	var l listing
	if err := m.do(ctx, "resolve", http.MethodGet, "/me/messages?"+q.Encode(), nil, "", &l); err != nil {
		return mail.Envelope{}, err
	}
	if len(l.Value) == 0 {
		return mail.Envelope{}, &mail.ProviderError{Provider: providerName, Op: "resolve", Err: fmt.Errorf("message-id %s: %w", id, mail.ErrNotFound)}
	}
	return envelope(l.Value[0]), nil
}

// plan splits Criteria into what Graph is asked and what is narrowed here.
// Phrases and subject terms need $search; dates, attachment presence and an
// exact address can go in $filter; the two cannot be combined, so when both
// exist $search carries the coarse request and the filterable fields are
// applied locally. A bare domain can never be filtered server-side.
type plan struct {
	query url.Values
	local mail.Criteria // fields Match must still check
}

func compile(c mail.Criteria) plan {
	q := url.Values{}
	q.Set("$select", selectFields)
	var search []string
	for _, p := range c.Phrases {
		search = append(search, fmt.Sprintf("%q", p))
	}
	if len(c.SubjectTerms) > 0 {
		terms := make([]string, 0, len(c.SubjectTerms))
		for _, t := range c.SubjectTerms {
			terms = append(terms, fmt.Sprintf("subject:%q", t))
		}
		search = append(search, "("+strings.Join(terms, " OR ")+")")
	}
	p := plan{query: q}
	if len(search) > 0 {
		q.Set("$search", strings.Join(search, " AND "))
		// Everything else narrows locally.
		p.local = mail.Criteria{From: c.From, To: c.To, HasAttachment: c.HasAttachment, After: c.After, Before: c.Before}
		return p
	}
	var filters []string
	if !c.After.IsZero() {
		filters = append(filters, "receivedDateTime ge "+c.After.UTC().Format(time.RFC3339))
	}
	if !c.Before.IsZero() {
		filters = append(filters, "receivedDateTime lt "+c.Before.UTC().Format(time.RFC3339))
	}
	if c.HasAttachment {
		filters = append(filters, "hasAttachments eq true")
	}
	if c.From != "" && strings.Contains(c.From, "@") {
		filters = append(filters, fmt.Sprintf("from/emailAddress/address eq '%s'", strings.ReplaceAll(c.From, "'", "''")))
	} else if c.From != "" {
		p.local.From = c.From
	}
	if c.To != "" {
		p.local.To = c.To // recipient filtering needs a lambda; narrow locally
	}
	if len(filters) > 0 {
		q.Set("$filter", strings.Join(filters, " and "))
		q.Set("$orderby", "receivedDateTime desc")
	}
	return p
}

// Search pages the coarse request and narrows locally until it has limit
// exact matches or the pages run out.
func (m *Mailbox) Search(ctx context.Context, c mail.Criteria, limit int) ([]mail.Envelope, error) {
	if limit <= 0 {
		limit = 25
	}
	p := compile(c)
	p.query.Set("$top", strconv.Itoa(pageSize))
	path := "/me/messages?" + p.query.Encode()
	var out []mail.Envelope
	for page := 0; page < maxPages && path != ""; page++ {
		var l listing
		if err := m.do(ctx, "search", http.MethodGet, path, nil, "", &l); err != nil {
			return nil, err
		}
		for _, msg := range l.Value {
			e := envelope(msg)
			if !p.local.IsZero() && !p.local.Match(e, "") {
				continue
			}
			out = append(out, e)
			if len(out) >= limit {
				return out, nil
			}
		}
		path = strings.TrimPrefix(l.NextLink, m.BaseURL)
		if l.NextLink != "" && path == l.NextLink {
			// Absolute link on another host: follow it as given.
			return m.followAbsolute(ctx, l.NextLink, p, out, limit, page+1)
		}
	}
	return out, nil
}

func (m *Mailbox) followAbsolute(ctx context.Context, link string, p plan, out []mail.Envelope, limit, page int) ([]mail.Envelope, error) {
	saved := m.BaseURL
	defer func() { m.BaseURL = saved }()
	m.BaseURL = ""
	for ; page < maxPages && link != ""; page++ {
		var l listing
		if err := m.do(ctx, "search", http.MethodGet, link, nil, "", &l); err != nil {
			return nil, err
		}
		for _, msg := range l.Value {
			e := envelope(msg)
			if !p.local.IsZero() && !p.local.Match(e, "") {
				continue
			}
			out = append(out, e)
			if len(out) >= limit {
				return out, nil
			}
		}
		link = l.NextLink
	}
	return out, nil
}

func (m *Mailbox) Fetch(ctx context.Context, id string) (mail.Message, error) {
	return m.fetch(ctx, "fetch", id, embedDepth)
}

func (m *Mailbox) fetch(ctx context.Context, op, id string, depth int) (mail.Message, error) {
	q := url.Values{}
	q.Set("$select", selectFields+",body,uniqueBody")
	q.Set("$expand", "attachments($select=id,name,contentType,size,isInline,contentId)")
	var msg message
	if err := m.do(ctx, op, http.MethodGet, "/me/messages/"+url.PathEscape(id)+"?"+q.Encode(), nil, "", &msg); err != nil {
		return mail.Message{}, err
	}
	out, err := translate(msg)
	if err != nil {
		return mail.Message{}, &mail.ProviderError{Provider: providerName, Op: op, Err: err}
	}
	// itemAttachments need a second request each to expand the item.
	for i, p := range out.Parts {
		if _, ok := p.Content.(mail.EmbeddedPart); !ok {
			continue
		}
		if depth <= 0 {
			out.Parts[i].Content = mail.EmbeddedPart{Truncated: true}
			continue
		}
		var a attachment
		path := "/me/messages/" + url.PathEscape(id) + "/attachments/" + url.PathEscape(msg.Attachments[i].ID) + "?$expand=microsoft.graph.itemattachment/item"
		if err := m.do(ctx, op, http.MethodGet, path, nil, "", &a); err != nil {
			return mail.Message{}, err
		}
		if a.Item == nil {
			out.Parts[i].Content = mail.EmbeddedPart{Truncated: true}
			continue
		}
		inner, err := translate(*a.Item)
		if err != nil {
			return mail.Message{}, &mail.ProviderError{Provider: providerName, Op: op, Err: err}
		}
		out.Parts[i].Content = mail.EmbeddedPart{Item: &inner, Truncated: hasEmbedded(inner) && depth-1 <= 0}
	}
	return out, nil
}

func hasEmbedded(m mail.Message) bool {
	for _, p := range m.Parts {
		if _, ok := p.Content.(mail.EmbeddedPart); ok {
			return true
		}
	}
	return false
}

func (m *Mailbox) Conversation(ctx context.Context, convID string) ([]mail.Message, error) {
	q := url.Values{}
	q.Set("$filter", fmt.Sprintf("conversationId eq '%s'", strings.ReplaceAll(convID, "'", "''")))
	q.Set("$select", selectFields+",body,uniqueBody")
	q.Set("$expand", "attachments($select=id,name,contentType,size,isInline,contentId)")
	q.Set("$orderby", "receivedDateTime")
	q.Set("$top", strconv.Itoa(pageSize))
	path := "/me/messages?" + q.Encode()
	var out []mail.Message
	for page := 0; page < maxPages && path != ""; page++ {
		var l listing
		if err := m.do(ctx, "conversation", http.MethodGet, path, nil, "", &l); err != nil {
			return nil, err
		}
		for _, msg := range l.Value {
			t, err := translate(msg)
			if err != nil {
				return nil, &mail.ProviderError{Provider: providerName, Op: "conversation", Err: err}
			}
			out = append(out, t)
		}
		path = strings.TrimPrefix(l.NextLink, m.BaseURL)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Received.Before(out[j-1].Received); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out, nil
}

// A handle is "<messageId>/<attachmentId>".
func (m *Mailbox) Open(ctx context.Context, h mail.Handle, w io.Writer) error {
	msgID, attID, ok := strings.Cut(string(h), "/")
	if !ok {
		return &mail.ProviderError{Provider: providerName, Op: "open", Err: fmt.Errorf("handle %q: %w", h, mail.ErrNoBytes)}
	}
	path := "/me/messages/" + url.PathEscape(msgID) + "/attachments/" + url.PathEscape(attID) + "/$value"
	return m.do(ctx, "open", http.MethodGet, path, nil, "", w)
}

// Send posts the prepared message in MIME form.
func (m *Mailbox) Send(ctx context.Context, p *mail.Prepared) (string, error) {
	if p.Size() > sendLimit {
		return "", &mail.ProviderError{Provider: providerName, Op: "send", Err: mail.ErrTooLarge}
	}
	body := base64.StdEncoding.EncodeToString(p.Bytes())
	if err := m.do(ctx, "send", http.MethodPost, "/me/sendMail", bytes.NewBufferString(body), "text/plain", nil); err != nil {
		return "", err
	}
	// sendMail returns 202 with no body; the Message-ID we set is the handle.
	return p.Header("Message-ID"), nil
}

// --- translation ----------------------------------------------------------

func addr(r recipient) mail.Address {
	return mail.Address{Name: r.EmailAddress.Name, Email: r.EmailAddress.Address}
}

func addrs(rs []recipient) []mail.Address {
	out := make([]mail.Address, 0, len(rs))
	for _, r := range rs {
		out = append(out, addr(r))
	}
	return out
}

func envelope(m message) mail.Envelope {
	e := mail.Envelope{
		ID:             m.ID,
		ConversationID: m.ConversationID,
		MessageID:      mail.MessageID(strings.Trim(m.InternetMessageID, "<> ")),
		DateHeader:     m.SentDateTime,
		To:             addrs(m.ToRecipients),
		Cc:             addrs(m.CcRecipients),
		Subject:        m.Subject,
		Snippet:        m.BodyPreview,
		HasAttachments: m.HasAttachments,
	}
	if m.From != nil {
		e.From = addr(*m.From)
	}
	if t, err := time.Parse(time.RFC3339Nano, m.ReceivedDateTime); err == nil {
		e.Received = t.UTC()
	} else if t, err := time.Parse(time.RFC3339Nano, m.SentDateTime); err == nil {
		e.Received = t.UTC()
	}
	if e.DateHeader == "" {
		e.DateHeader = m.ReceivedDateTime
	}
	return e
}

func body(b *itemBody) mail.Body {
	if b == nil {
		return mail.Body{}
	}
	if strings.EqualFold(b.ContentType, "html") {
		return mail.Body{HTML: b.Content}
	}
	return mail.Body{Text: b.Content}
}

func translate(m message) (mail.Message, error) {
	out := mail.Message{Envelope: envelope(m), Body: body(m.Body)}
	if m.UniqueBody != nil && m.UniqueBody.Content != "" {
		ub := body(m.UniqueBody)
		out.ProviderFolded = &ub
	}
	for i, a := range m.Attachments {
		p := mail.Part{Name: a.Name, MIME: a.ContentType, Size: a.Size, Inline: a.IsInline, ContentID: strings.Trim(a.ContentID, "<> ")}
		if p.Name == "" {
			p.Name = fmt.Sprintf("attachment-%d", i+1)
		}
		switch a.ODataType {
		case "#microsoft.graph.fileAttachment":
			p.Content = mail.StoredPart{Handle: mail.Handle(m.ID + "/" + a.ID)}
		case "#microsoft.graph.referenceAttachment":
			p.Content = mail.LinkedPart{URL: a.SourceURL}
		case "#microsoft.graph.itemAttachment":
			p.Content = mail.EmbeddedPart{}
		default:
			// An unknown kind must not quietly become "stored": the failure
			// this project already had once was a part reading as absent.
			return mail.Message{}, fmt.Errorf("attachment %q has unknown @odata.type %q", a.Name, a.ODataType)
		}
		out.Parts = append(out.Parts, p)
	}
	out.HasAttachments = len(out.Parts) > 0 || m.HasAttachments
	return out, nil
}

// RawMessage returns Graph's own JSON for a message with its attachments
// expanded, for recording fixtures from the real writer.
func (m *Mailbox) RawMessage(ctx context.Context, id string) (map[string]any, error) {
	q := url.Values{}
	q.Set("$select", selectFields+",body,uniqueBody")
	q.Set("$expand", "attachments($select=id,name,contentType,size,isInline,contentId)")
	var out map[string]any
	if err := m.do(ctx, "fetch", http.MethodGet, "/me/messages/"+url.PathEscape(id)+"?"+q.Encode(), nil, "", &out); err != nil {
		return nil, err
	}
	return out, nil
}
