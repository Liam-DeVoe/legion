package daemon

// The running copy's retry: a copy that came to nothing for a fault is taken again at the
// cadence's next period, between turns too, once the issue has a live role to ask; the cadence
// forgets an issue no claim names but a retired one; and a copy refused for its size owes no
// retry. These cases drive the copy rig of workspace_copies_test.go.

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

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

// The cadence forgets an issue that no claim of the project names at all — the issue of a branch
// pushed from outside, never run in this project — retry and all, since no copy of it can ever be
// stored to clear that retry, and the cadence's memory would grow with every such issue. A push of
// such an issue's branch logs its copy that found no role as a push of any issue does: at Info for
// the first in a period (noLiveRole). An issue with a claim that is not retired keeps its retry,
// whatever other state its claim is in.
func TestTheCadenceForgetsTheRetryOfAnIssueNoClaimNamesAndKeepsAClaimedIssues(t *testing.T) {
	rig := newCopyRig(t)
	ctx := context.Background()
	const unclaimed = "LEGION-404"
	rig.setClaimState(t, "suspended")
	rig.w.copyWorkspaceAtPush(unclaimed)
	rig.w.copyWorkspaceAtPush(rig.issue.Key)
	for _, line := range rig.awaitUnsaved(t, runningCopyNoRole, 2) {
		if line["level"] != "INFO" {
			t.Fatalf("the first push's copy of %v that found no role is logged as %v, want it at INFO", line["issue"], line)
		}
	}
	if owed := rig.w.copies.retries(); !slices.Equal(owed, []string{rig.issue.Key, unclaimed}) {
		t.Fatalf("the cadence owes retries of %v, want both copies that found no role", owed)
	}

	rig.w.copyWorkingWorkspaces(ctx)

	if owed := rig.w.copies.retries(); !slices.Equal(owed, []string{rig.issue.Key}) {
		t.Fatalf("after a pass the cadence owes retries of %v, want only %s, whose claim it read", owed, rig.issue.Key)
	}
	rig.w.copies.mu.Lock()
	_, remembered := rig.w.copies.issues[unclaimed]
	rig.w.copies.mu.Unlock()
	if remembered {
		t.Fatalf("the cadence still remembers %s, which no claim names", unclaimed)
	}
}

// A finished issue's claims are all retired, and the store keeps every claim for ever, so a retry
// such an issue owes — here a push of its branch whose copy found no live role — could never be
// paid: no role of it will be live again to ask. The cadence forgets it, as it forgets an issue
// no claim names, so the due ticks after the push name it under no_live_role no more, ask nothing,
// and owe it no retry. Kept, it was a line at Info every 5 minutes for as long as the daemon ran.
//
// The control is the cadence building the claims it keeps retries for from every claim, retired
// ones included: the first due tick then names the issue again, and the test fails there.
func TestTheCadenceForgetsTheRetryOfAnIssueWhoseClaimsAreAllRetired(t *testing.T) {
	rig := newCopyRig(t)
	ctx := context.Background()
	rig.clock.advance(0)
	rig.setClaimState(t, string(supervise.StateRetired))
	rig.w.copyWorkspaceAtPush(rig.issue.Key)
	seen := len(rig.awaitUnsaved(t, runningCopyNoRole, 1))
	if owed := rig.w.copies.retries(); !slices.Equal(owed, []string{rig.issue.Key}) {
		t.Fatalf("the push's copy that found no role left the cadence owing retries of %v, want %s", owed, rig.issue.Key)
	}

	for tick := range 2 {
		rig.wind(t)
		rig.w.copyWorkingWorkspaces(ctx)
		if lines := rig.unsavedFor(t, runningCopyNoRole)[seen:]; len(lines) != 0 {
			t.Fatalf("due tick %d after the push logged %v, want no %s line for an issue whose claims are all retired", tick+1, lines, runningCopyNoRole)
		}
	}

	if owed := rig.w.copies.retries(); len(owed) != 0 {
		t.Fatalf("the cadence owes retries of %v, want none for an issue whose claims are all retired", owed)
	}
	if asked := rig.bundler.reads(); len(asked) != 0 {
		t.Fatalf("the bundler was asked %v for an issue whose claims are all retired", asked)
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
