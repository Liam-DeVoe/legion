package supervise

import (
	"cmp"
	"context"
	"slices"
)

// TurnOutcome is one turn of a claim's agent as the backend's producer reported it when that turn
// ended: the launch generation it ran at, the producer's own sequence within that generation,
// whether the provider refused the turn, and what the provider said about a refusal.
//
// The producer is the one counter. Oh My Pi's turns are reported by pi-legion's agent_end hook and
// Claude Code's by the Claude bridge, each over POST /legion/v1/claims/turn-outcome; the turn's end
// on the worker stream (StreamTurnEnd) and every turn ordinal the daemon keeps take no part in
// this, so no two counters can drift. A turn no report arrived for — the process died before it
// could send one — is a key the window never holds, which counts neither way.
//
// The tags are the shape the store keeps the window in (the claims table's turn_outcomes), which
// is the one place a report is written.
type TurnOutcome struct {
	Generation uint64 `json:"generation"`
	Sequence   uint64 `json:"sequence"`
	Refused    bool   `json:"refused"`
	Message    string `json:"message,omitempty"`
}

// compareTurnOutcomes orders reports as their turns ran: by generation, and within a generation by
// the producer's own sequence. It is the order the window is kept in and the run is derived in, so
// a report that arrives late or out of order lands in its own place rather than at the end.
func compareTurnOutcomes(a, b TurnOutcome) int {
	if by := cmp.Compare(a.Generation, b.Generation); by != 0 {
		return by
	}
	return cmp.Compare(a.Sequence, b.Sequence)
}

// maxRefusalMessage bounds, in runes, the provider's message a report carries. The store keeps
// the newest reports for readers that inspect the provider's outcome, so a provider that returns a
// page of diagnostics leaves a sentence rather than a page in the store.
const maxRefusalMessage = 500

// turnOutcomeWindow is how many recent reports a claim keeps. The launch failure limit supplies
// the existing bounded storage size, but the retained reports do not decide a claim's state.
func (m *Machine) turnOutcomeWindow() int { return m.deps.Limits.LaunchFailures }

// recordTurnOutcome puts a report in the claim's window, in (generation, sequence) order, and
// trims the window to its bound (turnOutcomeWindow). A report whose key the window already holds
// replaces it, so a producer that repeats one changes nothing. No count is kept anywhere: every
// reader derives what it needs from the window as it stands (RefusedRun), which is what makes a
// late report and an in-order one leave the same answer.
func (m *Machine) recordTurnOutcome(o TurnOutcome) {
	if len(o.Message) > maxRefusalMessage {
		if runes := []rune(o.Message); len(runes) > maxRefusalMessage {
			o.Message = string(runes[:maxRefusalMessage]) + "…"
		}
	}
	at, found := slices.BinarySearchFunc(m.claim.TurnOutcomes, o, compareTurnOutcomes)
	if found {
		m.claim.TurnOutcomes[at] = o
	} else {
		m.claim.TurnOutcomes = slices.Insert(m.claim.TurnOutcomes, at, o)
	}
	if extra := len(m.claim.TurnOutcomes) - m.turnOutcomeWindow(); extra > 0 {
		m.claim.TurnOutcomes = slices.Delete(m.claim.TurnOutcomes, 0, extra)
	}
}

// RefusedRun is the run of refused turns the claim's retained reports end with, in the order the
// turns ran. It is derived on every read and stored nowhere. It is diagnostic information only:
// a provider-error result decides whether a claim can continue.
func (c Claim) RefusedRun() int {
	run := 0
	for i := len(c.TurnOutcomes) - 1; i >= 0 && c.TurnOutcomes[i].Refused; i-- {
		run++
	}
	return run
}

// LastRefusal is what the provider said about the newest refused turn the claim keeps, and "" when
// it keeps none or the provider said nothing.
func (c Claim) LastRefusal() string {
	for i := len(c.TurnOutcomes) - 1; i >= 0; i-- {
		if c.TurnOutcomes[i].Refused {
			return c.TurnOutcomes[i].Message
		}
	}
	return ""
}

// freshRun drops every turn the claim has reports for. The reports belong to the session that
// produced them and a claim leaves a session only by forgetting it.
func (m *Machine) freshRun() { m.claim.TurnOutcomes = nil }

// turnOutcome records the turn its producer reported and derives the claim's trailing run of
// refused turns from the window that leaves. The report carries no HTTP status, so it cannot
// decide whether the provider error can clear. The prompt result makes that decision.
func turnOutcome(m *Machine, ctx context.Context, ev Event) error {
	r := ev.(RequestTurnOutcome)
	m.recordTurnOutcome(TurnOutcome{Generation: r.Generation, Sequence: r.Sequence, Refused: r.Refused, Message: r.Message})
	run := m.claim.RefusedRun()
	m.log.Info("supervise: a turn ended", "generation", r.Generation, "sequence", r.Sequence,
		"refused", r.Refused, "refusedRun", run, "limit", m.deps.Limits.LaunchFailures)
	return m.persist(ctx)
}

// freshSession is whether a retry explicitly asks to start a new session, and why, for the line it
// logs. A provider refusal does not choose a new session.
func freshSession(ev Event) (bool, string) {
	switch ev := ev.(type) {
	case RequestRetry:
		if ev.FreshSession {
			return true, "the retry asked for a new session"
		}
	case RequestOperatorRetry:
		if ev.FreshSession {
			return true, "the retry asked for a new session"
		}
	}
	return false, ""
}
