package workflow

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// textEngine is stepEngine whose runtime places an issue's working copy at /work/<KEY>
// (Config.WorkspaceDir).
func textEngine() *Engine {
	engine := stepEngine(nil)
	engine.cfg.WorkspaceDir = func(issue record.Issue) string { return "/work/" + issue.Key }
	return engine
}

// lastStart is the task of the newest start of role in key's outbox.
func lastStart(t *testing.T, pool *pgxpool.Pool, key string, role claim.Role) string {
	t.Helper()
	starts := startsOf(t, pool, key, role)
	if len(starts) == 0 {
		t.Fatalf("%s's outbox holds no start of the %s", key, role)
	}
	return starts[len(starts)-1].Task
}

// completes applies role's completion of its step of LEGION-208 with the handoff record word-n at
// version n, through the completion route (intake.ApplyFact), which writes the role's phases row.
func completes(t *testing.T, pool *pgxpool.Pool, engine *Engine, role claim.Role, word string, n int) {
	t.Helper()
	fact := intake.HandoffComplete{Generation: 1, Issue: "LEGION-208", Role: role, Claim: claim.Token(string(role) + "-claim"),
		Summary: "done", Commit: "head", HandoffRecord: word + "-" + string(rune('0'+n)), HandoffVersion: n}
	if role == claim.RoleTester {
		fact.Verdict = "pass"
	}
	if result := applyFacts(t, pool, engine)(string(role)+"-"+word+"-"+string(rune('0'+n)), fact); result.Refusal != nil {
		t.Fatalf("the %s's completion %s version %d was refused: %+v", role, word, n, result.Refusal)
	}
}

// hasPrefix fails the test unless the task starts with want.
func hasPrefix(t *testing.T, who, task, want string) {
	t.Helper()
	if !strings.HasPrefix(task, want) {
		t.Fatalf("the %s's task = %q,\nwant it to start %q", who, task, want)
	}
}

// lacks fails the test if task holds any of words.
func lacks(t *testing.T, who, task string, words ...string) {
	t.Helper()
	for _, word := range words {
		if strings.Contains(task, word) {
			t.Fatalf("the %s's task = %q, want no %q", who, task, word)
		}
	}
}

// The task each agent of an owner's plan is told names its working copy, its task and its step in
// it, each step of the task in order, the task's text and the report of each earlier step (R4,
// R21, B-10): the reviewer of the task `implementer reviewer` reads the implementer's report, and
// the retro and the merger, which run no step, are told the task and its steps alone, and read the
// runs of the steps from Dispatch.
func TestATaskNamesItsWorkingCopyItsStepAndTheReportsBeforeIt(t *testing.T) {
	pool := migratedPool(t)
	engine := textEngine()
	root := stepRoot(t, pool, phase.Admitted)
	seedStepsPlan(t, pool, root.Key, root.Generation, "implementer", "reviewer")
	const continued = "Continue root. Issue: LEGION-208. Phase: "
	const working = " Working copy: branch legion/LEGION-208, directory /work/LEGION-208."
	const steps = " Steps: 1 implementer, 2 reviewer then Legion's retro and merge."

	move(t, pool, engine, root.Key, TriggerGateOpened, "", record.PhaseRow{})
	implementer := lastStart(t, pool, root.Key, claim.RoleImplementer)
	hasPrefix(t, "implementer", implementer, continued+"implementing."+working+" Task: a. Step 1 of 2: implementer."+steps+
		" Task text: Build the widget.")
	lacks(t, "implementer", implementer, "reported:")

	completes(t, pool, engine, claim.RoleImplementer, "implement", 1)
	reviewer := lastStart(t, pool, root.Key, claim.RoleReviewer)
	hasPrefix(t, "reviewer", reviewer, continued+"reviewing."+working+" Task: a. Step 2 of 2: reviewer."+steps+
		" Task text: Build the widget. Step 1 (implementer) reported: implement version 1."+
		" Handoff: implement version 1 (dispatch handoff-read --issue LEGION-208 --phase implement --version 1).")

	// A worker resumed in its step is told the same standing as the start that began it.
	applyInTx(t, pool, func(ctx context.Context, tx pgx.Tx) error {
		issue, err := engine.store.Issue(ctx, tx, root.Key)
		if err != nil {
			return err
		}
		return engine.StartPromotedWorker(ctx, tx, *issue, claim.RoleReviewer)
	})
	resumed := lastStart(t, pool, root.Key, claim.RoleReviewer)
	if want := continued + "reviewing." + working + " Task: a. Step 2 of 2: reviewer." + steps +
		" Task text: Build the widget. Step 1 (implementer) reported: implement version 1. Resume the existing phase work."; resumed != want {
		t.Fatalf("the resumed reviewer's task = %q,\nwant %q", resumed, want)
	}

	move(t, pool, engine, root.Key, TriggerReviewApproved, "", record.PhaseRow{})
	retro := lastStart(t, pool, root.Key, claim.RoleImplementer)
	hasPrefix(t, "retro", retro, continued+"retro."+working+" Task: a."+steps)
	lacks(t, "retro", retro, "reported:", "Task text:", "Step 1 of", "Step 2 of")

	move(t, pool, engine, root.Key, TriggerRetroCompleted, "", record.PhaseRow{})
	merger := lastStart(t, pool, root.Key, claim.RoleMerger)
	hasPrefix(t, "merger", merger, continued+"merging."+working+" Task: a."+steps)
	lacks(t, "merger", merger, "reported:", "Task text:", "Step 1 of", "Step 2 of")
}

// A move into a phase that starts no worker composes no task, since no worker is told one: the
// merge of an issue with plan rows closes it, as the merge of an issue with no plan rows does,
// though done holds no place in its task's line, which a start's task must name (standingPart).
func TestAMoveIntoAPhaseThatStartsNoWorkerComposesNoTask(t *testing.T) {
	pool := migratedPool(t)
	engine := textEngine()
	root := stepRoot(t, pool, phase.Admitted)
	seedStepsPlan(t, pool, root.Key, root.Generation, "implementer")
	move(t, pool, engine, root.Key, TriggerGateOpened, "", record.PhaseRow{})
	move(t, pool, engine, root.Key, TriggerImplementationReady, "", record.PhaseRow{})
	move(t, pool, engine, root.Key, TriggerRetroCompleted, "", record.PhaseRow{})
	starts := len(superviseRowsOf(t, pool, root.Key, "start"))

	if result := applyFacts(t, pool, engine)("merged", intake.PullRequestMerged{Repo: "sjawhar/legion", Number: 42, MergeSHA: "merge-42"}); result.Refusal != nil {
		t.Fatalf("the merge was refused: %+v", result.Refusal)
	}
	if issue := issueOf(t, pool, engine, root.Key); issue.Phase != phase.Done {
		t.Fatalf("%s stands in %s after its merge, want done", root.Key, issue.Phase)
	}
	for _, start := range superviseRowsOf(t, pool, root.Key, "start")[starts:] {
		if start.CheckLook == 0 {
			t.Fatalf("the merge wrote the start %+v, want only its production check's: done starts no phase worker", start)
		}
	}
}

// Each earlier step of a task has its report line when a later step starts, in the two shapes in
// which the row that holds the report is written again: a return to the implementer, whose next
// run's report the reviewer reads, and a return by returnTo to the planner, whose new plan the
// implementer and the tester read.
func TestATaskNamesTheNewReportOfAStepThatRanAgain(t *testing.T) {
	t.Run("implementer reviewer, the review fails and the implementer runs again", func(t *testing.T) {
		pool := migratedPool(t)
		engine := textEngine()
		root := stepRoot(t, pool, phase.Admitted)
		seedStepsPlan(t, pool, root.Key, root.Generation, "implementer", "reviewer")
		move(t, pool, engine, root.Key, TriggerGateOpened, "", record.PhaseRow{})
		completes(t, pool, engine, claim.RoleImplementer, "implement", 1)
		completes(t, pool, engine, claim.RoleReviewer, "review", 1)

		reviewRequestsChanges(t, pool, engine, root.Key)
		again := lastStart(t, pool, root.Key, claim.RoleImplementer)
		if !strings.Contains(again, " Task: a. Step 1 of 2: implementer.") || !strings.Contains(again, " Handoff: review version 1 (") {
			t.Fatalf("the implementer's task after the return = %q, want step 1 with the failed review's report", again)
		}
		lacks(t, "returned implementer", again, "reported:")

		completes(t, pool, engine, claim.RoleImplementer, "implement", 2)
		reviewer := lastStart(t, pool, root.Key, claim.RoleReviewer)
		if !strings.Contains(reviewer, " Task: a. Step 2 of 2: reviewer.") || !strings.Contains(reviewer, " Step 1 (implementer) reported: implement version 2.") {
			t.Fatalf("the reviewer's task of the second round = %q, want step 2 naming the implementer's new report", reviewer)
		}
		lacks(t, "second round's reviewer", reviewer, "reported: implement version 1.")
	})

	t.Run("planner implementer tester, returnTo 1 returns the task to the planner", func(t *testing.T) {
		pool := migratedPool(t)
		engine := textEngine()
		root := stepRoot(t, pool, phase.Admitted)
		seedStepsPlan(t, pool, root.Key, root.Generation, "planner", "implementer", "tester")
		move(t, pool, engine, root.Key, TriggerGateOpened, "", record.PhaseRow{})
		completes(t, pool, engine, claim.RolePlanner, "plan", 1)
		completes(t, pool, engine, claim.RoleImplementer, "implement", 1)
		if result := applyFacts(t, pool, engine)("fail-1", failedTest(root.Key, 1, "test-1")); result.Refusal != nil {
			t.Fatalf("the tester's fail naming step 1 was refused: %+v", result.Refusal)
		}
		planner := lastStart(t, pool, root.Key, claim.RolePlanner)
		if !strings.Contains(planner, " Task: a. Step 1 of 3: planner.") {
			t.Fatalf("the planner's task after the return = %q, want step 1", planner)
		}
		lacks(t, "returned planner", planner, "reported:")

		completes(t, pool, engine, claim.RolePlanner, "plan", 2)
		implementer := lastStart(t, pool, root.Key, claim.RoleImplementer)
		if !strings.Contains(implementer, " Task: a. Step 2 of 3: implementer.") ||
			!strings.Contains(implementer, " Task text: Build the widget. Step 1 (planner) reported: plan version 2. Handoff: plan version 2 (") {
			t.Fatalf("the implementer's task after the new plan = %q, want step 2 naming the planner's new report", implementer)
		}

		completes(t, pool, engine, claim.RoleImplementer, "implement", 2)
		tester := lastStart(t, pool, root.Key, claim.RoleTester)
		if want := " Task text: Build the widget. Step 1 (planner) reported: plan version 2. Step 2 (implementer) reported: implement version 2."; !strings.Contains(tester, " Task: a. Step 3 of 3: tester.") ||
			!strings.Contains(tester, want) {
			t.Fatalf("the tester's task of the second pass = %q, want step 3 holding %q", tester, want)
		}
	})
}

// An issue with no plan rows, which started before plans existed, is told today's task with the
// working copy line after its continue line and no other new line (R20): the prompts read a task
// with no `Task:` line as such an issue. With no runtime to place the working copy (a nil
// Config.WorkspaceDir), the line names the branch alone.
func TestATaskOfAnIssueWithNoPlanRowsIsTodaysTextWithItsWorkingCopy(t *testing.T) {
	for _, tc := range []struct {
		name    string
		engine  *Engine
		working string
	}{
		{"a runtime places the working copy", textEngine(), " Working copy: branch legion/LEGION-208, directory /work/LEGION-208."},
		{"no runtime places it", stepEngine(nil), " Working copy: branch legion/LEGION-208."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := migratedPool(t)
			root := stepRoot(t, pool, phase.Implementing)
			completes(t, pool, tc.engine, claim.RoleImplementer, "implement", 1)
			tester := lastStart(t, pool, root.Key, claim.RoleTester)
			if want := "Continue root. Issue: LEGION-208. Phase: testing." + tc.working +
				" Handoff: implement version 1 (dispatch handoff-read --issue LEGION-208 --phase implement --version 1). Handoff commit: head. Pull request: #42."; tester != want {
				t.Fatalf("the tester's task = %q,\nwant %q", tester, want)
			}
			applyInTx(t, pool, func(ctx context.Context, tx pgx.Tx) error {
				issue, err := tc.engine.store.Issue(ctx, tx, root.Key)
				if err != nil {
					return err
				}
				return tc.engine.StartPromotedWorker(ctx, tx, *issue, claim.RoleTester)
			})
			if resumed, want := lastStart(t, pool, root.Key, claim.RoleTester), "Continue root. Issue: LEGION-208. Phase: testing."+tc.working+
				" Resume the existing phase work."; resumed != want {
				t.Fatalf("the resumed tester's task = %q,\nwant %q", resumed, want)
			}
		})
	}
}

// taskReaders is what a step's worker reads to learn what its task's lines say: the composed
// prompt of each role that works a step, the retro or the merge (rolePrompt), and the skills those
// prompts send them to, legion-worker's with its references and legion-retro's.
func taskReaders(t *testing.T) string {
	t.Helper()
	var corpus strings.Builder
	for _, role := range []claim.Role{claim.RolePlanner, claim.RoleImplementer, claim.RoleTester, claim.RoleReviewer, claim.RoleMerger} {
		corpus.WriteString(rolePrompt(t, role))
	}
	skills := filepath.Join("..", "..", "..", "..", "skills")
	paths, err := filepath.Glob(filepath.Join(skills, "legion-worker", "references", "*.md"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("list the references of skill://legion-worker under %s: %v (%d found)", skills, err, len(paths))
	}
	for _, path := range append(paths, filepath.Join(skills, "legion-worker", "SKILL.md"), filepath.Join(skills, "legion-retro", "SKILL.md")) {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		corpus.Write(body)
	}
	return corpus.String()
}

// readersNameALine finds each label the readers name as a line of the worker's task: "your
// task's `Steps:` line", "a task with no `Task:` line", "a task whose `Steps:` line", "your task
// names `Last reviewed commit:".
var readersNameALine = regexp.MustCompile("task(?:'s| with no| whose| names| has no) `([A-Z][^`:]*):")

// Each label of a step's task that the prompts and the skills tell a worker to read is one the
// task writes, and each the task writes is one they name: the label is one constant, every line is
// built from it, and a rename on either side alone fails here. The forms the prompts spell out
// with placeholders (go/worker-common.md) are the task's words exactly, once the placeholders hold
// a step's values.
func TestEachTaskLabelIsOneThePromptsName(t *testing.T) {
	readers := taskReaders(t)
	issue := record.Issue{Key: "LEGION-208", Title: "root", Phase: phase.Reviewing}
	steps := []record.PlanStep{{Task: "a", Position: 1, Role: "implementer"}, {Task: "a", Position: 2, Role: "reviewer"}}
	w := Walk{Stored: true, Task: "a", Text: "Build the widget.", Steps: steps, Line: []phase.Phase{phase.Implementing, phase.Reviewing, phase.Retro, phase.Merging}}
	rows := []record.PhaseRow{{Role: claim.RoleImplementer, HandoffRecord: "implement-1", HandoffVersion: 1}}
	at, err := standingText(issue, func(record.Issue) string { return "/work" }, w, rows)
	if err != nil {
		t.Fatal(err)
	}
	tester := record.PhaseRow{Role: claim.RoleTester, HandoffRecord: "test-1", HandoffVersion: 1, Verdict: "pass"}
	written := task(issue, at, tester, &record.PullRequest{Number: 42}, "", "the reviewer asked again")

	labels := map[string]string{
		phaseLabel: "`" + phaseLabel + ": ", workingCopyLabel: "`" + workingCopyLabel + ":", planTaskLabel: "`" + planTaskLabel + ":",
		stepsLabel: "`" + stepsLabel + ":", taskTextLabel: "`" + taskTextLabel + ":", reportedLabel: "(<role>) " + reportedLabel + ":",
		handoffLabel: "`" + handoffLabel + ":", pullRequestLabel: "`" + pullRequestLabel + ":", reasonLabel: "`" + reasonLabel + ":",
	}
	for label, named := range labels {
		if !strings.Contains(written, " "+label+": ") {
			t.Errorf("the task %q writes no %q line, which the prompts name as %q", written, label, named)
		}
		if !strings.Contains(readers, named) {
			t.Errorf("no prompt or skill a worker reads names the task's %q line as %q", label, named)
		}
	}
	for _, match := range readersNameALine.FindAllStringSubmatch(readers, -1) {
		if _, ok := labels[match[1]]; !ok && match[1] != testerVerdictLabel && match[1] != ReviewedCommitLabel {
			t.Errorf("a prompt or a skill names the task's %q line (%q), and the task writes no such label", match[1], match[0])
		}
	}

	for template, values := range map[string][]string{
		"`Working copy: branch legion/<KEY>, directory <dir>.`":         {"<KEY>", "LEGION-208", "<dir>", "/work"},
		"`Task: <id>. Step <i> of <n>: <role>.`":                        {"<id>", "a", "<i>", "2", "<n>", "2", "<role>", "reviewer"},
		"`Steps: 1 <role>, 2 <role>, … then Legion's retro and merge.`": {"1 <role>, 2 <role>, …", "1 implementer, 2 reviewer"},
		"`Step <j> (<role>) reported: <word> version <v>.`":             {"<j>", "1", "<role>", "implementer", "<word>", "implement", "<v>", "1"},
	} {
		if !strings.Contains(readers, template) {
			t.Errorf("no prompt or skill a worker reads spells out %s", template)
			continue
		}
		if want := strings.NewReplacer(values...).Replace(strings.Trim(template, "`")); !strings.Contains(written, " "+want) {
			t.Errorf("the task %q does not say %q, the words of %s", written, want, template)
		}
	}
}
