package supervise

import (
	"cmp"
	"context"
	"slices"

	"github.com/sjawhar/legion/daemon/internal/redact"
	"github.com/sjawhar/legion/daemon/internal/store/storetext"
)

// TurnOutcome is one turn of a claim's agent as the backend's producer reported it when that turn
// ended: the launch generation it ran at, the producer's own sequence within that generation,
// whether the provider refused the turn, and what the provider said about a refusal. The daemon
// keeps each outcome as a record, and decides a refused turn from the HTTP status its report
// carries (turnOutcome): a status that cannot clear fails the claim, and any other refusal, one
// that names no status among them, fails nothing however long the run.
//
// The producer is the one counter. Oh My Pi's turns are reported by pi-legion's agent_end hook and
// Claude Code's by the Claude bridge, each over POST /legion/v1/claims/turn-outcome, one report for
// each turn its harness yields: an attempt the harness goes on from by itself — Oh My Pi's
// willContinue end of a retry or of a walk down its fallback chain, Claude Code's api_retry — is no
// turn and is not reported, so a turn moves the run of refused reports by one, as its prompt result
// moves the run of provider errors (Claim.ProviderErrorRun) by one. The turn's end on the worker
// stream (StreamTurnEnd) and every turn ordinal the daemon keeps take no part in this, so no two
// counters can drift. A turn no report arrived for — the process died before it could send one —
// is a key the claim never holds, which counts neither way.
//
// The tags are the shape the store keeps the reports in (the claims table's turn_outcomes), which
// is the one place a report is written.
type TurnOutcome struct {
	Generation uint64 `json:"generation"`
	Sequence   uint64 `json:"sequence"`
	Refused    bool   `json:"refused"`
	Message    string `json:"message,omitempty"`
}

// compareTurnOutcomes orders reports as their turns ran: by generation, and within a generation by
// the producer's own sequence. It is the order the reports are kept in and the run is derived in,
// so a report that arrives late or out of order lands in its own place rather than at the end.
func compareTurnOutcomes(a, b TurnOutcome) int {
	if by := cmp.Compare(a.Generation, b.Generation); by != 0 {
		return by
	}
	return cmp.Compare(a.Sequence, b.Sequence)
}

// maxRefusalMessage bounds, in runes, the provider's message a report carries. The store keeps
// the newest reports for readers that inspect the provider's outcome, so a provider that returns a
// page of diagnostics leaves a sentence rather than a page in the store. A message past the bound
// keeps its first maxRefusalMessage-1 runes and "…" (redact.Runes).
const maxRefusalMessage = 500

// turnOutcomeWindow is how many reports a claim keeps whole, the provider's words included, and
// how many it keeps at all when its newest report is clean. The launch failure limit supplies the
// bound; the trailing run of refused turns is kept past it, one key a turn (recordTurnOutcome).
func (m *Machine) turnOutcomeWindow() int { return max(m.deps.Limits.LaunchFailures, 1) }

// recordTurnOutcome puts a report among the claim's reports, in (generation, sequence) order, and
// trims them. A report whose key the claim already keeps replaces it, so a producer that repeats
// one changes nothing.
//
// The trim keeps the newest turnOutcomeWindow reports and, past them, every report from the
// newest clean one on: the trailing run of refused turns, and the clean turn before it that it
// started after. So every turn the run a reader derives (RefusedRun) counts is a key the claim
// still keeps, for as long as the run can grow, and each counts once whenever its report arrives:
// a repeat of a turn of the run finds its key and changes nothing, a late report of a turn inside
// the run lands in its own place and joins it, and a late clean one ends the run there. A report
// older than the newest clean one is in no run a reader reads, so the trim drops it once the bound
// is reached. A clean turn ends the run, and the trim then drops the old run's keys down to the
// bound. Past the bound a kept report keeps its key and outcome and drops the provider's words, so
// a long run costs a key a turn and no more. No count is kept anywhere: every reader derives what
// it needs from the reports as they stand, which is what makes a late report and an in-order one
// leave the same answer.
//
// The message is the provider's text, so it is kept as the daemon keeps a far side's text
// (storetext.FarSideText: made valid, then read whole by the URL rule) before redact.Runes cuts
// it, so the reports in memory are the reports the store holds, and the cut cannot leave half a
// credential.
func (m *Machine) recordTurnOutcome(o TurnOutcome) {
	o.Message = redact.Runes(storetext.FarSideText(o.Message), maxRefusalMessage)
	outcomes := m.claim.TurnOutcomes
	if at, found := slices.BinarySearchFunc(outcomes, o, compareTurnOutcomes); found {
		outcomes[at] = o
	} else {
		outcomes = slices.Insert(outcomes, at, o)
	}
	window := m.turnOutcomeWindow()
	newestClean := -1
	for i := len(outcomes) - 1; i >= 0; i-- {
		if !outcomes[i].Refused {
			newestClean = i
			break
		}
	}
	if drop := min(len(outcomes)-window, newestClean); drop > 0 {
		outcomes = slices.Delete(outcomes, 0, drop)
	}
	for i := range len(outcomes) - window {
		outcomes[i].Message = ""
	}
	m.claim.TurnOutcomes = outcomes
}

// RefusedRun is the run of refused turns the claim's reports end with, in the order the turns
// ran: the refused reports after the newest clean one, each of which the claim keeps however long
// the run gets (recordTurnOutcome). It is derived on every read and stored nowhere. It is a record:
// the controller's notice reads it beside the prompt results' run (Claim.errorRun), and it bounds
// nothing.
func (c Claim) RefusedRun() int {
	run := 0
	for i := len(c.TurnOutcomes) - 1; i >= 0 && c.TurnOutcomes[i].Refused; i-- {
		run++
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
// leaves the decision and the controller's notice to it rather than racing it, and marks that it
// has counted the turn (Machine.refusalAwaitsResult). A report that arrives after that result
// finds the decision made: a failed claim takes no report, and a status that can clear fails
// nothing.
func turnOutcome(m *Machine, ctx context.Context, ev Event) error {
	r := ev.(RequestTurnOutcome)
	before := m.claim.errorRun()
	m.recordTurnOutcome(TurnOutcome{Generation: r.Generation, Sequence: r.Sequence, Refused: r.Refused, Message: r.Message})
	awaited := m.promptAwaited()
	m.log.Info("supervise: a turn ended", "generation", r.Generation, "sequence", r.Sequence,
		"refused", r.Refused, "httpStatus", r.HTTPStatus, "refusedRun", m.claim.RefusedRun(), "promptResultDecides", awaited)
	m.refusalAwaitsResult = r.Refused && awaited
	if r.Refused && !awaited {
		m.tellControllerOfProviderErrors(before, "", providerErrorCause(r.HTTPStatus))
		if held, err := m.holdOnProviderError(ctx, r.HTTPStatus); held || err != nil {
			return err
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
