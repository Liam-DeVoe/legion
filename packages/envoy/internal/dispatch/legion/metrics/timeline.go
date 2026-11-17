package metrics

import (
	"slices"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/legion"
)

// Span is the half-open interval [From, To). Every rule here is written over spans rather than
// over clock hours: "each hour of a phase" is the hour as a measure, so a segment that holds for
// ten minutes claims ten minutes.
type Span struct {
	From time.Time
	To   time.Time
}

// Duration is the span's length, zero for an empty or inverted one.
func (s Span) Duration() time.Duration {
	if !s.To.After(s.From) {
		return 0
	}
	return s.To.Sub(s.From)
}

// The two phases this package adds to the daemon's own, which are named once in
// internal/dispatch/legion (legion.PhaseAdmitted and the rest, mirroring
// packages/daemon/internal/phase) and read from there. They are its reading of `admitted`,
// which the daemon does not split: an admitted issue holding a slot is its architect's design
// ("Design: roots that hold a slot while their phase is still `admitted`"), and an admitted
// issue holding none is the waiting line, whose time is on no bar ("Time in the queue is on no
// bar").
const (
	phaseQueued = "queued"
	phaseDesign = "design"
)

// childWorkingPhases are the phases "from planning to awaiting merge": a root with a child in one
// of them is waiting on its children (segment 4).
var childWorkingPhases = []string{
	legion.PhasePlanning, legion.PhaseImplementing, legion.PhaseTesting, legion.PhaseReviewing,
	legion.PhaseAccepting, legion.PhaseRetro, legion.PhaseMerging, legion.PhaseAwaitingMerge,
}

// parkedStatuses are the statuses whose time the work time subtracts: the daemon writes `backlog`
// when it parks a root, and the gate treats both as parked (daemon workflow/gate.go).
var parkedStatuses = []string{"backlog", "icebox"}

// The statuses the workflow writes with the phases a fallback timeline reads: the implementer's
// `in_progress`, the tester's `testing`, `needs_review` for both the reviewer's and the
// architect's, and `retro`, which the workflow writes as the implementer's retro starts and keeps
// through the merger's merging (daemon workflow/table.go's phaseStatus).
const (
	statusImplementing = "in_progress"
	statusTesting      = "testing"
	statusNeedsReview  = "needs_review"
	statusRetro        = "retro"
)

// sentBackStatuses are the statuses a send-back moves out of before observations exist:
// `needs_review` is the status of both reviewing and accepting, `testing` is its own, and
// `retro` is the status of both retro and merging, out of which a head that starts conflicting
// with its base sends the issue back to implementing like any other (the daemon workflow's
// TriggerConflict). Those are the words the spec's "an `in_progress` after `testing`,
// `needs_review` or `retro` (a send-back) is implementing" is read by.
var sentBackStatuses = []string{statusTesting, statusNeedsReview, statusRetro}

// phaseOrder is the workflow's order of the phases a fallback timeline names, the order the
// daemon's table moves an issue through. One rule reads it: a role's first event starts that
// role's phase only where the issue has not reached that phase already (phaseSteps).
var phaseOrder = []string{
	phaseDesign, legion.PhasePlanning, legion.PhaseImplementing, legion.PhaseTesting,
	legion.PhaseReviewing, legion.PhaseAccepting, legion.PhaseRetro, legion.PhaseMerging,
}

// rolePhase is the phase each role's first event on the issue starts, until the issue is first
// sent back (phaseSteps). The implementer works two phases, implementing and retro, "because the
// daemon gives retro to the implementer" (RoleFor in packages/daemon/internal/phase), and its
// first event starts the first of them: retro starts at the `retro` status the workflow writes
// for it (statusPhase). The merger's first event starts merging, which the event log tells from
// retro by nothing else, since the workflow keeps the status `retro` through both: so before
// observations an issue holds no merging after its first send-back, and a retro there runs on to
// the next status or the close. No mark of the event log names awaiting merge.
var rolePhase = map[string]string{
	"architect":   phaseDesign,
	"planner":     legion.PhasePlanning,
	"implementer": legion.PhaseImplementing,
	"tester":      legion.PhaseTesting,
	"reviewer":    legion.PhaseReviewing,
	"merger":      legion.PhaseMerging,
}

// handoffStarts is the phase each handoff word starts, as the daemon's phase.Handoff pairs them:
// the planner's `plan` starts implementing, the implementer's `implement` starts testing, the
// tester's `test` starts reviewing and the reviewer's `review` starts accepting ("Accepting
// starts at the `review` handoff"). The spec's "a handoff starts the next phase" is this map: a
// handoff ends the phase it names and leaves no hour between the two. Retro and merging write
// none, since each reports the commit it stands on, and accepting ends at the architect's
// sign-off, which is no handoff (the daemon's phase.Handoff); a word Legion never writes names no
// phase.
var handoffStarts = map[string]string{
	"plan":      legion.PhaseImplementing,
	"implement": legion.PhaseTesting,
	"test":      legion.PhaseReviewing,
	"review":    legion.PhaseAccepting,
}

// PhaseSpan is one stretch of one phase.
type PhaseSpan struct {
	Phase string
	Span  Span
}

// Phases is the issue's phase timeline and the index of the first of its spans the sampler
// observed, which is the timeline's length for an issue the sampler never observed.
//
// The event log draws the timeline up to the issue's first observed phase, and the daemon's own
// observations draw it from there on: "An issue in flight when its first observation is stored
// takes its phases from observations only from that observation on. Its hours before it come
// from these status rules, so they keep the phases its events mark rather than all falling on
// the first phase the sampler saw" (the spec's "Statuses before observations"). So an issue the
// sampler met before any mark of the event log is its observations' whole, and one it never met
// is the event log's whole (appendStepSpans, the spec's "Phase boundaries before observations").
//
// Where the observations take over is the answer's second half because the rules that read a
// phase before observations — metric 3's review rounds, its send-backs and the first review its
// hours are counted from — read the status history up to that moment. An issue whose only
// observation is the mark the sampler left after it closed has no observed hour of its own life:
// the stretch that mark opens has no duration, so sampledPhases keeps nothing of it, and the
// whole of its life is before observations.
func (p *Population) Phases(issue *Issue) ([]PhaseSpan, int) {
	until := p.issueEnd(issue)
	steps := phaseSteps(p.fallbackMarks(issue))
	observed := p.sampledPhases(issue)
	if len(observed) > 0 {
		until = observed[0].Span.From
		if cut := slices.IndexFunc(steps, func(step phaseMark) bool { return !step.at.Before(until) }); cut >= 0 {
			steps = steps[:cut]
		}
	}
	// An hour of the work time before the first phase starts is on that phase's bar (the spec's
	// "Work hours before any phase mark"), so no hour of the work time is off every bar. The
	// first phase is the event log's first step where one falls before the first observation,
	// and the first observed phase where none does. Every role's first event is a mark of its
	// own, so this reaches back only on an issue no worker session wrote on, whose work time a
	// handoff naming no phase opens (firstWorkerEvent).
	if at, worked := firstWorkerEvent(issue); worked {
		switch {
		case len(steps) > 0:
			if at.Before(steps[0].at) {
				steps[0].at = at
			}
		case len(observed) > 0:
			if at.Before(observed[0].Span.From) {
				observed[0].Span.From = at
			}
		}
	}
	timeline := appendStepSpans(make([]PhaseSpan, 0, len(steps)+len(observed)), steps, until)
	firstObserved := len(timeline)
	return append(timeline, observed...), firstObserved
}

// sampledPhases is the timeline the sampler's `phase` observations draw and nothing else: each
// observed phase from its mark to the next, the last to the issue's end, with each admitted
// stretch split into the design the issue held a slot through and the queue it did not. It is
// the issue's observed phase at each moment, which is what the two rules that read only an
// observed phase stand on (slotWaitHours and idleByRole): an hour before the sampler first saw
// the issue has no observed phase, so it is on neither.
func (p *Population) sampledPhases(issue *Issue) []PhaseSpan {
	marks := issue.Observed[legion.FieldPhase]
	if len(marks) == 0 {
		return nil
	}
	end := p.issueEnd(issue)
	var timeline []PhaseSpan
	for index, mark := range marks {
		phase := legion.JSONString([]byte(mark.Value))
		if phase == "" {
			continue
		}
		span := Span{From: mark.At, To: end}
		if index+1 < len(marks) {
			span.To = marks[index+1].At
		}
		if span.Duration() == 0 {
			continue
		}
		if phase != legion.PhaseAdmitted {
			timeline = append(timeline, PhaseSpan{Phase: phase, Span: span})
			continue
		}
		held := intersect([]Span{span}, p.slotSpans(issue, end))
		for _, holding := range held {
			timeline = append(timeline, PhaseSpan{Phase: phaseDesign, Span: holding})
		}
		for _, waiting := range subtract([]Span{span}, held) {
			timeline = append(timeline, PhaseSpan{Phase: phaseQueued, Span: waiting})
		}
	}
	slices.SortFunc(timeline, func(a, b PhaseSpan) int { return a.Span.From.Compare(b.Span.From) })
	return timeline
}

// slotSpans are the stretches the issue held an admission slot, from its own `slot` observations:
// never from the daemon's `admission.active`, which dispatch://INFRA-980 widens to every issue
// holding one, children included.
func (p *Population) slotSpans(issue *Issue, end time.Time) []Span {
	return markSpans(issue.Observed[legion.FieldSlot], end, func(value string) bool {
		return value != "" && value != "null"
	})
}

// phaseMark is one moment the event log says the issue's phase changed, and what says so.
type phaseMark struct {
	at     time.Time
	source markSource
	phase  string
}

// markSource is which of the three sources wrote a mark. It is also the order two marks of one
// moment are read in, and the last of them is the phase: a role's first event is the earliest
// word on a change, the handoff that starts the phase is the next, and the status the workflow
// wrote with it is the last.
type markSource int

const (
	sourceRole markSource = iota
	sourceHandoff
	sourceStatus
)

// appendStepSpans appends the timeline the event log's steps draw up to until: the phase the
// issue is in, as its role events, its handoffs and its Dispatch statuses move it, each phase
// running to the next one's start and the last to until.
//
// The spec's "Phase boundaries before observations": "Each phase ends, and the next one starts,
// at the earlier of the phase's own handoff and the next role's first event ... So a handoff
// starts the next phase, and no hour between phases is left over ... After a send-back, the
// Dispatch status sets the phase". The three sources are therefore one list of marks and the
// timeline is the step function they draw, with no gap between one phase and the next — which is
// what lets the seven bars of metric 2 add up to the work time while the no-role bar holds
// `awaiting_person` and `held` alone.
func appendStepSpans(timeline []PhaseSpan, steps []phaseMark, until time.Time) []PhaseSpan {
	for index, step := range steps {
		span := Span{From: step.at, To: until}
		if index+1 < len(steps) && steps[index+1].at.Before(until) {
			span.To = steps[index+1].at
		}
		if span.Duration() == 0 {
			continue
		}
		timeline = append(timeline, PhaseSpan{Phase: step.phase, Span: span})
	}
	return timeline
}

// fallbackMarks is every mark the event log holds of an issue's phase, in time order: each role's
// first event on the issue, each handoff, and each change of its Dispatch status.
func (p *Population) fallbackMarks(issue *Issue) []phaseMark {
	architect, hasArchitect := issue.FirstRoleEvent["architect"]
	var marks []phaseMark
	add := func(source markSource, at time.Time, phase string) {
		marks = append(marks, phaseMark{at: at, source: source, phase: phase})
	}
	for role, at := range issue.FirstRoleEvent {
		if phase, works := rolePhase[role]; works && role != "architect" {
			add(sourceRole, at, phase)
		}
	}
	// "An issue has a design phase when a session titled `Legion architect · <its own key>` wrote
	// an event on it ... whether or not it has a parent. The design phase runs from the issue's
	// first slot, or else from that architect's first event". A slot the sampler recorded after
	// the architect had spoken is a later admission, not the start of the design, so the earlier
	// of the two opens the phase.
	if hasArchitect {
		if slots := p.slotSpans(issue, p.issueEnd(issue)); len(slots) > 0 && slots[0].From.Before(architect) {
			architect = slots[0].From
		}
		add(sourceRole, architect, phaseDesign)
	}
	for _, handoff := range issue.Handoffs {
		if phase, starts := handoffStarts[handoff.Phase]; starts {
			add(sourceHandoff, handoff.At, phase)
		}
	}
	previous := ""
	for _, mark := range issue.StatusMarks {
		if mark.Value == previous {
			continue
		}
		if phase, named := statusPhase(previous, mark.Value); named {
			add(sourceStatus, mark.At, phase)
		}
		previous = mark.Value
	}
	slices.SortFunc(marks, func(a, b phaseMark) int {
		if at := a.at.Compare(b.at); at != 0 {
			return at
		}
		if source := int(a.source) - int(b.source); source != 0 {
			return source
		}
		return phaseRank(a.phase) - phaseRank(b.phase)
	})
	return marks
}

// phaseSteps is the step function the marks draw: the phase the issue is in, changing only where
// a mark changes it.
func phaseSteps(marks []phaseMark) []phaseMark {
	var steps []phaseMark
	reached := -1
	sentBack := false
	for _, mark := range marks {
		rank := phaseRank(mark.phase)
		// A role's first event starts that role's phase only where the issue has not reached
		// that phase already: the boundary the spec draws is "the next role's first event", so a
		// role that speaks again after its phase has passed — an implementer answering during
		// retro — does not send the issue back. Nor does any role's first event start a phase
		// once the issue has been sent back, however late that phase: "After a send-back, the
		// next phases come from status writes and handoffs" (the spec's "No re-entry by a first
		// event"). A handoff and a status move the phase either way, before a send-back and
		// after it.
		if mark.source == sourceRole && (sentBack || rank <= reached) {
			continue
		}
		// The send-back is the one status change statusPhase names implementing for: the
		// `in_progress` written over `testing`, `needs_review` or `retro` (sentBackStatuses),
		// the same move metric 3 counts as a send-back before observations (sentBackOnce). A
		// handoff that names an earlier phase moves the phase back, but it is no send-back.
		if mark.source == sourceStatus && mark.phase == legion.PhaseImplementing {
			sentBack = true
		}
		reached = max(reached, rank)
		last := len(steps) - 1
		if last >= 0 && steps[last].phase == mark.phase {
			continue
		}
		if last >= 0 && steps[last].at.Equal(mark.at) {
			// Two marks of one moment are one change, and the last word on it is the phase.
			steps[last].phase = mark.phase
			if last > 0 && steps[last-1].phase == mark.phase {
				steps = steps[:last]
			}
			continue
		}
		steps = append(steps, mark)
	}
	return steps
}

// phaseRank is a phase's place in the workflow's order, and -1 for one the fallback never names.
func phaseRank(phase string) int {
	return slices.Index(phaseOrder, phase)
}

// statusPhase is the phase a status change sets before observations exist: the spec's "After a
// send-back, the Dispatch status sets the phase: `in_progress` after `testing`, `needs_review` or
// `retro` is implementing, `testing` is testing, and `needs_review` is reviewing until the
// `review` handoff of that round, then accepting until the status changes" — the handoff mark is
// what turns that last stretch into accepting.
//
// The statuses that name no phase of their own — `todo`, `triage`, `backlog`, `icebox` and
// `done` — leave the phase as it was. The hours they stand for are the queue's and the parked
// ones, which the work time drops whatever phase the issue was in.
//
// Neither does the `in_progress` of admission: "A status write such as the `in_progress` of
// admission starts no phase" and "Design runs once, to the first planner event: an issue parked
// and admitted again goes back into the phase it left, not into design" (the spec's "What starts
// planning"). The design mark is `fallbackMarks`' own, from the issue's first slot or its
// architect's first event, and planning starts at the first planner event, so an `in_progress`
// here names the send-back alone.
func statusPhase(previous, status string) (string, bool) {
	switch status {
	case statusTesting:
		return legion.PhaseTesting, true
	case statusNeedsReview:
		return legion.PhaseReviewing, true
	case statusRetro:
		return legion.PhaseRetro, true
	case statusImplementing:
		// The workflow writes `in_progress` twice over: once where it admits an issue out of the
		// queue or out of parked, and once for every send-back to implementing, whatever phase
		// the issue comes back from (workflow/table.go's TriggerTesterFailed,
		// TriggerReviewRejected and TriggerChecksRed all write it). Only the send-back, a move
		// out of `testing`, `needs_review` or `retro`, names a phase.
		if slices.Contains(sentBackStatuses, previous) {
			return legion.PhaseImplementing, true
		}
		return "", false
	}
	return "", false
}

// enteredStatus is whether the mark at index of a status history is where the status became
// status: the mark holds it and the one before does not.
func enteredStatus(marks []Mark, index int, status string) bool {
	return marks[index].Value == status && (index == 0 || marks[index-1].Value != status)
}

// firstStatusEntry is when a status history first became status.
func firstStatusEntry(marks []Mark, status string) (time.Time, bool) {
	for index, mark := range marks {
		if enteredStatus(marks, index, status) {
			return mark.At, true
		}
	}
	return time.Time{}, false
}

// statusEntries counts how many times a status history became status.
func statusEntries(marks []Mark, status string) int {
	entries := 0
	for index := range marks {
		if enteredStatus(marks, index, status) {
			entries++
		}
	}
	return entries
}

// statusMoved is whether a status history ever went straight from one of `from` to `to`, which
// is how a move between two phases reads before any observation of them exists.
func statusMoved(marks []Mark, from []string, to string) bool {
	for index := 1; index < len(marks); index++ {
		if marks[index].Value == to && slices.Contains(from, marks[index-1].Value) {
			return true
		}
	}
	return false
}

// parkedSpans are the stretches the issue sat in a parked status (backlog or icebox), from the
// statuses its own events snapshotted.
func (p *Population) parkedSpans(issue *Issue) []Span {
	return markSpans(issue.StatusMarks, p.issueEnd(issue), func(value string) bool {
		return slices.Contains(parkedStatuses, value)
	})
}

// queuedSpans is the issue's queue time, and there is one definition of it: "every observed
// stretch in which the issue's phase is `admitted` and it holds no slot, whenever it falls. Work
// time to merge and every bar of phase time subtract the same queue time. A root in `admitted`
// that holds a slot is in design, not in the queue" (the spec's "Rule details — Queue time, one
// definition"). It is the phase timeline's own `queued` spans, so the work time and the bars
// cannot read it two ways and the seven bars of an issue add up to its work time on either
// timeline.
//
// Before observations it is none: the timeline the event log draws holds no queued phase, and
// the parked subtraction already covers the daemon's own parking.
func (p *Population) queuedSpans(issue *Issue) []Span {
	phases, _ := p.Phases(issue)
	return phaseSpansOf(phases, phaseQueued)
}

// markSpans is the stretches in which a field's recorded value satisfies holds, from the marks the
// sampler wrote for it: each mark runs until the next, and the last until end.
func markSpans(marks []Mark, end time.Time, holds func(value string) bool) []Span {
	var spans []Span
	for index, mark := range marks {
		if !holds(mark.Value) {
			continue
		}
		span := Span{From: mark.At, To: end}
		if index+1 < len(marks) {
			span.To = marks[index+1].At
		}
		if span.Duration() > 0 {
			spans = append(spans, span)
		}
	}
	return normalize(spans)
}

// phaseSpansOf is every span of the timeline whose phase is one of phases.
func phaseSpansOf(timeline []PhaseSpan, phases ...string) []Span {
	var spans []Span
	for _, one := range timeline {
		if slices.Contains(phases, one.Phase) {
			spans = append(spans, one.Span)
		}
	}
	return normalize(spans)
}

// isNormalized is whether spans are already what normalize would return: every span positive and
// each one starting strictly after the one before ends. An empty set is not, so normalize keeps
// answering nil for it.
func isNormalized(spans []Span) bool {
	if len(spans) == 0 {
		return false
	}
	for index, span := range spans {
		if span.Duration() <= 0 {
			return false
		}
		if index > 0 && !span.From.After(spans[index-1].To) {
			return false
		}
	}
	return true
}

// normalize drops the empty spans, orders what is left and merges what overlaps, so the set
// algebra below needs no special cases. A set already in that shape — which is what every
// caller here hands it, since each one is itself the answer of a normalize — is returned as it
// is, rather than sorted and copied again.
func normalize(spans []Span) []Span {
	if isNormalized(spans) {
		return spans
	}
	kept := make([]Span, 0, len(spans))
	for _, span := range spans {
		if span.Duration() > 0 {
			kept = append(kept, span)
		}
	}
	slices.SortFunc(kept, func(a, b Span) int { return a.From.Compare(b.From) })
	merged := kept[:0]
	for _, span := range kept {
		if last := len(merged) - 1; last >= 0 && !span.From.After(merged[last].To) {
			if span.To.After(merged[last].To) {
				merged[last].To = span.To
			}
			continue
		}
		merged = append(merged, span)
	}
	if len(merged) == 0 {
		return nil
	}
	return merged
}

// intersect is the time both sets cover.
func intersect(left, right []Span) []Span {
	left, right = normalize(left), normalize(right)
	var shared []Span
	for _, one := range left {
		for _, other := range right {
			if !other.To.After(one.From) {
				continue
			}
			if !other.From.Before(one.To) {
				break
			}
			span := Span{From: one.From, To: one.To}
			if other.From.After(span.From) {
				span.From = other.From
			}
			if other.To.Before(span.To) {
				span.To = other.To
			}
			if span.Duration() > 0 {
				shared = append(shared, span)
			}
		}
	}
	return normalize(shared)
}

// subtract is the time left carries that right does not.
func subtract(left, right []Span) []Span {
	left, right = normalize(left), normalize(right)
	var rest []Span
	for _, one := range left {
		from := one.From
		for _, other := range right {
			if !other.To.After(from) {
				continue
			}
			if !other.From.Before(one.To) {
				break
			}
			if other.From.After(from) {
				rest = append(rest, Span{From: from, To: other.From})
			}
			if other.To.After(from) {
				from = other.To
			}
		}
		if one.To.After(from) {
			rest = append(rest, Span{From: from, To: one.To})
		}
	}
	return normalize(rest)
}

// totalOf is how long a set of spans covers.
func totalOf(spans []Span) time.Duration {
	var total time.Duration
	for _, span := range normalize(spans) {
		total += span.Duration()
	}
	return total
}
