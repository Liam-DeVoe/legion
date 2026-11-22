package replay

import (
	"slices"
	"sort"
	"time"

	"github.com/sjawhar/legion/daemon/internal/dispatch"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/workflow"
)

// Timeline is a window's course as the measures read it (Measure): each issue's states over the
// window, each holding from its moment to the next one's, with the slots it was given, its phase
// moves and its waits on other issues. A record's timeline is read from its trace (the line
// timeline, lineTimeline), and a replay builds two (design 10.1): one from the views its admission
// passes leave and the claims its worker stub holds (the snapshot timeline, snapshots), and one
// from its own log, read through the window's line kinds as the record is. So the record and a
// replay are measured by one code, and the gap between a replay's two timelines is the error of
// reading a course from its lines.
type Timeline struct {
	// From and To are the window's span.
	From, To time.Time
	// Issues is each issue's course, by key.
	Issues map[string]*Course
}

// Course is one issue's course over a timeline.
type Course struct {
	// States is the issue's states in time order, each holding from its At to the next one's At,
	// or to the timeline's end. Before the first the issue stands in no state a measure counts.
	States []Stint
	// Given is each moment a pass gave the issue a slot, in order.
	Given []time.Time
	// Moves is each move of the issue's phase, in order.
	Moves []CourseMove
	// Rounds is each round of a send-back loop that moved no phase, in order: a conflicting head
	// merged forward while the issue kept its phase (record.ConflictMergeForward).
	Rounds []CourseRound
	// Waits is each wait of the issue on another issue, by the moment it began.
	Waits []CourseWait
}

// CourseRound is one round of a send-back loop that moved no phase: from the merge forward's ask
// to its landing (End zero while it stands), and its cause, a head that conflicts with its base
// (the windows' README, "The measures": under main a drawn conflict that keeps its phase is one
// round of conflict, its hours the merge-forward segment).
type CourseRound struct {
	Start, End time.Time
	Cause      Cause
}

// Stint is one state of an issue and the moment it began.
type Stint struct {
	At time.Time
	IssueState
}

// IssueState is what the measures read of one issue at a moment: whether it is in flight, whether
// it holds a slot and in which of the slots reading's states (record.HolderStateOf), whether a
// worker of it is in a turn, whether it stands past its review, its place in the waiting line, and,
// for a holder or an issue that waits for a slot, its tree and the Dispatch ranks that place it.
// An issue that is not in flight, holds no slot and stands in no part of the line is in the zero
// state, which no measure counts.
type IssueState struct {
	// Tree is the key of the issue's tree root; Rank and RootRank are the Dispatch ranks of the
	// issue and of that root, which record.RankLess compares to place it (the true ranks, which a
	// replay's own copy of Dispatch does not show under FirstStarts recorded, design 10.7). They
	// are set for a holder and an issue that waits for a slot alone, the two a pair of priority
	// taken is made of.
	Tree, Rank, RootRank string
	// InFlight says the daemon has started the issue's work and it has not reached done or left
	// the workflow (the windows' README, "The measures").
	InFlight bool
	// Holds says it holds a slot of the cap, and Holder is the state of the slots reading it is in
	// while it does ("" while it does not).
	Holds  bool
	Holder record.HolderState
	// InTurn says a worker of the issue, of any role, is in a turn.
	InTurn bool
	// PastReview says it stands in accepting, retro or merging, or is held from one of them.
	PastReview bool
	// Part is the part of the waiting line it stands in, zero for none; Blocked says
	// admission.blocked names it; Waiting says it is an issue that waits for a slot: a member of
	// the line that holds no slot, that no wait holds back, and that neither admission.blocked
	// nor admission.held names.
	Part    record.LinePart
	Blocked bool
	Waiting bool
}

// CourseMove is one move of an issue's phase: its moment, its phases, its trigger and, for a move
// back, its cause (CauseOf).
type CourseMove struct {
	At       time.Time
	From, To phase.Phase
	Trigger  workflow.TriggerKind
	Cause    Cause
}

// CourseWait is one wait of an issue on other issues: from the moment it began to the moment
// nothing was left to wait on (zero while it stands), and what ended it (the windows' README,
// "The measures": blocker_done, link_removed, child_done, child_moved_out, gate_closed,
// left_workflow, or open).
type CourseWait struct {
	Start, End time.Time
	EndedBy    WaitEnd
}

// course is key's course, made where none is yet.
func (t *Timeline) course(key string) *Course {
	if t.Issues == nil {
		t.Issues = map[string]*Course{}
	}
	c, ok := t.Issues[key]
	if !ok {
		c = &Course{}
		t.Issues[key] = c
	}
	return c
}

// set records that the issue stands in state from at: a stint where the state differs from the one
// it stands in, which at the moment of the last stint takes that stint's place.
func (c *Course) set(at time.Time, state IssueState) {
	n := len(c.States)
	switch {
	case n == 0 && state == (IssueState{}):
	case n > 0 && c.States[n-1].IssueState == state:
	case n > 0 && c.States[n-1].At.Equal(at):
		c.States[n-1].IssueState = state
		if n > 1 && c.States[n-2].IssueState == state {
			c.States = c.States[:n-1]
		}
	default:
		c.States = append(c.States, Stint{At: at, IssueState: state})
	}
}

// moveOf is the course's reading of a move: its cause where it moves back (CauseOf), none for a
// move back CauseOf names no cause of. drop is the msg of the line of the issue's pull request
// leaving the merge queue before a move out of merging, "" for none (SendBack.Drop).
func moveOf(at time.Time, from, to phase.Phase, trigger workflow.TriggerKind, drop string) CourseMove {
	move := CourseMove{At: at, From: from, To: to, Trigger: trigger}
	if phase.MovesBack(from, to) {
		if cause, named := CauseOf(SendBack{Trigger: trigger, From: from, To: to, Drop: drop}); named {
			move.Cause = cause
		}
	}
	return move
}

// pastReviewPhase says whether p is a phase past the review: accepting, retro or merging.
func pastReviewPhase(p phase.Phase) bool {
	return p == phase.Accepting || p == phase.Retro || p == phase.Merging
}

// rankFunc is the Dispatch rank of an issue at a moment.
type rankFunc func(key string, at time.Time) string

// ranksOf is the rank of each issue of t at each moment as its Dispatch events show it: the rank
// of its newest issue event at or before the moment, or of its first for a moment before every
// one, and "" for an issue the window holds no issue event of. A replay reads the ranks of its
// record's trace, the true ones, whatever its own copy of Dispatch shows (design 10.7). Each
// issue's events are indexed once, and a rank is read by binary search.
func ranksOf(t Trace) rankFunc {
	shown := make(map[string]shownStates, len(t.Issues))
	for key, iss := range t.Issues {
		shown[key] = shownOf(iss)
	}
	return func(key string, at time.Time) string {
		s := shown[key]
		if len(s.states) == 0 {
			return ""
		}
		if state := s.stateAt(at); state != nil {
			return state.Rank
		}
		return s.states[0].Rank
	}
}

// shownStates is an issue's Dispatch state as each of its issue events shows it, in time order,
// read at a moment by binary search.
type shownStates struct {
	at     []time.Time
	states []*DispatchIssueState
}

// shownOf is the states iss's issue events show, in the order its log holds them, which is time
// order (BuildTrace).
func shownOf(iss *IssueTrace) shownStates {
	var s shownStates
	for _, event := range iss.Dispatch {
		if event.Issue != nil {
			s.at = append(s.at, event.At)
			s.states = append(s.states, event.Issue)
		}
	}
	return s
}

// stateAt is the state the newest issue event at or before at shows, nil before the first
// (dispatchStateBefore, read by binary search).
func (s shownStates) stateAt(at time.Time) *DispatchIssueState {
	i := sort.Search(len(s.at), func(i int) bool { return s.at[i].After(at) })
	if i == 0 {
		return nil
	}
	return s.states[i-1]
}

// stretchCount is how many of a set of stretches stand from each of its moments on, read at a
// moment by binary search: a stretch stands from its start up to, not at, its end, and on where
// its end is zero.
type stretchCount struct {
	from   []time.Time
	counts []int
}

// countStretches is the count of stretches.
func countStretches(stretches [][2]time.Time) stretchCount {
	type edge struct {
		at    time.Time
		delta int
	}
	edges := make([]edge, 0, 2*len(stretches))
	for _, stretch := range stretches {
		edges = append(edges, edge{at: stretch[0], delta: 1})
		if !stretch[1].IsZero() {
			edges = append(edges, edge{at: stretch[1], delta: -1})
		}
	}
	slices.SortStableFunc(edges, func(a, b edge) int { return a.at.Compare(b.at) })
	var count stretchCount
	standing := 0
	for i, e := range edges {
		standing += e.delta
		if i+1 < len(edges) && edges[i+1].at.Equal(e.at) {
			continue
		}
		count.from, count.counts = append(count.from, e.at), append(count.counts, standing)
	}
	return count
}

// at is how many stretches stand at at.
func (c stretchCount) at(at time.Time) int {
	i := sort.Search(len(c.from), func(i int) bool { return c.from[i].After(at) })
	if i == 0 {
		return 0
	}
	return c.counts[i-1]
}

// gateSteps is a design gate's openness after each fact of it, read at a moment by binary search.
type gateSteps struct {
	at   []time.Time
	open []bool
}

// gateStepsOf is root's design gate as its gate facts leave it, in time order (BuildTrace reads
// the lines and the verdicts in one time order): a registration open or closed as it was
// registered, an opening, a person's approval, a closing or a request for changes. Only the
// facts that change the gate are kept.
func gateStepsOf(root *IssueTrace) gateSteps {
	var steps gateSteps
	open := false
	for _, fact := range root.Gates {
		switch fact.Kind {
		case GateRegisteredFact:
			open = fact.Open
		case GateOpenedFact, GateApproved:
			open = true
		case GateClosedFact, GateChangesRequested:
			open = false
		}
		if n := len(steps.open); n > 0 && steps.open[n-1] == open {
			continue
		}
		steps.at, steps.open = append(steps.at, fact.At), append(steps.open, open)
	}
	return steps
}

// openAt says whether the gate stands open at at; closed before its first fact.
func (g gateSteps) openAt(at time.Time) bool {
	i := sort.Search(len(g.at), func(i int) bool { return g.at[i].After(at) })
	return i > 0 && g.open[i-1]
}

// issueIndex is what the line timeline reads of one issue of a trace, each part built once from
// the issue's trace and read at any moment by binary search, so reading an issue at each moment
// its state can change costs a logarithm per read rather than a walk of its record.
type issueIndex struct {
	iss *IssueTrace
	// shown is its Dispatch states, and done each moment its status turned done or turned from
	// done.
	shown shownStates
	done  []time.Time
	// rankChanges is each moment an issue event changed its rank.
	rankChanges []time.Time
	// flight is its stretches in flight (IssueTrace.inFlight).
	flight [][2]time.Time
	// inFlight, slots, turns, waits, person and held count its stretches in flight, its slots,
	// its workers' turns, its waits on other issues (link and children), its waits on a person and
	// its holds.
	inFlight, slots, turns, waits, person, held stretchCount
	// gate is the design gate of the tree it roots.
	gate gateSteps
}

// indexIssue indexes iss.
func indexIssue(iss *IssueTrace) *issueIndex {
	x := &issueIndex{iss: iss, shown: shownOf(iss), flight: iss.inFlight(), gate: gateStepsOf(iss)}
	done, rank := false, ""
	for i, state := range x.shown.states {
		if isDone := state.Status == record.DoneStatus; i == 0 || isDone != done {
			done = isDone
			x.done = append(x.done, x.shown.at[i])
		}
		if i == 0 || state.Rank != rank {
			rank = state.Rank
			x.rankChanges = append(x.rankChanges, x.shown.at[i])
		}
	}
	x.inFlight = countStretches(x.flight)
	var slots, turns, waits, person, held [][2]time.Time
	for _, span := range iss.Slots {
		slots = append(slots, [2]time.Time{span.Start, span.End})
	}
	for _, turn := range iss.Turns {
		turns = append(turns, [2]time.Time{turn.Start, turn.End})
	}
	for _, wait := range iss.Waits {
		stretch := [2]time.Time{wait.Start, wait.End}
		switch wait.Kind {
		case WaitLink, WaitChildren:
			waits = append(waits, stretch)
		case WaitPerson:
			person = append(person, stretch)
		case WaitHeld:
			held = append(held, stretch)
		}
	}
	x.slots, x.turns, x.waits, x.person, x.held = countStretches(slots), countStretches(turns), countStretches(waits),
		countStretches(person), countStretches(held)
	return x
}

// entryOf is iss's latest entry at or before at, nil before its first (IssueTrace.entryAt, read by
// binary search over its entries in time order).
func entryOf(iss *IssueTrace, at time.Time) *Entry {
	i := sort.Search(len(iss.Entries), func(i int) bool { return iss.Entries[i].At.After(at) })
	if i == 0 {
		return nil
	}
	return &iss.Entries[i-1]
}

// visitOf is the visit iss stands in at at, nil for none (IssueTrace.visitAt, read by binary
// search over its visits, which follow one another in time order).
func visitOf(iss *IssueTrace, at time.Time) *Visit {
	i := sort.Search(len(iss.Visits), func(i int) bool { return iss.Visits[i].Enter.After(at) })
	if i == 0 {
		return nil
	}
	if visit := &iss.Visits[i-1]; visit.Exit.IsZero() || visit.Exit.After(at) {
		return visit
	}
	return nil
}

// lineReader reads the issues of a trace at any moment for the line timeline: the trace, the ranks
// and the rule it reads by, and each issue's index, built on its first read.
type lineReader struct {
	t     Trace
	ranks rankFunc
	rule  func(time.Time) record.SlotRule
	index map[string]*issueIndex
}

// of is key's index, nil for an issue the trace does not hold.
func (r *lineReader) of(key string) *issueIndex {
	if x, ok := r.index[key]; ok {
		return x
	}
	iss, ok := r.t.Issues[key]
	if !ok {
		return nil
	}
	x := indexIssue(iss)
	r.index[key] = x
	return x
}

// lineTimeline is the timeline read from t (the line timeline of design 10.1 (b)): the record's,
// or a replay's read from its own log through the window's line kinds. Each issue of t's project
// is read at each moment its state can change — a line or an event of its own, of its tree root
// or of an issue its links name, a rule's part beginning — with its ranks from ranks and the rule
// the daemon ran at that moment from rule. An issue is in flight from the first slot it took after
// an entry to its run's end; it stands in the line's returned part while in flight it holds no
// slot in a phase the rule keeps one in, unheld (released for a wait, or owing a start), blocked
// while a link or children wait of it stands; and in the starting part while, not yet started, it
// is a queued root handed to Legion in todo, or a todo child of a tree that stands in the workflow
// under an open design gate, its root waiting on no person, blocked while a blocker its links name
// is not done.
func lineTimeline(t Trace, ranks rankFunc, rule func(time.Time) record.SlotRule) Timeline {
	tl := Timeline{From: t.From, To: t.To, Issues: map[string]*Course{}}
	r := &lineReader{t: t, ranks: ranks, rule: rule, index: map[string]*issueIndex{}}
	for _, move := range t.Slots {
		if move.Given {
			if iss, ok := t.Issues[move.Issue]; ok && iss.Project == t.Project {
				c := tl.course(move.Issue)
				c.Given = append(c.Given, move.At)
			}
		}
	}
	for _, key := range sortedKeys(t.Issues) {
		iss := t.Issues[key]
		if iss.Project != t.Project {
			continue
		}
		x := r.of(key)
		c := tl.Issues[key]
		for _, at := range r.moments(x) {
			state := r.stateAt(x, at)
			if c == nil && state == (IssueState{}) {
				continue
			}
			if c == nil {
				c = tl.course(key)
			}
			c.set(at, state)
		}
		moves, waits := iss.Moves(), issueWaits(iss)
		if c == nil && len(moves)+len(waits)+len(iss.MergeForwards) > 0 {
			c = tl.course(key)
		}
		for _, move := range moves {
			c.Moves = append(c.Moves, moveOf(move.At, move.From, move.To, move.Trigger, move.Drop))
		}
		for _, round := range iss.MergeForwards {
			c.Rounds = append(c.Rounds, CourseRound{Start: round.Start, End: round.End, Cause: CauseConflict})
		}
		if len(waits) > 0 {
			c.Waits = waits
		}
	}
	return tl
}

// issueWaits is iss's waits on other issues as the measures count them: its link and children
// waits, in the order they began, each run of them that overlap read as one wait, from the first
// one's start to the last one's end, ended by what ended the last.
func issueWaits(iss *IssueTrace) []CourseWait {
	var waits []CourseWait
	for _, wait := range iss.Waits {
		if wait.Kind != WaitLink && wait.Kind != WaitChildren {
			continue
		}
		if n := len(waits); n > 0 && (waits[n-1].End.IsZero() || wait.Start.Before(waits[n-1].End)) {
			last := &waits[n-1]
			if !last.End.IsZero() && (wait.End.IsZero() || wait.End.After(last.End)) {
				last.End, last.EndedBy = wait.End, wait.EndedBy
			}
			continue
		}
		waits = append(waits, CourseWait{Start: wait.Start, End: wait.End, EndedBy: wait.EndedBy})
	}
	return waits
}

// moments is every moment from the trace's start to its end at which x's issue's state can
// change, in order, and the window's start: each of its own entries, visits, slots, turns, waits,
// stretches in flight and issue events, each change of a rule's part, each change of the done-ness
// of an issue its links name, and, for each tree it entered as a child, each change of the root's
// rank, design gate, visits and waits on a person.
func (r *lineReader) moments(x *issueIndex) []time.Time {
	t, iss := r.t, x.iss
	times := []time.Time{t.From}
	add := func(at ...time.Time) {
		for _, moment := range at {
			if !moment.IsZero() && !moment.Before(t.Start) && moment.Before(t.To) {
				times = append(times, moment)
			}
		}
	}
	var roots []string
	for _, entry := range iss.Entries {
		add(entry.At)
		if entry.Tree == "" || entry.Tree == iss.Key || slices.Contains(roots, entry.Tree) {
			continue
		}
		roots = append(roots, entry.Tree)
		if root := r.of(entry.Tree); root != nil {
			add(root.rankChanges...)
			add(root.gate.at...)
			add(root.person.from...)
			for _, visit := range root.iss.Visits {
				add(visit.Enter, visit.Exit)
			}
		}
	}
	for _, visit := range iss.Visits {
		add(visit.Enter, visit.Exit)
	}
	add(x.slots.from...)
	add(x.turns.from...)
	add(x.waits.from...)
	add(x.person.from...)
	add(x.held.from...)
	add(x.inFlight.from...)
	add(x.shown.at...)
	for _, part := range t.Parts {
		add(part.From, part.To)
	}
	var blockers []string
	for _, state := range x.shown.states {
		for _, key := range state.BlockedBy {
			if !slices.Contains(blockers, key) {
				blockers = append(blockers, key)
			}
		}
	}
	for _, key := range blockers {
		if blocker := r.of(key); blocker != nil {
			add(blocker.done...)
		}
	}
	slices.SortFunc(times, func(a, b time.Time) int { return a.Compare(b) })
	return slices.CompactFunc(times, func(a, b time.Time) bool { return a.Equal(b) })
}

// stateAt is the state x's issue stands in from at on.
func (r *lineReader) stateAt(x *issueIndex, at time.Time) IssueState {
	iss := x.iss
	entry := entryOf(iss, at)
	tree := iss.Key
	if entry != nil && entry.Tree != "" {
		tree = entry.Tree
	}
	state := IssueState{InFlight: x.inFlight.at(at) > 0, Holds: x.slots.at(at) > 0, InTurn: x.turns.at(at) > 0}
	visit := visitOf(iss, at)
	waiting, held := x.waits.at(at) > 0, x.held.at(at) > 0
	if visit != nil && (state.InFlight || state.Holds) {
		state.PastReview = pastReviewPhase(visit.Phase)
	}
	if state.Holds {
		state.Holder = r.holderState(x, at, tree, visit, waiting, held)
	}
	switch {
	case state.InFlight && !state.Holds && !held && visit != nil && r.rule(at).PhaseKeepsSlot(visit.Phase):
		state.Part, state.Blocked, state.Waiting = record.PartReturned, waiting, !waiting
	case !state.InFlight && !state.Holds && entry != nil:
		if member, blocked := r.startingAt(x, at, tree); member {
			state.Part, state.Blocked, state.Waiting = record.PartStarting, blocked, !blocked
		}
	}
	if !state.InFlight && !state.Holds && state.Part == 0 {
		return IssueState{}
	}
	if state.Holds || state.Waiting {
		state.Tree, state.Rank, state.RootRank = tree, r.ranks(iss.Key, at), r.ranks(tree, at)
	}
	return state
}

// startingAt says whether x's issue, not yet started in its entry, stands in the line's starting
// part at at, and whether it is blocked there: a queued root handed to Legion in todo, or a todo
// child of a tree whose root stands in the workflow, waits on no person and holds its design gate
// open; blocked while a blocker its links name is not done.
func (r *lineReader) startingAt(x *issueIndex, at time.Time, tree string) (bool, bool) {
	state := x.shown.stateAt(at)
	if state == nil || state.Status != "todo" {
		return false, false
	}
	blocked := r.unfinished(state.BlockedBy, at)
	if tree == x.iss.Key {
		return dispatch.CarriesLegionLabel(state.Labels), blocked
	}
	root := r.of(tree)
	if root == nil || visitOf(root.iss, at) == nil || root.person.at(at) > 0 || !root.gate.openAt(at) {
		return false, false
	}
	return true, blocked
}

// unfinished says whether a blocker of blockedBy is not done at at: its Dispatch events show it in
// another status, or the window holds none of it.
func (r *lineReader) unfinished(blockedBy []string, at time.Time) bool {
	for _, key := range blockedBy {
		blocker := r.of(key)
		if blocker == nil {
			return true
		}
		if state := blocker.shown.stateAt(at); state == nil || state.Status != record.DoneStatus {
			return true
		}
	}
	return false
}

// holderState is the state of the slots reading x's issue, holding a slot, stands in at at
// (record.HolderStateOf): its turns, its hold, its root's wait on a person and its wait on another
// issue, as its trace holds them. Nothing on its way to a worker shows in a trace, so a holder is
// working only in a turn.
func (r *lineReader) holderState(x *issueIndex, at time.Time, tree string, visit *Visit, waiting, held bool) record.HolderState {
	facts := record.HolderFacts{Issue: record.Issue{Key: x.iss.Key, Tree: tree}, WaitsOnIssue: waiting}
	if visit != nil {
		facts.Issue.Phase = visit.Phase
	}
	if held {
		facts.Issue.Phase = phase.Held
	}
	if x.turns.at(at) > 0 {
		facts.InTurn = []time.Time{at}
	}
	facts.Person = tree == x.iss.Key && x.person.at(at) > 0
	state, _ := record.HolderStateOf(facts)
	return state
}
