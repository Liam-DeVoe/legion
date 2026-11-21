package record

import (
	"slices"

	"github.com/sjawhar/legion/daemon/internal/phase"
)

// SlotRule is one rule of which issues hold one of the cap's admission slots and how the waiting
// line gives the slots out: the phases that keep a slot, which waits on another issue give it
// back, whether such a wait stops the phase under way, the order of the line, what a start may
// take from the cap, what a head that conflicts with its base does, and where inside merging the
// slot goes back. It is a value with a name, so that the daemon runs its own rule (MainRule) and a
// replay of a recorded window runs the daemon's own admission pass under another rule the
// repository keeps (SlotRules), and a rule is read by name rather than copied into a script.
//
// Every reader of the rule takes it from the pass, the engine or the projection it was given to
// (admit's Admission.UseSlotRule, workflow.Config.SlotRule, LineFacts.Rule): no reader reads a
// rule of its own choosing, and no rule is held in a package variable a reader could change, so
// two replays under two rules can run at once in one process.
type SlotRule struct {
	// Name is the rule's name, the word a caller picks it by (SlotRuleNamed). The zero value's
	// empty name is the daemon's own rule (OrMain).
	Name string
	// Commit is the commit of cybermonklabs/legion the rule's choices come from, so a reader of a
	// replay's result can read the code that made them.
	Commit string
	// KeepsSlotIn is every phase in which an issue the daemon has started keeps its slot
	// (PhaseKeepsSlot). A phase it leaves out holds none, and the start of that phase's worker
	// waits for no slot (workflow.StartOrOwe).
	KeepsSlotIn []phase.Phase
	// LinkWaitGivesBack says whether a started issue that waits on an unfinished Dispatch
	// blocked_by link gives its slot back for the wait (blockers.waitsOf).
	LinkWaitGivesBack bool
	// ChildrenWaitGivesBack says whether a started parent that waits on its open Dispatch
	// children gives its slot back for the wait (blockers.waitsOf).
	ChildrenWaitGivesBack bool
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
)

// StartTake is what a start may take from the cap, a SlotRule's choice.
type StartTake string

const (
	// TakeOwnSlot is the daemon's own: a start that needs a slot takes one of its own, and only
	// while the cap has one free.
	TakeOwnSlot StartTake = "own_slot"
	// TakeCover lets an issue that a slot holder waits on start on that holder's slot, one such
	// cover for each holder, whatever the cap says.
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
// the slot back and stop nothing; work that came back goes first, oldest first, then the line by
// rank; a start takes a slot of its own; a conflicting head keeps its phase while the base is
// merged forward. Its commit is the commit the rule was first written as a value at.
var MainRule = SlotRule{
	Name:   "main",
	Commit: "0d3786da536722f2eb26a793ab1b9827b6e90f47",
	KeepsSlotIn: []phase.Phase{phase.Admitted, phase.AwaitingPerson, phase.Planning, phase.Implementing, phase.Testing,
		phase.Reviewing, phase.Held},
	LinkWaitGivesBack:     true,
	ChildrenWaitGivesBack: true,
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
func (r SlotRule) PhaseKeepsSlot(at phase.Phase) bool {
	return slices.Contains(r.OrMain().KeepsSlotIn, at)
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

// SlotRules is every named rule the repository keeps, the daemon's own first. Each answer is a
// copy, so a caller that changes a rule it was handed changes no other caller's.
func SlotRules() []SlotRule {
	daemon := MainRule
	daemon.KeepsSlotIn = slices.Clone(MainRule.KeepsSlotIn)
	return []SlotRule{daemon}
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
