package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/store"
)

// ProjectReleaseRoute is the operator's route of the project release (dispatch://INFRA-3583 R6):
// `POST` starts it, or a dry run of it, and `GET` reads the newest release of the project.
const ProjectReleaseRoute = "/legion/v1/operator/project/release"

// ProjectReleaseRunsNoPod is the sentence a daemon on the tmux runtime refuses a project release
// with: the release moves Kubernetes pods, and that runtime runs none.
const ProjectReleaseRunsNoPod = "the tmux runtime runs no pod; a project release moves Kubernetes pods"

// ErrProjectReleaseRunsNoPod is ProjectRelease.Start's answer on a runtime that runs no pod, which
// the route answers 409 with ProjectReleaseRunsNoPod.
var ErrProjectReleaseRunsNoPod = errors.New(ProjectReleaseRunsNoPod)

// projectReleaseNoPod is the code of that refusal (Failure.Code).
const projectReleaseNoPod = "PROJECT_RELEASE_NO_POD"

// ProjectRelease is the daemon's project release as the routes reach it (internal/daemon's
// projectReleaser): one release per process, its record in the daemon's database, and the answers
// of the roles it asked.
type ProjectRelease interface {
	// Start starts the release with bound, or the daemon's pod_update_grace_minutes when bound
	// is zero, or runs a dry run of it. While a release of this process runs, or after it finished,
	// it starts nothing and answers that release with started false; a dry run answers its report
	// with started false. A runtime that runs no pod answers ErrProjectReleaseRunsNoPod.
	Start(ctx context.Context, bound time.Duration, dryRun bool) (view ProjectReleaseResponse, started bool, err error)
	// Read is the newest release of the project as its record holds it, and whether one ran.
	Read(ctx context.Context) (ProjectReleaseResponse, bool, error)
	// AskRound is the round the running release's asks name ("release:<id>"), "" while none runs.
	AskRound() string
	// Answer records the answer of token's claim to the running release's ask: the claim's
	// session and generation must be the ones the claim holds now (claim.SameAgentRefusal,
	// claim.StaleGeneration), and the claim must be one the release asked. It records the
	// answer's time on the release and asks the machine nothing. An answer that comes once the
	// bound of the claim's pod has passed is accepted and not recorded (ReleasedAsk).
	Answer(ctx context.Context, token claim.Token, session string, generation uint64, head string) error
}

// ProjectReleaseRequest is the body of `POST /legion/v1/operator/project/release`: the bound, a
// Go duration ("" for the daemon's pod_update_grace_minutes), and whether this is a dry run,
// which reads and stops nothing.
type ProjectReleaseRequest struct {
	Bound  string `json:"bound,omitempty"`
	DryRun bool   `json:"dryRun,omitempty"`
}

// ProjectReleaseResponse is one project release as the routes and the state show it: its record's
// id (none on a dry run), when it started and when it deletes every pod still waiting (BoundAt),
// that bound as a Go duration, when it finished its acts and when a boot ended it, each absent
// until it happens, who ended it ("boot", store.ProjectReleaseEndedByBoot), and its result as the
// release last recorded it. A dry run is finished when it is answered and is never recorded.
type ProjectReleaseResponse struct {
	ID         int64                `json:"id,omitempty"`
	DryRun     bool                 `json:"dryRun,omitempty"`
	StartedAt  time.Time            `json:"startedAt"`
	BoundAt    time.Time            `json:"boundAt"`
	Bound      string               `json:"bound"`
	FinishedAt *time.Time           `json:"finishedAt,omitempty"`
	EndedAt    *time.Time           `json:"endedAt,omitempty"`
	EndedBy    string               `json:"endedBy,omitempty"`
	Result     ProjectReleaseResult `json:"result"`
}

// ProjectReleaseResult is what the release did, or on a dry run what it would do now: one line per
// Sandbox of the project, in name order, followed in name order by any Sandbox the release found
// only when it listed the project again from the API server, and one per claim of its stop set, in
// token order. Both are never null. Failure is what stopped the release short of its acts, absent
// while nothing did.
type ProjectReleaseResult struct {
	Sandboxes []ReleasedSandbox `json:"sandboxes"`
	Claims    []ReleasedClaim   `json:"claims"`
	Failure   string            `json:"failure,omitempty"`
}

// The outcomes of a Sandbox's line (ReleasedSandbox.Outcome). An issue pod ends as one of the
// four deleted_* outcomes or as left, and every other Sandbox as deleted or left; a line not yet
// ended is waiting.
const (
	// ReleaseWaiting: the release has not ended this Sandbox yet.
	ReleaseWaiting = "waiting"
	// ReleaseDeletedClean: deleted, its last read finding nothing unpushed.
	ReleaseDeletedClean = "deleted_clean"
	// ReleaseDeletedUnread: deleted with no read, since no role process ran in it at any read.
	ReleaseDeletedUnread = "deleted_unread"
	// ReleaseDeletedLost: deleted with the work its last read found unpushed.
	ReleaseDeletedLost = "deleted_lost"
	// ReleaseDeletedReadRefused: deleted with its workspace unread at the end: its shim refused
	// the last read, or no role process ran at the last read while an earlier read reached one.
	// The line then keeps that earlier read's count or refusal, and its reason says so.
	ReleaseDeletedReadRefused = "deleted_read_refused"
	// ReleaseDeleted: a controller's, a cache's or a probe's Sandbox deleted.
	ReleaseDeleted = "deleted"
	// ReleaseLeft: the Sandbox stays, with its disk, for the reason the line gives.
	ReleaseLeft = "left"
)

// releaseDeletedOutcomes is every outcome of a Sandbox the release deleted (ReleasedSandbox.Deleted).
var releaseDeletedOutcomes = []string{ReleaseDeletedClean, ReleaseDeletedUnread, ReleaseDeletedLost, ReleaseDeletedReadRefused, ReleaseDeleted}

// ReleasedSandbox is one Sandbox's line of a release: its name, its kind (issue, controller, cache,
// probe or unknown), its issue for an issue pod, and its outcome. A line of work lost carries the
// count of unpushed commits and whether the working copy held a change at the last read; one of a
// workspace unread, the shim's refusal. A deleted_read_refused line whose last read found no role
// process keeps the count, the flag or the refusal of the latest read that reached one instead. A
// left line gives its reason, and an issue pod's line can give more reasons whatever its outcome:
// that its ask could not be written, so no role of it was asked, and that no role process ran at
// its last read; several reasons are joined by "; ". OtherImage marks an issue pod whose pod runs
// an image other than the daemon's own. Asked is each role the release asked, or on a dry run
// would ask, with when it answered or whether the bound passed first.
type ReleasedSandbox struct {
	Name               string                     `json:"name"`
	Kind               runtime.ProjectSandboxKind `json:"kind"`
	Issue              string                     `json:"issue,omitempty"`
	Outcome            string                     `json:"outcome"`
	Reason             string                     `json:"reason,omitempty"`
	Commits            *int                       `json:"commits,omitempty"`
	WorkingCopyChanged *bool                      `json:"workingCopyChanged,omitempty"`
	Refusal            string                     `json:"refusal,omitempty"`
	OtherImage         bool                       `json:"otherImage,omitempty"`
	Asked              []ReleasedAsk              `json:"asked,omitempty"`
}

// Deleted is whether the release deleted the line's Sandbox: an issue pod by any of its four
// deleted outcomes, any other Sandbox as ReleaseDeleted. A line left, or still waiting, is not.
func (s ReleasedSandbox) Deleted() bool { return slices.Contains(releaseDeletedOutcomes, s.Outcome) }

// ReleasedAsk is one asked role of a pod: when it answered, or Bound once the bound passed with no
// answer; neither while it may still answer, and neither on a dry run. An answer that comes once
// Bound is set is not recorded, so the two are never both set.
type ReleasedAsk struct {
	Role       claim.Role `json:"role"`
	AnsweredAt *time.Time `json:"answeredAt,omitempty"`
	Bound      bool       `json:"bound,omitempty"`
}

// ReleasedClaimUnrecorded is how a claim of the stop set ended when the store holds no record of
// it at the release's end (ReleasedClaim.Ended): the next boot starts no claim it holds no record of.
const ReleasedClaimUnrecorded = "unrecorded"

// ReleasedClaim is one claim of the release's stop set: its token, issue, role and session, and how
// it ended as its own record holds it at the end — `project_release` for a claim the daemon's next
// boot starts (BootStarts), or the source or state of a claim it does not, such as `operator`,
// `daemon`, `exit`, `failed` or `retired`, or ReleasedClaimUnrecorded for a claim the store holds
// no record of; absent while the release has not ended it, and when the release could not read
// the claims at its end. Detail is why a claim the release stopped, or held for an operator, was
// not stored suspended in time, or that its record could not be read at the end.
type ReleasedClaim struct {
	Token      claim.Token `json:"token"`
	Issue      string      `json:"issue,omitempty"`
	Role       claim.Role  `json:"role"`
	Session    string      `json:"session,omitempty"`
	Ended      string      `json:"ended,omitempty"`
	BootStarts bool        `json:"bootStarts"`
	Detail     string      `json:"detail,omitempty"`
}

// ProjectReleaseResponseOf is a release's record as the routes and the state show it. The result
// is the release's own JSON, which the store keeps unread; a record whose result names nothing yet
// (`{}`) shows empty lists.
func ProjectReleaseResponseOf(release store.ProjectRelease) (ProjectReleaseResponse, error) {
	view := ProjectReleaseResponse{
		ID: release.ID, StartedAt: release.StartedAt.UTC(), BoundAt: release.BoundAt.UTC(),
		Bound: release.BoundAt.Sub(release.StartedAt).String(), EndedBy: release.EndedBy,
	}
	if !release.FinishedAt.IsZero() {
		at := release.FinishedAt.UTC()
		view.FinishedAt = &at
	}
	if !release.EndedAt.IsZero() {
		at := release.EndedAt.UTC()
		view.EndedAt = &at
	}
	if len(release.Result) > 0 {
		if err := json.Unmarshal(release.Result, &view.Result); err != nil {
			return ProjectReleaseResponse{}, fmt.Errorf("read the result of project release %d: %w", release.ID, err)
		}
	}
	view.Result = view.Result.Listed()
	return view, nil
}

// Listed is result with each list present, so a result that names nothing yet reads as two empty
// lists rather than two absent ones.
func (r ProjectReleaseResult) Listed() ProjectReleaseResult {
	if r.Sandboxes == nil {
		r.Sandboxes = []ReleasedSandbox{}
	}
	if r.Claims == nil {
		r.Claims = []ReleasedClaim{}
	}
	return r
}

// startProjectRelease is `POST /legion/v1/operator/project/release`: 202 with the release's record
// once it is recorded, 200 with the report of a dry run, and 200 with this process's release, which
// starts nothing, once one ran here. A bound that is no positive Go duration is refused 400 before
// anything starts.
func (s *server) startProjectRelease(w http.ResponseWriter, r *http.Request) {
	if s.projectRelease == nil {
		writeJSON(w, http.StatusInternalServerError, errorBody("the project release is not served by this daemon"))
		return
	}
	var req ProjectReleaseRequest
	if !s.readOptionalBody(w, r, &req) {
		return
	}
	var bound time.Duration
	if req.Bound != "" {
		parsed, err := time.ParseDuration(req.Bound)
		if err != nil || parsed <= 0 {
			writeJSON(w, http.StatusBadRequest, errorBody(fmt.Sprintf("bound %q is no positive Go duration, such as 30m", req.Bound)))
			return
		}
		bound = parsed
	}
	view, started, err := s.projectRelease.Start(r.Context(), bound, req.DryRun)
	switch {
	case errors.Is(err, ErrProjectReleaseRunsNoPod):
		writeFailure(w, http.StatusConflict, projectReleaseNoPod, ProjectReleaseRunsNoPod)
		return
	case err != nil:
		s.logRequestFailure(r, err, "api: the project release could not start")
		writeJSON(w, http.StatusInternalServerError, errorBody("start the project release: "+err.Error()))
		return
	}
	status := http.StatusOK
	if started {
		status = http.StatusAccepted
	}
	writeJSON(w, status, view)
}

// readProjectRelease is `GET /legion/v1/operator/project/release`: 200 with the newest release of
// the project, its progress while it runs and its result once it finished, and 404 when none ran.
func (s *server) readProjectRelease(w http.ResponseWriter, r *http.Request) {
	if s.projectRelease == nil {
		writeJSON(w, http.StatusInternalServerError, errorBody("the project release is not served by this daemon"))
		return
	}
	view, found, err := s.projectRelease.Read(r.Context())
	if err != nil {
		s.logRequestFailure(r, err, "api: the project release could not be read")
		writeJSON(w, http.StatusInternalServerError, errorBody("read the project release: "+err.Error()))
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorBody("no project release of this project was recorded"))
		return
	}
	writeJSON(w, http.StatusOK, view)
}
