package workflow

import (
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/pods"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// heldWork is one issue a path that resumes work is read against: a task its owner stopped, held
// from testing (record.HoldTaskStopped), a parent whose plan has no acceptance step, held for the
// issues beneath it in place of its retro (record.HoldForChildren), and the twin of each, an issue
// with no plan rows standing in the phase that hold is from, which each path resumes as today
// (R20). The twins are the control: a path that gave no issue work would pass the held ones too.
type heldWork struct {
	name   string
	from   phase.Phase
	reason record.HoldReason
}

var heldWorks = []heldWork{
	{name: "a stopped task", from: phase.Testing, reason: record.HoldTaskStopped},
	{name: "a parent held for its children", from: phase.Retro, reason: record.HoldForChildren},
	{name: "an issue with no plan rows in testing, as today", from: phase.Testing},
	{name: "an issue with no plan rows in retro, as today", from: phase.Retro},
}

func (h heldWork) held() bool { return h.reason != "" }

// role is the role whose work the hold keeps from the issue, the role of its phase for a twin.
func (h heldWork) role() claim.Role { return phase.RoleFor(h.from) }

// stand writes issue as h stands: a twin in h.from with no plan rows; a held issue with the plan
// `implementer tester` of task a, its task stopped at step 2 with why (StopTask) or running at its
// retro, which the hold stands in place of.
func (h heldWork) stand(t *testing.T, pool *pgxpool.Pool, issue record.Issue) record.Issue {
	t.Helper()
	issue.Phase, issue.Hold = h.from, nil
	if !h.held() {
		seedIssue(t, pool, issue)
		return issue
	}
	issue.Phase, issue.Hold = phase.Held, &record.Hold{From: h.from, Reason: h.reason}
	seedIssue(t, pool, issue)
	seedStepsPlan(t, pool, issue.Key, issue.Generation, "implementer", "tester")
	seedRecord(t, pool, func(tx pgx.Tx) error {
		store := record.NewStore()
		if h.reason == record.HoldForChildren {
			return store.MoveTask(t.Context(), tx, issue.Key, issue.Generation, "a", record.TaskPlace{Kind: record.PlaceRetro}, record.TaskRunning)
		}
		if err := store.MoveTask(t.Context(), tx, issue.Key, issue.Generation, "a", record.TaskPlace{Kind: record.PlaceStep, Position: 2}, record.TaskRunning); err != nil {
			return err
		}
		return store.StopTask(t.Context(), tx, issue.Key, issue.Generation, "a", "task a of "+issue.Key+" was stopped by its owner")
	})
	return issue
}

// A stopped task and a parent held for its children stand in held (C2-1), and each path that
// brings an issue back to work picks the role it gives work to by the role of the issue's phase,
// which held has none of (phase.RoleFor, OpenPhaseRole). So none of these paths gives either one
// work: the ready of a claim (readyWorkerTask), the boot's resume by rule (booted, through
// suspendedPhaseOwners), the silence repair (workerSilent, through phaseWorkerRecord), and the CI
// settlement's wake of a released pod (wakeOnSettlement). The fifth path, a re-admission's
// promotion, is admission's (admit's TestAStoppedTaskAndAHeldParentGetNoWorkFromAPromotion). Each
// path's twins, issues with no plan rows in the same phases, are resumed as today.
func TestAStoppedTaskAndAHeldParentGetNoWork(t *testing.T) {
	t.Run("the ready of a claim", func(t *testing.T) {
		for _, h := range heldWorks {
			t.Run(h.name, func(t *testing.T) {
				pool := migratedPool(t)
				const key = "LEGION-209"
				h.stand(t, pool, record.Issue{Key: key, Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets",
					Tree: key, Project: "LEGION", Title: "a root", Generation: 1, Status: "in_progress", Rank: "U", HandedOver: true})
				seedSlot(t, pool, record.Slot{Issue: key, Index: 0})
				seedPhaseWorker(t, pool, key, h.role(), "")

				if _, err := intake.ApplyFact(t.Context(), pool, "supervise", "supervise:ready", intake.ClaimReady{Issue: key, Role: h.role(), Launch: 2},
					testEngine(config.DesignGateRootIssues, nil), admissionStub{}); err != nil {
					t.Fatalf("apply the ready: %v", err)
				}
				want := map[string]int{key + "/" + string(h.role()): 1}
				if h.held() {
					want = map[string]int{}
				}
				if got := superviseRequests(t, pool, "start"); !sameCounts(got, want) || !sameCounts(want, got) {
					t.Fatalf("the ready of the %s queued starts %v, want %v", h.role(), got, want)
				}
			})
		}
	})

	t.Run("the boot's resume by rule", func(t *testing.T) {
		for _, h := range heldWorks {
			t.Run(h.name, func(t *testing.T) {
				pool := migratedPool(t)
				project, err := claim.ProjectToken("LEGION")
				if err != nil {
					t.Fatal(err)
				}
				const key = "LEGION-212"
				h.stand(t, pool, record.Issue{Key: key, Repo: "sjawhar/legion", DefaultBranch: "main", GitHubRepo: "sjawhar/legion",
					Tree: key, Project: "LEGION", Title: "a root", Generation: 1, Status: "in_progress", Rank: "U"})
				seedTreeLifecycle(t, pool, key)
				seedRuleSuspendedOwner(t, pool, project, key, h.role())
				engine := testEngine(config.DesignGateRootIssues, nil)

				if _, err := intake.ApplyFact(t.Context(), pool, "daemon", "boot:LEGION:1", intake.DaemonBooted{Boot: 1}, engine, admissionStub{}); err != nil {
					t.Fatalf("apply the boot: %v", err)
				}
				rows := oneStartRowsOf(t, pool, key, engine.now())
				if h.held() && len(rows) != 0 {
					t.Fatalf("the start rows of %s after the boot = %+v, want none", key, rows)
				}
				if !h.held() && (len(rows) != 1 || rows[0].role != h.role() || rows[0].task == "") {
					t.Fatalf("the start rows of %s after the boot = %+v, want the one resume of its %s carrying its task", key, rows, h.role())
				}
			})
		}
	})

	t.Run("the silence repair", func(t *testing.T) {
		for _, h := range heldWorks {
			t.Run(h.name, func(t *testing.T) {
				pool := migratedPool(t)
				issue := h.stand(t, pool, record.Issue{Key: silenceIssue, Repo: "sjawhar/legion", DefaultBranch: "main", GitHubRepo: "sjawhar/legion",
					Tree: silenceIssue, Project: "LEGION", Title: "a worker that stopped taking turns", Generation: 1, Status: "in_progress", Rank: "U"})
				seedSilentWorker(t, pool, issue, h.role())

				if err := applySilence(t, pool, testEngine(config.DesignGateRootIssues, nil), aSilenceOf(h.role(), 1)); err != nil {
					t.Fatalf("apply the silence: %v", err)
				}
				rows, notices := silenceSuperviseRows(t, pool), noticesOfKind(t, pool, silenceIssue, workerSilentNotice)
				if h.held() && (len(rows) != 0 || len(notices) != 0) {
					t.Fatalf("after the silence of its %s, %s has supervise rows %+v and %d %s notices, want none", h.role(), silenceIssue, rows, len(notices), workerSilentNotice)
				}
				if !h.held() && (len(rows) != 1 || rows[0].op != "start" || rows[0].role != h.role() || rows[0].task == "" || len(notices) != 1) {
					t.Fatalf("after the silence of its %s, %s has supervise rows %+v and %d %s notices, want its one start carrying its task and the one notice",
						h.role(), silenceIssue, rows, len(notices), workerSilentNotice)
				}
			})
		}
	})

	t.Run("the CI settlement's wake of a released pod", func(t *testing.T) {
		for _, h := range heldWorks {
			t.Run(h.name, func(t *testing.T) {
				rig := newWakeRig(t)
				h.stand(t, rig.pool, record.Issue{Key: "LEGION-208", Repo: "sjawhar/legion", DefaultBranch: "main", GitHubRepo: "sjawhar/legion",
					Tree: "LEGION-208", Project: "LEGION", Title: "root", Generation: wakeIssueGeneration, Status: "in_progress", Rank: "U", LastDispatchSeq: 1})
				if h.role() != claim.RoleImplementer {
					seedReleasedClaim(t, rig.pool, h.role(), "session-"+string(h.role()))
				}

				rig.apply("settled", settlement("head1", "build"))
				if !h.held() {
					oneWake(t, rig.pool, h.role(), pods.WakeChecks, "head1", "build")
					return
				}
				if starts, resumes := wakeStarts(t, rig.pool), wakeResumes(t, rig.pool); len(starts) != 0 || len(resumes) != 0 {
					t.Fatalf("the settlement wrote starts %+v and resumes %v, want no wake", starts, resumes)
				}
			})
		}
	})
}

// seedSilentWorker records role's claim of issue as the silence repair finds a worker it would
// prompt (aSilentTester, for any role): the admission slot the issue's phase keeps, or its open
// tree, the role's phase row naming the entry the claim was last given, and the claim up and
// between turns, holding no task, its agent's last step two hours before the clock.
func seedSilentWorker(t *testing.T, pool *pgxpool.Pool, issue record.Issue, role claim.Role) {
	t.Helper()
	if record.KeepsSlot(issue) {
		seedSlot(t, pool, record.Slot{Issue: issue.Key, Index: 0, AdmittedAt: silenceClock()})
	} else {
		seedTreeLifecycle(t, pool, issue.Key)
	}
	token := silenceToken(t, role)
	seedPhase(t, pool, record.PhaseRow{Issue: issue.Key, Role: role, Claim: token, Entry: silenceEntry, EntryTask: silenceEntryTask, EntryRaisedAt: silenceStep()})
	if _, err := pool.Exec(t.Context(), `insert into claims (token, project, tree, issue, role, generation, session, session_file,
		state, update_round, step_at, activity_background, given_entry, launch_failures, prompt_failures, prompt_retires, uncertain_streak)
		values ($1, 'legion', $2, $2, $3, $4, 'ses', '/sessions/ses.jsonl', 'ready', '', $5, false, $6, 0, 0, 0, 0)`,
		string(token), issue.Key, string(role), int64(issue.Generation), silenceStep(), int64(silenceEntry)); err != nil {
		t.Fatalf("seed the %s's claim of %s: %v", role, issue.Key, err)
	}
}
