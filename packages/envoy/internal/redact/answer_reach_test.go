package redact_test

import (
	"encoding/json"
	"fmt"
	"net/http"
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
