// The client's own record of its connection's status, which a caller holding a lock reads
// without nats.go's connection lock (connStatus).

package bus

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"
)

// connStatus is the client's record of one connection's status, kept for the connection the
// client serves with (Client.connStatus) and read without nats.go's connection lock. nats.go v1.53.1
// holds that lock through the whole of a reconnect attempt, the dial and the handshake (doReconnect,
// nats.go:3458-3485), and every read of its state takes it (IsConnected, Status, Stats), so a read
// made then waits for the handshake to end: up to the attempt's connect timeout (connectTimeout),
// and while a hub's auth service holds a CONNECT, its authorization timeout. A caller that holds a
// session's lock, the shared stream read or a relay's restart turn and asks whether the connection
// is up (connForRequest, requestLife) held what it holds that long. The record, and a probe of
// nats.go's state bounded at heldLockBudget (upNow), answer without that wait.
//
// It counts the connection's drops and its connections back, from three sources, and takes the
// most each has seen, since none of them counts one that did not happen:
//   - nats.go's status reports (StatusChanged), the first to come: nats.go sends each change to a
//     listener's channel under its connection lock, before it sets the status. One goroutine
//     (follow) waits on the channel and nothing else reads it. A report can be lost: nats.go treats
//     a listener's channel that holds an unread report as closed, takes that report out, sends
//     nothing and stops sending that status to the channel for good (sendStatusEvent's first
//     select, nats.go:6682-6699), which a second report made before follow has taken the first
//     brings about.
//   - nats.go's disconnect, reconnect, connect and close callbacks, which nats.go queues on its one
//     callback goroutine and never drops: a disconnect at the start of every reconnect cycle, a
//     forced one's included, before its first attempt (doReconnect, nats.go:3289-3294), a reconnect
//     once one succeeds (:3525), the first connection of a client that connects in the background
//     (Client.onConnect), and the close (:6145-6155). They come later than the reports, behind every
//     callback queued before them. The callbacks alternate, so their count is kept as a state: a
//     disconnect counts a drop only while the callbacks read the connection up, and a connection
//     back ends the drop they counted, which keeps a callback a registration raced from counting
//     one drop twice.
//   - a check of nats.go's own status (refresh), on a goroutine of its own since the read can wait
//     for a handshake: every callback makes one (Client.endLifeIfDown), and so does a reader the
//     record or a probe answers down. When nothing reached the record while it read, it sets the
//     record to what nats.go read, whichever way the record was wrong.
//
// So the record trails nats.go by the moment the first of its sources takes to tell it. A reader
// takes its word that the connection is down, which costs nothing: the caller answers the loss at
// once and waits for the connection with nothing held (Reconnected, which reads nats.go's own
// state), and a record that read down a moment too long is set right by the refresh that answer
// makes. Where the record reads the connection up, a reader that decides from it confirms it in
// nats.go's own state (upNow, probe), read on a goroutine of its own and waited for no longer than
// heldLockBudget: nats.go holds its lock for microseconds to write or read a frame, and for a whole
// dial and handshake while it reconnects, so a lock kept past the budget says the connection is
// mid-reconnect, and the answer is down. That covers each moment the record trails nats.go: a drop
// nats.go reported that follow has not yet applied, a report lost, and a forced reconnect
// (ForceReconnect, which the keeper of a service's token makes to present a new one, natscred),
// whose first attempt comes at once, where a reconnect after a drop sleeps ReconnectWait first when
// the client has one server (doReconnect's doSleep, nats.go:3376). A caller holding a lock so waits
// for nats.go for at most heldLockBudget, never for a handshake.
//
// The record names each connected run of the connection (connEpoch): one begins when the record
// reads the connection connected and ends when it reads it not, which is when the lives of the
// requests bound to it end (requestLife). Connected here is nats.go's IsConnected: CONNECTED, or a
// drain in progress (DRAINING_SUBS, DRAINING_PUBS), which still serves. A closed connection is
// closed for good, and follow ends then.
type connStatus struct {
	conn    *nats.Conn
	channel chan nats.Status
	// refreshing is set while a refresh is in flight, so a flood of down answers makes one.
	refreshing atomic.Bool
	stopOnce   sync.Once

	mu sync.Mutex
	// reported is the last status report follow applied, and reportDowns and reportUps the drops
	// and the connections back those reports showed; callbackDowns and callbackUps are the
	// callbacks', and checkDowns and checkUps what refreshes made good.
	reported                   nats.Status
	reportDowns, reportUps     uint64
	callbackDowns, callbackUps uint64
	checkDowns, checkUps       uint64
	closed                     bool
	// epoch is the connected run in progress, nil while the connection is not connected.
	epoch *connEpoch
	// changed is closed, and replaced, at each change of what the record reads.
	changed chan struct{}
	// applied counts what the record has been told, so a refresh can tell whether something came
	// while it read nats.go's own status.
	applied uint64
	// probing is the probe of nats.go's state in flight (probe), nil when none is: one at a time,
	// however many callers wait for it, so a long handshake leaves one goroutine waiting on
	// nats.go's lock, not one per caller. probes counts the probes started, which a row reads.
	probing *connProbe
	probes  atomic.Uint64
	// done is closed once the record stops following: the connection closed, or the client
	// discarded it (stop).
	done chan struct{}
	// hold is a test's seam, nil outside tests: while it is open, the record applies nothing its
	// sources tell it (awaitHold), so a row can read nats.go and the record apart.
	hold chan struct{}
}

// awaitHold waits while a test holds the record (hold).
func (s *connStatus) awaitHold() {
	s.mu.Lock()
	hold := s.hold
	s.mu.Unlock()
	if hold != nil {
		<-hold
	}
}

// connEpoch is one connected run of a connection: down is closed when it ends.
type connEpoch struct {
	down chan struct{}
}

// everyStatus is every status nats.go reports a connection's change to.
var everyStatus = []nats.Status{nats.DISCONNECTED, nats.CONNECTED, nats.CLOSED, nats.RECONNECTING, nats.CONNECTING, nats.DRAINING_SUBS, nats.DRAINING_PUBS}

// statusUp reports whether status is connected as nats.go's IsConnected reads it.
func statusUp(status nats.Status) bool {
	return status == nats.CONNECTED || status == nats.DRAINING_SUBS || status == nats.DRAINING_PUBS
}

// followConnStatus starts the record of conn's status: it registers its channel and its callbacks,
// around those conn already has, and then reads conn's status, so a change between the two reaches
// the record, taking nats.go's lock to do it. It is called with no lock of the client's held
// (install, ensureConnWithContext), except for a client built without one (Client.statusLocked).
func followConnStatus(conn *nats.Conn) *connStatus {
	s := &connStatus{conn: conn, changed: make(chan struct{}), done: make(chan struct{})}
	s.channel = conn.StatusChanged(everyStatus...)
	disconnected, reconnected, closed := conn.DisconnectErrHandler(), conn.ReconnectHandler(), conn.ClosedHandler()
	conn.SetDisconnectErrHandler(func(nc *nats.Conn, err error) {
		s.callback(func() {
			if s.callbackUps == s.callbackDowns {
				s.callbackDowns++
			}
		})
		if disconnected != nil {
			disconnected(nc, err)
		}
	})
	conn.SetReconnectHandler(func(nc *nats.Conn) {
		s.connectedBack()
		if reconnected != nil {
			reconnected(nc)
		}
	})
	conn.SetClosedHandler(func(nc *nats.Conn) {
		s.callback(func() { s.closed = true })
		if closed != nil {
			closed(nc)
		}
	})
	status := conn.Status()
	s.mu.Lock()
	s.reported = status
	if !statusUp(status) {
		s.reportDowns, s.callbackDowns, s.checkDowns = 1, 1, 1
	}
	s.closed = status == nats.CLOSED
	s.settleLocked()
	stopped := s.closed
	s.mu.Unlock()
	if stopped {
		s.stop()
		return s
	}
	go s.follow()
	return s
}

// connectedBack is the connect or reconnect callback's report: the connection is connected, which
// ends the drop the callbacks counted.
func (s *connStatus) connectedBack() {
	s.callback(func() { s.callbackUps = s.callbackDowns })
}

// stop ends the record's following: follow returns and the channel stops listening. The client
// stops the record of a connection it discards (ensureConnWithContext), whose callbacks it clears.
func (s *connStatus) stop() {
	s.stopOnce.Do(func() {
		close(s.done)
	})
}

// read is the connection's status as the record reads it - CLOSED, a down status (RECONNECTING),
// CONNECTED, or the drain in progress nats.go last reported - and its connected run in progress
// (nil while it is not connected).
func (s *connStatus) read() (nats.Status, *connEpoch) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statusLocked(), s.epoch
}

// up reports whether the record reads the connection connected (statusUp).
func (s *connStatus) up() bool {
	status, _ := s.read()
	return statusUp(status)
}

// awaitUp waits until the record reads the connection connected, refreshing it from nats.go's own
// state (refresh) at each turn, and reports whether it did before ctx ended or the connection
// closed. Reconnected calls it once nats.go reads the connection connected, so the caller it
// answers retries on a record that agrees, rather than on one still trailing nats.go's report,
// which answers the loss again at once. It holds nothing while it waits.
func (s *connStatus) awaitUp(ctx context.Context) bool {
	for {
		s.mu.Lock()
		status, changed := s.statusLocked(), s.changed
		s.mu.Unlock()
		switch {
		case statusUp(status):
			return true
		case status == nats.CLOSED:
			return false
		}
		s.refresh()
		select {
		case <-changed:
		case <-ctx.Done():
			return false
		}
	}
}

// heldLockBudget is how long a probe of nats.go's own state (probe) waits for nats.go's connection
// lock. nats.go holds that lock for microseconds to write or read a frame, and for a whole dial and
// handshake while it reconnects (doReconnect), up to the attempt's connect timeout: a probe kept
// waiting longer finds the connection mid-reconnect. On a box loaded enough to keep a probe's
// goroutine waiting this long, the caller answers the loss and retries once the connection reads
// connected, which costs it a retry and holds it no longer.
const heldLockBudget = 100 * time.Millisecond

// connProbe is one probe's answer, set before done is closed.
type connProbe struct {
	done       chan struct{}
	status     nats.Status
	reconnects uint64
}

// probe is nats.go's own status of the connection and its reconnect count, read on a goroutine of
// its own and waited for up to heldLockBudget: answered is false when nats.go's connection lock was
// held longer, which only a dial or a handshake does. One probe runs at a time; a caller that comes
// while one runs waits for that one's answer. A probe left waiting ends when nats.go lets its lock
// go, at the end of the attempt.
func (s *connStatus) probe() (status nats.Status, reconnects uint64, answered bool) {
	s.mu.Lock()
	p := s.probing
	if p == nil {
		p = &connProbe{done: make(chan struct{})}
		s.probing = p
		s.probes.Add(1)
		go func() {
			status, reconnects := s.conn.Status(), s.conn.Stats().Reconnects
			s.mu.Lock()
			p.status, p.reconnects = status, reconnects
			s.probing = nil
			s.mu.Unlock()
			close(p.done)
		}()
	}
	s.mu.Unlock()
	budget := time.NewTimer(heldLockBudget)
	defer budget.Stop()
	select {
	case <-p.done:
		return p.status, p.reconnects, true
	case <-budget.C:
		return 0, 0, false
	}
}

// upNow reports whether the connection is connected (statusUp) now, without a wait for a
// reconnect's dial or handshake: false at once while the record reads it down, else nats.go's own
// status (probe), with a probe that nats.go's lock kept waiting past heldLockBudget read as down,
// the connection mid-reconnect. reconnects is nats.go's reconnect count with an up answer. A down
// answer refreshes the record (refresh), which sets right a record that read down too long.
func (s *connStatus) upNow() (up bool, reconnects uint64) {
	if !s.up() {
		s.refresh()
		return false, 0
	}
	status, reconnects, answered := s.probe()
	if !answered || !statusUp(status) {
		s.refresh()
		return false, 0
	}
	return true, reconnects
}

// statusLocked is the status the record's counts give.
func (s *connStatus) statusLocked() nats.Status {
	switch {
	case s.closed:
		return nats.CLOSED
	case s.epoch == nil:
		return nats.RECONNECTING
	case s.reported == nats.DRAINING_SUBS || s.reported == nats.DRAINING_PUBS:
		return s.reported
	default:
		return nats.CONNECTED
	}
}

// downsLocked and upsLocked are the most drops and connections back any source has seen. No
// source counts one that did not happen, and each counts a connection back only after a drop, so
// ups never exceeds downs and the connection is connected when they are equal.
func (s *connStatus) downsLocked() uint64 {
	return max(s.reportDowns, s.callbackDowns, s.checkDowns)
}

func (s *connStatus) upsLocked() uint64 {
	return max(s.reportUps, s.callbackUps, s.checkUps)
}

// follow applies each report nats.go sends the record's channel, in its order, until the record
// stops following, and then stops listening.
func (s *connStatus) follow() {
	defer s.conn.RemoveStatusListener(s.channel)
	for {
		select {
		case status := <-s.channel:
			s.awaitHold()
			s.mu.Lock()
			s.reportLocked(status)
			s.mu.Unlock()
		case <-s.done:
			return
		}
	}
}

// reportLocked applies one status report: a change from connected to not is a drop, from not to
// connected a connection back, and CLOSED is the end.
func (s *connStatus) reportLocked(status nats.Status) {
	wasUp := statusUp(s.reported)
	s.reported = status
	switch up := statusUp(status); {
	case status == nats.CLOSED:
		s.closed = true
	case wasUp && !up:
		s.reportDowns++
	case !wasUp && up:
		s.reportUps++
	}
	s.applied++
	s.settleLocked()
}

// callback applies a callback's report under the record's lock.
func (s *connStatus) callback(apply func()) {
	s.awaitHold()
	s.mu.Lock()
	defer s.mu.Unlock()
	apply()
	s.applied++
	s.settleLocked()
}

// refresh reads nats.go's own status, on a goroutine of its own since the read can wait for a
// handshake, and when nothing reached the record while it read and the read disagrees, makes good
// the drop or the connection back the record's sources have not yet told it of. One runs at a time.
func (s *connStatus) refresh() {
	if !s.refreshing.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer s.refreshing.Store(false)
		s.mu.Lock()
		applied := s.applied
		s.mu.Unlock()
		status := s.conn.Status()
		s.awaitHold()
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.applied != applied || s.closed {
			return
		}
		downs, ups := s.downsLocked(), s.upsLocked()
		switch {
		case status == nats.CLOSED:
			s.closed = true
		case statusUp(status) && ups < downs:
			s.checkUps = downs
		case !statusUp(status) && ups == downs:
			s.checkDowns = ups + 1
		default:
			return
		}
		s.applied++
		s.settleLocked()
	}()
}

// settleLocked begins or ends the connected run the counts give, wakes every wait for a change,
// and stops following once the connection is closed.
func (s *connStatus) settleLocked() {
	up := !s.closed && s.upsLocked() == s.downsLocked()
	switch {
	case up && s.epoch == nil:
		s.epoch = &connEpoch{down: make(chan struct{})}
	case !up && s.epoch != nil:
		close(s.epoch.down)
		s.epoch = nil
	}
	close(s.changed)
	s.changed = make(chan struct{})
	if s.closed {
		s.stopOnce.Do(func() { close(s.done) })
	}
}
