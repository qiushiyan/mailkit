package main

import (
	"encoding/json/v2"
	"fmt"
	"io"

	"github.com/qiushiyan/mailkit/internal/cluster"
	"github.com/qiushiyan/mailkit/internal/images"
	"github.com/qiushiyan/mailkit/internal/mail"
	"github.com/qiushiyan/mailkit/internal/render"
)

// output is where a command's result goes: one JSON document on stdout, or
// with --text the human form. JSON is the default because the reader is
// usually a model, and the processed form must be the one it gets without
// asking (docs/output-design.md).
type output struct {
	w    io.Writer
	text bool
}

func (o output) json(v any) error {
	b, err := json.Marshal(v, json.Deterministic(true))
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(o.w, string(b))
	return err
}

func (o output) printf(format string, args ...any) { fmt.Fprintf(o.w, format, args...) }

// The JSON shapes below are a contract: skills/read-email reads them, and
// testdata/contract pins them.

type envelopeOut struct {
	ID             string `json:"id"`
	ConversationID string `json:"conversation_id"`
	MessageID      string `json:"message_id,omitempty"`
	Date           string `json:"date"`
	From           string `json:"from"`
	To             string `json:"to,omitempty"`
	Cc             string `json:"cc,omitempty"`
	Subject        string `json:"subject"`
	Snippet        string `json:"snippet,omitempty"`
}

func envOut(e mail.Envelope) envelopeOut {
	return envelopeOut{ID: e.ID, ConversationID: e.ConversationID, MessageID: string(e.MessageID),
		Date: e.DateHeader, From: e.From.String(), To: mail.Joined(e.To), Cc: mail.Joined(e.Cc),
		Subject: e.Subject, Snippet: e.Snippet}
}

type partOut struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Handle    string `json:"handle,omitempty"`
	URL       string `json:"url,omitempty"`
	Size      int64  `json:"size"`
	MIME      string `json:"mime_type"`
	Inline    bool   `json:"inline"`
	ContentID string `json:"content_id,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	// Embedded is the attached message itself, for an embedded_message
	// part: this output is the caller's only copy of its text.
	Embedded *embeddedOut `json:"embedded,omitempty"`
}

type embeddedOut struct {
	From        string    `json:"from"`
	To          string    `json:"to,omitempty"`
	Date        string    `json:"date"`
	Subject     string    `json:"subject"`
	Body        string    `json:"body"`
	Attachments []partOut `json:"attachments"`
}

func partsOut(parts []mail.Part) []partOut {
	out := make([]partOut, 0, len(parts))
	for _, p := range parts {
		o := partOut{Name: p.Name, Kind: p.Kind(), Size: p.Size, MIME: p.MIME, Inline: p.Inline, ContentID: p.ContentID}
		switch c := p.Content.(type) {
		case mail.StoredPart:
			o.Handle = string(c.Handle)
		case mail.LinkedPart:
			o.URL = c.URL
		case mail.EmbeddedPart:
			o.Truncated = c.Truncated
			if c.Item != nil {
				o.Embedded = &embeddedOut{
					From: c.Item.From.String(), To: mail.Joined(c.Item.To), Date: c.Item.DateHeader, Subject: c.Item.Subject,
					Body: render.Text(c.Item.Body), Attachments: partsOut(c.Item.Parts),
				}
			}
		}
		out = append(out, o)
	}
	return out
}

type readOut struct {
	envelopeOut
	Body string `json:"body"`
	render.Fold
	Attachments  []partOut        `json:"attachments"`
	RemoteImages []images.Fetched `json:"remote_images,omitempty"`
	NextSteps    []string         `json:"next_steps"`
}

// rawMsg is one message of thread --raw.
type rawMsg struct {
	envelopeOut
	Body        string    `json:"body"`
	Attachments []partOut `json:"attachments"`
}

// read prints a message for people: headers, attachment lines (with any
// embedded message in full), downloaded pictures, the body, then what the
// fold did and the next steps.
func (o output) read(r readOut) {
	for _, kv := range [][2]string{{"Date", r.Date}, {"From", r.From}, {"To", r.To}, {"Cc", r.Cc}, {"Subject", r.Subject}} {
		if kv[1] != "" {
			o.printf("%-9s %s\n", kv[0], kv[1])
		}
	}
	for _, p := range r.Attachments {
		tag := ""
		if p.Inline {
			tag = " [inline]"
		}
		if p.Kind != "stored" {
			tag += " [" + p.Kind + "]"
		}
		o.printf("%-9s %s (%s)%s\n", "Attach", p.Name, render.Size(p.Size), tag)
		if e := p.Embedded; e != nil {
			o.printf("\n--- embedded message: %s\n    From: %s\n    Date: %s\n    Subject: %s\n\n%s\n--- end embedded message\n", p.Name, e.From, e.Date, e.Subject, e.Body)
		}
	}
	for _, img := range r.RemoteImages {
		if img.Error != "" {
			o.printf("%-9s %s -- FAILED: %s\n", "Remote", truncate(img.URL, 70), img.Error)
			continue
		}
		note := ""
		if img.Kind != images.Image {
			note = "  <- " + string(img.Kind)
		}
		o.printf("%-9s %s  %dx%d%s\n", "Remote", img.Path, img.Width, img.Height, note)
	}
	o.printf("\n%s\n", r.Body)
	if r.QuotedChars > 0 {
		o.printf("\n[%d chars of quoted history folded -- it repeats earlier turns; --raw to see it]\n", r.QuotedChars)
	}
	if r.FoldRejected != "" {
		o.printf("\n[quote %s]\n", r.FoldRejected)
	}
	for _, s := range r.NextSteps {
		o.printf("\n%s\n", s)
	}
}

// context prints a cluster for people: the seed, the signals it searched
// on, what was held back, and each hit with its reasons.
func (o output) context(r cluster.Result) {
	o.printf("seed  %s  %s\n      %s\n", r.Seed.ID, r.Seed.Date, r.Seed.Subject)
	o.printf("signals  identifiers=%v  domain=%s  tokens=%v\n", r.Signals.Identifiers, orDash(r.Signals.Domain), r.Signals.SubjectTokens)
	for _, d := range r.DroppedAsTooCommon {
		o.printf("dropped  %s -- %s hits, not distinctive\n", d.Reason, d.Hits)
	}
	if r.BelowMinScore > 0 {
		o.printf("held back  %d weaker match(es); --min-score 1 to see them\n", r.BelowMinScore)
	}
	o.printf("\n")
	if len(r.Related) == 0 {
		o.printf("nothing related found\n")
		return
	}
	for _, h := range r.Related {
		o.printf("[%2d] %s  %s\n     %s\n     from: %s\n", h.Score, h.ID, h.Date, truncate(h.Subject, 70), h.From)
		for _, w := range h.Why {
			o.printf("     why : %s\n", w)
		}
		o.printf("\n")
	}
}

// orEmpty keeps next_steps an array in JSON when there are none.
func orEmpty(steps []string) []string {
	if steps == nil {
		return []string{}
	}
	return steps
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
