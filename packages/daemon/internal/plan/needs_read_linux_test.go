//go:build linux

package plan

import (
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// readBound is the most CPU time of its own thread that FromBlocks may spend reading each plan of
// TestEachNeedOfAPlanIsReadInLinearTime. The read takes about 10 ms and 60 ms of it on those
// plans, and the read that scanned the needs a task had kept took 10.6 s and 5.58 s.
const readBound = time.Second

// threadCPU is the CPU time the calling thread has spent, its user and its system time. The caller
// locks its goroutine to that thread, so the reading counts its own work and no other test's, and
// a busy machine, which makes the thread wait, adds nothing to it.
func threadCPU(t *testing.T) time.Duration {
	t.Helper()
	var usage unix.Rusage
	if err := unix.Getrusage(unix.RUSAGE_THREAD, &usage); err != nil {
		t.Fatalf("read the thread's CPU time: %v", err)
	}
	return time.Duration(usage.Utime.Nano() + usage.Stime.Nano())
}

// FromBlocks reads each `needs` word of a task once and looks it up in a set of the ids and of the
// words the task has kept already (resolved), so the read takes time linear in the words. Each
// plan below is read within readBound of the thread's own CPU time, and its task a needs each task
// its words name, once, and keeps each word that names no task, once: 50,000 words that name no
// task, each written twice, and a task that needs each of 50,000 others. The fault is a read that
// scans what the task has kept for each word, quadratic in the words of one block; the control is
// resolved with slices.Contains over the needs and the words kept in place of its two sets.
//
// A count of steps cannot hold this read, as one holds the check of cycles. resolved makes one
// lookup for each word, by a set or by a scan, and the scan's loop runs inside slices.Contains, in
// the standard library, where no count of this package runs: a count the read kept would read the
// same with the scan as without it. So the read is held to 1 s of the thread's CPU time: in this
// pull request's runs the right read took 10 to 80 ms of it on these plans, and the control 3.9 to
// 15.8 s. It catches that scan at these sizes; it proves no bound of time in general.
func TestEachNeedOfAPlanIsReadInLinearTime(t *testing.T) {
	// needing is a plan of a task a whose `needs` writes words, then a task of each id of ids.
	needing := func(words []string, ids []string) []Block {
		blocks := []Block{{ID: "a", Type: TaskBlockType, Attributes: map[string]string{"needs": strings.Join(words, " ")}}}
		for _, id := range ids {
			blocks = append(blocks, Block{ID: id, Type: TaskBlockType})
		}
		return blocks
	}
	for _, tc := range []struct {
		name              string
		blocks            []Block
		needs, unresolved []string
	}{
		{name: "50,000 words that name no task, each twice", blocks: needing(append(numbered("x", 50_000), numbered("x", 50_000)...), nil), unresolved: numbered("x", 50_000)},
		{name: "one task needing each of 50,000 others", blocks: needing(numbered("t", 50_000), numbered("t", 50_000)), needs: numbered("t", 50_000)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			before := threadCPU(t)
			p, err := FromBlocks("plan-md", 1, tc.blocks)
			spent := threadCPU(t) - before
			if err != nil {
				t.Fatalf("FromBlocks(%s): %v", tc.name, err)
			}
			if got := p.Tasks[0]; !reflect.DeepEqual(got.Needs, tc.needs) || !reflect.DeepEqual(got.Unresolved, tc.unresolved) {
				t.Fatalf("FromBlocks(%s): a needs %d tasks and keeps %d words, want %d and %d", tc.name, len(got.Needs), len(got.Unresolved), len(tc.needs), len(tc.unresolved))
			}
			if spent > readBound {
				t.Fatalf("FromBlocks(%s) spent %v of its thread's CPU, want at most %v", tc.name, spent, readBound)
			}
			t.Logf("FromBlocks(%s) spent %v of its thread's CPU", tc.name, spent)
		})
	}
}
