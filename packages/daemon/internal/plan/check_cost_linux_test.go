//go:build linux

package plan

import (
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// checkBound is the most CPU time register_plan's read of a plan's needs (FromBlocks) or its check
// of cycles (Validate) may take on each plan below. Each runs in time near-linear in the plan, a
// few steps for each task and each need. A check of cycles that searches a ring of 10,000 tasks
// again from every need follows about 100 million needs, 10,000 for each one; a read that scans
// the needs a task has kept for each word it reads compares about 1.25 billion ids for one task
// that needs each of 50,000 others.
const checkBound = time.Second

// threadCPU is the CPU time the calling thread has spent, its user and its system time. The
// caller locks its goroutine to that thread, so the reading counts its own work and no other
// test's.
func threadCPU(t *testing.T) time.Duration {
	t.Helper()
	var usage unix.Rusage
	if err := unix.Getrusage(unix.RUSAGE_THREAD, &usage); err != nil {
		t.Fatalf("read the thread's CPU time: %v", err)
	}
	return time.Duration(usage.Utime.Nano() + usage.Stime.Nano())
}

// Dispatch's document limits let an owner write a plan of 10,000 tasks, and register_plan checks
// it before the fact lock, where nothing but the request's end bounds the check's cost. So the
// check of cycles takes time near-linear in the plan: one pass finds the sets of tasks that need
// each other, and one breadth-first search inside each such set that holds a cycle names it. A
// ring of 10,000 tasks, each needing the next, is one such set, and its check stays within
// checkBound of the thread's CPU time, with the ring's one cycle as its one refusal. The fault is
// a breadth-first search from every need, quadratic on a ring; the control is that check, the
// cycles of the release before, which follows about 100 million needs on this ring.
func TestARingOfTenThousandTasksIsRefusedInNearLinearTime(t *testing.T) {
	const size = 10_000
	ring := ringOf(size)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	before := threadCPU(t)
	refusals, err := Validate(t.Context(), ring, Deployment{Roles: BuiltinRoles()})
	spent := threadCPU(t) - before
	if err != nil {
		t.Fatalf("Validate(a ring of %d tasks): %v", size, err)
	}
	chain := make([]string, size)
	for i := range chain {
		chain[i] = fmt.Sprintf("t%05d needs t%05d", i, (i+1)%size)
	}
	want := []Refusal{{Task: "t00000", Reason: "task t00000 needs itself: " + strings.Join(chain, ", ") + "."}}
	if !reflect.DeepEqual(refusals, want) {
		t.Fatalf("Validate(a ring of %d tasks) gave %d refusals, want the one that names the ring from t00000", size, len(refusals))
	}
	if spent > checkBound {
		t.Fatalf("Validate(a ring of %d tasks) spent %v of CPU, want at most %v", size, spent, checkBound)
	}
	t.Logf("Validate(a ring of %d tasks) spent %v of CPU", size, spent)
}

// register_plan reads each need of a plan before the fact lock, where nothing but the request's
// end bounds the read's cost, so FromBlocks reads a task's `needs` in time linear in its words:
// each word is looked up in a set of the ids and of the words the task has kept. Each plan below
// is read within checkBound of the thread's CPU time; its task a needs every task its words name,
// once, and keeps every word that names no task, once. The first three are the sizes a review
// measured the read at: a needing each of 10,000 other tasks, and a whose 20,000 or 50,000 words
// name no task, each written twice (800 KB of words at the largest, within the 1 MiB of markdown
// a Dispatch document holds, pmdoc.MaxDocumentBytes in packages/envoy/internal/dispatch/pmdoc).
// The fourth is a needing each of 50,000 other tasks, more than one document holds (their blocks
// alone pass that limit), so that the read of the ids, which scanned only the ids kept and never
// more than the plan's tasks, is held to the bound at a size where its scan is long. The fifth is a
// whose 20,000 words are references into another issue, each written twice, which a keeps in
// OutsideIssue, once each, from a set as it keeps the words that name no task. The fault is a read
// that scans the needs a task has kept for each word it reads, quadratic in the words of one block;
// the control is that read, the resolveNeeds of the release before, which compares about 1.25
// billion ids on the fourth plan.
func TestEachNeedOfAPlanIsReadInLinearTime(t *testing.T) {
	// needing is a plan of a task "a" whose `needs` writes words, then a task of each id of ids.
	needing := func(words []string, ids []string) []Block {
		blocks := []Block{{ID: "a", Type: TaskBlockType, Attributes: map[string]string{"needs": strings.Join(words, " ")}}}
		for _, id := range ids {
			blocks = append(blocks, Block{ID: id, Type: TaskBlockType})
		}
		return blocks
	}
	// named is size ids, prefix then a number of six digits.
	named := func(prefix string, size int) []string {
		ids := make([]string, size)
		for i := range ids {
			ids[i] = fmt.Sprintf("%s%06d", prefix, i)
		}
		return ids
	}
	// other starts a reference to a block of another issue's plan, which names no task of this
	// plan whatever its block id.
	other := "dispatch://ACME-2/artifact/plan-md#b-x"
	for _, tc := range []struct {
		name                       string
		blocks                     []Block
		needs, unresolved, outside []string
	}{
		{name: "one task needing each of 10,000 others", blocks: needing(named("t", 10_000), named("t", 10_000)), needs: named("t", 10_000)},
		{name: "20,000 words that name no task, each twice", blocks: needing(append(named("x", 20_000), named("x", 20_000)...), nil), unresolved: named("x", 20_000)},
		{name: "50,000 words that name no task, each twice", blocks: needing(append(named("x", 50_000), named("x", 50_000)...), nil), unresolved: named("x", 50_000)},
		{name: "one task needing each of 50,000 others", blocks: needing(named("t", 50_000), named("t", 50_000)), needs: named("t", 50_000)},
		{name: "20,000 references into another issue, each twice", blocks: needing(append(named(other, 20_000), named(other, 20_000)...), nil),
			outside: named(other, 20_000)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			before := threadCPU(t)
			p, err := FromBlocks(t.Context(), "ACME-1", "plan-md", 1, tc.blocks)
			spent := threadCPU(t) - before
			if err != nil {
				t.Fatalf("FromBlocks(%s): %v", tc.name, err)
			}
			if got := p.Tasks[0]; !reflect.DeepEqual(got.Needs, tc.needs) || !reflect.DeepEqual(got.Unresolved, tc.unresolved) || !reflect.DeepEqual(got.OutsideIssue, tc.outside) {
				t.Fatalf("FromBlocks(%s): a needs %d tasks and leaves %d words and %d references into another issue, want %d, %d and %d",
					tc.name, len(got.Needs), len(got.Unresolved), len(got.OutsideIssue), len(tc.needs), len(tc.unresolved), len(tc.outside))
			}
			if spent > checkBound {
				t.Fatalf("FromBlocks(%s) spent %v of CPU, want at most %v", tc.name, spent, checkBound)
			}
			t.Logf("FromBlocks(%s) spent %v of CPU", tc.name, spent)
		})
	}
}
