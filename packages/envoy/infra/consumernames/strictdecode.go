package consumernames

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"reflect"
	"slices"
	"strings"

	selection "github.com/sjawhar/envoy/consumernames"
)

// The selection bucket's values are read strictly. Any principal whose grant holds a JetStream API
// request can store the server's JSON answer under any key of the bucket, by naming the key's
// subject ($KV.<bucket>.<key>) as the request's reply subject: a STREAM.INFO answer stored under a
// session's key decodes leniently (json.Unmarshal) as an empty record, which selects nothing, and
// a reader would then remove the session's consumer as unrecorded. So every reader of the bucket
// takes a value only through readSelectionValue, which answers a value that is not exactly a value
// of its key's kind as malformed.

// strictRecord decodes data, a value under a session's or a durable's key, as exactly one
// selection.Record (strictDecode).
func strictRecord(data []byte) (selection.Record, error) {
	var record selection.Record
	if err := strictDecode(data, &record); err != nil {
		return selection.Record{}, fmt.Errorf("no selection record: %w", err)
	}
	return record, nil
}

// strictStoreState decodes data, the sentinel's value, as exactly one selection.StoreState
// (strictDecode).
func strictStoreState(data []byte) (selection.StoreState, error) {
	var state selection.StoreState
	if err := strictDecode(data, &state); err != nil {
		return selection.StoreState{}, fmt.Errorf("no store state: %w", err)
	}
	return state, nil
}

// strictBarrierPrefix begins every key of a hold's bucket barrier (holdrecord.Barrier).
const strictBarrierPrefix = "hold.barrier."

// strictBarrier refuses data under key, a hold's barrier key hold.barrier.<hold id>, unless it is
// exactly what holdrecord.Barrier puts there: the hold id itself, one subject token.
func strictBarrier(key string, data []byte) error {
	id, ok := strings.CutPrefix(key, strictBarrierPrefix)
	if !ok || id == "" || strings.ContainsAny(id, ".*> \t\r\n") {
		return fmt.Errorf("%q is no hold's barrier key", key)
	}
	if string(data) != id {
		return errors.New("no barrier value: a barrier's value is its hold id")
	}
	return nil
}

// selectionValue is one key's value of the selection bucket as readSelectionValue read it: a
// session's or a durable's record, or the sentinel's store state; a hold's barrier holds neither.
type selectionValue struct {
	record *selection.Record
	state  *selection.StoreState
}

// readSelectionValue reads key's value, whose last write is no delete or purge marker, by its
// key's kind: a session's (selection.CheckKey) or a durable's (selection.CheckDurableKey) record
// (strictRecord), the sentinel's store state (strictStoreState), or a hold's barrier, its hold id
// (strictBarrier). A key of no kind the bucket holds is refused whatever it holds: nothing writes
// one but a reply subject. An error is a malformed value, which every caller answers as UNKNOWN or
// refuses, naming the key, and acts on nothing.
//
// It is the one place the census, status, cleanup, restore, create-bucket and repair-sentinel
// decode a value of the bucket. Shape alone cannot refuse a well-formed record copied from one key
// onto another, which a holder of the bucket's direct get can do, so a value is also held to the
// selection package's own rule for the key it was read under (consumernames record.go, Record.check
// and StoreState.check, which its stores' Read and State apply): a record carries that key
// (selection.Record.Key), and the sentinel carries selection.SentinelKey and names the session
// family (selection.StoreState.Key, Family), the one family the bucket keeps. The package exports
// no decode of a value read raw, which a walk of the bucket reads, so the rule is written here
// beside the strict decode.
func readSelectionValue(key statusKey) (selectionValue, error) {
	switch {
	case selection.CheckKey(key.Key) == nil || selection.CheckDurableKey(key.Key) == nil:
		record, err := strictRecord(key.Value)
		if err != nil {
			return selectionValue{}, err
		}
		if record.Key != key.Key {
			return selectionValue{}, fmt.Errorf("no selection record of its own: it carries the key %q", record.Key)
		}
		return selectionValue{record: &record}, nil
	case key.Key == selection.SentinelKey:
		state, err := strictStoreState(key.Value)
		if err != nil {
			return selectionValue{}, err
		}
		if state.Key != selection.SentinelKey || state.Family != selection.SessionFamily {
			return selectionValue{}, fmt.Errorf("no sentinel of its own: it carries the key %q and names the family %q, not %s and %s",
				state.Key, state.Family, selection.SentinelKey, selection.SessionFamily)
		}
		return selectionValue{state: &state}, nil
	case strings.HasPrefix(key.Key, strictBarrierPrefix):
		return selectionValue{}, strictBarrier(key.Key, key.Value)
	}
	return selectionValue{}, errors.New("no key of a kind the bucket holds (a session's or a durable's record, the sentinel, a hold's barrier)")
}

// strictDecode decodes data into v, a pointer to a struct, as exactly one JSON object of v's type
// and nothing more: json.Decoder with DisallowUnknownFields, no data after the object, and then
// strictFields over the object, which also takes each key only by its field's exact name
// (DisallowUnknownFields matches a key to a field without regard to case) and requires each field
// whose tag has no omitempty, at every depth. Its errors name keys, never values.
func strictDecode(data []byte, v any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(v); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("data follows the JSON object")
	}
	return strictFields(data, reflect.TypeOf(v).Elem(), "the value")
}

// strictUnmarshaler is the interface of a type that decodes itself (time.Time): strictFields does
// not look inside its value, which its own UnmarshalJSON has read.
var strictUnmarshaler = reflect.TypeFor[json.Unmarshaler]()

// strictFields checks raw, a JSON value json.Decoder has already decoded into a value of type t,
// at where: a struct's value must be an object whose every key is one of its fields' exact names
// and which holds every field whose tag has no omitempty or omitzero, each checked in turn; a
// slice's items are checked one by one. A null is taken only where json.Marshal can write one (a
// pointer, a slice, a map, an interface). A struct with an embedded field is refused: the types
// strictDecode reads (selection.Record, selection.StoreState) have none.
func strictFields(raw []byte, t reflect.Type, where string) error {
	if string(bytes.TrimSpace(raw)) == "null" {
		switch t.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Map, reflect.Interface:
			return nil
		}
		return fmt.Errorf("%s is null", where)
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if reflect.PointerTo(t).Implements(strictUnmarshaler) {
		return nil
	}
	switch t.Kind() {
	case reflect.Struct:
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil {
			return fmt.Errorf("%s is no JSON object", where)
		}
		fields := map[string]reflect.Type{}
		for i := range t.NumField() {
			f := t.Field(i)
			if f.Anonymous {
				return fmt.Errorf("%s's type %s embeds %s, which the strict decode does not read", where, t, f.Name)
			}
			if !f.IsExported() {
				continue
			}
			name, options, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "-" {
				continue
			}
			if name == "" {
				name = f.Name
			}
			fields[name] = f.Type
			optional := slices.ContainsFunc(strings.Split(options, ","), func(o string) bool { return o == "omitempty" || o == "omitzero" })
			if _, held := object[name]; !held && !optional {
				return fmt.Errorf("%s holds no %q", where, name)
			}
		}
		for _, key := range slices.Sorted(maps.Keys(object)) {
			ft, ok := fields[key]
			if !ok {
				return fmt.Errorf("%s holds %q, which is no field's exact name", where, key)
			}
			if err := strictFields(object[key], ft, where+"."+key); err != nil {
				return err
			}
		}
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			return nil
		}
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return fmt.Errorf("%s is no JSON array", where)
		}
		for i, item := range items {
			if err := strictFields(item, t.Elem(), fmt.Sprintf("%s[%d]", where, i)); err != nil {
				return err
			}
		}
	}
	return nil
}
