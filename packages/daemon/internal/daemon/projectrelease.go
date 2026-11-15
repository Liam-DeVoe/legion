package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sjawhar/legion/daemon/internal/api"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// projectReleaseReread is how often a pod that waits for its asked roles has its claims read
// again: a turn's end raises no event the release can subscribe to, so the release looks, and it
// looks at once when an answer arrives.
const projectReleaseReread = 5 * time.Second

// projectReleasePoll is how often a stop the release asked for is read for the claim being stored
// suspended. The stop itself is the machine's and takes the worker's stop grace; this is only how
// soon the release notices it landed.
const projectReleasePoll = 250 * time.Millisecond

// releaseUnknownKind is why a Sandbox whose labels name none of the kinds a release takes is
// left, by the release and by its dry run alike.
const releaseUnknownKind = "its labels name no kind of Sandbox the release deletes"

// projectReleaseStore is what the project release reads and writes of the daemon's store: its
// record, the stop set a dry run reads as the release's own transaction reads it, each claim it
// stops or holds as the store holds it, which is what decides that the claim is stored suspended
// (awaitSettled), and every claim as the store holds it, which is where each claim's line of the
// result is read from at the end.
type projectReleaseStore interface {
	StartProjectRelease(ctx context.Context, project string, startedAt, boundAt time.Time) (store.ProjectRelease, []store.TakenOverIdleRelease, []store.ProjectReleaseClaim, error)
	RecordProjectRelease(ctx context.Context, id int64, result json.RawMessage) error
	FinishProjectRelease(ctx context.Context, id int64, result json.RawMessage, at time.Time) error
	ReadProjectRelease(ctx context.Context, project string) (store.ProjectRelease, bool, error)
	ProjectReleaseStopSet(ctx context.Context, project string) ([]store.ProjectReleaseClaim, error)
	ClaimByToken(ctx context.Context, token claim.Token) (supervise.Claim, bool, error)
	Claims(ctx context.Context) ([]supervise.Claim, error)
}

// projectReleaser is the daemon's project release (dispatch://INFRA-3583 R6): the operator's
// `legion project release` starts it through the API (api.ProjectRelease), and it runs as one
// goroutine of this process, since R7 ends a release at the next boot and never resumes one, so
// nothing of its acts needs a durable retry. Its record is durable; its acts are not. One release
// runs per process: a second start answers the first.
//
// The release does its acts in this order. It sets the daemon-wide mark and records itself
// (Start), from when on the daemon starts nothing of the project (R7). It lists every Sandbox of
// the project and classifies each issue pod from its claims: left, when a claim of it is suspended
// by an operator; no read, when no role process runs there; read otherwise. Each pod is then taken
// on a goroutine of its own: a read pod's workspace is read through one running role's shim, and
// each running role of a pod whose workspace holds unpushed work, whose shim refused the read, or
// that has a role in a turn is asked. Each asked pod waits until each asked role answered and no
// role of it is in a turn, or the bound; then its last read, its stop and its delete
// (stopAndDelete). Once every issue pod ended, the controller's claim and pod, the repository
// caches and any probe go. Each stop is waited for until the stop timeout after the later of the
// stop and the bound, since a claim in a turn is stopped at the turn's end or at the bound
// (stopDeadline), and a claim counts as stopped only once the store holds it so (awaitSettled). A
// pod it leaves, the controller's among them, has its claims held for an operator (hold). The
// result grows on the record as each Sandbox ends, and the release finishes once each claim it
// held for an operator is stored suspended or its wait ran out. Every goroutine it starts takes a
// store mark of its own (store.ForConcurrentWorker), since its reads, stops and holds run side by
// side and the pool refuses a second connection under one mark.
type projectReleaser struct {
	// ctx is the release goroutine's lifetime, supervision's: a daemon that stops ends the release
	// where it stands, and the next boot ends its record and starts the claims it had stopped.
	ctx      context.Context
	project  string
	mark     *projectRelease
	store    projectReleaseStore
	machines *supervisor
	// pods is the runtime's part a release needs, nil on a runtime that runs no pod (tmux),
	// which refuses the release (api.ErrProjectReleaseRunsNoPod).
	pods runtime.ProjectPods
	// enqueue writes the release's asks as outbox rows (supervision.enqueue); nil in a deployment
	// with no outbox, where no role is asked.
	enqueue func(ctx context.Context, issue string, payloads ...record.OutboxPayload) error
	// armBackstops arms the backstop an idle release this release took over would have armed at
	// its teardown (outbox.armBackstops); nil with no workflow, which arms none.
	armBackstops func(ctx context.Context, issue string) error
	// defaultBound is pod_update_grace_minutes, the bound of a release that names none, and
	// stopTimeout is worker_stop_timeout_seconds, which bounds the wait for a stop to be stored
	// after the later of the stop and the bound (stopDeadline).
	defaultBound time.Duration
	stopTimeout  time.Duration
	reread, poll time.Duration
	now          func() time.Time
	log          *slog.Logger

	mu  sync.Mutex
	run *releaseRun
}

// newProjectReleaser is the release of the daemon serve builds: over supervision's machines and
// runtime, the store, and the workflow's outbox where the daemon runs one. A runtime that runs no
// pod (tmux) gives it no pods, and every start it is asked is refused (api.ErrProjectReleaseRunsNoPod).
func newProjectReleaser(s *supervision, st *store.Store, w *workflowRuntime) *projectReleaser {
	r := &projectReleaser{
		ctx: s.supervisor.ctx, project: s.plan.project, mark: s.plan.release, store: st, machines: s.supervisor,
		// The workflow set supervision's enqueue as it attached, which the boot does before it serves.
		enqueue:      s.enqueue,
		defaultBound: s.cfg.PodUpdateGrace, stopTimeout: s.cfg.WorkerStopTimeout,
		reread: projectReleaseReread, poll: projectReleasePoll, now: time.Now, log: s.log,
	}
	if pods, ok := s.runtime.(runtime.ProjectPods); ok {
		r.pods = pods
	}
	if w != nil && w.outbox != nil {
		runner := w.outbox
		r.armBackstops = func(ctx context.Context, key string) error {
			issue, err := runner.issue(ctx, key)
			if err != nil {
				return err
			}
			return runner.armBackstops(ctx, issue)
		}
	}
	return r
}

// releaseRun is the release of this process, once its record committed.
type releaseRun struct {
	id    int64
	bound time.Time
	// stop is the stop set StartProjectRelease read in the transaction that recorded the release:
	// that set, and no later reading, is the set the release stops.
	stop     []store.ProjectReleaseClaim
	finished atomic.Bool

	mu     sync.Mutex
	result api.ProjectReleaseResult
	// asked is each claim the release asked, and where its answer is recorded.
	asked map[claim.Token]releaseAsk
	// held is each claim the release asked an operator's suspension of, in a pod it left (an
	// issue pod or the controller's), whose storage the release waits for before it finishes, and
	// heldRefused where the machines' refusals of those suspensions are written.
	held        []claim.Token
	heldRefused *refusals
	// detail is why a claim the release stopped with no pod of the project was not stored
	// suspended in time (stopDeadline), written on the claim's line at the end.
	detail map[claim.Token]string

	// recording serializes the writes of the result, so a later write is never overtaken by an
	// earlier snapshot.
	recording sync.Mutex
}

// releaseAsk is where one asked claim's answer goes: its pod's line and its place among the line's
// asked roles, and the pod's nudge, which wakes the pod's wait at once.
type releaseAsk struct {
	line, ask int
	nudge     chan struct{}
}

// podClass is how a release takes an issue pod (classifyReleasePod).
type podClass int

const (
	releaseReads podClass = iota
	releaseUnread
	releaseLeaves
)

// releasePod is one issue pod the release takes: its line of the result, its Sandbox, the claims
// of the stop set in it, how it is taken, and the nudge an answer to its ask sends.
type releasePod struct {
	line    int
	sandbox runtime.ProjectSandbox
	stop    []store.ProjectReleaseClaim
	class   podClass
	why     string
	nudge   chan struct{}
}

// releaseRead is one read of a pod's workspace: whether a role process was there to read it, what
// it found, and the shim's refusal.
type releaseRead struct {
	read    bool
	work    runtime.UnpushedWork
	refusal string
}

// Start is api.ProjectRelease's start: the release's record, or the record this process's release
// already holds, or a dry run's report.
func (r *projectReleaser) Start(ctx context.Context, bound time.Duration, dryRun bool) (api.ProjectReleaseResponse, bool, error) {
	if r.pods == nil {
		return api.ProjectReleaseResponse{}, false, api.ErrProjectReleaseRunsNoPod
	}
	if bound <= 0 {
		bound = r.defaultBound
	}
	r.mu.Lock()
	if r.run != nil {
		r.mu.Unlock()
		view, _, err := r.Read(ctx)
		return view, false, err
	}
	if dryRun {
		r.mu.Unlock()
		view, err := r.dryRun(ctx, bound)
		return view, false, err
	}
	defer r.mu.Unlock()
	// The mark is set before the record commits, so no launch slips between the two; a mark
	// already held is a release this process ran, which a run would have recorded above.
	if !r.mark.mark() {
		return api.ProjectReleaseResponse{}, false, errors.New("the project release's mark is already held, with no release of this process recorded")
	}
	startedAt := r.now().UTC()
	release, takenOver, stop, err := r.store.StartProjectRelease(ctx, r.project, startedAt, startedAt.Add(bound))
	if err != nil {
		r.mark.unmark()
		return api.ProjectReleaseResponse{}, false, err
	}
	r.mark.recorded(release.ID)
	run := &releaseRun{id: release.ID, bound: release.BoundAt, stop: stop, asked: map[claim.Token]releaseAsk{},
		heldRefused: newRefusals()}
	r.run = run
	r.log.Info("project release: recorded; the daemon starts nothing of the project until its next boot",
		"release", release.ID, "bound", bound.String(), "boundAt", release.BoundAt.Format(time.RFC3339),
		"claims", len(stop), "takenOver", len(takenOver))
	r.armTakenOver(takenOver)
	go r.release(run)
	view, err := api.ProjectReleaseResponseOf(release)
	return view, true, err
}

// armTakenOver arms, once the take-over has committed, the backstop each idle release the project
// release took over would have armed at its teardown, for the claims it had already stopped:
// those claims stay `released` and wait for their role's wake, which comes after the next boot. A
// release that had stopped no claim — the release of a pod no role can use never stops one — arms
// nothing.
func (r *projectReleaser) armTakenOver(takenOver []store.TakenOverIdleRelease) {
	for _, idle := range takenOver {
		if len(idle.Suspended) == 0 {
			continue
		}
		if r.armBackstops == nil {
			r.log.Info("project release: took over an idle release that had stopped claims; no workflow arms its backstop",
				"issue", idle.Issue, "roles", idle.Suspended)
			continue
		}
		if err := r.armBackstops(r.ctx, idle.Issue); err != nil {
			r.log.Error("project release: arm the backstop of an idle release it took over", "issue", idle.Issue, "error", err)
		}
	}
}

// Read is api.ProjectRelease's read: the newest release of the project as its record holds it.
func (r *projectReleaser) Read(ctx context.Context) (api.ProjectReleaseResponse, bool, error) {
	release, found, err := r.store.ReadProjectRelease(ctx, r.project)
	if err != nil || !found {
		return api.ProjectReleaseResponse{}, found, err
	}
	view, err := api.ProjectReleaseResponseOf(release)
	return view, true, err
}

// current is this process's release while it runs, nil before and once it finished.
func (r *projectReleaser) current() *releaseRun {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.run == nil || r.run.finished.Load() {
		return nil
	}
	return r.run
}

// AskRound is api.ProjectRelease's round of the running release's asks.
func (r *projectReleaser) AskRound() string {
	if run := r.current(); run != nil {
		return projectReleaseRound(run.id)
	}
	return ""
}

// Answer is api.ProjectRelease's record of an asked role's answer. The claim is read from its
// machine's published view, which waits on no decision in flight, and held to the session and the
// generation the answer names, as the machine holds every agent request (fenceRequest). The answer
// is recorded on the release, never on the claim, and wakes its pod's wait at once. An answer that
// comes once its pod's bound has passed is accepted and not recorded: the line keeps the bound,
// which is what decided the pod.
func (r *projectReleaser) Answer(ctx context.Context, token claim.Token, session string, generation uint64, head string) error {
	run := r.current()
	if run == nil {
		return &supervise.RefusedError{Request: "ready_for_update", Reason: "no project release runs now"}
	}
	m, ok := r.machines.Machine(token)
	if !ok {
		return &supervise.RefusedError{Request: "ready_for_update", Reason: "the daemon supervises no such claim"}
	}
	held := m.View()
	if generation != held.Generation {
		return fmt.Errorf("ready_for_update for %s at generation %d, the claim is at %d: %w", token, generation, held.Generation, claim.StaleGeneration)
	}
	if held.Session != "" && session != held.Session {
		return fmt.Errorf("ready_for_update for %s as session %q, the claim resumes %q: %w", token, session, held.Session, claim.SameAgentRefusal)
	}
	run.mu.Lock()
	asked, ok := run.asked[token]
	if !ok {
		run.mu.Unlock()
		return &supervise.RefusedError{State: held.State, Request: "ready_for_update",
			Reason: "the project release " + projectReleaseRound(run.id) + " did not ask this claim; it holds no answer from it"}
	}
	ask := &run.result.Sandboxes[asked.line].Asked[asked.ask]
	if ask.Bound {
		run.mu.Unlock()
		r.log.Info("project release: an asked role answered after its pod's bound; the release keeps the bound",
			"release", run.id, "claim", token, "head", head)
		return nil
	}
	if ask.AnsweredAt == nil {
		at := r.now().UTC()
		ask.AnsweredAt = &at
	}
	run.mu.Unlock()
	r.log.Info("project release: an asked role answered", "release", run.id, "claim", token, "head", head)
	r.record(ctx, run)
	nudge(asked.nudge)
	return nil
}

// release is the goroutine of one release, from its inventory to its finish.
func (r *projectReleaser) release(run *releaseRun) {
	ctx := store.ForConcurrentWorker(r.ctx)
	sandboxes, err := r.pods.ProjectSandboxes(ctx)
	// The stop set's lines are written whatever the listing answered, so a release that stops
	// short at it still names each claim of the stop set, as its record holds it at the end.
	pods, others := r.inventory(run, sandboxes)
	if err != nil {
		r.fail(ctx, run, fmt.Sprintf("list the project's Sandboxes: %v", err))
		return
	}
	r.record(ctx, run)

	// Each pod is read and taken on a goroutine of its own, so its ask goes out as soon as its
	// own read is done and one pod's slow shim delays no other pod's ask.
	var wg sync.WaitGroup
	for _, pod := range pods {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := store.ForConcurrentWorker(ctx)
			var first releaseRead
			if pod.class == releaseReads {
				first = r.read(ctx, pod.sandbox.Issue)
				r.noteRead(run, pod, first)
			}
			r.takePod(ctx, run, pod, first)
		}()
	}
	// A claim of the stop set whose issue has no Sandbox of the project is starting where no pod
	// stands yet (a paced wake parked over no process): it is stopped as the others are, so the
	// boot starts it, and it has no pod to delete. One not stored suspended in time has that
	// written on its line.
	if podless := podlessStops(run.stop, pods); len(podless) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pending := r.stopClaims(store.ForConcurrentWorker(ctx), run, podless, "the project release stops it; it has no pod of the project")
			if len(pending) == 0 {
				return
			}
			r.log.Warn("project release: claims with no pod of the project were not stored suspended in time",
				"release", run.id, "timeout", r.stopTimeout.String(), "bound", run.bound.Format(time.RFC3339), "claims", pendingWords(pending))
			run.mu.Lock()
			if run.detail == nil {
				run.detail = map[claim.Token]string{}
			}
			for token, why := range pending {
				run.detail[token] = r.stopWait() + ": " + why
			}
			run.mu.Unlock()
		}()
	}
	wg.Wait()
	if ctx.Err() != nil {
		return
	}
	r.takeOthers(ctx, run, others)
	r.finish(ctx, run)
}

// inventory writes the result's first lines — one per Sandbox, each waiting, and one per claim of
// the stop set — and classifies each issue pod from its claims as the machines hold them now. The
// other Sandboxes are answered by their lines.
func (r *projectReleaser) inventory(run *releaseRun, sandboxes []runtime.ProjectSandbox) ([]*releasePod, []int) {
	views := r.viewsByIssue()
	stopByIssue := map[string][]store.ProjectReleaseClaim{}
	for _, c := range run.stop {
		stopByIssue[c.Issue] = append(stopByIssue[c.Issue], c)
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	var pods []*releasePod
	var others []int
	for i, s := range sandboxes {
		line := api.ReleasedSandbox{Name: s.Name, Kind: s.Kind, Issue: s.Issue, Outcome: api.ReleaseWaiting,
			OtherImage: s.Kind == runtime.ProjectSandboxIssue && s.PodUID != "" && !s.ConfiguredImage}
		run.result.Sandboxes = append(run.result.Sandboxes, line)
		if s.Kind != runtime.ProjectSandboxIssue {
			others = append(others, i)
			continue
		}
		pod := &releasePod{line: i, sandbox: s, stop: stopByIssue[s.Issue], nudge: make(chan struct{}, 1)}
		pod.class, pod.why = classifyReleasePod(views[s.Issue])
		pods = append(pods, pod)
	}
	for _, c := range run.stop {
		run.result.Claims = append(run.result.Claims, api.ReleasedClaim{Token: c.Token, Issue: c.Issue, Role: c.Role, Session: c.Session})
	}
	return pods, others
}

// classifyReleasePod is how the release takes an issue pod, from its claims: left when an operator
// holds one of them (heldRoles), with the roles named; no read when no role process of it has
// said it is ready — none ever started, every claim failed, retired or suspended, or its role is
// starting again — and read otherwise.
func classifyReleasePod(claims []supervise.ClaimView) (podClass, string) {
	if held := heldRoles(claims); held != "" {
		return releaseLeaves, "an operator suspended its " + held
	}
	if len(runningClaims(claims)) == 0 {
		return releaseUnread, ""
	}
	return releaseReads, ""
}

// heldRoles names the roles of claims an operator holds (Claim.RuleHolds: suspended by an
// operator, or the operator's suspension held for its turn), "" when none.
func heldRoles(claims []supervise.ClaimView) string {
	var held []string
	for _, c := range claims {
		if c.RuleHolds() {
			held = append(held, string(c.Role))
		}
	}
	return strings.Join(held, ", ")
}

// runningClaims is every claim of claims whose role process runs and has said it is ready, the
// roles a release reads through and asks.
func runningClaims(claims []supervise.ClaimView) []supervise.ClaimView {
	var running []supervise.ClaimView
	for _, c := range claims {
		if supervise.SaidReady(c.State) && c.Locator != nil {
			running = append(running, c)
		}
	}
	return running
}

// inTurn is whether any of claims is in a turn (workingClaims).
func inTurn(claims []supervise.ClaimView) bool {
	return slices.ContainsFunc(claims, func(c supervise.ClaimView) bool { return c.State == supervise.StateWorking })
}

// viewsByIssue is every claim the daemon supervises on an issue, as its machine last published
// it, by issue and in token order: the inventory's one reading of every pod's claims.
func (r *projectReleaser) viewsByIssue() map[string][]supervise.ClaimView {
	byIssue := map[string][]supervise.ClaimView{}
	for _, m := range r.machines.supervised() {
		v := m.View()
		if v.Issue != "" {
			byIssue[v.Issue] = append(byIssue[v.Issue], v)
		}
	}
	for _, claims := range byIssue {
		slices.SortFunc(claims, byViewToken)
	}
	return byIssue
}

// issueViews is every claim the daemon supervises on issue, as its machine last published it, in
// token order, read now. It keeps only issue's claims of the machines' lock-free snapshot, as
// supervisor.claimInTurn does, since it runs at each pod's re-read and inside the runtime's pod
// lock (stopAndDelete's authorize).
func (r *projectReleaser) issueViews(issue string) []supervise.ClaimView {
	if issue == "" {
		return nil
	}
	var views []supervise.ClaimView
	for _, m := range r.machines.supervised() {
		if v := m.View(); v.Issue == issue {
			views = append(views, v)
		}
	}
	slices.SortFunc(views, byViewToken)
	return views
}

// byViewToken orders claim views by token.
func byViewToken(a, b supervise.ClaimView) int {
	return strings.Compare(string(a.Token), string(b.Token))
}

// readAll reads every read pod at once and answers each pod's read by its place in pods: the dry
// run's reads, which ask nothing after them. The release reads each pod on that pod's own
// goroutine (release).
func (r *projectReleaser) readAll(ctx context.Context, run *releaseRun, pods []*releasePod) []releaseRead {
	reads := make([]releaseRead, len(pods))
	var wg sync.WaitGroup
	for i, pod := range pods {
		if pod.class != releaseReads {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			reads[i] = r.read(store.ForConcurrentWorker(ctx), pod.sandbox.Issue)
			r.noteRead(run, pod, reads[i])
		}()
	}
	wg.Wait()
	return reads
}

// read reads issue's workspace through one running role's shim, the first by token. A pod where
// no role process runs now is unread; a shim that refused, and a read that failed, are a refusal.
func (r *projectReleaser) read(ctx context.Context, issue string) releaseRead {
	running := runningClaims(r.issueViews(issue))
	if len(running) == 0 {
		return releaseRead{}
	}
	work, err := r.pods.ReadUnpushed(ctx, issue, running[0].Role)
	if err != nil {
		return releaseRead{read: true, refusal: err.Error()}
	}
	return releaseRead{read: true, work: work}
}

// noteRead writes what read found on pod's line, as the line reports it at the end.
func (r *projectReleaser) noteRead(run *releaseRun, pod *releasePod, read releaseRead) {
	run.mu.Lock()
	defer run.mu.Unlock()
	writeRead(&run.result.Sandboxes[pod.line], read)
}

// writeRead sets line's count, flag and refusal from read, and clears what an earlier read left.
// Unpushed is decided by the count of commits alone: a role that pushed its working-copy commit
// itself reads no commit and a changed working copy, which is nothing unpushed, and the flag is
// reported as what the working copy held at that read.
func writeRead(line *api.ReleasedSandbox, read releaseRead) {
	line.Commits, line.WorkingCopyChanged, line.Refusal = nil, nil, ""
	if !read.read {
		return
	}
	if read.refusal != "" {
		line.Refusal = read.refusal
		return
	}
	commits, changed := read.work.Commits, read.work.WorkingCopyChanged
	line.Commits, line.WorkingCopyChanged = &commits, &changed
}

// asks is whether read, with claims as they stand, is a pod the release asks: its workspace holds
// unpushed work, its shim refused, or a role of it is in a turn.
func asks(read releaseRead, claims []supervise.ClaimView) bool {
	return read.read && (read.work.Commits > 0 || read.refusal != "" || inTurn(claims))
}

// takePod takes one issue pod to its end. A pod left from the start has its claims held for an
// operator and stays. A pod with no read is deleted unread. A read pod that the first read asks
// for is asked and waits; one that it does not is read again just before its stop, and a turn, a
// commit or a refusal found then makes it asked, since a turn can start after the first read
// without the daemon (an Envoy message, a background job's result). Then its last read, its stop
// and its delete.
func (r *projectReleaser) takePod(ctx context.Context, run *releaseRun, pod *releasePod, first releaseRead) {
	switch pod.class {
	case releaseLeaves:
		r.leave(ctx, run, pod, pod.why)
		return
	case releaseUnread:
		r.stopAndDelete(ctx, run, pod, releaseRead{})
		return
	}
	asked := false
	if asks(first, r.issueViews(pod.sandbox.Issue)) {
		asked = r.ask(ctx, run, pod)
	}
	if asked {
		r.wait(ctx, run, pod)
	}
	last := r.read(ctx, pod.sandbox.Issue)
	if !asked && asks(last, r.issueViews(pod.sandbox.Issue)) && r.ask(ctx, run, pod) {
		r.noteRead(run, pod, last)
		r.wait(ctx, run, pod)
		last = r.read(ctx, pod.sandbox.Issue)
	}
	if ctx.Err() != nil {
		return
	}
	r.stopAndDelete(ctx, run, pod, last)
}

// ask sends one ask to each running role of pod, on the role's own Envoy topic (record.UpdateAsk
// under the release's round, which the outbox publishes without the claim-round read), and
// records each asked role on the pod's line before the rows are written, so an answer that comes
// at once finds it. It answers whether any role was asked: a deployment with no outbox asks none,
// and the pod then waits for no answer.
func (r *projectReleaser) ask(ctx context.Context, run *releaseRun, pod *releasePod) bool {
	running := runningClaims(r.issueViews(pod.sandbox.Issue))
	if len(running) == 0 {
		return false
	}
	if r.enqueue == nil {
		r.log.Info("project release: this deployment has no outbox, so no role of the pod is asked",
			"release", run.id, "sandbox", pod.sandbox.Name)
		return false
	}
	round := projectReleaseRound(run.id)
	text := supervise.ReleaseTask(round, pod.sandbox.Issue, run.bound)
	rows := make([]record.OutboxPayload, 0, len(running))
	run.mu.Lock()
	line := &run.result.Sandboxes[pod.line]
	for _, c := range running {
		if _, already := run.asked[c.Token]; already {
			continue
		}
		run.asked[c.Token] = releaseAsk{line: pod.line, ask: len(line.Asked), nudge: pod.nudge}
		line.Asked = append(line.Asked, api.ReleasedAsk{Role: c.Role})
		rows = append(rows, record.UpdateAsk{Role: c.Role, Round: round, Pod: pod.sandbox.PodUID, Text: text})
	}
	run.mu.Unlock()
	if len(rows) == 0 {
		return true
	}
	if err := r.enqueue(ctx, pod.sandbox.Issue, rows...); err != nil {
		r.log.Error("project release: enqueue the ask to the pod's roles; the pod waits to its bound",
			"release", run.id, "sandbox", pod.sandbox.Name, "error", err)
	} else {
		r.log.Info("project release: asked each running role of the pod to push and end its turn",
			"release", run.id, "sandbox", pod.sandbox.Name, "roles", len(rows), "bound", run.bound.Format(time.RFC3339))
	}
	r.record(ctx, run)
	return true
}

// wait holds an asked pod until each asked role answered and no role of the pod is in a turn, or
// until the bound, on a wall-clock timer, so a test whose machines run on a clock that never fires
// still sees the bound: the pod is read again every reread and at once on an answer. At the bound
// each role that has not answered is recorded as the bound.
func (r *projectReleaser) wait(ctx context.Context, run *releaseRun, pod *releasePod) {
	bound := time.NewTimer(time.Until(run.bound))
	defer bound.Stop()
	reread := time.NewTicker(r.reread)
	defer reread.Stop()
	for {
		if run.answeredAll(pod) && !inTurn(r.issueViews(pod.sandbox.Issue)) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-bound.C:
			run.passBound(pod)
			r.record(ctx, run)
			return
		case <-reread.C:
		case <-pod.nudge:
		}
	}
}

// answeredAll is whether every role asked on pod's line has answered.
func (run *releaseRun) answeredAll(pod *releasePod) bool {
	run.mu.Lock()
	defer run.mu.Unlock()
	for _, asked := range run.result.Sandboxes[pod.line].Asked {
		if asked.AnsweredAt == nil {
			return false
		}
	}
	return true
}

// passBound records the bound on each asked role of pod's line that has not answered.
func (run *releaseRun) passBound(pod *releasePod) {
	run.mu.Lock()
	defer run.mu.Unlock()
	line := &run.result.Sandboxes[pod.line]
	for i := range line.Asked {
		if line.Asked[i].AnsweredAt == nil {
			line.Asked[i].Bound = true
		}
	}
}

// stopAndDelete ends one issue pod after its last read: each claim of the stop set in it is asked
// a project release's suspension, bounded by the release's bound, and the release waits until
// each is stored suspended, so no deletion of the pod reads as a role's death. A claim that failed
// or retired meanwhile holds no process and is settled as it stands (stopSettled). A claim stored
// suspended under another source is asked once more, so an idle release's hold already in flight
// is taken over by rank and the boot starts it. Then one of two ends. A pod a claim of which an
// operator suspended meanwhile, or whose claims were not all stored settled by the stop's deadline
// (stopDeadline), is left, each of its claims held for an operator so that no boot starts it.
// Otherwise the pod is deleted (IssueSuspender.SuspendIssue), with an authorize the runtime calls
// under its pod lock that holds only while the release runs and no claim of the pod is an
// operator's, and whose false answer leaves the pod as well.
func (r *projectReleaser) stopAndDelete(ctx context.Context, run *releaseRun, pod *releasePod, last releaseRead) {
	tokens := stopTokens(pod.stop)
	pending := r.stopClaims(ctx, run, tokens, "the project release stops it to delete its pod")
	if ctx.Err() != nil {
		return
	}
	for _, token := range tokens {
		if _, waiting := pending[token]; waiting {
			continue
		}
		m, ok := r.machines.Machine(token)
		if !ok {
			continue
		}
		if v := m.View(); v.State == supervise.StateSuspended && v.SuspendedBy != supervise.SuspendedByProjectRelease &&
			v.SuspendedBy != supervise.SuspendedByOperator {
			if err := m.Handle(ctx, supervise.RequestSuspend{Claim: token, By: supervise.SuspendedByProjectRelease,
				Reason: "the project release takes over its suspension"}); err != nil {
				r.log.Warn("project release: take over a claim's suspension", "claim", token, "error", err)
			}
		}
	}
	if held := heldRoles(r.issueViews(pod.sandbox.Issue)); held != "" {
		r.leave(ctx, run, pod, "an operator suspended its "+held+" during the release")
		return
	}
	if len(pending) > 0 {
		r.leave(ctx, run, pod, "its claims were "+r.stopWait()+": "+pendingWords(pending))
		return
	}
	authorized := false
	err := r.pods.SuspendIssue(ctx, pod.sandbox.Issue, pod.sandbox.Tree, func(context.Context) (bool, error) {
		authorized = ctx.Err() == nil && !run.finished.Load() && heldRoles(r.issueViews(pod.sandbox.Issue)) == ""
		return authorized, nil
	})
	switch {
	case err != nil:
		r.leave(ctx, run, pod, "its delete failed: "+err.Error())
		return
	case !authorized:
		r.leave(ctx, run, pod, "an operator suspended one of its claims as its delete began")
		return
	}
	run.mu.Lock()
	line := &run.result.Sandboxes[pod.line]
	writeRead(line, last)
	line.Outcome = deletedOutcome(last)
	run.mu.Unlock()
	r.log.Info("project release: deleted an issue pod", "release", run.id, "sandbox", pod.sandbox.Name, "outcome", line.Outcome)
	r.record(ctx, run)
}

// deletedOutcome is the outcome of a pod deleted after read.
func deletedOutcome(read releaseRead) string {
	switch {
	case !read.read:
		return api.ReleaseDeletedUnread
	case read.refusal != "":
		return api.ReleaseDeletedReadRefused
	case read.work.Commits > 0:
		return api.ReleaseDeletedLost
	}
	return api.ReleaseDeletedClean
}

// leave is a pod left with its disk, for why: each claim of the stop set in it is held for an
// operator, so no boot starts any of them (R6, R8), and every claim of the pod is taken out of its
// update round (hold). The release waits for each hold to be stored before it finishes, and a
// refusal of one is written on that claim's line.
func (r *projectReleaser) leave(ctx context.Context, run *releaseRun, pod *releasePod, why string) {
	held := stopTokens(pod.stop)
	var others []claim.Token
	for _, v := range r.issueViews(pod.sandbox.Issue) {
		if !slices.Contains(held, v.Token) {
			others = append(others, v.Token)
		}
	}
	r.hold(ctx, run, held, others, "the project release leaves its pod: "+why)
	run.mu.Lock()
	line := &run.result.Sandboxes[pod.line]
	line.Outcome, line.Reason = api.ReleaseLeft, why
	run.mu.Unlock()
	r.log.Warn("project release: left an issue pod and its disk", "release", run.id, "sandbox", pod.sandbox.Name, "reason", why)
	r.record(ctx, run)
}

// hold asks each of held an operator's suspension for reason, which takes over a project
// release's by rank and holds one still in flight, so no boot starts the claim, and adds it to the
// claims finish waits for (run.held), with a refusal written to run.heldRefused. Then each claim of
// held and of others that records an update round is taken out of it, as the round's end takes it
// (supervise.PodUpdateEnded): the release leaves no round open on any claim (R6), and a round left
// on a claim of a pod the release left could replace that pod, and the disk the release left it
// to keep, once an operator's resume lifts the last hold (podupdate.go's heldByOperator). The
// round goes after the suspension, so the task the round held back is handed to no role the
// release is stopping. Each claim's requests run on a goroutine of its own, under a store mark of
// its own, and hold returns once each was answered.
func (r *projectReleaser) hold(ctx context.Context, run *releaseRun, held, others []claim.Token, reason string) {
	var wg sync.WaitGroup
	for i, token := range slices.Concat(held, others) {
		holds := i < len(held)
		m, ok := r.machines.Machine(token)
		if !ok {
			if holds {
				run.heldRefused.set(token, "the daemon supervises no machine for it")
			}
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := store.ForConcurrentWorker(ctx)
			if holds {
				err := m.Handle(ctx, supervise.RequestSuspend{Claim: token, By: supervise.SuspendedByOperator, Reason: reason})
				if err != nil && !errors.Is(err, supervise.ErrSuspendHeld) {
					run.heldRefused.set(token, err.Error())
				}
			}
			if round := m.View().UpdateRound; round != "" {
				if err := m.Handle(ctx, supervise.PodUpdateEnded{Claim: token, Round: round}); err != nil {
					r.log.Warn("project release: take a claim of a Sandbox it leaves out of its update round",
						"release", run.id, "claim", token, "round", round, "error", err)
				}
			}
		}()
	}
	wg.Wait()
	run.mu.Lock()
	run.held = append(run.held, held...)
	run.mu.Unlock()
}

// stopClaims asks each of tokens the project release's suspension, bounded by the release's bound,
// and waits until each is stored settled (awaitSettled) or the stop's deadline has passed, on the
// wall clock (stopDeadline). It answers each claim not settled by then with what is known of why:
// the machine's refusal, and how the claim stands in memory or in the store. A suspension held for
// a turn is the machine's to end, at the turn's end or at the bound, so a held one is waited for
// like any other.
func (r *projectReleaser) stopClaims(ctx context.Context, run *releaseRun, tokens []claim.Token, reason string) map[claim.Token]string {
	refused := r.suspend(ctx, tokens, supervise.RequestSuspend{By: supervise.SuspendedByProjectRelease, Deadline: run.bound, Reason: reason},
		newRefusals())
	return r.awaitSettled(ctx, tokens, false, r.stopDeadline(run), refused)
}

// stopDeadline is when the release stops waiting for a stop it asks now to be stored: the stop
// timeout after the later of now and the bound. The machine holds a suspension that reaches a
// claim in a turn until the turn ends or until the deadline the release names, its bound
// (holdSuspension), so a role or the controller whose turn is still allowed to run is waited for,
// and the runtime's stop of the process takes up to the stop timeout after that.
func (r *projectReleaser) stopDeadline(run *releaseRun) time.Time {
	from := time.Now()
	if run.bound.After(from) {
		from = run.bound
	}
	return from.Add(r.stopTimeout)
}

// stopWait is how a line says that the wait for a stop ran out (stopDeadline).
func (r *projectReleaser) stopWait() string {
	return "not stored suspended within the stop timeout of " + r.stopTimeout.String() + " after the later of its stop and the bound"
}

// refusals is where the machines' refusals of the suspensions a release asked are written as they
// come, by claim.
type refusals struct {
	mu  sync.Mutex
	why map[claim.Token]string
}

func newRefusals() *refusals { return &refusals{why: map[claim.Token]string{}} }

func (f *refusals) set(token claim.Token, why string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.why[token] = why
}

func (f *refusals) of(token claim.Token) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	why, ok := f.why[token]
	return why, ok
}

// suspend asks each of tokens request's suspension, each on a goroutine of its own under a store
// mark of its own, and writes each refusal to refused as it comes: a token the daemon supervises
// no machine for at once, and a machine's refusal once it answers. A suspension already held for a
// turn (supervise.ErrSuspendHeld) is no refusal, since the machine ends it itself.
func (r *projectReleaser) suspend(ctx context.Context, tokens []claim.Token, request supervise.RequestSuspend, refused *refusals) *refusals {
	for _, token := range tokens {
		m, ok := r.machines.Machine(token)
		if !ok {
			refused.set(token, "the daemon supervises no machine for it")
			continue
		}
		request := request
		request.Claim = token
		go func() {
			if err := m.Handle(store.ForConcurrentWorker(ctx), request); err != nil && !errors.Is(err, supervise.ErrSuspendHeld) {
				refused.set(token, err.Error())
			}
		}()
	}
	return refused
}

// awaitSettled waits, without asking anything, until each of tokens the daemon supervises is
// settled as a stop byOperator asks (stopSettled) both as its machine holds it and as the store
// holds it, or until the wall clock passes until. The stored row is what decides, since a pod is
// deleted only once each of its claims is stored suspended: a suspension whose write failed
// leaves the machine's claim suspended and the row as it was (supervise's owed write). The store
// is read once every machine holds its claim settled, and at the last look, so a claim still
// stopping costs no read. It answers each claim not settled by then with why — how its machine
// holds it, and how the store holds it where the machine holds it settled, after the machine's
// refusal where refused holds one — and each token with no machine with its refusal.
func (r *projectReleaser) awaitSettled(ctx context.Context, tokens []claim.Token, byOperator bool, until time.Time,
	refused *refusals) map[claim.Token]string {
	for {
		last := !time.Now().Before(until) || ctx.Err() != nil
		pending := map[claim.Token]string{}
		var settled []supervise.Claim
		var unsupervised []claim.Token
		for _, token := range tokens {
			m, ok := r.machines.Machine(token)
			if !ok {
				unsupervised = append(unsupervised, token)
				continue
			}
			if v := m.View().Claim; stopSettled(v, byOperator) {
				settled = append(settled, v)
			} else {
				pending[token] = "it stands " + claimStanding(v)
			}
		}
		if len(pending) == 0 || last {
			for _, c := range settled {
				if why, stored := r.storedSettled(ctx, c, byOperator); !stored {
					pending[c.Token] = why
				}
			}
		}
		if len(pending) > 0 && !last {
			select {
			case <-ctx.Done():
			case <-time.After(r.poll):
			}
			continue
		}
		for token, stands := range pending {
			if why, ok := refused.of(token); ok {
				pending[token] = why + "; " + stands
			}
		}
		for _, token := range unsupervised {
			if why, ok := refused.of(token); ok {
				pending[token] = why
			}
		}
		return pending
	}
}

// storedSettled is whether c, which its machine holds settled as a stop byOperator asks, is stored
// settled too (stopSettled), and how the two stand when it is not.
func (r *projectReleaser) storedSettled(ctx context.Context, c supervise.Claim, byOperator bool) (string, bool) {
	stored, found, err := r.store.ClaimByToken(ctx, c.Token)
	stands := "it stands " + claimStanding(c)
	switch {
	case err != nil:
		return stands + ", and its stored record could not be read: " + err.Error(), false
	case !found:
		return stands + ", and the store holds no record of it", false
	case !stopSettled(stored, byOperator):
		return stands + ", and the store holds it " + claimStanding(stored), false
	}
	return "", true
}

// claimStanding is how c stands, as a line names it: its state, and the source of its suspension
// when it is suspended.
func claimStanding(c supervise.Claim) string {
	if c.State == supervise.StateSuspended && c.SuspendedBy != "" {
		return string(c.State) + " by " + string(c.SuspendedBy)
	}
	return string(c.State)
}

// stopSettled is whether c stands as a stop asked leaves it, read of the machine's claim and of the
// stored row alike (awaitSettled). Suspended is settled: by an operator for an operator's hold,
// and by any source for the release's own, whose take-over of another source is asked after
// (stopAndDelete). A claim that failed or retired is settled too: no stop changes it, it holds no
// process, so no deletion of its pod reads as its death, and the boot starts neither, so its line
// names how it ended.
func stopSettled(c supervise.Claim, byOperator bool) bool {
	switch c.State {
	case supervise.StateFailed, supervise.StateRetired:
		return true
	case supervise.StateSuspended:
		return !byOperator || c.SuspendedBy == supervise.SuspendedByOperator
	}
	return false
}

// takeOthers ends every Sandbox that is no issue's, once every issue pod has ended: the
// controller's claim is stopped and waited for as the issue pods' claims were, so a controller in
// a turn is waited for until the turn's end or the bound (stopDeadline), and its pod deleted
// through the runtime's release of the claim (ReleaseProjectController); each repository cache and
// image probe is deleted through the runtime's fenced delete. A delete the volume guard refuses,
// or one the fence finds written since it was decided, leaves that Sandbox, named with the
// refusal, as does a Sandbox of a kind the release does not know. A controller's pod left — its
// claim not stored suspended in time, or its delete refused — has the controller's claim held for
// an operator, as a left issue pod's claims are (hold), so no boot starts the controller while
// its pod stands.
func (r *projectReleaser) takeOthers(ctx context.Context, run *releaseRun, others []int) {
	var controllers []claim.Token
	for _, c := range run.stop {
		if claim.IsController(c.Role, c.Issue, c.Tree) {
			controllers = append(controllers, c.Token)
		}
	}
	pending := r.stopClaims(ctx, run, controllers, "the project release stops it to delete its pod")
	controller := claim.ControllerToken(r.project)
	for _, i := range others {
		run.mu.Lock()
		line := run.result.Sandboxes[i]
		run.mu.Unlock()
		var err error
		switch line.Kind {
		case runtime.ProjectSandboxController:
			if why, waiting := pending[controller]; waiting {
				err = fmt.Errorf("the controller's claim was %s: %s", r.stopWait(), why)
			} else {
				err = r.pods.ReleaseProjectController(ctx, controller)
			}
			if err != nil {
				r.hold(ctx, run, controllers, nil, "the project release leaves the controller's pod: "+err.Error())
			}
		case runtime.ProjectSandboxCache, runtime.ProjectSandboxProbe:
			err = r.pods.DeleteProjectSandbox(ctx, line.Name)
		default:
			err = errors.New(releaseUnknownKind)
		}
		run.mu.Lock()
		at := &run.result.Sandboxes[i]
		if err != nil {
			at.Outcome, at.Reason = api.ReleaseLeft, err.Error()
		} else {
			at.Outcome = api.ReleaseDeleted
		}
		run.mu.Unlock()
		if err != nil {
			r.log.Warn("project release: left a Sandbox", "release", run.id, "sandbox", line.Name, "kind", line.Kind, "reason", err)
		} else {
			r.log.Info("project release: deleted a Sandbox", "release", run.id, "sandbox", line.Name, "kind", line.Kind)
		}
		r.record(ctx, run)
	}
}

// finish waits for each claim the release held for an operator in a pod it left (run.held) to be
// stored suspended, on a wall-clock bound of twice the stop timeout from the start of this wait
// (the hold's own deadline, then the runtime's stop grace before the claim is stored), writes each
// claim's line from its record as it stands, and finishes the release. An operator's hold lives in
// memory only, so a release reported finished leaves no left pod's claim stored working for a
// restart to adopt.
func (r *projectReleaser) finish(ctx context.Context, run *releaseRun) {
	run.mu.Lock()
	held := slices.Clone(run.held)
	run.mu.Unlock()
	within := 2 * r.stopTimeout
	pending := r.awaitSettled(ctx, held, true, time.Now().Add(within), run.heldRefused)
	if ctx.Err() != nil {
		return
	}
	if len(pending) > 0 {
		for token, why := range pending {
			pending[token] = fmt.Sprintf("not stored suspended by %s within %s: %s", supervise.SuspendedByOperator, within, why)
		}
		r.log.Warn("project release: claims it held for an operator in a left pod were not stored suspended in time",
			"release", run.id, "claims", pendingWords(pending))
	}
	r.writeClaims(ctx, run, pending)
	// The finish's result is read under the lock every record takes, so an answer recorded just
	// before it is in it and none after it is written over it.
	run.recording.Lock()
	defer run.recording.Unlock()
	run.finished.Store(true)
	if err := r.store.FinishProjectRelease(ctx, run.id, run.snapshot(), r.now().UTC()); err != nil {
		r.log.Error("project release: record the release finished", "release", run.id, "error", err)
		return
	}
	r.log.Info("project release: finished; the daemon's next boot starts each claim it stopped", "release", run.id)
}

// writeClaims writes each stop-set claim's line from the claim's own record as the store holds it
// now: how it ended is its suspension's source when it is suspended, and its state otherwise, and
// only a claim held `project_release` is one the next boot starts. A claim the store holds no
// record of is api.ReleasedClaimUnrecorded. pending, and the run's detail, are why a claim was not
// stored suspended in time. A store that cannot be read leaves each line's end unwritten, saying
// so, rather than naming one the release did not read.
func (r *projectReleaser) writeClaims(ctx context.Context, run *releaseRun, pending map[claim.Token]string) {
	claims, err := r.store.Claims(ctx)
	if err != nil {
		r.log.Error("project release: read the claims it stopped", "release", run.id, "error", err)
	}
	stored := map[claim.Token]supervise.Claim{}
	for _, c := range claims {
		stored[c.Token] = c
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	for i := range run.result.Claims {
		line := &run.result.Claims[i]
		if why, ok := run.detail[line.Token]; ok {
			line.Detail = why
		}
		if why, ok := pending[line.Token]; ok {
			line.Detail = why
		}
		if err != nil {
			line.Ended, line.BootStarts = "", false
			line.Detail = "how it stands is not known: the release could not read the claims at its end: " + err.Error()
			continue
		}
		c, ok := stored[line.Token]
		if !ok {
			line.Ended, line.BootStarts = api.ReleasedClaimUnrecorded, false
			continue
		}
		line.Ended = string(c.State)
		if c.State == supervise.StateSuspended {
			line.Ended = string(c.SuspendedBy)
		}
		line.BootStarts = c.ProjectReleased()
	}
}

// fail records a release that could not go on with what stopped it, and finishes it: nothing more
// of it acts, and the boot ends it as it ends any release.
func (r *projectReleaser) fail(ctx context.Context, run *releaseRun, why string) {
	r.log.Error("project release: stopped short of its acts", "release", run.id, "reason", why)
	run.mu.Lock()
	run.result.Failure = why
	run.mu.Unlock()
	r.finish(ctx, run)
}

// record writes the result as it stands on the release's record. A write that fails is logged;
// the next one carries everything it would have.
func (r *projectReleaser) record(ctx context.Context, run *releaseRun) {
	run.recording.Lock()
	defer run.recording.Unlock()
	if run.finished.Load() {
		return
	}
	if err := r.store.RecordProjectRelease(ctx, run.id, run.snapshot()); err != nil && ctx.Err() == nil {
		r.log.Error("project release: record its result", "release", run.id, "error", err)
	}
}

// snapshot is the result as it stands, as the record keeps it.
func (run *releaseRun) snapshot() json.RawMessage {
	run.mu.Lock()
	defer run.mu.Unlock()
	encoded, err := json.Marshal(run.result.Listed())
	if err != nil {
		// Every field of the result is a plain value; it always encodes.
		panic(fmt.Sprintf("encode the project release's result: %v", err))
	}
	return encoded
}

// dryRun is the release's inventory, classification and reads, with no ask, no stop and no
// record: each Sandbox with the outcome a release would give it now, each pod a release would ask
// with the roles it would ask, and each claim a release would stop. The stop set is read from the
// store as the release's own transaction reads it (store.ProjectReleaseStopSet).
func (r *projectReleaser) dryRun(ctx context.Context, bound time.Duration) (api.ProjectReleaseResponse, error) {
	startedAt := r.now().UTC()
	sandboxes, err := r.pods.ProjectSandboxes(ctx)
	if err != nil {
		return api.ProjectReleaseResponse{}, fmt.Errorf("list the project's Sandboxes: %w", err)
	}
	stop, err := r.store.ProjectReleaseStopSet(ctx, r.project)
	if err != nil {
		return api.ProjectReleaseResponse{}, err
	}
	run := &releaseRun{bound: startedAt.Add(bound), stop: stop, asked: map[claim.Token]releaseAsk{}}
	pods, others := r.inventory(run, sandboxes)
	reads := r.readAll(ctx, run, pods)
	left := map[string]bool{}
	for i, pod := range pods {
		line := &run.result.Sandboxes[pod.line]
		switch pod.class {
		case releaseLeaves:
			line.Outcome, line.Reason = api.ReleaseLeft, pod.why
			left[pod.sandbox.Issue] = true
			continue
		case releaseUnread:
			line.Outcome = api.ReleaseDeletedUnread
			continue
		}
		views := r.issueViews(pod.sandbox.Issue)
		if asks(reads[i], views) {
			for _, c := range runningClaims(views) {
				line.Asked = append(line.Asked, api.ReleasedAsk{Role: c.Role})
			}
		}
		line.Outcome = deletedOutcome(reads[i])
	}
	for _, i := range others {
		line := &run.result.Sandboxes[i]
		line.Outcome = api.ReleaseDeleted
		if line.Kind == runtime.ProjectSandboxUnknown {
			line.Outcome, line.Reason = api.ReleaseLeft, releaseUnknownKind
		}
	}
	for i := range run.result.Claims {
		line := &run.result.Claims[i]
		line.Ended, line.BootStarts = string(supervise.SuspendedByProjectRelease), true
		if left[line.Issue] {
			line.Ended, line.BootStarts = string(supervise.SuspendedByOperator), false
		}
	}
	finished := r.now().UTC()
	return api.ProjectReleaseResponse{DryRun: true, StartedAt: startedAt, BoundAt: run.bound, Bound: bound.String(),
		FinishedAt: &finished, Result: run.result.Listed()}, nil
}

// stopTokens is the tokens of claims, in their order.
func stopTokens(claims []store.ProjectReleaseClaim) []claim.Token {
	tokens := make([]claim.Token, len(claims))
	for i, c := range claims {
		tokens[i] = c.Token
	}
	return tokens
}

// podlessStops is every claim of stop on an issue that has no pod among pods, the controller's
// left out, which the release stops after the issue pods.
func podlessStops(stop []store.ProjectReleaseClaim, pods []*releasePod) []claim.Token {
	withPod := map[string]bool{}
	for _, pod := range pods {
		withPod[pod.sandbox.Issue] = true
	}
	var podless []claim.Token
	for _, c := range stop {
		if c.Issue != "" && !withPod[c.Issue] {
			podless = append(podless, c.Token)
		}
	}
	return podless
}

// pendingWords is pending as one sentence, claim by claim in token order.
func pendingWords(pending map[claim.Token]string) string {
	tokens := slices.Sorted(maps.Keys(pending))
	words := make([]string, len(tokens))
	for i, token := range tokens {
		words[i] = string(token) + " (" + pending[token] + ")"
	}
	return strings.Join(words, ", ")
}
