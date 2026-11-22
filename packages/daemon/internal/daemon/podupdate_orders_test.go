package daemon

// The ends of an update round over a pod, P, against every kind of member the round can have
// (dispatch://INFRA-4425). Every end names a member by one fact, read by one predicate
// (registeredInRoundPod): an agent of the member registered a session in P, and the member had not
// answered the round. Each row of the table brings the implementer, as one kind of member, to one
// end of the round over P, and holds the issue's message and the end's log line to naming the
// implementer, or to naming nobody.
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
// state holds a registered agent in P, so P is known (named); one that shows an abandoned session
// and no registered agent, so the set is unknown and the end names it as main names every member
// that had not answered, both where it waits on P with no session at the end and where it records
// a process in the pod in P's place by then; and one that shows no session, so the set is known
// and empty (named nothing, where main would name it).
//
// The pairs the table has no row for cannot occur, each for the reason given:
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
//
// The negative controls run in place with podupdate.go restored byte for byte after each (the
// pull request records the runs): registeredInRoundPod answering true fails every row of a member
// that had not answered and names nothing; answering false fails every row that names; and
// notAskedToPush's loop over sessionClaims(group) in place of group fails the row where the sweep
// replaces P through the tester's process past a member that lost its session.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/runtime/fake"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// roundPodEnd is how the round over podUpdatePod ends in the order table.
type roundPodEnd int

const (
	// endBySweep is the sweep's own replacement of P through the implementer, whose launch waits on
	// it (replacePodForRound): no role process runs in P, so no read of the workspace is made.
	endBySweep roundPodEnd = iota
	// endThroughAProcess is the sweep's own replacement of P through the tester's process, once the
	// tester answered, after one read of the workspace through the tester where a member's work is
	// on P's disk, which the runtime is scripted to find work in (readRoundWorkspace).
	endThroughAProcess
	// endPodGone is P going on its own, which the runtime answers gone while the claims still record
	// it, as a process or as the pod a launch waits on (endRoundOverAGonePod).
	endPodGone
	// endRecreated is P recreated in podUpdateReplacementPod with the exchange identity: the
	// implementer's next launch comes up there, and the sweep reads the claims recording it
	// (reconcileIssuePodUpdate, endRoundWhosePodWent).
	endRecreated
	// endRecreatedRefusing is P recreated in podUpdateReplacementPod without the exchange identity:
	// the runtime refuses the implementer's next launch for that pod, and the sweep reads the claim
	// waiting on it (endRoundWhosePodWent).
	endRecreatedRefusing
	// endAtALaunch is a launch's replacement of P, the implementer's own (roundPodEndsAtLaunch),
	// which the sweep ends the round as (endRoundALaunchReplaced).
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
	restartKnown
	restartUnknown
	restartEmpty
)

func (k memberKind) String() string {
	return [...]string{
		"an agent of it registered in the pod and it answered",
		"an agent of it registered in the pod and it did not answer",
		"an agent of it registered in the pod, then it lost its session and was refused with none",
		"it was refused for the pod with no session and never registered there",
		"it was refused for the pod with no session, then registered in the pod in its place",
		"an agent of it registered in the pod, then it started fresh in the pod in its place",
		"after a restart over a stored claim whose registered agent runs in the pod",
		"after a restart over a stored claim that shows an abandoned session",
		"after a restart over a stored claim that shows no session",
	}[k]
}

// The update round over P names a member only where an agent of it registered a session in P and
// it had not answered, at every end. named is the role and the state each row's message and line
// name, "" for none.
func TestPodUpdateNamesWhatTheRoundsPodTookAtEachEndOfEachMember(t *testing.T) {
	const launching, ready = "implementer (launching)", "implementer (ready)"
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
		{endBySweep, restartKnown, launching},
		{endBySweep, restartUnknown, launching},
		{endRecreated, restartUnknown, launching},
		{endRecreated, restartEmpty, ""},
	} {
		t.Run(row.end.String()+": "+row.kind.String(), func(t *testing.T) {
			rig := orderedTo(t, row.end, row.kind)
			wantRoundEnd(t, rig, row.end, row.kind == answeredInPod, row.named)
		})
	}
}

// orderedTo brings a round over podUpdatePod, with the implementer of kind as its member, to end,
// and answers the rig once the implementer's machine has taken the end of the round. The
// implementer is queued with no session at the rig's boot, so its machine knows every pod an agent
// of it registers in from then on (supervise.Claim.RegisteredPods); where end is
// endThroughAProcess, the tester runs in P beside it, registered, and answers the round. The
// restart rows restart the daemon (podUpdateRig.restart) and go on with the rig it answers.
func orderedTo(t *testing.T, end roundPodEnd, kind memberKind) *podUpdateRig {
	t.Helper()
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
	case restartKnown:
		// The daemon restarts while the implementer's registered agent runs in P, so the stored
		// claim names P (supervise's bootRegisteredPods). After the restart its process ends, its
		// relaunch finds no session, and its fresh start is refused for P with none.
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
// nobody where named is "". answered is whether the implementer answered the round, which is what
// the line of a pod that went says where it names nobody.
func wantRoundEnd(t *testing.T, rig *podUpdateRig, end roundPodEnd, answered bool, named string) {
	t.Helper()
	implementer := claim.RoleImplementer
	var namedRoles []claim.Role
	if named != "" {
		namedRoles = []claim.Role{implementer}
	}
	messages := rig.messages()
	replaced := loggedLines(t, rig.logs.String(), podUpdateReplacedLine)
	switch end {
	case endBySweep, endThroughAProcess:
		through := implementer
		if end == endThroughAProcess {
			through = claim.RoleTester
		}
		if calls := rig.replacements(); len(calls) != 1 || calls[0].Pod.Claim != rig.tokens[through] || podOf(calls[0].Pod.Locator) != podUpdatePod {
			t.Fatalf("ReplacePod was asked %+v, want once, through the %s in pod %s", calls, through, podUpdatePod)
		}
		if len(replaced) != 1 || replaced[0]["atTheBound"] != false {
			t.Fatalf("the replacement was logged %+v, want one line that no bound made", replaced)
		}
		line := replaced[0]
		wantField(t, line, "notWaitedFor")
		wantField(t, line, "waitingOnThePodNotWaitedFor", namedRoles...)
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
			if len(messages) != 1 || !strings.Contains(messages[0], toldNotWaitedForRoles+named+".") {
				t.Fatalf("the issue was written %q, want one message that names %s under %q", messages, named, toldNotWaitedForRoles)
			}
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
		logs := rig.logs.String()
		lost, noWorker, overs := loggedLines(t, logs, podWentLostLine), loggedLines(t, logs, podWentNoWorkerLine), loggedLines(t, logs, over)
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
			wantField(t, noWorker[0], "unansweredMembers", implementer)
		}
	}
	if len(messages) != 0 {
		t.Errorf("the issue was written %q, want nothing: no member that had not answered ran a worker in the round's pod", messages)
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
