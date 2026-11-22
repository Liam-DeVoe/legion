package replay

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/workflow"
)

// knobValuesOf is each knob of b by its JSON name and its value: every string field of a Branch
// but its name.
func knobValuesOf(b Branch) map[string]string {
	values := map[string]string{}
	fields := reflect.ValueOf(b)
	for i := range fields.NumField() {
		tag, _, _ := strings.Cut(fields.Type().Field(i).Tag.Get("json"), ",")
		if tag != "name" {
			values[tag] = fields.Field(i).String()
		}
	}
	return values
}

// The branches of design section 7 are the main branch and each change of one knob to one of its
// other values, each at each slowdown factor, 24 in all: at f 1.0, 1.2 and 1.6, the main branch
// (offset, offset, recorded, recorded, none, fitted), named main, and main+timing=wall,
// main+answer=wall, main+afterSendBack=less-slot-wait, main+firstStarts=rank,
// main+neverStarted=drawn, main+neverStarted=drawn-done and main+merges=zero, each differing from
// the main branch in its one named knob and read back whole by BranchNamed. A name with a value its
// knob does not take is refused.
func TestBranchesAreTheMainBranchAndEachOneKnobChange(t *testing.T) {
	branches := Branches()
	if len(branches) != 24 {
		t.Fatalf("Branches lists %d branches, want 24", len(branches))
	}
	wantNames := []string{"main", "main+afterSendBack=less-slot-wait", "main+answer=wall", "main+firstStarts=rank", "main+merges=zero",
		"main+neverStarted=drawn", "main+neverStarted=drawn-done", "main+timing=wall"}
	wantMain := map[string]string{"timing": "offset", "answer": "offset", "afterSendBack": "recorded", "firstStarts": "recorded",
		"neverStarted": "none", "merges": "fitted"}
	if got := knobValuesOf(MainBranch); !reflect.DeepEqual(got, wantMain) {
		t.Errorf("the main branch is %v, want %v", got, wantMain)
	}
	for _, f := range []float64{1.0, 1.2, 1.6} {
		var names []string
		for _, at := range branches {
			if at.Slowdown != f {
				continue
			}
			b := at.Branch
			names = append(names, b.Name)
			if parsed, err := BranchNamed(b.Name); err != nil || parsed != b {
				t.Errorf("at f %v the branch %+v reads back by its name as %+v (%v)", f, b, parsed, err)
			}
			var changed []string
			for knob, value := range knobValuesOf(b) {
				if value != wantMain[knob] {
					changed = append(changed, knob+"="+value)
				}
			}
			switch {
			case b.Name == MainBranch.Name && len(changed) != 0:
				t.Errorf("at f %v the main branch changes %v", f, changed)
			case b.Name != MainBranch.Name && (len(changed) != 1 || b.Name != MainBranch.Name+"+"+changed[0]):
				t.Errorf("at f %v the branch named %s changes %v, want the one knob its name names", f, b.Name, changed)
			}
		}
		slices.Sort(names)
		if !slices.Equal(names, wantNames) {
			t.Errorf("at f %v Branches lists %v, want %v", f, names, wantNames)
		}
	}
	for _, name := range []string{"main+timing=later", "main+slowdown=1.2", "main+merges", "other"} {
		if _, err := BranchNamed(name); err == nil {
			t.Errorf("BranchNamed accepts %q", name)
		}
	}
}

// knobRun replays window under rule on the branch named name, at seed 1 and f 1, and answers its
// result and its log.
func knobRun(t *testing.T, window Window, rule record.SlotRule, name string) (RunResult, []replayLine) {
	t.Helper()
	branch, err := BranchNamed(name)
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	result, err := Run(context.Background(), RunConfig{Window: window, Rule: rule, Branch: branch, Seed: 1, Slowdown: 1, PGDSN: replayDSN(t), Out: out})
	if err != nil {
		t.Fatalf("replay the window under %s on %s: %v", rule.Name, name, err)
	}
	return result, replayLog(t, out)
}

// linesAt is the moments of the lines of log that match.
func linesAt(log []replayLine, match func(replayLine) bool) []time.Time {
	var at []time.Time
	for _, line := range log {
		if match(line) {
			at = append(at, line.Time)
		}
	}
	return at
}

// pastReviewKept is the record of a daemon that keeps the slot past the review: key's segment in
// from ends at at and it moves to to by trigger, its slot kept, and the role of to starts its turn.
func (b *windowBuilder) pastReviewKept(at time.Time, key string, from, to phase.Phase, trigger workflow.TriggerKind) {
	b.t.Helper()
	b.turnEnds(at, key, phase.RoleFor(from))
	b.moved(at, key, from, to, trigger)
	if role := phase.RoleFor(to); role != "" {
		b.turnStarts(at, key, role)
	}
}

// rootThroughReview is key, a root promoted at at, working 15 minutes in each phase from its
// architect's design to its reviewer's approval: in accepting from at + 75 minutes.
func (b *windowBuilder) rootThroughReview(at time.Time, key string) {
	b.t.Helper()
	b.rootPromoted(at, key)
	b.rootDesigned(at.Add(15*time.Minute), key)
	b.segmentEnds(at.Add(30*time.Minute), key, phase.Planning, phase.Implementing, workflow.TriggerPlannerCompleted)
	b.segmentEnds(at.Add(45*time.Minute), key, phase.Implementing, phase.Testing, workflow.TriggerImplementationReady)
	b.segmentEnds(at.Add(60*time.Minute), key, phase.Testing, phase.Reviewing, workflow.TriggerTesterPassed)
}

// laterStartWindow is a window recorded under keep-past-review whose second root starts 45
// minutes later in the record than in a replay under main, so every event that moves with its
// work comes sooner in the replay. The cap is 1 and the spec is a gated design step. ACME-1, a
// root, holds the slot from 09:00 through its review and past it, to its merge at 11:00, when
// ACME-2 is promoted; a replay under main gives the slot back at ACME-1's accepting at 10:15 and
// promotes ACME-2 then. ACME-2's architect writes version 1 of its spec 10 minutes into its turn
// and registers it, unapproved, at 15; a person approves it 30 minutes after the version (11:40
// in the record), which opens the gate and starts planning. Ten minutes into planning (11:50) its
// planner links it as blocked by ACME-3, an issue Legion does not work, and the slot goes back for
// the wait; a person closes ACME-3 20 minutes after the link (12:10), and ACME-2 takes its slot
// again. Planning runs 40 minutes, to 12:20.
func laterStartWindow(t *testing.T) Window {
	b := newWindowBuilder(t, clock(9, 0), clock(13, 0))
	b.window.Parts[0].Rule = "keep-past-review"
	b.capped(1)
	b.gated(record.DesignStepSpec)
	b.boot(clock(9, 0))
	b.file(clock(9, 0), "ACME-1", filing{Title: "Export the widget list", Status: "todo", Rank: "a0", Labels: handedToLegion})
	b.rootPromoted(clock(9, 0), "ACME-1")
	b.file(clock(9, 1), "ACME-3", filing{Title: "Agree the shelf names with the warehouse", Status: "backlog", Rank: "a3"})
	b.file(clock(9, 2), "ACME-2", filing{Title: "Count the widgets per shelf", Status: "todo", Rank: "a1", Labels: handedToLegion})
	b.specWritten(clock(9, 5), "ACME-1")
	b.approves(clock(9, 10), "ACME-1", "")
	b.rootDesigned(clock(9, 15), "ACME-1")
	b.segmentEnds(clock(9, 30), "ACME-1", phase.Planning, phase.Implementing, workflow.TriggerPlannerCompleted)
	b.segmentEnds(clock(9, 45), "ACME-1", phase.Implementing, phase.Testing, workflow.TriggerImplementationReady)
	b.segmentEnds(clock(10, 0), "ACME-1", phase.Testing, phase.Reviewing, workflow.TriggerTesterPassed)
	b.pastReviewKept(clock(10, 15), "ACME-1", phase.Reviewing, phase.Accepting, workflow.TriggerReviewApproved)
	b.pastReviewKept(clock(10, 30), "ACME-1", phase.Accepting, phase.Retro, workflow.TriggerSignOff)
	b.pastReviewKept(clock(10, 45), "ACME-1", phase.Retro, phase.Merging, workflow.TriggerRetroCompleted)
	b.turnEnds(clock(11, 0), "ACME-1", claim.RoleMerger)
	b.moved(clock(11, 0), "ACME-1", phase.Merging, phase.Done, workflow.TriggerPullRequestMerged)
	b.leaves(clock(11, 0), "ACME-1", record.DoneStatus)
	b.rootPromoted(clock(11, 0), "ACME-2")
	b.specWritten(clock(11, 10), "ACME-2")
	b.turnEnds(clock(11, 15), "ACME-2", claim.RoleArchitect)
	b.registered(clock(11, 15), "ACME-2", false)
	b.approves(clock(11, 40), "ACME-2", "")
	b.info(clock(11, 40), "workflow: design gate opened", "issue", "ACME-2", "artifact", b.artifact("ACME-2"), "event", "artifact.approved",
		"version", 1)
	b.rootFirstPhase(clock(11, 40), "ACME-2")
	planner := eventActor{Kind: "session", ID: string(b.token("ACME-2", claim.RolePlanner))}
	b.changed(clock(11, 50), "ACME-2", planner, func(issue *builtIssue) { issue.BlockedBy = []string{"ACME-3"} })
	b.slotReleased(clock(11, 50), "ACME-2")
	b.edit(clock(12, 10), "ACME-3", setStatus(record.DoneStatus))
	b.info(clock(12, 10), "admission gives a slot back to an issue whose wait ended", "issue", "ACME-2", "phase", string(phase.Planning))
	b.slotTaken(clock(12, 10), "ACME-2")
	b.segmentEnds(clock(12, 20), "ACME-2", phase.Planning, phase.Implementing, workflow.TriggerPlannerCompleted)
	return b.build()
}

// Timing places the close of an outside blocker that ended a wait (assumption 4): under offset at
// the replay's start of the wait plus its recorded offset from the record's, under wall at its
// recorded time. The test replays laterStartWindow under main, where ACME-2's planning, and with
// it its planner's link to ACME-3, comes 45 minutes sooner than in the record: the link at 11:05.
// On the main branch ACME-3 is closed 20 minutes after the link, at 11:25, and ACME-2 takes its
// slot back then; on main+timing=wall ACME-3 is closed at its recorded 12:10, and ACME-2 takes
// its slot back at 12:10.
func TestTheTimingKnobPlacesAWaitsEndByItsAnchor(t *testing.T) {
	window := laterStartWindow(t)
	for _, c := range []struct {
		branch string
		back   time.Time
	}{{"main", clock(11, 25)}, {"main+timing=wall", clock(12, 10)}} {
		t.Run(c.branch, func(t *testing.T) {
			_, log := knobRun(t, window, mainRule(t), c.branch)
			taken := linesAt(log, func(line replayLine) bool { return line.Msg == "admission: slot taken" && line.Issue == "ACME-2" })
			want := []time.Time{clock(10, 15), c.back}
			if !slices.EqualFunc(taken, want, time.Time.Equal) {
				t.Errorf("ACME-2 took a slot at %s, want at %s: its promotion and its wait's end", describeTimes(taken), describeTimes(want))
			}
		})
	}
}

// Answer places a person's review of a spec version (assumption 5): under offset at the replay's
// moment of the version plus its recorded offset from the record's, under wall at its recorded
// time. The test replays laterStartWindow under main, where ACME-2's architect writes its spec's
// version 1 at 10:25, 45 minutes sooner than in the record. On the main branch the person approves
// it 30 minutes later, at 10:55, and the open gate moves ACME-2 into planning then; on
// main+answer=wall the approval comes at its recorded 11:40, and ACME-2 moves into planning at
// 11:40.
func TestTheAnswerKnobPlacesAnAnswerByItsQuestion(t *testing.T) {
	window := laterStartWindow(t)
	for _, c := range []struct {
		branch string
		moved  time.Time
	}{{"main", clock(10, 55)}, {"main+answer=wall", clock(11, 40)}} {
		t.Run(c.branch, func(t *testing.T) {
			_, log := knobRun(t, window, mainRule(t), c.branch)
			moved := linesAt(log, func(line replayLine) bool {
				return line.Msg == phaseChangedLine && line.Issue == "ACME-2" && line.From == string(phase.Admitted) && line.To == string(phase.Planning)
			})
			if len(moved) != 1 || !moved[0].Equal(c.moved) {
				t.Errorf("ACME-2 moved into planning at %s, want once at %s", describeTimes(moved), describeTimes([]time.Time{c.moved}))
			}
		})
	}
}

// sendBackWindow is a window recorded under main in which work after a send-back waits for a
// slot. The cap is 1. ACME-1, a root, works 15 minutes in each phase to accepting at 10:15, where
// its slot goes to ACME-2. At 10:45 ACME-1's architect sends it back to implementing, which needs a
// slot the cap does not have; at 11:05 a person closes ACME-2, which leaves the workflow, and
// ACME-1 takes the slot: its implementer works from 11:05 to the move to testing at 11:35, after a
// recorded wait for the slot of 20 minutes.
func sendBackWindow(t *testing.T) Window {
	b := newWindowBuilder(t, clock(9, 0), clock(12, 0))
	b.capped(1)
	b.boot(clock(9, 0))
	b.file(clock(9, 0), "ACME-1", filing{Title: "Export the widget list", Status: "todo", Rank: "a0", Labels: handedToLegion})
	b.file(clock(9, 1), "ACME-2", filing{Title: "Count the widgets per shelf", Status: "todo", Rank: "a1", Labels: handedToLegion})
	b.rootThroughReview(clock(9, 0), "ACME-1")
	b.segmentEnds(clock(10, 15), "ACME-1", phase.Reviewing, phase.Accepting, workflow.TriggerReviewApproved)
	b.rootPromoted(clock(10, 15), "ACME-2")
	b.rootDesigned(clock(10, 30), "ACME-2")
	b.turnEnds(clock(10, 45), "ACME-1", claim.RoleArchitect)
	b.moved(clock(10, 45), "ACME-1", phase.Accepting, phase.Implementing, workflow.TriggerBackward)
	b.edit(clock(11, 5), "ACME-2", setStatus(record.DoneStatus))
	b.leaves(clock(11, 5), "ACME-2", record.DoneStatus)
	b.info(clock(11, 5), "admission gives a slot back to an issue whose work came back", "issue", "ACME-1", "phase", string(phase.Implementing))
	b.slotTaken(clock(11, 5), "ACME-1")
	b.turnStarts(clock(11, 5), "ACME-1", claim.RoleImplementer)
	b.segmentEnds(clock(11, 35), "ACME-1", phase.Implementing, phase.Testing, workflow.TriggerImplementationReady)
	return b.build()
}

// AfterSendBack sizes the work after a send-back (assumption 13): under recorded at its recorded
// length from its worker start, under less-slot-wait at that length less its recorded wait for a
// slot. The test replays sendBackWindow under main, its own rule, so ACME-1's implementer starts
// at 11:05 under either value: on the main branch it moves to testing after its recorded 30
// minutes, at 11:35; on main+afterSendBack=less-slot-wait after 30 less the 20 it waited for the
// slot, at 11:15.
func TestTheAfterSendBackKnobShortensWorkAfterASendBack(t *testing.T) {
	window := sendBackWindow(t)
	for _, c := range []struct {
		branch string
		moved  time.Time
	}{{"main", clock(11, 35)}, {"main+afterSendBack=less-slot-wait", clock(11, 15)}} {
		t.Run(c.branch, func(t *testing.T) {
			_, log := knobRun(t, window, mainRule(t), c.branch)
			moved := linesAt(log, func(line replayLine) bool {
				return line.Msg == phaseChangedLine && line.Issue == "ACME-1" && line.From == string(phase.Implementing) && line.To == string(phase.Testing)
			})
			want := []time.Time{clock(9, 45), c.moved}
			if !slices.EqualFunc(moved, want, time.Time.Equal) {
				t.Errorf("ACME-1 moved from implementing to testing at %s, want at %s", describeTimes(moved), describeTimes(want))
			}
		})
	}
}

// firstStartsWindow is a window recorded under main whose first starts differ from the board's
// order once a replay holds its slot longer. The cap is 1. ACME-1, a root, holds the slot to
// accepting at 10:15 and merges at 11:00; ACME-2 (rank a2), filed at 09:01, takes the slot at 10:15;
// ACME-3 (rank a1), filed at 10:20, takes it at ACME-2's accepting at 11:30. Under keep-past-review
// ACME-1 keeps its slot to its merge, so at 11:00 ACME-2 and ACME-3 both wait for it.
func firstStartsWindow(t *testing.T) Window {
	b := newWindowBuilder(t, clock(9, 0), clock(12, 0))
	b.capped(1)
	b.boot(clock(9, 0))
	b.file(clock(9, 0), "ACME-1", filing{Title: "Export the widget list", Status: "todo", Rank: "a0", Labels: handedToLegion})
	b.file(clock(9, 1), "ACME-2", filing{Title: "Count the widgets per shelf", Status: "todo", Rank: "a2", Labels: handedToLegion})
	b.rootThroughReview(clock(9, 0), "ACME-1")
	b.segmentEnds(clock(10, 15), "ACME-1", phase.Reviewing, phase.Accepting, workflow.TriggerReviewApproved)
	b.rootPromoted(clock(10, 15), "ACME-2")
	b.file(clock(10, 20), "ACME-3", filing{Title: "Keep the last search on the shelf screen", Status: "todo", Rank: "a1", Labels: handedToLegion})
	b.segmentEnds(clock(10, 30), "ACME-1", phase.Accepting, phase.Retro, workflow.TriggerSignOff)
	b.rootDesigned(clock(10, 30), "ACME-2")
	b.segmentEnds(clock(10, 45), "ACME-1", phase.Retro, phase.Merging, workflow.TriggerRetroCompleted)
	b.segmentEnds(clock(10, 45), "ACME-2", phase.Planning, phase.Implementing, workflow.TriggerPlannerCompleted)
	b.segmentEnds(clock(11, 0), "ACME-1", phase.Merging, phase.Done, workflow.TriggerPullRequestMerged)
	b.leaves(clock(11, 0), "ACME-1", record.DoneStatus)
	b.segmentEnds(clock(11, 0), "ACME-2", phase.Implementing, phase.Testing, workflow.TriggerImplementationReady)
	b.segmentEnds(clock(11, 15), "ACME-2", phase.Testing, phase.Reviewing, workflow.TriggerTesterPassed)
	b.segmentEnds(clock(11, 30), "ACME-2", phase.Reviewing, phase.Accepting, workflow.TriggerReviewApproved)
	b.rootPromoted(clock(11, 30), "ACME-3")
	b.rootDesigned(clock(11, 45), "ACME-3")
	return b.build()
}

// FirstStarts orders the issues whose first phase can start (design 10.7): under recorded by the
// order the record started them in, under rank by the board's rank. The test replays
// firstStartsWindow under keep-past-review, where ACME-1's slot comes free at its merge at 11:00
// with ACME-2 (started second in the record, rank a2) and ACME-3 (started third, rank a1) both
// waiting: on the main branch ACME-2 takes it, and on main+firstStarts=rank ACME-3 does.
func TestTheFirstStartsKnobOrdersFirstStartsByTheRecordOrByRank(t *testing.T) {
	window := firstStartsWindow(t)
	for _, c := range []struct {
		branch, first string
	}{{"main", "ACME-2"}, {"main+firstStarts=rank", "ACME-3"}} {
		t.Run(c.branch, func(t *testing.T) {
			result, log := knobRun(t, window, replayedRule(t, "keep-past-review"), c.branch)
			var taken []string
			for _, line := range log {
				if line.Msg == "admission: slot taken" && line.Time.Equal(clock(11, 0)) {
					taken = append(taken, line.Issue)
				}
			}
			if !slices.Equal(taken, []string{c.first}) {
				t.Errorf("at 11:00 the slot went to %v, want %s", taken, c.first)
			}
			if first, ok := result.Scorecard.FirstSlot[c.first]; !ok || !first.Equal(clock(11, 0)) {
				t.Errorf("%s's first slot is %s, want 11:00", c.first, stamp(first))
			}
		})
	}
}

// setBackRootWindow is a window from 10:00 to 12:45 under a cap of 1, its inputs reaching back to
// the daemon's boot at 07:00. ACME-1, a root at the board's lowest rank (z1), runs from its filing
// at 07:05 to its merge at 07:45, and a person sets it back to todo at 09:00, where it waits for the
// slot ACME-4 (a4) has held since its promotion at 08:45 and gives back at its accepting at 10:15.
// ACME-2 (a2) and ACME-3 (a3), handed to Legion at 09:10 and 09:20, wait with it: ACME-2 takes the
// slot at 10:15 and works to its accepting at 11:30, where ACME-3 takes it and works to reviewing
// at 12:30. The record never gives ACME-1 a slot in the window.
func setBackRootWindow(t *testing.T) Window {
	b := newWindowBuilder(t, clock(10, 0), clock(12, 45))
	b.reaches(3 * time.Hour)
	b.capped(1)
	b.boot(clock(7, 0))
	mergedRoot(b, "ACME-1", "z1", clock(7, 5))
	b.file(clock(8, 45), "ACME-4", filing{Title: "Rotate the warehouse keys", Status: "todo", Rank: "a4", Labels: handedToLegion})
	b.rootThroughReview(clock(8, 45), "ACME-4")
	b.edit(clock(9, 0), "ACME-1", setStatus("todo"))
	b.ownTree("ACME-1")
	b.file(clock(9, 10), "ACME-2", filing{Title: "Count the widgets per shelf", Status: "todo", Rank: "a2", Labels: handedToLegion})
	b.file(clock(9, 20), "ACME-3", filing{Title: "Keep the last search on the shelf screen", Status: "todo", Rank: "a3", Labels: handedToLegion})
	b.segmentEnds(clock(10, 15), "ACME-4", phase.Reviewing, phase.Accepting, workflow.TriggerReviewApproved)
	b.rootThroughReview(clock(10, 15), "ACME-2")
	b.segmentEnds(clock(11, 30), "ACME-2", phase.Reviewing, phase.Accepting, workflow.TriggerReviewApproved)
	b.rootThroughReview(clock(11, 30), "ACME-3")
	return b.build()
}

// FirstStarts recorded orders the issues by their first slot in the window (design 10.7): a slot of
// a run that ended before the window, or one held at its start from before it, is no first start.
// The test reads setBackRootWindow, where ACME-1's first slot came in its earlier run at 07:05 and
// ACME-4 holds its slot at the start from 08:45: the ranks show ACME-2 and ACME-3 alone, at 01 and
// 02, and a replay under main gives the slot to ACME-2 at 10:15 and to ACME-3 at 11:30, as the
// record did, and none to ACME-1. Ranked by its first slot anywhere, ACME-1 led the line at 10:15
// and held the slot with no recorded work left to open, and neither ACME-2 nor ACME-3 started.
func TestARootSetBackBeforeTheWindowWaitsBehindTheWindowsFirstStarts(t *testing.T) {
	checkRecordedFirstStarts(t, setBackRootWindow(t), map[string]string{"ACME-2": "01", "ACME-3": "02"}, "ACME-2 at 10:15", "ACME-3 at 11:30")
}

// inFlightTreeWindow is a window from 10:00 to 13:00 under a cap of 1, its inputs reaching back to
// the daemon's boot at 07:00. ACME-1, a root at rank a1, holds the slot from its promotion at 08:00
// while its architect designs it. A person files its two children todo under it, ACME-2 (a2) at
// 09:00 and ACME-3 (a3) at 09:05, ACME-3 blocked by ACME-4, an issue Legion is not given; ACME-5, a
// second root at rank a5, is handed to Legion at 09:30 and waits for the slot. At 10:10 ACME-1's
// design opens its gate: it waits for its open children and gives its slot back, and ACME-2 takes
// it, ahead of ACME-5 by its tree's rank. ACME-2 reaches accepting at 11:10, while the link still
// holds ACME-3, and ACME-5 takes the slot; a person closes ACME-4 at 11:20, and ACME-3 takes the
// slot at ACME-5's accepting at 12:25.
func inFlightTreeWindow(t *testing.T) Window {
	b := newWindowBuilder(t, clock(10, 0), clock(13, 0))
	b.reaches(3 * time.Hour)
	b.capped(1)
	b.boot(clock(7, 0))
	b.file(clock(8, 0), "ACME-1", filing{Title: "Export the widget list", Status: "todo", Rank: "a1", Labels: handedToLegion})
	b.rootPromoted(clock(8, 0), "ACME-1")
	b.file(clock(8, 10), "ACME-4", filing{Title: "Agree the shelf names with the warehouse", Status: "backlog", Rank: "a9"})
	b.file(clock(9, 0), "ACME-2", filing{Title: "Write the export's header row", Parent: "ACME-1", Tree: "ACME-1", Status: "todo", Rank: "a2"})
	b.file(clock(9, 5), "ACME-3", filing{Title: "Name the header row's columns", Parent: "ACME-1", Tree: "ACME-1", Status: "todo", Rank: "a3",
		BlockedBy: []string{"ACME-4"}})
	b.file(clock(9, 30), "ACME-5", filing{Title: "Count the widgets per shelf", Status: "todo", Rank: "a5", Labels: handedToLegion})
	b.turnEnds(clock(10, 10), "ACME-1", claim.RoleArchitect)
	b.registered(clock(10, 10), "ACME-1", true)
	b.info(clock(10, 10), "admission: an issue waits for the Dispatch children under it", "issue", "ACME-1", "tree", "ACME-1",
		"generation", 1, "children", 2)
	b.slotReleased(clock(10, 10), "ACME-1")
	b.childStarts(clock(10, 10), "ACME-2")
	b.segmentEnds(clock(10, 25), "ACME-2", phase.Planning, phase.Implementing, workflow.TriggerPlannerCompleted)
	b.segmentEnds(clock(10, 40), "ACME-2", phase.Implementing, phase.Testing, workflow.TriggerImplementationReady)
	b.segmentEnds(clock(10, 55), "ACME-2", phase.Testing, phase.Reviewing, workflow.TriggerTesterPassed)
	b.segmentEnds(clock(11, 10), "ACME-2", phase.Reviewing, phase.Accepting, workflow.TriggerReviewApproved)
	b.rootThroughReview(clock(11, 10), "ACME-5")
	b.edit(clock(11, 20), "ACME-4", setStatus(record.DoneStatus))
	b.segmentEnds(clock(12, 25), "ACME-5", phase.Reviewing, phase.Accepting, workflow.TriggerReviewApproved)
	b.childStarts(clock(12, 25), "ACME-3", "ACME-4")
	return b.build()
}

// FirstStarts recorded shows each tree's root at its tree's earliest first start in the window
// (design 10.7), since the line orders the issues whose first phase can start by their tree root's
// rank first. The test reads inFlightTreeWindow, where ACME-1 holds its slot from before the start
// and takes none in the window: it ranks by ACME-2's start at 10:10, 01, above ACME-5's at 11:10,
// 02, and a replay under main gives the slot to ACME-2 at 10:10, to ACME-5 at 11:10 and to ACME-3
// at 12:25, as the record did. At its board rank ACME-1 ranked below every first start, and
// ACME-5 took the slot at 10:10 ahead of ACME-2. Root-first ordering cannot keep the recorded order
// where a later child of an earlier tree waits at the same pass as a later tree's first start: the
// child goes first by its root's rank, though a record whose board ranked the later tree higher
// started that tree first. The link keeps ACME-3 out of the line at 11:10, so this window has no
// such pass.
func TestATreeInFlightAtTheStartRanksByItsFirstChildsStart(t *testing.T) {
	checkRecordedFirstStarts(t, inFlightTreeWindow(t), map[string]string{"ACME-1": "01", "ACME-2": "01", "ACME-5": "02", "ACME-3": "03"},
		"ACME-2 at 10:10", "ACME-5 at 11:10", "ACME-3 at 12:25")
}

// checkRecordedFirstStarts reads window's trace, which must place every fact, and checks the ranks
// firstStartRanks shows its issues at against ranks, and each slot a replay of it under main gives,
// as the issue and the minute, against taken.
func checkRecordedFirstStarts(t *testing.T, window Window, ranks map[string]string, taken ...string) {
	t.Helper()
	trace, err := BuildTrace(window)
	if err != nil {
		t.Fatalf("build the trace: %v", err)
	}
	if len(trace.Unplaced) != 0 {
		t.Errorf("the trace could not place %+v", trace.Unplaced)
	}
	if got := firstStartRanks(trace); !reflect.DeepEqual(got, ranks) {
		t.Errorf("the recorded first starts rank %v, want %v", got, ranks)
	}

	_, log := knobRun(t, window, mainRule(t), "main")
	var given []string
	for _, line := range log {
		if line.Msg == "admission: slot taken" {
			given = append(given, line.Issue+" at "+line.Time.UTC().Format("15:04"))
		}
	}
	if !slices.Equal(given, taken) {
		t.Errorf("the replay gave the slot to %v, want %v", given, taken)
	}
}

// neverStartedWindow is a window recorded under keep-past-review with an issue the record never
// started. The cap is 1. ACME-1, a root, holds the slot from 09:00 past its review to its merge at
// 11:00, 15 minutes in each phase; ACME-2 then holds it from 11:00 with the same work, in
// accepting from 12:15 to the window's end; ACME-3, handed to Legion at 09:02, never starts. A
// replay under main gives ACME-1's slot back at its accepting at 10:15, ACME-2 starts then and
// reaches accepting at 11:30, and ACME-3 takes the slot at 11:30.
func neverStartedWindow(t *testing.T) Window {
	b := newWindowBuilder(t, clock(9, 0), clock(13, 0))
	b.window.Parts[0].Rule = "keep-past-review"
	b.capped(1)
	b.boot(clock(9, 0))
	b.file(clock(9, 0), "ACME-1", filing{Title: "Export the widget list", Status: "todo", Rank: "a0", Labels: handedToLegion})
	b.file(clock(9, 1), "ACME-2", filing{Title: "Count the widgets per shelf", Status: "todo", Rank: "a1", Labels: handedToLegion})
	b.file(clock(9, 2), "ACME-3", filing{Title: "Keep the last search on the shelf screen", Status: "todo", Rank: "a2", Labels: handedToLegion})
	b.rootThroughReview(clock(9, 0), "ACME-1")
	b.pastReviewKept(clock(10, 15), "ACME-1", phase.Reviewing, phase.Accepting, workflow.TriggerReviewApproved)
	b.pastReviewKept(clock(10, 30), "ACME-1", phase.Accepting, phase.Retro, workflow.TriggerSignOff)
	b.pastReviewKept(clock(10, 45), "ACME-1", phase.Retro, phase.Merging, workflow.TriggerRetroCompleted)
	b.turnEnds(clock(11, 0), "ACME-1", claim.RoleMerger)
	b.moved(clock(11, 0), "ACME-1", phase.Merging, phase.Done, workflow.TriggerPullRequestMerged)
	b.leaves(clock(11, 0), "ACME-1", record.DoneStatus)
	b.rootThroughReview(clock(11, 0), "ACME-2")
	b.pastReviewKept(clock(12, 15), "ACME-2", phase.Reviewing, phase.Accepting, workflow.TriggerReviewApproved)
	return b.build()
}

// NeverStarted gives the work of an issue the record never started (assumption 8): none under
// none; under drawn the work of an issue the record started in the window, drawn with the run's
// seed; under drawn-done that of one it finished. The test replays neverStartedWindow under main,
// where ACME-3 takes a slot at 11:30. On the main branch no worker of ACME-3 starts a turn. On
// main+neverStarted=drawn its work is ACME-1's or ACME-2's, alike through its review, and on
// main+neverStarted=drawn-done ACME-1's, the one issue finished: its architect's turn begins at
// 11:30, the drawn design is registered as ACME-3's own at 11:45, its planner's turn begins then,
// and after 15 minutes in each of planning, implementing, testing and reviewing its architect's
// turn in accepting begins at 12:45. Each run names its scenario.
func TestTheNeverStartedKnobGivesAnUnstartedIssueItsScenario(t *testing.T) {
	window := neverStartedWindow(t)
	drawnArchitect := []time.Time{clock(11, 30), clock(12, 45)}
	for _, c := range []struct {
		branch, scenario string
		architect        []time.Time
		planner          []time.Time
	}{
		{branch: "main", scenario: "none"},
		{branch: "main+neverStarted=drawn", scenario: "drawn", architect: drawnArchitect, planner: []time.Time{clock(11, 45)}},
		{branch: "main+neverStarted=drawn-done", scenario: "drawn-done", architect: drawnArchitect, planner: []time.Time{clock(11, 45)}},
	} {
		t.Run(c.branch, func(t *testing.T) {
			result, log := knobRun(t, window, mainRule(t), c.branch)
			if result.Scorecard.Scenario != c.scenario {
				t.Errorf("the run names the scenario %q, want %q", result.Scorecard.Scenario, c.scenario)
			}
			if first, ok := result.Scorecard.FirstSlot["ACME-3"]; !ok || !first.Equal(clock(11, 30)) {
				t.Errorf("ACME-3's first slot is %s, want 11:30", stamp(first))
			}
			turns := func(role claim.Role) []time.Time {
				token := "legion-acme-acme-3-" + string(role)
				return linesAt(log, func(line replayLine) bool { return line.Msg == "supervise: a turn started" && line.Claim == token })
			}
			if got := turns(claim.RoleArchitect); !slices.EqualFunc(got, c.architect, time.Time.Equal) {
				t.Errorf("ACME-3's architect started turns at %s, want at %s", describeTimes(got), describeTimes(c.architect))
			}
			if got := turns(claim.RolePlanner); !slices.EqualFunc(got, c.planner, time.Time.Equal) {
				t.Errorf("ACME-3's planner started turns at %s, want at %s", describeTimes(got), describeTimes(c.planner))
			}
		})
	}
}

// conflictWindow is a window recorded under legion-v20.42.5, whose conflicting head moves back to
// implementing, with one conflict loop, and the merges of its base branch. ACME-1, a root, works
// 15 minutes in each phase from 09:00 to merging at 10:45; main took a merge at 10:30, inside its
// first stretch (09:45 to 11:00), which ends in a conflict at 11:00; the loop's implementing runs
// 30 minutes, and ACME-1 is back in merging at 12:30 and merges at 12:45, its second stretch
// (11:30 to 12:45) holding no merge. Its stretches separate: the chance fitted at M = 1 is 1 and at
// M = 0 is 0.
func conflictWindow(t *testing.T) Window {
	b := newWindowBuilder(t, clock(9, 0), clock(13, 0))
	b.window.Parts[0].Rule = "legion-v20.42.5"
	b.capped(2)
	b.boot(clock(9, 0))
	b.file(clock(9, 0), "ACME-1", filing{Title: "Export the widget list", Status: "todo", Rank: "a0", Labels: handedToLegion})
	b.rootThroughReview(clock(9, 0), "ACME-1")
	b.segmentEnds(clock(10, 15), "ACME-1", phase.Reviewing, phase.Accepting, workflow.TriggerReviewApproved)
	b.segmentEnds(clock(10, 30), "ACME-1", phase.Accepting, phase.Retro, workflow.TriggerSignOff)
	b.segmentEnds(clock(10, 45), "ACME-1", phase.Retro, phase.Merging, workflow.TriggerRetroCompleted)
	b.segmentEnds(clock(11, 0), "ACME-1", phase.Merging, phase.Implementing, workflow.TriggerConflict)
	b.segmentEnds(clock(11, 30), "ACME-1", phase.Implementing, phase.Testing, workflow.TriggerImplementationReady)
	b.segmentEnds(clock(11, 45), "ACME-1", phase.Testing, phase.Reviewing, workflow.TriggerTesterPassed)
	b.segmentEnds(clock(12, 0), "ACME-1", phase.Reviewing, phase.Accepting, workflow.TriggerReviewApproved)
	b.segmentEnds(clock(12, 15), "ACME-1", phase.Accepting, phase.Retro, workflow.TriggerSignOff)
	b.segmentEnds(clock(12, 30), "ACME-1", phase.Retro, phase.Merging, workflow.TriggerRetroCompleted)
	b.segmentEnds(clock(12, 45), "ACME-1", phase.Merging, phase.Done, workflow.TriggerPullRequestMerged)
	b.leaves(clock(12, 45), "ACME-1", record.DoneStatus)
	window := b.build()
	window.Merges = map[string][]Merge{builtRepository: {
		{SHA: "5d2c8a1e9b7f4036c1e2d3a4b5c6d7e8f9a0b1c2", At: clock(10, 30), Branch: "main"},
		{SHA: "8b1e4f7a2c5d9036e1f2a3b4c5d6e7f8a9b0c1d2", At: clock(12, 45), Branch: "main"},
	}}
	return window
}

// Merges sets the merges term of the conflict draw (assumption 3): under fitted the stretch draws
// at the chance its merges give, under zero at the base chance alone. The test replays
// conflictWindow under main. On the main branch ACME-1's first stretch draws a conflict at 11:00
// (its M is 1), the engine asks the merge forward, the merge-forward segment runs the loop's 30
// minutes of implementing and lands at 11:30, and ACME-1's last merging segment runs from then: it
// merges at 11:45, with one round of conflict of half an hour. On main+merges=zero the stretch
// draws at the base chance, below 10⁻⁶, does not conflict, and ACME-1's last merging segment runs
// from 11:00: it merges at 11:15, with no merge forward and no round of conflict.
func TestTheMergesKnobSetsTheTermToZero(t *testing.T) {
	window := conflictWindow(t)
	for _, c := range []struct {
		branch string
		done   time.Time
		asked  []time.Time
		landed []time.Time
	}{
		{branch: "main", done: clock(11, 45), asked: []time.Time{clock(11, 0)}, landed: []time.Time{clock(11, 30)}},
		{branch: "main+merges=zero", done: clock(11, 15)},
	} {
		t.Run(c.branch, func(t *testing.T) {
			result, log := knobRun(t, window, mainRule(t), c.branch)
			if result.Conflicts.TermZero != (c.branch != "main") {
				t.Errorf("the run's conflict model %s, on the branch %s", result.Conflicts, c.branch)
			}
			if done := result.Scorecard.DoneAt["ACME-1"]; !done.Equal(c.done) {
				t.Errorf("ACME-1 is done at %s, want %s", stamp(done), stamp(c.done))
			}
			asked := linesAt(log, func(line replayLine) bool { return line.Msg == mergeForwardAskedLine && line.Issue == "ACME-1" })
			landed := linesAt(log, func(line replayLine) bool { return line.Msg == mergeForwardLandedLine && line.Issue == "ACME-1" })
			if !slices.EqualFunc(asked, c.asked, time.Time.Equal) || !slices.EqualFunc(landed, c.landed, time.Time.Equal) {
				t.Errorf("the merge forward of ACME-1 was asked at %s and landed at %s, want %s and %s", describeTimes(asked), describeTimes(landed),
					describeTimes(c.asked), describeTimes(c.landed))
			}
			loop, counted := result.Scorecard.Loops[string(CauseConflict)]
			switch {
			case len(c.asked) > 0 && (loop.Rounds != 1 || loop.Hours != 0.5):
				t.Errorf("the replay counts %+v of conflict, want one round of half an hour, the merge-forward segment", loop)
			case len(c.asked) == 0 && counted:
				t.Errorf("the replay counts %+v of conflict, and no stretch conflicted", loop)
			}
		})
	}
}
