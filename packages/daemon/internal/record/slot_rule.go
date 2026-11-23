package record

import (
	"slices"
	"time"

	"github.com/sjawhar/legion/daemon/internal/phase"
)

// SlotRule is one rule of which issues hold one of the cap's admission slots and how the waiting
// line gives the slots out: the phases that keep a slot, which waits on another issue give it
// back and after how long, whether such a wait stops the phase under way, the order of the line,
// what a start may take from the cap, what a head that conflicts with its base does, and where
// inside merging the slot goes back. It is a value with a name, so that the daemon runs its own
// rule (MainRule) and a replay of a recorded window runs the daemon's own admission pass under
// another rule the repository keeps (SlotRules), and a rule is read by name rather than copied
// into a script.
//
// Every reader of the rule takes it from the pass, the engine or the projection it was given to
// (workflow.Config.SlotRule, which the admission pass reads through its engine, LineFacts.Rule):
// no reader reads a rule of its own choosing, and no rule is held in a package variable a reader
// could change, so two replays under two rules can run at once in one process.
type SlotRule struct {
	// Name is the rule's name, the word a caller picks it by (SlotRuleNamed). The zero value's
	// empty name is the daemon's own rule (OrMain).
	Name string
	// Commit is the commit of cybermonklabs/legion the rule's choices come from, so a reader of a
	// replay's result can read the code that made them.
	Commit string
	// KeepsSlotIn is every phase in which an issue the daemon has started keeps its slot
	// (SlotRule.PhaseKeepsSlot). A phase it leaves out holds none, and the start of that phase's
	// worker waits for no slot (workflow.StartOrOwe).
	KeepsSlotIn []phase.Phase
	// LinkWaitGivesBack says whether a started issue that waits on an unfinished Dispatch
	// blocked_by link gives its slot back for the wait (blockers.waitsOf).
	LinkWaitGivesBack bool
	// ChildrenWaitGivesBack says whether a started parent that waits on its open Dispatch
	// children gives its slot back for the wait (blockers.waitsOf).
	ChildrenWaitGivesBack bool
	// WaitGrace is how long a wait on another issue that gives the slot back (LinkWaitGivesBack,
	// ChildrenWaitGivesBack) lasts before it does: a slot holder whose wait is younger than the
	// grace keeps its slot through it, as a holder keeps it through a wait the rule keeps the slot
	// for (blockers.KeptWaits), and the first admission pass at or after the wait's start plus the
	// grace gives the slot back as a rule with no grace gives it back at once (LineFacts.InWaitGrace,
	// blockers.waitsOf). The wait's start is the pass that first found the holder waiting while it
	// kept its slot, which marks it on the slot (Slot.WaitsSince). No pass is asked for at the
	// grace's end, since the daemon has no wake an admission pass can ask for at a moment of its
	// choosing: the slot goes back at the next pass after the grace, which every fact of the
	// project and every controller tick makes, and that delay is the rule's own (GraceOver). Zero,
	// the daemon's own and every rule's but wait-grace's and wait-grace-1h's, gives the slot back
	// at the pass that finds the wait.
	WaitGrace time.Duration
	// WaitStopsPhase says whether a wait on another issue stops the phase under way: under a rule
	// where it does, a started parent waits on its open children in whatever phase it stands
	// (ChildWaitsOf), and the worker of the phase is stopped for the wait. Under a rule where it
	// does not, the phase goes on while the issue waits and only its next start waits.
	WaitStopsPhase bool
	// Order is the order of the waiting line (Waiting).
	Order LineOrder
	// Take is what a start may take from the cap (admit's capLets).
	Take StartTake
	// Conflict is what a head that conflicts with its base does once a second read confirms the
	// conflict (workflow's mergeability, TableFor).
	Conflict ConflictPath
	// ReleaseAtReady says whether an issue in merging gives its slot back at the merger's ready
	// mark: the merger's completion of merging, which records the head it stands on
	// (PhaseRow.HandoffCommit) while the issue waits in merging for the merge itself. It is read
	// only under a rule that keeps a slot in merging (KeepsSlotIn).
	ReleaseAtReady bool
}

// LineOrder is the order of the waiting line, a SlotRule's choice.
type LineOrder string

const (
	// LineReturnedOldestFirst is the daemon's own order: first the issues whose work came back,
	// oldest first, then the issues that have not started, by their tree root's Dispatch rank
	// (Waiting, WaitingLineBand).
	LineReturnedOldestFirst LineOrder = "returned_oldest_first"
	// LineReturnedByRank places an issue whose work came back by its board rank (RankLess over
	// its tree root's record and then its own) rather than by the moment its work came back.
	LineReturnedByRank LineOrder = "returned_by_rank"
	// LineBlockersFirst is the daemon's own order with the issues that a started issue waits on
	// placed first among the issues that have not started.
	LineBlockersFirst LineOrder = "blockers_first"
	// LineReturnedByFirstSlot places an issue whose work came back by the moment of its first slot
	// in its current generation (LineFacts.FirstSlots), the earliest first, rather than by the
	// moment its work came back: an issue the daemon started early that gave its slot back stands
	// ahead, when its work comes back, of the returned issues it started before.
	LineReturnedByFirstSlot LineOrder = "returned_by_first_slot"
)

// StartTake is what a start may take from the cap, a SlotRule's choice.
type StartTake string

const (
	// TakeOwnSlot is the daemon's own: a start that needs a slot takes one of its own, and only
	// while the cap has one free.
	TakeOwnSlot StartTake = "own_slot"
	// TakeCover lets an issue that a counted waiter waits on start on that waiter's slot whatever
	// the cap says, one such cover for each holder: the waiter gives its slot to the issue, which
	// holds it for the waiter until the issue is done or the waiter no longer waits on it, and the
	// waiter takes a slot of its own back at once then (LineFacts.Covers, admit's covers).
	TakeCover StartTake = "cover"
)

// ConflictPath is what a head that conflicts with its base does, a SlotRule's choice.
type ConflictPath string

const (
	// ConflictMergeForward is the daemon's own: the issue keeps its phase and the implementer is
	// asked to merge the base forward (workflow's mergeForward).
	ConflictMergeForward ConflictPath = "merge_forward"
	// ConflictMovesBack moves the issue back to implementing by workflow.TriggerConflict.
	ConflictMovesBack ConflictPath = "moves_back_conflict"
	// ConflictChecksRed moves the issue back to implementing by workflow.TriggerChecksRed.
	ConflictChecksRed ConflictPath = "moves_back_checks_red"
)

// MainRule is the daemon's own rule, the one it is built with: a slot is kept from the start
// through the review and in held, and none past the review; a link wait and a children wait give
// the slot back at the pass that finds them, with no grace, and stop nothing; work that came back
// goes first, oldest first, then the line by rank; a start takes a slot of its own; a conflicting
// head keeps its phase while the base is merged forward. Its commit is the commit the rule was
// first written as a value at.
var MainRule = SlotRule{
	Name:   "main",
	Commit: "0d3786da536722f2eb26a793ab1b9827b6e90f47",
	KeepsSlotIn: []phase.Phase{phase.Admitted, phase.AwaitingPerson, phase.Planning, phase.Implementing, phase.Testing,
		phase.Reviewing, phase.Held},
	LinkWaitGivesBack:     true,
	ChildrenWaitGivesBack: true,
	WaitGrace:             0,
	WaitStopsPhase:        false,
	Order:                 LineReturnedOldestFirst,
	Take:                  TakeOwnSlot,
	Conflict:              ConflictMergeForward,
	ReleaseAtReady:        false,
}

// OrMain is r, or MainRule where r is the zero value a caller that names no rule holds: a
// workflow.Config, a LineFacts or an admission built without one runs the daemon's own rule.
func (r SlotRule) OrMain() SlotRule {
	if r.Name == "" {
		return MainRule
	}
	return r
}

// PhaseKeepsSlot says whether an issue standing at at keeps its slot under r (KeepsSlotIn).
// MainRule's list, the daemon's own, is the phases that hold one of the cap's slots: admitted,
// where a root's architect writes its design and a started child waits to be entered again;
// awaiting_person; the four phases a worker works up to the review — planning, implementing,
// testing and reviewing; and held, which is reached here only where the record names no earlier
// phase for the hold (StandsAt). Every other phase holds none: accepting, retro and merging each
// stand past the issue's review, and done has finished.
//
// It is written as one list, in the rule value, so that a later change adds or removes one entry
// and every reader of the rule moves with it. Two read it, and a phase in one and not the other
// would be a deadlock: the cap's own half (KeepsSlot) and the mark a start of a phase worker
// carries for the slot it waits for (workflow.StartOrOwe), whose row the outbox holds unclaimable
// while the issue holds no slot. PastReview is the complement of the phases a rule refuses, for
// the repair that must reach an issue holding no row.
func (r SlotRule) PhaseKeepsSlot(at phase.Phase) bool {
	return slices.Contains(r.OrMain().KeepsSlotIn, at)
}

// ReadsReadyMark says whether r reads the merger's ready mark (LineFacts.Ready): it gives a slot
// back there (ReleaseAtReady) and keeps one in merging, where the mark is made. The read of the
// line's facts reads the mark under such a rule alone (ReadLineFactsUnder), and so do a replay's
// own tables, which keep the mark of a release that made it by a move.
func (r SlotRule) ReadsReadyMark() bool {
	r = r.OrMain()
	return r.ReleaseAtReady && r.PhaseKeepsSlot(phase.Merging)
}

// GraceOver says whether a wait on another issue that began at since has lasted r's grace by now
// (WaitGrace), so that it gives the slot back: at once under a rule with no grace, and from since
// plus the grace on under one with a grace. The pass that gives the slot back is the first at or
// after that moment, so the delay to it is the rule's own.
func (r SlotRule) GraceOver(since, now time.Time) bool {
	return !now.Before(since.Add(r.OrMain().WaitGrace))
}

// KeepsSlot is the phase half of r's admission rule, as the package's KeepsSlot is of the
// daemon's own: issue is in the workflow (InWorkflow) and stands, as StandsAt reads it, in a
// phase r keeps a slot in.
func (r SlotRule) KeepsSlot(issue Issue) bool {
	return InWorkflow(issue) && r.PhaseKeepsSlot(StandsAt(issue))
}

// NeedsSlot says whether issue must hold one of the cap's slots now under r, as the package's
// NeedsSlot says it under the daemon's own rule: its tree is live, its root does not linger, and
// it keeps a slot where it stands (KeepsSlot).
func (r SlotRule) NeedsSlot(issue, root Issue, live bool) bool {
	return live && !root.Lingers() && r.KeepsSlot(issue)
}

// PastReview is r's phase half read the other way round: issue stands, as StandsAt reads it, in
// a phase short of done that r keeps no slot in, so it holds none of the cap's slots there while
// its work goes on. Under MainRule those phases are accepting, retro and merging, the three past
// the review, which is why the method carries the package's name for them (the package's
// PastReview); under a rule that keeps a slot in one of them, or keeps none in a phase up to the
// review, it names the phases that rule leaves without one, whatever their place in the
// workflow.
//
// It is what the two readers of "holds no slot because its phase keeps none" ask: the repair of
// a worker with no task, whose own domain is the slot holders and which must reach such an issue
// besides (admit's readSlotHolders), and the state's answer of `why`, which serves such an issue
// at work no answer (projection's atWorkPastReview). The readers that mean the workflow's own
// "past the review", where the merge is the next step whatever slot the issue holds, read the
// package's PastReview, which no rule changes (classify.ConflictNeedsMerge, workflow's merge
// forward).
func (r SlotRule) PastReview(issue Issue) bool {
	at := StandsAt(issue)
	return at != phase.Done && !r.PhaseKeepsSlot(at)
}

// ChildrenFirstCandidates is every issue of issues whose Dispatch children r's children-first
// rule can hold it for, by key and sorted: it stands in the workflow, its tree is recorded, not
// lingering and under an open design gate, and it stands at phase.Admitted, its own first phase
// still to start. Those are the only issues the children-first rule can hold, so they are the
// only parents whose Dispatch children are read at all (ChildWaitsOf). The keys come out sorted,
// so one pass reads the same set in the same order whatever order the records arrived in.
//
// The open gate is what puts the start of the wait where the spec puts it: at the gate's opening,
// before the architect's design-approved turn releases the wave. Before that opening a root's
// architect is still writing the spec and its own first phase is nothing the line would offer a
// slot to, so a root there waits for nothing and is told nothing; from the opening the root and
// every released member of its tree are issues whose first phase the line does offer a slot to,
// and each of them waits for whatever Dispatch still shows hanging under it.
//
// Under a rule whose wait stops the phase under way (WaitStopsPhase) the last term goes: a parent
// the daemon has started waits on its open children in whatever phase it stands, as one whose
// first phase never started does, and gives its slot back for that wait (blockers.waitsOf), so its
// children are read wherever it stands. Under a rule whose start may take a cover (TakeCover) the
// gate's term is that it has opened at some time (TreeGate.EverOpened): a parent's wait on its
// children stands from the first opening of its design gate, also while a new spec version has
// closed the gate again, so a cover a child holds for its parent does not end with the version.
func (r SlotRule) ChildrenFirstCandidates(issues []Issue, gates map[string]TreeGate) []string {
	anyPhase := r.OrMain().WaitStopsPhase
	everOpened := r.OrMain().Take == TakeCover
	records := byKey(issues)
	keys := make([]string, 0, len(issues))
	for _, issue := range issues {
		// The root must be among the records and must not linger. A root whose tree has left the
		// workflow is no part of a live read at all — its phase is done and its status is not
		// todo (Store.LiveIssues) — so an unrecorded root is a tree that has gone, and reading
		// the zero record's linger would answer false for exactly the case this test is for.
		root, recorded := records[issue.Tree]
		if (!anyPhase && issue.Phase != phase.Admitted) || !InWorkflow(issue) || !recorded || root.Lingers() {
			continue
		}
		if gate := gates[issue.Tree]; !gate.Open() && !(everOpened && gate.EverOpened()) {
			continue
		}
		keys = append(keys, issue.Key)
	}
	slices.Sort(keys)
	return keys
}

// SlotRules is every named rule the repository keeps, the daemon's own first, and then the rules
// the replay's comparison runs beside it: the rules two recorded windows ran under
// (legion-v20.42.5, legion-v19.57.3) and the seven candidates of the comparison (keep-past-review,
// blockers-first, returned-by-rank, cover, wait-grace and wait-grace-1h, one rule at two graces,
// and returned-by-first-slot). `legion replay rules` prints them. Each answer is built afresh, so a
// caller that changes a rule it was handed changes no other caller's.
func SlotRules() []SlotRule {
	return []SlotRule{mainRuleWith(MainRule.Name, func(*SlotRule) {}), windowOneRule(), windowTwoRule(), keepPastReviewRule(),
		blockersFirstRule(), returnedByRankRule(), coverRule(),
		waitGraceRule("wait-grace", waitGrace), waitGraceRule("wait-grace-1h", longWaitGrace), returnedByFirstSlotRule()}
}

// The commits the recorded rules come from: release 20.42.5, the last a deployment ran over its
// window one, whose slot rule is the one 20.25.1 (52b3cf5c) ran before it and whose transition
// table carries the five rows that move a conflicting head back to implementing; and release
// 19.57.3, which the same deployment ran over its window two.
const (
	conflictMovesBackCommit = "958173da0ea4cd03290a1964edf9eb92e6685200"
	awaitingMergeCommit     = "9542c6fbbecd184ccee73523ab3defa39d6cf23a"
)

// mainRuleWith is MainRule under name, moved by change and nothing else, its phases a copy: the
// one way a rule that is the daemon's own with one choice changed is built, so its other choices
// are the daemon's own by construction. Its commit is MainRule's, the code its every other choice
// comes from.
func mainRuleWith(name string, change func(*SlotRule)) SlotRule {
	rule := MainRule
	rule.Name = name
	rule.KeepsSlotIn = slices.Clone(MainRule.KeepsSlotIn)
	change(&rule)
	return rule
}

// everyWorkflowPhase is every phase an issue the daemon has started stands in until it is done:
// the phases a rule that keeps a started issue's slot through its whole work keeps one in.
func everyWorkflowPhase() []phase.Phase {
	return []phase.Phase{phase.Admitted, phase.AwaitingPerson, phase.Planning, phase.Implementing, phase.Testing,
		phase.Reviewing, phase.Accepting, phase.Retro, phase.Merging, phase.Held}
}

// windowOneRule is the rule named legion-v20.42.5, which releases 20.25.1 and 20.42.5 ran: the
// daemon's own slot choices — no slot past the review (#3573) and none during a wait on another
// issue (#3715) — and a head that conflicts with its base moving back to implementing by workflow's
// TriggerConflict, the rows of that release's transition table the daemon's own table no longer
// carries (workflow.TableFor), where main keeps the phase and merges the base forward (#4464).
func windowOneRule() SlotRule {
	return mainRuleWith("legion-v20.42.5", func(rule *SlotRule) {
		rule.Commit = conflictMovesBackCommit
		rule.Conflict = ConflictMovesBack
	})
}

// windowTwoRule is the rule named legion-v19.57.3, which release 19.57.3 ran (#2751 and #3206): a
// started issue keeps its slot in every phase of the workflow until the merger's ready mark inside
// merging, which that release's awaiting_merge was and a replay reads as a mark (ReleaseAtReady,
// LineFacts.Ready); no wait on another issue gives the slot back; and red checks, that release's
// one path of a conflicting head, move the issue back to implementing by workflow's
// TriggerChecksRed.
func windowTwoRule() SlotRule {
	return mainRuleWith("legion-v19.57.3", func(rule *SlotRule) {
		rule.Commit = awaitingMergeCommit
		rule.KeepsSlotIn = everyWorkflowPhase()
		rule.LinkWaitGivesBack, rule.ChildrenWaitGivesBack = false, false
		rule.Conflict = ConflictChecksRed
		rule.ReleaseAtReady = true
	})
}

// keepPastReviewRule is the daemon's own rule with the slot kept in accepting, retro and merging
// too, so a send-back past the review finds its issue holding its slot.
func keepPastReviewRule() SlotRule {
	return mainRuleWith("keep-past-review", func(rule *SlotRule) {
		rule.KeepsSlotIn = everyWorkflowPhase()
	})
}

// blockersFirstRule is the daemon's own rule with each issue that a started issue waits on placed
// first among the issues that have not started (LineBlockersFirst).
func blockersFirstRule() SlotRule {
	return mainRuleWith("blockers-first", func(rule *SlotRule) {
		rule.Order = LineBlockersFirst
	})
}

// returnedByRankRule is the daemon's own rule with an issue whose work came back placed in the
// line by its board rank rather than by the moment it gave its slot back (LineReturnedByRank).
func returnedByRankRule() SlotRule {
	return mainRuleWith("returned-by-rank", func(rule *SlotRule) {
		rule.Order = LineReturnedByRank
	})
}

// coverRule is the rule of the tool's spec at version 4 and its decisions at version 1: a started
// issue keeps its slot in every phase until its work is done, and no wait on another issue gives
// it back; an issue a counted waiter waits on starts at once on that waiter's slot, which it then
// holds for the waiter, one waiter for each holder, and the waiter is not counted while the cover
// stands (TakeCover; admit's covers). The line keeps the daemon's own order: the issues that came
// back oldest first and the rest by the board's rank, with nothing put ahead of the rank. A
// conflicting head keeps its phase, as on main, which the spec was written against.
func coverRule() SlotRule {
	return mainRuleWith("cover", func(rule *SlotRule) {
		rule.KeepsSlotIn = everyWorkflowPhase()
		rule.LinkWaitGivesBack, rule.ChildrenWaitGivesBack = false, false
		rule.Take = TakeCover
	})
}

// waitGraceRule is the rule named name, the daemon's own rule with a wait on another issue that
// gives the slot back only once it has lasted grace (WaitGrace): a holder keeps its slot through a
// shorter wait, which neither frees a slot nor sends the holder to the line, and a longer one gives
// it back at the first pass at or after the grace's end, as the daemon's rule gives it back at
// once. The repository keeps it at two graces, wait-grace (waitGrace) and wait-grace-1h
// (longWaitGrace), so the comparison shows what a longer grace keeps and costs. Its reason is a
// deployment's reported cases: a started issue's blocked_by link set and taken off within a minute
// took its slot back and placed it last of 79 in the line's returned part, which orders by the
// moment the work came back, so one minute of a wait put work in progress behind every issue that
// came back before it.
func waitGraceRule(name string, grace time.Duration) SlotRule {
	return mainRuleWith(name, func(rule *SlotRule) {
		rule.WaitGrace = grace
	})
}

// waitGrace is the grace of the rule wait-grace, five minutes, read from the recorded window the
// comparison names window one (94 hours at a cap of 157). There a slot that came free was taken in
// the pass that freed it (776 of 819 releases), so the median time from a slot's release to its
// next taking, 19 microseconds, names no grace; the median time from one pass that freed a slot to
// the next was 4 minutes 44 seconds (760 such passes), the moment a slot next comes free, and five
// minutes is that time rounded up. Of the 114 waits on another issue that gave a slot back there,
// one lasted under a minute (15 seconds) and the next shortest 14 minutes, the median 20 hours, so
// five minutes keeps the slot through the one-minute class and through no longer wait of the
// window, at a cost of five more minutes of slot for each longer wait: 9.4 slot-hours of the
// window's 14,768. The pass that gives the slot back is the first after the grace's end, the
// rule's own delay: over that window's waits the next pass a replay makes (an event, a recorded
// move, a slot moved or a tick) came a median 2.6 seconds after the grace's end, at most 31.5.
const waitGrace = 5 * time.Minute

// longWaitGrace is the grace of the rule wait-grace-1h, one hour: wait-grace at a longer grace, to
// show beside it what a longer grace keeps and what it costs. Of the same 114 waits of window one,
// 8 ended within an hour (7.0%) and one more stood open at the window's end after less than an
// hour, so an hour keeps the slot through 9 (7.9%), where five minutes keeps it through 1; and each
// wait holds its slot for the shorter of its length and the hour, 109.28 slot-hours in all, where
// five minutes holds 9.42. The comparison reads both from each window's record (replay's
// graceKeeps).
const longWaitGrace = time.Hour

// returnedByFirstSlotRule is the daemon's own rule with an issue whose work came back placed in the
// line by the moment of its first slot in its current generation (LineReturnedByFirstSlot) rather
// than by the moment its work came back. Its reason is a deployment's reported cases: a started
// issue sent back from merging for one forward merge stood last of 76 in the line's returned part,
// and one whose one-minute wait gave its slot back last of 79, since that part orders by the moment
// the work came back; under this rule each stands where its first admission puts it, ahead of the
// returned work the daemon started after it. The daemon records no first slot once a slot goes
// back (its slot row goes with it), so a replay keeps the first slots in a table of its own
// (LineFacts.FirstSlots, admit's RuleTables), and a deployment that adopts this rule needs that
// record in the daemon's own store first.
func returnedByFirstSlotRule() SlotRule {
	return mainRuleWith("returned-by-first-slot", func(rule *SlotRule) {
		rule.Order = LineReturnedByFirstSlot
	})
}

// SlotRuleNamed is the rule SlotRules keeps under name, and whether it keeps one.
func SlotRuleNamed(name string) (SlotRule, bool) {
	for _, rule := range SlotRules() {
		if rule.Name == name {
			return rule, true
		}
	}
	return SlotRule{}, false
}
