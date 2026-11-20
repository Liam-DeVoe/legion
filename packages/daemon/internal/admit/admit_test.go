package admit

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/dispatch"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/projection"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/testwait"
	"github.com/sjawhar/legion/daemon/internal/workflow"
)

var _ intake.Handler = (*Admission)(nil)

func TestApplyFactAdmitsRootAndQueuesWhenFull(t *testing.T) {
	pool := migratedPool(t)
	admission := newAdmission(t, 1, slog.New(slog.NewTextHandler(io.Discard, nil)))

	apply(t, pool, admission, "root-todo", intake.DispatchIssue{Key: "LEGION-208", ResolvedRepo: "acme/widgets", Seq: 1, Type: "issue.updated", Status: "todo", Title: "Admission", Rank: "B", HandedOver: handed}, engineStub{})
	root := issue(t, pool, "LEGION-208")
	if root.Phase != phase.Admitted || root.Status != "in_progress" || root.Tree != root.Key {
		t.Fatalf("admitted root = %#v, want admitted root with truthful in_progress status and its own tree", root)
	}
	assertSlots(t, pool, []record.Slot{{Issue: "LEGION-208", Index: 0, AdmittedAt: fixedNow}})
	assertEffects(t, pool, []effect{
		{kind: record.OutboxKindDispatchStatus, issue: "LEGION-208", payload: record.OrdinaryStatusWrite("in_progress", "todo", "")},
		{kind: record.OutboxKindIssueBranch, issue: "LEGION-208", payload: record.IssueBranch{Generation: 1}},
		{kind: record.OutboxKindSupervise, issue: "LEGION-208", payload: record.SuperviseRequest{Op: "start", Tree: "LEGION-208", Role: claim.RoleArchitect, Generation: 1,
			Task: architectStart(t, pool, admission, "LEGION-208", ""), Entry: 1}},
	})

	apply(t, pool, admission, "queued-todo", intake.DispatchIssue{Key: "LEGION-209", ResolvedRepo: "acme/widgets", Seq: 1, Type: "issue.updated", Status: "todo", Title: "Queued", Rank: "A", HandedOver: handed}, engineStub{})
	queued := issue(t, pool, "LEGION-209")
	if queued.Status != "todo" || queued.Tree != queued.Key {
		t.Fatalf("queued root = %#v, want slotless root with its observed todo status", queued)
	}
	assertSlots(t, pool, []record.Slot{{Issue: "LEGION-208", Index: 0, AdmittedAt: fixedNow}})
	assertWaiting(t, pool, []string{"LEGION-209"})
	assertEffects(t, pool, []effect{
		{kind: record.OutboxKindDispatchStatus, issue: "LEGION-208", payload: record.OrdinaryStatusWrite("in_progress", "todo", "")},
		{kind: record.OutboxKindIssueBranch, issue: "LEGION-208", payload: record.IssueBranch{Generation: 1}},
		{kind: record.OutboxKindSupervise, issue: "LEGION-208", payload: record.SuperviseRequest{Op: "start", Tree: "LEGION-208", Role: claim.RoleArchitect, Generation: 1,
			Task: architectStart(t, pool, admission, "LEGION-208", ""), Entry: 1}},
	})
}

func TestApplyFactPromotesWaitingRootsInRankOrder(t *testing.T) {
	pool := migratedPool(t)
	admission := newAdmission(t, 2, slog.New(slog.NewTextHandler(io.Discard, nil)))
	seedSlotted(t, pool, "LEGION-ACTIVE-1", "A")
	seedSlotted(t, pool, "LEGION-ACTIVE-2", "B")

	for _, event := range []intake.DispatchIssue{
		{Key: "LEGION-C", ResolvedRepo: "acme/widgets", Seq: 1, Type: "issue.updated", Status: "todo", Title: "rank C", Rank: "C", HandedOver: handed},
		{Key: "LEGION-A", ResolvedRepo: "acme/widgets", Seq: 1, Type: "issue.updated", Status: "todo", Title: "rank A", Rank: "A", HandedOver: handed},
		{Key: "LEGION-B", ResolvedRepo: "acme/widgets", Seq: 1, Type: "issue.updated", Status: "todo", Title: "rank B", Rank: "B", HandedOver: handed},
	} {
		apply(t, pool, admission, "arrive-"+event.Key, event, engineStub{})
	}

	apply(t, pool, admission, "leave-first", intake.DispatchIssue{Key: "LEGION-ACTIVE-1", ResolvedRepo: "acme/widgets", Seq: 2, Type: "issue.updated", Status: "done", Title: "active", Rank: "A"}, engineStub{})
	assertSlots(t, pool, []record.Slot{
		{Issue: "LEGION-A", Index: 0, AdmittedAt: fixedNow},
		{Issue: "LEGION-ACTIVE-2", Index: 1, AdmittedAt: fixedNow},
	})

	apply(t, pool, admission, "leave-second", intake.DispatchIssue{Key: "LEGION-ACTIVE-2", ResolvedRepo: "acme/widgets", Seq: 2, Type: "issue.updated", Status: "done", Title: "active", Rank: "B"}, engineStub{})
	assertSlots(t, pool, []record.Slot{
		{Issue: "LEGION-A", Index: 0, AdmittedAt: fixedNow},
		{Issue: "LEGION-B", Index: 1, AdmittedAt: fixedNow},
	})
	assertWaiting(t, pool, []string{"LEGION-C"})
}

// A workflow root whose key names a tree the operator opened (an operator root spawn, or the
// lifecycle migration's operator tree) cannot be admitted while that lifecycle is open. That is
// the one root's wait: the fact goes on and admits the next root in rank order, and intake keeps
// flowing.
func TestAnOperatorTreeHoldsOnlyItsOwnWorkflowRootsAdmission(t *testing.T) {
	pool := migratedPool(t)
	var logged bytes.Buffer
	admission := newAdmission(t, 2, slog.New(slog.NewTextHandler(&logged, nil)))
	if _, err := pool.Exec(context.Background(), `insert into tree_lifecycles (project, tree, epoch, authority) values ('legion', 'LEGION-OPS', 1, 'operator')`); err != nil {
		t.Fatal(err)
	}
	apply(t, pool, admission, "arrive-ops", intake.DispatchIssue{Key: "LEGION-OPS", ResolvedRepo: "acme/widgets", Seq: 1, Type: "issue.updated", Status: "todo", Title: "held by the operator", Rank: "A", HandedOver: handed}, engineStub{})
	apply(t, pool, admission, "arrive-b", intake.DispatchIssue{Key: "LEGION-B", ResolvedRepo: "acme/widgets", Seq: 1, Type: "issue.updated", Status: "todo", Title: "rank B", Rank: "B", HandedOver: handed}, engineStub{})
	assertSlots(t, pool, []record.Slot{{Issue: "LEGION-B", Index: 0, AdmittedAt: fixedNow}})
	assertWaiting(t, pool, []string{"LEGION-OPS"})
	if !strings.Contains(logged.String(), "admission waits for its tree's other authority") || !strings.Contains(logged.String(), "issue=LEGION-OPS") {
		t.Fatalf("log %q, want the held root's wait named", logged.String())
	}
}

func TestApplyFactLeavesEngineRecordedChildWithoutSlotOrEffects(t *testing.T) {
	pool := migratedPool(t)
	admission := newAdmission(t, 1, slog.New(slog.NewTextHandler(io.Discard, nil)))
	seedSlotted(t, pool, "LEGION-ROOT", "A")
	parent := "LEGION-ROOT"
	child := record.Issue{Key: "LEGION-CHILD", Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets", Project: "LEGION", Title: "child", Parent: &parent, Tree: "LEGION-ROOT", Phase: phase.Admitted, Generation: 1, Status: "todo", Rank: "B", LastDispatchSeq: 1}

	apply(t, pool, admission, "child-todo", intake.DispatchIssue{Key: child.Key, ResolvedRepo: "acme/widgets", Seq: 1, Type: "issue.updated", Status: "todo", Title: child.Title, Parent: parent, Rank: child.Rank}, engineStub{store: record.NewStore(), recordChild: &child})
	if got := issue(t, pool, child.Key); !reflect.DeepEqual(withoutMovement(got), child) {
		t.Fatalf("child record = %#v, want engine-owned child %#v", got, child)
	}
	assertSlots(t, pool, []record.Slot{{Issue: "LEGION-ROOT", Index: 0, AdmittedAt: fixedNow}})
	assertWaiting(t, pool, nil)
	assertEffects(t, pool, nil)
}

func TestApplyFactDropsWaitingIssueWhenHumanMovesItOutOfTodo(t *testing.T) {
	for _, status := range []string{"triage", "icebox", "backlog", "done"} {
		t.Run(status, func(t *testing.T) {
			pool := migratedPool(t)
			admission := newAdmission(t, 0, slog.New(slog.NewTextHandler(io.Discard, nil)))
			seedWaiting(t, pool, "LEGION-WAITING", "A")

			apply(t, pool, admission, "leave-"+status, intake.DispatchIssue{Key: "LEGION-WAITING", ResolvedRepo: "acme/widgets", Seq: 2, Type: "issue.updated", Status: status, Title: "waiting", Rank: "A"}, engineStub{})
			if got := issue(t, pool, "LEGION-WAITING"); got.Status != status {
				t.Fatalf("status = %q, want %q", got.Status, status)
			}
			assertSlots(t, pool, nil)
			assertWaiting(t, pool, nil)
		})
	}
}

func TestApplyFactReleasesSlotWhenEngineCompletesPhaseAndPromotesHead(t *testing.T) {
	pool := migratedPool(t)
	admission := newAdmission(t, 1, slog.New(slog.NewTextHandler(io.Discard, nil)))
	seedSlotted(t, pool, "LEGION-ACTIVE", "A")
	seedWaiting(t, pool, "LEGION-NEXT", "B")

	apply(t, pool, admission, "phase-done", intake.DispatchIssue{Key: "LEGION-ACTIVE", ResolvedRepo: "acme/widgets", Seq: 2, Type: "issue.updated", Status: "in_progress", Title: "active", Rank: "A"}, engineStub{store: record.NewStore(), done: "LEGION-ACTIVE"})
	assertSlots(t, pool, []record.Slot{{Issue: "LEGION-NEXT", Index: 0, AdmittedAt: fixedNow}})
	if got := issue(t, pool, "LEGION-NEXT"); got.Status != "in_progress" {
		t.Fatalf("promoted status = %q, want in_progress", got.Status)
	}
	assertEffects(t, pool, []effect{
		{kind: record.OutboxKindDispatchStatus, issue: "LEGION-NEXT", payload: record.OrdinaryStatusWrite("in_progress", "todo", "")},
		{kind: record.OutboxKindIssueBranch, issue: "LEGION-NEXT", payload: record.IssueBranch{Generation: 1}},
		{kind: record.OutboxKindSupervise, issue: "LEGION-NEXT", payload: record.SuperviseRequest{Op: "start", Tree: "LEGION-NEXT", Role: claim.RoleArchitect, Generation: 1,
			Task: architectStart(t, pool, admission, "LEGION-NEXT", ""), Entry: 1}},
	})
}

func TestApplyFactReleasesSlotWhenDispatchLeavesActiveSetAndPromotesHead(t *testing.T) {
	pool := migratedPool(t)
	admission := newAdmission(t, 1, slog.New(slog.NewTextHandler(io.Discard, nil)))
	seedSlotted(t, pool, "LEGION-ACTIVE", "A")
	seedWaiting(t, pool, "LEGION-NEXT", "B")

	apply(t, pool, admission, "status-done", intake.DispatchIssue{Key: "LEGION-ACTIVE", ResolvedRepo: "acme/widgets", Seq: 2, Type: "issue.updated", Status: "done", Title: "active", Rank: "A"}, engineStub{})
	assertSlots(t, pool, []record.Slot{{Issue: "LEGION-NEXT", Index: 0, AdmittedAt: fixedNow}})
	if got := issue(t, pool, "LEGION-ACTIVE"); got.Status != "done" {
		t.Fatalf("released issue status = %q, want done", got.Status)
	}
}

// superviseOps counts the supervise rows of op queued for issue.
func superviseOps(t *testing.T, pool *pgxpool.Pool, issue, op string) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(context.Background(), `select count(*) from outbox where issue = $1 and kind = 'supervise' and payload->>'op' = $2`, issue, op).Scan(&count); err != nil {
		t.Fatalf("count %s rows of %s: %v", op, issue, err)
	}
	return count
}

// A re-admitted root that waits for a slot no longer lingers, so a fact in that window can move a
// child and start its worker. The root's promotion then starts its mid-phase children: a child's
// worker is started unless the newest of its role's operations that will still act is a start.
// Each row is the child tester's outbox and claim when the root is promoted, and how many starts
// the promotion adds: a second start would give the same task twice, and a missing one leaves the
// child mid-phase with nobody working it once a queued stop suspends its worker.
func TestPromotionStartsAChildUnlessItsWorkerIsStartedForTheRun(t *testing.T) {
	type seed struct {
		enqueue func(op record.SuperviseOp, generation uint64) int64
		claim   func(state string, serving uint64, lastStart int64)
		// pending gives the claim a task it holds undelivered or unconfirmed.
		pending func(generation uint64, p phase.Phase)
		// confirmedPending gives the claim a task whose turn already started.
		confirmedPending func(generation uint64, p phase.Phase)
		// otherClaim records a claim on the same issue and role under another daemon's project token.
		otherClaim func(state string, lastStart int64)
		// oldTreeClaim records this daemon's claim still in a tree other than the issue's: a carried
		// child's role whose turn in its old tree has not ended.
		oldTreeClaim func(state string, serving uint64, lastStart int64)
	}
	for _, tc := range []struct {
		name  string
		setup func(s seed)
		want  int
	}{
		{name: "no worker", setup: func(seed) {}, want: 1},
		{name: "the window's start still queued", setup: func(s seed) { s.enqueue("start", 1) }, want: 0},
		{name: "a live claim the window's start resumed", setup: func(s seed) { s.claim("working", 1, 7) }, want: 0},
		{name: "a claim new to the run, its first task not yet confirmed", setup: func(s seed) { s.claim("ready", 0, 7) }, want: 0},
		{name: "a live claim the close's suspend will still stop", setup: func(s seed) {
			s.claim("working", 1, 0)
			s.enqueue("suspend", 1)
		}, want: 1},
		{name: "a live claim whose window start superseded the close's suspend", setup: func(s seed) {
			s.claim("working", 1, s.enqueue("suspend", 1)+1)
		}, want: 0},
		{name: "a start queued before the close's suspend", setup: func(s seed) {
			s.claim("working", 1, 0)
			s.enqueue("start", 1)
			s.enqueue("suspend", 1)
		}, want: 1},
		{name: "a start queued after the close's suspend", setup: func(s seed) {
			s.claim("working", 1, 0)
			s.enqueue("suspend", 1)
			s.enqueue("start", 1)
		}, want: 0},
		{name: "a live claim with the ended linger's tree close still queued", setup: func(s seed) {
			s.claim("working", 1, 0)
			s.enqueue("tree_close", 1)
		}, want: 0},
		{name: "a live claim with only an earlier generation's suspend queued", setup: func(s seed) {
			s.claim("working", 1, 0)
			s.enqueue("suspend", 0)
		}, want: 0},
		{name: "a suspended claim serving the generation", setup: func(s seed) { s.claim("suspended", 1, 7) }, want: 1},
		{name: "a failed claim serving the generation", setup: func(s seed) { s.claim("failed", 1, 7) }, want: 1},
		{name: "a launch-uncertain claim holding the phase's task", setup: func(s seed) {
			s.claim("launch_uncertain", 1, 7)
			s.pending(1, phase.Testing)
		}, want: 0},
		{name: "a launch-uncertain claim holding the phase's task confirmed", setup: func(s seed) {
			s.claim("launch_uncertain", 1, 7)
			s.confirmedPending(1, phase.Testing)
		}, want: 1},
		{name: "a launch-uncertain claim holding an earlier phase's task", setup: func(s seed) {
			s.claim("launch_uncertain", 1, 7)
			s.pending(1, phase.Implementing)
		}, want: 1},
		{name: "a claim holding the phase's task that the close's suspend will still stop", setup: func(s seed) {
			s.claim("launch_uncertain", 1, 0)
			s.pending(1, phase.Testing)
			s.enqueue("suspend", 1)
		}, want: 1},
		{name: "a failed claim holding the phase's task", setup: func(s seed) {
			s.claim("failed", 1, 7)
			s.pending(1, phase.Testing)
		}, want: 1},
		{name: "a retired claim holding the phase's task", setup: func(s seed) {
			s.claim("retired", 1, 7)
			s.pending(1, phase.Testing)
		}, want: 1},
		{name: "a suspended claim holding the phase's task", setup: func(s seed) {
			s.claim("suspended", 1, 7)
			s.pending(1, phase.Testing)
		}, want: 1},
		{name: "a queued claim holding the phase's task", setup: func(s seed) {
			s.claim("queued", 1, 7)
			s.pending(1, phase.Testing)
		}, want: 1},
		{name: "another daemon's live claim on the same issue and role", setup: func(s seed) { s.otherClaim("working", 0) }, want: 1},
		{name: "a live claim still in its old tree, a carried child's turn there not over", setup: func(s seed) { s.oldTreeClaim("working", 1, 7) }, want: 1},
		{name: "a launch-uncertain claim still in its old tree holding the phase's task", setup: func(s seed) {
			s.oldTreeClaim("launch_uncertain", 1, 7)
			s.pending(1, phase.Testing)
		}, want: 1},
		{name: "another daemon's claim beside this daemon's, with the close's suspend queued", setup: func(s seed) {
			s.claim("working", 1, 0)
			s.otherClaim("working", 0)
			s.enqueue("suspend", 1)
		}, want: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := migratedPool(t)
			admission := newAdmission(t, 1, slog.New(slog.NewTextHandler(io.Discard, nil)))
			engine := workflow.New(record.NewStore(), workflow.Config{Project: testProject, Repositories: testRepositories, Steps: config.DesignGateRootIssues.Steps(), Clock: func() time.Time { return fixedNow }}, nil)
			seedSlotted(t, pool, "LEGION-100", "A")
			root := record.Issue{Key: "LEGION-208", Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets", Project: "LEGION", Title: "root", Tree: "LEGION-208", Phase: phase.Admitted, Generation: 2, Status: "todo", Rank: "B", HandedOver: true, LastDispatchSeq: 2}
			putIssue(t, pool, root)
			parentKey := root.Key
			child := record.Issue{Key: "LEGION-209", Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets", Project: "LEGION", Title: "child", Tree: root.Key, Parent: &parentKey,
				Phase: phase.Testing, Generation: 1, Status: "testing", Rank: "C", LastDispatchSeq: 1}
			putIssue(t, pool, child)
			project, err := claim.ProjectToken(testProject)
			if err != nil {
				t.Fatal(err)
			}
			token, err := claim.NewToken(project, child.Key, claim.RoleTester)
			if err != nil {
				t.Fatal(err)
			}
			enqueueRequest := func(payload record.SuperviseRequest) int64 {
				t.Helper()
				inTx(t, pool, func(tx pgx.Tx) {
					store := record.NewStore()
					row, err := record.NewFencedRow(context.Background(), tx, store, child.Key, payload, fixedNow)
					if err != nil {
						t.Fatal(err)
					}
					if err := store.Enqueue(context.Background(), tx, row); err != nil {
						t.Fatalf("enqueue %s: %v", payload.Op, err)
					}
				})
				var id int64
				if err := pool.QueryRow(context.Background(), `select max(id) from outbox`).Scan(&id); err != nil {
					t.Fatalf("read the %s row's id: %v", payload.Op, err)
				}
				return id
			}
			putClaim := func(token claim.Token, project, tree, state string, serving uint64, lastStart int64) {
				t.Helper()
				if _, err := pool.Exec(context.Background(), `insert into claims (token, project, tree, issue, role, generation, session, session_file, state,
					launch_failures, prompt_failures, prompt_retires, uncertain_streak, serving_generation, last_start_row)
					values ($1, $2, $3, 'LEGION-209', 'tester', 1, 'ses_tester', '', $4, 0, 0, 0, 0, $5, $6)`,
					string(token), project, tree, state, int64(serving), lastStart); err != nil {
					t.Fatalf("record the %s claim %s: %v", state, token, err)
				}
			}
			putPending := func(generation uint64, p phase.Phase, confirmedAt *time.Time) {
				t.Helper()
				if _, err := pool.Exec(context.Background(), `insert into pending_task_deliveries (claim_token, delivery_id, task, queued_at, generation, phase, confirmed_at)
					values ($1, 'outbox:7', 'Carry on.', now(), $2, $3, $4)`, string(token), int64(generation), string(p), confirmedAt); err != nil {
					t.Fatalf("record the pending task: %v", err)
				}
			}
			tc.setup(seed{
				enqueue: func(op record.SuperviseOp, generation uint64) int64 {
					payload := record.SuperviseRequest{Op: op, Tree: root.Key, Role: claim.RoleTester, Generation: generation}
					switch op {
					case "start":
						payload.Phase, payload.Task = child.Phase, workflow.ResumePhaseTask(child)
					case "tree_close":
						// The close of the linger the re-admission ended: the root's generation 1.
						payload.Linger = 1
					}
					return enqueueRequest(payload)
				},
				claim: func(state string, serving uint64, lastStart int64) {
					putClaim(token, project, root.Key, state, serving, lastStart)
				},
				pending: func(generation uint64, p phase.Phase) {
					putPending(generation, p, nil)
				},
				confirmedPending: func(generation uint64, p phase.Phase) {
					confirmed := fixedNow
					putPending(generation, p, &confirmed)
				},
				otherClaim: func(state string, lastStart int64) {
					other, err := claim.NewToken("otherlegion", child.Key, claim.RoleTester)
					if err != nil {
						t.Fatal(err)
					}
					putClaim(other, "otherlegion", root.Key, state, 1, lastStart)
				},
				oldTreeClaim: func(state string, serving uint64, lastStart int64) {
					putClaim(token, project, "LEGION-207", state, serving, lastStart)
				},
			})
			// resumes counts the starts that carry the phase's task marked as its resume task.
			testerStarts := func() (starts, resumes int) {
				t.Helper()
				if err := pool.QueryRow(context.Background(), `select count(*), count(*) filter (where coalesce(payload->>'task', '') <> '' and payload->>'resumeTask' = 'true') from outbox
					where issue = 'LEGION-209' and kind = 'supervise' and payload->>'op' = 'start' and coalesce(payload->>'task', '') <> '' and payload->>'role' = 'tester' and payload->>'phase' = 'testing'`).
					Scan(&starts, &resumes); err != nil {
					t.Fatalf("count the child's starts: %v", err)
				}
				return starts, resumes
			}
			before, beforeResumes := testerStarts()

			apply(t, pool, admission, "free-the-slot", intake.DispatchIssue{Key: "LEGION-100", ResolvedRepo: "acme/widgets", Seq: 2, Type: "issue.closed", Status: "done", Title: "LEGION-100", Rank: "A"}, engine)
			if slotted := issue(t, pool, root.Key); slotted.Status != "in_progress" {
				t.Fatalf("the waiting root after the slot freed = %s, want it promoted", slotted.Status)
			}
			after, afterResumes := testerStarts()
			if added, addedResumes := after-before, afterResumes-beforeResumes; added != tc.want || addedResumes != tc.want {
				t.Fatalf("tester starts the promotion added = %d, %d of them with the phase's resume task; want %d, each with it", added, addedResumes, tc.want)
			}
		})
	}
}

// Every newer Dispatch observation is recorded, not only a status change: re-ranking a waiting
// root, renaming it, or re-parenting it arrives as an issue.updated at the same status, and the
// waiting line has to follow Dispatch rank order at once, not after the next boot's read. It runs
// through the real engine, which sees every fact before admission.
func TestApplyFactRecordsRankTitleAndParentChangesAtTheSameStatus(t *testing.T) {
	pool := migratedPool(t)
	admission := newAdmission(t, 1, slog.New(slog.NewTextHandler(io.Discard, nil)))
	engine := workflow.New(record.NewStore(), workflow.Config{Project: testProject, Repositories: testRepositories, Steps: config.DesignGateRootIssues.Steps(), Clock: func() time.Time { return fixedNow }}, nil)
	seedSlotted(t, pool, "LEGION-1", "A")
	seedWaiting(t, pool, "LEGION-2", "B")
	seedWaiting(t, pool, "LEGION-3", "C")

	apply(t, pool, admission, "rerank", intake.DispatchIssue{Key: "LEGION-3", ResolvedRepo: "acme/widgets", Seq: 2, Type: "issue.updated", Status: "todo", Title: "renamed", Parent: "LEGION-9", Rank: "AB", HandedOver: handed}, engine)
	got := issue(t, pool, "LEGION-3")
	if got.Rank != "AB" || got.Title != "renamed" || got.Parent == nil || *got.Parent != "LEGION-9" || got.LastDispatchSeq != 2 {
		t.Fatalf("observed record = %#v, want rank AB, title renamed, parent LEGION-9, seq 2", got)
	}
	assertWaiting(t, pool, []string{"LEGION-3", "LEGION-2"})
}

// A title is a person's own text, so it can hold a NUL, which the store keeps as U+FFFD. The boot
// listing shows the title as Dispatch holds it, at the sequence the record has already applied, and
// the record keeps what the store made of it: the two are the same title, so the listing writes
// nothing, as it writes nothing of any title the record already holds. Compared as listed, the
// title would differ from its own stored copy, and the record would be written again at every boot.
func TestTheBootListingOfATitleHoldingNULWritesNothingTheRecordAlreadyHolds(t *testing.T) {
	pool := migratedPool(t)
	facts := store.NewPool(pool)
	admission := newAdmission(t, 1, slog.New(slog.NewTextHandler(io.Discard, nil)))
	engine := workflow.New(record.NewStore(), workflow.Config{Project: testProject, Repositories: testRepositories, Steps: config.DesignGateRootIssues.Steps(), Clock: func() time.Time { return fixedNow }}, nil)
	seedSlotted(t, pool, "LEGION-1", "A")
	seedWaiting(t, pool, "LEGION-3", "C")
	const title = "re\x00named"
	ctx := context.Background()

	if _, err := intake.ApplyFact(ctx, facts, "dispatch", "rename", intake.DispatchIssue{Key: "LEGION-3", ResolvedRepo: "acme/widgets", Seq: 2, Type: "issue.updated",
		Status: "todo", Title: title, Rank: "C", HandedOver: handed}, engine, admission); err != nil {
		t.Fatalf("apply the rename: %v", err)
	}
	if got := issue(t, pool, "LEGION-3"); got.Title != "re\uFFFDnamed" || got.LastDispatchSeq != 2 {
		t.Fatalf("the renamed record is titled %q at seq %d, want %q at 2", got.Title, got.LastDispatchSeq, "re\uFFFDnamed")
	}
	// xmin is the transaction that wrote the row's current version: every write of the row,
	// whatever it changes, gives it another.
	written := func() string {
		t.Helper()
		var xmin string
		if err := pool.QueryRow(ctx, `select xmin::text from issues where key = $1`, "LEGION-3").Scan(&xmin); err != nil {
			t.Fatalf("read the record's xmin: %v", err)
		}
		return xmin
	}
	before := written()

	listed := []intake.DispatchIssue{intake.ListedIssue(dispatch.IssueSummary{Key: "LEGION-3", Title: title, Status: "todo", Rank: "C", HandedOver: handed, LastSeq: 2, ResolvedRepo: "acme/widgets"})}
	scoped, committed := intake.WithCommitHooks(ctx)
	if err := pgx.BeginFunc(scoped, facts, func(tx pgx.Tx) error {
		return admission.Reconcile(scoped, tx, listed, 2, intake.DispatchConsumerPosition{AckFloorStream: 2, Idle: true})
	}); err != nil {
		t.Fatalf("reconcile the boot listing: %v", err)
	}
	committed()
	if after := written(); after != before {
		t.Fatalf("the boot listing of the title the record holds wrote the record again (xmin %s, then %s), want no write", before, after)
	}
	if got := issue(t, pool, "LEGION-3"); got.Title != "re\uFFFDnamed" || got.LastDispatchSeq != 2 {
		t.Fatalf("the record after the boot listing is titled %q at seq %d, want %q at 2", got.Title, got.LastDispatchSeq, "re\uFFFDnamed")
	}
}

// The boot read opens a new run of a child the daemon closed that a person set back to todo while
// the daemon was down, as the live event does (decision 8 of dispatch://INFRA-4730): a new
// generation at admitted, under its parent on Dispatch's chain. Its tree is written once and stays,
// it is no root, and its first phase waits for the gate above it, so it takes no slot.
func TestReconcileNeverReadmitsAChildAsARoot(t *testing.T) {
	pool := migratedPool(t)
	admission := newAdmission(t, 2, slog.New(slog.NewTextHandler(io.Discard, nil)))
	seedSlotted(t, pool, "LEGION-1", "A")
	parent := "LEGION-1"
	putIssue(t, pool, record.Issue{Key: "LEGION-2", Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets", Tree: "LEGION-1", Project: testProject, Title: "child", Parent: &parent, Phase: phase.Done, Generation: 1, Status: "done", Rank: "B"})

	reconcile(t, pool, admission, []dispatch.IssueSummary{
		{Key: "LEGION-1", ResolvedRepo: "acme/widgets", Title: "LEGION-1", Status: "in_progress", Rank: "A"},
		{Key: "LEGION-2", ResolvedRepo: "acme/widgets", Title: "child", Status: "todo", Parent: &parent, Rank: "B"},
	})
	if got := issue(t, pool, "LEGION-2"); got.Tree != "LEGION-1" || got.Generation != 2 || got.Phase != phase.Admitted || got.HoldsGate {
		t.Fatalf("reconciled child = %#v, want a new run at generation 2, admitted, under LEGION-1 and holding no gate of its own", got)
	}
	assertSlots(t, pool, []record.Slot{{Issue: "LEGION-1", Index: 0, AdmittedAt: fixedNow}})
}

// The boot read opens a new run of a root the daemon closed that a person set back to todo while
// the daemon was down, exactly as the live event does: a new generation at admitted, promoted.
func TestReconcileReadmitsALingeringRootSetBackToTodo(t *testing.T) {
	pool := migratedPool(t)
	admission := newAdmission(t, 1, slog.New(slog.NewTextHandler(io.Discard, nil)))
	putIssue(t, pool, record.Issue{Key: "LEGION-CLOSED", Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets", Project: "LEGION", Title: "closed", Tree: "LEGION-CLOSED", Phase: phase.Done, Generation: 3, Status: "done", Rank: "A", HoldsGate: true})

	reconcile(t, pool, admission, []dispatch.IssueSummary{{Key: "LEGION-CLOSED", ResolvedRepo: "acme/widgets", Title: "closed", Status: "todo", Rank: "A", HandedOver: handed}})
	again := issue(t, pool, "LEGION-CLOSED")
	if again.Generation != 4 || again.Phase != phase.Admitted || again.Status != "in_progress" {
		t.Fatalf("reconciled root = %#v, want a new run at generation 4, admitted and promoted", again)
	}
	assertSlots(t, pool, []record.Slot{{Issue: "LEGION-CLOSED", Index: 0, AdmittedAt: fixedNow}})
}

func TestReconcileFillsRaisedCapInRankOrderAndIsIdempotent(t *testing.T) {
	pool := migratedPool(t)
	admission := newAdmission(t, 1, slog.New(slog.NewTextHandler(io.Discard, nil)))
	seedWaiting(t, pool, "LEGION-B", "B")
	seedWaiting(t, pool, "LEGION-A", "A")
	seedWaiting(t, pool, "LEGION-C", "C")
	admission.cap = 2
	summaries := []dispatch.IssueSummary{
		{Key: "LEGION-C", ResolvedRepo: "acme/widgets", Title: "C", Status: "todo", Rank: "C", HandedOver: handed},
		{Key: "LEGION-B", ResolvedRepo: "acme/widgets", Title: "B", Status: "todo", Rank: "B", HandedOver: handed},
		{Key: "LEGION-A", ResolvedRepo: "acme/widgets", Title: "A", Status: "todo", Rank: "A", HandedOver: handed},
	}

	reconcile(t, pool, admission, summaries)
	assertSlots(t, pool, []record.Slot{
		{Issue: "LEGION-A", Index: 0, AdmittedAt: fixedNow},
		{Issue: "LEGION-B", Index: 1, AdmittedAt: fixedNow},
	})
	assertWaiting(t, pool, []string{"LEGION-C"})
	firstEffects := effects(t, pool)
	if len(firstEffects) != 6 {
		t.Fatalf("effects after raised-cap reconcile = %#v, want two status, two issue_branch and two supervise rows", firstEffects)
	}

	reconcile(t, pool, admission, summaries)
	if got := effects(t, pool); !reflect.DeepEqual(got, firstEffects) {
		t.Fatalf("second reconcile effects = %#v, want unchanged %#v", got, firstEffects)
	}
}

// A status change applies through Reconcile only when the summary's own sequence is genuinely
// newer than the record's, and the consumer has caught up to it — otherwise it is either deferred
// (behind) or, level with what is already recorded, left alone (see hold_test.go's
// TestReconcileLeavesAnIssueTheStreamHoldsNewerEventsFor and applySummary).
func TestReconcileReleasesSlotWhoseDispatchStatusLeftActiveSet(t *testing.T) {
	pool := migratedPool(t)
	admission := newAdmission(t, 1, slog.New(slog.NewTextHandler(io.Discard, nil)))
	seedSlotted(t, pool, "LEGION-ACTIVE", "A")

	reconcileWithPosition(t, pool, admission, []dispatch.IssueSummary{
		{Key: "LEGION-ACTIVE", ResolvedRepo: "acme/widgets", Title: "active", Status: "done", Rank: "A", LastSeq: 1},
	}, 1, 1, true)
	assertSlots(t, pool, nil)
	if got := issue(t, pool, "LEGION-ACTIVE"); got.Status != "done" {
		t.Fatalf("reconciled status = %q, want done", got.Status)
	}
}

func TestCapturedDispatchTodoEventAdmitsAndProjectsActiveSlot(t *testing.T) {
	pool := migratedPool(t)
	admission := newAdmission(t, 1, slog.New(slog.NewTextHandler(io.Discard, nil)))
	js := testJetStream(t)
	consumers, err := intake.OpenConsumers(context.Background(), js, pool, intake.ConsumerSpec{Project: "CAPTURE", Repositories: []ghrepo.Repository{ghrepo.MustParse("acme/widgets")}, AckWait: time.Second, NakDelay: time.Millisecond, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatalf("OpenConsumers: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- consumers.Run(ctx, engineStub{}, admission) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run: %v", err)
		}
	})

	captured, err := os.ReadFile("../intake/testdata/dispatch/issue-updated.json")
	if err != nil {
		t.Fatalf("read captured Dispatch issue.updated event: %v", err)
	}
	// The captured issue carries no labels; this one is handed to Legion.
	labeled := strings.Replace(string(captured), `\"labels\":[]`, `\"labels\":[\"legion\"]`, 1)
	if labeled == string(captured) {
		t.Fatal("the captured event has no empty labels to hand the issue over with")
	}
	if _, err := js.Publish(context.Background(), "notifications.dispatch.issue.CAPTURE-3.issue.updated", []byte(labeled)); err != nil {
		t.Fatalf("publish captured Dispatch event: %v", err)
	}
	testwait.Eventually(t, "captured event admission", func() bool {
		tx, err := pool.Begin(context.Background())
		if err != nil {
			return false
		}
		defer tx.Rollback(context.Background())
		state, err := projection.Project(context.Background(), tx, record.NewStore(), "CAPTURE", nil, nil, nil, nil, nil, time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC))
		if err != nil {
			return false
		}
		return reflect.DeepEqual(state.Admission.Active, []string{"CAPTURE-3"}) && state.Issues["CAPTURE-3"].Slot != nil
	})
}
