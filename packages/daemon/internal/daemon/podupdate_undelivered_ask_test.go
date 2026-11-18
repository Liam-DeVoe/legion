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

// A round publishes its ask once per claim, and the answer a role gives stands for its round. So
// an ask the sweep queued before the role answered, and that the outbox runs after the answer,
// reaches nobody and marks nothing: the answer is what the ask was for. The same holds for a
// claim the daemon started again in another pod while its ask waited: the round asks that claim
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
// round reached its session, its role answered the round, it left the round's pod or it failed,
// and a claim in another round is owed none. This is the list before the sweep asks whether the
// claim's agent said it is ready (askRound's supervise.SaidReady), which drops a failed claim as
// it drops every claim that is not ready, so the rule's failed case taken out shows here and in no
// sweep that runs. The control is the ready claim with none of these, which the sweep owes its ask.
//
// The negative control is the rule with its failed case taken out: the failed claim is then on
// the list, and its case fails. This test holds each case of the rule as the sweep reads it, and
// the next one (TestPodUpdateSweepOwesAnAskExactlyWhereTheRuleDoes) holds that the sweep refuses
// and owes no claim by a statement of its own that the rule does not make: a sweep that dropped
// failed claims on its own before it read the rule passes this test under that same mutant, and
// fails the next one.
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

// The sweep's list (unaskedClaims) holds a claim exactly where the claim has a session and its own
// record owes it an ask (supervise's Claim.OwesUpdateAsk), over every mix of the facts the two
// read: six states, and each of a session, a delivered ask, an answer, a leave of the round's pod
// and another round, set or not, which is 192 claims. With the test above it holds the one rule of
// whom a round asks: that test fails when the rule loses a case, and this one fails when the sweep
// refuses or owes a claim by a statement of its own that the rule does not make, such as its own
// failed filter put back while the rule's failed case is taken out, or its left-pod check taken
// out. Neither test fails for a statement of the sweep's own that agrees with the rule in every
// case, such as that failed filter put back with the rule whole: it changes no claim's answer, and
// it shows here the first time the rule changes and the two disagree.
//
// The negative control is the sweep's own failed filter put back with the rule's failed case taken
// out: this test then fails on the failed claim with a session that the rule owes an ask, and the
// test above passes. The test also fails when its set holds no owed or no refused claim, since
// such a set shows nothing.
func TestPodUpdateSweepOwesAnAskExactlyWhereTheRuleDoes(t *testing.T) {
	at := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	states := []supervise.ClaimState{supervise.StateReady, supervise.StateWorking, supervise.StateIdle,
		supervise.StateFailed, supervise.StateSuspended, supervise.StateLaunching}
	checked, owedSeen, refusedSeen := 0, 0, 0
	for _, state := range states {
		for bits := range 1 << 5 {
			c := supervise.Claim{Token: "legion-legion-implementer", Role: claim.RoleImplementer,
				State: state, UpdateRound: podUpdateRound}
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
			owed, why := c.OwesUpdateAsk(podUpdateRound)
			want := c.Session != "" && owed
			asking := unaskedClaims([]supervise.Claim{c}, podUpdateRound)
			got := len(asking) == 1 && asking[0].Token == c.Token
			if got != want || len(asking) > 1 {
				t.Errorf("a %s claim, session %q, asked %v, answered %v, left %q, in round %q: the sweep owes it an ask = %t (%d claims), the rule says owed=%t (%v); want the sweep to owe it one exactly where the rule does and it has a session",
					c.State, c.Session, c.UpdateTaskedAt != nil, c.UpdateSignalledAt != nil, c.UpdateLeftRound, c.UpdateRound, got, len(asking), owed, why)
			}
			checked++
			if want {
				owedSeen++
			} else {
				refusedSeen++
			}
		}
	}
	if owedSeen == 0 || refusedSeen == 0 {
		t.Fatalf("checked %d claims, %d owed and %d refused: a set with no owed or no refused claim shows nothing", checked, owedSeen, refusedSeen)
	}
	t.Logf("checked %d claims: %d owed an ask, %d refused one", checked, owedSeen, refusedSeen)
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
