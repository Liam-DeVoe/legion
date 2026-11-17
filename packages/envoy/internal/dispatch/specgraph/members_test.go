package specgraph

import (
	"bytes"
	"encoding/json/v2"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// scenarioFileTypes is each JSON file of a scenario with the type that ReadScenario reads it into.
var scenarioFileTypes = map[string]reflect.Type{
	"approved.json": reflect.TypeFor[ApprovedFile](),
	"subjects.json": reflect.TypeFor[[]ScenarioSubject](),
	"history.json":  reflect.TypeFor[FixtureHistory](),
	"ties.json":     reflect.TypeFor[map[string]map[string][]string](),
	"results.json":  reflect.TypeFor[[]scenarioResult](),
	"jobs.json":     reflect.TypeFor[[]JobRun](),
	"uncited.json":  reflect.TypeFor[map[string]map[string]string](),
	"units.json":    reflect.TypeFor[[]Unit](),
}

// libraryInputTypes is the type of each input that a caller of the library gives: an Input of
// Derive and CheckStates, the FixtureHistory that a scenario gives as its History, and the units of
// WaitCycle.
var libraryInputTypes = []reflect.Type{reflect.TypeFor[Input](), reflect.TypeFor[FixtureHistory](), reflect.TypeFor[[]Unit]()}

// structTypes is each struct type of this package that a value of type t holds, t itself first,
// through pointers, slices, maps and members. It does not go into Tree, the spec files of
// Input.Files, which Parse gives from the text of the files and Validate judges: a tree that the
// rules refuse reads unknown (refusals), and its nodes are blocks of the files, which no file of a
// scenario writes as members.
func structTypes(t reflect.Type, types []reflect.Type) []reflect.Type {
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Map:
		return structTypes(t.Elem(), types)
	case reflect.Struct:
		if t.PkgPath() != reflect.TypeFor[Input]().PkgPath() || t == reflect.TypeFor[Tree]() || slices.Contains(types, t) {
			return types
		}
		types = append(types, t)
		for _, m := range inputMembers(t) {
			types = structTypes(m.field.Type, types)
		}
	}
	return types
}

// inputTypes is each struct type that a file of a scenario or an input of the library holds.
func inputTypes() []reflect.Type {
	var types []reflect.Type
	for _, name := range slices.Sorted(maps.Keys(scenarioFileTypes)) {
		types = structTypes(scenarioFileTypes[name], types)
	}
	for _, t := range libraryInputTypes {
		types = structTypes(t, types)
	}
	return types
}

// A member that a writer leaves out is its zero value, and a rule that read that value as a real
// one would turn a state toward true or hide a failure. So each member of each input type has a
// mark (memberMark): nonzero, present or zero, each but nonzero with its reason, or one of two
// members, which marks the other one back. A member with no mark, or with a mark of another form,
// fails this test, and so does a JSON file of a scenario that the test reads no type of.
func TestEachMemberOfAnInputHasAMark(t *testing.T) {
	t.Parallel()
	var files []string
	for _, name := range append(slices.Clone(scenarioRequired), scenarioOptional...) {
		if strings.HasSuffix(name, ".json") {
			files = append(files, name)
		}
	}
	if got := slices.Sorted(maps.Keys(scenarioFileTypes)); !slices.Equal(got, slices.Sorted(slices.Values(files))) {
		t.Errorf("the test reads the types of %q, and a scenario holds the JSON files %q", got, files)
	}
	types, marked := inputTypes(), 0
	for _, typ := range types {
		members := inputMembers(typ)
		for _, m := range members {
			mark, err := markOf(m.field)
			if err != nil {
				t.Errorf("%s: %v", typ, err)
				continue
			}
			marked++
			if mark.kind != "one of" {
				continue
			}
			i := slices.IndexFunc(members, func(o inputMember) bool { return o.name == mark.other })
			if i < 0 {
				t.Errorf("%s: member %s is one of %s, which %s does not hold", typ, m.field.Name, mark.other, typ)
				continue
			}
			if other, err := markOf(members[i].field); err != nil || other.kind != "one of" || other.other != m.name {
				t.Errorf("%s: member %s is one of %s, which is not one of %s (%+v, %v)", typ, m.field.Name, mark.other, m.name, other, err)
			}
		}
	}
	if len(types) < 12 || marked < 50 {
		t.Errorf("%d types and %d members, which is fewer than the inputs hold: %v", len(types), marked, types)
	}
}

// memberBase is a case in which each member of each input type is set, and set in two elements of
// its list or more where the member is in a list: T, L and C of TestCheckState, with T approved
// two times; Q, a program check of production, and G, a query of the graph; at c1, whose history
// holds c2 and c3 after it, a failing run of each check before its pass, a caught failing example of
// each, and a failing example of C that missed its fault; the runs of C's job in two legs and of
// Q's in one; at the commit that production runs at deploy d1 and transaction tx1. subjects are the
// subjects of its scenario, two at the head of a branch and two at a deploy, and units two units.
func memberBase(t *testing.T) (s *checkCase, in Input, subjects []ScenarioSubject, units []Unit) {
	t.Helper()
	s = newCheckCase(t)
	q, g := checkStateProductionProgram, checkStateGraph
	s.add(q)
	s.add(g)
	earlier := checkStateTop
	earlier.Text = "Envoy delivers each message to its session."
	s.approveEarlier(checkStateApproved.Add(-24*time.Hour), earlier)
	s.commit("c2", "c1", "docs/x.md")
	s.commit("c3", "c2", "docs/x.md")
	run := checkStateRun
	fault := func(c Check, outcome Outcome, author string, after time.Duration) Result {
		return Result{Check: c.ID, Kind: ResultFailingExample, Outcome: outcome, At: run.Add(after), FaultAuthor: author, TestAuthors: []string{"author-a"}}
	}
	missed, caught := fault(checkStateCheck, OutcomePass, "author-c", 6*time.Minute), fault(checkStateCheck, OutcomeFail, "author-b", 5*time.Minute)
	for _, r := range []*Result{&missed, &caught} {
		r.Commit, r.Job, r.Leg, r.Attempt = "c1", "go-checks", "linux", 1
	}
	qFault := fault(q, OutcomeFail, "author-b", 5*time.Minute)
	qFault.TestCommit = "c1"
	qRun := func(outcome Outcome, attempt int, after time.Duration) Result {
		return Result{Check: q.ID, Kind: ResultRun, Outcome: outcome, Deploy: "d1", Job: q.Test.Job, Leg: "linux", Attempt: attempt, TestCommit: "c1", At: run.Add(after)}
	}
	gRun := func(outcome Outcome, after time.Duration) Result {
		return Result{Check: g.ID, Kind: ResultRun, Outcome: outcome, GraphTx: "tx1", At: run.Add(after)}
	}
	s.Results = []Result{
		legRun("c1", "linux", 2, OutcomeFail, run.Add(time.Minute)), legRun("c1", "linux", 1, OutcomePass, run), caught, missed,
		qRun(OutcomeFail, 2, time.Minute), qRun(OutcomePass, 1, 0), qFault,
		gRun(OutcomeFail, time.Minute), gRun(OutcomePass, 0), {Check: g.ID, Kind: ResultFailingExample, Outcome: OutcomeFail, At: run.Add(5 * time.Minute)},
	}
	s.JobRuns = []JobRun{{Job: "go-checks", Leg: "linux", Commit: "c1", Ended: true}, {Job: "go-checks", Leg: "arm", Commit: "c1", Ended: true},
		{Job: q.Test.Job, Leg: "linux", Commit: "c1", Ended: true}}
	s.Subject = Point{Commit: "c1", Deploy: "d1", GraphTx: "tx1"}
	in = s.input()
	subjects = []ScenarioSubject{
		{Point: Point{Commit: "c1", Branch: "b"}, Now: in.Now}, {Point: Point{Commit: "c1", Branch: "main"}, Now: in.Now},
		{Point: in.Subject, Now: in.Now}, {Point: Point{Commit: "c1", Deploy: "d2", GraphTx: "tx2"}, Now: in.Now},
	}
	units = []Unit{
		{ID: "u-a", Serves: []string{checkStateLeaf.ID}, Needs: []string{}, WaitsOn: []string{checkStateTop.ID}},
		{ID: "u-b", Serves: []string{checkStateTop.ID}, Needs: []string{"u-a"}, WaitsOn: []string{}},
	}
	return s, in, subjects, units
}

// memberScenario is memberBase as the files of a scenario, each written by the type that
// ReadScenario reads it into: the approved spec by WriteApproved, and each other file as
// encoding/json/v2 writes its type, so each member of each element is in the file.
func memberScenario(t *testing.T) fstest.MapFS {
	t.Helper()
	s, in, subjects, units := memberBase(t)
	fsys := fstest.MapFS{}
	put := func(name string, v any) {
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		fsys[name] = &fstest.MapFile{Data: data}
	}
	var approved bytes.Buffer
	if err := WriteApproved(&approved, in.Approved); err != nil {
		t.Fatal(err)
	}
	fsys["approved.json"] = &fstest.MapFile{Data: approved.Bytes()}
	for name, data := range s.specFiles() {
		fsys["files/c1/"+name] = &fstest.MapFile{Data: data}
	}
	put("history.json", s.history)
	ties := map[string]map[string][]string{}
	for commit, byCheck := range in.Ties {
		ties[commit] = map[string][]string{}
		for check, tie := range byCheck {
			ties[commit][check] = tie.Paths
		}
	}
	put("ties.json", ties)
	results := make([]scenarioResult, len(in.Results))
	for i, r := range in.Results {
		results[i] = scenarioResult{Result: r, CheckVersion: len(in.Approved.Nodes[r.Check].Versions)}
	}
	put("results.json", results)
	put("jobs.json", in.JobRuns)
	put("subjects.json", subjects)
	put("units.json", units)
	return fsys
}

// outcomeOf is what the reader and the derivation give for the scenario fsys: the refusal of the
// reader, or the state at each subject, or its refusal, and the cycle of the units.
func outcomeOf(fsys fstest.MapFS) string {
	s, err := ReadScenario(fsys)
	if err != nil {
		return "refused: " + err.Error()
	}
	var out strings.Builder
	for i := range s.Subjects {
		state, err := Derive(s.Input(i))
		fmt.Fprintf(&out, "%+v %v\n", state, err)
	}
	if s.Units != nil {
		cycle, err := WaitCycle(s.Units)
		fmt.Fprintf(&out, "cycle %q %v\n", cycle, err)
	}
	return out.String()
}

// object is one JSON object of a file of a scenario: its JSON pointer, and its members.
type object struct {
	pointer string
	members map[string]any
}

// objectsOf is each object of type typ in v, a JSON value of type t at the pointer.
func objectsOf(v any, t reflect.Type, pointer string, typ reflect.Type) []object {
	var objects []object
	switch t.Kind() {
	case reflect.Pointer:
		return objectsOf(v, t.Elem(), pointer, typ)
	case reflect.Slice:
		items, _ := v.([]any)
		for i, item := range items {
			objects = append(objects, objectsOf(item, t.Elem(), pointer+"/"+strconv.Itoa(i), typ)...)
		}
	case reflect.Map:
		members, _ := v.(map[string]any)
		for _, key := range slices.Sorted(maps.Keys(members)) {
			objects = append(objects, objectsOf(members[key], t.Elem(), pointer+"/"+pointerToken.Replace(key), typ)...)
		}
	case reflect.Struct:
		members, isObject := v.(map[string]any)
		if !isObject {
			return nil
		}
		if t == typ {
			objects = append(objects, object{pointer, members})
		}
		for _, m := range inputMembers(t) {
			if m.name != "" {
				objects = append(objects, objectsOf(members[m.name], m.field.Type, pointer+"/"+pointerToken.Replace(m.name), typ)...)
			}
		}
	}
	return objects
}

// jsonAt is the value at the pointer of v, a JSON value whose tokens hold no ~ or /.
func jsonAt(v any, pointer string) any {
	if pointer == "" {
		return v
	}
	for _, token := range strings.Split(pointer[1:], "/") {
		switch c := v.(type) {
		case []any:
			i, _ := strconv.Atoi(token)
			v = c[i]
		case map[string]any:
			v = c[token]
		}
	}
	return v
}

// withJSONAt is v with the value at the pointer x; v is changed in place, and the pointer "" gives
// x.
func withJSONAt(v any, pointer string, x any) any {
	if pointer == "" {
		return x
	}
	last := pointer[strings.LastIndexByte(pointer, '/')+1:]
	switch c := jsonAt(v, parentOf(pointer)).(type) {
	case []any:
		i, _ := strconv.Atoi(last)
		c[i] = x
	case map[string]any:
		c[last] = x
	}
	return v
}

// parentOf is the pointer of the value that holds the value at the pointer.
func parentOf(pointer string) string { return pointer[:max(strings.LastIndexByte(pointer, '/'), 0)] }

// TestAMemberLeftOutOfAFileIsReadByItsMark leaves each member of each type of each JSON file of a
// scenario out of one element, in memberScenario, where the base sets it, and reads the scenario:
//
//   - beside an element that sets it: for a result, a failing run, or a failing example of which the
//     test caught the fault, of one check, beside its pass, as for D1's run with no at;
//   - alone: the list of the element holds only that element; and an object that is the whole file,
//     alone.
//
// A member marked nonzero, present or one of is refused, where the same element with the member
// reads otherwise; one marked zero reads as the same element with the member at its zero value, the
// reading that its reason states. A member of a one of mark is refused too beside the other member.
func TestAMemberLeftOutOfAFileIsReadByItsMark(t *testing.T) {
	t.Parallel()
	base := memberScenario(t)
	if got := outcomeOf(base); strings.HasPrefix(got, "refused") || strings.Contains(got, "specgraph:") {
		t.Fatalf("the base: %s", got)
	}
	read := func(t *testing.T, name string) any {
		var v any
		if err := json.Unmarshal(base[name].Data, &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	with := func(t *testing.T, name string, v any) fstest.MapFS {
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		fsys := maps.Clone(base)
		fsys[name] = &fstest.MapFile{Data: data}
		return fsys
	}
	checked := 0
	for _, name := range slices.Sorted(maps.Keys(scenarioFileTypes)) {
		for _, typ := range structTypes(scenarioFileTypes[name], nil) {
			for _, m := range inputMembers(typ) {
				if m.name == "" {
					continue
				}
				mark, err := markOf(m.field)
				if err != nil {
					t.Errorf("%s: %v", typ, err)
					continue
				}
				zero := zeroJSON(t, m.field.Type)
				// The element that leaves the member out: one that sets it, a non-zero value first, and for
				// a result one of outcome fail; and an element beside it that sets it too.
				objects := slices.DeleteFunc(objectsOf(read(t, name), scenarioFileTypes[name], "", typ), func(o object) bool { return o.members[m.name] == nil })
				slices.SortStableFunc(objects, func(a, b object) int {
					return boolOrder(reflect.DeepEqual(a.members[m.name], zero), reflect.DeepEqual(b.members[m.name], zero))
				})
				if typ == reflect.TypeFor[scenarioResult]() {
					objects = slices.DeleteFunc(objects, func(o object) bool { return o.members["outcome"] != "fail" && o.members["outcome"] != "pass" })
					slices.SortStableFunc(objects, func(a, b object) int {
						return boolOrder(a.members["outcome"] != "fail", b.members["outcome"] != "fail")
					})
				}
				element, beside := -1, -1
				for i := 0; i < len(objects) && beside < 0; i++ {
					for j := range objects {
						if j != i && parentOf(objects[j].pointer) == parentOf(objects[i].pointer) && objects[i].pointer != "" &&
							(typ != reflect.TypeFor[scenarioResult]() || objects[j].members["outcome"] == "pass" && objects[i].members["outcome"] == "fail" &&
								objects[j].members["check"] == objects[i].members["check"] && objects[j].members["kind"] == objects[i].members["kind"]) {
							element, beside = i, j
							break
						}
					}
				}
				if len(objects) == 1 && objects[0].pointer == "" {
					element = 0
				}
				if element < 0 {
					t.Errorf("%s: no element of %s sets %s beside another one that sets it, so the test reads no absence of it", name, typ, m.name)
					continue
				}
				e := objects[element]
				// placement is the file with e in its place, as edit leaves it: beside, or alone in its list.
				placement := func(t *testing.T, alone bool, edit func(members map[string]any)) fstest.MapFS {
					v := read(t, name)
					members := maps.Clone(jsonAt(v, e.pointer).(map[string]any))
					edit(members)
					if alone && e.pointer != "" {
						return with(t, name, withJSONAt(v, parentOf(e.pointer), []any{members}))
					}
					return with(t, name, withJSONAt(v, e.pointer, members))
				}
				for _, alone := range []bool{false, true} {
					where := fmt.Sprintf("%s %s of %s %s, %s", name, m.name, typ.Name(), e.pointer, map[bool]string{false: "beside " + objectsPointer(objects, beside), true: "alone"}[alone])
					if e.pointer == "" && !alone {
						continue
					}
					left := outcomeOf(placement(t, alone, func(members map[string]any) { delete(members, m.name) }))
					checked++
					switch mark.kind {
					case "zero":
						if set := outcomeOf(placement(t, alone, func(members map[string]any) { members[m.name] = zero })); left != set {
							t.Errorf("%s: left out it reads\n%s\nand at its zero value %v\n%s", where, left, zero, set)
						}
					default:
						kept := outcomeOf(placement(t, alone, func(map[string]any) {}))
						if !strings.HasPrefix(left, "refused") || left == kept {
							t.Errorf("%s: a member marked %s left out reads\n%s\nand set\n%s", where, mark.kind, left, kept)
						}
					}
					if mark.kind == "one of" && !alone {
						both := outcomeOf(placement(t, alone, func(members map[string]any) { members[mark.other] = "x9" }))
						if !strings.HasPrefix(both, "refused") {
							t.Errorf("%s: with %s set too it reads\n%s", where, mark.other, both)
						}
					}
				}
			}
		}
	}
	t.Logf("%d readings of a member left out", checked)
}

// objectsPointer is the pointer of objects[i], and "none" for no i.
func objectsPointer(objects []object, i int) string {
	if i < 0 {
		return "none"
	}
	return objects[i].pointer
}

// boolOrder orders false before true.
func boolOrder(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return 1
	}
	return -1
}

// zeroJSON is the zero value of type t as encoding/json/v2 writes it, read back as a JSON value.
func zeroJSON(t *testing.T, typ reflect.Type) any {
	t.Helper()
	data, err := json.Marshal(reflect.Zero(typ).Interface())
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// step is one step from a value to a value that it holds: a member, an element of a list, a value
// of a map by its key, or the value that a pointer or an interface holds (field and index -1, no
// key).
type step struct {
	field, index int
	key          reflect.Value
}

// instancesOf is the path of each struct value of type typ in v.
func instancesOf(v reflect.Value, typ reflect.Type, path []step) [][]step {
	var found [][]step
	next := func(s step, w reflect.Value) {
		found = append(found, instancesOf(w, typ, append(slices.Clone(path), s))...)
	}
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			next(step{field: -1, index: -1}, v.Elem())
		}
	case reflect.Slice:
		for i := range v.Len() {
			next(step{field: -1, index: i}, v.Index(i))
		}
	case reflect.Map:
		for _, key := range v.MapKeys() {
			next(step{field: -1, index: -1, key: key}, v.MapIndex(key))
		}
		slices.SortFunc(found, func(a, b []step) int { return strings.Compare(fmt.Sprint(a), fmt.Sprint(b)) })
	case reflect.Struct:
		if v.Type() == typ {
			found = append(found, path)
		}
		if v.Type().PkgPath() != typ.PkgPath() || v.Type() == reflect.TypeFor[Tree]() {
			return found
		}
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				next(step{field: i, index: -1}, v.Field(i))
			}
		}
	}
	return found
}

// edit applies change to the value at path from v, which is settable, and sets each map value and
// interface on the path to the value that change leaves.
func edit(v reflect.Value, path []step, change func(reflect.Value)) {
	if len(path) == 0 {
		change(v)
		return
	}
	s, rest := path[0], path[1:]
	switch v.Kind() {
	case reflect.Pointer:
		edit(v.Elem(), rest, change)
	case reflect.Interface:
		held := reflect.New(v.Elem().Type()).Elem()
		held.Set(v.Elem())
		edit(held, rest, change)
		v.Set(held)
	case reflect.Slice:
		edit(v.Index(s.index), rest, change)
	case reflect.Map:
		held := reflect.New(v.Type().Elem()).Elem()
		held.Set(v.MapIndex(s.key))
		edit(held, rest, change)
		v.SetMapIndex(s.key, held)
	case reflect.Struct:
		edit(v.Field(s.field), rest, change)
	}
}

// TestAMemberLeftZeroAtTheEntryIsReadByItsMark gives the library each input of memberBase with one
// member of one value at its zero value, as a caller that does not set it gives it: for each member
// marked nonzero, beside a value that sets it (for a result, the failing run of C beside its pass)
// and alone in its list or map, Derive and CheckStates (WaitCycle for a unit) give an error, where
// the same input with the member set gives none or another one; for a member of a one of mark, also
// with both members set. To a caller of the library, a zero value of a member marked present or zero
// is the value that its reason states, and no file is read.
func TestAMemberLeftZeroAtTheEntryIsReadByItsMark(t *testing.T) {
	t.Parallel()
	refusal := func(t *testing.T, in Input, units []Unit, root reflect.Type) string {
		if root == reflect.TypeFor[[]Unit]() {
			_, err := WaitCycle(units)
			return fmt.Sprint(err)
		}
		_, err := Derive(in)
		_, checksErr := CheckStates(in)
		if fmt.Sprint(err) != fmt.Sprint(checksErr) {
			t.Errorf("Derive: %v; CheckStates: %v", err, checksErr)
		}
		return fmt.Sprint(err)
	}
	checked := 0
	for _, root := range []reflect.Type{reflect.TypeFor[Input](), reflect.TypeFor[[]Unit]()} {
		types := structTypes(root, nil)
		if root == reflect.TypeFor[Input]() {
			types = structTypes(reflect.TypeFor[FixtureHistory](), types) // the History of memberBase
		}
		for _, typ := range types {
			for _, m := range inputMembers(typ) {
				mark, err := markOf(m.field)
				if err != nil {
					t.Errorf("%s: %v", typ, err)
					continue
				}
				if mark.kind != "nonzero" && mark.kind != "one of" {
					continue
				}
				// variant is the refusal of memberBase with change made to the value at the path of the
				// instance of typ, or, alone, to its list or map with only that instance in it.
				variant := func(t *testing.T, instance int, alone bool, change func(reflect.Value)) (string, string) {
					_, in, _, units := memberBase(t)
					v := reflect.ValueOf(&in).Elem()
					if root == reflect.TypeFor[[]Unit]() {
						v = reflect.ValueOf(&units).Elem()
					}
					paths := instancesOf(v, typ, nil)
					path := paths[instance]
					if alone {
						container, last := path[:len(path)-1], path[len(path)-1]
						edit(v, container, func(c reflect.Value) {
							only := reflect.New(c.Type()).Elem()
							if c.Kind() == reflect.Slice {
								only.Set(reflect.Append(reflect.MakeSlice(c.Type(), 0, 1), c.Index(last.index)))
							} else {
								only.Set(reflect.MakeMap(c.Type()))
								only.SetMapIndex(last.key, c.MapIndex(last.key))
							}
							c.Set(only)
						})
						if last.index >= 0 {
							path = append(slices.Clone(container), step{field: -1, index: 0})
						}
					}
					edit(v, path, change)
					return refusal(t, in, units, root), fmt.Sprint(path)
				}
				_, in, _, units := memberBase(t)
				v := reflect.ValueOf(&in).Elem()
				if root == reflect.TypeFor[[]Unit]() {
					v = reflect.ValueOf(&units).Elem()
				}
				paths := instancesOf(v, typ, nil)
				instance := slices.IndexFunc(paths, func(path []step) bool {
					found := false
					edit(v, path, func(s reflect.Value) { found = !s.FieldByIndex(m.index).IsZero() })
					return found
				})
				switch {
				case instance < 0 && mark.kind == "one of":
					instance = 0 // its zero is the other member's case; both set is this member's
				case instance < 0:
					t.Errorf("%s: memberBase sets %s of no %s, so the test reads no zero of it", root, m.field.Name, typ)
					continue
				}
				for _, alone := range []bool{false, true} {
					// A value is alone in a list or a map of others; a member or the root has no others.
					path := paths[instance]
					if alone && (len(path) == 0 || path[len(path)-1].index < 0 && !path[len(path)-1].key.IsValid()) {
						continue
					}
					var set bool
					edit(v, path, func(s reflect.Value) { set = !s.FieldByIndex(m.index).IsZero() })
					if set {
						kept, _ := variant(t, instance, alone, func(reflect.Value) {})
						zeroed, at := variant(t, instance, alone, func(s reflect.Value) {
							f := s.FieldByIndex(m.index)
							f.Set(reflect.Zero(f.Type()))
						})
						checked++
						if zeroed == "<nil>" || zeroed == kept {
							t.Errorf("%s %s at %v (alone %t): with it zero the entry gives %s, and with it set %s", typ, m.field.Name, at, alone, zeroed, kept)
						}
					}
					if mark.kind == "one of" && !alone {
						i := slices.IndexFunc(inputMembers(typ), func(o inputMember) bool { return o.name == mark.other })
						both, _ := variant(t, instance, alone, func(s reflect.Value) {
							s.FieldByIndex(m.index).SetString("x9")
							s.FieldByIndex(inputMembers(typ)[i].index).SetString("x8")
						})
						checked++
						if both == "<nil>" {
							t.Errorf("%s %s and %s both set: the entry gives no error", typ, m.field.Name, mark.other)
						}
					}
				}
			}
		}
	}
	t.Logf("%d readings of a member at its zero value", checked)
}
