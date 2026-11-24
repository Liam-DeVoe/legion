package daemon

import (
	"context"
	"maps"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/testwait"
)

// The running-copy cadence's own rows: what it remembers of each issue it copies, when it turns
// itself off, and how far apart it starts the copies of one turn. The rig they share and the
// copies' other rows are workspace_copies_test.go's.

// The cadence's own memory holds one entry per issue it has copied, and an entry whose copy is
// not in flight, that owes no retry and whose period has passed answers due exactly as a missing
// one does. Such an entry is dropped where the map is written, so a daemon that runs for weeks
// holds entries for the issues it is copying rather than for every issue it has ever copied. An
// entry whose last copy came to nothing is kept whatever its age: dropped, it would owe no retry,
// and an issue between turns would never be copied again.
func TestTheCadenceForgetsAnIssueWhoseWorkspaceCopyPeriodHasPassed(t *testing.T) {
	t.Parallel()
	clock := &heldClock{}
	clock.advance(0)
	copies := runningCopies{now: clock.Now}
	for _, issue := range []string{"INFRA-1", "INFRA-2", "INFRA-4"} {
		if !copies.begin(copyAsk{of: supervise.Claim{Issue: issue}}) {
			t.Fatalf("the cadence refused the first copy of %s", issue)
		}
	}
	copies.done("INFRA-1", false)
	copies.done("INFRA-2", false)
	copies.done("INFRA-4", true)
	clock.advance(2 * runningCopyPeriod)
	if !copies.begin(copyAsk{of: supervise.Claim{Issue: "INFRA-3"}}) {
		t.Fatal("the cadence refused the first copy of INFRA-3")
	}

	copies.done("INFRA-3", false)

	copies.mu.Lock()
	held := slices.Sorted(maps.Keys(copies.issues))
	copies.mu.Unlock()
	if want := []string{"INFRA-3", "INFRA-4"}; !slices.Equal(held, want) {
		t.Fatalf("the cadence remembers %v, want %v: an entry whose period has passed is dropped where the map is written, unless it owes a retry", held, want)
	}
	// The issues it forgot are due at once, which is what the entries it dropped said too.
	now := clock.Now()
	for _, issue := range []string{"INFRA-1", "INFRA-2"} {
		if !copies.due(issue, now, idleTickInterval) {
			t.Fatalf("%s is not due though the cadence forgot it; a dropped entry must answer as a missing one", issue)
		}
	}
	if copies.due("INFRA-3", now, idleTickInterval) {
		t.Fatal("the copy that just ended is due again at once")
	}
	if owed := copies.retries(); !slices.Equal(owed, []string{"INFRA-4"}) {
		t.Fatalf("the cadence owes retries of %v, want the one issue whose copy came to nothing", owed)
	}
}

// What does turn the cadence off is having nothing to ask or nowhere to put the answer: a runtime
// that bundles no workspace, which is `runtime: tmux`, and a daemon with no store. Either leaves
// the tick running no ticker at all, as release's own tick does where release is off.
func TestTheWorkspaceCopyTickDoesNothingWithoutABundlerOrAStore(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		w    *workflowRuntime
	}{
		{name: "the runtime bundles no workspace", w: &workflowRuntime{copyStore: &store.Store{}}},
		{name: "no store", w: &workflowRuntime{copyBundler: &copyBundler{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			done := make(chan struct{})
			go func() { defer close(done); tc.w.tickWorkspaceCopies(context.Background()) }()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("tickWorkspaceCopies kept running with no bundler or no store")
			}
		})
	}
}

// A turn that ends while the cadence's copy of its issue is still being built is copied again
// once that copy ends. The copy in flight was read before the turn's last work was written, and
// the cadence takes no copy between turns of an issue whose last copy was stored, so without a
// copy after it the newest copy of a pod whose roles are all idle is not the one its last turn
// ended with, which is what the daemon holds itself to (dispatch://INFRA-1117/spec#b-r1): an
// eviction of that idle pod then loses the end of the turn. One copy of an issue is still in
// flight at a time; the turn's end is owed, not dropped.
func TestATurnThatEndsWhileACopyIsInFlightIsCopiedAgainOnceThatCopyEnds(t *testing.T) {
	t.Parallel()
	rig := newCopyRig(t)
	rig.copiesTurnEnds()
	ctx := context.Background()
	midTurn := copyRigBundle
	midTurn.At = time.Now().UTC()
	ended := copyRigBundle
	ended.Bundle, ended.Head, ended.Tips = []byte("PACK the work the turn ended with, written after the copy in flight read the workspace"), "beef", []string{"beef"}
	release := make(chan struct{})
	rig.bundler.holds(func(ctx context.Context) (runtime.WorkspaceBundle, error) {
		select {
		case <-release:
			// The workspace from now on holds the work the turn ended with.
			later := ended
			later.At = time.Now().UTC()
			rig.bundler.answers(later, nil)
			return midTurn, nil
		case <-ctx.Done():
			return runtime.WorkspaceBundle{}, ctx.Err()
		}
	})
	rig.turnStart(t)

	// The cadence's copy inside the turn starts, and is still being built when the turn ends.
	rig.w.copyWorkingWorkspaces(ctx)
	rig.awaitAsks(t, 1)
	rig.turnEnd(t)

	close(release)
	// The issue is between turns now and the copy in flight is stored, so no pass of the cadence
	// would copy it again: the copy the turn's end owed is the only one that can hold that work.
	rig.awaitCopy(t, len(ended.Bundle))
	if asked := rig.bundler.reads(); len(asked) != 2 {
		t.Fatalf("the bundler was asked %v, want the copy in flight and the one the turn's end owed after it", asked)
	}
}

// Inside a turn the cadence starts its copies of one issue at most a period apart when the
// daemon's tick arrives on time, and never less than a period less one tick apart
// (runningCopyPeriod). The period is counted from the start of the copy before it: a copy holds
// the workspace as it was when it started, so an eviction loses the work since that start, and a
// period counted from the copy's end would let a copy that took two minutes put the next start
// seven minutes after its own. Here the first copy takes two minutes of the cadence's clock, and
// the tick comes every minute, on the minute.
func TestTheCadenceStartsTheCopiesOfOneTurnAtMostAPeriodApart(t *testing.T) {
	t.Parallel()
	rig := newCopyRig(t)
	ctx := context.Background()
	tick := rig.w.copyTick()
	rig.clock.advance(0)
	// starts is the cadence's clock as the bundler is asked for each copy, which is when that copy
	// took the issue's place: the clock does not move while a copy is being started.
	var mu sync.Mutex
	var starts []time.Time
	release := make(chan struct{})
	rig.bundler.holds(func(ctx context.Context) (runtime.WorkspaceBundle, error) {
		mu.Lock()
		starts = append(starts, rig.clock.Now())
		first := len(starts) == 1
		mu.Unlock()
		if first {
			select {
			case <-release:
			case <-ctx.Done():
				return runtime.WorkspaceBundle{}, ctx.Err()
			}
		}
		taken := copyRigBundle
		taken.At = time.Now().UTC()
		return taken, nil
	})
	started := func() []time.Time {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(starts)
	}
	// pass is one tick of the cadence at the clock's time. A copy the tick starts reads the clock
	// as it takes the issue's place, so the test moves the clock on only once that copy holds the
	// place, or the tick found the issue not due.
	pass := func() {
		t.Helper()
		rig.w.copyWorkingWorkspaces(ctx)
		if !testwait.Holds(testwait.Bounded(t, 30*time.Second, time.Second), testwait.Poll, func() bool {
			return !rig.w.copies.due(rig.issue.Key, rig.clock.Now(), tick)
		}) {
			t.Fatalf("the copy the tick at %s found due never started; the daemon logged:\n%s", rig.clock.Now(), rig.logs.String())
		}
	}
	rig.turnStart(t)

	pass()
	rig.awaitAsks(t, 1)
	// The first copy is still being built at the next two ticks, and ends after the second.
	for range 2 {
		rig.clock.advance(tick)
		pass()
	}
	close(release)
	rig.awaitQuiet(t)
	for minute := 3; len(started()) < 2 && minute <= 10; minute++ {
		rig.clock.advance(tick)
		pass()
		rig.awaitQuiet(t)
	}

	got := started()
	if len(got) < 2 {
		t.Fatalf("the cadence started %d copies in ten minutes of one turn, want a second one; the daemon logged:\n%s", len(got), rig.logs.String())
	}
	if apart := got[1].Sub(got[0]); apart > runningCopyPeriod || apart < runningCopyPeriod-tick {
		t.Fatalf("the turn's second copy started %s after its first, want at most %s and no less than %s", apart, runningCopyPeriod, runningCopyPeriod-tick)
	}
}
