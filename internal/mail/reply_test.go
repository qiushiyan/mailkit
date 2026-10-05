package mail_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/qiushiyan/mailkit/internal/fixtures"
	"github.com/qiushiyan/mailkit/internal/mail"
)

// Owner of what a reply derives from the message it answers.

func emails(as []mail.Address) []string {
	out := make([]string, 0, len(as))
	for _, a := range as {
		out = append(out, a.Email)
	}
	return out
}

func TestNewReply_Recipients(t *testing.T) {
	self := []string{"me@example.com", "alias@example.com"}
	addrs := func(es ...string) []mail.Address {
		var out []mail.Address
		for _, e := range es {
			out = append(out, mail.Address{Email: e})
		}
		return out
	}
	base := mail.Envelope{ID: "m1", MessageID: "m1@example.test", Subject: "Plan"}
	cases := []struct {
		name    string
		from    string
		replyTo []string
		to, cc  []string
		all     bool
		wantTo  []string
		wantCc  []string
		wantErr string
	}{
		{name: "reply goes to the sender", from: "liam@help.example", to: []string{"me@example.com"}, wantTo: []string{"liam@help.example"}},
		{name: "Reply-To wins over the sender", from: "liam@help.example", replyTo: []string{"ticket-42@help.example"}, to: []string{"me@example.com"}, wantTo: []string{"ticket-42@help.example"}},
		{name: "reply leaves the other recipients off", from: "a@x.example", to: []string{"me@example.com", "b@x.example"}, cc: []string{"c@x.example"}, wantTo: []string{"a@x.example"}},
		{name: "reply-all adds everyone else, never the account", from: "a@x.example", to: []string{"me@example.com", "b@x.example"}, cc: []string{"alias@example.com", "c@x.example"}, all: true,
			wantTo: []string{"a@x.example", "b@x.example"}, wantCc: []string{"c@x.example"}},
		{name: "reply-all lists each address once", from: "a@x.example", to: []string{"A@X.example", "b@x.example"}, cc: []string{"b@x.example"}, all: true, wantTo: []string{"a@x.example", "b@x.example"}},
		{name: "my own message is answered to its recipients", from: "me@example.com", to: []string{"fhashim@quintain.example"}, wantTo: []string{"fhashim@quintain.example"}},
		{name: "my own message, reply-all keeps its cc", from: "Me@Example.com", to: []string{"a@x.example"}, cc: []string{"c@x.example", "alias@example.com"}, all: true,
			wantTo: []string{"a@x.example"}, wantCc: []string{"c@x.example"}},
		{name: "a note to self is answered to self", from: "me@example.com", to: []string{"me@example.com"}, wantTo: []string{"me@example.com"}},
		{name: "a Reply-To naming only the account leaves no one", from: "a@x.example", replyTo: []string{"me@example.com"}, wantErr: "no one to reply to"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := base
			e.From = mail.Address{Email: tc.from}
			e.ReplyTo, e.To, e.Cc = addrs(tc.replyTo...), addrs(tc.to...), addrs(tc.cc...)
			r, err := mail.NewReply(e, self, tc.all)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error %q, got %v (to %v)", tc.wantErr, err, emails(r.To))
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := emails(r.To); !slices.EqualFunc(got, tc.wantTo, strings.EqualFold) {
				t.Errorf("To = %v, want %v", got, tc.wantTo)
			}
			if got := emails(r.Cc); !slices.EqualFunc(got, tc.wantCc, strings.EqualFold) {
				t.Errorf("Cc = %v, want %v", got, tc.wantCc)
			}
		})
	}
}

func TestReplySubject(t *testing.T) {
	for in, want := range map[string]string{
		"Refund request":            "Re: Refund request",
		"Re: Refund request":        "Re: Refund request",
		"RE: Quintain Living - 819": "RE: Quintain Living - 819", // Outlook's prefix is kept, not stacked
		"re:lowercase":              "re:lowercase",
		"  Padded  ":                "Re: Padded",
		"Fwd: Beyond Naive RAG":     "Re: Fwd: Beyond Naive RAG",
		"Reunion dinner":            "Re: Reunion dinner", // "Re" as a word is not a prefix
		"":                          "Re:",
	} {
		if got := mail.ReplySubject(in); got != want {
			t.Errorf("ReplySubject(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseMessageIDs(t *testing.T) {
	got := mail.ParseMessageIDs("<a@x>\r\n <b@y>, (comment) <c@z> <> trailing")
	if want := []mail.MessageID{"a@x", "b@y", "c@z"}; !slices.Equal(got, want) {
		t.Errorf("ids = %v, want %v", got, want)
	}
	if got := mail.ParseMessageIDs("no ids here"); len(got) != 0 {
		t.Errorf("ids from plain text: %v", got)
	}
}

// The recorded tenancy thread is the real writer's shape: Apple Mail and
// Outlook both writing the chain. A reply to its last message continues the
// chain the message carries, and -- the last message being mine -- goes
// back to the person it was sent to.
func TestNewReply_ContinuesTheRecordedChain(t *testing.T) {
	thread := fixtures.Thread(t, fixtures.TenancyThread)
	last := thread[len(thread)-1]
	r, err := mail.NewReply(last.Envelope, []string{"qiushi.yann@gmail.com"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := emails(r.To); len(got) != 1 || !strings.EqualFold(got[0], "FHashim@quintainliving.com") {
		t.Errorf("reply to my own message goes to its recipient, got %v", got)
	}
	refs := strings.Fields(r.References)
	if len(refs) != len(last.References)+1 || refs[len(refs)-1] != "<"+string(last.MessageID)+">" || r.InReplyTo != refs[len(refs)-1] {
		t.Fatalf("References must be the parent's chain then the parent:\n got %v\n parent refs %v", refs, last.References)
	}
	for i, id := range last.References {
		if refs[i] != "<"+string(id)+">" {
			t.Errorf("chain reordered at %d: %s", i, refs[i])
		}
	}
	if r.Subject != last.Subject {
		t.Errorf("a subject already prefixed stays as it is: %q -> %q", last.Subject, r.Subject)
	}
	if p := r.Parent(); p.ID != last.ID || p.ConversationID != fixtures.TenancyThread || p.MessageID != last.MessageID {
		t.Errorf("parent = %+v", p)
	}
}

// RFC 5322 §3.6.4: with no References, a single-id In-Reply-To starts the
// chain; with neither, the chain is the parent alone. A message with no
// Message-ID cannot be answered in thread at all.
func TestNewReply_ChainFallbacksAndRefusal(t *testing.T) {
	e := mail.Envelope{ID: "m2", MessageID: "m2@x", From: mail.Address{Email: "a@x.example"}, InReplyTo: []mail.MessageID{"m1@x"}}
	if r, _ := mail.NewReply(e, nil, false); r.References != "<m1@x> <m2@x>" {
		t.Errorf("In-Reply-To fallback: %q", r.References)
	}
	e.InReplyTo = []mail.MessageID{"m0@x", "m1@x"}
	if r, _ := mail.NewReply(e, nil, false); r.References != "<m2@x>" {
		t.Errorf("an ambiguous In-Reply-To starts no chain: %q", r.References)
	}
	e.MessageID = ""
	if _, err := mail.NewReply(e, nil, false); err == nil || !strings.Contains(err.Error(), "Message-ID") {
		t.Errorf("no Message-ID must refuse, got %v", err)
	}
}
