package store

import (
	"context"
	"strings"
	"testing"
)

// TestOnlyTheEventsIDDefaultDrawsFromItsSequence reads the schema the migrations build, in no
// spelling at all, for what the safe horizon's proof needs of it (events/horizon.go, "Safe
// horizon"): only events.id's own default depends on events_id_seq, events has no child table and
// no partition, and events is a plain table. A table made `like events including defaults` (or
// `including all`), `inherits (events)` or `partition of events` takes that default without its
// migration naming the sequence, and a write to it takes RowExclusiveLock on that table, not on
// events, which is the lock the horizon dates a writer by. A child's or a partition's rows are
// rows of events too, so the horizon would pass an id a write to one still holds open. A view or
// a SQL-body function that names the sequence depends on it here as well; a plpgsql body records
// no dependency, which TestNoMigrationDrawsAnEventIDOutsideTheInsertDefault reads the text for.
// The check fails when events.id has no nextval('events_id_seq') default, since it would then
// read nothing.
func TestOnlyTheEventsIDDefaultDrawsFromItsSequence(t *testing.T) {
	store := openEmptyTestStore(t)
	ctx := context.Background()
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	rows, err := store.Pool.Query(ctx, `
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
		where not exists (select 1 from own)`)
	if err != nil {
		t.Fatal(err)
	}
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
	if len(offences) > 0 {
		t.Fatalf("the migrated schema lets an event id be drawn, or an event row be written, outside an "+
			"insert into events:\n\t%s\nLet only events.id's default draw from events_id_seq, and keep events a "+
			"plain table with no child or partition: the safe horizon dates a writer by the RowExclusiveLock "+
			"its insert takes on events (events/horizon.go).", strings.Join(offences, "\n\t"))
	}
}
