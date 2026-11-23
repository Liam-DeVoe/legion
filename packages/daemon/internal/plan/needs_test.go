package plan

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// needWord is a need written as Dispatch writes a reference to the block id of the plan document.
func needWord(id string) string { return "dispatch://ACME-1/artifact/plan-md#b-" + id }

// taskNeeding is a `plan_task` block id, with the steps `implementer reviewer`, whose `needs`
// writes the words needs, and no `needs` when there are none.
func taskNeeding(id string, needs ...string) Block {
	attributes := map[string]string{"steps": "implementer reviewer"}
	if len(needs) > 0 {
		attributes["needs"] = strings.Join(needs, " ")
	}
	return Block{ID: id, Type: TaskBlockType, Attributes: attributes, Text: "What task " + id + " does."}
}

// planOfTasksAToE is the plan of five tasks a to e, each need written as a reference to a block:
// c needs b, d needs a and c, and e needs d.
func planOfTasksAToE(t *testing.T) Plan {
	t.Helper()
	return planOf(t, 1, taskNeeding("a"), taskNeeding("b"), taskNeeding("c", needWord("b")),
		taskNeeding("d", needWord("a"), needWord("c")), taskNeeding("e", needWord("d")))
}

// FromBlocks gives each task's Needs as the ids of the tasks they name, in the order the block
// writes them: a reference's block id after its last `#b-`, decoded as Dispatch encodes one (`%2E`
// is a `.`) with a pin after it left out, and a word with no `#b-` as itself. A task named twice is
// needed once. Each word that names no task stays in Unresolved. So no caller holds a plan whose
// needs are words. The fault is a plan read with the words of its needs, which no reader of a task
// id can read, and which Validate takes; the control is a FromBlocks that keeps each word whole,
// under which d needs the words.
func TestFromBlocksReadsEachNeedAsTheTaskItNames(t *testing.T) {
	read := planOf(t, 1, taskNeeding("a.b"), taskNeeding("c"),
		taskNeeding("d", needWord("c")+"@v2", needWord("a%2Eb"), "x", "c"),
		taskNeeding("e", "d", needWord("d")+"@vx"))
	if got := read.Tasks[2]; !reflect.DeepEqual(got.Needs, []string{"c", "a.b"}) || !reflect.DeepEqual(got.Unresolved, []string{"x"}) {
		t.Fatalf("d needs %q and leaves %q unresolved, want [c a.b] and [x]", got.Needs, got.Unresolved)
	}
	// A pin that is no `@v<N>` makes the reference name no block, as Dispatch reads it.
	if got := read.Tasks[3]; !reflect.DeepEqual(got.Needs, []string{"d"}) || !reflect.DeepEqual(got.Unresolved, []string{needWord("d") + "@vx"}) {
		t.Fatalf("e needs %q and leaves %q unresolved, want [d] and the reference with the bad pin", got.Needs, got.Unresolved)
	}
	if got := read.Tasks[0]; got.Needs != nil || got.Unresolved != nil {
		t.Fatalf("a, which needs nothing, needs %q and leaves %q unresolved", got.Needs, got.Unresolved)
	}
}

// A need that names no task of the plan waits for a task that never runs, so Validate refuses
// it for good, once for each such word in the order the block writes them, naming the word as the
// block writes it. The fault is such a plan taken; the control is a FromBlocks that keeps each
// word whole, under which the reference to b-x is refused by its id and the plan of tasks a to e
// answers four refusals of a need of no task.
func TestANeedThatNamesNoTaskIsRefused(t *testing.T) {
	d := Deployment{Roles: BuiltinRoles()}
	for _, tc := range []struct {
		name string
		plan Plan
		want []Refusal
	}{
		{name: "a word", plan: planOf(t, 1, taskNeeding("a"), taskNeeding("d", "x")),
			want: []Refusal{{Task: "d", Reason: "task d needs x, which names no task of this plan."}}},
		{name: "a reference to the block of no task", plan: planOf(t, 1, taskNeeding("a"), taskNeeding("d", needWord("x"))),
			want: []Refusal{{Task: "d", Reason: "task d needs dispatch://ACME-1/artifact/plan-md#b-x, which names no task of this plan."}}},
		{name: "each word in the block's order", plan: planOf(t, 1, taskNeeding("a"), taskNeeding("d", "y", needWord("a"), "x")),
			want: []Refusal{
				{Task: "d", Reason: "task d needs y, which names no task of this plan."},
				{Task: "d", Reason: "task d needs x, which names no task of this plan."},
			}},
		{name: "a word written twice", plan: planOf(t, 1, taskNeeding("a"), taskNeeding("d", "x", "x")),
			want: []Refusal{{Task: "d", Reason: "task d needs x, which names no task of this plan."}}},
		{name: "the plan of tasks a to e", plan: planOfTasksAToE(t)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := validated(t, tc.plan, d); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Validate =\n%+v\nwant\n%+v", got, tc.want)
			}
		})
	}
}

// A task that needs itself through other tasks never starts, so Validate refuses each set of
// tasks that need each other for good, once, with one cycle of the set: the shortest from its
// first task in id order back to that task, each task's needs taken in the order its block writes
// them. A need that closes no cycle is in none. So a set through which several cycles run gives
// one reason, which names its first task, and the owner who breaks that cycle reads the next one
// at the next try. The fault is a cycle that walks for ever or is taken, or a set named by no
// cycle. The control is Validate without the check of cycles, under which the first case answers
// no refusal.
func TestATaskThatNeedsItselfIsRefused(t *testing.T) {
	d := Deployment{Roles: BuiltinRoles()}
	itself := func(task, chain string) Refusal {
		return Refusal{Task: task, Reason: "task " + task + " needs itself: " + chain + "."}
	}
	for _, tc := range []struct {
		name string
		plan Plan
		want []Refusal
	}{
		{name: "a cycle of three tasks", plan: planOf(t, 1, taskNeeding("a", needWord("b")), taskNeeding("b", needWord("c")), taskNeeding("c", needWord("a"))),
			want: []Refusal{itself("a", "a needs b, b needs c, c needs a")}},
		{name: "a task that names itself", plan: planOf(t, 1, taskNeeding("a", "a")),
			want: []Refusal{itself("a", "a needs a")}},
		{name: "two cycles", plan: planOf(t, 1, taskNeeding("a", "b"), taskNeeding("b", "a"), taskNeeding("c", "d"), taskNeeding("d", "c")),
			want: []Refusal{itself("a", "a needs b, b needs a"), itself("c", "c needs d, d needs c")}},
		{name: "a cycle named by its first task in id order, whatever the document's order", plan: planOf(t, 1,
			taskNeeding("c", needWord("a")), taskNeeding("b", needWord("c")), taskNeeding("a", needWord("b"))),
			want: []Refusal{itself("a", "a needs b, b needs c, c needs a")}},
		{name: "two cycles through one task are one set, named by the shortest cycle from its first task", plan: planOf(t, 1,
			taskNeeding("a", "c", "b"), taskNeeding("b", "a"), taskNeeding("c", "d"), taskNeeding("d", "a")),
			want: []Refusal{itself("a", "a needs b, b needs a")}},
		{name: "two cycles that share a task are one set, named once", plan: planOf(t, 1, taskNeeding("a", "b"), taskNeeding("b", "a", "c"), taskNeeding("c", "b")),
			want: []Refusal{itself("a", "a needs b, b needs a")}},
		{name: "a need outside the cycle", plan: planOf(t, 1, taskNeeding("a", "b"), taskNeeding("b", "a"), taskNeeding("e", "a")),
			want: []Refusal{itself("a", "a needs b, b needs a")}},
		{name: "the plan of tasks a to e", plan: planOfTasksAToE(t)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := validated(t, tc.plan, d); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Validate =\n%+v\nwant\n%+v", got, tc.want)
			}
		})
	}
}

// ringOf is a plan of size tasks t00000, t00001, … in which each task needs the next and the last
// needs the first: one set of tasks that need each other, whose one cycle passes through every
// task. Its tasks name no steps, so Validate's only cause in it is the cycle.
func ringOf(size int) Plan {
	tasks := make([]Task, size)
	for i := range tasks {
		tasks[i] = Task{ID: fmt.Sprintf("t%05d", i), Needs: []string{fmt.Sprintf("t%05d", (i+1)%size)}}
	}
	return Plan{Tasks: tasks}
}

// register_plan checks a plan its owner wrote before the fact lock, so the check of cycles runs on
// the request's context: once the context has ended, Validate stops and answers the context's
// error and no refusal. The fault is a check that runs to its end after its request has gone; the
// control is a check of cycles that never reads its context, under which the ring of 1,000 tasks
// answers its refusal and no error.
func TestAnEndedContextStopsTheCheckOfCycles(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	refusals, err := Validate(ctx, ringOf(1_000), Deployment{Roles: BuiltinRoles()})
	if !errors.Is(err, context.Canceled) || refusals != nil {
		t.Fatalf("Validate under an ended context = %d refusals and error %v, want none and %v", len(refusals), err, context.Canceled)
	}
}

// endsAt is a context that ends at its read number end: its Err answers nil before that read and
// context.Canceled from it on, and reads counts every read, so a test ends a check at the read
// it names and sees where the check stopped.
type endsAt struct {
	context.Context
	end, reads int
}

func (c *endsAt) Err() error {
	if c.reads++; c.reads >= c.end {
		return context.Canceled
	}
	return nil
}

// endedAt is an ended, as the check of cycles passes one to components and cycleFrom, that
// answers stop from its call number end on, with the count of its calls.
func endedAt(end int, stop error) (ended func() error, calls *int) {
	calls = new(int)
	return func() error {
		if *calls++; *calls >= end {
			return stop
		}
		return nil
	}, calls
}

// A request can end while its check of cycles runs, not only before it starts. The check stops at
// the first read of its context that finds it ended, and answers its error and no refusal:
// components, the pass over the tasks and their needs, and cycleFrom, the search of a set's
// cycle, each read ended once for each step, and Validate reads its context at every
// contextSteps-th step of the two. Each case ends the check in the middle of one of them, on a ring
// of 4,096 tasks, whose pass takes 8,192 steps and whose search follows 4,096 needs. The fault is a
// check that reads its context only before it starts; the control is each of the two without its
// read of ended, under which its case answers the ring whole and no error, and Validate without
// its reads at the steps, under which its case answers the ring's refusal.
func TestARequestThatEndsDuringTheCheckOfCyclesStopsIt(t *testing.T) {
	const size = 4_096
	stop := errors.New("the request ended")
	needs := make([][]int, size)
	for i := range needs {
		needs[i] = []int{(i + 1) % size}
	}
	t.Run("in the pass over the tasks and their needs", func(t *testing.T) {
		ended, calls := endedAt(size, stop)
		if _, _, err := components(needs, ended); !errors.Is(err, stop) || *calls != size {
			t.Fatalf("components with ended ending at its call %d = error %v after %d calls, want %v at that call", size, err, *calls, stop)
		}
	})
	t.Run("in the search of the set's cycle", func(t *testing.T) {
		component, parent := make([]int, size), make([]int, size)
		for i := range parent {
			parent[i] = -1
		}
		ended, calls := endedAt(size/2, stop)
		if path, err := cycleFrom(needs, component, parent, 0, ended); !errors.Is(err, stop) || path != nil || *calls != size/2 {
			t.Fatalf("cycleFrom with ended ending at its call %d = %d tasks and error %v after %d calls, want none and %v at that call", size/2, len(path), err, *calls, stop)
		}
	})
	t.Run("in Validate, at the request's context", func(t *testing.T) {
		// The first read is the one before the check starts; the second is at its step
		// contextSteps, inside its pass over the tasks.
		ctx := &endsAt{Context: t.Context(), end: 2}
		refusals, err := Validate(ctx, ringOf(size), Deployment{Roles: BuiltinRoles()})
		if !errors.Is(err, context.Canceled) || refusals != nil || ctx.reads != 2 {
			t.Fatalf("Validate with a context ending at its read 2 = %d refusals and error %v after %d reads, want none and %v at that read", len(refusals), err, ctx.reads, context.Canceled)
		}
	})
}

// The work of an issue merges as one change, at the end of the one task that no task needs, so
// a plan with more than one such task is refused as not carried out yet, naming them in id order.
// A plan whose tasks all lead to one task is not refused for it (it is still refused for its
// several tasks, until this release runs them). The fault is two tasks whose work would each
// merge by itself, taken; the control is Supported without the refusal, whose answer to a and b
// lacks the second reason.
func TestTwoTasksThatNoTaskNeedsAreNotCarriedOutYet(t *testing.T) {
	two := planOf(t, 1, taskNeeding("b"), taskNeeding("a"))
	if got := Sinks(two.Tasks); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("Sinks(b, a) = %q, want [a b], in id order", got)
	}
	want := []Refusal{
		{Task: "", Reason: "the plan has 2 tasks (b, a), and this release runs one task for each issue." + yet},
		{Task: "", Reason: "the plan has 2 tasks that no task needs (a, b), and the work of each would merge by itself." + yet},
	}
	if got := Supported(two); !reflect.DeepEqual(got, want) {
		t.Fatalf("Supported(a, b) =\n%+v\nwant\n%+v", got, want)
	}

	aToE := planOfTasksAToE(t)
	if got := Sinks(aToE.Tasks); !reflect.DeepEqual(got, []string{"e"}) {
		t.Fatalf("Sinks(the plan of tasks a to e) = %q, want [e]", got)
	}
	got := Supported(aToE)
	if len(got) == 0 || got[0].Reason != "the plan has 5 tasks (a, b, c, d, e), and this release runs one task for each issue."+yet {
		t.Fatalf("Supported(the plan of tasks a to e) = %+v, want the refusal of its five tasks first", got)
	}
	for _, refusal := range got {
		if strings.Contains(refusal.Reason, "no task needs") {
			t.Fatalf("Supported(the plan of tasks a to e) = %+v, want no refusal of tasks that no task needs: e alone is one", got)
		}
	}
}
