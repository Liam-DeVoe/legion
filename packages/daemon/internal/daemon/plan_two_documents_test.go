// The two documents of an issue's plan, each with one writer, through the whole daemon over the
// real Dispatch of this package's tests (ownerStepsRig, owner_steps_rig_test.go). On the project's
// default steps the planner writes plan.md, the plan review approves it, and the later steps build
// from it. The owner writes its decisions for the task default into a document of its own,
// owner-plan.md, and Legion takes each version of it while the task runs. Under an owner's plan
// whose steps name the planner, the planner's plan stands below the owner's block in plan.md. An
// owner's version that changes only that block's text leaves every other block as it was.
package daemon

import (
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/api"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/phase"
)

// ownerDocument is the document an owner on the project's default steps writes its decisions
// into. The check's control names plan.md here, the document the owner's texts named before.
const ownerDocument = "owner-plan.md"

// defaultText is Legion's text of the task default, where its owner gave none.
const defaultText = "The project's default steps."

// stored is a document version Dispatch stored: the document's id and the version's number.
type stored struct {
	id      string
	version int
}

// upload is a write of markdown as the document name on the walk's issue, as `dispatch artifact`
// writes it. A first upload of a name stores version 1. A later one replaces the document and
// names token, the token of the text it replaces (none for a first upload).
func (r *ownerStepsRig) upload(name, markdown, token string) stored {
	r.t.Helper()
	body := map[string]any{"name": name, "content": markdown}
	if token != "" {
		body["precondition"] = map[string]string{"document": token}
	}
	var answer struct {
		Artifact struct {
			ID string `json:"id"`
		} `json:"artifact"`
		Version struct {
			Number int `json:"number"`
		} `json:"version"`
	}
	r.person(http.MethodPost, "/api/v1/issues/"+r.key+"/artifacts", body, http.StatusCreated, &answer)
	if answer.Artifact.ID == "" || answer.Version.Number == 0 {
		r.t.Fatalf("Dispatch answered the upload of %s on %s with no document or no version: %+v", name, r.key, answer)
	}
	return stored{id: answer.Artifact.ID, version: answer.Version.Number}
}

// liveBlock is one block of a live document as Dispatch lists it: its id, its type, the byte
// range of its rendering in the markdown, its token and the comments anchored in it.
type liveBlock struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	From       int    `json:"from"`
	To         int    `json:"to"`
	Token      string `json:"token"`
	References struct {
		Comments int `json:"comments"`
	} `json:"references"`
}

// liveDocument is a document as it stands now: its markdown, its token, its version and its
// blocks, from one read of Dispatch's text route.
type liveDocument struct {
	Markdown string      `json:"markdown"`
	Token    string      `json:"token"`
	Version  *int        `json:"version"`
	Blocks   []liveBlock `json:"blocks"`
}

// live reads the document artifact as it stands now, with its blocks.
func (r *ownerStepsRig) live(artifact string) liveDocument {
	r.t.Helper()
	var read liveDocument
	r.person(http.MethodGet, "/api/v1/artifacts/"+artifact+"/text?blocks=true", nil, http.StatusOK, &read)
	if read.Version == nil || read.Token == "" {
		r.t.Fatalf("the read of %s names no version or no token: %+v", artifact, read)
	}
	return read
}

// versionText is the markdown of version of the document artifact.
func (r *ownerStepsRig) versionText(artifact string, version int) string {
	r.t.Helper()
	var read struct {
		Markdown string `json:"markdown"`
	}
	r.person(http.MethodGet, "/api/v1/artifacts/"+artifact+"/versions/"+strconv.Itoa(version), nil, http.StatusOK, &read)
	return read.Markdown
}

// approval is the review state of the document artifact as `dispatch read` prints it on its
// Approval line: the state, the version the review approved, and the document's newest version.
func (r *ownerStepsRig) approval(artifact string) string {
	r.t.Helper()
	var read struct {
		Approval *struct {
			State         string `json:"state"`
			Version       *int   `json:"version"`
			LatestVersion int    `json:"latest_version"`
		} `json:"approval"`
	}
	r.person(http.MethodGet, "/api/v1/artifacts/"+artifact, nil, http.StatusOK, &read)
	if read.Approval == nil || read.Approval.Version == nil {
		return "none"
	}
	return read.Approval.State + " v" + strconv.Itoa(*read.Approval.Version) + " of v" + strconv.Itoa(read.Approval.LatestVersion)
}

// planReview is the plan review approving version of the plan artifact, as `dispatch
// plan-approve` records it.
func (r *ownerStepsRig) planReview(artifact string, version int) {
	r.t.Helper()
	r.person(http.MethodPost, "/api/v1/artifacts/"+artifact+"/plan-reviews", map[string]any{"version": version}, http.StatusCreated, nil)
}

// plannersPlan is what the later steps read of a plan.md: each block with its id, its type, its
// token and its count of anchored comments, the plan_task blocks by id, each comment on the
// document with whether its anchor still holds, the plan review's approval, and how many plan
// tasks of the issue serve each requirement of its spec.
type plannersPlan struct {
	blocks   []string
	tasks    []string
	comments []string
	approval string
	served   map[string]int
}

// readPlan is the walk's issue's plan.md, the document artifact, as the later steps read it.
func (r *ownerStepsRig) readPlan(artifact string) plannersPlan {
	r.t.Helper()
	read := plannersPlan{approval: r.approval(artifact), served: map[string]int{}}
	for _, block := range r.live(artifact).Blocks {
		read.blocks = append(read.blocks, block.ID+" "+block.Type+" "+block.Token+" comments "+strconv.Itoa(block.References.Comments))
		if block.Type == "plan_task" {
			read.tasks = append(read.tasks, block.ID)
		}
	}
	var comments []struct {
		Body   string `json:"body"`
		Anchor *struct {
			ArtifactID string `json:"artifact_id"`
			Quote      string `json:"quote"`
			Orphaned   bool   `json:"orphaned"`
		} `json:"anchor"`
	}
	r.person(http.MethodGet, "/api/v1/issues/"+r.key+"/comments?artifact="+artifact, nil, http.StatusOK, &comments)
	for _, comment := range comments {
		if comment.Anchor != nil && comment.Anchor.ArtifactID == artifact {
			read.comments = append(read.comments, comment.Body+" on "+strconv.Quote(comment.Anchor.Quote)+" orphaned "+strconv.FormatBool(comment.Anchor.Orphaned))
		}
	}
	var coverage struct {
		Requirements []struct {
			BlockID   string `json:"block_id"`
			PlanTasks int    `json:"plan_tasks"`
		} `json:"requirements"`
	}
	r.person(http.MethodGet, "/api/v1/issues/"+r.key+"/coverage", nil, http.StatusOK, &coverage)
	for _, requirement := range coverage.Requirements {
		read.served[requirement.BlockID] = requirement.PlanTasks
	}
	return read
}

// holdsPlan fails the test unless plan.md, the document artifact, reads as want: the planner's
// task blocks t1, t2 and t3 first, and then every other part of the read.
func (r *ownerStepsRig) holdsPlan(after, artifact string, want plannersPlan) {
	r.t.Helper()
	got := r.readPlan(artifact)
	if !slices.Equal(got.tasks, []string{"t1", "t2", "t3"}) {
		r.t.Fatalf("after %s the newest version of plan.md holds the task blocks %q, want the planner's t1, t2 and t3", after, got.tasks)
	}
	if !reflect.DeepEqual(got, want) {
		r.t.Fatalf("after %s plan.md reads\n%+v\nwant it as the planner left it\n%+v", after, got, want)
	}
}

// outsideBlocks is each block of the live document artifact that stands outside every plan_task
// block, its id to its token.
func (r *ownerStepsRig) outsideBlocks(artifact string) map[string]string {
	r.t.Helper()
	blocks := r.live(artifact).Blocks
	outside := map[string]string{}
	for _, block := range blocks {
		inTask := slices.ContainsFunc(blocks, func(task liveBlock) bool {
			return task.Type == "plan_task" && task.From <= block.From && block.To <= task.To
		})
		if !inTask {
			outside[block.ID] = block.Token
		}
	}
	return outside
}

// outsideTasks is markdown with each plan_task block cut out, from its opening line through its
// closing fence: the text a worker compares between the approved version of a plan and the newest.
func outsideTasks(markdown string) string {
	var kept strings.Builder
	inside := false
	for line := range strings.Lines(markdown) {
		switch {
		case !inside && strings.HasPrefix(line, ":::plan_task"):
			inside = true
		case inside && strings.TrimSpace(line) == ":::":
			inside = false
		case !inside:
			kept.WriteString(line)
		}
	}
	return kept.String()
}

// openGateAt is a person approving the walk's issue's spec at version and its architect
// registering that version (register_gate), which opens the issue's gate; the issue's first step
// must then be first.
func (r *ownerStepsRig) openGateAt(version int, first phase.Phase) {
	r.t.Helper()
	spec := r.specs[r.key]
	r.person(http.MethodPost, "/api/v1/artifacts/"+spec+"/reviews", map[string]any{"state": "approved"}, http.StatusCreated, nil)
	status, body := r.d.request(http.MethodPost, "/legion/v1/gates/register", map[string]any{
		"grantId": r.worker(claim.RoleArchitect).grant(), "issue": r.key, "artifactId": spec, "version": version}, false)
	if status != http.StatusOK {
		r.t.Fatalf("register_gate of %s's spec version %d = %d %s, want 200", r.key, version, status, body)
	}
	r.waitPhase(first)
}

// threeRequirements is a spec part marking three requirements, r1, r2 and r3.
const threeRequirements = "\n:::requirement{#r1}\nThe reader reads the input.\n:::\n\n" +
	":::requirement{#r2}\nThe writer writes the output.\n:::\n\n" +
	":::requirement{#r3}\nThe reader and the writer run as one.\n:::\n"

// plannerTasks is a planner's plan.md on issue key of the default steps: three task blocks, each
// serving one requirement of the spec, the second needing the first and the third the second.
func plannerTasks(key string) string {
	spec := "dispatch://" + key + "/spec#b-"
	plan := "dispatch://" + key + "/artifact/plan-md#b-"
	return "# Plan\n\n" +
		":::plan_task{#t1 serves=\"" + spec + "r1\"}\nBuild the reader.\n:::\n\n" +
		":::plan_task{#t2 needs=\"" + plan + "t1\" serves=\"" + spec + "r2\"}\nBuild the writer.\n:::\n\n" +
		":::plan_task{#t3 needs=\"" + plan + "t2\" serves=\"" + spec + "r3\"}\nJoin the two.\n:::\n"
}

// Issue C runs the project's default steps. Its planner writes plan.md with three task blocks that
// have needs and serves, a comment is anchored on one, and the plan review approves that version.
// While the implementer works, the owner makes owner-plan.md with one block default and no steps,
// and gives it. register_plan answers 200 and names step 2, the implementer. The task default
// keeps its state, its place and its counts, and plan.md reads as the planner left it: the same
// blocks with the same ids and tokens, the comment anchored, the approval, and one plan task for
// each requirement. The tester's start task holds the owner's text. While the tester works, the
// owner gives version 2 of owner-plan.md, which Legion takes the same way. The planner then writes
// a new version of plan.md, which changes nothing in owner-plan.md, and the reviewer's start task
// holds the owner's newest text.
//
// Issue D runs an owner's plan whose steps name the planner. The planner writes its plan below the
// owner's block in plan.md, and the plan review approves that version. While the implementer
// works, the owner gives a version that changes only its block's text. register_plan answers 200.
// Each block outside the task block keeps the id and the token it had in the approved version, and
// the approval still names that version. The text outside the task block is the same in the two
// versions, which is what a worker compares. A planner's edit of a section then changes a token
// and that text, so both reads can tell the two writers apart.
func TestTheOwnersDocumentLeavesThePlannersPlanAsItIs(t *testing.T) {
	r := newOwnerStepsRig(t, newLiveProofOn(t, workflowNATS(t)), startScratchDispatchOn(t, scratchDispatchOptions{}), 2, config.Project{})
	const cOne, cTwo, dOne, dTwo = "Owner text one", "Owner text two", "Owner text three", "Owner text four"

	r.editPlan(r.specs[r.key], map[string]any{"op": "insert", "after": "end", "markdown": threeRequirements}, 2)
	r.openGateAt(2, phase.Planning)
	planner := r.worker(claim.RolePlanner)
	holdsText(t, "planner", takeText(planner), defaultText, cOne)
	plan := r.upload("plan.md", plannerTasks(r.key), "").id
	r.person(http.MethodPost, "/api/v1/issues/"+r.key+"/comments", map[string]any{
		"body": "The writer needs the reader's format.", "anchor": map[string]any{"artifact": plan, "quote": "Build the writer."}}, http.StatusCreated, nil)
	r.planReview(plan, *r.live(plan).Version)
	approved := r.readPlan(plan)
	if len(approved.comments) != 1 || !strings.HasSuffix(approved.comments[0], "orphaned false") ||
		!reflect.DeepEqual(approved.served, map[string]int{"r1": 1, "r2": 1, "r3": 1}) || !strings.HasPrefix(approved.approval, "approved ") {
		t.Fatalf("the planner's approved plan.md reads %+v, want one anchored comment, one plan task for each requirement and the review's approval", approved)
	}
	r.finish(planner, "plan", api.HandoffCompleteRequest{Summary: "planned"}, phase.Implementing)
	implementer := r.worker(claim.RoleImplementer)
	holdsText(t, "implementer", takeText(implementer), defaultText, cOne)

	before := r.taskRow("default")
	owner := r.upload(ownerDocument, textPlan("default", "", cOne), "")
	if got := r.registeredRunning(owner.id, owner.version)["default"]; got == nil || *got != (runningAct{Kind: "step", Place: 2, Role: string(claim.RoleImplementer)}) {
		t.Fatalf("register_plan of %s's version %d names default running at %+v, want step 2, the implementer", ownerDocument, owner.version, got)
	}
	if after := r.taskRow("default"); after != before {
		t.Fatalf("task default after the owner's first version is %s, want it as it was: %s", after, before)
	}
	r.holdsPlan("the owner's first version", plan, approved)
	if !r.github.opened(r.number) {
		r.github.open("legion/"+r.key, r.number)
	}
	r.finish(implementer, "implement", api.HandoffCompleteRequest{Summary: "implemented", Commit: stepRecordsHead}, phase.Testing)
	tester := r.worker(claim.RoleTester)
	holdsText(t, "tester", takeText(tester), cOne, defaultText)

	before = r.taskRow("default")
	second := r.upload(ownerDocument, textPlan("default", "", cTwo), r.live(owner.id).Token)
	if second.id != owner.id || second.version != owner.version+1 {
		t.Fatalf("the owner's second write stored %+v, want version %d of %s", second, owner.version+1, owner.id)
	}
	if got := r.registeredRunning(second.id, second.version)["default"]; got == nil || *got != (runningAct{Kind: "step", Place: 3, Role: string(claim.RoleTester)}) {
		t.Fatalf("register_plan of %s's version %d names default running at %+v, want step 3, the tester", ownerDocument, second.version, got)
	}
	if after := r.taskRow("default"); after != before {
		t.Fatalf("task default after the owner's second version is %s, want it as it was: %s", after, before)
	}
	r.holdsPlan("the owner's second version", plan, approved)

	owned := r.live(second.id)
	r.editPlan(plan, map[string]any{"op": "replace", "find": "Join the two.", "with": "Join the two, and log each write."}, *r.live(plan).Version+1)
	if got := r.live(second.id); got.Markdown != owned.Markdown || got.Token != owned.Token || *got.Version != *owned.Version {
		t.Fatalf("after the planner's new version of plan.md, %s reads %+v, want it as it was: %+v", ownerDocument, got, owned)
	}
	r.finish(tester, "test", api.HandoffCompleteRequest{Summary: "the criteria read pass", Verdict: "pass"}, phase.Reviewing)
	holdsText(t, "reviewer", takeText(r.worker(claim.RoleReviewer)), cTwo, cOne)

	r.on(r.issues[1].key)
	plan = r.document("plan.md", textPlan("a", "planner implementer", dOne))
	if status, body := r.registerPlan(plan, 1); status != http.StatusOK {
		t.Fatalf("register_plan of %s's version 1 = %d %s, want 200", r.key, status, body)
	}
	r.openGate(phase.Planning)
	planner = r.worker(claim.RolePlanner)
	takeText(planner)
	r.editPlan(plan, map[string]any{"op": "insert", "after": "end",
		"markdown": "## Approach\n\nRead the input first.\n\n## Order\n\nThe reader, then the writer.\n"}, 2)
	r.planReview(plan, 2)
	approvedBlocks := r.outsideBlocks(plan)
	if len(approvedBlocks) < 4 {
		t.Fatalf("the approved plan.md holds %d blocks outside its task block, %v, want the planner's two headings and two paragraphs", len(approvedBlocks), approvedBlocks)
	}
	r.finish(planner, "plan", api.HandoffCompleteRequest{Summary: "planned"}, phase.Implementing)
	takeText(r.worker(claim.RoleImplementer))

	r.editPlan(plan, map[string]any{"op": "replace", "find": dOne, "with": dTwo}, 3)
	if got := r.registeredRunning(plan, 3)["a"]; got == nil || *got != (runningAct{Kind: "step", Place: 2, Role: string(claim.RoleImplementer)}) {
		t.Fatalf("register_plan of %s's text-only version 3 names task a running at %+v, want step 2, the implementer", r.key, got)
	}
	if got := r.outsideBlocks(plan); !reflect.DeepEqual(got, approvedBlocks) {
		t.Fatalf("after the owner's text-only version the blocks outside the task block are %v, want the approved version's %v", got, approvedBlocks)
	}
	if got := r.approval(plan); got != "stale v2 of v3" {
		t.Fatalf("after the owner's text-only version the approval reads %q, want it to name the approved version 2", got)
	}
	approvedText, ownersText := r.versionText(plan, 2), r.versionText(plan, 3)
	if !strings.Contains(ownersText, dTwo) || outsideTasks(ownersText) != outsideTasks(approvedText) {
		t.Fatalf("version 3 = %q, want it to hold %q and to equal version 2 outside the task block: %q", ownersText, dTwo, approvedText)
	}

	r.editPlan(plan, map[string]any{"op": "replace", "find": "Read the input first.", "with": "Read the input twice."}, 4)
	if got := r.outsideBlocks(plan); reflect.DeepEqual(got, approvedBlocks) {
		t.Fatalf("after the planner's edit of a section the blocks outside the task block are %v, the approved version's: the tokens cannot tell the writers apart", got)
	}
	if outsideTasks(r.versionText(plan, 4)) == outsideTasks(approvedText) {
		t.Fatalf("after the planner's edit of a section version 4 equals version 2 outside the task block: the text cannot tell the writers apart")
	}
}
