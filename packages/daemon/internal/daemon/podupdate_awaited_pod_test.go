package daemon

// The update round over a pod a claim's launch waits on (dispatch://INFRA-4425 R4). Under
// dispatch_token_broker with no dispatch_token_transition_file, a launch that would keep a live
// pod built before the exchange identity is refused (runtime.AwaitingPodRound), and the claim
// records no process while it waits: its machine keeps the pod the refusal named instead
// (supervise.Claim.AwaitsPod). For each claim it groups that records no process, the sweep reads
// that pod off the claim's machine (reconcilePodUpdates), so the round opens over it, waits on no
// answer of the claim, and replaces the pod through it where no claim records a process there;
// the claim's next try, within a minute, comes up in the pod that replaced it. The end-to-end
// cases drive the pod-update rig of podupdate_test.go; the rows hold each reader alone.
//
// That the launch after the round carries no Dispatch token is the Sandbox runtime's to show,
// since no runtime fake builds a pod: internal/runtime/sandbox's
// TestTheRoundReplacesThePodItsRefusalNamesAndTheNextLaunchTradesTheIdentity replaces the pod
// through the refusal's own locator and reads the start of the launch after it.

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/runtime/fake"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// podUpdateExchangeDrift is what the Sandbox runtime's PodDrift reports of a pod built before the
// exchange identity once the deployment names an exchange audience (movedExchangeIdentity): the
// drift a round over such a pod is opened for.
const podUpdateExchangeDrift = "agent-secrets-exchange (unset), now audience=legion-broker-exchange expiry=3600s path=token"

// podRoundRetry is how long a start refused for want of its pod's round waits before it is tried
// again (supervise's podRoundRetry): the wait a test fires to run that retry.
const podRoundRetry = time.Minute

// awaitedLocator is the pod the Sandbox runtime's refusal of token's launch names: the locator a
// process of token's claim in pod would have had, at the refused launch's generation.
func awaitedLocator(token claim.Token, pod string, generation uint64) runtime.Locator {
	loc := *podUpdateLocator(token, podUpdateIssue, pod)
	sandbox := *loc.Sandbox
	sandbox.Generation = generation
	loc.Sandbox, loc.Incarnation = &sandbox, runtime.SandboxIncarnation(pod, generation)
	return loc
}

// refuseUntilReplaced makes every Resume of role's claim the Sandbox runtime's refusal of a launch
// into podUpdatePod (runtime.AwaitingPodRound), at that launch's own generation, until a pod has
// been replaced: a runtime refuses such a launch at every try while the kept pod stands. Once a
// ReplacePod has run, a Resume goes through, and answers what ScriptResume queued.
func (r *podUpdateRig) refuseUntilReplaced(role claim.Role) {
	r.t.Helper()
	token := r.tokens[role]
	r.rt.OnResume = func(context.Context) error {
		if len(r.rt.CallsOf("ReplacePod")) > 0 {
			return nil
		}
		resumes := r.rt.CallsOf("Resume")
		spec := resumes[len(resumes)-1].Spec
		if spec.Claim != token {
			return nil
		}
		return fmt.Errorf("launch %s: %w", token, &runtime.AwaitingPodRound{
			Pod:    awaitedLocator(token, podUpdatePod, spec.Generation),
			Reason: "pod legion-legion-484 carries no exchange identity and dispatch_token_transition_file is not set; its update round replaces it",
		})
	}
}

// diesRefused is role's process dying, and the relaunch that death raises refused by the runtime
// for want of the pod's round: the claim is left launching with no process, its machine keeping
// the pod the refusal named.
func (r *podUpdateRig) diesRefused(role claim.Role) {
	r.t.Helper()
	m, ok := r.sup.Machine(r.tokens[role])
	if !ok {
		r.t.Fatalf("%s is not supervised", role)
	}
	ended := r.claimOf(role).Locator
	if ended == nil {
		r.t.Fatalf("the %s records no process to die", role)
	}
	err := m.Handle(r.ctx, supervise.RuntimeObservation{Observation: runtime.Observation{Locator: *ended, Kind: runtime.Gone, At: r.clock.Now()}})
	if !errors.Is(err, runtime.ErrAwaitingPodRound) {
		r.t.Fatalf("the %s's relaunch answered %v, want the runtime's wait for its pod's round", role, err)
	}
	c := r.claimOf(role)
	if c.State != supervise.StateLaunching || c.Locator != nil || podOf(c.AwaitsPod) != podUpdatePod {
		r.t.Fatalf("the refused %s is %s at %+v waiting on %+v, want launching with no process and waiting on pod %s",
			role, c.State, c.Locator, c.AwaitsPod, podUpdatePod)
	}
}

// retryLaunch runs the minute's retry of every start the runtime refused for want of its pod's
// round, as the machines' timers do, and waits for each decision it starts.
func (r *podUpdateRig) retryLaunch() {
	r.t.Helper()
	if fired := r.clock.fire(podRoundRetry); fired == 0 {
		r.t.Fatal("no retry of a refused launch was armed")
	}
}

// The defect R4 names: an issue whose only claim's launch is refused for want of its pod's round,
// with no process recorded anywhere in that pod. The claim's retries are refused every minute,
// and it keeps waiting on the same pod through them. The sweep opens the round over that pod,
// asks the claim nothing and waits on no answer of it, and replaces the pod through the locator
// the refusal named. The claim's next try comes up in the pod that replaced it, which has drifted
// from nothing, and the controller is told nothing: the round ended before its bound.
//
// The round passed over no claim, but no role process ran in the pod, so the replacement is the
// replacement of a pod no role process ran in (processlessRead), as the claim's own retry would
// have made it had it reached the pod first (endRoundALaunchReplaced). Its line names the read as
// not made for that reason, and one message on the issue names the waiting implementer as a
// member that did not answer the round: its worker died in the pod, so what it did not push went
// with the pod (lostClaims).
//
// The negative control is #4392 at f7c2ed015f: the line said the read was not made because "The
// round passed over no claim that had not answered.", and the issue was written nothing.
func TestPodUpdateReplacesThePodItsOnlyClaimsLaunchWaitsOn(t *testing.T) {
	rig := newPodUpdateRig(t, readyWorker(claim.RoleImplementer))
	token := rig.tokens[claim.RoleImplementer]
	rig.drifts(podUpdateExchangeDrift)
	rig.refuseUntilReplaced(claim.RoleImplementer)
	rig.diesRefused(claim.RoleImplementer)
	rig.retryLaunch()
	if c := rig.claimOf(claim.RoleImplementer); c.Locator != nil || podOf(c.AwaitsPod) != podUpdatePod {
		t.Fatalf("after a refused retry the claim is at %+v waiting on %+v, want no process and the same pod", c.Locator, c.AwaitsPod)
	}
	// The pod the round replaces is the one the last refusal named, at that refused try's own
	// generation (refuseUntilReplaced).
	resumes := rig.rt.CallsOf("Resume")
	refused := resumes[len(resumes)-1].Spec.Generation

	rig.tick()
	opened := rig.awaitRound(claim.RoleImplementer, podUpdateRound)
	if opened.UpdateReasons != podUpdateExchangeDrift {
		t.Errorf("the round was opened for %q, want the drift the runtime read through the awaited pod, %q", opened.UpdateReasons, podUpdateExchangeDrift)
	}

	replaced := rig.awaitReplacement(1, "through the claim whose launch waits on it")
	if got := replaced[0].Pod; got.Claim != token || got.Locator == nil ||
		!reflect.DeepEqual(*got.Locator, awaitedLocator(token, podUpdatePod, refused)) {
		t.Fatalf("ReplacePod was asked %+v, want the refused claim's own locator in pod %s", got, podUpdatePod)
	}
	for _, ask := range rig.asks() {
		t.Errorf("the round asked %s (%+v), want nothing asked of a claim that records no process", ask.Role, ask)
	}
	rig.awaitClaim(claim.RoleImplementer, "the round's end", func(c supervise.Claim) bool { return c.UpdateRound == "" })

	rig.rt.ScriptResume(fake.SpawnResult{Locator: *podUpdateLocator(token, podUpdateIssue, podUpdateReplacementPod)})
	rig.workers[claim.RoleImplementer] = fake.NewConn()
	rig.conns.Register(token, rig.workers[claim.RoleImplementer])
	rig.retryLaunch()
	if c := rig.claimOf(claim.RoleImplementer); podOf(c.Locator) != podUpdateReplacementPod || c.AwaitsPod != nil {
		t.Fatalf("the try after the round is at %+v waiting on %+v, want it in the pod %s and waiting on none",
			c.Locator, c.AwaitsPod, podUpdateReplacementPod)
	}
	rig.registerInto(claim.RoleImplementer, podUpdateReplacementPod)
	rig.tick()
	if c := rig.claimOf(claim.RoleImplementer); c.UpdateRound != "" {
		t.Errorf("a sweep over the pod that replaced the round's own opened round %q, want none: that pod has drifted from nothing", c.UpdateRound)
	}
	lines := loggedLines(t, rig.logs.String(), podUpdateReplacedLine)
	if len(lines) != 1 {
		t.Fatalf("the replacement was logged %+v, want one line", lines)
	}
	if line := lines[0]; line["workspaceRead"] != "not made" || line["workspaceNotReadBecause"] != noProcessRead {
		t.Errorf("the replacement's line names the read %+v, want one not made because %q", line, noProcessRead)
	}
	wantToldUnanswered(t, rig.messages(), "implementer (launching)")
	if notices := rig.notices(); len(notices) > 0 {
		t.Errorf("the controller was told %+v, want nothing: the round ended before its bound", notices)
	}
}

// A second claim of the issue records a process in the pod while the first claim's launch waits
// on it. The round goes through that process as every round does: the second claim is asked and
// the pod waits on its answer, and the pod is replaced through its process, not through the
// waiting claim's locator. The waiting claim is recorded in the round and asked nothing.
func TestPodUpdateGoesThroughAProcessASecondClaimStartsWhileTheFirstWaits(t *testing.T) {
	rig := newPodUpdateRig(t,
		readyWorker(claim.RoleImplementer),
		podUpdateRole{role: claim.RoleTester, state: supervise.StateQueued},
	)
	tester := rig.tokens[claim.RoleTester]
	rig.drifts(podUpdateExchangeDrift)
	rig.refuseUntilReplaced(claim.RoleImplementer)
	rig.diesRefused(claim.RoleImplementer)
	rig.startInPod(claim.RoleTester, podUpdatePod)

	rig.awaitAsked(claim.RoleTester, podUpdateRound)
	waiting := rig.awaitClaim(claim.RoleImplementer, "the round recorded on the claim that waits", func(c supervise.Claim) bool {
		return c.UpdateRound == podUpdateRound
	})
	if waiting.UpdateTaskedAt != nil {
		t.Errorf("the waiting claim records an ask of the round at %s, want none", waiting.UpdateTaskedAt)
	}
	if n := len(rig.replacements()); n != 0 {
		t.Fatalf("the pod was replaced %d times before the tester answered, want none", n)
	}

	rig.signal(claim.RoleTester, "c0ffee")
	replaced := rig.awaitReplacement(1, "once the tester answered")
	if got := replaced[0].Pod; got.Claim != tester || got.Locator == nil || !reflect.DeepEqual(*got.Locator, *podUpdateLocator(tester, podUpdateIssue, podUpdatePod)) {
		t.Fatalf("ReplacePod was asked %+v, want the tester's process in pod %s", got, podUpdatePod)
	}
	roles := []claim.Role{}
	for _, ask := range rig.asks() {
		roles = append(roles, ask.Role)
	}
	if !slices.Equal(roles, []claim.Role{claim.RoleTester}) {
		t.Errorf("the round asked %v, want the tester alone", roles)
	}
}

// replacedThroughAProcessBesideAWaitingClaim carries a round to its replacement through the
// tester's process, beside the implementer, whose process died in the pod and whose relaunch the
// runtime refused for want of the pod's round (diesRefused): a member of the round with a session
// whose launch waits on the pod (awaitsRoundPod), which has not answered and has no process to
// answer with. The round waits on no answer of it (waitingClaims) and passes over no claim, so it
// ends on the tester's answer. work is what a read of the issue's workspace finds. It holds that
// the pod is replaced once, through the tester's process, and that the replacement's one line
// names nobody under notWaitedFor and the implementer under waitingOnThePodNotWaitedFor, and it
// returns the rig and that line.
func replacedThroughAProcessBesideAWaitingClaim(t *testing.T, work runtime.WorkspaceWork) (*podUpdateRig, map[string]any) {
	t.Helper()
	rig := newPodUpdateRig(t, readyWorker(claim.RoleImplementer), readyWorker(claim.RoleTester))
	tester := rig.tokens[claim.RoleTester]
	rig.rt.ScriptWorkspaceWork(podUpdateIssue, work)
	rig.drifts(podUpdateExchangeDrift)
	rig.refuseUntilReplaced(claim.RoleImplementer)
	rig.diesRefused(claim.RoleImplementer)

	rig.tick()
	rig.awaitAsked(claim.RoleTester, podUpdateRound)
	rig.awaitRound(claim.RoleImplementer, podUpdateRound)
	rig.signal(claim.RoleTester, "c0ffee")
	replaced := rig.awaitReplacement(1, "once the tester answered")
	if got := replaced[0].Pod; got.Claim != tester || podOf(got.Locator) != podUpdatePod {
		t.Fatalf("ReplacePod was asked %+v, want the tester's process in pod %s", got, podUpdatePod)
	}
	rig.awaitClaim(claim.RoleTester, "the round's end", func(c supervise.Claim) bool { return c.UpdateRound == "" })
	lines := loggedLines(t, rig.logs.String(), podUpdateReplacedLine)
	if len(lines) != 1 || lines[0]["atTheBound"] != false {
		t.Fatalf("the replacement was logged %+v, want one line that no bound made", lines)
	}
	line := lines[0]
	wantField(t, line, "notWaitedFor")
	wantField(t, line, "waitingOnThePodNotWaitedFor", claim.RoleImplementer)
	return rig, line
}

// wantReadThroughTheTester holds the replacement beside a waiting claim to one read of the
// workspace, made through the tester, whose process the pod is replaced through, and named on the
// line as found, which is "found work" or "found no work".
func wantReadThroughTheTester(t *testing.T, rig *podUpdateRig, line map[string]any, found string) {
	t.Helper()
	if reads := rig.rt.CallsOf("WorkspaceWork"); len(reads) != 1 || reads[0].Issue != podUpdateIssue || reads[0].Role != claim.RoleTester {
		t.Errorf("the workspace was read %+v, want one read of %s through the tester", reads, podUpdateIssue)
	}
	if line["workspaceRead"] != found || line["workspaceReadBy"] != string(claim.RoleTester) {
		t.Errorf("the replacement's line names the read %+v, want one the tester made that %s", line, found)
	}
}

// The round's pod holds the tester's process, and the implementer's process died there before
// its relaunch was refused, so its launch waits on the pod. It is a member of the round with a
// session that has not answered. No role was asked to push what its worker left on the pod's
// disk, as no role is asked for a claim the round passed over, so the replacement through the
// tester's process reads the workspace through the tester first, though the round passed over no
// claim. The read finds an unpushed commit, and one message on the issue names the implementer
// with its state, under the sentence of a read made through a process, and the commit. Had the
// implementer's own retry reached the pod first, it would have replaced the pod at a launch and
// named the implementer too (endRoundALaunchReplaced), so the issue hears of that work whichever
// replacement comes first.
//
// The negative control is #4392 at a4cdff2799: the round passed over no claim, so the replacement
// made no read, and the issue was written nothing.
func TestPodUpdateReadsTheWorkspaceForAWaitingClaimBesideAProcess(t *testing.T) {
	rig, line := replacedThroughAProcessBesideAWaitingClaim(t,
		runtime.WorkspaceWork{Head: passedOverHead, Commits: []string{passedOverCommit}, Count: 1})
	wantReadThroughTheTester(t, rig, line, "found work")
	messages := rig.messages()
	if len(messages) != 1 {
		t.Fatalf("the issue was written %d messages, want one: %q", len(messages), messages)
	}
	for _, want := range []string{toldNotWaitedForRoles + "implementer (launching).", passedOverCommit, "Work that is not on a branch at origin is lost."} {
		if !strings.Contains(messages[0], want) {
			t.Errorf("the message does not say %q:\n%s", want, messages[0])
		}
	}
	if strings.Contains(messages[0], toldUnansweredRoles) {
		t.Errorf("the message says %q, want the sentence of a read made through a process:\n%s", toldUnansweredRoles, messages[0])
	}
}

// The same round over a workspace whose read finds no work: the replacement reads the workspace
// through the tester, and the issue is written nothing, since the read found nothing that went
// with the pod.
//
// The negative control is #4392 at a4cdff2799: the line named the read as not made, since the
// round passed over no claim.
func TestPodUpdateWritesNothingWhereTheReadForAWaitingClaimFindsNoWork(t *testing.T) {
	rig, line := replacedThroughAProcessBesideAWaitingClaim(t, runtime.WorkspaceWork{Head: passedOverHead})
	wantReadThroughTheTester(t, rig, line, "found no work")
	if messages := rig.messages(); len(messages) != 0 {
		t.Errorf("the issue was written %q, want nothing: the read found no work", messages)
	}
}

// The replacement's log line names each claim with a session that the round did not wait for
// (replacePodForRound). The issue has two such claims. The implementer's process died and the
// runtime refused its relaunch for want of the pod's round, so its launch waits on the pod and it
// has no process to answer. The planner failed, so the round asks it nothing. The round waits for
// neither of them: it replaces the pod before its bound, through the locator the refusal named,
// and its one line names the planner in notWaitedFor and the implementer in
// waitingOnThePodNotWaitedFor.
//
// The control is a line with one of the two fields empty: this test then fails and names that
// field.
func TestPodUpdateReplacementLineNamesEachClaimTheRoundDidNotWaitFor(t *testing.T) {
	rig := newPodUpdateRig(t,
		readyWorker(claim.RoleImplementer),
		podUpdateRole{role: claim.RolePlanner, state: supervise.StateFailed, session: true,
			contract: readyWorker(claim.RolePlanner).contract},
	)
	token := rig.tokens[claim.RoleImplementer]
	rig.drifts(podUpdateExchangeDrift)
	rig.refuseUntilReplaced(claim.RoleImplementer)
	rig.diesRefused(claim.RoleImplementer)
	resumes := rig.rt.CallsOf("Resume")
	refused := resumes[len(resumes)-1].Spec.Generation

	rig.tick()
	rig.awaitRound(claim.RoleImplementer, podUpdateRound)
	replaced := rig.awaitReplacement(1, "with no claim to wait for")
	if got := replaced[0].Pod; got.Claim != token || got.Locator == nil ||
		!reflect.DeepEqual(*got.Locator, awaitedLocator(token, podUpdatePod, refused)) {
		t.Fatalf("ReplacePod was asked %+v, want the locator the refusal named, in pod %s",
			got, podUpdatePod)
	}
	lines := loggedLines(t, rig.logs.String(), podUpdateReplacedLine)
	if len(lines) != 1 || lines[0]["atTheBound"] != false {
		t.Fatalf("the replacement was logged %+v, want one line that no bound made", lines)
	}
	wantField(t, lines[0], "notWaitedFor", claim.RolePlanner)
	wantField(t, lines[0], "waitingOnThePodNotWaitedFor", claim.RoleImplementer)
}

// The round passed over a claim the daemon suspended, and no claim records a process in the pod,
// so the pod is replaced through the claim whose launch waits on it (podProcess). The passed-over
// claim would make a read of the issue's workspace due (readRoundWorkspace), but no role process
// runs in the pod to make it: the waiting claim runs none there, and the Sandbox runtime refuses a
// read through a claim that records no process (its runningRoleConn), as the fake is scripted to
// here. So the replacement makes no read, as the replacement of a pod no role process ran in makes
// none (processlessRead). Its line names the read as not made because no role process ran in the
// pod, and names the claims as every replacement through ReplacePod does. The one message on the
// issue names what the replacement by issue and at a launch name (lostClaims): each member that
// did not answer the round, the suspended planner and the waiting implementer, whose worker died
// in the pod with what it did not push. It says that no role process ran in the pod.
//
// The negative controls are #4392 before its fix, where the round read the workspace through the
// waiting implementer, the read failed, and the message said that the read failed; and #4392 at
// f7c2ed015f, where the message named the planner alone, as a claim the round did not wait for.
func TestPodUpdateMakesNoReadThroughAClaimWhoseLaunchWaitsOnThePod(t *testing.T) {
	rig := newPodUpdateRig(t,
		readyWorker(claim.RoleImplementer),
		suspendedRole(claim.RolePlanner, supervise.SuspendedByDaemon),
	)
	token := rig.tokens[claim.RoleImplementer]
	rig.rt.FailWorkspaceWork(podUpdateIssue, fmt.Errorf("read %s's workspace: %s holds no role process to read it", podUpdateIssue, token))
	rig.drifts(podUpdateExchangeDrift)
	rig.refuseUntilReplaced(claim.RoleImplementer)
	rig.diesRefused(claim.RoleImplementer)

	rig.tick()
	rig.awaitRound(claim.RoleImplementer, podUpdateRound)
	rig.awaitRound(claim.RolePlanner, podUpdateRound)
	replaced := rig.awaitReplacement(1, "through the claim whose launch waits on it")
	if got := replaced[0].Pod; got.Claim != token || podOf(got.Locator) != podUpdatePod {
		t.Fatalf("ReplacePod was asked %+v, want the waiting implementer's locator in pod %s", got, podUpdatePod)
	}
	if reads := rig.rt.CallsOf("WorkspaceWork"); len(reads) != 0 {
		t.Errorf("the workspace was read %+v, want no read: no role process ran in the pod", reads)
	}
	lines := loggedLines(t, rig.logs.String(), podUpdateReplacedLine)
	if len(lines) != 1 || lines[0]["atTheBound"] != false {
		t.Fatalf("the replacement was logged %+v, want one line that no bound made", lines)
	}
	line := lines[0]
	if line["workspaceRead"] != "not made" || line["workspaceNotReadBecause"] != noProcessRead {
		t.Errorf("the replacement's line names the read %+v, want one not made because %q", line, noProcessRead)
	}
	wantField(t, line, "notWaitedFor", claim.RolePlanner)
	wantField(t, line, "waitingOnThePodNotWaitedFor", claim.RoleImplementer)

	wantToldUnanswered(t, rig.messages(), "planner (suspended)", "implementer (launching)")
}

// toldUnansweredRoles is the lead sentence of the message on the issue that names the members of
// the round that had not answered it, of a pod no role process ran in (workspaceLostMessage).
const toldUnansweredRoles = "These roles did not answer the update round before the replacement. No process of these roles ran in the pod: "

// toldNotWaitedForRoles is the lead sentence of the message on the issue that names the claims the
// round did not wait for, over a read made through a process (workspaceLostMessage).
const toldNotWaitedForRoles = "The round did not wait for these roles, because no process of these roles ran in the pod: "

// wantToldUnanswered holds messages, what the issue was written, to the one message a replacement
// of a pod no role process ran in writes: it names each of named, a role with its state, as a
// member that did not answer the round, says that no role process ran in the pod to read the
// workspace, and says that work not on a branch at origin is lost.
func wantToldUnanswered(t *testing.T, messages []string, named ...string) {
	t.Helper()
	if len(messages) != 1 {
		t.Fatalf("the issue was written %d messages, want one: %q", len(messages), messages)
	}
	message := messages[0]
	for _, want := range append([]string{toldUnansweredRoles, "No role process ran in the pod", "Work that is not on a branch at origin is lost."}, named...) {
		if !strings.Contains(message, want) {
			t.Errorf("the message does not say %q:\n%s", want, message)
		}
	}
	for _, not := range []string{toldNotWaitedForRoles, "The read failed"} {
		if strings.Contains(message, not) {
			t.Errorf("the message says %q, want the message of a pod no role process ran in:\n%s", not, message)
		}
	}
}

// The lead sentence of the message on the issue says why it names its roles
// (workspaceLostMessage). A replacement of a pod no role process ran in names each member that had
// not answered the round (lostClaims), whoever replaced the pod: the sweep by issue, a launch, or
// the sweep through a claim whose launch waits on the pod. So its message says these roles did not
// answer. A read the sweep made through a process names the claims the round did not wait for
// (notWaitedFor), so its message says the round did not wait for them, whether the read failed or
// found work. The sweep makes both the replacement through a waiting claim and the read through a
// process (viaProcess), so whether a role process ran in the pod chooses the sentence
// (workspaceRead.noProcess), not who replaced it.
//
// The negative control is #4392 at f7c2ed015f, whose sentence followed who replaced the pod
// (workspaceRead.by): the sweep's replacement through a waiting claim said that the round did not
// wait for roles that had not answered it.
func TestTheWorkspaceLostMessageSaysWhyItNamesItsRoles(t *testing.T) {
	round := podUpdateRound
	planner := supervise.Claim{Token: mustClaimToken(t, podUpdateIssue, claim.RolePlanner), Issue: podUpdateIssue, Role: claim.RolePlanner,
		State: supervise.StateSuspended, Session: "ses_planner", UpdateRound: round}
	unanswered, notWaitedFor := toldUnansweredRoles+"planner (suspended).", toldNotWaitedForRoles+"planner (suspended)."
	for name, tc := range map[string]struct {
		read      workspaceRead
		want, not string
	}{
		"a read through a process that failed": {
			read: workspaceRead{round: round, by: viaProcess, role: claim.RoleImplementer, err: errors.New("jj answered 1")},
			want: notWaitedFor, not: unanswered},
		"a read through a process that found work": {
			read: workspaceRead{round: round, by: viaProcess, role: claim.RoleImplementer, work: runtime.WorkspaceWork{Head: "abc123", Commits: []string{"def456"}, Count: 1}},
			want: notWaitedFor, not: unanswered},
		"the sweep's replacement through a claim whose launch waits on the pod": {
			read: processlessRead(round, viaProcess), want: unanswered, not: notWaitedFor},
		"a replacement by issue":    {read: processlessRead(round, viaIssue), want: unanswered, not: notWaitedFor},
		"a replacement at a launch": {read: processlessRead(round, viaLaunch), want: unanswered, not: notWaitedFor},
	} {
		t.Run(name, func(t *testing.T) {
			message := workspaceLostMessage(round, podUpdateExchangeDrift, []supervise.Claim{planner}, tc.read, false)
			if !strings.Contains(message, tc.want) || strings.Contains(message, tc.not) {
				t.Errorf("the message reads:\n%s\nwant it to say %q, and not %q", message, tc.want, tc.not)
			}
		})
	}
}

// waitingOn is role's claim of the rig's issue with no process, its launch waiting on pod
// (supervise.Claim.AwaitsPod), in round where round is not "", and with a session where session
// says so.
func waitingOn(t *testing.T, role claim.Role, pod, round string, session bool) supervise.Claim {
	t.Helper()
	token := mustClaimToken(t, podUpdateIssue, role)
	awaited := awaitedLocator(token, pod, 2)
	c := supervise.Claim{Token: token, Issue: podUpdateIssue, Role: role, State: supervise.StateLaunching, AwaitsPod: &awaited, UpdateRound: round}
	if session {
		c.Session = "ses_" + string(role)
	}
	return c
}

// runningIn is role's claim of the rig's issue, ready with a session and a process in pod, in
// round where round is not "".
func runningIn(t *testing.T, role claim.Role, pod, round string) supervise.Claim {
	t.Helper()
	token := mustClaimToken(t, podUpdateIssue, role)
	return supervise.Claim{Token: token, Issue: podUpdateIssue, Role: role, State: supervise.StateReady,
		Session: "ses_" + string(role), Locator: podUpdateLocator(token, podUpdateIssue, pod), UpdateRound: round}
}

// The group's pod, and the drift read of it, take a claim's awaited pod where it records no
// process: it is the pod the round opens over, two claims that record it and a process in it are
// one pod, and an awaited pod other than a process's is two pods, a replacement in flight. Its
// drift is read through the awaited locator, which carries the claim's own token.
func TestTheRoundReadsThePodAClaimsLaunchWaitsOn(t *testing.T) {
	for name, tc := range map[string]struct {
		group []supervise.Claim
		pod   string
		split bool
	}{
		"the only claim waits on the pod": {
			group: []supervise.Claim{waitingOn(t, claim.RoleImplementer, podUpdatePod, "", true)}, pod: podUpdatePod},
		"a claim waits on the pod another runs in": {
			group: []supervise.Claim{waitingOn(t, claim.RoleImplementer, podUpdatePod, "", true), runningIn(t, claim.RoleTester, podUpdatePod, "")}, pod: podUpdatePod},
		"a claim waits on another pod than a process runs in": {
			group: []supervise.Claim{waitingOn(t, claim.RoleImplementer, podUpdateReplacementPod, "", true), runningIn(t, claim.RoleTester, podUpdatePod, "")}, split: true},
		"no claim records a process or a pod": {
			group: []supervise.Claim{{Token: mustClaimToken(t, podUpdateIssue, claim.RoleTester), Issue: podUpdateIssue, Role: claim.RoleTester,
				State: supervise.StateSuspended, Session: "ses_tester"}}},
	} {
		t.Run(name, func(t *testing.T) {
			if pod, split := groupPod(tc.group); pod != tc.pod || split != tc.split {
				t.Fatalf("groupPod = %q, split %t; want %q, split %t", pod, split, tc.pod, tc.split)
			}
		})
	}

	t.Run("the drift is read through the awaited pod", func(t *testing.T) {
		waiting := waitingOn(t, claim.RoleImplementer, podUpdatePod, "", true)
		rt := fake.NewRuntime()
		rt.ScriptPodDrift(waiting.Token, podUpdateExchangeDrift)
		s := &supervision{runtime: rt, log: quietLogger()}
		reasons, reading := s.groupDrift(context.Background(), podUpdateIssue, []supervise.Claim{waiting})
		if reading != podRead || !slices.Equal(reasons, []string{podUpdateExchangeDrift}) {
			t.Fatalf("groupDrift = %q, reading %d; want %q, podRead", reasons, reading, podUpdateExchangeDrift)
		}
		asked := rt.CallsOf("PodDrift")
		if len(asked) != 1 || asked[0].Pod.Claim != waiting.Token || !reflect.DeepEqual(asked[0].Pod.Locator, waiting.AwaitsPod) {
			t.Fatalf("PodDrift was asked %+v, want the waiting claim's awaited pod %+v once", asked, waiting.AwaitsPod)
		}
		rt.ScriptPodGone(waiting.Token, true)
		if _, reading := s.groupDrift(context.Background(), podUpdateIssue, []supervise.Claim{waiting}); reading != podGone {
			t.Fatalf("groupDrift of an awaited pod that is gone reads %d, want podGone", reading)
		}
	})
}

// The round's answer set: a claim whose launch waits on the round's own pod is one the round is
// posted to — with a session or without, since it may be the issue's only claim — and one the
// replacement waits on no answer of, since it has no process in that pod. With a session and no
// answer, the gone-pod report names it as having lost what it left unpushed, as any member: its
// process can have died in that pod before its relaunch was refused. The replacement's line names
// it too (unwaitedOnThePod), and also where it does not record the round yet, which the report
// does not, since that claim is no member of the round. With an answer to the round, it pushed its
// work before the pod went, and neither names it. With no session it is posted the round only as
// a way to the pod, and neither names anything of it. A claim with a session that waits on another
// pod, and every claim with no process that waits on none, is weighed as any claim is: a claim the
// daemon suspended is passed over and waited for by nobody (roundAsks, dispatch://INFRA-4109 R1),
// and a member with no session that waits on no pod of this round is named lost, since the round
// is posted to no such claim and it forgot its session after the post (lostClaims). A claim with
// no session that waits on another pod is no way to this round's pod, so the round is not posted
// to it.
func TestTheRoundWaitsOnNoAnswerOfAClaimWhoseLaunchWaitsOnItsPod(t *testing.T) {
	round := podUpdateRound
	clock := roundClock{now: podUpdateStart, grace: podUpdateGrace}
	answeredIt := waitingOn(t, claim.RoleImplementer, podUpdatePod, round, true)
	signalled := podUpdateStart
	answeredIt.UpdateSignalledAt = &signalled
	for name, tc := range map[string]struct {
		claim                           supervise.Claim
		posted, waiting, lost, unwaited bool
	}{
		"it waits on the round's pod, with a session": {
			claim:  waitingOn(t, claim.RoleImplementer, podUpdatePod, round, true),
			posted: true, lost: true, unwaited: true},
		"it waits on the round's pod, with a session, and answered the round": {
			claim: answeredIt, posted: true},
		"it waits on the round's pod, with a session, and records no round": {
			claim:  waitingOn(t, claim.RoleImplementer, podUpdatePod, "", true),
			posted: true, unwaited: true},
		"it waits on the round's pod, with no session": {
			claim: waitingOn(t, claim.RoleImplementer, podUpdatePod, round, false), posted: true},
		"it waits on another pod, with a session": {
			claim: waitingOn(t, claim.RoleImplementer, podUpdateReplacementPod, round, true), posted: true, waiting: true, lost: true},
		"it waits on another pod, with no session": {
			claim: waitingOn(t, claim.RoleImplementer, podUpdateReplacementPod, round, false), lost: true},
		"it is suspended and waits on no pod": {
			claim: supervise.Claim{Token: mustClaimToken(t, podUpdateIssue, claim.RoleImplementer), Issue: podUpdateIssue, Role: claim.RoleImplementer,
				State: supervise.StateSuspended, Session: "ses_implementer", UpdateRound: round},
			posted: true, lost: true},
		"it runs in the round's pod": {
			claim: runningIn(t, claim.RoleImplementer, podUpdatePod, round), posted: true, waiting: true, lost: true},
		"it has no session, no process and waits on no pod": {
			claim: supervise.Claim{Token: mustClaimToken(t, podUpdateIssue, claim.RoleImplementer), Issue: podUpdateIssue, Role: claim.RoleImplementer,
				State: supervise.StateQueued, UpdateRound: round},
			lost: true},
	} {
		t.Run(name, func(t *testing.T) {
			group := []supervise.Claim{tc.claim}
			for _, check := range []struct {
				reader string
				got    []supervise.Claim
				want   bool
			}{
				{"postedClaims", postedClaims(group, round), tc.posted},
				{"waitingClaims", waitingClaims(group, round, clock), tc.waiting},
				{"lostClaims", lostClaims(group, round), tc.lost},
				{"unwaitedOnThePod", unwaitedOnThePod(group, round), tc.unwaited},
			} {
				if named := len(check.got) == 1; named != check.want {
					t.Errorf("%s names %v, want the claim named: %t", check.reader, namesOf(check.got), check.want)
				}
			}
		})
	}
}

// The pod is replaced through a claim whose launch waits on it where no claim records a process
// there, through the locator the runtime's refusal named; a process in the pod is still the one
// the pod is ended through, whichever claim comes first, and a claim waiting on another pod is no
// way to this one. podProcess answers the claim itself, and the runtime is asked through it at
// the process it records or the pod its launch waits on (podKnown).
func TestTheRoundReplacesThePodThroughAClaimWhoseLaunchWaitsOnIt(t *testing.T) {
	round := podUpdateRound
	waiting := waitingOn(t, claim.RoleImplementer, podUpdatePod, round, true)
	elsewhere := waitingOn(t, claim.RoleImplementer, podUpdateReplacementPod, round, true)
	running := runningIn(t, claim.RoleTester, podUpdatePod, round)
	movedOn := runningIn(t, claim.RoleTester, podUpdateReplacementPod, round)
	for name, tc := range map[string]struct {
		group []supervise.Claim
		want  *supervise.Claim
	}{
		"only a claim that waits on the pod":               {group: []supervise.Claim{waiting}, want: &waiting},
		"a process in the pod, after a claim that waits":   {group: []supervise.Claim{waiting, running}, want: &running},
		"a claim that waits on another pod":                {group: []supervise.Claim{elsewhere}},
		"a process in another pod, and a claim that waits": {group: []supervise.Claim{movedOn, waiting}, want: &waiting},
	} {
		t.Run(name, func(t *testing.T) {
			through, found := podProcess(tc.group, round)
			if tc.want == nil {
				if found {
					t.Fatalf("podProcess = %+v, want no way to the round's pod", through)
				}
				return
			}
			if !found || !reflect.DeepEqual(through, *tc.want) {
				t.Fatalf("podProcess = %+v (found %t), want %+v", through, found, *tc.want)
			}
			got, want := podKnown(through), podLocator(*tc.want)
			if got.Claim != tc.want.Token || !reflect.DeepEqual(got.Locator, want) {
				t.Fatalf("podKnown = %+v, want %s through %+v", got, tc.want.Token, want)
			}
			if err := got.Validate(); err != nil {
				t.Fatalf("podKnown answered a process no runtime takes: %v", err)
			}
		})
	}
}
