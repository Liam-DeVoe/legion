package workflow

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/pods"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// A wake brings a released pod back. The release suspended every claim of the pod with the source
// `released` and deleted the pod, its disk and its Sandbox; a wake resumes one of those claims,
// which relaunches its session in a fresh pod whose launcher restores the workspace bundle the
// release saved. Five things wake a pod, and this file holds four of them: the CI settlement, the
// role message and the agent message the Envoy listener reports, and the stored backstop. The
// fifth — a delivery the daemon already had for a role of the pod — needs nothing here, because a
// delivery to a suspended claim already resumes it (supervise's deliverResuming) and the delivery
// is its own account of why the pod came back.
//
// Every wake acts only on a claim that is still `released`, of an issue still in the workflow
// whose tree is live, and a role comes back once while its pod is on its way: the role's one open
// resume is what says so, so one head that settles again as each of its checks finishes wakes the
// pod once.

// Pods is the daemon's record of its released pods as the workflow reads and writes it: the Envoy
// listener's liveness report, the claims a release left released, the wake that brings one back,
// and the backstop a wake fires. The daemon hands one in (Config.Pods, the store's own
// PodRecordsOf), which holds the project as the claim-keyed tables spell it, so the engine carries
// one project and reaches Postgres through an interface here as it does through record.Store.
//
// A wake is taken whole by TakePodWake: the resume recorded against the release the wake ends,
// which the role's one open resume refuses a second of, and the woken role's backstop cancelled.
// The store owns that order, and this package never writes a part of it.
type Pods interface {
	// RecordListenerReady stamps when the Envoy listener last said it is alive and reporting,
	// which is the whole of R7's gate.
	RecordListenerReady(ctx context.Context, tx pgx.Tx, at time.Time) error
	// ReleasedClaimOfRole is the released claim of issue's role, and whether there is one.
	ReleasedClaimOfRole(ctx context.Context, tx pgx.Tx, issue string, role claim.Role) (supervise.Claim, bool, error)
	// ReleasedClaimByToken and ReleasedClaimBySession are the released claim a listener's report
	// names, by the role token a message was published to and by the session one was sent to.
	// Every daemon reads the report and only the one holding a released claim of that name acts.
	ReleasedClaimByToken(ctx context.Context, tx pgx.Tx, token string) (supervise.Claim, bool, error)
	ReleasedClaimBySession(ctx context.Context, tx pgx.Tx, session string) (supervise.Claim, bool, error)
	// ClaimInReleaseBySession and ClaimInReleaseByToken are the live claim a listener's report
	// names while a release of its pod has not ended: a claim neither suspended, failed nor
	// retired, whose issue's release mark still stands. They answer the claim, when the release
	// was marked, and whether there is one. A wake that finds such a claim comes before the
	// release records the claim as released, and is applied again (listenerWake).
	ClaimInReleaseBySession(ctx context.Context, tx pgx.Tx, session string) (supervise.Claim, time.Time, bool, error)
	ClaimInReleaseByToken(ctx context.Context, tx pgx.Tx, token string) (supervise.Claim, time.Time, bool, error)
	// StartQueued is whether the outbox holds a start of issue's role at generation that names
	// no phase and waits for no slot: the start a cancelled release queues for each claim it
	// suspended, and the start a wake queues. Such a role is already coming back.
	StartQueued(ctx context.Context, tx pgx.Tx, issue string, role claim.Role, generation uint64) (bool, error)
	// TakePodWake takes one wake of issue's role and answers whether this call took it; false is
	// a wake another pass already took, which is already bringing the pod back.
	TakePodWake(ctx context.Context, tx pgx.Tx, issue string, role claim.Role, kind string, at time.Time) (bool, error)
	// ClosePodGoneResume closes the open resume of issue's role when the pod it was waiting for
	// has gone away: a release of the issue recorded since that wake was taken. It answers the
	// issue's newest release where it closed a row, and 0 where it closed none.
	ClosePodGoneResume(ctx context.Context, tx pgx.Tx, issue string, role claim.Role) (int64, error)
	// FirePodBackstop marks the backstop fired and answers whether this call fired it.
	FirePodBackstop(ctx context.Context, tx pgx.Tx, id int64) (bool, error)
	// OpenPodBackstop is whether issue's role has a backstop armed and not yet fired: a release
	// that left the role with its phase open armed it, so the backstop brings the pod back. The
	// cutover's repair reads it to pass over a merger something will still wake
	// (cutoverMergerRepair).
	OpenPodBackstop(ctx context.Context, tx pgx.Tx, issue string, role claim.Role) (bool, error)
}

// wakeRow is the outbox row a wake writes: a `start` of the woken role at the issue's current
// generation, naming no phase, carrying the task that says why the pod came back. It is the same
// row a phase start writes, which is the point — the outbox's start executor already resumes a
// suspended claim by rule (RequestRuleResume), already records the start that cancels a release
// still in flight (StartedBy, read by store.IssueReleaseStop), and already delivers the row's
// task once the relaunched agent is ready. Naming no phase keeps the row out of every phase
// fence: the issue's phase may move between the wake and the relaunch, and the wake's task is
// never the phase's work.
func wakeRow(issue record.Issue, role claim.Role, task string) record.SuperviseRequest {
	return record.SuperviseRequest{Op: "start", Tree: issue.Tree, Role: role, Generation: issue.Generation, Task: task}
}

// A wake report that finds its claim in a release that has not ended is applied again every
// wakeRetryInRelease, for at most wakeHeldForRelease after the report's own time. That limit is
// longer than INFRA's 610-second bound on a stop (runtime/sandbox), so a release that ends
// records the claim inside it. A release that has not ended by then has a fault of its own, and
// the wake is dropped with one warning (listenerWakeInRelease).
const (
	wakeRetryInRelease = 30 * time.Second
	wakeHeldForRelease = 15 * time.Minute
)

// WakeTask is the one task a woken role is given: which wake brought its pod back and what that
// wake was about. A message Envoy kept for the role can reach the woken session as a card before
// this task does, so the role message sentence says so. R6 asks for exactly one such task or
// message per wake, and the outbox row that carries it is written in the wake's own transaction,
// so a wake that commits has its task and one that does not has neither.
//
// The texts of the two message wakes hold whether or not Envoy stored the message. The wake
// report does not say whether it did, so each text says what happens to a message Envoy stored
// and to one it did not.
func WakeTask(kind, detail string) string {
	const preamble = "Your issue's pod had been released while it was idle, and this wake brought it back. " +
		"Your session, your workspace and any work you had not pushed are as you left them. "
	switch kind {
	case pods.WakeChecks:
		return preamble + "The wake is a CI settlement on your pull request's head: " + detail +
			". Read the checks before you decide anything."
	case pods.WakeRoleMessage:
		return preamble + "The wake is a message somebody published to your role topic (" + detail +
			") while your pod was gone. A message that Envoy kept for your role reaches this session, " +
			"for one hour after Envoy kept it. It comes as an Envoy card that shows kept_at, possibly " +
			"before this task. When that hour ends before a session takes the message, Envoy drops it " +
			"and tells its sender. A message that Envoy did not keep, such as one sent with keep: false, " +
			"does not reach you, and its sender must send it again."
	case pods.WakeAgentMessage:
		return preamble + "The wake is a message somebody sent to your session (" + detail +
			") while your pod was gone. A message that Envoy stored for your session reaches this session, " +
			"for 72 hours after Envoy stored it. A message that Envoy did not store, such as a direct send " +
			"that Envoy refused, does not reach you, and its sender must send it again."
	case pods.WakeBackstop:
		return preamble + "The wake is the release's backstop running out: " + detail +
			". Nothing else asked for you, so take up your phase where you left it."
	default:
		return preamble + "The wake is " + kind + ": " + detail + "."
	}
}

// listenerWake applies one wake report of the Envoy listener (intake.ListenerWake). A
// `listener_ready` report is the positive liveness signal R7's gate reads and wakes nothing. A
// role or agent report names a role token or a session, and this daemon acts only on one of its
// own claims under that name: every daemon consumes the subject, because the listener does not
// know which daemon owns a session and must not learn.
//
// A released claim is woken (listenerWakeReleased). A live claim whose pod a release is still
// taking makes the report wait for the release to end (listenerWakeInRelease). Any other report
// does nothing.
func (e *Engine) listenerWake(ctx context.Context, tx pgx.Tx, fact intake.ListenerWake) (intake.Result, error) {
	if e.cfg.Pods == nil {
		return intake.Result{}, nil
	}
	if fact.Kind == intake.ListenerWakeReady {
		return intake.Result{}, e.cfg.Pods.RecordListenerReady(ctx, tx, fact.At)
	}
	var released supervise.Claim
	var found bool
	var err error
	kind, detail := pods.WakeRoleMessage, fact.Role
	if fact.Kind == intake.ListenerWakeAgent {
		kind, detail = pods.WakeAgentMessage, fact.Session
		released, found, err = e.cfg.Pods.ReleasedClaimBySession(ctx, tx, fact.Session)
	} else {
		released, found, err = e.cfg.Pods.ReleasedClaimByToken(ctx, tx, fact.Role)
	}
	if err != nil {
		return intake.Result{}, err
	}
	if found {
		return intake.Result{}, e.listenerWakeReleased(ctx, tx, released, kind, detail, fact)
	}
	return intake.Result{}, e.listenerWakeInRelease(ctx, tx, kind, detail, fact)
}

// listenerWakeReleased wakes the released claim a listener's report names, unless the role is
// already coming back on a start the outbox holds (Pods.StartQueued). A cancelled release queues
// that start for each claim it suspended, and the claim reads `released` until the start runs
// (store.CancelIssueRelease). The pod never went away, so a wake taken then would record a resume
// and queue a second start for a role that needs neither.
func (e *Engine) listenerWakeReleased(ctx context.Context, tx pgx.Tx, released supervise.Claim, kind, detail string, fact intake.ListenerWake) error {
	issue, err := e.store.Issue(ctx, tx, released.Issue)
	if err != nil || issue == nil {
		return err
	}
	queued, err := e.cfg.Pods.StartQueued(ctx, tx, issue.Key, released.Role, issue.Generation)
	if err != nil {
		return err
	}
	if queued {
		e.log.Info("wake: the role is coming back on a start already queued; this wake takes nothing",
			"issue", issue.Key, "role", released.Role, "kind", kind, "detail", detail, "report", fact.Report)
		return nil
	}
	_, err = e.wakeReleased(ctx, tx, released, kind, detail, fact.At)
	return err
}

// listenerWakeInRelease is a report that found no released claim. A release stops a role's
// process before it records the claim as released, and that record waits for the stop to end. A
// report that comes in that gap finds a live claim whose issue's release mark still stands
// (Pods.ClaimInReleaseBySession, Pods.ClaimInReleaseByToken). The report is then applied again
// after wakeRetryInRelease (intake.RetryLater, Waiting): once the release records the claim, the
// report wakes it, and once a cancel ends the release, the report finds the claim live or its
// start queued. A report still in that gap wakeHeldForRelease after its own time is dropped with
// one warning. A report that finds no claim in a release does nothing.
func (e *Engine) listenerWakeInRelease(ctx context.Context, tx pgx.Tx, kind, detail string, fact intake.ListenerWake) error {
	var held supervise.Claim
	var marked time.Time
	var found bool
	var err error
	if fact.Kind == intake.ListenerWakeAgent {
		held, marked, found, err = e.cfg.Pods.ClaimInReleaseBySession(ctx, tx, fact.Session)
	} else {
		held, marked, found, err = e.cfg.Pods.ClaimInReleaseByToken(ctx, tx, fact.Role)
	}
	if err != nil || !found {
		return err
	}
	deadline := fact.At.Add(wakeHeldForRelease)
	if !e.now().Before(deadline) {
		e.log.Warn("wake: a wake that came during a release was dropped: the release did not end within 15 minutes of the report",
			"issue", held.Issue, "role", held.Role, "kind", kind, "detail", detail, "report", fact.Report, "at", fact.At)
		return nil
	}
	return &intake.RetryLater{After: wakeRetryInRelease, Waiting: true, Err: fmt.Errorf(
		"the %s wake for %s finds %s's %s in a release marked at %s; it is applied again until the release ends, at the latest until %s",
		kind, detail, held.Issue, held.Role, marked.UTC().Format(time.RFC3339), deadline.UTC().Format(time.RFC3339))}
}

// wakeOnSettlement wakes a released pod on a CI settlement the daemon records for the head of its
// issue's pull request, which is R2. It runs before the settlement is applied, so the woken role
// is told of the settlement the same transaction records.
//
// Only the worker of the pod's open phase is woken: the settlement is that worker's business and
// no one else's, and an architect reads it from its own notices. A head that settles again as each
// of its checks finishes wakes the pod no second time, because the role's resume is still open.
func (e *Engine) wakeOnSettlement(ctx context.Context, tx pgx.Tx, fact intake.PullRequestChecks) (claim.Role, error) {
	if e.cfg.Pods == nil || fact.HeadSHA == "" {
		return "", nil
	}
	pr, err := e.pullRequest(ctx, tx, fact.Repo, fact.Number)
	if err != nil || pr == nil {
		return "", err
	}
	issue, err := e.store.Issue(ctx, tx, pr.Issue)
	if err != nil || issue == nil {
		return "", err
	}
	role := OpenPhaseRole(issue.Phase)
	if role == "" {
		return "", nil
	}
	released, found, err := e.cfg.Pods.ReleasedClaimOfRole(ctx, tx, issue.Key, role)
	if err != nil || !found || released.ServingGeneration != issue.Generation {
		return "", err
	}
	// The role is the settlement's to tell only when this call is what woke it: a wake another
	// pass took is already bringing the pod back with a task of its own, and a claim this
	// settlement did not wake is told as any live role is (tellChecks).
	woken, err := e.wakeReleased(ctx, tx, released, pods.WakeChecks, settlementDetail(fact), e.now())
	if err != nil || !woken {
		return "", err
	}
	return role, nil
}

// settlementDetail names the head and what its settlement came to, which is what the woken role's
// task carries: the checks the settlement names failed, then the ones it names cancelled, and
// "every check passed" when it names neither.
func settlementDetail(fact intake.PullRequestChecks) string {
	head := fact.HeadSHA
	if len(head) > 12 {
		head = head[:12]
	}
	switch {
	case len(fact.Failing) > 0:
		return head + " failed " + strings.Join(fact.Failing, ", ")
	case len(fact.Cancelled) > 0:
		return head + " had " + strings.Join(fact.Cancelled, ", ") + " cancelled"
	default:
		return head + " passed every check"
	}
}

// podBackstopDue wakes a released pod on its backstop, which is R5: backstop_minutes after the
// release, whatever else did or did not happen. The row is fired under a conditional update, so a
// backstop two passes read fires once, and the row survives a restart, so one that came due while
// the daemon was down fires at the first pass after it starts.
func (e *Engine) podBackstopDue(ctx context.Context, tx pgx.Tx, fact intake.PodBackstopDue) (intake.Result, error) {
	if e.cfg.Pods == nil {
		return intake.Result{}, nil
	}
	fired, err := e.cfg.Pods.FirePodBackstop(ctx, tx, fact.ID)
	if err != nil || !fired {
		return intake.Result{}, err
	}
	released, found, err := e.cfg.Pods.ReleasedClaimOfRole(ctx, tx, fact.Issue, fact.Role)
	if err != nil || !found {
		return intake.Result{}, err
	}
	detail := fmt.Sprintf("the backstop armed for %s's %s came due", fact.Issue, fact.Role)
	_, err = e.wakeReleased(ctx, tx, released, pods.WakeBackstop, detail, e.now())
	return intake.Result{}, err
}

// wakeReleased is the one act every wake of this file performs, and the one place the gates are
// read: the claim is still released, and its tree is live — neither the issue nor its root has
// left the workflow and neither lingers (pods.InWorkflowNotLingering). A pod in any other state
// is a close's to stop or an operator's to resume, never a wake's.
//
// A wake of a role whose resume is already open wakes nothing a second time: that pod is coming
// back, and the start row of the wake that took it carries the task. A wake that takes it records
// itself against the release it ends and cancels the woken role's backstop (Pods.TakePodWake), and
// writes the one outbox row that resumes the role and gives it the task naming the wake. Both are
// this transaction's, so a wake this daemon took is a wake whose start row is queued: a take that
// committed without one is what wedges a role, which is why the outbox's own delivery wake, which
// resumes a claim in process instead, heals a refused wake (wakeForDelivery).
//
// One row would refuse a wake wrongly, and it is closed before the take rather than read out of a
// refusal: the resume of a pod that has gone away. A release of a pod no claim of the issue can
// use ends no wake and so closes no resume, and it still takes the pod, leaving a row that would
// otherwise refuse every later wake of that role — a CI settlement queueing nothing, and R5's
// backstop spent on a refusal, since podBackstopDue fires the row before it calls this. So each
// wake first closes a resume of this role written before the issue's newest release
// (Pods.ClosePodGoneResume), which matches nothing while a pod is on its way back, since such a
// resume was itself written after every release there is.
func (e *Engine) wakeReleased(ctx context.Context, tx pgx.Tx, released supervise.Claim, kind, detail string, at time.Time) (bool, error) {
	issue, err := e.store.Issue(ctx, tx, released.Issue)
	if err != nil || issue == nil {
		return false, err
	}
	root, err := e.store.Issue(ctx, tx, issue.Tree)
	if err != nil || root == nil {
		return false, err
	}
	if !pods.InWorkflowNotLingering(issue.Standing(), root.Standing(), record.OutOfWorkflow) {
		return false, nil
	}
	newest, err := e.cfg.Pods.ClosePodGoneResume(ctx, tx, issue.Key, released.Role)
	if err != nil {
		return false, err
	}
	if newest > 0 {
		e.log.Warn("wake: a pod went away under an open resume of a released role; this wake closes that resume and takes its own",
			"issue", issue.Key, "role", released.Role, "newest_release", newest, "kind", kind, "detail", detail)
	}
	taken, err := e.cfg.Pods.TakePodWake(ctx, tx, issue.Key, released.Role, kind, at)
	if err != nil || !taken {
		return false, err
	}
	e.log.Info("wake: a released pod is coming back", "issue", issue.Key, "role", released.Role, "kind", kind, "detail", detail)
	return true, e.enqueue(ctx, tx, issue.Key, wakeRow(*issue, released.Role, WakeTask(kind, detail)))
}

// OpenPhaseRole is the phase worker of an open phase, and "" where there is none: a phase no role
// works, and the architect, whose phases (accepting, and every phase of a tree root the table
// gives no worker) are the architect's own turn rather than a worker's. It is the one statement of
// "the worker of the open phase" the release reads: which role a release arms a backstop for —
// an architect gets none, because nothing of its own is due and a sub-architect woken for nothing
// would hold a pod open for the backstop period — which role a CI settlement wakes, and which
// live claim of a pod has a phase reminder armed in it.
func OpenPhaseRole(p phase.Phase) claim.Role {
	if role := phase.RoleFor(p); role != claim.RoleArchitect {
		return role
	}
	return ""
}
