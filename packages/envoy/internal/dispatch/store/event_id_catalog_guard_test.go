package store

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// eventIDCatalogOffences reads the schema the migrations build, in no spelling at all, for what
// the safe horizon's proof needs of it (events/horizon.go, "Safe horizon"): only events.id's own
// default depends on events_id_seq, events has no child table and no partition, and events is a
// plain table. It answers one row per offence, and a row also when events.id has no
// nextval('events_id_seq') default, since the read would then see nothing.
const eventIDCatalogOffences = `
	with own as (
		select ad.oid from pg_attrdef ad join pg_attribute a on a.attrelid = ad.adrelid and a.attnum = ad.adnum
		where ad.adrelid = 'events'::regclass and a.attname = 'id'
			and pg_get_expr(ad.adbin, ad.adrelid) = 'nextval(''events_id_seq''::regclass)'
	)
	select pg_describe_object(d.classid, d.objid, d.objsubid) || ' depends on events_id_seq'
	from pg_depend d
	where d.refclassid = 'pg_class'::regclass and d.refobjid = 'events_id_seq'::regclass
		and not (d.classid = 'pg_attrdef'::regclass and d.objid in (select oid from own))
	union all
	select inhrelid::regclass::text || ' is a child or a partition of events'
	from pg_inherits where inhparent = 'events'::regclass
	union all
	select 'events is not a plain table (relkind ' || relkind::text || ')'
	from pg_class where oid = 'events'::regclass and relkind <> 'r'
	union all
	select 'events.id has no nextval(''events_id_seq'') default: this check reads nothing'
	where not exists (select 1 from own)`

// catalogQuerier is what eventIDCatalogOffences runs on: the store's pool, or a transaction.
type catalogQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// readEventIDCatalogOffences runs eventIDCatalogOffences on db and answers its rows.
func readEventIDCatalogOffences(ctx context.Context, t *testing.T, db catalogQuerier) []string {
	t.Helper()
	rows, err := db.Query(ctx, eventIDCatalogOffences)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var offences []string
	for rows.Next() {
		var offence string
		if err := rows.Scan(&offence); err != nil {
			t.Fatal(err)
		}
		offences = append(offences, offence)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return offences
}

// TestOnlyTheEventsIDDefaultDrawsFromItsSequence holds the migrated schema to
// eventIDCatalogOffences. A table made `like events including defaults` (or `including all`),
// `inherits (events)` or `partition of events` takes events' id default without its migration
// naming the sequence, and a write to it takes RowExclusiveLock on that table, not on events,
// which is the lock the horizon dates a writer by. A child's or a partition's rows are rows of
// events too, so the horizon would pass an id a write to one still holds open. A view or a
// SQL-body function that names the sequence depends on it here as well; a plpgsql body records no
// dependency, which TestNoMigrationDrawsAnEventIDOutsideTheInsertDefault reads the text for.
func TestOnlyTheEventsIDDefaultDrawsFromItsSequence(t *testing.T) {
	store := openEmptyTestStore(t)
	ctx := context.Background()
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// The read names each shape it is there to refuse, each made on the migrated schema itself in
	// a transaction that rolls back, as the keyed-write and decision-block guards show they see
	// what they guard (TestTheKeyedWriteGuardSeesWhatItGuards, TestTheDecisionBlockGuardSeesWhatItGuards).
	t.Run("the read names each shape that draws from the sequence or writes rows of events", func(t *testing.T) {
		tx, err := store.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		for _, statement := range []string{
			`create table rt_child () inherits (events)`,
			`create table rt_like (like events including defaults)`,
			`create view rt_view as select nextval('events_id_seq') as id`,
		} {
			if _, err := tx.Exec(ctx, statement); err != nil {
				t.Fatalf("%s: %v", statement, err)
			}
		}
		got := strings.Join(readEventIDCatalogOffences(ctx, t, tx), "\n")
		for _, want := range []string{
			"rt_child is a child or a partition of events",
			"default value for column id of table rt_child depends on events_id_seq",
			"default value for column id of table rt_like depends on events_id_seq",
			"rule _RETURN on view rt_view depends on events_id_seq",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("the read did not name %q; it read:\n%s", want, got)
			}
		}
	})

	if offences := readEventIDCatalogOffences(ctx, t, store.Pool); len(offences) > 0 {
		t.Fatalf("the migrated schema lets an event id be drawn, or an event row be written, outside an "+
			"insert into events:\n\t%s\nLet only events.id's default draw from events_id_seq, and keep events a "+
			"plain table with no child or partition: the safe horizon dates a writer by the RowExclusiveLock "+
			"its insert takes on events (events/horizon.go).", strings.Join(offences, "\n\t"))
	}
}
