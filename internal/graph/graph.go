// Package graph is the Mailbox adapter for Microsoft 365 over the Graph
// REST API, with azidentity for tokens.
//
// It is hand-typed over net/http rather than the generated SDK for one
// concrete reason: sending the prepared RFC 5322 bytes needs the MIME form
// of /me/sendMail (Content-Type: text/plain, base64 body), which the SDK's
// models do not express, and the gate's promise is that those bytes go out
// unchanged. Five endpoints are small enough to type by hand.
//
// Graph differs from Gmail in four ways this package absorbs: $search and
// $filter cannot appear in one request, so a query is compiled to one and
// the rest is narrowed locally over pages; attachments come in three kinds,
// two of which hold no bytes; the API offers uniqueBody, its own quote
// folding, which is passed up as a hint; and a reply joins its
// conversation only through Graph's own reply actions, which choose the
// recipients themselves (see replyAction).
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
	"maps"
	"net/http"
	"net/url"
	"slices"
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

// do performs one request and decodes the response into out (a JSON
// target, an io.Writer to stream into, or nil). Errors are plain; the
// public method that called labels them.
func (m *Mailbox) do(ctx context.Context, method, path string, body io.Reader, contentType string, out any) error {
	target := path
	if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
		target = m.BaseURL + path
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := m.Client.Do(req)
	if err != nil {
		return err
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
		if oe.Error.Code != "" {
			detail = oe.Error.Code + ": " + detail
		}
		return mail.StatusError(resp.StatusCode, detail)
	}
	switch out := out.(type) {
	case nil:
		return nil
	case io.Writer:
		_, err := io.Copy(out, resp.Body)
		return err
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}
	return nil
}

// wrap labels an error at the public boundary.
func (m *Mailbox) wrap(op string, err error) error {
	return mail.Wrap(providerName, op, m.LoginHint, err)
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
	ID                string      `json:"id"`
	InternetMessageID string      `json:"internetMessageId"`
	ConversationID    string      `json:"conversationId"`
	ReceivedDateTime  string      `json:"receivedDateTime"`
	SentDateTime      string      `json:"sentDateTime"`
	Subject           string      `json:"subject"`
	BodyPreview       string      `json:"bodyPreview"`
	HasAttachments    bool        `json:"hasAttachments"`
	From              *recipient  `json:"from"`
	ReplyTo           []recipient `json:"replyTo"`
	ToRecipients      []recipient `json:"toRecipients"`
	CcRecipients      []recipient `json:"ccRecipients"`
	// InternetMessageHeaders is returned only when selected, and only for
	// mail that came through transport -- a sent item may have none.
	InternetMessageHeaders []header     `json:"internetMessageHeaders"`
	Body                   *itemBody    `json:"body"`
	UniqueBody             *itemBody    `json:"uniqueBody"`
	Attachments            []attachment `json:"attachments"`
}

type header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
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

const selectFields = "id,internetMessageId,conversationId,receivedDateTime,sentDateTime,subject,from,replyTo,toRecipients,ccRecipients,bodyPreview,hasAttachments"

// fetchFields adds what one fetched message carries beyond a listing row:
// the bodies, and the transport headers a reply continues the chain from.
const fetchFields = selectFields + ",body,uniqueBody,internetMessageHeaders"

// --- Mailbox ---------------------------------------------------------------

func (m *Mailbox) Account(ctx context.Context) (mail.Account, error) {
	addr, err := m.address(ctx)
	if err != nil {
		return mail.Account{}, m.wrap("account", err)
	}
	return mail.Account{Address: addr, SendLimit: sendLimit}, nil
}

func (m *Mailbox) address(ctx context.Context) (string, error) {
	var me struct {
		Mail              string `json:"mail"`
		UserPrincipalName string `json:"userPrincipalName"`
	}
	if err := m.do(ctx, http.MethodGet, "/me?$select=mail,userPrincipalName", nil, "", &me); err != nil {
		return "", err
	}
	if me.Mail != "" {
		return me.Mail, nil
	}
	return me.UserPrincipalName, nil
}

func (m *Mailbox) Resolve(ctx context.Context, id mail.MessageID) (mail.Envelope, error) {
	id = mail.MessageID(strings.Trim(string(id), "<> "))
	q := url.Values{}
	q.Set("$filter", fmt.Sprintf("internetMessageId eq '%s'", strings.ReplaceAll("<"+string(id)+">", "'", "''")))
	q.Set("$select", selectFields)
	q.Set("$top", "1")
	var l listing
	if err := m.do(ctx, http.MethodGet, "/me/messages?"+q.Encode(), nil, "", &l); err != nil {
		return mail.Envelope{}, m.wrap("resolve", err)
	}
	if len(l.Value) == 0 {
		return mail.Envelope{}, m.wrap("resolve", fmt.Errorf("message-id %s: %w", id, mail.ErrNotFound))
	}
	return envelope(l.Value[0]), nil
}

// compile turns Criteria into the coarse Graph request. Phrases and
// subject terms need $search; dates, attachment presence and an exact
// address can go in $filter; the two cannot be combined, so when both
// exist $search carries the request. Whatever Graph was not asked, or
// answers more loosely than the port (KQL stems subject terms; a bare
// domain cannot be filtered), the caller narrows with the port's walk.
func compile(c mail.Criteria) url.Values {
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
	if len(search) > 0 {
		q.Set("$search", strings.Join(search, " AND "))
		return q
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
	}
	if len(filters) > 0 {
		q.Set("$filter", strings.Join(filters, " and "))
		q.Set("$orderby", "receivedDateTime desc")
	}
	return q
}

// Search pages the coarse request and narrows it with the port's walk.
func (m *Mailbox) Search(ctx context.Context, c mail.Criteria, limit int) ([]mail.Envelope, error) {
	q := compile(c)
	q.Set("$top", strconv.Itoa(pageSize))
	link := "/me/messages?" + q.Encode()
	hits, err := mail.Narrow(limit, c, func() ([]mail.Envelope, bool, error) {
		if link == "" {
			return nil, false, nil
		}
		var l listing
		if err := m.do(ctx, http.MethodGet, link, nil, "", &l); err != nil {
			return nil, false, err
		}
		page := make([]mail.Envelope, 0, len(l.Value))
		for _, msg := range l.Value {
			page = append(page, envelope(msg))
		}
		link = l.NextLink
		return page, link != "", nil
	})
	return hits, m.wrap("search", err)
}

func (m *Mailbox) Fetch(ctx context.Context, id string) (mail.Message, error) {
	msg, err := m.fetch(ctx, id)
	return msg, m.wrap("fetch", err)
}

// fetch gets the message with its attachment list, then completes the two
// kinds the list cannot carry: an itemAttachment's message needs a second
// request to expand, and a referenceAttachment's sourceUrl is not a base
// property the $expand projection returns. One level of embedding is
// expanded; a message embedded inside an embedded message is reported as
// truncated, never as absent.
func (m *Mailbox) fetch(ctx context.Context, id string) (mail.Message, error) {
	q := url.Values{}
	q.Set("$select", fetchFields)
	q.Set("$expand", "attachments($select=id,name,contentType,size,isInline,contentId)")
	var msg message
	if err := m.do(ctx, http.MethodGet, "/me/messages/"+url.PathEscape(id)+"?"+q.Encode(), nil, "", &msg); err != nil {
		return mail.Message{}, err
	}
	out, err := translate(msg)
	if err != nil {
		return mail.Message{}, err
	}
	return out, m.complete(ctx, msg, &out)
}

// complete fills the parts of a translated message that the listing
// projection cannot carry; see fetch.
func (m *Mailbox) complete(ctx context.Context, msg message, out *mail.Message) error {
	id := msg.ID
	for i, p := range out.Parts {
		attPath := "/me/messages/" + url.PathEscape(id) + "/attachments/" + url.PathEscape(msg.Attachments[i].ID)
		switch p.Content.(type) {
		case mail.LinkedPart:
			var a attachment
			if err := m.do(ctx, http.MethodGet, attPath, nil, "", &a); err != nil {
				return err
			}
			out.Parts[i].Content = mail.LinkedPart{URL: a.SourceURL}
		case mail.EmbeddedPart:
			var a attachment
			if err := m.do(ctx, http.MethodGet, attPath+"?$expand=microsoft.graph.itemattachment/item", nil, "", &a); err != nil {
				return err
			}
			if a.Item == nil {
				out.Parts[i].Content = mail.EmbeddedPart{Truncated: true}
				continue
			}
			inner, err := translate(*a.Item)
			if err != nil {
				return err
			}
			for j, ip := range inner.Parts {
				if _, ok := ip.Content.(mail.EmbeddedPart); ok {
					inner.Parts[j].Content = mail.EmbeddedPart{Truncated: true}
				}
			}
			out.Parts[i].Content = mail.EmbeddedPart{Item: &inner}
		}
	}
	return nil
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
	for path != "" {
		var l listing
		if err := m.do(ctx, http.MethodGet, path, nil, "", &l); err != nil {
			return nil, m.wrap("conversation", err)
		}
		for _, msg := range l.Value {
			t, err := translate(msg)
			if err != nil {
				return nil, m.wrap("conversation", err)
			}
			if err := m.complete(ctx, msg, &t); err != nil {
				return nil, m.wrap("conversation", err)
			}
			out = append(out, t)
		}
		path = l.NextLink
	}
	mail.SortByReceived(out)
	return out, nil
}

// A handle is "<messageId>/<attachmentId>".
func (m *Mailbox) Open(ctx context.Context, h mail.Handle, w io.Writer) error {
	msgID, attID, ok := strings.Cut(string(h), "/")
	if !ok {
		return m.wrap("open", fmt.Errorf("handle %q: %w", h, mail.ErrNoBytes))
	}
	path := "/me/messages/" + url.PathEscape(msgID) + "/attachments/" + url.PathEscape(attID) + "/$value"
	return m.wrap("open", m.do(ctx, http.MethodGet, path, nil, "", w))
}

// Send posts the prepared message in MIME form: to /me/sendMail when it
// starts a conversation, to the parent's reply action when it answers one.
// Graph answers 202 with no body either way, so there is no provider id or
// conversation to report; the draft record keeps the RFC Message-ID for
// recovery.
func (m *Mailbox) Send(ctx context.Context, p *mail.Prepared, parent *mail.Parent) (mail.Sent, error) {
	if p.Size() > sendLimit {
		return mail.Sent{}, m.wrap("send", mail.ErrTooLarge)
	}
	path := "/me/sendMail"
	if parent != nil {
		action, err := m.replyAction(ctx, p, parent.ID)
		if err != nil {
			return mail.Sent{}, m.wrap("send", fmt.Errorf("%w: %w", mail.ErrNotSent, err))
		}
		path = "/me/messages/" + url.PathEscape(parent.ID) + "/" + action
	}
	body := base64.StdEncoding.EncodeToString(p.Bytes())
	return mail.Sent{}, m.wrap("send", m.do(ctx, http.MethodPost, path, bytes.NewBufferString(body), "text/plain", nil))
}

// replyAction picks the reply action that sends p, as a reply to the
// message parentID, to exactly the recipients p names -- or says why none
// can.
//
// Exchange groups a conversation by Thread-Index, not by In-Reply-To or
// References, so sendMail with threading headers starts a conversation of
// its own; the reply actions are the way into one (createReply would need
// Mail.ReadWrite, which this app does not ask for). But the MIME form of
// the reply actions addresses the reply itself: /reply "uses the sender of
// the original message as recipient", /replyAll "loads the sender and all
// recipients of the original message". The reference does not say whether
// the MIME To and Cc are honoured on top, nor whether a Reply-To wins over
// the sender as it does in the JSON form. So an action is used only when
// every reading of the reference reaches exactly the recipients that were
// previewed; anything else is refused before a byte leaves. The account's
// own address is left out of both sides: whether Graph copies the account
// on its own reply is not a recipient anyone approved or missed.
// Doc-derived until a tenant exists (outlook-status.md).
func (m *Mailbox) replyAction(ctx context.Context, p *mail.Prepared, parentID string) (string, error) {
	if p.Header("Bcc") != "" {
		return "", errors.New("Outlook's reply actions choose a reply's recipients themselves and never include a Bcc; draft the reply without --bcc")
	}
	q := url.Values{}
	q.Set("$select", "from,replyTo,toRecipients,ccRecipients")
	var orig message
	if err := m.do(ctx, http.MethodGet, "/me/messages/"+url.PathEscape(parentID)+"?"+q.Encode(), nil, "", &orig); err != nil {
		return "", err
	}
	self, err := m.address(ctx)
	if err != nil {
		return "", err
	}
	set := func(groups ...[]mail.Address) map[string]bool {
		out := map[string]bool{}
		for _, g := range groups {
			for _, a := range g {
				if e := strings.ToLower(a.Email); e != "" && !strings.EqualFold(e, self) {
					out[e] = true
				}
			}
		}
		return out
	}
	var sender []mail.Address
	if orig.From != nil {
		sender = []mail.Address{addr(*orig.From)}
	}
	replyTo := sender
	if len(orig.ReplyTo) > 0 {
		replyTo = addrs(orig.ReplyTo)
	}
	to, cc := addrs(orig.ToRecipients), addrs(orig.CcRecipients)
	readings := []struct {
		action string
		sets   []map[string]bool
	}{
		{"reply", []map[string]bool{set(sender), set(replyTo)}},
		{"replyAll", []map[string]bool{set(sender, to, cc), set(replyTo, to, cc)}},
	}
	want := set(mail.ParseAddressList(p.Header("To")), mail.ParseAddressList(p.Header("Cc")))
	for _, r := range readings {
		if maps.Equal(r.sets[0], want) && maps.Equal(r.sets[1], want) {
			return r.action, nil
		}
	}
	return "", fmt.Errorf("Outlook's reply actions address a reply themselves -- to the original's sender, or to everyone on it for reply-all -- and this draft's recipients (%s) are not one of those, so it cannot be sent as a reply there without reaching people the preview did not show; re-draft with --reply or --reply-all and no extra --cc",
		strings.Join(slices.Sorted(maps.Keys(want)), ", "))
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
		ReplyTo:        addrs(m.ReplyTo),
	}
	for _, h := range m.InternetMessageHeaders {
		switch strings.ToLower(h.Name) {
		case "in-reply-to":
			e.InReplyTo = mail.ParseMessageIDs(h.Value)
		case "references":
			e.References = mail.ParseMessageIDs(h.Value)
		}
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
	out.HasAttachments = mail.HasAttachments(out.Parts)
	return out, nil
}

// RawMessage returns Graph's own JSON for a message with its attachments
// expanded, for recording fixtures from the real writer.
func (m *Mailbox) RawMessage(ctx context.Context, id string) (map[string]any, error) {
	q := url.Values{}
	q.Set("$select", fetchFields)
	q.Set("$expand", "attachments($select=id,name,contentType,size,isInline,contentId)")
	var out map[string]any
	if err := m.do(ctx, http.MethodGet, "/me/messages/"+url.PathEscape(id)+"?"+q.Encode(), nil, "", &out); err != nil {
		return nil, err
	}
	return out, nil
}
