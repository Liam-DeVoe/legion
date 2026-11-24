package workflow

import (
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// The events of an ancestor's document, request and issue can reach a step that waits on the
// ancestor's approval request late: main's #4590 replays a record the intake gave up on through
// the handling a live delivery takes, after later events applied (dispatch://INFRA-4728). Each
// case here applies, after the pending registration, an event a later act undid, with the read of
// Dispatch its preparation makes now (intake.BasisRead, intake.AncestorRead), and the step must
// not move; each control applies an event whose read shows its act, and the step must move. So the
// cases fail where the engine acts on the event alone, and the controls where it acts on nothing.

// stillPending fails t unless LEGION-208's spec step still waits on ask-100 at version 4 after
// what, with nothing opened or ended, nobody told, and no pending-basis seed queued beside the
// registration's.
func stillPending(t *testing.T, pool *pgxpool.Pool, what string) {
	t.Helper()
	if gate := gatesOf(t, pool, gatedRoot)[record.DesignStepSpec]; !gate.PendingOnBasis() || gate.Basis.Pending.Ask != "ask-100" ||
		gate.Basis.Version != 4 || record.GateOpen(gate) {
		t.Errorf("the spec step after %s = %#v, want it still closed and pending on ask-100 at version 4", what, gate)
	}
	for _, kind := range []record.NoticeKind{"basis-ended", "design-approved", "basis-changes-requested"} {
		if got := noticesOfKind(t, pool, gatedRoot, kind); len(got) != 0 {
			t.Errorf("%s notices after %s = %#v, want none: the late event moved nothing", kind, what, got)
		}
	}
	if got := noticesOfKind(t, pool, basisAncestor, "resting-root"); len(got) != 1 {
		t.Errorf("resting-root notices after %s = %#v, want the pending registration's alone", what, got)
	}
	if seeds := pendingSeeds(t, pool, gatedRoot); len(seeds) != 1 {
		t.Errorf("pending-basis seeds after %s = %#v, want the registration's alone: the event carried its read", what, seeds)
	}
}

// ended fails t unless LEGION-208's spec step no longer waits and stands closed on its own
// document with no basis, after one basis-ended notice naming act, and nothing opened.
func ended(t *testing.T, pool *pgxpool.Pool, act string) {
	t.Helper()
	if gate := gatesOf(t, pool, gatedRoot)[record.DesignStepSpec]; gate.Basis != nil || record.GateOpen(gate) || gate.ArtifactID != gateDoc {
		t.Errorf("the spec step = %#v, want it closed on the root's own document with no basis", gate)
	}
	if got := noticesOfKind(t, pool, gatedRoot, "basis-ended"); len(got) != 1 || !strings.Contains(got[0].Reason, act) {
		t.Errorf("basis-ended notices = %#v, want one naming %q", got, act)
	}
	if got := noticesOfKind(t, pool, gatedRoot, "design-approved"); len(got) != 0 {
		t.Errorf("design-approved notices = %#v, want none: the step did not open", got)
	}
}

// The tester's three late events (dispatch://INFRA-4728's probe) and the fourth, each applied
// after the pending registration with the read its preparation makes now. Before the
// registration, the person asked for changes on version 4 at sequence 3, and the architect then
// asked for approval of version 4 again, as ask-100: so Dispatch shows ask-100 open, with that
// changes request as the latest review (latestThree).
//
//   - An approval of version 4 older than that changes request, given up then and replayed now,
//     opens nothing: Dispatch's latest review is no approval.
//   - That changes request itself, older than ask-100, replayed now, ends nothing: ask-100 is open.
//   - The withdrawal of ask-100 that the ancestor's close recorded, given up, replayed after the
//     ancestor's reopen opened ask-100 again (Dispatch records no restore of an approval request:
//     the reopen's own ask.opened is the event), ends nothing: the read shows ask-100 open and the
//     ancestor at work.
//   - The person's approval at 60, delivered after their changes request at 62 superseded it,
//     opens nothing and ends the wait on the changes request the read shows; that changes request,
//     applied after, adds nothing.
func TestALateEventOnAPendingBasisChangesNothingDispatchNoLongerShows(t *testing.T) {
	reason := "name the rollback path"
	latestThree := requestOpen()
	latestThree.Version, latestThree.Reason, latestThree.By = new(4), &reason, basisPerson
	latestThree.Seq, latestThree.At = 3, ancestorReview(intake.DispatchArtifactChangesRequested, 4, 3, reason).Mark.At
	withdrawal := intake.DispatchAskClosed{Key: basisAncestor, Ask: "ask-100", Kind: record.AskApproval, Artifact: basisDoc, State: "resolved",
		Actor: basisPerson, Mark: askSeq(61), Withdrawn: true}

	t.Run("an approval of the version older than the person's changes request on it", func(t *testing.T) {
		pool, _, apply := registeredPending(t)
		late := ancestorReview(intake.DispatchArtifactApproved, 4, 2, "")
		late.BasisRead = readOf(latestThree, ancestorAtWork)

		apply("late", late)

		stillPending(t, pool, "the late approval")
	})

	t.Run("a changes request on the version older than the request the step waits on", func(t *testing.T) {
		pool, _, apply := registeredPending(t)
		late := ancestorReview(intake.DispatchArtifactChangesRequested, 4, 3, reason)
		late.BasisRead = readOf(latestThree, ancestorAtWork)

		apply("late", late)

		stillPending(t, pool, "the late changes request")
	})

	t.Run("a withdrawal of the request the ancestor's reopen opened again", func(t *testing.T) {
		pool, _, apply := registeredPending(t)
		apply("reopened", intake.DispatchIssue{Key: basisAncestor, Seq: 63, Type: "issue.updated", Status: "in_progress", Title: "ancestor", Rank: "T",
			HandedOver: true, AncestorRead: &ancestorAtWork})
		apply("request-opened-again", intake.DispatchAskOpened{Key: basisAncestor, Ask: "ask-100", Kind: record.AskApproval, Artifact: basisDoc, Mark: askSeq(64)})
		late := withdrawal
		late.BasisRead = readOf(requestOpen(), ancestorAtWork)

		apply("late", late)

		stillPending(t, pool, "the late withdrawal")
	})

	t.Run("an approval a later changes request superseded", func(t *testing.T) {
		pool, _, apply := registeredPending(t)
		superseding := ancestorReview(intake.DispatchArtifactChangesRequested, 4, 62, reason)
		late := ancestorReview(intake.DispatchArtifactApproved, 4, 60, "")
		late.BasisRead = readOf(latestOf(superseding), ancestorAtWork)
		changes := `user:alice requested changes on version 4 of the primary document of LEGION-100 (dispatch://LEGION-100/spec): "name the rollback path".`

		apply("late", late)

		ended(t, pool, changes)
		if got := noticesOfKind(t, pool, basisAncestor, "resting-root"); len(got) != 2 || !strings.Contains(got[1].Reason, changes) {
			t.Errorf("resting-root notices = %#v, want the registration's and the end naming the changes request", got)
		}

		apply("superseding", current(superseding))

		ended(t, pool, changes)
		for _, kind := range []record.NoticeKind{"basis-changes-requested", "design-changes-requested"} {
			if got := noticesOfKind(t, pool, gatedRoot, kind); len(got) != 0 {
				t.Errorf("%s notices after the changes request = %#v, want none: the wait it ended had ended", kind, got)
			}
		}
		if seeds := pendingSeeds(t, pool, gatedRoot); len(seeds) != 1 {
			t.Errorf("pending-basis seeds = %#v, want the registration's alone", seeds)
		}
	})
}

// The control of the late events: the person's approval at 60, delivered after they asked for
// changes at 61 and approved again at 63, carries a read that shows the approval at 63 as
// Dispatch's latest review of the latest version. The read opens the step, on that latest
// review's place in the ancestor's event log and its reviewer, as a registration on that approval
// would, not on the event's. The open step keeps that place as its approval (BasisChangesRequest's
// mark.After), so the changes request at 61, delivered after the opening, passes nothing on and
// leaves the step open, while one recorded after the approval at 63 is passed on.
func TestAnApprovalTheReadShowsOpensTheStepAndAnOlderChangesRequestPassesNothing(t *testing.T) {
	pool, _, apply := registeredPending(t)
	latest := ancestorReview(intake.DispatchArtifactApproved, 4, 63, "")
	late := ancestorReview(intake.DispatchArtifactApproved, 4, 60, "")
	late.BasisRead = readOf(latestOf(latest), ancestorAtWork)

	apply("late-approval", late)

	gate := gatesOf(t, pool, gatedRoot)[record.DesignStepSpec]
	if !gate.OnBasis() || !record.GateOpen(gate) || gate.Basis.Approval != latest.Mark || gate.Basis.Approver != "user:alice" {
		t.Fatalf("the spec step = %#v, want it open on the approval the read shows, at %#v by user:alice", gate, latest.Mark)
	}
	if got := noticesOfKind(t, pool, gatedRoot, "design-approved"); len(got) != 1 {
		t.Fatalf("design-approved notices = %#v, want one", got)
	}

	apply("older-changes", ancestorReview(intake.DispatchArtifactChangesRequested, 4, 61, "an earlier objection"))

	if gate := gatesOf(t, pool, gatedRoot)[record.DesignStepSpec]; !gate.OnBasis() || !record.GateOpen(gate) || gate.Basis.Approval != latest.Mark {
		t.Fatalf("the spec step after the older changes request = %#v, want it still open on the approval at 63", gate)
	}
	for _, kind := range []record.NoticeKind{"basis-changes-requested", "basis-ended"} {
		if got := noticesOfKind(t, pool, gatedRoot, kind); len(got) != 0 {
			t.Fatalf("%s notices after the older changes request = %#v, want none: it came before the approval the step opened on", kind, got)
		}
	}

	apply("newer-changes", ancestorReview(intake.DispatchArtifactChangesRequested, 4, 64, "name the rollback path"))

	if got := noticesOfKind(t, pool, gatedRoot, "basis-changes-requested"); len(got) != 1 || !strings.Contains(got[0].Reason, `"name the rollback path"`) {
		t.Fatalf("basis-changes-requested notices after the newer changes request = %#v, want one naming its reason", got)
	}
	if gate := gatesOf(t, pool, gatedRoot)[record.DesignStepSpec]; !gate.OnBasis() || !record.GateOpen(gate) {
		t.Fatalf("the spec step after the newer changes request = %#v, want it still open (R3)", gate)
	}
}

// An event that reaches a waiting step with no read of Dispatch on it (one applied with no
// preparation, as the boot's catch-up applies one, or one prepared before the registration
// committed) opens and ends nothing on its own: it queues the step's pending-basis seed, whose
// read the workflow judges instead. The person's approval, a close of the request and the
// ancestor's done are each such a cue here.
func TestACueWithNoReadQueuesThePendingBasisSeedAndChangesNothing(t *testing.T) {
	for _, tc := range []struct {
		name string
		fact intake.Fact
	}{
		{"the person's approval", ancestorReview(intake.DispatchArtifactApproved, 4, 60, "")},
		{"a session's close of the request", intake.DispatchAskClosed{Key: basisAncestor, Ask: "ask-100", Kind: record.AskApproval, Artifact: basisDoc,
			State: "resolved", Actor: basisPerson, Mark: askSeq(61)}},
		{"the ancestor done", intake.DispatchIssue{Key: basisAncestor, Seq: 70, Type: "issue.updated", Status: record.DoneStatus, Title: "ancestor", Rank: "T",
			HandedOver: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool, _, apply := registeredPending(t)

			apply("cue", tc.fact)

			if gate := gatesOf(t, pool, gatedRoot)[record.DesignStepSpec]; !gate.PendingOnBasis() || record.GateOpen(gate) {
				t.Fatalf("the spec step = %#v, want it still pending on ask-100", gate)
			}
			for _, kind := range []record.NoticeKind{"basis-ended", "design-approved"} {
				if got := noticesOfKind(t, pool, gatedRoot, kind); len(got) != 0 {
					t.Fatalf("%s notices = %#v, want none: an event with no read moves nothing", kind, got)
				}
			}
			want := record.PendingBasisSeed{Step: record.DesignStepSpec, ArtifactID: gateDoc, BasisArtifactID: basisDoc, BasisVersion: 4, Ask: "ask-100", Generation: 1}
			if seeds := pendingSeeds(t, pool, gatedRoot); len(seeds) != 2 || seeds[1] != want {
				t.Fatalf("pending-basis seeds = %#v, want the registration's and the cue's %#v", seeds, want)
			}
		})
	}
}

// The ancestor's done, given up and replayed while its reopen is not yet applied, ends no wait
// on its request: the read of the issue its preparation makes shows the ancestor at work and
// labelled (R1's first condition), so the event is a cue that finds the ancestor still meeting
// it. The control: the same event whose read shows the ancestor done ends the wait, naming that.
func TestALateAncestorDoneEndsNoWaitWhileDispatchShowsItOpen(t *testing.T) {
	done := func(read intake.AncestorRead) intake.DispatchIssue {
		return intake.DispatchIssue{Key: basisAncestor, Seq: 70, Type: "issue.updated", Status: record.DoneStatus, Title: "ancestor", Rank: "T",
			HandedOver: true, AncestorRead: &read, ReadFor: theWait}
	}

	t.Run("the read shows the ancestor reopened", func(t *testing.T) {
		pool, _, apply := registeredPending(t)

		apply("late-done", done(ancestorAtWork))

		stillPending(t, pool, "the late done")
	})

	t.Run("the read shows the ancestor done", func(t *testing.T) {
		pool, _, apply := registeredPending(t)

		apply("done", done(intake.AncestorRead{Status: record.DoneStatus, HandedOver: true}))

		ended(t, pool, "Dispatch shows LEGION-100 done, so LEGION-100 is accepted.")
	})
}

// A cue's read decides only the steps its preparation read Dispatch for (intake.BasisStep): a
// step that came to wait on the ancestor between that read and the fact's apply is judged by a
// read of its own, its pending-basis seed, and never by a read taken before it waited. Here the
// preparation read Dispatch for LEGION-208's wait on ask-100 and found that wait's end: the
// person's changes request on version 4, ask-100 closed, or the ancestor done. Before the fact
// takes the lock, LEGION-209 registers a step on version 4 (on ask-200, which the architect opened
// on that version after the changes request and the reopen, or on ask-100 itself, registered
// from a read before its close). LEGION-208's wait ends as the read shows, and LEGION-209's stays,
// with its seed queued, though the stale read would end it. The same holds for LEGION-208 itself
// registered again on ask-200 after the read: the read was for its wait on ask-100.
func TestACuesReadDecidesOnlyTheStepsItWasReadFor(t *testing.T) {
	const later, laterDoc = "LEGION-209", "artifact-209"
	reason := "name the rollback path"
	changes := ancestorReview(intake.DispatchArtifactChangesRequested, 4, 62, reason)
	for _, tc := range []struct {
		name string
		ask  string
		fact intake.Fact
		act  string
	}{
		{name: "the person's changes request", ask: "ask-200", fact: current(changes),
			act: `user:alice requested changes on version 4 of the primary document of LEGION-100 (dispatch://LEGION-100/spec): "name the rollback path".`},
		{name: "a session's close of the request", ask: "ask-100", fact: intake.DispatchAskClosed{Key: basisAncestor, Ask: "ask-100", Kind: record.AskApproval,
			Artifact: basisDoc, State: "resolved", Actor: basisPerson, Mark: askSeq(61), BasisRead: readOf(requestClosed(), ancestorAtWork)},
			act: "user:alice closed approval request ask-100 of version 4"},
		{name: "the ancestor done", ask: "ask-200", fact: intake.DispatchIssue{Key: basisAncestor, Seq: 70, Type: "issue.updated", Status: record.DoneStatus,
			Title: "ancestor", Rank: "T", HandedOver: true, AncestorRead: &intake.AncestorRead{Status: record.DoneStatus, HandedOver: true}, ReadFor: theWait},
			act: "Dispatch shows LEGION-100 done, so LEGION-100 is accepted."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool, _, apply := registeredPending(t)
			seedIssue(t, pool, record.Issue{Key: later, Repo: "sjawhar/legion", DefaultBranch: "main", GitHubRepo: "sjawhar/legion",
				Tree: later, Project: "LEGION", Title: "later", Phase: phase.Admitted, Generation: 1, Status: "in_progress", Rank: "V",
				HandedOver: true, LastDispatchSeq: 1})
			waiting := pendingBasis()
			waiting.RegisteredAt = basisEngineNow.Add(time.Hour)
			seedGate(t, pool, record.PendOnBasis(record.DesignGate{Issue: later, ArtifactID: laterDoc, LatestVersion: 4}, *waiting, tc.ask))

			apply("cue", tc.fact)

			ended(t, pool, tc.act)
			if gate := gatesOf(t, pool, later)[record.DesignStepSpec]; !gate.PendingOnBasis() || gate.Basis.Pending.Ask != tc.ask || record.GateOpen(gate) {
				t.Errorf("the spec step of %s = %#v, want it still pending on %s: the read was not taken for it", later, gate, tc.ask)
			}
			if got := noticesOfKind(t, pool, later, "basis-ended"); len(got) != 0 {
				t.Errorf("basis-ended notices of %s = %#v, want none", later, got)
			}
			want := record.PendingBasisSeed{Step: record.DesignStepSpec, ArtifactID: laterDoc, BasisArtifactID: basisDoc, BasisVersion: 4, Ask: tc.ask, Generation: 1}
			if seeds := pendingSeeds(t, pool, later); len(seeds) != 1 || seeds[0] != want {
				t.Errorf("pending-basis seeds of %s = %#v, want its own read, %#v", later, seeds, want)
			}
		})
	}

	t.Run("the step registered again on another request", func(t *testing.T) {
		pool, _, apply := registeredPending(t)
		again := pendingBasis()
		again.Pending = &record.PendingBasis{Ask: "ask-200"}
		apply("register-on-ask-200", onBasis(again, 7))

		apply("cue", current(changes))

		if gate := gatesOf(t, pool, gatedRoot)[record.DesignStepSpec]; !gate.PendingOnBasis() || gate.Basis.Pending.Ask != "ask-200" || record.GateOpen(gate) {
			t.Fatalf("the spec step = %#v, want it still pending on ask-200: the read was taken for its wait on ask-100", gate)
		}
		if got := noticesOfKind(t, pool, gatedRoot, "basis-ended"); len(got) != 0 {
			t.Fatalf("basis-ended notices = %#v, want none", got)
		}
		if seeds := pendingSeeds(t, pool, gatedRoot); len(seeds) != 3 || seeds[2].Ask != "ask-200" {
			t.Fatalf("pending-basis seeds = %#v, want the two registrations' and the cue's on ask-200", seeds)
		}
	})
}
