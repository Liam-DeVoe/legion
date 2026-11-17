package workflow

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/store/migrations"
)

// A rollback hands the store to an earlier release, which moves the issue and starts and ends its
// steps without counting a start or marking an end in step_starts, so the roll forward finds each
// place's row as this release left it. A run the earlier release started is none the row counted:
// its end after the roll forward is a record of its own, at the next count, with the start its
// role's entry recorded, whether the run this release counted there had ended before the rollback
// or was still open at it. The run this release counted keeps its own id, and no write after the
// roll forward names it.
func TestAStepTheEarlierReleaseStartedEndsInARecordOfItsOwn(t *testing.T) {
	for _, tc := range []struct {
		name string
		// open is whether the tester's first run, which this release counted, was still running
		// at the rollback.
		open bool
	}{
		{name: "its place's run had ended at the rollback"},
		{name: "its place's run was still open at the rollback", open: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := migratedPool(t)
			stepRoot(t, pool, phase.Implementing)
			move(t, pool, stepEngine(nil), "LEGION-208", TriggerImplementationReady, "", record.PhaseRow{})
			if !tc.open {
				move(t, pool, stepEngine(nil), "LEGION-208", TriggerTesterFailed, "", record.PhaseRow{})
			}
			rollBackStepRecords(t, pool)
			// The earlier release runs the tester again, from its own transitions: the tester's
			// second entry, raised at secondRun, and for the run still open at the rollback, its
			// fail and the implementer's round that brought the work back to testing first, the
			// fail counting the pass every release's move back counts (recordPass).
			secondRun := stepClock.Add(time.Hour)
			if tc.open {
				engine := stepEngine(nil)
				applyInTx(t, pool, func(ctx context.Context, tx pgx.Tx) error {
					return engine.recordPass(ctx, tx, "LEGION-208", phase.Testing, phase.Implementing, nil)
				})
			}
			execSQL(t, pool, `update issues set phase = 'testing' where key = 'LEGION-208'`)
			execSQL(t, pool, `update phases set entry = entry + 1, entry_raised_at = $1 where issue = 'LEGION-208' and role = 'tester'`, secondRun)
			rollForwardStepRecords(t, pool)

			before := len(stepWrites(t, pool, "LEGION-208"))
			move(t, pool, stepEngine(nil), "LEGION-208", TriggerTesterPassed, "", record.PhaseRow{})
			after := stepWrites(t, pool, "LEGION-208")[before:]
			counted := StepID("LEGION-208", 1, "default", "step", 3, 1)
			for _, w := range after {
				if w.ID == counted {
					t.Fatalf("a write after the roll forward names the run this release counted (%s): %+v", counted, w)
				}
			}
			if len(after) != 2 {
				t.Fatalf("writes after the roll forward = %+v, want the end of the earlier release's tester run and the start of reviewing", after)
			}
			end := after[0]
			assertEnd(t, end, "step", 3, 1, "passed", nil)
			if want := StepID("LEGION-208", 1, "default", "step", 3, 2); end.ID != want || end.StartedAt == nil || !end.StartedAt.Equal(secondRun) ||
				end.Agent != treeRoleToken(t, "LEGION-208", claim.RoleTester) {
				t.Fatalf("the end = %+v, want the record %s of the second count, the tester's, started at %s", end, want, secondRun)
			}
		})
	}
}

// execSQL runs one statement on pool, with args.
func execSQL(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// stepRecordsRollForward is the version of the migration whose header carries the step records'
// way back, which the way back re-arms and the roll forward applies again.
const stepRecordsRollForward = 20261117064152

// rollBackStepRecords runs the way back the roll forward's migration carries in its header, as an
// operator runs it with the daemon stopped (docs/kubernetes.md, "Rolling back the release that
// records each step", carries the same block; internal/textcheck holds the two to one text).
func rollBackStepRecords(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	all, err := migrations.All()
	if err != nil {
		t.Fatalf("read the embedded migrations: %v", err)
	}
	mark := fmt.Sprintf("-- delete from schema_version where version = %d;", stepRecordsRollForward)
	for _, migration := range all {
		if migration.Version != stepRecordsRollForward {
			continue
		}
		var block []string
		for _, line := range strings.Split(migration.SQL, "\n") {
			if block == nil && !strings.HasPrefix(line, "-- insert into step_starts_rollback") {
				continue
			}
			block = append(block, strings.TrimPrefix(line, "-- "))
			if line == mark {
				execSQL(t, pool, strings.Join(block, "\n"))
				return
			}
		}
		t.Fatalf("%s carries no way back from its insert into step_starts_rollback to %q", migration.Name, mark)
	}
	t.Fatalf("no embedded migration has version %d, the step records' roll forward", stepRecordsRollForward)
}

// rollForwardStepRecords is the newer image's boot over the store the earlier release left: its
// Migrate, which applies the roll forward the way back re-armed, and that one migration alone.
func rollForwardStepRecords(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	st, err := store.Open(t.Context(), pool.Config().ConnString())
	if err != nil {
		t.Fatalf("open the store: %v", err)
	}
	defer st.Close()
	if applied, err := st.Migrate(t.Context()); err != nil || applied != 1 {
		t.Fatalf("the roll forward's Migrate applied %d migrations, %v; want the one the way back re-armed", applied, err)
	}
}
