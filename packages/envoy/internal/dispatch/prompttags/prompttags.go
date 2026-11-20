// Package prompttags keeps a text a model call's user message holds from opening or closing a tag
// of that message (OWASP's LLM01, prompt injection). A message wraps each text it holds in a tag
// of its own, `<person_text>` or `<thread>`, so a text that holds such a tag as its own words - a
// text about that markup, or one written to close its region early and go on as another part or as
// an instruction - would move the message's structure. Escape escapes each opening and closing of
// one of the message's tags in a text: its "<" written "&lt;", and each such escape the text
// already holds gaining one "amp;" ("&amp;lt;"), so Unescape undoes it exactly and the text reads
// back whole. Every other "<" stays as it is, since it opens no tag of the message, so a text that
// holds no such tag is the message's byte for byte. The reviewer (internal/dispatch/reviewer) and
// the records job (internal/dispatch/wordrecords) build their messages with it.
package prompttags

import (
	"regexp"
	"strings"
)

// Tags escapes the openings and closings of one message's tags.
type Tags struct {
	// opening is a tag's "<" and its tail; escaped is one whose "<" is escaped once or more, its
	// "amp;"s in the first group; once is one escaped once, and twice one escaped twice or more,
	// its "amp;"s after the first in the first group.
	opening, escaped, once, twice *regexp.Regexp
}

// New is the escape of the tags names, the tag names of one message.
func New(names ...string) *Tags {
	// tail is a tag's name after its "<": a "/" first for a closing, and the space or the ">"
	// that ends the name.
	tail := `(/?(?:` + strings.Join(names, "|") + `)[\s>])`
	return &Tags{
		opening: regexp.MustCompile(`<` + tail),
		escaped: regexp.MustCompile(`&((?:amp;)*)lt;` + tail),
		once:    regexp.MustCompile(`&lt;` + tail),
		twice:   regexp.MustCompile(`&amp;((?:amp;)*)lt;` + tail),
	}
}

// Escape is text as the message holds it: each opening and closing of one of its tags escaped, so
// it opens and closes none.
func (t *Tags) Escape(text string) string {
	if !strings.ContainsAny(text, "<&") {
		return text
	}
	return t.opening.ReplaceAllString(t.escaped.ReplaceAllString(text, "&amp;${1}lt;${2}"), "&lt;${1}")
}

// Unescape is words a reply copies from a text as the text holds them: Escape undone.
func (t *Tags) Unescape(text string) string {
	if !strings.Contains(text, "&") {
		return text
	}
	return t.twice.ReplaceAllString(t.once.ReplaceAllString(text, "<${1}"), "&${1}lt;${2}")
}
