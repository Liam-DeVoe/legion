package store

// TypeMigrations are the migrations that write the store's type nodes, by file name in the
// migrations directory, in the order the runner applies them: the seed, then each migration that
// has changed a type since. Each appends the schema's own supertag and attribute nodes to
// graph_nodes and their tuples to graph_tuples, and no other migration writes the tuple table.
// A new type migration joins this list. The tests that hold the type migrations read them from
// here: cmd/dispatch's, which run the ones before the store's backfill a second time and read the
// schema version they carry, and refs' plan test, which reads their statements against the tuple
// table as inserts alone.
var TypeMigrations = []string{
	"20261109051232_graph_supertags.up.sql",
	"20261113230000_graph_supertags_check_ci_check.up.sql",
	"20261115130000_graph_supertags_element_versions.up.sql",
	"20261116121500_issue_tree_query_supertags.up.sql",
	// The lens_kind supertag and the attribute nodes of the types that declare no block type,
	// each held to its migration by cmd/dispatch's requireStoreOnlyAttributesMatchTheirMigrations.
	"20261116123354_lens_catalogue.up.sql",
	// Version 5: plan_task's steps (main's #4611), the first type migration after the backfill.
	"20261123080000_graph_supertags_plan_task_steps.up.sql",
	// Version 6: the decision and reading block types.
	"20261123174700_graph_supertags_decision_reading.up.sql",
	// Version 7: the criterion block type and the issue tree's attributes.
	"20261123174800_issue_tree_block_schema.up.sql",
	// Version 8: the change request's attributes, the component and conclusion block types, and
	// the supertags of the approved spec (internal/dispatch/specstore).
	"20261123174900_spec_change_block_schema.up.sql",
}
