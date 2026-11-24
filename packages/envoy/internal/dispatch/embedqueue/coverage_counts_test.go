package embedqueue

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/embed"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// countingTrigger is one trigger the coverage counts are kept by, as pg_trigger holds it: the
// table it is on, its name, whether it fires for each row or once for each statement, the events
// it fires after, in the order insert, update, delete, truncate, and the columns an update must
// name to fire it, sorted, nil for an update of any column.
type countingTrigger struct {
	table, name string
	row         bool
	events      []string
	columns     []string
}

// String is the trigger as a failure names it.
func (trigger countingTrigger) String() string {
	level := "statement"
	if trigger.row {
		level = "row"
	}
	events := strings.Join(trigger.events, " or ")
	if len(trigger.columns) > 0 {
		events += " (update of " + strings.Join(trigger.columns, ", ") + ")"
	}
	return fmt.Sprintf("%s on %s: after %s, for each %s", trigger.name, trigger.table, events, level)
}

// countingTriggers is every trigger the coverage counts are kept by (plan D7, "Triggers"), each
// named search_coverage_<table>_<what>: the name is the contract this test holds the migration
// to, so a trigger the migration names otherwise is missing here and one it adds that this list
// does not hold is an extra. On embeddings, three statement triggers, after insert, after update
// and after delete, since Postgres takes transition tables only on a trigger of one event; on
// components, artifact_versions, check_results and handoffs two each, after insert and after
// delete. The row triggers of issues, artifacts and artifact_versions refresh the state row of
// the issue a write concerns, each fired by an update of the columns the plan names that issue's
// class is read from: an issue's title and created_at, an artifact's issue_key and is_primary,
// and a version's markdown, the issue's spec where the artifact is its primary document. Each
// of those seven tables has a statement trigger after truncate, and search_coverage_issues has
// a row trigger of its own that writes the old class minus and the new class plus as delta
// rows. route_roster is read directly and has none.
var countingTriggers = []countingTrigger{
	{"embeddings", "search_coverage_embeddings_insert", false, []string{"insert"}, nil},
	{"embeddings", "search_coverage_embeddings_update", false, []string{"update"}, nil},
	{"embeddings", "search_coverage_embeddings_delete", false, []string{"delete"}, nil},
	{"embeddings", "search_coverage_embeddings_truncate", false, []string{"truncate"}, nil},
	{"components", "search_coverage_components_insert", false, []string{"insert"}, nil},
	{"components", "search_coverage_components_delete", false, []string{"delete"}, nil},
	{"components", "search_coverage_components_truncate", false, []string{"truncate"}, nil},
	{"artifact_versions", "search_coverage_artifact_versions_insert", false, []string{"insert"}, nil},
	{"artifact_versions", "search_coverage_artifact_versions_delete", false, []string{"delete"}, nil},
	{"artifact_versions", "search_coverage_artifact_versions_rows", true, []string{"insert", "update", "delete"}, []string{"markdown"}},
	{"artifact_versions", "search_coverage_artifact_versions_truncate", false, []string{"truncate"}, nil},
	{"check_results", "search_coverage_check_results_insert", false, []string{"insert"}, nil},
	{"check_results", "search_coverage_check_results_delete", false, []string{"delete"}, nil},
	{"check_results", "search_coverage_check_results_truncate", false, []string{"truncate"}, nil},
	{"handoffs", "search_coverage_handoffs_insert", false, []string{"insert"}, nil},
	{"handoffs", "search_coverage_handoffs_delete", false, []string{"delete"}, nil},
	{"handoffs", "search_coverage_handoffs_truncate", false, []string{"truncate"}, nil},
	{"issues", "search_coverage_issues_rows", true, []string{"insert", "update", "delete"}, []string{"created_at", "title"}},
	{"issues", "search_coverage_issues_truncate", false, []string{"truncate"}, nil},
	{"artifacts", "search_coverage_artifacts_rows", true, []string{"insert", "update", "delete"}, []string{"is_primary", "issue_key"}},
	{"artifacts", "search_coverage_artifacts_truncate", false, []string{"truncate"}, nil},
	{"search_coverage_issues", "search_coverage_issue_classes", true, []string{"insert", "update", "delete"}, nil},
}

// The bits of pg_trigger.tgtype (Postgres's catalog/pg_trigger.h, TRIGGER_TYPE_*).
const (
	triggerTypeRow      = 1 << 0
	triggerTypeBefore   = 1 << 1
	triggerTypeInsert   = 1 << 2
	triggerTypeDelete   = 1 << 3
	triggerTypeUpdate   = 1 << 4
	triggerTypeTruncate = 1 << 5
	triggerTypeInstead  = 1 << 6
)

// installedCountingTrigger is one trigger whose name begins search_coverage_ as pg_trigger holds
// it: what countingTrigger names, its timing, and tgenabled, 'O' for a trigger that fires in the
// ordinary mode of session_replication_role.
type installedCountingTrigger struct {
	countingTrigger
	timing  string
	enabled string
}

// installedCountingTriggers reads every trigger of the database whose name begins
// search_coverage_, by table and name.
func installedCountingTriggers(t *testing.T, database *store.Store) map[string]installedCountingTrigger {
	t.Helper()
	rows, err := database.Pool.Query(context.Background(), `
		select c.relname::text, t.tgname::text, t.tgtype::int, t.tgenabled::text,
		       array(select a.attname::text from pg_attribute a
		              where a.attrelid = t.tgrelid and a.attnum = any(t.tgattr::int2[])
		              order by a.attname)
		  from pg_trigger t join pg_class c on c.oid = t.tgrelid
		 where not t.tgisinternal and t.tgname like 'search\_coverage\_%'`)
	if err != nil {
		t.Fatalf("read the counting triggers: %v", err)
	}
	installed, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (installedCountingTrigger, error) {
		var trigger installedCountingTrigger
		var tgtype int
		if err := row.Scan(&trigger.table, &trigger.name, &tgtype, &trigger.enabled, &trigger.columns); err != nil {
			return trigger, err
		}
		trigger.row = tgtype&triggerTypeRow != 0
		switch {
		case tgtype&triggerTypeInstead != 0:
			trigger.timing = "instead of"
		case tgtype&triggerTypeBefore != 0:
			trigger.timing = "before"
		default:
			trigger.timing = "after"
		}
		for _, event := range []struct {
			bit  int
			name string
		}{{triggerTypeInsert, "insert"}, {triggerTypeUpdate, "update"}, {triggerTypeDelete, "delete"}, {triggerTypeTruncate, "truncate"}} {
			if tgtype&event.bit != 0 {
				trigger.events = append(trigger.events, event.name)
			}
		}
		if len(trigger.columns) == 0 {
			trigger.columns = nil
		}
		return trigger, nil
	})
	if err != nil {
		t.Fatalf("scan the counting triggers: %v", err)
	}
	byName := map[string]installedCountingTrigger{}
	for _, trigger := range installed {
		byName[trigger.table+"."+trigger.name] = trigger
	}
	return byName
}

// countingTriggersDiffer names every way the installed triggers differ from countingTriggers:
// each listed trigger missing, each one whose level, timing, events or update columns differ,
// each one not enabled in the ordinary mode, and each trigger named search_coverage_ that the
// list does not hold. It is empty when they agree.
func countingTriggersDiffer(installed map[string]installedCountingTrigger) []string {
	var differences []string
	listed := map[string]bool{}
	for _, want := range countingTriggers {
		key := want.table + "." + want.name
		listed[key] = true
		got, found := installed[key]
		switch {
		case !found:
			differences = append(differences, "missing: "+want.String())
		case got.timing != "after" || got.row != want.row || !slices.Equal(got.events, want.events) || !slices.Equal(got.columns, want.columns):
			differences = append(differences, fmt.Sprintf("%s is %s (%s), want %s", key, got.countingTrigger, got.timing, want))
		case got.enabled != "O":
			differences = append(differences, fmt.Sprintf("%s has tgenabled %q, want 'O': it must fire in the ordinary mode", key, got.enabled))
		}
	}
	for _, key := range slices.Sorted(maps.Keys(installed)) {
		if !listed[key] {
			differences = append(differences, "not in countingTriggers: "+installed[key].countingTrigger.String())
		}
	}
	return differences
}

// TestEveryCountedTableCarriesItsCountingTrigger holds R7h: every table the coverage counts are
// read from carries the triggers that keep them (countingTriggers), each an ordinary trigger
// that fires after its events and is enabled, on a fresh database and again after
// withTableTriggersOff has turned each such table's triggers off and back on, as the tests that
// seed past the triggers do. The triggers are ordinary, not `enable always`, since the migration
// census admits no ENABLE ALWAYS TRIGGER (internal/pgmigrate/exempt.go): a write under
// session_replication_role = replica fires none of them, and the recount is what follows it.
func TestEveryCountedTableCarriesItsCountingTrigger(t *testing.T) {
	database := storetest.Open(t)
	if differences := countingTriggersDiffer(installedCountingTriggers(t, database)); len(differences) > 0 {
		t.Fatalf("on a fresh database the counting triggers differ from the %d listed:\n%s",
			len(countingTriggers), strings.Join(differences, "\n"))
	}

	var tables []string
	for _, trigger := range countingTriggers {
		if !slices.Contains(tables, trigger.table) {
			tables = append(tables, trigger.table)
		}
	}
	for _, table := range tables {
		withTableTriggersOff(t, database, table, func(querier) {})
	}
	if differences := countingTriggersDiffer(installedCountingTriggers(t, database)); len(differences) > 0 {
		t.Fatalf("after withTableTriggersOff on %s the counting triggers differ from the %d listed:\n%s",
			strings.Join(tables, ", "), len(countingTriggers), strings.Join(differences, "\n"))
	}
}

// keptReport is the report the kept counts give for the identity $1, by kind: items, current and
// failed, summed over the base rows and the delta rows not yet folded (plan D7, "What the read
// answers"), for each kind holding an item.
const keptReport = `
	select kind, sum(items)::bigint,
	       coalesce(sum(items) filter (where matched and $1::text <> '' and model = $1::text), 0)::bigint,
	       (coalesce(sum(items) filter (where dead), 0)
	        - coalesce(sum(items) filter (where dead and matched and $1::text <> '' and model = $1::text), 0))::bigint
	from (select kind, model, matched, dead, items from search_coverage_counts
	      union all
	      select kind, model, matched, dead, items from search_coverage_deltas) kept
	group by kind
	having sum(items) <> 0
	order by kind`

// mainsReport is what mainCoverageQuery answers for the identity $1, by kind, in keptReport's
// shape: every kind but session, which route_roster's one row gives and no count keeps, holding an
// item.
const mainsReport = `
	select kind, items, current, failed
	from (` + mainCoverageQuery + `) main (kind, covered, items, current, failed, waited)
	where kind <> 'session' and items <> 0
	order by kind`

// requireKeptCountsAreMains fails the test unless the kept counts (keptReport) answer what
// mainCoverageQuery answers, for the configured identity, for none and for another, all in one
// REPEATABLE READ snapshot, printing both answers where they differ.
func requireKeptCountsAreMains(t *testing.T, database *store.Store, stage string) {
	t.Helper()
	ctx := context.Background()
	tx, err := database.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatalf("%s: begin the snapshot both reports read: %v", stage, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	report := func(statement, identity string) []string {
		t.Helper()
		rows, err := tx.Query(ctx, statement, identity)
		if err != nil {
			t.Fatalf("%s: read a report for %q: %v", stage, identity, err)
		}
		lines, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (string, error) {
			var kind string
			var items, current, failed int64
			err := row.Scan(&kind, &items, &current, &failed)
			return fmt.Sprintf("%s: items %d, current %d, failed %d", kind, items, current, failed), err
		})
		if err != nil {
			t.Fatalf("%s: read a report for %q: %v", stage, identity, err)
		}
		return lines
	}
	for _, identity := range []string{configuredIdentity, "", foreignIdentity} {
		kept, mains := report(keptReport, identity), report(mainsReport, identity)
		if !slices.Equal(kept, mains) {
			t.Errorf("%s, identity %q: the kept counts answer\n  %s\nwhere main's statement answers\n  %s",
				stage, identity, strings.Join(kept, "\n  "), strings.Join(mains, "\n  "))
		}
	}
}

// TestTwoWritesToOneIssuesInputsLeaveItsStateRowCurrent holds R7's "after every write that fires
// the triggers" where two transactions change two inputs of one issue at once: A edits its title
// and B rewrites its spec's latest version in place, B's statement starting while A, which has
// refreshed the issue, is still open. The issue's vector is of the text both writes make
// together, so it is current only once the issue's state row is computed from both. B's refresh
// waits for the issue's state row until A commits and only then computes the row, so it reads A's
// title; a refresh that computed before it waited would read the title A had not committed yet
// and write, after A's commit, a class with no vector of the issue's text.
func TestTwoWritesToOneIssuesInputsLeaveItsStateRowCurrent(t *testing.T) {
	database := storetest.Open(t)
	ctx := context.Background()
	const key, title, spec = "TWO-1", "The title A writes", "## Summary\n\nThe spec B writes."
	seedIssue(t, database, "TWO", key, " ")
	document := seedSpec(t, database.Pool, "TWO", key, "\t")
	var version string
	if err := database.Pool.QueryRow(ctx, `select id::text from artifact_versions where artifact_id = $1`, document).Scan(&version); err != nil {
		t.Fatalf("read the spec's version: %v", err)
	}
	updateRow(t, database.Pool, "issue", key,
		`embedding = $3::vector, model = $4, embedded_at = now(),
		 embedded_hash = encode(sha256(convert_to(issue_embedding_text($5, $6), 'UTF8')), 'hex')`,
		embed.Literal(oneVector()), configuredIdentity, title, spec)
	requireKeptCountsAreMains(t, database, "before the writes")

	a, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin A: %v", err)
	}
	defer func() { _ = a.Rollback(ctx) }()
	b, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin B: %v", err)
	}
	defer func() { _ = b.Rollback(ctx) }()
	var bPID uint32
	if err := b.QueryRow(ctx, `select pg_backend_pid()`).Scan(&bPID); err != nil {
		t.Fatalf("read B's backend: %v", err)
	}
	mustExec(t, a, `update issues set title = $2 where key = $1`, key, title)
	rewritten := make(chan error, 1)
	go func() {
		_, err := b.Exec(ctx, `update artifact_versions set markdown = $2 where id = $1`, version, spec)
		rewritten <- err
	}()
	if err, early := storetest.WaitForQueuedBehind(t, a, bPID, rewritten, 10*time.Second); early {
		t.Fatalf("B's rewrite of the spec ended (%v) before A committed: it never waited on A", err)
	}
	if err := a.Commit(ctx); err != nil {
		t.Fatalf("commit A: %v", err)
	}
	select {
	case err := <-rewritten:
		if err != nil {
			t.Fatalf("B's rewrite of the spec: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("B's rewrite of the spec did not end within 10s of A's commit")
	}
	if err := b.Commit(ctx); err != nil {
		t.Fatalf("commit B: %v", err)
	}
	requireKeptCountsAreMains(t, database, "after A and B committed")
}
