package replay

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/projection"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/testpg/testdb"
	"github.com/sjawhar/legion/daemon/internal/workflow"
)

// handAt is the moment clock (15:04) of the day every hand-worked timeline of this file runs on.
func handAt(t *testing.T, clock string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, "2001-03-05T"+clock+":00Z")
	if err != nil {
		t.Fatalf("read the moment %s: %v", clock, err)
	}
	return at
}

// handStint is the state an issue of a hand-worked timeline stands in from clock on.
func handStint(t *testing.T, clock string, state IssueState) Stint {
	t.Helper()
	return Stint{At: handAt(t, clock), IssueState: state}
}

// assertOccupancy fails where got is not want, floats to within fixtureTolerance.
func assertOccupancy(t *testing.T, name string, got, want Occupancy) {
	t.Helper()
	if math.Abs(got.Average-want.Average) > fixtureTolerance || got.Max != want.Max || math.Abs(got.Hours-want.Hours) > fixtureTolerance {
		t.Errorf("%s = %+v, want %+v", name, got, want)
	}
}

// assertNear fails where got stands further than fixtureTolerance from want.
func assertNear(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > fixtureTolerance {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}

// stateAt is the state c stands in at at, its stint at at among them, and false where it stands in
// none yet.
func stateAt(c *Course, at time.Time) (IssueState, bool) {
	i := sort.Search(len(c.States), func(i int) bool { return c.States[i].At.After(at) }) - 1
	if i < 0 {
		return IssueState{}, false
	}
	return c.States[i].IssueState, true
}

// The two readings of started work that waits for a slot stand side by side (spec R4; the windows'
// README, "The measures"): reading (b) counts each member of the line's returned part that holds
// no slot, reading (a) the same members while admission.blocked does not name them, and each
// gives its average over the span, its largest count and its hours. Over six hours: ACME-6 waits
// only for a slot from 09:00 to 10:00; ACME-1 is given back for a wait at 10:00, blocked to 11:00,
// and waits only for a slot to 12:00; ACME-2 waits only for a slot from 11:30 to 13:00; ACME-5 is
// an answered root in the returned part that holds its slot, which neither reading counts. ACME-6
// leaves the line at the instant ACME-1 enters it, so the two are never counted together and the
// largest count is the two of 11:30 to 12:00. Reading (b): 1 + 2 + 1.5 = 4.5 hours, an average of
// 0.75, at most 2. Reading (a): 1 + 1 + 1.5 = 3.5 hours, an average of 0.58333…, at most 2. The
// issues past their review stand apart: ACME-3 with its slot from 13:00 to 14:00, ACME-4 with none
// from 13:00 to 15:00.
func TestStartedWorkWithoutASlotTwoReadings(t *testing.T) {
	returned := func(blocked bool) IssueState {
		return IssueState{InFlight: true, Part: record.PartReturned, Blocked: blocked, Waiting: !blocked}
	}
	holding := IssueState{InFlight: true, Holds: true}
	tl := Timeline{From: handAt(t, "09:00"), To: handAt(t, "15:00"), Issues: map[string]*Course{
		"ACME-6": {States: []Stint{handStint(t, "09:00", returned(false)), handStint(t, "10:00", holding)}},
		"ACME-1": {States: []Stint{handStint(t, "09:00", holding), handStint(t, "10:00", returned(true)),
			handStint(t, "11:00", returned(false)), handStint(t, "12:00", holding)}},
		"ACME-2": {States: []Stint{handStint(t, "11:30", returned(false)), handStint(t, "13:00", holding)}},
		"ACME-5": {States: []Stint{handStint(t, "09:00", IssueState{InFlight: true, Holds: true, Part: record.PartReturned})}},
		"ACME-3": {States: []Stint{handStint(t, "13:00", IssueState{InFlight: true, Holds: true, PastReview: true}),
			handStint(t, "14:00", IssueState{})}},
		"ACME-4": {States: []Stint{handStint(t, "13:00", IssueState{InFlight: true, PastReview: true})}},
	}}
	card := Measure(tl)
	assertOccupancy(t, "startedWithoutSlot", card.StartedWithoutSlot, Occupancy{Average: 4.5 / 6, Max: 2, Hours: 4.5})
	assertOccupancy(t, "waitOnlyForSlot", card.WaitOnlyForSlot, Occupancy{Average: 3.5 / 6, Max: 2, Hours: 3.5})
	assertOccupancy(t, "pastReviewHolding", card.PastReviewHolding, Occupancy{Average: 1.0 / 6, Max: 1, Hours: 1})
	assertOccupancy(t, "pastReviewNotHolding", card.PastReviewNotHolding, Occupancy{Average: 2.0 / 6, Max: 1, Hours: 2})
}

// Priority taken is the time-average share of the pairs of a slot holder and an issue that waits
// for a slot in which the waiting issue ranks above the holder (spec R4), by record.RankLess over
// the tree roots' ranks and then the issues' own, and the waiting issues are the plan's: members of
// either part of the line that hold no slot and that no wait, admission.blocked or admission.held
// holds. Over four hours, with ACME-1 (a2) holding throughout and ACME-2 (a0) holding but from
// 11:00 to 12:00: ACME-3, a queued root at a1, waits from 09:00 to 11:00; ACME-4, a first-phase
// child of ACME-2's tree at z9, waits from 10:00 on; ACME-7, given back and blocked, and ACME-8, a
// queued root its link holds back at 0, wait for no slot and count in no pair.
//
//	09:00 to 10:00: ACME-3 above ACME-1 of 2 pairs, 1/2.
//	10:00 to 11:00: ACME-3 above ACME-1, and ACME-4 above ACME-1 by its root's a0, of 4; ACME-4
//	stands below ACME-2 in ACME-2's own tree: 2/4.
//	11:00 to 12:00: ACME-4 above ACME-1, of 1: 1.
//	12:00 to 13:00: ACME-4 above ACME-1 of 2: 1/2.
//
// The average is (0.5 + 0.5 + 1 + 0.5)/4 = 0.625.
func TestPriorityInversionShare(t *testing.T) {
	holder := func(key, rank string) IssueState {
		return IssueState{Tree: key, Rank: rank, RootRank: rank, InFlight: true, Holds: true}
	}
	waiting := func(tree, rank, rootRank string, part record.LinePart) IssueState {
		return IssueState{Tree: tree, Rank: rank, RootRank: rootRank, Part: part, Waiting: true}
	}
	tl := Timeline{From: handAt(t, "09:00"), To: handAt(t, "13:00"), Issues: map[string]*Course{
		"ACME-1": {States: []Stint{handStint(t, "09:00", holder("ACME-1", "a2"))}},
		"ACME-2": {States: []Stint{handStint(t, "09:00", holder("ACME-2", "a0")),
			handStint(t, "11:00", IssueState{InFlight: true, PastReview: true}),
			handStint(t, "12:00", holder("ACME-2", "a0"))}},
		"ACME-3": {States: []Stint{handStint(t, "09:00", waiting("ACME-3", "a1", "a1", record.PartStarting)),
			handStint(t, "11:00", IssueState{})}},
		"ACME-4": {States: []Stint{handStint(t, "10:00", waiting("ACME-2", "z9", "a0", record.PartStarting))}},
		"ACME-7": {States: []Stint{handStint(t, "09:00", IssueState{InFlight: true, Part: record.PartReturned, Blocked: true})}},
		"ACME-8": {States: []Stint{handStint(t, "09:00", IssueState{Part: record.PartStarting, Blocked: true})}},
	}}
	assertNear(t, "priorityTaken", Measure(tl).PriorityTaken, 0.625)
}

// Each send-back loop is counted by its cause (causes.go), one round for each move back inside the
// span, its hours from the move back to the issue's next entry into the phase the move left, or to
// the span's end; and each wait on another issue by what ended it, its hours inside the span, a
// wait that stands at the end counted as open (spec R4). Over six hours: ACME-1's review asks for
// changes at 10:00 and it is back in reviewing at 11:30 (1.5 hours), and its architect sends it
// back from accepting at 13:00 and it is not back by 15:00 (2 hours); ACME-2's test fails at 12:00
// and it is back in testing at 12:20 (1/3 hour); ACME-3 is sent back from accepting at 09:30 and is
// back at 10:30 (1 hour); ACME-4's move back at 08:00, before the span, is no round of it. The
// waits: ACME-1's link wait 09:00 to 10:00, ended by its blocker's done; ACME-3's from 08:00, its
// link taken off at 09:30 (half an hour in the span); ACME-2's children wait from 14:00, open at
// 15:00 (1 hour); and ACME-4's from 10:00 to 16:00, which still stands at 15:00 and so is open
// whatever ended it later (5 hours).
func TestLoopHoursByCause(t *testing.T) {
	move := func(clock string, from, to phase.Phase, trigger workflow.TriggerKind) CourseMove {
		return moveOf(handAt(t, clock), from, to, trigger, "")
	}
	tl := Timeline{From: handAt(t, "09:00"), To: handAt(t, "15:00"), Issues: map[string]*Course{
		"ACME-1": {
			Moves: []CourseMove{
				move("09:00", phase.Testing, phase.Reviewing, workflow.TriggerTesterPassed),
				move("10:00", phase.Reviewing, phase.Implementing, workflow.TriggerReviewRejected),
				move("11:00", phase.Implementing, phase.Testing, workflow.TriggerImplementationReady),
				move("11:30", phase.Testing, phase.Reviewing, workflow.TriggerTesterPassed),
				move("12:00", phase.Reviewing, phase.Accepting, workflow.TriggerReviewApproved),
				move("13:00", phase.Accepting, phase.Implementing, workflow.TriggerBackward),
			},
			Waits: []CourseWait{{Start: handAt(t, "09:00"), End: handAt(t, "10:00"), EndedBy: WaitBlockerDone}},
		},
		"ACME-2": {
			Moves: []CourseMove{
				move("12:00", phase.Testing, phase.Implementing, workflow.TriggerTesterFailed),
				move("12:20", phase.Implementing, phase.Testing, workflow.TriggerImplementationReady),
			},
			Waits: []CourseWait{{Start: handAt(t, "14:00"), EndedBy: WaitOpen}},
		},
		"ACME-3": {
			Moves: []CourseMove{
				move("09:30", phase.Accepting, phase.Implementing, workflow.TriggerBackward),
				move("10:00", phase.Implementing, phase.Testing, workflow.TriggerImplementationReady),
				move("10:15", phase.Testing, phase.Reviewing, workflow.TriggerTesterPassed),
				move("10:30", phase.Reviewing, phase.Accepting, workflow.TriggerReviewApproved),
			},
			Waits: []CourseWait{{Start: handAt(t, "08:00"), End: handAt(t, "09:30"), EndedBy: WaitLinkRemoved}},
		},
		"ACME-4": {
			Moves: []CourseMove{
				move("08:00", phase.Reviewing, phase.Implementing, workflow.TriggerReviewRejected),
				move("08:30", phase.Implementing, phase.Testing, workflow.TriggerImplementationReady),
			},
			Waits: []CourseWait{{Start: handAt(t, "10:00"), End: handAt(t, "16:00"), EndedBy: WaitBlockerDone}},
		},
	}}
	card := Measure(tl)
	wantLoops := map[string]LoopHours{
		string(CauseReviewChanges): {Rounds: 1, Hours: 1.5},
		string(CauseTestFailed):    {Rounds: 1, Hours: 1.0 / 3},
		string(CauseSendBack):      {Rounds: 2, Hours: 3},
	}
	wantWaits := map[string]WaitHours{
		string(WaitBlockerDone): {Count: 1, Hours: 1},
		string(WaitLinkRemoved): {Count: 1, Hours: 0.5},
		string(WaitOpen):        {Count: 2, Hours: 6},
	}
	for _, diff := range scorecardDiffs(Scorecard{Loops: card.Loops, Waits: card.Waits}, Scorecard{Loops: wantLoops, Waits: wantWaits}) {
		t.Error(diff)
	}
}

// Slot-hours are read over each slot from the pass that gives it to its release, with a worker of
// the holder, of any role, in a turn or with none (spec R4); a turn of an issue that holds no slot
// is in neither. Over three hours: ACME-1 holds a slot from 09:00 to 11:00, in a turn to 10:00 and
// in none after; ACME-2 holds one from 10:00 on, in a turn throughout; ACME-3 is in a turn from
// 09:00 with no slot. In a turn: 1 + 2 = 3 hours; in none: 1 hour. The issues in flight, ACME-1
// and ACME-3 for three hours and ACME-2 for two: 8 hours, an average of 8/3, at most 3.
func TestSlotHoursByTurn(t *testing.T) {
	tl := Timeline{From: handAt(t, "09:00"), To: handAt(t, "12:00"), Issues: map[string]*Course{
		"ACME-1": {States: []Stint{handStint(t, "09:00", IssueState{InFlight: true, Holds: true, InTurn: true}),
			handStint(t, "10:00", IssueState{InFlight: true, Holds: true}), handStint(t, "11:00", IssueState{InFlight: true, InTurn: true})}},
		"ACME-2": {States: []Stint{handStint(t, "10:00", IssueState{InFlight: true, Holds: true, InTurn: true})}},
		"ACME-3": {States: []Stint{handStint(t, "09:00", IssueState{InFlight: true, InTurn: true})}},
	}}
	card := Measure(tl)
	assertNear(t, "slotHoursInTurn", card.SlotHoursInTurn, 3)
	assertNear(t, "slotHoursNoTurn", card.SlotHoursNoTurn, 1)
	assertOccupancy(t, "inFlight", card.InFlight, Occupancy{Average: 8.0 / 3, Max: 3, Hours: 8})
}

// A window's own scorecard needs no replay (spec R4): Score reads every measure from the record
// alone, by the code a replay is measured by. Each fixture window recorded under main whose
// expect.json names main on the main branch is one whose replay under main keeps the record's
// course, so its record's scorecard is the one its README works out by hand, but for what a replay
// alone names: the source is the record, and a record runs under no rule, branch, seed or
// slowdown, rests on none of a replay's assumptions and takes no never-started scenario.
func TestScorecardOfTheRecord(t *testing.T) {
	scored := 0
	for _, name := range fixtureWindowNames(t) {
		dir := filepath.Join(fixtureWindowsDir, name)
		window, err := ReadWindow(dir)
		if err != nil {
			t.Fatalf("window %s: %v", name, err)
		}
		expect, err := readFixtureExpect(dir)
		if err != nil {
			t.Fatalf("window %s: %v", name, err)
		}
		want, named := expect.Rules[record.MainRule.Name]
		if recordedRule(window) != record.MainRule.Name || !named || expect.Branch != "" {
			continue
		}
		scored++
		t.Run(name, func(t *testing.T) {
			got, err := Score(window)
			if err != nil {
				t.Fatalf("score the record of %s: %v", name, err)
			}
			want.Source, want.Rule, want.Branch, want.Seed, want.Slowdown = "record", "", "", 0, 0
			want.Assumptions, want.Scenario = []int{}, ""
			for _, diff := range scorecardDiffs(got, want) {
				t.Errorf("the record of %s: %s", name, diff)
			}
		})
	}
	if scored == 0 {
		t.Fatalf("no fixture window is recorded under main with a scorecard under main: nothing was scored")
	}
}

// The replay measures from the same states `legion state` reads (spec R6): at ten moments of a
// fixture's replay, spread over the run, each a moment every act of the instant has run at, the
// slot holders in each state of the replay's snapshot timeline are the holders projection.Project
// counts in each over the replay's own database at that moment, read as the daemon reads its
// state: its claims, the claims nothing will wake (workflow.Stranded) and the holders' claims as
// the tick reads them.
func TestReplayStatesAreTheProjectionsStates(t *testing.T) {
	dsn := os.Getenv("LEGION_TEST_PG_DSN")
	if dsn == "" {
		t.Skip(testdb.SkipMessage)
	}
	const name = "two-waiters-one-blocker"
	window, err := ReadWindow(filepath.Join(fixtureWindowsDir, name))
	if err != nil {
		t.Fatalf("window %s: %v", name, err)
	}
	token, err := claim.ProjectToken(window.Project)
	if err != nil {
		t.Fatalf("the claim token of %s: %v", window.Project, err)
	}
	type moment struct {
		at     time.Time
		counts map[record.HolderState]int
	}
	var projected []moment
	rule := record.MainRule
	look := func(ctx context.Context, st *store.Store, at time.Time) error {
		return st.Tx(ctx, func(tx pgx.Tx) error {
			records := record.NewStore()
			claims, err := st.ClaimsTx(ctx, tx)
			if err != nil {
				return err
			}
			holderClaims, err := records.HolderClaims(ctx, tx, token)
			if err != nil {
				return err
			}
			stranded, err := workflow.Stranded(ctx, tx, records, window.Project, window.GateSteps, claims, nil, at)
			if err != nil {
				return err
			}
			state, err := projection.Project(ctx, projection.Inputs{Tx: tx, Store: records, Project: window.Project,
				Steps: window.GateSteps, Claims: claims, Stranded: stranded, Now: at, SlotRule: rule, Cap: capAt(window, at),
				HolderClaims: holderClaims})
			if err != nil {
				return err
			}
			slots := state.Admission.Slots
			if slots == nil {
				return fmt.Errorf("the state at %s carries no slots reading", stamp(at))
			}
			projected = append(projected, moment{at: at, counts: map[record.HolderState]int{record.HolderWorking: slots.Working.Count,
				record.HolderStopped: slots.Stopped.Count, record.HolderPerson: slots.Person.Count, record.HolderIssue: slots.Issue.Count,
				record.HolderIdle: slots.Idle.Count}})
			return nil
		})
	}
	result, err := Run(context.Background(), RunConfig{Window: window, Rule: rule, Branch: MainBranch, Seed: 1, Slowdown: 1, PGDSN: dsn,
		Out: t.TempDir(), atSettled: look})
	if err != nil {
		t.Fatalf("replay %s: %v", name, err)
	}
	const moments = 10
	if len(projected) < moments {
		t.Fatalf("the replay of %s settled at %d moments, want at least %d", name, len(projected), moments)
	}
	for i := range moments {
		at := projected[i*len(projected)/moments]
		got := map[record.HolderState]int{}
		for _, state := range record.HolderStates {
			got[state] = 0
		}
		for _, c := range result.snapshot.Issues {
			if state, ok := stateAt(c, at.at); ok && state.Holds {
				got[state.Holder]++
			}
		}
		if fmt.Sprint(got) != fmt.Sprint(at.counts) {
			t.Errorf("at %s the replay counts its holders by state %v, and projection.Project over its database %v", stamp(at.at), got, at.counts)
		}
	}
}

// The two timelines of a replay agree (design 10.1; the design review's Q5): on every fixture
// window, on a window shaped as a deployment's (deploymentShapedWindow: issues placed at its
// start, holds read from failed claims, children filed in it, a stop and a boot), and on a window
// with a drop from the merge queue (queueDropWindow), each measure of the line timeline, which
// BuildTrace reads from the replay's own log through the window's line kinds as it reads a record,
// stands within a third of R5's gap for that measure of the snapshot timeline, which the admission
// passes' own views give: 10% of the issues done and of the slots given each day, 0.05 of the
// share of priority taken, 30% of every other measure (the median wait for a slot and the hours of
// reading (a) among them), and, for each issue's first slot and its done, 60 minutes at the
// median. A gap of the line timeline wider than that would make a record's scorecard, which only
// the lines can give, err by more than a replay may. The deployment-shaped window holds a root that
// waits for its children again after its last child closed, with no line of the second wait, since
// the daemon tells a generation's wait once. In the queue-drop window both timelines name the
// merger's send-back one round of queue_drop and none of send_back: the replay writes the line of
// the pull request leaving the merge queue that the record shows before the move, as the trace of
// its own log needs to name the move's cause.
func TestSnapshotAndLineTimelinesAgree(t *testing.T) {
	dsn := os.Getenv("LEGION_TEST_PG_DSN")
	if dsn == "" {
		t.Skip(testdb.SkipMessage)
	}
	for _, name := range fixtureWindowNames(t) {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(fixtureWindowsDir, name)
			window, err := ReadWindow(dir)
			if err != nil {
				t.Fatalf("window %s: %v", name, err)
			}
			expect, err := readFixtureExpect(dir)
			if err != nil {
				t.Fatalf("window %s: %v", name, err)
			}
			runs, err := expect.runs()
			if err != nil {
				t.Fatalf("window %s: %v", name, err)
			}
			for _, run := range runs {
				if run.rule != record.MainRule.Name {
					continue
				}
				t.Run(run.name, func(t *testing.T) {
					slowdown := run.want.Slowdown
					if slowdown == 0 {
						slowdown = 1
					}
					timelinesAgree(t, dsn, window, run.branch, run.want.Seed, slowdown)
				})
			}
		})
	}
	t.Run("deployment-shaped", func(t *testing.T) {
		timelinesAgree(t, dsn, deploymentShapedWindow(t), MainBranch, 1, 1)
	})
	t.Run("queue-drop", func(t *testing.T) {
		result := timelinesAgree(t, dsn, queueDropWindow(t), MainBranch, 1, 1)
		for _, timeline := range []struct {
			name string
			card Scorecard
		}{{"snapshot", result.Scorecard}, {"line", result.LineScorecard}} {
			drop, sent := timeline.card.Loops[string(CauseQueueDrop)], timeline.card.Loops[string(CauseSendBack)]
			if drop.Rounds != 1 || drop.Hours == 0 || sent.Rounds != 0 {
				t.Errorf("the %s timeline counts %+v of queue_drop and %+v of send_back, want one round of queue_drop and none of send_back",
					timeline.name, drop, sent)
			}
		}
	})
}

// queueDropWindow is a window from 09:00 to 15:00 with a drop from the merge queue. ACME-1, a root,
// is promoted at 09:05 and works 15 to 30 minutes in each phase to merging at 11:40. At 12:00 its
// pull request leaves the merge queue, and at 12:10 its merger sends it back to implementing. It
// works through each phase again to merging at 14:00, and merges at 14:20.
func queueDropWindow(t *testing.T) Window {
	b := newWindowBuilder(t, clock(9, 0), clock(15, 0))
	b.boot(clock(9, 0))
	b.file(clock(9, 5), "ACME-1", filing{Title: "Export the widget list", Status: "todo", Rank: "a0", Labels: handedToLegion})
	b.rootPromoted(clock(9, 5), "ACME-1")
	b.rootDesigned(clock(9, 20), "ACME-1")
	b.segmentEnds(clock(9, 50), "ACME-1", phase.Planning, phase.Implementing, workflow.TriggerPlannerCompleted)
	toMerging := func(implemented time.Time) {
		b.segmentEnds(implemented, "ACME-1", phase.Implementing, phase.Testing, workflow.TriggerImplementationReady)
		b.segmentEnds(implemented.Add(20*time.Minute), "ACME-1", phase.Testing, phase.Reviewing, workflow.TriggerTesterPassed)
		b.segmentEnds(implemented.Add(40*time.Minute), "ACME-1", phase.Reviewing, phase.Accepting, workflow.TriggerReviewApproved)
		b.segmentEnds(implemented.Add(60*time.Minute), "ACME-1", phase.Accepting, phase.Retro, workflow.TriggerSignOff)
		b.segmentEnds(implemented.Add(80*time.Minute), "ACME-1", phase.Retro, phase.Merging, workflow.TriggerRetroCompleted)
	}
	toMerging(clock(10, 20))
	b.info(clock(12, 0), "workflow: a pull request in merging left the merge queue; the daemon reads it from GitHub once", "issue", "ACME-1",
		"pullRequest", 41, "head", "3c9e1f0a7b2d4e5f6a7b8c9d0e1f2a3b4c5d6e7f")
	b.segmentEnds(clock(12, 10), "ACME-1", phase.Merging, phase.Implementing, workflow.TriggerBackward)
	toMerging(clock(12, 40))
	b.segmentEnds(clock(14, 20), "ACME-1", phase.Merging, phase.Done, workflow.TriggerPullRequestMerged)
	b.leaves(clock(14, 20), "ACME-1", record.DoneStatus)
	return b.build()
}

// timelinesAgree replays window under main on branch, with seed and slowdown, and fails for each
// measure in which the line timeline stands further from the snapshot timeline than a third of
// R5's gap for it, and where the median gap of each issue's first slot and done is above a third
// of R5's 60 minutes (TestSnapshotAndLineTimelinesAgree). It answers the replay's result.
func timelinesAgree(t *testing.T, dsn string, window Window, branch Branch, seed int64, slowdown float64) RunResult {
	t.Helper()
	result, err := Run(context.Background(), RunConfig{Window: window, Rule: record.MainRule, Branch: branch, Seed: seed,
		Slowdown: slowdown, PGDSN: dsn, Out: t.TempDir()})
	if err != nil {
		t.Fatalf("the replay failed: %v", err)
	}
	var timeErrors []float64
	for _, gap := range result.Gaps {
		if strings.HasPrefix(gap.Measure, "firstSlot[") || strings.HasPrefix(gap.Measure, "doneAt[") {
			timeErrors = append(timeErrors, math.Abs(gap.Gap()))
			continue
		}
		if allowed := validationGap(gap.Measure, gap.Snapshot) / 3; math.Abs(gap.Gap()) > allowed+fixtureTolerance {
			t.Errorf("%s is %v in the snapshot timeline and %v in the line timeline, a gap of %v, above a third of R5's gap, %v",
				gap.Measure, gap.Snapshot, gap.Line, gap.Gap(), allowed)
		}
	}
	if errorMinutes := median(timeErrors); errorMinutes > 60.0/3+fixtureTolerance {
		t.Errorf("the median gap of each issue's first slot and done between the two timelines is %v minutes, above a third of R5's 60",
			errorMinutes)
	}
	return result
}

// validationGap is the gap R5 allows the replay of a measure whose recorded value is recorded: 10%
// of the issues done and of the slots given out on a day, 0.05 of the share of priority taken, and
// 30% of every other measure, the median wait for a slot and the hours of started work that waits
// only for a slot among them.
func validationGap(measure string, recorded float64) float64 {
	switch {
	case measure == "done" || strings.HasPrefix(measure, "slotsGivenByDay["):
		return 0.10 * math.Abs(recorded)
	case measure == "priorityTaken":
		return 0.05
	default:
		return 0.30 * math.Abs(recorded)
	}
}
