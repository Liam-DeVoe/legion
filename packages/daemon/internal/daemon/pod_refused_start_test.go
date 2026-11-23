package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/runtime/fake"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/workflow"
)

// podRefusalRig is LEGION-208 in testing, its tester's claim suspended by the daemon with its
// session kept and its phases row on entry 2, over an outbox runner that drains on the test's
// goroutine. Every launch of the claim the runtime refuses is told to the workflow as the daemon
// tells it (workflowRuntime.podRefused, supervise's Deps.PodRefused), and the machines run on a
// clock whose timers fire only when the test fires them (heldClock).
type podRefusalRig struct {
	t       *testing.T
	runner  *outbox
	rt      *fake.Runtime
	pool    *store.Pool
	clock   *heldClock
	machine *supervise.Machine
	token   claim.Token
}

// newPodRefusalRig builds the rig. answers builds the workflow runtime the refusals are told to,
// over the rig's pool and records, whose handlers the outbox runner applies its own facts with, as
// the daemon's runner does; deps changes the machines' deps before the claim is created, since a
// machine takes its deps where it is built.
func newPodRefusalRig(t *testing.T, answers func(pool *store.Pool, records record.Store) *workflowRuntime, deps func(*supervise.Deps)) *podRefusalRig {
	t.Helper()
	runner, sup, pool := runnerIn(t, phase.Testing, "testing", &outboxPublisher{}, quietLogger())
	clock := &heldClock{}
	sup.deps.Clock = clock
	told := answers(pool, runner.records)
	sup.deps.PodRefused = told.podRefused
	runner.handlers = told.handlers
	if deps != nil {
		deps(&sup.deps)
	}
	token := mustClaimToken(t, "LEGION-208", claim.RoleTester)
	putPhaseRow(t, pool, runner.records, record.PhaseRow{Issue: "LEGION-208", Role: claim.RoleTester, Claim: token, Entry: 2, EntryTask: "Test it.",
		EntryRaisedAt: time.Now().UTC().Add(-time.Hour)})
	machine, _, err := sup.Create(context.Background(), supervise.Claim{Token: token, Project: "legion", Tree: "LEGION-208", Issue: "LEGION-208",
		Role: claim.RoleTester, Generation: 1, ServingGeneration: 1, State: supervise.StateSuspended, SuspendedBy: supervise.SuspendedByDaemon,
		Session: "ses-tester", SessionFile: t.TempDir() + "/session.jsonl"}, "")
	if err != nil {
		t.Fatalf("create the tester's claim: %v", err)
	}
	return &podRefusalRig{t: t, runner: runner, rt: sup.deps.Runtime.(*fake.Runtime), pool: pool, clock: clock, machine: machine, token: token}
}

// engineAnswers is the workflow runtime that applies each refusal's fact through the workflow
// engine of LEGION.
func engineAnswers(pool *store.Pool, records record.Store) *workflowRuntime {
	engine := workflow.New(records, workflow.Config{Project: "LEGION", Repositories: testRepositories, Steps: config.DesignGateRootIssues.Steps()}, quietLogger())
	return &workflowRuntime{pool: pool, records: records, handlers: []intake.Handler{engine}, log: quietLogger(), failed: make(chan error, 1)}
}

// queueTheTestersStart writes the outbox start that brings the tester back for entry 2 of testing,
// as the transition that raised that entry queued it.
func (r *podRefusalRig) queueTheTestersStart() {
	r.t.Helper()
	enqueueOutbox(r.t, r.pool, r.runner.records, mustOutboxRow(r.t, "LEGION-208", record.SuperviseRequest{Op: "start", Tree: "LEGION-208",
		Role: claim.RoleTester, Phase: phase.Testing, Entry: 2, Task: "Test it.", Generation: 1}, time.Now().Add(-time.Minute)))
}

// refuseOnPod1 makes pod-1 LEGION-208's pod, and every later launch of the tester one that pod
// cannot run until its update round or its release replaces it.
func (r *podRefusalRig) refuseOnPod1() {
	r.rt.ScriptIssuePod("LEGION-208", "pod-1")
	r.rt.OnResume = func(context.Context) error {
		return runtime.AwaitsPodUpdate{Pod: "pod-1", Reason: "launch " + string(r.token) + ": the pod pod-1 of LEGION-208 was built without checker"}
	}
}

// starts is every supervise start the outbox holds, in order.
func (r *podRefusalRig) starts() []record.SuperviseRequest {
	r.t.Helper()
	rows, err := r.pool.Query(context.Background(), `select payload from outbox where kind = 'supervise' and payload->>'op' = 'start' order by id`)
	if err != nil {
		r.t.Fatal(err)
	}
	defer rows.Close()
	var got []record.SuperviseRequest
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			r.t.Fatal(err)
		}
		var start record.SuperviseRequest
		if err := json.Unmarshal(payload, &start); err != nil {
			r.t.Fatal(err)
		}
		got = append(got, start)
	}
	if err := rows.Err(); err != nil {
		r.t.Fatal(err)
	}
	return got
}

// toldTheController waits, bounded, for the pod-wait-held notice the refusal's fact writes in its
// own transaction: the fact is applied on a goroutine of its own, so its commit is what the test
// waits for before it reads what else that transaction wrote.
func (r *podRefusalRig) toldTheController() {
	r.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		var told int
		if err := r.pool.QueryRow(context.Background(), `select count(*) from outbox where kind = $1 and payload->>'kind' = $2`,
			string(record.OutboxKindControllerNotice), string(record.PodWaitHeldNotice)).Scan(&told); err != nil {
			r.t.Fatal(err)
		}
		if told > 0 {
			return
		}
		if time.Now().After(deadline) {
			r.t.Fatal("the refusal's fact never committed: the outbox holds no pod-wait-held notice after 20 s")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// launchWriteRefused is the outbox's claim store, which refuses the first write of a claim
// launching, as a store that did not answer refuses it: the launch's own write, which arms its
// retry at the probe interval (supervise's relaunchLater).
type launchWriteRefused struct {
	*outboxClaimStore
	refused atomic.Bool
}

func (s *launchWriteRefused) PutClaim(ctx context.Context, c supervise.Claim) error {
	if c.State == supervise.StateLaunching && s.refused.CompareAndSwap(false, true) {
		return errors.New("the store did not answer the launch's write")
	}
	return s.outboxClaimStore.PutClaim(ctx, c)
}

// A launch its issue's pod refuses after the outbox start that raised it has finished leaves no
// row in the outbox to bring the claim back: the start delivered its task to a claim still
// launching and ended, and the refusal, which suspends the claim for the daemon and retires that
// task, comes later, on a timer of the machine's. Two launches come that way: the turn the wake
// pacing held a resume for, and the retry of a launch whose write the store did not take, which
// carries the start's work. Either way the workflow reads no start of the claim in the outbox in
// the refusal's own transaction, and queues one: the tester's owed entry, the task its phases row
// holds, marked as a resend of that entry, so whatever replaces the pod resumes the claim on that
// work.
func TestALaunchItsPodRefusesAfterItsStartRowFinishedQueuesAStartOfTheClaim(t *testing.T) {
	for _, tc := range []struct {
		name string
		deps func(*supervise.Deps)
		// launchingAfterItsRow drains the rig's start row to its finish, leaving the claim
		// launching with no process under the timer this case fires, and answers that timer's
		// wait.
		launchingAfterItsRow func(t *testing.T, r *podRefusalRig) time.Duration
	}{
		{
			name: "the turn of a resume the wake pacing held",
			deps: func(d *supervise.Deps) {
				d.WakePace = supervise.WakePace{Rate: 1, Burst: 1, Reserve: func(now time.Time) time.Time { return now.Add(parkedTurn) }}
			},
			launchingAfterItsRow: func(t *testing.T, r *podRefusalRig) time.Duration {
				drainAt(t, r.runner, 0, "the start row's attempt")
				if held := r.machine.View(); held.State != supervise.StateLaunching || held.Locator != nil || held.WakeWaiting == nil {
					t.Fatalf("after the start row's attempt the tester is %s recording %+v with wake %+v, want it launching with no process under the resume the pacing parked",
						held.State, held.Locator, held.WakeWaiting)
				}
				return parkedTurn
			},
		},
		{
			name: "the retry of a launch whose write the store did not take",
			deps: func(d *supervise.Deps) {
				d.Store = &launchWriteRefused{outboxClaimStore: d.Store.(*outboxClaimStore)}
			},
			launchingAfterItsRow: func(t *testing.T, r *podRefusalRig) time.Duration {
				drainAt(t, r.runner, 0, "the start row's first attempt")
				if live, _ := r.clock.armed(time.Second); live != 1 {
					t.Fatalf("after the launch's write failed %d timers wait the probe interval, want the one retry of the launch", live)
				}
				drainAt(t, r.runner, parkFound, "the start row's second attempt")
				if held := r.machine.View(); held.State != supervise.StateLaunching || held.Locator != nil {
					t.Fatalf("after the start row's second attempt the tester is %s recording %+v, want it launching with no process under the retry", held.State, held.Locator)
				}
				return time.Second
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newPodRefusalRig(t, engineAnswers, tc.deps)
			r.queueTheTestersStart()
			wait := tc.launchingAfterItsRow(t, r)
			if left := r.starts(); len(left) != 0 {
				t.Fatalf("the outbox still holds the starts %+v, want the tester's start finished once it delivered its task", left)
			}

			r.refuseOnPod1()
			if fired := r.clock.fire(wait); fired != 1 {
				t.Fatalf("fired %d timers waiting %s, want the one the launch waits on", fired, wait)
			}
			if held := r.machine.Claim(); held.State != supervise.StateSuspended || held.SuspendedBy != supervise.SuspendedByDaemon {
				t.Fatalf("after the refused launch the tester is %s by %q, want it suspended by the daemon", held.State, held.SuspendedBy)
			}
			r.toldTheController()
			got := r.starts()
			if len(got) != 1 {
				t.Fatalf("after the refusal the outbox holds the starts %+v, want one start of the tester: nothing else brings it back once another pod stands", got)
			}
			if s := got[0]; s.Role != claim.RoleTester || s.Phase != phase.Testing || s.Entry != 2 || !strings.HasPrefix(s.Task, "Test it.") || !s.ResendEntry {
				t.Fatalf("the queued start is %+v, want the tester's owed entry 2 of testing, its stored task, marked as a resend of that entry", s)
			}
		})
	}
}

// A refusal's fact is how the workflow learns that a claim waits for its pod and is what queues
// the start that brings the claim back, so a fact the workflow cannot apply is never only logged:
// it stops the workflow, and with it the daemon, as a claim's terminal fact does (terminal). The
// claim's suspension for the daemon is durable, so the next boot's resume by rule starts it
// (workflow's booted). Here every fact the workflow applies fails.
func TestARefusalsFactTheWorkflowCannotApplyStopsTheWorkflow(t *testing.T) {
	failing := errors.New("the fact's transaction failed")
	var answers *workflowRuntime
	r := newPodRefusalRig(t, func(pool *store.Pool, records record.Store) *workflowRuntime {
		answers = &workflowRuntime{pool: pool, records: records, handlers: []intake.Handler{&outboxFactHandler{err: failing}}, log: quietLogger(),
			failed: make(chan error, 1)}
		return answers
	}, nil)
	r.refuseOnPod1()
	if err := r.machine.Handle(context.Background(), supervise.RequestRuleResume{Claim: r.token, Reason: "it owns its issue's step"}); !errors.Is(err, runtime.ErrAwaitsPodUpdate) {
		t.Fatalf("the resume answered %v, want the refusal, which is runtime.ErrAwaitsPodUpdate", err)
	}
	select {
	case err := <-answers.failed:
		if !errors.Is(err, failing) {
			t.Fatalf("the workflow stopped on %v, want the fact's failure", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the refusal's fact failed and the workflow went on: a lost fact leaves the claim suspended with nothing to start it")
	}
}

// The turn of a resume the wake pacing held can come, and its launch be refused, while the start
// row that the pacing parked it for is still finishing: the row has delivered its task and returned,
// and its finish has not yet deleted it. The refusal's fact then reads that row as a start of the
// claim and queues none, and the row's finish leaves the claim suspended with nothing to start it.
// So the finish of a start row whose claim's last launch its pod refused asks the workflow again,
// once the row is gone, and the workflow queues the claim's start where none stands (the tester's
// owed entry). The interleaving is driven here: the row's attempt runs (execute) and its finish
// (settle) waits until the wake's turn has been refused and its fact has committed.
func TestARefusalWhileItsParkedStartRowIsStillFinishingQueuesAStartOnceTheRowEnds(t *testing.T) {
	r := newPodRefusalRig(t, engineAnswers, func(d *supervise.Deps) {
		d.WakePace = supervise.WakePace{Rate: 1, Burst: 1, Reserve: func(now time.Time) time.Time { return now.Add(parkedTurn) }}
	})
	r.queueTheTestersStart()
	var id int64
	if err := r.pool.QueryRow(context.Background(), `select id from outbox where kind = 'supervise'`).Scan(&id); err != nil {
		t.Fatalf("read the start row's id: %v", err)
	}
	row := mustOutboxRow(t, "LEGION-208", record.SuperviseRequest{Op: "start", Tree: "LEGION-208", Role: claim.RoleTester, Phase: phase.Testing,
		Entry: 2, Task: "Test it.", Generation: 1}, time.Now().Add(-time.Minute))
	row.ID = id
	if err := r.runner.execute(context.Background(), row); err != nil {
		t.Fatalf("the start row's attempt: %v", err)
	}
	if held := r.machine.View(); held.State != supervise.StateLaunching || held.WakeWaiting == nil {
		t.Fatalf("after the start row's attempt the tester is %s with wake %+v, want it launching under the resume the pacing parked", held.State, held.WakeWaiting)
	}

	r.refuseOnPod1()
	if fired := r.clock.fire(parkedTurn); fired != 1 {
		t.Fatalf("fired %d timers for the wake's turn, want the one the pacing armed", fired)
	}
	r.toldTheController()
	if got := r.starts(); len(got) != 1 {
		t.Fatalf("while the row still stands the outbox holds the starts %+v, want the row's own alone: the refusal read it as the claim's start", got)
	}

	r.runner.settle(context.Background(), row, nil)
	deadline := time.Now().Add(20 * time.Second)
	for {
		got := r.starts()
		if len(got) == 1 && got[0].ResendEntry {
			if s := got[0]; s.Role != claim.RoleTester || s.Phase != phase.Testing || s.Entry != 2 || !strings.HasPrefix(s.Task, "Test it.") {
				t.Fatalf("the queued start is %+v, want the tester's owed entry 2 of testing, its stored task, marked as a resend of that entry", s)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("20 s after the row's finish the outbox holds the starts %+v, want the tester's owed entry queued: nothing else brings it back", got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
