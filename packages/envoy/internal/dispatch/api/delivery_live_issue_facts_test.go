package api

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/delivery"
	"github.com/sjawhar/envoy/internal/dispatch/legionfacts"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// issueFactsChange is one delivery_changes row of the stored per-issue Legion facts: the project
// in Repo and the issue in Ref, with the issue's close before and after the change (migration
// 20261116151147).
func issueFactsChange(project, issue string, closedBefore, closedAfter time.Time) delivery.Change {
	return delivery.Change{Seq: 1, Kind: "issue_facts", Repo: project, Ref: issue, TOld: &closedBefore, TNew: &closedAfter}
}

// boardViewShowing is a Legion board of project naming the issues given, as a recompute's answer
// leaves them.
func boardViewShowing(project string, issues ...string) *deliveryView {
	return viewShowingIssues(deliveryLiveSpec{kind: legionBoardView, project: project}, issues)
}

// deliveryViewShowing is a view that reads the deployment's delivery rows, naming the issues
// given. Its window is the last seven days, which this change's kind never reads.
func deliveryViewShowing(kind string, issues ...string) *deliveryView {
	return viewShowingIssues(deliveryLiveSpec{kind: kind, preset: 7}, issues)
}

func viewShowingIssues(spec deliveryLiveSpec, issues []string) *deliveryView {
	view := &deliveryView{spec: spec, issues: map[string]struct{}{}}
	for _, issue := range issues {
		view.issues[issue] = struct{}{}
	}
	return view
}

// TestAnIssueFactsChangeTouchesTheLegionBoardOfItsProject: a write of one issue's stored Legion
// facts moves the Legion board of that issue's project and no other view at all. The board's read
// takes the rows of the issues it shows, every issue of each tree a card stands in among them
// (legionfacts.Read), so a row of an issue the board names nowhere yet - a new member of a shown
// tree, the first row of an issue, a row a build's whole read writes - moves its answer as much
// as a row of an issue it already shows does, and a board that marked only the issues it names
// would stand behind its GET until some other write woke it.
//
// No delivery view is woken, whichever issue it names: the stored facts have one reader
// (legionfacts.Read, which the board calls), so no timeline, measures or pipeline answer can
// move when a facts row does. That matters on a burst: a build's whole read of a project rewrites
// every row whose values or writer moved, and the build a project's record names computes again
// every row the other build of a roll wrote, and each rewrite logs a change, so one such pass
// would otherwise force a whole Delivery recompute on every subscribed page.
func TestAnIssueFactsChangeTouchesTheLegionBoardOfItsProject(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	change := issueFactsChange("INFRA", "INFRA-7", now.Add(-26*time.Hour), now.Add(-2*time.Hour))

	tests := []struct {
		what string
		view *deliveryView
		want bool
	}{
		{"a Legion board of the project that shows the issue", boardViewShowing("INFRA", "INFRA-7", "INFRA-8"), true},
		{"a Legion board of the project that shows other issues", boardViewShowing("INFRA", "INFRA-8"), true},
		{"a Legion board of the project that shows none", boardViewShowing("INFRA"), true},
		{"a Legion board of another project", boardViewShowing("OPS", "OPS-7"), false},
		{"a timeline that shows the issue", deliveryViewShowing(deliveryLiveTimeline, "INFRA-7"), false},
		{"a timeline that shows other issues", deliveryViewShowing(deliveryLiveTimeline, "INFRA-8"), false},
		{"a measures view that shows the issue", deliveryViewShowing(deliveryLiveMeasures, "INFRA-7"), false},
		{"a measures view that shows other issues", deliveryViewShowing(deliveryLiveMeasures, "INFRA-8"), false},
		{"a pipeline view that shows the issue", deliveryViewShowing(deliveryLivePipeline, "INFRA-7"), false},
		{"a pipeline view that shows other issues", deliveryViewShowing(deliveryLivePipeline, "INFRA-8"), false},
	}
	woken, left := 0, 0
	for _, test := range tests {
		got := test.view.touchedBy(change, now)
		if got != test.want {
			t.Errorf("%s: touchedBy = %t, want %t", test.what, got, test.want)
		}
		if got {
			woken++
		} else {
			left++
		}
	}

	// The board still judges no freshness: the worker reads classes.delivery for that
	// (delivery_live_hub_worker.go), this change moves no delivery row, and the flag stays off,
	// so the verdict stays the one the delivery views' own reads set.
	if deliveryLiveKinds[legionBoardView].delivery {
		t.Error("the Legion board reads no delivery row, so it must judge no freshness")
	}
	t.Logf("a change of %s in %s: %d of %d views woken, %d left, Legion board delivery flag %t",
		change.Ref, change.Repo, woken, len(tests), left, deliveryLiveKinds[legionBoardView].delivery)
}

// writeIssueFacts writes one issue's stored Legion facts, in the tree of root, whose work falls
// between first and last, closed at closed or open where it is nil, as the facts consumer writes
// one issue's row (legionfacts.WriteProject). The row is written by hand rather than computed, so
// the row a check reads is the one it states.
func writeIssueFacts(t *testing.T, database *store.Store, project, issue, root string, first, last time.Time, closed *time.Time) {
	t.Helper()
	row := legionfacts.Row{
		Project: project, Issue: issue, OpenedAt: first, ClosedAt: closed, RootKey: root,
		WorkFirstAt: &first, WorkLastAt: &last, UpdatedAt: time.Now().UTC(),
	}
	if _, err := legionfacts.WriteProject(t.Context(), database.Pool,
		legionfacts.ProjectWrite{Project: project, Rows: []legionfacts.Row{row}, Stated: []string{issue}}); err != nil {
		t.Fatalf("write the stored facts of %s: %v", issue, err)
	}
}

// recomputeOfView matches a change's recompute of the one view the frame names, by the key
// identical views share.
func recomputeOfView(t *testing.T, frame DeliveryLiveView) func(deliveryHubNote) bool {
	t.Helper()
	spec, refusal := parseDeliveryLiveView(frame)
	if refusal != nil {
		t.Fatalf("read the %s view: %+v", frame.Kind, refusal)
	}
	return func(note deliveryHubNote) bool {
		return note.what == "recompute begin" && note.view == spec.key && note.cause == deliveryCauseChange
	}
}

// TestAnIssueFactsWriteSendsTheLegionBoardItsEntryAndNoFreshness: the write of one issue's stored
// Legion facts reaches an open board through the change log, as a delivery row's write reaches an
// open Delivery page. The board carries that issue's stored row in its issues map, so the write
// is recomputed and sent as a delta of the one entry, and the recompute sends no freshness frame,
// because the board reads no delivery row and judges no verdict. The first row of an issue the
// board names nowhere, a closed new member of a tree it shows, which only the read of that tree by
// its root brings into the map, reaches it the same way, as the entry its GET would answer. A
// closed row of a tree the board does not show recomputes it into the same body, which sends
// nothing. The timeline shows none of them and is sent nothing at all.
//
// It holds the hub to one recompute of the board for each write of its project and to none of
// any other view: the hub recomputes every view when its event feed is taken again, which no
// write of this test caused, so another recompute here is a mark this row did not ask for.
func TestAnIssueFactsWriteSendsTheLegionBoardItsEntryAndNoFreshness(t *testing.T) {
	rig := newHubRig(t, nil)
	seedHubSettings(t, rig.database)
	seedLegionProjects(t, rig.handler, "INFRA")
	nameLegionDaemon(t, rig.handler, "INFRA", "http://legion-infra.internal:13370")
	seedLegionBoard(t, rig.database, "INFRA", rig.clock.Now())

	// The timeline's read sets the hub's first freshness verdict, so any frame after it is a
	// verdict that changed rather than the first one the hub ever knew.
	timelineFrame, boardFrame := presetView(deliveryLiveTimeline, 7, ""), legionView("INFRA")
	timeline := newFakeLiveConn()
	rig.hub.Subscribe(timeline, subscribeFrame("d", timelineFrame))
	timeline.snapshot(t, 5*time.Second, "d")
	board := newFakeLiveConn()
	rig.hub.Subscribe(board, subscribeFrame("b", boardFrame))
	board.snapshot(t, 5*time.Second, "b")
	board.freshness(t, 5*time.Second, "the verdict after the board's first snapshot")
	rig.settle(t)

	// INFRA-4 is on the board's cards, and INFRA-99 on none and with no row before its write: a
	// closed new member of the tree of INFRA-3, whose card the board shows, so neither its key nor
	// the open rows bring it in, and its stored root alone does.
	workedAt := rig.clock.Now().Add(-5 * time.Hour)
	for _, write := range []struct {
		issue  string
		closed *time.Time
	}{{"INFRA-4", nil}, {"INFRA-99", new(workedAt.Add(2 * time.Hour))}} {
		issue := write.issue
		shown := rig.mark()
		writeIssueFacts(t, rig.database, "INFRA", issue, "INFRA-3", workedAt, workedAt.Add(time.Hour), write.closed)
		rig.hub.catchUpNow()
		rig.waitFor(t, shown, 5*time.Second, "a recompute of the board of "+issue+"'s project", recomputeOfView(t, boardFrame))
		delta := board.delta(t, 5*time.Second, "b")
		if _, sent := delta.Maps["issues"][issue]; !sent {
			t.Fatalf("the delta after the facts write of %s carries the issues entries %v, want %s's", issue, delta.Maps["issues"], issue)
		}
		board.none(t, viewMaxWait, "the delta of the facts write of "+issue+", which carries no freshness verdict")
		boardRecomputes := rig.count(shown, recomputeOfView(t, boardFrame))
		if allRecomputes := rig.count(shown, recomputeBegin(deliveryCauseChange)); boardRecomputes != 1 || allRecomputes != 1 {
			t.Errorf("the facts write of %s recomputed the board %d times and %d views in all, want the board once and no other view",
				issue, boardRecomputes, allRecomputes)
		}
	}
	// INFRA-98 is a closed issue, the root of a tree no card stands in: its row wakes the board of
	// its project, whose recompute answers the body it sent, so nothing is sent.
	unshown := rig.mark()
	writeIssueFacts(t, rig.database, "INFRA", "INFRA-98", "INFRA-98", workedAt, workedAt.Add(time.Hour), new(workedAt.Add(2*time.Hour)))
	rig.hub.catchUpNow()
	rig.waitFor(t, unshown, 5*time.Second, "a recompute of the board of INFRA-98's project", recomputeOfView(t, boardFrame))
	board.none(t, viewMaxWait, "the facts write of INFRA-98, of a tree the board does not show")
	if n := rig.count(unshown, recomputeOfView(t, boardFrame)); n != 1 {
		t.Errorf("the facts write of INFRA-98 recomputed the board %d times, want once", n)
	}
	timeline.none(t, viewMaxWait, "the facts writes of issues the timeline does not show")

	// A row of a project no open view shows leaves the hub idle and tells no page anything.
	idle := rig.mark()
	writeIssueFacts(t, rig.database, "OPS", "OPS-1", "OPS-1", workedAt, workedAt.Add(time.Hour), nil)
	rig.hub.catchUpNow()
	rig.waitNoRecompute(t, viewMaxWait)
	board.none(t, time.Second, "the facts write of a project no view shows")
	timeline.none(t, time.Second, "the facts write of a project no view shows")
	if n := rig.count(idle, recomputeBegin(deliveryCauseChange)); n != 0 {
		t.Errorf("the facts write of another project recomputed %d views, want none", n)
	}
	t.Log("the facts writes of INFRA-4, on a card, and INFRA-99, closed and on none in a tree the board shows, each recomputed the board once and sent it one delta carrying its entry and no freshness frame, and sent the timeline nothing; a write of INFRA-98, of a tree the board does not show, recomputed it once and sent nothing; a write of OPS-1 recomputed nothing and sent nothing")
}

// batchRows is how many stored facts rows one whole read of a project of INFRA's size rewrites:
// the 210 issues of the facts' own rig that carry Legion phase work. A build's first whole read
// rewrites every row whose values or writer moved, as the newer build of a roll rewrites every
// row the older wrote, and the trigger logs one delivery change for each (migration
// 20261111093100).
const batchRows = 210

// writeFactsBatch writes count rows of the project in one write, as one whole read does, giving
// every row the work hours named. It answers the keys WriteProject says it changed. The rows are
// written by hand rather than computed, so the batch a measurement counts is the batch this
// states.
func writeFactsBatch(t *testing.T, database *store.Store, project string, count int, hours float64, at time.Time) []string {
	t.Helper()
	rows := make([]legionfacts.Row, 0, count)
	for number := 1; number <= count; number++ {
		first := at.Add(-time.Duration(number) * time.Minute)
		last := first.Add(time.Duration(hours * float64(time.Hour)))
		rows = append(rows, legionfacts.Row{
			Project: project, Issue: fmt.Sprintf("%s-%d", project, number),
			OpenedAt: at.Add(-48 * time.Hour), HoursByPhase: map[string]float64{"implementing": hours},
			WorkHours: hours, RootKey: project + "-1", WorkFirstAt: &first, WorkLastAt: &last,
			UpdatedAt: at,
		})
	}
	changed, err := legionfacts.WriteProject(t.Context(), database.Pool,
		legionfacts.ProjectWrite{Project: project, Rows: rows, Whole: true})
	if err != nil {
		t.Fatalf("write %d stored facts rows of %s: %v", count, project, err)
	}
	return changed
}

// TestWhatOneBatchOfChangedLegionFactsRowsCostsAnOpenBoard: what one pass that rewrites every row
// of a project costs a subscribed Legion board. A whole read writes every changed row in one pass,
// the trigger logs a change row for each, and the hub marks the board once per change row it
// reads.
//
// A mark is a flag on the view, not a recompute (markDirtyLocked), and a dirty view is due
// viewQuiet after its last mark or viewMaxWait after its first (dueAt). The clock stands still,
// so the whole batch is one instant to the hub however long the writes take: nothing is due
// until the test moves the clock, and what the hub does then is the measurement.
//
// The rows are written once before the subscription, so the board's first answer names every one
// of them and the batch rewrites rows the board already shows, which is what a build's whole
// read of a project that already holds rows writes, and the delta carries every entry it moved.
func TestWhatOneBatchOfChangedLegionFactsRowsCostsAnOpenBoard(t *testing.T) {
	clock := newStillHubClock()
	rig := newClockedHubRig(t, clock)
	seedHubSettings(t, rig.database)
	seedLegionProjects(t, rig.handler, "INFRA")
	nameLegionDaemon(t, rig.handler, "INFRA", "http://legion-infra.internal:13370")
	seedLegionBoard(t, rig.database, "INFRA", clock.Now())
	writeFactsBatch(t, rig.database, "INFRA", batchRows, 4, clock.Now())

	conn := newFakeLiveConn()
	rig.hub.Subscribe(conn, subscribeFrame("b", legionView("INFRA")))
	snapshot := conn.snapshot(t, 10*time.Second, "b")
	var answered struct {
		Issues map[string]json.RawMessage `json:"issues"`
	}
	if err := json.Unmarshal(snapshot.Body, &answered); err != nil {
		t.Fatalf("read the board's first answer: %v", err)
	}
	if len(answered.Issues) != batchRows {
		t.Fatalf("the board's first answer names %d issues entries, want the %d rows the batch below rewrites",
			len(answered.Issues), batchRows)
	}
	rig.settle(t)

	start := rig.mark()
	before := latestChange(t, rig.database)
	// One pass that rewrites the project: every row's hours have moved, so every row is rewritten.
	changed := writeFactsBatch(t, rig.database, "INFRA", batchRows, 5, clock.Now())
	logged := latestChange(t, rig.database) - before
	if len(changed) != batchRows || logged != int64(batchRows) {
		t.Fatalf("the batch rewrote %d rows and logged %d changes, want %d of each: a batch of another size is another measurement",
			len(changed), logged, batchRows)
	}
	rig.hub.catchUpNow()
	conn.none(t, 2*viewQuiet, "a batch of facts rows the clock has not moved past")
	if n := rig.count(start, recomputeBegin(deliveryCauseChange)); n != 0 {
		t.Fatalf("recomputes before the batch's quiet passed = %d, want 0", n)
	}

	started := time.Now()
	clock.Advance(viewQuiet)
	delta := conn.delta(t, 30*time.Second, "b")
	took := time.Since(started)
	conn.none(t, viewMaxWait, "the batch's one delta")
	recomputes := rig.count(start, recomputeBegin(deliveryCauseChange))
	if recomputes != 1 {
		t.Errorf("a batch of %d changed rows cost the board %d recomputes, want the one its marks coalesce into",
			batchRows, recomputes)
	}
	t.Logf("one batch: %d rows rewritten in one Write, %d delivery changes logged, %d recompute(s) of the subscribed board and 1 frame to it, carrying %d issues entries; the recompute answered %s after the quiet passed",
		len(changed), logged, recomputes, len(delta.Maps["issues"]), took.Round(time.Millisecond))
}
