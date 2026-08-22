"""HTML to text, shaped for the folding and tidying rules downstream.

Two providers must produce the same text or every rule calibrated on one
silently misfires on the other, so the conversion belongs here rather than
being outsourced to whatever a provider's CLI happens to do.

Doing it here also recovers information the outsourced version threw away.
Outlook replies were thought to carry no quote markers at all -- the boundary
between what someone says and what they quote surviving only as a `From:/Sent:`
line. That turns out to be false: the HTML wraps quoted history in
<blockquote>, and the converter was discarding it. Emitting "> " for
blockquote depth turns the most attribution-dangerous case into the easiest
one.

Images get a marker rather than vanishing. A picture that converts to a bare
parenthesised URL is indistinguishable from a link, which is how a screenshot
carrying the only real content of a message got read as absence.
"""

import html
import re
from html.parser import HTMLParser

# Rendered but contributing no text of their own.
VOID = {"br", "img", "hr"}
# Split by whether the element has an end tag. A void element in the skip set
# would increment the depth counter and never decrement it, suppressing the
# entire rest of the document -- which is exactly what happened.
SKIP_CONTAINER = {"script", "style", "head", "title"}
SKIP_VOID = {"meta", "link", "base"}
# `tr` is absent deliberately: its end tag already emits the row break, and
# listing it here would double every one.
BLOCK = {"p", "div", "table", "ul", "ol", "h1", "h2", "h3", "h4", "h5",
         "h6", "blockquote", "section", "article", "header", "footer", "pre"}

BLANK_RUN = re.compile(r"\n{3,}")
# A line holding only quote markers is an artefact of where the block tags
# fell, not a quoted blank line worth keeping.
EMPTY_QUOTE_LINE = re.compile(r"^[>\s]*>[>\s]*$", re.M)
TRAILING_WS = re.compile(r"[ \t]+$", re.M)


class _Converter(HTMLParser):
    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.out = []
        self.skip_depth = 0
        self.quote_depth = 0
        self.link = None
        self.link_text = []
        self.cell_open = False

    # -- helpers ------------------------------------------------------
    def _emit(self, text):
        if self.link is not None:
            self.link_text.append(text)
        else:
            self.out.append(text)

    def _newline(self, count=1):
        self._emit("\n" * count)

    # -- parser hooks -------------------------------------------------
    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        if tag in SKIP_VOID:
            return
        if tag in SKIP_CONTAINER:
            self.skip_depth += 1
            return
        if self.skip_depth:
            return

        if tag == "br":
            self._newline()
        elif tag == "img":
            # An alt attribute is the sender's own description; keep it.
            alt = (attrs.get("alt") or "").strip()
            src = (attrs.get("src") or "").strip()
            label = f"[image: {alt}]" if alt else "[image]"
            self._emit(f"{label} ( {src} )" if src else label)
        elif tag == "a":
            self.link = (attrs.get("href") or "").strip()
            self.link_text = []
        elif tag == "li":
            self._newline()
            self._emit("- ")
        elif tag in ("td", "th"):
            if self.cell_open:
                self._emit(" | ")
            self.cell_open = True
        elif tag == "blockquote":
            self.quote_depth += 1
            self._newline(2)
        elif tag in BLOCK:
            self._newline()

    def handle_endtag(self, tag):
        if tag in SKIP_CONTAINER:
            self.skip_depth = max(0, self.skip_depth - 1)
            return
        if tag in SKIP_VOID or self.skip_depth:
            return

        if tag == "a":
            text = "".join(self.link_text).strip()
            href, self.link, self.link_text = self.link, None, []
            if not text:
                self._emit(href)
            elif not href or _same_target(text, href):
                self._emit(text)
            else:
                self._emit(f"{text} ( {href} )")
        elif tag in ("td", "th"):
            pass
        elif tag == "tr":
            self.cell_open = False
            self._newline()
        elif tag == "blockquote":
            self.quote_depth = max(0, self.quote_depth - 1)
            self._newline(2)
        elif tag in BLOCK:
            self._newline()

    def handle_data(self, data):
        if self.skip_depth:
            return
        # Collapse runs of whitespace but keep the fact that there was some.
        text = re.sub(r"[ \t\r\n]+", " ", data)
        if text.strip() or text == " ":
            self._emit(text)

    def close_text(self):
        self.close()
        return "".join(self.out)


def _same_target(text, href):
    """True when the anchor text adds nothing to the href."""
    a = text.strip().rstrip("/").lower()
    b = href.strip().rstrip("/").lower()
    b = re.sub(r"^(https?://|mailto:)", "", b)
    return a == b or b.endswith(a) and len(a) > 6


def convert(source):
    """HTML in, text out. Quoted history comes back prefixed with '> '."""
    if not source:
        return ""
    parser = _QuoteAware()
    parser.feed(source)
    text = parser.close_text()
    text = html.unescape(text)
    text = text.replace("\xa0", " ")
    text = TRAILING_WS.sub("", text)
    text = EMPTY_QUOTE_LINE.sub("", text)
    return BLANK_RUN.sub("\n\n", text).strip()


class _QuoteAware(_Converter):
    """Prefixes lines written while inside a <blockquote> with '> ' per level.

    Done at emit time rather than as a post-pass: by the time the whole
    document is a string, which lines were quoted is exactly the information
    that has been lost.
    """

    def _emit(self, text):
        if self.link is not None:
            self.link_text.append(text)
            return
        if self.quote_depth:
            prefix = "> " * self.quote_depth
            text = text.replace("\n", "\n" + prefix)
        self.out.append(text)
