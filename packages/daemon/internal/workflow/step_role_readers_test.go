package workflow

import (
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/pods"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// deploymentChecker is a role the deployment defines: no phase of Legion's is its, so a step of
// it runs as the phase step, and the plan rows name it as the issue's running role (StepRole).
const deploymentChecker claim.Role = "checker"

// standAtStep writes issue standing at the phase step, or held from it (a failed claim's hold,
// which keeps the phase it left), with the plan `implementer checker` of task a running at step
// 2, so the running role read with the issue is the checker. It answers the issue as the store
// reads it.
func standAtStep(t *testing.T, pool *pgxpool.Pool, issue record.Issue, held bool) record.Issue {
	t.Helper()
	issue.Phase, issue.Hold = phase.Step, nil
	if held {
		issue.Phase, issue.Hold = phase.Held, &record.Hold{From: phase.Step}
	}
	seedIssue(t, pool, issue)
	seedStepsPlan(t, pool, issue.Key, issue.Generation, "implementer", string(deploymentChecker))
	seedRecord(t, pool, func(tx pgx.Tx) error {
		return record.NewStore().MoveTask(t.Context(), tx, issue.Key, issue.Generation, "a",
			record.TaskPlace{Kind: record.PlaceStep, Position: 2}, record.TaskRunning)
	})
	issue.StepRole = deploymentChecker
	return issue
}

// An issue whose running step is of a role the deployment defines stands at step, which no role
// of Legion's works, so each path that brings a worker back to its work reads the role with the
// issue (OpenRoleOf, Issue.RoleAt): the ready of a claim (readyWorkerTask), the boot's resume by
// rule (booted, through suspendedPhaseOwners), and the CI settlement's wake of a released pod
// (wakeOnSettlement) each give the checker its work. Held from step, the issue stands in held,
// which no role works, so none of them gives the checker anything. Stranded reads the phase the
// hold took the issue from, so it names a failed checker on a held issue as on one at step: only
// its architect's retry starts it again.
func TestTheResumePathsGiveTheRunningDeploymentRoleItsWork(t *testing.T) {
	for _, held := range []bool{false, true} {
		name := "at step"
		if held {
			name = "held from step"
		}
		t.Run(name, func(t *testing.T) {
			t.Run("the ready of a claim", func(t *testing.T) {
				pool := migratedPool(t)
				const key = "LEGION-209"
				standAtStep(t, pool, record.Issue{Key: key, Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets",
					Tree: key, Project: "LEGION", Title: "a root", Generation: 1, Status: "in_progress", Rank: "U", HandedOver: true}, held)
				seedSlot(t, pool, record.Slot{Issue: key, Index: 0})
				seedPhaseWorker(t, pool, key, deploymentChecker, "")

				if _, err := intake.ApplyFact(t.Context(), pool, "supervise", "supervise:ready", intake.ClaimReady{Issue: key, Role: deploymentChecker, Launch: 2},
					testEngine(config.DesignGateRootIssues, nil), admissionStub{}); err != nil {
					t.Fatalf("apply the ready: %v", err)
				}
				want := map[string]int{key + "/" + string(deploymentChecker): 1}
				if held {
					want = map[string]int{}
				}
				if got := superviseRequests(t, pool, "start"); !sameCounts(got, want) || !sameCounts(want, got) {
					t.Fatalf("the ready of the checker queued starts %v, want %v", got, want)
				}
			})

			t.Run("the boot's resume by rule", func(t *testing.T) {
				pool := migratedPool(t)
				project, err := claim.ProjectToken("LEGION")
				if err != nil {
					t.Fatal(err)
				}
				const key = "LEGION-212"
				standAtStep(t, pool, record.Issue{Key: key, Repo: "sjawhar/legion", DefaultBranch: "main", GitHubRepo: "sjawhar/legion",
					Tree: key, Project: "LEGION", Title: "a root", Generation: 1, Status: "in_progress", Rank: "U"}, held)
				seedTreeLifecycle(t, pool, key)
				seedRuleSuspendedOwner(t, pool, project, key, deploymentChecker)
				engine := testEngine(config.DesignGateRootIssues, nil)

				if _, err := intake.ApplyFact(t.Context(), pool, "daemon", "boot:LEGION:1", intake.DaemonBooted{Boot: 1}, engine, admissionStub{}); err != nil {
					t.Fatalf("apply the boot: %v", err)
				}
				rows := oneStartRowsOf(t, pool, key, engine.now())
				if held && len(rows) != 0 {
					t.Fatalf("the start rows of %s after the boot = %+v, want none", key, rows)
				}
				if !held && (len(rows) != 1 || rows[0].role != deploymentChecker || rows[0].task == "") {
					t.Fatalf("the start rows of %s after the boot = %+v, want the one resume of its checker carrying its task", key, rows)
				}
			})

			t.Run("the CI settlement's wake of a released pod", func(t *testing.T) {
				rig := newWakeRig(t)
				standAtStep(t, rig.pool, record.Issue{Key: "LEGION-208", Repo: "sjawhar/legion", DefaultBranch: "main", GitHubRepo: "sjawhar/legion",
					Tree: "LEGION-208", Project: "LEGION", Title: "root", Generation: wakeIssueGeneration, Status: "in_progress", Rank: "U", LastDispatchSeq: 1}, held)
				seedReleasedClaim(t, rig.pool, deploymentChecker, "session-checker")

				rig.apply("settled", settlement("head1", "build"))
				if !held {
					oneWake(t, rig.pool, deploymentChecker, pods.WakeChecks, "head1", "build")
					return
				}
				if starts, resumes := wakeStarts(t, rig.pool), wakeResumes(t, rig.pool); len(starts) != 0 || len(resumes) != 0 {
					t.Fatalf("the settlement wrote starts %+v and resumes %v, want no wake", starts, resumes)
				}
			})
		})
	}

	t.Run("Stranded", func(t *testing.T) {
		atStep := strandedIssue("LEGION-30", "LEGION-30", phase.Step)
		atStep.StepRole = deploymentChecker
		heldFromStep := strandedIssue("LEGION-31", "LEGION-31", phase.Held)
		heldFromStep.Hold, heldFromStep.StepRole = &record.Hold{From: phase.Step}, deploymentChecker
		failedAtStep := strandedClaim(t, "LEGION-30", "LEGION-30", deploymentChecker, supervise.StateFailed)
		failedHeld := strandedClaim(t, "LEGION-31", "LEGION-31", deploymentChecker, supervise.StateFailed)
		claims := []supervise.Claim{failedAtStep, failedHeld}
		stored := map[claim.Token]supervise.Claim{}
		for _, c := range claims {
			stored[c.Token] = c
		}
		s := &strandedStore{issues: []record.Issue{atStep, heldFromStep}, slots: slotsOf("LEGION-30", "LEGION-31"), claims: stored}

		got, err := Stranded(t.Context(), nil, s, "LEGION", nil, claims, nil, strandedNow)
		if err != nil {
			t.Fatalf("Stranded: %v", err)
		}
		if want := claims; !reflect.DeepEqual(got, want) {
			t.Fatalf("stranded = %v, want the failed checker of the issue at step and of the issue held from it, %v", names(got), names(want))
		}
	})
}
