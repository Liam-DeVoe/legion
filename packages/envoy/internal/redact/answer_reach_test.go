package redact_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/redact"
	"github.com/sjawhar/envoy/internal/redact/redacttest"
)

// refusedWith is a refusal whose message echoes the request's bearer.
func refusedWith(auth string) string { return `{"message":"the token ` + auth + ` was refused"}` }

// echoed is an embedded unexported struct whose exported member encoding/json promotes into the
// struct that embeds it.
type echoed struct {
	Message redact.RemoteText `json:"message"`
}

// TestDecodeHoldsABoxPromotedFromAnEmbeddedUnexportedStruct: encoding/json decodes the exported
// members of an embedded struct whatever the embedded type's own export, by value or behind a
// pointer the caller set, so a box promoted that way is another party's words like any other and
// prints with the request's bearer redacted. Captures redact.answer-embedded/<case>.
func TestDecodeHoldsABoxPromotedFromAnEmbeddedUnexportedStruct(t *testing.T) {
	answer := answerOf(t, http.StatusForbidden, refusedWith)
	var byValue struct{ echoed }
	if err := answer.Decode(&byValue); err != nil {
		t.Fatal(err)
	}
	byPointer := struct{ *echoed }{&echoed{}}
	if err := answer.Decode(&byPointer); err != nil {
		t.Fatal(err)
	}
	// Control: encoding/json filled both, so a print of them is a print of the answer's member.
	if !byValue.Message.Equal("the token Bearer "+redacttest.Token+" was refused") || !byPointer.Message.Equal("the token Bearer "+redacttest.Token+" was refused") {
		t.Fatal("control: Decode did not fill the promoted member, so this test reads nothing")
	}
	redacttest.RequireNone(t, "redact.answer-embedded/value", byValue.Message.String())
	redacttest.RequireNone(t, "redact.answer-embedded/pointer", byPointer.Message.String())
}

// said is the box under an unexported name of this package's own. Embedded, it is an embedded
// field of an unexported name, whose address reflect hands out only as a value that cannot be
// used, and the box's UnmarshalJSON is promoted to the struct that embeds it, so encoding/json
// decodes a JSON string body whole into it.
type said = redact.RemoteText

// Said is the same box under an exported name, which reflect addresses.
type Said = redact.RemoteText

// TestDecodeRefusesABoxEmbeddedUnderAnUnexportedName: a destination that embeds the box itself
// under an unexported name holds a box no record can reach, since reflect gives no usable address
// of such a field to call the box's method through. Decode refuses it with fixed text that names
// no word of the answer, where it panicked. The control embeds the same box under an exported
// name, which Decode fills and records, so it prints with the request's bearer redacted.
func TestDecodeRefusesABoxEmbeddedUnderAnUnexportedName(t *testing.T) {
	answer := answerOf(t, http.StatusForbidden, func(auth string) string { return strconv.Quote("the token " + auth + " was refused") })
	var unexported struct{ said }
	err := answer.Decode(&unexported)
	if err == nil {
		t.Fatal("Decode filled a box embedded under an unexported name, which no record of the answer reaches")
	}
	if strings.Contains(err.Error(), redacttest.Token) || strings.Contains(err.Error(), "was refused") {
		t.Errorf("Decode's refusal %q quotes the answer", err)
	}
	var exported struct{ Said }
	if err := answer.Decode(&exported); err != nil {
		t.Fatalf("control: Decode of the box embedded under an exported name = %v, want it filled", err)
	}
	if !exported.Said.Equal("the token Bearer " + redacttest.Token + " was refused") {
		t.Fatal("control: Decode did not fill the embedded box, so this test reads nothing")
	}
	redacttest.RequireNone(t, "redact.answer-embedded/exported-alias", exported.Said.String())
}

// TestDecodeHoldsABoxBehindAnInterfaceTheCallerSet: encoding/json decodes a member into the value
// an interface field points at where the caller set a non-nil pointer there first (its indirect),
// so a box behind it is the answer's words like any other and prints with the request's bearer
// redacted. Captures redact.answer-interface.
func TestDecodeHoldsABoxBehindAnInterfaceTheCallerSet(t *testing.T) {
	answer := answerOf(t, http.StatusForbidden, func(auth string) string { return `{"detail":` + refusedWith(auth) + `}` })
	detail := &echoed{}
	var envelope struct {
		Detail any `json:"detail"`
	}
	envelope.Detail = detail
	if err := answer.Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	// Control: encoding/json decoded into the caller's pointer rather than a map of its own.
	if envelope.Detail != any(detail) || !detail.Message.Equal("the token Bearer "+redacttest.Token+" was refused") {
		t.Fatalf("control: Decode filled %#v, not the caller's pointer, so this test reads nothing", envelope.Detail)
	}
	redacttest.RequireNone(t, "redact.answer-interface", detail.Message.String())
}

// TestAnAnswerRefusesAWholeBodyDestinationBehindPointers: a pointer to a pointer to a
// json.RawMessage, an interface value, a string or a byte slice takes the body whole as plain
// text as the pointer to it does, since encoding/json allocates each pointer it meets; Decode
// refuses each. The control is a struct behind the same two pointers, which it fills.
func TestAnAnswerRefusesAWholeBodyDestinationBehindPointers(t *testing.T) {
	answer := answerOf(t, http.StatusOK, refusedWith)
	var raw *json.RawMessage
	var a *any
	var s **string
	var b *[]byte
	for name, into := range map[string]any{"raw": &raw, "any": &a, "string": &s, "bytes": &b} {
		if err := answer.Decode(into); err == nil {
			t.Errorf("Decode filled a pointer to a %s with the body whole", name)
		}
	}
	printed := ""
	if raw != nil {
		printed += string(*raw)
	}
	if a != nil {
		printed += fmt.Sprint(*a)
	}
	redacttest.RequireNone(t, "redact.answer-whole-behind-pointers", printed)
	var control *echoed
	if err := answer.Decode(&control); err != nil || control == nil || control.Message.IsEmpty() {
		t.Fatalf("control: Decode of a struct behind two pointers = %v, %+v; want it filled", err, control)
	}
}

// boxedKey is a map key that holds the box behind a pointer it embeds, as a key of a struct type
// may: its UnmarshalText is the box's own, promoted through the pointer, which is nil in the zero
// key encoding/json makes, so encoding/json panicked decoding into it.
type boxedKey struct{ *redact.RemoteText }

// printedKeys is each key of every map v holds - through a pointer, an interface value, a list or
// a struct's member - as fmt prints it.
func printedKeys(v reflect.Value) string {
	var b strings.Builder
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			b.WriteString(printedKeys(v.Elem()))
		}
	case reflect.Slice:
		for i := range v.Len() {
			b.WriteString(printedKeys(v.Index(i)))
		}
	case reflect.Struct:
		for i := range v.NumField() {
			b.WriteString(printedKeys(v.Field(i)))
		}
	case reflect.Map:
		for _, key := range v.MapKeys() {
			fmt.Fprintf(&b, "%v ", key.Interface())
		}
	}
	return b.String()
}

// TestDecodeRefusesADestinationWhoseMapKeyHoldsABox: encoding/json fills a map's key through the
// key's UnmarshalText, which the box has, and a map's key has no address, so no record of the
// answer reaches a box there: a key that echoes the request's bearer prints it as it came. The box
// itself, and every type that holds it, is no map key (it holds its record behind a func, which
// makes it not comparable), but a key can hold it behind a pointer. Decode refuses such a
// destination before it decodes anything - a map keyed by a pointer to the box or to a
// RemoteNumber, a struct's member, a list of such maps, one behind a nil pointer, one behind an
// interface value the caller set to a pointer, and a struct key that embeds a pointer to the box,
// into which encoding/json panicked - with fixed text that quotes no word of the answer, and the
// destination holds no key. The control is the same answer decoded with the key as a plain string,
// which encoding/json fills. Captures redact.answer-box-key/<case>.
func TestDecodeRefusesADestinationWhoseMapKeyHoldsABox(t *testing.T) {
	for _, box := range []reflect.Type{reflect.TypeFor[redact.RemoteText](), reflect.TypeFor[redact.RemoteNumber]()} {
		if box.Comparable() {
			t.Errorf("%v is comparable, so a map can key on it, where no record reaches it", box)
		}
	}
	keyed := func(auth string) string { return `{"the token ` + auth + ` was refused":1}` }
	member := func(auth string) string { return `{"codes":` + keyed(auth) + `}` }
	listed := func(auth string) string { return `{"codes":[` + keyed(auth) + `]}` }
	var top map[*redact.RemoteText]int
	var field struct {
		Codes map[*redact.RemoteText]int `json:"codes"`
	}
	var number map[*redact.RemoteNumber]int
	var list struct {
		Codes []map[*redact.RemoteText]int `json:"codes"`
	}
	var nilPointer struct {
		Codes *map[*redact.RemoteText]int `json:"codes"`
	}
	set := map[*redact.RemoteText]int{}
	var behind struct {
		Codes any `json:"codes"`
	}
	behind.Codes = &set
	var embedded map[boxedKey]int
	for _, c := range []struct {
		name   string
		answer func(auth string) string
		into   any
	}{
		{"map", keyed, &top},
		{"member", member, &field},
		{"number", keyed, &number},
		{"list", listed, &list},
		{"nil-pointer", member, &nilPointer},
		{"interface", member, &behind},
		{"embedded-pointer", keyed, &embedded},
	} {
		t.Run(c.name, func(t *testing.T) {
			answer := answerOf(t, http.StatusForbidden, c.answer)
			err := func() (err error) {
				defer func() {
					if p := recover(); p != nil {
						err = fmt.Errorf("Decode panicked: %v", p)
					}
				}()
				return answer.Decode(c.into)
			}()
			printed := printedKeys(reflect.ValueOf(c.into))
			redacttest.RequireNone(t, "redact.answer-box-key/"+c.name, fmt.Sprintf("%s| %v", printed, err))
			switch {
			case err == nil:
				t.Errorf("Decode filled %s, whose map's key holds a box no record of the answer reaches: %s", c.name, printed)
			case strings.Contains(err.Error(), "was refused") || !strings.Contains(err.Error(), "map's key"):
				t.Errorf("Decode's refusal %q quotes the answer or does not name the map's key", err)
			case printed != "":
				t.Errorf("Decode refused %s after it filled it: %s", c.name, printed)
			}
		})
	}
	var control map[string]int
	if err := answerOf(t, http.StatusForbidden, keyed).Decode(&control); err != nil || len(control) != 1 {
		t.Fatalf("control: Decode of the same answer keyed by a plain string = %v, %v; want it filled", err, control)
	}
}
