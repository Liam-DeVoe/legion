package daemon

import (
	"slices"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/texts"
)

// The change gives each suspended claim of an update-stalled notice its source (suspended_by,
// supervise.Claim.SuspendedBy), and the controller's prompt reads `released` as "the daemon's idle
// release of the claim's pod" (roles/controller-root.md, texts.UpdateWaiting's doc). A project's
// pause stops each role of a pod with the same source (supervise's pauseStopDue:
// RequestSuspend{By: SuspendedByReleased}). Such a claim is the one exception of the round
// (podHolds.owes): the round asks it and waits for it, and a podless round past its bound with it
// unanswered is told (tellPodlessRound), where a claim an idle release suspended is passed over and
// alone tells nothing. So the claim the round waits for, the reason the notice exists, is listed as
// an idle release, word for word as a claim the round passes over. R2: the fields are "the state
// that the reader acts on that its event has"; the ruling of round 10: the change must not state a
// fact wrongly. Here the two claims' rows are the same, and one of them is named what it is not.
func TestAnUpdateStalledNoticeTellsAPausesStopFromAnIdleRelease(t *testing.T) {
	const round = podUpdateRound
	opened := podUpdateStart
	clockNow := podUpdateBound.Add(time.Hour)
	claimOf := func(role claim.Role) supervise.Claim {
		return supervise.Claim{Token: mustClaimToken(t, podUpdateIssue, role), Project: podUpdateProject, Tree: podUpdateIssue,
			Issue: podUpdateIssue, Role: role, State: supervise.StateSuspended, SuspendedBy: supervise.SuspendedByReleased,
			Session: "ses-" + string(role), UpdateRound: round, UpdateReasons: podUpdateDrift, UpdateOpenedAt: &opened}
	}
	// The pause stopped the implementer and its resume ended the hold before the round opened; it
	// still owes the implementer a launch. The tester's pod was released idle, which no pause owes.
	hold := store.PausePodHold{ID: 1, Pause: 1, Project: "LEGION", Issue: podUpdateIssue, HeldAt: opened.Add(-time.Hour),
		EndedAt: opened.Add(-time.Minute), End: store.PausePodHoldResumed, Owed: []claim.Role{claim.RoleImplementer}}
	clock := roundClock{now: clockNow, grace: podUpdateGrace, holds: podHolds{hold}}
	group := []supervise.Claim{claimOf(claim.RoleImplementer), claimOf(claim.RoleTester)}

	unanswered := unansweredClaims(group, round, clock)
	if len(unanswered) != 1 || unanswered[0].Role != claim.RoleImplementer {
		t.Fatalf("the round waits for %+v, want the implementer the pause stopped alone", unanswered)
	}
	if !clock.passesOver(group[1], round) || clock.passesOver(group[0], round) {
		t.Fatalf("the round passes over the implementer %t and the tester %t, want the tester alone",
			clock.passesOver(group[0], round), clock.passesOver(group[1], round))
	}

	stalled := updateStalledNotice(updateStall{issue: podUpdateIssue, round: round, holds: texts.HoldsNoPod,
		waiting: waitingOf(round, noticeClaims(group)), group: group})
	rows := map[claim.Role]texts.UpdateWaiting{}
	for _, w := range stalled.Update.Waiting {
		w.Claim, w.Role = "", w.Role
		rows[w.Role] = w
	}
	implementer, tester := rows[claim.RoleImplementer], rows[claim.RoleTester]
	implementer.Role, tester.Role = "", ""
	if slices.Equal([]texts.UpdateWaiting{implementer}, []texts.UpdateWaiting{tester}) {
		t.Errorf("the update-stalled notice (holds: %s) lists the implementer a project's pause stopped, which the round waits for, and the tester an idle release suspended, which the round passes over, alike: %+v; both say suspended_by: %s, which the controller's prompt reads as the daemon's idle release of the claim's pod",
			stalled.Holds, implementer, implementer.SuspendedBy)
	}
}

// The same through the sweep: a podless round whose issue pod the runtime cannot say of (its
// IssuePod read answers nothing, so podlessRound reaches tellPodlessRound, as on a runtime that
// replaces no pod by issue), whose implementer a project's pause stopped (suspended released, the
// pause still owing it a launch) and whose tester an idle release suspended. An hour past the
// bound the round is told (holds: no-pod), because the pause's claim has not answered
// (unansweredClaims' podHolds.owes); the tester alone would tell nothing. The notice names both
// claims `suspended_by: released`, which the controller's prompt reads as "the daemon's idle
// release of the claim's pod".
func TestAPodlessUpdateStalledNoticeTellsAPausesStopFromAnIdleRelease(t *testing.T) {
	rig := newPodUpdateRig(t, releasedUnasked(pauseStoppedRole), releasedUnasked(claim.RoleTester))
	rig.recordPause(store.PausePodHoldResumed, pauseStoppedRole)
	rig.clockAt(podUpdateBound.Add(time.Hour))
	for range 3 {
		rig.tick()
	}
	rig.settleAll()

	notices := rig.notices()
	if len(notices) != 1 || notices[0].Holds != texts.HoldsNoPod || notices[0].Update == nil {
		t.Fatalf("the controller was told %+v, want one no-pod notice of the round the pause's claim holds", notices)
	}
	rows := map[claim.Role]texts.UpdateWaiting{}
	for _, w := range notices[0].Update.Waiting {
		role := w.Role
		w.Claim, w.Role = "", ""
		rows[role] = w
	}
	if len(rows) != 2 {
		t.Fatalf("the notice lists %+v, want the implementer and the tester", notices[0].Update.Waiting)
	}
	if rows[pauseStoppedRole] == rows[claim.RoleTester] {
		t.Errorf("the update-stalled notice (holds: %s) lists the %s a project's pause stopped, which the round waits for, and the tester an idle release suspended alike: %+v; suspended_by: %s is \"the daemon's idle release of the claim's pod\" in the controller's prompt",
			notices[0].Holds, pauseStoppedRole, rows[claim.RoleTester], rows[claim.RoleTester].SuspendedBy)
	}
}
