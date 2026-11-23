package replay

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// architectSession is the id of the session that runs the claim of sessionClaimWindow's root
// architect, as Dispatch names it.
const architectSession = "00000000-0000-4000-8000-0000000000c1"

// releasedRootLine is the line of admission's release of a root it reads no tree of as running and
// nothing as ever starting (admit's releaseUnslotted).
const releasedRootLine = "admission: released a root no tree of which runs and nothing would ever start"

// sessionClaimWindow is rootAwaitingPersonWindow's record from 09:30 on, with the root architect's
// session named as Dispatch records it: an id of its own, under the title `Legion architect ·
// ACME-1`. That session claims ACME-1 in Dispatch at 09:06, writes version 1 of the spec at 09:17
// and asks a person to approve it at 09:18; at 09:20 its turn ends, the registration hands the
// document to the person, and the root waits on them, keeping its slot. The window starts at 09:30
// with the root waiting and its architect between turns. At 10:00 a person retitles ACME-1, an
// event that, as every issue event since the claim, carries the claim of the architect's session.
// At 10:20 the person approves the version: the gate opens, the root's architect resumes and
// ACME-2, its child, starts.
func sessionClaimWindow(t *testing.T) Window {
	b := newWindowBuilder(t, clock(9, 30), clock(15, 0))
	b.gated(record.DesignStepSpec)
	b.boot(clock(9, 0))
	b.file(clock(9, 5), "ACME-1", filing{Title: "Export the widget list", Status: "todo", Rank: "a0", Labels: handedToLegion})
	b.rootPromoted(clock(9, 5), "ACME-1")
	architect := recordedSession("ACME-1", claim.RoleArchitect, architectSession)
	b.claimedBy(clock(9, 6), "ACME-1", architect)
	b.file(clock(9, 10), "ACME-2", filing{Title: "Write the export's header row", Parent: "ACME-1", Tree: "ACME-1", Status: "todo", Rank: "a1"})
	b.specWrittenBy(clock(9, 17), "ACME-1", architect)
	b.asksApprovalBy(clock(9, 18), "ACME-1", approvalAsk, architect)
	b.turnEnds(clock(9, 20), "ACME-1", claim.RoleArchitect)
	b.registered(clock(9, 20), "ACME-1", false)
	since := b.seq("ACME-1")
	b.info(clock(9, 20), "workflow: a registration hands a design document to the person", "issue", "ACME-1",
		"step", string(record.DesignStepSpec), "version", 1, "since_seq", since)
	b.info(clock(9, 20), "workflow: root waits on a person and keeps its admission slot", "issue", "ACME-1", "tree", "ACME-1",
		"reason", "spec version 1 is awaiting a person's review", "since_seq", since)
	b.edit(clock(10, 0), "ACME-1", func(issue *builtIssue) { issue.Title = "Export the widget list as a sheet" })
	b.approves(clock(10, 20), "ACME-1", approvalAsk)
	b.info(clock(10, 20), "workflow: design gate opened", "issue", "ACME-1", "artifact", b.artifact("ACME-1"), "event", "approved", "version", 1)
	b.info(clock(10, 20), "workflow: a person's review or a newer spec version answered a root's wait", "issue", "ACME-1",
		"version", 1, "event", "approved", "ask", "", "waited_version", 1)
	b.turnStarts(clock(10, 20), "ACME-1", claim.RoleArchitect)
	b.childStarts(clock(10, 20), "ACME-2")
	b.turnEnds(clock(10, 35), "ACME-1", claim.RoleArchitect)
	return b.build()
}

// A root that waits on a person keeps its slot and its place in the workflow while its Dispatch
// claim is held by a session that claims within its tree (workflow.Unresumable), which admission
// asks at every pass. In the record that session is the root architect's own, whose claim the
// daemon holds between the architect's turns. The test replays sessionClaimWindow under main: the
// person's retitle at 10:00 brings the claim of the architect's session into the replay's copy of
// Dispatch, and the root is kept as the record kept it — no release of it — until the person's
// approval at 10:20, which opens its gate and starts ACME-2 then. A start state that writes no claim
// of the architect between its turns, or claims that run another session than the record's,
// releases the root at the retitle, and ACME-2 never starts.
func TestARootWaitingOnAPersonKeepsItsArchitectsSession(t *testing.T) {
	_, log := knobRun(t, sessionClaimWindow(t), mainRule(t), "main")
	if released := linesAt(log, func(line replayLine) bool { return line.Msg == releasedRootLine && line.Issue == "ACME-1" }); len(released) != 0 {
		t.Errorf("the replay released ACME-1 at %s, which the record kept waiting on the person", describeTimes(released))
	}
	seconds := func(times []time.Time) []time.Time {
		for i := range times {
			times[i] = times[i].Truncate(time.Second)
		}
		return times
	}
	opened := seconds(linesAt(log, func(line replayLine) bool {
		return line.Msg == "workflow: design gate opened" && line.Issue == "ACME-1"
	}))
	if want := []time.Time{clock(10, 20)}; !slices.EqualFunc(opened, want, time.Time.Equal) {
		t.Errorf("ACME-1's gate opened at %s, want at %s, the person's approval", describeTimes(opened), describeTimes(want))
	}
	started := seconds(linesAt(log, func(line replayLine) bool {
		return line.Msg == phaseChangedLine && line.Issue == "ACME-2" && line.From == string(phase.Admitted) && line.To == string(phase.Planning)
	}))
	if want := []time.Time{clock(10, 20)}; !slices.EqualFunc(started, want, time.Time.Equal) {
		t.Errorf("ACME-2 moved into planning at %s, want at %s, as its root's gate opened", describeTimes(started), describeTimes(want))
	}
}

// withSessionsAsRecorded is w with each session its events name by a claim token — as the fixtures
// name a worker's session — named instead as Dispatch records a worker's session: by an id of its
// own, under the title Legion gives the role's session, `Legion <role> · <KEY>`, wherever the
// event names it (its actor, an ask's author, a version's author). It answers the window and how
// many sessions it renamed.
func withSessionsAsRecorded(t *testing.T, w Window) (Window, int) {
	t.Helper()
	renamed := map[string]map[string]any{}
	var rename func(v any) any
	rename = func(v any) any {
		switch v := v.(type) {
		case map[string]any:
			if kind, _ := v["kind"].(string); kind == "session" {
				id, _ := v["id"].(string)
				if ref, err := claimRefOf(claim.Token(id)); err == nil && ref.Issue != "" {
					if _, seen := renamed[id]; !seen {
						renamed[id] = map[string]any{"kind": "session", "id": fmt.Sprintf("00000000-0000-4000-8000-%012x", 0xc00+len(renamed)),
							"origin": map[string]any{"host": "omp", "session_title": fmt.Sprintf("Legion %s · %s", ref.Role, ref.Issue)}}
					}
					return renamed[id]
				}
			}
			for _, key := range slices.Sorted(maps.Keys(v)) {
				v[key] = rename(v[key])
			}
		case []any:
			for i := range v {
				v[i] = rename(v[i])
			}
		}
		return v
	}
	events := make(map[string][]json.RawMessage, len(w.Events))
	for _, key := range slices.Sorted(maps.Keys(w.Events)) {
		for _, raw := range w.Events[key] {
			var event map[string]any
			if err := json.Unmarshal(raw, &event); err != nil {
				t.Fatalf("read an event of %s: %v", key, err)
			}
			changed, err := json.Marshal(rename(event))
			if err != nil {
				t.Fatalf("write an event of %s: %v", key, err)
			}
			events[key] = append(events[key], changed)
		}
	}
	w.Events = events
	return w, len(renamed)
}

// A worker's event is an act of its claim's work, which a replay places at its offset in the
// piece of that work (placeEvents), whether the record names the worker's session by its claim
// token or, as Dispatch records it, by an id of its own under the title Legion gives the role's
// session. The test replays laterStartWindow with its workers' sessions named as Dispatch records
// them, under main, where ACME-2's work comes 45 minutes sooner than in the record: its architect's
// version 1 of the spec comes at 10:25, so the person's approval, 30 minutes after the version,
// moves ACME-2 into planning at 10:55; and its planner's link to ACME-3 comes at 11:05, so ACME-3's
// close, 20 minutes after the link, gives ACME-2 its slot back at 11:25 — the moments the window
// with its sessions named by their tokens replays them at (TestTheAnswerKnobPlacesAnAnswerByItsQuestion,
// TestTheTimingKnobPlacesAWaitsEndByItsAnchor). Placed at their recorded times instead, the version
// would move ACME-2 into planning at 11:40 and the link its slot back at 12:10.
func TestAWorkersEventMovesWithItsWorkWhateverNamesItsSession(t *testing.T) {
	window, renamed := withSessionsAsRecorded(t, laterStartWindow(t))
	if renamed != 3 {
		t.Fatalf("laterStartWindow names %d sessions by their claim tokens, want 3: the architects of ACME-1 and ACME-2 and ACME-2's planner", renamed)
	}
	_, log := knobRun(t, window, mainRule(t), "main")
	moved := linesAt(log, func(line replayLine) bool {
		return line.Msg == phaseChangedLine && line.Issue == "ACME-2" && line.From == string(phase.Admitted) && line.To == string(phase.Planning)
	})
	if want := []time.Time{clock(10, 55)}; !slices.EqualFunc(moved, want, time.Time.Equal) {
		t.Errorf("ACME-2 moved into planning at %s, want at %s: 30 minutes after its architect's version, which moves with its work",
			describeTimes(moved), describeTimes(want))
	}
	taken := linesAt(log, func(line replayLine) bool { return line.Msg == "admission: slot taken" && line.Issue == "ACME-2" })
	if want := []time.Time{clock(10, 15), clock(11, 25)}; !slices.EqualFunc(taken, want, time.Time.Equal) {
		t.Errorf("ACME-2 took a slot at %s, want at %s: its promotion and the end of the wait its planner's link, which moves with its work, began",
			describeTimes(taken), describeTimes(want))
	}
}
