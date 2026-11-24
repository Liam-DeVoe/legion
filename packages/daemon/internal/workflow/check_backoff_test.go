package workflow

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// lastWake is the newest check_wake row of issue: when it is due, and the wake it carries.
func lastWake(t *testing.T, rig checkRig, issue string) (time.Time, record.CheckWakeDue) {
	t.Helper()
	rows := checkWakeRows(t, rig.pool, issue)
	if len(rows) == 0 {
		t.Fatalf("%s has no check_wake row", issue)
	}
	row := rows[len(rows)-1]
	return row.nextAt, decodeWake(t, row)
}

// startLookAt applies the check_wake row of wake at its time, as the outbox does when the row comes
// due, and fails the test unless the look after it is then open.
func startLookAt(t *testing.T, rig checkRig, issue string, at time.Time, wake record.CheckWakeDue) {
	t.Helper()
	rig.clock.now = at
	rig.apply(fmt.Sprintf("wake-%d", wake.Look), intake.CheckWakeDue{Issue: issue, Look: wake.Look, OpenedAt: wake.OpenedAt, Reason: wake.Reason})
	if check := productionCheck(t, rig.pool, issue); check.Looks != wake.Look+1 || !check.LookOpen {
		t.Fatalf("the check at the wake of look %d = %+v, want look %d open", wake.Look, check, wake.Look+1)
	}
}

// failLook fails the checking role's claim at at, inside the look the check holds open, and
// answers when the next look is due and its wake.
func failLook(t *testing.T, rig checkRig, issue string, at time.Time) (time.Time, record.CheckWakeDue) {
	t.Helper()
	rig.clock.now = at
	look := productionCheck(t, rig.pool, issue).Looks
	rig.apply(fmt.Sprintf("tester-failed-%d", look), failedClaim(issue, claim.RoleTester))
	check := productionCheck(t, rig.pool, issue)
	if check.LookOpen || check.Looks != look {
		t.Fatalf("the check after its look %d lost its tester = %+v, want that look ended", look, check)
	}
	due, wake := lastWake(t, rig, issue)
	if wake.Look != look || check.WakeAt == nil || !check.WakeAt.Equal(due) {
		t.Fatalf("the check after its look %d lost its tester = %+v, its newest wake %+v due %s; want the wake of that look, the one the check records", look, check, wake, due)
	}
	return due, wake
}

// The checking role's claim failing in look after look backs the check off: the first failed look
// names the next a look bound (2 h) out, and each failed look in a row after it doubles that, so
// the second and third failures name wakes 4 h and 8 h after their looks, and the fourth 16 h.
// The doubling never names a wake past the check's bound: the fifth, 32 h out from a look 30 h
// into a 48 h check, names none, and the bound's own row starts the last look. Each look after a
// failed one is a start of the checking role, which the outbox runs as the retry of the failed
// claim with one attempt left (TestCheckLookStartRetriesAFailedClaimWithOneAttemptLeft). At main
// every failed look named its next one 2 h out, so a role that could not start was launched a
// whole budget's worth of times every two hours until the check's bound.
func TestACheckRoleFailureDoublesTheNextLookInterval(t *testing.T) {
	rig := childCheckRig(t)
	at := checkOpened
	for i, want := range []time.Duration{2 * time.Hour, 4 * time.Hour, 8 * time.Hour, 16 * time.Hour} {
		due, wake := failLook(t, rig, "LEGION-209", at)
		if got := due.Sub(at); got != want {
			t.Fatalf("failure %d at %s named the next look %s out, want %s", i+1, at, got, want)
		}
		startLookAt(t, rig, "LEGION-209", due, wake)
		starts := superviseRowsOf(t, rig.pool, "LEGION-209", "start")
		if last := starts[len(starts)-1]; last.CheckLook != wake.Look+1 || last.Role != claim.RoleTester || last.FreshSession {
			t.Fatalf("the newest start after failure %d = %+v, want look %d handed to the tester, asking no new session", i+1, last, wake.Look+1)
		}
		at = due
	}
	wakes := len(checkWakeRows(t, rig.pool, "LEGION-209"))
	rig.clock.now = at
	rig.apply("tester-failed-5", failedClaim("LEGION-209", claim.RoleTester))
	check := productionCheck(t, rig.pool, "LEGION-209")
	if !check.Open() || check.LookOpen || check.WakeAt != nil {
		t.Fatalf("the check after its fifth failed look, %s into a %s check = %+v, want it open, the look ended and no wake: 32 h out is past its bound", at.Sub(checkOpened), checkBound, check)
	}
	if got := len(checkWakeRows(t, rig.pool, "LEGION-209")); got != wakes {
		t.Fatalf("check_wake rows = %d after the fifth failed look, want the %d before it: no wake past the bound", got, wakes)
	}
}

// A look its role reports ends the run of failed looks: after two failed looks (2 h, then 4 h),
// the third look's tester reports pending with no time of its own, which names the next look a
// look bound out as every such report does, and the failure of the look after it names its next
// look 2 h out again, not 8 h.
func TestAReportedLookResetsTheCheckRoleBackoffToTwoHours(t *testing.T) {
	rig := childCheckRig(t)
	due, wake := failLook(t, rig, "LEGION-209", checkOpened)
	startLookAt(t, rig, "LEGION-209", due, wake)
	due, wake = failLook(t, rig, "LEGION-209", due)
	if got := due.Sub(checkOpened.Add(CheckLookBound)); got != 4*time.Hour {
		t.Fatalf("the second failed look named its next %s out, want 4h: the backoff this test resets did not build", got)
	}
	startLookAt(t, rig, "LEGION-209", due, wake)

	reported := due.Add(20 * time.Minute)
	rig.clock.now = reported
	rig.apply("pending-3", intake.CheckReport{Issue: "LEGION-209", Look: 3, Verdict: "pending", Summary: "the deploy has not carried the merge"})
	due, wake = lastWake(t, rig, "LEGION-209")
	if wake.Look != 3 || !due.Equal(reported.Add(CheckLookBound)) {
		t.Fatalf("the newest wake after look 3's pending = %+v due %s, want look 3's a look bound out, at %s", wake, due, reported.Add(CheckLookBound))
	}
	startLookAt(t, rig, "LEGION-209", due, wake)

	failed, _ := failLook(t, rig, "LEGION-209", due)
	if got := failed.Sub(due); got != CheckLookBound {
		t.Fatalf("the failed look after a reported one named its next %s out, want %s: a report ends the run of failed looks", got, CheckLookBound)
	}
}

// A claim of the checking role that fails after its look ended — the look's bound ended it while
// the claim was still failing its way through its budget, so no look is open — counts as a failed
// look too. The wake that look's end named stands, and its look's start retries the claim as any
// look after a failed one does, with one attempt left (the daemon's outbox); the architect is told
// at once in a worker-died that names the look, the failure's cause and that retry. The failure of
// the look that wake starts is the second failed look in a row, so it names the next look 4 h out.
// At main a failure between looks reached nothing at all: no notice, and the next failure named
// its next look 2 h out like the first.
func TestACheckRoleFailureBetweenLooksIsRetriedAtTheNextLook(t *testing.T) {
	rig := childCheckRig(t)
	bound := checkOpened.Add(CheckLookBound)
	rig.clock.now = bound
	rig.apply("look-bound-1", intake.CheckLookEnded{Issue: "LEGION-209", Look: 1, Cause: "look bound", OpenedAt: checkOpened})
	due, wake := lastWake(t, rig, "LEGION-209")
	if wake.Look != 1 || !due.Equal(bound.Add(CheckLookBound)) {
		t.Fatalf("the wake look 1's bound named = %+v due %s, want look 1's a look bound out, at %s", wake, due, bound.Add(CheckLookBound))
	}
	wakes := len(checkWakeRows(t, rig.pool, "LEGION-209"))
	before := len(noticesOfKind(t, rig.pool, "LEGION-209", "worker-died"))

	rig.clock.now = bound.Add(10 * time.Minute)
	rig.apply("tester-failed-between", failedClaim("LEGION-209", claim.RoleTester))
	check := productionCheck(t, rig.pool, "LEGION-209")
	if !check.Open() || check.LookOpen || check.Looks != 1 || check.WakeAt == nil || !check.WakeAt.Equal(due) {
		t.Fatalf("the check after its tester failed between looks = %+v, want it open after look 1 with the wake at %s standing", check, due)
	}
	if got := len(checkWakeRows(t, rig.pool, "LEGION-209")); got != wakes {
		t.Fatalf("check_wake rows = %d after the failure between looks, want the %d before it: the standing wake starts the next look", got, wakes)
	}
	died := noticesOfKind(t, rig.pool, "LEGION-209", "worker-died")
	if len(died) != before+1 {
		t.Fatalf("worker-died notices = %d after the failure between looks, want %d: the architect is told", len(died), before+1)
	}
	reason := died[len(died)-1].Reason
	for _, want := range []string{
		"production check of LEGION-209 lost its tester after look 1 ended: ",
		"its cause is never_registered",
		"look 2 retries the claim at " + due.Format(time.RFC3339) + " with one attempt left of the budget its failure spent",
	} {
		if !strings.Contains(reason, want) {
			t.Errorf("the worker-died of the failure between looks reads %q, want it to say %q", reason, want)
		}
	}

	startLookAt(t, rig, "LEGION-209", due, wake)
	starts := superviseRowsOf(t, rig.pool, "LEGION-209", "start")
	if last := starts[len(starts)-1]; last.CheckLook != 2 || last.Role != claim.RoleTester || last.FreshSession {
		t.Fatalf("the newest start at the standing wake = %+v, want look 2 handed to the tester, asking no new session", last)
	}
	failed, _ := failLook(t, rig, "LEGION-209", due)
	if got := failed.Sub(due); got != 2*CheckLookBound {
		t.Fatalf("look 2's failure after the failure between looks named its next look %s out, want %s: the second failed look in a row", got, 2*CheckLookBound)
	}
}
