package texts

import (
	"strconv"
	"strings"
	"time"
)

// The bounds of carried words, in UTF-16 units, as Dispatch counts a body.
const (
	// OneAct is the bound of the words of one act, where a text carries one act.
	OneAct = 4000
	// EachOfSeveral is the bound of the words of each act, where a text carries several acts.
	EachOfSeveral = 1000
	// Summary is the bound of a worker's summary.
	Summary = 1000
)

// Words are the words of another author that a text carries in a block of their own.
type Words struct {
	// Text is the words as the event carried them, after the bound.
	Text string
	// By names the author. Set it only where the fields of the text do not name the author.
	By string
	// At is the time of the act. Set it only where the fields of the text do not give the time.
	At time.Time
	// Field is the path of the words in the payload, such as kept[1].answer, where the words
	// are a field of a record in a list. Render sets it for the Words of a list's records.
	Field string
	// Rest is the count of UTF-16 units that the bound cut off.
	Rest int
	// More is true where the event marked its body as cut, or reached the daemon as a
	// payload_omitted stand-in. Then the count of the rest is not known.
	More bool
}

// WordsMeta is what a notice's payload carries for one field that holds another author's
// words, in the payload's words object, keyed by the path of that field.
type WordsMeta struct {
	By   string    `json:"by,omitempty"`
	At   time.Time `json:"at,omitzero"`
	Rest int       `json:"rest,omitempty"`
	More bool      `json:"more,omitempty"`
}

// Bound cuts text to its first limit UTF-16 units. It returns that head and the count of
// UTF-16 units it cut off. A character outside the Basic Multilingual Plane counts two units,
// and Bound never splits it: where its two units would cross the limit, the head leaves it out.
func Bound(text string, limit int) (head string, rest int) {
	units := 0
	for i, r := range text {
		n := utf16Units(r)
		if units+n > limit {
			return text[:i], utf16Length(text[i:])
		}
		units += n
	}
	return text, 0
}

// Carry bounds text to limit and gives the Words that a text carries. Where the event marked
// its body as cut (more), the Words say that more follows and give no count of the rest.
// Otherwise they give the count of the UTF-16 units that the bound cut off. The caller sets By
// and At where the fields of the text do not name them.
func Carry(text string, limit int, more bool) Words {
	head, rest := Bound(text, limit)
	if more {
		return Words{Text: head, More: true}
	}
	return Words{Text: head, Rest: rest}
}

// Block draws w in the block of R1. The tag is name, or, where the words hold </name, the
// first of name-1, name-2 and on whose closing tag the words do not hold. The opening tag
// carries the attributes by, at, field, rest and more, in that order, each only where it is
// set. Then come a new line, the words exactly as given, a new line and the closing tag.
func Block(name string, w Words) string {
	tag := blockTag(name, w.Text)
	var b strings.Builder
	b.Grow(len(tag)*2 + len(w.Text) + 64)
	b.WriteString("<")
	b.WriteString(tag)
	if w.By != "" {
		writeAttribute(&b, "by", w.By)
	}
	if !w.At.IsZero() {
		writeAttribute(&b, "at", formatTime(w.At))
	}
	if w.Field != "" {
		writeAttribute(&b, "field", w.Field)
	}
	if w.Rest > 0 {
		writeAttribute(&b, "rest", strconv.Itoa(w.Rest))
	}
	if w.More {
		writeAttribute(&b, "more", "true")
	}
	b.WriteString(">\n")
	b.WriteString(w.Text)
	b.WriteString("\n</")
	b.WriteString(tag)
	b.WriteString(">")
	return b.String()
}

// empty says whether w carries nothing at all: no words, no cut and no mark that more follows.
func (w Words) empty() bool {
	return w.Text == "" && w.Rest == 0 && !w.More
}

// blockTag is the first name of name, name-1, name-2 and on whose closing tag text does not
// hold.
func blockTag(name, text string) string {
	if !strings.Contains(text, "</"+name) {
		return name
	}
	for n := 1; ; n++ {
		tag := name + "-" + strconv.Itoa(n)
		if !strings.Contains(text, "</"+tag) {
			return tag
		}
	}
}

// writeAttribute writes one attribute of an opening tag. The value escapes the ampersand, the
// angle brackets, the double quote and every control character below U+0020 as XML character
// references, so no value can end the attribute or the tag.
func writeAttribute(b *strings.Builder, key, value string) {
	b.WriteString(" ")
	b.WriteString(key)
	b.WriteString(`="`)
	for i := range len(value) {
		switch c := value[i]; {
		case c == '&':
			b.WriteString("&amp;")
		case c == '<':
			b.WriteString("&lt;")
		case c == '>':
			b.WriteString("&gt;")
		case c == '"':
			b.WriteString("&quot;")
		case c < 0x20:
			b.WriteString("&#")
			b.WriteString(strconv.Itoa(int(c)))
			b.WriteString(";")
		default:
			b.WriteByte(c)
		}
	}
	b.WriteString(`"`)
}

// utf16Units is the count of UTF-16 units of r. A byte that is not valid UTF-8 decodes as
// U+FFFD, one unit, as a JSON decoder reads it.
func utf16Units(r rune) int {
	if r > 0xFFFF {
		return 2
	}
	return 1
}

// utf16Length is the count of UTF-16 units of text.
func utf16Length(text string) int {
	n := 0
	for _, r := range text {
		n += utf16Units(r)
	}
	return n
}

// formatTime writes t as the texts write a time: RFC 3339 in UTC, with the fraction of a second
// only where t has one.
func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}
