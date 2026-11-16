package session

import "time"

// InstanceMatches reports whether a session's stored client instance id and the one a caller
// carries name the same client instance (one Oh My Pi Envoy extension instance, one Claude Code
// channel-server process). Two decisions ask it, and both are a caller acting on a session it
// says is its own: a soft claim naming its predecessor, before it takes the role from that
// predecessor (POST /v1/roles/set), where the same conversation continuing inside its own
// instance may take it and another instance's live session may not be displaced by one; and the
// conditional DELETE /v1/sessions/<id> (DeleteConditions.keeps), where a caller that mints an
// instance id of its own deregisters the session it registered and must not delete the live
// entry another process wrote after it resumed the same session id.
//
// Every other decision that weighs two instances - a claim of a role held under the claimant's
// own session id, a release of that session's role claims (store.RoleReleaseAuthority), and the
// refusal of a superseded instance (NewerInstance) - reads an absent instance as naming no process
// at all, so it matches nothing and nothing stands in for it. The admission of an absent id below
// is these two callers' alone.
//
// An empty id on either side is a client that predates the field, and keeps the old rule: the
// claim goes through (the listener then logs the takeover) and so does the delete, because a
// client that cannot name its instance must still be able to recover its own role and to
// deregister its own session, and refusing it would strand it instead. Two non-empty ids must be
// equal; nothing else makes two instances the same one.
//
// The ids are compared as stored, with no trimming of their own: the value a registration
// stored, the one POST /v1/roles/set states and the one DELETE /v1/sessions/<id> carries are
// each trimmed where they enter, so an id that reaches this test is already the id itself.
func InstanceMatches(stored, requested string) bool {
	return stored == "" || requested == "" || stored == requested
}

// SupersededInstanceLimit is how many superseded client instances one session's entry keeps
// (SessionEntry.SupersededInstances). A registration that supersedes one more drops the oldest.
const SupersededInstanceLimit = 8

// SupersededInstanceMemory is how long a session's entry keeps a superseded client instance. An
// instance id is minted at each process start, so an id past this age is a process that has run
// on under the session id for a day after a newer one took it. Its next registration then takes
// the session back, as a new instance's does, and the instance it takes it from is refused from
// then on, with a WARN line that names both.
const SupersededInstanceMemory = 24 * time.Hour

// SupersededInstance is one client instance that a registration of a newer instance took a
// session from (SessionEntry.SupersededInstances).
type SupersededInstance struct {
	ClientInstanceID string `json:"client_instance_id"`
	// At is the moment the newer registration took the session, in unix milliseconds.
	At int64 `json:"at"`
}

// keeps reports whether e lists instance as superseded within SupersededInstanceMemory of now.
func (e SessionEntry) keeps(instance string, now time.Time) bool {
	for _, superseded := range e.SupersededInstances {
		if superseded.ClientInstanceID == instance {
			return now.Sub(time.UnixMilli(superseded.At)) < SupersededInstanceMemory
		}
	}
	return false
}

// Supersedes reports whether entry's own client instance is newer than instance: entry names an
// instance, instance is another one, and entry lists instance as superseded, as of this
// registry's clock. An empty instance names no process, so it is never superseded, and an entry
// that names no instance supersedes nothing. POST /v1/roles/set reads it to pass a role claim an
// older instance holds to the newer instance that claims it.
func (r *SessionRegistry) Supersedes(entry SessionEntry, instance string) bool {
	return entry.ClientInstanceID != "" && instance != "" && instance != entry.ClientInstanceID &&
		entry.keeps(instance, r.now())
}

// NewerInstance answers the client instance that holds entry's session over instance: entry's
// own, where entry is live and supersedes instance (Supersedes). The registration route (POST
// /v1/interests/subscribe), POST /v1/roles/set and the hand-back route (POST
// /v1/interests/unsubscribe) refuse instance then, so two processes under one session id do not
// take its relay in turns, the older one takes no role claim, and its hand-back removes nothing
// from the newer one's interest row. A brand-new instance is never refused: entry keeps no record
// of it, so its registration takes the session. Nor is any instance once entry is past the live
// window: the newer process has stopped answering, so the one that still registers takes the
// session back.
func (r *SessionRegistry) NewerInstance(entry SessionEntry, instance string) (string, bool) {
	if !r.Supersedes(entry, instance) || r.EntryState(entry) != Live {
		return "", false
	}
	return entry.ClientInstanceID, true
}

// SupersededAfter is the superseded list the entry a registration of instance writes carries,
// given previous, the entry it replaces (the zero entry where there is none). A registration of
// another instance than the one previous names supersedes that one, which goes first, stamped with
// this registry's clock. The rest of previous's list follows, less instance itself, less an entry
// past SupersededInstanceMemory, and up to SupersededInstanceLimit in all. A side that names no
// instance supersedes nothing, since there is no process to name.
//
// The list is built from the record the registration replaces, so a registration of the newer
// instance carries it forward on every heartbeat. A clean stop deletes the record, and with it the
// list: the registration that follows a clean hand-off is refused nothing.
func (r *SessionRegistry) SupersededAfter(previous SessionEntry, instance string) []SupersededInstance {
	now := r.now()
	var kept []SupersededInstance
	if previous.ClientInstanceID != "" && instance != "" && previous.ClientInstanceID != instance {
		kept = append(kept, SupersededInstance{ClientInstanceID: previous.ClientInstanceID, At: now.UnixMilli()})
	}
	for _, superseded := range previous.SupersededInstances {
		if len(kept) == SupersededInstanceLimit {
			break
		}
		if superseded.ClientInstanceID == "" || superseded.ClientInstanceID == instance ||
			superseded.ClientInstanceID == previous.ClientInstanceID ||
			now.Sub(time.UnixMilli(superseded.At)) >= SupersededInstanceMemory {
			continue
		}
		kept = append(kept, superseded)
	}
	return kept
}
