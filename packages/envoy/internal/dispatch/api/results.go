package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/delivery"
	"github.com/sjawhar/envoy/internal/dispatch/docs"
	"github.com/sjawhar/envoy/internal/dispatch/githubapp"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
	"github.com/sjawhar/envoy/internal/dispatch/text"
)

// results.go records and reads the results of an issue's checks. A check is a `check` block of
// a Verification document, and a result says one run of it passed or failed, at one commit.
// The record route takes a result for a check of the recording issue or of any issue above it
// (refs.IssueAncestors) and appends `result.recorded` on the check's own issue: a role's result,
// or, for a check CI proves, the result of the CI job its `ci_check` names, recorded with that
// job's own results token (refuseResultOfAnotherRecorder). The read route lists the checks of
// one issue's Verification document with every result recorded against each, from any issue,
// newest first, and says what each check compared those results with: a `before_merge` check is
// compared with the pull request heads of the check's own issue and with the head of each branch
// a CI job's result on that issue names, so a child's pull request and a child's branch answer for
// nothing against its parent's check. Who may record which result, and the row each recorder's
// result becomes, is results_recorder.go's; the two tables the rows live in are results_rows.go's;
// the comparison is answered in results_comparison.go, and whether a pass stands in
// results_pass_stands.go.

// checkResultRole bounds the role a result names: the word the pull-request body line prints.
var checkResultRole = regexp.MustCompile(`^[a-z-]{1,32}$`)

// resultCommitSHA is the commit a result ran at, as the column's own check spells it.
var resultCommitSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// The two values of a check's `when`, beside before_merge (docs.CheckWhenBeforeMerge), that the
// closed enum pmdoc/schema/blocks.json declares under `choices`.
const (
	checkWhenAfterMerge    = "after_merge"
	checkWhenDefaultBranch = "default_branch"
)

// checkResultReadingRunes is how much of the role's reading the pull-request body line carries,
// and how much of the check's own words the line of a derived result carries (ciVerdictLine).
const checkResultReadingRunes = 80

// checkBlockRef is where one `check` block a result names lives: the issue whose Verification
// document holds it, that document, and the block within it, with the block's canonical
// reference, as the results read's ref of the check is written (text.FormatBlockRef): the check a
// CI job's statement names. The resolve settles it and nothing moves it: a result is recorded
// against this block or against none.
type checkBlockRef struct {
	IssueKey   string
	ArtifactID string
	BlockID    string
	Ref        string
}

// indexedCheck is what document_blocks holds for one check block: the document version its
// words last changed in, and their digest. The two are read in one statement and judged
// together, never one without the other (settledCheckUnderLocks).
type indexedCheck struct {
	Version int
	SHA256  string
}

// checkResolution is one reading of a check block: the attributes the write judges (its `when`,
// run, negative control and ci_check) and the surface a CI job's row takes, read out of the text
// the block index recorded at the indexed version, whose digest is the indexed digest
// (results_settled_check.go), carried with that version and digest. They travel as one value
// because the attributes are that text's alone: the write half re-reads the pair and its text
// under its locks, and a pair that moved is read again whole out of its own text, never patched
// one half at a time (settledCheckUnderLocks).
type checkResolution struct {
	indexedCheck
	Attributes docs.CheckAttributes
	Surface    string
}

// resolvedCheck is one `check` block a result names, as a resolve found it: where it lives, and
// the reading of it in hand.
type resolvedCheck struct {
	checkBlockRef
	checkResolution
}

// checkResultInput is the body POST /api/v1/issues/{key}/results takes from a role. A CI job's
// results token sends the four members of ciJobResultInput alone (refusalFor).
type checkResultInput struct {
	Actor           *model.Actor `json:"actor"`
	Check           string       `json:"check" required:"true"`
	Role            string       `json:"role" required:"true"`
	Surface         string       `json:"surface"`
	Run             string       `json:"run"`
	Observed        string       `json:"observed"`
	Commit          string       `json:"commit" required:"true"`
	NegativeControl string       `json:"negative_control"`
	Verdict         string       `json:"verdict" required:"true"`
	Failed          int          `json:"failed"`
	Skipped         int          `json:"skipped"`
	Excluded        int          `json:"excluded"`
	Screenshots     []string     `json:"screenshots"`
}

// checkResultRecorded is the body POST /api/v1/issues/{key}/results answers.
type checkResultRecorded struct {
	Line   string            `json:"line"`
	Result model.CheckResult `json:"result"`
}

// POST /api/v1/issues/{key}/results  (any authenticated actor)
//
// Records one result against one check. {key} is the recording issue, which need not be the
// check's: a result against a check of an issue above it is taken, and the event goes on that
// issue. Such a result is no proof of the check, since it stands at a basis only at a head of
// a pull request of the check's own issue (comparisonReader). The answer carries the stored
// row and the pull-request body line it implies, so every writer writes one grammar.
//
// A CI job's results token (model.Credential.ResultsOnly) records here the result of a check CI
// proves whose ci_check names the token's check run, and no other; every other caller records a
// role's result, and none of a check CI proves (refuseResultOfAnotherRecorder).
func (s *server) recordCheckResult(w http.ResponseWriter, r *http.Request) {
	var input checkResultInput
	body, err := readJSONBody(r, &input, maxJSONRequestBytes, errJSONTooLarge)
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	logUndeclaredMembers(r, &input, body)
	// The credential decides which body the route takes, so it is read before the actor: a CI
	// job's body that names an actor is refused for naming it, as each other field of a role's
	// result is, rather than for the session it names. The caller is decided once a request
	// (optionalActor), so requireActor below presents nothing again.
	caller, _, err := s.optionalActor(r)
	if err != nil {
		s.writeAuthenticationError(w, err)
		return
	}
	credential := caller.Credential
	if err := input.refusalFor(credential, body); err != nil {
		s.writeHandlerError(w, err)
		return
	}
	actor, ok := s.requireActor(w, r, input.Actor)
	if !ok {
		return
	}
	issueKey := r.PathValue("key")
	pool, err := s.readyPool()
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}

	// The check is resolved before the transaction opens, from the block index alone
	// (results_settled_check.go), and no issue's owner lock is held across that read: four
	// writers parked on one issue's lock already cost a read of an unrelated issue the whole
	// seventeen seconds the lock was held (store/pool.go). Nothing the lock protects is settled
	// here — the check's words and its `when` are the document's, not the issue's — and what it
	// does protect, the block's indexed version and digest, is read again under it below with
	// the text whose digest that is.
	check, err := s.resolveCheck(r.Context(), pool, issueKey, input.Check)
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	// A CI job's check run, read once and before the transaction opens for the reason the check
	// is resolved there: it is a GitHub round trip, though the process keeps its answer, since a
	// check run never changes its name or its commit (specCheckRun). Both refusals below read
	// it, and the job's statement takes its name, the run's attempt and its principal. Every
	// other credential reads none.
	recorder := resultRecorder{credential: credential}
	if credential != nil && credential.ResultsOnly {
		read, err := s.ciJobCheckRun(r.Context(), *credential.CI)
		if err != nil {
			s.writeHandlerError(w, err)
			return
		}
		numbers, err := runNumbersOf(*credential.CI)
		if err != nil {
			s.writeHandlerError(w, err)
			return
		}
		if recorder.principal, err = resultPrincipal(*credential.CI); err != nil {
			s.writeHandlerError(w, err)
			return
		}
		recorder.run, recorder.attempt = &read, numbers.attempt
	}
	// Refused here, before refuseUnknownCommit: a role's result for a check CI proves spends no
	// GitHub read at all.
	if err := s.refuseResultOfAnotherRecorder(r.Context(), pool, check, credential, recorder.run); err != nil {
		s.writeHandlerError(w, err)
		return
	}
	if credential != nil && credential.ResultsOnly {
		if err := refuseBranchOfAnotherIssue(r.Context(), pool, issueKey, pushedBranch(*credential.CI)); err != nil {
			s.writeHandlerError(w, err)
			return
		}
	}

	// The one repository the check's issue resolves to, read once for the two callers below:
	// the refusal of a commit that it, and every other repository of the issue's pull requests,
	// does not hold, and the read-through of the pull requests GitHub names for that commit after
	// the write. It is nil when there is nothing to ask GitHub about — a deployment with no App
	// key, an issue that resolves to no repository, and a read of Dispatch's own rows that
	// failed, which is logged and stores the result as every answer but the proof of absence
	// does.
	//
	// It is the check's issue's repository, not the recording issue's: readThroughPullRequests
	// and the results read both resolve check_issue_key, and a refusal keyed on the recording
	// issue would disagree with the read about which repository the commit belongs to. It is
	// resolved through the one helper the results read resolves through (results_comparison.go,
	// server.checkIssueRepository), under the canonical name the repository is called today: a
	// refusal asking under a name the repository was renamed away from reads the 404 GitHub
	// answers for that name's installation, where the read reads the commit — so it would
	// store every result of such an issue.
	//
	// The App key is checked here and not in the helper: every use of the answer below is a
	// GitHub round trip, while the results read needs the repository even with no App key, for
	// the default-branch answers it reads from Dispatch's own stored rows.
	//
	// The helper says whether its resolve answered at all, which the results read needs to tell
	// a check that compares with nothing from one it could not compare (results_comparison.go,
	// nothingReason). This route takes the repository alone, on purpose: both of its uses below
	// are already answered by a nil, and the two nothings would earn one answer here anyway.
	// Nothing but GitHub's proof that the repository holds no such commit refuses a record, so
	// a resolve that did not answer must store the result, exactly as a deployment with no App
	// key and an issue with no repository do.
	var checkRepo *string
	if s.deps.GitHub != nil {
		checkRepo, _ = s.checkIssueRepository(r.Context(), pool, check.IssueKey)
	}

	// Asked here for the reason resolveCheck is asked here, before the transaction opens: it is
	// a GitHub round trip, and the comment over that resolve records what a slow read under the
	// two issues' owner locks costs. Nothing is written before it, so a refusal leaves no row and
	// no event behind. The answer is the repository that held the commit, where one did: the
	// read-through after the write reads through it, since for a pull request the issue links it
	// may be another repository than the issue's own.
	heldIn, err := s.refuseUnknownCommit(r.Context(), check.IssueKey, checkRepo, input.Commit)
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	readThrough := checkRepo
	if heldIn != nil {
		readThrough = heldIn
	}

	written, err := s.recordResultUnderOwnerLocks(r.Context(), issueKey, check, input, actor, recorder)
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	// The read-through runs before the event: the Verification tab refetches on
	// result.recorded, and no later event refreshes it, so a tab that read the results before
	// the pull request row landed would show this result standing at no basis until something
	// else happened.
	s.readThroughPullRequests(r.Context(), check.IssueKey, written.result.CommitSHA, readThrough)
	s.publish(r.Context(), written.event)
	s.storeDisplayTrees(r.Context(), written.result.ID, written.result.Observed)
	WriteJSON(w, http.StatusCreated, checkResultRecorded{
		// The check as the transaction that wrote the row saw it, which after a settlement that
		// landed between the resolve and the locks is not the one resolveCheck read: the line
		// names the `when` the result was judged by.
		Line:   checkResultLine(written.check, written.result),
		Result: written.result,
	})
}

// resultRecorder is who records a result, as the route read it before its transaction opened:
// the request's credential, and for a CI job's results token the check run its token names
// (ciJobCheckRun), that run's attempt and the principal the job's statement takes
// (resultPrincipal). run is nil for every other credential, a role's.
type resultRecorder struct {
	credential *model.Credential
	run        *githubapp.CheckRun
	attempt    int
	principal  string
}

// recordedCheckResult is what one results write produces: the row it wrote, the event it
// appended, and the check as the transaction that wrote them saw it. That check is not the one
// the route resolved when a settlement moved the block between the resolve and the locks, so
// the pull-request body line is written from this one.
type recordedCheckResult struct {
	result model.CheckResult
	event  model.Event
	check  resolvedCheck
}

// recordResultUnderOwnerLocks is the locked half of the write, and the only place this route
// opens a transaction: it takes both issues' owner locks, re-reads under them the check as the
// block index holds it (settledCheckUnderLocks), judges the result by that reading, writes the
// row and its event and commits.
//
// Every judgement the write makes on the check's attributes is made here, on the reading in hand
// under the locks: who may record the result (refuseResultOfAnotherRecorder, whose 409
// CHECK_PROVED_BY_CI and CHECK_NOT_THIS_JOBS turn on ci_check, run, `when` and negative control),
// the `when` a closed issue is judged by, and the surface and negative control a CI job's row
// takes. The route judged the reading resolveCheck made before the transaction opened too, so a
// refusal costs no GitHub read; a settlement landing between that resolve and these locks moves
// the reading, and it is judged again here.
//
// Nothing under these locks reads the live document or GitHub: the check is one statement over
// the block index, and every read is made before the first write, so the transaction holds a
// transaction id only for the event's append, the row's insert and the COMMIT.
func (s *server) recordResultUnderOwnerLocks(
	ctx context.Context, issueKey string, check resolvedCheck, input checkResultInput, actor model.Actor,
	recorder resultRecorder,
) (recordedCheckResult, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return recordedCheckResult{}, err
	}
	defer tx.Rollback(ctx)

	// Both issues' owner locks are taken in one key order, whether or not either is open: a
	// result for an after-merge check is taken on a closed issue, and the lock still stops it
	// closing or reopening under the write. The check's issue is usually the recording issue's
	// ancestor, so locking them in call order would let one transaction hold the child while
	// waiting for the parent and another hold the parent while waiting for the child;
	// lockIssueOwners is the one key order every writer of two issues takes. It is the owner
	// lock and not the issue rows, so this read phase holds no transaction id.
	if _, err := lockIssueOwners(ctx, tx, issueKey, check.IssueKey); err != nil {
		return recordedCheckResult{}, err
	}
	recordingOpen, checkOpen, err := issuesOpenForResult(ctx, tx, issueKey, check.IssueKey)
	if err != nil {
		return recordedCheckResult{}, err
	}
	// The version, digest and attributes the row is judged by are the block's as this
	// transaction sees them, read again under the locks: a settlement that landed between the
	// resolve and the locks would otherwise have the result recorded against the words it
	// replaced, and a check reworded from before_merge to after_merge, or given a ci_check, would
	// be judged by the attributes it carried before the locks.
	if check.checkResolution, err = settledCheckUnderLocks(ctx, tx, check.checkBlockRef, check.checkResolution, input.Check); err != nil {
		return recordedCheckResult{}, err
	}
	// Asked again under the locks, of the reading just made: a settlement can have given the
	// block a ci_check, or named another job in it, since the resolve. A result that races a
	// browser's write giving the block a ci_check can commit beside it, since that write's
	// settlement has not indexed it yet; the settlement then holds the block to the fourth rule
	// (docs.CICheckRuleIn) and takes that ci_check off, as the release before does.
	if err := s.refuseResultOfAnotherRecorder(ctx, tx, check, recorder.credential, recorder.run); err != nil {
		return recordedCheckResult{}, err
	}
	// A closed issue takes a result only for a check that runs after the merge or on the default
	// branch: such a check can only be proved once the issue has closed, so refusing it would make
	// the production check unrecordable. A result for a pre-merge check stays refused there, as a
	// document version does; a comment and a message are the other writes a closed issue takes.
	if (!recordingOpen || !checkOpen) && check.Attributes.When != checkWhenAfterMerge && check.Attributes.When != checkWhenDefaultBranch {
		return recordedCheckResult{}, errorf(http.StatusConflict, "ISSUE_CLOSED",
			"issue is closed: only a result for a check whose when is after_merge or default_branch is recorded there, and %s is %s", check.BlockID, check.Attributes.When)
	}
	if err := s.refuseResultScreenshots(ctx, tx, issueKey, check.IssueKey, input.Screenshots); err != nil {
		return recordedCheckResult{}, err
	}

	var result model.CheckResult
	insert := insertCheckResult
	if recorder.run != nil {
		// The job's result and its event name the run alone: the person its results token
		// carries is the broker's result entry, who ran no test (ciJobActor).
		actor = ciJobActor(actor)
		if actor.Project == nil {
			// brokerTokenActor names the project of every token that names one, and a results
			// token names one project, so this is Dispatch's own fault.
			return recordedCheckResult{}, errors.New("a results token's actor names no project")
		}
		result = ciJobCheckResult(input, check, actor, *recorder.run, *recorder.credential.CI, recorder.attempt, recorder.principal, issueKey)
		insert = insertCIJobCheckResult
	} else {
		result = roleCheckResult(input, check, actor, issueKey)
	}
	event, err := s.appendEvent(ctx, tx, issueOwner(check.IssueKey).event("result.recorded", actor, model.CheckResultEventPayload{
		ResultID: result.ID, Issue: issueKey, CheckIssue: check.IssueKey,
		CheckArtifactID: check.ArtifactID, CheckBlockID: check.BlockID, CheckVersion: check.Version,
		When: check.Attributes.When, Verdict: result.Verdict, Role: result.Role, CommitSHA: result.CommitSHA,
	}))
	if err != nil {
		return recordedCheckResult{}, err
	}
	seq := int64(event.Seq)
	result.EventSeq = &seq
	if err := insert(ctx, tx, &result); err != nil {
		return recordedCheckResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return recordedCheckResult{}, err
	}
	return recordedCheckResult{result: result, event: event, check: check}, nil
}

// issueCheckResults is the body GET /api/v1/issues/{key}/results answers.
type issueCheckResults struct {
	Checks                 []model.CheckBlock `json:"checks"`
	Issue                  string             `json:"issue"`
	VerificationArtifactID string             `json:"verification_artifact_id"`
}

// GET /api/v1/issues/{key}/results  (any authenticated caller)
//
// The checks of the issue's Verification document as the block index holds each, each with what its
// results are compared with and whether a pass of it stands, and every result recorded
// against it from any issue, newest first, each with where its commit stands and whether the
// check's words moved since. An issue with no Verification document has no checks and no
// error.
//
// A check CI proves carries `ci` too, read from GitHub at the issue's newest pull request
// head, and a role's result of it counts for nothing (not_counted). Its results are the one
// derived from the run its
// `ci_check` names (deriveCIResults), which is judged as a recorded result is, beside each
// result a run of its CI job recorded (role ci, branch set), newest first. A reader of one
// result of such a check finds the derived one by its id (ci-run:<check run id>), not as the
// first.
//
// ?at=<commit> replaces what every check is compared with by the one commit the reader names.
// The only read of GitHub it leaves is the check runs of a check CI proves, read at the issue's
// newest pull request head as on every read: the derived result is that head's, and stands at
// the named commit only where that commit is the head.
//
// An end-to-end test process whose hide-state file exists answers without the state the server
// computes from the results (stateHidden, withoutComputedState).
func (s *server) listCheckResults(w http.ResponseWriter, r *http.Request) {
	if !s.requireAuthenticated(w, r) {
		return
	}
	at := r.URL.Query().Get("at")
	if at != noCommitNamed && !resultCommitSHA.MatchString(at) {
		writeError(w, "INVALID_QUERY", http.StatusBadRequest,
			"at must be a commit of 40 lowercase hex characters")
		return
	}
	issueKey := r.PathValue("key")
	if err := s.requireIssueExists(r.Context(), issueKey); err != nil {
		s.writeHandlerError(w, err)
		return
	}
	artifact, held, err := s.verificationDocument(r.Context(), s.deps.Store.Pool, issueKey, "")
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	body := issueCheckResults{Checks: []model.CheckBlock{}, Issue: issueKey, VerificationArtifactID: artifact.ID}
	if !held {
		WriteJSON(w, http.StatusOK, body)
		return
	}
	trees := s.readDisplayTrees(r.Context(), wantsDisplayTrees(r))
	checks, err := s.checkBlocksOf(r.Context(), s.deps.Store.Pool, artifact, trees)
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	recorded, err := loadCheckResults(r.Context(), s.deps.Store.Pool, artifact.ID)
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	retracted, err := loadCheckResultRetractions(r.Context(), s.deps.Store.Pool, artifact.ID)
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	for index := range checks {
		results := recorded[checks[index].BlockID]
		if results == nil {
			results = []model.CheckResult{}
		}
		for result := range results {
			if retraction, held := retracted[results[result].ID]; held {
				results[result].Retracted = &retraction
			}
		}
		checks[index].Results = results
	}
	headCI, err := s.deriveCIResults(r.Context(), issueKey, checks)
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	// Every head GitHub has to be asked for is learned before the first check is answered, so
	// two `before_merge` checks of one issue name one list of bases and not two
	// (comparisonReader.learn).
	reader := newComparisonReader(s, at)
	reader.headCI = headCI
	reader.learn(r.Context(), checks)
	for index := range checks {
		reader.answer(r.Context(), &checks[index])
	}
	answerCompatibilityCurrency(checks, at)
	keepFieldsOlderCommandsRead(checks)
	// Each result's observed text is registered for its tree once no check's results are copied
	// any more, since what is registered is a pointer into the row: deriveCIResults replaces a
	// check's results, and keepFieldsOlderCommandsRead moves them into new lists, results and the
	// recorded_results of a check CI proves, whose results take their trees as well.
	for index := range checks {
		for result := range checks[index].Results {
			trees.text(&checks[index].Results[result].ObservedTree, checks[index].Results[result].Observed)
		}
		for result := range checks[index].RecordedResults {
			trees.text(&checks[index].RecordedResults[result].ObservedTree, checks[index].RecordedResults[result].Observed)
		}
	}
	body.Checks = checks
	trees.resolve()
	if s.stateHidden() {
		hidden, err := withoutComputedState(body)
		if err != nil {
			s.writeHandlerError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, hidden)
		return
	}
	WriteJSON(w, http.StatusOK, body)
}

// keepFieldsOlderCommandsRead moves each fact the results read gained with the branch heads and
// a CI job's own results out of the fields a dispatch command released before them reads, into
// fields of its own, so each of those keeps the meaning that command reads it with:
//
//   - a branch head a check compares with moves from compared_with to branch_heads, since that
//     command prints a basis of a kind it does not know as a commit it named;
//   - a result of results whose commit is such a head answers commit_stands as against
//     compared_with, other_commit, and names the head in at_branch_head;
//   - every result of a check CI proves but its derived one moves from results to
//     recorded_results, since that command reads results[0] of such a check as the derived one.
//
// It runs last: every answer, pass_stands among them, and the compatibility currency are made from
// the whole lists first. @legion/contracts' checkWithNewerFields puts them back for a reader of
// this build.
func keepFieldsOlderCommandsRead(checks []model.CheckBlock) {
	for index := range checks {
		check := &checks[index]
		compared := make([]model.CheckBasis, 0, len(check.ComparedWith))
		for _, basis := range check.ComparedWith {
			if basis.Kind == model.CheckBasisBranchHead {
				check.BranchHeads = append(check.BranchHeads, basis)
				continue
			}
			compared = append(compared, basis)
		}
		check.ComparedWith = compared
		results := make([]model.CheckResult, 0, len(check.Results))
		for _, result := range check.Results {
			if check.CI != nil && !strings.HasPrefix(result.ID, ciResultIDPrefix) {
				check.RecordedResults = append(check.RecordedResults, result)
				continue
			}
			if stands := result.CommitStands; stands != nil && stands.Basis != nil && stands.Basis.Kind == model.CheckBasisBranchHead {
				head := *stands.Basis
				result.AtBranchHead = &head
				result.CommitStands = &model.ResultCommitStands{Answer: model.CommitOtherCommit}
			}
			results = append(results, result)
		}
		check.Results = results
	}
}

// issuesOpenForResult answers whether the recording issue and the check's issue are open, in
// one query under the owner locks the caller has already taken (lockIssueOwners). It makes no
// refusal of its own for a closed issue, as requireOpenOwnerStatus does: the results route
// decides for itself, since a closed issue takes a result for an after-merge check. An issue
// with no row is the route's 404, which the locks never answer: lockIssueOwners takes an owner's
// lock whether or not a row holds that key.
func issuesOpenForResult(ctx context.Context, tx pgx.Tx, issueKey, checkIssueKey string) (bool, bool, error) {
	open := map[string]bool{}
	rows, err := tx.Query(ctx, `select key, closed_at is null from issues where key = any($1)`,
		[]string{issueKey, checkIssueKey})
	if err != nil {
		return false, false, fmt.Errorf("read whether %s and %s are open: %w", issueKey, checkIssueKey, err)
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var isOpen bool
		if err := rows.Scan(&key, &isOpen); err != nil {
			return false, false, fmt.Errorf("read whether %s and %s are open: %w", issueKey, checkIssueKey, err)
		}
		open[key] = isOpen
	}
	if err := rows.Err(); err != nil {
		return false, false, fmt.Errorf("read whether %s and %s are open: %w", issueKey, checkIssueKey, err)
	}
	recording, held := open[issueKey]
	if !held {
		return false, false, errorf(http.StatusNotFound, "NOT_FOUND", "issue not found")
	}
	check, held := open[checkIssueKey]
	if !held {
		return false, false, errorf(http.StatusNotFound, "NOT_FOUND", "issue not found")
	}
	return recording, check, nil
}

// requireIssueExists is the read route's 404 for an issue nobody has.
func (s *server) requireIssueExists(ctx context.Context, key string) error {
	if s.deps.Store == nil || s.deps.Store.Pool == nil {
		return errorf(http.StatusServiceUnavailable, "DATABASE_UNAVAILABLE", "database unavailable")
	}
	var exists bool
	if err := s.deps.Store.Pool.QueryRow(ctx, `select true from issues where key = $1`, key).Scan(&exists); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errorf(http.StatusNotFound, "NOT_FOUND", "issue not found")
		}
		return fmt.Errorf("read issue %s: %w", key, err)
	}
	return nil
}

// verificationDocument is one issue's Verification document and whether it has one. slug, when
// it is not "", holds the answer to the document filed under it, which is how a canonical block
// reference names one. A row whose role is null answers to its slug alone: a document the
// release before this one created during the rolling deploy carries no role until the next
// version written under its row repairs it (documentRole), and the slug is meanwhile the only
// thing that says what the document is. Every reader of an issue's Verification document in
// this package goes through here — the results routes, the check resolver and the approval
// request's second document.
func (s *server) verificationDocument(ctx context.Context, q queryer, issueKey, slug string) (model.Artifact, bool, error) {
	artifact, err := scanArtifact(q.QueryRow(ctx, `
		select `+artifactColumns+` from artifacts
		where issue_key = $1 and `+verificationRolePredicate+` and ($2 = '' or slug = $2)
		limit 1
	`, issueKey, slug))
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Artifact{}, false, nil
	}
	if err != nil {
		return model.Artifact{}, false, fmt.Errorf("read the Verification document of %s: %w", issueKey, err)
	}
	return artifact, true, nil
}

func unknownCheck(value string) error {
	return errorf(http.StatusBadRequest, "UNKNOWN_CHECK",
		"%s is no check of this issue's Verification document or of one above it", value)
}

// validCICheck reports whether check is one CI proves: it carries ci_check and keeps the three
// rules of such a block (docs.CICheckRule). The kind of a check is its block's as the block index
// holds it, read again at each read (results_settled_check.go), so a role's result of such a check
// counts for nothing (notCountedOnCICheck). A write is held to a fourth rule too, that no result a
// role recorded stands against the block (docs.CICheckRuleIn), so such a result stands against a
// block carrying ci_check only until the settlement that repairs the block. Any other check is an
// ordinary check, whatever its ci_check says.
func validCICheck(check model.CheckBlock) bool {
	attributes := docs.CheckAttributesOfBlock(check)
	return docs.CarriesCICheck(attributes) && docs.CICheckRule(attributes) == ""
}

// blockWords is a block's text as a reader sees it: every text node under it, joined by single
// blanks, with the text of a link and every dispatch:// reference dropped, so the pull-request
// body line the result implies carries no link and no Dispatch host. blockContent is the same
// block with both kept.
func blockWords(node *pmdoc.Node) string {
	var words []string
	pmdoc.Walk(node, func(child *pmdoc.Node) bool {
		if child.Type != "text" || child.Text == "" {
			return true
		}
		for _, mark := range child.Marks {
			if mark.Type == "link" {
				return true
			}
		}
		for _, word := range strings.Fields(child.Text) {
			if strings.Contains(word, "://") {
				continue
			}
			words = append(words, word)
		}
		return true
	})
	return strings.Join(words, " ")
}

// blockContent is a typed block's own content as markdown: what stands between its opening line
// and its closing one, rendered from node as a document of its own, so neither the directive's
// line nor its attributes are in it and every link and every dispatch:// reference is. Its one
// use is the content typedBlocksIn hands visit for a block the document's read gives no byte
// range for. That is every block of a document whose read with blocks failed, which
// typedBlockMarkdown then reads with Text alone and gives no text for any block. Every other
// typed block's content is cut from its stored text (blockTextContent). node comes from the
// parse of the document's live rendering, which writes every typed block's id, so this
// rendering writes no id that the document does not hold. A block holding nothing has no
// content and so no tree; one whose content cannot be rendered apart from its document is logged
// and has none either, since the read's other fields do not depend on it.
func blockContent(node *pmdoc.Node) string {
	if len(node.Children) == 0 {
		return ""
	}
	markdown, err := pmdoc.Render(&pmdoc.Node{Type: "doc", Children: node.Children})
	if err != nil {
		id, _ := node.Attrs[pmdoc.BlockIDAttr].(string)
		slog.Warn("dispatch: render a typed block's content for its tree", "type", node.Type, "block", id, "error", err)
		return ""
	}
	return markdown
}

// checkResultLine is the pull-request body line one recorded result implies. Dispatch writes it
// rather than each caller, so every writer writes one grammar; it carries no link to Dispatch
// and no Dispatch host.
//
// The tail is the reading the role recorded (Observed) and not the check's own words, which
// every reader of the pull request can read on the issue and which a pass and a fail would
// carry alike: the line's whole purpose is to say what was seen. A reading longer than the
// budget is cut at a word boundary and marked with an ellipsis, so a reader can tell a cut
// line from a short one; a result with no reading says so, since a line that simply stopped
// after the role would read as a reading nobody recorded.
func checkResultLine(check resolvedCheck, result model.CheckResult) string {
	line := fmt.Sprintf("- Check %s (%s): %s at %s, recorded by %s.",
		check.BlockID, check.Attributes.When, result.Verdict, result.CommitSHA, result.Role)
	reading := strings.Join(strings.Fields(result.Observed), " ")
	if reading == "" {
		return line + " The role recorded no reading."
	}
	return line + " " + cutWords(reading)
}

// cutWords is s cut at a word boundary to checkResultReadingRunes, with an ellipsis where it was
// cut, so a reader can tell a cut line from a short one (text.HeadWords).
func cutWords(s string) string {
	head, cut := text.HeadWords(s, checkResultReadingRunes)
	if cut {
		head += "…"
	}
	return head
}

// sessionOf is the session a result was recorded by, empty for a person's.
func sessionOf(actor model.Actor) string {
	if actor.Kind == "session" {
		return actor.ID
	}
	return ""
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// refuseResultScreenshots holds a result's screenshots to image documents of the recording
// issue or of the check's issue, named by their dispatch:// reference.
func (s *server) refuseResultScreenshots(ctx context.Context, q queryer, issueKey, checkIssueKey string, screenshots []string) error {
	for _, screenshot := range screenshots {
		ref, ok := text.ParseRef(screenshot, "")
		if !ok || ref.Kind != "artifact" || ref.IssueKey == "" ||
			(ref.IssueKey != issueKey && ref.IssueKey != checkIssueKey) {
			return errorf(http.StatusBadRequest, "RESULT_INPUT",
				"screenshot %q names no image document of %s or %s", screenshot, issueKey, checkIssueKey)
		}
		var found bool
		err := q.QueryRow(ctx, `
			select true from artifacts where issue_key = $1 and slug = $2 and kind = 'image'
		`, ref.IssueKey, ref.ID).Scan(&found)
		if errors.Is(err, pgx.ErrNoRows) {
			return errorf(http.StatusBadRequest, "RESULT_INPUT",
				"screenshot %q names no image document of %s or %s", screenshot, issueKey, checkIssueKey)
		}
		if err != nil {
			return fmt.Errorf("read screenshot %q: %w", screenshot, err)
		}
	}
	return nil
}

// refuseUnknownCommit refuses a result at a commit that no repository of the check's issue holds:
// 400 RESULT_COMMIT_UNKNOWN, naming the commit and each repository it asked. repo is the
// repository the check's issue resolves to, which recordCheckResult reads once for this refusal
// and for the read-through after the write; a nil repo is nothing to ask GitHub about and no
// refusal. A deployment with no App key resolves none, so the key is not checked again here.
//
// The repositories of the issue are its own and the repository of each open or merged pull
// request of the issue (delivery.RepositoriesOfIssue), which for a pull request the issue links
// may be another one (R17 of the spec of several open pull requests). A commit that is the head
// of such a pull request is taken with no read of GitHub at all (delivery.HeadAttributedTo): its
// row says which repository holds it. Every other commit is asked of the issue's own repository,
// then of each other one in name order, until one holds it, and the answer is the repository that
// held it, which the read-through after the write reads through. A commit at no head may be one a
// CI job pushed to a branch of the other repository, so the other repositories are asked too.
//
// It runs before the transaction opens, so a refused result leaves no check_results row and no
// result.recorded event behind. Dispatch stored a result at a mistyped commit no repository
// held once, and a reader could not tell it from the real head, since both print as twelve
// characters.
//
// It refuses on nothing but GitHub's proof, from each repository asked, that the repository does
// not hold the commit, which delivery.RepositoryHoldsCommit defines as the one 422 whose message
// says GitHub found no commit for the SHA. Any other GitHub answer from any of them, and a read of
// Dispatch's own rows that failed, stores the result: no such answer can block the result a closed
// issue takes, the production check's after-merge result — whose merge commit is on the branch its
// pull request merged into and is held anyway.
func (s *server) refuseUnknownCommit(ctx context.Context, checkIssueKey string, repo *string, sha string) (*string, error) {
	if repo == nil {
		return nil, nil
	}
	pool := s.deps.Store.Pool
	known, err := delivery.HeadAttributedTo(ctx, pool, checkIssueKey, sha)
	if err != nil {
		slog.Warn("dispatch: read the pull request heads of a recorded result's commit",
			"issue", checkIssueKey, "commit", sha, "error", err)
	}
	if known {
		return nil, nil
	}
	repos := []string{*repo}
	proven := true
	others, err := delivery.RepositoriesOfIssue(ctx, pool, checkIssueKey)
	if err != nil {
		slog.Warn("dispatch: read the repositories of a recorded result's issue",
			"issue", checkIssueKey, "error", err)
		proven = false
	}
	for _, other := range others {
		if !strings.EqualFold(other, *repo) {
			repos = append(repos, other)
		}
	}
	for _, asked := range repos {
		held, err := delivery.RepositoryHoldsCommit(ctx, s.deps.GitHub, asked, sha)
		if err != nil {
			slog.Info("dispatch: GitHub could not say whether a recorded result's commit is held",
				"repo", asked, "commit", sha, "error", err)
			proven = false
			continue
		}
		if held {
			return &asked, nil
		}
	}
	if !proven {
		return nil, nil
	}
	return nil, errorf(http.StatusBadRequest, "RESULT_COMMIT_UNKNOWN",
		"%s %s no commit %s: record the result once the commit is pushed",
		joinRepositories(repos), pluralHold(len(repos)), sha)
}

// joinRepositories names repos as one phrase: `a`, `a and b`, `a, b and c`.
func joinRepositories(repos []string) string {
	if len(repos) < 2 {
		return strings.Join(repos, "")
	}
	return strings.Join(repos[:len(repos)-1], ", ") + " and " + repos[len(repos)-1]
}

// pluralHold is the verb of a refusal that names count repositories.
func pluralHold(count int) string {
	if count == 1 {
		return "holds"
	}
	return "hold"
}

// readThroughPullRequests teaches Dispatch the pull requests GitHub names for a result's
// commit when that commit is the head of none it already knows for the check's issue, so a
// result recorded at the head of a pull request no event has reached Dispatch for stands at
// that head at once. It asks about the check's own issue and reads through repo: the repository
// refuseUnknownCommit found holds the commit, which for a pull request the issue links may be
// another than the issue's own, else the one repository that issue resolves to, which
// recordCheckResult resolved before the write: the issue comparisonReader.headBases names the
// heads of. A nil repo — a deployment with no App key, an issue resolving to no repository, or a
// resolve that failed — leaves nothing to read through, and it is answered before the database
// is read, since no head this read found could be used.
//
// It never fails the record: the row is written, and a head Dispatch could not learn leaves
// the result read exactly as it would have been without the call.
func (s *server) readThroughPullRequests(ctx context.Context, checkIssueKey, sha string, repo *string) {
	if repo == nil || s.deps.Store == nil || s.deps.Store.Pool == nil {
		return
	}
	known, err := delivery.HeadAttributedTo(ctx, s.deps.Store.Pool, checkIssueKey, sha)
	if err != nil {
		slog.Warn("dispatch: read the pull request heads of a recorded result", "issue", checkIssueKey, "error", err)
		return
	}
	if known {
		return
	}
	if _, _, err := delivery.ReadThroughCommitPulls(
		ctx, s.deps.Store.Pool, s.deps.GitHub, s.deps.Events, *repo, sha, time.Now()); err != nil {
		slog.Info("dispatch: GitHub named no pull request for a recorded result's commit",
			"repo", *repo, "commit", sha, "error", err)
	}
}
