package texts

import (
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
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

// wordsTag is the struct tag of a string field of a notice or a role-message record that holds
// another author's words: texts:"words".
const wordsTag = "words"

// WordsPaths lists the path of each field of record that holds another author's words and is
// set: a string field tagged texts:"words" that is not empty, and a Words or *Words field that
// carries words, a cut or a mark that more follows. A path is the field's json name after the json
// names of the objects that hold it, joined by dots, with the index of a record in a list after the
// list's name, counted from 0: summary, kept[1].answer, catch_up.kept.decisions[1].answer. An
// embedded struct with no json tag gives its fields in its place, as encoding/json reads it. The
// paths come in the order of the fields' declaration.
//
// The words object of a notice's or a role message's payload is keyed by these paths, with a
// WordsMeta for each.
func WordsPaths(record any) []string {
	return wordsPathsIn(nil, reflect.ValueOf(record), "")
}

// wordsPathsIn appends the paths of the words fields that v holds at path: v's own fields for a
// struct, the fields of each record for a list, and nothing for any other value.
func wordsPathsIn(paths []string, v reflect.Value, path string) []string {
	for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return paths
		}
		v = v.Elem()
	}
	switch v.Kind() {
	case reflect.Struct:
		if v.Type() == timeType {
			return paths
		}
		return wordsPathsOf(paths, v, path)
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			paths = wordsPathsIn(paths, v.Index(i), path+"["+strconv.Itoa(i)+"]")
		}
	}
	return paths
}

// wordsPathsOf appends the paths of the words fields of the struct v, whose own path is path
// (empty for the record itself).
func wordsPathsOf(paths []string, v reflect.Value, path string) []string {
	for i := range v.NumField() {
		field := v.Type().Field(i)
		tag, tagged := field.Tag.Lookup("json")
		if tag == "-" || !field.IsExported() {
			continue
		}
		value := v.Field(i)
		if field.Anonymous && !tagged {
			paths = wordsPathsIn(paths, value, path)
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" {
			name = field.Name
		}
		if path != "" {
			name = path + "." + name
		}
		switch {
		case field.Type == wordsType:
			if !value.Interface().(Words).empty() {
				paths = append(paths, name)
			}
		case field.Type == wordsPtrType:
			if !value.IsNil() && !value.Elem().Interface().(Words).empty() {
				paths = append(paths, name)
			}
		case field.Tag.Get("texts") == wordsTag:
			if value.Kind() == reflect.String && value.Len() > 0 {
				paths = append(paths, name)
			}
		default:
			paths = wordsPathsIn(paths, value, name)
		}
	}
	return paths
}

// Bound cuts text to its first limit UTF-16 units. It returns that head and the count of
// UTF-16 units it cut off. A character outside the Basic Multilingual Plane counts two units,
// and Bound never splits it: where its two units would cross the limit, the head leaves it out.
// A byte that is not valid UTF-8 decodes as U+FFFD, one unit, as a JSON decoder reads it.
func Bound(text string, limit int) (head string, rest int) {
	units := 0
	for i, r := range text {
		n := utf16.RuneLen(r)
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

// Block draws w in a block of its own, the block that carries another author's words after a
// text's fields (dispatch://INFRA-3686/spec R1). The tag is name, or, where the words hold <name
// or </name, the first of name-1, name-2 and on whose opening and closing tags the words do not
// hold, so the words can neither open nor close an element of the block's tag. The opening tag
// carries the attributes by, at, field, rest and more, in that order, each only where it is set.
// Then come a new line, the words exactly as given, a new line and the closing tag.
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

// blockTag is the first name of name, name-1, name-2 and on such that text holds neither <tag
// nor </tag. A plain substring test: words that hold <name-1 or <names also step past name.
func blockTag(name, text string) string {
	if !holdsTag(text, name) {
		return name
	}
	for n := 1; ; n++ {
		tag := name + "-" + strconv.Itoa(n)
		if !holdsTag(text, tag) {
			return tag
		}
	}
}

// holdsTag says whether text holds <tag or </tag.
func holdsTag(text, tag string) bool {
	for {
		i := strings.IndexByte(text, '<')
		if i < 0 {
			return false
		}
		text = text[i+1:]
		if strings.HasPrefix(strings.TrimPrefix(text, "/"), tag) {
			return true
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

// utf16Length is the count of UTF-16 units of text, each byte that is not valid UTF-8 one unit
// (U+FFFD).
func utf16Length(text string) int {
	n := 0
	for _, r := range text {
		n += utf16.RuneLen(r)
	}
	return n
}

// formatTime writes t as the texts write a time: RFC 3339 in UTC, with the fraction of a second
// only where t has one.
func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}
