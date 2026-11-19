package docs

import (
	"context"
	"errors"
	"sync/atomic"
	"time"
)

// settlementWorkers is how many document settlements run at once, every way one starts: a timer, a
// room's last browser leaving (settleLastPeer), a retry, the resumption and the shutdown drain all
// end in settleRoomWithin, which takes one of these slots before it loads a cold room. Each running
// settlement holds one connection of the store's settlements pool (store.Pool.Settlements) for its
// transaction, and that pool has as many connections as there are slots
// (TestSettlementSlotsMatchTheSettlementsPool), so a settlement holding a slot never waits for a
// connection behind another settlement. Without the bound N owed documents - every one a backlog
// of resumptions arms at once - ran N settlements, each loading its room and wanting a connection,
// which is DR-05's collapse.
//
// A settlement does not wait for the requests to ease before it takes a slot, as a compaction's
// fold does (requestYield). That wait was built, measured on the load test and refused
// (INFRA-592). The execution trace of the span where a burst's recovery keeps failing (30 to 55 s
// after the drop) puts the settlement timers second only to the collector's mark workers among
// the work that is not a request handler: 5.4 to 5.6 s of the 75 processor seconds, over 50 to 53
// settlements (about 105 ms of processor each), against 15 s for the handlers. A settlement that
// waited out the request pressure before it took its slot, for at most 10 s, made a step's
// settlement work grow by about 35%: the settlements pool's acquires in the two minutes after the
// drop went from 791 and 818 to 1,042 to 1,108 over four runs, and the request p99 of the windows
// that fail did not improve. A settlement that waits is likelier to see its room's generation
// move, which ends it and arms another (openLiveWrite), so to defer settlements is to multiply
// them. The slots are what bounds them.
const settlementWorkers = 4

// settlementTimeout bounds a settlement a timer starts (settleRoom), from its wait for a slot to its
// commit. A settlement still running at the bound rolls back, keeps the document's
// pending-settlement row and counts no failure: the resumption settles it once the load that held
// it up has passed (RunSettlementResumption). Two parts it cannot stop: a cold room's load, since
// ygo loads a room with no context of its own (servicePersistenceAdapter.LoadDoc), and what a pass
// has left to do once it has written into the room (settlementFinishBound).
const settlementTimeout = 60 * time.Second

// errSettlementTimeout is the cause a settlement's context ends with at settlementTimeout, which
// tells its retry the deadline from a shutdown's budget.
var errSettlementTimeout = errors.New("document settlement ran past its deadline")

// settlementFinishBound bounds what a settlement pass has left to do once it has written into its
// room: the broadcast of that update, its append, the reads, rows and events its commit needs, and
// the commit. That work runs apart from the settlement's deadline (settlementTimeout), under this
// bound of its own, as a commit runs apart from its caller's deadline (store.CommitContext). The
// room and its browsers hold the update by then, so a deadline falling there would fail the room,
// close every editor's socket and reload the document over work the pass had all but committed.
// Only work the database leaves unanswered for this long - an update in the room that will never
// reach the store - fails the room now.
const settlementFinishBound = 30 * time.Second

// errSettlementFinishTimeout is the cause the context of a pass that has written into its room ends
// with at settlementFinishBound.
var errSettlementFinishTimeout = errors.New("document settlement ran past the bound of the pass that wrote into its room")

// finishInRoom is the context a settlement pass finishes what it started on, once it has written
// into its room: ctx's values - its transaction, its ledger, its room cursor - without the
// settlement's deadline, bounded at settlementFinishBound.
func finishInRoom(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeoutCause(context.WithoutCancel(ctx), settlementFinishBound, errSettlementFinishTimeout)
}

// settlementSlots is the bound settlementWorkers sets: a slot per running settlement, and counts of
// the settlements running and of those waiting for a slot, which the metrics endpoint reports.
type settlementSlots struct {
	slots   chan struct{}
	running atomic.Int64
	waiting atomic.Int64
}

func newSettlementSlots(size int) *settlementSlots {
	return &settlementSlots{slots: make(chan struct{}, size)}
}

// acquire waits for a slot until ctx ends, and returns its release. A settlement waiting here holds
// no connection and no lock, so the wait is the only place the excess queues.
func (s *settlementSlots) acquire(ctx context.Context) (func(), error) {
	s.waiting.Add(1)
	select {
	case s.slots <- struct{}{}:
		s.waiting.Add(-1)
		s.running.Add(1)
		return func() {
			s.running.Add(-1)
			<-s.slots
		}, nil
	case <-ctx.Done():
		s.waiting.Add(-1)
		return nil, context.Cause(ctx)
	}
}

// SettlementLoad is how many document settlements hold a slot and how many wait for one.
type SettlementLoad struct {
	Running int64
	Waiting int64
}

// SettlementLoad reports the settlements running and waiting now (settlementWorkers), the
// settlements_running and settlements_waiting gauges of the metrics endpoint.
func (s *Service) SettlementLoad() SettlementLoad {
	return SettlementLoad{Running: s.settlements.running.Load(), Waiting: s.settlements.waiting.Load()}
}
