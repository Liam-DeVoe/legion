package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
	"github.com/sjawhar/legion/daemon/internal/githubrest"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// sideReviewGitHubRead bounds a side-review route's read of its pull request from GitHub: 30
// seconds, the bound the daemon's writer of legion-verdict gives its read of the same pull request
// (internal/daemon's issueRead), since the API holds no bound of its own for a GitHub read.
const sideReviewGitHubRead = 30 * time.Second

// Side reviews. A pull request that no Legion issue owns, on a verdict repository whose entry takes
// side reviews (config.VerdictRepository.SideReviews), has no phase and so no reviewer of its own:
// handoff_complete takes a verdict only from the role that works an issue's phase. So the project's
// controller names a running Legion reviewer whose tree did not write the change and is on the
// reviewed repository, for that pull request at its head (pull_request_reviewer), and relays the
// verdict that reviewer sends it (pull_request_verdict). The daemon's writer of legion-verdict reads
// that verdict, a store.Verdict of no issue, as the verdict on the head (the daemon's
// outsideInputs). One caller the daemon knows, the controller, stands between a reviewer session
// and the merge check, and the daemon itself refuses a verdict from a session the controller did not
// name for that pull request and head.
//
// Both routes take a call only from the project's controller claim, only for an open pull request
// no Legion issue owns on a repository whose entry takes side reviews, and only at the pull
// request's current head, as GitHub answers it; every other call is refused VERDICT_CALLER_FORBIDDEN
// (sideCaller, sidePullRequest). Every refusal is written before anything is recorded.

// PullRequestReviewerRequest is `POST /legion/v1/pull-request-reviewer`'s body: the controller's
// naming of Session, a running Legion reviewer's session, as the side reviewer of pull request
// Number of Repository at Head. The `legion` tool's pull_request_reviewer sends it, and contracts'
// LegionPullRequestReviewerRequest mirrors it.
type PullRequestReviewerRequest struct {
	GrantID    string `json:"grantId"`
	Repository string `json:"repository"`
	Number     int    `json:"number"`
	Head       string `json:"head"`
	Session    string `json:"session"`
}

// PullRequestVerdictRequest is `POST /legion/v1/pull-request-verdict`'s body: the verdict, approve
// or changes, that the controller relays from Session, the reviewer it named for pull request
// Number of Repository at Head. The `legion` tool's pull_request_verdict sends it, and contracts'
// LegionPullRequestVerdictRequest mirrors it.
type PullRequestVerdictRequest struct {
	GrantID    string `json:"grantId"`
	Repository string `json:"repository"`
	Number     int    `json:"number"`
	Head       string `json:"head"`
	Verdict    string `json:"verdict"`
	Session    string `json:"session"`
}

// pullRequestReviewer records the controller's naming of a side reviewer: the session, a running
// reviewer claim's of this project (liveReviewer), whose verdict on the pull request at that head the
// daemon takes. A later naming of the same pull request and head stands over an earlier one
// (store.SideReviewerTx), and where it names another session it asks the writer to decide the head
// again, since the verdict the earlier reviewer gave stands no longer.
func (s *server) pullRequestReviewer(w http.ResponseWriter, r *http.Request) {
	var req PullRequestReviewerRequest
	if !s.readBody(w, r, &req, s.redeemIdentityCheck) || !s.requireSideFields(w, r, req.Number, field{"grantId", req.GrantID},
		field{"repository", req.Repository}, field{"head", req.Head}, field{"session", req.Session}) {
		return
	}
	if !s.sideCaller(w, r, "pull_request_reviewer", req.GrantID) {
		return
	}
	repository, ok := s.sidePullRequest(w, r, req.Repository, req.Number, req.Head)
	if !ok {
		return
	}
	ctx := r.Context()
	live, err := s.liveReviewer(ctx, req.Session)
	if err != nil {
		s.logRequestFailure(r, err, "api: read the claims a side reviewer may be named from", "session", req.Session)
		s.writeFailure(w, r, http.StatusInternalServerError, "RECORD_UNAVAILABLE", "could not read the project's claims; nothing was recorded")
		return
	}
	if !live {
		s.writeFailure(w, r, http.StatusConflict, "VERDICT_REVIEWER_NOT_LIVE", fmt.Sprintf(
			"session %s is no running reviewer of this project: name the session of a reviewer claim that is ready, working or idle; nothing was recorded", req.Session))
		return
	}
	var replaced bool
	if err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		earlier, found, err := store.SideReviewerTx(ctx, tx, repository, req.Number, req.Head)
		if err != nil {
			return err
		}
		replaced = found && earlier.Session != req.Session
		_, err = store.RecordSideReviewerTx(ctx, tx, store.SideReviewer{Repository: repository, Number: req.Number, Head: req.Head,
			Session: req.Session, RecordedAt: s.now().UTC()})
		return err
	}); err != nil {
		s.logRequestFailure(r, err, "api: record a pull request reviewer", "repository", repository.String(), "number", req.Number)
		s.writeFailure(w, r, http.StatusInternalServerError, "RECORD_UNAVAILABLE", "could not record the pull request's reviewer; nothing was recorded")
		return
	}
	s.log.Info("api: recorded a pull request reviewer", "repository", repository.String(), "number", req.Number, "head", req.Head,
		"reviewer_session", req.Session, "replaced", replaced)
	if replaced && s.writeVerdict != nil {
		s.writeVerdict(repository.String(), req.Number, req.Head)
	}
	s.writeAnswer(w, r, AnswerEmptyObject, http.StatusOK, EmptyResponse{})
}

// liveReviewer is whether session is the session of a running reviewer claim of this project: a
// claim of the reviewer role whose agent said it can be prompted (supervise.SaidReady). A daemon
// that supervises no claims has none.
func (s *server) liveReviewer(ctx context.Context, session string) (bool, error) {
	if s.supervisor == nil {
		return false, nil
	}
	claims, err := s.supervisor.Claims(ctx)
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(claims, func(c supervise.Claim) bool {
		return c.Role == claim.RoleReviewer && c.Session == session && supervise.SaidReady(c.State)
	}), nil
}

// pullRequestVerdict records the verdict the controller relays from a side reviewer, and asks the
// daemon's writer for legion-verdict on the head once it has committed. It takes the verdict only
// from the session the newest naming of that pull request and head names, and refuses any other
// VERDICT_REVIEWER_NOT_NAMED, a session named at another head included: a reviewer's verdict is on
// the code it read. A verdict that is neither approve nor changes is refused
// REVIEWER_VERDICT_REQUIRED.
func (s *server) pullRequestVerdict(w http.ResponseWriter, r *http.Request) {
	var req PullRequestVerdictRequest
	if !s.readBody(w, r, &req, s.redeemIdentityCheck) || !s.requireSideFields(w, r, req.Number, field{"grantId", req.GrantID},
		field{"repository", req.Repository}, field{"head", req.Head}, field{"session", req.Session}) {
		return
	}
	if !s.sideCaller(w, r, "pull_request_verdict", req.GrantID) {
		return
	}
	// No plugin before ReviewerVerdictContract offers this call, so its verdict is read in the
	// reviewer's own two words alone.
	verdict, refusal := reviewerVerdictOf(req.Verdict, ReviewerVerdictContract)
	if refusal != "" {
		s.writeFailure(w, r, http.StatusBadRequest, "REVIEWER_VERDICT_REQUIRED", refusal)
		return
	}
	repository, ok := s.sidePullRequest(w, r, req.Repository, req.Number, req.Head)
	if !ok {
		return
	}
	ctx := r.Context()
	var named store.SideReviewer
	var found bool
	if err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if named, found, err = store.SideReviewerTx(ctx, tx, repository, req.Number, req.Head); err != nil || !found || named.Session != req.Session {
			return err
		}
		_, err = store.RecordVerdictTx(ctx, tx, store.Verdict{Repository: repository, Number: req.Number, Head: req.Head,
			Verdict: store.VerdictWord(verdict), Session: req.Session, RecordedAt: s.now().UTC()})
		return err
	}); err != nil {
		s.logRequestFailure(r, err, "api: record a pull request verdict", "repository", repository.String(), "number", req.Number)
		s.writeFailure(w, r, http.StatusInternalServerError, "RECORD_UNAVAILABLE", "could not record the pull request's verdict; nothing was recorded")
		return
	}
	if !found || named.Session != req.Session {
		recorded := "the controller named no reviewer of it at that head"
		if found {
			recorded = "the reviewer the controller named of it at that head is session " + named.Session
		}
		s.writeFailure(w, r, http.StatusForbidden, "VERDICT_REVIEWER_NOT_NAMED", fmt.Sprintf(
			"this verdict on pull request #%d of %s at %s is session %s's, and %s: relay only the verdict of the reviewer you named for that pull request and head; nothing was recorded",
			req.Number, repository, req.Head, req.Session, recorded))
		return
	}
	s.log.Info("api: recorded a pull request verdict", "repository", repository.String(), "number", req.Number, "head", req.Head,
		"verdict", verdict, "reviewer_session", req.Session)
	if s.writeVerdict != nil {
		s.writeVerdict(repository.String(), req.Number, req.Head)
	}
	s.writeAnswer(w, r, AnswerEmptyObject, http.StatusOK, EmptyResponse{})
}

// requireSideFields refuses a side-review call that leaves out one of fields, as
// requireFailureFields does, or names no pull request's number.
func (s *server) requireSideFields(w http.ResponseWriter, r *http.Request, number int, fields ...field) bool {
	if !s.requireFailureFields(w, r, fields...) {
		return false
	}
	if number <= 0 {
		s.writeFailure(w, r, http.StatusBadRequest, "MISSING_FIELD", fmt.Sprintf("number is the pull request's number, not %d", number))
		return false
	}
	return true
}

// sideCaller redeems grantID and answers whether it is the project's controller claim's: a grant
// the controller's registered session minted. A claim's grant and a controller grant the
// operator's bearer minted (credential.Grant.Operator) are refused VERDICT_CALLER_FORBIDDEN: the
// controller is the one caller that names a side reviewer and relays its verdict.
func (s *server) sideCaller(w http.ResponseWriter, r *http.Request, op, grantID string) bool {
	grant, ok := s.redeem(w, r, grantID)
	if !ok {
		return false
	}
	if grant.Controller && !grant.Operator {
		return true
	}
	who := "the operator's bearer"
	if !grant.Controller {
		who = "the " + string(grant.Role) + "'s claim"
	}
	s.writeFailure(w, r, http.StatusForbidden, "VERDICT_CALLER_FORBIDDEN", fmt.Sprintf(
		"%s is the project's controller's call, and this one comes from %s; nothing was recorded", op, who))
	return false
}

// sidePullRequest checks the pull request a side-review call names, and answers its repository
// under the name GitHub holds it under now (Options.Serving), which webhook facts record its
// reviews under and the side review is recorded under too. It refuses VERDICT_CALLER_FORBIDDEN a
// repository no verdict entry names or whose entry takes no side reviews, a pull request the
// daemon's record holds for a Legion issue, one GitHub does not have or shows closed, one on a
// Legion issue's branch (another project's included), and a head that is not the one GitHub
// shows the pull request at now. It reads that pull request from GitHub with the daemon's read
// token (GET /pulls/{number}), since the record of a pull request no Legion issue owns holds
// neither whether it is open nor, for one opened before the daemon heard its repository, its head.
func (s *server) sidePullRequest(w http.ResponseWriter, r *http.Request, repo string, number int, head string) (ghrepo.Repository, bool) {
	forbid := func(format string, args ...any) (ghrepo.Repository, bool) {
		s.writeFailure(w, r, http.StatusForbidden, "VERDICT_CALLER_FORBIDDEN", fmt.Sprintf(format, args...)+"; nothing was recorded")
		return ghrepo.Repository{}, false
	}
	if s.pool == nil {
		s.writeFailure(w, r, http.StatusInternalServerError, "RECORD_UNAVAILABLE", "the daemon's store is unavailable")
		return ghrepo.Repository{}, false
	}
	requested, err := ghrepo.Parse("the repository of a side review", repo)
	if err != nil {
		s.writeFailure(w, r, http.StatusBadRequest, "INVALID_REPOSITORY", err.Error()+"; nothing was recorded")
		return ghrepo.Repository{}, false
	}
	if s.verdictEntry == nil {
		return forbid("a side review is taken only on a verdict repository, and this daemon writes legion-verdict on none")
	}
	// The verdict is recorded, and the writer asked, under the name GitHub holds the repository
	// under now, so the entry is looked up under that name, as the writer looks it up
	// (workflowRuntime.verdictEntry): a call the writer would write nothing for is refused here,
	// before anything is recorded.
	repository := s.serving(requested)
	if entry, named := s.verdictEntry(repository); !named || !entry.SideReviews {
		return forbid("a side review is taken only on a repository whose verdict_repositories entry sets side_reviews: true, under the name GitHub holds it under now, and %s's entry does not", repository)
	}
	ctx := r.Context()
	var recorded *record.PullRequest
	if err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var read error
		recorded, read = ghrepo.Under(s.renames, requested.String(), func(name string) (*record.PullRequest, error) {
			return s.records.PullRequestByNumber(ctx, tx, name, number)
		})
		return read
	}); err != nil {
		s.logRequestFailure(r, err, "api: read whether a Legion issue owns a side review's pull request", "repository", requested.String(), "number", number)
		s.writeFailure(w, r, http.StatusInternalServerError, "RECORD_UNAVAILABLE", "could not read the pull request's record; nothing was recorded")
		return ghrepo.Repository{}, false
	}
	if recorded != nil {
		return forbid("the daemon records pull request #%d of %s as Legion issue %s's, whose own round's reviewer decides it", number, requested, recorded.Issue)
	}
	if s.tokens == nil {
		s.writeFailure(w, r, http.StatusInternalServerError, "GITHUB_TOKEN_SOURCE_UNAVAILABLE", "GitHub token source is unavailable")
		return ghrepo.Repository{}, false
	}
	// GitHub's own read, minted for and addressed to the repository under the name it holds now.
	lease, err := s.tokens.ReadToken(ctx, repository.Owner())
	if err != nil {
		s.logRequestFailure(r, err, "api: mint the read token for a side review's pull request", "repository", repository.String())
		s.writeFailure(w, r, http.StatusBadGateway, "GITHUB_TOKEN_FAILED", fmt.Sprintf("could not mint the read token for %s; nothing was recorded, so send the call again", repository))
		return ghrepo.Repository{}, false
	}
	github := githubrest.Client{Token: lease.Token, API: githubrest.RepositoryAPI(s.githubAPI, repository)}
	var pull githubrest.PullRequest
	read, cancel := context.WithTimeout(ctx, sideReviewGitHubRead)
	defer cancel()
	if err := github.Get(read, "/pulls/"+strconv.Itoa(number), &pull); err != nil {
		s.refuseGitHubRead(w, r, err, repository, number)
		return ghrepo.Repository{}, false
	}
	if pull.State != "open" {
		return forbid("pull request #%d of %s is %s, and a side review decides only an open pull request", number, repository, pull.State)
	}
	if key, issueBranch := claim.BranchIssue(pull.Head.Ref); issueBranch {
		return forbid("pull request #%d of %s is on %s, Legion issue %s's branch, whose own round's reviewer decides it", number, repository, pull.Head.Ref, key)
	}
	if pull.Head.SHA != head {
		return forbid("this call names head %s, and the head of pull request #%d of %s is %s: name the reviewer at that head, and relay the verdict it gives there",
			head, number, repository, pull.Head.SHA)
	}
	return repository, true
}

// refuseGitHubRead answers a side-review call whose read of pull request number of repository
// failed with err, in the code that says what a caller can do about it. GitHub's answer is logged
// with its status. A 404 is refused VERDICT_CALLER_FORBIDDEN: GitHub answers it for a pull request
// it does not have, and for a repository the App's installation cannot read. GitHub's rate limit is
// a 503 carrying the wait GitHub asked for (Retry-After); a 401 or 403 that is no rate limit is a
// 502 a retry does not mend (GITHUB_READ_REFUSED); any other answer, and a read that did not end
// within its bound, is a 502 to send again (PULL_REQUEST_READ_FAILED).
func (s *server) refuseGitHubRead(w http.ResponseWriter, r *http.Request, err error, repository ghrepo.Repository, number int) {
	var answer *githubrest.Answer
	if !errors.As(err, &answer) {
		s.logRequestFailure(r, err, "api: read a side review's pull request", "repository", repository.String(), "number", number)
		s.writeFailure(w, r, http.StatusBadGateway, "PULL_REQUEST_READ_FAILED", fmt.Sprintf(
			"could not read pull request #%d of %s from GitHub; nothing was recorded, so send the call again", number, repository))
		return
	}
	s.log.Warn("api: GitHub answered a side review's read of its pull request", "repository", repository.String(), "number", number,
		"status", answer.Status, "rate_limited", answer.RateLimited)
	switch {
	case answer.RateLimited:
		if answer.RetryAfter > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int((answer.RetryAfter+time.Second-1)/time.Second)))
		}
		s.writeFailure(w, r, http.StatusServiceUnavailable, "GITHUB_RATE_LIMITED", fmt.Sprintf(
			"GitHub's rate limit refused the read of pull request #%d of %s; nothing was recorded, so send the call again once Retry-After has passed", number, repository))
	case answer.Status == http.StatusNotFound:
		s.writeFailure(w, r, http.StatusForbidden, "VERDICT_CALLER_FORBIDDEN", fmt.Sprintf(
			"GitHub has no pull request #%d on %s that the App can read; nothing was recorded", number, repository))
	case answer.Status == http.StatusUnauthorized || answer.Status == http.StatusForbidden:
		s.writeFailure(w, r, http.StatusBadGateway, "GITHUB_READ_REFUSED", fmt.Sprintf(
			"GitHub refused the App's read of pull request #%d of %s (%d), which a retry does not mend: the App's installation on %s must grant it; nothing was recorded",
			number, repository, answer.Status, repository.Owner()))
	default:
		s.writeFailure(w, r, http.StatusBadGateway, "PULL_REQUEST_READ_FAILED", fmt.Sprintf(
			"GitHub answered the read of pull request #%d of %s with %d; nothing was recorded, so send the call again", number, repository, answer.Status))
	}
}
