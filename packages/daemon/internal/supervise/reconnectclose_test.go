package supervise

import (
	"errors"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/runtime"
)

// The tester's red tests of test round 11 of dispatch://INFRA-2811, from the reviewer's probes
// (F1 and F2, 2026-11-23), under plan decision 8's connection rule: a close arms the reconnect
// deadline whole from the close, and a reconnect hello bounds what it leaves before any step of it
// that can fail.

// A close after a reconnect hello whose ask the agent left unanswered gives the live worker a
// whole reconnect bound from that close: a deadline the hello armed earlier is not where the claim
// is judged. A shim that comes back inside a whole bound of the close, after the hello's deadline,
// finds its process neither retired nor charged.
//
// The control is the same close after an answered ask, which arms a whole bound from the close.
// The negative control is the head this test was written at (b48764f6): closeView keeps the
// hello's deadline (armReconnectUnlessArmed), and at it reconnectDeadline reads the view closed
// and retires the process charged, as never reconnected.
func TestACloseAfterAnUnansweredHelloArmsAWholeReconnectBoundFromTheClose(t *testing.T) {
	for _, answered := range []bool{false, true} {
		t.Run(map[bool]string{false: "the hello's ask unanswered", true: "the hello's ask answered"}[answered], func(t *testing.T) {
			h := boundsHarness(t)
			h.takeOf("implement the plan")
			h.reconnectLater()
			if !answered {
				h.conn.FailGetState(errors.New("the agent did not answer in time"))
			}
			h.helloOf(true, false)
			if answered {
				h.answerAsk(false)
			}
			h.advance(deadline - 30*time.Second)
			h.reconnectLater()
			closedAt := h.clock.Now()

			due, armed := h.dueOf(TimerRegistration)
			if !armed || due.Before(closedAt.Add(deadline)) {
				t.Errorf("after the second close the reconnect deadline is armed %t, due %s after the close; want it due a whole bound (%s) after the close",
					armed, due.Sub(closedAt), deadline)
			}
			h.advance(30 * time.Second)
			h.wantCalls("Suspend", 0)
			if b := h.budgets(); b.LaunchFailures != 0 {
				t.Errorf("30 s after the close the budgets read %+v: the live worker was charged", b)
			}
			if c := h.claim().PendingCause; c != "" {
				t.Errorf("30 s after the close the pending cause is %q, want none", c)
			}
		})
	}
}

// A close over no pending task of a claim whose agent said it is ready retires nothing at a
// deadline a hello armed while the claim still held a task: the task finished while the hello's
// ask went unanswered, and the close then leaves the claim with no reconnect deadline, as a close
// of a claim holding no task always has.
//
// The negative control is the head this test was written at (b48764f6): dropReconnectWithTask
// keeps the hello's deadline over the unanswered ask, closeView over no task arms and disarms
// nothing, and at the deadline the live claim holding no task is retired and charged.
func TestACloseOverNoTaskAfterAnUnansweredHelloRetiresNothing(t *testing.T) {
	h := boundsHarness(t)
	number := h.takeOf("implement the plan")
	h.reconnectLater()
	h.conn.FailGetState(errors.New("the agent did not answer in time"))
	h.helloOf(true, false)
	h.reportOf(1, number)
	if p := h.claim().Pending; p != nil {
		t.Fatalf("pending %+v after the report, want the task finished", p)
	}
	h.advance(deadline - 30*time.Second)
	h.reconnectLater()

	if _, armed := h.dueOf(TimerRegistration); armed {
		t.Errorf("after a close over no task the reconnect deadline is still armed")
	}
	h.advance(30 * time.Second)
	h.wantCalls("Suspend", 0)
	if b := h.budgets(); b.LaunchFailures != 0 {
		t.Errorf("30 s after the close the budgets read %+v: a live claim holding no task was charged", b)
	}
}

// A reconnect hello whose decision fails on the store before it reads the view still leaves the
// claim bounded: the reconnect deadline stands (or the view is asked), so a take awaiting its turn
// over that connection is not left with no bound at all.
//
// The negative control is the head this test was written at (b48764f6): reconnected disarms the
// registration deadline first, the claim write fails before askView, the failed decision is
// dropped, and the view stays cut with no deadline: twenty deadlines later the take still awaits
// its turn, nothing charged and nothing taken back.
func TestAHelloWhoseDecisionFailsOnTheStoreLeavesTheClaimBounded(t *testing.T) {
	h := waitsHarness(t, currentPlugin)
	h.withSecrets()
	h.manualTakes()
	h.reach(StateReady)
	number := h.takeOf("implement the plan")
	h.reconnectLater()
	h.store.fail("PutClaim", errors.New("the store is down"))
	h.conn.SetStreaming(false)
	h.conns.Register(h.token, h.conn)
	err := h.m.Handle(h.ctx, StreamHello{Claim: h.token, Generation: h.generation(), BacklogWhole: true, AgentSecrets: testIdentity})
	h.m.Wait()
	h.store.fail("PutClaim", nil)
	_, armed := h.dueOf(TimerRegistration)
	h.m.mu.Lock()
	view := h.m.connSeen
	h.m.mu.Unlock()
	if !armed && view != viewAsked && view != viewWhole {
		t.Errorf("after the hello's failed decision (%v) the view is %d and no reconnect deadline is armed: nothing bounds take %d", err, view, number)
	}
	h.advance(10 * deadline)
	h.observe(runtime.Alive)
	h.advance(10 * deadline)
	if p := h.claim().Pending; p != nil && p.Number == number && p.awaitsTurn() && len(h.calls("Suspend")) == 0 && len(h.charges()) == 0 {
		t.Errorf("twenty reconnect deadlines after the failed hello, take %d still awaits its turn with nothing charged, taken back or relaunched", number)
	}
}
