package plan

import (
	"reflect"
	"testing"
)

// top, partPlace and elementPlace are the three places a task block stands at (Places).
var top = Place{}

func partPlace(task string) Place    { return Place{Standing: PartOfTask, Task: task} }
func elementPlace(task string) Place { return Place{Standing: ElementOfTask, Task: task} }

// Each task block stands at one of three places, by the block it stands directly under: under no
// task block, a task at the top; under a task with a sub-owner, a part of that task; under a task
// with no sub-owner, or under an element, an element of that task. A part of a part with a
// sub-owner is a part of it, so the depth is not bounded, and a parent written later in the
// document places its block all the same. A parent that would close a cycle is taken by no block:
// the first block of the cycle in the document's order stands at the top, as Dispatch places it.
//
// The faults are a part read as an element, or an element as a part, which Legion would carry out
// as work. The control is a Places that answers a part for every parent that is a task: the rows
// of an element fail.
func TestPlacesGivesEachTaskBlockItsPlace(t *testing.T) {
	for _, tc := range []struct {
		name   string
		blocks []TaskBlock
		want   map[string]Place
	}{
		{name: "under a task with a sub-owner", blocks: []TaskBlock{{ID: "f", Owner: "architect"}, {ID: "f1", Parent: "f"}},
			want: map[string]Place{"f": top, "f1": partPlace("f")}},
		{name: "under a task with no sub-owner", blocks: []TaskBlock{{ID: "a"}, {ID: "a1", Parent: "a"}},
			want: map[string]Place{"a": top, "a1": elementPlace("a")}},
		{name: "under an element", blocks: []TaskBlock{{ID: "a"}, {ID: "a1", Parent: "a"}, {ID: "a1x", Parent: "a1", Owner: "architect"}, {ID: "a1y", Parent: "a1x"}},
			want: map[string]Place{"a": top, "a1": elementPlace("a"), "a1x": elementPlace("a"), "a1y": elementPlace("a")}},
		{name: "a part of a part whose task has a sub-owner", blocks: []TaskBlock{{ID: "f", Owner: "architect"}, {ID: "f1", Parent: "f", Owner: "architect"}, {ID: "f1a", Parent: "f1"}},
			want: map[string]Place{"f": top, "f1": partPlace("f"), "f1a": partPlace("f1")}},
		{name: "under a part with no sub-owner", blocks: []TaskBlock{{ID: "f", Owner: "architect"}, {ID: "f1", Parent: "f"}, {ID: "f1a", Parent: "f1"}},
			want: map[string]Place{"f": top, "f1": partPlace("f"), "f1a": elementPlace("f1")}},
		{name: "under a block that is no task block", blocks: []TaskBlock{{ID: "a", Parent: "r1"}},
			want: map[string]Place{"a": top}},
		{name: "under no block", blocks: []TaskBlock{{ID: "a"}},
			want: map[string]Place{"a": top}},
		{name: "a parent written later in the document", blocks: []TaskBlock{{ID: "f1", Parent: "f"}, {ID: "f", Owner: "architect"}},
			want: map[string]Place{"f": top, "f1": partPlace("f")}},
		{name: "parents that close a cycle", blocks: []TaskBlock{{ID: "b", Parent: "a", Owner: "architect"}, {ID: "a", Parent: "b", Owner: "architect"}, {ID: "c", Parent: "a"}},
			want: map[string]Place{"b": top, "a": partPlace("b"), "c": partPlace("a")}},
		{name: "a chain into a cycle", blocks: []TaskBlock{{ID: "x", Parent: "a"}, {ID: "a", Parent: "b", Owner: "architect"}, {ID: "b", Parent: "a", Owner: "architect"}},
			want: map[string]Place{"a": top, "b": partPlace("a"), "x": partPlace("a")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Places(tc.blocks); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Places =\n%+v\nwant\n%+v", got, tc.want)
			}
		})
	}
}

// parented is a `plan_task` block id with the steps implementer, whose `parent` is the word parent,
// and no `parent` where parent is "".
func parented(id, parent string) Block {
	attributes := map[string]string{"steps": "implementer"}
	if parent != "" {
		attributes["parent"] = parent
	}
	return Block{ID: id, Type: TaskBlockType, Attributes: attributes, Text: "What task " + id + " does."}
}

// FromBlocks reads the parent of each task block from its `parent` at the version given, by five
// rules. A block with no `parent` is a task at the top. A `parent` that names a task block of the
// version makes a part or an element, by that task: a part has its task in PartOf, and an element is
// no task of the plan, kept in Elements. A `parent` that names a typed block of the version that is
// no task block, a requirement, a role or a callout, makes a task at the top. So does a `parent`
// that names a block of another document, of another issue, or of an ask, a comment or a message,
// whatever its id: the primary document's `/spec` form names the primary document alone, and a
// document's `/artifact/<slug>` form that document alone. A `parent` that names an id of the plan's
// own document that no typed block of the version holds puts its block at the top with the id in
// MissingParent, which Validate refuses. Dispatch gives a version's typed blocks alone, so a
// heading's id, which Dispatch mints anew at each read, is such an id, as is one that never
// existed.
//
// The faults are a part read from `part_of`, which no block writes now, and a block of another
// document read as the block of this one with its id. The controls: a read that still takes
// `attributes["part_of"]` as PartOf fails the row of the part (f1 stands at the top), and a read
// that takes every `parent` naming a task block as a part fails the row of the element.
func TestFromBlocksPlacesEachTaskBlockByItsParent(t *testing.T) {
	ref := func(document, id string) string { return document + "#b-" + id }
	const notes, other, primary = "dispatch://ACME-1/artifact/notes-md", "dispatch://ACME-2/artifact/plan-md", "dispatch://ACME-1/spec"
	owned := Block{ID: "f", Type: TaskBlockType, Attributes: map[string]string{"owner": "architect"}, Text: "What task f does."}
	requirement := Block{ID: "r1", Type: "requirement", Text: "What the plan serves."}
	role := Block{ID: "prober", Type: RoleBlockType, Attributes: map[string]string{"type": "testing"}, Text: "You probe."}
	callout := Block{ID: "note", Type: "callout", Text: "A note."}
	for _, tc := range []struct {
		name     string
		document string
		blocks   []Block
		// placed is the PartOf of each task of the plan by its id, elements its elements, and
		// missing the MissingParent of each task that has one.
		placed   map[string]string
		elements []Element
		missing  map[string]string
	}{
		{name: "no parent", blocks: []Block{parented("a", "")}, placed: map[string]string{"a": ""}},
		{name: "a task block with a sub-owner", blocks: []Block{owned, parented("f1", ref(planDocument, "f"))},
			placed: map[string]string{"f": "", "f1": "f"}},
		{name: "a task block with no sub-owner", blocks: []Block{parented("a", ""), parented("a1", ref(planDocument, "a"))},
			placed: map[string]string{"a": ""}, elements: []Element{{ID: "a1", Of: "a", Under: "a", Names: []string{"steps"}}}},
		{name: "an element", blocks: []Block{parented("a", ""), {ID: "a1", Type: TaskBlockType, Attributes: map[string]string{"parent": ref(planDocument, "a")}},
			{ID: "a1x", Type: TaskBlockType, Attributes: map[string]string{"parent": ref(planDocument, "a1"), "owner": "architect", "merges": "false"}}},
			placed: map[string]string{"a": ""}, elements: []Element{{ID: "a1", Of: "a", Under: "a"}, {ID: "a1x", Of: "a", Under: "a1", Names: []string{"owner", "merges"}}}},
		{name: "a part of a part whose task has a sub-owner", blocks: []Block{owned,
			{ID: "f1", Type: TaskBlockType, Attributes: map[string]string{"parent": ref(planDocument, "f"), "owner": "architect"}}, parented("f1a", ref(planDocument, "f1"))},
			placed: map[string]string{"f": "", "f1": "f", "f1a": "f1"}},
		{name: "a requirement of the version", blocks: []Block{requirement, parented("a", ref(planDocument, "r1"))}, placed: map[string]string{"a": ""}},
		{name: "a role block of the version", blocks: []Block{role, parented("a", ref(planDocument, "prober"))}, placed: map[string]string{"a": ""}},
		{name: "a callout of the version", blocks: []Block{callout, parented("a", ref(planDocument, "note"))}, placed: map[string]string{"a": ""}},
		{name: "a block of another document of the issue, with a task's id", blocks: []Block{owned, parented("g", ref(notes, "f"))},
			placed: map[string]string{"f": "", "g": ""}},
		{name: "a block of another issue, with a task's id", blocks: []Block{owned, parented("g", ref(other, "f"))},
			placed: map[string]string{"f": "", "g": ""}},
		{name: "a block of an ask, a comment and a message", blocks: []Block{owned,
			parented("g", "dispatch://ACME-1/ask/0f9e8d7c-6b5a-4c3d-8e2f-1a0b9c8d7e6f#b-f"),
			parented("h", "dispatch://ACME-1/comment/1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d#b-f"),
			parented("k", "dispatch://ACME-1/message/2b3c4d5e-6f7a-4b8c-9d0e-1f2a3b4c5d6e#b-f")},
			placed: map[string]string{"f": "", "g": "", "h": "", "k": ""}},
		{name: "the primary document's form, where the plan is not the primary document", blocks: []Block{owned, parented("g", ref(primary, "f"))},
			placed: map[string]string{"f": "", "g": ""}},
		{name: "the primary document's form, where the plan is the primary document", document: primary,
			blocks: []Block{owned, parented("f1", ref(primary, "f"))}, placed: map[string]string{"f": "", "f1": "f"}},
		{name: "this document's artifact form, where the plan is the primary document", document: primary,
			blocks: []Block{owned, parented("g", ref(planDocument, "f"))}, placed: map[string]string{"f": "", "g": ""}},
		{name: "a pinned reference to a task block", blocks: []Block{owned, parented("f1", ref(planDocument, "f")+"@v2")},
			placed: map[string]string{"f": "", "f1": "f"}},
		{name: "a heading's id from the live document", blocks: []Block{owned, parented("f1", ref(planDocument, "h-why-the-plan"))},
			placed: map[string]string{"f": "", "f1": ""}, missing: map[string]string{"f1": "h-why-the-plan"}},
		{name: "an id that never existed", blocks: []Block{owned, parented("f1", ref(planDocument, "gone"))},
			placed: map[string]string{"f": "", "f1": ""}, missing: map[string]string{"f1": "gone"}},
		{name: "an id that does not decode", blocks: []Block{owned, parented("f1", ref(planDocument, "f%zz"))},
			placed: map[string]string{"f": "", "f1": ""}, missing: map[string]string{"f1": ref(planDocument, "f%zz")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			document := planDocument
			if tc.document != "" {
				document = tc.document
			}
			p, err := FromBlocks("ACME-1", "plan-md", document, 1, tc.blocks)
			if err != nil {
				t.Fatalf("FromBlocks: %v", err)
			}
			placed, missing := map[string]string{}, map[string]string{}
			for _, task := range p.Tasks {
				placed[task.ID] = task.PartOf
				if task.MissingParent != "" {
					missing[task.ID] = task.MissingParent
				}
			}
			if !reflect.DeepEqual(placed, tc.placed) {
				t.Errorf("the tasks and their PartOf = %v, want %v", placed, tc.placed)
			}
			if !reflect.DeepEqual(p.Elements, tc.elements) {
				t.Errorf("Elements = %+v, want %+v", p.Elements, tc.elements)
			}
			if tc.missing == nil {
				tc.missing = map[string]string{}
			}
			if !reflect.DeepEqual(missing, tc.missing) {
				t.Errorf("MissingParent of each task = %v, want %v", missing, tc.missing)
			}
		})
	}
}

// An element is no task of the plan: the checks, who changes a task, and the parts of a task never
// see it, and a part names its task in PartOf. The plan holds f, its part f1 and a with its element
// a1, and the element's needs and text are no task's. The fault is an element read as a task,
// which Legion would carry out and which a version could not change without changing a task. The
// control is a FromBlocks that keeps each element among the tasks: Tasks, PartsOf and ChangedTasks
// each fail.
func TestAnElementIsNoTaskOfThePlan(t *testing.T) {
	element := func(text string) Block {
		return Block{ID: "a1", Type: TaskBlockType, Attributes: map[string]string{"parent": needWord("a"), "needs": needWord("f")}, Text: text}
	}
	owned := Block{ID: "f", Type: TaskBlockType, Attributes: map[string]string{"owner": "architect"}, Text: "What task f does."}
	held := planOf(t, 1, owned, parented("f1", needWord("f")), parented("a", ""), element("How a starts."))
	ids := make([]string, len(held.Tasks))
	for i, task := range held.Tasks {
		ids[i] = task.ID
	}
	if want := []string{"f", "f1", "a"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("the tasks of the plan = %q, want %q: the element a1 is none", ids, want)
	}
	if got := held.Tasks[1].PartOf; got != "f" {
		t.Fatalf("PartOf(f1) = %q, want f", got)
	}
	if got := held.PartsOf("a"); got != nil {
		t.Fatalf("PartsOf(a) = %q, want none: a1 is an element of a, no part", got)
	}
	if got := held.WriterOf("a1"); got != "" {
		t.Fatalf("WriterOf(a1) = %q, want \"\": a1 is no task", got)
	}
	next := planOf(t, 2, owned, parented("f1", needWord("f")), parented("a", ""), element("How a starts, said again."))
	if got := ChangedTasks(held, next); len(got) != 0 {
		t.Fatalf("ChangedTasks(the element's text changed) = %q, want none", got)
	}
}
