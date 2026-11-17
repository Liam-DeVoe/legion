package specgraph

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

// decodeJSON decodes data, one JSON value, into v, and form is the format as a person reads it,
// such as approvedForm, which it names for a value of another type. It is the one reader of every
// JSON file of a format of this package (approved.json, ApprovedFile, and each JSON file of a
// scenario), and it refuses each text that the format does not hold:
//
//   - a byte that is not UTF-8 (RFC 8259 §8.1: JSON text "MUST be encoded using UTF-8"): "it is not
//     UTF-8";
//   - a member that v's type does not name, also one whose name differs from the format's in the
//     case of a letter, since encoding/json/v2 matches a name to a field with regard to case:
//     "member "<JSON pointer>" is not in the format";
//   - a member name that one object gives two times: "member "<JSON pointer>" is in one object two
//     times";
//   - no value, or text after the value: "it holds no JSON value", "text after the JSON value";
//   - a value of another type than the form needs, such as a string in the place of a number or a
//     time that time.Time does not read as RFC 3339: "it is not in the form <form>: the value at
//     "<JSON pointer>" is of another type", and "it is not in the form <form>" for the whole text.
//     The words are the package's, since encoding/json/v2 names a Go type, and picks its verb,
//     "cannot" or "unable to", once in each process;
//   - a text that is not JSON, in the words of encoding/json/jsontext, which name the place too.
//
// A map of the format, such as the paths of a commit's tree, takes any name, in its own letters,
// one time. The report of go test -json is no format of this package: ReadGoTestJSON reads it as
// go test writes it, and passes over the members that it does not name.
func decodeJSON(data []byte, v any, form string) error {
	if !utf8.Valid(data) {
		return errors.New("it is not UTF-8")
	}
	dec := jsontext.NewDecoder(bytes.NewReader(data))
	err := json.UnmarshalDecode(dec, v, json.RejectUnknownMembers(true))
	var semantic *json.SemanticError
	var syntactic *jsontext.SyntacticError
	switch {
	case errors.Is(err, io.EOF):
		return errors.New("it holds no JSON value")
	case errors.As(err, &semantic) && errors.Is(err, json.ErrUnknownName):
		return fmt.Errorf("member %q is not in the format", semantic.JSONPointer)
	case errors.As(err, &semantic) && semantic.JSONPointer == "":
		return fmt.Errorf("it is not in the form %s", form)
	case errors.As(err, &semantic):
		return fmt.Errorf("it is not in the form %s: the value at %q is of another type", form, semantic.JSONPointer)
	case errors.As(err, &syntactic) && errors.Is(err, jsontext.ErrDuplicateName):
		return fmt.Errorf("member %q is in one object two times", syntactic.JSONPointer)
	case err != nil:
		return err
	}
	if _, err := dec.ReadToken(); !errors.Is(err, io.EOF) {
		return errors.New("text after the JSON value")
	}
	return nil
}

// decodeJSONFrom reads all of r and decodes it with decodeJSON.
func decodeJSONFrom(r io.Reader, v any, form string) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	return decodeJSON(data, v, form)
}
