package supervise

import (
	"cmp"
	"context"
	"slices"
)

// TurnOutcome is one turn of a claim's agent as the backend's producer reported it when that turn
// ended: the launch generation it ran at, the producer's own sequence within that generation,
// whether the provider refused the turn, and what the provider said about a refusal. The daemon
// keeps each outcome as a record, and decides a refused turn from the HTTP status its report
// carries (turnOutcome): a status that cannot clear fails the claim, and any other refusal, one
// that names no status among them, fails nothing however long the run.
//
// The producer is the one counter. Oh My Pi's turns are reported by pi-legion's agent_end hook and
// Claude Code's by the Claude bridge, each over POST /legion/v1/claims/turn-outcome; the turn's end
// on the worker stream (StreamTurnEnd) and every turn ordinal the daemon keeps take no part in
// this, so no two counters can drift. A turn no report arrived for — the process died before it
// could send one — is a key the window never holds, which counts neither way.
//
// Run is how many refused turns the entry stands for when the window's bound has counted older
// refused reports into it (recordTurnOutcome), and zero for an entry that stands for its own turn
// alone (turns).
//
// The tags are the shape the store keeps the window in (the claims table's turn_outcomes), which
// is the one place a report is written.
type TurnOutcome struct {
	Generation uint64 `json:"generation"`
	Sequence   uint64 `json:"sequence"`
	Refused    bool   `json:"refused"`
	Message    string `json:"message,omitempty"`
	Run        int    `json:"run,omitempty"`
}

// turns is how many turns of the run the entry counts: its own, and the older refused turns the
// window's bound counted into it.
func (o TurnOutcome) turns() int { return max(o.Run, 1) }

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
// the existing bounded storage size; a refused report the bound drops is counted into the next
// one where the run goes on through both (recordTurnOutcome), so the bound holds the storage and
// not the count.
func (m *Machine) turnOutcomeWindow() int { return max(m.deps.Limits.LaunchFailures, 1) }

// recordTurnOutcome puts a report in the claim's window, in (generation, sequence) order, and
// trims the window to its bound (turnOutcomeWindow). A report whose key the window already holds
// replaces it, so a producer that repeats one changes nothing. The oldest entry the bound drops is
// counted into the entry after it when both are refused (TurnOutcome.Run): the run goes on
// through the two, so the run a reader derives from the window (RefusedRun) is the whole run
// however long it gets. A dropped entry whose run the next entry does not continue ended before
// it, and counts toward no run a reader reads. No other count is kept: every reader derives what
// it needs from the window as it stands, which is what makes a late report and an in-order one
// leave the same answer. A report so late that its key is older than every entry the window keeps
// is counted in the place the bound gives it, the window's oldest end.
func (m *Machine) recordTurnOutcome(o TurnOutcome) {
	if len(o.Message) > maxRefusalMessage {
		if runes := []rune(o.Message); len(runes) > maxRefusalMessage {
			o.Message = string(runes[:maxRefusalMessage]) + "…"
		}
	}
	at, found := slices.BinarySearchFunc(m.claim.TurnOutcomes, o, compareTurnOutcomes)
	if found {
		if o.Refused {
			// The repeat of an entry the bound counted older turns into still stands for them.
			o.Run = m.claim.TurnOutcomes[at].Run
		}
		m.claim.TurnOutcomes[at] = o
	} else {
		m.claim.TurnOutcomes = slices.Insert(m.claim.TurnOutcomes, at, o)
	}
	for len(m.claim.TurnOutcomes) > m.turnOutcomeWindow() {
		if dropped, next := m.claim.TurnOutcomes[0], &m.claim.TurnOutcomes[1]; dropped.Refused && next.Refused {
			next.Run = next.turns() + dropped.turns()
		}
		m.claim.TurnOutcomes = slices.Delete(m.claim.TurnOutcomes, 0, 1)
	}
}

// RefusedRun is the run of refused turns the claim's reports end with, in the order the turns
// ran, counted whole however long it is (recordTurnOutcome). It is derived on every read and
// stored nowhere. It is a record: the controller's notice reads it beside the prompt results' run
// (Claim.errorRun), and it bounds nothing.
func (c Claim) RefusedRun() int {
	run := 0
	for i := len(c.TurnOutcomes) - 1; i >= 0 && c.TurnOutcomes[i].Refused; i-- {
		run += c.TurnOutcomes[i].turns()
	}
	return run
}

// errorRun is the claim's run of turns in a row that a provider error ended, as the larger of the
// two counts that see such turns: the prompt results' (Claim.ProviderErrorRun), which see every
// turn a daemon prompt ran, and the producer's reports' (RefusedRun), which see every turn, the
// turns no daemon prompt ran among them. A turn both see moves each by one, so the larger is the
// run with no turn counted twice; where the producer reports nothing, the prompt results' count
// stands alone. The controller's notice reads it (tellControllerOfProviderErrors).
func (c Claim) errorRun() int { return max(c.ProviderErrorRun, c.RefusedRun()) }

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
// produced them: a claim that forgets its session drops them, and so does a compaction that ran,
// which starts the session's run of refused turns again (compactEnded).
func (m *Machine) freshRun() { m.claim.TurnOutcomes = nil }

// turnOutcome keeps the turn its producer reported as a record, and decides a refused one from
// the HTTP status its report carries, by the rule a prompt result is decided by
// (holdOnProviderError): a status that cannot clear fails the claim at once, and so holds the
// issue for its architect. That is the one decision a turn no daemon prompt ran gets, since no
// prompt result follows it: a phase reminder's turn, an Envoy message's, the agent's own. A
// refusal whose status can clear, and one whose report names no status, fails nothing however
// long the run; it counts toward the run the project's controller is told of at six
// (tellControllerOfProviderErrors), as a turn a prompt ran does.
//
// A report that arrives while a prompt of the claim's task is in flight or waits on its result
// (promptAwaited) is that prompt's turn's, since the pinned Oh My Pi and the Claude bridge each
// run one prompt at a time: the prompt's own result decides it (providerErrored), with what only
// it carries — Oh My Pi's retryable flag, the model, and the task it takes back — so the report
// leaves the decision to it rather than racing it. A report that arrives after that result finds
// the decision made: a failed claim takes no report, and a status that can clear fails nothing.
func turnOutcome(m *Machine, ctx context.Context, ev Event) error {
	r := ev.(RequestTurnOutcome)
	before := m.claim.errorRun()
	m.recordTurnOutcome(TurnOutcome{Generation: r.Generation, Sequence: r.Sequence, Refused: r.Refused, Message: r.Message})
	awaited := m.promptAwaited()
	m.log.Info("supervise: a turn ended", "generation", r.Generation, "sequence", r.Sequence,
		"refused", r.Refused, "httpStatus", r.HTTPStatus, "refusedRun", m.claim.RefusedRun(), "promptResultDecides", awaited)
	if r.Refused {
		m.tellControllerOfProviderErrors(before, "", providerErrorCause(r.HTTPStatus))
		if !awaited {
			if held, err := m.holdOnProviderError(ctx, r.HTTPStatus); held || err != nil {
				return err
			}
		}
	}
	return m.persist(ctx)
}

// promptAwaited is whether a prompt of the claim's task is in flight (Machine.send) or
// acknowledged and waiting on its result (awaitsResult): the turn running or just ended is that
// prompt's, and its result is still to come.
func (m *Machine) promptAwaited() bool { return m.send != nil || m.awaitsResult() }

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
