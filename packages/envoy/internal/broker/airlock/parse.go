package airlock

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Decode reads data, a file of one JSON object, into v: Rules, or the profiles file the broker
// reads its proxy blocks from (policy.ParseProfiles), whose blocks are this package's entries
// under a profile's name. It is the one strict reader of those files, so each refuses the same
// faults in the same words.
//
// A field v does not know is refused rather than ignored, so a broker from before a new field
// refuses a file that uses it instead of serving the file with that field's rule missing.
// Anything after the file's one JSON object but white space is refused for the same reason: a
// second value no reader applies would be a rule its author believes is in force, and an object
// that ends at a stray bracket is one its author meant to go on. A file that is null, or any JSON
// value but an object, is refused too: encoding/json reads a null into v as nothing at all, which
// would serve the file as one of no entries. Decode does not validate what it read; the caller
// does (Validate, for Rules).
func Decode(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return parseError(data, err)
	}
	// The value decoded, so it is valid JSON, and v is a struct, into which every value but an
	// object and null is a type error above: a value that starts with "n" here is the literal null.
	if value := bytes.TrimLeft(data, " \t\r\n"); value[0] == 'n' {
		return errors.New("the file is null: it holds no JSON object")
	}
	// More steps over the white space after the object, so the offset is where whatever follows
	// it starts. Token then reads that, and answers io.EOF only when nothing does: a second value
	// and a stray closing bracket alike are something.
	dec.More()
	at := dec.InputOffset() + 1
	if _, err := dec.Token(); err != io.EOF {
		return fmt.Errorf("the file goes on after its one JSON object, at %s", position(data, at))
	}
	return nil
}

// parseError names the place the decoder stopped at, which is what a person needs to find
// it in the file: the byte offset encoding/json counts, and the line and column that offset
// falls on. An unknown field carries no offset in Go's error, so there that error's own
// text is the whole report. A file that ends before its object does carries none either, and
// says which of the two it is: empty, or cut off inside the object.
func parseError(data []byte, err error) error {
	var syntax *json.SyntaxError
	var wrongType *json.UnmarshalTypeError
	switch {
	case errors.As(err, &syntax):
		return fmt.Errorf("not JSON: %s, at %s", syntax, position(data, syntax.Offset))
	case errors.As(err, &wrongType):
		field := wrongType.Field
		if field == "" {
			return fmt.Errorf("the file is a JSON %s, not one JSON object, at %s", wrongType.Value, position(data, wrongType.Offset))
		}
		return fmt.Errorf("%s is not a %s, at %s", field, wrongType.Type, position(data, wrongType.Offset))
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		return errors.New(strings.TrimPrefix(err.Error(), "json: "))
	case errors.Is(err, io.EOF):
		return errors.New("the file is empty: it holds no JSON object")
	case errors.Is(err, io.ErrUnexpectedEOF):
		return fmt.Errorf("not JSON: the file ends inside its JSON object, at %s", position(data, int64(len(data))+1))
	default:
		return fmt.Errorf("not one JSON object: %s", err)
	}
}

// position reads a decoder offset as "byte offset N, line L, column C". encoding/json
// counts the bytes it had read when it stopped, so the offset is the one-based index of the
// byte it stopped on. A column counts bytes, not characters, as the offset does.
func position(data []byte, offset int64) string {
	end := offset - 1
	if end < 0 {
		end = 0
	}
	if end > int64(len(data)) {
		end = int64(len(data))
	}
	line, column := 1, 1
	for _, b := range data[:end] {
		if b == '\n' {
			line, column = line+1, 1
		} else {
			column++
		}
	}
	return fmt.Sprintf("byte offset %d, line %d, column %d", offset, line, column)
}
