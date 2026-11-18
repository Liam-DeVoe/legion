package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// PostgreSQL's text and jsonb cannot hold U+0000, so a statement given caller text holding one
// fails, and a route that reached that statement answered a 500 naming nothing the caller could
// fix. The decoder of every request body (decodeJSON) refuses such text before the route reads
// it, naming where the caller wrote the character. A member a route decodes into a Go string holds
// no other text PostgreSQL refuses, since decoding writes each byte that is not UTF-8, and each
// lone surrogate escape, as U+FFFD. A json.RawMessage or []byte member keeps the bytes or the
// escapes as sent, and no body a route of this package decodes has one: a route that adds one, to
// store it, holds it to UTF-8 and to no lone surrogate itself.

// nulEscape is the only way JSON carries U+0000: a decoder refuses the raw control character.
var nulEscape = []byte(`\u0000`)

// unstorableJSON refuses the first string of the JSON value body holds - a member name or a
// value, read in the order the body writes them - that holds U+0000, naming it by its place in
// the body: planReason, tasks[0].text, or "body" for a bare string. It names the place and never
// the string, since a body can carry a secret (refuseBadBody). A body without the escape is passed
// over unread. Every string is read from the body's tokens, a repeated member's every copy among
// them, rather than from a value decoded again: a route's decoder keeps a string a later copy does
// not replace (a null leaves a string field as it was, an object merges into a struct field), and
// a map keeps only the last copy, so a check of the map would pass over the string the route acts
// on. A number is read as its text (UseNumber), so a number no route parses, in a member a lenient
// route ignores, refuses nothing here. The body decoded already (decodeJSON), so a token that does
// not read is refused with the tokenizer's error rather than passed over.
func unstorableJSON(body []byte) error {
	if !bytes.Contains(body, nulEscape) {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	return unstorableJSONValue("", decoder)
}

// unstorableJSONValue reads the next value of decoder, which stands at path in the body, and
// refuses its first string that holds U+0000.
func unstorableJSONValue(path string, decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	switch token {
	case json.Delim('['):
		for index := 0; decoder.More(); index++ {
			if err := unstorableJSONValue(fmt.Sprintf("%s[%d]", path, index), decoder); err != nil {
				return err
			}
		}
	case json.Delim('{'):
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			// Token returns an object's key as a string, or the error returned above: in the
			// state before a member's name it reads a '"' as the name and refuses any other byte.
			name := key.(string)
			// A name is written with each U+0000 in it as the six characters \u0000, so no
			// refusal sends the character back.
			member := strings.ReplaceAll(name, "\x00", `\u0000`)
			if path != "" {
				member = path + "." + member
			}
			if err := unstorableText(member, name); err != nil {
				return err
			}
			if err := unstorableJSONValue(member, decoder); err != nil {
				return err
			}
		}
	default:
		if text, ok := token.(string); ok {
			if path == "" {
				path = "body"
			}
			return unstorableText(path, text)
		}
		return nil
	}
	// The closing delimiter of the array or the object.
	_, err = decoder.Token()
	return err
}

// unstorableText refuses value when it holds U+0000, naming place and where the first one
// stands, counted from 1 in code points as the daemon's caps count (planrating.ReasonMax).
func unstorableText(place, value string) error {
	at := strings.IndexByte(value, 0)
	if at < 0 {
		return nil
	}
	return fmt.Errorf("%s holds a NUL character (U+0000) at character %d, which the daemon cannot store", place, utf8.RuneCountInString(value[:at])+1)
}
