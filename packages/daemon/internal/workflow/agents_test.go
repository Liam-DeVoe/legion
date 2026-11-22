package workflow

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/plan"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// agentsOfTask is the name of the agent of each stored step of task, in place order.
func agentsOfTask(stored *record.StoredPlan, task string) []claim.Agent {
	var names []claim.Agent
	for _, step := range stored.Steps[task] {
		names = append(names, step.Agent)
	}
	return names
}

// A later version in which a step with no key would need the name its role gives while a key holds
// that name from an earlier version is a plan this release cannot carry out: it runs one agent of
// each role for an issue. The version is refused as a plan, 409 PLAN_REFUSED with R14's ending,
// before anything is written, and the fact stays unspent; version 1's rows, with their names,
// stand.
func TestALaterVersionThatWouldRenameAKeyIsRefusedAsAPlan(t *testing.T) {
	pool := migratedPool(t)
	engine := returnEngine()
	apply := applyFacts(t, pool, engine)
	root := stepRoot(t, pool, phase.Admitted)
	key, gen := root.Key, root.Generation
	mustTake(t, "version 1", apply(planID(key, gen, 1), ownersPlan(t, key, gen, 1, givenTask(t, "Build the widget.", "implementer:a", "tester:a", "reviewer"))))
	move(t, pool, engine, key, TriggerGateOpened, record.TaskPlace{}, record.PhaseRow{})
	if result := apply("stop", ownersStop(t, root, "a")); result.Refusal != nil {
		t.Fatalf("the owner's stop was refused: %+v", result.Refusal)
	}
	before := planStandingOf(t, pool, engine, key, gen)
	if got := agentsOfTask(before.plan, "a"); !slices.Equal(got, []claim.Agent{"implementer", "implementer", "reviewer"}) {
		t.Fatalf("the agents of version 1 = %q, want key a named implementer at steps 1 and 2, and reviewer", got)
	}

	id := planID(key, gen, 2)
	reason := "step 1 of task a needs an agent of its own, and another agent of this plan holds the name implementer of its role from an earlier version; " +
		"this release runs one agent of each role for an issue. This release of Legion does not carry it out yet."
	want := &intake.Refusal{Status: 409, Code: "PLAN_REFUSED", Message: "Legion refused this plan and recorded nothing: " + reason,
		Reasons: []plan.Refusal{{Task: "a", Reason: reason}}}
	assertPlanRefusal(t, pool, engine, key, gen, id,
		apply(id, ownersPlan(t, key, gen, 2, givenTask(t, "Build the widget.", "implementer", "tester:a", "reviewer"))), want, before)
}

// A version that gives a task again names its keys from the rows read before it replaces the
// task's steps, so a key keeps the name it had, even where its first step is now of another role.
func TestARegivenTaskKeepsTheNamesOfItsKeys(t *testing.T) {
	pool := migratedPool(t)
	engine := returnEngine()
	apply := applyFacts(t, pool, engine)
	root := stepRoot(t, pool, phase.Admitted)
	key, gen := root.Key, root.Generation
	mustTake(t, "version 1", apply(planID(key, gen, 1), ownersPlan(t, key, gen, 1, givenTask(t, "Build the widget.", "implementer", "tester:a", "reviewer"))))
	if got := agentsOfTask(planStandingOf(t, pool, engine, key, gen).plan, "a"); !slices.Equal(got, []claim.Agent{"implementer", "tester", "reviewer"}) {
		t.Fatalf("the agents of version 1 = %q, want implementer, key a named tester, and reviewer", got)
	}
	mustTake(t, "version 2", apply(planID(key, gen, 2), ownersPlan(t, key, gen, 2, givenTask(t, "Build the widget, and its manual.", "implementer:a", "tester:a", "reviewer"))))
	if got := agentsOfTask(planStandingOf(t, pool, engine, key, gen).plan, "a"); !slices.Equal(got, []claim.Agent{"tester", "tester", "reviewer"}) {
		t.Fatalf("the agents of version 2 = %q, want key a still named tester at steps 1 and 2, and reviewer", got)
	}
}

// The project's default steps are named from their own steps alone when the first step of a run
// with no owner's plan writes them: each agent has a name, the steps of a key share it.
func TestTheDefaultStepsAreNamedWhenTheyAreWritten(t *testing.T) {
	engine := returnEngine()
	engine.cfg.DefaultSteps = []plan.Step{{Role: "implementer"}, {Role: "tester", Key: "r"}, {Role: "reviewer", Key: "r"}}
	_, _, steps := engine.defaultRows(record.Issue{Key: "LEGION-208", Generation: 1})
	var names []claim.Agent
	for _, step := range steps {
		names = append(names, step.Agent)
	}
	if want := []claim.Agent{"implementer", "tester", "tester"}; !slices.Equal(names, want) {
		t.Fatalf("the default steps are named %q, want %q", names, want)
	}
}

// tickingClock gives engine a clock that moves a second at each read, as a real clock moves
// between two transactions: a role that stands at two steps has a row for each, and its row now is
// the one whose entry was raised last (record.PhaseRowOf), which a clock that never moves cannot
// tell apart.
func tickingClock(engine *Engine) {
	now := stepClock
	engine.cfg.Clock = func() time.Time {
		now = now.Add(time.Second)
		return now
	}
}

// rolesRows is every phases row of key's role, by place.
func rolesRows(t *testing.T, pool *pgxpool.Pool, engine *Engine, key string, role claim.Role) map[int]record.PhaseRow {
	t.Helper()
	rows := map[int]record.PhaseRow{}
	applyInTx(t, pool, engine, func(ctx context.Context, tx pgx.Tx) error {
		all, err := engine.store.Phases(ctx, tx, key)
		for _, row := range all {
			if row.Role == role {
				rows[row.StepPlace] = row
			}
		}
		return err
	})
	return rows
}

// roleRowNow is role's row of key now, the one whose entry was raised last (record.PhaseRowOf),
// read with no walk: where an entry rose since, it is the row that rose.
func roleRowNow(t *testing.T, pool *pgxpool.Pool, engine *Engine, key string, role claim.Role) record.PhaseRow {
	t.Helper()
	var row record.PhaseRow
	applyInTx(t, pool, engine, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := engine.store.Phases(ctx, tx, key)
		row = record.PhaseRowOf(rows, role)
		return err
	})
	return row
}

// A role's row of no step, the row a new generation cleared, is the row its first step's entry
// raises: the role keeps one row, which carries the record it last named across the generation as
// it did, and no row of no step stands beside the step's. Before, the entry wrote a second row at
// the step and the cleared row stood on beside it, read by any reader of the role's first row.
func TestARowOfNoStepBecomesTheRowOfTheRolesStep(t *testing.T) {
	pool := migratedPool(t)
	engine := textEngine()
	tickingClock(engine)
	root := stepRoot(t, pool, phase.Admitted)
	seedPhase(t, pool, record.PhaseRow{Issue: root.Key, Role: claim.RoleImplementer, Claim: agentToken(t, "implementer"), Entry: 4,
		LastHandoff: "implement-of-the-generation-before", EntryRaisedAt: stepClock})
	seedStepsPlan(t, pool, root.Key, root.Generation, "implementer", "reviewer")
	move(t, pool, engine, root.Key, TriggerGateOpened, record.TaskPlace{}, record.PhaseRow{})
	rows := rolesRows(t, pool, engine, root.Key, claim.RoleImplementer)
	if row, ok := rows[1]; len(rows) != 1 || !ok || row.StepTask != "a" || row.Entry != 5 || row.LastHandoff != "implement-of-the-generation-before" {
		t.Fatalf("the implementer's rows = %+v, want one, at step 1 of task a, raised to entry 5, keeping the record it last named", rows)
	}
}

// agentToken is the claim token of agent on LEGION-208.
func agentToken(t *testing.T, agent claim.Agent) claim.Token {
	t.Helper()
	token, err := claim.AgentToken("legion", "LEGION-208", agent)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

// A role stands at two steps of `implementer:a reviewer implementer:a`, and each step's run keeps a
// row of its own, so the task of step 3 names the report of step 1 and of step 2, each from the row
// of its place, and step 3 runs in step 1's claim. A fourth step reads the report of each of the
// implementer's two steps once, under its own place. Before, the implementer's one row was step
// 1's until step 3's start cleared its report, so step 3 named no report of step 1, and a match of
// the rows by role names each implementer's report under both of its places.
func TestTheThirdStepsTaskNamesTheReportsOfStepsOneAndTwo(t *testing.T) {
	for _, tc := range []struct {
		name  string
		steps []string
	}{
		{"implementer:a reviewer implementer:a", []string{"implementer:a", "reviewer", "implementer:a"}},
		{"implementer:a reviewer implementer:a tester", []string{"implementer:a", "reviewer", "implementer:a", "tester"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := migratedPool(t)
			engine := textEngine()
			tickingClock(engine)
			root := stepRoot(t, pool, phase.Admitted)
			seedStepsPlan(t, pool, root.Key, root.Generation, tc.steps...)
			move(t, pool, engine, root.Key, TriggerGateOpened, record.TaskPlace{}, record.PhaseRow{})
			completesAs(t, pool, engine, claim.RoleImplementer, agentToken(t, "implementer"), "implement", 1)
			completesAs(t, pool, engine, claim.RoleReviewer, agentToken(t, "reviewer"), "review", 1)
			move(t, pool, engine, root.Key, TriggerReviewApproved, record.TaskPlace{}, record.PhaseRow{})
			assertWalksAt(t, pool, engine, root.Key, phase.Implementing, stepPlace(3))

			third := lastStart(t, pool, root.Key, claim.RoleImplementer)
			for _, says := range []string{" Step 3 of " + string(rune('0'+len(tc.steps))) + ": implementer:a.",
				" Step 1 (implementer:a) reported: implement version 1.", " Step 2 (reviewer) reported: review version 1."} {
				if !strings.Contains(third, says) {
					t.Fatalf("step 3's task = %q, want it to say %q", third, says)
				}
			}
			implementer := rolesRows(t, pool, engine, root.Key, claim.RoleImplementer)
			if first, again := implementer[1], implementer[3]; first.Claim != agentToken(t, "implementer") || again.Claim != first.Claim {
				t.Fatalf("the implementer's rows name the claims %q at step 1 and %q at step 3, want both %q: one key is one agent",
					first.Claim, again.Claim, agentToken(t, "implementer"))
			}
			if len(tc.steps) < 4 {
				return
			}
			completesAs(t, pool, engine, claim.RoleImplementer, agentToken(t, "implementer"), "implement", 2)
			fourth := lastStart(t, pool, root.Key, claim.RoleTester)
			want := " Step 1 (implementer:a) reported: implement version 1. Step 2 (reviewer) reported: review version 1." +
				" Step 3 (implementer:a) reported: implement version 2."
			if !strings.Contains(fourth, want) || strings.Count(fourth, "reported: implement") != 2 {
				t.Fatalf("step 4's task = %q, want each step's report once: %q", fourth, want)
			}
		})
	}
}

// While step 2 of `implementer:a reviewer implementer:a` runs, the engine's row of the implementer
// is the row of step 1, the implementer's latest step before it, which holds the report step 2
// reads; while step 3 runs it is the row of step 3. The reviewer's row read at step 3 is step 2's.
func TestAReviewerReadsTheLatestImplementersRowBeforeIt(t *testing.T) {
	pool := migratedPool(t)
	engine := textEngine()
	tickingClock(engine)
	root := stepRoot(t, pool, phase.Admitted)
	seedStepsPlan(t, pool, root.Key, root.Generation, "implementer:a", "reviewer", "implementer:a")
	move(t, pool, engine, root.Key, TriggerGateOpened, record.TaskPlace{}, record.PhaseRow{})
	completes(t, pool, engine, claim.RoleImplementer, "implement", 1)
	if row := roleRowOf(t, pool, engine, root.Key, claim.RoleImplementer); row.StepPlace != 1 || row.HandoffRecord != "implement-1" {
		t.Fatalf("the implementer's row while step 2 runs = %+v, want step 1's, with its report", row)
	}
	completes(t, pool, engine, claim.RoleReviewer, "review", 1)
	move(t, pool, engine, root.Key, TriggerReviewApproved, record.TaskPlace{}, record.PhaseRow{})
	first := rolesRows(t, pool, engine, root.Key, claim.RoleImplementer)[1]
	if row := roleRowOf(t, pool, engine, root.Key, claim.RoleImplementer); row.StepPlace != 3 || row.HandoffRecord != "" || row.Entry <= first.Entry {
		t.Fatalf("the implementer's row while step 3 runs = %+v, want step 3's, raised above step 1's entry %d with no report yet", row, first.Entry)
	}
	if first.HandoffRecord != "implement-1" {
		t.Fatalf("step 1's row after step 3 started = %+v, want its report kept", first)
	}
	if row := roleRowOf(t, pool, engine, root.Key, claim.RoleReviewer); row.StepPlace != 2 || row.HandoffRecord != "review-1" {
		t.Fatalf("the reviewer's row while step 3 runs = %+v, want step 2's", row)
	}
}

// A role written before its first step is written on the row of that step, as the one row of a
// role was: a reviewer's row read and written back while the task stands at the implementer's step
// is the row of the reviewer's step, which the reviewer's start then raises, so the role keeps one
// row and what was written stands on it.
func TestAWriteBeforeARolesFirstStepLandsOnThatStepsRow(t *testing.T) {
	pool := migratedPool(t)
	engine := textEngine()
	tickingClock(engine)
	root := stepRoot(t, pool, phase.Admitted)
	seedStepsPlan(t, pool, root.Key, root.Generation, "implementer", "reviewer")
	move(t, pool, engine, root.Key, TriggerGateOpened, record.TaskPlace{}, record.PhaseRow{})
	applyInTx(t, pool, engine, func(ctx context.Context, tx pgx.Tx) error {
		row, err := engine.phaseRow(ctx, tx, root.Key, claim.RoleReviewer)
		if err != nil {
			return err
		}
		row.ReviewedHead = "head-read-before"
		return engine.store.PutPhase(ctx, tx, row)
	})
	completes(t, pool, engine, claim.RoleImplementer, "implement", 1)
	rows := rolesRows(t, pool, engine, root.Key, claim.RoleReviewer)
	if row, ok := rows[2]; len(rows) != 1 || !ok || row.ReviewedHead != "head-read-before" || row.Entry == 0 {
		t.Fatalf("the reviewer's rows = %+v, want one, at step 2, raised, with the commit written before its step", rows)
	}
}

// A role that stands at two steps runs its phase at two places, and a move back to that phase
// reaches the nearest of them before the task's place, as a failed step's work returns to the
// nearest step of the type implementation: in `implementer:a reviewer implementer:a tester`, the
// tester's move back to implementing reaches step 3, the implementer's step the tester tested.
// Before, a phase that ran at two earlier places served no backward row, and the move was refused
// as one no row serves.
func TestAMoveBackToAPhaseThatRunsTwiceReachesTheNearest(t *testing.T) {
	pool := migratedPool(t)
	engine := textEngine()
	tickingClock(engine)
	root := stepRoot(t, pool, phase.Admitted)
	seedStepsPlan(t, pool, root.Key, root.Generation, "implementer:a", "reviewer", "implementer:a", "tester")
	implementer := agentToken(t, "implementer")
	move(t, pool, engine, root.Key, TriggerGateOpened, record.TaskPlace{}, record.PhaseRow{})
	completesAs(t, pool, engine, claim.RoleImplementer, implementer, "implement", 1)
	completesAs(t, pool, engine, claim.RoleReviewer, agentToken(t, "reviewer"), "review", 1)
	move(t, pool, engine, root.Key, TriggerReviewApproved, record.TaskPlace{}, record.PhaseRow{})
	completesAs(t, pool, engine, claim.RoleImplementer, implementer, "implement", 2)
	assertWalksAt(t, pool, engine, root.Key, phase.Testing, stepPlace(4))
	if result := applyFacts(t, pool, engine)("tester-back", intake.BackwardMove{Issue: root.Key, Requester: claim.RoleTester, To: phase.Implementing,
		Reason: "the harness needs a fixture"}); result.Refusal != nil {
		t.Fatalf("the tester's move back to implementing was refused: %+v", result.Refusal)
	}
	assertWalksAt(t, pool, engine, root.Key, phase.Implementing, stepPlace(3))
}

// The claim of a role's worker read with no walk (record.WorkerClaim) is the claim of the row the
// engine reads where the task stands (record.PhaseRowAt): in `implementer:a tester:b implementer:b
// reviewer`, where key a is named implementer and key b tester, the implementer's worker is key a
// at places 1 and 2, key b at places 3 and 4, and key a again once a failed review returns the task
// to step 1. Before, the first row of the role answered, key a at every place.
func TestWorkerClaimPicksTheRowThatPhaseRowAtGives(t *testing.T) {
	pool := migratedPool(t)
	engine := textEngine()
	tickingClock(engine)
	root := stepRoot(t, pool, phase.Admitted)
	seedStepsPlan(t, pool, root.Key, root.Generation, "implementer:a", "tester:b", "implementer:b", "reviewer")
	keyA, keyB := agentToken(t, "implementer"), agentToken(t, "tester")
	worker := func(at record.TaskPlace, want claim.Token) {
		t.Helper()
		assertWalksAt(t, pool, engine, root.Key, issueOf(t, pool, engine, root.Key).Phase, at)
		applyInTx(t, pool, engine, func(ctx context.Context, tx pgx.Tx) error {
			rows, err := engine.store.Phases(ctx, tx, root.Key)
			if err != nil {
				return err
			}
			row, err := engine.phaseRow(ctx, tx, root.Key, claim.RoleImplementer)
			if err != nil {
				return err
			}
			got, err := record.WorkerClaim("legion", root.Key, claim.RoleImplementer, rows)
			if err != nil {
				return err
			}
			if got != want || row.Claim != want {
				t.Errorf("at %+v the implementer's worker = %q and the engine's row names %q, want %q", at, got, row.Claim, want)
			}
			return nil
		})
	}
	move(t, pool, engine, root.Key, TriggerGateOpened, record.TaskPlace{}, record.PhaseRow{})
	worker(stepPlace(1), keyA)
	completesAs(t, pool, engine, claim.RoleImplementer, keyA, "implement", 1)
	worker(stepPlace(2), keyA)
	completesAs(t, pool, engine, claim.RoleTester, keyB, "test", 1)
	worker(stepPlace(3), keyB)
	completesAs(t, pool, engine, claim.RoleImplementer, keyB, "implement", 2)
	worker(stepPlace(4), keyB)
	move(t, pool, engine, root.Key, TriggerReviewRejected, stepPlace(1), record.PhaseRow{})
	worker(stepPlace(1), keyA)
}

// Two agents of one role keep a row of it each, and the entry a step's start raises is above every
// row of its role: in `implementer:a tester:b implementer:b reviewer`, the failed review returns
// the task to step 1, whose row rises above step 3's, so key b's completion at the entry it was
// handed for step 3 is behind step 1's and refused HANDOFF_STALE_ENTRY, and step 1's row keeps key
// a and no report. Before, the entry rose only above the rows of its own agent, so step 1's row and
// step 3's stood on one entry, and key b's completion passed the fence and was recorded as step 1's,
// its claim and its record on that row.
func TestAStepsEntryRisesAboveEveryRowOfItsRole(t *testing.T) {
	pool := migratedPool(t)
	engine := textEngine()
	tickingClock(engine)
	root := stepRoot(t, pool, phase.Admitted)
	seedStepsPlan(t, pool, root.Key, root.Generation, "implementer:a", "tester:b", "implementer:b", "reviewer")
	keyA, keyB := agentToken(t, "implementer"), agentToken(t, "tester")
	move(t, pool, engine, root.Key, TriggerGateOpened, record.TaskPlace{}, record.PhaseRow{})
	completesAs(t, pool, engine, claim.RoleImplementer, keyA, "implement", 1)
	completesAs(t, pool, engine, claim.RoleTester, keyB, "test", 1)
	completesAs(t, pool, engine, claim.RoleImplementer, keyB, "implement", 2)
	served := rolesRows(t, pool, engine, root.Key, claim.RoleImplementer)[3].Entry
	move(t, pool, engine, root.Key, TriggerReviewRejected, stepPlace(1), record.PhaseRow{})
	result := applyFacts(t, pool, engine)("implementer-late-key-b", intake.HandoffComplete{Generation: 1, Issue: root.Key, Role: claim.RoleImplementer,
		Claim: keyB, Entry: served, Summary: "done", Commit: "head", HandoffRecord: "implement-3", HandoffVersion: 3})
	rows := rolesRows(t, pool, engine, root.Key, claim.RoleImplementer)
	if result.Refusal == nil || result.Refusal.Code != intake.StaleEntryCode {
		t.Fatalf("key b's completion at entry %d after the return to step 1 = %+v, want %s; step 1's row names %q with the record %q at entry %d",
			served, result.Refusal, intake.StaleEntryCode, rows[1].Claim, rows[1].HandoffRecord, rows[1].Entry)
	}
	if first, third := rows[1], rows[3]; first.Claim != keyA || first.HandoffRecord != "" || first.Entry <= third.Entry {
		t.Fatalf("step 1's row after the refusal names %q with the record %q at entry %d, want %q with none, above step 3's entry %d",
			first.Claim, first.HandoffRecord, first.Entry, keyA, third.Entry)
	}
}

// A completion counts only for the step its turn was given. In `implementer:a reviewer
// implementer:a`, the reviewer's approval enters step 3 by a forward move at the head step 1 left,
// and key a's turn of step 1, still open on step 1's entry, completes at the pull request's head
// with a record of its own. Its entry is behind step 3's and is step 1's, another step of the role,
// so it is refused HANDOFF_STALE_ENTRY, step 3's row records nothing and the task stays at step 3.
// Before, a completion behind its row at the head the record holds was taken unless a move back
// raised the row's entry, so the turn of step 1 was recorded as step 3's and moved the task on to
// its retro before step 3's task reached the agent.
func TestATurnOfARolesEarlierStepDoesNotCompleteItsLaterStep(t *testing.T) {
	pool := migratedPool(t)
	engine := textEngine()
	tickingClock(engine)
	root := stepRoot(t, pool, phase.Admitted)
	seedPR(t, pool, record.PullRequest{State: record.PullRequestOpen, Issue: root.Key, Repo: "sjawhar/legion", Number: 42, Branch: "legion/" + root.Key,
		HeadSHA: "head", Failing: []string{}})
	seedStepsPlan(t, pool, root.Key, root.Generation, "implementer:a", "reviewer", "implementer:a")
	implementer := agentToken(t, "implementer")
	move(t, pool, engine, root.Key, TriggerGateOpened, record.TaskPlace{}, record.PhaseRow{})
	served := rolesRows(t, pool, engine, root.Key, claim.RoleImplementer)[1].Entry
	completesAs(t, pool, engine, claim.RoleImplementer, implementer, "implement", 1)
	completesAs(t, pool, engine, claim.RoleReviewer, agentToken(t, "reviewer"), "review", 1)
	move(t, pool, engine, root.Key, TriggerReviewApproved, record.TaskPlace{}, record.PhaseRow{})
	assertWalksAt(t, pool, engine, root.Key, phase.Implementing, stepPlace(3))
	result := applyFacts(t, pool, engine)("implementer-step-1-turn", intake.HandoffComplete{Generation: 1, Issue: root.Key, Role: claim.RoleImplementer,
		Claim: implementer, Entry: served, Summary: "done", Commit: "head", HandoffRecord: "implement-9", HandoffVersion: 9})
	third := rolesRows(t, pool, engine, root.Key, claim.RoleImplementer)[3]
	if result.Refusal == nil || result.Refusal.Code != intake.StaleEntryCode {
		t.Fatalf("the turn of step 1 completing at entry %d and the head after step 3 was entered = %+v, want %s; step 3's row names the record %q",
			served, result.Refusal, intake.StaleEntryCode, third.HandoffRecord)
	}
	if third.HandoffRecord != "" || third.Completed() {
		t.Fatalf("step 3's row after the refusal = %+v, want no report", third)
	}
	assertWalksAt(t, pool, engine, root.Key, phase.Implementing, stepPlace(3))
}

// A keyed agent's next step after a new generation is numbered above every entry its claim was
// given, whatever rows the generation's clear folded away. In `implementer:a reviewer:b
// implementer:b`, key b, named reviewer, works steps 2 and 3 and the retro, the last of them on the
// implementer's row at place 0, so its claim is last given the retro's entry, the role's highest.
// The re-admission folds the implementer's rows into that one, and step 1 of the new generation
// raises it and writes key a's claim on it, so no row names key b above the reviewer's. Step 2's
// start must still carry an entry above the one key b's claim was given: the supervisor reads a
// start at or below that number as given already and queues nothing (supervise.Machine.given), and
// the admission pass reads the claim as level with its row (OwedEntry). Before, step 2 was numbered
// on the rows alone, at the reviewer's entry plus one, below the retro's, and its task never
// reached key b.
func TestAKeyedAgentsStepAfterANewGenerationRisesAboveWhatItsClaimWasGiven(t *testing.T) {
	pool := migratedPool(t)
	engine := textEngine()
	tickingClock(engine)
	root := stepRoot(t, pool, phase.Admitted)
	steps := []string{"implementer:a", "reviewer:b", "implementer:b"}
	seedStepsPlan(t, pool, root.Key, root.Generation, steps...)
	keyA, keyB := agentToken(t, "implementer"), agentToken(t, "reviewer")
	if got := seedPhaseWorker(t, pool, root.Key, claim.RoleReviewer, ""); got != keyB {
		t.Fatalf("the reviewer's claim seeded is %q, want key b's %q", got, keyB)
	}
	move(t, pool, engine, root.Key, TriggerGateOpened, record.TaskPlace{}, record.PhaseRow{})
	completesAs(t, pool, engine, claim.RoleImplementer, keyA, "implement", 1)
	completesAs(t, pool, engine, claim.RoleReviewer, keyB, "review", 1)
	move(t, pool, engine, root.Key, TriggerReviewApproved, record.TaskPlace{}, record.PhaseRow{})
	completesAs(t, pool, engine, claim.RoleImplementer, keyB, "implement", 2)
	if got := issueOf(t, pool, engine, root.Key).Phase; got != phase.Retro {
		t.Fatalf("%s stands in %s after step 3, want its retro", root.Key, got)
	}
	retro := rolesRows(t, pool, engine, root.Key, claim.RoleImplementer)[0]
	if retro.Claim != keyB {
		t.Fatalf("the retro's row names %q, want key b's claim %q", retro.Claim, keyB)
	}
	// The supervisor writes what each start queued to the claim (supervise.Machine.queue); the
	// retro's task is the last key b was queued.
	seedGivenEntry(t, pool, keyB, retro.Entry)

	readmitRoot(t, pool, engine, root.Key)
	next := root.Generation + 1
	seedStepsPlan(t, pool, root.Key, next, steps...)
	move(t, pool, engine, root.Key, TriggerGateOpened, record.TaskPlace{}, record.PhaseRow{})
	if result := applyFacts(t, pool, engine)("implementer-implement-next", intake.HandoffComplete{Generation: next, Issue: root.Key,
		Role: claim.RoleImplementer, Claim: keyA, Summary: "done", Commit: "head", HandoffRecord: "implement-3", HandoffVersion: 3}); result.Refusal != nil {
		t.Fatalf("key a's completion of step 1 of generation %d was refused: %+v", next, result.Refusal)
	}
	assertWalksAt(t, pool, engine, root.Key, phase.Reviewing, stepPlace(2))
	starts := roleStartsOf(t, pool, root.Key, claim.RoleReviewer)
	start := starts[len(starts)-1]
	reviewer := roleRowNow(t, pool, engine, root.Key, claim.RoleReviewer)
	if start.Generation != next || reviewer.Claim != keyB || start.Entry != reviewer.Entry || start.Entry <= retro.Entry {
		t.Fatalf("step 2's start of generation %d carries the entry %d (generation %d) and the reviewer's row names %q at %d, want key b's claim %q "+
			"at an entry above %d, the retro's entry key b's claim was given, which the supervisor reads as given already",
			next, start.Entry, start.Generation, reviewer.Claim, reviewer.Entry, keyB, retro.Entry)
	}
}

// completesAs is completes under the claim token, the agent's own, as the route names the claim
// whose grant reported, which the completion writes on the role's row.
func completesAs(t *testing.T, pool *pgxpool.Pool, engine *Engine, role claim.Role, token claim.Token, word string, n int) {
	t.Helper()
	fact := intake.HandoffComplete{Generation: 1, Issue: "LEGION-208", Role: role, Claim: token,
		Summary: "done", Commit: "head", HandoffRecord: word + "-" + string(rune('0'+n)), HandoffVersion: n}
	if role == claim.RoleTester {
		fact.Verdict = "pass"
	}
	if result := applyFacts(t, pool, engine)(string(role)+"-"+word+"-"+string(rune('0'+n)), fact); result.Refusal != nil {
		t.Fatalf("the %s's completion %s version %d was refused: %+v", role, word, n, result.Refusal)
	}
}
