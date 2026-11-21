package replay

import (
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/workflow"
)

// TriggerReady is the merger's ready move of the release at 9542c6fb, which took an issue from
// merging into PhaseAwaitingMerge once the merger had readied its pull request. Today's workflow
// has no such move: a window of that release holds it, and the trace reads it as a mark inside
// merging (MoveSources, assumption 20), where the rule infra-w2 gives the slot back.
const TriggerReady workflow.TriggerKind = "ready"

// PhaseAwaitingMerge is the phase of the release at 9542c6fb in which an issue waited for its
// merge once the merger had readied it. Today's workflow has no such phase: the trace folds the
// time an issue spent in it into merging (MoveSources, assumption 19), so a window of that
// release is replayed on today's phases.
const PhaseAwaitingMerge phase.Phase = "awaiting_merge"

// MoveWay is how a replay makes one kind of recorded phase move. The daemon makes some moves
// itself, from facts a replay feeds it, and a replay must leave those to it; the rest come from
// the record, at the end of the work segment the record shows ending in them.
type MoveWay string

const (
	// MoveTransition is a move made at the end of its recorded work segment through
	// workflow.(*Engine).ReplayTransition: the row lookup and the transition the daemon runs for
	// the trigger, refused unless the issue stands where the record says it stood. It is the way
	// of the completions a handoff or a review settles, whose checks the record shows passed.
	MoveTransition MoveWay = "transition"
	// MoveFact is a move made at the end of its recorded work segment through the daemon's own
	// handler of a fact (MoveSource.Fact), so the guards that handler keeps — a sign-off refused
	// while a child is in the workflow, a merge that waits for the issues beneath it — hold in a
	// replay as they held in the record.
	MoveFact MoveWay = "fact"
	// MovePass is a move the daemon's own admission pass makes from the state a replay reaches,
	// as gate_opened is the promotion of an issue whose first phase the line lets start. A replay
	// never injects it: the recorded move only measures how far the replay's moment is from the
	// record's.
	MovePass MoveWay = "pass"
	// MoveDraw is a move a replay's conflict draw makes, at the rate fitted to the window. The
	// recorded moves of the trigger are not injected, or each conflict would count twice.
	MoveDraw MoveWay = "draw"
	// MoveMark is a move that moves no phase in today's workflow: a fact of the replay's own
	// (MoveSource.Fact) writes a mark inside the phase the issue stands in, which a rule may read.
	MoveMark MoveWay = "mark"
)

// MoveSource is how the trace builder reads one trigger's recorded moves and how a replay makes
// them: the line the record holds each move in, the way the replay makes it and, for a trigger of
// a recorded release that today's table lacks, how the move is mapped onto today's phases and the
// assumptions the mapping rests on.
type MoveSource struct {
	// Line is the msg of the recorded line each move of the trigger is read from, whose trigger
	// attribute is the key the source stands under.
	Line string
	// Way is how the replay makes the move.
	Way MoveWay
	// Fact names the fact the replay applies for the move where Way is MoveFact or MoveMark, as
	// <package>.<type>: a fact of package intake, which the daemon's own handler applies, or one
	// of this package's.
	Fact string
	// To is the phase the replay moves the issue to where it is one phase whatever phase the
	// issue left, as a release's checks_red moved an issue back to implementing from any phase;
	// empty where each move goes where its row of the table goes.
	To phase.Phase
	// Folds is a phase a recorded release entered by this trigger and today's workflow lacks,
	// which the trace reads as the phase the move left: the time an issue spent in it is time in
	// that phase, and a move out of it is a move out of that phase. Empty for a trigger that
	// enters no such phase.
	Folds phase.Phase
	// Assumptions is the numbers of the assumptions (Assumptions) the mapping of the move rests
	// on, for a move of a recorded release today's table lacks; none for a move today's table
	// makes as the record shows it made.
	Assumptions []int
}

// MoveSources is the trace builder's map from a recorded move to the move a replay makes, by the
// trigger the record's line names: one entry for each trigger of a row of the table any rule a
// replay runs carries (workflow.TableFor), and for each trigger of a recorded release that
// today's table lacks, which the entry maps onto today's phases. A recorded move whose trigger
// has no entry here has no source in the replay, and the inventory test of the table
// (TestEveryRowOfTheTableHasASourceInTheRecord) names it.
var MoveSources = map[workflow.TriggerKind]MoveSource{}
