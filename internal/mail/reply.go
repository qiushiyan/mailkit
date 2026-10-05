package mail

import (
	"errors"
	"fmt"
	"strings"
)

// Parent is the message a sent message answers, named the ways providers
// place a reply: Gmail threads a sent message by ConversationID, Graph
// replies to the message by ID. MessageID is what the reply's In-Reply-To
// names. It travels on the Send call, never in the prepared bytes, so the
// draft record keeps it and a commit sends into the conversation that was
// previewed.
type Parent struct {
	ID             string
	ConversationID string
	MessageID      MessageID
}

// Sent is what a provider reports about a message it accepted. A field is
// empty when the provider does not say -- Graph answers a send with 202
// and no body -- and is never filled in by guessing.
type Sent struct {
	ID             string
	ConversationID string
}

// Reply is everything answering one message derives from it: who the
// answer goes to, what it is called, and the headers and parent that
// thread it. Every provider threads on some of these and every client on
// others, so all of them come from here, once.
type Reply struct {
	// Original is the message answered.
	Original Envelope
	To, Cc   []Address
	// Subject is the original's with "Re: " before it. Gmail threads a
	// reply only when the subjects match, so it is derived, never typed.
	Subject string
	// InReplyTo and References are header values, ready to write.
	InReplyTo  string
	References string
}

// Parent is where the reply goes.
func (r Reply) Parent() Parent {
	return Parent{ID: r.Original.ID, ConversationID: r.Original.ConversationID, MessageID: r.Original.MessageID}
}

// NewReply derives the answer to e from the account answering it. self is
// every address the account sends as; all is reply-all.
//
// Recipients follow RFC 5322 §3.6.3 as mail clients apply it: the
// original's Reply-To when it has one, else its sender; a message the
// account sent itself is answered to its own recipients. Reply-all adds
// everyone else on the original. The account is never added to a reply,
// and each address appears once.
func NewReply(e Envelope, self []string, all bool) (Reply, error) {
	if e.MessageID == "" {
		return Reply{}, fmt.Errorf("message %s has no Message-ID, so a reply to it cannot be threaded", e.ID)
	}
	mine := func(a Address) bool {
		for _, s := range self {
			if s != "" && strings.EqualFold(strings.TrimSpace(s), a.Email) {
				return true
			}
		}
		return false
	}
	r := Reply{Original: e, Subject: ReplySubject(e.Subject)}
	seen := map[string]bool{}
	add := func(to *[]Address, as []Address, skipSelf bool) {
		for _, a := range as {
			key := strings.ToLower(a.Email)
			if key == "" || seen[key] || (skipSelf && mine(a)) {
				continue
			}
			seen[key] = true
			*to = append(*to, a)
		}
	}
	switch {
	case mine(e.From):
		// Answering one's own message continues it to the same people,
		// which can be oneself: a note to self is answered to self.
		add(&r.To, e.To, false)
	case len(e.ReplyTo) > 0:
		add(&r.To, e.ReplyTo, true)
	default:
		add(&r.To, []Address{e.From}, true)
	}
	if all {
		add(&r.To, e.To, true)
		add(&r.Cc, e.Cc, true)
	}
	if len(r.To) == 0 {
		return Reply{}, errors.New("message " + e.ID + " names no one to reply to")
	}

	// RFC 5322 §3.6.4: the parent's References, or its In-Reply-To when
	// that names exactly one message, followed by the parent itself.
	chain := e.References
	if len(chain) == 0 && len(e.InReplyTo) == 1 {
		chain = e.InReplyTo
	}
	refs := make([]string, 0, len(chain)+1)
	for _, id := range chain {
		if id != e.MessageID {
			refs = append(refs, "<"+string(id)+">")
		}
	}
	r.InReplyTo = "<" + string(e.MessageID) + ">"
	r.References = strings.Join(append(refs, r.InReplyTo), " ")
	return r, nil
}

// ReplySubject puts "Re: " before a subject once: one that already starts
// with a reply prefix, in any case (Outlook writes "RE:"), is kept as it is.
func ReplySubject(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 3 && strings.EqualFold(s[:3], "re:") {
		return s
	}
	return strings.TrimSpace("Re: " + s)
}

// ParseMessageIDs reads the ids out of an In-Reply-To or References header,
// in order, brackets removed. Text between ids (comments, commas, folding)
// is not an id and is skipped.
func ParseMessageIDs(h string) []MessageID {
	var out []MessageID
	for {
		open := strings.IndexByte(h, '<')
		if open < 0 {
			return out
		}
		end := strings.IndexByte(h[open:], '>')
		if end < 0 {
			return out
		}
		if id := strings.TrimSpace(h[open+1 : open+end]); id != "" {
			out = append(out, MessageID(id))
		}
		h = h[open+end+1:]
	}
}
