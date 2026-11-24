package replay

import (
	"context"
	"encoding/json"
	"maps"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/workflow"
)

// The priority a rule reads is the one the window's Dispatch events set, as the moment of the pass
// sees it: the last event at or before it that carries the field, an event that sets none setting
// none, and an issue no event has set a priority of by then having none. ACME-1 is filed a P2 at
// 09:00 and raised to P0 at 10:00; ACME-2's events carry no priority field; ACME-3 is filed a P1
// and set to none at 10:00.
func TestThePriorityBookReadsEachIssuesPriorityAtThePass(t *testing.T) {
	event := func(at time.Time, payload map[string]any) json.RawMessage {
		raw, err := json.Marshal(map[string]any{"created_at": at, "payload": payload})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	w := Window{Events: map[string][]json.RawMessage{
		"ACME-1": {event(clock(10, 0), map[string]any{"priority": 0}), event(clock(9, 0), map[string]any{"priority": 2})},
		"ACME-2": {event(clock(9, 0), map[string]any{"status": "todo"})},
		"ACME-3": {event(clock(9, 0), map[string]any{"priority": 1}), event(clock(10, 0), map[string]any{"priority": nil})},
	}}
	book, err := priorityBookOf(w)
	if err != nil {
		t.Fatalf("read the priorities: %v", err)
	}
	issues := []record.Issue{{Key: "ACME-1"}, {Key: "ACME-2"}, {Key: "ACME-3"}, {Key: "ACME-4"}}
	for _, c := range []struct {
		at   time.Time
		want map[string]int
	}{
		{clock(8, 59), map[string]int{}},
		{clock(9, 30), map[string]int{"ACME-1": 2, "ACME-3": 1}},
		{clock(10, 0), map[string]int{"ACME-1": 0}},
	} {
		if got := book.at(c.at, issues); !maps.Equal(got, c.want) {
			t.Errorf("at %s the priorities are %v, want %v", stamp(c.at), got, c.want)
		}
	}
}

// recordPriorityQueue writes into b a cap of 1 that ACME-1 holds from 09:00 until its accepting at
// 10:15, and two trees waiting behind it: ACME-2, ranked first and filed as given by second, and
// ACME-3, ranked second and filed as given by third. The record, under the daemon's own rule,
// gives the slot to ACME-2 at 10:15.
func recordPriorityQueue(b *windowBuilder, second, third filing) {
	b.t.Helper()
	b.capped(1)
	b.gated(record.DesignStepSpec)
	b.boot(clock(9, 0))
	b.file(clock(9, 0), "ACME-1", filing{Title: "Export the widget list", Status: "todo", Rank: "a0", Labels: handedToLegion})
	b.rootPromoted(clock(9, 0), "ACME-1")
	b.file(clock(9, 2), "ACME-2", second)
	b.file(clock(9, 4), "ACME-3", third)
	b.specWritten(clock(9, 5), "ACME-1")
	b.approves(clock(9, 10), "ACME-1", "")
	b.rootDesigned(clock(9, 15), "ACME-1")
	b.segmentEnds(clock(9, 30), "ACME-1", phase.Planning, phase.Implementing, workflow.TriggerPlannerCompleted)
	b.segmentEnds(clock(9, 45), "ACME-1", phase.Implementing, phase.Testing, workflow.TriggerImplementationReady)
	b.segmentEnds(clock(10, 0), "ACME-1", phase.Testing, phase.Reviewing, workflow.TriggerTesterPassed)
	b.segmentEnds(clock(10, 15), "ACME-1", phase.Reviewing, phase.Accepting, workflow.TriggerReviewApproved)
	b.rootPromoted(clock(10, 15), "ACME-2")
	b.specWrittenBy(clock(10, 25), "ACME-2", recordedSession("ACME-2", claim.RoleArchitect, "00000000-0000-4000-8000-0000000000d6"))
	b.turnEnds(clock(10, 30), "ACME-2", claim.RoleArchitect)
	b.registered(clock(10, 30), "ACME-2", false)
	b.segmentEnds(clock(10, 30), "ACME-1", phase.Accepting, phase.Retro, workflow.TriggerSignOff)
	b.segmentEnds(clock(10, 45), "ACME-1", phase.Retro, phase.Merging, workflow.TriggerRetroCompleted)
	b.turnEnds(clock(11, 0), "ACME-1", claim.RoleMerger)
	b.moved(clock(11, 0), "ACME-1", phase.Merging, phase.Done, workflow.TriggerPullRequestMerged)
	b.leaves(clock(11, 0), "ACME-1", record.DoneStatus)
}

// A rule that orders the line by Dispatch priority first gives a freed slot to the P0 behind a tree
// the board ranks first, reading the priority from the window's own events. Replayed under main,
// the record's rule, ACME-2, ranked first, takes ACME-1's slot at 10:15 as recorded and ACME-3 none;
// under priority-first ACME-3, the P0, takes it at 10:15 and ACME-2, the P2, none, since ACME-3,
// which the record never started, has no recorded work and holds the slot to the window's end.
func TestAPriorityRuleGivesTheFreedSlotToTheP0(t *testing.T) {
	b := newWindowBuilder(t, clock(9, 0), clock(13, 0))
	recordPriorityQueue(b,
		filing{Title: "Count the widgets per shelf", Status: "todo", Rank: "a1", Labels: handedToLegion, Priority: new(2)},
		filing{Title: "Fix the shelf screen", Status: "todo", Rank: "a2", Labels: handedToLegion, Priority: new(0)})
	window := b.build()
	for _, c := range []struct {
		rule          string
		first, second string
	}{{"main", "ACME-2", "ACME-3"}, {"priority-first", "ACME-3", "ACME-2"}} {
		t.Run(c.rule, func(t *testing.T) {
			result, err := Run(context.Background(), RunConfig{Window: window, Rule: replayedRule(t, c.rule), Branch: MainBranch, Seed: 1,
				Slowdown: 1, PGDSN: replayDSN(t), Out: t.TempDir()})
			if err != nil {
				t.Fatalf("replay the window under %s: %v", c.rule, err)
			}
			if first, ok := result.Scorecard.FirstSlot[c.first]; !ok || !first.Truncate(time.Minute).Equal(clock(10, 15)) {
				t.Errorf("%s's first slot is %s, want 10:15", c.first, stamp(first))
			}
			if second, ok := result.Scorecard.FirstSlot[c.second]; ok {
				t.Errorf("%s takes a first slot at %s, want none: the one slot is %s's to the window's end", c.second, stamp(second), c.first)
			}
		})
	}
}

// A rule that gives an idle waiting holder's slot back frees the slot of a root whose architect
// asked a person to approve its design and has had no turn for an hour. ACME-1 takes the one slot
// at 09:00, its architect writes the design and asks for approval, and its turn ends at 09:10; the
// person approves at 12:00, and ACME-1 runs to its accepting at 13:15, where the record gives the
// slot to ACME-2. Replayed under main, ACME-2's first slot is 13:15, as recorded. Under
// idle-return-1h ACME-1 is idle an hour after its architect's last turn in the replay, and the first
// pass after that gives ACME-2 the slot: not before 10:10, the earliest ACME-1 can be idle, and
// before 12:00, the approval that sets ACME-1 to work again. The hourly tick and the moment the
// replay's own turn of the architect ends place that pass, so the test holds the interval and not
// a minute. ACME-1's slot was taken at 09:00, which its mark keeps (record.Waiting).
func TestAnIdleHolderGivesItsSlotBack(t *testing.T) {
	b := newWindowBuilder(t, clock(9, 0), clock(15, 0))
	b.capped(1)
	b.gated(record.DesignStepSpec)
	b.boot(clock(9, 0))
	b.file(clock(9, 0), "ACME-1", filing{Title: "Export the widget list", Status: "todo", Rank: "a0", Labels: handedToLegion})
	b.rootPromoted(clock(9, 0), "ACME-1")
	b.file(clock(9, 2), "ACME-2", filing{Title: "Count the widgets per shelf", Status: "todo", Rank: "a1", Labels: handedToLegion})
	b.specWritten(clock(9, 5), "ACME-1")
	b.asksApproval(clock(9, 6), "ACME-1", "00000000-0000-4000-8000-0000000000e1")
	b.turnEnds(clock(9, 10), "ACME-1", claim.RoleArchitect)
	b.registered(clock(9, 10), "ACME-1", false)
	b.approves(clock(12, 0), "ACME-1", "00000000-0000-4000-8000-0000000000e1")
	b.gateOpened(clock(12, 0), "ACME-1", intake.DispatchArtifactApproved, 1)
	b.rootFirstPhase(clock(12, 0), "ACME-1")
	b.segmentEnds(clock(12, 30), "ACME-1", phase.Planning, phase.Implementing, workflow.TriggerPlannerCompleted)
	b.segmentEnds(clock(12, 45), "ACME-1", phase.Implementing, phase.Testing, workflow.TriggerImplementationReady)
	b.segmentEnds(clock(13, 0), "ACME-1", phase.Testing, phase.Reviewing, workflow.TriggerTesterPassed)
	b.segmentEnds(clock(13, 15), "ACME-1", phase.Reviewing, phase.Accepting, workflow.TriggerReviewApproved)
	b.rootPromoted(clock(13, 15), "ACME-2")
	b.specWrittenBy(clock(13, 25), "ACME-2", recordedSession("ACME-2", claim.RoleArchitect, "00000000-0000-4000-8000-0000000000e6"))
	b.turnEnds(clock(13, 30), "ACME-2", claim.RoleArchitect)
	b.registered(clock(13, 30), "ACME-2", false)
	b.segmentEnds(clock(13, 30), "ACME-1", phase.Accepting, phase.Retro, workflow.TriggerSignOff)
	b.segmentEnds(clock(13, 45), "ACME-1", phase.Retro, phase.Merging, workflow.TriggerRetroCompleted)
	b.turnEnds(clock(14, 0), "ACME-1", claim.RoleMerger)
	b.moved(clock(14, 0), "ACME-1", phase.Merging, phase.Done, workflow.TriggerPullRequestMerged)
	b.leaves(clock(14, 0), "ACME-1", record.DoneStatus)
	window := b.build()
	for _, c := range []struct {
		rule      string
		from, end time.Time
	}{{"main", clock(13, 15), clock(13, 16)}, {"idle-return-1h", clock(10, 10), clock(12, 0)}} {
		t.Run(c.rule, func(t *testing.T) {
			result, err := Run(context.Background(), RunConfig{Window: window, Rule: replayedRule(t, c.rule), Branch: MainBranch, Seed: 1,
				Slowdown: 1, PGDSN: replayDSN(t), Out: t.TempDir()})
			if err != nil {
				t.Fatalf("replay the window under %s: %v", c.rule, err)
			}
			if first, ok := result.Scorecard.FirstSlot["ACME-2"]; !ok || first.Before(c.from) || !first.Before(c.end) {
				t.Errorf("ACME-2's first slot is %s, want at or after %s and before %s", stamp(first), stamp(c.from), stamp(c.end))
			}
		})
	}
}
