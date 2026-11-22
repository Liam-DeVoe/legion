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
// rule can hold it for, by key and sorted (the package's ChildrenFirstCandidates says why each
// term is there): it stands in the workflow, its tree is recorded, not lingering and under an
// open design gate, and it stands at phase.Admitted, its own first phase still to start. Under a
// rule whose wait stops the phase under way (WaitStopsPhase) the last term goes: a parent the
// daemon has started waits on its open children in whatever phase it stands, as one whose first
// phase never started does, and gives its slot back for that wait (blockers.waitsOf), so its
// children are read wherever it stands. Under a rule whose start may take a cover (TakeCover) the
// gate's term is that it has opened at some time (TreeGate.EverOpened): a parent's wait on its
// children stands from the first opening of its design gate, also while a new spec version has
// closed the gate again, so a cover a child holds for its parent does not end with the version.
func (r SlotRule) ChildrenFirstCandidates(issues []Issue, gates map[string]TreeGate) []string {
	anyPhase := r.OrMain().WaitStopsPhase
	everOpened := r.OrMain().Take == TakeCover
	byKey := make(map[string]Issue, len(issues))
	for _, issue := range issues {
		byKey[issue.Key] = issue
	}
	keys := make([]string, 0, len(issues))
	for _, issue := range issues {
		// The root must be among the records and must not linger. A root whose tree has left the
		// workflow is no part of a live read at all — its phase is done and its status is not
		// todo (Store.LiveIssues) — so an unrecorded root is a tree that has gone, and reading
		// the zero record's linger would answer false for exactly the case this test is for.
		root, recorded := byKey[issue.Tree]
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
// the replay's comparison runs beside it: the rules two recorded windows ran under (infra-w1,
// infra-w2) and the four candidates of the comparison (keep-past-review, blockers-first,
// returned-by-rank, cover). `legion replay rules` prints them. Each answer is built afresh, so a
// caller that changes a rule it was handed changes no other caller's.
func SlotRules() []SlotRule {
	daemon := MainRule
	daemon.KeepsSlotIn = slices.Clone(MainRule.KeepsSlotIn)
	return []SlotRule{daemon, windowOneRule(), windowTwoRule(), keepPastReviewRule(), blockersFirstRule(), returnedByRankRule(), coverRule()}
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

// windowOneRule is the rule named infra-w1, which releases 20.25.1 and 20.42.5 ran: the daemon's
// own slot choices — no slot past the review (#3573) and none during a wait on another issue
// (#3715) — and a head that conflicts with its base moving back to implementing by workflow's
// TriggerConflict, the rows of that release's transition table the daemon's own table no longer
// carries (workflow.TableFor), where main keeps the phase and merges the base forward (#4464).
func windowOneRule() SlotRule {
	return mainRuleWith("infra-w1", func(rule *SlotRule) {
		rule.Commit = conflictMovesBackCommit
		rule.Conflict = ConflictMovesBack
	})
}

// windowTwoRule is the rule named infra-w2, which release 19.57.3 ran (#2751 and #3206): a started
// issue keeps its slot in every phase of the workflow until the merger's ready mark inside
// merging, which that release's awaiting_merge was and a replay reads as a mark (ReleaseAtReady,
// LineFacts.Ready); no wait on another issue gives the slot back; and red checks, that release's
// one path of a conflicting head, move the issue back to implementing by workflow's
// TriggerChecksRed.
func windowTwoRule() SlotRule {
	return mainRuleWith("infra-w2", func(rule *SlotRule) {
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

// SlotRuleNamed is the rule SlotRules keeps under name, and whether it keeps one.
func SlotRuleNamed(name string) (SlotRule, bool) {
	for _, rule := range SlotRules() {
		if rule.Name == name {
			return rule, true
		}
	}
	return SlotRule{}, false
}
