package store

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// evalChartsAnswerVersion gives eval_versions its declared charts and its answer, and states
// evals_current again with them (dispatch://INFRA-2918, R20).
const evalChartsAnswerVersion = 20261123024730

// The migration over versions the release before it wrote: its census counts 0; every version
// then reads no charts (an empty list) and no answer (an empty text) through eval_versions and
// evals_current, and every other column as it was; an insert that names neither column, as the
// release before writes one during the roll, holds both defaults; one that names them holds them
// and is the eval's current version; and charts that are no list are refused by
// eval_versions_charts_array.
func TestTheChartsMigrationGivesEveryVersionNoChartsAndNoAnswer(t *testing.T) {
	ctx := context.Background()
	store := openEmptyTestStore(t)
	if err := store.migrate(ctx, migrationsThrough(t, evalChartsAnswerVersion-1)); err != nil {
		t.Fatalf("migrate through %d: %v", evalChartsAnswerVersion-1, err)
	}
	// The insert of the release before: every column it names, and neither of the new two.
	const releaseBeforeInsert = `
		insert into eval_versions (eval_id, version, project_key, slug, name, kind, source, written_via, written_by)
		values ($1, $2, 'EVAL', $3, $4, 'experiment', 'logged', 'register', '{"kind":"user","id":"alice"}')`
	if _, err := store.Pool.Exec(ctx, `insert into projects (key, name, default_assignee) values ('EVAL', 'Evals', 'alice')`); err != nil {
		t.Fatalf("seed the project: %v", err)
	}
	for _, version := range []struct {
		version    int
		slug, name string
	}{{1, "wait-time", "Wait"}, {2, "wait-time", "Wait time"}, {1, "review-rounds", "Review rounds"}} {
		if _, err := store.Pool.Exec(ctx, releaseBeforeInsert, "EVAL/"+version.slug, version.version, version.slug, version.name); err != nil {
			t.Fatalf("seed version %d of %s as the release before wrote it: %v", version.version, version.slug, err)
		}
	}
	const versionsBut = `select jsonb_agg(to_jsonb(v) - 'charts' - 'answer' order by v.eval_id, v.version)::text from eval_versions v`
	const currentBut = `select jsonb_agg(to_jsonb(v) - 'charts' - 'answer' order by v.eval_id)::text from evals_current v`
	versions, current := readText(t, store, versionsBut), readText(t, store, currentBut)

	if got := censusCount(t, store, evalChartsAnswerVersion); got != 0 {
		t.Fatalf("the census at %d = %d, want 0", evalChartsAnswerVersion-1, got)
	}
	if err := store.migrate(ctx, migrationsThrough(t, evalChartsAnswerVersion)); err != nil {
		t.Fatalf("migrate to %d: %v", evalChartsAnswerVersion, err)
	}

	for query, want := range map[string]string{
		`select string_agg(eval_id || ' v' || version || ' ' || charts::text || ' ' || quote_literal(answer), ', ' order by eval_id, version) from eval_versions`: `EVAL/review-rounds v1 [] '', EVAL/wait-time v1 [] '', EVAL/wait-time v2 [] ''`,
		`select string_agg(eval_id || ' v' || version || ' ' || charts::text || ' ' || quote_literal(answer), ', ' order by eval_id) from evals_current`:          `EVAL/review-rounds v1 [] '', EVAL/wait-time v2 [] ''`,
		versionsBut: versions,
		currentBut:  current,
	} {
		if got := readText(t, store, query); got != want {
			t.Errorf("after %d, %s\n= %s\nwant %s", evalChartsAnswerVersion, query, got, want)
		}
	}

	if _, err := store.Pool.Exec(ctx, releaseBeforeInsert, "EVAL/review-rounds", 2, "review-rounds", "Rounds"); err != nil {
		t.Fatalf("insert a version as the release before writes one: %v", err)
	}
	if _, err := store.Pool.Exec(ctx, `
		insert into eval_versions (eval_id, version, project_key, slug, name, kind, source, written_via, written_by, charts, answer)
		values ('EVAL/wait-time', 3, 'EVAL', 'wait-time', 'Wait time', 'experiment', 'logged', 'register', '{"kind":"user","id":"alice"}',
			'[{"id": "wait", "kind": "xy", "title": "Wait", "y": {"series": "wait"}}]', 'Most waits end within an hour.')
	`); err != nil {
		t.Fatalf("insert a version with charts and an answer: %v", err)
	}
	const newest = `select string_agg(eval_id || ' v' || version || ' ' || jsonb_array_length(charts) || ' ' || quote_literal(answer), ', ' order by eval_id) from evals_current`
	if got, want := readText(t, store, newest), `EVAL/review-rounds v2 0 '', EVAL/wait-time v3 1 'Most waits end within an hour.'`; got != want {
		t.Errorf("evals_current after the two inserts = %s, want %s", got, want)
	}

	_, err := store.Pool.Exec(ctx, `
		insert into eval_versions (eval_id, version, project_key, slug, kind, source, written_via, written_by, charts)
		values ('EVAL/wait-time', 4, 'EVAL', 'wait-time', 'experiment', 'logged', 'register', '{"kind":"user","id":"alice"}', '{"id": "wait"}')
	`)
	var refused *pgconn.PgError
	if !errors.As(err, &refused) || refused.Code != "23514" || refused.ConstraintName != "eval_versions_charts_array" {
		t.Fatalf("an insert of charts that are an object answered %v, want check_violation of eval_versions_charts_array", err)
	}
	t.Logf("census 0; every version reads no charts and no answer; the release before's insert holds the defaults; an object refused: %s", refused.Message)
}
