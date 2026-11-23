package workflow

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// standsThreeTasks is LEGION-209, a root in implementing with its admission slot, holding
// threeTasks' plan: a at the implementer's step, b at the reviewer's, c held at the tester's.
func standsThreeTasks(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	seedIssue(t, pool, record.Issue{Key: silenceIssue, Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets",
		Tree: silenceIssue, Project: "LEGION", Title: "a root", Phase: phase.Implementing, Generation: 1, Status: "in_progress", Rank: "U",
		HandedOver: true})
	seedSlot(t, pool, record.Slot{Issue: silenceIssue, Index: 0, AdmittedAt: silenceClock()})
	threeTasks(t, pool, silenceIssue, 1)
}

// reviewerResumed fails unless the reviewer's newest start of LEGION-209 is the resume task of
// b's step: the phase's resume, composed on the copy read for task b, which names it.
func reviewerResumed(t *testing.T, pool *pgxpool.Pool, door string) {
	t.Helper()
	starts := roleStartsOf(t, pool, silenceIssue, claim.RoleReviewer)
	if len(starts) == 0 {
		t.Fatalf("%s queued no start of the reviewer of %s", door, silenceIssue)
	}
	start := starts[len(starts)-1]
	if start.Phase != phase.Reviewing || !strings.Contains(start.Task, " Task: b. Step 2 of 3: reviewer.") ||
		!strings.Contains(start.Task, "Resume the existing phase work.") {
		t.Fatalf("%s's start of the reviewer of %s = %+v, want the resume of b's step 2 in reviewing", door, silenceIssue, start)
	}
}

// Each door that resumes the worker of one running step of a plan of several tasks composes that
// worker's task on the copy of the issue read for the step's task (StepCopy): of LEGION-209's
// tasks a at the implementer's step, b at the reviewer's and c held at the tester's, the
// reviewer, level with the entry its phases row names, is sent the resume of b's step 2 by the
// boot's resume of its claim the daemon suspended (booted), by the ready of its claim
// (readyWorkerTask) and by the repair of its silence (workerSilent). Each door read the issue at
// the step's phase alone, which names no task, so the resume's walk of a plan of several tasks
// failed, and with it the whole fact. The start of such a copy names the claim of its step's
// agent, so the boot's resumes of a's implementer and b's reviewer stand side by side, each the
// resume of its own task's step.
//
// The fault it catches is the start of one step composed on a copy of the issue that names no
// task (its control: StepCopy answering the issue at the step's phase, as each door read it
// before, under which each door's fact fails with the walk that names no task), and a resume of
// one task's step that takes out another's (its control: the start of a copy that names no claim,
// under which the boot leaves one of the two resumes).
func TestEachResumeOfAStepIsComposedOnItsTasksCopy(t *testing.T) {
	t.Run("the boot's resume by rule", func(t *testing.T) {
		pool := migratedPool(t)
		standsThreeTasks(t, pool)
		project, err := claim.ProjectToken("LEGION")
		if err != nil {
			t.Fatal(err)
		}
		seedRuleSuspendedOwner(t, pool, project, silenceIssue, claim.RoleReviewer)

		mustSpend(t, "the boot", applyFacts(t, pool, testEngine(config.DesignGateRootIssues, nil))("boot", intake.DaemonBooted{}))

		reviewerResumed(t, pool, "the boot")
	})

	t.Run("the boot's resumes of two steps", func(t *testing.T) {
		pool := migratedPool(t)
		standsThreeTasks(t, pool)
		project, err := claim.ProjectToken("LEGION")
		if err != nil {
			t.Fatal(err)
		}
		seedRuleSuspendedOwner(t, pool, project, silenceIssue, claim.RoleImplementer)
		seedRuleSuspendedOwner(t, pool, project, silenceIssue, claim.RoleReviewer)

		mustSpend(t, "the boot", applyFacts(t, pool, testEngine(config.DesignGateRootIssues, nil))("boot", intake.DaemonBooted{}))

		reviewerResumed(t, pool, "the boot")
		implementer := roleStartsOf(t, pool, silenceIssue, claim.RoleImplementer)
		if len(implementer) != 1 || implementer[0].Phase != phase.Implementing || !strings.Contains(implementer[0].Task, " Task: a. Step 1 of 3: implementer.") {
			t.Fatalf("the boot's starts of the implementer of %s = %+v, want the one resume of a's step 1 beside the reviewer's", silenceIssue, implementer)
		}
	})

	t.Run("the ready of the claim", func(t *testing.T) {
		pool := migratedPool(t)
		standsThreeTasks(t, pool)
		token := seedPhaseWorker(t, pool, silenceIssue, claim.RoleReviewer, "")
		if _, err := pool.Exec(t.Context(), `update claims set generation = 1 where token = $1`, string(token)); err != nil {
			t.Fatalf("put the reviewer's claim at generation 1: %v", err)
		}

		if _, err := intake.ApplyFact(t.Context(), pool, "supervise", "supervise:reviewer:2:ready",
			intake.ClaimReady{Issue: silenceIssue, Role: claim.RoleReviewer, Launch: 2}, testEngine(config.DesignGateRootIssues, nil), admissionStub{}); err != nil {
			t.Fatalf("apply the ready: %v", err)
		}

		reviewerResumed(t, pool, "the ready")
	})

	t.Run("the repair of its silence", func(t *testing.T) {
		pool := migratedPool(t)
		standsThreeTasks(t, pool)
		token := silenceToken(t, claim.RoleReviewer)
		seedPhase(t, pool, record.PhaseRow{Issue: silenceIssue, Role: claim.RoleReviewer, Claim: token,
			Entry: silenceEntry, EntryTask: "Review b.", EntryRaisedAt: silenceStep()})
		if _, err := pool.Exec(t.Context(), `insert into claims (token, project, tree, issue, role, generation, session, session_file,
			state, step_at, given_entry, launch_failures, prompt_failures, prompt_retires, uncertain_streak)
			values ($1, 'legion', $2, $2, 'reviewer', 1, 'ses', '/sessions/ses.jsonl', 'ready', $3, $4, 0, 0, 0, 0)`,
			string(token), silenceIssue, silenceStep(), silenceEntry); err != nil {
			t.Fatalf("seed the reviewer's claim: %v", err)
		}

		if err := applySilence(t, pool, testEngine(config.DesignGateRootIssues, nil), aSilenceOf(claim.RoleReviewer, 1)); err != nil {
			t.Fatalf("apply the silence of the reviewer: %v", err)
		}

		reviewerResumed(t, pool, "the silence repair")
	})
}
