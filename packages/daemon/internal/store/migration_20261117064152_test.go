package store

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/store/migrations"
)

// stepRecordsRollForwardVersion is the version of the migration this file tests: the roll forward
// of a rollback of the release that records each step, whose header carries that rollback's way
// back.
const stepRecordsRollForwardVersion = 20261117064152

// stepRecordsWayBack is the way back the roll forward's header carries, from its insert into
// step_starts_rollback to the delete of its own version row, without the comment markers: the
// statements an operator runs (docs/kubernetes.md carries the same block, and internal/textcheck
// holds the two to one text).
func stepRecordsWayBack(t *testing.T) string {
	t.Helper()
	all, err := migrations.All()
	if err != nil {
		t.Fatalf("read the embedded migrations: %v", err)
	}
	mark := fmt.Sprintf("-- delete from schema_version where version = %d;", stepRecordsRollForwardVersion)
	for _, migration := range all {
		if migration.Version != stepRecordsRollForwardVersion {
			continue
		}
		var block []string
		for _, line := range strings.Split(migration.SQL, "\n") {
			if block == nil && !strings.HasPrefix(line, "-- insert into step_starts_rollback") {
				continue
			}
			block = append(block, strings.TrimPrefix(line, "-- "))
			if line == mark {
				return strings.Join(block, "\n")
			}
		}
		t.Fatalf("%s carries no way back from its insert into step_starts_rollback to %q", migration.Name, mark)
	}
	t.Fatalf("no embedded migration has version %d", stepRecordsRollForwardVersion)
	return ""
}

// A rollback hands the store to an earlier release, which moves issues and starts and ends their
// steps without a row of step_starts. The way back keeps each issue's witness and re-arms the roll
// forward, which the newer image's Migrate then applies again: each row of an issue whose witness
// changed reads 'uncounted', and every other row stands as it was. Here the earlier release:
// raises a root's pass (its tester failed and ran again), moves another into the phase whose
// counted run had ended, re-admits a tree's root (its member's step starts again), lingers a
// tree's root (its member's step ends, but not a member's merge that awaits its pull request),
// releases a held issue into the phase it was held from, and re-admits a root of its own; it does
// nothing to the last root. A second run of the way back, before any roll forward, keeps the
// witness of the first, which the earlier release has moved since.
func TestTheRollForwardMarksUncountedEachIssueTheEarlierReleaseMoved(t *testing.T) {
	ctx := context.Background()
	store := emptyStore(t)
	if _, err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	exec := func(sql string) {
		t.Helper()
		if _, err := store.Pool().Exec(ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	issue := func(key, tree, phase, heldFrom string, generation int) {
		t.Helper()
		held := "null"
		if heldFrom != "" {
			held = "'" + heldFrom + "'"
		}
		exec(fmt.Sprintf(`insert into issues (key, tree, project, title, phase, held_from, generation, status, rank, last_dispatch_seq)
			values ('%s', '%s', 'LEGION', '%s', '%s', %s, %d, 'in_progress', 'A', 1)`, key, tree, key, phase, held, generation))
		exec(fmt.Sprintf(`insert into phases (issue, role, claim, handoff_commit, rounds, verdict, pass)
			values ('%s', 'implementer', '', '', 0, '', 1)`, key))
	}
	row := func(key, kind string, place int, run string) {
		t.Helper()
		exec(fmt.Sprintf(`insert into step_starts (issue, task, kind, place, starts, agent, started_at, run)
			values ('%s', 'default', '%s', %d, 2, '', now(), '%s')`, key, kind, place, run))
	}
	issue("LEGION-201", "LEGION-201", "testing", "", 1)
	row("LEGION-201", "step", 3, "open")
	row("LEGION-201", "step", 2, "ended")
	issue("LEGION-202", "LEGION-202", "testing", "", 1)
	row("LEGION-202", "step", 3, "open")
	row("LEGION-202", "step", 2, "ended")
	issue("LEGION-203", "LEGION-203", "implementing", "", 1)
	row("LEGION-203", "step", 3, "ended")
	issue("LEGION-210", "LEGION-210", "done", "", 1)
	issue("LEGION-211", "LEGION-210", "testing", "", 4)
	row("LEGION-211", "step", 3, "open")
	issue("LEGION-220", "LEGION-220", "done", "", 1)
	issue("LEGION-221", "LEGION-220", "testing", "", 4)
	row("LEGION-221", "step", 3, "open")
	issue("LEGION-222", "LEGION-220", "held", "merging", 4)
	row("LEGION-222", "merge", 0, "open")
	issue("LEGION-230", "LEGION-230", "held", "testing", 1)
	row("LEGION-230", "step", 3, "open")
	issue("LEGION-240", "LEGION-240", "reviewing", "", 1)
	row("LEGION-240", "step", 4, "open")

	exec(stepRecordsWayBack(t))
	var recorded bool
	if err := store.Pool().QueryRow(ctx, `select exists (select 1 from schema_version where version = $1)`, stepRecordsRollForwardVersion).Scan(&recorded); err != nil || recorded {
		t.Fatalf("after the way back the roll forward is recorded = %t, %v; want its row deleted, so the roll forward applies it again", recorded, err)
	}
	// The earlier release.
	exec(`update phases set pass = pass + 1 where issue = 'LEGION-202' and role = 'implementer'`)
	exec(`update issues set phase = 'testing' where key = 'LEGION-203'`)
	exec(stepRecordsWayBack(t))
	exec(`update issues set generation = 2 where key = 'LEGION-210'`)
	exec(`update issues set linger_until = now() + interval '1 hour' where key = 'LEGION-220'`)
	exec(`update issues set phase = 'testing', held_from = null where key = 'LEGION-230'`)
	exec(`update issues set generation = 2 where key = 'LEGION-240'`)

	applied, err := store.Migrate(ctx)
	if err != nil || applied != 1 {
		t.Fatalf("the roll forward's Migrate applied %d migrations, %v; want the one the way back re-armed", applied, err)
	}
	runs := map[string]string{}
	rows, err := store.Pool().Query(ctx, `select issue || ' ' || kind || ' ' || place, run from step_starts`)
	if err != nil {
		t.Fatalf("read step_starts: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var place, run string
		if err := rows.Scan(&place, &run); err != nil {
			t.Fatalf("scan step_starts: %v", err)
		}
		runs[place] = run
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read step_starts: %v", err)
	}
	want := map[string]string{
		"LEGION-201 step 3": "open", "LEGION-201 step 2": "ended",
		"LEGION-202 step 3": "uncounted", "LEGION-202 step 2": "uncounted",
		"LEGION-203 step 3": "uncounted",
		"LEGION-211 step 3": "uncounted",
		"LEGION-221 step 3": "uncounted", "LEGION-222 merge 0": "open",
		"LEGION-230 step 3": "open",
		"LEGION-240 step 4": "uncounted",
	}
	for place, run := range want {
		if runs[place] != run {
			t.Errorf("after the roll forward %s reads %q, want %q", place, runs[place], run)
		}
	}
	if len(runs) != len(want) {
		t.Errorf("step_starts holds %d rows after the roll forward, want the %d it held: %v", len(runs), len(want), runs)
	}
	var kept int
	if err := store.Pool().QueryRow(ctx, `select count(*) from step_starts_rollback`).Scan(&kept); err != nil || kept != 0 {
		t.Fatalf("step_starts_rollback holds %d rows after the roll forward, %v; want none, so a later rollback keeps its own", kept, err)
	}
}
