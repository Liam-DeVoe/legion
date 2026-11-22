package daemon

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/appauth"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
	"github.com/sjawhar/legion/daemon/internal/githubrest"
	"github.com/sjawhar/legion/daemon/internal/intake"
	planwords "github.com/sjawhar/legion/daemon/internal/plan"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/verdict"
)

// The kinds of head a write of legion-verdict names in its log line: a legion/<KEY> head of this
// daemon's project, and a head no Legion issue owns.
const (
	headLegion  = "legion"
	headOutside = "outside"
)

// The lines the writer logs, besides each write's own legion_verdict_write: once per pull request
// whose head is another project's issue branch, where no verdict stands, once while the App is in
// token mode, each time a merge-state read goes on after waiting its bound on its issue's write,
// and each write that fails, naming what repairs it.
const (
	otherProjectsHead  = "verdict: the head is another project's issue branch, of whose steps this daemon holds none, so no verdict stands there"
	verdictTokenMode   = "verdict: legion-verdict is not written: the App is in token mode, which mints no verdict token"
	verdictReadWentOn  = "verdict: a merge-state read went on before its issue's write of legion-verdict ended"
	verdictWriteFailed = "verdict: a write of legion-verdict failed; the writers' sweep at boot and every 10 minutes writes it again"
)

// verdictWriter is the state of the daemon's writer of the required check legion-verdict (spec
// R4, R19): the repositories its configuration names under verdict_repositories, the id of the one
// App, by which it tells that App's check runs from every other App's, the heads a fact asked it to
// write on that it has not written yet, and what the merge-state reads of an issue wait on. A fact
// that can change a head's verdict asks for the write on its commit (workflow.Config.WriteVerdict,
// queueVerdictWrite), a merge group's asks for one on the group's commit (queueMergeGroupWrite),
// and one goroutine writes them one at a time (writeVerdicts), so a fact's transaction waits on no
// GitHub call and two writes of one head never both make a run. A write a fact asked for is held in
// memory, and a restart loses one not made: the writers' sweep, at boot and every 10 minutes,
// writes each head whose verdict the record holds and GitHub shows no run of this daemon's on.
type verdictWriter struct {
	entries []config.VerdictRepository
	// appID is the one App's id (github_apps.implement.app_id), 0 where the configuration names
	// none a number reads from, which tells no run as the App's. A rename of the App changes its
	// slug and its bot's login, never its id (spec R16, R19).
	appID int64
	// bound is how long a merge-state read waits on its issue's write (await).
	bound time.Duration

	mu sync.Mutex
	// queue is each head a fact asked for and not yet taken, oldest first, and swept each the
	// writers' sweep asked for (overForeign), which the writer takes only while queue is empty, so
	// a fact's write, which its issue's merge-state reads wait on, waits behind no sweep's; running
	// is how many writes are being made; pending is, by issue, how many of all three are that
	// issue's, which its merge-state reads wait on (await), and ended is closed and made again each
	// time a write ends.
	queue   []verdictTarget
	swept   []verdictTarget
	running int
	pending map[string]int
	ended   chan struct{}
	wake    chan struct{}
	// foreign is, by repository (lowercased) and number, each pull request whose head is another
	// project's issue branch, with that issue, which the daemon has logged once and decides as a
	// head no verdict stands at.
	foreign map[string]string
	// tokenMode logs once that the App is in token mode (appauth.ErrNoVerdictToken).
	tokenMode sync.Once
}

// verdictTarget is one head a write of legion-verdict is asked for: the pull request's repository
// and number, the head, and the issue whose merge-state reads wait on the write ("" for a pull
// request no Legion issue owns). A merge group's write (queueMergeGroupWrite) names the group's
// commit as group, which it writes on, and no head: it decides the pull request at the head that
// stands when the write is made (writeMergeGroup). overForeign is writeDecided's: the writers'
// sweep's ask to put failure over another writer's run or status where no verdict stands. alone is
// the sweep's word that its list of open pull requests shows no other at head, so the write reads
// GitHub for none (atHead).
type verdictTarget struct {
	issue       string
	repository  ghrepo.Repository
	number      int
	head        string
	group       string
	overForeign bool
	alone       bool
}

// newVerdictWriter is the writer of entries, appID being the one App's id as the configuration
// spells it.
func newVerdictWriter(entries []config.VerdictRepository, appID string) *verdictWriter {
	id, err := strconv.ParseInt(appID, 10, 64)
	if err != nil {
		id = 0
	}
	return &verdictWriter{entries: entries, appID: id, bound: issueRead, pending: map[string]int{}, ended: make(chan struct{}),
		wake: make(chan struct{}, 1), foreign: map[string]string{}}
}

// entry is the verdict_repositories entry that names repository, compared without case as GitHub
// compares names, and whether one does. A writer of no configuration names none.
func (v *verdictWriter) entry(repository string) (config.VerdictRepository, bool) {
	if v == nil {
		return config.VerdictRepository{}, false
	}
	for _, entry := range v.entries {
		if strings.EqualFold(entry.Repository.String(), repository) {
			return entry, true
		}
	}
	return config.VerdictRepository{}, false
}

// repositories is every repository the writer writes on, which GitHub intake hears whatever the
// mapping names (heardRepositories) and decodes the outside facts of (intake.ConsumerSpec).
func (v *verdictWriter) repositories() []ghrepo.Repository {
	if v == nil {
		return nil
	}
	repositories := make([]ghrepo.Repository, 0, len(v.entries))
	for _, entry := range v.entries {
		repositories = append(repositories, entry.Repository)
	}
	return repositories
}

// add queues target, a fact's or, where it asks for overForeign, the writers' sweep's, unless the
// same target is queued already and not yet taken, which the one write answers; and wakes the
// writer.
func (v *verdictWriter) add(target verdictTarget) {
	v.mu.Lock()
	defer v.mu.Unlock()
	queue := &v.queue
	if target.overForeign {
		queue = &v.swept
	}
	for _, queued := range *queue {
		if queued == target {
			return
		}
	}
	*queue = append(*queue, target)
	v.pending[target.issue]++
	select {
	case v.wake <- struct{}{}:
	default:
	}
}

// next takes the oldest queued target a fact asked for, or, where none waits, the oldest the
// writers' sweep asked for, and whether there was one.
func (v *verdictWriter) next() (verdictTarget, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	queue := &v.queue
	if len(*queue) == 0 {
		queue = &v.swept
	}
	if len(*queue) == 0 {
		return verdictTarget{}, false
	}
	target := (*queue)[0]
	*queue = (*queue)[1:]
	v.running++
	return target, true
}

// done ends the write of target that next took, and tells every read waiting on its issue.
func (v *verdictWriter) done(target verdictTarget) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.running--
	if v.pending[target.issue]--; v.pending[target.issue] <= 0 {
		delete(v.pending, target.issue)
	}
	close(v.ended)
	v.ended = make(chan struct{})
}

// idle is whether no write is queued or being made.
func (v *verdictWriter) idle() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.queue) == 0 && len(v.swept) == 0 && v.running == 0
}

// await returns true once issue has no write of legion-verdict queued or being made, false once
// the writer's bound passes with one still pending, or ctx's error once ctx ends. The merge-state
// reads of the issue call it before each read (readMergeState): GitHub answers `blocked` until
// the required check exists, so a read made before the write that a verdict asked for on the same
// commit would hold the round on a check about to be written (spec R4). The one writer makes every
// head's writes in turn, so a slow GitHub or a long queue before the issue's write would hold its
// reads with no end: after the bound the read goes on, as it did before there was a writer. A
// runtime with no writer waits on nothing.
func (v *verdictWriter) await(ctx context.Context, issue string) (bool, error) {
	if v == nil {
		return true, nil
	}
	bound := time.NewTimer(v.bound)
	defer bound.Stop()
	for {
		v.mu.Lock()
		pending, ended := v.pending[issue], v.ended
		v.mu.Unlock()
		if pending == 0 {
			return true, nil
		}
		select {
		case <-ended:
		case <-bound.C:
			return false, nil
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
}

// foreignKey names a pull request in the writer's foreign set.
func foreignKey(repository string, number int) string {
	return strings.ToLower(repository) + "#" + strconv.Itoa(number)
}

// otherIssue is the other project's issue whose branch the pull request's head is, "" where the
// writer knows of none.
func (v *verdictWriter) otherIssue(repository string, number int) string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.foreign[foreignKey(repository, number)]
}

// markForeign records that the pull request's head is issue's branch, another project's, and says
// whether this is the first time.
func (v *verdictWriter) markForeign(repository string, number int, issue string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	key := foreignKey(repository, number)
	if v.foreign[key] != "" {
		return false
	}
	v.foreign[key] = issue
	return true
}

// queueVerdictWrite is the engine's Config.WriteVerdict, and the outside facts' own ask: it queues a
// write of legion-verdict on head of pull request number of repo for the writer (writeVerdicts),
// with issue's merge-state reads held until it ends. It is called on a fact's commit, and before the
// same commit asks for a read (workflow's askVerdictWrite), so the read waits for the write. A
// repository no verdict entry names (verdictEntry) asks nothing, since its write would write
// nothing and its issue's reads would wait on it; so does a repository name that does not parse,
// which is logged, and a runtime with no writer.
func (w *workflowRuntime) queueVerdictWrite(issue, repo string, number int, head string) {
	if w.verdicts == nil {
		return
	}
	repository, err := ghrepo.Parse("the repository of a legion-verdict write", repo)
	if err != nil {
		w.log.Warn("verdict: a write of legion-verdict names no repository, so nothing is written", "repository", repo, "pull_request", number, "head", head, "error", err)
		return
	}
	if _, ok := w.verdictEntry(repository); !ok {
		return
	}
	w.verdicts.add(verdictTarget{issue: issue, repository: repository, number: number, head: head})
}

// verdictEntry is the verdict_repositories entry that names repository, under the name a record
// keeps, the one GitHub serves it under now (repositories.serving), or the other name of a
// configured rename (Renames.Partner), and whether one does: an entry written under the name a
// rename moved the repository away from names it under its new name too.
func (w *workflowRuntime) verdictEntry(repository ghrepo.Repository) (config.VerdictRepository, bool) {
	if entry, ok := w.verdicts.entry(repository.String()); ok {
		return entry, true
	}
	if entry, ok := w.verdicts.entry(w.repositories.serving(repository).String()); ok {
		return entry, true
	}
	if partner, renamed := w.renames().Partner(repository.String()); renamed {
		return w.verdicts.entry(partner.String())
	}
	return config.VerdictRepository{}, false
}

// namesVerdictRepository is whether repo, the name a webhook fact carries, is a verdict
// repository under any of its names (verdictEntry). A name that does not parse is none, and so is
// every name for a runtime with no writer.
func (w *workflowRuntime) namesVerdictRepository(repo string) bool {
	repository, err := ghrepo.Parse("the repository of a webhook fact", repo)
	if err != nil || w.verdicts == nil {
		return false
	}
	_, ok := w.verdictEntry(repository)
	return ok
}

// writeVerdicts makes each write queueVerdictWrite queued, one at a time and oldest first, until
// ctx ends. A write that fails - GitHub's error, its rate limit, a token that could not be minted -
// is logged and not made again here: the writers' sweep, at boot and every 10 minutes, writes each
// head whose verdict the record holds and that GitHub shows no run of this daemon's on.
func (w *workflowRuntime) writeVerdicts(ctx context.Context) {
	for {
		target, ok := w.verdicts.next()
		if !ok {
			select {
			case <-w.verdicts.wake:
				continue
			case <-ctx.Done():
				return
			}
		}
		var err error
		if target.group != "" {
			err = w.writeMergeGroup(ctx, target)
		} else {
			err = w.writeVerdictOver(ctx, target)
		}
		if err != nil && ctx.Err() == nil {
			w.log.Warn(verdictWriteFailed, "repository", target.repository.String(), "pull_request", target.number, "head", target.head,
				"group", target.group, "issue", target.issue, "error", err)
		}
		w.verdicts.done(target)
	}
}

// writeVerdictOver writes legion-verdict on target's head, of target's pull request, from the
// verdict the daemon records at that head (verdict.Decide) of that pull request and of every other
// open pull request at the head (atHead): success where it stands, failure where a refusal stands,
// and nothing where neither does, so the head cannot merge (spec R4); with target's overForeign,
// the writers' sweep's ask, failure over another writer's run or status there (writeDecided). It
// writes only on a repository its verdict_repositories names, under the name a record keeps or the
// one GitHub serves it under now (verdictEntry), and on every legion/<KEY> head there, of whatever
// project: another project's issue head holds no verdict here (decideVerdict). It creates its own
// check run or turns each run of the one App to the head's conclusion, the runs its record holds
// among them, and writes its conclusion over each status of the one App its record does not hold
// (spec R19, writeDecided). A success this daemon wrote stands only while the head's verdict does:
// a write a fact asks for on a head with no verdict now turns that success to failure, and writes
// nothing, and reads nothing of GitHub, on a head it wrote no success on.
//
// Once a pass writes a Legion head's success, or turns its run or status to success, the issue's
// merge state is read once more, a ladder's wait later (mergeStateWait): GitHub can answer the read
// the write held (verdictWriter.await) with `blocked` while it has not yet counted the new run, an
// answer that ends the climb, and no other fact asks again, since Envoy leaves the one App's runs
// out of the CI record. A pass that finds the success already written asks for nothing more: the
// pass that wrote it asked already.
func (w *workflowRuntime) writeVerdictOver(ctx context.Context, target verdictTarget) error {
	entry, ok := w.verdictEntry(target.repository)
	if !ok {
		return nil
	}
	decided, err := w.decideVerdict(ctx, entry, target.repository, target.number, target.head)
	if err != nil {
		return err
	}
	written := verdictTarget{repository: target.repository, number: target.number, head: target.head}
	standing := false
	if decided.conclusion == verdict.None && !target.overForeign {
		if standing, err = w.wroteSuccess(ctx, written); err != nil || !standing {
			return err
		}
	}
	if decided.conclusion != verdict.Failure && !target.alone {
		if decided, err = w.atHead(ctx, entry, target.repository, target.number, decided); err != nil {
			return err
		}
	}
	if decided.conclusion == verdict.None && standing {
		decided.conclusion = verdict.Failure
	}
	changed, err := w.writeDecidedChanged(ctx, written, decided, target.overForeign)
	if err != nil {
		return err
	}
	if changed && decided.conclusion == verdict.Success && decided.issue != "" {
		issue := decided.issue
		time.AfterFunc(w.mergeStateWait(), func() {
			if ctx.Err() == nil {
				w.queueMergeState(issue)
			}
		})
	}
	return nil
}

// atHead is decided, the decision of pull request number at decided.head, made over every other
// open pull request at that head as well (spec R4). A check run belongs to a commit, not to a pull
// request, so the one legion-verdict at a head is every open pull request's there:
//   - failure where a refusal of any of them stands, since a refusal at a head beats every approval
//     at that head;
//   - where a Legion issue owns the head, as one of them is a legion/<KEY> head or a task branch of
//     whatever project, success only where each such pull request's steps all passed there, since
//     legion-verdict is success at such a head only then;
//   - at a head no Legion issue owns, success where the verdict of any of them stands;
//   - and nothing otherwise.
//
// GitHub lists the pull requests of a commit (GET /commits/{sha}/pulls), and only the open ones whose
// head is that commit are decided, each by decideVerdict. A failure needs no other read, and the
// callers ask for none then. The reason of a failure for want of an independent review stays only
// where the head's conclusion is the pull request's own.
func (w *workflowRuntime) atHead(ctx context.Context, entry config.VerdictRepository, repository ghrepo.Repository, number int, decided verdictDecision) (verdictDecision, error) {
	if decided.head == currentHead {
		return decided, nil
	}
	others, err := w.openPullRequestsAt(ctx, repository, decided.head)
	if err != nil {
		return verdictDecision{}, err
	}
	at := []verdictDecision{decided}
	for _, other := range others {
		if other == number {
			continue
		}
		each, err := w.decideVerdict(ctx, entry, repository, other, decided.head)
		if err != nil {
			return verdictDecision{}, err
		}
		at = append(at, each)
	}
	combined := decided
	if combined.conclusion = headConclusion(at); combined.conclusion != decided.conclusion {
		combined.reason = ""
	}
	return combined, nil
}

// headConclusion is legion-verdict's conclusion at one head from the decisions of the open pull
// requests there (atHead).
func headConclusion(at []verdictDecision) verdict.Conclusion {
	legion, legionPassed, outsidePassed := false, true, false
	for _, decided := range at {
		switch {
		case decided.conclusion == verdict.Failure:
			return verdict.Failure
		case decided.kind == headLegion:
			legion, legionPassed = true, legionPassed && decided.conclusion == verdict.Success
		default:
			outsidePassed = outsidePassed || decided.conclusion == verdict.Success
		}
	}
	if legion && legionPassed || !legion && outsidePassed {
		return verdict.Success
	}
	return verdict.None
}

// openPullRequestsAt is the number of each open pull request of repository whose head is head, as
// GitHub lists the pull requests of that commit with the daemon's read token (GET
// /commits/{sha}/pulls).
func (w *workflowRuntime) openPullRequestsAt(ctx context.Context, repository ghrepo.Repository, head string) ([]int, error) {
	serving := w.repositories.serving(repository)
	lease, err := w.tokens.ReadToken(ctx, serving.Owner())
	if err != nil {
		return nil, fmt.Errorf("mint the read token for %s: %w", serving, err)
	}
	client := githubrest.Client{Token: lease.Token, API: githubrest.RepositoryAPI(w.githubAPI, serving)}
	read, cancel := context.WithTimeout(ctx, issueRead)
	defer cancel()
	pulls, err := githubrest.GetPages[githubrest.PullRequest](read, client, "/commits/"+head+"/pulls?per_page=100")
	if err != nil {
		return nil, fmt.Errorf("list the pull requests of %s at %s: %w", serving, head, err)
	}
	var open []int
	for _, pull := range pulls {
		if pull.State == "open" && pull.Head.SHA == head {
			open = append(open, pull.Number)
		}
	}
	return open, nil
}

// wroteSuccess is whether the newest check run or the newest commit status the store records the
// daemon wrote on target's head concluded success.
func (w *workflowRuntime) wroteSuccess(ctx context.Context, target verdictTarget) (bool, error) {
	var newest map[store.VerdictWriteKind]store.VerdictConclusion
	err := pgx.BeginFunc(ctx, w.pool, func(tx pgx.Tx) error {
		writes, err := store.VerdictWritesTx(ctx, tx, target.repository, target.head)
		newest = map[store.VerdictWriteKind]store.VerdictConclusion{}
		for _, written := range writes {
			newest[written.Kind] = written.Conclusion
		}
		return err
	})
	return newest[store.VerdictWriteCheckRun] == store.VerdictConclusionSuccess || newest[store.VerdictWriteStatus] == store.VerdictConclusionSuccess, err
}

// verdictDecision is what decideVerdict decided of a head: the head, legion-verdict's conclusion
// there, on a Legion head the reason of a failure no step refused (verdict.Reason, which the run's
// output names), the kind of head (headLegion, headOutside) and, on a Legion head of this project,
// the issue that owns it.
type verdictDecision struct {
	head       string
	conclusion verdict.Conclusion
	reason     verdict.Reason
	kind       string
	issue      string
}

// currentHead, as decideVerdict's head, asks it to decide the pull request at the head it stands at
// now, which a merge group's write does (writeMergeGroup): the group's event names the pull request
// and its own commit, not the head.
const currentHead = ""

// decideVerdict decides head of pull request number of repository, entry being the repository's
// verdict entry, or, for currentHead, the head the pull request stands at now: the record's, or the
// one GitHub shows. A pull request the project's records hold is a Legion head of the issue that
// owns it, decided from the steps of its task's plan at the head and the reviews delivered at the
// head (legionInputs), and, where every step passed there, from the change's commits
// (independence), read after the transaction so it holds no connection over a GitHub call. A
// legion/<KEY> head of another project's issue is a Legion head no verdict stands at: this daemon
// holds no step of that issue (otherProjectsHead). Every other pull request is an outside one,
// decided from its reviews and, where the entry takes them, its side reviewer's verdict
// (outsideInputs). A pull request no record holds yet - one opened before the daemon heard its
// repository - is read from GitHub once (GET /pulls/{number}): one GitHub shows closed merges
// nothing and has no verdict, as a closed Legion pull request is written on by nobody (workflow's
// askVerdictWrite); a legion/<KEY> head holds no verdict here, of this project's issue since no
// record holds it and of another project's as above; and any other is recorded as an outside pull
// request at the head GitHub shows and decided as one.
func (w *workflowRuntime) decideVerdict(ctx context.Context, entry config.VerdictRepository, repository ghrepo.Repository, number int, head string) (verdictDecision, error) {
	if other := w.verdicts.otherIssue(repository.String(), number); other != "" {
		return w.otherProjectsHead(repository, number, other, head), nil
	}
	var decided verdictDecision
	var legion *legionHead
	known, other := false, ""
	if err := pgx.BeginFunc(ctx, w.pool, func(tx pgx.Tx) error {
		pr, err := ghrepo.Under(w.renames(), repository.String(), func(name string) (*record.PullRequest, error) {
			return w.records.PullRequestByNumber(ctx, tx, name, number)
		})
		if err != nil {
			return err
		}
		if pr != nil {
			known = true
			if head == currentHead {
				head = pr.HeadSHA
			}
			issue, err := w.records.Issue(ctx, tx, pr.Issue)
			if err != nil || issue == nil {
				other = pr.Issue
				return err
			}
			read, err := w.legionInputs(ctx, tx, entry, *issue, *pr, head)
			legion = &read
			decided = verdictDecision{head: head, kind: headLegion, issue: issue.Key}
			return err
		}
		outside, found, err := store.OutsidePullRequestTx(ctx, tx, repository, number)
		if err != nil || !found {
			return err
		}
		known = true
		if head == currentHead {
			head = outside.Head
		}
		in, err := outsideInputs(ctx, tx, entry, repository, number, head)
		decided.conclusion, _ = verdict.Decide(in)
		decided.head, decided.kind = head, headOutside
		return err
	}); err != nil {
		return decided, err
	}
	switch {
	case other != "":
		return w.otherProjectsHead(repository, number, other, head), nil
	case legion != nil:
		w.independence(ctx, repository, number, legion)
		decided.conclusion, decided.reason = verdict.Decide(legion.in)
		return decided, nil
	case known:
		return decided, nil
	}
	pull, err := w.readVerdictPullRequest(ctx, repository, number)
	if err != nil {
		return verdictDecision{}, err
	}
	if head == currentHead {
		head = pull.Head.SHA
	}
	if pull.State != "open" {
		return verdictDecision{head: head, kind: headOutside}, nil
	}
	if key, issueBranch := claim.BranchIssue(pull.Head.Ref); issueBranch {
		if !claim.KeyOfProject(w.dispatchProject, key) {
			return w.otherProjectsHead(repository, number, key, head), nil
		}
		// A head of this project's issue that no record holds has no verdict here.
		return verdictDecision{head: head, kind: headLegion}, nil
	}
	err = pgx.BeginFunc(ctx, w.pool, func(tx pgx.Tx) error {
		if _, err := store.RecordOutsidePullRequestTx(ctx, tx, store.OutsidePullRequest{Repository: repository, Number: number,
			Head: pull.Head.SHA, UpdatedAt: pull.UpdatedAt, RecordedAt: w.clockNow().UTC()}); err != nil {
			return err
		}
		in, err := outsideInputs(ctx, tx, entry, repository, number, head)
		decided.conclusion, _ = verdict.Decide(in)
		decided.head, decided.kind = head, headOutside
		return err
	})
	return decided, err
}

// otherProjectsHead is the decision of head of pull request number of repository, whose head branch
// is issue's, another project's: a Legion head where no verdict stands, since this daemon holds no
// step of that issue (spec R4), so the writer writes no success there and failure over each run and
// status of the one App it did not write (R19). It is logged once per pull request.
func (w *workflowRuntime) otherProjectsHead(repository ghrepo.Repository, number int, issue, head string) verdictDecision {
	w.noteOtherProject(repository.String(), number, issue)
	return verdictDecision{head: head, kind: headLegion}
}

// legionHead is what decideVerdict reads of a Legion head in its transaction (legionInputs): the
// inputs verdict.Decide reads, and, by its index among their steps, the session names the daemon
// records for the claim of each review step whose agent approved the head and ran no
// implementation step of the task: the claims the Omp-Session test of the change's commits is
// still to read (independence).
type legionHead struct {
	in       verdict.Inputs
	sessions map[int][]string
}

// legionInputs is what the record holds of head of issue's pull request pr: the steps of the task
// its run walks (workflow.Engine.VerdictSteps), by #4611's step type; for the task's testing
// steps, the states Dispatch published of its checks at the head (record.HeadStates), which every
// testing step reads; for each review step, the verdict at the head of its step (store.StepVerdict,
// workflow's recordVerdict), and whether the claim that gave it ran an implementation step of the
// task, as the step starts record each step's agent; the sessions that claim records
// (store.ClaimSessionNamesTx), none of which can be read where it records none; and every review
// of the pull request delivered by webhook (store.VerdictReview), with entry's ci_reviewer, whose
// request for changes refuses a Legion head too (spec R4). Each is read at head alone, so a pass at
// an earlier commit counts at no later head.
func (w *workflowRuntime) legionInputs(ctx context.Context, tx pgx.Tx, entry config.VerdictRepository, issue record.Issue, pr record.PullRequest, head string) (legionHead, error) {
	repository, err := ghrepo.Parse("the repository of "+issue.Key+"'s pull request", pr.Repo)
	if err != nil {
		return legionHead{}, err
	}
	steps, err := w.engine.VerdictSteps(ctx, tx, issue)
	if err != nil {
		return legionHead{}, err
	}
	implemented := map[claim.Token]bool{}
	tested := false
	for _, step := range steps {
		switch {
		case step.Type == planwords.TypeImplementation && step.Agent != "":
			implemented[step.Agent] = true
		case step.Type == planwords.TypeTesting:
			tested = true
		}
	}
	read := legionHead{in: verdict.Inputs{Head: head, Legion: true, Steps: make([]verdict.Step, 0, len(steps)), CIReviewer: entry.CIReviewer},
		sessions: map[int][]string{}}
	for i, step := range steps {
		read.in.Steps = append(read.in.Steps, verdict.Step{Type: step.Type})
		if step.Type != planwords.TypeReview {
			continue
		}
		given, found, err := store.StepVerdictTx(ctx, tx, repository, pr.Number, head, step.Place.Task, step.Place.Place)
		if err != nil {
			return legionHead{}, err
		}
		if !found || given.Issue != issue.Key {
			continue
		}
		agent := claim.Token(given.Session)
		read.in.Steps[i].Verdict, read.in.Steps[i].Wrote = given.Verdict, implemented[agent]
		if given.Verdict != verdict.ReviewerApprove || implemented[agent] {
			continue
		}
		names, err := store.ClaimSessionNamesTx(ctx, tx, agent)
		if err != nil {
			return legionHead{}, err
		}
		read.in.Steps[i].Unread, read.sessions[i] = len(names) == 0, names
	}
	if tested {
		held, err := w.records.HeadStates(ctx, tx, issue.Key, pr.Repo, head)
		if err != nil {
			return legionHead{}, err
		}
		read.in.Checks = checksAt(held)
	}
	read.in.Reviews, err = reviewsOf(ctx, tx, repository, pr.Number)
	return read, err
}

// checksAt is each check of the head's set and the state the daemon holds of it there, "" for
// none, in the set's order: nil where no set is held at the head.
func checksAt(held record.HeadStates) []verdict.Check {
	if held.Checks == nil {
		return nil
	}
	checks := make([]verdict.Check, 0, len(held.Checks))
	for _, check := range held.Checks {
		checks = append(checks, verdict.Check{Name: check, State: held.States[check].State})
	}
	return checks
}

// changeUnread is what the writer logs when it cannot read the change's commits, and so counts no
// review step of the head as independent.
const changeUnread = "verdict: the change's commits could not be read, so no review step counts as independent"

// independence reads the change's commits for the Omp-Session test of read's head where that test
// decides anything: every testing and review step passed at the head (verdict.StepsPassed), and an
// approving review step's claim ran no implementation step and records a session. A commit whose
// Omp-Session trailer names one of that claim's sessions marks its step as one whose claim wrote
// part of the change. Where the commits cannot be read the change is unread, and no review step
// counts as independent (verdict.Inputs.Unread); the line it logs says why.
func (w *workflowRuntime) independence(ctx context.Context, repository ghrepo.Repository, number int, read *legionHead) {
	readable := false
	for _, names := range read.sessions {
		readable = readable || len(names) > 0
	}
	if !readable || !verdict.StepsPassed(read.in) {
		return
	}
	trailers, err := w.changeSessions(ctx, repository, number, read.in.Head)
	if err != nil {
		read.in.Unread = true
		w.log.Warn(changeUnread, "repository", repository.String(), "pull_request", number, "head", read.in.Head, "error", err)
		return
	}
	for i, names := range read.sessions {
		if slices.ContainsFunc(names, func(name string) bool { return trailers[name] }) {
			read.in.Steps[i].Wrote = true
		}
	}
}

// listedCommit is one commit of a pull request as GitHub lists them (GET /pulls/{number}/commits):
// its id and its message.
type listedCommit struct {
	SHA    string `json:"sha"`
	Commit struct {
		Message string `json:"message"`
	} `json:"commit"`
}

// changeCommitsListed is the most commits GitHub lists of a pull request: a list that long may
// leave commits of the change out.
const changeCommitsListed = 250

// changeSessions is every session the Omp-Session trailers of the change's commits name: pull
// request number's commits as GitHub lists them with the daemon's read token, which must reach
// head, the head decided. A list GitHub refuses, one that holds as many commits as GitHub lists at
// most, and one that does not reach head - the pull request stands at another head - is no
// reading of the change, and an error.
func (w *workflowRuntime) changeSessions(ctx context.Context, repository ghrepo.Repository, number int, head string) (map[string]bool, error) {
	serving := w.repositories.serving(repository)
	lease, err := w.tokens.ReadToken(ctx, serving.Owner())
	if err != nil {
		return nil, fmt.Errorf("mint the read token for %s: %w", serving, err)
	}
	client := githubrest.Client{Token: lease.Token, API: githubrest.RepositoryAPI(w.githubAPI, serving)}
	read, cancel := context.WithTimeout(ctx, issueRead)
	defer cancel()
	commits, err := githubrest.GetPages[listedCommit](read, client, "/pulls/"+strconv.Itoa(number)+"/commits")
	if err != nil {
		return nil, fmt.Errorf("list the commits of %s#%d: %w", serving, number, err)
	}
	if len(commits) >= changeCommitsListed {
		return nil, fmt.Errorf("%s#%d lists %d commits, the most GitHub lists, so the change may hold more", serving, number, len(commits))
	}
	if !slices.ContainsFunc(commits, func(c listedCommit) bool { return c.SHA == head }) {
		return nil, fmt.Errorf("the %d commits GitHub lists of %s#%d do not reach %s", len(commits), serving, number, head)
	}
	sessions := map[string]bool{}
	for _, c := range commits {
		for _, session := range ompSessions(c.Commit.Message) {
			sessions[session] = true
		}
	}
	return sessions, nil
}

// ompSessionLine is a commit message's Omp-Session trailer line, as `legion git` writes it
// (cmd/legion's gitTrailers): the key in any case, as git compares trailer keys, and the session
// id.
var ompSessionLine = regexp.MustCompile(`(?im)^omp-session:[ \t]*(\S+)[ \t\r]*$`)

// ompSessions is every session id an Omp-Session line of message names, wherever it stands in the
// message: a squash or a cherry-pick can carry a commit's trailer into the body of another, and a
// line read so can only take a review's independence away.
func ompSessions(message string) []string {
	var sessions []string
	for _, match := range ompSessionLine.FindAllStringSubmatch(message, -1) {
		sessions = append(sessions, match[1])
	}
	return sessions
}

// outsideInputs is what the daemon records of head of an outside pull request: its reviews, and,
// where entry takes side reviews, the verdict the controller relayed from its side reviewer at the
// head (a store.Verdict of no issue).
func outsideInputs(ctx context.Context, tx pgx.Tx, entry config.VerdictRepository, repository ghrepo.Repository, number int, head string) (verdict.Inputs, error) {
	in := verdict.Inputs{Head: head, CIReviewer: entry.CIReviewer, SideReviews: entry.SideReviews}
	if entry.SideReviews {
		given, found, err := store.VerdictTx(ctx, tx, repository, number, head)
		if err != nil {
			return verdict.Inputs{}, err
		}
		// A side verdict stands only while its session is the reviewer the controller names at the
		// head now: a later naming of another session withdraws it.
		named, current, err := store.SideReviewerTx(ctx, tx, repository, number, head)
		if err != nil {
			return verdict.Inputs{}, err
		}
		if found && given.Issue == "" && current && named.Session == given.Session {
			in.Side = given.Verdict
		}
	}
	var err error
	in.Reviews, err = reviewsOf(ctx, tx, repository, number)
	return in, err
}

// reviewsOf is every review of the pull request the daemon recorded, in the order it recorded them,
// as a verdict reads them: each with GitHub's id and submission time, by which verdict.Decide takes
// each author's last.
func reviewsOf(ctx context.Context, tx pgx.Tx, repository ghrepo.Repository, number int) ([]verdict.Review, error) {
	recorded, err := store.VerdictReviewsTx(ctx, tx, repository, number)
	if err != nil {
		return nil, err
	}
	reviews := make([]verdict.Review, 0, len(recorded))
	for _, r := range recorded {
		reviews = append(reviews, verdict.Review{Head: r.Head, Author: r.Author, Person: r.AuthorCanWrite, State: r.State,
			GitHubID: r.GitHubID, SubmittedAt: r.SubmittedAt})
	}
	return reviews, nil
}

// readVerdictPullRequest reads pull request number of repository from GitHub with the daemon's
// read token (GET /pulls/{number}).
func (w *workflowRuntime) readVerdictPullRequest(ctx context.Context, repository ghrepo.Repository, number int) (githubrest.PullRequest, error) {
	serving := w.repositories.serving(repository)
	lease, err := w.tokens.ReadToken(ctx, serving.Owner())
	if err != nil {
		return githubrest.PullRequest{}, fmt.Errorf("mint the read token for %s: %w", serving, err)
	}
	client := githubrest.Client{Token: lease.Token, API: githubrest.RepositoryAPI(w.githubAPI, serving)}
	read, cancel := context.WithTimeout(ctx, issueRead)
	defer cancel()
	var pull githubrest.PullRequest
	if err := client.Get(read, "/pulls/"+strconv.Itoa(number), &pull); err != nil {
		return githubrest.PullRequest{}, fmt.Errorf("read pull request %s#%d: %w", serving, number, err)
	}
	return pull, nil
}

// renames is the deployment's repository renames, none for a runtime a test builds with no
// repositories.
func (w *workflowRuntime) renames() ghrepo.Renames {
	if w.repositories == nil {
		return ghrepo.Renames{}
	}
	return w.repositories.renames
}

// noteOtherProject logs, once per pull request, that its head is another project's issue branch,
// where no verdict stands since this daemon holds no step of that issue (otherProjectsHead), and
// keeps that issue for the pull request, so the next decision of it reads nothing.
func (w *workflowRuntime) noteOtherProject(repository string, number int, issue string) {
	if w.verdicts.markForeign(repository, number, issue) {
		w.log.Info(otherProjectsHead, "repository", repository, "pull_request", number, "issue", issue, "project", w.dispatchProject)
	}
}

// listedCheckRun is one check run as GitHub lists a commit's (GET /commits/{ref}/check-runs): its
// id, its conclusion and the id of the App that made it, which a rename of the App leaves as it is.
type listedCheckRun struct {
	ID         int64  `json:"id"`
	Conclusion string `json:"conclusion"`
	App        struct {
		ID int64 `json:"id"`
	} `json:"app"`
}

// listedStatus is one commit status as GitHub lists a commit's (GET /commits/{ref}/statuses),
// newest first: its id, context, state and the account that created it.
type listedStatus struct {
	ID      int64  `json:"id"`
	Context string `json:"context"`
	State   string `json:"state"`
	Creator struct {
		Login string `json:"login"`
	} `json:"creator"`
}

// writeDecided writes decided's conclusion as legion-verdict on target's head with the daemon's own
// verdict token (appauth.Tokens.VerdictToken), and records each write in the store
// (store.VerdictWrite) and in one legion_verdict_write line. It reads the head's legion-verdict
// check runs and commit statuses first, and tells the one App's runs by the App's id. What each run
// and status of the App there should show is the head's conclusion, or failure where the head has
// none (spec R19):
//   - each run of the App the record does not hold is updated to it, whatever it shows, and so
//     becomes one the record holds;
//   - each run the record holds is updated to it where GitHub shows it at another conclusion,
//     whether the daemon's own write left it there or another token of the App wrote over it;
//   - a run of the daemon's own is created only where the head has a conclusion and no run of the
//     App is there at all;
//   - the newest legion-verdict status the App's bot created gets a status of the daemon's own over
//     it, with that state, when the record does not hold it or GitHub shows it at another state:
//     GitHub reads the newest status of each context.
//
// A head with no conclusion is written on only when overForeign is set, which is the writers'
// sweep's ask to put failure over each run and status of the App there that does not show it; a
// write a fact asks for (writeVerdictOver without overForeign) writes nothing there and makes no
// GitHub call. In token mode the daemon mints no verdict token (appauth.ErrNoVerdictToken): it
// writes nothing and logs that once. One goroutine makes every write (writeVerdicts), so no two
// writes of one head read its runs together and each create one.
func (w *workflowRuntime) writeDecided(ctx context.Context, target verdictTarget, decided verdictDecision, overForeign bool) error {
	_, err := w.writeDecidedChanged(ctx, target, decided, overForeign)
	return err
}

// writeDecidedChanged is writeDecided, and answers too, where it answers no error, whether it
// created or updated a check run or set a commit status: a pass that writes a success is followed
// by one more merge-state read (writeVerdictOver), and a pass that finds it written is not.
func (w *workflowRuntime) writeDecidedChanged(ctx context.Context, target verdictTarget, decided verdictDecision, overForeign bool) (bool, error) {
	if decided.conclusion == verdict.None && !overForeign {
		return false, nil
	}
	serving := w.repositories.serving(target.repository)
	lease, err := w.tokens.VerdictToken(ctx, serving.Owner())
	if errors.Is(err, appauth.ErrNoVerdictToken) {
		w.verdicts.tokenMode.Do(func() { w.log.Warn(verdictTokenMode, "repository", target.repository.String()) })
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("mint the verdict token for %s: %w", serving, err)
	}
	client := githubrest.Client{Token: lease.Token, API: githubrest.RepositoryAPI(w.githubAPI, serving)}
	call, cancel := context.WithTimeout(ctx, issueRead)
	defer cancel()
	ownRuns, ownStatuses, err := w.recordedVerdictWrites(ctx, target)
	if err != nil {
		return false, err
	}
	runs, err := githubrest.GetListPages[listedCheckRun](call, client, "/commits/"+target.head+"/check-runs?check_name="+verdict.CheckName+"&filter=all", "check_runs")
	if err != nil {
		return false, fmt.Errorf("list the legion-verdict runs of %s at %s: %w", serving, target.head, err)
	}
	over := decided.conclusion
	if over == verdict.None {
		over = verdict.Failure
	}
	shown, changed := false, false
	for _, run := range runs {
		if w.verdicts.appID == 0 || run.App.ID != w.verdicts.appID {
			continue
		}
		shown = true
		foreign := run.ID
		if ownRuns[run.ID] {
			if run.Conclusion == string(over) {
				continue
			}
			foreign = 0
		}
		if err := w.updateVerdictRun(call, client, target, decided, run.ID, over, foreign); err != nil {
			return false, err
		}
		changed = true
	}
	if decided.conclusion != verdict.None && !shown {
		if err := w.createVerdictRun(call, client, target, decided); err != nil {
			return false, err
		}
		changed = true
	}
	statuses, err := githubrest.GetPages[listedStatus](call, client, "/commits/"+target.head+"/statuses")
	if err != nil {
		return false, fmt.Errorf("list the commit statuses of %s at %s: %w", serving, target.head, err)
	}
	for _, status := range statuses {
		if status.Context != verdict.CheckName || status.Creator.Login != w.appLogin {
			continue
		}
		switch {
		case !ownStatuses[status.ID]:
			return true, w.createVerdictStatus(call, client, target, decided.kind, over, status.ID)
		case status.State != string(over):
			return true, w.createVerdictStatus(call, client, target, decided.kind, over, 0)
		}
		return changed, nil
	}
	return changed, nil
}

// recordedVerdictWrites is the GitHub id of each check run and of each commit status the store
// records the daemon wrote on target's head.
func (w *workflowRuntime) recordedVerdictWrites(ctx context.Context, target verdictTarget) (runs, statuses map[int64]bool, err error) {
	runs, statuses = map[int64]bool{}, map[int64]bool{}
	err = pgx.BeginFunc(ctx, w.pool, func(tx pgx.Tx) error {
		writes, err := store.VerdictWritesTx(ctx, tx, target.repository, target.head)
		for _, written := range writes {
			if written.Kind == store.VerdictWriteCheckRun {
				runs[written.GitHubID] = true
			} else {
				statuses[written.GitHubID] = true
			}
		}
		return err
	})
	return runs, statuses, err
}

// The summaries of a legion-verdict run that concludes failure for want of an independent review
// (verdict.NoIndependentReview, verdict.ChangeUnread), under the title verdictOutput gives it. The
// merger reads them on the head it would merge and tells the owner (spec R4), so their words are
// the merger's prompt's to quote: change them with prompts/roles/merger.md.
const (
	noIndependentReviewSummary = "Every testing and review step of the task's plan passed at this head, and no review step that approved it was run by an agent whose claim wrote none of the change: an independent review is missing."
	changeUnreadSummary        = "Every testing and review step of the task's plan passed at this head, but the daemon could not read the change's commits or the sessions of the review steps' claims, so it counts no review as independent: an independent review is missing."
)

// verdictOutput is the title and summary a check run of legion-verdict shows when it concludes
// conclusion on a head of kind, decided being what the writer decided there: a failure for want
// of an independent review says so, and every other conclusion names the head's kind alone.
func verdictOutput(decided verdictDecision, conclusion verdict.Conclusion) map[string]string {
	summary := map[verdict.Reason]string{verdict.NoIndependentReview: noIndependentReviewSummary, verdict.ChangeUnread: changeUnreadSummary}[decided.reason]
	if summary == "" || conclusion != decided.conclusion {
		return map[string]string{
			"title":   verdict.CheckName + ": " + string(conclusion),
			"summary": "The Legion daemon wrote this from the verdicts it records at this " + decided.kind + " head.",
		}
	}
	return map[string]string{"title": verdict.CheckName + ": " + string(conclusion) + " (no independent review)", "summary": summary}
}

// createVerdictRun creates the daemon's own legion-verdict check run on target's head, completed
// with decided's conclusion (POST /check-runs), and records it.
func (w *workflowRuntime) createVerdictRun(ctx context.Context, client githubrest.Client, target verdictTarget, decided verdictDecision) error {
	var created struct {
		ID int64 `json:"id"`
	}
	if err := client.Post(ctx, "/check-runs", map[string]any{"name": verdict.CheckName, "head_sha": target.head, "status": "completed",
		"conclusion": string(decided.conclusion), "output": verdictOutput(decided, decided.conclusion)}, &created); err != nil {
		return fmt.Errorf("create legion-verdict on %s at %s: %w", target.repository, target.head, err)
	}
	return w.recordVerdictWrite(ctx, target, decided.kind, store.VerdictWriteCheckRun, created.ID, decided.conclusion, 0)
}

// updateVerdictRun updates check run id on target's head to conclusion (PATCH /check-runs/{id}),
// with the output decided gives it (verdictOutput), and records it, foreign being the id of the
// run when another writer made it, 0 when it is the daemon's own.
func (w *workflowRuntime) updateVerdictRun(ctx context.Context, client githubrest.Client, target verdictTarget, decided verdictDecision, id int64, conclusion verdict.Conclusion, foreign int64) error {
	if err := client.Patch(ctx, "/check-runs/"+strconv.FormatInt(id, 10), map[string]any{"status": "completed",
		"conclusion": string(conclusion), "output": verdictOutput(decided, conclusion)}, nil); err != nil {
		return fmt.Errorf("update legion-verdict run %d on %s at %s: %w", id, target.repository, target.head, err)
	}
	return w.recordVerdictWrite(ctx, target, decided.kind, store.VerdictWriteCheckRun, id, conclusion, foreign)
}

// createVerdictStatus sets a legion-verdict commit status of the daemon's own on target's head with
// conclusion's state (POST /statuses/{sha}) and records it, foreign being the id of the status of
// another writer it goes over, 0 for one of the daemon's own.
func (w *workflowRuntime) createVerdictStatus(ctx context.Context, client githubrest.Client, target verdictTarget, kind string, conclusion verdict.Conclusion, foreign int64) error {
	var created struct {
		ID int64 `json:"id"`
	}
	if err := client.Post(ctx, "/statuses/"+target.head, map[string]any{"state": string(conclusion), "context": verdict.CheckName,
		"description": "Written by the Legion daemon from the verdicts it records at this " + kind + " head."}, &created); err != nil {
		return fmt.Errorf("set the legion-verdict status on %s at %s: %w", target.repository, target.head, err)
	}
	return w.recordVerdictWrite(ctx, target, kind, store.VerdictWriteStatus, created.ID, conclusion, foreign)
}

// recordVerdictWrite logs one write GitHub made in one legion_verdict_write line, and records it
// in the store: the repository, the pull request, the head and its kind, what was written and its
// GitHub id, the conclusion, and the id of another writer's run or status it went over (0 for
// none), which is what a read of the App's runs at a head matches each one against (spec R19). The
// line comes first, so a write the store fails to record is still named; the store's failure is
// the write's, and the next sweep writes over the run its record does not hold.
func (w *workflowRuntime) recordVerdictWrite(ctx context.Context, target verdictTarget, kind string, written store.VerdictWriteKind, id int64, conclusion verdict.Conclusion, foreign int64) error {
	w.log.Info("legion_verdict_write", "repository", target.repository.String(), "pull_request", target.number, "head", target.head,
		"head_kind", kind, "kind", string(written), "id", id, "conclusion", string(conclusion), "foreign_id", foreign)
	if err := pgx.BeginFunc(ctx, w.pool, func(tx pgx.Tx) error {
		_, err := store.RecordVerdictWriteTx(ctx, tx, store.VerdictWrite{Repository: target.repository, Head: target.head, Kind: written,
			GitHubID: id, Conclusion: conclusion, ForeignID: foreign, WrittenAt: w.clockNow().UTC()})
		return err
	}); err != nil {
		return fmt.Errorf("record the write of legion-verdict %s %d on %s at %s: %w", written, id, target.repository, target.head, err)
	}
	return nil
}

// verdictFacts is the intake handler of the facts a verdict reads that the workflow does not
// record: an outside pull request's sighting and its reviews, and the reviews of a Legion head
// delivered by webhook. It runs in each fact's transaction, after the workflow, and records what
// the fact says; an outside review asks for a write on the fact's commit, and a Legion head's
// review is asked for by the workflow, before its merge-state read (workflow's askVerdictWrite).
func (w *workflowRuntime) verdictFacts() intake.Handler { return verdictFacts{w: w} }

type verdictFacts struct{ w *workflowRuntime }

func (h verdictFacts) Apply(ctx context.Context, tx pgx.Tx, fact intake.Fact) (intake.Result, error) {
	switch fact := fact.(type) {
	case intake.OutsidePullRequest:
		return intake.Result{}, h.w.recordOutsidePullRequest(ctx, tx, fact)
	case intake.OutsideReview:
		return intake.Result{}, h.w.recordOutsideReview(ctx, tx, fact)
	case intake.PullRequestReview:
		return intake.Result{}, h.w.recordLegionReview(ctx, tx, fact)
	case intake.PullRequestOpened:
		h.w.noteIssueBranch(ctx, fact.Repo, fact.Number, fact.Branch)
	case intake.PullRequestSynchronized:
		h.w.noteIssueBranch(ctx, fact.Repo, fact.Number, fact.Branch)
	}
	return intake.Result{}, nil
}

// recordOutsidePullRequest records the sighting of an outside pull request of a verdict repository
// at its head, with GitHub's clock of the pull request the event carries, so a sighting delivered
// late takes no head back (store.OutsidePullRequestTx). A new head has no verdict, so nothing is
// written.
func (w *workflowRuntime) recordOutsidePullRequest(ctx context.Context, tx pgx.Tx, fact intake.OutsidePullRequest) error {
	if !w.namesVerdictRepository(fact.Repo) {
		return nil
	}
	repository, err := ghrepo.Parse("the repository of an outside pull request", fact.Repo)
	if err != nil {
		return err
	}
	_, err = store.RecordOutsidePullRequestTx(ctx, tx, store.OutsidePullRequest{Repository: repository, Number: fact.Number,
		Head: fact.HeadSHA, UpdatedAt: fact.UpdatedAt, RecordedAt: w.clockNow().UTC()})
	return err
}

// recordOutsideReview records a review of an outside pull request of a verdict repository that a
// verdict reads (an approval or a request for changes) at the commit it read, and asks, on the
// commit, for a write on that commit. A review of a pull request whose head the writer knows to be
// another project's issue branch records nothing.
func (w *workflowRuntime) recordOutsideReview(ctx context.Context, tx pgx.Tx, fact intake.OutsideReview) error {
	if !w.namesVerdictRepository(fact.Repo) || !fact.Deciding() || w.verdicts.otherIssue(fact.Repo, fact.Number) != "" {
		return nil
	}
	head, err := w.recordReview(ctx, tx, fact.Repo, fact.PullRequestReview)
	if err != nil || head == "" {
		return err
	}
	repo, number := fact.Repo, fact.Number
	intake.OnCommit(ctx, func() { w.queueVerdictWrite("", repo, number, head) })
	return nil
}

// recordLegionReview records a webhook's review of a Legion head of the project's own on a verdict
// repository that a verdict reads, one not written with the one App's token, under the repository
// the pull request's record keeps: a person's request for changes refuses that head until the same
// person approves it, whatever phase the issue has reached (spec R4), which the round's decision,
// cleared when the round ends, cannot hold. A review of another project's Legion head is that
// project's, and logged once.
func (w *workflowRuntime) recordLegionReview(ctx context.Context, tx pgx.Tx, fact intake.PullRequestReview) error {
	if !w.namesVerdictRepository(fact.Repo) || !fact.Deciding() || fact.Author == w.appLogin {
		return nil
	}
	pr, err := ghrepo.Under(w.renames(), fact.Repo, func(name string) (*record.PullRequest, error) {
		return w.records.PullRequestByNumber(ctx, tx, name, fact.Number)
	})
	if err != nil || pr == nil {
		return err
	}
	issue, err := w.records.Issue(ctx, tx, pr.Issue)
	if err != nil {
		return err
	}
	if issue == nil {
		repo, number, other := fact.Repo, fact.Number, pr.Issue
		intake.OnCommit(ctx, func() { w.noteOtherProject(repo, number, other) })
		return nil
	}
	_, err = w.recordReview(ctx, tx, pr.Repo, fact)
	return err
}

// recordReview records review under repo at the commit it read (commit_id), and answers that head:
// the one a review is a verdict at, as the workflow holds a person's refusal at it (workflow's
// review) and asks for the write there (askMergeState). A review that names none records nothing
// and answers "", rather than standing at the pull request's head the event carried, which is not
// the code the review read.
func (w *workflowRuntime) recordReview(ctx context.Context, tx pgx.Tx, repo string, review intake.PullRequestReview) (string, error) {
	head := review.CommitID
	if head == "" {
		return "", nil
	}
	repository, err := ghrepo.Parse("the repository of a review", repo)
	if err != nil {
		return "", err
	}
	_, err = store.RecordVerdictReviewTx(ctx, tx, store.VerdictReview{Repository: repository, Number: review.Number, GitHubID: review.ID,
		Head: head, Author: review.Author, AuthorCanWrite: review.AuthorCanWrite, State: strings.ToLower(review.State),
		SubmittedAt: review.SubmittedAt, RecordedAt: w.clockNow().UTC()})
	return head, err
}

// noteIssueBranch logs, on the commit and once per pull request, a pull request of a verdict
// repository whose head is another project's issue branch (noteOtherProject).
func (w *workflowRuntime) noteIssueBranch(ctx context.Context, repo string, number int, branch string) {
	key, issueBranch := claim.BranchIssue(branch)
	if !w.namesVerdictRepository(repo) || !issueBranch || claim.KeyOfProject(w.dispatchProject, key) {
		return
	}
	intake.OnCommit(ctx, func() { w.noteOtherProject(repo, number, key) })
}
