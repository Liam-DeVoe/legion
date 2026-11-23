// A later version of an owner's plan that changes only the text of a running task, through the
// whole daemon over the real Dispatch of this package's tests (ownerStepsRig,
// owner_steps_rig_test.go): Legion takes it, the task goes on where it stands, and every start
// task Legion sends for the task afterwards holds the new text, while the agent at work is sent
// nothing for the version.
package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/api"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/runtime/fake"
	"github.com/sjawhar/legion/daemon/internal/shimwire"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/testwait"
)

// textPlan is a plan.md with one task block, id, whose steps attribute names steps (none for a
// block that names no steps) and whose text is text.
func textPlan(id, steps, text string) string {
	if steps == "" {
		return ":::plan_task{#" + id + "}\n" + text + "\n:::\n"
	}
	return ":::plan_task{#" + id + " steps=\"" + steps + "\"}\n" + text + "\n:::\n"
}

// editPlan is a person writing a later version of the document artifact through Dispatch's edit
// route, replacing find with with, and fails unless Dispatch stored version want.
func (r *ownerStepsRig) editPlan(artifact, find, with string, want int) {
	r.t.Helper()
	var edited struct {
		Version *struct {
			Number int `json:"number"`
		} `json:"version"`
	}
	r.person(http.MethodPost, "/api/v1/artifacts/"+artifact+"/edits", map[string]any{
		"ops": []map[string]string{{"op": "replace", "find": find, "with": with}},
	}, http.StatusOK, &edited)
	if edited.Version == nil || edited.Version.Number != want {
		r.t.Fatalf("the edit of %s stored version %+v, want version %d", artifact, edited.Version, want)
	}
}

// takeText is c taking the daemon's next task, as take does, and answers that task's text.
func takeText(c *stepClaimant) string {
	c.r.t.Helper()
	prompt := c.receive()
	c.answer()
	c.send(shimwire.AgentStart{DeliveryID: prompt.DeliveryID})
	c.waitState(supervise.StateWorking)
	return prompt.Message
}

// holdsText fails the test unless task holds want and not was.
func holdsText(t *testing.T, who, task, want, was string) {
	t.Helper()
	if !strings.Contains(task, "\n"+want+"\n") || strings.Contains(task, was) {
		t.Fatalf("the %s's start task = %q,\nwant it to hold %q and not %q", who, task, want, was)
	}
}

// registeredRunning is register_plan's answer of artifact at version, which must be 200, and the
// act each task of it runs, by task.
func (r *ownerStepsRig) registeredRunning(artifact string, version int) map[string]*api.RunningStep {
	r.t.Helper()
	status, body := r.registerPlan(artifact, version)
	if status != http.StatusOK {
		r.t.Fatalf("register_plan of %s's version %d = %d %s, want 200", r.key, version, status, body)
	}
	var taken api.PlanRegisterResponse
	if err := json.Unmarshal([]byte(body), &taken); err != nil {
		r.t.Fatalf("decode register_plan's answer %s: %v", body, err)
	}
	running := map[string]*api.RunningStep{}
	for _, task := range taken.Plan.Tasks {
		running[task.ID] = task.Running
	}
	return running
}

// taskRow is where task a of the walk's issue stands in the daemon's store, with its counts.
func (r *ownerStepsRig) taskRow(task string) string {
	r.t.Helper()
	var state, kind string
	var position, pass, rounds, conflicts int
	if err := r.st.Pool().QueryRow(context.Background(), `select state, running_kind, position, pass, rounds, conflict_rounds
		from plan_tasks where issue = $1 and task = $2`, r.key, task).Scan(&state, &kind, &position, &pass, &rounds, &conflicts); err != nil {
		r.t.Fatalf("read task %s of %s: %v", task, r.key, err)
	}
	return fmt.Sprintf("%s at %s %d, pass %d, rounds %d, conflict rounds %d", state, kind, position, pass, rounds, conflicts)
}

// launchCount is how many launches of method the runtime has made of token.
func (r *ownerStepsRig) launchCount(token claim.Token, method string) int {
	count := 0
	for _, call := range r.rt.CallsOf(method) {
		if call.Spec.Claim == token {
			count++
		}
	}
	return count
}

// processEnds is the end of c's process, which the runtime reports gone, and the wait for the
// daemon's next launch of its claim of method (Spawn or Resume).
func (r *ownerStepsRig) processEnds(c *stepClaimant, method string) {
	r.t.Helper()
	held := r.d.claim(c.token)
	if held.Locator == nil {
		r.t.Fatalf("%s records no process", c.token)
	}
	before := r.launchCount(c.token, method)
	c.sh.end()
	r.rt.Emit(runtime.Observation{Locator: *held.Locator, Kind: runtime.Gone, At: time.Now(), Detail: "the driver ended the process"})
	testwait.WithinPolling(r.t, stepRecordsWait, 50*time.Millisecond, fmt.Sprintf("a %s of %s", method, c.token), func() bool {
		return r.launchCount(c.token, method) > before
	})
}

// Check c-text-change. Issue A runs its owner's task a, `implementer reviewer`, with the text
// "Text one". While the implementer works, the owner gives version 2, whose one change is the text
// "Text two": register_plan answers 200 and names step 1, the implementer; task a keeps its state,
// its place and its counts; and the implementer is sent nothing. The implementer's session is then
// lost, and the fresh session's first task holds "Text two". The process ends again and resumes
// its session, and the task sent to it again holds "Text two". So do the first reviewer's task, the
// implementer's after the review's return, the second reviewer's and the retro's, and none holds
// "Text one". While the second reviewer works, version 3 changes the steps of a, and Legion refuses
// it PLAN_REFUSED, naming task a and saying it runs. The step records count the starts of a run
// with no version 2. Issue B runs the project's default steps, and while its planner works the
// owner gives a plan.md whose one block, default, names no steps and holds "Text three":
// register_plan answers 200, and B's implementer's task holds "Text three" and the default steps.
func TestALaterVersionChangesTheTextOfARunningTaskForEveryLaterStart(t *testing.T) {
	r := newOwnerStepsRig(t, newLiveProofOn(t, workflowNATS(t)), startScratchDispatchOn(t, scratchDispatchOptions{}), 2, config.Project{})
	const one, two, three = "Text one", "Text two", "Text three"
	plan := r.document("plan.md", textPlan("a", "implementer reviewer", one))
	if status, body := r.registerPlan(plan, 1); status != http.StatusOK {
		t.Fatalf("register_plan of %s's version 1 = %d %s, want 200", r.key, status, body)
	}
	r.openGate(phase.Implementing)
	implementer := r.worker(claim.RoleImplementer)
	holdsText(t, "implementer", takeText(implementer), one, two)
	before := r.taskRow("a")

	r.editPlan(plan, one, two, 2)
	running := r.registeredRunning(plan, 2)
	if got := running["a"]; got == nil || *got != (api.RunningStep{Kind: "step", Place: 1, Role: string(claim.RoleImplementer)}) {
		t.Fatalf("register_plan of version 2 names task a running at %+v, want step 1, the implementer", got)
	}
	if after := r.taskRow("a"); after != before {
		t.Fatalf("task a after version 2 is %s, want it as it was: %s", after, before)
	}
	select {
	case prompt := <-implementer.prompts:
		t.Fatalf("the implementer at work was sent %q for version 2, want nothing", prompt.Message)
	case <-time.After(liveSettle):
	}

	// The implementer's session is lost: no store holds it, so the relaunch spawns a fresh agent.
	r.rt.ScriptResume(fake.SpawnResult{Err: fmt.Errorf("resume %s: %w: the session table holds no such session",
		implementer.token, runtime.ErrSessionFileMissing)})
	r.processEnds(implementer, "Spawn")
	delete(r.claims, implementer.token)
	fresh := r.worker(claim.RoleImplementer)
	if fresh.session == implementer.session {
		t.Fatalf("the implementer's fresh agent registered in the lost session %s", implementer.session)
	}
	holdsText(t, "fresh implementer", takeText(fresh), two, one)

	// Its process ends again and resumes its own session: the task is sent to it again.
	r.processEnds(fresh, "Resume")
	r.reattach(fresh)
	holdsText(t, "resumed implementer", takeText(fresh), two, one)
	if !r.github.opened(r.number) {
		r.github.open("legion/"+r.key, r.number)
	}
	r.finish(fresh, "implement", api.HandoffCompleteRequest{Summary: "implemented", Commit: stepRecordsHead}, phase.Reviewing)

	reviewer := r.worker(claim.RoleReviewer)
	holdsText(t, "first reviewer", takeText(reviewer), two, one)
	reviewer.endTurn()
	r.publishReview(r.number, "CHANGES_REQUESTED")
	r.waitPhase(phase.Implementing)

	holdsText(t, "returned implementer", takeText(fresh), two, one)
	r.finish(fresh, "implement", api.HandoffCompleteRequest{Summary: "implemented again", Commit: stepRecordsHead}, phase.Reviewing)
	holdsText(t, "second reviewer", takeText(reviewer), two, one)

	r.editPlan(plan, `steps="implementer reviewer"`, `steps="implementer tester reviewer"`, 3)
	status, body := r.registerPlan(plan, 3)
	var refusal api.PlanRefusal
	if err := json.Unmarshal([]byte(body), &refusal); err != nil || status != http.StatusConflict || refusal.Code != "PLAN_REFUSED" ||
		len(refusal.Reasons) != 1 || refusal.Reasons[0].Task != "a" || !strings.Contains(refusal.Reasons[0].Reason, "task a is running") {
		t.Fatalf("register_plan of version 3, which changes a's steps while it runs, = %d %s, want 409 PLAN_REFUSED naming task a as running", status, body)
	}
	reviewer.endTurn()
	r.publishReview(r.number, "APPROVED")
	r.waitPhase(phase.Retro)
	holdsText(t, "retro", takeText(fresh), two, one)

	starts := strings.Join(r.stepStarts(), "\n")
	for _, want := range []string{"a step 1 starts 2 ", "a step 2 starts 2 ", "a retro 0 starts 1 "} {
		if !strings.Contains(starts+" ", want) {
			t.Fatalf("the step starts of %s are\n%s\nwant %q, as a run with no version 2 counts them", r.key, starts, want)
		}
	}

	r.on(r.issues[1].key)
	r.openGate(phase.Planning)
	planner := r.worker(claim.RolePlanner)
	takeText(planner)
	defaults := r.document("plan.md", textPlan("default", "", three))
	if got := r.registeredRunning(defaults, 1)["default"]; got == nil || got.Kind != "step" || got.Place != 1 || got.Role != string(claim.RolePlanner) {
		t.Fatalf("register_plan of %s's default block names it running at %+v, want step 1, the planner", r.key, got)
	}
	r.finish(planner, "plan", api.HandoffCompleteRequest{Summary: "planned"}, phase.Implementing)
	task := takeText(r.worker(claim.RoleImplementer))
	holdsText(t, r.key+"'s implementer", task, three, "The project's default steps.")
	if !strings.Contains(task, "Task: default. Step 2 of ") || !strings.Contains(task, "1 planner, 2 implementer") {
		t.Fatalf("%s's implementer's task %q does not run the project's default steps", r.key, task)
	}
}
