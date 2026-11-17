package admission

import (
	"sync"
	"sync/atomic"
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
//     holds none of the places. A decision that finds every place held is refused at once with
//     admission's own refusal, 429 OVERLOADED for a full queue, and its proof is not spent;
//   - a socket's heartbeat check, where it reads the request pool (api's whileCallerResolves: the
//     sign-in cookie's session, a personal token's row). One runs every heartbeat for every open
//     socket, so a restart's reconnect lines them up: 991 waited on the pool at once in INFRA-592's
//     stress step (tester round 38). A check that finds every place held is skipped, and a skip
//     counts toward the heartbeat's ceiling as a transient failure does (api's
//     heartbeatTransientCeiling). A check that reads no pool - a broker-issued token's, which asks
//     the broker alone - takes no place and always runs;
//   - a document socket's admission reads, made before the socket is handed to ygo (docs'
//     Service.ServeHTTP): its caller's session, its document's issue and the live server
//     processes. A restart's reconnect sends every editor's socket through them at once. They hold
//     one place between them for the three request-pool reads (Hold), taken before the first and
//     given back before the recovery wait and the document's load, neither of which reads the
//     request pool, and long before the hand-off, after which the handler holds its request for
//     the socket's whole life; a socket that finds every place held is refused as the document
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
// None of them waits for a place: a decision or a socket refused is told to come back after
// Retry-After, and a check skipped is made at the next heartbeat, so none of them queues for the
// pool behind the rest. A place is held for the whole of the work that reads the pool: a
// decision's proof check and the row it writes, once the broker has answered, a check's whole
// check, and a socket's three admission reads.

// OutsideLimit is how many places the work outside admission may hold at once: the request
// pool's connections admission does not hand out, unadmittedConnections, 8 of the 48. Admission's
// own cap is the pool's size less those (MaxAdmittedUnits), so the 8 stay the 8 whatever the
// pool's size, and admission's 40 is what moves with it. The DPoP decisions, the heartbeat checks
// and the document sockets' admission reads together then hold no more of the pool than admission
// leaves. Nothing bounded them before, and the 8 did not last: at ten times the load with bearer
// tokens alone the request pool's connections in use reached 48 in all four of the head's steps,
// with up to 15 callers waiting (INFRA-592, tester round 38).
const OutsideLimit = unadmittedConnections

// OutsideUser is a kind of work that takes a place of OutsideLimit, which the refusals are counted
// by (OutsideStats.Refused).
type OutsideUser int

const (
	// DPoPDecision is the decision of a broker-issued token's DPoP presentation (api's
	// decideBrokerToken); one the limit refuses is answered 429 OVERLOADED.
	DPoPDecision OutsideUser = iota
	// HeartbeatCheck is a socket's heartbeat resolving its caller again where that reads the
	// request pool (api's whileCallerResolves); one the limit refuses is skipped.
	HeartbeatCheck
	// SocketAdmission is a document socket's admission reads, its caller's session, its issue and
	// the live server processes (docs' Service.ServeHTTP); one the limit refuses is answered 503
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

// Outside is a Limiter's places for the request-pool work outside admission (OutsideLimit). It
// never queues: Take answers at once. A nil Outside limits nothing, so a server a test builds
// without a Limiter takes every place it asks for.
type Outside struct {
	inUse   atomic.Int64
	refused [outsideUserCount]atomic.Uint64
}

// Take takes a place for user's work and reports whether it did. Where every place is held it
// counts user's refusal and reports false at once, and the work is not done: its caller refuses
// it or skips it. A caller that took a place gives it back with Release once its work has given
// its connection back.
func (o *Outside) Take(user OutsideUser) bool {
	if o == nil {
		return true
	}
	for {
		held := o.inUse.Load()
		if held >= OutsideLimit {
			o.refused[user].Add(1)
			return false
		}
		if o.inUse.CompareAndSwap(held, held+1) {
			return true
		}
	}
}

// Release gives back a place Take took.
func (o *Outside) Release() {
	if o == nil {
		return
	}
	o.inUse.Add(-1)
}

// Hold is Take for work that gives its place back at a point of its own and must give it back on
// every other way out as well: a document socket's admission reads, whose place goes back before
// the socket is handed to ygo, and as the handler returns where the socket was refused. It answers
// the release that gives the place back and whether the place was taken. The release gives it
// back once however often it is called, so a caller may call it where its work ends and defer it
// too; a refused Hold's release gives nothing back.
func (o *Outside) Hold(user OutsideUser) (release func(), held bool) {
	if !o.Take(user) {
		return func() {}, false
	}
	return sync.OnceFunc(o.Release), true
}

// OutsideStats is what an Outside holds and has refused.
type OutsideStats struct {
	// Limit is OutsideLimit, and InUse the places held now.
	Limit, InUse int
	// Refused counts, by user, the work refused a place since the Limiter was made: a DPoP
	// decision answered 429 OVERLOADED, a heartbeat check skipped, a document socket answered 503.
	Refused [outsideUserCount]uint64
}

// Stats reads the counts. Each is read on its own, so a scrape that races a Take may see it in one
// count and not yet in another.
func (o *Outside) Stats() OutsideStats {
	stats := OutsideStats{Limit: OutsideLimit, InUse: int(o.inUse.Load())}
	for user := range outsideUserCount {
		stats.Refused[user] = o.refused[user].Load()
	}
	return stats
}
