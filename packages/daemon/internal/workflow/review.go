package workflow

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/classify"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/notify"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/plan"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/verdict"
)

// withoutVerdictCheck is fact with the daemon's own required check, legion-verdict
// (verdict.CheckName), taken out of its check runs, the checks it names failed and cancelled, and
// its commit statuses, and whether that check was all it named. The daemon writes that check
// from the verdicts, so it is no CI a role reads or waits on: a settlement of it alone wakes and
// tells nobody (spec R4, "the daemon's own write wakes no role"). Envoy leaves the verdict App's
// runs out of a head's CI record already; this holds the daemon to the same rule whatever a
// listener sends, in each list it can name the check in, a failed or cancelled one with no run of
// it listed among them. A settlement that names no such check is fact itself, copied nowhere.
func withoutVerdictCheck(fact intake.PullRequestChecks) (intake.PullRequestChecks, bool) {
	isRun := func(run record.AttemptRun) bool { return run.Name == verdict.CheckName }
	isStatus := func(status record.CommitStatus) bool { return status.Context == verdict.CheckName }
	isName := func(name string) bool { return name == verdict.CheckName }
	if !slices.ContainsFunc(fact.CheckRuns, isRun) && !slices.ContainsFunc(fact.Statuses, isStatus) &&
		!slices.Contains(fact.Failing, verdict.CheckName) && !slices.Contains(fact.Cancelled, verdict.CheckName) {
		return fact, false
	}
	fact.CheckRuns = slices.DeleteFunc(slices.Clone(fact.CheckRuns), isRun)
	fact.Failing = slices.DeleteFunc(slices.Clone(fact.Failing), isName)
	fact.Cancelled = slices.DeleteFunc(slices.Clone(fact.Cancelled), isName)
	fact.Statuses = slices.DeleteFunc(slices.Clone(fact.Statuses), isStatus)
	return fact, len(fact.CheckRuns)+len(fact.Statuses)+len(fact.Failing)+len(fact.Cancelled) == 0
}

// checks applies a CI settlement to the pull request and tells the roles that work the issue what
// it says (tellChecks). It moves nothing: a failed CI check is information the worker holding the
// phase decides on - rerun, wait, or fix - and not the daemon's to read the semantics of
// (dispatch://INFRA-714/spec R1).
//
// It runs for every settlement fact, after the wake (wakeOnSettlement), and each settlement it
// does not apply is named by one INFO line once the fact commits, since a pull request whose head
// got no CI message otherwise leaves nothing to say whether the daemon dropped its settlement or
// never heard it (dispatch://INFRA-4135 R6): one that no recorded pull request matches
// (intake.NoRecordedPullRequestLine, which the consumer writes for nearly every such settlement
// before any transaction, so here it names a record gone by the time the fact applies); one that
// stands for no head the record holds (classify.SettlementFor), with the record's head, the head
// its checks stand at and whether a head run is open; and one of the head that the fence takes as
// stale or conflicting (classify.ClassifySettlement), with the generation and snapshot the record
// holds. A redelivery of a settlement already applied is no drop and logs nothing. Each line
// begins with the settlement's identity (intake.SettlementIdentity), by which it is matched to
// the listener's line that published the settlement, and is written once for each identity
// (logDroppedSettlement).
func (e *Engine) checks(ctx context.Context, tx pgx.Tx, fact intake.PullRequestChecks, woken claim.Role) (intake.Result, error) {
	pr, err := e.pullRequest(ctx, tx, fact.Repo, fact.Number)
	if err != nil {
		return intake.Result{}, err
	}
	if pr == nil {
		e.logDroppedSettlement(ctx, fact, intake.NoRecordedPullRequestLine)
		return intake.Result{}, nil
	}
	// The listener settles every commit of the pull request, its head or not, and a settlement
	// stands only for the commit it names: it is for the head only when it names the head. While a
	// head run is open the settlement is recorded in it whether it stands or not
	// (classify.ApplyCheckSettlement). A list of commit statuses the decoder could not read costs
	// the settlement nothing else: the fact names no status, and the pull request keeps the
	// statuses it holds for the head, since a settlement's statuses are merged into those per
	// context and a context it leaves out stays as it is kept (classify.ApplySettlement,
	// intake.PullRequestChecks.StatusesUnread).
	candidate := settlementCandidate(fact)
	told := settledChecks(*pr)
	settled, applied := classify.ApplyCheckSettlement(*pr, candidate)
	if !applied {
		e.logUnappliedSettlement(ctx, fact, *pr, candidate)
		if pr.Run != nil {
			*pr = settled
			if err := e.store.PutPullRequest(ctx, tx, *pr); err != nil {
				return intake.Result{}, err
			}
		}
		// The write above is one of the two pull-request writes of this package that mark no
		// movement of the issue (record.Issue.MovedAt, the rule in engine.go's dispatchIssue),
		// the other being a push onto the base branch, which is no news of the pull request
		// either (base_push.go). It records the fence the daemon read and decides nothing — no
		// run and no outcome changed — so it is the daemon learning what it already knew, which
		// is never a movement. Marking it would refresh a stopped tree's quiet time at every
		// duplicate settlement the listener sends, and a tree that stopped with a settled head
		// would never be found at all. The write is made with the store's own PutPullRequest and
		// not through putPullRequest, though that helper's own test would mark nothing for most
		// writes of this shape: the settlement that pushes an open head run past its bound of
		// steps closes the run and decides nothing, and the helper would read the run's closing
		// as news. internal/admit's TestTheRecordKeepsWhenAnIssueLastMoved drives that write in
		// its case of the listener's settlement that decides nothing, and fails where this write
		// goes through the helper.
		//
		// A settlement that does not stand for the head tells nobody, and GitHub's merge state is
		// read for the head all the same (Apply's askMergeState): a run GitHub counts for the head
		// can settle on a commit the record does not stand at, and the state is the only answer
		// that says so.
		return intake.Result{}, nil
	}
	// The settlement that applies is the other half of that pair, and it is news: the head it
	// settled on, the runs it reports and what each of them concluded are new to the record, and
	// a GitHub event of the issue's pull request is a movement of the issue
	// (dispatch://INFRA-2560 R3). So this write goes through the helper that marks it
	// (putPullRequest) while the duplicate above does not, and a tree whose last news was its CI
	// settling is measured from that settlement rather than from whatever moved the issue before
	// it.
	*pr = settled
	if err := e.putPullRequest(ctx, tx, *pr); err != nil {
		return intake.Result{}, err
	}
	return intake.Result{}, e.tellChecks(ctx, tx, pr, told, woken)
}

// settlementCandidate is the settlement fact as the apply step reads it, and the one place it is
// built from the fact: checks applies it, and the wake (wakeOnSettlement) asks the apply step
// whether it applies before it wakes anyone, so the two read one candidate and cannot drift. It is
// the fact as delivered: a list of commit statuses the decoder could not read is no status here,
// whatever the wake's task names in its place.
func settlementCandidate(fact intake.PullRequestChecks) classify.SettlementCandidate {
	return classify.SettlementCandidate{Head: fact.HeadSHA, CheckRuns: fact.CheckRuns, Generation: fact.Generation, Snapshot: fact.Snapshot,
		Failing: fact.Failing, Cancelled: fact.Cancelled, Statuses: fact.Statuses}
}

// logUnappliedSettlement names a settlement checks does not apply to pr, the record as it stood
// before the settlement: one that stands for no head the record holds, and one of its head the
// fence takes as stale or conflicting. Every other settlement not applied, the redelivery of one
// applied (classify.SettlementDuplicate) among them, logs nothing.
func (e *Engine) logUnappliedSettlement(ctx context.Context, fact intake.PullRequestChecks, pr record.PullRequest, candidate classify.SettlementCandidate) {
	stood, stands := classify.SettlementFor(pr, candidate)
	if !stands {
		e.logDroppedSettlement(ctx, fact, "workflow: a checks settlement stands for no head the record holds",
			"recorded_head", pr.HeadSHA, "checked_head", pr.CheckedHead, "head_run_open", pr.Run != nil, "issue", pr.Issue)
		return
	}
	switch classified := classify.ClassifySettlement(stood, candidate); classified {
	case classify.SettlementStale, classify.SettlementConflict:
		e.logDroppedSettlement(ctx, fact, "workflow: a checks settlement of the head was not applied",
			"classification", string(classified), "recorded_generation", stood.Generation, "recorded_snapshot", stood.Snapshot, "issue", pr.Issue)
	}
}

// logDroppedSettlement writes msg at INFO once the fact's transaction commits, after the
// settlement's identity and then args, unless the engine has logged a drop of the same settlement
// already: one settlement delivered many times writes one line, and the identity is marked in the
// commit's own hook, so a fact rolled back neither writes the line nor keeps its retry from
// writing it (Engine.droppedSettlements).
func (e *Engine) logDroppedSettlement(ctx context.Context, fact intake.PullRequestChecks, msg string, args ...any) {
	identity := fact.Identity()
	args = append(identity.Attributes(), args...)
	store.OnCommit(ctx, func() {
		if e.droppedSettlements.Add(identity) {
			e.log.Info(msg, args...)
		}
	})
}

// conflictConfirmWait is how long after a first CONFLICTING read of a head the read that can
// confirm it comes, at the least (dispatch://INFRA-4374 R2). GitHub documents that `mergeable` is
// computed by a background job and that a client asks again "after giving the job time to
// complete", and it documents no time for the job (https://docs.github.com/en/rest/pulls/pulls,
// "Get a pull request"). The daemon already waits 30 seconds between reads of a head GitHub
// answers `unknown` for (the daemon's mergeStateWait); the spec's floor of one minute is two of
// those waits, and this is that floor. It is compared with the reads' own times, and with the
// engine's clock where a fact carries none, so tests control it.
const conflictConfirmWait = time.Minute

// mergeability records what GitHub's last read found for whether the pull request's head can be
// merged into its base (intake.PullRequestMergeability, readPullRequest's own GitHub answer), and
// acts on a conflict only once a second read has confirmed it and the issue's next step needs a
// head that merges.
//
// A head that conflicts with its base is judged in two reads (dispatch://INFRA-4374 R2). GitHub
// computes mergeability in a background job and can answer CONFLICTING about a base it has not
// caught up with, so the first CONFLICTING read of an open pull request's head records itself
// (record.PullRequest.ConflictRead: the head, the base branch and the time of the read), queues the
// read that asks GitHub again once conflictConfirmWait has passed (record.ConflictConfirm), logs
// itself, and acts on nothing. A later CONFLICTING read of the same head and the same base branch,
// made conflictConfirmWait or more after the first, confirms the conflict: the record holds the
// head conflicting (ConflictingHead) from then on, drops the first read, and decides. A
// CONFLICTING read sooner than the wait changes nothing of the first read, and one of another
// head or another base branch is a first read of its own. The base branch is compared by name and
// never by commit: GitHub's base.sha does not name the base it computed against, and a conflict
// with a newer base is still a conflict. A MERGEABLE read releases the first read and logs both
// reads. An UNKNOWN read leaves it as it is, and the second read's own row releases it once its
// last attempt still answers UNKNOWN, while the first read is still the one the row was queued for
// (conflictConfirmDone, record.ConflictConfirm.FirstReadAt). After a release, the next CONFLICTING
// read is a first read again, with a row of its own.
//
// The decision is R0's (dispatch://INFRA-4374 R0, classify.ConflictNeedsMerge), read on the
// base's rules for its required checks, which the confirming read carries
// (intake.PullRequestMergeability.Rules). A reason to act asks the implementer, or in merging the
// merger, to merge the base forward, and the issue keeps its phase (mergeForward, R1); no reason
// leaves the conflict recorded and logged with the checks the base requires and those green on
// the head, and the task's steps go on at that head. A CONFLICTING read of a head the
// record already holds conflicting decides again in the same way, since the phase the issue has
// reached since can need what the phase before did not, and mergeForward asks nothing twice for
// one head.
//
// GitHub's own merge state for the head rides along on the same fact: it is what says whether
// GitHub would merge this head now, which the tri-state mergeability cannot, and it is what an
// approval standing on the head waits for (reviewRound). So in reviewing the round is asked again
// once the state is recorded, whatever the read decided - this read is the one that moves an
// approved issue to accepting, as often as not. The base the read names is recorded with
// them, since that read is the only one that names the base of a pull request opened before the
// record kept one: it is what a push onto that branch finds the pull request by (basePush), and a
// read that answers for a base push already recorded clears the push's mark, the two having been
// read together.
//
// A read that answers for another head than the record stands at is dropped whole, and logged: a
// push landed between GitHub's answer and this apply, so the verdict is about a tree the record
// has moved past, and an approval must never advance on it. Nothing is lost by dropping it - that
// push is itself a fact that asks for a read of the head it left (askMergeState). The read time
// goes with it: a stamp is never recorded against a head the record has already left, since it
// would say that head's state was read when it was another head's that was.
//
// A read GitHub answered before the read the record holds of its head
// (record.PullRequest.StateReadAt) is dropped whole as well, and logged: nothing is written,
// nothing decided and no round asked again. The readers of the pull request run apart, and the
// per-issue read dates its answer before it follows the head under the fact lock (daemon's
// readIssuePullRequest), so a MERGEABLE answer made before a push to the base can be applied after
// the CONFLICTING answer of a read made since. Applied, it would release that conflict's first
// read, and the record would say mergeable while GitHub says CONFLICTING. A fact that carries no
// read time is never dropped for its time.
//
// The read time the fact carries (intake.PullRequestMergeability.ReadAt) is what
// record.PullRequest.StateReadAt is written from, and it is the one thing a read that confirms
// what is recorded still moves: the row is written for it alone. A fact that carries none - every
// one minted by something that did not read GitHub, as a test's PullRequestMergeability is -
// writes no stamp and erases none, so the recorded one stands as the last real read left it. The
// two reads of a conflict are timed by the same read time, and by the engine's clock for a fact
// that carries none.
//
// Whatever the read decides, it is also the judgement the conflict-round exemption is read from
// (record.PullRequest.ConflictingHead, recordRound): a confirmed conflict holds the pull request's
// own head conflicting, a MERGEABLE read of that same head releases it, and an UNKNOWN read,
// which is what GitHub answers for a head sitting in the merge queue, leaves the mark as it is -
// GitHub has not computed an answer, so there is nothing to record and nothing to take back. The
// mark is a second column rather than a reading of Mergeability because every read rewrites that
// one from GitHub's answer: the UNKNOWN of a read on the ladder that follows would lose the
// conflict, and the next round at the conflicting head would count.
func (e *Engine) mergeability(ctx context.Context, tx pgx.Tx, fact intake.PullRequestMergeability) (intake.Result, error) {
	pr, err := e.pullRequest(ctx, tx, fact.Repo, fact.Number)
	if err != nil || pr == nil {
		return intake.Result{}, err
	}
	if fact.HeadSHA != pr.HeadSHA {
		e.logOnCommit(ctx, "workflow: GitHub's merge state answered for a head the pull request has moved past; the read is dropped",
			"issue", pr.Issue, "pullRequest", pr.Number, "answered", fact.HeadSHA, "head", pr.HeadSHA,
			"mergeable", string(fact.Mergeable), "mergeState", string(fact.MergeState))
		return intake.Result{}, nil
	}
	if !fact.ReadAt.IsZero() && fact.ReadAt.Before(pr.StateReadAt) {
		e.logOnCommit(ctx, "workflow: GitHub's merge state answered before the read the record holds; the read is dropped",
			"issue", pr.Issue, "pullRequest", pr.Number, "head", pr.HeadSHA, "readAt", fact.ReadAt, "stateReadAt", pr.StateReadAt,
			"mergeable", string(fact.Mergeable), "mergeState", string(fact.MergeState))
		return intake.Result{}, nil
	}
	base := pr.Base
	if fact.Base != "" {
		base = fact.Base
	}
	readAt := fact.ReadAt
	if readAt.IsZero() {
		readAt = e.now()
	}
	prior := *pr
	conflicting, first, decide := pr.ConflictingHead, pr.ConflictRead, false
	switch fact.Mergeable {
	case record.MergeabilityConflicting:
		switch {
		case pr.State != record.PullRequestOpen:
			// A pull request that is not open merges nothing, so its conflict is nothing to act on.
		case conflicting == pr.HeadSHA:
			decide = true
		case first != nil && first.Head == pr.HeadSHA && first.Base == base:
			if readAt.Sub(first.At) >= conflictConfirmWait {
				conflicting, first, decide = pr.HeadSHA, nil, true
			}
		default:
			first = &record.ConflictRead{Head: pr.HeadSHA, Base: base, At: readAt}
			if err := e.enqueueAt(ctx, tx, pr.Issue, record.ConflictConfirm{Number: pr.Number, Head: pr.HeadSHA, FirstReadAt: readAt}, readAt.Add(conflictConfirmWait)); err != nil {
				return intake.Result{}, err
			}
			e.logOnCommit(ctx, "workflow: a conflict read waits for a second read",
				"issue", pr.Issue, "pullRequest", pr.Number, "head", pr.HeadSHA, "base", base, "readAt", readAt)
		}
	case record.MergeabilityMergeable:
		if conflicting == pr.HeadSHA {
			conflicting = ""
		}
		if first != nil {
			e.logOnCommit(ctx, "workflow: a second read did not confirm the conflict",
				"issue", pr.Issue, "pullRequest", pr.Number, "head", first.Head, "base", first.Base,
				"firstReadAt", first.At, "readAt", readAt, "mergeable", string(fact.Mergeable), "mergeState", string(fact.MergeState))
			first = nil
		}
	}
	stateReadAt := pr.StateReadAt
	if !fact.ReadAt.IsZero() {
		stateReadAt = fact.ReadAt
	}
	// The last clause is the confirming read's: it answers what is already recorded, and the time
	// it was read is the whole of what it has to say.
	if pr.Mergeability != fact.Mergeable || pr.MergeState != fact.MergeState || pr.Base != base || pr.BaseMoved != "" ||
		pr.ConflictingHead != conflicting || pr.ConflictRead != first || !stateReadAt.Equal(pr.StateReadAt) {
		pr.Mergeability, pr.MergeState, pr.Base, pr.BaseMoved, pr.ConflictingHead, pr.ConflictRead, pr.StateReadAt =
			fact.Mergeable, fact.MergeState, base, "", conflicting, first, stateReadAt
		if err := e.putPullRequest(ctx, tx, *pr); err != nil {
			return intake.Result{}, err
		}
	}
	issue, err := e.store.Issue(ctx, tx, pr.Issue)
	if err != nil || issue == nil {
		return intake.Result{}, err
	}
	if decide {
		green := classify.GreenContexts(*pr)
		if why := classify.ConflictNeedsMerge(*issue, fact.Rules, green); why != "" {
			if err := e.mergeForward(ctx, tx, *issue, pr, why); err != nil {
				return intake.Result{}, err
			}
		} else {
			// Rules is nil for a read that carried none, which accepting and retro decide on too: the
			// conflict waits there whatever the rules (classify.ConflictNeedsMerge).
			var required []string
			if fact.Rules != nil {
				required = fact.Rules.Required
			}
			e.logOnCommit(ctx, "workflow: a confirmed conflict needs no merge yet",
				"issue", pr.Issue, "pullRequest", pr.Number, "head", pr.HeadSHA, "base", base, "phase", issue.Phase,
				"required", required, "green", green)
		}
	}
	return intake.Result{}, e.reaskRound(ctx, tx, *issue, pr, prior, byMergeState)
}

// settledChecks is what the CI settlement standing for the pull request's head says: the head it
// settled on, and each check it reports with what it came to, in name order; "" while no
// settlement stands. A check the settlement names as failed failed, one it names as cancelled was
// cancelled, one whose run GitHub skipped (record.RunSkipped) was skipped, and one it reports as a
// run and names no other way succeeded - the listener settles a commit once every check it has
// seen is terminal. Nothing here judges those conclusions: a job GitHub never started is named
// skipped and decides nothing, and what they mean for the work is the reading worker's
// (dispatch://INFRA-714/spec R1). The line is the whole of what the daemon knows of that head's
// CI, so two settlements that leave it the same say nothing new (tellChecks).
//
// Each check is named by the word GitHub gave its run (record.AttemptRun.GitHubConclusion) where
// the settlement carried one, and by the daemon's conclusion where it did not (shownAs). GitHub's
// word is what tells a check that ran from one that did not, where the daemon's conclusion takes
// every run that neither failed nor was cancelled as succeeded or skipped. A skipped check ran
// nothing on the head, and a role that read it as a pass would believe a suite held back for the
// merge queue passed on a head where it never ran (dispatch://INFRA-4135 R1), so where the line
// names any check skipped it says once, after the commit statuses, that a skipped check ran
// nothing (skippedClause). A settlement that carries no word and names no run skipped, as a
// listener of an earlier release sends, gives the line the daemon gave before it read either.
//
// Each name's conclusion is the newest run of it among the settlements of the head the fence
// stands at, which is what the record holds (classify.SettlementFor): a settlement stands only
// for the head it names, so a check re-run there and passed is no longer red however its earlier
// run ended. A check still running on that head is in no settlement of it - the listener settles
// a commit only once every check it has seen there is terminal - so this line names it not at
// all: a pending check is no verdict, and never a failure.
//
// A check run's name is whoever wrote the workflow's text, and this line is read into an agent's
// prompt, so each name is quoted as a Go string (strconv.Quote): the quotes bound it, and a quote
// or a newline inside it is escaped rather than closing the one around it. A name of ordinary
// characters reads as itself inside the quotes.
//
// After the check runs it names each commit status the record keeps for the checked head, with
// its state, in context order (namedStatuses), and nothing where it keeps none, so the line of a
// head that carries no status reads as it did before the daemon kept any. A status is an entry of
// its own kind, apart from the check runs: a context that shares a check run's name is named in
// both places.
func settledChecks(pr record.PullRequest) string {
	if pr.CheckedHead == "" {
		return ""
	}
	conclusions := make(map[string]record.RunConclusion, len(pr.CheckRuns)+len(pr.Failing)+len(pr.Cancelled))
	for _, run := range pr.CheckRuns {
		conclusions[run.Name] = record.RunSucceeded
		if run.Conclusion == record.RunSkipped {
			conclusions[run.Name] = record.RunSkipped
		}
	}
	for _, name := range pr.Cancelled {
		conclusions[name] = record.RunCancelled
	}
	for _, name := range pr.Failing {
		conclusions[name] = record.RunFailed
	}
	shown := make(map[string]string, len(conclusions))
	for name, conclusion := range conclusions {
		shown[name] = string(conclusion)
	}
	// The fence holds one run per name (classify's mergeAttemptSets), the run that decided it.
	for _, run := range pr.CheckRuns {
		shown[run.Name] = shownAs(run, conclusions[run.Name])
	}
	statuses := namedStatuses(keptStatuses(pr, pr.CheckedHead))
	if len(shown) == 0 {
		return "CI settled at " + pr.CheckedHead + " reporting no check" + statuses
	}
	return "CI settled at " + pr.CheckedHead + ": " + namedConclusions(shown) + statuses + skippedClause(shown)
}

// headChecks is every check run GitHub reported on the pull request's current head with what that
// run concluded, in name order; "" when the record holds none. It is the line the notice of a head
// GitHub will not merge from names (stuckMergeStateReason), where the settlement's own line would
// not be true: a settlement standing for the head can be of the code head below it, and a name
// whose newest run is there ran on other heads of the tree and not on this one. What GitHub
// weighs for the merge is the current head, so that is the head whose checks the notice names,
// and a required check whose run is missing here is the thing its reader looks for.
//
// Each run is named by the word GitHub gave it where the settlement carried one, and by its
// conclusion where it did not (shownAs), and where any run is named skipped the line says once,
// after the commit statuses, that a skipped check ran nothing on this head (skippedClause), as
// settledChecks does and for the same reason: a required check GitHub skipped is one that never
// ran there, and its reader must not take it for a pass (dispatch://INFRA-4135 R1).
//
// Each run of the fence carries the head it ran on and its conclusion (record.AttemptRun), so
// this is a reading of the fence and not of a settlement, through classify.HeadRuns, the one test
// of a run's head the daemon's readers share: a check run on this head that a newer run of the
// same name on another head has replaced in the fence is not named, and nor is a run stored before
// the fence carried its head.
//
// After the runs it names each commit status the record keeps for the current head, with its
// state (namedStatuses), when the checked head is the current head (classify.Settled), and none
// otherwise: a status the record keeps is stamped with the head whose settlement named it, and one
// of another head says nothing of this one.
func headChecks(pr record.PullRequest) string {
	runs := classify.HeadRuns(pr)
	shown := make(map[string]string, len(runs))
	for _, run := range runs {
		shown[run.Name] = shownAs(run, run.Conclusion)
	}
	if len(shown) == 0 {
		return ""
	}
	var statuses []record.CommitStatus
	if classify.Settled(pr) {
		statuses = keptStatuses(pr, pr.HeadSHA)
	}
	return "GitHub reported on head " + pr.HeadSHA + ": " + namedConclusions(shown) + namedStatuses(statuses) + skippedClause(shown)
}

// shownAs is what a text names run's check as, conclusion being what the text concludes the check
// came to: the word GitHub gave the run (record.AttemptRun.GitHubConclusion) where the settlement
// carried one and the run's own conclusion is that conclusion, and the conclusion otherwise. The
// word is the run's, so it names the check only where that run is what decided it: a name the
// settlement's lists give another conclusion than its run's is named by the lists' conclusion, and
// a run with no word - from a listener that sends none, or a word the decoder refused - by the
// daemon's conclusion, as before the daemon kept a word. Unlike a check's name, the word is shown
// unquoted: the decoder keeps only a word of lower-case letters and underscores (intake's
// conclusionWord), so it can carry nothing else into the agent's prompt the line is read into.
func shownAs(run record.AttemptRun, conclusion record.RunConclusion) string {
	if run.GitHubConclusion != "" && run.Conclusion == conclusion {
		return run.GitHubConclusion
	}
	return string(conclusion)
}

// namedConclusions is each check name with what the line names it as (shownAs: GitHub's word where
// the settlement carried one, else the daemon's conclusion), in name order, as both lines above
// spell it.
func namedConclusions(shown map[string]string) string {
	names := make([]string, 0, len(shown))
	for name := range shown {
		names = append(names, name)
	}
	slices.Sort(names)
	for i, name := range names {
		names[i] = strconv.Quote(name) + " " + shown[name]
	}
	return strings.Join(names, ", ")
}

// skippedRanNothing is what each text the roles read of a head's CI says once where it names a
// check skipped: GitHub skipped the check, so it ran nothing on the head, and a role must not read
// it as a pass (dispatch://INFRA-4135 R1).
const skippedRanNothing = "a skipped check ran nothing on this head"

// skippedClause is what settledChecks and headChecks add after the commit statuses where they name
// any check skipped, by GitHub's word or by the daemon's conclusion, the two being spelled alike
// (record.RunSkipped); "" where they name none, so a line with no skipped check reads as before.
func skippedClause(shown map[string]string) string {
	for _, word := range shown {
		if word == string(record.RunSkipped) {
			return "; " + skippedRanNothing
		}
	}
	return ""
}

// keptStatuses is every commit status pr keeps for head (record.PullRequest.Statuses), in context
// order; none when what pr keeps is another head's. The stamp is what keeps a list out of a text
// about another head (record.CommitStatus.StampedWith): a daemon of an earlier release moves
// checked_head after a rollback and leaves the list as this one left it (record.CommitStatus.Head).
func keptStatuses(pr record.PullRequest, head string) []record.CommitStatus {
	var kept []record.CommitStatus
	for _, status := range pr.Statuses {
		if status.StampedWith(head) {
			kept = append(kept, status)
		}
	}
	slices.SortFunc(kept, func(a, b record.CommitStatus) int { return strings.Compare(a.Context, b.Context) })
	return kept
}

// namedStatuses is what settledChecks and headChecks add after the check runs for statuses: each
// context, quoted as a check name is (namedConclusions), with its state as GitHub spells it, in
// the order given; "" for none.
func namedStatuses(statuses []record.CommitStatus) string {
	if len(statuses) == 0 {
		return ""
	}
	named := make([]string, len(statuses))
	for i, status := range statuses {
		named[i] = strconv.Quote(status.Context) + " " + string(status.State)
	}
	return "; commit statuses: " + strings.Join(named, ", ")
}

// tellChecks tells the roles that work the issue what the settlement now standing for the pull
// request's head says (settledChecks), and moves nothing: the daemon passes the CI result on, and
// the worker that holds the phase decides whether to re-run, wait or fix
// (dispatch://INFRA-714/spec R1). One message goes to three roles - the worker holding the issue's
// phase, the implementer, whose code the checks ran on, and the architect that owns the issue
// (owningArchitect) - each on its own role topic (record.RoleMessage), the way the daemon reaches
// a role holder with a message. In merging the implementer is not told: the merge and what breaks
// it are the merger's to decide, and the merger asks the implementer when it needs it
// (cybermonklabs/legion#4979). A held issue keeps the worker of Hold.From (record.StandsAt).
// A role named twice, the implementer holding the phase, is told once, and a phase no worker holds
// names none (addressRoles). told is what the settlement
// standing for the head said before this one, so a second settlement of the same head that leaves
// the same conclusions tells nobody again, and so does a settlement that stands for no head. Linger
// holds a member of a closed tree where it stood (record.TreeLingers): every role of the tree is
// suspended there, so the settlement is recorded and nobody is told.
//
// A role whose released pod this same settlement woke is told nothing: its wake task already
// names the settlement (WakeTask with pods.WakeChecks), and a message about the same settlement
// would reach it as a second wake of one release, described to the worker as something somebody
// published to its role topic. woken is that role, "" when the settlement woke nobody, and every
// other role — a live one, and one the wake did not reach — is told as before.
//
// A holder this daemon cannot name is an error here, and with it the settlement's whole
// transaction: the settlement moves no issue, so there is nothing kept by telling the rest, and
// the next settlement of the head asks again.
func (e *Engine) tellChecks(ctx context.Context, tx pgx.Tx, pr *record.PullRequest, told string, woken claim.Role) error {
	settled := settledChecks(*pr)
	if settled == "" || settled == told {
		return nil
	}
	issue, err := e.store.Issue(ctx, tx, pr.Issue)
	if err != nil || issue == nil {
		return err
	}
	if lingers, err := record.TreeLingers(ctx, e.store, tx, issue.Tree); err != nil || lingers {
		return err
	}
	// The architect told is the one that owns the issue now (owningArchitect): the issue's own
	// while its claim holds its role, else the nearest one above it that does, else the tree
	// root's. A phase the architect itself holds (accepting) is that same architect's acceptance,
	// so it is told once, as the owner; and an architect this settlement woke would be the issue's
	// own released claim, which holds its role and so is that owner too.
	recipients := []roleRecipient{}
	if woken != claim.RoleArchitect {
		owner, err := e.owningArchitect(ctx, tx, *issue)
		if err != nil {
			return err
		}
		recipients = append(recipients, roleRecipient{issue: owner, role: claim.RoleArchitect, row: issue.Key})
	}
	roles := []claim.Role{RoleFor(record.StandsAt(*issue))}
	if !record.StandsIn(*issue, phase.Merging) {
		roles = append(roles, claim.RoleImplementer)
	}
	for _, role := range roles {
		if role == claim.RoleArchitect || role == woken {
			continue
		}
		recipients = append(recipients, roleRecipient{issue: issue.Key, role: role, row: issue.Key})
	}
	addressed, err := e.addressRoles(issue.Project, recipients, nil)
	if err != nil {
		return err
	}
	body := fmt.Sprintf("Pull request #%d: %s. Nothing moved on it: decide whether to re-run the checks, wait for them, or fix the code.", pr.Number, settled)
	for _, to := range addressed {
		if err := e.enqueue(ctx, tx, to.issue, record.RoleMessage{Role: string(to.token), Body: body}); err != nil {
			return err
		}
	}
	return nil
}

// owningArchitect is the key of the issue whose architect owns issue now: the end of the owner
// walk from issue itself, as the outbox routes a notice of issue's phase, over the architect
// claims of issue's tree this transaction reads (record.ReadOwnerWalk). That is issue's own
// architect while its claim holds its role, else the nearest one above it whose claim does, else
// the tree root's. A tree root is its own without a read.
func (e *Engine) owningArchitect(ctx context.Context, tx pgx.Tx, issue record.Issue) (string, error) {
	if claim.IsTreeRoot(issue.Key, issue.Tree) {
		return issue.Tree, nil
	}
	project, err := claim.ProjectToken(issue.Project)
	if err != nil {
		return "", err
	}
	walk, err := record.ReadOwnerWalk(ctx, e.store, tx, project, issue.Tree)
	if err != nil {
		return "", err
	}
	return walk.Owner(issue, false), nil
}

// RefusesRound is why review, one that decides (intake.PullRequestReview.Decides: a request for
// changes), must not decide its pull request's review round, "" when nothing refuses it. A
// round's verdict is the reviewer's, which reaches the daemon with its completion of reviewing
// (handoff, decisionOf), so the one GitHub review that decides is a person's request for changes,
// delivered by its webhook, which refuses the head; no approval decides at all, since on a head a
// Legion issue owns only the reviewer's verdict approves it, which is what writes its success. The
// daemon reads no list of a pull request's reviews: GitHub's list moves each review's commit_id to
// the pull request's current head, so a listed review is no evidence of the code it read. The
// whole rule, in this order:
//
//   - A review written with a token of the one App (Config.AppLogin) decides nothing. Every role
//     acts as that App, so its login says nothing of who wrote the review, and the reviewer posts
//     its findings as a COMMENT, which decides nothing either.
//   - A request for changes from an account with no write access to the repository, or of
//     another type than User (intake.PullRequestReview.AuthorCanWrite, which the daemon's
//     reviewerCanWrite reads from GitHub before the fact reaches this transaction), decides
//     nothing: on a public repository any account can review, and a bot is an App whose token an
//     agent can hold.
//   - A person's request for changes, delivered by its webhook, refuses the head on the commit_id
//     the event carried, and so decides the round.
//
// No rule turns on the head the record holds, so a refusal stands at every head the record can
// come to hold.
func (e *Engine) RefusesRound(review intake.PullRequestReview) string {
	switch {
	case e.appWrote(review.Author):
		return "the one App wrote it, and no review of the App decides a round"
	case !review.AuthorCanWrite:
		return "its author is no account of type User with write access to the repository"
	}
	return ""
}

func (e *Engine) review(ctx context.Context, tx pgx.Tx, fact intake.PullRequestReview) (intake.Result, error) {
	pr, err := e.pullRequest(ctx, tx, fact.Repo, fact.Number)
	if err != nil || pr == nil {
		return intake.Result{}, err
	}
	issue, err := e.store.Issue(ctx, tx, pr.Issue)
	if err != nil || issue == nil {
		return intake.Result{}, err
	}
	reviewer, err := e.phaseRow(ctx, tx, issue.Key, claim.RoleReviewer)
	if err != nil {
		return intake.Result{}, err
	}
	// No review is the reviewer's answer to a round it left undecided: its answer is a verdict
	// with its completion (handoff). So a review tells the architect only when it changes how the
	// round is stuck.
	before := e.reviewRound(*issue, reviewer, pr)
	// Only a request for changes decides anything (intake.PullRequestReview.Decides), and one
	// judgement says whether it may decide this round (RefusesRound). Any other review orders
	// nothing either, so a comment or an approval written after a decision but delivered before
	// it, or an outsider's review, cannot make the decision look old.
	state := strings.ToLower(fact.State)
	decides := fact.Decides()
	if decides {
		if reason := e.RefusesRound(fact); reason != "" {
			args := []any{"issue", issue.Key, "pull_request", pr.Number, "author", fact.Author, "state", state,
				"commit_id", fact.CommitID, "head", pr.HeadSHA}
			// A request for changes the App wrote is a role acting as a reviewer did before the
			// verdict, which the operator is told of; any other is an account that may not decide.
			if e.appWrote(fact.Author) {
				e.warnOnCommit(ctx, "workflow: a review decides nothing: "+reason, args...)
			} else {
				e.logOnCommit(ctx, "workflow: a review decides nothing: "+reason, args...)
			}
			decides = false
		}
	}
	if !decides {
		_, err := e.settleRound(ctx, tx, *issue, reviewer, pr, before, byReview)
		return intake.Result{}, err
	}
	// Deciding reviews are ordered by when they were submitted, then by GitHub's review id
	// (record.ReviewOrder), so a review submitted before one already processed - redelivered, or
	// from an earlier round - records nothing. The pull request keeps the newest it has had across
	// rounds. A review without an id is ordered by when it arrives.
	if fact.ID != 0 {
		order := fact.Order()
		if !order.After(pr.ReviewSeen) {
			return intake.Result{}, nil
		}
		pr.ReviewSeen = order
		if err := e.putPullRequest(ctx, tx, *pr); err != nil {
			return intake.Result{}, err
		}
	}
	// The decision belongs to the review round, not to the pull request's head: the round must
	// still know what refused it, on which head, and what it said for the next round's
	// implementer. The round is open while the issue stands in reviewing, a hold from reviewing
	// counted as standing there (record.StandsIn, the one shape every question about where an
	// issue stands is written over), since the retry puts it back there with the reviewer's work
	// kept. A review outside the round decides nothing and counts no round. The person's request
	// for changes is recorded on the commit_id its webhook carried, the head it was written on,
	// and leaves the commit the reviewer read where it stands (record.PhaseRow.ReviewedHead),
	// since the reviewer did not read the person's.
	if !record.StandsIn(*issue, phase.Reviewing) {
		return intake.Result{}, nil
	}
	reviewer.Decision = &record.ReviewDecision{State: state, Body: fact.Body, Head: fact.CommitID}
	if err := e.store.PutPhase(ctx, tx, reviewer); err != nil {
		return intake.Result{}, err
	}
	_, err = e.settleRound(ctx, tx, *issue, reviewer, pr, before, byReview)
	return intake.Result{}, err
}

// decisionOf is the round's decision the reviewer's verdict writes on its row (handoff): approve
// as approved and changes as changes_requested, at the commit the verdict names, the head the
// reviewer reviewed, with the summary its completion carried as the body; nil for no verdict,
// which decides nothing and leaves the round stuck (stuckUndecided). The state keeps a GitHub
// review's spelling, which a daemon of the release before the verdict reads in the same column
// (record.ReviewDecision).
func decisionOf(fact intake.HandoffComplete) *record.ReviewDecision {
	var state string
	switch store.VerdictWord(fact.Verdict) {
	case store.VerdictApprove:
		state = "approved"
	case store.VerdictChanges:
		state = "changes_requested"
	default:
		return nil
	}
	return &record.ReviewDecision{State: state, Body: fact.Summary, Head: fact.Commit}
}

// The fact that wrote a review-stuck notice, its summary. The architect prompt
// (prompts/go/architect-common.md) reads a notice the reviewer's completion wrote after the
// architect asked as the reviewer answering, and every other as news about the round; it names
// each of these words, so a change to one changes that prompt too. The retry of a held round is no
// fact of the round's own, so its notice carries the round's facts instead (byRetry).
const (
	byCompletion = "the reviewer's completion"
	byReview     = "a review that is not the reviewer's answer"
	byPush       = "a push"
	byMergeState = "GitHub's merge state for the head"
	byMerge      = "the pull request's merge"
)

// byRetry is the summary of the review-stuck notice the retry of a round held from reviewing
// writes (retryPhase): the round GitHub's answer left stuck while it was held, told for the first
// time at the retry. It names the round's facts, since no fact of the round's own wrote it: the
// head the round is stuck at, the reviewer claim whose verdict decided it, and how long before now
// that verdict came (the reviewer row's completion).
func byRetry(reviewer record.PhaseRow, head string, now time.Time) string {
	given := "with no verdict recorded"
	if !reviewer.CompletedAt.IsZero() {
		since := "less than a minute"
		if waited := now.Sub(reviewer.CompletedAt).Round(time.Minute); waited >= time.Minute {
			since = strings.TrimSuffix(waited.String(), "0s")
		}
		given = "its verdict given " + since + " ago"
	}
	return fmt.Sprintf("the retry of the held round: head %s, reviewer %s, %s", head, reviewer.Claim, given)
}

// roundOutcome is what a review round comes to after a fact (reviewRound).
type roundOutcome int

const (
	// roundOpen is a round still owed its reviewer's completion or its decision, or waiting on the
	// head a push is bringing; or an issue not in reviewing, since one held
	// from reviewing ends only once the retry restores it; or an approval that stands on the head
	// of a pull request closed without merging, which the architect decides (reviewRound).
	roundOpen roundOutcome = iota
	// roundApproved ends the round: the reviewer's approval approves the head's code
	// (classify.ApprovalStands), which GitHub answers for with a merge state the issue may advance
	// on, or with `blocked` once every check run it reported on that head has concluded, or which
	// the pull request has already merged at. No CI verdict is read: the reviewer reads the head's
	// checks on GitHub before it approves, a settlement moves no issue
	// (dispatch://INFRA-714/spec R1), and the merger answers the merge state at merging.
	roundApproved
	// roundRejected ends the round: the reviewer's verdict asked for changes, or a person with
	// write access requested them, whether or not the reviewer has completed (reviewRound).
	roundRejected
	// roundStuck is a round that nothing on its way will end (stuckCause): one its reviewer
	// completed with no verdict, one whose approval is of other code than the head's, or one whose
	// approval GitHub will not merge from.
	roundStuck
)

// stuckCause is why a round that nothing on its way will end is stuck.
type stuckCause int

const (
	// stuckUndecided: its reviewer completed with no verdict. No GitHub review stands in for one:
	// a COMMENT decides nothing, and neither does any review RefusesRound refuses.
	stuckUndecided stuckCause = iota + 1
	// stuckApprovedOtherCode: its approval is of a head whose code the current head may not carry.
	stuckApprovedOtherCode
	// stuckMergeState: its approval approves the head's code, and GitHub answers for that head,
	// whose own checks have all concluded, with a merge state it would not merge from and the round
	// does not end on - behind its base, a draft, or dirty with no second read confirming it.
	// Nothing the daemon does moves it: GitHub's answer is about the head on the branch, and only a
	// push that earns the answer the base wants changes it. The reviewer's verdict has decided the
	// round, so no new verdict of the same head moves it either.
	stuckMergeState
)

// round is reviewRound's account of a review round. A stuck round names its cause, the head it is
// stuck at, the merge state GitHub gave for that head, and what the architect, the reviewer and a
// restarted reviewer are told of it. waitsOnMergeState is the one thing the round is owed from
// outside the record: an approval that stands on the head with GitHub's answer for it not one that
// ends the round, which the daemon reads GitHub for (settleRound, daemon's readMergeState). It is
// set for the round that waits for a first answer and for the stuck one alike, since the state
// GitHub gives a head changes under it - a base push clears `behind`, and the head's last check
// concluding makes `blocked` a state the round ends on - and a read is the only thing that would
// ever say so. Which round that is, is classify.RoundWaitsOnMergeState: the engine and the state
// build read that one rule, so the issue's view carries a mergeWait exactly for the rounds the
// daemon reads GitHub for.
type round struct {
	outcome           roundOutcome
	cause             stuckCause
	head              string
	state             record.MergeState
	reason            string
	waitsOnMergeState bool
}

// stuckAs says whether r is stuck in the same way as other: the same cause at the same head. A
// fact that is not the reviewer's own tells only when the round is stuck in a new way, so a
// second push classified late at the same head tells nothing. It is read for the two causes a
// review decides - the round nothing decided, and the approval of other code - and not for a
// round GitHub will not merge from, which is deduped against the mark the notice leaves on the
// decision instead (settleRound, record.ReviewDecision.ToldStuckHead): other is recomputed from
// the record as the fact found it, so it remembers nothing of an answer GitHub gave and took
// back, and `behind`, `unknown`, `behind` would tell twice.
func (r round) stuckAs(other round) bool {
	return r.outcome == roundStuck && other.outcome == roundStuck &&
		r.cause == other.cause && r.head == other.head
}

// stuckMergeStateReason is what the architect and the reviewer are told of an approval standing
// on a head GitHub answers for with a merge state it would not merge from and the round does not
// end on - `behind`, `draft`, or a `dirty` no second read has confirmed (reviewRound): the head,
// the pull request, the state in GitHub's own spelling, every check run GitHub reported on that
// head with the conclusion it came to, and every commit status the record keeps for that head
// with its state (headChecks), which is the whole of what the daemon knows of the head's own CI.
// There is always a run to name: the only caller builds this reason for a round it has already
// reported a head for, which it does only once GitHub has reported a check run on that head itself
// and every one of them has concluded (reviewRound, classify.ChecksConcludedOnHead), and that gate
// and headChecks read the same runs of the fence (classify.HeadRuns), so headChecks names at least
// the run the gate found.
//
// It names the head's own check runs rather than everything the settlement standing for the head
// reports, which can be the code head's below it: GitHub weighs the current head for the merge,
// so a name whose newest run ran on another head of the tree says nothing about why this head is
// held, and naming it here would read as a run GitHub made on this one.
//
// It ends with what ends the round at that head (classify.RoundWaitsOnMergeState): GitHub
// answering a state it would merge from, or `blocked` with every check run it reported there
// concluded. A `blocked` head is never told of here: once its own checks have concluded its
// approval ends the round, and the merger answers that state at merging, and until then the round
// waits. It says nothing of what clears the state: a push that earns what the base requires is one
// way, and marking a draft ready for review is another.
func stuckMergeStateReason(pr record.PullRequest) string {
	return fmt.Sprintf("the reviewer approved head %s on pull request #%d, and GitHub reports that head %s rather than one it would merge. %s. The issue waits in reviewing until GitHub reports that head clean, unstable or has_hooks, or blocked with every check run it reported there concluded",
		pr.HeadSHA, pr.Number, pr.MergeState, headChecks(pr))
}

// reviewRound is what issue's review round comes to, row being its reviewer's and pr its pull
// request as a fact left them. The reviewer's verdict decides it, which its completion of reviewing
// carries and the daemon records as the round's decision at the commit it names (handoff,
// decisionOf), and a person's request for changes ends it too, whether or not the reviewer has
// completed: the reviewer's completion that follows is recorded on its own row (engine's
// leftItsPhase). An approval ends it once it approves the head's code (classify.ApprovalStands)
// and the merge state the record holds for that head is one the round ends on: a state GitHub
// would merge from (record.MergeState.Mergeable), or `blocked` once GitHub has reported a check run
// on that head itself and every one of them has concluded (classify.ChecksConcludedOnHead).
// `blocked` ends it because, with the default steps, legion-verdict is a check the base requires
// that waits for the owner's acceptance, which comes after the round: a round that waited for
// GitHub to answer that the head can merge would wait for itself (spec R4). The daemon reads no
// ruleset, so it cannot tell a missing legion-verdict from another required check or review that
// holds the head `blocked`, and the merger answers that state at merging. An approval ends it too
// once the pull request has merged at that head: a merged pull request has nothing left to merge,
// GitHub answers `unknown` for it for good, and the daemon never reads its state again
// (askMergeStateFor), so nothing it could wait on would ever come.
// An approval of a head the record holds in a confirmed conflict with its base
// (classify.ConflictRound) ends the round too, whatever GitHub's merge state for it
// (dispatch://INFRA-4374 R0): nothing GitHub answers about that head changes until the base is
// merged forward, and that merge is the merger's once the issue reaches merging, so the move into
// merging asks the merger for it (transition, mergeForwardOnEntry, record.ConflictMergeNext,
// cybermonklabs/legion#4979). While the first conflicting read of the head waits for its second
// (classify.FirstConflictReadWaits), the approval waits where it stands, whatever state GitHub
// answers, and is never reported stuck: GitHub's `dirty` is not yet a conflict the daemon holds,
// and the second read decides it.
// An approval that stands on the head of a pull request closed without merging keeps the round
// open: the architect has the pr-closed-unmerged notice and decides what comes next, and a
// reopen asks GitHub for the head's merge state again (the opened handler, then askMergeState).
// An approval that stands on an open pull request's head with any other answer waits on GitHub's
// next answer about that head, which is classify.RoundWaitsOnMergeState - the rule the state
// build reads too, so the two never drift. An approval with no merge state on record for its
// head - no read of the head on record, or a read whose answer carried no state
// (record.PullRequest.StateReadAt) - or with `unknown` while GitHub computes it, waits where it
// stands, and the read that lands moves it (mergeability, the ladder behind daemon's
// readMergeState). So does an approval
// whose head still has a check running: a check the base requires holds the head `blocked` while
// it runs, which says nothing about the round. What tells the two apart is the head's own check
// runs (classify.ChecksConcludedOnHead): the round ends on `blocked`, or reports a head stuck,
// only once GitHub has reported a check run on that head itself and every one of them has
// concluded. A settlement of an earlier head never satisfies it, and must not: the push that left
// this head starts CI of its own, and GitHub answers `blocked` while it runs. Read off an earlier
// head's settlement, the round would end on the head before its own checks had run, or a stuck
// head be marked told before the checks that would clear it had finished.
// An approval GitHub answers with any other state it would not merge from - `behind`, `draft`, or
// a `dirty` whose first read no longer waits and no second read confirmed - on a head whose own
// checks have all concluded, is stuck (stuckMergeState): the architect and the reviewer are told,
// once for each head and state (record.ReviewDecision.ToldStuckHead), since the checks that would
// have cleared it are over and nothing else is on its way to move the issue.
// The round reads no CI verdict: what a check concluded moves nothing and
// decides no round - only whether every check reported has concluded, which is what GitHub's own
// state is waiting for - and the reviewer reads the head's checks on GitHub itself. A completed round
// that nothing on its way would end is stuck, its reason naming the
// head, since the decision it needs is of the head: its reviewer completed with no verdict
// (stuckUndecided), or its approval is of other code than the head's (stuckApprovedOtherCode). An
// approval of a head that code is on its way from keeps the round open, and classify.CodeOnItsWay
// says when that is.
func (e *Engine) reviewRound(issue record.Issue, row record.PhaseRow, pr *record.PullRequest) round {
	if issue.Phase != phase.Reviewing {
		return round{}
	}
	completed := row.Completed()
	decided := ""
	if row.Decision != nil {
		decided = row.Decision.State
	}
	if decided == "changes_requested" {
		return round{outcome: roundRejected}
	}
	if pr == nil {
		return round{}
	}
	switch {
	case decided == "approved" && classify.ApprovalStands(*pr, row.Decision.Head):
		switch pr.State {
		case record.PullRequestClosed:
			// A closed pull request merges nothing until a reopen, and the architect decides what
			// comes of it (the pr-closed-unmerged notice), so its approval ends no round.
			return round{}
		case record.PullRequestMerged:
			// A merged pull request has nothing left to merge, and nothing the round could wait
			// on ever comes, so its approval ends the round.
			return round{outcome: roundApproved}
		}
		// Only an open pull request is left, and only its round can wait on GitHub's answer. An
		// approval whose head GitHub answers a state the round ends on - one it would merge from,
		// or `blocked` with every check run of the head concluded - ends it here, and so does an
		// approval of a head held in a confirmed conflict, which waits on no answer of GitHub's,
		// since none changes until the base is merged forward: classify.RoundWaitsOnMergeState
		// holds for neither, whatever the merge state of the second.
		if !classify.RoundWaitsOnMergeState(issue, row, pr) {
			return round{outcome: roundApproved}
		}
		// The approval stands on the head of an open pull request and GitHub's answer for that
		// head is not one the round ends on, so the round waits on GitHub's answer for it: the
		// unread state and `unknown` because GitHub has given none yet, every other because the one
		// it gave can change under the head, a head GitHub has reported no concluded check run on
		// because a check still running there is what the state is waiting for, and a head whose
		// first conflicting read waits for its second because GitHub's `dirty` is no conflict until
		// that read confirms it (mergeability), which then ends the round, or releases it. The
		// daemon reads for each (settleRound), and the settlement that ends the last check is itself
		// a fact that asks for a read. Which approval waits is the rule the state build reads too
		// (classify.RoundWaitsOnMergeState), so the view carries a mergeWait for exactly this round.
		if pr.MergeState == record.MergeStateUnread || pr.MergeState == record.MergeStateUnknown ||
			!classify.ChecksConcludedOnHead(*pr) || classify.FirstConflictReadWaits(*pr) {
			return round{waitsOnMergeState: true}
		}
		return round{outcome: roundStuck, cause: stuckMergeState, head: pr.HeadSHA, state: pr.MergeState,
			waitsOnMergeState: true, reason: stuckMergeStateReason(*pr)}
	case !completed:
		return round{}
	case decided == "":
		return round{outcome: roundStuck, cause: stuckUndecided, head: pr.HeadSHA, reason: undecidedReason(*pr)}
	case classify.ApprovalSetAside(*pr, row.Decision.Head):
		return round{outcome: roundStuck, cause: stuckApprovedOtherCode, head: pr.HeadSHA,
			reason: fmt.Sprintf("the reviewer approved %s on pull request #%d, which does not approve head %s: a push since may have changed code, so only %s or a person's request for changes ends the round",
				row.Decision.Head, pr.Number, pr.HeadSHA, verdictThatEnds(pr.HeadSHA))}
	}
	return round{}
}

// undecidedReason is what the architect is told of a round its reviewer completed with no verdict
// (stuckUndecided), as a reviewer before the verdict completed: what would end the round, and what
// does not, so a reader looking at an approval on GitHub does not wait for it to count.
func undecidedReason(pr record.PullRequest) string {
	return fmt.Sprintf("the reviewer completed its round on pull request #%d with no verdict: only %s or a person's request for changes ends the round; no GitHub review written with the one App's token, no person's approval and no COMMENT decides it",
		pr.Number, verdictThatEnds(pr.HeadSHA))
}

// verdictThatEnds is what a stuck round's reason names as the reviewer's way to end it: a verdict
// of head, sent with a completion of reviewing that names a handoff record of its own (the
// workflow refuses one naming the record the reviewer already named, HANDOFF_NOT_NEW).
func verdictThatEnds(head string) string {
	return fmt.Sprintf("the reviewer's verdict of head %s - its completion of reviewing with verdict approve or changes, commit %s and a handoff record written since -", head, head)
}

// implementationReady moves issue from implementing to testing on the implementer's completion of
// the round that row records, once the pull request pr is recorded open or merged
// (TriggerImplementationReady: a record closed without merging, or none, moves nothing). It
// reads no CI result: a settlement that arrived while the implementer worked moved nothing and
// moves nothing now, since the roles were told what it said when it arrived (checks,
// dispatch://INFRA-714/spec R1).
func (e *Engine) implementationReady(ctx context.Context, tx pgx.Tx, issue record.Issue, row record.PhaseRow, pr *record.PullRequest) error {
	return e.transition(ctx, tx, issue, TriggerImplementationReady, "", row, pr, "")
}

// settleRound acts on what issue's review round comes to (reviewRound, with row its reviewer's and
// pr its pull request as the fact left them), and says whether it moved the issue. An ended round
// moves on, its request for changes back to implementing; a stuck round is told to the architect,
// in a notice whose summary is by, the fact that wrote it - unless it was told already. For the
// two causes the reviewer's verdict decides, that is the round having been stuck the same way (stuckAs) before
// that fact, which is before: each caller passes the round as its own fact found it, so a round
// stuck at a head the architect was told of is told once and no fact about the same head tells it
// again. The issue stays in reviewing, and the notice names the reviewer's role topic, which the
// architect asks the decision on. Linger holds a member of a closed tree where it stood
// (record.TreeLingers), and leaves no mark: nothing was told, so the head is told when the tree
// runs again.
//
// A round GitHub will not merge from (stuckMergeState) is the one cause the reviewer cannot
// decide, and the whole of its path - its own dedup, its own message to the reviewer, a notice
// with no topic - is tellStuckMergeState's, which this asks for once.
func (e *Engine) settleRound(ctx context.Context, tx pgx.Tx, issue record.Issue, row record.PhaseRow, pr *record.PullRequest, before round, by string) (bool, error) {
	r := e.reviewRound(issue, row, pr)
	switch r.outcome {
	case roundRejected:
		if lingers, err := record.TreeLingers(ctx, e.store, tx, issue.Tree); err != nil || lingers {
			return false, err
		}
		// The implementer's task names the reviewer's verdict only where that verdict is what
		// rejected the round: a person's request for changes can reject a round the reviewer
		// approved, and an approve beside that request's reason would tell the implementer the
		// opposite of what sent the work back.
		handoff := row
		own, err := e.reviewerVerdictAt(ctx, tx, issue, pr, row.Decision.Head)
		if err != nil {
			return false, err
		}
		if own != store.VerdictChanges {
			handoff.Decision = nil
		}
		return true, e.transition(ctx, tx, issue, TriggerReviewRejected, "", handoff, pr, row.Decision.Body)
	case roundApproved:
		return true, e.transition(ctx, tx, issue, TriggerReviewApproved, "", row, pr, "")
	case roundStuck:
		if r.cause == stuckMergeState {
			return false, e.tellStuckMergeState(ctx, tx, issue, row, r, by)
		}
		// Every other cause is deduped against the round as this fact found it, which the record
		// answers for.
		if r.stuckAs(before) {
			return false, nil
		}
		if lingers, err := record.TreeLingers(ctx, e.store, tx, issue.Tree); err != nil || lingers {
			return false, err
		}
		reviewer, err := reviewerToken(issue)
		if err != nil {
			return false, err
		}
		return false, e.notice(ctx, tx, issue.Key, record.Notice{Kind: "review-stuck", Role: claim.RoleReviewer, Phase: phase.Reviewing,
			Summary: by, Reason: r.reason, Topic: notify.RoleTopicPrefix + string(reviewer)})
	}
	return false, nil
}

// tellStuckMergeState tells the architect and the reviewer of the one stuck cause a review
// cannot decide: an approval of the head's code that GitHub answers for with a merge state it
// would not merge from (stuckMergeState). It is the whole of that cause's path through
// settleRound, which the rest of the function shares nothing with but the notice it ends in -
// the dedup is against another thing, the reviewer is told as well as the architect, and the
// notice names no topic - so settleRound asks which cause this is once and leaves it here.
//
// It is deduped against the mark the last notice left on the reviewer's decision (told), which
// outlives the answers GitHub gives in between: the state is read back from GitHub every time,
// and `behind`, `unknown`, `behind` is one finding told once. The mark is written with the
// notice, so a notice linger holds (record.TreeLingers) leaves none and the head is told when
// the tree runs again.
//
// The reviewer's own message is a role message (record.RoleMessage, the way the daemon reaches a
// role holder with something to read): the reviewer approved that head and would otherwise watch
// a round it decided go nowhere, and it is the role reading the head's checks on GitHub, which
// is where what the notice names is answered. The architect's notice names no role topic, since
// no decision of the reviewer's would move this round.
func (e *Engine) tellStuckMergeState(ctx context.Context, tx pgx.Tx, issue record.Issue, row record.PhaseRow, r round, by string) error {
	if told(row, r) {
		return nil
	}
	if lingers, err := record.TreeLingers(ctx, e.store, tx, issue.Tree); err != nil || lingers {
		return err
	}
	reviewer, err := reviewerToken(issue)
	if err != nil {
		return err
	}
	if err := e.enqueue(ctx, tx, issue.Key, record.RoleMessage{Role: string(reviewer),
		Body: "The round you approved is stuck: " + r.reason + "."}); err != nil {
		return err
	}
	row.Decision.ToldStuckHead, row.Decision.ToldStuckState = r.head, r.state
	if err := e.store.PutPhase(ctx, tx, row); err != nil {
		return err
	}
	return e.notice(ctx, tx, issue.Key, record.Notice{Kind: "review-stuck", Role: claim.RoleReviewer, Phase: phase.Reviewing,
		Summary: by, Reason: r.reason})
}

// reviewerToken is the claim token of issue's reviewer, whose role topic the architect is given
// to ask the decision on and whose role a message addresses.
func reviewerToken(issue record.Issue) (claim.Token, error) {
	project, err := claim.ProjectToken(issue.Project)
	if err != nil {
		return "", err
	}
	return claim.NewToken(project, issue.Key, claim.RoleReviewer)
}

// told says whether the notice a round GitHub will not merge from would write has already gone
// out: the reviewer's decision carries the head and state it was last reported stuck on
// (record.ReviewDecision.ToldStuckHead). It answers for that cause alone, which is the only
// caller (tellStuckMergeState); every other is deduped against the round as the fact found it
// (stuckAs), which the record answers for them.
func told(row record.PhaseRow, r round) bool {
	return row.Decision != nil &&
		row.Decision.ToldStuckHead == r.head && row.Decision.ToldStuckState == r.state
}

// askMergeState asks the daemon to read GitHub's merge state for the pull request fact is of,
// when the fact owes that read (askMergeStateFor, Config.ReadMergeState): the round the fact has
// just been applied to leaves waiting on GitHub's answer for the head (round.waitsOnMergeState),
// or the fact moves the head of an issue in implementing through merging, which a conflict with
// the base needs read (the gate's reason, classify.MergeStateReadOwed, dispatch://INFRA-4374 R0).
// It is the engine's own account of what the round is owed rather than a second reading of
// the record beside it, and it runs on the commit, so the fact's transaction waits for no GitHub
// call.
//
// Every fact that can change GitHub's answer asks, and the list is this function's switch rather
// than a sentence in a comment and nine calls spread through the handlers: the pull request's own
// events, a push to it, a review, the reviewer's verdict, a CI settlement, and a push to the base
// it sits on. The switch
// also names which of them move the head (classify.MergeStateFactHeadMove): the pull request
// opened or reopened, and a synchronize, which is the fact that moves the head a push to the
// branch carries (its case says why the push is not one). Apply calls it once, after the handler,
// so a handler's own early return cannot drop the read and no handler recomputes the round
// settleRound has just computed (dispatch://INFRA-714 R3). GitHub's own answer does not ask - the
// fact a read applies is mergeability, and a read that asked for another would read the same head
// for as long as GitHub kept answering the same way. What a read answers is the ladder's to decide
// (daemon's mergeStateLadder), and the next fact of the issue asks again.
//
// The switch also names the head whose verdict a fact can change, which has legion-verdict written
// before any read (Config.WriteVerdict, askVerdictWrite): the reviewer's verdict, at the commit its
// completion of reviewing names, and a review step's verdict past review, at the commit it names
// (reviewVerdict); and a webhook's approval or request for changes not written with the one App's
// token, at the commit it read. Two more facts ask for that write in their own handlers, and owe no
// read, since nothing GitHub answers waits on them: the owner's acceptance, at the head the record
// holds (recordAcceptance), and a check state at the head the pull request stands at (checkState).
// No other fact changes a verdict, and a push writes nothing on the head it makes: a check run
// belongs to one commit, so the new head has no legion-verdict until every step's pass names it
// (spec R4).
//
// A base push is the one fact of many pull requests, and the one this leaves alone: its handler
// walks the open pull requests recorded on the base once, and asks for each read inside that
// walk (basePush), since the record it overwrites as it goes is what tells a redelivered push
// from a first delivery.
func (e *Engine) askMergeState(ctx context.Context, tx pgx.Tx, fact intake.Fact) error {
	if e.cfg.ReadMergeState == nil && e.cfg.WriteVerdict == nil {
		return nil
	}
	var pr *record.PullRequest
	var err error
	kind, verdict := classify.MergeStateFactOther, ""
	switch fact := fact.(type) {
	case intake.PullRequestOpened:
		pr, err = e.pullRequestByBranch(ctx, tx, fact.Repo, fact.Branch)
		kind = classify.MergeStateFactHeadMove
	case intake.PullRequestSynchronized:
		pr, err = e.pullRequestByBranch(ctx, tx, fact.Repo, fact.Branch)
		kind = classify.MergeStateFactHeadMove
	case intake.Push:
		// The push moves no head of the record: its synchronize, from GitHub or from the per-issue
		// read (daemon's followHead), is the fact that moves the head, and the one a head move is
		// read for. So the push asks only as a review or a settlement does, for a round it leaves
		// waiting on GitHub, and one push to a branch asks for no ladder beside its synchronize's.
		pr, err = e.pullRequestByBranch(ctx, tx, fact.Repo, fact.Branch)
	case intake.PullRequestChecks:
		pr, err = e.pullRequest(ctx, tx, fact.Repo, fact.Number)
	case intake.PullRequestReview:
		pr, err = e.pullRequest(ctx, tx, fact.Repo, fact.Number)
		if fact.Deciding() && !e.appWrote(fact.Author) {
			verdict = fact.CommitID
		}
	case intake.HandoffComplete:
		// The reviewer's verdict decides the round with its completion, so its approval is the
		// fact that leaves the round waiting on GitHub's answer for the head; no other role's
		// completion changes what GitHub answers or a verdict: the tester's carries none, since
		// Dispatch's check states at the tested head decide the testing phase.
		if fact.Role != claim.RoleReviewer {
			return nil
		}
		if fact.Verdict != "" {
			verdict = fact.Commit
		}
		pr, err = e.store.PullRequest(ctx, tx, fact.Issue)
	case intake.ReviewVerdict:
		// The reviewer's verdict past review is the verdict of the head it names (reviewVerdict).
		pr, err = e.store.PullRequest(ctx, tx, fact.Issue)
		verdict = fact.Commit
	case intake.BasePush:
		// Asked for inside the walk that records the push, which is the last reader of what
		// tells a redelivery from a first delivery (basePush).
		return nil
	default:
		return nil
	}
	if err != nil || pr == nil {
		return err
	}
	if err := e.askVerdictWrite(ctx, tx, pr, verdict); err != nil {
		return err
	}
	return e.askMergeStateFor(ctx, tx, pr, kind)
}

// askVerdictWrite asks the daemon, on the commit, to write legion-verdict on head of pr
// (Config.WriteVerdict), "" asking nothing. Only an open pull request of an issue of the project's
// own is asked for: a closed one merges nothing, and the record a fact finds by its repository and
// number can be another project's issue's. This daemon holds none of that issue's steps, so no
// verdict stands at its head and no fact of it gives anything to write; the writers' sweep writes
// failure over the one App's runs and statuses there. It is registered before askMergeStateFor
// registers the read on the same commit, and commit hooks run in order (intake.WithCommitHooks), so
// the daemon has the write pending when the read is asked for, and holds the read until the write
// ends.
func (e *Engine) askVerdictWrite(ctx context.Context, tx pgx.Tx, pr *record.PullRequest, head string) error {
	if e.cfg.WriteVerdict == nil || head == "" || pr.State != record.PullRequestOpen {
		return nil
	}
	issue, err := e.store.Issue(ctx, tx, pr.Issue)
	if err != nil || issue == nil {
		return err
	}
	key, repo, number := issue.Key, pr.Repo, pr.Number
	store.OnCommit(ctx, func() { e.cfg.WriteVerdict(key, repo, number, head) })
	return nil
}

// askMergeStateFor queues the read for one pull request when a fact of kind fact asks for it, by
// one of the gate's two reasons (classify.MergeStateReadOwed: a head move of an issue in
// implementing through merging, a base push of an issue in testing through merging) or by the
// third, which the gate does not hold: the issue's review round waiting on GitHub's answer for the
// head (reviewRound's waitsOnMergeState), or ended by an approval the fact did not move on,
// worked out here only for a fact the gate owes no read for. The guard on a daemon with no reader
// is here as well as in askMergeState, because a base push reaches this from its handler's walk
// rather than through that switch (basePush), and that walk is the one caller that names
// classify.MergeStateFactBasePush. A pull request that is not open is read for nothing: it merges
// nothing until a reopen, which asks again, and its round waits on no answer
// (classify.RoundWaitsOnMergeState holds only for an open one), so the test of its state here
// saves the store reads below.
func (e *Engine) askMergeStateFor(ctx context.Context, tx pgx.Tx, pr *record.PullRequest, fact classify.MergeStateFact) error {
	if e.cfg.ReadMergeState == nil || pr.State != record.PullRequestOpen {
		return nil
	}
	issue, err := e.store.Issue(ctx, tx, pr.Issue)
	if err != nil || issue == nil {
		return err
	}
	owed := classify.MergeStateReadOwed(*issue, fact)
	if !owed {
		// The round's own answer matters only where the fact and the phase owe no read: every
		// phase a round can wait on GitHub in already owes one on a head move or a base push. A
		// round the fact left ended and unmoved owes one too: a CI settlement moves no issue
		// (checks), so the settlement that concludes the head's last check under a recorded
		// `blocked` leaves in reviewing an approval the round now ends on, and GitHub's answer for
		// the head, given with its checks concluded, is the fact that ends it (mergeability, then
		// settleRound).
		reviewer, err := e.phaseRow(ctx, tx, issue.Key, claim.RoleReviewer)
		if err != nil {
			return err
		}
		r := e.reviewRound(*issue, reviewer, pr)
		owed = r.waitsOnMergeState || r.outcome == roundApproved
	}
	if !owed {
		return nil
	}
	key := issue.Key
	store.OnCommit(ctx, func() { e.cfg.ReadMergeState(key) })
	return nil
}

// reviewStep is one review step of an issue's task, a step of the type review (plan.TypeReview):
// its place, the phase it runs as, and its agent (stepAgent), "" where none is known. In this
// release the review steps are the reviewer's and the owner's acceptance (plan.BuiltinRoles).
type reviewStep struct {
	place record.StepPlace
	at    phase.Phase
	agent claim.Token
}

// reviewSteps is each review step of w's task, the walk of issue's run, in the task's order, with
// its agent as store holds it in tx (stepAgentOf).
func reviewSteps(ctx context.Context, store record.Store, tx pgx.Tx, issue record.Issue, w Walk) ([]reviewStep, error) {
	var steps []reviewStep
	for i, step := range w.Steps {
		if step.Type != plan.TypeReview || i >= len(w.Line) {
			continue
		}
		place := record.TaskPlace{Kind: record.PlaceStep, Position: i + 1}.StepPlace(w.Task)
		agent, err := stepAgentOf(ctx, store, tx, issue, place, w.Line[i])
		if err != nil {
			return nil, err
		}
		steps = append(steps, reviewStep{place: place, at: w.Line[i], agent: agent})
	}
	return steps, nil
}

// stepAgent is the agent of the step at place of issue's task, which runs as at, on e's store
// (stepAgentOf).
func (e *Engine) stepAgent(ctx context.Context, tx pgx.Tx, issue record.Issue, place record.StepPlace, at phase.Phase) (claim.Token, error) {
	return stepAgentOf(ctx, e.store, tx, issue, place, at)
}

// stepAgentOf is the agent of the review step at place of issue's task, which runs as at, as store
// holds it in tx: the claim the latest start of that place records (record.StepStart.Agent: the
// start writes the phase worker's claim, stepClaim, and for the owner's acceptance the architect
// that owns the issue, acceptingAgent). A reviewer's step no start recorded an agent of started
// under the release before the daemon recorded each step's start, and can still be at work when
// this release deploys, so its agent is the issue's reviewer claim, the claim that step's role runs
// under. Any other step no start recorded has no agent, "".
func stepAgentOf(ctx context.Context, store record.Store, tx pgx.Tx, issue record.Issue, place record.StepPlace, at phase.Phase) (claim.Token, error) {
	start, err := store.StepStarts(ctx, tx, issue.Key, place)
	if err != nil || start.Agent != "" || at != phase.Reviewing {
		return claim.Token(start.Agent), err
	}
	return stepClaim(issue, issue.Key, claim.RoleReviewer)
}

// stepOfAgent is the review step of steps whose record names agent its agent, and false where none
// does. No claim is the agent of two steps of one task: each step's agent is its own role's claim.
func stepOfAgent(steps []reviewStep, agent claim.Token) (reviewStep, bool) {
	i := slices.IndexFunc(steps, func(step reviewStep) bool { return step.agent != "" && step.agent == agent })
	if i < 0 {
		return reviewStep{}, false
	}
	return steps[i], true
}

// agentsOf names the agent of each of steps, for a refusal that says whose verdict the daemon
// takes, "no claim yet" for a step with none.
func agentsOf(steps []reviewStep) string {
	if len(steps) == 0 {
		return "its task has no review step"
	}
	named := make([]string, 0, len(steps))
	for _, step := range steps {
		agent := string(step.agent)
		if agent == "" {
			agent = "no claim yet"
		}
		named = append(named, fmt.Sprintf("step %d, run in %s: %s", step.place.Place, step.at, agent))
	}
	return "its review steps' agents are " + strings.Join(named, "; ")
}

// passed says whether step has passed on w, the walk of issue's run: its place stands before the
// place issue stands at, as every step's does in the retro and the merge. The step issue stands in
// has not passed: its own agent's completion or request decides it at the head it stands at.
func (step reviewStep) passed(w Walk, issue record.Issue) bool {
	stands, ok := w.PlaceOf(record.StandsAt(issue))
	if !ok {
		return false
	}
	return stands.Kind != record.PlaceStep || step.place.Place < stands.Position
}

// reviewStepRefusal is the refusal of fact, a completion that carries a verdict, where the step
// issue stands in is a review step whose agent is another claim than fact's (stepCallerRefusal).
func (e *Engine) reviewStepRefusal(ctx context.Context, tx pgx.Tx, issue record.Issue, fact intake.HandoffComplete) (*intake.Refusal, error) {
	return e.stepCallerRefusal(ctx, tx, issue, fact.Claim, "completion")
}

// stepCallerRefusal is the refusal of call, a request by the claim by that carries the verdict of
// the step issue stands in, where that step is a review step (plan.TypeReview) whose agent
// (stepAgent) is another claim: VERDICT_CALLER_FORBIDDEN (spec R5: the daemon takes a review step's
// verdict only from the claim that runs that step, as its step record names it). The call is a
// review step's completion (reviewStepRefusal), or the owner's sign_off or request_backward_move
// out of accepting, its acceptance's approve or changes (signOff, backward), which review_verdict
// holds to the same record past review (reviewVerdict, stepOfAgent). A step with no agent names no
// claim the verdict may come from. nil where by is the step's agent, or the issue stands in no review
// step.
//
// The record names the agent the step's start wrote (startAgent): for the owner's acceptance, the
// architect that owned the issue when accepting started (acceptingAgent). A change of owner after
// that start, as when an operator's suspension or a failure of that architect's claim moves the
// owner walk to an architect above it, writes no start, so the new owner's call is refused here as
// its review_verdict is, and the recorded agent's call is taken once its claim is back. Only a
// later start of accepting names another agent.
func (e *Engine) stepCallerRefusal(ctx context.Context, tx pgx.Tx, issue record.Issue, by claim.Token, call string) (*intake.Refusal, error) {
	w, err := e.walk(ctx, tx, issue)
	if err != nil {
		return nil, err
	}
	place, role, typ, ok := placeOnWalk(w, issue.Phase)
	if !ok || typ != plan.TypeReview {
		return nil, nil
	}
	agent, err := e.stepAgent(ctx, tx, issue, place, issue.Phase)
	if err != nil || agent == by {
		return nil, err
	}
	named := "no claim"
	if agent != "" {
		named = "claim " + string(agent)
	}
	return &intake.Refusal{Status: 403, Code: "VERDICT_CALLER_FORBIDDEN", Message: fmt.Sprintf(
		"the verdict of step %d of %s, the %s's, is taken only from the claim its step record names as its agent, %s, and this %s is claim %s's; nothing was recorded",
		place.Place, issue.Key, role, named, call, by)}, nil
}

// acceptanceHeadRefusal is the refusal of the owner's call from accepting, what (sign_off or its
// request_backward_move), that names commit where the record holds issue's pull request pr at
// another head: VERDICT_HEAD_NOT_CURRENT (spec R5: the owner's acceptance is its verdict at the
// head it names, and a head the pull request has moved off, by a merge forward among others, is
// code the call does not accept). It is judged under the fact's lock, before the call records or
// moves anything, so a refused call changes nothing. nil where commit is the current head, and
// where the record holds no pull request: there is no head to judge, and no verdict is recorded.
func acceptanceHeadRefusal(issue record.Issue, pr *record.PullRequest, what, commit string) *intake.Result {
	if pr == nil || commit == pr.HeadSHA {
		return nil
	}
	named := "names " + commit
	if commit == "" {
		named = "names no commit"
	}
	result := refused("VERDICT_HEAD_NOT_CURRENT", fmt.Sprintf(
		"this %s of %s %s, and the head of its pull request #%d is %s: read that head and call %s again naming it; nothing was recorded",
		what, issue.Key, named, pr.Number, pr.HeadSHA, what))
	return &result
}

// recordAcceptance records the owner's acceptance as the verdict of its review step (spec R5):
// by's sign_off as approve, and its request_backward_move as changes, word either, at commit, the
// head the owner accepted or sent back. Its caller has held by to the claim the step's record names
// (stepCallerRefusal) and commit to the pull request's current head (acceptanceHeadRefusal). It
// asks, on that commit, for the write of legion-verdict there (askVerdictWrite): with the default
// steps the acceptance is the last review step to pass, so its approve is what writes success, and
// its changes refuse the head (spec R4). An issue that does not stand in accepting, a request that
// names no claim, a pull request not recorded, and a tree that lingers, whose request changes
// nothing, record nothing and ask nothing.
func (e *Engine) recordAcceptance(ctx context.Context, tx pgx.Tx, issue record.Issue, by claim.Token, word store.VerdictWord, pr *record.PullRequest, commit string) error {
	if issue.Phase != phase.Accepting || by == "" || pr == nil || commit == "" {
		return nil
	}
	if lingers, err := record.TreeLingers(ctx, e.store, tx, issue.Tree); err != nil || lingers {
		return err
	}
	if err := e.recordStepVerdict(ctx, tx, issue, pr, phase.Accepting, by, string(word), commit); err != nil {
		return err
	}
	return e.askVerdictWrite(ctx, tx, pr, commit)
}

// recordStepVerdict records the verdict of the review step issue runs at at on the walk of its run,
// by the claim by, on pr at commit: the row the writer of legion-verdict reads a Legion head's review
// step's verdict from (store.Verdict, keyed by the step's task and place). The round's decision
// (record.PhaseRow.Decision) cannot be that row, since a person's request for changes replaces it,
// and a person's later approval of the same head lifts that refusal and leaves the reviewer's
// approve to write success again (spec R4). A pull request not recorded yet records nothing, since
// there is no head to write on, and neither does a phase the walk runs no review step at.
func (e *Engine) recordStepVerdict(ctx context.Context, tx pgx.Tx, issue record.Issue, pr *record.PullRequest, at phase.Phase, by claim.Token, word, commit string) error {
	if pr == nil {
		return nil
	}
	w, err := e.walk(ctx, tx, issue)
	if err != nil {
		return err
	}
	place, _, typ, ok := placeOnWalk(w, at)
	if !ok || typ != plan.TypeReview {
		return nil
	}
	return e.recordVerdict(ctx, tx, issue.Key, pr, place, by, word, commit)
}

// recordVerdict records by's verdict word of the review step at place of issue's task on pr at
// commit (recordStepVerdict, reviewVerdict).
func (e *Engine) recordVerdict(ctx context.Context, tx pgx.Tx, issue string, pr *record.PullRequest, place record.StepPlace, by claim.Token, word, commit string) error {
	repository, err := ghrepo.Parse("the repository of "+issue+"'s pull request", pr.Repo)
	if err != nil {
		return err
	}
	_, err = store.RecordVerdictTx(ctx, tx, store.Verdict{Repository: repository, Number: pr.Number, Head: commit, Issue: issue,
		Task: place.Task, Step: place.Place, Verdict: store.VerdictWord(word), Session: string(by), RecordedAt: e.now()})
	return err
}

// reviewerVerdictAt is the verdict issue's reviewer's step recorded on pr at commit
// (recordStepVerdict), "" where none is recorded, pr is not recorded yet, or the walk runs no step
// in reviewing. A round's decision can be a person's request for changes, which replaces the
// reviewer's (record.PhaseRow.Decision), so the move a rejected round makes reads this row to tell
// whether the reviewer's own verdict rejected it (settleRound).
func (e *Engine) reviewerVerdictAt(ctx context.Context, tx pgx.Tx, issue record.Issue, pr *record.PullRequest, commit string) (store.VerdictWord, error) {
	if pr == nil || commit == "" {
		return "", nil
	}
	w, err := e.walk(ctx, tx, issue)
	if err != nil {
		return "", err
	}
	place, _, _, ok := placeOnWalk(w, phase.Reviewing)
	if !ok {
		return "", nil
	}
	repository, err := ghrepo.Parse("the repository of "+issue.Key+"'s pull request", pr.Repo)
	if err != nil {
		return "", err
	}
	given, found, err := store.StepVerdictTx(ctx, tx, repository, pr.Number, commit, place.Task, place.Place)
	if err != nil || !found || given.Issue != issue.Key {
		return "", err
	}
	return given.Verdict, nil
}

// recordPass counts task one more pass through the implementing, testing and reviewing stretch,
// which every move back into an earlier phase opens, counts the move itself under its own pair,
// and fires the deployment's hooks that stand on the move (fireWorkBackHooks). The counts are the
// task's (record.Store.ReturnCounts): on its plan_tasks row for an issue with plan rows, and on the
// implementer's phases row for an issue with none, where they lived before plans.
//
// The pass bounds nothing: it is the counter the handoff completion key carries
// (api/handoff.go), and its whole job is to make a completion at a commit an earlier pass
// reported a fact of its own. BackwardMoves is what a hook counts, under the move's own
// "<from>-<to>" pair, spelled as phase.BackwardPairs() spells the pairs (phase.Pair): a review
// that sends the work back and a test that sends it back are two counts and never one. Both rise
// here, where every backward move passes, rather than beside the review round, which only a
// review requesting changes opens (recordRound): a hook standing on that count would never see a
// tester's fail or a worker's own move back.
//
// A move the record holds as a conflict with the base raises no pair and fires no hook: nobody
// sent the work back, so there is no move of a kind for a hook to count, and the pass it still
// raises is the completion key's alone. Everything is written in the move's own transaction, so
// a move the record holds and a prompt nobody wrote cannot happen, and nothing here holds the
// move back: the transition starts the next role whatever a hook said.
func (e *Engine) recordPass(ctx context.Context, tx pgx.Tx, issue record.Issue, task string, from, to phase.Phase, pr *record.PullRequest) error {
	counts, err := e.store.ReturnCounts(ctx, tx, issue.Key, issue.Generation, task)
	if err != nil {
		return err
	}
	counts.Pass++
	if pr != nil && classify.ConflictRound(*pr) {
		return e.store.PutReturnCounts(ctx, tx, issue.Key, issue.Generation, task, counts)
	}
	if counts.BackwardMoves == nil {
		counts.BackwardMoves = make(map[string]int, 1)
	}
	counts.BackwardMoves[phase.Pair(from, to)]++
	if err := e.store.PutReturnCounts(ctx, tx, issue.Key, issue.Generation, task, counts); err != nil {
		return err
	}
	return e.fireWorkBackHooks(ctx, tx, issue.Key, from, to, counts.BackwardMoves, pr)
}

// recordRound counts task one more review round, which only a review that requested changes
// ends, and tells the architect once for each value review_round_cap holds. The counts are the
// task's, where recordPass keeps them. The move's own pair and the deployment's hooks are counted
// where every backward move passes (recordPass), so a round and a move are counted apart: this
// one is the cap's, and everything it writes it writes in the move's own transaction.
//
// What the cap reads is Rounds less the rounds a conflict with the base caused
// (classify.ConflictRound, record.ReturnCounts.ConflictRounds). A round opened while the record
// holds the pull request's own head as conflicting is answered by the implementer's forward
// merge, whatever else was asked of it, so it costs the issue nothing - which is what
// dispatch://INFRA-1442 asked for, on a tree that reached the cap on three rounds of which two
// were conflicts. Rounds rises for both kinds, so the record keeps what the issue spent beside
// what it was excused, and the message names both numbers: an architect woken by it reads how
// many rounds were counted against the cap and how many were not.
//
// A conflict round posts neither the cap's message nor its notice, whatever the counted rounds
// stand at: it leaves that number as the round before it left it, and a notice is itself a way
// of counting, waking the architect to decide a round nobody spent.
//
// The cap's message and its pr-blocked notice go once for each value the cap holds, and not on
// every counted round at or above it: the round that tells writes the cap it read into
// record.ReturnCounts.CapTold, and a later round compares the two. The comparison is != and never
// <, because a cap lowered under a row that already told a higher value must tell once at the new
// value, which < would never do. A raised cap tells again at its new number, and a new
// generation tells again from its own first crossing, because clearGeneration clears the mark.
//
// Nothing here holds the move back: the transition starts the next role whatever the counts read. A write that fails is still an error of the move's transaction, as
// every other write of that transaction is, and is returned rather than swallowed.
func (e *Engine) recordRound(ctx context.Context, tx pgx.Tx, issue record.Issue, task string, pr *record.PullRequest) error {
	counts, err := e.store.ReturnCounts(ctx, tx, issue.Key, issue.Generation, task)
	if err != nil {
		return err
	}
	counts.Rounds++
	if pr != nil && classify.ConflictRound(*pr) {
		// A conflict round ends here: it tells nobody, so the write of the two counts is all it
		// has to do.
		counts.ConflictRounds++
		return e.store.PutReturnCounts(ctx, tx, issue.Key, issue.Generation, task, counts)
	}
	counted := counts.Rounds - counts.ConflictRounds
	tell := counted >= e.cfg.ReviewRoundCap && counts.CapTold != e.cfg.ReviewRoundCap
	if tell {
		counts.CapTold = e.cfg.ReviewRoundCap
	}
	if err := e.store.PutReturnCounts(ctx, tx, issue.Key, issue.Generation, task, counts); err != nil {
		return err
	}
	if tell {
		message := fmt.Sprintf("Issue reached review_round_cap=%d: %d rounds counted, %d conflict rounds not counted.",
			e.cfg.ReviewRoundCap, counted, counts.ConflictRounds)
		if err := e.enqueue(ctx, tx, issue.Key, record.MessagePost{Body: message}); err != nil {
			return err
		}
		if err := e.notice(ctx, tx, issue.Key, record.Notice{Kind: "pr-blocked", Role: claim.RoleArchitect, Reason: message}); err != nil {
			return err
		}
	}
	return nil
}
