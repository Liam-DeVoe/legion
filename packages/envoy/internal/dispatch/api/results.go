package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/broker/oauthwire"
	"github.com/sjawhar/envoy/internal/dispatch/delivery"
	"github.com/sjawhar/envoy/internal/dispatch/docs"
	"github.com/sjawhar/envoy/internal/dispatch/githubapp"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
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
// nothing against its parent's check. The comparison itself is answered in
// results_comparison.go.

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

// checkResultColumns is the column list every check_results read scans, in scanCheckResult's
// order. branch is null on a role's row, which reads as "". attestation is null on every row but
// a CI job's.
const checkResultColumns = `id::text, issue_key, check_issue_key, check_artifact_id::text, check_block_id,
	check_version, check_sha256, role, surface, run, observed, commit_sha, negative_control,
	failed, skipped, excluded, verdict, screenshots, session, actor, created_at, event_seq,
	coalesce(branch, ''), attestation`

// codeRefNotABranch refuses a results token whose run is of a ref that names no branch: a CI
// job records a result for the push of a branch, and its row names that branch.
const codeRefNotABranch = "REF_NOT_A_BRANCH"

// codeRepositoryNotTheIssues refuses a results token whose run is of another repository than the
// one the check's issue resolves to.
const codeRepositoryNotTheIssues = "REPOSITORY_NOT_THE_ISSUES"

// codeBranchNotTheIssues refuses a CI job's result on an issue whose change the branch its run
// pushed is not (refuseBranchOfAnotherIssue).
const codeBranchNotTheIssues = "BRANCH_NOT_THE_ISSUES"

// branchRefPrefix is what the ref of a branch push begins with.
const branchRefPrefix = "refs/heads/"

// resolvedCheck is one `check` block a result names: where it lives, the version its words last
// changed in and their digest as document_blocks holds them, and the attributes the write judges
// as the live document holds them (its `when`, run, negative control and ci_check, and the
// surface a CI job's row takes).
type resolvedCheck struct {
	IssueKey   string
	ArtifactID string
	BlockID    string
	// Ref is the check's canonical block reference, as the results read's ref of the check is
	// written (text.FormatBlockRef): the check a CI job's statement names.
	Ref        string
	Version    int
	SHA256     string
	Attributes docs.CheckAttributes
	Surface    string
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

// ciJobResultInput is every member a CI job's results token may send on POST
// /api/v1/issues/{key}/results: what its run observed, and nothing about who the job is, which
// Dispatch reads from the token and from GitHub. refusalFor names each other member a job's
// body carries.
type ciJobResultInput struct {
	Check    string `json:"check"`
	Commit   string `json:"commit"`
	Verdict  string `json:"verdict"`
	Observed string `json:"observed"`
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

	// The check is resolved before the transaction opens. Resolving it folds the live
	// Verification document, which for a document no room holds is a cold read bounded by a
	// minute (docs/read_cache.go), and no issue row lock is held across a read that long: four
	// writers parked on one issue's row lock already cost a read of an unrelated issue the whole
	// seventeen seconds the lock was held (store/pool.go). Nothing the lock protects is settled
	// here — the check's words and its `when` are the document's, not the issue's — and what it
	// does protect, the block's indexed version and digest, is read again under it below.
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
	var (
		run       *githubapp.CheckRun
		attempt   int
		principal string
	)
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
		if principal, err = resultPrincipal(*credential.CI); err != nil {
			s.writeHandlerError(w, err)
			return
		}
		run, attempt = &read, numbers.attempt
	}
	// Refused here, before refuseUnknownCommit: a role's result for a check CI proves spends no
	// GitHub read at all.
	if err := s.refuseResultOfAnotherRecorder(r.Context(), pool, check, credential, run); err != nil {
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
	// the refusal of a commit it does not hold, and the read-through of the pull requests
	// GitHub names for that commit after the write. It is nil when there is nothing to ask
	// GitHub about — a deployment with no App key, an issue that resolves to no repository,
	// and a read of Dispatch's own rows that failed, which is logged and stores the result as
	// every answer but the proof of absence does.
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
	// two issue row locks costs. Nothing is written before it, so a refusal leaves no row and no
	// event behind.
	if err := s.refuseUnknownCommit(r.Context(), checkRepo, input.Commit); err != nil {
		s.writeHandlerError(w, err)
		return
	}

	tx, err := s.begin(r.Context())
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	defer tx.Rollback(r.Context())

	// Both rows are locked in one key order, whether or not either is open: a result for an
	// after-merge check is taken on a closed issue, and the lock still stops it closing or
	// reopening under the write. The check's issue is usually the recording issue's ancestor,
	// so locking them in call order would let one transaction hold the child while waiting for
	// the parent and another hold the parent while waiting for the child; lockIssueRows is the
	// one key order every writer of two issue rows takes.
	if _, err := lockIssueRows(r.Context(), tx, issueKey, check.IssueKey); err != nil {
		s.writeHandlerError(w, err)
		return
	}
	recordingOpen, checkOpen, err := issuesOpenForResult(r.Context(), tx, issueKey, check.IssueKey)
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	// The version, digest and attributes the row is judged by are the block's as this
	// transaction sees them, read again under the locks: a document version that landed between
	// the resolve and the lock would otherwise have the result recorded against the words it
	// replaced, and a check reworded from before_merge to after_merge would be judged by the
	// `when` it carried before the lock. The digest is read under the locks either way, and the
	// attributes only when that digest moved, which is the only way the block's words can have
	// changed.
	if err := s.refreshResolvedCheck(r.Context(), tx, &check, input.Check); err != nil {
		s.writeHandlerError(w, err)
		return
	}
	// Asked again under the locks: a document write can have given the block a ci_check since the
	// resolve, and a role's result recorded since takes the block back from CI. Both are read in
	// this transaction, so whichever of a racing result and a racing ci_check commits, every
	// reader reads the block as one kind of check.
	if err := s.refuseResultOfAnotherRecorder(r.Context(), tx, check, credential, run); err != nil {
		s.writeHandlerError(w, err)
		return
	}
	// A closed issue takes a result only for a check that runs after the merge or on the default
	// branch: such a check can only be proved once the issue has closed, so refusing it would make
	// the production check unrecordable. A result for a pre-merge check stays refused there, as a
	// document version does; a comment and a message are the other writes a closed issue takes.
	if (!recordingOpen || !checkOpen) && check.Attributes.When != checkWhenAfterMerge && check.Attributes.When != checkWhenDefaultBranch {
		writeError(w, "ISSUE_CLOSED", http.StatusConflict,
			fmt.Sprintf("issue is closed: only a result for a check whose when is after_merge or default_branch is recorded there, and %s is %s", check.BlockID, check.Attributes.When))
		return
	}
	if err := s.refuseResultScreenshots(r.Context(), tx, issueKey, check.IssueKey, input.Screenshots); err != nil {
		s.writeHandlerError(w, err)
		return
	}

	var result model.CheckResult
	if run != nil {
		// The job's result and its event name the run alone: the person its results token
		// carries is the broker's result entry, who ran no test (ciJobActor).
		actor = ciJobActor(actor)
		if actor.Project == nil {
			// brokerTokenActor names the project of every token that names one, and a results
			// token names one project, so this is Dispatch's own fault.
			s.writeHandlerError(w, errors.New("a results token's actor names no project"))
			return
		}
		result = ciJobCheckResult(input, check, actor, *run, *credential.CI, attempt, principal, issueKey)
	} else {
		result = roleCheckResult(input, check, actor, issueKey)
	}
	event, err := s.appendEvent(r.Context(), tx, issueOwner(check.IssueKey).event("result.recorded", actor, model.CheckResultEventPayload{
		ResultID: result.ID, Issue: issueKey, CheckIssue: check.IssueKey,
		CheckArtifactID: check.ArtifactID, CheckBlockID: check.BlockID, CheckVersion: check.Version,
		When: check.Attributes.When, Verdict: result.Verdict, Role: result.Role, CommitSHA: result.CommitSHA,
	}))
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	seq := int64(event.Seq)
	result.EventSeq = &seq
	if err := insertCheckResult(r.Context(), tx, &result); err != nil {
		s.writeHandlerError(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		s.writeHandlerError(w, err)
		return
	}
	// The read-through runs before the event: the Verification tab refetches on
	// result.recorded, and no later event refreshes it, so a tab that read the results before
	// the pull request row landed would show this result standing at no basis until something
	// else happened.
	s.readThroughPullRequests(r.Context(), check.IssueKey, result.CommitSHA, checkRepo)
	s.publish(r.Context(), event)
	WriteJSON(w, http.StatusCreated, checkResultRecorded{
		Line:   checkResultLine(check, result),
		Result: result,
	})
}

// issueCheckResults is the body GET /api/v1/issues/{key}/results answers.
type issueCheckResults struct {
	Checks                 []model.CheckBlock `json:"checks"`
	Issue                  string             `json:"issue"`
	VerificationArtifactID string             `json:"verification_artifact_id"`
}

// GET /api/v1/issues/{key}/results  (any authenticated caller)
//
// The checks of the issue's Verification document in document order, each with what its
// results are compared with and whether a pass of it stands, and every result recorded
// against it from any issue, newest first, each with where its commit stands and whether the
// check's words moved since. An issue with no Verification document has no checks and no
// error.
//
// A check CI proves carries `ci` too, read from GitHub at the issue's newest pull request
// head, and holds no result a role recorded: its results are the one derived from the run its
// `ci_check` names (deriveCIResults), which is judged as a recorded result is, beside each
// result a run of its CI job recorded (role ci, branch set), newest first. A reader of one
// result of such a check finds the derived one by its id (ci-run:<check run id>), not as the
// first.
//
// ?at=<commit> replaces what every check is compared with by the one commit the reader names.
// The only read of GitHub it leaves is the check runs of a check CI proves, read at the issue's
// newest pull request head as on every read: the derived result is that head's, and stands at
// the named commit only where that commit is the head.
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
	checks, err := s.checkBlocksOf(r.Context(), s.deps.Store.Pool, artifact)
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
	if err := s.deriveCIResults(r.Context(), issueKey, checks); err != nil {
		s.writeHandlerError(w, err)
		return
	}
	// Every head GitHub has to be asked for is learned before the first check is answered, so
	// two `before_merge` checks of one issue name one list of bases and not two
	// (comparisonReader.learn).
	reader := newComparisonReader(s, at)
	reader.learn(r.Context(), checks)
	for index := range checks {
		reader.answer(r.Context(), &checks[index])
	}
	answerCompatibilityCurrency(checks, at)
	body.Checks = checks
	WriteJSON(w, http.StatusOK, body)
}

// refusalFor is every refusal a result's own fields earn before anything is read, for the
// credential that sends them and body, the bytes they were decoded from. A CI job's results
// token (model.Credential.ResultsOnly) is held to its run (ciJobRefusal); every other credential
// records a role's result, held to the rules below.
func (input checkResultInput) refusalFor(credential *model.Credential, body []byte) error {
	if credential != nil && credential.ResultsOnly {
		return input.ciJobRefusal(credential.CI, body)
	}
	if strings.TrimSpace(input.Check) == "" {
		return errorf(http.StatusBadRequest, "RESULT_INPUT", "check names the check block the result proves")
	}
	if !checkResultRole.MatchString(input.Role) {
		return errorf(http.StatusBadRequest, "RESULT_INPUT",
			"role is required and is at most 32 characters of a-z and -, the word the pull-request line prints")
	}
	if input.Verdict != "pass" && input.Verdict != "fail" {
		return errorf(http.StatusBadRequest, "RESULT_INPUT", "verdict is pass or fail")
	}
	if !resultCommitSHA.MatchString(input.Commit) {
		return errorf(http.StatusBadRequest, "RESULT_INPUT", "commit is the 40-character lowercase hex commit the check ran at")
	}
	for name, count := range map[string]int{"failed": input.Failed, "skipped": input.Skipped, "excluded": input.Excluded} {
		if count < 0 {
			return errorf(http.StatusBadRequest, "RESULT_INPUT", "%s counts cases and cannot be negative", name)
		}
	}
	if input.Verdict == "pass" && (input.Failed > 0 || input.Skipped > 0 || input.Excluded > 0) {
		return errorf(http.StatusBadRequest, "RESULT_CONTRADICTS",
			"a pass counts no failed, skipped or excluded case; this one counts %d failed, %d skipped and %d excluded",
			input.Failed, input.Skipped, input.Excluded)
	}
	return nil
}

// ciJobRefusal is every refusal the body of a CI job's result earns before anything is read, run
// being the run its results token was minted for. The body names what the run observed and
// nothing about who the job is, which Dispatch reads from the token and from GitHub, so a member
// beyond ciJobResultInput's four is refused by name rather than ignored; a body that names one
// is a caller that thinks it sets it. The commit is the run's, and the run is of a branch push,
// whose branch its row names (ciJobCheckResult).
func (input checkResultInput) ciJobRefusal(run *oauthwire.CIRun, body []byte) error {
	if undeclared := undeclaredMembers(&ciJobResultInput{}, body); len(undeclared) > 0 {
		return errorf(http.StatusBadRequest, codeResultInput,
			"a CI job's result takes check, commit, verdict and observed alone, and names %s: Dispatch reads who recorded a result from its token, never from its body",
			strings.Join(undeclared, ", "))
	}
	if strings.TrimSpace(input.Check) == "" {
		return errorf(http.StatusBadRequest, codeResultInput, "check names the check block the result proves")
	}
	if input.Verdict != "pass" && input.Verdict != "fail" {
		return errorf(http.StatusBadRequest, codeResultInput, "verdict is pass or fail")
	}
	if !resultCommitSHA.MatchString(input.Commit) {
		return errorf(http.StatusBadRequest, codeResultInput, "commit is the 40-character lowercase hex commit the job tested")
	}
	if run == nil {
		// brokerTokenActor carries the run of every results token it accepts.
		return errors.New("a results token's credential carries no run")
	}
	if input.Commit != run.SHA {
		return commitNotTheRuns(input.Commit, run.SHA)
	}
	if pushedBranch(*run) == "" {
		return errorf(http.StatusForbidden, codeRefNotABranch,
			"this token's run is of %q, which names no branch: a CI job records a result only for the push of a branch",
			run.Ref)
	}
	return nil
}

// pushedBranch is the branch whose push run ran on: its ref without refs/heads/, and "" for a ref
// that names no branch.
func pushedBranch(run oauthwire.CIRun) string {
	branch, ok := strings.CutPrefix(run.Ref, branchRefPrefix)
	if !ok {
		return ""
	}
	return branch
}

// issuesOpenForResult answers whether the recording issue and the check's issue are open, in
// one query over the rows the caller has already locked (lockIssueRows). It makes no refusal of
// its own for a closed issue, as requireOpenOwnerStatus does: the results route decides for itself,
// since a closed issue takes a result for an after-merge check. An issue with no row is the
// route's 404, which is the lock's answer too: lockIssueRows skips a key nothing holds.
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

// resolveCheck finds the check a result names among the Verification documents of the recording
// issue and of every issue above it. The value is a block id, or the canonical block reference
// (`dispatch://KEY/artifact/verification-md#b-<id>`) that names one document when a block id is
// not unique in that set: an id two documents share is 409 CHECK_AMBIGUOUS, and an id no
// Verification document of the set holds — or one that is not a `check` block — is 400
// UNKNOWN_CHECK.
func (s *server) resolveCheck(ctx context.Context, q queryer, issueKey, value string) (resolvedCheck, error) {
	ancestors, err := refs.IssueAncestors(ctx, q, issueKey)
	if err != nil {
		return resolvedCheck{}, err
	}
	scope := append([]string{issueKey}, ancestors...)

	blockID, artifactFilter := value, ""
	if ref, ok := text.CanonicalBlockRef(value); ok {
		// A block of a document carries its document's fields and no Item; a block of a comment
		// or a message names that item in Item and is no check.
		if ref.Item != "" || ref.IssueKey == "" || ref.ID == "" || ref.Block == "" {
			return resolvedCheck{}, unknownCheck(value)
		}
		blockID = ref.Block
		document, held, err := s.verificationDocument(ctx, q, ref.IssueKey, ref.ID)
		if err != nil {
			return resolvedCheck{}, err
		}
		if !held {
			return resolvedCheck{}, unknownCheck(value)
		}
		artifactFilter = document.ID
	}

	rows, err := q.Query(ctx, `
		select a.issue_key, a.slug, d.artifact_id::text, d.changed_version, d.text_sha256
		from document_blocks d
		join artifacts a on a.id = d.artifact_id
		where `+aliasedVerificationRole+`
		  and a.issue_key = any($1) and d.type = 'check' and d.block_id = $2
		  and ($3 = '' or d.artifact_id = $3::uuid)
		order by a.issue_key
	`, scope, blockID, artifactFilter)
	if err != nil {
		return resolvedCheck{}, fmt.Errorf("resolve check %q of %s: %w", value, issueKey, err)
	}
	defer rows.Close()
	var found []resolvedCheck
	var references []string
	for rows.Next() {
		check := resolvedCheck{BlockID: blockID}
		var slug string
		if err := rows.Scan(&check.IssueKey, &slug, &check.ArtifactID, &check.Version, &check.SHA256); err != nil {
			return resolvedCheck{}, fmt.Errorf("resolve check %q of %s: %w", value, issueKey, err)
		}
		if reference, ok := text.FormatBlockRef(text.Ref{
			Kind: "block", IssueKey: check.IssueKey, ID: slug, Block: blockID,
		}); ok {
			check.Ref = reference
			references = append(references, reference)
		}
		found = append(found, check)
	}
	if err := rows.Err(); err != nil {
		return resolvedCheck{}, fmt.Errorf("resolve check %q of %s: %w", value, issueKey, err)
	}
	if len(found) == 0 {
		return resolvedCheck{}, unknownCheck(value)
	}
	if len(found) > 1 {
		issues := make([]string, len(found))
		for index, check := range found {
			issues[index] = check.IssueKey
		}
		return resolvedCheck{}, errorf(http.StatusConflict, "CHECK_AMBIGUOUS",
			"check %s is a check of the Verification document of %s: name one with its reference, %s",
			blockID, strings.Join(issues, " and "), strings.Join(references, " or "))
	}
	check := found[0]
	if err := s.readCheckAttributes(ctx, &check); err != nil {
		return resolvedCheck{}, err
	}
	return check, nil
}

func unknownCheck(value string) error {
	return errorf(http.StatusBadRequest, "UNKNOWN_CHECK",
		"%s is no check of this issue's Verification document or of one above it", value)
}

// refreshResolvedCheck re-reads, under the locks the transaction holds, the indexed version and
// digest of a check resolved before it opened (recordCheckResult), so the row records the block
// as it stands at the commit, and its attributes with them when that block moved: the refusal a
// closed issue earns turns on `when`, the refusal of a result by another recorder on ci_check,
// run, `when` and negative control, and a CI job's row takes the check's surface and negative
// control, all read before the lock, and a version landing in between can have changed them. A
// block the index no longer holds is no check any more.
//
// The digest is document_blocks.text_sha256, the sha256 of the block's whole markdown as the
// index writes it (refs.BlockTextSum over markdown[From:To]), its opening line and that line's
// attributes included, so a check reworded from before_merge to after_merge, or given a
// ci_check, carries a digest of its own. A digest the resolve already read is therefore the same
// words under the same attributes, and the live document is not read a second time for them.
// That read is the slow one to hold two issue row locks across: a document version landing
// between the resolve and the lock is exactly what leaves the read cache cold
// (docs/read_cache.go), so the one case the second read would answer differently is the one it
// would answer slowest in. Only a digest that moved pays for it.
func (s *server) refreshResolvedCheck(ctx context.Context, q queryer, check *resolvedCheck, named string) error {
	resolved := check.SHA256
	err := q.QueryRow(ctx, `
		select changed_version, text_sha256 from document_blocks
		where artifact_id = $1::uuid and block_id = $2 and type = 'check'
	`, check.ArtifactID, check.BlockID).Scan(&check.Version, &check.SHA256)
	if errors.Is(err, pgx.ErrNoRows) {
		return unknownCheck(named)
	}
	if err != nil {
		return fmt.Errorf("read the check %s of document %s: %w", check.BlockID, check.ArtifactID, err)
	}
	if check.SHA256 == resolved {
		return nil
	}
	return s.readCheckAttributes(ctx, check)
}

// readCheckAttributes reads the attributes of check's block out of the live document, as the
// rules read them (docs.CheckAttributesOfBlock), and its surface as written. The index holds a
// block's type and digest but not its attributes, so the document itself is the one place they
// are written.
func (s *server) readCheckAttributes(ctx context.Context, check *resolvedCheck) error {
	// The index holds the block, so the live document has it too unless a write landed between
	// the two reads; such a block carries the schema's defaults alone, `when` before_merge.
	attributes, surface := docs.CheckAttributes{When: docs.CheckWhenBeforeMerge}, ""
	err := s.typedBlocksIn(ctx, check.ArtifactID, "check", func(node *pmdoc.Node, id string) {
		if id == check.BlockID {
			block := docs.CheckBlockOf(node)
			attributes, surface = docs.CheckAttributesOfBlock(block), block.Surface
		}
	})
	if err != nil {
		return err
	}
	check.Attributes, check.Surface = attributes, surface
	return nil
}

// validCICheck reports whether check, a block the results read loaded with its results, is one
// CI proves: it carries ci_check and keeps the four rules of such a check, the three of
// docs.CICheckRule and that no result a role recorded stands against it, which holdsRoleResult
// answers from the rows the read loaded. Only such a check is CI's to the results routes; any
// other is an ordinary check, whatever its ci_check says.
func validCICheck(check model.CheckBlock, holdsRoleResult bool) bool {
	attributes := docs.CheckAttributesOfBlock(check)
	return docs.CarriesCICheck(attributes) && docs.CICheckRule(attributes) == "" && !holdsRoleResult
}

// refuseResultOfAnotherRecorder refuses a result whose recorder is not the one that does the work
// of its check (R12: nobody writes a result for work that it did not do), in this order:
//
//  1. A check CI proves (docs.CICheckRuleIn answers "") takes no result from any credential but
//     a CI job's results token: 409 CHECK_PROVED_BY_CI, naming the block and the job.
//  2. A results token records only the result of a check CI proves whose ci_check, trimmed, is
//     the name of the token's check run: 409 CHECK_NOT_THIS_JOBS, naming the check, its
//     ci_check (or that it carries none) and the check run.
//  3. The token's check run is GitHub's at the token's commit (403 CHECK_RUN_NOT_THE_RUNS), and
//     the token's repository is the one the check's issue resolves to, compared without case
//     (server.checkIssueRepository; 403 REPOSITORY_NOT_THE_ISSUES), each naming both. refusalFor
//     has held the body's commit to the token's already.
//
// Any other credential's result for any other check is judged by the rest of the route. Whether
// a role recorded a result against the block, which decides whether it is CI's, is read through
// q: the pool before the transaction opens, and the transaction itself under the locks. run is
// the token's check run (ciJobCheckRun), and nil for every other credential.
func (s *server) refuseResultOfAnotherRecorder(ctx context.Context, q queryer, check resolvedCheck, credential *model.Credential, run *githubapp.CheckRun) error {
	carries := docs.CarriesCICheck(check.Attributes)
	rule := ""
	if carries {
		var err error
		if rule, err = docs.CICheckRuleIn(ctx, q, check.ArtifactID, check.BlockID, check.Attributes); err != nil {
			return err
		}
	}
	provedByCI := carries && rule == ""
	if credential == nil || !credential.ResultsOnly {
		if provedByCI {
			return errorf(http.StatusConflict, "CHECK_PROVED_BY_CI",
				"check %s names CI job %q: only a run of that job records its result, through the secrets broker",
				check.BlockID, check.Attributes.CICheck)
		}
		return nil
	}
	if run == nil || credential.CI == nil {
		return fmt.Errorf("judge a CI job's result for check %s: the token's check run was not read", check.BlockID)
	}
	if !provedByCI || check.Attributes.CICheck != run.Name {
		named := "carries no ci_check"
		switch {
		case carries && !provedByCI:
			named = fmt.Sprintf("names CI job %q and is no check CI proves (%s)", check.Attributes.CICheck, rule)
		case carries:
			named = fmt.Sprintf("names CI job %q", check.Attributes.CICheck)
		}
		return errorf(http.StatusConflict, codeCheckNotThisJobs,
			"check %s %s, and this token's check run is %q: a job records results only for the checks whose ci_check names its check run",
			check.BlockID, named, run.Name)
	}
	if run.HeadSHA != credential.CI.SHA {
		return checkRunNotTheRuns(run.HeadSHA, run.ID, credential.CI.SHA)
	}
	repo, answered := s.checkIssueRepository(ctx, q, check.IssueKey)
	if !answered {
		return fmt.Errorf("judge a CI job's result for check %s: the repository of %s did not resolve", check.BlockID, check.IssueKey)
	}
	if repo == nil || !strings.EqualFold(*repo, credential.CI.Repository) {
		resolves := "no repository"
		if repo != nil {
			resolves = *repo
		}
		return errorf(http.StatusForbidden, codeRepositoryNotTheIssues,
			"this token's run is of %s, and issue %s resolves to %s: a CI job records a result only for an issue of the repository it ran in",
			credential.CI.Repository, check.IssueKey, resolves)
	}
	return nil
}

// refuseBranchOfAnotherIssue refuses a CI job's result on issueKey's route when the branch whose
// push its run ran on does not belong to that issue (delivery.BranchOfIssue): a job's result
// counts for an issue only on a branch of that issue's change, so a job of one issue's branch
// never makes a pass stand for another's (403 BRANCH_NOT_THE_ISSUES). The branch is quoted, so
// a name holding a line break prints on one line.
func refuseBranchOfAnotherIssue(ctx context.Context, q refs.Queryer, issueKey, branch string) error {
	belongs, err := delivery.BranchOfIssue(ctx, q, issueKey, branch)
	if err != nil {
		return err
	}
	if !belongs {
		return errorf(http.StatusForbidden, codeBranchNotTheIssues,
			"this token's run pushed branch %q, which is no branch of issue %s: a branch belongs to an issue when it is the head branch of a pull request of that issue, or when the first issue key its name holds is that issue's, as Dispatch attributes a pull request by its branch",
			branch, issueKey)
	}
	return nil
}

// ciJobCheckRun is the check run a results token was minted in, as GitHub names it in the run's
// repository, through the cache the project's results route reads it through (specCheckRun). A
// GitHub that does not answer leaves the result unjudged: 503 CHECK_RUN_UNREADABLE, and the job
// writes it again.
func (s *server) ciJobCheckRun(ctx context.Context, ci oauthwire.CIRun) (githubapp.CheckRun, error) {
	id, err := strconv.ParseInt(ci.CheckRunID, 10, 64)
	if err != nil {
		// The verifier admits no other (brokertoken.shapeOf), so this is Dispatch's own fault.
		return githubapp.CheckRun{}, fmt.Errorf("read the check run of a results token: %w", err)
	}
	owner, name, _ := strings.Cut(ci.Repository, "/")
	run, err := s.specCheckRun(ctx, &repositoryReader{s: s, owner: owner, name: name}, ci.Repository, id)
	if err != nil {
		return githubapp.CheckRun{}, errorf(http.StatusServiceUnavailable, codeCheckRunUnreadable,
			"GitHub did not answer the read of check run %d of %s, so the result was not judged; write it again: %v",
			id, ci.Repository, err)
	}
	return run, nil
}

// roleCheckResult is the row a role's result is stored as: each field as the role recorded it,
// under the actor the request resolved to.
func roleCheckResult(input checkResultInput, check resolvedCheck, actor model.Actor, issueKey string) model.CheckResult {
	return model.CheckResult{
		ID: uuid.NewString(), IssueKey: issueKey, CheckIssueKey: check.IssueKey,
		CheckArtifactID: check.ArtifactID, CheckBlockID: check.BlockID,
		CheckVersion: check.Version, CheckSHA256: check.SHA256,
		Role: input.Role, Surface: input.Surface, Run: input.Run, Observed: input.Observed,
		CommitSHA: input.Commit, NegativeControl: input.NegativeControl,
		Failed: input.Failed, Skipped: input.Skipped, Excluded: input.Excluded,
		Verdict: input.Verdict, Screenshots: nonNilStrings(input.Screenshots),
		Session: sessionOf(actor), Actor: actor,
	}
}

// ciJobActor is the actor a CI job's result and its event are recorded under: the session of the
// run its results token proved, and no person. The person the broker's result entry names ran
// nothing, so it stays inside the server, as on the project's results route
// (model.ResultRecorder): the airlock authenticated the run (R12).
func ciJobActor(actor model.Actor) model.Actor {
	actor.Owner = nil
	return actor
}

// ciJobCheckResult is the row a CI job's result is stored as: what its run observed, under the
// identity its token proved and nothing its body named. Its role is ci and its run the check
// run's name, as on the result derived from that run (ciAnswerOf); what it observed is kept by
// the rule of the project's results route (cutObserved); its surface and negative control are
// the check's; its commit is the run's, which ciJobRefusal held the body's to; its session and
// actor are the run's (ciJobActor); and Branch is the branch whose push the run ran on, which
// marks the row a CI job's, so the fourth rule of its check never counts it
// (docs.CICheckRuleIn). Attestation is the in-toto statement of its run
// (attestedResult.statement), as the project's results route builds one: attempt is the run's
// attempt (runNumbersOf), and principal the broker's principal of the run (resultPrincipal).
func ciJobCheckResult(input checkResultInput, check resolvedCheck, actor model.Actor, run githubapp.CheckRun, ci oauthwire.CIRun, attempt int, principal string, issueKey string) model.CheckResult {
	session, observed := sessionOf(actor), cutObserved(input.Observed)
	statement := attestedResult{
		predicateType: checkBlockResultPredicateType, project: *actor.Project, check: check.Ref,
		procedureDigest: check.SHA256,
		test:            model.ResultTest{Job: check.Attributes.CICheck},
		verdict:         input.Verdict, observed: observed, checkRunName: run.Name,
		run: ci, attempt: attempt, session: session, principal: principal,
	}.statement()
	return model.CheckResult{
		ID: uuid.NewString(), IssueKey: issueKey, CheckIssueKey: check.IssueKey,
		CheckArtifactID: check.ArtifactID, CheckBlockID: check.BlockID,
		CheckVersion: check.Version, CheckSHA256: check.SHA256,
		Role: ciResultRole, Surface: check.Surface, Run: run.Name, Observed: observed,
		CommitSHA: ci.SHA, Branch: pushedBranch(ci), NegativeControl: check.Attributes.NegativeControl,
		Verdict: input.Verdict, Screenshots: []string{},
		Session: session, Actor: actor, Attestation: &statement,
	}
}

// typedBlocksIn walks the live markdown of one document and hands visit each block of blockType
// in document order, with its block id. The markdown is read live and parsed; nothing is
// written. A rendering can repeat an id; the block index keys one row per id, and so does this,
// so only the first holder of an id is visited. Both readers of a live document's typed blocks
// come through here: the checks of a Verification document (checkBlocksIn) and the requirements
// of a spec (requirementBlocksIn).
func (s *server) typedBlocksIn(ctx context.Context, artifactID, blockType string, visit func(node *pmdoc.Node, id string)) error {
	markdown, err := s.deps.Docs.Text(ctx, artifactID)
	if err != nil {
		return err
	}
	tree, err := pmdoc.ParseRendering(markdown)
	if err != nil {
		return fmt.Errorf("read the %s blocks of document %s: %w", blockType, artifactID, err)
	}
	seen := map[string]struct{}{}
	pmdoc.Walk(tree, func(node *pmdoc.Node) bool {
		if node.Type != blockType {
			return true
		}
		id, _ := node.Attrs[pmdoc.BlockIDAttr].(string)
		if id == "" {
			return true
		}
		if _, repeated := seen[id]; repeated {
			return true
		}
		seen[id] = struct{}{}
		visit(node, id)
		return true
	})
	return nil
}

// checkBlocksIn is every `check` block of one document, in document order, with its attributes
// and its words (typedBlocksIn).
func (s *server) checkBlocksIn(ctx context.Context, artifactID string) ([]model.CheckBlock, error) {
	var checks []model.CheckBlock
	err := s.typedBlocksIn(ctx, artifactID, "check", func(node *pmdoc.Node, _ string) {
		check := docs.CheckBlockOf(node)
		check.ArtifactID, check.Text, check.Results = artifactID, blockWords(node), []model.CheckResult{}
		checks = append(checks, check)
	})
	if err != nil {
		return nil, err
	}
	return checks, nil
}

// checkBlocksOf is checkBlocksIn with each block's reference, issue and indexed version and
// digest filled in. The reference is built from the document's own slug, which is a property of
// the artifacts row and not of a block row, so a Verification document whose blocks the index
// does not hold yet still answers a reference every check can be addressed by.
func (s *server) checkBlocksOf(ctx context.Context, q queryer, artifact model.Artifact) ([]model.CheckBlock, error) {
	checks, err := s.checkBlocksIn(ctx, artifact.ID)
	if err != nil {
		return nil, err
	}
	if len(checks) == 0 {
		return []model.CheckBlock{}, nil
	}
	issueKey := ""
	if artifact.IssueKey != nil {
		issueKey = *artifact.IssueKey
	}
	indexed := make(map[string]model.CheckBlock, len(checks))
	rows, err := q.Query(ctx, `
		select block_id, changed_version, text_sha256
		from document_blocks where artifact_id = $1 and type = 'check'
	`, artifact.ID)
	if err != nil {
		return nil, fmt.Errorf("read the indexed checks of %s: %w", artifact.ID, err)
	}
	defer rows.Close()
	for rows.Next() {
		var block model.CheckBlock
		if err := rows.Scan(&block.BlockID, &block.Version, &block.SHA256); err != nil {
			return nil, fmt.Errorf("read the indexed checks of %s: %w", artifact.ID, err)
		}
		indexed[block.BlockID] = block
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the indexed checks of %s: %w", artifact.ID, err)
	}
	for index := range checks {
		checks[index].IssueKey = issueKey
		checks[index].Ref, _ = text.FormatBlockRef(text.Ref{
			Kind: "block", IssueKey: issueKey, ID: artifact.Slug, Block: checks[index].BlockID,
		})
		if block, ok := indexed[checks[index].BlockID]; ok {
			checks[index].Version, checks[index].SHA256 = block.Version, block.SHA256
		}
	}
	return checks, nil
}

// blockWords is a block's text as a reader sees it: every text node under it, joined by single
// blanks, with the text of a link and every dispatch:// reference dropped, so the pull-request
// body line the result implies carries no link and no Dispatch host.
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

// insertCheckResult writes one result's row and fills in the created_at the row takes, which
// the record route answers with: the pointer is the caller's own value, not a copy of it. A
// role's row, whose Branch is "", stores a null branch (docs.CICheckRuleIn). A role's row stores a
// null statement; a CI job's stores its statement with the row's own recorded_at
// (stampedAttestation), and the result answers the stored one, so the record's answer is what the
// read answers.
func insertCheckResult(ctx context.Context, tx pgx.Tx, result *model.CheckResult) error {
	actorJSON, err := encodeJSON(result.Actor)
	if err != nil {
		return err
	}
	// A role's row stores a null statement: an untyped nil binds as SQL NULL.
	var attestation any
	if result.Attestation != nil {
		encoded, err := json.Marshal(result.Attestation)
		if err != nil {
			return fmt.Errorf("encode the statement of a result: %w", err)
		}
		attestation = encoded
	}
	var stored []byte
	if err := tx.QueryRow(ctx, `
		insert into check_results (
			id, issue_key, check_issue_key, check_artifact_id, check_block_id, check_version,
			check_sha256, role, surface, run, observed, commit_sha, negative_control,
			failed, skipped, excluded, verdict, screenshots, session, actor, event_seq, branch, attestation
		)
		values ($1::uuid, $2, $3, $4::uuid, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21,
			nullif($22::text, ''), `+stampedAttestation("$23")+`)
		returning created_at, attestation
	`,
		result.ID, result.IssueKey, result.CheckIssueKey, result.CheckArtifactID, result.CheckBlockID,
		result.CheckVersion, result.CheckSHA256, result.Role, result.Surface, result.Run, result.Observed,
		result.CommitSHA, result.NegativeControl, result.Failed, result.Skipped, result.Excluded,
		result.Verdict, result.Screenshots, result.Session, actorJSON, result.EventSeq, result.Branch, attestation,
	).Scan(&result.CreatedAt, &stored); err != nil {
		return err
	}
	return decodeAttestation(stored, &result.Attestation)
}

// loadCheckResults is every result recorded against the checks of one Verification document,
// from any issue, by the block id of the check each names, newest first within each. One query
// serves the whole document: a Verification document holding twenty checks is one round trip,
// not twenty.
func loadCheckResults(ctx context.Context, q queryer, artifactID string) (map[string][]model.CheckResult, error) {
	rows, err := q.Query(ctx, `
		select `+checkResultColumns+`
		from check_results
		where check_artifact_id = $1::uuid
		order by check_block_id, created_at desc, id desc
	`, artifactID)
	if err != nil {
		return nil, fmt.Errorf("read the results of document %s: %w", artifactID, err)
	}
	defer rows.Close()
	results := map[string][]model.CheckResult{}
	for rows.Next() {
		result, err := scanCheckResult(rows)
		if err != nil {
			return nil, fmt.Errorf("read the results of document %s: %w", artifactID, err)
		}
		results[result.CheckBlockID] = append(results[result.CheckBlockID], result)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the results of document %s: %w", artifactID, err)
	}
	return results, nil
}

func scanCheckResult(row pgx.Row) (model.CheckResult, error) {
	var result model.CheckResult
	var actorJSON, attestationJSON []byte
	if err := row.Scan(
		&result.ID, &result.IssueKey, &result.CheckIssueKey, &result.CheckArtifactID, &result.CheckBlockID,
		&result.CheckVersion, &result.CheckSHA256, &result.Role, &result.Surface, &result.Run,
		&result.Observed, &result.CommitSHA, &result.NegativeControl, &result.Failed, &result.Skipped,
		&result.Excluded, &result.Verdict, &result.Screenshots, &result.Session, &actorJSON,
		&result.CreatedAt, &result.EventSeq, &result.Branch, &attestationJSON,
	); err != nil {
		return model.CheckResult{}, err
	}
	if err := json.Unmarshal(actorJSON, &result.Actor); err != nil {
		return model.CheckResult{}, fmt.Errorf("decode a result's actor: %w", err)
	}
	if err := decodeAttestation(attestationJSON, &result.Attestation); err != nil {
		return model.CheckResult{}, err
	}
	return result, nil
}

// decodeAttestation reads a stored check_results.attestation into *into: nil for a null column,
// which every row but a CI job's holds.
func decodeAttestation(stored []byte, into **model.ResultAttestation) error {
	if stored == nil {
		*into = nil
		return nil
	}
	statement := new(model.ResultAttestation)
	if err := json.Unmarshal(stored, statement); err != nil {
		return fmt.Errorf("decode a result's statement: %w", err)
	}
	*into = statement
	return nil
}

// refuseUnknownCommit refuses a result at a commit repo definitely does not hold: 400
// RESULT_COMMIT_UNKNOWN, naming the commit and that `owner/repo`. repo is the repository the
// check's issue resolves to, which recordCheckResult reads once for this refusal and for the
// read-through after the write; a nil repo is nothing to ask GitHub about and no refusal. A
// deployment with no App key resolves none, so the key is not checked again here.
//
// It runs before the transaction opens, so a refused result leaves no check_results row and no
// result.recorded event behind. Dispatch stored a result at a mistyped commit no repository
// held once, and a reader could not tell it from the real head, since both print as twelve
// characters.
//
// It refuses on nothing but GitHub's proof that the repository does not hold the commit, which
// delivery.RepositoryHoldsCommit defines as the one 422 whose message says GitHub found no
// commit for the SHA. No other GitHub answer can block the result a closed issue takes, the
// production check's after-merge result — whose merge commit is on the branch its pull request
// merged into and is held anyway.
func (s *server) refuseUnknownCommit(ctx context.Context, repo *string, sha string) error {
	if repo == nil {
		return nil
	}
	held, err := delivery.RepositoryHoldsCommit(ctx, s.deps.GitHub, *repo, sha)
	if err != nil {
		slog.Info("dispatch: GitHub could not say whether a recorded result's commit is held",
			"repo", *repo, "commit", sha, "error", err)
		return nil
	}
	if held {
		return nil
	}
	return errorf(http.StatusBadRequest, "RESULT_COMMIT_UNKNOWN",
		"%s holds no commit %s: record the result once the commit is pushed", *repo, sha)
}

// readThroughPullRequests teaches Dispatch the pull requests GitHub names for a result's
// commit when that commit is the head of none it already knows for the check's issue, so a
// result recorded at the head of a pull request no event has reached Dispatch for stands at
// that head at once. It asks about the check's own issue and reads through repo, the one
// repository that issue resolves to, which recordCheckResult resolved before the write: the
// issue comparisonReader.headBases names the heads of. A nil repo — a deployment with no App
// key, an issue resolving to no repository, or a resolve that failed — leaves nothing to read
// through, and it is answered before the database is read, since no head this read found
// could be used.
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
