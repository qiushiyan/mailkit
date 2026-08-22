package render

import (
	"fmt"
	"github.com/qiushiyan/mailkit/internal/norm"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/qiushiyan/mailkit/internal/mail"
)

// Turn is one message's contribution to a transcript.
type Turn struct {
	ID      string `json:"id"`
	Date    string `json:"date"`
	From    string `json:"from"`
	To      string `json:"to,omitempty"`
	Subject string `json:"subject"`
	Said    string `json:"said"`
	Fold
	Attachments []PartSummary `json:"attachments"`
}

// Fold is what the quote fold did to one message.
type Fold struct {
	QuotedChars int    `json:"quoted_chars,omitempty"`
	QuoteMarker string `json:"quote_marker,omitempty"`
	// FoldRejected records why a detected quote was kept: the check found
	// its prose was not upstream, so folding would have destroyed it.
	FoldRejected string `json:"fold_rejected,omitempty"`
}

// PartSummary is the attachment line a transcript shows.
type PartSummary struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Inline bool   `json:"inline"`
}

// Transcript is a conversation rendered chronologically with repetition
// folded.
type Transcript struct {
	Turns           []Turn `json:"turns"`
	RawChars        int    `json:"raw_chars"`
	TranscriptChars int    `json:"transcript_chars"`
	FoldedChars     int    `json:"folded_chars"`
}

// FoldOne decides what to do with a single message's quoted tail, given the
// text of everything earlier in its conversation. A quote is folded only
// once it has been checked to repeat that pool; marker detection alone is
// not enough to make it safe, because an Apple Mail forward prefixes
// "Begin forwarded message:" with the same "> " it uses for replies, and
// folding one destroyed 6390 characters with no other copy.
//
// providerFolded is the provider's own quote-stripped text, when it offers
// one. It is a candidate boundary, verified the same way: its removed tail
// must exist in the pool too.
func FoldOne(body, pool, providerFolded string) (said string, f Fold) {
	spoken, quoted, marker := Split(body)
	if quoted == "" && providerFolded != "" {
		// Our markers found nothing but the provider removed something.
		// Treat what it removed as the quote and verify it like ours. The
		// boundary is found in the body's own bytes: the provider's text
		// differs in whitespace and punctuation, so its length says nothing
		// about where the body should be cut.
		if end, ok := prefixEnd(body, providerFolded); ok && end < len(body) {
			spoken, quoted, marker = body[:end], body[end:], "provider"
		}
	}
	if quoted == "" {
		return Tidy(spoken), Fold{QuoteMarker: marker}
	}
	ratio, n := Coverage(quoted, pool)
	if ratio < FoldCoverage {
		return Tidy(body), Fold{QuoteMarker: marker,
			FoldRejected: fmt.Sprintf("kept: %d%% of %d quoted lines are not in earlier turns", int(ratio*100), n)}
	}
	return Tidy(spoken), Fold{QuotedChars: len(quoted), QuoteMarker: marker}
}

// prefixEnd returns the byte offset in body just past the text that prefix
// covers in normalised terms, carried on through the punctuation that
// closes the spoken line. ok is false when prefix is empty or is not a
// prefix of body.
func prefixEnd(body, prefix string) (int, bool) {
	fb, ends := norm.Fold(body)
	fp := norm.Text(prefix)
	if fp == "" || !strings.HasPrefix(fb, fp) {
		return 0, false
	}
	end := ends[len(fp)-1]
	for end < len(body) {
		r, n := utf8.DecodeRuneInString(body[end:])
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '\n' || r == '\r' {
			break
		}
		end += n
	}
	return end, true
}

// Read folds one message the way Build folds it as a turn of conv: against
// everything earlier in the conversation's order. A message not in conv
// has no upstream, so nothing is folded.
func Read(msg mail.Message, conv []mail.Message) (said string, f Fold) {
	for i := range conv {
		if conv[i].ID == msg.ID {
			turn := Build(conv[:i+1]).Turns[i]
			return turn.Said, turn.Fold
		}
	}
	return FoldOne(Text(msg.Body), "", providerText(msg))
}

func providerText(m mail.Message) string {
	if m.ProviderFolded == nil {
		return ""
	}
	return Text(*m.ProviderFolded)
}

// Build renders messages (ascending) as a transcript, one turn each.
func Build(msgs []mail.Message) Transcript {
	var t Transcript
	var pool strings.Builder
	for _, m := range msgs {
		body := Text(m.Body)
		said, f := FoldOne(body, pool.String(), providerText(m))
		pool.WriteString("\n")
		pool.WriteString(body)
		t.RawChars += len(body)
		t.TranscriptChars += len(said)
		t.Turns = append(t.Turns, Turn{
			ID: m.ID, Date: m.DateHeader, From: m.From.String(), To: mail.Joined(m.To),
			Subject: m.Subject, Said: said, Fold: f, Attachments: Summaries(m.Parts),
		})
	}
	t.FoldedChars = t.RawChars - t.TranscriptChars
	return t
}

// Summaries lists parts for output.
func Summaries(parts []mail.Part) []PartSummary {
	out := make([]PartSummary, 0, len(parts))
	for _, p := range parts {
		out = append(out, PartSummary{Name: p.Name, Kind: p.Kind(), Inline: p.Inline})
	}
	return out
}

// Render is the human/model-readable form of a transcript.
func Render(t Transcript) string {
	var b strings.Builder
	for i, turn := range t.Turns {
		fmt.Fprintf(&b, "--- %d. %s\n    %s\n", i+1, turn.Date, turn.From)
		if turn.To != "" {
			fmt.Fprintf(&b, "    to: %s\n", turn.To)
		}
		if len(turn.Attachments) > 0 {
			names := make([]string, 0, len(turn.Attachments))
			for _, a := range turn.Attachments {
				names = append(names, a.Name)
			}
			fmt.Fprintf(&b, "    attachments: %s\n", strings.Join(names, ", "))
		}
		b.WriteString("\n")
		if turn.Said == "" {
			b.WriteString("(no new text -- quoting only)")
		} else {
			b.WriteString(turn.Said)
		}
		if turn.QuotedChars > 0 {
			fmt.Fprintf(&b, "\n\n    [folded %d chars quoted via %s; those turns appear above]", turn.QuotedChars, turn.QuoteMarker)
		}
		if turn.FoldRejected != "" {
			fmt.Fprintf(&b, "\n\n    [quote %s]", turn.FoldRejected)
		}
		b.WriteString("\n\n")
	}
	fmt.Fprintf(&b, "transcript %d chars from %d raw (%d folded as repetition)",
		t.TranscriptChars, t.RawChars, t.FoldedChars)
	return b.String()
}
