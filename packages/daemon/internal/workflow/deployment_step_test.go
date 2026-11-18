package workflow

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// stepHead is the commit every completion of these tests reports.
const stepHead = "0123456789abcdef0123456789abcdef01234567"

// stepReports applies role's completion of key at its generation 1 with verdict ("" for none),
// summary and returnTo (0 for none), as the route hands it to the engine, and fails the test on a
// refusal; handoffRecord is the handoff record it names ("" for none).
func stepReports(t *testing.T, pool *pgxpool.Pool, engine *Engine, key string, role claim.Role, verdict, summary, handoffRecord string, returnTo int) {
	t.Helper()
	fact := intake.HandoffComplete{Generation: 1, Issue: key, Role: role, Claim: claim.Token(string(role) + "-claim"), Summary: summary, Commit: stepHead,
		Verdict: verdict, HandoffRecord: handoffRecord, HandoffVersion: 1, ReturnTo: returnTo}
	if result := applyFacts(t, pool, engine)(string(role)+"-"+verdict+"-"+handoffRecord+"-"+summary, fact); result.Refusal != nil {
		t.Fatalf("the %s's completion of %s was refused: %+v", role, key, result.Refusal)
	}
}

// stepRoleOf is key's running role as the store reads it.
func stepRoleOf(t *testing.T, pool *pgxpool.Pool, engine *Engine, key string) claim.Role {
	t.Helper()
	return issueOf(t, pool, engine, key).StepRole
}

// A step of a role the deployment defines runs as step whatever its type (C2): checker of the
// type testing, critic of the type review and audit of a type of the deployment's own. After the
// implementer's completion the issue stands at step with the role running and the role started
// once. The role's fail returns the task to the first step of the type implementation before it,
// whose task carries the role's report under its own name, not the tester's; the failed step's
// record says failed, returned to 1; a failed step of the type review counts a review round. The
// role's pass moves the task to its retro and starts the implementer's retro.
func TestADeploymentRoleOfAnyTypeStartsAndReports(t *testing.T) {
	for _, tc := range []struct {
		role   claim.Role
		typ    string
		rounds int
	}{
		{"checker", "testing", 0},
		{"critic", "review", 1},
		{"audit", "security", 0},
	} {
		t.Run(string(tc.role), func(t *testing.T) {
			pool := migratedPool(t)
			engine := returnEngine()
			root := stepRoot(t, pool, phase.Admitted)
			seedStepsPlan(t, pool, root.Key, root.Generation, "implementer", string(tc.role))
			if _, err := pool.Exec(t.Context(), "update plan_steps set type = $2 where issue = $1 and task = 'a' and position = 2", root.Key, tc.typ); err != nil {
				t.Fatalf("store the type of %s: %v", tc.role, err)
			}
			move(t, pool, engine, root.Key, TriggerGateOpened, record.TaskPlace{}, record.PhaseRow{})

			stepReports(t, pool, engine, root.Key, claim.RoleImplementer, "", "built", "impl-1", 0)
			assertWalksAt(t, pool, engine, root.Key, phase.Step, stepPlace(2))
			if got := stepRoleOf(t, pool, engine, root.Key); got != tc.role {
				t.Fatalf("the running role = %q, want %s", got, tc.role)
			}
			if starts := startsOf(t, pool, root.Key, tc.role); len(starts) != 1 || starts[0].Phase != phase.Step {
				t.Fatalf("the starts of the %s = %+v, want one, of step", tc.role, starts)
			}

			implementerStarts := len(startsOf(t, pool, root.Key, claim.RoleImplementer))
			stepReports(t, pool, engine, root.Key, tc.role, "fail", "The check found a gap.", "", 0)
			assertWalksAt(t, pool, engine, root.Key, phase.Implementing, stepPlace(1))
			starts := startsOf(t, pool, root.Key, claim.RoleImplementer)
			if len(starts) != implementerStarts+1 {
				t.Fatalf("the implementer's starts after the fail = %d, want one more than %d", len(starts), implementerStarts)
			}
			told := starts[len(starts)-1].Task
			if says := " Step 2 (" + string(tc.role) + ") reported: fail. The check found a gap."; !strings.Contains(told, says) {
				t.Fatalf("the implementer's task = %q, want it to say %q", told, says)
			}
			if strings.Contains(told, testerVerdictLabel) {
				t.Fatalf("the implementer's task = %q, want no %s: the report is the %s's", told, testerVerdictLabel, tc.role)
			}
			if end := lastEndOfStep(t, pool, root.Key, 2); resultOf(end) != "failed" || end.ReturnedTo == nil || *end.ReturnedTo != 1 {
				t.Fatalf("the end of step 2 = %+v, want failed, returned to 1", end)
			}
			if counts := taskCounts(t, pool, root.Key, root.Generation); counts.Rounds != tc.rounds {
				t.Fatalf("the task's rounds = %d, want %d", counts.Rounds, tc.rounds)
			}

			stepReports(t, pool, engine, root.Key, claim.RoleImplementer, "", "fixed", "impl-2", 0)
			assertWalksAt(t, pool, engine, root.Key, phase.Step, stepPlace(2))
			stepReports(t, pool, engine, root.Key, tc.role, "pass", "All clear.", "", 0)
			assertWalksAt(t, pool, engine, root.Key, phase.Retro, record.TaskPlace{Kind: record.PlaceRetro})
			if retros := startsIn(t, pool, root.Key, claim.RoleImplementer, phase.Retro); len(retros) != 1 {
				t.Fatalf("the implementer's retro starts = %+v, want one", retros)
			}
		})
	}
}

// lastEndOfStep is the newest end of the step at place step in key's step records.
func lastEndOfStep(t *testing.T, pool *pgxpool.Pool, key string, step int) record.StepRecord {
	t.Helper()
	var last *record.StepRecord
	for _, write := range stepWrites(t, pool, key) {
		if isEnd(write) && write.Step == step {
			last = &write
		}
	}
	if last == nil {
		t.Fatalf("%s holds no end of step %d", key, step)
	}
	return *last
}

// Two steps of roles the deployment defines are two places of one phase: in `implementer checker
// linter` the checker's pass starts the linter, the issue stays at step with the linter running, a
// late completion of the checker moves nothing, and the linter's fail naming step 2 starts the
// checker again. In `implementer checker linter critic`, the critic's fail naming step 3 starts the
// linter again, and the critic's step ends failed, returned to 3.
func TestTwoDeploymentStepsAreTwoPlaces(t *testing.T) {
	types := map[string]string{"checker": "testing", "linter": "testing", "critic": "review"}

	t.Run("implementer checker linter", func(t *testing.T) {
		pool := migratedPool(t)
		engine := returnEngine()
		root := stepRoot(t, pool, phase.Step)
		seedStepsAt(t, pool, root.Key, root.Generation, 2, types, "implementer", "checker", "linter")

		stepReports(t, pool, engine, root.Key, "checker", "pass", "Checked.", "", 0)
		assertWalksAt(t, pool, engine, root.Key, phase.Step, stepPlace(3))
		if got := stepRoleOf(t, pool, engine, root.Key); got != "linter" {
			t.Fatalf("the running role = %q, want linter", got)
		}
		if starts := startsOf(t, pool, root.Key, "linter"); len(starts) != 1 {
			t.Fatalf("the linter's starts = %+v, want one", starts)
		}

		fact := intake.HandoffComplete{Generation: 1, Issue: root.Key, Role: "checker", Claim: "checker-claim", Summary: "Checked again.", Commit: stepHead, Verdict: "pass"}
		applyFacts(t, pool, engine)("checker-late", fact)
		assertWalksAt(t, pool, engine, root.Key, phase.Step, stepPlace(3))
		if got := stepRoleOf(t, pool, engine, root.Key); got != "linter" {
			t.Fatalf("after the checker's late completion the running role = %q, want linter", got)
		}

		checkers := len(startsOf(t, pool, root.Key, "checker"))
		stepReports(t, pool, engine, root.Key, "linter", "fail", "Lint failed.", "", 2)
		assertWalksAt(t, pool, engine, root.Key, phase.Step, stepPlace(2))
		if got := len(startsOf(t, pool, root.Key, "checker")); got != checkers+1 {
			t.Fatalf("the checker's starts after the linter's fail = %d, want %d", got, checkers+1)
		}
	})

	t.Run("implementer checker linter critic", func(t *testing.T) {
		pool := migratedPool(t)
		engine := returnEngine()
		root := stepRoot(t, pool, phase.Step)
		seedStepsAt(t, pool, root.Key, root.Generation, 4, types, "implementer", "checker", "linter", "critic")

		stepReports(t, pool, engine, root.Key, "critic", "fail", "Lint again.", "", 3)
		assertWalksAt(t, pool, engine, root.Key, phase.Step, stepPlace(3))
		if starts := startsOf(t, pool, root.Key, "linter"); len(starts) != 1 {
			t.Fatalf("the linter's starts = %+v, want one", starts)
		}
		if end := lastEndOfStep(t, pool, root.Key, 4); resultOf(end) != "failed" || end.ReturnedTo == nil || *end.ReturnedTo != 3 {
			t.Fatalf("the end of step 4 = %+v, want failed, returned to 3", end)
		}
	})
}

// A role the deployment defines reports pass or fail and opens no pull request, so in `critic
// implementer` the critic's pass, with no pull request recorded, starts the implementer.
func TestADeploymentRoleBeforeTheImplementerNeedsNoPullRequest(t *testing.T) {
	pool := migratedPool(t)
	engine := returnEngine()
	root := record.Issue{Key: "LEGION-208", Repo: "sjawhar/legion", DefaultBranch: "main", GitHubRepo: "sjawhar/legion", Tree: "LEGION-208",
		Project: "LEGION", Title: "root", Phase: phase.Step, Generation: 1, Status: "in_progress", Rank: "U", LastDispatchSeq: 1}
	seedIssue(t, pool, root)
	seedTreeLifecycle(t, pool, root.Key)
	seedStepsAt(t, pool, root.Key, root.Generation, 1, map[string]string{"critic": "review"}, "critic", "implementer")

	stepReports(t, pool, engine, root.Key, "critic", "pass", "The design holds.", "", 0)
	assertWalksAt(t, pool, engine, root.Key, phase.Implementing, stepPlace(2))
	if starts := startsOf(t, pool, root.Key, claim.RoleImplementer); len(starts) != 1 {
		t.Fatalf("the implementer's starts = %+v, want one", starts)
	}
}

// Each notice of a step of a role the deployment defines names that role: the failed claim of the
// checker that runs the issue's step holds the issue from step, and the held notice names the
// checker; the held notice of an issue held from step (HeldNotice) names its running role; and the
// checker's pass writes one phase-finished notice, which names the checker.
func TestAHeldStepOfTheDeploymentNamesItsRole(t *testing.T) {
	t.Run("the checker's failed claim", func(t *testing.T) {
		pool := migratedPool(t)
		engine := returnEngine()
		root := stepRoot(t, pool, phase.Step)
		seedStepsAt(t, pool, root.Key, root.Generation, 2, map[string]string{"checker": "testing"}, "implementer", "checker")

		applyRefusingNothing(t, pool, engine, intake.ClaimFailed{Issue: root.Key, Role: "checker", Reason: "the pane died"})

		if issue := issueOf(t, pool, engine, root.Key); issue.Phase != phase.Held || issue.Hold == nil || issue.Hold.From != phase.Step {
			t.Fatalf("%s stands in %s with hold %+v, want held from step", root.Key, issue.Phase, issue.Hold)
		}
		if roles := rolesOf(noticesOf(t, pool, root.Key, "held")); len(roles) != 1 || roles[0] != "checker" {
			t.Fatalf("the held notices name %v, want the checker once", roles)
		}
	})

	t.Run("HeldNotice", func(t *testing.T) {
		issue := record.Issue{Key: "LEGION-208", Phase: phase.Held, Hold: &record.Hold{From: phase.Step}, StepRole: "checker"}
		if got := HeldNotice(issue); got.Role != "checker" || got.Phase != phase.Step {
			t.Fatalf("HeldNotice of an issue held from step = %+v, want the checker's, of step", got)
		}
	})

	t.Run("the checker's pass", func(t *testing.T) {
		pool := migratedPool(t)
		engine := returnEngine()
		root := stepRoot(t, pool, phase.Step)
		seedStepsAt(t, pool, root.Key, root.Generation, 2, map[string]string{"checker": "testing"}, "implementer", "checker")

		stepReports(t, pool, engine, root.Key, "checker", "pass", "Checked.", "", 0)

		if roles := rolesOf(noticesOf(t, pool, root.Key, "phase-finished")); len(roles) != 1 || roles[0] != "checker" {
			t.Fatalf("the phase-finished notices name %v, want the checker once", roles)
		}
	})
}

// A completion that arrives after an event moved the issue on to a step of a role the deployment
// defines is a report of a step whose run ended, as before any other step: in `implementer reviewer
// checker`, the approving review moves the task from the reviewer's step 2 to the checker's step 3
// before the reviewer completes, and the reviewer's completion is answered that its step 2 ended
// and Legion runs step 3 now, its handoff record kept on its row, and the task stays at step 3.
func TestALateCompletionBeforeADeploymentStepIsAnsweredAsAnEndedStep(t *testing.T) {
	pool := migratedPool(t)
	engine := returnEngine()
	root := stepRoot(t, pool, phase.Admitted)
	seedStepsPlan(t, pool, root.Key, root.Generation, "implementer", "reviewer", "checker")
	if _, err := pool.Exec(t.Context(), "update plan_steps set type = 'testing' where issue = $1 and task = 'a' and role = 'checker'", root.Key); err != nil {
		t.Fatalf("store the type of checker: %v", err)
	}
	move(t, pool, engine, root.Key, TriggerGateOpened, record.TaskPlace{}, record.PhaseRow{})
	move(t, pool, engine, root.Key, TriggerImplementationReady, record.TaskPlace{}, record.PhaseRow{})
	reviewer := seedPhaseWorker(t, pool, root.Key, claim.RoleReviewer, "")
	move(t, pool, engine, root.Key, TriggerReviewApproved, record.TaskPlace{}, record.PhaseRow{})
	assertWalksAt(t, pool, engine, root.Key, phase.Step, stepPlace(3))

	result := applyFacts(t, pool, engine)("reviewer-late", intake.HandoffComplete{Generation: 1, Issue: root.Key, Role: claim.RoleReviewer,
		Claim: reviewer, Summary: "Approved.", Commit: stepHead, HandoffRecord: "review-1", HandoffVersion: 1})

	const ended = "step 2 (reviewer) of task a of LEGION-208 has ended, and Legion runs step 3 (checker) now."
	if result.Refusal == nil || result.Refusal.Code != "STEP_ENDED" || !strings.HasPrefix(result.Refusal.Message, ended) {
		t.Fatalf("the reviewer's late completion = %+v, want STEP_ENDED beginning %q", result.Refusal, ended)
	}
	if row := roleRowOf(t, pool, engine, root.Key, claim.RoleReviewer); row.LastHandoff != "review-1" {
		t.Fatalf("the reviewer's row keeps the handoff record %q, want review-1", row.LastHandoff)
	}
	assertWalksAt(t, pool, engine, root.Key, phase.Step, stepPlace(3))
}
