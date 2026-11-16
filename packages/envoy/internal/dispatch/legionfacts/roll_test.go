package legionfacts

import (
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/events"
)

// writers is the build each stored row of the history's project records as its writer, by key.
func (c *consumer) writers(t *testing.T) map[string]string {
	t.Helper()
	rows, err := c.database.Pool.Query(t.Context(),
		`select issue_key, written_build from issue_facts where project_key = $1`, c.fixture.Project)
	if err != nil {
		t.Fatalf("read the stored rows' writers: %v", err)
	}
	defer rows.Close()
	writers := map[string]string{}
	for rows.Next() {
		var key, build string
		if err := rows.Scan(&key, &build); err != nil {
			t.Fatalf("read the stored rows' writers: %v", err)
		}
		writers[key] = build
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the stored rows' writers: %v", err)
	}
	return writers
}

// writtenBy is the keys of writers whose writer is build, sorted.
func writtenBy(writers map[string]string, build string) []string {
	keys := []string{}
	for key, writer := range writers {
		if writer == build {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	return keys
}

// TestARollEndsWithEveryRowTheNewerBuildsAndNoBuildWritesWithNoInput is how a roll ends with no
// timer (R27, otherBuildsRows): two loops of two builds on one database, sharing its cursors as
// two processes of a roll do, the newer started after the older read the project whole.
//
//  1. The newer process's first pass reads the project whole, since the record names a build
//     recorded before it started, and records its own build.
//  2. The older process, still serving, writes rows by its own rule for an event its feed reads,
//     and neither reads the project whole nor takes the record back, since the record names a
//     build recorded after it started.
//  3. The newer process's next pass, which the older write's change notifies, computes every row
//     the older wrote again, so no row is left that another build wrote.
//  4. The older process's next pass rewrites nothing of the newer's, and with no input moving
//     neither process writes a row, however far the clock moves.
func TestARollEndsWithEveryRowTheNewerBuildsAndNoBuildWritesWithNoInput(t *testing.T) {
	older := newConsumer(t, 3)
	project := older.fixture.Project
	older.pass(t)
	if build, _ := markedBuild(t, older); build != testBuild {
		t.Fatalf("the older process's first pass recorded %q, want %q", build, testBuild)
	}

	newer := &consumer{history: older.history, at: older.at.Add(time.Minute)}
	newer.deps = Deps{Pool: older.database.Pool, Build: otherBuild, Horizon: events.NewHorizon(older.database.Pool)}
	newer.sched = schedule{page: feedPage, now: func() time.Time { return newer.at }, started: newer.at}

	// 1. The newer build reads the project whole and records itself.
	first := newer.pass(t)
	if build, _ := markedBuild(t, newer); build != otherBuild {
		t.Fatalf("the newer process's first pass left the record naming %q, want its own build %q", build, otherBuild)
	}
	if left := writtenBy(newer.writers(t), testBuild); len(left) != 0 {
		t.Errorf("after the newer build's whole read %d rows still name the older build as their writer: %v", len(left), left)
	}
	recordedAt := computedWholeAt(t, newer)[project]

	// 2. The older build writes rows for an event its feed reads.
	older.at = newer.at.Add(time.Minute)
	issue := older.fixture.Open[0]
	older.writeEvent(t, issue, "implementer", "comment.created", `{"status":"in_progress"}`, older.at.Add(-time.Minute))
	olderWrote := older.pass(t)
	if !slices.Contains(olderWrote, issue) {
		t.Fatalf("the older process's pass after an event on %s wrote %v, which does not hold it: this test examined nothing", issue, olderWrote)
	}
	if build, _ := markedBuild(t, older); build != otherBuild {
		t.Errorf("the older process took the record: it names %q, want the newer build %q", build, otherBuild)
	}
	if at := computedWholeAt(t, older)[project]; !at.Equal(recordedAt) {
		t.Errorf("the older process read %s whole at %s; the newer build had recorded it at %s", project, at, recordedAt)
	}
	stamped := writtenBy(older.writers(t), testBuild)
	if !slices.Equal(stamped, olderWrote) {
		t.Errorf("the rows that name the older build are %v, want the %v its pass wrote", stamped, olderWrote)
	}

	// 3. The newer build's next pass computes them again.
	newer.at = older.at.Add(time.Second)
	corrected := newer.pass(t)
	if left := writtenBy(newer.writers(t), testBuild); len(left) != 0 {
		t.Errorf("after the newer build's next pass %d rows still name the older build as their writer: %v", len(left), left)
	}
	for _, key := range stamped {
		if !slices.Contains(corrected, key) {
			t.Errorf("%s was written by the older build and the newer build's next pass left it alone; it changed %v", key, corrected)
		}
	}
	if outsideIt := outside(corrected, stamped); len(outsideIt) > 0 {
		t.Errorf("the newer build's correcting pass changed %d rows the older build had not written: %v", len(outsideIt), outsideIt)
	}

	// 4. The older build rewrites nothing of the newer's, and with no input neither writes.
	writers := newer.writers(t)
	for _, later := range []time.Duration{time.Minute, time.Hour, 72 * time.Hour} {
		older.at, newer.at = newer.at.Add(later), newer.at.Add(later)
		if changed := older.pass(t); len(changed) != 0 {
			t.Errorf("the older process's pass %s on, with no input moving, changed %v", later, changed)
		}
		if changed := newer.pass(t); len(changed) != 0 {
			t.Errorf("the newer process's pass %s on, with no input moving, changed %v", later, changed)
		}
	}
	if after := newer.writers(t); !maps.Equal(after, writers) {
		t.Errorf("the rows' writers moved over passes with no input moving: %v, from %v", after, writers)
	}
	t.Logf("the newer build's whole read changed %d rows; the older build then wrote %d rows by its own rule (%v); the newer build's next pass computed %d again (%v), and %d rows name the newer build; three passes of each with no input wrote nothing",
		len(first), len(olderWrote), olderWrote, len(corrected), corrected, len(writtenBy(writers, otherBuild)))
}
