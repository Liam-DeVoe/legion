package daemon

import (
	"context"
	"errors"
	"fmt"
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
// whose head is another project's issue branch, once while the App is in token mode, each time a
// merge-state read goes on after waiting its bound on its issue's write, and each write that fails,
// naming what repairs it.
const (
	otherProjectsHead  = "verdict: no daemon writes legion-verdict here: the head is another project's issue branch"
	verdictTokenMode   = "verdict: legion-verdict is not written: the App is in token mode, which mints no verdict token"
	verdictReadWentOn  = "verdict: a merge-state read went on before its issue's write of legion-verdict ended"
	verdictWriteFailed = "verdict: a write of legion-verdict failed; the writers' sweep at boot and every 10 minutes writes it again"
)

// verdictWriter is the state of the daemon's writer of the required check legion-verdict (spec
// R4, R19): the repositories its configuration names under verdict_repositories, the heads a fact
// asked it to write on that it has not written yet, and what the merge-state reads of an issue wait
// on. A fact that can change a head's verdict asks for the write on its commit
// (workflow.Config.WriteVerdict, queueVerdictWrite), a merge group's asks for one on the group's
// commit (queueMergeGroupWrite), and one goroutine writes them one at a time (writeVerdicts), so a
// fact's transaction waits on no GitHub call and two writes of one head never both make a run. A
// write a fact asked for is held in memory, and a restart loses one not made:
// the writers' sweep, at boot and every 10 minutes, writes each head whose verdict the record holds
// and GitHub shows no run of this daemon's on.
type verdictWriter struct {
	entries []config.VerdictRepository
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
	// project's issue branch, with that issue, which the daemon has logged once and writes nothing
	// on.
	foreign map[string]string
	// tokenMode logs once that the App is in token mode (appauth.ErrNoVerdictToken).
	tokenMode sync.Once
}

// verdictTarget is one head a write of legion-verdict is asked for: the pull request's repository
// and number, the head, and the issue whose merge-state reads wait on the write ("" for a pull
// request no Legion issue owns). A merge group's write (queueMergeGroupWrite) names the group's
// commit as group, which it writes on, and no head: it decides the pull request at the head that
// stands when the write is made (writeMergeGroup). overForeign is writeDecided's: the writers'
// sweep's ask to put failure over another writer's run or status where no verdict stands.
type verdictTarget struct {
	issue       string
	repository  ghrepo.Repository
	number      int
	head        string
	group       string
	overForeign bool
}

func newVerdictWriter(entries []config.VerdictRepository) *verdictWriter {
	return &verdictWriter{entries: entries, bound: issueRead, pending: map[string]int{}, ended: make(chan struct{}),
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
			err = w.writeVerdictOver(ctx, target.repository, target.number, target.head, target.overForeign)
		}
		if err != nil && ctx.Err() == nil {
			w.log.Warn(verdictWriteFailed, "repository", target.repository.String(), "pull_request", target.number, "head", target.head,
				"group", target.group, "issue", target.issue, "error", err)
		}
		w.verdicts.done(target)
	}
}

// writeVerdictOver writes legion-verdict on head of pull request number of repository from the
// verdict the daemon records at that head (verdict.Decide): success where it stands, failure where a
// refusal stands, and nothing where neither does, so the head cannot merge (spec R4); with
// overForeign, the writers' sweep's ask, failure over another writer's run or status there
// (writeDecided). It writes only on a repository its verdict_repositories names, and on a
// legion/<KEY> head only of its own project: another project's issue branch is that project's
// daemon's to write, and is logged once per pull request. A repository is named under the name
// GitHub serves it under now as well as the one a record keeps (verdictEntry). It creates its own
// check run or updates the one its record holds, and writes its conclusion over each run and
// status of the one App its record does not hold (spec R19, writeDecided).
//
// Once a pass writes a Legion head's success, or turns its run or status to success, the issue's
// merge state is read once more, a ladder's wait later (mergeStateWait): GitHub can answer the read
// the write held (verdictWriter.await) with `blocked` while it has not yet counted the new run, an
// answer that ends the climb, and no other fact asks again, since Envoy leaves the one App's runs
// out of the CI record. A pass that finds the success already written asks for nothing more: the
// pass that wrote it asked already.
func (w *workflowRuntime) writeVerdictOver(ctx context.Context, repository ghrepo.Repository, number int, head string, overForeign bool) error {
	entry, ok := w.verdictEntry(repository)
	if !ok {
		return nil
	}
	decided, err := w.decideVerdict(ctx, entry, repository, number, head)
	if err != nil {
		return err
	}
	if decided.otherIssue != "" {
		w.noteOtherProject(repository.String(), number, decided.otherIssue)
		return nil
	}
	target := verdictTarget{repository: repository, number: number, head: head}
	// A success this daemon wrote stands only while the verdict that wrote it does: where the head
	// has none now, its own run and status are turned to failure, so the head cannot merge on it.
	if decided.conclusion == verdict.None {
		standing, err := w.wroteSuccess(ctx, target)
		if err != nil {
			return err
		}
		if standing {
			decided.conclusion = verdict.Failure
		}
	}
	changed, err := w.writeDecidedChanged(ctx, target, decided, overForeign)
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

// verdictDecision is what writeVerdictOver decided of a head: legion-verdict's conclusion, the kind of
// head (headLegion, headOutside) and, on a Legion head, the issue that owns it; or otherIssue,
// another project's issue whose branch the head is, on which this daemon writes nothing.
type verdictDecision struct {
	conclusion verdict.Conclusion
	kind       string
	issue      string
	otherIssue string
}

// currentHead, as decideVerdict's head, asks it to decide the pull request at the head it stands at
// now, which a merge group's write does (writeMergeGroup): the group's event names the pull request
// and its own commit, not the head.
const currentHead = ""

// decideVerdict decides head of pull request number of repository, entry being the repository's
// verdict entry, or, for currentHead, the head the pull request stands at now: the record's, or the
// one GitHub shows. A pull request the project's records hold is a Legion head of the issue that
// owns it, decided from the tester's completion, the reviewer's verdict at the head and the reviews
// delivered at the head (legionInputs); one another project's records hold is that project's.
// Every other pull request is an outside one, decided from its reviews and, where the entry takes
// them, its side reviewer's verdict (outsideInputs). A pull request no record holds yet - one
// opened before the daemon heard its repository - is read from GitHub once (GET /pulls/{number}):
// one GitHub shows closed merges nothing and has no verdict, as a closed Legion pull request is
// written on by nobody (workflow's askVerdictWrite); a legion/<KEY> head is KEY's; and any other is
// recorded as an outside pull request at the head GitHub shows and decided as one.
func (w *workflowRuntime) decideVerdict(ctx context.Context, entry config.VerdictRepository, repository ghrepo.Repository, number int, head string) (verdictDecision, error) {
	if other := w.verdicts.otherIssue(repository.String(), number); other != "" {
		return verdictDecision{otherIssue: other}, nil
	}
	var decided verdictDecision
	known := false
	if err := pgx.BeginFunc(ctx, w.pool, func(tx pgx.Tx) error {
		pr, err := ghrepo.Under(w.renames(), repository.String(), func(name string) (*record.PullRequest, error) {
			return w.records.PullRequestByNumber(ctx, tx, name, number)
		})
		if err != nil {
			return err
		}
		if pr != nil {
			known = true
			issue, err := w.records.Issue(ctx, tx, pr.Issue)
			if err != nil || issue == nil {
				decided.otherIssue = pr.Issue
				return err
			}
			if head == currentHead {
				head = pr.HeadSHA
			}
			in, err := w.legionInputs(ctx, tx, *issue, *pr, head)
			decided = verdictDecision{conclusion: verdict.Decide(in), kind: headLegion, issue: issue.Key}
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
		decided = verdictDecision{conclusion: verdict.Decide(in), kind: headOutside}
		return err
	}); err != nil || known {
		return decided, err
	}
	pull, err := w.readVerdictPullRequest(ctx, repository, number)
	if err != nil {
		return verdictDecision{}, err
	}
	if pull.State != "open" {
		return verdictDecision{kind: headOutside}, nil
	}
	if key, issueBranch := claim.BranchIssue(pull.Head.Ref); issueBranch {
		if !claim.KeyOfProject(w.dispatchProject, key) {
			return verdictDecision{otherIssue: key}, nil
		}
		// A head of this project's issue that no record holds has no verdict here.
		return verdictDecision{kind: headLegion}, nil
	}
	if head == currentHead {
		head = pull.Head.SHA
	}
	err = pgx.BeginFunc(ctx, w.pool, func(tx pgx.Tx) error {
		if _, err := store.RecordOutsidePullRequestTx(ctx, tx, store.OutsidePullRequest{Repository: repository, Number: number,
			Head: pull.Head.SHA, RecordedAt: w.clockNow().UTC()}); err != nil {
			return err
		}
		in, err := outsideInputs(ctx, tx, entry, repository, number, head)
		decided = verdictDecision{conclusion: verdict.Decide(in), kind: headOutside}
		return err
	})
	return decided, err
}

// legionInputs is what the record holds of head of issue's pull request pr: the tester's result,
// which is no pass until legion-verdict decides a testing step by Dispatch's check states at the
// head (verdict.NoTestingPass), the reviewer's verdict at the head (store.Verdict, workflow's
// recordVerdict), and every review of the pull request delivered by webhook (store.VerdictReview).
func (w *workflowRuntime) legionInputs(ctx context.Context, tx pgx.Tx, issue record.Issue, pr record.PullRequest, head string) (verdict.Inputs, error) {
	repository, err := ghrepo.Parse("the repository of "+issue.Key+"'s pull request", pr.Repo)
	if err != nil {
		return verdict.Inputs{}, err
	}
	in := verdict.Inputs{Head: head, Legion: true, Tester: verdict.NoTestingPass}
	given, found, err := store.VerdictTx(ctx, tx, repository, pr.Number, head)
	if err != nil {
		return verdict.Inputs{}, err
	}
	if found && given.Issue == issue.Key {
		in.Reviewer = verdict.Reviewer{Verdict: given.Verdict, Commit: given.Head, TestedCommit: given.TestedCommit}
	}
	in.Reviews, err = reviewsOf(ctx, tx, repository, pr.Number)
	return in, err
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
// on which this daemon writes no legion-verdict: the daemon of that project writes it, on a
// repository its own verdict_repositories names.
func (w *workflowRuntime) noteOtherProject(repository string, number int, issue string) {
	if w.verdicts.markForeign(repository, number, issue) {
		w.log.Info(otherProjectsHead, "repository", repository, "pull_request", number, "issue", issue, "project", w.dispatchProject)
	}
}

// listedCheckRun is one check run as GitHub lists a commit's (GET /commits/{ref}/check-runs): its
// id, its conclusion and the slug of the App that made it.
type listedCheckRun struct {
	ID         int64  `json:"id"`
	Conclusion string `json:"conclusion"`
	App        struct {
		Slug string `json:"slug"`
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
// check runs and commit statuses first. Its own run, the newest the record holds, is updated to the
// conclusion where it shows another, or created where there is none. Each run of the one App the
// record does not hold is updated to the conclusion, or to failure where the head has none (spec
// R19), and so becomes one the record holds; where such a run already carries the conclusion, no
// run of its own is created beside it. The newest legion-verdict status the App's bot created gets
// a status of the daemon's own over it, with the conclusion's state (failure where there is none),
// when the record does not hold it or holds it at another state: GitHub reads the newest status of
// each context.
//
// A head with no conclusion is written on only when overForeign is set, which is the writers'
// sweep's ask to put failure over another writer's run or status at a head no verdict stands at;
// a write a fact asks for (writeVerdictOver without overForeign) writes nothing there and makes no GitHub call. In token mode
// the daemon mints no verdict token (appauth.ErrNoVerdictToken): it writes nothing and logs that
// once. One goroutine makes every write (writeVerdicts), so no two writes of one head read its
// runs together and each create one.
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
	slug := strings.TrimSuffix(w.appLogin, "[bot]")
	var own *listedCheckRun
	carried, changed := false, false
	for i := range runs {
		run := &runs[i]
		switch {
		case slug == "" || run.App.Slug != slug:
		case ownRuns[run.ID]:
			if own == nil || run.ID > own.ID {
				own = run
			}
		default:
			if err := w.updateVerdictRun(call, client, target, decided.kind, run.ID, over, run.ID); err != nil {
				return false, err
			}
			carried = carried || over == decided.conclusion
			changed = true
		}
	}
	if decided.conclusion != verdict.None {
		switch {
		case own != nil && own.Conclusion != string(decided.conclusion):
			if err := w.updateVerdictRun(call, client, target, decided.kind, own.ID, decided.conclusion, 0); err != nil {
				return false, err
			}
			changed = true
		case own == nil && !carried:
			if err := w.createVerdictRun(call, client, target, decided); err != nil {
				return false, err
			}
			changed = true
		}
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
		case decided.conclusion != verdict.None && status.State != string(decided.conclusion):
			return true, w.createVerdictStatus(call, client, target, decided.kind, decided.conclusion, 0)
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

// verdictOutput is the title and summary a check run of legion-verdict shows.
func verdictOutput(kind string, conclusion verdict.Conclusion) map[string]string {
	return map[string]string{
		"title":   verdict.CheckName + ": " + string(conclusion),
		"summary": "The Legion daemon wrote this from the verdicts it records at this " + kind + " head.",
	}
}

// createVerdictRun creates the daemon's own legion-verdict check run on target's head, completed
// with decided's conclusion (POST /check-runs), and records it.
func (w *workflowRuntime) createVerdictRun(ctx context.Context, client githubrest.Client, target verdictTarget, decided verdictDecision) error {
	var created struct {
		ID int64 `json:"id"`
	}
	if err := client.Post(ctx, "/check-runs", map[string]any{"name": verdict.CheckName, "head_sha": target.head, "status": "completed",
		"conclusion": string(decided.conclusion), "output": verdictOutput(decided.kind, decided.conclusion)}, &created); err != nil {
		return fmt.Errorf("create legion-verdict on %s at %s: %w", target.repository, target.head, err)
	}
	return w.recordVerdictWrite(ctx, target, decided.kind, store.VerdictWriteCheckRun, created.ID, decided.conclusion, 0)
}

// updateVerdictRun updates check run id on target's head to conclusion (PATCH /check-runs/{id}) and
// records it, foreign being the id of the run when another writer made it, 0 when it is the
// daemon's own.
func (w *workflowRuntime) updateVerdictRun(ctx context.Context, client githubrest.Client, target verdictTarget, kind string, id int64, conclusion verdict.Conclusion, foreign int64) error {
	if err := client.Patch(ctx, "/check-runs/"+strconv.FormatInt(id, 10), map[string]any{"status": "completed",
		"conclusion": string(conclusion), "output": verdictOutput(kind, conclusion)}, nil); err != nil {
		return fmt.Errorf("update legion-verdict run %d on %s at %s: %w", id, target.repository, target.head, err)
	}
	return w.recordVerdictWrite(ctx, target, kind, store.VerdictWriteCheckRun, id, conclusion, foreign)
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

// recordVerdictWrite records one write in the store and logs it in one legion_verdict_write line:
// the repository, the pull request, the head and its kind, what was written and its GitHub id, the
// conclusion, and the id of another writer's run or status it went over (0 for none), which is
// what a read of the App's runs at a head matches each one against (spec R19).
func (w *workflowRuntime) recordVerdictWrite(ctx context.Context, target verdictTarget, kind string, written store.VerdictWriteKind, id int64, conclusion verdict.Conclusion, foreign int64) error {
	if err := pgx.BeginFunc(ctx, w.pool, func(tx pgx.Tx) error {
		_, err := store.RecordVerdictWriteTx(ctx, tx, store.VerdictWrite{Repository: target.repository, Head: target.head, Kind: written,
			GitHubID: id, Conclusion: conclusion, ForeignID: foreign, WrittenAt: w.clockNow().UTC()})
		return err
	}); err != nil {
		return fmt.Errorf("record the write of legion-verdict %s %d on %s at %s: %w", written, id, target.repository, target.head, err)
	}
	w.log.Info("legion_verdict_write", "repository", target.repository.String(), "pull_request", target.number, "head", target.head,
		"head_kind", kind, "kind", string(written), "id", id, "conclusion", string(conclusion), "foreign_id", foreign)
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
// at its head. A new head has no verdict, so nothing is written.
func (w *workflowRuntime) recordOutsidePullRequest(ctx context.Context, tx pgx.Tx, fact intake.OutsidePullRequest) error {
	if !w.namesVerdictRepository(fact.Repo) {
		return nil
	}
	repository, err := ghrepo.Parse("the repository of an outside pull request", fact.Repo)
	if err != nil {
		return err
	}
	_, err = store.RecordOutsidePullRequestTx(ctx, tx, store.OutsidePullRequest{Repository: repository, Number: fact.Number,
		Head: fact.HeadSHA, RecordedAt: w.clockNow().UTC()})
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
