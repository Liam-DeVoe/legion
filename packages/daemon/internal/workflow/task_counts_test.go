package workflow

import (
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// The returns of a task are counted on that task (record.Store.ReturnCounts), so what the counts
// tell names the task: the architect's notice at review_round_cap and each prompt a work-back hook
// sends start `task <id>: `, since a run with plan rows can hold more than one task and the owner
// must know which one went round. A run with no plan rows has no task to name, and its notice and
// prompts read as they did before plans (the twin below).
//
// The fault this holds: a notice or a prompt that names no task, which with two tasks at one time
// cannot say whose review went round. Its control is the code before this change, under which the
// notice reads `Issue reached review_round_cap=3: …` and the prompt is the hook's own text alone.
func TestTheCapNoticeAndTheHookPromptNameTheTask(t *testing.T) {
	pool := migratedPool(t)
	engine := returnEngine()
	root := stepRoot(t, pool, phase.Admitted)
	seedStepsPlan(t, pool, root.Key, root.Generation, "implementer", "tester", "reviewer")
	move(t, pool, engine, root.Key, TriggerGateOpened, record.TaskPlace{}, record.PhaseRow{})
	for range 3 {
		move(t, pool, engine, root.Key, TriggerImplementationReady, record.TaskPlace{}, record.PhaseRow{})
		move(t, pool, engine, root.Key, TriggerTesterPassed, record.TaskPlace{}, record.PhaseRow{})
		reviewRequestsChanges(t, pool, engine, root.Key)
	}
	testsFail(t, pool, engine, root.Key, 2)

	want := "task a: Issue reached review_round_cap=3: 3 rounds counted, 0 conflict rounds not counted."
	if blocked := noticesOf(t, pool, root.Key, "pr-blocked"); len(blocked) != 1 || blocked[0].Reason != want {
		t.Fatalf("the pr-blocked notices = %+v, want one whose reason is %q", blocked, want)
	}
	prompts := hookPrompts(t, pool)
	if len(prompts) == 0 {
		t.Fatal("the hook sent no prompt at the 5th return: this proves nothing of what a prompt names")
	}
	for _, prompt := range prompts {
		if !strings.HasPrefix(prompt.Prompt, "task a: ") || strings.TrimPrefix(prompt.Prompt, "task a: ") == "" {
			t.Fatalf("the %s's prompt reads %q, want `task a: ` before the hook's own text", prompt.Role, prompt.Prompt)
		}
	}
	// The issue's message keeps its one fixed line, which a reader of a fleet counts the cap by.
	if messages, _ := capPosts(t, pool); len(messages) != 1 || strings.HasPrefix(messages[0], "task ") {
		t.Fatalf("the cap's messages = %q, want the one fixed line", messages)
	}
}

// The twin: an issue with no plan rows names no task, and its notice and prompts read as before.
func TestAnIssueWithNoPlanRowsNamesNoTaskInItsCapNoticeOrHookPrompt(t *testing.T) {
	pool := migratedPool(t)
	engine := workBackEngine(3, Hook{Name: "work-back", On: HookEventWorkBack, Every: 1,
		Roles: []claim.Role{claim.RoleImplementer}, Prompt: saySoFar})
	seedWorkBackIssue(t, pool, headOne)
	for _, round := range []string{"1", "2", "3"} {
		reviewerRequestsChanges(t, pool, engine, round, headOne)
	}

	want := "Issue reached review_round_cap=3: 3 rounds counted, 0 conflict rounds not counted."
	if blocked := noticesOf(t, pool, "LEGION-208", "pr-blocked"); len(blocked) != 1 || blocked[0].Reason != want {
		t.Fatalf("the pr-blocked notices = %+v, want one whose reason is %q", blocked, want)
	}
	prompts := hookPrompts(t, pool)
	if len(prompts) != 3 {
		t.Fatalf("the hook sent %d prompts over three returns, want one at each", len(prompts))
	}
	for _, prompt := range prompts {
		if prompt.Prompt != saySoFar {
			t.Fatalf("the %s's prompt reads %q, want the hook's own text %q", prompt.Role, prompt.Prompt, saySoFar)
		}
	}
}
