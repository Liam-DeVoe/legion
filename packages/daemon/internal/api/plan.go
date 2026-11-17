package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/credential"
	"github.com/sjawhar/legion/daemon/internal/dispatch"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/plan"
)

// PlanRegisterRequest is register_plan: the owner of an issue, an architect of the issue's tree,
// gives Legion the plan in one version of a document of the issue.
type PlanRegisterRequest struct {
	GrantID    string `json:"grantId"`
	Issue      string `json:"issue"`
	ArtifactID string `json:"artifactId"`
	Version    int    `json:"version"`
}

// PlanRegisterResponse is the plan register_plan took: the version given and each of its tasks.
type PlanRegisterResponse struct {
	Plan RegisteredPlan `json:"plan"`
}

// RegisteredPlan is one version of a plan as register_plan read it.
type RegisteredPlan struct {
	Version int              `json:"version"`
	Tasks   []RegisteredTask `json:"tasks"`
}

// RegisteredTask is one task of a plan register_plan took: its id and the steps its block names,
// each as the plan writes it (`role` or `role:key`). A task whose block names none has no step
// here, never null: it runs the project's default steps, which the workflow reads when the task's
// work starts, so the answer names none rather than guessing what the project will run then.
type RegisteredTask struct {
	ID    string   `json:"id"`
	Steps []string `json:"steps"`
}

// PlanRefusal is the answer to a plan Legion does not take, 409 PLAN_REFUSED: the code and the
// sentence every refusal carries, and one reason for each cause (intake.PlanRefused).
type PlanRefusal struct {
	Code    string       `json:"code"`
	Error   string       `json:"error"`
	Reasons []PlanReason `json:"reasons"`
}

// PlanReason is one cause of a refused plan: the task it names, "" for a cause of the whole plan.
type PlanReason struct {
	Task   string `json:"task"`
	Reason string `json:"reason"`
}

// registerPlanRoute is register_plan as a refusal names it, the operation the caller retries.
const registerPlanRoute = "register_plan"

// OwnerPlanContract is the daemon contract that added the owner's plan (version.go's 156): the
// routes register_plan and stop_task, a failed step's returnTo on handoff_complete, and the hold
// reasons task-stopped and children in the state's holdReason, which a caller below it is served
// without (stateNarrowings).
const OwnerPlanContract = 156

// writeRefusal writes a committed refusal: a refused plan with its reasons beside the code
// (PlanRefusal), and every other refusal as its code and its sentence (Failure).
func writeRefusal(w http.ResponseWriter, refusal *intake.Refusal) {
	if refusal.Reasons == nil {
		writeFailure(w, refusal.Status, refusal.Code, refusal.Message)
		return
	}
	if noter, ok := w.(refusalNoter); ok {
		noter.noteRefusal(refusal.Code)
	}
	reasons := make([]PlanReason, len(refusal.Reasons))
	for i, reason := range refusal.Reasons {
		reasons[i] = PlanReason{Task: reason.Task, Reason: reason.Reason}
	}
	writeJSON(w, refusal.Status, PlanRefusal{Code: refusal.Code, Error: refusal.Message, Reasons: reasons})
}

// registerPlan is register_plan (R5, R14). It authorizes the owner (planOwner), reads the
// document's approval for the version Dispatch has and the issue carrying it (documentOfIssue),
// reads the plan's blocks at the version given (planAt), and refuses with one reason for each
// cause of a plan this release does not carry out (plan.Supported) or no release can
// (plan.Validate); each of those answers records nothing. A plan both take is recorded as
// intake.PlanGiven, whose rules of a later version (R16) are the workflow's.
//
// The fact's id is the issue, the generation the plan is given at, the document and the version:
// a retry of a registration taken is that fact, answered the same, and the same version given at
// a later generation of the issue is a new one.
func (s *server) registerPlan(w http.ResponseWriter, r *http.Request) {
	var req PlanRegisterRequest
	if !s.readBody(w, r, &req, s.redeemIdentityCheck) || !requireFailureFields(w,
		field{"grantId", req.GrantID},
		field{"issue", req.Issue},
		field{"artifactId", req.ArtifactID},
	) {
		return
	}
	if !claim.IsIssueKey(req.Issue) {
		writeFailure(w, http.StatusBadRequest, "INVALID_ISSUE", "issue is not an issue key")
		return
	}
	if !validUUID(req.ArtifactID) {
		writeFailure(w, http.StatusBadRequest, "INVALID_ARTIFACT_ID", "artifactId must be a UUID")
		return
	}
	if req.Version < 1 {
		writeFailure(w, http.StatusBadRequest, "INVALID_VERSION", "version must be positive")
		return
	}
	artifactID := strings.ToLower(req.ArtifactID)
	grant, generation, ok := s.planOwner(w, r, req.GrantID, req.Issue)
	if !ok {
		return
	}
	approval, ok := s.documentOfIssue(w, r, artifactID, req.Issue, registerPlanRoute)
	if !ok {
		return
	}
	// The version is the caller's; only Dispatch's read says the document has it.
	if req.Version > approval.LatestVersion {
		writeFailure(w, http.StatusConflict, "VERSION_NOT_IN_DISPATCH", fmt.Sprintf(
			"Dispatch shows %s at version %d, not %d; give register_plan a version that Dispatch has", artifactID, approval.LatestVersion, req.Version))
		return
	}
	given, ok := s.planAt(w, r, artifactID, req.Version, req.Issue)
	if !ok {
		return
	}
	// Both checks run, so a plan with two causes gets two reasons: what this release does not
	// carry out yet, then what no release can.
	refusals := append(plan.Supported(given), plan.Validate(given, plan.Deployment{Roles: plan.BuiltinRoles()})...)
	if len(refusals) > 0 {
		writeRefusal(w, intake.PlanRefused(refusals))
		return
	}
	s.applyFact(w, r, fmt.Sprintf("plan:%s:%d:%s:%d", req.Issue, generation, artifactID, req.Version),
		intake.PlanGiven{Issue: req.Issue, Generation: generation, Plan: given, GivenBy: grant.Claim},
		PlanRegisterResponse{Plan: registeredPlan(given)})
}

// planOwner authorizes register_plan for an architect of the issue's tree, and answers the
// generation the plan is given at. A recorded issue is its tree's architects', the root's or a
// sub-architect's (record.ArchitectOfTree, the rule every architect operation holds it to,
// architectForIssue), and its plan is given at its generation. One the daemon has not recorded — a
// child before release_children records it (C2-2) — is the tree's when Dispatch's parent chain
// reaches the tree (dispatchTreeMember) and it has not entered the workflow, triage, backlog,
// icebox or todo, and its plan is given at generation 0, where it waits for the issue's first step.
func (s *server) planOwner(w http.ResponseWriter, r *http.Request, grantID, issue string) (credential.Grant, uint64, bool) {
	grant, ok := s.architectGrant(w, r, grantID)
	if !ok {
		return credential.Grant{}, 0, false
	}
	recorded, owned, err := s.ownedIssue(r.Context(), issue, grant.Claim)
	if err != nil {
		writeFailure(w, http.StatusInternalServerError, "RECORD_UNAVAILABLE", "could not read issue tree membership")
		return credential.Grant{}, 0, false
	}
	if recorded != nil {
		if !owned {
			writeFailure(w, http.StatusForbidden, "ISSUE_OUTSIDE_TREE", "issue is outside the architect tree")
			return credential.Grant{}, 0, false
		}
		return grant, recorded.Generation, true
	}
	if s.dispatch == nil {
		writeFailure(w, http.StatusInternalServerError, "DISPATCH_UNAVAILABLE", "Dispatch is unavailable")
		return credential.Grant{}, 0, false
	}
	unrecorded, ok := s.dispatchTreeMember(w, r, grant.Tree, issue, s.cachedIssueReader(registerPlanRoute))
	if !ok {
		return credential.Grant{}, 0, false
	}
	switch unrecorded.Status {
	case "triage", "backlog", "icebox", "todo":
		return grant, 0, true
	}
	writeFailure(w, http.StatusConflict, "ISSUE_ALREADY_RELEASED", fmt.Sprintf(
		"%s is %s in Dispatch and the daemon holds no record of it; register_plan takes a plan for an issue the daemon has not recorded only while it is triage, backlog, icebox or todo",
		issue, unrecorded.Status))
	return credential.Grant{}, 0, false
}

// TaskStopRequest is stop_task: the owner of a recorded issue stops one task of the plan Legion
// runs for it (R16), so that a later version of the plan may change that task.
type TaskStopRequest struct {
	GrantID string `json:"grantId"`
	Issue   string `json:"issue"`
	Task    string `json:"task"`
}

// stopTask is stop_task: an architect of the tree of an issue the daemon has recorded
// (architectForIssue) stops one task of it, answered 200 with no body. The stop is the
// workflow's (intake.TaskStopped), which answers a task the plan does not hold (TaskNotFound) and
// one that is done (TaskDone). Each request is its own fact (requestFactID): a task the owner
// stops, starts again with a later version and stops once more is stopped twice, and a retry of a
// stop already applied finds the task stopped, which changes nothing.
func (s *server) stopTask(w http.ResponseWriter, r *http.Request) {
	var req TaskStopRequest
	if !s.readBody(w, r, &req, s.redeemIdentityCheck) || !requireFailureFields(w,
		field{"grantId", req.GrantID},
		field{"issue", req.Issue},
		field{"task", req.Task},
	) {
		return
	}
	if !claim.IsIssueKey(req.Issue) {
		writeFailure(w, http.StatusBadRequest, "INVALID_ISSUE", "issue is not an issue key")
		return
	}
	grant, _, ok := s.architectForIssue(w, r, req.GrantID, req.Issue)
	if !ok {
		return
	}
	s.applyFact(w, r, requestFactID("tasks/stop"), intake.TaskStopped{Issue: req.Issue, Task: req.Task, By: grant.Claim}, EmptyResponse{})
}

// planAt is the plan in version of the document artifactID, which documentOfIssue already held
// to issue: its blocks as Dispatch gives them for that version (dispatch.Client.PlanDocument),
// read as a plan (plan.FromBlocks). A block the plan's grammar cannot read is a refused plan
// whose one reason names the block; a Dispatch that gives no blocks cannot show Legion a plan at
// all. Every refusal records nothing.
func (s *server) planAt(w http.ResponseWriter, r *http.Request, artifactID string, version int, issue string) (plan.Plan, bool) {
	document, err := s.dispatch.PlanDocument(r.Context(), artifactID, version)
	switch {
	case errors.Is(err, dispatch.ErrNoBlocks):
		writeFailure(w, http.StatusBadGateway, "PLAN_UNREADABLE", fmt.Sprintf(
			"Dispatch gave version %d of %s without its blocks, so Legion cannot read a plan from it: that Dispatch predates the plan's blocks. Nothing was recorded; give the plan again once Dispatch is upgraded",
			version, artifactID))
		return plan.Plan{}, false
	case dispatch.Missing(err), err == nil && document.IssueKey != issue:
		writeFailure(w, http.StatusNotFound, "ARTIFACT_NOT_ON_ISSUE", fmt.Sprintf("%s is not a document of %s", artifactID, issue))
		return plan.Plan{}, false
	case err != nil:
		writeFailure(w, http.StatusBadGateway, "DISPATCH_FAILED", fmt.Sprintf("Dispatch read of %s at version %d failed; retry %s", artifactID, version, registerPlanRoute))
		return plan.Plan{}, false
	}
	blocks := make([]plan.Block, len(document.Blocks))
	for i, block := range document.Blocks {
		blocks[i] = plan.Block{ID: block.ID, Type: block.Type, Attributes: block.Attributes, Text: block.Text}
	}
	given, err := plan.FromBlocks(artifactID, version, blocks)
	if err != nil {
		writeRefusal(w, intake.PlanRefused([]plan.Refusal{{Reason: err.Error() + "."}}))
		return plan.Plan{}, false
	}
	return given, true
}

// registeredPlan is given as register_plan answers it.
func registeredPlan(given plan.Plan) RegisteredPlan {
	tasks := make([]RegisteredTask, len(given.Tasks))
	for i, task := range given.Tasks {
		steps := make([]string, len(task.Steps))
		for j, step := range task.Steps {
			steps[j] = step.String()
		}
		tasks[i] = RegisteredTask{ID: task.ID, Steps: steps}
	}
	return RegisteredPlan{Version: given.Version, Tasks: tasks}
}
