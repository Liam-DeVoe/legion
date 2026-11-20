package outbox

import (
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// An entry of the held-copy record that no pass has found waiting for fallbackClaim - its hold
// moved to another process - leaves the gauges at once and leaves the record at the next pass
// that holds a copy, with no scrape of the gauges: reading them changes nothing, and the record
// keeps only what this process's passes last found waiting. The record reads a clock the test
// moves (heldClock), and the stale entry ages by that clock alone, so a read of the wall clock in
// the sweep leaves the entry in the record and fails the row.
func TestAHandedOverHeldCopyLeavesTheRecordWithoutAScrape(t *testing.T) {
	clock := &heldClock{at: time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)}
	held := newHeldCopies(clock.now)
	issue := "A-1"
	event := model.Event{IssueKey: &issue}
	stale := fallbackCopy{commentID: "stale", destination: "notifications.agent.s1"}
	held.hold(stale, event, model.MentionCopyHolder{EventID: 1}, time.Hour)
	clock.advance(2 * fallbackClaim)
	entries := func() []string {
		held.mu.Lock()
		defer held.mu.Unlock()
		var comments []string
		for key := range held.rows {
			comments = append(comments, key.comment)
		}
		return comments
	}

	if count, _ := held.samples(clock.now()); count != 0 {
		t.Fatalf("the gauges read %d, want nothing of a copy no pass found waiting for %s", count, fallbackClaim)
	}
	if got := entries(); len(got) != 1 {
		t.Fatalf("reading the gauges left the record %v, want it unchanged: the gauges only read", got)
	}
	held.hold(fallbackCopy{commentID: "fresh", destination: "notifications.agent.s1"}, event,
		model.MentionCopyHolder{EventID: 2}, time.Hour)
	got := entries()
	t.Logf("the record after the next pass held a copy: %v", got)
	if len(got) != 1 || got[0] != "fresh" {
		t.Fatalf("the record holds %v, want the fresh copy alone: the pass drops the stale one", got)
	}
}

// The outbox New makes - cmd/dispatch's - gives its held-copy record the wall clock: a copy a pass
// holds through it is found waiting at a wall-clock time, and its age read 30 s later on the wall
// clock is 30 s. The bounds are the wall clock read before and after the hold, so no load moves
// them. A record given any other clock - one that stands still, or a test's - finds the copy
// stale at that read, or reads another age.
func TestTheOutboxGivesItsHeldCopyRecordTheWallClock(t *testing.T) {
	issue := "A-1"
	before := time.Now()
	held := New(Deps{}).deps.held
	held.hold(fallbackCopy{commentID: "c1", destination: "notifications.agent.s1"}, model.Event{IssueKey: &issue},
		model.MentionCopyHolder{EventID: 1}, time.Hour)
	after := time.Now()
	const later = 30 * time.Second
	count, oldest := held.samples(after.Add(later))
	if count != 1 || oldest < later || oldest > later+after.Sub(before) {
		t.Fatalf("read %v after the hold on the wall clock, the record counts %d copies, the oldest held %v; want one, held %v to %v",
			later, count, oldest, later, later+after.Sub(before))
	}
}
