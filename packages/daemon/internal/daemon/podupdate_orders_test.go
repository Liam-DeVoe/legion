package daemon

// The ends of an update round over a pod, P, against every kind of member the round can have
// (dispatch://INFRA-4425). Every end names a member by one fact, read by one predicate
// (workOnRoundPod): the member had not answered the round, and its unpushed work may be on P's
// disk, because an agent of it registered a session in P, its first launch after an idle release
// went into P or was refused for it, the round passes it over, or a restart left its set unknown.
// Each row of the table brings the implementer, as one kind of member, to one end of the round
// over P, and holds the issue's message and the end's log line to naming the implementer, or to
// naming nobody.
//
// The ends (roundPodEnd): the sweep replaces P through a claim whose launch waits on it
// (replacePodForRound); the sweep replaces P through another claim's process, the tester's, whose
// read of the workspace finds work (readRoundWorkspace); P goes on its own, and the runtime
// answers it gone while the claims still record it (endRoundOverAGonePod); P is recreated with
// the exchange identity, and the claims record the pod in its place (endRoundWhosePodWent); P is
// recreated without it, and the runtime refuses each retry again for the pod in its place, which
// the claims then wait on; and a launch replaces P (roundPodEndsAtLaunch,
// endRoundALaunchReplaced).
//
// The members (memberKind): an agent of it registered in P and it answered; an agent of it
// registered in P and it did not answer; an agent of it registered in P, and it lost that
// session, its fresh start refused for P with no session, so it waits on P; it was refused for P
// with no session and never registered there; it was refused for P with no session and its retry
// came up in the pod in P's place, where its agent registered; an agent of it registered in P, and
// when P went its relaunch found no session and started fresh in the pod in P's place, whose
// agent has not registered by the end; and the three restart rows the architect asked for
// (2026-11-22T12:14Z), each a machine the daemon's restart built over a stored claim: one whose
// state holds a registered agent in P, which always shows the session that agent registered, so
// the set is unknown (named); one that shows an abandoned session and no registered agent, so the
// set is unknown and the end names it, both where it waits on P with no session at the end and
// where it records a process in the pod in P's place by then; and one that shows no session, so
// the set is known and empty (named nothing).
//
// Then the members an idle release suspended, the rows INFRA-1484's architect asked for
// (2026-11-22T17:33Z). An agent of each of the implementer and the tester registered in an earlier
// pod, P0. The release of P0 saved the unpushed work of the workspace both share as a bundle and
// suspended both, and the tester's wake brought the issue's pod back as P, which restored the
// bundle before any role started, and where the tester registered. Then the implementer stays
// released, so the round passes it over (named); the same after a second release, of the pod
// between P0 and P, with the implementer released throughout (named); the same where P mounts
// the issue's kept volume, so P drops the bundle, since the volume holds the work (named, and the
// work is on that volume); a wake of the implementer during the round that the runtime refuses for
// P before its agent registers (named); and a wake that comes up in P before its agent registers
// (named). The last two are not passed over any more, and no agent of them registered in P: their
// first launch after the release put the pod it went into, or was refused for, in their set
// (supervise's Machine.addRestoringPod). Two more rows hold the passed-over case to an agent that
// ran somewhere: a released member whose session is forgotten during the round, whose set still
// holds P0, is named where the pod goes; and a member refused for P with no session, which never
// registered anywhere and which the daemon suspends during the round, is named at no end.
//
// The pairs the table has no row for are of two sorts. These cannot occur, each for the reason
// given:
//
//   - A member that runs in the pod in P's place (registered there, or started fresh there) beside
//     the sweep's replacement of P, through a waiting claim or through a process: the claims would
//     record two pods (groupPod), which holds every replacement while it lasts, or, with no claim
//     left in P, record the pod in P's place, which is the end where P is recreated.
//   - The same member where P goes on its own and the runtime answers it gone: a claim that records
//     the pod in P's place makes the sweep read the claims' other pod before it asks the runtime
//     about P (reconcileIssuePodUpdate), which is the end where P is recreated.
//   - The same member where P is recreated without the identity: the pod in P's place refuses
//     every launch, so no retry comes up anywhere, and a fresh start refused there is the member
//     that lost its session and waits.
//   - A member woken into P that comes up there before its agent registers, where the sweep
//     replaces P through a waiting claim or through the tester's process, or a launch replaces P:
//     its process in P is the one the sweep replaces P through (podProcess) and keeps P from a
//     launch's replacement (keepAtLaunch), and the round asks it and waits for its answer
//     (roundAsks, awaitsAnswer), so the sweep replaces P only at the round's bound.
//
// The others are left out on purpose. A member released through a chain of two releases, or on
// a kept volume, is held at two ends, the read through the tester's process and P going on its
// own: it is named by the same case of the predicate as the member released once, whose rows
// cover every end, and only the way its work came to P's disk differs. A released member whose
// session is forgotten is held where P goes, on its own or recreated: the replacement through a
// process names no claim with no session that the round did not wait for (notWaitedFor reads the
// claims with a session), so the other ends name it nothing whatever the predicate says.
//
// The negative controls run in place with podupdate.go or machine.go restored byte for byte after
// each (the pull request records the runs): workOnRoundPod answering true fails every row of a
// member that had not answered and names nothing; answering false fails every row that names;
// notAskedToPush's loop over sessionClaims(group) in place of group fails the row where the sweep
// replaces P through the tester's process past a member that lost its session; its passed-over
// case read for every claim the round passes over fails the rows of the member that never ran,
// and read only for a claim with a session fails the rows of the released member whose session
// was forgotten; and addRestoringPod adding nothing fails every row of a woken member.
// podupdate.go, machine.go and table.go as they stood at 692d89cb88 fail every row of a member an
// idle release suspended.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/runtime/fake"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// roundPodEnd is how the round over podUpdatePod ends in the order table.
type roundPodEnd int

const (
	// endBySweep is the sweep's own replacement of P through a claim whose launch waits on it
	// (replacePodForRound), the implementer, or the tester where the implementer stays released: no
	// role process runs in P, so no read of the workspace is made.
	endBySweep roundPodEnd = iota
	// endThroughAProcess is the sweep's own replacement of P through the tester's process, once the
	// tester answered, after one read of the workspace through the tester where a member's work is
	// on P's disk, which the runtime is scripted to find work in (readRoundWorkspace).
	endThroughAProcess
	// endPodGone is P going on its own, which the runtime answers gone while the claims still record
	// it, as a process or as the pod a launch waits on (endRoundOverAGonePod).
	endPodGone
	// endRecreated is P recreated in podUpdateReplacementPod with the exchange identity: the next
	// launch of each claim that ran in P comes up there, and the sweep reads the claims recording it
	// (reconcileIssuePodUpdate, endRoundWhosePodWent).
	endRecreated
	// endRecreatedRefusing is P recreated in podUpdateReplacementPod without the exchange identity:
	// the runtime refuses the next launch of each claim that ran in P for that pod, and the sweep
	// reads the claims waiting on it (endRoundWhosePodWent).
	endRecreatedRefusing
	// endAtALaunch is a launch's replacement of P (roundPodEndsAtLaunch), the implementer's own, or
	// the tester's where the implementer stays released, which the sweep ends the round as
	// (endRoundALaunchReplaced).
	endAtALaunch
)

func (e roundPodEnd) String() string {
	return [...]string{
		"the sweep replaces the pod through a waiting claim",
		"the sweep replaces the pod through the tester's process after a read that finds work",
		"the pod goes on its own",
		"the pod is recreated with the exchange identity",
		"the pod is recreated without it and refuses the retry again",
		"a launch replaces the pod",
	}[e]
}

// memberKind is what the implementer, the member each row is about, did in and around P before
// the round's end.
type memberKind int

const (
	answeredInPod memberKind = iota
	unansweredInPod
	lostSessionInPod
	neverInPod
	registeredElsewhere
	freshElsewhere
	restartRegistered
	restartUnknown
	restartEmpty
	releasedRestored
	releasedChain
	releasedKeptVolume
	wokenRefused
	wokenIntoPod
	releasedLostSession
	neverRunSuspended
)

func (k memberKind) String() string {
	return [...]string{
		"an agent of it registered in the pod and it answered",
		"an agent of it registered in the pod and it did not answer",
		"an agent of it registered in the pod, then it lost its session and was refused with none",
		"it was refused for the pod with no session and never registered there",
		"it was refused for the pod with no session, then registered in the pod in its place",
		"an agent of it registered in the pod, then it started fresh in the pod in its place",
		"after a restart over a stored claim whose registered agent runs in the pod, read as unknown",
		"after a restart over a stored claim that shows an abandoned session",
		"after a restart over a stored claim that shows no session",
		"an idle release suspended it in an earlier pod, whose bundle the pod restored",
		"an idle release suspended it in an earlier pod, and it stayed suspended through a second release",
		"an idle release suspended it in an earlier pod of the kept volume the pod mounts",
		"an idle release suspended it, and its wake was refused for the pod before its agent registered",
		"an idle release suspended it, and its wake came up in the pod before its agent registered",
		"an idle release suspended it in an earlier pod, and its session was forgotten during the round",
		"it was refused for the pod with no session, never registered, and the daemon suspended it",
	}[k]
}

// suspended is whether the daemon suspended the implementer of kind before the round's end: an
// idle release before the round over P opened, or the daemon's own rule while the round runs
// (suspendedTo brings such a member to its end).
func (k memberKind) suspended() bool {
	return k >= releasedRestored
}

// passedOver is whether the implementer of kind stays suspended until the round's end, so the
// round passes it over (supervise's Claim.PassedOverByRound): the replacement's line names it
// under notWaitedFor, beside the claims the round did not wait for, where it has a session and is
// named, and the claim whose launch waits on P at the sweep's replacement is the tester.
func (k memberKind) passedOver() bool {
	switch k {
	case releasedRestored, releasedChain, releasedKeptVolume, releasedLostSession, neverRunSuspended:
		return true
	}
	return false
}

// The update round over P names a member only where it had not answered and its work may be on
// P's disk, at every end. named is the role and the state each row's message and line name, ""
// for none.
func TestPodUpdateNamesWhatTheRoundsPodTookAtEachEndOfEachMember(t *testing.T) {
	const launching, ready, suspended = "implementer (launching)", "implementer (ready)", "implementer (suspended)"
	for _, row := range []struct {
		end   roundPodEnd
		kind  memberKind
		named string
	}{
		{endBySweep, answeredInPod, ""},
		{endBySweep, unansweredInPod, launching},
		{endBySweep, lostSessionInPod, launching},
		{endBySweep, neverInPod, ""},
		{endThroughAProcess, answeredInPod, ""},
		{endThroughAProcess, unansweredInPod, launching},
		{endThroughAProcess, lostSessionInPod, launching},
		{endThroughAProcess, neverInPod, ""},
		{endPodGone, answeredInPod, ""},
		{endPodGone, unansweredInPod, ready},
		{endPodGone, lostSessionInPod, launching},
		{endPodGone, neverInPod, ""},
		{endRecreated, answeredInPod, ""},
		{endRecreated, unansweredInPod, ready},
		{endRecreated, lostSessionInPod, launching},
		{endRecreated, neverInPod, ""},
		{endRecreated, registeredElsewhere, ""},
		{endRecreated, freshElsewhere, launching},
		{endRecreatedRefusing, answeredInPod, ""},
		{endRecreatedRefusing, unansweredInPod, launching},
		{endRecreatedRefusing, lostSessionInPod, launching},
		{endRecreatedRefusing, neverInPod, ""},
		{endAtALaunch, answeredInPod, ""},
		{endAtALaunch, unansweredInPod, launching},
		{endAtALaunch, lostSessionInPod, launching},
		{endAtALaunch, neverInPod, ""},
		{endAtALaunch, registeredElsewhere, ""},
		{endAtALaunch, freshElsewhere, launching},
		{endBySweep, restartRegistered, launching},
		{endBySweep, restartUnknown, launching},
		{endRecreated, restartUnknown, launching},
		{endRecreated, restartEmpty, ""},
		{endBySweep, releasedRestored, suspended},
		{endThroughAProcess, releasedRestored, suspended},
		{endPodGone, releasedRestored, suspended},
		{endRecreated, releasedRestored, suspended},
		{endRecreatedRefusing, releasedRestored, suspended},
		{endAtALaunch, releasedRestored, suspended},
		{endThroughAProcess, releasedChain, suspended},
		{endPodGone, releasedChain, suspended},
		{endThroughAProcess, releasedKeptVolume, suspended},
		{endPodGone, releasedKeptVolume, suspended},
		{endBySweep, wokenRefused, launching},
		{endThroughAProcess, wokenRefused, launching},
		{endPodGone, wokenRefused, launching},
		{endRecreated, wokenRefused, launching},
		{endRecreatedRefusing, wokenRefused, launching},
		{endAtALaunch, wokenRefused, launching},
		{endPodGone, wokenIntoPod, launching},
		{endRecreated, wokenIntoPod, launching},
		{endRecreatedRefusing, wokenIntoPod, launching},
		{endPodGone, releasedLostSession, suspended},
		{endRecreated, releasedLostSession, suspended},
		{endBySweep, neverRunSuspended, ""},
		{endThroughAProcess, neverRunSuspended, ""},
		{endPodGone, neverRunSuspended, ""},
		{endRecreated, neverRunSuspended, ""},
		{endRecreatedRefusing, neverRunSuspended, ""},
		{endAtALaunch, neverRunSuspended, ""},
	} {
		t.Run(row.end.String()+": "+row.kind.String(), func(t *testing.T) {
			rig := orderedTo(t, row.end, row.kind)
			wantRoundEnd(t, rig, row.end, row.kind, row.named)
		})
	}
}

// orderedTo brings a round over podUpdatePod, with the implementer of kind as its member, to end,
// and answers the rig once the implementer's machine has taken the end of the round. The
// implementer is queued with no session at the rig's boot, so its machine knows every pod an agent
// of it registers in from then on (supervise.Claim.RegisteredPods); where end is
// endThroughAProcess, the tester runs in P beside it, registered, and answers the round. The
// restart rows restart the daemon (podUpdateRig.restart) and go on with the rig it answers. A
// member the daemon suspended before the round's end is brought to its end by suspendedTo.
func orderedTo(t *testing.T, end roundPodEnd, kind memberKind) *podUpdateRig {
	t.Helper()
	if kind.suspended() {
		return suspendedTo(t, end, kind)
	}
	implementer, tester := claim.RoleImplementer, claim.RoleTester
	roles := []podUpdateRole{{role: implementer, state: supervise.StateQueued}}
	if end == endThroughAProcess {
		roles = append(roles, readyWorker(tester))
	}
	rig := newPodUpdateRig(t, roles...)
	rig.rt.ScriptWorkspaceWork(podUpdateIssue, runtime.WorkspaceWork{Head: passedOverHead, Commits: []string{passedOverCommit}, Count: 1})
	rig.drifts(podUpdateExchangeDrift)
	refusals := rig.refuseLaunches()

	switch kind {
	case lostSessionInPod:
		rig.startInPod(implementer, podUpdatePod)
		rig.loseSessionRefused(refusals, implementer, podUpdatePod)
	case neverInPod, registeredElsewhere, restartEmpty:
		refusals.refuse(implementer, podUpdatePod)
		rig.refusedFirstLaunch(implementer)
	case restartUnknown:
		// An agent of it registers in P, an operator suspends it and forgets its session, and the
		// daemon restarts before any round: the stored claim shows the abandoned session and no
		// registered agent (supervise.Claim.Abandoned), so the restarted machine's set is
		// unknown. The operator's resume, with no session, is refused for P.
		rig.startInPod(implementer, podUpdatePod)
		rig.suspendByOperator(implementer)
		rig.handle(implementer, supervise.RequestForgetSession{Claim: rig.tokens[implementer], Session: "ses_" + string(implementer)})
		rig, refusals = rig.restarted()
		refusals.refuse(implementer, podUpdatePod)
		rig.resumedRefused(implementer)
	default:
		rig.startInPod(implementer, podUpdatePod)
	}

	rig.tick()
	rig.awaitRound(implementer, podUpdateRound)
	if end == endThroughAProcess {
		rig.awaitAsked(tester, podUpdateRound)
	}
	if kind == answeredInPod {
		rig.awaitAsked(implementer, podUpdateRound)
		rig.signal(implementer, "c0ffee")
	}

	switch kind {
	case restartRegistered:
		// The daemon restarts while the implementer's registered agent runs in P, so the stored
		// claim shows the session that agent registered, and the restarted machine reads the set
		// as unknown, with no pod in it (supervise's bootRegisteredPods). After the restart its
		// process ends, its relaunch finds no session, and its fresh start is refused for P with
		// none.
		rig, refusals = rig.restarted()
		rig.loseSessionRefused(refusals, implementer, podUpdatePod)
	case restartEmpty:
		// The operator suspends the waiting implementer, which ends its wait, before the daemon
		// restarts: the stored claim shows no session, so the restarted machine's set is known
		// and empty. The operator's resume is refused for P.
		if err := rig.machineOf(implementer).Handle(rig.ctx, supervise.RequestSuspend{Claim: rig.tokens[implementer], By: supervise.SuspendedByOperator}); err != nil &&
			!errors.Is(err, supervise.ErrSuspendHeld) {
			t.Fatalf("suspend the waiting implementer: %v", err)
		}
		rig.retryLaunch()
		rig.awaitClaim(implementer, "the held suspension to suspend it", func(c supervise.Claim) bool {
			return c.State == supervise.StateSuspended && c.AwaitsPod == nil
		})
		rig, refusals = rig.restarted()
		refusals.refuse(implementer, podUpdatePod)
		rig.resumedRefused(implementer)
	}

	waitsWithItsSession := kind == answeredInPod || kind == unansweredInPod
	switch end {
	case endBySweep, endThroughAProcess:
		if waitsWithItsSession {
			rig.goneRefused(refusals, implementer, podUpdatePod, false)
		}
		if end == endThroughAProcess {
			rig.signal(tester, "c0ffee")
		}
		rig.awaitReplacement(1, "by the sweep")
		rig.awaitClaim(implementer, "the round's end", func(c supervise.Claim) bool { return c.UpdateRound == "" })
		rig.settleAll()
		return rig
	case endPodGone:
		rig.podGone()
	case endRecreated:
		switch kind {
		case answeredInPod, unansweredInPod:
			rig.displacedInto(implementer, podUpdateReplacementPod)
		case freshElsewhere:
			rig.startsFreshInto(implementer, podUpdateReplacementPod, true)
		default:
			pod := rig.retriesInto(refusals, implementer, podUpdateReplacementPod)
			if kind == registeredElsewhere || kind == restartEmpty {
				rig.registerInto(implementer, pod)
			}
		}
		rig.rt.ScriptPodDrift(rig.tokens[implementer])
	case endRecreatedRefusing:
		if waitsWithItsSession {
			rig.goneRefused(refusals, implementer, podUpdateReplacementPod, true)
		} else {
			refusals.refuse(implementer, podUpdateReplacementPod)
			rig.retryLaunch()
			rig.awaitClaim(implementer, "its retry refused for the pod in P's place", func(c supervise.Claim) bool {
				return podOf(c.AwaitsPod) == podUpdateReplacementPod
			})
		}
		rig.rt.ScriptPodDrift(rig.tokens[implementer])
	case endAtALaunch:
		if waitsWithItsSession {
			rig.goneRefused(refusals, implementer, podUpdatePod, false)
		}
		var pod string
		if kind == freshElsewhere {
			pod = rig.startsFreshInto(implementer, podUpdatePod, false)
		} else {
			pod = rig.retriesInto(refusals, implementer, podUpdatePod)
		}
		if atLaunch := rig.rt.CallsOf("ReplaceRoundPodAtLaunch"); len(atLaunch) != 1 || atLaunch[0].Spec.Claim != rig.tokens[implementer] {
			t.Fatalf("the round's pod was replaced at a launch %+v, want once, by the implementer's", atLaunch)
		}
		if pod == podUpdatePod {
			t.Fatalf("the implementer's launch came up in the round's pod %s, want the pod the launch built in its place", pod)
		}
		if kind == registeredElsewhere {
			rig.registerInto(implementer, pod)
		}
	}
	rig.awaitSwept(implementer, "its round to end", func(c supervise.Claim) bool { return c.UpdateRound == "" })
	rig.settleAll()
	return rig
}

// wantRoundEnd holds the end of the round over podUpdatePod that orderedTo brought about to the
// issue's message and the end's log line, which name the implementer, as named renders it, or
// nobody where named is "". kind is the implementer's: whether it answered the round is what the
// line of a pod that went says where it names nobody; one the round passes over is named under
// notWaitedFor where a replacement's line names claims the round did not wait for, and at the
// sweep's replacement through a waiting claim that claim is the tester; and on a kept volume the
// line and the message say the work is still on that volume (roundKeptVolume).
func wantRoundEnd(t *testing.T, rig *podUpdateRig, end roundPodEnd, kind memberKind, named string) {
	t.Helper()
	implementer := claim.RoleImplementer
	answered, kept := kind == answeredInPod, kind == releasedKeptVolume
	var namedRoles []claim.Role
	if named != "" {
		namedRoles = []claim.Role{implementer}
	}
	messages := rig.messages()
	replaced := loggedLines(t, rig.logs.String(), podUpdateReplacedLine)
	switch end {
	case endBySweep, endThroughAProcess:
		through := implementer
		if end == endThroughAProcess || kind.passedOver() {
			through = claim.RoleTester
		}
		if calls := rig.replacements(); len(calls) != 1 || calls[0].Pod.Claim != rig.tokens[through] || podOf(calls[0].Pod.Locator) != podUpdatePod {
			t.Fatalf("ReplacePod was asked %+v, want once, through the %s in pod %s", calls, through, podUpdatePod)
		}
		if len(replaced) != 1 || replaced[0]["atTheBound"] != false {
			t.Fatalf("the replacement was logged %+v, want one line that no bound made", replaced)
		}
		line := replaced[0]
		if kind.passedOver() {
			wantField(t, line, "notWaitedFor", namedRoles...)
			wantField(t, line, "waitingOnThePodNotWaitedFor")
		} else {
			wantField(t, line, "notWaitedFor")
			wantField(t, line, "waitingOnThePodNotWaitedFor", namedRoles...)
		}
		if value, logged := line["keptVolume"]; (value == true) != kept || logged != kept {
			t.Errorf("the replacement's line says keptVolume=%v, want it there (true) only where the pod's disk is a kept volume (%t)", value, kept)
		}
		switch {
		case end == endBySweep:
			if line["workspaceRead"] != "not made" || line["workspaceNotReadBecause"] != noProcessRead {
				t.Errorf("the replacement's line names the read %+v, want one not made because %q", line, noProcessRead)
			}
			if named != "" {
				wantToldUnanswered(t, messages, named)
				return
			}
		case named != "":
			wantReadThroughTheTester(t, rig, line, "found work")
			if len(messages) != 1 || !strings.Contains(messages[0], toldNotWaitedForRoles+named+".") || !strings.Contains(messages[0], passedOverCommit) {
				t.Fatalf("the issue was written %q, want one message that names %s under %q, and the commit %s", messages, named, toldNotWaitedForRoles, passedOverCommit)
			}
			wantKeptSaid(t, messages[0], kept, "The pod is gone. Its disk is a kept volume, and the volume is still there.")
			return
		default:
			if reads := rig.rt.CallsOf("WorkspaceWork"); len(reads) != 0 || line["workspaceRead"] != "not made" ||
				line["workspaceNotReadBecause"] != nothingUnaskedRead {
				t.Errorf("the workspace was read %+v and the line names the read %+v, want no read, not made because %q", reads, line, nothingUnaskedRead)
			}
		}
	case endAtALaunch:
		if calls := rig.replacements(); len(calls) != 0 {
			t.Errorf("ReplacePod called %+v, want the launch's replacement alone", calls)
		}
		if len(replaced) != 1 || replaced[0]["atLaunch"] != true {
			t.Fatalf("the replacement was logged %+v, want one line of a replacement at a launch", replaced)
		}
		wantField(t, replaced[0], "unansweredMembers", namedRoles...)
		if named != "" {
			wantToldUnanswered(t, messages, named)
			return
		}
	default:
		if calls := rig.replacements(); len(calls) != 0 || len(replaced) != 0 {
			t.Errorf("ReplacePod called %+v and logged %+v, want no replacement: the round's pod went on its own", calls, replaced)
		}
		over := podWentOverLine
		if end == endPodGone {
			over = podNotThereOverLine
		}
		went := podWentLostLine
		if kept {
			went = podWentKeptOnVolumeLine
		}
		logs := rig.logs.String()
		lost, noWorker, overs := loggedLines(t, logs, went), loggedLines(t, logs, podWentNoWorkerLine), loggedLines(t, logs, over)
		switch {
		case named != "":
			if len(lost) != 1 || len(noWorker)+len(overs) != 0 {
				t.Fatalf("the round's end was logged %+v, %+v and %+v, want one line that a member lost what it had not pushed", lost, noWorker, overs)
			}
			wantField(t, lost[0], "lost", implementer)
			if len(messages) != 1 || !strings.Contains(messages[0], "The pod that ran this issue's roles is gone") ||
				!strings.Contains(messages[0], "before the pod went: "+named+".") {
				t.Fatalf("the issue was written %q, want one message that its pod went and that %s lost what it had not pushed", messages, named)
			}
			wantKeptSaid(t, messages[0], kept, "The pod's disk is a kept volume, and the volume is still there.")
			return
		case answered:
			if len(overs) != 1 || len(lost)+len(noWorker) != 0 {
				t.Fatalf("the round's end was logged %+v, %+v and %+v, want one line that every member answered", overs, lost, noWorker)
			}
		default:
			if len(noWorker) != 1 || len(lost)+len(overs) != 0 {
				t.Fatalf("the round's end was logged %+v, %+v and %+v, want one line that no member that had not answered ran a worker in the pod",
					noWorker, lost, overs)
			}
			wantField(t, noWorker[0], "ranNoWorkerInPod", implementer)
		}
	}
	if len(messages) != 0 {
		t.Errorf("the issue was written %q, want nothing: no member that had not answered ran a worker in the round's pod", messages)
	}
}

// podWentKeptOnVolumeLine is the line of the end of a round whose pod went with a member that had
// not answered, where the pod's disk is a kept volume that is still there (podWentWithoutItsRound,
// roundKeptVolume): what that member had not pushed is still on the volume.
const podWentKeptOnVolumeLine = "supervise: the pod of an open update round is gone; its disk is a kept volume, so what the roles that had not answered did not push is still on it"

// wantKeptSaid holds message, written on the issue at a round's end, to saying says, its sentence
// that the pod's disk is a kept volume that is still there, where kept is true, and to not saying
// it otherwise.
func wantKeptSaid(t *testing.T, message string, kept bool, says string) {
	t.Helper()
	if strings.Contains(message, says) != kept {
		t.Errorf("the message says %q: %t, want %t, whether the pod's disk is a kept volume that is still there:\n%s",
			says, !kept, kept, message)
	}
}

// launchRefusals is, by claim, the pod the runtime refuses each launch of that claim for, for want
// of the pod's update round (awaitingRound), and "" for a claim whose launches go through
// (refuseLaunches). A test writes a claim's pod before the launch it is for, and the launches read
// it as they run.
type launchRefusals struct {
	mu     sync.Mutex
	tokens map[claim.Role]claim.Token
	pods   map[claim.Token]string
	// resumes is each claim one Resume of which goes through to its scripted answer whatever pod
	// it is refused for, as a relaunch that finds its session in no store is answered before any
	// pod is asked (loseSessionRefused).
	resumes map[claim.Token]bool
}

// refuse makes each later launch of role's claim refused for pod, or, for "", lets it through.
func (f *launchRefusals) refuse(role claim.Role, pod string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pods[f.tokens[role]] = pod
}

// refusing is the pod the launch of token that method makes now is refused for, "" for none.
func (f *launchRefusals) refusing(token claim.Token, method string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if method == "Resume" && f.resumes[token] {
		delete(f.resumes, token)
		return ""
	}
	return f.pods[token]
}

// refuseLaunches makes every Spawn and Resume of a claim of the rig the runtime's refusal of a
// launch into the pod launchRefusals names for that claim (awaitingRound), at the launch's own
// generation, and lets it through where it names none. It answers the refusals, which name none.
func (r *podUpdateRig) refuseLaunches() *launchRefusals {
	refusals := &launchRefusals{tokens: r.tokens, pods: map[claim.Token]string{}, resumes: map[claim.Token]bool{}}
	hook := func(method string) func(context.Context) error {
		return func(context.Context) error {
			calls := r.rt.CallsOf(method)
			spec := calls[len(calls)-1].Spec
			if pod := refusals.refusing(spec.Claim, method); pod != "" {
				return awaitingRound(spec.Claim, pod, spec.Generation)
			}
			return nil
		}
	}
	r.rt.OnSpawn, r.rt.OnResume = hook("Spawn"), hook("Resume")
	return refusals
}

// restarted is the daemon restarting (restart), with the sweep of the rig it answers writing its
// lines where this rig's do, and that rig's runtime refusing launches as refuseLaunches makes it,
// over the drift of the pod the round is over.
func (r *podUpdateRig) restarted() (*podUpdateRig, *launchRefusals) {
	r.t.Helper()
	next := r.restart()
	next.s.log = r.log
	next.drifts(podUpdateExchangeDrift)
	return next, next.refuseLaunches()
}

// machineOf is role's claim's machine.
func (r *podUpdateRig) machineOf(role claim.Role) *supervise.Machine {
	r.t.Helper()
	m, ok := r.sup.Machine(r.tokens[role])
	if !ok {
		r.t.Fatalf("the %s is not supervised", role)
	}
	return m
}

// goneRefused is role's process ending, with its pod where displaced is set, and the relaunch that
// end raises refused for pod: the claim keeps its session, records no process, and waits on pod.
func (r *podUpdateRig) goneRefused(refusals *launchRefusals, role claim.Role, pod string, displaced bool) {
	r.t.Helper()
	ended := r.claimOf(role).Locator
	if ended == nil {
		r.t.Fatalf("the %s records no process to end", role)
	}
	refusals.refuse(role, pod)
	err := r.machineOf(role).Handle(r.ctx, supervise.RuntimeObservation{Observation: runtime.Observation{
		Locator: *ended, Kind: runtime.Gone, At: r.clock.Now(), Displaced: displaced,
	}})
	if !errors.Is(err, runtime.ErrAwaitingPodRound) {
		r.t.Fatalf("the %s's relaunch answered %v, want the runtime's wait for its pod's round", role, err)
	}
	if c := r.claimOf(role); c.Session == "" || c.Locator != nil || podOf(c.AwaitsPod) != pod {
		r.t.Fatalf("the refused %s has session %q and process %+v, waiting on %+v; want its session kept, no process, and the wait on pod %s",
			role, c.Session, c.Locator, c.AwaitsPod, pod)
	}
}

// loseSessionRefused is role's process ending while its pod stays, and its relaunch finding the
// session in no store, so the launch forgets the session and spawns a fresh agent in the same try
// (supervise's startFresh), which the runtime refuses for pod: the claim waits on pod with no
// session, its abandoned session recorded (supervise.Claim.Abandoned).
func (r *podUpdateRig) loseSessionRefused(refusals *launchRefusals, role claim.Role, pod string) {
	r.t.Helper()
	token := r.tokens[role]
	ended := r.claimOf(role).Locator
	if ended == nil {
		r.t.Fatalf("the %s records no process to end", role)
	}
	refusals.refuse(role, pod)
	refusals.mu.Lock()
	refusals.resumes[token] = true
	refusals.mu.Unlock()
	r.rt.ScriptResume(fake.SpawnResult{Err: fmt.Errorf("resume %s: %w: the session table holds no such session", token, runtime.ErrSessionFileMissing)})
	err := r.machineOf(role).Handle(r.ctx, supervise.RuntimeObservation{Observation: runtime.Observation{
		Locator: *ended, Kind: runtime.Gone, At: r.clock.Now(),
	}})
	if !errors.Is(err, runtime.ErrAwaitingPodRound) {
		r.t.Fatalf("the %s's relaunch answered %v, want its fresh spawn refused for the pod's round", role, err)
	}
	if c := r.claimOf(role); c.Session != "" || c.Abandoned.Session == "" || c.Locator != nil || podOf(c.AwaitsPod) != pod {
		r.t.Fatalf("the %s has session %q, abandoned %q, process %+v, waiting on %+v; want its session forgotten, no process and the wait on pod %s",
			role, c.Session, c.Abandoned.Session, c.Locator, c.AwaitsPod, pod)
	}
}

// startsFreshInto is role's process ending, with its pod where displaced is set, and its relaunch
// finding the session in no store, so the launch forgets the session and spawns a fresh agent
// into pod, which comes up there, or in the pod a launch into pod builds in its place (fake's
// launchInto). No agent of it registers there. It answers the pod the fresh agent came up in.
func (r *podUpdateRig) startsFreshInto(role claim.Role, pod string, displaced bool) string {
	r.t.Helper()
	token := r.tokens[role]
	ended := r.claimOf(role).Locator
	if ended == nil {
		r.t.Fatalf("the %s records no process to end", role)
	}
	conn := fake.NewConn()
	r.conns.Register(token, conn)
	r.workers[role] = conn
	r.rt.ScriptResume(fake.SpawnResult{Err: fmt.Errorf("resume %s: %w: the session table holds no such session", token, runtime.ErrSessionFileMissing)})
	r.rt.ScriptSpawn(fake.SpawnResult{Locator: *podUpdateLocator(token, podUpdateIssue, pod)})
	r.handle(role, supervise.RuntimeObservation{Observation: runtime.Observation{
		Locator: *ended, Kind: runtime.Gone, At: r.clock.Now(), Displaced: displaced,
	}})
	c := r.claimOf(role)
	if c.Session != "" || c.Locator == nil || podOf(c.Locator) == podUpdatePod {
		r.t.Fatalf("the %s started fresh has session %q and process %+v, want no session and a process in a pod other than %s",
			role, c.Session, c.Locator, podUpdatePod)
	}
	return podOf(c.Locator)
}

// retriesInto lets role's next retry of its refused launch through to a launch into pod, a Resume
// where the claim has a session and a Spawn where it has none, which comes up there, or in the pod
// a launch into pod builds in its place (fake's launchInto). It answers the pod the launch came up
// in.
func (r *podUpdateRig) retriesInto(refusals *launchRefusals, role claim.Role, pod string) string {
	r.t.Helper()
	token := r.tokens[role]
	conn := fake.NewConn()
	r.conns.Register(token, conn)
	r.workers[role] = conn
	launch := fake.SpawnResult{Locator: *podUpdateLocator(token, podUpdateIssue, pod)}
	if r.claimOf(role).Session != "" {
		r.rt.ScriptResume(launch)
	} else {
		r.rt.ScriptSpawn(launch)
	}
	refusals.refuse(role, "")
	r.retryLaunch()
	c := r.awaitClaim(role, "its retry to record a process", func(c supervise.Claim) bool { return c.Locator != nil })
	return podOf(c.Locator)
}

// resumedRefused is an operator's resume of role's suspended claim, whose launch the runtime
// refuses for the pod refuseLaunches names for it: the claim waits on that pod with no process.
func (r *podUpdateRig) resumedRefused(role claim.Role) {
	r.t.Helper()
	if err := r.machineOf(role).Handle(r.ctx, supervise.RequestResume{Claim: r.tokens[role]}); !errors.Is(err, runtime.ErrAwaitingPodRound) {
		r.t.Fatalf("the operator's resume of the %s answered %v, want its launch refused for the pod's round", role, err)
	}
	if c := r.claimOf(role); c.Locator != nil || c.AwaitsPod == nil {
		r.t.Fatalf("the resumed %s records process %+v, waiting on %+v; want no process and a wait on a pod", role, c.Locator, c.AwaitsPod)
	}
}

// The pods of the members an idle release suspended (suspendedTo): releasedPod is P0, the issue's
// pod before P, in which an agent of the implementer and of the tester registered and whose idle
// release saved their workspace's unpushed work; chainedPod is the pod between P0 and P, where a
// second release came before the round (releasedChain).
const (
	releasedPod = "2b6d9e14-7c30-4a85-b1f6-0e9a3c5d7f28"
	chainedPod  = "6e1f3a8c-9d24-4b07-a5c3-8f2e0b4d6a19"
)

// The clock of the releases before the round over P opens at podUpdateStart, each before the
// next: the Sandbox whose kept volume P0 and P mount is made (releasedKeptVolume), P0's release
// saves its work, the pod between P0 and P is made and its release saves the work again
// (releasedChain), and P is made.
var (
	keptVolumeMadeAt = podUpdateStart.Add(-2 * time.Hour)
	releasedSavedAt  = podUpdateStart.Add(-40 * time.Minute)
	chainedMadeAt    = podUpdateStart.Add(-35 * time.Minute)
	chainedSavedAt   = podUpdateStart.Add(-20 * time.Minute)
	roundPodMadeAt   = podUpdateStart.Add(-10 * time.Minute)
)

// suspendedTo is orderedTo for a member the daemon suspended before the round's end
// (memberKind.suspended). The implementer and the tester are queued with no session at the rig's
// boot, so each machine knows every pod an agent of it registers in from then on.
//
// Where kind is neverRunSuspended, the tester registers in P, and the implementer's first launch,
// with no session, is refused for P, so it waits on P and no agent of it registers anywhere; once
// the round over P has opened, the daemon suspends the implementer by its own rule. Every other
// kind is released first. Both start in P0, where an agent of each registers, and P0's idle
// release saves the work of the workspace they share and suspends both (releases). Where kind is
// releasedChain, the tester's wake brings back the pod between P0 and P, which restores that work,
// and that pod's release saves the work again and suspends the tester; the implementer stays
// released through both. Then the tester's wake brings the issue's pod back as P, which restores
// the last release's work, or drops it where P mounts the kept volume that holds it
// (releasedKeptVolume), and the tester registers in P (wakesInto). The round over P opens and asks
// the tester. Then the implementer's session is forgotten (releasedLostSession), or a woken
// implementer is woken, before the tester answers: its launch is refused for P (wokenRefused) or
// comes up in P (wokenIntoPod), and no agent of it registers. The tester answers, and the round
// goes to end as the tester, the one claim whose agent ran in P, and a woken implementer bring it
// about.
func suspendedTo(t *testing.T, end roundPodEnd, kind memberKind) *podUpdateRig {
	t.Helper()
	implementer, tester := claim.RoleImplementer, claim.RoleTester
	rig := newPodUpdateRig(t, podUpdateRole{role: implementer, state: supervise.StateQueued}, podUpdateRole{role: tester, state: supervise.StateQueued})
	rig.rt.ScriptWorkspaceWork(podUpdateIssue, runtime.WorkspaceWork{Head: passedOverHead, Commits: []string{passedOverCommit}, Count: 1})
	refusals := rig.refuseLaunches()

	if kind == neverRunSuspended {
		rig.startInPod(tester, podUpdatePod)
		refusals.refuse(implementer, podUpdatePod)
		rig.refusedFirstLaunch(implementer)
	} else {
		rig.startInPod(implementer, releasedPod)
		rig.startInPod(tester, releasedPod)
		rig.releases(releasedPod, 1, releasedSavedAt, implementer, tester)
		var keptSince *time.Time
		switch kind {
		case releasedChain:
			rig.wakesInto(tester, chainedPod, chainedMadeAt, nil)
			rig.releases(chainedPod, 2, chainedSavedAt, tester)
		case releasedKeptVolume:
			rig.rt.ScriptKeptVolume(podUpdateIssue, keptVolumeMadeAt)
			keptSince = &keptVolumeMadeAt
		}
		rig.wakesInto(tester, podUpdatePod, roundPodMadeAt, keptSince)
	}
	rig.drifts(podUpdateExchangeDrift)

	rig.tick()
	rig.awaitRound(implementer, podUpdateRound)
	rig.awaitAsked(tester, podUpdateRound)
	if c := rig.claimOf(implementer); kind != neverRunSuspended && (!c.Released() || c.UpdateTaskedAt != nil) {
		t.Fatalf("the implementer is %s by %q, asked at %v; want it released and never asked: the round passes it over", c.State, c.SuspendedBy, c.UpdateTaskedAt)
	}
	switch kind {
	case neverRunSuspended:
		// The daemon suspends the waiting implementer by its own rule; the suspension waits for the
		// launch's turn, which the retry gives it.
		if err := rig.machineOf(implementer).Handle(rig.ctx, supervise.RequestSuspend{Claim: rig.tokens[implementer], By: supervise.SuspendedByDaemon}); err != nil &&
			!errors.Is(err, supervise.ErrSuspendHeld) {
			t.Fatalf("suspend the waiting implementer: %v", err)
		}
		rig.retryLaunch()
		c := rig.awaitClaim(implementer, "the held suspension to suspend it", func(c supervise.Claim) bool {
			return c.State == supervise.StateSuspended && c.AwaitsPod == nil
		})
		if !c.PassedOverByRound() || c.Session != "" || len(c.RegisteredPods.Pods) != 0 || c.RegisteredPods.Unknown {
			t.Fatalf("the suspended implementer is %s by %q with session %q and set %+v; want it passed over, with no session and a known, empty set",
				c.State, c.SuspendedBy, c.Session, c.RegisteredPods)
		}
	case releasedLostSession:
		rig.handle(implementer, supervise.RequestForgetSession{Claim: rig.tokens[implementer], Session: "ses_" + string(implementer)})
		if c := rig.claimOf(implementer); !c.Released() || c.Session != "" || c.UpdateRound != podUpdateRound || len(c.RegisteredPods.Pods) != 1 || c.RegisteredPods.Unknown {
			t.Fatalf("the implementer is %s by %q with session %q in round %q and set %+v; want it released in the round, with no session and the set it registered P0 in",
				c.State, c.SuspendedBy, c.Session, c.UpdateRound, c.RegisteredPods)
		}
	case wokenRefused:
		refusals.refuse(implementer, podUpdatePod)
		if err := rig.machineOf(implementer).Handle(rig.ctx, rig.wakeFor(implementer)); !errors.Is(err, runtime.ErrAwaitingPodRound) {
			t.Fatalf("the implementer's wake answered %v, want its launch refused for the pod's round", err)
		}
		if c := rig.claimOf(implementer); c.Session == "" || c.Locator != nil || podOf(c.AwaitsPod) != podUpdatePod {
			t.Fatalf("the woken implementer has session %q and process %+v, waiting on %+v; want its session kept, no process, and the wait on pod %s",
				c.Session, c.Locator, c.AwaitsPod, podUpdatePod)
		}
	case wokenIntoPod:
		rig.intoPod(implementer, podUpdatePod)
		rig.handle(implementer, rig.wakeFor(implementer))
		if c := rig.claimOf(implementer); c.State != supervise.StateLaunching || podOf(c.Locator) != podUpdatePod {
			t.Fatalf("the woken implementer is %s at %+v, want it launching in pod %s, its agent not registered", c.State, c.Locator, podUpdatePod)
		}
	}
	rig.signal(tester, "c0ffee")

	switch end {
	case endThroughAProcess:
		rig.awaitReplacement(1, "by the sweep")
		rig.awaitClaim(implementer, "the round's end", func(c supervise.Claim) bool { return c.UpdateRound == "" })
		rig.settleAll()
		return rig
	case endBySweep, endAtALaunch:
		// No role process is left in P. Where the implementer stays released, the tester's
		// process ends and its relaunch is refused for P, so the tester waits on P; where the
		// woken implementer waits on P, the workflow suspends the tester, which answered, so the
		// implementer's wait is the one way to P.
		through := implementer
		if kind.passedOver() {
			through = tester
			rig.goneRefused(refusals, tester, podUpdatePod, false)
		} else {
			rig.suspendBy(tester, supervise.SuspendedByDaemon)
		}
		if end == endBySweep {
			rig.awaitReplacement(1, "by the sweep")
			rig.awaitClaim(implementer, "the round's end", func(c supervise.Claim) bool { return c.UpdateRound == "" })
			rig.settleAll()
			return rig
		}
		if pod := rig.retriesInto(refusals, through, podUpdatePod); pod == podUpdatePod {
			t.Fatalf("the %s's launch came up in the round's pod %s, want the pod the launch built in its place", through, pod)
		}
		if atLaunch := rig.rt.CallsOf("ReplaceRoundPodAtLaunch"); len(atLaunch) != 1 || atLaunch[0].Spec.Claim != rig.tokens[through] {
			t.Fatalf("the round's pod was replaced at a launch %+v, want once, by the %s's", atLaunch, through)
		}
	case endPodGone:
		rig.podGone()
	case endRecreated:
		rig.displacedInto(tester, podUpdateReplacementPod)
		switch kind {
		case wokenRefused:
			if pod := rig.retriesInto(refusals, implementer, podUpdateReplacementPod); pod != podUpdateReplacementPod {
				t.Fatalf("the implementer's retry came up in pod %s, want the pod in P's place, %s", pod, podUpdateReplacementPod)
			}
		case wokenIntoPod:
			rig.relaunchedUnregistered(implementer, podUpdateReplacementPod)
		}
		rig.rt.ScriptPodDrift(rig.tokens[implementer])
		rig.rt.ScriptPodDrift(rig.tokens[tester])
	case endRecreatedRefusing:
		rig.goneRefused(refusals, tester, podUpdateReplacementPod, true)
		switch kind {
		case wokenRefused:
			refusals.refuse(implementer, podUpdateReplacementPod)
			rig.retryLaunch()
			rig.awaitClaim(implementer, "its retry refused for the pod in P's place", func(c supervise.Claim) bool {
				return podOf(c.AwaitsPod) == podUpdateReplacementPod
			})
		case wokenIntoPod:
			rig.goneRefused(refusals, implementer, podUpdateReplacementPod, true)
		}
		rig.rt.ScriptPodDrift(rig.tokens[implementer])
		rig.rt.ScriptPodDrift(rig.tokens[tester])
	}
	rig.awaitSwept(implementer, "its round to end", func(c supervise.Claim) bool { return c.UpdateRound == "" })
	rig.settleAll()
	return rig
}

// releases is the idle release of pod, the issue's pod, in which each of roles runs, as the
// outbox's release rows make it: the save bundles the unpushed work of the workspace every role
// of the issue shares off pod, under release, while the roles' processes still run
// (bundleIssueWorkspace, store.PutWorkspaceBundle), and each role stop then suspends its claim
// released (supervise.SuspendedByReleased), which lets its process go. The teardown that takes pod
// is the runtime's, and no claim records pod after the stops.
func (r *podUpdateRig) releases(pod string, release int64, savedAt time.Time, roles ...claim.Role) {
	r.t.Helper()
	if err := r.st.PutWorkspaceBundle(r.ctx, podUpdateIssue, release, runtime.WorkspaceBundle{
		Bundle: []byte("PACK the work of " + podUpdateIssue + " that no branch on origin holds"), Head: passedOverHead,
		Tips: []string{passedOverCommit}, At: savedAt, PodUID: pod,
	}); err != nil {
		r.t.Fatalf("save the workspace bundle of the release of pod %s: %v", pod, err)
	}
	for _, role := range roles {
		r.suspendBy(role, supervise.SuspendedByReleased)
	}
}

// wakesInto is a wake of role's released claim (wakeFor) that brings the issue's pod back as pod,
// made at madeAt, where role starts and its agent registers. Every start into a pod asks for the
// last release's saved work first (sandbox's launchers.start): a pod made after the save takes the
// bundle and restores it before the role starts, and its launcher reports the restore done
// (store.TakeWorkspaceBundleForRestore, DeleteRestoredWorkspaceBundle); a pod whose Sandbox owns
// the issue's kept volume, made at keptSince before the save, mounts the volume the work was taken
// off, so the saved copy is dropped (store.DropHeldWorkspaceBundle). keptSince is nil for a pod
// with no kept volume.
func (r *podUpdateRig) wakesInto(role claim.Role, pod string, madeAt time.Time, keptSince *time.Time) {
	r.t.Helper()
	if keptSince != nil {
		if _, dropped, err := r.st.DropHeldWorkspaceBundle(r.ctx, podUpdateProject, podUpdateIssue, pod, *keptSince); err != nil || !dropped {
			r.t.Fatalf("pod %s dropped the saved bundle: %t (%v), want it dropped: its kept volume holds the work", pod, dropped, err)
		}
	} else {
		saved, take, err := r.st.TakeWorkspaceBundleForRestore(r.ctx, podUpdateProject, podUpdateIssue, pod, madeAt)
		if err != nil || take != runtime.WorkspaceBundleSend || saved.Head != passedOverHead {
			r.t.Fatalf("pod %s took %v of the saved bundle at head %q (%v), want the bundle sent to it", pod, take, saved.Head, err)
		}
		if err := r.st.DeleteRestoredWorkspaceBundle(r.ctx, podUpdateIssue, saved.Release); err != nil {
			r.t.Fatalf("report the restore of release %d into pod %s: %v", saved.Release, pod, err)
		}
	}
	r.intoPod(role, pod)
	r.handle(role, r.wakeFor(role))
	r.registerInto(role, pod)
}

// wakeFor is the resume a wake makes of role's released claim, as the outbox's wake for a
// delivery the daemon holds for that role makes it (wakeForDelivery).
func (r *podUpdateRig) wakeFor(role claim.Role) supervise.RequestRuleResume {
	return supervise.RequestRuleResume{Claim: r.tokens[role], Reason: "a wake for a delivery the daemon holds for the " + string(role)}
}

// relaunchedUnregistered is role's process ending with its pod and the daemon starting the role
// again on its own session in pod (displacedInto), where no agent of it registers by the end.
func (r *podUpdateRig) relaunchedUnregistered(role claim.Role, pod string) {
	r.t.Helper()
	ended := r.claimOf(role).Locator
	if ended == nil {
		r.t.Fatalf("the %s records no process to end with its pod", role)
	}
	r.intoPod(role, pod)
	r.handle(role, supervise.RuntimeObservation{Observation: runtime.Observation{
		Locator: *ended, Kind: runtime.Gone, At: r.clock.Now(), Displaced: true,
	}})
	r.awaitClaim(role, "its relaunch into the pod in P's place", func(c supervise.Claim) bool { return podOf(c.Locator) == pod })
}
