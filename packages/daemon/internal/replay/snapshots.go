package replay

import (
	"maps"
	"slices"
	"time"

	"github.com/sjawhar/legion/daemon/internal/admit"
	"github.com/sjawhar/legion/daemon/internal/blockers"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/workflow"
)

// snapshots builds a replay's snapshot timeline (design 10.1 (a)): at each instant the replay
// settles, each issue's state as the newest admission pass left it (admit.PassView, handed over
// by the pass's observer), the claims the worker stub holds and the issues the replay holds in
// flight; and each issue's slots as the passes gave them, its moves as the engine wrote them and
// its waits on other issues as the passes judged them. It reads nothing of its own: every part is
// what the daemon's code decided from, or the stub's own record of the claims it wrote.
type snapshots struct {
	timeline Timeline
	ranks    rankFunc
	// view is the newest pass's view, nil before the first; fresh says it came after the last
	// settle. byKey, slotOf, line, heldBack, blocked and held are what a settle reads of it, read
	// once for each view (read).
	view     *admit.PassView
	fresh    bool
	byKey    map[string]record.Issue
	slotOf   map[string]record.Slot
	line     map[string]record.LineEntry
	heldBack map[string]bool
	blocked  map[string]bool
	held     map[string]bool
	// given is each slot the passes gave out since the last settle, waits each issue's wait on
	// other issues that stands, by key, and live each issue whose last state is not the zero
	// state.
	given []record.SlotGiven
	waits map[string]*standingWait
	live  map[string]bool
	// firstStartHeld says a pass found an issue not yet started held by a hold other than the cap
	// (assumption 7): a link, a design gate, an ancestor, a tree.
	firstStartHeld bool
	// keys, working and byIssue are what a settle gathers of the instant, kept from one settle
	// to the next so a settle makes none of them anew: byIssue keeps a holder's slice of claims,
	// emptied, for as long as it holds its slot.
	keys    map[string]struct{}
	working map[string]bool
	byIssue map[string][]supervise.Claim
}

// standingWait is a wait of an issue on other issues that stands: when it began, and the blockers
// and open children it waited on at the last settle.
type standingWait struct {
	start           time.Time
	links, children []string
}

// firstStartHolds is the held words (record.HeldReason) of a hold other than the cap that holds
// the first start of an issue not yet started (assumption 7).
var firstStartHolds = []record.HeldReason{record.HeldTreeClosed, record.HeldTreeParked, record.HeldAncestry,
	record.HeldRootAwaitingPerson, record.HeldDesignGate, record.HeldDispatchBlocker, record.HeldChildrenFirst,
	record.HeldAcceptedAncestor}

// newSnapshots is the snapshot timeline of a replay of [from, to), its ranks read by ranks.
func newSnapshots(from, to time.Time, ranks rankFunc) *snapshots {
	return &snapshots{timeline: Timeline{From: from, To: to, Issues: map[string]*Course{}}, ranks: ranks,
		waits: map[string]*standingWait{}, live: map[string]bool{}, keys: map[string]struct{}{}, working: map[string]bool{},
		byIssue: map[string][]supervise.Claim{}}
}

// pass is the admission pass's observer (admit.Admission.UsePassObserver): the view of a pass
// whose fact committed.
func (s *snapshots) pass(view admit.PassView) {
	s.view, s.fresh = &view, true
	s.given = append(s.given, view.Given...)
}

// moved records a move of key's phase the engine wrote at at, drop being the line of its pull
// request leaving the merge queue the record shows before a move out of merging (SendBack.Drop).
func (s *snapshots) moved(key string, at time.Time, from, to phase.Phase, trigger workflow.TriggerKind, drop string) {
	c := s.timeline.course(key)
	c.Moves = append(c.Moves, moveOf(at, from, to, trigger, drop))
}

// roundBegins records that key's conflicting head was asked to merge its base forward at at, the
// issue keeping its phase: a round of conflict begins, unless one stands already (the engine asks
// again on a later move at a head it asked of).
func (s *snapshots) roundBegins(key string, at time.Time) {
	c := s.timeline.course(key)
	if n := len(c.Rounds); n > 0 && c.Rounds[n-1].End.IsZero() {
		return
	}
	c.Rounds = append(c.Rounds, CourseRound{Start: at, Cause: CauseConflict})
}

// roundEnds records that key's merge forward landed at at, which ends the round that stands.
func (s *snapshots) roundEnds(key string, at time.Time) {
	c := s.timeline.course(key)
	if n := len(c.Rounds); n > 0 && c.Rounds[n-1].End.IsZero() {
		c.Rounds[n-1].End = at
	}
}

// read reads the newest view once: its records and its own slots by key, its line and what holds
// each member back, admission.blocked and admission.held over its judgement (blockers.Blocked,
// record.HeldList), as the state's projection reads them; and whether an issue not yet started,
// neither in flight nor holding a slot, is held by a hold other than the cap.
func (s *snapshots) read(inFlight map[string]bool) {
	v := s.view
	s.byKey = make(map[string]record.Issue, len(v.Issues))
	for _, issue := range v.Issues {
		s.byKey[issue.Key] = issue
	}
	s.slotOf = make(map[string]record.Slot, len(v.Slots))
	own := make([]record.Slot, 0, len(v.Slots))
	for _, slot := range v.Slots {
		if _, ours := s.byKey[slot.Issue]; ours {
			s.slotOf[slot.Issue] = slot
			own = append(own, slot)
		}
	}
	s.line = make(map[string]record.LineEntry, len(v.Line))
	s.heldBack = map[string]bool{}
	for _, entry := range v.Line {
		s.line[entry.Issue.Key] = entry
		if len(blockers.LineWaitsOn(v.Rows, entry)) > 0 {
			s.heldBack[entry.Issue.Key] = true
		}
	}
	s.blocked = map[string]bool{}
	for _, row := range blockers.Blocked(v.Rows, v.Issues, v.Line, record.ChildWaitRows(v.Facts.ChildWaits)) {
		s.blocked[row.Issue] = true
	}
	s.held = map[string]bool{}
	for _, row := range record.HeldList(v.Issues, own, v.Line, v.Dropped, v.Facts) {
		s.held[row.Issue] = true
		if _, holds := s.slotOf[row.Issue]; !holds && !inFlight[row.Issue] && slices.Contains(firstStartHolds, row.Reason) {
			s.firstStartHeld = true
		}
	}
	for key := range s.heldBack {
		if _, holds := s.slotOf[key]; !holds && !inFlight[key] {
			s.firstStartHeld = true
		}
	}
}

// settle records the state the replay stands in at at, once every act of the instant is done: the
// slots the passes gave since the last settle, each issue's state, and each wait on other issues
// that began or ended. claims are the stub's claims, inFlight the issues in flight, and dispatch
// the replay's copy of Dispatch, which says what ended a wait. It reads each issue that is in
// flight, holds a slot, stands in the line, waits on another issue or stood in a counted state at
// the last settle, once.
func (s *snapshots) settle(at time.Time, claims map[claim.Token]supervise.Claim, inFlight map[string]bool, dispatch *shadow) {
	for _, given := range s.given {
		c := s.timeline.course(given.Issue)
		c.Given = append(c.Given, at)
	}
	s.given = s.given[:0]
	if s.view == nil {
		return
	}
	if s.fresh {
		s.read(inFlight)
		s.fresh = false
	}
	v := s.view
	clear(s.keys)
	clear(s.working)
	for key, held := range s.byIssue {
		if _, holds := s.slotOf[key]; holds {
			s.byIssue[key] = held[:0]
		} else {
			delete(s.byIssue, key)
		}
	}
	keys, working, byIssue := s.keys, s.working, s.byIssue
	for _, c := range claims {
		if c.State == supervise.StateWorking {
			working[c.Issue] = true
		}
		if _, holds := s.slotOf[c.Issue]; holds {
			byIssue[c.Issue] = append(byIssue[c.Issue], c)
		}
	}
	for _, set := range []map[string]bool{inFlight, s.live} {
		for key := range set {
			keys[key] = struct{}{}
		}
	}
	for key := range s.slotOf {
		keys[key] = struct{}{}
	}
	for key := range s.line {
		keys[key] = struct{}{}
	}
	for key := range s.waits {
		keys[key] = struct{}{}
	}
	for key := range keys {
		issue, recorded := s.byKey[key]
		tree := key
		if recorded && issue.Tree != "" {
			tree = issue.Tree
		}
		slot, holds := s.slotOf[key]
		state := IssueState{InFlight: inFlight[key], Holds: holds, InTurn: working[key]}
		if holds {
			state.Holder = s.holderState(issue, slot, byIssue[key])
		}
		if recorded && (state.InFlight || holds) {
			state.PastReview = pastReviewPhase(record.StandsAt(issue))
		}
		if entry, member := s.line[key]; member {
			state.Part, state.Blocked = entry.Part, s.blocked[key]
			state.Waiting = !holds && !s.heldBack[key] && !state.Blocked && !s.held[key]
		}
		if !state.InFlight && !holds && state.Part == 0 {
			state = IssueState{}
		}
		if holds || state.Waiting {
			state.Tree, state.Rank, state.RootRank = tree, s.ranks(key, at), s.ranks(tree, at)
		}
		if state != (IssueState{}) || s.timeline.Issues[key] != nil {
			s.timeline.course(key).set(at, state)
		}
		if state == (IssueState{}) {
			delete(s.live, key)
		} else {
			s.live[key] = true
		}
		s.waitAt(at, key, issue, recorded && record.InWorkflow(issue), inFlight[key], v, dispatch)
	}
}

// holderState is the state of the slots reading issue, holding slot, stands in
// (record.HolderStateOf), gathered from the view and claims, the stub's claims of its issue, as
// the slots reading gathers a holder's facts (blockers.SlotsReading): its claims, the start it is
// owed, its hold, its wait on a person and the waits it keeps its slot through. The stub's claims
// are none that nothing will wake, since a replay's worker is never stranded.
func (s *snapshots) holderState(issue record.Issue, slot record.Slot, claims []supervise.Claim) record.HolderState {
	v := s.view
	facts := record.HolderFacts{Issue: issue, AdmittedAt: slot.AdmittedAt, WaitSince: slot.WaitsSince}
	record.HolderClaimFacts(&facts, record.HolderClaimsOf(claims), nil)
	if owed, owes := v.Facts.Owed[issue.Key]; owes {
		facts.Starting = append(facts.Starting, owed.CreatedAt)
	}
	if issue.Hold != nil {
		facts.HeldSince = issue.Hold.Since
	}
	facts.Person = issue.AwaitingPerson() && !issue.Answered() && claim.IsTreeRoot(issue.Key, issue.Tree)
	facts.WaitsOnIssue = len(blockers.KeptWaits(v.Rows, issue, v.Facts)) > 0
	state, _ := record.HolderStateOf(facts)
	return state
}

// waitAt begins, carries or ends key's wait on other issues at at: an issue in flight that stands
// before its review waits while a blocker its links name is not done (blockers.Unfinished over
// the pass's mirror rows) or a Dispatch child its first phase waits for is open (the pass's
// children-first judgement); the wait ends at the first settle where nothing is left.
func (s *snapshots) waitAt(at time.Time, key string, issue record.Issue, inWorkflow, inFlight bool, v *admit.PassView, dispatch *shadow) {
	var links, children []string
	if inWorkflow && inFlight && beforeReview(record.StandsAt(issue)) {
		links = blockers.Unfinished(v.Rows, key)
		for _, child := range v.Facts.ChildWaits[key].Open {
			children = append(children, child.Key)
		}
	}
	wait := s.waits[key]
	switch {
	case len(links)+len(children) > 0 && wait == nil:
		s.waits[key] = &standingWait{start: at, links: links, children: children}
	case len(links)+len(children) > 0:
		wait.links, wait.children = links, children
	case wait != nil:
		end := WaitLeft
		if inWorkflow && inFlight {
			end = waitEnd(key, issue, wait, v, dispatch)
		}
		c := s.timeline.course(key)
		c.Waits = append(c.Waits, CourseWait{Start: wait.start, End: at, EndedBy: end})
		delete(s.waits, key)
	}
}

// waitEnd is what ended key's wait, which stood on wait's blockers and children at the settle
// before: an issue that moved past its review moved on; a wait on blockers ended with a link taken
// off where one of them is no longer linked, and with a blocker done where each is linked still;
// a wait on children ended by the first of them, as the replay's copy of Dispatch shows it now: a
// child closed done or icebox is done, one under another parent moved out, and a child still open
// under the issue holds it no longer because its tree's gate closed.
func waitEnd(key string, issue record.Issue, wait *standingWait, v *admit.PassView, dispatch *shadow) WaitEnd {
	if !beforeReview(record.StandsAt(issue)) {
		return WaitMovedOn
	}
	if len(wait.links) > 0 {
		for _, blocker := range wait.links {
			if !slices.Contains(v.Rows[key].Blockers, blocker) {
				return WaitLinkRemoved
			}
		}
		return WaitBlockerDone
	}
	for _, child := range wait.children {
		shown, ok := dispatch.issues[child]
		switch {
		case !ok:
			return WaitChildDone
		case shown.status() == record.DoneStatus || shown.status() == "icebox":
			return WaitChildDone
		case shown.parent() != key:
			return WaitChildMovedOut
		case !v.Facts.Gates[issue.Tree].Open():
			return WaitGateClosed
		}
	}
	return WaitChildDone
}

// timelineAt is the snapshot timeline with every wait that stands at its end left open.
func (s *snapshots) timelineAt() Timeline {
	for _, key := range slices.Sorted(maps.Keys(s.waits)) {
		c := s.timeline.course(key)
		c.Waits = append(c.Waits, CourseWait{Start: s.waits[key].start, EndedBy: WaitOpen})
	}
	s.waits = map[string]*standingWait{}
	return s.timeline
}

// beginWaitsAt begins each issue of tl that waits for a slot in its first state, taken at tl's
// start, at the moment starts gives it, where that is earlier: the moment the record shows it
// began that wait (recordedWaitStarts). The start state is the record's (assumption 6), so when
// its waits began is part of it, and the measures that run back to a wait's beginning
// (Scorecard.MedianSlotWaitMinutes) read it as the record's own scorecard and the line timeline
// do. The state before tl's start is the first state carried back; no measure counts time before
// the start. The waits on other issues that stand at the start need nothing: the measures count
// their hours from the window's start alone (measureSpan).
func (tl Timeline) beginWaitsAt(starts map[string]time.Time) {
	for key, since := range starts {
		c := tl.Issues[key]
		if c == nil || len(c.States) == 0 || !c.States[0].At.Equal(tl.From) || !waitsForSlot(c.States[0].IssueState) ||
			!since.Before(tl.From) {
			continue
		}
		c.States = slices.Insert(c.States, 0, Stint{At: since, IssueState: c.States[0].IssueState})
	}
}

// startWaiters is the issues of tl that wait for a slot in their first state, taken at tl's start.
func (tl Timeline) startWaiters() []string {
	var keys []string
	for key, c := range tl.Issues {
		if len(c.States) > 0 && c.States[0].At.Equal(tl.From) && waitsForSlot(c.States[0].IssueState) {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	return keys
}

// waitsOnIssues says whether the timeline holds a wait of an issue on another issue
// (assumption 4).
func (s *snapshots) waitsOnIssues() bool {
	if len(s.waits) > 0 {
		return true
	}
	for _, c := range s.timeline.Issues {
		if len(c.Waits) > 0 {
			return true
		}
	}
	return false
}
