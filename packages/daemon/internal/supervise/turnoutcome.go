package supervise

import (
	"cmp"
	"context"
	"fmt"
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
//
// Unapplied says that what the report says of the claim's task (reportedTask) was never done: the
// write of the report failed, which ends the decision before that step. It is the one thing that
// tells a report the producer sends again under the same key from a report already applied
// (turnOutcome), and it is written with the window, so a daemon that read the window back tells
// the two apart as the daemon that recorded it would.
type TurnOutcome struct {
	Generation uint64 `json:"generation"`
	Sequence   uint64 `json:"sequence"`
	Refused    bool   `json:"refused"`
	Message    string `json:"message,omitempty"`
	Unapplied  bool   `json:"unapplied,omitempty"`
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

// maxRefusalMessage bounds, in runes, the provider's message a report carries. The message reaches
// the claim's failure, which is read back in notices and in every state answer, so a provider that
// returns a page of diagnostics leaves a sentence rather than a page in the store.
const maxRefusalMessage = 500

// turnOutcomeWindow is how many reports a claim keeps: the launch failure limit
// (launch_failure_limit, 3 by default), which is the length the trailing run has to reach to fail
// the claim. A longer run cannot exist, since the claim fails the moment it reaches the limit, so
// the newest limit reports are everything RefusedRun can read; a report older than all of them
// cannot shorten a run derived over them, and is dropped. That bound is why a claim whose agent
// works for a week carries a handful of rows rather than a turn-by-turn history nothing reads.
func (m *Machine) turnOutcomeWindow() int { return m.deps.Limits.LaunchFailures }

// recordTurnOutcome puts a report in the claim's window, in (generation, sequence) order, and
// trims the window to its bound (turnOutcomeWindow). A report whose key the window already holds
// replaces it and answers repeat: the producer sent it again because it heard no answer, and its
// first attempt was applied. The one exception is a first attempt whose write failed
// (TurnOutcome.Unapplied), which never reached the task, so the report sent again is its first
// application rather than a repeat. No count is kept anywhere: every reader derives what it needs
// from the window as it stands (RefusedRun), which is what makes a late report and an in-order
// one leave the same answer.
func (m *Machine) recordTurnOutcome(o TurnOutcome) (repeat bool) {
	if len(o.Message) > maxRefusalMessage {
		if runes := []rune(o.Message); len(runes) > maxRefusalMessage {
			o.Message = string(runes[:maxRefusalMessage]) + "…"
		}
	}
	at, found := slices.BinarySearchFunc(m.claim.TurnOutcomes, o, compareTurnOutcomes)
	if found {
		repeat = !m.claim.TurnOutcomes[at].Unapplied
		m.claim.TurnOutcomes[at] = o
	} else {
		m.claim.TurnOutcomes = slices.Insert(m.claim.TurnOutcomes, at, o)
	}
	if extra := len(m.claim.TurnOutcomes) - m.turnOutcomeWindow(); extra > 0 {
		m.claim.TurnOutcomes = slices.Delete(m.claim.TurnOutcomes, 0, extra)
	}
	return repeat
}

// appliedTurnOutcome is the window's entry under the key of r, where the window holds one from an
// attempt that was applied (TurnOutcome.Unapplied false).
func (m *Machine) appliedTurnOutcome(r RequestTurnOutcome) (TurnOutcome, bool) {
	key := TurnOutcome{Generation: r.Generation, Sequence: r.Sequence}
	at, found := slices.BinarySearchFunc(m.claim.TurnOutcomes, key, compareTurnOutcomes)
	if !found || m.claim.TurnOutcomes[at].Unapplied {
		return TurnOutcome{}, false
	}
	return m.claim.TurnOutcomes[at], true
}

// unappliedTurnOutcome marks the window's entry for o's key as a report that never reached the
// claim's task (TurnOutcome.Unapplied). A report the window trimmed away has no entry to mark.
func (m *Machine) unappliedTurnOutcome(o TurnOutcome) {
	if at, found := slices.BinarySearchFunc(m.claim.TurnOutcomes, o, compareTurnOutcomes); found {
		m.claim.TurnOutcomes[at].Unapplied = true
	}
}

// RefusedRun is the run of refused turns the claim's reports end with, in the order the turns ran.
// It is derived on every read and stored nowhere, so no count is ever mutated and a report that
// arrives late lands in its place and is counted there. The run carries across generations: a
// session the provider refused is refused again when it is resumed, which is why the retry of a
// claim this failed starts a new session (freshSession), and a new session starts a fresh run
// (freshRun).
func (c Claim) RefusedRun() int {
	run := 0
	for i := len(c.TurnOutcomes) - 1; i >= 0 && c.TurnOutcomes[i].Refused; i-- {
		run++
	}
	return run
}

// LastRefusal is what the provider said about the newest refused turn the claim keeps, and "" when
// it keeps none or the provider said nothing: the sentence the claim's failure names.
func (c Claim) LastRefusal() string {
	for i := len(c.TurnOutcomes) - 1; i >= 0; i-- {
		if c.TurnOutcomes[i].Refused {
			return c.TurnOutcomes[i].Message
		}
	}
	return ""
}

// freshRun drops every turn the claim has reports for, so its refused run starts again. The run
// belongs to the session whose turns were refused, and a claim leaves a session only by forgetting
// it (forgetSession): every relaunch resumes the session the claim recorded, and a registration
// naming another is refused, so the forgetting is the one moment a fresh run begins.
func (m *Machine) freshRun() { m.claim.TurnOutcomes = nil }

// turnOutcome records the turn its producer reported and derives the claim's trailing run of
// refused turns from the window that leaves. At the launch failure limit the claim fails: the
// provider has refused every turn since the last one it took, so nothing of the issue's work is
// being done and every later turn of that session would be refused the same way
// (dispatch://INFRA-714). A run short of the limit is the report written, and then what the
// report says of the claim's task (reportedTask).
//
// A refused turn that carries a provider error is counted here only when no provider-error
// ladder takes it. The ladder takes the turn of the task a take of this claim holds: a report
// that names that task's number (reachesLadder) starts the ladder's wait and keeps the task
// (reportedTask, providererror.go), and the ladder has no attempt limit (dispatch://INFRA-956 R2)
// and tells the project's controller at a run of its own (providerErrorRunTold), so counting
// such a turn here as well would fail the claim out of the launch budget before that run could
// ever be reached. A refused turn whose report names no task, or a number no take of the claim
// holds — a reminder's, a card's, a background result's, a late report of a task already
// finished — reaches no ladder, and nothing else counts it, so it counts toward the refused run
// whatever error it carries: that is the run of refusals INFRA-714 fails the claim at, and since
// both producers name a provider error beside every refused turn (claudebridge's turnEnded,
// pi-legion's turn-outcome), it is the only way such a run is counted at all. A refused turn
// that names no provider error counts as it always has.
//
// A repeat (recordTurnOutcome) is written and goes no further. The producer sends a report again
// when its first attempt had no answer, which a daemon restart in the middle of the request
// leaves too, so the repeat can find a claim the first attempt already changed: a task that
// attempt took back and that the restarted daemon has given again. Applying the repeat to that
// task would take back a take no turn has run on, and count the provider error a second time. A
// repeat that carries a provider error keeps the count its first attempt made, too: whether that
// turn reached a ladder was decided against the claim as the first attempt found it, and the
// take that first attempt took back reaches no ladder any more. A report whose write fails is
// marked as never applied (TurnOutcome.Unapplied), so the producer's repeat of it still does what
// it says of the task.
func turnOutcome(m *Machine, ctx context.Context, ev Event) error {
	r := ev.(RequestTurnOutcome)
	refused := r.Refused && (r.ProviderError == nil || !m.reachesLadder(r))
	if first, applied := m.appliedTurnOutcome(r); applied && r.ProviderError != nil {
		refused = first.Refused
	}
	o := TurnOutcome{Generation: r.Generation, Sequence: r.Sequence, Refused: refused, Message: r.Message}
	repeat := m.recordTurnOutcome(o)
	run := m.claim.RefusedRun()
	m.log.Info("supervise: a turn ended", "generation", r.Generation, "sequence", r.Sequence,
		"refused", refused, "providerError", r.ProviderError != nil, "refusedRun", run, "limit", m.deps.Limits.LaunchFailures)
	if run >= m.deps.Limits.LaunchFailures {
		return m.fail(ctx, CauseProviderError, refusalCause(run, m.claim.LastRefusal()))
	}
	if err := m.persist(ctx); err != nil {
		if !repeat {
			m.unappliedTurnOutcome(o)
		}
		return err
	}
	if repeat {
		m.log.Info("supervise: a turn's report repeats one already applied; it changes nothing more",
			"generation", r.Generation, "sequence", r.Sequence, "task", r.Task)
		return nil
	}
	return m.reportedTask(ctx, r)
}

// reportedTask is what a turn-outcome report says of the claim's task. A report that names the
// number of the task a take holds is that task's turn reported over: one that ends in a provider
// error keeps the task under its number and starts the ladder's wait (providerErrored), and any
// other finishes it (finishTask) and ends the claim's run of provider-error turns. A report that
// names another number, or none, finishes nothing: a late report of a task already finished
// must not touch the one that replaced it, and a worker whose plugin names no number has its task
// finished by the end of the turn instead (finishAtTurnsEnd, endFrameFinishes). Where the take's
// turn had started, such a report from a plugin that names its task's number says that turn was
// not the take's (notTheTakesTurn).
//
// A report can name the accepted take's number before this daemon saw that take's turn start:
// the report comes over HTTP and the start over the worker stream, and nothing orders the two.
// The turn ran all the same, so what its start does for the take is done first (startedUnseen),
// and the task is then finished, or taken back, as the turn's report says.
//
// A report that beats the end of its turn finishes the task while the claim is still working,
// and the end then runs what was held for the turn (turnEnded); one that follows the end runs it
// here (afterTurn).
func (m *Machine) reportedTask(ctx context.Context, r RequestTurnOutcome) error {
	p := m.claim.Pending
	if !m.reachesLadder(r) {
		if r.Task != 0 {
			m.log.Info("supervise: a turn's report names a task that no take of this claim holds; it finishes nothing",
				"task", r.Task, "pending", pendingNumber(p))
		}
		return m.notTheTakesTurn(ctx, r)
	}
	if p.awaitsTurn() {
		if err := m.startedUnseen(ctx); err != nil {
			return err
		}
	}
	if r.ProviderError != nil {
		if err := m.providerErrored(ctx, *r.ProviderError); err != nil {
			return err
		}
	} else {
		if m.claim.ProviderErrorRun != 0 {
			m.log.Info("supervise: a turn of this task ended with no provider error; the run of provider errors is over",
				"task", p.Number, "providerErrorRun", m.claim.ProviderErrorRun)
			m.claim.ProviderErrorRun = 0
		}
		if err := m.finishTask(ctx, finishedByReport); err != nil {
			return err
		}
	}
	if (m.claim.State != StateReady && m.claim.State != StateIdle) || m.compacting != nil {
		return nil
	}
	return m.afterTurn(ctx)
}

// reachesLadder is whether a turn-outcome report is of the turn of the task a take of this claim
// holds: it names that task's number, and the take stands accepted, its turn started or not. A
// report of that turn is what finishes the task or, carrying a provider error, takes it to the
// ladder (reportedTask); any other report finishes nothing and reaches no ladder, so its refusal
// is the launch budget's (turnOutcome).
func (m *Machine) reachesLadder(r RequestTurnOutcome) bool {
	p := m.claim.Pending
	return r.Task != 0 && p != nil && p.Number == r.Task && p.taken()
}

// notTheTakesTurnLine is the line of a take given its turn back (notTheTakesTurn), at Info, with
// the task's number.
const notTheTakesTurnLine = "supervise: a turn's report names no task of the take whose turn it was given; the take awaits its turn again"

// notTheTakesTurn is a turn-outcome report naming no task, or another task's number, while the
// accepted take of the pending task has its turn started: the turn the daemon gave the take —
// the first that started after it was accepted (turnStarted) — was not the take's. The worker can
// be answered the take while a run no take started is running, a background result's or a
// reminder's, and it holds the text until that run is over and reports the run as no task's; or
// its answer was lost and it asked again, and was answered the same text after that run started
// (answeredFromRecord). Either way the text's own turn is still to come. So where the claim's
// plugin names its task's number (TakesNoTask is false: it reports every turn of a task with the
// number), the take goes back to awaiting its turn: the delivery's turn start is undone
// (TurnStarted) with the text it was answered kept, and the next turn that starts is the take's
// turn. No end-frame bound stands over such a claim's task to finish it meanwhile
// (endFrameFinishes): its report is what finishes it. R5's second wait is on the take again
// from this decision (secondWaitNow, armWaits), and the claim counts as in a turn for another
// role's adoption until that turn ends (Claim.InTurn).
//
// A plugin below TaskTakeContract names no number in any report, so for its claim a report
// naming none says nothing of which turn ran, and the end of the take's turn finishes the task
// as before (finishAtTurnsEnd): that is what finishes the task a migrated old worker took
// (decision 8 of dispatch://INFRA-2811). A report of an earlier generation than the claim's is
// of a turn no take of this process held, and says nothing of it either; and a take whose text no
// record kept (Delivery) has no text to await its turn with.
//
// The undoing stands in memory whether or not its write lands: a write that fails leaves the
// delivery unsaved, and settle writes it again.
func (m *Machine) notTheTakesTurn(ctx context.Context, r RequestTurnOutcome) error {
	p := m.claim.Pending
	if p == nil || !p.turnStarted() || p.TakenText == "" || r.Generation != m.claim.Generation ||
		TakesNoTask(m.claim.PluginContract) {
		return nil
	}
	m.log.Info(notTheTakesTurnLine, "task", p.Number)
	p.TurnStarted = false
	return m.putPending(ctx)
}

// startedUnseen is the start of the accepted take's turn, done at that turn's report because the
// report came first (reportedTask). The turn is the take's: the delivery records its turn started
// and the claim is written with it (startTakesTurn), so the finish that follows earns the claim
// the serving credit a finished take's turn earns (retireTask) rather than reading the task as
// never run; and the prompt budgets go to zero, as at any turn's start (R5).
//
// A ready or idle claim stays where it is: the report says the turn is over, and no turn's times
// are written. The start frame still to come is this turn's, which the machine remembers
// (Machine.reportedAhead), so its arrival names this task and starts no second turn of it
// (turnStarted). A working claim's start frame has come already: the take awaited its turn again
// because an earlier report said the turn it was given was not its own (notTheTakesTurn), while
// the text's turn was the one running, so no start is still to come for it.
func (m *Machine) startedUnseen(ctx context.Context) error {
	p := m.claim.Pending
	m.log.Info("supervise: a turn's report names the accepted take's task before its turn start was seen; the turn is the take's",
		"task", p.Number)
	m.resetPromptBudgets()
	if m.claim.State != StateWorking {
		m.reportedAhead = p.Number
	}
	return m.startTakesTurn(ctx)
}

// pendingNumber is the number of the task pending, 0 for none.
func pendingNumber(p *Delivery) uint64 {
	if p == nil {
		return 0
	}
	return p.Number
}

// refusalCause is the failure a spent run of refused turns names: what refused the claim, how many
// turns in a row, and the provider's own words for the newest of them, which is the one fact a
// person reading the failed claim can act on. The wording is the architect's own
// (dispatch://INFRA-714/artifact/review-rounds-md, item 2 of the refusal section), and the
// architect's notice of the failure quotes it.
func refusalCause(run int, message string) string {
	if message == "" {
		return fmt.Sprintf("the provider refused %d turns in a row, and said nothing about why", run)
	}
	return fmt.Sprintf("the provider refused %d turns in a row: %s", run, message)
}

// freshSession is whether a retry starts a new session rather than relaunching the recorded one,
// and why, for the line it logs. An operator asks for one with `legion claims retry
// --fresh-session`. A claim the provider refused out of its budget takes one whoever retried it —
// its architect's retry_or_escalate, the next start of its role, or that operator — because
// resuming the session those turns were refused in is what would be refused again.
func freshSession(ev Event, c Claim, limit int) (bool, string) {
	asked := false
	switch ev := ev.(type) {
	case RequestRetry:
		asked = ev.FreshSession
	case RequestOperatorRetry:
		asked = ev.FreshSession
	}
	switch {
	case asked:
		return true, "the retry asked for a new session"
	case c.RefusedRun() >= limit:
		return true, "the provider refused the recorded session out of its launch budget"
	}
	return false, ""
}
