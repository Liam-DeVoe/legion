package store

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/record"
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
// steps without a row of step_starts. The way back keeps each issue's witness and the moment of the
// rollback, and re-arms the roll forward, which the newer image's Migrate then applies again: each
// row of an issue whose witness changed reads 'uncounted', and every other row stands as it was;
// and each run such a row held open at the rollback, which the earlier release ended, gets the end
// of its record, stopped at that moment, under the identity the row kept. Here the earlier release:
// raises a root's pass (its tester failed and ran again), moves another into the phase whose
// counted run had ended, re-admits a tree's root (its member's step starts again), lingers a
// tree's root (its member's step ends, but not a member's merge that awaits its pull request),
// releases a held issue into the phase it was held from, and re-admits a root of its own; it does
// nothing to the last root. A second run of the way back, before any roll forward, keeps the
// witness and the moment of the first, which the earlier release has moved since.
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
	// Each row's record names a uuid of its own, as StepID's does: md5 of the issue, the kind and
	// the place, read back below as the id each end must carry.
	row := func(key, kind string, place int, run string) {
		t.Helper()
		exec(fmt.Sprintf(`insert into step_starts (issue, task, kind, place, starts, agent, started_at, run, record)
			values ('%[1]s', 'default', '%[2]s', %[3]d, 2, 'agent of %[1]s', '2026-11-17T01:00:00Z', '%[4]s',
			jsonb_build_object('id', md5('%[1]s %[2]s %[3]d')::uuid, 'task', 'default', 'step', %[3]d, 'kind', '%[2]s', 'role', 'tester',
				'type', 'testing', 'agent', 'agent of %[1]s', 'generation', 1, 'started_at', '2026-11-17T01:00:00Z'))`, key, kind, place, run))
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
	var rolledBackAt time.Time
	if err := store.Pool().QueryRow(ctx, `select taken_at from step_starts_rollback where issue = 'LEGION-202'`).Scan(&rolledBackAt); err != nil {
		t.Fatalf("read the moment the way back kept: %v", err)
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
	ends := map[string]record.StepRecord{}
	outbox, err := store.Pool().Query(ctx, `select issue, payload from outbox where kind = 'step_record'`)
	if err != nil {
		t.Fatalf("read the outbox: %v", err)
	}
	defer outbox.Close()
	for outbox.Next() {
		row := record.OutboxRow{Kind: record.OutboxKindStepRecord}
		if err := outbox.Scan(&row.Issue, &row.Payload); err != nil {
			t.Fatalf("scan the outbox: %v", err)
		}
		payload, err := record.DecodeOutboxPayload(row)
		if err != nil {
			t.Fatalf("the roll forward's step record row of %s does not decode as the daemon reads it: %v", row.Issue, err)
		}
		ends[row.Issue] = payload.(record.StepRecord)
	}
	if err := outbox.Err(); err != nil {
		t.Fatalf("read the outbox: %v", err)
	}
	ended := []string{"LEGION-202 step 3", "LEGION-211 step 3", "LEGION-221 step 3", "LEGION-240 step 4"}
	if len(ends) != len(ended) {
		t.Errorf("the roll forward wrote the ends of %v, want those of the runs open at the rollback that the earlier release ended: %v", ends, ended)
	}
	for _, run := range ended {
		issue, place, _ := strings.Cut(run, " ")
		var id string
		if err := store.Pool().QueryRow(ctx, `select md5($1)::uuid::text`, issue+" "+place).Scan(&id); err != nil {
			t.Fatalf("compute the id of %s: %v", run, err)
		}
		end := ends[issue]
		if end.ID != id || end.Agent != "agent of "+issue || end.Role != "tester" || end.Generation != 1 ||
			end.Result == nil || *end.Result != "stopped" || end.EndedAt == nil || !end.EndedAt.Equal(rolledBackAt) {
			t.Errorf("the roll forward's end of %s = %+v, want the record %s the row kept, stopped at the rollback, %s", run, end, id, rolledBackAt)
		}
	}
}

// The way back an operator runs keeps what the roll forward needs and touches nothing else: every
// pending row of the outbox — the step records' own, which fill their records at the roll forward,
// and the issue's other work — and every row of step_starts stand as they stood, and the one
// schema row it deletes is the roll forward's. Run twice, as a second rollback would before any roll
// forward, it leaves the same store.
func TestTheWayBackOfTheStepRecordsKeepsEveryOtherRow(t *testing.T) {
	ctx := context.Background()
	store := emptyStore(t)
	if _, err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, seed := range []string{
		`insert into issues (key, tree, project, title, phase, generation, status, rank, last_dispatch_seq)
			values ('LEGION-208', 'LEGION-208', 'LEGION', 'root', 'testing', 1, 'testing', 'A', 1)`,
		`insert into step_starts (issue, task, kind, place, starts, agent, started_at, run, record)
			values ('LEGION-208', 'default', 'step', 3, 4, 'legion-legion-legion-208-tester', '2026-11-17T01:00:00Z', 'open', '{"id": "kept"}'),
			('LEGION-208', 'default', 'step', 2, 2, 'legion-legion-legion-208-implementer', '2026-11-17T00:00:00Z', 'ended', null)`,
		`insert into outbox (kind, issue, payload, attempts, next_at, last_error) values
			('step_record', 'LEGION-208', '{"id": "a start not yet written"}', 3, now() + interval '1 minute', 'Dispatch answered 503'),
			('supervise', 'LEGION-208', '{"op": "start"}', 0, now(), ''),
			('dispatch_status', 'LEGION-209', '{"status": "testing"}', 1, now() + interval '2 minutes', 'timeout')`,
	} {
		if _, err := store.Pool().Exec(ctx, seed); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	read := func(query string) []string {
		t.Helper()
		rows, err := store.Pool().Query(ctx, query)
		if err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		defer rows.Close()
		var all []string
		for rows.Next() {
			var row string
			if err := rows.Scan(&row); err != nil {
				t.Fatalf("%s: %v", query, err)
			}
			all = append(all, row)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return all
	}
	const (
		outbox   = `select row(id, kind, issue, payload, attempts, next_at, last_error, lease_token, lease_until)::text from outbox order by id`
		starts   = `select row(s.*)::text from step_starts s order by issue, kind, place`
		versions = `select version::text from schema_version order by version`
	)
	outboxBefore, startsBefore, versionsBefore := read(outbox), read(starts), read(versions)
	if len(outboxBefore) != 3 || len(startsBefore) != 2 {
		t.Fatalf("seeded %d outbox rows and %d step_starts rows, want 3 and 2", len(outboxBefore), len(startsBefore))
	}
	for run := 1; run <= 2; run++ {
		if _, err := store.Pool().Exec(ctx, stepRecordsWayBack(t)); err != nil {
			t.Fatalf("run %d of the way back: %v", run, err)
		}
		if got := read(outbox); !reflect.DeepEqual(got, outboxBefore) {
			t.Errorf("after run %d of the way back the outbox holds %v, want every pending row as it stood: %v", run, got, outboxBefore)
		}
		if got := read(starts); !reflect.DeepEqual(got, startsBefore) {
			t.Errorf("after run %d of the way back step_starts holds %v, want every count as it stood: %v", run, got, startsBefore)
		}
		var want []string
		for _, version := range versionsBefore {
			if version != fmt.Sprint(stepRecordsRollForwardVersion) {
				want = append(want, version)
			}
		}
		if got := read(versions); !reflect.DeepEqual(got, want) {
			t.Errorf("after run %d of the way back schema_version lost %d rows, want the roll forward's alone", run, len(versionsBefore)-len(got))
		}
	}
}
