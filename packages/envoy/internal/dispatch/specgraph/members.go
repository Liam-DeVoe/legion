package specgraph

import (
	"encoding/json/v2"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// A member of an input type of the derivation, a type of a file of a scenario (ReadScenario) or of
// an Input (Derive, CheckStates), a FixtureHistory or a Unit (WaitCycle), is read as its type marks
// it with the tag input, since a member that its writer left out is its zero value, and a rule that
// read that value as a real one would turn a state toward true or hide a failure. Each member has
// one of four marks, and a test fails for a member with none (TestEachMemberOfAnInputHasAMark):
//
//   - input:"nonzero": its zero value is no value of the format, so the validation that the reader
//     and the entry of the derivation share refuses it (Result.fault, JobRun.fault, subjectFault,
//     ApprovedNode.check, FixtureHistory.check, Input.check, unitsFault), and a file that leaves it
//     out is refused too, as its zero value;
//   - input:"present: <reason>": its zero value is a value of the format, as the reason says, such
//     as false for removed, so a file must set it (unsetFault), and to a caller of the library the
//     zero value is that value;
//   - input:"zero: <reason>": its zero value is a value of the format, as the reason says, and a
//     file that leaves it out sets that value;
//   - input:"one of <member>: <reason>": of it and the other member, exactly one is set, and the
//     validation refuses neither and both, as the reason says.
type memberMark struct {
	kind  string // "nonzero", "present", "zero" or "one of"
	other string // the other member of a "one of" mark, by its JSON name
}

// markOf is the mark of the member f, or an error that names what the tag lacks: a kind, or a
// reason after a mark that needs one.
func markOf(f reflect.StructField) (memberMark, error) {
	tag := f.Tag.Get("input")
	if tag == "nonzero" {
		return memberMark{kind: tag}, nil
	}
	head, reason, _ := strings.Cut(tag, ": ")
	if !hasWord(reason) {
		return memberMark{}, fmt.Errorf("member %s has the mark %q, not nonzero or a mark with its reason", f.Name, tag)
	}
	switch {
	case head == "present" || head == "zero":
		return memberMark{kind: head}, nil
	case strings.HasPrefix(head, "one of ") && hasWord(strings.TrimPrefix(head, "one of ")):
		return memberMark{kind: "one of", other: strings.TrimPrefix(head, "one of ")}, nil
	}
	return memberMark{}, fmt.Errorf("member %s has the mark %q, not nonzero, present, zero or one of", f.Name, tag)
}

// inputMember is one member of an input type, as its JSON text names it: its name, its field and
// the path of the field's index from the type.
type inputMember struct {
	name  string
	field reflect.StructField
	index []int
}

// inputMembers is each member of the struct type t, in the order of its fields, a member of an
// embedded struct with no JSON name of its own in its place, as encoding/json/v2 reads it inline.
// A member that JSON does not hold (json:"-") is a member too, with no name.
func inputMembers(t reflect.Type) []inputMember {
	var members []inputMember
	for _, f := range reflect.VisibleFields(t) {
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if f.Anonymous && name == "" && f.Type.Kind() == reflect.Struct {
			continue // its own members follow it in VisibleFields
		}
		if name == "" {
			name = f.Name
		}
		if name == "-" {
			name = ""
		}
		members = append(members, inputMember{name: name, field: f, index: f.Index})
	}
	return members
}

// unsetFault is the refusal of the first member of a value of v's type that data, a text that
// decodeJSON took into v in the form form, does not set, of each member that its type marks
// present: one that data leaves out or gives as null, which the decode reads as its zero value. The
// zero value of such a member is a real value of the form, as false is for removed, so only the
// text tells a member that its writer left out from one that it set. It names the place of the
// member as decodeJSON names a place, and nil when data sets each.
func unsetFault(data []byte, v any, form string) error {
	var text any
	if err := json.Unmarshal(data, &text); err != nil {
		return err
	}
	if place, unset := unsetPlace(text, reflect.TypeOf(v), ""); unset {
		return fmt.Errorf("it is not in the form %s: the value at %s is not set", form, quoted(place))
	}
	return nil
}

// unsetPlace is the JSON pointer of the first member marked present that text, a value of type t,
// does not set, and whether there is one.
func unsetPlace(text any, t reflect.Type, place string) (string, bool) {
	switch t.Kind() {
	case reflect.Pointer:
		return unsetPlace(text, t.Elem(), place)
	case reflect.Slice:
		items, _ := text.([]any)
		for i, item := range items {
			if p, unset := unsetPlace(item, t.Elem(), place+"/"+strconv.Itoa(i)); unset {
				return p, true
			}
		}
	case reflect.Map:
		members, _ := text.(map[string]any)
		for _, key := range slices.Sorted(maps.Keys(members)) {
			if p, unset := unsetPlace(members[key], t.Elem(), place+"/"+pointerToken.Replace(key)); unset {
				return p, true
			}
		}
	case reflect.Struct:
		members, isObject := text.(map[string]any)
		if !isObject || t.PkgPath() != reflect.TypeFor[memberMark]().PkgPath() {
			return "", false
		}
		for _, m := range inputMembers(t) {
			if m.name == "" {
				continue
			}
			value := members[m.name]
			if mark, _ := markOf(m.field); value == nil && mark.kind == "present" {
				return place + "/" + pointerToken.Replace(m.name), true
			}
			if p, unset := unsetPlace(value, m.field.Type, place+"/"+pointerToken.Replace(m.name)); unset {
				return p, true
			}
		}
	}
	return "", false
}

// pointerToken writes a name as a token of a JSON pointer (RFC 6901 §3).
var pointerToken = strings.NewReplacer("~", "~0", "/", "~1")
