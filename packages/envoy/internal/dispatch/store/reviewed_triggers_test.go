package store

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/pgmigrate"
)

// reviewedGraphNodesTrigger is one of graph_nodes' triggers as pgmigrate's reviewedTriggers entry
// reviewed it: its definition as pg_get_triggerdef prints it, and the function it runs, whose
// source reviewedTriggerFunctions holds.
type reviewedGraphNodesTrigger struct{ definition, function string }

// reviewedGraphNodesTriggers are graph_nodes' triggers as pgmigrate's reviewedTriggers entries
// reviewed them, and reviewedTriggerFunctions the source of each function they run as
// pg_proc.prosrc holds it. The census admits a bounded write of graph_nodes above its table limit
// beside these triggers by name alone, and reads no trigger's body (pgmigrate's
// unreviewedTriggers), so a migration that changes a trigger or its function changes what was
// reviewed, and this test fails until a person reads the new text and writes it here and on that
// list.
var reviewedGraphNodesTriggers = map[string]reviewedGraphNodesTrigger{
	"graph_nodes_block_schema_written": {"CREATE TRIGGER graph_nodes_block_schema_written AFTER INSERT OR UPDATE ON public.graph_nodes FOR EACH ROW WHEN ((new.kind = ANY (ARRAY['supertag'::text, 'attribute'::text]))) EXECUTE FUNCTION block_schema_changed()", "block_schema_changed"},
	"graph_nodes_block_schema_removed": {"CREATE TRIGGER graph_nodes_block_schema_removed AFTER DELETE ON public.graph_nodes FOR EACH ROW WHEN ((old.kind = ANY (ARRAY['supertag'::text, 'attribute'::text]))) EXECUTE FUNCTION block_schema_changed()", "block_schema_changed"},
	"graph_nodes_head_marks":           {"CREATE TRIGGER graph_nodes_head_marks AFTER INSERT OR UPDATE ON public.graph_nodes FOR EACH ROW WHEN ((current_setting('dispatch.head_rows'::text, true) IS DISTINCT FROM 'kept'::text)) EXECUTE FUNCTION issue_head_marks_drop()", "issue_head_marks_drop"},
}

var reviewedTriggerFunctions = map[string]string{
	"block_schema_changed":  reviewedBlockSchemaChanged,
	"issue_head_marks_drop": reviewedIssueHeadMarksDrop,
}

// reviewedBlockSchemaChanged is block_schema_changed's source as 20261109051232_graph_supertags
// created it, the body both block schema triggers run: one notification on one channel with no
// payload, which Postgres folds into one per transaction, and no table read.
const reviewedBlockSchemaChanged = "\nbegin\n  perform pg_notify('dispatch_block_schema', '');\n  return null;\nend "

// reviewedIssueHeadMarksDrop is issue_head_marks_drop's source as 20261123140000_issue_tree_head
// created it, the body graph_nodes_head_marks runs: for each row, a probe of issue_head_nodes_node
// for the node, then, for each issue that probe and the node's address name, the issue's head fill
// lock in share mode and a delete of its issue_head_marks row by its primary key.
const reviewedIssueHeadMarksDrop = `
declare
  held text;
begin
  if tg_table_name = 'graph_tuples' then
    for held in
      select found.issue_key from (
        select head.issue_key from issue_head_nodes head
        where head.node_kind = new.from_kind and head.node_address = new.from_id
        union
        select head.issue_key from issue_head_nodes head
        where new.kind = 'part_of' and new.to_kind <> 'value'
          and head.node_kind = new.to_kind and head.node_address = new.to_id
        union
        select named.issue_key from (values
          (case when new.from_kind = 'issue' then new.from_id
                when new.from_kind in ('artifact', 'version', 'block') and new.from_id not like 'lens/%'
                  then split_part(new.from_id, '/', 1) end),
          (case when new.kind <> 'part_of' or new.to_kind = 'value' then null
                when new.to_kind = 'issue' then new.to_id
                when new.to_kind in ('artifact', 'version', 'block') and new.to_id not like 'lens/%'
                  then split_part(new.to_id, '/', 1) end)
        ) named (issue_key)
        where named.issue_key is not null
      ) found
      order by hashtext('issue:' || found.issue_key), found.issue_key
    loop
      perform pg_advisory_xact_lock_shared(1212498244, hashtext('issue:' || held));
      delete from issue_head_marks mark where mark.issue_key = held;
    end loop;
  else
    for held in
      select found.issue_key from (
        select head.issue_key from issue_head_nodes head
        where head.node_kind = new.kind and head.node_address = new.id
        union
        select case when new.kind = 'issue' then new.id else split_part(new.id, '/', 1) end
        where new.kind = 'issue' or (new.kind in ('artifact', 'version', 'block') and new.id not like 'lens/%')
      ) found
      order by hashtext('issue:' || found.issue_key), found.issue_key
    loop
      perform pg_advisory_xact_lock_shared(1212498244, hashtext('issue:' || held));
      delete from issue_head_marks mark where mark.issue_key = held;
    end loop;
  end if;
  return null;
end
`

// The graph_nodes triggers on pgmigrate's reviewed list, over a store this tree's runner
// migrated, are the triggers the list's entries reviewed: the list names exactly the enabled
// triggers graph_nodes holds, and each one's definition and its function's source are the reviewed
// text. The control changes the function's body in the test's own database, as a later migration
// could, and the same comparison must then name it.
func TestTheReviewedTriggersAreTheOnesTheStoreHolds(t *testing.T) {
	ctx := context.Background()
	store := openEmptyTestStore(t)
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	reviewed := pgmigrate.ReviewedTriggerNames("graph_nodes")
	if len(reviewed) == 0 {
		t.Fatal("pgmigrate reviews no trigger of graph_nodes, so this test compares nothing")
	}
	var held []string
	rows, err := store.Pool.Query(ctx, `
		select t.tgname::text from pg_trigger t
		where t.tgrelid = 'graph_nodes'::regclass and not t.tgisinternal and t.tgenabled <> 'D'
		order by t.tgname`)
	if err != nil {
		t.Fatalf("list graph_nodes' triggers: %v", err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("read a trigger's name: %v", err)
		}
		held = append(held, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("list graph_nodes' triggers: %v", err)
	}
	if !slices.Equal(slices.Sorted(slices.Values(reviewed)), held) {
		t.Fatalf("graph_nodes holds the enabled triggers %q and pgmigrate reviews %q: each must be reviewed, and reviewed as the store holds it", held, reviewed)
	}
	if differences := reviewedTriggerDifferences(t, ctx, store, reviewed); len(differences) != 0 {
		t.Fatalf("graph_nodes' reviewed triggers are not the ones the store holds:\n%s", differences)
	}

	// The first control: one trigger made again without its when clause, so it runs for every
	// node of the store, as a later migration could make it. The comparison must name it alone.
	if _, err := store.Pool.Exec(ctx, `drop trigger graph_nodes_block_schema_removed on graph_nodes;
		create trigger graph_nodes_block_schema_removed after delete on graph_nodes
		for each row execute function block_schema_changed()`); err != nil {
		t.Fatalf("control: make graph_nodes_block_schema_removed again: %v", err)
	}
	if differences := reviewedTriggerDifferences(t, ctx, store, reviewed); len(differences) != 1 || !strings.HasPrefix(differences[0], "graph_nodes_block_schema_removed: defined as") {
		t.Fatalf("control: with graph_nodes_block_schema_removed made without its when clause the comparison found %q, want that trigger named alone", differences)
	}

	// The second control: the function both triggers run, changed as a later migration could
	// change it. The comparison must name the function as well.
	if _, err := store.Pool.Exec(ctx, `create or replace function block_schema_changed() returns trigger language plpgsql as $$
begin
  perform pg_notify('dispatch_block_schema', (select count(*)::text from graph_tuples));
  return null;
end $$`); err != nil {
		t.Fatalf("control: change block_schema_changed: %v", err)
	}
	if differences := reviewedTriggerDifferences(t, ctx, store, reviewed); len(differences) != 2 ||
		!slices.ContainsFunc(differences, func(d string) bool { return strings.HasPrefix(d, "block_schema_changed: its source is") }) {
		t.Fatalf("control: with block_schema_changed reading graph_tuples the comparison found %q, want the trigger and the function named", differences)
	}
}

// reviewedTriggerDifferences is each of the reviewed triggers whose definition, or whose
// function's source, is not the reviewed text, and the function once where its source differs.
func reviewedTriggerDifferences(t *testing.T, ctx context.Context, store *Store, names []string) []string {
	t.Helper()
	var differences []string
	functions := map[string]bool{}
	for _, name := range names {
		want, ok := reviewedGraphNodesTriggers[name]
		if !ok {
			differences = append(differences, fmt.Sprintf("%s: pgmigrate reviews it and this test holds no reviewed text for it", name))
			continue
		}
		var definition, function, source string
		if err := store.Pool.QueryRow(ctx, `
			select pg_get_triggerdef(t.oid), p.proname::text, p.prosrc from pg_trigger t join pg_proc p on p.oid = t.tgfoid
			where t.tgrelid = 'graph_nodes'::regclass and t.tgname = $1`, name).Scan(&definition, &function, &source); err != nil {
			t.Fatalf("read the trigger %s: %v", name, err)
		}
		if definition != want.definition {
			differences = append(differences, fmt.Sprintf("%s: defined as %q, reviewed as %q", name, definition, want.definition))
		}
		if function != want.function {
			differences = append(differences, fmt.Sprintf("%s: runs %s, reviewed as running %s", name, function, want.function))
			continue
		}
		if reviewed := reviewedTriggerFunctions[function]; source != reviewed && !functions[function] {
			functions[function] = true
			differences = append(differences, fmt.Sprintf("%s: its source is %q, reviewed as %q", function, source, reviewed))
		}
	}
	return differences
}
