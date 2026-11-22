package daemon

// The running copy's retry: a copy that came to nothing for a fault is taken again at the
// cadence's next period, between turns too, once the issue has a live role to ask; the cadence
// forgets the retry of an issue that has left the workflow, and keeps that of a live issue
// between phases; and a copy refused for its size owes no retry. These cases drive the copy rig of
// workspace_copies_test.go.

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// A copy that comes to nothing at a turn's end is taken again at the next period, though the
// issue is between turns by then. That copy was the only one to hold the work the turn ended
// with, and nothing else copies an idle issue's workspace, so without the retry an eviction of the
// idle pod would restore a copy from before that turn's end. The stored copy pays the retry: the
// cadence then leaves the idle issue alone, as it leaves every issue between turns whose last copy
// holds the turn's end.
func TestACopyThatFailedAtATurnsEndIsTakenAgainBetweenTurnsUntilOneIsStored(t *testing.T) {
	rig := newCopyRig(t)
	rig.copiesTurnEnds()
	ctx := context.Background()
	rig.clock.advance(0)
	rig.bundler.answers(runtime.WorkspaceBundle{}, errors.New("the shim answered nothing"))
	rig.turnStart(t)
	rig.turnEnd(t)
	if line := rig.awaitLine(t, runningCopyUnsavedLine); line["reason"] != runningCopyBundlerFailed {
		t.Fatalf("the turn's end copy is logged as %v, want reason %s", line, runningCopyBundlerFailed)
	}
	rig.awaitQuiet(t)
	ended := copyRigBundle
	ended.Bundle, ended.Head, ended.Tips, ended.At = []byte("PACK the work the turn ended with"), "beef", []string{"beef"}, time.Now().UTC()
	rig.bundler.answers(ended, nil)

	// Inside the period the idle issue is left alone.
	rig.w.copyWorkingWorkspaces(ctx)
	rig.asksStay(t, 1)

	// At the next period the cadence takes it again, its roles idle, and stores it.
	rig.wind(t)
	rig.w.copyWorkingWorkspaces(ctx)
	rig.awaitCopy(t, len(ended.Bundle))
	if got := rig.machine.Claim().State; got != supervise.StateIdle {
		t.Fatalf("the claim is %s while the retry was taken, want the idle its turn's end left", got)
	}

	// The stored copy paid the retry, so the periods after it take nothing.
	for range 2 {
		rig.wind(t)
		rig.w.copyWorkingWorkspaces(ctx)
	}
	rig.asksStay(t, 2)
}

// A copy that found no live role owes a retry like any other copy that came to nothing, and the
// retry waits for a live role: an issue whose roles were released or stopped has no running pod
// whose work a copy could hold, so each due tick skips it, asks no shim, and the issue keeps its
// retry. The daemon's log names such an issue under no_live_role at Info, once a period: the
// first due tick of a period writes the line, the other ticks of that period write none, and the
// first of the next period writes it again, so an issue idle for hours is named every five
// minutes and not every minute. Once a resumed or relaunched pod's role is live, the first due
// tick takes the copy and stores it, and the stored copy pays the retry. The copy that finds no
// role here is a push's, the one copy that reads the claims for a role after the fact: a turn's
// end hands its copy the claim whose turn ended.
//
// The controls are noLiveRole without the period's limit, under which the test fails at the
// second tick's line, and noLiveRole at Debug, under which it fails at the first tick's level.
func TestTheCadenceNamesARetryWithNoLiveRoleAtInfoOnceAPeriodAndAsksNothing(t *testing.T) {
	rig := newCopyRig(t)
	ctx := context.Background()
	rig.clock.advance(0)
	rig.setClaimState(t, "suspended")
	rig.w.copyWorkspaceAtPush(rig.issue.Key)
	seen := len(rig.awaitUnsaved(t, runningCopyNoRole, 1))
	// tickLines runs one pass of the cadence and answers the no-role lines it wrote.
	tickLines := func() []map[string]any {
		t.Helper()
		rig.w.copyWorkingWorkspaces(ctx)
		lines := rig.unsavedFor(t, runningCopyNoRole)[seen:]
		seen += len(lines)
		return lines
	}

	// The first due tick names the issue at Info.
	rig.wind(t)
	if lines := tickLines(); len(lines) != 1 || lines[0]["level"] != "INFO" || lines[0]["issue"] != rig.issue.Key {
		t.Fatalf("the first due tick with no live role logged %v, want one line at INFO naming %s", lines, rig.issue.Key)
	}
	// The next tick, inside the same period, is due too and writes nothing.
	rig.clock.advance(rig.w.copyTick())
	if lines := tickLines(); len(lines) != 0 {
		t.Fatalf("a tick inside the period of the last no-role line logged %v, want nothing", lines)
	}
	// The first tick of the next period names the issue again.
	rig.clock.advance(runningCopyPeriod - rig.w.copyTick())
	if lines := tickLines(); len(lines) != 1 || lines[0]["level"] != "INFO" {
		t.Fatalf("the first tick of the next period logged %v, want one line at INFO", lines)
	}
	if asked := rig.bundler.reads(); len(asked) != 0 {
		t.Fatalf("the bundler was asked %v for an issue with no live role", asked)
	}

	// A role is live again, and the retry is due: the tick takes the copy and stores it.
	rig.setClaimState(t, "idle")
	rig.w.copyWorkingWorkspaces(ctx)
	rig.awaitCopy(t, len(copyRigBundle.Bundle))
	rig.wind(t)
	rig.w.copyWorkingWorkspaces(ctx)
	rig.asksStay(t, 1)
}

// The cadence forgets an issue the daemon's project does not record — the issue of a branch
// pushed from outside, never run in this project — retry and all, since no copy of it can ever be
// stored to clear that retry, and the cadence's memory would grow with every such issue. A push of
// such an issue's branch logs its copy that found no role as a push of any issue does: at Info for
// the first in a period (noLiveRole). A recorded issue still in the workflow keeps its retry,
// whatever state its claim is in.
func TestTheCadenceForgetsTheRetryOfAnIssueTheProjectDoesNotRecordAndKeepsALiveIssues(t *testing.T) {
	rig := newCopyRig(t)
	ctx := context.Background()
	const unrecorded = "LEGION-404"
	rig.setClaimState(t, "suspended")
	rig.w.copyWorkspaceAtPush(unrecorded)
	rig.w.copyWorkspaceAtPush(rig.issue.Key)
	for _, line := range rig.awaitUnsaved(t, runningCopyNoRole, 2) {
		if line["level"] != "INFO" {
			t.Fatalf("the first push's copy of %v that found no role is logged as %v, want it at INFO", line["issue"], line)
		}
	}
	if owed := rig.w.copies.retries(); !slices.Equal(owed, []string{rig.issue.Key, unrecorded}) {
		t.Fatalf("the cadence owes retries of %v, want both copies that found no role", owed)
	}

	rig.w.copyWorkingWorkspaces(ctx)

	if owed := rig.w.copies.retries(); !slices.Equal(owed, []string{rig.issue.Key}) {
		t.Fatalf("after a pass the cadence owes retries of %v, want only %s, whose record it read", owed, rig.issue.Key)
	}
	rig.w.copies.mu.Lock()
	_, remembered := rig.w.copies.issues[unrecorded]
	rig.w.copies.mu.Unlock()
	if remembered {
		t.Fatalf("the cadence still remembers %s, which the project does not record", unrecorded)
	}
}

// A phase worker's exit retires its claim, and so does an operator's stop, while the issue stays
// in the workflow: the next phase's start brings the claim back on the same workspace. A retry the
// issue owes then — here a push's copy that found no live role after the worker exited — is kept
// across the due ticks between the phases, named under no_live_role once a period as any retry
// that waits for a role is, and it is paid once the claim returns with a live role: the copy is
// taken and stored, and no retry is owed after it.
//
// The control is the cadence forgetting the retry of an issue whose claims are all retired: the
// first due tick then forgets it, and the test fails there.
func TestTheCadenceKeepsTheRetryOfALiveIssueBetweenPhases(t *testing.T) {
	rig := newCopyRig(t)
	ctx := context.Background()
	rig.clock.advance(0)
	rig.setClaimState(t, string(supervise.StateRetired))
	rig.w.copyWorkspaceAtPush(rig.issue.Key)
	rig.awaitUnsaved(t, runningCopyNoRole, 1)

	for tick := range 2 {
		rig.wind(t)
		rig.w.copyWorkingWorkspaces(ctx)
		if owed := rig.w.copies.retries(); !slices.Equal(owed, []string{rig.issue.Key}) {
			t.Fatalf("due tick %d between the phases left the cadence owing retries of %v, want %s's kept", tick+1, owed, rig.issue.Key)
		}
	}
	if asked := rig.bundler.reads(); len(asked) != 0 {
		t.Fatalf("the bundler was asked %v while the issue had no live role", asked)
	}

	// The next phase's start brings the claim back with a live role.
	rig.setClaimState(t, "idle")
	rig.wind(t)
	rig.w.copyWorkingWorkspaces(ctx)
	rig.awaitCopy(t, len(copyRigBundle.Bundle))
	rig.awaitQuiet(t)
	if owed := rig.w.copies.retries(); len(owed) != 0 {
		t.Fatalf("the cadence owes retries of %v once the returned claim's copy was stored, want none", owed)
	}
}

// An issue closed — done, or parked out of the workflow, on its own or with its tree, as an issue
// close and a tree close leave it — has no live role the cadence will ask for a copy to pay its
// retry, so the cadence forgets a retry it owes, and the due ticks after it name it under
// no_live_role no more, ask nothing and owe it nothing. Kept, it was a line at Info every 5 minutes
// for as long as the daemon ran, since the store keeps a closed issue's retired claims for ever.
//
// The control is the cadence keeping the retry of every issue a claim names: the rows whose issue
// has a retired claim then name it again at the first due tick, and fail there. The child rows
// have a control each: the closed child of a live tree's is the decision without the issue's own
// record, and the child of a parked tree's the decision without its root's; each fails at the
// first due tick.
func TestTheCadenceForgetsTheRetryOfAClosedIssue(t *testing.T) {
	for _, tc := range []struct {
		name string
		// close writes the records that close the issue it answers, which owes the retry.
		close func(t *testing.T, rig *copyRig) string
	}{
		{name: "DoneAndClosed", close: func(t *testing.T, rig *copyRig) string {
			done := rig.issue
			done.Status, done.Phase = record.DoneStatus, phase.Done
			putOutboxIssue(t, rig.pool, rig.issues, done)
			rig.setClaimState(t, string(supervise.StateRetired))
			return rig.issue.Key
		}},
		{name: "ParkedOutOfTheWorkflow", close: func(t *testing.T, rig *copyRig) string {
			parked := rig.issue
			parked.Status = "backlog"
			putOutboxIssue(t, rig.pool, rig.issues, parked)
			rig.setClaimState(t, string(supervise.StateRetired))
			return rig.issue.Key
		}},
		// A child still in its phase whose tree root is parked: its tree is closed, whatever its
		// own record still says.
		{name: "AChildOfAParkedTree", close: func(t *testing.T, rig *copyRig) string {
			parked := rig.issue
			parked.Status = "backlog"
			putOutboxIssue(t, rig.pool, rig.issues, parked)
			child := rig.issue
			child.Key, child.Parent, child.Title = "LEGION-209", record.ParentOf(rig.issue.Key), "child"
			putOutboxIssue(t, rig.pool, rig.issues, child)
			return child.Key
		}},
		// A child closed as done on its own, under a root still in the workflow: its own record
		// is what says it is closed.
		{name: "AClosedChildOfALiveTree", close: func(t *testing.T, rig *copyRig) string {
			child := rig.issue
			child.Key, child.Parent, child.Title = "LEGION-209", record.ParentOf(rig.issue.Key), "child"
			child.Status, child.Phase = record.DoneStatus, phase.Done
			putOutboxIssue(t, rig.pool, rig.issues, child)
			return child.Key
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newCopyRig(t)
			ctx := context.Background()
			rig.clock.advance(0)
			issue := tc.close(t, rig)
			rig.w.copyWorkspaceAtPush(issue)
			seen := len(rig.awaitUnsaved(t, runningCopyNoRole, 1))
			if owed := rig.w.copies.retries(); !slices.Equal(owed, []string{issue}) {
				t.Fatalf("the push's copy that found no role left the cadence owing retries of %v, want %s", owed, issue)
			}

			for tick := range 2 {
				rig.wind(t)
				rig.w.copyWorkingWorkspaces(ctx)
				if lines := rig.unsavedFor(t, runningCopyNoRole)[seen:]; len(lines) != 0 {
					t.Fatalf("due tick %d after the push logged %v, want no %s line for a closed issue", tick+1, lines, runningCopyNoRole)
				}
			}

			if owed := rig.w.copies.retries(); len(owed) != 0 {
				t.Fatalf("the cadence owes retries of %v, want none for a closed issue", owed)
			}
			if asked := rig.bundler.reads(); len(asked) != 0 {
				t.Fatalf("the bundler was asked %v for a closed issue", asked)
			}
		})
	}
}

// A copy refused for its size is no fault to retry, so it owes no retry: the work stays over the
// bound until a role pushes or drops some of it, and a retry between turns would have the shim
// build the same refused bundle every period for as long as the idle pod stands, none of which
// could be stored. A turn's end refused for its size on an idle claim is therefore asked once, and
// the periods after it ask nothing. With a turn running, the cadence asks again at its next period,
// as it asks of every issue in a turn, and not inside the period. The shim refuses the bundle with
// its size, as the fake connection answers it, as production's does.
func TestACopyRefusedForItsSizeOwesNoRetryAndIsAskedAgainOnlyInsideATurn(t *testing.T) {
	rig := newCopyRig(t)
	ctx := context.Background()
	rig.clock.advance(0)
	over := copyRigBundle
	over.Bundle = make([]byte, runtime.MaxWorkspaceBundleBytes+1)
	rig.conn.SetWorkspaceBundle(over)
	rig.bundler.holds(func(ctx context.Context) (runtime.WorkspaceBundle, error) {
		return shimBundle(ctx, rig.conn, rig.issue.Key)
	})

	rig.w.copyWorkspaceAtTurnEnd(rig.machine.Claim(), nil)
	rig.awaitUnsaved(t, runningCopyTooLarge, 1)
	rig.awaitQuiet(t)
	if owed := rig.w.copies.retries(); len(owed) != 0 {
		t.Fatalf("the copy refused for its size left retries of %v owed, want none", owed)
	}

	// The idle issue is left alone at every period after it.
	for range 3 {
		rig.wind(t)
		rig.w.copyWorkingWorkspaces(ctx)
	}
	rig.asksStay(t, 1)
	if lines := rig.unsavedFor(t, runningCopyTooLarge); len(lines) != 1 {
		t.Fatalf("the daemon logged %d copies refused for their size, want the turn's end's alone; it logged:\n%s", len(lines), rig.logs.String())
	}

	// A turn runs, and its period is long up: the cadence asks again, and then not again until the
	// next period.
	rig.turnStart(t)
	rig.w.copyWorkingWorkspaces(ctx)
	rig.awaitUnsaved(t, runningCopyTooLarge, 2)
	rig.awaitQuiet(t)
	rig.w.copyWorkingWorkspaces(ctx)
	rig.asksStay(t, 2)
	rig.wind(t)
	rig.w.copyWorkingWorkspaces(ctx)
	rig.awaitAsks(t, 3)
	rig.awaitQuiet(t)
}
