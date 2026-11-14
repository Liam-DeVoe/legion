package main

import (
	"github.com/sjawhar/envoy/internal/session"
)

// recipientCounts is how many sessions, other than sender, a message published on topic is for,
// as publishResponse carries the two numbers: recipients is every session the topic is for and
// connected how many of those are live now, a pointer each so that a count of zero is sent as the
// number it is. Both are nil on a listener whose known-session store does not answer
// (session.KnownSessions.Opened: no store at all, one whose NATS grant does not open the bucket
// yet, one whose first read of the bucket has not completed, or one that stopped following it for
// longer than the stale bound), whose count would be the live sessions alone; the publish route
// leaves both fields out of its answer then, rather than send a number that means something
// narrower than the one it names.
//
// recipients is the union of two stores, deduped by session id: the known-session records whose
// topics match the topic (session.KnownSessions.MatchEach), one per session that registered in
// the last 30 days, and the interest rows whose topics match it on any machine
// (store.Registry.MatchAllMachines). A session that registered through a listener of a release
// older than those records has a row and no record, and it is a recipient like any other, which
// is why the rows are in the union too.
//
// connected is how many of those sessions the session registry holds a live entry for, by the
// rule every reader of a session's age reads (session.SessionRegistry.Lookup, session.StateOf): an
// unresponsive session is not counted, since a sender reading "running now" acts on it. It is the
// number a reader acts on: against 30 days of records a team topic almost never counts zero, so
// recipients alone would never again tell a sender that nobody is there to read its message.
//
// Every read here is of memory: both matchers answer from their caches' subject indexes and the
// session registry from its cache, so counting makes no JetStream API call and the publish it
// precedes waits on no round trip of its own.
func recipientCounts(d *listenerDeps, topic, sender string) (recipients, connected *int) {
	if !d.known.Opened() {
		return nil, nil
	}
	live := 0
	counted := map[string]struct{}{}
	count := func(sessionID string) {
		if sessionID == sender {
			return
		}
		if _, dup := counted[sessionID]; dup {
			return
		}
		counted[sessionID] = struct{}{}
		if _, state := d.sessions.Lookup(sessionID); state == session.Live {
			live++
		}
	}
	d.known.MatchEach(topic, func(record session.KnownSession) { count(record.SessionID) })
	for _, interest := range d.registry.MatchAllMachines(topic) {
		count(interest.SessionID)
	}
	total := len(counted)
	return &total, &live
}
