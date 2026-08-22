package mail

import (
	"strings"
	"testing"
	"time"
)

// Owner of the search grammar and of what "matches" means.

func TestParseQuery_PortableGrammar(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name, q string
		want    func(Criteria) bool
		wantErr bool
	}{
		{"bare words are phrases", `ikea assembly`, func(c Criteria) bool { return len(c.Phrases) == 2 && c.Phrases[1] == "assembly" }, false},
		{"quoted phrase stays whole", `"order 1623209215"`, func(c Criteria) bool { return len(c.Phrases) == 1 && c.Phrases[0] == "order 1623209215" }, false},
		{"from domain", `from:taskrabbit.co.uk`, func(c Criteria) bool { return c.From == "taskrabbit.co.uk" }, false},
		{"subject quoted", `subject:"two words"`, func(c Criteria) bool { return len(c.SubjectTerms) == 1 && c.SubjectTerms[0] == "two words" }, false},
		{"has attachment", `has:attachment`, func(c Criteria) bool { return c.HasAttachment }, false},
		{"absolute dates", `after:2026/08/01 before:2026-08-10`, func(c Criteria) bool { return c.After.Day() == 1 && c.Before.Day() == 10 }, false},
		{"operators are case-insensitive", `After:2026/08/01 Newer_Than:2d`, func(c Criteria) bool { return c.After.Equal(now.AddDate(0, 0, -2)) && c.Before.IsZero() }, false},
		{"newer_than anchors on now", `newer_than:7d`, func(c Criteria) bool { return c.After.Equal(now.AddDate(0, 0, -7)) }, false},
		{"older_than", `older_than:1m`, func(c Criteria) bool { return c.Before.Before(now.AddDate(0, 0, -29)) }, false},
		{"unknown operator is an error", `label:inbox`, nil, true},
		{"has:other is an error", `has:drive`, nil, true},
		{"bad date is an error", `after:yesterday`, nil, true},
		{"empty value is an error", `from:`, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := ParseQuery(tc.q, now)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got %+v", c)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !tc.want(c) {
				t.Errorf("unexpected criteria %+v", c)
			}
		})
	}
}

func TestCriteria_Match(t *testing.T) {
	at := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	e := Envelope{
		Received: at, Subject: "Your IKEA assembly is booked",
		From:           Address{Email: "notify@mail.taskrabbit.co.uk"},
		To:             []Address{{Email: "me@example.com"}},
		HasAttachments: false,
	}
	text := "Order #1623209215 has been confirmed. Thanks!"
	cases := []struct {
		name string
		c    Criteria
		want bool
	}{
		{"zero matches everything", Criteria{}, true},
		{"phrase ignores punctuation and case", Criteria{Phrases: []string{"order 1623209215"}}, true},
		{"phrase absent", Criteria{Phrases: []string{"refund"}}, false},
		{"subject any-of", Criteria{SubjectTerms: []string{"refund", "assembly"}}, true},
		{"subject none", Criteria{SubjectTerms: []string{"refund"}}, false},
		{"from bare domain matches subdomain", Criteria{From: "taskrabbit.co.uk"}, true},
		{"from exact address must be exact", Criteria{From: "other@taskrabbit.co.uk"}, false},
		{"to domain", Criteria{To: "example.com"}, true},
		{"after inclusive", Criteria{After: at}, true},
		{"before exclusive", Criteria{Before: at}, false},
		{"after later", Criteria{After: at.Add(time.Second)}, false},
		{"has attachment", Criteria{HasAttachment: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.c.Match(e, text); got != tc.want {
				t.Errorf("Match = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAddress_Domain(t *testing.T) {
	for in, want := range map[string]string{
		"no-reply@mail.corp.co.uk": "corp.co.uk",
		"a@taskrabbit.com":         "taskrabbit.com",
		"a@news.ikea.com":          "ikea.com",
		"Name <x@sub.example.org>": "example.org",
		"nonsense":                 "",
	} {
		if got := ParseAddress(in).Domain(); got != want {
			t.Errorf("Domain(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseDate_AlwaysZoned(t *testing.T) {
	for _, s := range []string{
		"Thu, 6 Aug 2026 10:51:55 +0100",
		"Thu, 6 Aug 2026 10:51:55 +0000 (UTC)",
		"6 Aug 2026 10:51:55",
		"2026-08-06T10:51:55Z",
	} {
		tm, err := ParseDate(s)
		if err != nil {
			t.Errorf("%q: %v", s, err)
			continue
		}
		if tm.Location() == nil {
			t.Errorf("%q: no location", s)
		}
	}
	if _, err := ParseDate("not a date"); err == nil {
		t.Error("garbage should fail, not silently zero")
	}
}

func TestPrepared_BytesCannotBeMutatedAfterReview(t *testing.T) {
	p, err := NewPrepared(strings.NewReader("From: a@b.c\r\nTo: d@e.f\r\nSubject: hi\r\n\r\nbody\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	b := p.Bytes()
	b[0] = 'X'
	again, _ := NewPrepared(p.Reader())
	if again.Digest() != p.Digest() || p.Header("From") != "a@b.c" {
		t.Fatal("a caller's write to Bytes() changed what would be sent")
	}
}
