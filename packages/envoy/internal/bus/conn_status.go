// A read of a connection's status that waits for no reconnect's handshake (connProber).

package bus

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"
)

// heldLockBudget is how long a read of nats.go's state (connProber.probe) waits for nats.go's
// connection lock. nats.go v1.53.1 holds that lock for microseconds to write or read a frame, and
// for a whole reconnect attempt, the dial and the handshake (doReconnect, nats.go:3458-3485), up to
// the attempt's connect timeout (connectTimeout), or the hub's authorization timeout while its auth
// service holds the CONNECT: a read kept waiting longer finds the connection mid-reconnect, and
// reads it down. On a box loaded enough to keep the read's goroutine waiting this long, the caller
// answers the loss and retries once the connection is back, which costs it a retry and holds it no
// longer.
const heldLockBudget = 100 * time.Millisecond

// connProber reads one connection's status and reconnect count from nats.go, on a goroutine of its
// own, and answers within heldLockBudget. Every read of nats.go's state takes nats.go's connection
// lock (IsConnected, Status, Stats), so a caller that holds a lock of its own - a session's
// consumer lock, the shared stream read, a relay's restart turn - and asked nats.go directly while
// a reconnect's handshake was in progress held what it holds for the whole handshake. A caller asks
// the prober instead (connForRequest, requestLife, closedUnder, Connected) and waits at most
// heldLockBudget.
//
// One read runs at a time, so a long handshake leaves one goroutine waiting on nats.go's lock,
// however many callers ask. Reads are numbered, and a caller takes the answer of a read begun after
// it asked, never one that may have read nats.go's state before its question: a caller that comes
// while an earlier read is in flight waits for that one to end and then joins the next, within its
// one budget.
type connProber struct {
	conn *nats.Conn
	// probes counts the reads begun; a row reads it.
	probes atomic.Uint64

	mu sync.Mutex
	// probing is the read in flight, nil when none is.
	probing *connProbe
}

// connProbe is one read's answer, set before done is closed.
type connProbe struct {
	done       chan struct{}
	status     nats.Status
	reconnects uint64
	// seq is the read's number among the prober's reads (probes).
	seq uint64
}

// newConnProber is the prober of conn's status.
func newConnProber(conn *nats.Conn) *connProber {
	return &connProber{conn: conn}
}

// probe is nats.go's own status of the connection and its reconnect count, read on a goroutine of
// its own by a read begun after the call and waited for up to heldLockBudget: answered is false
// when nats.go's connection lock was held longer, which only a dial or a handshake does. A read
// left waiting ends when nats.go lets its lock go, at the end of the attempt.
func (p *connProber) probe() (status nats.Status, reconnects uint64, answered bool) {
	budget := time.NewTimer(heldLockBudget)
	defer budget.Stop()
	p.mu.Lock()
	asked := p.probes.Load()
	for {
		run := p.probing
		if run == nil {
			run = &connProbe{done: make(chan struct{}), seq: p.probes.Add(1)}
			p.probing = run
			go func() {
				status, reconnects := p.conn.Status(), p.conn.Stats().Reconnects
				p.mu.Lock()
				run.status, run.reconnects = status, reconnects
				p.probing = nil
				p.mu.Unlock()
				close(run.done)
			}()
		}
		p.mu.Unlock()
		select {
		case <-run.done:
			if run.seq > asked {
				return run.status, run.reconnects, true
			}
		case <-budget.C:
			return 0, 0, false
		}
		p.mu.Lock()
	}
}

// upNow reports whether the connection is connected now as nats.go's IsConnected reads it -
// CONNECTED, or a drain in progress (DRAINING_SUBS, DRAINING_PUBS), which still serves - without a
// wait for a reconnect's dial or handshake: a read nats.go's lock kept waiting past heldLockBudget
// reads as down, the connection mid-reconnect. reconnects is nats.go's reconnect count with an up
// answer.
func (p *connProber) upNow() (up bool, reconnects uint64) {
	status, reconnects, answered := p.probe()
	if !answered || !statusUp(status) {
		return false, 0
	}
	return true, reconnects
}

// statusUp reports whether status is connected as nats.go's IsConnected reads it.
func statusUp(status nats.Status) bool {
	return status == nats.CONNECTED || status == nats.DRAINING_SUBS || status == nats.DRAINING_PUBS
}
