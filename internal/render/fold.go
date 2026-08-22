package render

import (
	"github.com/qiushiyan/mailkit/internal/norm"
	"html"
	"regexp"
	"strings"
)

// A quote boundary is where a message stops speaking and starts quoting.
// Ordered: the Outlook header block is checked before the looser
// attribution line so a message carrying both folds at the earlier one.
var boundaries = []struct {
	name string
	re   *regexp.Regexp
}{
	{"outlook-header", regexp.MustCompile(`(?ms)^\s*From:\s*.+?$\n^\s*Sent:\s*.+?$`)},
	{"original-message", regexp.MustCompile(`(?mi)^\s*-{2,}\s*Original Message\s*-{2,}\s*$`)},
	{"attribution", regexp.MustCompile(`(?m)^\s*On\s+.{4,80}?\s+wrote:\s*$`)},
	{"attribution-inline", regexp.MustCompile(`(?m)^\s*On\s+.{4,120},\s*.{2,80}<[^>]+>\s*wrote:\s*$`)},
	{"caret", regexp.MustCompile(`(?m)^>`)},
}

// A forwarded body is usually the only copy of what it contains.
var forwardMarkers = regexp.MustCompile(`(?mi)^[>\s]*(-{3,}\s*Forwarded message\s*-{3,}|Begin forwarded message:)`)

// Split returns the part written now, the part quoted, and the marker that
// separated them. A forward, or a body with no recognised boundary, comes
// back whole as spoken.
func Split(body string) (spoken, quoted, marker string) {
	if body == "" {
		return "", "", ""
	}
	if forwardMarkers.MatchString(body) {
		return body, "", "forward"
	}
	earliest := -1
	for _, b := range boundaries {
		if loc := b.re.FindStringIndex(body); loc != nil && (earliest < 0 || loc[0] < earliest) {
			earliest, marker = loc[0], b.name
		}
	}
	if earliest < 0 {
		return body, "", ""
	}
	return strings.TrimRight(body[:earliest], " \t\n"), strings.TrimSpace(body[earliest:]), marker
}

var (
	// The HTML-to-text step renders every link as text<url>, which doubles
	// every address. MAILTO_DUP in the Python used a backreference RE2 has
	// no equivalent for; here both sides are captured and compared in code.
	mailtoAny = regexp.MustCompile(`(?i)([^\s<>]+)<mailto:([^>]+)>`)
	linkDup   = regexp.MustCompile(`([^\s<>]+)<(https?://[^>]+)>`)
	banners   = []*regexp.Regexp{
		regexp.MustCompile(`(?mi)^You don't often get email from .*?$`),
		regexp.MustCompile(`(?mi)^\s*Learn why this is important\s*$`),
		regexp.MustCompile(`(?mi)^\s*\[?External( Email)?\]?:?\s*$`),
	}
	separatorRun = regexp.MustCompile(`(?m)^[_\-=]{8,}\s*$`)
)

// Tidy undoes the artefacts of the HTML-to-text conversion. Content only
// moves or loses a duplicate copy of itself; nothing is reworded.
func Tidy(text string) string {
	if text == "" {
		return ""
	}
	text = mailtoAny.ReplaceAllStringFunc(text, func(m string) string {
		sub := mailtoAny.FindStringSubmatch(m)
		return sub[1] // whether or not the target repeats the anchor, the anchor is what was said
	})
	text = linkDup.ReplaceAllStringFunc(text, func(m string) string {
		sub := linkDup.FindStringSubmatch(m)
		anchor, url := sub[1], sub[2]
		stripped := strings.TrimRight(url, "/")
		stripped = strings.TrimPrefix(strings.TrimPrefix(stripped, "https://"), "http://")
		if strings.HasSuffix(stripped, strings.TrimRight(anchor, "/")) {
			return anchor
		}
		return anchor + " (" + url + ")"
	})
	for _, b := range banners {
		text = b.ReplaceAllString(text, "")
	}
	text = separatorRun.ReplaceAllString(text, "")
	// Last, so a decoded &lt; cannot manufacture a text<url> pattern for the
	// rules above to act on.
	text = html.UnescapeString(text)
	text = strings.ReplaceAll(text, " ", " ")
	return strings.TrimSpace(blankRun.ReplaceAllString(text, "\n\n"))
}

var (
	// Lines that carry no prose: attribution, header blocks, contact chrome.
	// They are structure, so their absence upstream never means content
	// was lost.
	structural   = regexp.MustCompile(`(?i)^(On\s.+wrote:|From:|To:|Sent:|Cc:|Bcc:|Subject:|Date:|E:|T:|W:|You don't often get email)`)
	quotePrefix  = regexp.MustCompile(`^[>\s]+`)
	minProseLine = 40
	// FoldCoverage is the fraction of a quoted block's prose lines that must
	// already exist upstream before the block is folded.
	FoldCoverage = 0.8
)

func contentLines(text string) []string {
	var out []string
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimSpace(quotePrefix.ReplaceAllString(line, ""))
		if line != "" && !structural.MatchString(line) {
			out = append(out, line)
		}
	}
	return out
}

func proseLines(text string) []string {
	var out []string
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimSpace(quotePrefix.ReplaceAllString(line, ""))
		if len(line) >= minProseLine && !structural.MatchString(line) {
			out = append(out, line)
		}
	}
	return out
}

// Coverage is how much of a quoted block genuinely repeats something in
// pool. It returns the ratio and the number of lines considered: prose
// lines when the block has any, otherwise every non-structural line, so a
// quote of short answers ("Yes, 3pm works.") is checked rather than waved
// through. Only a block that is structure alone counts as fully covered.
func Coverage(quoted, pool string) (float64, int) {
	lines := proseLines(quoted)
	if len(lines) == 0 {
		lines = contentLines(quoted)
	}
	if len(lines) == 0 {
		return 1, 0
	}
	flat := norm.Text(pool)
	found := 0
	for _, l := range lines {
		if strings.Contains(flat, norm.Text(l)) {
			found++
		}
	}
	return float64(found) / float64(len(lines)), len(lines)
}
