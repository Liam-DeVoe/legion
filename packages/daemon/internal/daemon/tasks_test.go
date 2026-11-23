// The whole-daemon tests of a plan of several tasks (tasksRig): the booted daemon over the real
// Dispatch of this package's tests, the fake runtime, and a fake GitHub whose refs, commits and
// pull requests are a real bare git repository's (tasksGitHub), so each branch the daemon makes
// for a task, each join of a task's needs and each pull request an implementer opens is one git
// can read. The owner of one issue gives it a plan of several tasks and opens its gate; the test
// plays each agent of each step, task by task, and reads where each task stands from the store.
// Beside them, the issues of one task that the release before several tasks left mid-walk, booted
// on that release's store (acrossIssue).
package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/api"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/githubtest"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/store/migrations"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/testpg/testdb"
	"github.com/sjawhar/legion/daemon/internal/testwait"
	"github.com/sjawhar/legion/daemon/internal/workflow"
)

// examplePlan is the example plan of several tasks in the form this release takes, each need the
// reference to its task's block of plan.md that %[1]s begins (givePlanText): a and b need nothing
// and run at one time, c needs b, d needs a and c, and e needs d, whose work merges. This release
// runs one agent of each role for an issue, so the steps of a role share one key in every task,
// and the two tasks that run at one time start at steps of two roles: a at its implementer, b at
// its planner.
const examplePlan = `:::plan_task{#a steps="implementer:i reviewer:r"}
Change the parser of the widget.
:::

:::plan_task{#b steps="planner:p implementer:i reviewer:r"}
Design the store of the widget, then change it.
:::

:::plan_task{#c needs="%[1]sb" steps="implementer:i reviewer:r"}
Move the readers of the store.
:::

:::plan_task{#d needs="%[1]sa %[1]sc" steps="implementer:i reviewer:r"}
Join the parser and the store.
:::

:::plan_task{#e needs="%[1]sd" steps="implementer:i tester:t"}
Check the whole change.
:::
`

// The example plan runs its tasks at one time. Once the gate opens, a and b both run, each at its
// first step, before either is done, and c, d and e wait. b's planner ends while a's implementer
// works, so b's implementer step waits for the one implementer, and starts when a's step ends. c
// waits until b is done, d until a and c are, and e until d is; each starts once they are, and the
// issue merges e's pull request. Every launch is in the issue's one pod, and the implementer and
// the reviewer each run every step of theirs in the one session of their first launch.
//
// The fault: a task that starts before the tasks it needs are done works from a base without
// their work. The control is a mutation of the release's code, run by hand: workflow's needsDone
// answering true for every task, so c starts at the gate beside b, and the check that c waits
// fails.
func TestTheExamplePlanRunsItsTasksAtOneTime(t *testing.T) {
	r := newTasksRig(t)
	r.givePlanText(examplePlan)
	r.firstStep()
	r.waitAt("a", 1)
	r.waitAt("b", 1)
	r.stillWaiting("both a and b run", "c", "d", "e")

	r.planStep("b")
	r.waitAgent("b", 2)
	r.implementStep("a", "main")
	r.waitAt("b", 2)
	r.reviewStep("a")
	r.waitDone("a")
	r.stillWaiting("a is done and b is not", "c", "d", "e")

	r.implementStep("b", "main")
	r.stillWaiting("b reviews", "c", "d", "e")
	r.reviewStep("b")
	r.waitDone("b")
	r.waitAt("c", 1)
	r.stillWaiting("c runs", "d", "e")

	r.implementStep("c", r.branchOf("b"))
	r.stillWaiting("c reviews", "d", "e")
	r.reviewStep("c")
	r.waitDone("c")
	r.waitAt("d", 1)
	r.stillWaiting("d runs", "e")

	r.implementStep("d", r.branchOf("d")+claim.JoinBranchSuffix)
	r.stillWaiting("d reviews", "e")
	r.reviewStep("d")
	r.waitDone("d")
	r.waitAt("e", 1)

	r.implementStep("e", "main")
	r.testStep("e")
	r.retroAndMerge("e")
	r.onePod(claim.RoleImplementer, claim.RoleReviewer, claim.RolePlanner, claim.RoleTester)
}

// agentsPlan is two tasks, x and y, that need nothing and whose first steps are of one key, k, and
// the task z that needs both, whose work merges, each need written as examplePlan's.
const agentsPlan = `:::plan_task{#x steps="implementer:k reviewer:r"}
Change the parser of the widget.
:::

:::plan_task{#y steps="implementer:k reviewer:r"}
Change the store of the widget.
:::

:::plan_task{#z needs="%[1]sx %[1]sy" steps="implementer:k reviewer:r"}
Join the parser and the store.
:::
`

// The agents of tasks with one last task: x and y start at one time, each at a step of the key k,
// whose one agent works x's step first (task id order) while y's step waits for it, given no task
// of its own; once x's step ends, y's step starts in the same session, with no launch of a second
// agent.
//
// The fault: a step of a key that starts while its agent works a step of another task gives the
// one agent two tasks at once. The control is a mutation of the release's code, run by hand:
// workflow's waitForAgent answering that no step waits, so y's step starts at the gate beside x's,
// and the check that y's step waits for its agent fails.
func TestTheAgentsOfTasksWithOneLastTask(t *testing.T) {
	r := newTasksRig(t)
	r.givePlanText(agentsPlan)
	r.firstStep()
	r.waitAt("x", 1)
	r.waitAgent("y", 1)
	k := r.worker(claim.RoleImplementer)
	r.implementStep("x", "main")
	r.waitAt("y", 1)
	if again := r.worker(claim.RoleImplementer); again != k {
		t.Fatalf("y's step went to the claimant of session %s, want the one of x's step, %s", again.session, k.session)
	}
	r.implementStep("y", "main")
	if got := r.d.claim(k.token).Session; got != k.session {
		t.Errorf("the claim of k holds the session %q after y's step, want %q, the session of x's step", got, k.session)
	}
	r.onePod(claim.RoleImplementer)
}

// tasksRig is the whole daemon with one issue at admitted whose owner gives it a plan of several
// tasks (ownerStepsRig), over the fake GitHub of a real repository (gh), and the pull request each
// task's implementer opened, by task.
type tasksRig struct {
	*ownerStepsRig
	gh    *tasksGitHub
	pulls map[string]int
}

// newTasksRig is newTasksRigWith for one issue over the package's broker and a Dispatch of this
// checkout, which the package's own tests run.
func newTasksRig(t *testing.T) *tasksRig {
	t.Helper()
	return newTasksRigWith(t, newLiveProofOn(t, workflowNATS(t)), startScratchDispatchOn(t, scratchDispatchOptions{}), "One change split in several tasks")
}

// newTasksRigWith sets up dispatch, a running Dispatch, with the run's project and its repository
// mapping, one issue of each of titles created with a spec and seeded at admitted with no design
// gate and no pull request (seedAdmitted), and boots the daemon of proof at it over the fake GitHub
// of a real repository, with each issue's architect registered. Nothing of an issue starts until
// its gate opens (firstStep). The steps act on the first issue until on moves them. Dispatch
// refuses an issue whose title is a near copy of an open one's, so no two titles share a word
// that counts.
func newTasksRigWith(t *testing.T, proof liveProof, dispatch *scratchDispatch, titles ...string) *tasksRig {
	t.Helper()
	gh := newTasksGitHub(t)
	// A Dispatch project key is at most ten characters, and the daemon's project is its Dispatch
	// project, so the run's project is a short one of its own.
	proof.cfg.Project = "TK" + randomSuffix(t)[:6]
	proof.cfg.Projects = map[string]config.Project{proof.cfg.Project: {}}
	proof.cfg.AdmissionCap = 8
	var err error
	if proof.project, err = claim.ProjectToken(proof.cfg.Project); err != nil {
		t.Fatal(err)
	}
	// The boot reads the GitHub it is given from the walk's stand-in (stepRecordsRig.boot); every
	// read of this rig's GitHub goes to gh.
	r := &tasksRig{ownerStepsRig: &ownerStepsRig{stepRecordsRig: &stepRecordsRig{liveProof: proof, dispatch: dispatch,
		github: &stepRecordsGitHub{t: t, url: gh.url}, claims: map[claim.Token]*stepClaimant{}, waits: make(chan claim.Token, 16)},
		specs: map[string]string{}}, gh: gh, pulls: map[string]int{}}
	if err := os.WriteFile(r.cfg.DispatchTokenFile, []byte(r.dispatch.token+"\n"), 0o600); err != nil {
		t.Fatalf("write the Dispatch token file: %v", err)
	}
	r.cfg.DispatchURL = r.dispatch.base
	r.person(http.MethodPost, "/api/v1/projects", map[string]string{"key": r.cfg.Project, "name": "Several tasks"}, http.StatusCreated, nil)
	r.person(http.MethodPut, "/api/v1/settings/repo-projects/"+liveRepo, map[string]any{"project": r.cfg.Project, "default": true}, http.StatusOK, nil)
	for _, title := range titles {
		var spec struct {
			createdIssue
			PrimaryArtifactID string `json:"primary_artifact_id"`
		}
		r.person(http.MethodPost, "/api/v1/issues", map[string]string{"project": r.cfg.Project, "title": title,
			"spec": "The issue's change, as its owner states it.\n"}, http.StatusCreated, &spec)
		if spec.PrimaryArtifactID == "" {
			t.Fatalf("Dispatch created %s with no primary document", spec.Key)
		}
		created := spec.createdIssue
		r.person(http.MethodPatch, "/api/v1/issues/"+created.Key, map[string]any{"status": "in_progress", "labels": []string{"legion"}}, http.StatusOK, &created)
		r.specs[created.Key] = spec.PrimaryArtifactID
		r.issues = append(r.issues, walkIssue{key: created.Key, title: title})
		r.on(created.Key)
		r.seedAdmitted(created.Rank, created.LastSeq)
	}
	r.boot()
	for _, issue := range r.issues {
		r.on(issue.key)
		r.worker(claim.RoleArchitect)
	}
	r.on(r.issues[0].key)
	return r
}

// givePlanText writes form as plan.md on the issue, each %[1]s in it the reference to a block of
// that document as Dispatch writes one, `dispatch://<KEY>/artifact/plan-md#b-`, which the task's id
// ends, and has its architect give version 1 of it, which the daemon must take.
func (r *tasksRig) givePlanText(form string) {
	r.t.Helper()
	artifact := r.document("plan.md", fmt.Sprintf(form, "dispatch://"+r.key+"/artifact/plan-md#b-"))
	if status, body := r.registerPlan(artifact, 1); status != http.StatusOK {
		r.t.Fatalf("register_plan of %s's plan = %d %s, want 200", r.key, status, body)
	}
}

// tasksRow is where one task of the issue stands, as plan_tasks holds it: its state, the kind and
// place of its act, what its step waits for, and the branch its first start gave it.
type tasksRow struct {
	state, kind string
	position    int
	wait        string
	branch      string
}

// rows is each task of the issue's generation, by id.
func (r *tasksRig) rows() map[string]tasksRow {
	r.t.Helper()
	rows, err := r.st.Pool().Query(context.Background(), `select task, state, running_kind, position, step_wait, branch from plan_tasks
		where issue = $1 and generation = (select generation from issues where key = $1)`, r.key)
	if err != nil {
		r.t.Fatalf("read the tasks of %s: %v", r.key, err)
	}
	defer rows.Close()
	tasks := map[string]tasksRow{}
	for rows.Next() {
		var task string
		var row tasksRow
		if err := rows.Scan(&task, &row.state, &row.kind, &row.position, &row.wait, &row.branch); err != nil {
			r.t.Fatal(err)
		}
		tasks[task] = row
	}
	if err := rows.Err(); err != nil {
		r.t.Fatal(err)
	}
	return tasks
}

// waitTask waits for task to stand as holds says, what in the failure's words.
func (r *tasksRig) waitTask(task, what string, holds func(tasksRow) bool) {
	r.t.Helper()
	var last tasksRow
	if !testwait.Holds(stepRecordsWait, 100*time.Millisecond, func() bool {
		last = r.rows()[task]
		return holds(last)
	}) {
		r.t.Fatalf("task %s of %s stands %+v, want %s", task, r.key, last, what)
	}
}

// waitAt waits for task to run its step at place, started: no wait for its join or its agent.
func (r *tasksRig) waitAt(task string, place int) {
	r.t.Helper()
	r.waitTask(task, fmt.Sprintf("running its step %d, started", place), func(row tasksRow) bool {
		return row.state == string(record.TaskRunning) && row.kind == "step" && row.position == place && row.wait == string(record.StepWaitNone)
	})
}

// waitAgent waits for task's step at place to wait for its agent, which works a step of another task.
func (r *tasksRig) waitAgent(task string, place int) {
	r.t.Helper()
	r.waitTask(task, fmt.Sprintf("running at its step %d, which waits for its agent", place), func(row tasksRow) bool {
		return row.state == string(record.TaskRunning) && row.kind == "step" && row.position == place && row.wait == string(record.StepWaitAgent)
	})
}

// waitDone waits for task to be done.
func (r *tasksRig) waitDone(task string) {
	r.t.Helper()
	r.waitTask(task, "done", func(row tasksRow) bool { return row.state == string(record.TaskDone) })
}

// stillWaiting fails the test unless each of tasks still waits for the tasks it needs, when the
// walk stands where when says.
func (r *tasksRig) stillWaiting(when string, tasks ...string) {
	r.t.Helper()
	rows := r.rows()
	for _, task := range tasks {
		if row := rows[task]; row.state != string(record.TaskWaiting) {
			r.t.Fatalf("task %s of %s stands %+v when %s, want waiting for the tasks it needs", task, r.key, row, when)
		}
	}
}

// branchOf is the branch of task, which its first start gave it (record.PlanTask.BranchOf).
func (r *tasksRig) branchOf(task string) string {
	r.t.Helper()
	row, ok := r.rows()[task]
	if !ok {
		r.t.Fatalf("the plan of %s holds no task %s", r.key, task)
	}
	if row.branch == "" {
		return claim.IssueBranch(r.key)
	}
	return row.branch
}

// takeFor is role's claimant taking its next task, which must be of task: it names task's working
// copy, its branch.
func (r *tasksRig) takeFor(role claim.Role, task string) *stepClaimant {
	r.t.Helper()
	c := r.worker(role)
	prompt := c.take()
	if line := " Working copy: branch " + r.branchOf(task) + ","; !strings.Contains(prompt.Message, line) {
		r.t.Fatalf("the %s's task = %q, want task %s's, naming its working copy %q", role, prompt.Message, task, line)
	}
	return c
}

// done ends c's step: its handoff record of word (none for word ""), its completion req, which the
// daemon must take, and the end of its turn.
func (r *tasksRig) done(c *stepClaimant, word string, req api.HandoffCompleteRequest) {
	r.t.Helper()
	if word != "" {
		req.HandoffID, req.HandoffVersion = r.writeHandoff(c.session, word)
	}
	if status, body := r.complete(c, req); status != http.StatusOK {
		r.t.Fatalf("the %s's completion = %d %s, want 200", c.role, status, body)
	}
	c.endTurn()
}

// planStep is the planner's step of task, at its place 1.
func (r *tasksRig) planStep(task string) {
	r.t.Helper()
	r.done(r.takeFor(claim.RolePlanner, task), "plan", api.HandoffCompleteRequest{Summary: "planned " + task})
}

// implementStep is the implementer's step of task: it commits the task's work on the task's
// branch, opens the task's pull request against base and completes at that commit; the task then
// stands at its next step.
func (r *tasksRig) implementStep(task, base string) {
	r.t.Helper()
	c := r.takeFor(claim.RoleImplementer, task)
	branch := r.branchOf(task)
	at := r.rows()[task].position
	head := r.gh.repo.Commit(r.t, branch, map[string]string{task + ".txt": "the work of task " + task + "\n"}, "the work of task "+task)
	r.pulls[task] = r.gh.open(branch, base)
	r.done(c, "implement", api.HandoffCompleteRequest{Summary: "implemented " + task, Commit: head})
	r.waitTask(task, fmt.Sprintf("past its step %d", at), func(row tasksRow) bool { return row.position != at || row.state != string(record.TaskRunning) })
}

// reviewStep is the reviewer's step of task: it takes its task, and a person approves the task's
// pull request at its head.
func (r *tasksRig) reviewStep(task string) {
	r.t.Helper()
	c := r.takeFor(claim.RoleReviewer, task)
	c.endTurn()
	number := r.pulls[task]
	head := r.gh.repo.Head(r.t, r.branchOf(task))
	r.reviews++
	payload, err := json.Marshal(map[string]string{
		"action": "submitted", "kind": "review", "repo": liveRepo, "number": strconv.Itoa(number), "state": "APPROVED",
		"commit_id": head, "head_sha": head, "author": stepRecordsPerson, "body": "task " + task,
		"review_id": strconv.Itoa(number*10 + r.reviews), "submitted_at": time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		r.t.Fatal(err)
	}
	r.publish(fmt.Sprintf("notifications.github.%s.pr.%d.review", r.subjectRepo(), number), "github",
		fmt.Sprintf("review-%d-%d", number, r.reviews), string(payload))
}

// testStep is the tester's step of task, which passes.
func (r *tasksRig) testStep(task string) {
	r.t.Helper()
	r.done(r.takeFor(claim.RoleTester, task), "test", testCompletion)
}

// retroAndMerge is the implementer's retro of task, the task whose work merges, at its branch's
// head, then GitHub's merge of its pull request once the merger is launched; the issue is then
// done.
func (r *tasksRig) retroAndMerge(task string) {
	r.t.Helper()
	c := r.takeFor(claim.RoleImplementer, task)
	branch := r.branchOf(task)
	head := r.gh.repo.Head(r.t, branch)
	req := r.retroCompletion(&record.PlanRating{Rating: 4, Reason: "The plan fit the work."})
	req.Commit = head
	r.done(c, "", req)
	r.waitPhase(phase.Merging)
	r.awaitLaunch(r.token(claim.RoleMerger))
	number := r.pulls[task]
	r.gh.merged(number)
	payload, err := json.Marshal(map[string]string{
		"action": "closed", "kind": "pr", "repo": liveRepo, "number": strconv.Itoa(number), "merged": "true",
		"merge_commit_sha": "merge-" + head, "head_sha": head, "base_ref": "main", "head_ref": branch,
		"updated_at": time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		r.t.Fatal(err)
	}
	r.publish(fmt.Sprintf("notifications.github.%s.pr.%d", r.subjectRepo(), number), "github", fmt.Sprintf("pr-%d-merged", number), string(payload))
	r.waitPhase(phase.Done)
}

// onePod fails the test unless every launch the runtime was asked for is of the issue, in its one
// pod, and each of roles was launched once: every step of its agent ran in the session of that
// launch.
func (r *tasksRig) onePod(roles ...claim.Role) {
	r.t.Helper()
	launches := map[claim.Token]int{}
	for _, call := range r.rt.Calls() {
		if call.Method != "Spawn" && call.Method != "Resume" {
			continue
		}
		if call.Spec.Issue != r.key {
			r.t.Errorf("the runtime was asked to launch %s for %s, want every launch in the pod of %s", call.Spec.Claim, call.Spec.Issue, r.key)
		}
		launches[call.Spec.Claim]++
	}
	for _, role := range roles {
		if n := launches[r.token(role)]; n != 1 {
			r.t.Errorf("the %s of %s was launched %d times, want once: each of its steps in the session of its one launch", role, r.key, n)
		}
	}
}

// tasksGitHub is GitHub's REST API over one real repository, liveRepo (githubtest.GitRepository):
// its branches, the joins of its branches and its pull requests are the repository's, each pull
// request clean until GitHub merges it (merged), the person of the reviews has write access, and a
// GraphQL root knows no pull request. Every other route is a 404, so nothing reaches github.com.
type tasksGitHub struct {
	t    *testing.T
	repo *githubtest.GitRepo
	url  string
	mu   sync.Mutex
	// merges is each pull request GitHub merged, by number.
	merges map[int]bool
}

func newTasksGitHub(t *testing.T) *tasksGitHub {
	t.Helper()
	owner, name, _ := strings.Cut(liveRepo, "/")
	g := &tasksGitHub{t: t, repo: githubtest.GitRepository(t, owner, name, "main"), merges: map[int]bool{}}
	prefix := "/repos/" + liveRepo
	g.url = githubtest.Server(t, githubtest.Spec{Handle: func(w http.ResponseWriter, r *http.Request) {
		rest, ofRepository := strings.CutPrefix(r.URL.Path, prefix)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/graphql":
			_, _ = io.WriteString(w, `{"data":{"repository":{"pullRequest":null}}}`)
		case r.Method == http.MethodGet && ofRepository && rest == "/collaborators/"+stepRecordsPerson+"/permission":
			_, _ = io.WriteString(w, `{"permission":"write","user":{"login":"`+stepRecordsPerson+`"}}`)
		case r.Method == http.MethodGet && ofRepository && strings.HasPrefix(rest, "/pulls/") && strings.HasSuffix(rest, "/reviews"):
			_, _ = io.WriteString(w, `[]`)
		case r.Method == http.MethodGet && ofRepository && (rest == "/pulls" || strings.HasPrefix(rest, "/pulls/")):
			g.readPulls(w, r)
		case g.repo.Handler(w, r):
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"message":"Not Found"}`)
		}
	}})
	return g
}

// readPulls answers a read of one pull request or of a list as the repository answers it, each
// open one mergeable and clean, and each GitHub merged closed and merged, left out of a list of
// the open ones.
func (g *tasksGitHub) readPulls(w http.ResponseWriter, r *http.Request) {
	recorded := httptest.NewRecorder()
	g.repo.Handler(recorded, r)
	if recorded.Code != http.StatusOK {
		w.WriteHeader(recorded.Code)
		_, _ = w.Write(recorded.Body.Bytes())
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	shape := func(pull map[string]any) map[string]any {
		number, _ := pull["number"].(float64)
		pull["updated_at"] = "2026-11-22T00:00:00Z"
		if g.merges[int(number)] {
			head, _ := pull["head"].(map[string]any)
			pull["state"], pull["merged"], pull["mergeable"], pull["mergeable_state"] = "closed", true, true, "unknown"
			pull["merge_commit_sha"] = fmt.Sprintf("merge-%v", head["sha"])
			return pull
		}
		pull["mergeable"], pull["mergeable_state"] = true, "clean"
		return pull
	}
	var answer any
	if strings.HasSuffix(r.URL.Path, "/pulls") {
		var listed []map[string]any
		if err := json.Unmarshal(recorded.Body.Bytes(), &listed); err != nil {
			g.t.Errorf("decode the repository's list of pull requests: %v", err)
		}
		open := r.URL.Query().Get("state") == "" || r.URL.Query().Get("state") == "open"
		kept := []map[string]any{}
		for _, pull := range listed {
			if pull = shape(pull); !open || pull["state"] == "open" {
				kept = append(kept, pull)
			}
		}
		answer = kept
	} else {
		var pull map[string]any
		if err := json.Unmarshal(recorded.Body.Bytes(), &pull); err != nil {
			g.t.Errorf("decode the repository's pull request: %v", err)
		}
		answer = shape(pull)
	}
	_ = json.NewEncoder(w).Encode(answer)
}

// open is an agent opening the pull request of branch against base, as `gh pr create` does, which
// the repository must take; it answers its number.
func (g *tasksGitHub) open(branch, base string) int {
	g.t.Helper()
	body, err := json.Marshal(map[string]string{"title": branch, "head": branch, "base": base, "body": "The work of " + branch + "."})
	if err != nil {
		g.t.Fatal(err)
	}
	response, err := (&http.Client{Timeout: 10 * time.Second}).Post(g.url+"/repos/"+liveRepo+"/pulls", "application/json", bytes.NewReader(body))
	if err != nil {
		g.t.Fatalf("open the pull request of %s against %s: %v", branch, base, err)
	}
	defer response.Body.Close()
	answer, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusCreated {
		g.t.Fatalf("open the pull request of %s against %s = %d %s (%v), want 201", branch, base, response.StatusCode, answer, err)
	}
	var opened struct {
		Number int `json:"number"`
	}
	if err := json.Unmarshal(answer, &opened); err != nil || opened.Number == 0 {
		g.t.Fatalf("decode the opened pull request %s: %v", answer, err)
	}
	return opened.Number
}

// merged is GitHub's merge of pull request number: every read from now on answers it merged.
func (g *tasksGitHub) merged(number int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.merges[number] = true
}

// taskHeldMigration ends the name of the migration that holds a task where it stands, the first one
// of the release of several tasks. It is found by its name rather than its number, so the store of
// the release before is the same whatever number a later merge gives it.
const taskHeldMigration = "_task_held.up.sql"

// acrossIssue is one issue of one task, a, that the release before several tasks left mid-walk:
// its key and title, the role of each step of its plan, the place its task stood at, whether it is
// held there for the failure of its step's claim, and the session the release before recorded for
// each of its claims.
type acrossIssue struct {
	key, title string
	steps      []claim.Role
	place      int
	held       bool
	sessions   map[claim.Role]sliceFourSession
}

// role is the role of the step the issue's task stood at.
func (i *acrossIssue) role() claim.Role { return i.steps[i.place-1] }

// stands is the phase of the step the issue's task stood at.
func (i *acrossIssue) stands() phase.Phase {
	return map[claim.Role]phase.Phase{claim.RoleImplementer: phase.Implementing, claim.RoleTester: phase.Testing,
		claim.RoleReviewer: phase.Reviewing}[i.role()]
}

// An issue of one task goes on across the release. The store is the one the release before
// several tasks leaves: every embedded migration below taskHeldMigration, applied by hand, with
// three issues of one task, a, written in that schema's columns (seedAcross): one at its step 1 of
// `implementer reviewer`, one at its step 2 of it, and one held at testing, its step 2 of
// `implementer tester`, for its tester's failed claim. The release's migrations then run on it, and
// the whole daemon boots on it over the real Dispatch of this package's tests. Each goes on from its
// step: the step's claim, resumed in the session the release before recorded, takes the step's
// task, the held one's once its owner retries it, and each reaches done (goOnAcross); no step
// before its own starts again, each claim keeps its session, and task a is done (wentOnAcross).
//
// The fault: an issue in flight at the release that starts again from its first step redoes the
// work it did. The control is a fourth issue seeded as the one at its step 2 of `implementer
// reviewer`, whose review then requests changes: the daemon starts its step 1 again, which is the
// start the fault makes, and the same check must say so.
func TestAnIssueOfOneTaskGoesOnAcrossTheRelease(t *testing.T) {
	r, walks, control := newAcrossRig(t)
	for _, issue := range walks {
		r.on(issue.key)
		r.goOnAcross(issue)
	}
	for _, issue := range walks {
		if err := r.wentOnAcross(issue); err != nil {
			t.Fatal(err)
		}
		if got := string(r.d.state().Issues[issue.key].Phase); got != string(phase.Done) {
			t.Fatalf("%s, left at its step %d, stands in %s after its walk, want done", issue.key, issue.place, got)
		}
	}

	// The control: the issue whose review sends it back starts again at its first step, and the
	// check names it.
	r.on(control.key)
	r.review("CHANGES_REQUESTED", phase.Implementing)
	r.awaitLaunch(r.token(claim.RoleImplementer))
	err := r.wentOnAcross(control)
	if err == nil {
		t.Fatalf("control: %s, whose review sent it back to its first step, passed the check of an issue that went on from its step 2", control.key)
	}
	if want := "started again at its first step"; !strings.Contains(err.Error(), want) {
		t.Fatalf("control: the check of %s says %q, want it to say it %s", control.key, err, want)
	}
}

// goOnAcross walks issue to done from the step the release before left it at: the held one after
// its owner's retry, its tester then passing; the one at its implementer through its pull request
// and a review that approves; the one at its reviewer through that review; then each one's retro
// and merge.
func (r *sliceFourRig) goOnAcross(issue *acrossIssue) {
	t := r.t
	t.Helper()
	switch {
	case issue.held:
		c := r.worker(claim.RoleArchitect)
		c.ownTurn()
		status, body := r.d.request(http.MethodPost, "/legion/v1/phase/retry", map[string]any{"grantId": c.grant(), "issue": issue.key, "decision": "retry"}, false)
		if status != http.StatusOK {
			t.Fatalf("the owner's retry of %s = %d %s, want 200", issue.key, status, body)
		}
		r.waitPhase(phase.Testing)
		r.work(claim.RoleTester, "test", testCompletion, phase.Retro)
	case issue.place == 1:
		r.github.open(claim.IssueBranch(r.key), r.number)
		r.work(claim.RoleImplementer, "implement", api.HandoffCompleteRequest{Summary: "implemented", Commit: stepRecordsHead}, phase.Reviewing)
		r.review("APPROVED", phase.Retro)
	default:
		r.review("APPROVED", phase.Retro)
	}
	r.retro()
	r.merge()
}

// wentOnAcross is the check that issue went on from the step the release before left its task at:
// no step before that one started again (step_starts), each claim the release before recorded holds
// the same session, and its task is done. The error names the first thing that does not hold.
func (r *sliceFourRig) wentOnAcross(issue *acrossIssue) error {
	r.t.Helper()
	ctx := context.Background()
	var earlier int
	if err := r.st.Pool().QueryRow(ctx, `select count(*) from step_starts where issue = $1 and task = 'a' and kind = 'step' and place < $2`,
		issue.key, issue.place).Scan(&earlier); err != nil {
		return fmt.Errorf("read the step starts of %s: %w", issue.key, err)
	}
	if earlier != 0 {
		return fmt.Errorf("%s, left at its step %d by the release before, started again at its first step: %d starts of a step before it",
			issue.key, issue.place, earlier)
	}
	for role, kept := range issue.sessions {
		if got := r.d.claim(r.tokenOf(issue.key, role)).Session; got != kept.id {
			return fmt.Errorf("%s's %s holds the session %q, want %q, the one the release before recorded", issue.key, role, got, kept.id)
		}
	}
	var state string
	if err := r.st.Pool().QueryRow(ctx, `select state from plan_tasks where issue = $1 and generation = 1 and task = 'a'`, issue.key).Scan(&state); err != nil {
		return fmt.Errorf("read task a of %s: %w", issue.key, err)
	}
	if state != string(record.TaskDone) {
		return fmt.Errorf("task a of %s is %s after its walk, want done", issue.key, state)
	}
	return nil
}

// newAcrossRig is the store the release before several tasks leaves (storeBeforeSeveralTasks),
// seeded with the three issues of one task that go on and the control (seedAcross), migrated to
// this release's schema, with the daemon booted on it at the real Dispatch of this package's tests,
// each issue's architect resumed in the session the earlier daemon recorded, and the claim of each
// step the issues stood at but the held one's.
func newAcrossRig(t *testing.T) (*sliceFourRig, []*acrossIssue, *acrossIssue) {
	t.Helper()
	proof := newLiveProofOn(t, workflowNATS(t))
	st, dsn := storeBeforeSeveralTasks(t)
	proof.st, proof.cfg.PostgresDSN = st, dsn
	proof.cfg.Project = "AR" + randomSuffix(t)[:6]
	proof.cfg.Projects = map[string]config.Project{proof.cfg.Project: {}}
	proof.cfg.AdmissionCap = 8
	var err error
	if proof.project, err = claim.ProjectToken(proof.cfg.Project); err != nil {
		t.Fatal(err)
	}
	number, err := strconv.ParseInt(randomSuffix(t)[:6], 16, 64)
	if err != nil {
		t.Fatal(err)
	}
	r := &sliceFourRig{stepRecordsRig: &stepRecordsRig{liveProof: proof, dispatch: startScratchDispatchOn(t, scratchDispatchOptions{}),
		github: newStepRecordsGitHub(t), claims: map[claim.Token]*stepClaimant{}, waits: make(chan claim.Token, 16)}}
	if err := os.WriteFile(r.cfg.DispatchTokenFile, []byte(r.dispatch.token+"\n"), 0o600); err != nil {
		t.Fatalf("write the Dispatch token file: %v", err)
	}
	r.cfg.DispatchURL = r.dispatch.base
	r.person(http.MethodPost, "/api/v1/projects", map[string]string{"key": r.cfg.Project, "name": "Across the release"}, http.StatusCreated, nil)
	r.person(http.MethodPut, "/api/v1/settings/repo-projects/"+liveRepo, map[string]any{"project": r.cfg.Project, "default": true}, http.StatusOK, nil)
	implementerReviewer := []claim.Role{claim.RoleImplementer, claim.RoleReviewer}
	walks := []*acrossIssue{
		{title: "Implementer resumes its step", steps: implementerReviewer, place: 1},
		{title: "Reviewer resumes its round", steps: implementerReviewer, place: 2},
		{title: "Tester retried after hold", steps: []claim.Role{claim.RoleImplementer, claim.RoleTester}, place: 2, held: true},
	}
	control := &acrossIssue{title: "Control sent back to its first step", steps: implementerReviewer, place: 2}
	for slot, issue := range append(slices.Clone(walks), control) {
		var created createdIssue
		status := workflow.PhaseStatus(issue.stands())
		r.person(http.MethodPost, "/api/v1/issues", map[string]string{"project": r.cfg.Project, "title": issue.title}, http.StatusCreated, &created)
		r.person(http.MethodPatch, "/api/v1/issues/"+created.Key, map[string]any{"status": status, "labels": []string{"legion"}}, http.StatusOK, &created)
		issue.key = created.Key
		r.issues = append(r.issues, walkIssue{key: created.Key, title: issue.title, number: int(number) + 1 + slot})
		r.on(created.Key)
		if issue.place >= 2 {
			r.github.open(claim.IssueBranch(r.key), r.number)
		}
		issue.sessions = r.seedAcross(issue, slot+1, status, created.Rank, created.LastSeq)
	}
	if _, err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate the store the release before several tasks left to this release's schema: %v", err)
	}
	r.boot()
	for _, issue := range append(slices.Clone(walks), control) {
		r.on(issue.key)
		r.resume(claim.RoleArchitect, issue.sessions[claim.RoleArchitect])
		if kept, ok := issue.sessions[issue.role()]; ok {
			r.resume(issue.role(), kept)
		}
	}
	return r, walks, control
}

// storeBeforeSeveralTasks is a database of this test's own carrying every embedded migration whose
// version is below taskHeldMigration's, applied by hand in version order (applyMigrationsByHand):
// the schema the release before several tasks leaves.
func storeBeforeSeveralTasks(t *testing.T) (*store.Store, string) {
	t.Helper()
	dsn := testdb.Database(t, "legion_several_tasks")
	st, err := store.Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open the store the release before several tasks leaves: %v", err)
	}
	t.Cleanup(st.Close)
	all, err := migrations.All()
	if err != nil {
		t.Fatalf("read the embedded migrations: %v", err)
	}
	first := 0
	for _, migration := range all {
		if strings.HasSuffix(migration.Name, taskHeldMigration) {
			first = migration.Version
		}
	}
	if first == 0 {
		t.Fatalf("no embedded migration ends %s, the first one of the release of several tasks", taskHeldMigration)
	}
	var before []migrations.Migration
	for _, migration := range all {
		if migration.Version < first {
			before = append(before, migration)
		}
	}
	applyMigrationsByHand(t, st, before)
	return st, dsn
}

// seedAcross writes the walk's issue as the release before several tasks leaves issue, with plain
// SQL in that release's columns: the root handed to Legion in slot, its tree under the workflow,
// its approved design gate, its open pull request once it is past its implementer, the owner's
// plan of issue's steps at version 1 with its task a running at issue's place, the implementer's
// phases row of its done step 1 once past it, the row of the
// step's role raised at entry 1 for that step, and the claims of the architect and of that role
// (none for the held one's tester, whose claim failed), each a launch the earlier daemon began in
// the session it recorded, which the boot resumes. The held one stands held from the phase of its
// step for a failed claim, which records no reason. It answers those sessions.
func (r *sliceFourRig) seedAcross(issue *acrossIssue, slot int, status, rank string, seq int64) map[claim.Role]sliceFourSession {
	t := r.t
	t.Helper()
	ctx := context.Background()
	exec := func(what, statement string, args ...any) {
		t.Helper()
		if _, err := r.st.Pool().Exec(ctx, statement, args...); err != nil {
			t.Fatalf("write %s in the columns of the release before several tasks: %v", what, err)
		}
	}
	key, stands := r.key, issue.stands()
	issuePhase, heldFrom := string(stands), any(nil)
	if issue.held {
		issuePhase, heldFrom = string(phase.Held), string(stands)
	}
	exec("the root "+key, `insert into issues (key, tree, project, title, phase, generation, status, rank, last_dispatch_seq,
		handed_over, dispatch_status, repo, default_branch, github_repo, held_from)
		values ($1, $1, $2, $3, $4, 1, $5, $6, $7, true, $5, $8, 'main', $8, $9)`,
		key, r.cfg.Project, issue.title, issuePhase, status, rank, seq, liveRepo, heldFrom)
	exec("the tree of "+key, `insert into tree_lifecycles (project, tree, epoch, authority) values ($1, $2, 1, 'workflow')`, r.project, key)
	exec("the design gate of "+key, `insert into design_gates (issue, artifact_id, latest_version, approved_version, ever_opened, step)
		values ($1, $2, 1, 1, true, 'spec')`, key, "spec-"+key)
	exec("the slot of "+key, `insert into slots (issue, index, admitted_at) values ($1, $2, now() - interval '1 day')`, key, slot)
	if issue.place >= 2 {
		exec("the pull request of "+key, `insert into pull_requests (issue, repo, number, branch, head_sha, head_updated_at,
			failing, fix_attempts, blocked_attempts, check_runs, generation, snapshot, head_counted, state, mergeability, merge_sha)
			values ($1, $2, $3, $4, $5, now() - interval '3 hours', '[]', 0, 0, '[]', 1, '', '', 'open', 'MERGEABLE', '')`,
			key, liveRepo, r.number, claim.IssueBranch(key), stepRecordsHead)
	}
	exec("the plan of "+key, `insert into issue_plans (issue, generation, version, source, artifact_id, given_by, given_at)
		values ($1, 1, 1, 'owner', $2, $3, now() - interval '5 hours')`, key, "plan-"+key, string(r.token(claim.RoleArchitect)))
	exec("the task a of "+key, `insert into plan_tasks (issue, generation, task, plan_version, block_text, state, running_kind, position)
		values ($1, 1, 'a', 1, 'Make the change the issue asks for.', 'running', 'step', $2)`, key, issue.place)
	types := map[claim.Role]string{claim.RoleImplementer: "implementation", claim.RoleTester: "testing", claim.RoleReviewer: "review"}
	for at, role := range issue.steps {
		exec("step "+strconv.Itoa(at+1)+" of task a of "+key, `insert into plan_steps (issue, generation, task, position, role, type)
			values ($1, 1, 'a', $2, $3, $4)`, key, at+1, string(role), types[role])
	}
	if issue.place >= 2 {
		exec("the implementer's row of "+key, `insert into phases (issue, role, claim, handoff_commit, rounds, pass, verdict, summary,
			last_handoff, completed_at, step_task, step_place) values ($1, 'implementer', $2, $3, 0, 0, '', 'implemented', $4,
			now() - interval '2 hours', 'a', 1)`, key, string(r.token(claim.RoleImplementer)), stepRecordsHead, "implement-record-"+key)
	}
	// The step's entry carries the resume task the earlier daemon stored for it, in the shape the
	// workflow composes a resume (workflow's Engine.ResumeTask), as seedSliceFourIssue writes it.
	resume := fmt.Sprintf("Continue %s. Issue: %s. Phase: %s. Working copy: branch %s. Resume the existing phase work.",
		issue.title, key, stands, claim.IssueBranch(key))
	exec("the "+string(issue.role())+"'s row of "+key, `insert into phases (issue, role, claim, handoff_commit, rounds, verdict, summary,
		last_handoff, entry, entry_task, entry_raised_at, step_task, step_place) values ($1, $2, $3, '', 0, '', '', '', 1, $4,
		now() - interval '1 hour', 'a', $5)`, key, string(issue.role()), string(r.token(issue.role())), resume, issue.place)
	claims := []claim.Role{claim.RoleArchitect}
	if !issue.held {
		claims = append(claims, issue.role())
	}
	sessions := map[claim.Role]sliceFourSession{}
	for _, role := range claims {
		token := r.token(role)
		kept := sliceFourSession{id: "ses_" + string(role) + "_" + strings.ToLower(key), file: filepath.Join(r.cfg.StateDir, key+"-"+string(role)+".jsonl")}
		sessions[role] = kept
		exec("the "+string(role)+"'s claim on "+key, `insert into claims (token, project, tree, issue, role, generation, session,
			session_file, state, launch_failures, prompt_failures, prompt_retires, uncertain_streak, boot_token_hash, serving_generation,
			tree_epoch, plugin_contract) values ($1, $2, $3, $3, $4, 1, $5, $6, 'launching', 0, 0, 0, 0, $7, 1, 1, $8)`,
			string(token), r.project, key, string(role), kept.id, kept.file, supervise.HashBootToken("interrupted-"+string(token)),
			api.OwnerPlanContract)
		writePrompt(t, r.cfg, token)
	}
	return sessions
}
