package daemon

// An update round's ask counts once it reached a session (dispatch://INFRA-4109 R4,
// dispatch://INFRA-4131 R4, verification's c-undelivered-ask-asked-again). The sweep enqueues the
// ask and marks nothing; the outbox's row publishes it under the claim's own lock and marks the
// claim asked only when the publish reached a session (updateAsk, supervise's
// Machine.PublishUpdateAsk). So an ask that finds no live holder on the role's topic
// (notify.ErrNoHolder) leaves the claim unasked, a later sweep asks it again, and the round ends
// once it answers, long before its bound; an ask that reached a session is the only one its role
// gets, whatever other row of the round runs before or after it.
//
// These cases drive the pod-update rig of podupdate_test.go, whose role-message lane runs each ask
// row the sweep wrote through the outbox's own executor, against a listener that refuses a topic
// no live session holds as the real one does (holderPublisher, notice_routing_test.go).

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/notify"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/store/migrations"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/wait"
)

// updateAskCutover is the migration that clears the ask mark an earlier release wrote when it
// queued an ask still queued at the deploy (store's 20261117200337).
const updateAskCutover = 20261117200337

// migrate runs the embedded migration of version over the rig's database again, as the deploy's
// Migrate runs it once over a database the earlier release left. The rig's database was built
// from every embedded migration before any claim was seeded, so this is the one run that finds
// the state the migration is for.
func (r *podUpdateRig) migrate(version int) {
	r.t.Helper()
	all, err := migrations.All()
	if err != nil {
		r.t.Fatalf("read the embedded migrations: %v", err)
	}
	for _, migration := range all {
		if migration.Version == version {
			if _, err := r.pool.Exec(r.ctx, migration.SQL); err != nil {
				r.t.Fatalf("run %s: %v", migration.Name, err)
			}
			return
		}
	}
	r.t.Fatalf("no embedded migration has version %d", version)
}

// A deploy stops the earlier release with an ask it queued, and marked asked at the enqueue, still
// in the outbox. This release reads the mark as an ask that reached a session, so its row would
// publish nothing. The cutover migration clears that mark, so after the deploy the row publishes
// the ask, which reaches the session and marks the claim, and no sweep asks again: the role reads
// the ask once.
//
// The negative control is the same deploy without the migration: the row finds the earlier
// release's mark and publishes nothing, so the role is never asked and the round waits for its
// bound. It is run by leaving the migrate call out.
func TestPodUpdateAskTheEarlierReleaseQueuedAndMarkedIsDeliveredOnceAfterTheCutover(t *testing.T) {
	earlier := readyWorker(claim.RoleImplementer)
	earlier.round, earlier.reasons = podUpdateRound, podUpdateDrift
	rig := newPodUpdateRig(t, earlier)
	if c := rig.claimOf(claim.RoleImplementer); c.UpdateTaskedAt == nil {
		t.Fatalf("the implementer holds no ask mark, want the earlier release's mark written at the enqueue")
	}
	if err := rig.s.enqueue(rig.ctx, podUpdateIssue, record.UpdateAsk{
		Role: claim.RoleImplementer, Round: podUpdateRound, Pod: podUpdatePod,
		Text: supervise.UpdateTask(podUpdateRound, podUpdateDrift, podUpdateBound),
	}); err != nil {
		t.Fatalf("queue the earlier release's ask: %v", err)
	}

	rig.migrate(updateAskCutover)
	next := rig.restart()
	next.drifts(podUpdateDrift)
	next.runLane()
	if n := next.deliveredTo(claim.RoleImplementer); n != 1 {
		t.Fatalf("the implementer's topic was delivered %d asks after the deploy, want the queued one", n)
	}
	if c := next.claimOf(claim.RoleImplementer); c.UpdateTaskedAt == nil {
		t.Fatalf("the implementer records no ask after its queued ask was delivered, want it marked")
	}
	for range 3 {
		next.clock.advance(time.Minute)
		next.tick()
		next.runLane()
	}
	if n := next.deliveredTo(claim.RoleImplementer); n != 1 {
		t.Errorf("the implementer's topic was delivered %d asks, want one in all", n)
	}
	if n := next.askedOf(claim.RoleImplementer); n != 1 {
		t.Errorf("the implementer was asked %d times, want the one the earlier release queued", n)
	}
}

// deliveredTo is how many asks the rig's listener delivered to role's topic.
func (r *podUpdateRig) deliveredTo(role claim.Role) int {
	r.listener.mu.Lock()
	defer r.listener.mu.Unlock()
	n := 0
	for _, publish := range r.listener.delivered {
		if publish.topic == r.roleTopic(role) {
			n++
		}
	}
	return n
}

// askedOf is how many update-ask rows the sweep has written for role, finished or not.
func (r *podUpdateRig) askedOf(role claim.Role) int {
	r.t.Helper()
	n := 0
	for _, ask := range r.asks() {
		if ask.Role == role {
			n++
		}
	}
	return n
}

// A round opens over two ready roles. The implementer's fresh session has not claimed its topic
// yet, so its first ask finds no holder and ends undelivered; the tester's first ask is delivered.
// The ask that reached no session leaves the implementer unasked, so a later sweep asks it again,
// once its session holds the topic, and that ask is delivered. Each role then answers, and the
// pod is replaced minutes after the round opened, long before its thirty-minute bound and with no
// bound named on the replacement's line.
//
// The control is the tester: its first ask reached a session, so it is marked and asked once over
// every sweep of the round. The implementer is asked twice, and no more once its second ask was
// delivered.
//
// The negative control is the rule of main before this change, BET-1291's planner and BET-690's
// reviewer on 2026-11-17: the sweep marked the claim asked when it enqueued the ask, the row that
// found no holder finished, and no sweep asked again, so the round waited for its bound.
func TestPodUpdateAskThatFoundNoHolderIsAskedAgainAndTheRoundEndsLongBeforeItsBound(t *testing.T) {
	rig := newPodUpdateRig(t, readyWorker(claim.RoleImplementer), readyWorker(claim.RoleTester))
	rig.drifts(podUpdateDrift)
	clocks := rig.replacementClocks()
	rig.listener.setAbsent(rig.roleTopic(claim.RoleImplementer))

	rig.tick()
	rig.awaitAskQueued(claim.RoleImplementer, podUpdateRound)
	rig.awaitAskQueued(claim.RoleTester, podUpdateRound)
	rig.runLane()

	lost := loggedLines(t, rig.logs.String(), `"msg":"`+askNoHolderLine+`"`)
	if len(lost) == 0 {
		t.Fatalf("no line %q in\n%s\nwant the implementer's ask finished undelivered", askNoHolderLine, rig.logs.String())
	}
	for _, line := range lost {
		if line["role"] != string(claim.RoleImplementer) {
			t.Fatalf("line %q = %v, want it for the implementer's ask alone", askNoHolderLine, line)
		}
	}
	if c := rig.claimOf(claim.RoleImplementer); c.UpdateTaskedAt != nil {
		t.Fatalf("the implementer's ask reached no session, and its claim records it asked at %v; want it unasked, so a later sweep asks it again", c.UpdateTaskedAt)
	}
	if c := rig.claimOf(claim.RoleTester); c.UpdateTaskedAt == nil {
		t.Fatalf("the tester's ask was delivered, and its claim records no ask; want it marked")
	}

	// The implementer's session now holds its topic, as a ready session does inside its first
	// turn or at the first-turn bound (supervise.SaidReady).
	rig.listener.setAbsent()
	rig.clock.advance(time.Minute)
	rig.awaitAsked(claim.RoleImplementer, podUpdateRound)
	for range 3 {
		rig.clock.advance(time.Minute)
		rig.tick()
		rig.runLane()
	}

	if n := rig.askedOf(claim.RoleImplementer); n != 2 {
		t.Errorf("the implementer was asked %d times, want twice: once lost, once delivered", n)
	}
	if n := rig.askedOf(claim.RoleTester); n != 1 {
		t.Errorf("the tester was asked %d times, want once: its first ask was delivered", n)
	}
	for _, role := range []claim.Role{claim.RoleImplementer, claim.RoleTester} {
		if n := rig.deliveredTo(role); n != 1 {
			t.Errorf("the %s's topic was delivered %d asks, want one", role, n)
		}
	}

	rig.signal(claim.RoleImplementer, "a1b2c3d")
	rig.signal(claim.RoleTester, "")
	rig.awaitReplacement(1, "once both roles answered")
	at := clocks()
	if len(at) != 1 || !at[0].Before(podUpdateStart.Add(10*time.Minute)) {
		t.Fatalf("the pod was replaced at %v, want once, minutes after the round opened at %s and long before its bound at %s",
			at, podUpdateStart, podUpdateBound)
	}
	if ended := loggedLines(t, rig.logs.String(), podUpdateReplacedLine); len(ended) != 1 || ended[0]["atTheBound"] != false {
		t.Fatalf("the round's end was logged %+v, want one line that no bound made", ended)
	}
}

// The row can run as soon as the enqueue commits, before the sweep takes its next step. Here the
// lane runs it inside the enqueue itself, and the role's topic has no holder: the ask is lost,
// and nothing the sweep does after the enqueue may count it as given. The next sweep asks the
// claim again, and that ask, once the session holds the topic, is delivered and marks the claim.
//
// The negative control is a mark made after the enqueue, which is where the sweep made it: on
// main the lost row finished and the sweep's mark stood, and with the take-back of 74ab1cd3 the
// row found no mark to take back, the sweep's mark landed after it, and no sweep asked again.
func TestPodUpdateAskThatFoundNoHolderBeforeTheSweepMovedOnIsAskedAgain(t *testing.T) {
	rig := newPodUpdateRig(t, readyWorker(claim.RoleImplementer))
	rig.drifts(podUpdateDrift)
	rig.listener.setAbsent(rig.roleTopic(claim.RoleImplementer))
	enqueue := rig.s.enqueue
	rig.s.enqueue = func(ctx context.Context, issue string, payloads ...record.OutboxPayload) error {
		if err := enqueue(ctx, issue, payloads...); err != nil {
			return err
		}
		for _, payload := range payloads {
			if _, ask := payload.(record.UpdateAsk); ask {
				rig.runLane()
				break
			}
		}
		return nil
	}

	rig.tick()
	rig.awaitRound(claim.RoleImplementer, podUpdateRound)
	rig.tick()
	if n := rig.askedOf(claim.RoleImplementer); n != 1 {
		t.Fatalf("the sweep after the round recorded itself asked the implementer %d times, want once", n)
	}
	if n := rig.deliveredTo(claim.RoleImplementer); n != 0 {
		t.Fatalf("the implementer's topic was delivered %d asks while no session held it, want none", n)
	}
	if c := rig.claimOf(claim.RoleImplementer); c.UpdateTaskedAt != nil {
		t.Fatalf("the implementer's ask was lost before the sweep moved on, and its claim records it asked at %v; want it unasked", c.UpdateTaskedAt)
	}

	rig.listener.setAbsent()
	rig.clock.advance(time.Minute)
	rig.awaitAsked(claim.RoleImplementer, podUpdateRound)
	for range 3 {
		rig.clock.advance(time.Minute)
		rig.tick()
	}
	if n := rig.askedOf(claim.RoleImplementer); n != 2 {
		t.Errorf("the implementer was asked %d times, want twice: once lost, once delivered", n)
	}
	if n := rig.deliveredTo(claim.RoleImplementer); n != 1 {
		t.Errorf("the implementer's topic was delivered %d asks, want one", n)
	}
}

// Two rows of one round for one claim: an older row and a newer one, both queued before the
// sweep asks — what a daemon before this change could leave, a row whose mark the store refused
// beside the row the next sweep wrote for the same claim. Whichever order they meet a holder in,
// the role is delivered one ask: the row that reaches the session marks the claim under its lock,
// the other finds the mark and publishes nothing, and an older row that found no holder takes
// nothing from the newer one. The sweep asks no claim while an ask of it waits in the outbox, and
// none once an ask reached it.
//
// The negative control is 74ab1cd3's take-back: the older row that found no holder cleared the
// mark the sweep had made for the newer row, the newer row's delivery left the claim unmarked,
// and the next sweep asked the session again. On main, with no take-back, every queued row
// publishes, and a session the first row reached is asked by the second as well.
func TestPodUpdateAskNoHolderOfAnOlderRowLeavesOneDeliveredAskOfTheRound(t *testing.T) {
	for _, tc := range []struct {
		name string
		// unheld is whether the older row runs while the role's topic has no holder.
		unheld bool
	}{
		{name: "the older row finds no holder, and the newer row the session", unheld: true},
		{name: "both rows find the session", unheld: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newPodUpdateRig(t, readyWorker(claim.RoleImplementer))
			rig.drifts(podUpdateDrift)
			rig.tick()
			rig.awaitRound(claim.RoleImplementer, podUpdateRound)
			text := supervise.UpdateTask(podUpdateRound, podUpdateDrift, podUpdateBound)
			for range 2 {
				if err := rig.s.enqueue(rig.ctx, podUpdateIssue, record.UpdateAsk{
					Role: claim.RoleImplementer, Round: podUpdateRound, Pod: podUpdatePod, Text: text,
				}); err != nil {
					t.Fatalf("queue a row of the round: %v", err)
				}
			}
			rig.tick()
			rows := rig.rows(record.OutboxKindUpdateAsk)
			if len(rows) < 2 {
				t.Fatalf("the outbox holds %d asks, want the two queued rows", len(rows))
			}

			if tc.unheld {
				rig.listener.setAbsent(rig.roleTopic(claim.RoleImplementer))
			}
			rig.runAsk(rows[0])
			rig.listener.setAbsent()
			rig.runLane()
			rig.awaitAsked(claim.RoleImplementer, podUpdateRound)
			for range 3 {
				rig.clock.advance(time.Minute)
				rig.tick()
				rig.runLane()
			}

			if n := rig.deliveredTo(claim.RoleImplementer); n != 1 {
				t.Fatalf("the implementer's topic was delivered %d asks of the round, want one", n)
			}
			if c := rig.claimOf(claim.RoleImplementer); c.UpdateTaskedAt == nil {
				t.Fatalf("the implementer records no ask, want the delivered ask marked")
			}
			if n := rig.askedOf(claim.RoleImplementer); n != 2 {
				t.Errorf("the implementer was asked %d times, want the two queued rows alone: the sweep asks no claim an ask of the round reached or waits for", n)
			}
		})
	}
}

// The ask is published and marked under the claim's own lock, and that lock is taken without
// waiting (supervise's Machine.PublishUpdateAsk). An issue's role-message lane runs one row at a
// time (record.LaneKey), and a Sandbox relaunch holds a claim's lock for minutes while the runtime
// waits out its pod, so a row that waited for that lock would hold every phase-ended message, hook
// prompt and ask of that issue behind one relaunch. Here the claim's machine is held inside a
// Resume: the row returns a wait at once, having published nothing and marked nothing, and the
// row's next attempt once the relaunch is over publishes the ask, which is delivered once in all.
//
// The negative control is a row that takes the lock with waiting: the run below does not return
// while the relaunch is held inside its Resume. On 74ab1cd3, which published with no lock held,
// the row publishes while the relaunch holds the claim.
func TestPodUpdateAskWaitsWhileItsClaimsLockIsHeldAndIsDeliveredOnce(t *testing.T) {
	rig := newPodUpdateRig(t, readyWorker(claim.RoleImplementer))
	rig.drifts(podUpdateDrift)
	rig.tick()
	row := rig.awaitAskQueued(claim.RoleImplementer, podUpdateRound)
	rig.settleAll()

	// The implementer's machine is held inside a Resume: it holds its own lock for the whole
	// call, as a relaunch into a pod being built does.
	inside, held := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(held) })
	defer release()
	rig.rt.HoldResume(func() {
		rig.rt.HoldResume(nil)
		close(inside)
		<-held
	})
	m, _ := rig.sup.Machine(rig.tokens[claim.RoleImplementer])
	gone := supervise.RuntimeObservation{Observation: runtime.Observation{
		Locator: *rig.claimOf(claim.RoleImplementer).Locator, Kind: runtime.Gone, Detail: "the process ended",
	}}
	go func() { _ = m.Handle(rig.ctx, gone) }()
	select {
	case <-inside:
	case <-time.After(10 * time.Second):
		t.Fatal("the relaunch never reached its Resume")
	}

	ran := make(chan error, 1)
	go func() { ran <- rig.lane.execute(rig.ctx, row) }()
	select {
	case err := <-ran:
		if !errors.Is(err, wait.ErrWaiting) {
			t.Fatalf("the ask over a held claim = %v, want a wait that keeps the row", err)
		}
		t.Logf("the row waits: %v", err)
	case <-time.After(10 * time.Second):
		release()
		<-ran
		t.Fatal("the ask did not return while its claim's machine was inside a Resume; it waited on the lock")
	}
	if n := rig.deliveredTo(claim.RoleImplementer); n != 0 {
		t.Fatalf("the ask was delivered %d times while the claim's lock was held, want none: the publish and its mark are one step under that lock", n)
	}
	if v := m.View(); v.UpdateTaskedAt != nil {
		t.Fatalf("the claim records an ask at %v while its lock was held, want none", v.UpdateTaskedAt)
	}

	release()
	rig.runAsk(row)
	if c := rig.claimOf(claim.RoleImplementer); c.UpdateTaskedAt == nil {
		t.Fatalf("the claim records no ask after the row's next attempt, want the delivered ask marked")
	}
	for range 3 {
		rig.clock.advance(time.Minute)
		rig.tick()
		rig.runLane()
	}
	if n := rig.deliveredTo(claim.RoleImplementer); n != 1 {
		t.Errorf("the implementer's topic was delivered %d asks, want one in all", n)
	}
	if n := rig.askedOf(claim.RoleImplementer); n != 1 {
		t.Errorf("the implementer was asked %d times, want once", n)
	}
}

// A round publishes its ask at most once to each claim it asks (supervise's Claim.OwesUpdateAsk),
// and the answer a role gives stands for its round. So an ask the sweep queued before the role
// answered, and that the outbox runs after the answer, reaches nobody and marks nothing: the
// answer is what the ask was for. The same holds for a claim that has left the round's pod, which
// the daemon started again in another pod while its ask waited: the round asks such a claim
// nothing, as the sweep asks it nothing. In both cases the row ends with nothing marked, because
// no ask reached the role.
//
// There are two controls. The ask that runs with nothing between the enqueue and the row is
// delivered once and marked, and so is the ask of a claim started again in the round's own pod,
// which is still in the round.
func TestPodUpdateAskQueuedBeforeTheClaimNeededNoneReachesNobody(t *testing.T) {
	for _, tc := range []struct {
		name      string
		between   func(*podUpdateRig)
		delivered int
	}{
		{name: "the control: nothing happens between the enqueue and the row", between: func(*podUpdateRig) {}, delivered: 1},
		{name: "the role answers before the row runs", between: func(rig *podUpdateRig) {
			rig.signal(claim.RoleImplementer, "c0ffee")
		}, delivered: 0},
		{name: "the control: the role is started again in the round's own pod before the row runs", between: func(rig *podUpdateRig) {
			rig.displacedInto(claim.RoleImplementer, podUpdatePod)
		}, delivered: 1},
		{name: "the role is started again in another pod before the row runs", between: func(rig *podUpdateRig) {
			rig.displacedInto(claim.RoleImplementer, podUpdateReplacementPod)
		}, delivered: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newPodUpdateRig(t, readyWorker(claim.RoleImplementer))
			rig.drifts(podUpdateDrift)
			rig.tick()
			rig.awaitRound(claim.RoleImplementer, podUpdateRound)
			row := rig.awaitAskQueued(claim.RoleImplementer, podUpdateRound)
			rig.settleAll()
			tc.between(rig)
			if c := rig.claimOf(claim.RoleImplementer); c.UpdateRound != podUpdateRound || c.UpdateTaskedAt != nil {
				t.Fatalf("the implementer before its row runs = %+v, want it in the round and unasked", c)
			}

			rig.runAsk(row)

			if n := rig.deliveredTo(claim.RoleImplementer); n != tc.delivered {
				t.Fatalf("the implementer's topic was delivered %d asks, want %d", n, tc.delivered)
			}
			var marked *time.Time
			if err := rig.pool.QueryRow(rig.ctx, "select update_tasked_at from claims where token = $1",
				string(rig.tokens[claim.RoleImplementer])).Scan(&marked); err != nil {
				t.Fatalf("read the implementer's stored claim: %v", err)
			}
			if asked := rig.claimOf(claim.RoleImplementer).UpdateTaskedAt; (asked != nil) != (tc.delivered == 1) || (marked != nil) != (tc.delivered == 1) {
				t.Fatalf("the implementer records an ask at %v, stored %v; want a mark only where the ask was delivered", asked, marked)
			}
		})
	}
}

// A claim can fail for real while the ask the sweep queued for it waits in the outbox, and its
// process can run on: its turn ends in a provider error that holds the issue (HTTP 400) while the
// runtime refuses the suspension fail asks for, so the listener still holds the role's topic. The
// row publishes nothing and marks nothing, and so does the same row run by a second daemon, which
// restores the failed claim from the store with nothing of the first daemon's memory. The control
// is the same turn ending in an error that clears (HTTP 503): the claim does not fail, and the row
// is delivered once and marked, by either daemon.
func TestPodUpdateAskQueuedBeforeTheClaimFailedReachesNobodyAcrossARestart(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		failed    bool
		restart   bool
		delivered int
	}{
		{name: "the control: a turn that ends in an error that clears", status: 503, delivered: 1},
		{name: "a claim that failed with its suspension refused", status: 400, failed: true},
		{name: "the control: a turn that ends in an error that clears, then a restart", status: 503, restart: true, delivered: 1},
		{name: "a claim that failed with its suspension refused, then a restart", status: 400, failed: true, restart: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newPodUpdateRig(t, readyWorker(claim.RoleImplementer))
			rig.drifts(podUpdateDrift)
			rig.tick()
			rig.awaitRound(claim.RoleImplementer, podUpdateRound)
			row := rig.awaitAskQueued(claim.RoleImplementer, podUpdateRound)
			rig.settleAll()

			token := rig.tokens[claim.RoleImplementer]
			rig.rt.FailSuspend(errors.New("the runtime refused the suspension"))
			rig.handle(claim.RoleImplementer, supervise.RequestDeliver{Claim: token, Task: "implement the plan",
				ID: "outbox:4801", Phase: phase.Implementing, Generation: 1})
			rig.settle(claim.RoleImplementer)
			rig.handle(claim.RoleImplementer, supervise.StreamTurnStart{Claim: token})
			rig.settle(claim.RoleImplementer)
			sent := rig.workers[claim.RoleImplementer].Prompts()
			if len(sent) == 0 || rig.claimOf(claim.RoleImplementer).Pending == nil {
				t.Fatalf("the implementer was sent %+v and holds %+v; want its task sent and held, or this case shows nothing",
					sent, rig.claimOf(claim.RoleImplementer).Pending)
			}
			rig.handle(claim.RoleImplementer, supervise.StreamTurnEnd{Claim: token})
			rig.handle(claim.RoleImplementer, supervise.StreamPromptResult{Claim: token, DeliveryID: sent[len(sent)-1].DeliveryID,
				AgentInvoked: true, Error: "the provider answered an error", HTTPStatus: tc.status})
			rig.settleAll()
			c := rig.claimOf(claim.RoleImplementer)
			if c.ProviderErrorRun == 0 {
				t.Fatalf("the implementer is %s with no provider error counted: the turn's result was not taken, so this case shows nothing", c.State)
			}
			if (c.State == supervise.StateFailed) != tc.failed || c.UpdateRound != podUpdateRound || c.UpdateTaskedAt != nil {
				t.Fatalf("the implementer before its row runs is %s in round %q, marked %v; want failed=%t, still in round %s, unasked",
					c.State, c.UpdateRound, c.UpdateTaskedAt, tc.failed, podUpdateRound)
			}
			if tc.failed && len(rig.rt.CallsOf("Suspend")) == 0 {
				t.Fatal("the failure asked the runtime for no suspension, so this case shows nothing")
			}

			drains := rig
			if tc.restart {
				drains = rig.restart()
				if c := drains.claimOf(claim.RoleImplementer); (c.State == supervise.StateFailed) != tc.failed || c.UpdateRound != podUpdateRound {
					t.Fatalf("the implementer after the restart is %s in round %q; want failed=%t in round %s", c.State, c.UpdateRound, tc.failed, podUpdateRound)
				}
			}
			drains.runAsk(row)

			if n := drains.deliveredTo(claim.RoleImplementer); n != tc.delivered {
				t.Fatalf("the implementer's topic was delivered %d asks, want %d", n, tc.delivered)
			}
			var marked *time.Time
			if err := drains.pool.QueryRow(drains.ctx, "select update_tasked_at from claims where token = $1", string(token)).Scan(&marked); err != nil {
				t.Fatalf("read the implementer's stored claim: %v", err)
			}
			if (marked != nil) != (tc.delivered == 1) {
				t.Fatalf("the stored ask mark = %v, want a mark only where the ask was delivered", marked)
			}
		})
	}
}

// The sweep asks no claim that left the round's pod, at any sweep after it left. Two roles stand
// in the round's pod. The implementer is started again in another pod, which leaves the round
// (supervise's leftPodRoundAt writes that once, and no later launch takes it back), and then
// started again in the round's own pod, where the tester still holds the round open. While the
// issue's claims record two pods the sweep asks nobody, as a replacement in flight, so only the
// move back puts the left claim in front of the sweep's ask. Over three later sweeps, with the
// lane run after each, the implementer's rows of the round stay at the one the sweep queued
// before it left.
//
// The negative control is the sweep with its left-pod case taken out (unaskedClaims asking a
// claim that OwesUpdateAsk refuses as left): it queues the implementer a row of the round at each
// of the three sweeps. Without the move back, this case passes with that case taken out.
func TestPodUpdateNoSweepAsksAClaimThatLeftTheRoundsPod(t *testing.T) {
	rig := newPodUpdateRig(t, readyWorker(claim.RoleImplementer), readyWorker(claim.RoleTester))
	rig.drifts(podUpdateDrift)
	rig.tick()
	rig.awaitRound(claim.RoleImplementer, podUpdateRound)
	rig.awaitAskQueued(claim.RoleImplementer, podUpdateRound)
	rig.settleAll()
	rig.displacedInto(claim.RoleImplementer, podUpdateReplacementPod)
	rig.runLane()
	rig.displacedInto(claim.RoleImplementer, podUpdatePod)
	for range 3 {
		rig.clock.advance(time.Minute)
		rig.tick()
		rig.runLane()
	}
	if c := rig.claimOf(claim.RoleImplementer); c.UpdateRound != podUpdateRound || !c.LeftPodRound(podUpdateRound) {
		t.Fatalf("the implementer at the end = round %q, left %q; want it still in round %s and left, or this case shows nothing", c.UpdateRound, c.UpdateLeftRound, podUpdateRound)
	}
	n := 0
	for _, ask := range rig.asks() {
		if ask.Role == claim.RoleImplementer && ask.Round == podUpdateRound {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("the sweep queued the implementer %d asks of round %s, want only the one before it left the pod", n, podUpdateRound)
	}
}

// The sweep owes a claim an ask where the claim's own record owes it one (supervise's
// Claim.OwesUpdateAsk): a claim of the round with a session is owed its ask until an ask of the
// round reached its session or its role answered the round, and a failed claim, one that has left
// the round's pod and a claim in another round are owed none. This is the list before the sweep
// asks whether the claim's agent said it is ready (askRound's supervise.SaidReady), which drops a
// failed claim as it drops every claim that is not ready, so the rule's failed case taken out
// shows here and in no sweep that runs. The control is the ready claim with none of these, which
// the sweep owes its ask.
//
// The negative control is the rule with its failed case taken out: the failed claim is then on
// the list, and its case fails. This test holds each case of the rule as the sweep reads it, and
// the next one (TestPodUpdateSweepOwesAnAskExactlyWhereTheRuleDoes) holds, over the facts of a
// claim it names, that the sweep refuses and owes no claim by a statement of its own that the
// rule does not make: a sweep that dropped failed claims on its own before it read the rule passes
// this test under that same mutant, and fails the next one.
func TestPodUpdateSweepOwesAnAskWhereTheClaimsOwnRecordOwesOne(t *testing.T) {
	at := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		set  func(*supervise.Claim)
		owed bool
	}{
		{name: "the control: a ready claim of the round with no ask and no answer is owed its ask", set: func(*supervise.Claim) {}, owed: true},
		{name: "a claim an ask of the round reached is owed none", set: func(c *supervise.Claim) { c.UpdateTaskedAt = &at }},
		{name: "a claim that answered the round is owed none", set: func(c *supervise.Claim) { c.UpdateSignalledAt = &at }},
		{name: "a claim that left the round's pod is owed none", set: func(c *supervise.Claim) { c.UpdateLeftRound = podUpdateRound }},
		{name: "a claim that failed is owed none", set: func(c *supervise.Claim) { c.State = supervise.StateFailed }},
		{name: "a claim in another round is owed none", set: func(c *supervise.Claim) { c.UpdateRound = "pod:" + podUpdateReplacementPod }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := supervise.Claim{Token: "legion-legion-implementer", Role: claim.RoleImplementer, Session: "ses_i",
				State: supervise.StateReady, UpdateRound: podUpdateRound}
			tc.set(&c)
			asking := unaskedClaims([]supervise.Claim{c}, podUpdateRound)
			if owed := len(asking) == 1 && asking[0].Token == c.Token; owed != tc.owed || len(asking) > 1 {
				t.Fatalf("the sweep owes an ask to %d claims of one %s claim in round %q, asked %v, answered %v, left %q; want the claim owed=%t",
					len(asking), c.State, c.UpdateRound, c.UpdateTaskedAt, c.UpdateSignalledAt, c.UpdateLeftRound, tc.owed)
			}
		})
	}
}

// sweepFacts is the claims the sweep tests below hold the sweep to the rule over: every state a
// claim can be in (supervise.States), every role a claim can be on (claim.Roles, and the
// controller's, which is none of them), and each of a session, a delivered ask, an answer, a leave
// of the round's pod, another round and a process locator, set or not, which is 64 claims for each
// state and role, in that order. Each claim carries token(role) and records the round's opening,
// as the one write of a round on a claim does (supervise's openPodUpdate), so the sweep's ask has
// a bound to state (roundBound). A state or a role added to those lists joins the tests without an
// edit.
func sweepFacts(at time.Time, token func(claim.Role) claim.Token) []supervise.Claim {
	var claims []supervise.Claim
	for _, state := range supervise.States() {
		for _, role := range sweepRoles() {
			for bits := range 1 << 6 {
				opened := at
				c := supervise.Claim{Token: token(role), Role: role, State: state, UpdateRound: podUpdateRound,
					UpdateOpenedAt: &opened}
				if bits&1 != 0 {
					c.Session = "ses_i"
				}
				if bits&2 != 0 {
					c.UpdateTaskedAt = &at
				}
				if bits&4 != 0 {
					c.UpdateSignalledAt = &at
				}
				if bits&8 != 0 {
					c.UpdateLeftRound = podUpdateRound
				}
				if bits&16 != 0 {
					c.UpdateRound = "pod:" + podUpdateReplacementPod
				}
				if bits&32 != 0 {
					c.Locator = &runtime.Locator{Claim: c.Token, Incarnation: "inc-1"}
				}
				claims = append(claims, c)
			}
		}
	}
	return claims
}

// sweepRoles is every role a claim can be on: claim.Roles, and the controller's.
func sweepRoles() []claim.Role { return append(slices.Clone(claim.Roles), claim.RoleController) }

// sweepRig drives the sweep's whole path to the ask over claims a test builds: askRound, which
// reads the sweep's list (unaskedClaims), keeps each claim of it whose agent said it is ready and
// that a machine of this daemon holds, reads the round's asks still queued, and enqueues one ask
// for each claim it keeps. It is the pod-update rig with a ready worker of every workflow role, so
// a machine holds the claim of each role's token, and the controller's token, like any token of no
// issue, is held by none. The round's enqueue is recorded rather than written: a test puts groups
// to askRound hundreds of thousands of times, and the outbox's row and its publish are the other
// tests' of this file. With nothing written, no ask of the round is ever queued, so each group is
// asked afresh.
type sweepRig struct {
	rig   *podUpdateRig
	asked []record.UpdateAsk
}

func newSweepRig(t *testing.T) *sweepRig {
	t.Helper()
	workers := make([]podUpdateRole, 0, len(claim.Roles))
	for _, role := range claim.Roles {
		workers = append(workers, readyWorker(role))
	}
	r := &sweepRig{rig: newPodUpdateRig(t, workers...)}
	r.rig.s.log = quietLogger()
	r.rig.s.enqueue = func(_ context.Context, issue string, rows ...record.OutboxPayload) error {
		for _, row := range rows {
			ask, ok := row.(record.UpdateAsk)
			if !ok || issue != podUpdateIssue {
				t.Fatalf("the sweep enqueued %T on %s, want only update asks of %s", row, issue, podUpdateIssue)
			}
			r.asked = append(r.asked, ask)
		}
		return nil
	}
	return r
}

// token is the token of role's claim: the rig's for a workflow role, and for the controller one no
// machine of the rig holds.
func (r *sweepRig) token(role claim.Role) claim.Token {
	if token, ok := r.rig.tokens[role]; ok {
		return token
	}
	return claim.Token("legion-legion-" + string(role))
}

// hold puts group, which holds one claim of a role at most, to the sweep's whole path, and
// reports each claim of it the sweep treats otherwise than the rule. The sweep's list
// (unaskedClaims) must hold a claim exactly where it has a session and its own record owes it an
// ask (supervise's Claim.OwesUpdateAsk), and askRound must enqueue one ask of the round for a claim
// exactly where the list holds it, its agent said it is ready (supervise.SaidReady) and a machine
// of this daemon holds it. asked is how many claims of the group are owed an ask that way.
func (r *sweepRig) hold(group []supervise.Claim) (asked int, wrong []string) {
	listed := unaskedClaims(group, podUpdateRound)
	r.asked = r.asked[:0]
	r.rig.s.askRound(r.rig.ctx, podUpdateIssue, group, podUpdateRound, podUpdatePod)
	for _, c := range group {
		owed, why := c.OwesUpdateAsk(podUpdateRound)
		wantListed := c.Session != "" && owed
		_, held := r.rig.sup.Machine(c.Token)
		wantAsked := wantListed && supervise.SaidReady(c.State) && held
		gotListed, gotAsked := 0, 0
		for _, l := range listed {
			if l.Token == c.Token {
				gotListed++
			}
		}
		for _, a := range r.asked {
			if a.Role == c.Role && a.Round == podUpdateRound {
				gotAsked++
			}
		}
		if wantAsked {
			asked++
		}
		if (gotListed == 1) != wantListed || gotListed > 1 || (gotAsked == 1) != wantAsked || gotAsked > 1 {
			wrong = append(wrong, fmt.Sprintf("%s: the sweep's list holds it %d times and the sweep asks it %d times, where the rule says owed=%t (%v), so want it listed %t and asked %t",
				describeSwept(c), gotListed, gotAsked, owed, why, wantListed, wantAsked))
		}
	}
	for _, a := range r.asked {
		if a.Round != podUpdateRound || !slices.ContainsFunc(group, func(c supervise.Claim) bool { return c.Role == a.Role }) {
			wrong = append(wrong, fmt.Sprintf("the sweep asked %s in round %q, which is no claim of the group's round", a.Role, a.Round))
		}
	}
	return asked, wrong
}

func describeSwept(c supervise.Claim) string {
	return fmt.Sprintf("a %s %s claim, session %q, locator %t, asked %v, answered %v, left %q, in round %q, pending task %t, told %t",
		c.State, c.Role, c.Session, c.Locator != nil, c.UpdateTaskedAt != nil, c.UpdateSignalledAt != nil, c.UpdateLeftRound,
		c.UpdateRound, c.Pending != nil, c.UpdateToldAt != nil)
}

// sweepGroupSizes is each group a claim is put to the sweep in: alone, beside one claim of another
// role, and beside one claim of every other role, which is an issue pod with all its roles.
var sweepGroupSizes = []struct {
	name string
	size int
}{{"alone", 1}, {"beside a claim of another role", 2}, {"beside a claim of every other role", len(claim.Roles) + 1}}

// sweepRoleCount and sweepStateCount are how many roles and states sweepFacts tries, so a claim's
// index in it says its state, its role and its mix of facts.
var sweepRoleCount, sweepStateCount = len(sweepRoles()), len(supervise.States())

// sweepGroup puts c, which stands at index i of facts (sweepFacts), into dst as a group of size
// claims, and returns the group. Each other claim is taken from others, which is facts or facts
// with one more field set: it is of another role than c, and its state and its facts are another
// mix than c's, chosen by i, so that over the claims of facts every claim stands beside every kind
// of claim. vary, where it is not nil, is applied to the k-th other claim. c stands at index i mod
// size of the group.
func sweepGroup(dst, others []supervise.Claim, i, size int, c supervise.Claim, vary func(other *supervise.Claim, k int)) []supervise.Claim {
	bits, role, state := i%64, i/64%sweepRoleCount, i/64/sweepRoleCount
	dst = dst[:0]
	for k := 1; k < size; k++ {
		dst = append(dst, others[((state+3*k)%sweepStateCount*sweepRoleCount+(role+k)%sweepRoleCount)*64+(bits+17*k)%64])
		if vary != nil {
			vary(&dst[len(dst)-1], k)
		}
	}
	return slices.Insert(dst, i%size, c)
}

// The sweep's whole path to the ask asks a claim exactly where the claim has a session, its own
// record owes it an ask (supervise's Claim.OwesUpdateAsk), its agent said it is ready and a
// machine of this daemon holds it (sweepRig.hold), over every mix of the facts of a claim
// sweepFacts names, and whatever the other claims of its issue hold. Each claim is put to the sweep
// alone, beside a claim of another role and beside a claim of every other role (sweepGroup), and
// each other claim of a group holds one more field of the claim, a field at any depth, chosen in
// turn (claimLeaves). Every claim of every group is held to the rule.
//
// With the test above it holds the one rule of whom a round asks, which is neither a failed claim
// nor one that has left the round's pod: that test fails when the rule loses a case, and this one
// fails when the sweep refuses or owes a claim by a statement of its own that the rule does not
// make. That statement can stand in the sweep's list or in askRound, which filters the list again
// before it enqueues the ask, and it can read the claim or the group: a filter of its own on one
// state, on one role or on a locator present or absent, its own failed filter put back while the
// rule's failed case is taken out, its left-pod check taken out, or a filter on a pending task
// that holds only where the group has more than one claim. The test after it holds the same over
// every field of a claim. None of them fails for a statement of the sweep's own that agrees with
// the rule in every group, such as that failed filter put back with the rule whole: it changes no
// claim's answer, and it shows here the first time the rule changes and the two disagree.
//
// The negative control is the sweep's own failed filter put back with the rule's failed case taken
// out: this test then fails on each failed claim with a session that the rule owes an ask, and the
// test above passes. The test also fails when it saw no claim owed an ask or no claim refused one,
// since such a set shows nothing.
func TestPodUpdateSweepOwesAnAskExactlyWhereTheRuleDoes(t *testing.T) {
	r := newSweepRig(t)
	at := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	facts := sweepFacts(at, r.token)
	leaves, unset := claimLeaves()
	if len(unset) > 0 || len(leaves) == 0 {
		t.Fatalf("the claim holds fields this test cannot set (%v), or none it can (%d), so it cannot vary the claims beside each claim", unset, len(leaves))
	}
	checked, owed, refused := 0, 0, 0
	buf := make([]supervise.Claim, 0, sweepRoleCount)
	for _, g := range sweepGroupSizes {
		wrong, seen, example := 0, 0, ""
		for i, c := range facts {
			group := sweepGroup(buf, facts, i, g.size, c, func(other *supervise.Claim, k int) {
				if leaf := leaves[(i*7+k)%len(leaves)]; leaf.empty(*other) {
					leaf.set(other, at)
				}
			})
			asked, misses := r.hold(group)
			seen += len(group)
			owed += asked
			refused += len(group) - asked
			wrong += len(misses)
			if example == "" && len(misses) > 0 {
				example = misses[0]
			}
		}
		checked += seen
		if wrong > 0 {
			t.Errorf("%s: the sweep disagrees with the rule on %d of %d claims, for example %s", g.name, wrong, seen, example)
		}
	}
	if owed == 0 || refused == 0 {
		t.Fatalf("checked %d claims of %d groups, %d owed an ask and %d refused one: a set with no owed or no refused claim shows nothing", checked, len(facts)*len(sweepGroupSizes), owed, refused)
	}
	t.Logf("checked %d claims of %d groups: %d owed an ask, %d refused one", checked, len(facts)*len(sweepGroupSizes), owed, refused)
}

// sweepFieldsEveryClaimSets is the claim's fields that every claim of sweepFacts sets, so the test
// below can set them on no claim, each with why the sweep's tests still hold the sweep over them.
var sweepFieldsEveryClaimSets = map[string]string{
	"Token":          "a claim is known by it; each role's claim has its own, so the test above varies it with the role",
	"Role":           "the test above tries every role",
	"State":          "the test above tries every state",
	"UpdateRound":    "the test above tries the round and another round",
	"UpdateOpenedAt": "every claim of a round records the round's opening, which the ask's bound is read from",
}

// The sweep's whole path to the ask asks a claim exactly where the claim has a session, its own
// record owes it an ask (which it owes neither a failed claim nor one that has left the round's
// pod), its agent said it is ready and a machine of this daemon holds it (sweepRig.hold), whatever
// else the claim holds. Each exported field of supervise.Claim at every depth below it — through
// structs, pointers and slice elements, so each of the four budgets, each field of a pending task,
// of the handshake and of a turn's outcome — is set in turn (claimLeaves), one field at a time:
// every claim of sweepFacts that leaves that field empty is put to the sweep again with the field
// set, alone, beside a claim of another role and beside a claim of every other role, each of which
// holds the field too (sweepGroup). So a filter of the sweep's own on a field the test above does
// not vary — a pending task, a task's text or the moment a turn confirmed it, the round's told
// mark, the claim's deaths or prompt failures, the claim's issue — fails here, in the sweep's list
// and in askRound alike, on the claims with a session the rule owes an ask: a sweep that skipped
// every claim holding a task would leave a working role unasked until the round's bound, and every
// other test of the sweep passes under it. A field added to the claim joins the test without an
// edit, and a field of a type this test cannot set fails it.
//
// The test leaves out the fields every claim of sweepFacts sets (sweepFieldsEveryClaimSets: the
// token, the role, the state, the round and the round's opening), each named with why, and it
// fails when one of them is no field of the claim or is empty on some claim, and when a field it
// does not name is tried on no claim.
//
// Each field is set to one value, and one field at a time. So this test does not fail for a
// filter of the sweep's own on one value of a field other than the one it sets: a filter on a
// claim an operator suspended (supervise.SuspendedByOperator) passes it, since SuspendedBy is set
// to "x" here. Nor does it fail for a filter on two fields set together, such as a pending task on
// a claim whose round was told, since no claim here holds both; nor for a filter that agrees with
// the rule in every group, as the test above says.
//
// The negative control is a filter of the sweep's own on a pending task (Claim.Pending), in the
// sweep's list or in askRound: this test then fails with each field of Pending set, and the test
// above fails too, on the claims beside which a pending task is set. The test also fails when it
// tried no field, or saw no owed or no refused claim, since such a set shows nothing.
func TestPodUpdateSweepOwesAnAskExactlyWhereTheRuleDoesWhateverElseTheClaimHolds(t *testing.T) {
	r := newSweepRig(t)
	at := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	facts := sweepFacts(at, r.token)
	leaves, unset := claimLeaves()
	if len(unset) > 0 {
		t.Fatalf("the fields %v cannot be set, so this test cannot try them", unset)
	}
	top := map[string]bool{}
	for _, f := range reflect.VisibleFields(reflect.TypeOf(supervise.Claim{})) {
		top[f.Name] = true
	}
	for name := range sweepFieldsEveryClaimSets {
		if !top[name] {
			t.Fatalf("%s is named as a field every claim of sweepFacts sets, and the claim has no such field", name)
		}
	}
	checked, owed, refused, fields := 0, 0, 0, 0
	buf := make([]supervise.Claim, 0, sweepRoleCount)
	others := make([]supervise.Claim, len(facts))
	for _, leaf := range leaves {
		for j, c := range facts {
			if others[j] = c; leaf.empty(c) {
				leaf.set(&others[j], at)
			}
		}
		tried, wrong, example := 0, 0, ""
		for i, c := range facts {
			if !leaf.empty(c) {
				continue
			}
			tried++
			for _, g := range sweepGroupSizes {
				group := sweepGroup(buf, others, i, g.size, others[i], nil)
				asked, misses := r.hold(group)
				checked += len(group)
				owed += asked
				refused += len(group) - asked
				wrong += len(misses)
				if example == "" && len(misses) > 0 {
					example = g.name + ", " + misses[0]
				}
			}
		}
		_, everyClaimSets := sweepFieldsEveryClaimSets[leaf.top]
		switch {
		case everyClaimSets && tried > 0:
			t.Errorf("%s is named as a field every claim of sweepFacts sets, and %d claims leave %s empty: try it, and take it off the list", leaf.top, tried, leaf.name)
		case !everyClaimSets && tried == 0:
			t.Errorf("no claim of sweepFacts leaves %s empty, so this test tried it on no claim: name %s among the fields every claim sets, with why the sweep's tests still hold the sweep over it", leaf.name, leaf.top)
		case tried > 0:
			fields++
		}
		if wrong > 0 {
			t.Errorf("with %s set, the sweep disagrees with the rule on %d claims of %d groups, for example %s; want the sweep to ask a claim exactly where the rule owes it an ask, it has a session, its agent said it is ready and a machine holds it",
				leaf.name, wrong, tried*len(sweepGroupSizes), example)
		}
	}
	if fields == 0 || owed == 0 || refused == 0 {
		t.Fatalf("checked %d claims over %d fields, %d owed and %d refused: a set with no field tried, or no owed or no refused claim, shows nothing", checked, fields, owed, refused)
	}
	t.Logf("checked %d claims over %d fields of a claim at every depth: %d owed an ask, %d refused one", checked, fields, owed, refused)
}

// claimLeaf is one exported field of supervise.Claim at any depth below it, through structs,
// pointers and slice elements, that holds a value of its own: a string, a bool, a number or a
// time. name is its path ("Pending.ConfirmedAt", "TurnOutcomes[0].Generation"), and top is the
// claim's own field it is under.
type claimLeaf struct {
	name, top string
	path      []leafStep
}

// leafStep is one step of a leaf's path: a struct's field (kind reflect.Struct), the value a
// pointer points to (reflect.Pointer), or a slice's first element (reflect.Slice).
type leafStep struct {
	kind  reflect.Kind
	field int
}

// claimLeaves is every leaf of supervise.Claim, and the name of each field at any depth that holds
// a value this test cannot set — an interface, a map, a function, a channel, a struct with no
// exported field, or a type that holds itself — which the tests fail on.
func claimLeaves() (leaves []claimLeaf, unset []string) {
	timeType := reflect.TypeOf(time.Time{})
	var walk func(t reflect.Type, name, top string, path []leafStep, seen []reflect.Type)
	walk = func(t reflect.Type, name, top string, path []leafStep, seen []reflect.Type) {
		if t == timeType {
			leaves = append(leaves, claimLeaf{name: name, top: top, path: slices.Clone(path)})
			return
		}
		if slices.Contains(seen, t) {
			unset = append(unset, name+" (a type that holds itself)")
			return
		}
		seen = append(slices.Clone(seen), t)
		switch t.Kind() {
		case reflect.String, reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
			leaves = append(leaves, claimLeaf{name: name, top: top, path: slices.Clone(path)})
		case reflect.Pointer:
			walk(t.Elem(), name, top, append(slices.Clone(path), leafStep{kind: reflect.Pointer}), seen)
		case reflect.Slice:
			walk(t.Elem(), name+"[0]", top, append(slices.Clone(path), leafStep{kind: reflect.Slice}), seen)
		case reflect.Struct:
			exported := 0
			for i := range t.NumField() {
				if f := t.Field(i); f.IsExported() {
					exported++
					walk(f.Type, name+"."+f.Name, top, append(slices.Clone(path), leafStep{kind: reflect.Struct, field: i}), seen)
				}
			}
			if exported == 0 {
				unset = append(unset, fmt.Sprintf("%s (%s, no exported field)", name, t))
			}
		default:
			unset = append(unset, fmt.Sprintf("%s (%s)", name, t))
		}
	}
	claimType := reflect.TypeOf(supervise.Claim{})
	for i := range claimType.NumField() {
		if f := claimType.Field(i); f.IsExported() {
			walk(f.Type, f.Name, f.Name, []leafStep{{kind: reflect.Struct, field: i}}, nil)
		}
	}
	return leaves, unset
}

// empty is whether c leaves the leaf empty: a nil pointer or an empty slice on its path, or a
// zero value at its end.
func (l claimLeaf) empty(c supervise.Claim) bool {
	v := reflect.ValueOf(c)
	for _, s := range l.path {
		switch s.kind {
		case reflect.Struct:
			v = v.Field(s.field)
		case reflect.Pointer:
			if v.IsNil() {
				return true
			}
			v = v.Elem()
		case reflect.Slice:
			if v.Len() == 0 {
				return true
			}
			v = v.Index(0)
		}
	}
	return v.IsZero()
}

// set sets the leaf on *c to a value that is not empty: at for a time, "x" for a string, true for
// a bool and 1 for a number. Each pointer and slice on its path is copied first, or made where it
// is nil or empty, so no claim that shares one with c is written through.
func (l claimLeaf) set(c *supervise.Claim, at time.Time) {
	v := reflect.ValueOf(c).Elem()
	for _, s := range l.path {
		switch s.kind {
		case reflect.Struct:
			v = v.Field(s.field)
		case reflect.Pointer:
			p := reflect.New(v.Type().Elem())
			if !v.IsNil() {
				p.Elem().Set(v.Elem())
			}
			v.Set(p)
			v = p.Elem()
		case reflect.Slice:
			n := max(v.Len(), 1)
			s := reflect.MakeSlice(v.Type(), n, n)
			reflect.Copy(s, v)
			v.Set(s)
			v = s.Index(0)
		}
	}
	switch {
	case v.Type() == reflect.TypeOf(time.Time{}):
		v.Set(reflect.ValueOf(at))
	case v.Kind() == reflect.String:
		v.SetString("x")
	case v.Kind() == reflect.Bool:
		v.SetBool(true)
	case v.CanInt():
		v.SetInt(1)
	case v.CanUint():
		v.SetUint(1)
	case v.CanFloat():
		v.SetFloat(1)
	}
}

// The sweep's ask reaches a ready claim the rule owes an ask whatever else the claim holds, through
// the rig's real outbox: for the rig's ready implementer, the control with nothing more set and
// then each exported field of the claim at every depth that the claim leaves empty, set in turn
// (claimLeaves), the sweep (askRound) queues one ask of a round of the case's own exactly where
// the claim's own record owes it one (supervise's Claim.OwesUpdateAsk, which owes a failed claim
// and one that has left the round's pod nothing) and its agent said it is ready. Each case is its
// own round, so no ask an earlier case queued stands in its way. The acceptance test of this
// change wrote it as the red of a filter placed in askRound: a sweep that skipped every claim
// holding a pending task there passed every other test of the sweep, and failed this one.
//
// The negative control is that filter: this test then fails with each field of Pending set, as
// "the sweep queued 0 asks of the ready implementer". The test also fails when it tried no field,
// or saw no owed or no refused case.
func TestPodUpdateTheSweepsAskReachesEveryOwedReadyClaimWhateverElseItHolds(t *testing.T) {
	rig := newPodUpdateRig(t, readyWorker(claim.RoleImplementer))
	opened := podUpdateStart
	base := rig.claimOf(claim.RoleImplementer)
	base.UpdateOpenedAt, base.UpdateReasons = &opened, podUpdateDrift
	leaves, unset := claimLeaves()
	if len(unset) > 0 {
		t.Fatalf("the fields %v cannot be set, so this test cannot try them", unset)
	}
	n, fields, owedSeen, refusedSeen := 0, 0, 0, 0
	try := func(label string, c supervise.Claim) {
		t.Helper()
		n++
		round := fmt.Sprintf("pod:case-%04d", n)
		c.UpdateRound = round
		owed, why := c.OwesUpdateAsk(round)
		want := owed && supervise.SaidReady(c.State)
		rig.s.askRound(rig.ctx, podUpdateIssue, []supervise.Claim{c}, round, podUpdatePod)
		got := 0
		for _, ask := range rig.asks() {
			if ask.Round == round {
				if ask.Role != claim.RoleImplementer {
					t.Fatalf("%s: an ask of round %s went to %s, want only the implementer", label, round, ask.Role)
				}
				got++
			}
		}
		if (got == 1) != want || got > 1 {
			t.Errorf("%s: the sweep queued %d asks of the ready implementer, state %s; the rule says owed=%t (%v); want one exactly where the rule owes one and the agent said it is ready",
				label, got, c.State, owed, why)
		}
		if want {
			owedSeen++
		} else {
			refusedSeen++
		}
	}
	try("the control, no field set", base)
	for _, leaf := range leaves {
		if leaf.top == "UpdateRound" || !leaf.empty(base) {
			continue
		}
		c := base
		leaf.set(&c, opened)
		fields++
		try("with "+leaf.name+" set", c)
	}
	if fields == 0 || owedSeen == 0 || refusedSeen == 0 {
		t.Fatalf("tried %d fields, %d owed and %d refused: a test with no field tried, or no owed or no refused case, shows nothing", fields, owedSeen, refusedSeen)
	}
	t.Logf("tried the control and %d fields of a claim at every depth through askRound: %d owed an ask, %d refused one", fields, owedSeen, refusedSeen)
}

// stoppingPublisher delivers through the listener and then cancels the context its row runs
// under, as a daemon stop does while a row is in its publish.
type stoppingPublisher struct {
	inner  notify.Publisher
	cancel context.CancelFunc
}

func (p stoppingPublisher) Publish(ctx context.Context, topic, message string, payload any, key string) error {
	err := p.inner.Publish(ctx, topic, message, payload, key)
	p.cancel()
	return err
}

func (p stoppingPublisher) PublishKept(ctx context.Context, topic, message string, payload any, key string) (bool, error) {
	kept, err := p.inner.PublishKept(ctx, topic, message, payload, key)
	p.cancel()
	return kept, err
}

// A daemon stop that cancels the lane while its row is in a delivered publish still leaves the
// mark written: the mark's write runs under context.WithoutCancel, bounded by the claim-write
// deadline (supervise's Machine.PublishUpdateAsk), as the outbox's own settle takes a fresh
// context. Otherwise every ordinary stop after a delivered publish would leave the stored claim
// unmarked, and the next boot would ask the role again. The negative control is dc009ea4, which
// wrote the mark under the lane's context: the store refused the write as cancelled, the row
// answered an error and the stored claim held no mark.
func TestPodUpdateAskDeliveredAsTheDaemonStopsIsStillMarked(t *testing.T) {
	rig := newPodUpdateRig(t, readyWorker(claim.RoleImplementer))
	rig.drifts(podUpdateDrift)
	rig.tick()
	rig.awaitRound(claim.RoleImplementer, podUpdateRound)
	row := rig.awaitAskQueued(claim.RoleImplementer, podUpdateRound)
	rig.settleAll()

	stopping, stop := context.WithCancel(rig.ctx)
	defer stop()
	rig.lane.notices = stoppingPublisher{inner: rig.listener, cancel: stop}
	if err := rig.lane.execute(stopping, row); err != nil {
		t.Fatalf("the ask delivered as the daemon stopped = %v, want the row finished", err)
	}
	if n := rig.deliveredTo(claim.RoleImplementer); n != 1 {
		t.Fatalf("the implementer's topic was delivered %d asks, want one", n)
	}
	var marked *time.Time
	if err := rig.pool.QueryRow(rig.ctx, "select update_tasked_at from claims where token = $1",
		string(rig.tokens[claim.RoleImplementer])).Scan(&marked); err != nil {
		t.Fatalf("read the implementer's stored claim: %v", err)
	}
	if marked == nil {
		t.Fatal("the ask reached the implementer as the daemon stopped, and the stored claim records no mark; want it marked, so the next boot does not ask again")
	}
}
