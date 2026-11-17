package admission

import (
	"container/list"
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// The request pool's work outside admission. Admission hands out MaxAdmittedUnits of the request
// pool's store.SharedPoolSize connections; the rest are the work it does not admit, and three kinds
// of that work take a place of OutsideLimit before they read the pool, so they hold at most that
// share of it however many of them arrive at once:
//
//   - a broker-issued token's DPoP presentation, which the API decides before admission (api's
//     decideBrokerToken) and whose decision writes the proof's single-use row on the request pool
//     (store.DPoPProofs). Every such request waited on the pool's acquire queue before admission
//     could refuse it, with nothing bounding how many did. The broker is asked whether the token
//     is live first, holding no place, and the place is taken only for what reads the pool, the
//     proof's check against the answer the broker just gave and its row, so a broker slow to answer
//     holds none of the places. A decision refused a place is answered admission's own refusal for
//     the same reason, 429 OVERLOADED with Retry-After, and its proof is not spent;
//   - a socket's heartbeat check, where it reads the request pool (api's whileCallerResolves: the
//     sign-in cookie's session, a personal token's row). One runs every heartbeat for every open
//     socket, so a restart's reconnect lines them up: 991 waited on the pool at once in INFRA-592's
//     stress step (tester round 38). A check refused a place is skipped, and a skip counts toward
//     the heartbeat's ceiling as a transient failure does (api's heartbeatTransientCeiling). A
//     check that reads no pool - a broker-issued token's, which asks the broker alone - takes no
//     place and always runs;
//   - a document socket's admission reads, made before the socket is handed to ygo (docs'
//     Service.ServeHTTP): its caller's session, its document's issue and the live server
//     processes, the last of which one socket a second makes for every socket (docs'
//     fleetCacheTTL), so most sockets make two. A restart's reconnect sends every editor's socket
//     through them at once. They hold one place between them for those reads (Hold), taken before
//     the first and given back before the recovery wait and the document's load, neither of which
//     reads the request pool, and long before the hand-off, after which the handler holds its
//     request for the socket's whole life; a socket refused a place is refused as the document
//     service refuses a socket it cannot serve now, 503 with Retry-After.
//
// The pool's other users outside admission take none, each for a reason of its own: the live
// socket's upgrade and an events subscription's head check are one statement for each socket
// opened and each subscription started, where a socket's heartbeat checks every 15 s for as long as
// the socket lives; the /auth routes and the GitHub proxy are a person's sign-in and page loads,
// and the sign-in callback's code is single use; the going run's stream polls, and its run's
// writes, are one person's question, one statement at a time; a broadcast's deliveries run after
// their request's answer, four at a time for each broadcast (api's broadcastDeliveryWorkers); the
// dashboard's assets of other builds are read through a cache that keeps them, one read in flight
// for each. The test hooks never run in production.
//
// Work that finds every place held waits for one, first in first out, as a request waits in its
// class's queue and a heavy read in its lane's: in a queue of at most OutsideQueueLimit, for at most
// a second counted from when it asked (Limits.OutsideWait). It was refused at once before, and on
// INFRA-592's stress step (ten times the load, 1,000 editors, a restart) that refused 47,962 of the
// 48,580 refused document sockets, skipped 4,136 heartbeat checks and closed 223 sockets at the
// heartbeat's ceiling: the places were held by work waiting on a request pool the load had filled,
// and each burst - 1,000 editors coming back after the restart - found them held for a moment and
// was refused whole and sent again, so the handshakes per editor rose from 52.8 to 70.9. Work that
// finds the queue full, or whose second runs out, is refused as before (OutsideRefusal: the same
// QueueFull and WaitExceeded a class's queue refuses for), and work whose context ends while it
// waits leaves the queue. A place is held for the whole of the work that reads the pool: a
// decision's proof check and the row it writes, once the broker has answered, a check's whole
// check, and a socket's three admission reads.

// OutsideLimit is how many places the work outside admission may hold at once: the request
// pool's connections admission does not hand out, unadmittedConnections, 8 of the 48. Admission's
// own cap is the pool's size less those (MaxAdmittedUnits), so the 8 stay the 8 whatever the
// pool's size, and admission's 40 is what moves with it. The DPoP decisions, the heartbeat checks
// and the document sockets' admission reads together then hold no more of the pool than admission
// leaves, however long the queue for a place. Nothing bounded them before, and the 8 did not last:
// at ten times the load with bearer tokens alone the request pool's connections in use reached 48
// in all four of the head's steps, with up to 15 callers waiting (INFRA-592, tester round 38).
const OutsideLimit = unadmittedConnections

// OutsideQueueLimit is how much work may wait for a place at once: queueFactor times OutsideLimit,
// 16, the shape of a class's queue and a lane's (queueFactor says why). Work that finds it full is
// refused at once (QueueFull).
const OutsideQueueLimit = queueFactor * OutsideLimit

// OutsideUser is a kind of work that takes a place of OutsideLimit, which the refusals are counted
// by (OutsideStats.Refused).
type OutsideUser int

const (
	// DPoPDecision is the decision of a broker-issued token's DPoP presentation (api's
	// decideBrokerToken); one refused a place is answered 429 OVERLOADED.
	DPoPDecision OutsideUser = iota
	// HeartbeatCheck is a socket's heartbeat resolving its caller again where that reads the
	// request pool (api's whileCallerResolves); one refused a place is skipped.
	HeartbeatCheck
	// SocketAdmission is a document socket's admission reads, its caller's session, its issue and
	// the live server processes (docs' Service.ServeHTTP); one refused a place is answered 503
	// with Retry-After.
	SocketAdmission
	outsideUserCount
)

var outsideUserNames = [outsideUserCount]string{
	DPoPDecision: "dpop_decision", HeartbeatCheck: "heartbeat_check", SocketAdmission: "socket_admission",
}

func (u OutsideUser) String() string { return outsideUserNames[u] }

// OutsideUsers are the users in order, for a caller that reports on each of them (the metrics
// endpoint).
func OutsideUsers() []OutsideUser {
	return []OutsideUser{DPoPDecision, HeartbeatCheck, SocketAdmission}
}

// OutsideRefusal is work Outside gave no place: Reason is QueueFull, the queue for a place held
// OutsideQueueLimit already, or WaitExceeded, no place came free within the wait.
type OutsideRefusal struct {
	User   OutsideUser
	Reason Reason
}

func (r *OutsideRefusal) Error() string {
	return fmt.Sprintf("no place of the request pool's share outside admission for a %s: %s", r.User, r.Reason)
}

// Outside is a Limiter's places for the request-pool work outside admission (OutsideLimit), and
// the queue the work waits in for one: a gate of a class's shape (gate) with no weights and no
// second class, so its waiters are served first in, first out. A nil Outside limits nothing, so a
// server a test builds without a Limiter takes every place it asks for.
type Outside struct {
	// wait is how long work waits for a place (Limits.OutsideWait), set once by New.
	wait time.Duration

	// mu guards the places held, the queue and its longest length.
	mu        sync.Mutex
	used      int
	waiters   list.List // of chan struct{}, in arrival order
	maxQueued int

	refused [outsideUserCount]outsideRefusalCounts
}

// outsideRefusalCounts counts one user's refusals by reason.
type outsideRefusalCounts struct {
	queueFull, waitExceeded atomic.Uint64
}

// Take takes a place for user's work and answers nil once it holds one. Where every place is held
// it waits for one in the queue, behind every work that asked before it, for at most the wait,
// and answers an *OutsideRefusal - QueueFull at once where the queue is full, WaitExceeded where
// the wait runs out - or ctx's error where ctx ends first, holding no place and having left the
// queue either way. A caller that took a place gives it back with Release once its work has given
// its connection back.
func (o *Outside) Take(ctx context.Context, user OutsideUser) error {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	if o.waiters.Len() == 0 && o.used < OutsideLimit {
		o.used++
		o.mu.Unlock()
		return nil
	}
	if o.waiters.Len() >= OutsideQueueLimit {
		o.mu.Unlock()
		o.refused[user].queueFull.Add(1)
		return &OutsideRefusal{User: user, Reason: QueueFull}
	}
	ready := make(chan struct{})
	element := o.waiters.PushBack(ready)
	o.maxQueued = max(o.maxQueued, o.waiters.Len())
	o.mu.Unlock()

	timer := time.NewTimer(o.wait)
	defer timer.Stop()
	var failure error
	select {
	case <-ready:
		return nil
	case <-ctx.Done():
		failure = ctx.Err()
	case <-timer.C:
		failure = &OutsideRefusal{User: user, Reason: WaitExceeded}
	}

	o.mu.Lock()
	select {
	case <-ready:
		// Handed a place in the moment the wait ended: work whose wait ran out takes it, since it
		// is here; work whose context ended gives it straight on.
		o.mu.Unlock()
		if ctx.Err() == nil {
			return nil
		}
		o.Release()
		return failure
	default:
	}
	o.waiters.Remove(element)
	o.mu.Unlock()
	if _, refused := failure.(*OutsideRefusal); refused {
		o.refused[user].waitExceeded.Add(1)
	}
	return failure
}

// Release gives back a place Take took, handing it to the work at the head of the queue where any
// waits.
func (o *Outside) Release() {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if front := o.waiters.Front(); front != nil {
		o.waiters.Remove(front)
		close(front.Value.(chan struct{}))
		return
	}
	o.used--
}

// Hold is Take for work that gives its place back at a point of its own and must give it back on
// every other way out as well: a document socket's admission reads, whose place goes back before
// the socket is handed to ygo, and as the handler returns where the socket was refused. It answers
// the release that gives the place back and Take's answer. The release gives the place back once
// however often it is called, so a caller may call it where its work ends and defer it too; the
// release of a Hold that took no place gives nothing back.
func (o *Outside) Hold(ctx context.Context, user OutsideUser) (release func(), err error) {
	if err := o.Take(ctx, user); err != nil {
		return func() {}, err
	}
	return sync.OnceFunc(o.Release), nil
}

// OutsideStats is what an Outside holds, queues and has refused.
type OutsideStats struct {
	// Limit is OutsideLimit, InUse the places held now, Queued the work waiting for one now and
	// MaxQueued the most that has waited at once since the Limiter was made.
	Limit, InUse, Queued, MaxQueued int
	// Refused counts, by user, the work refused a place since the Limiter was made: a DPoP
	// decision answered 429 OVERLOADED, a heartbeat check skipped, a document socket answered 503.
	Refused [outsideUserCount]OutsideRefusals
}

// OutsideRefusals is one user's refusals by reason.
type OutsideRefusals struct {
	QueueFull, WaitExceeded uint64
}

// Stats reads the counts: the places, the queue and its longest length together, and each
// refusal count on its own, so a scrape that races a refusal may see it in one count and not yet
// in another.
func (o *Outside) Stats() OutsideStats {
	o.mu.Lock()
	stats := OutsideStats{Limit: OutsideLimit, InUse: o.used, Queued: o.waiters.Len(), MaxQueued: o.maxQueued}
	o.mu.Unlock()
	for user := range outsideUserCount {
		stats.Refused[user] = OutsideRefusals{
			QueueFull: o.refused[user].queueFull.Load(), WaitExceeded: o.refused[user].waitExceeded.Load(),
		}
	}
	return stats
}
