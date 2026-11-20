package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/sjawhar/envoy/internal/logtest"
	"github.com/sjawhar/envoy/internal/pgmigrate"
)

// The migrations that build embeddings partitioned by project beside the table and mirror its
// writes into it (20261120105330, 20261120105818).
const (
	embeddingsPartitionedVersion = 20261120105330
	embeddingsMirrorVersion      = 20261120105818
)

// The lines the partition half of the fill writes, as partitionEmbeddings writes them.
const (
	partitionCopyBeganLine    = "dispatch: the embeddings partition copy began"
	partitionCopyFinishedLine = "dispatch: the embeddings partition copy finished"
	partitionSwappedLine      = "dispatch: embeddings is partitioned by project: the copy is swapped in"
	partitionNothingLine      = "dispatch: embeddings is partitioned by project already: there is nothing to copy"
)

// openPartitionTestStore is a migrated database holding two projects, PARTA and PARTB, with
// issues issues each, whose rows the issue trigger wrote, and one window whose owner is gone,
// which belongs to no project.
func openPartitionTestStore(t *testing.T, issues int) (*Store, string) {
	t.Helper()
	ctx := context.Background()
	database := openEmptyTestStore(t)
	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	execAll(t, database,
		`insert into projects (key, name, default_assignee) values ('PARTA', 'A', 'alice@d.example'), ('PARTB', 'B', 'alice@d.example')`,
		fmt.Sprintf(`insert into issues (key, project_key, number, title, created_by, rank)
			select p || '-' || n, p, n, 'Issue ' || n || ' of ' || p, '{"kind":"user","id":"alice"}', lpad(n::text, 8, '0')
			  from unnest(array['PARTA', 'PARTB']) p cross join generate_series(1, %d) n`, issues),
		`update embeddings set embedding = array_fill(0.5::real, array[1536])::vector, model = 'dev:test', embedded_hash = content_hash
		  where kind = 'issue'`,
	)
	return database, seedOwnerlessWindow(t, database)
}

func execAll(t *testing.T, database *Store, statements ...string) {
	t.Helper()
	for _, statement := range statements {
		if _, err := database.Pool.Exec(context.Background(), statement); err != nil {
			t.Fatalf("%s: %v", strings.Join(strings.Fields(statement), " "), err)
		}
	}
}

// partitionOf is the table the row of kind and id is stored in.
func partitionOf(t *testing.T, database *Store, kind, id string) string {
	t.Helper()
	var table string
	if err := database.Pool.QueryRow(context.Background(),
		`select tableoid::regclass::text from embeddings where kind = $1 and id = $2`, kind, id).Scan(&table); err != nil {
		t.Fatalf("read the table %s %s is stored in: %v", kind, id, err)
	}
	return table
}

// embeddingsContent is every row of table, each as the text of all its columns, in key order:
// what a copy must carry over unchanged.
func embeddingsContent(t *testing.T, database *Store, table string) []string {
	t.Helper()
	rows, err := database.Pool.Query(context.Background(), `select (e.*)::text from `+table+` e order by kind, id`)
	if err != nil {
		t.Fatalf("read %s: %v", table, err)
	}
	defer rows.Close()
	var content []string
	for rows.Next() {
		var row string
		if err := rows.Scan(&row); err != nil {
			t.Fatalf("read %s: %v", table, err)
		}
		content = append(content, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read %s: %v", table, err)
	}
	return content
}

func requirePartitioned(t *testing.T, database *Store, want bool) {
	t.Helper()
	partitioned, err := database.embeddingsPartitioned(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if partitioned != want {
		t.Fatalf("embeddings partitioned = %t, want %t", partitioned, want)
	}
}

// The fill's second half copies every stored row into the table partitioned by project, each into
// its project's partition and a row of no project into the partition of none, unchanged, and
// swaps that table in under embeddings' own name and index names; a later boot's fill finds
// nothing to copy. The writers then write the partitioned table: a title edit reaches its row
// through embeddings_enqueue's new key, and a project made after the swap gets its partition.
func TestTheFillCopiesEveryRowIntoItsProjectsPartitionAndSwapsItIn(t *testing.T) {
	ctx := context.Background()
	database, ownerless := openPartitionTestStore(t, EmbeddingPartitionCopyBatch+30)
	before := embeddingsContent(t, database, "embeddings")
	requirePartitioned(t, database, false)
	records := &logtest.Recorder{}
	logtest.SetDefault(t, records)

	if _, err := database.FillEmbeddingProjectKeys(ctx); err != nil {
		t.Fatalf("fill: %v", err)
	}

	requirePartitioned(t, database, true)
	if after := embeddingsContent(t, database, "embeddings"); !slices.Equal(after, before) {
		t.Errorf("the swapped-in table holds %d rows that differ from the %d stored before the copy", len(after), len(before))
	}
	for _, row := range []struct{ kind, id, table string }{
		{"issue", "PARTA-1", "embeddings_project_parta"},
		{"issue", "PARTB-7", "embeddings_project_partb"},
		{"comment_block", ownerless, "embeddings_no_project"},
	} {
		if table := partitionOf(t, database, row.kind, row.id); table != row.table {
			t.Errorf("%s %s is stored in %s, want %s", row.kind, row.id, table, row.table)
		}
	}
	var indexes []string
	if err := database.Pool.QueryRow(ctx, `select array_agg(indexrelid::regclass::text order by 1) from pg_index where indrelid = 'embeddings'::regclass`).
		Scan(&indexes); err != nil {
		t.Fatal(err)
	}
	wantIndexes := []string{"embeddings_cosine", "embeddings_key", "embeddings_kind_project", "embeddings_pending",
		"embeddings_window_embedded", "embeddings_window_owner", "embeddings_window_unembedded"}
	if slices.Sort(indexes); !slices.Equal(indexes, wantIndexes) {
		t.Errorf("embeddings' indexes are %v, want %v", indexes, wantIndexes)
	}
	for _, gone := range []string{"embeddings_partitioned", "embeddings_partitioned_copy"} {
		var found *string
		if err := database.Pool.QueryRow(ctx, `select to_regclass($1)::text`, gone).Scan(&found); err != nil || found != nil {
			t.Errorf("%s is still there after the swap (%v, %v)", gone, found, err)
		}
	}
	oneLine(t, records, partitionCopyBeganLine)
	finishedLine := oneLine(t, records, partitionCopyFinishedLine)
	if rows := loggedAttr(t, finishedLine, "rows").Int64(); rows != int64(len(before)) {
		t.Errorf("the copy finished reporting %d rows read, want the %d stored", rows, len(before))
	}
	// Every row was written after the mirror's migration, so the mirror carried each over and the
	// copy inserted none of them; what it inserted is the rows the mirror had not reached.
	if inserted := loggedAttr(t, finishedLine, "inserted").Int64(); inserted != 0 {
		t.Errorf("the copy finished reporting %d rows inserted, want none: the mirror carried every row over", inserted)
	}
	oneLine(t, records, partitionSwappedLine)

	// The writers, on the swapped-in table.
	execAll(t, database,
		`update issues set title = 'A new title' where key = 'PARTA-1'`,
		`insert into projects (key, name, default_assignee) values ('LATER', 'Later', 'alice@d.example')`,
		`insert into issues (key, project_key, number, title, created_by, rank) values ('LATER-1', 'LATER', 1, 'Later one', '{"kind":"user","id":"alice"}', 'a')`,
	)
	var text, hash, embedded string
	if err := database.Pool.QueryRow(ctx, `select text_snapshot, content_hash, coalesce(embedded_hash, '') from embeddings where kind = 'issue' and id = 'PARTA-1'`).
		Scan(&text, &hash, &embedded); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "A new title") || hash == embedded {
		t.Errorf("after the title edit PARTA-1's row holds %q, hash %s embedded at %s: the enqueue did not meet the row on the partitioned key", text, hash, embedded)
	}
	if table := partitionOf(t, database, "issue", "LATER-1"); table != "embeddings_project_later" {
		t.Errorf("a project made after the swap stores its issue's row in %s, want embeddings_project_later", table)
	}
	var count int
	if err := database.Pool.QueryRow(ctx, `select count(*) from embeddings where kind = 'issue' and id = 'PARTA-1'`).Scan(&count); err != nil || count != 1 {
		t.Errorf("PARTA-1 has %d rows after its edit, want 1 (%v)", count, err)
	}

	after := &logtest.Recorder{}
	restore := logtest.Swap(t, after)
	defer restore()
	if _, err := database.FillEmbeddingProjectKeys(ctx); err != nil {
		t.Fatalf("fill again: %v", err)
	}
	oneLine(t, after, partitionNothingLine)
	if done, err := database.EmbeddingProjectKeyFillDone(ctx); err != nil || !done {
		t.Errorf("EmbeddingProjectKeyFillDone = %t, %v after the swap, want true", done, err)
	}
}

// The labelling moves a row it labels from the partition of no project into its project's, by
// the UPDATE that labels it, on the partitioned table as on the one before it: a row stored before
// its project was known is in the partition of none, and the fill's pass is what puts it where a
// search of its project reads.
func TestTheFillMovesARowFromTheNoProjectPartitionIntoItsProjects(t *testing.T) {
	ctx := context.Background()
	database, _ := openPartitionTestStore(t, 3)
	if _, err := database.FillEmbeddingProjectKeys(ctx); err != nil {
		t.Fatalf("partition: %v", err)
	}
	// A row stored before its project was known: the label trigger would label it again as it
	// lands in the partition of none, so it is off while the row is made.
	execAll(t, database,
		`alter table embeddings disable trigger embeddings_project_key_label`,
		`update embeddings set project_key = null where kind = 'issue' and id = 'PARTB-2'`,
		`alter table embeddings enable trigger embeddings_project_key_label`,
		`update embeddings_project_key_backfill set kind = '', id = '', done = false`,
	)
	if table := partitionOf(t, database, "issue", "PARTB-2"); table != "embeddings_no_project" {
		t.Fatalf("PARTB-2 unlabelled is stored in %s, want embeddings_no_project", table)
	}
	labelled, err := database.FillEmbeddingProjectKeys(ctx)
	if err != nil {
		t.Fatalf("fill: %v", err)
	}
	if labelled != 1 {
		t.Errorf("the fill labelled %d rows, want PARTB-2's", labelled)
	}
	if table := partitionOf(t, database, "issue", "PARTB-2"); table != "embeddings_project_partb" {
		t.Errorf("PARTB-2 labelled is stored in %s, want embeddings_project_partb", table)
	}
}

// Every write of embeddings made while the copy runs reaches the table beside it: an insert, an
// update of the text, an update that unlabels a row and one that labels it, each moving it
// between partitions, a delete and a truncate. Compared column for column with the table written.
func TestTheMirrorCarriesEveryWriteMadeBesideTheCopy(t *testing.T) {
	database, ownerless := openPartitionTestStore(t, 2)
	requireMirrored := func(what string) {
		t.Helper()
		if stored, mirrored := embeddingsContent(t, database, "embeddings"), embeddingsContent(t, database, "embeddings_partitioned"); !slices.Equal(stored, mirrored) {
			t.Fatalf("after %s embeddings holds\n\t%v\nand embeddings_partitioned\n\t%v", what, stored, mirrored)
		}
	}
	requireMirrored("the seed")
	execAll(t, database, `update issues set title = 'Retitled' where key = 'PARTA-2'`)
	requireMirrored("a title edit")
	execAll(t, database, `update embeddings set project_key = null where id = 'PARTB-1'`)
	if table := partitionOfIn(t, database, "embeddings_partitioned", "PARTB-1"); table != "embeddings_no_project" {
		t.Errorf("PARTB-1 unlabelled is mirrored in %s, want embeddings_no_project", table)
	}
	execAll(t, database, `update embeddings set project_key = 'PARTB' where id = 'PARTB-1'`)
	if table := partitionOfIn(t, database, "embeddings_partitioned", "PARTB-1"); table != "embeddings_project_partb" {
		t.Errorf("PARTB-1 labelled again is mirrored in %s, want embeddings_project_partb", table)
	}
	requireMirrored("an unlabel and a label")
	execAll(t, database, fmt.Sprintf(`delete from embeddings where id = '%s'`, ownerless))
	requireMirrored("a delete")
	execAll(t, database, `truncate embeddings`)
	requireMirrored("a truncate")
}

func partitionOfIn(t *testing.T, database *Store, table, id string) string {
	t.Helper()
	var partition string
	if err := database.Pool.QueryRow(context.Background(),
		`select tableoid::regclass::text from `+table+` where id = $1`, id).Scan(&partition); err != nil {
		t.Fatalf("read where %s holds %s: %v", table, id, err)
	}
	return partition
}

// A copy batch holds the rows it read until it commits: a write of one of them waits, and then
// lands on the copied row, so the write is what the partitioned table keeps and not the older
// copy. The control is the same write against a batch that has committed, which does not wait.
func TestACopyBatchHoldsAWriteOfItsRowsUntilItCommits(t *testing.T) {
	ctx := context.Background()
	database, _ := openPartitionTestStore(t, 2)
	execAll(t, database, `delete from embeddings_partitioned`)
	batch, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer batch.Rollback(ctx)
	var read, copied int64
	var finished bool
	if err := batch.QueryRow(ctx, copyEmbeddingsBatchQuery, "", "", EmbeddingPartitionCopyBatch).Scan(&read, &copied, &finished); err != nil {
		t.Fatalf("copy a batch: %v", err)
	}
	wrote := make(chan error, 1)
	go func() {
		_, err := database.Pool.Exec(ctx, `update embeddings set text_snapshot = 'written while the batch held it' where id = 'PARTA-1'`)
		wrote <- err
	}()
	if waited := waitForALockWaiter(t, database, 5*time.Second); !waited {
		t.Fatal("the write of a row the open batch read did not wait for the batch")
	}
	select {
	case err := <-wrote:
		t.Fatalf("the write finished before the batch committed (%v)", err)
	default:
	}
	if err := batch.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-wrote; err != nil {
		t.Fatalf("the write: %v", err)
	}
	var text string
	if err := database.Pool.QueryRow(ctx, `select text_snapshot from embeddings_partitioned where id = 'PARTA-1'`).Scan(&text); err != nil {
		t.Fatal(err)
	}
	if text != "written while the batch held it" {
		t.Errorf("embeddings_partitioned holds %q for PARTA-1, want the write that waited for the batch", text)
	}
	// The control: nothing holds the row now, so the same write does not wait.
	done := make(chan error, 1)
	go func() {
		_, err := database.Pool.Exec(ctx, `update embeddings set text_snapshot = 'written after' where id = 'PARTA-1'`)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a write with no batch open waited: the wait above proves nothing")
	}
}

// waitForALockWaiter answers whether some backend of the database waits for a lock within bound.
func waitForALockWaiter(t *testing.T, database *Store, bound time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(bound)
	for time.Now().Before(deadline) {
		var waiting int
		if err := database.Pool.QueryRow(context.Background(),
			`select count(*) from pg_stat_activity where datname = current_database() and wait_event_type = 'Lock'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// The swap refuses, and changes nothing, when the copy holds fewer rows than embeddings.
func TestTheSwapRefusesACopyThatHoldsFewerRows(t *testing.T) {
	ctx := context.Background()
	database, _ := openPartitionTestStore(t, 2)
	execAll(t, database,
		`delete from embeddings_partitioned where id = 'PARTA-1'`,
		`update embeddings_partitioned_copy set done = true`,
	)
	_, err := database.FillEmbeddingProjectKeys(ctx)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "P0001" || !strings.Contains(pgErr.Message, "the copy is not complete") {
		t.Fatalf("fill over a short copy = %v, want the swap's refusal", err)
	}
	requirePartitioned(t, database, false)
	if done, err := database.EmbeddingProjectKeyFillDone(ctx); err != nil || done {
		t.Errorf("EmbeddingProjectKeyFillDone = %t, %v, want false while embeddings is not swapped", done, err)
	}
}

// Every project has its partition from the transaction that inserts it, and a row of a project
// that does not exist is refused rather than kept anywhere, before the swap (by the mirror) and
// after it.
func TestEveryProjectHasItsPartitionAndARowOfNoProjectThatExistsIsRefused(t *testing.T) {
	ctx := context.Background()
	database, _ := openPartitionTestStore(t, 1)
	tx, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `insert into projects (key, name, default_assignee) values ('INTX', 'In a transaction', 'alice@d.example')`); err != nil {
		t.Fatal(err)
	}
	var partition *string
	if err := tx.QueryRow(ctx, `select to_regclass('embeddings_project_intx')::text`).Scan(&partition); err != nil || partition == nil {
		t.Errorf("INTX has no partition inside the transaction that inserts it (%v)", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := database.Pool.QueryRow(ctx, `select to_regclass('embeddings_project_intx')::text`).Scan(&partition); err != nil || partition != nil {
		t.Errorf("INTX's partition outlived the rolled-back insert of INTX (%v)", err)
	}
	refused := func(when string) {
		t.Helper()
		_, err := database.Pool.Exec(ctx, `insert into embeddings (kind, id, text_snapshot, content_hash, project_key) values ('issue', 'NOPE-1', 'x', 'x', 'NOPE')`)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
			t.Errorf("%s, a row of project NOPE = %v, want SQLSTATE 23514 (no partition)", when, err)
		}
	}
	refused("before the swap")
	if _, err := database.FillEmbeddingProjectKeys(ctx); err != nil {
		t.Fatal(err)
	}
	refused("after the swap")
}

// The two migrations take the census's size limit as their shapes say they must: the partitioned
// table's migration locks no table above ACCESS SHARE but projects, and the mirror's is exempt
// from the limit however large embeddings is. The limit is injected just above projects' size,
// over a database whose embeddings is far above it. The control is an in-place rename of
// embeddings over the same database, which the census refuses at that limit.
func TestCensusAdmitsThePartitionMigrationsOverALargeEmbeddings(t *testing.T) {
	ctx := context.Background()
	for _, migration := range []struct {
		file string
		want []string
	}{
		{"20261120105330_embeddings_partitioned.up.sql", []string{"embeddings_partitioned", "embeddings_partitioned_copy", "projects"}},
		{"20261120105818_embeddings_partitioned_mirror.up.sql", []string{"embeddings"}},
	} {
		sql, err := migrationFiles.ReadFile("migrations/" + migration.file)
		if err != nil {
			t.Fatal(err)
		}
		if touched := pgmigrate.TouchedTables(string(sql)); !slices.Equal(touched, migration.want) {
			t.Errorf("%s touches %v, want %v", migration.file, touched, migration.want)
		}
	}

	store := openEmptyTestStore(t)
	migrateThrough(t, store, embeddingsPartitionedVersion-1)
	execAll(t, store,
		`insert into projects (key, name, default_assignee) values ('BIG', 'Big', 'alice@d.example')`,
		`insert into issues (key, project_key, number, title, created_by, rank)
			select 'BIG-' || n, 'BIG', n, repeat('Issue ' || n || ' ', 40), '{"kind":"user","id":"alice"}', lpad(n::text, 8, '0')
			  from generate_series(1, 2000) n`,
	)
	var projects, embeddings int64
	if err := store.Pool.QueryRow(ctx, `select pg_total_relation_size('projects'), pg_total_relation_size('embeddings')`).Scan(&projects, &embeddings); err != nil {
		t.Fatal(err)
	}
	limit := projects + 1
	if embeddings <= 4*limit {
		t.Fatalf("embeddings is %d bytes against a limit of %d: the census would measure nothing above it", embeddings, limit)
	}
	report, err := census(ctx, store.Pool.Config().ConnString(), migrationsThrough(t, embeddingsMirrorVersion), pgmigrate.CensusOptions{TableLimit: limit})
	if err != nil {
		t.Fatalf("census: %v", err)
	}
	if report.Refused() {
		t.Errorf("the partition migrations were refused over a %d-byte embeddings at a %d-byte limit: %v", embeddings, limit, refusals(report))
	}
	if len(report.Pending) != 2 || report.Pending[1].TableSizeExemption != "create function and create trigger" {
		t.Errorf("pending = %+v, want the two migrations, the mirror's exempt as its function and triggers", report.Pending)
	}

	inPlace := migrationsThrough(t, embeddingsPartitionedVersion-1)
	inPlace["migrations/20261120105330_embeddings_partitioned.up.sql"] = &fstest.MapFile{Data: []byte("alter table embeddings rename to embeddings_unpartitioned;")}
	inPlace["migrations/20261120105330_embeddings_partitioned.census.sql"] = &fstest.MapFile{Data: []byte("select 0;")}
	control, err := census(ctx, store.Pool.Config().ConnString(), inPlace, pgmigrate.CensusOptions{TableLimit: limit})
	if err != nil {
		t.Fatalf("census of the in-place shape: %v", err)
	}
	if !control.Refused() {
		t.Errorf("the census admitted a rename of a %d-byte embeddings at a %d-byte limit: this test's admission proves nothing", embeddings, limit)
	}
}
