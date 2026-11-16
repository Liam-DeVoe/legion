package main

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/sjawhar/envoy/internal/logging"
	"github.com/sjawhar/envoy/internal/session"
)

// Two processes can run under one session id, each with a client instance id of its own. The
// newer registration takes the session and its relay: a registration naming another instance than
// the live entry names supersedes that instance (session.SessionRegistry.SupersededAfter), so a
// process that restarts after a crash with a new instance id registers at once. From then on the
// older instance is refused while the newer one's entry is live
// (session.SessionRegistry.NewerInstance): its registration (subscribeHandler), its hand-back of
// interests (POST /v1/interests/unsubscribe) and its role claim (roleSetHandler) are each answered
// 409, naming the session and the newer instance. So the older process moves nothing of the relay
// (no consumer call, no entry, no epoch restart, no change to the interest row the consumer
// filters on) and takes no role claim, and the two processes do not take the session's relay in
// turns. The relay subject is the session's own, so an older process that keeps it open receives
// the frames the listener relays there for the newer one; the plugins' shared consumer delivery
// drops them unanswered while its registration is refused for this reason (@legion/envoy-client's
// setSuperseded). A clean hand-off deletes the older registration first (DELETE
// /v1/sessions/<id>?client_instance_id=<older>), and the registration that follows finds no entry
// and is refused nothing.

// supersededReason is the reason a registration, an interest hand-back or a role claim of an older
// instance is refused with (supersededRefusal), short and machine-readable.
const supersededReason = "superseded_instance"

// supersededRefusal is the 409 answer to a registration or an interest hand-back of a client
// instance that a newer instance's registration superseded. The request is refused before anything
// is written, so it leaves the session's entry, interest row, consumer and relay as the newer
// instance left them.
type supersededRefusal struct {
	Error  string `json:"error"`
	Reason string `json:"reason"`
	// ClientInstanceID is the newer instance, which holds the session.
	ClientInstanceID string `json:"client_instance_id"`
}

// supersededError is the sentence every refusal of an older instance carries, which the plugin
// shows its agent.
func supersededError(sessionID, older, newer string) string {
	return fmt.Sprintf("another process holds session %s: client instance %s registered it after this process (client instance %s). "+
		"The listener refuses the registrations and role claims of this process while that process runs. "+
		"Stop this process, or run it under a session id of its own",
		sessionID, newer, older)
}

// supersededBy answers the newer client instance that holds sessionID over instance, as entry
// names it (session.SessionRegistry.NewerInstance), and logs the conflict at WARN the first time
// this listener refuses it (supersededWarnings). A false answer is a registration or a claim the
// rule does not refuse.
func (d *listenerDeps) supersededBy(logger *logging.Logger, sessionID, instance string, entry session.SessionEntry) (string, bool) {
	newer, superseded := d.sessions.NewerInstance(entry, instance)
	if !superseded {
		return "", false
	}
	if d.supersededWarned.first(supersededConflict{sessionID: sessionID, older: instance, newer: newer}, time.Now()) {
		logger.Warn("listener refused the older of two processes under one session id: a newer process registered the session after it",
			slog.String("session_id", sessionID),
			slog.String("client_instance_id", instance),
			slog.String("newer_client_instance_id", newer))
	}
	return newer, true
}

// supersededWarningLimit bounds how many conflicts supersededWarnings remembers.
const supersededWarningLimit = 1024

// supersededConflict is one session whose older client instance is refused for a newer one.
type supersededConflict struct {
	sessionID, older, newer string
}

// supersededWarnings is the listener's memory of the conflicts it has logged, so a refused
// process that registers again at every heartbeat costs one WARN line and not one a heartbeat.
// Each conflict is logged once per listener process while it is remembered: for
// session.SupersededInstanceMemory, the time the session's entry keeps the older instance. At most
// supersededWarningLimit conflicts are remembered; one more forgets the expired ones, and when
// none has expired it forgets them all, so a conflict is at worst logged once more. A listener
// restart forgets them too. The rule itself is kept in the session's entry, which every listener
// reads; only this record of what was logged is a listener's own. The zero value is ready to use.
type supersededWarnings struct {
	mu     sync.Mutex
	warned map[supersededConflict]time.Time
}

// first reports whether conflict is not remembered as logged, and remembers it as logged at now.
func (w *supersededWarnings) first(conflict supersededConflict, now time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if at, ok := w.warned[conflict]; ok && now.Sub(at) < session.SupersededInstanceMemory {
		return false
	}
	if w.warned == nil {
		w.warned = make(map[supersededConflict]time.Time)
	}
	if len(w.warned) >= supersededWarningLimit {
		for remembered, at := range w.warned {
			if now.Sub(at) >= session.SupersededInstanceMemory {
				delete(w.warned, remembered)
			}
		}
		if len(w.warned) >= supersededWarningLimit {
			clear(w.warned)
		}
	}
	w.warned[conflict] = now
	return true
}
