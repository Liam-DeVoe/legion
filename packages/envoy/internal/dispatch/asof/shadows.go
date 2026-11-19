package asof

import "strings"

// shadow is the definition of one table at a cut. Definition gives a statement that answers
// every column of Table, by name and type, as the table stood at the cut whose timestamptz
// literal it is given. Inside a definition every base table has its schema (public.events), so
// no shadow reads another shadow of the same WITH list by its name.
type shadow struct {
	Table      string
	Definition func(cut string) string
}

// shadows are the shadowed tables, in the order Shadows writes them. Each one is the plan's
// decision 2 for its table.
var shadows = []shadow{
	// An event exists at the cut when the transaction that wrote it began at or before the cut:
	// created_at is that transaction's start.
	{Table: "events", Definition: func(cut string) string {
		return "select * from public.events where created_at <= " + cut
	}},
}

// Shadows is the body of a WITH list that holds one member per shadowed table,
// `<table> as not materialized (<definition>)`, the cut written in each as a timestamptz
// literal. Bind puts it in front of a statement. NOT MATERIALIZED lets PostgreSQL push the
// statement's own filters into each definition, as it does for the table.
func Shadows(cut Cut) string {
	literal := cut.literal()
	var list strings.Builder
	for index, shadow := range shadows {
		if index > 0 {
			list.WriteString(",\n")
		}
		list.WriteString(shadow.Table)
		list.WriteString(" as not materialized (")
		list.WriteString(shadow.Definition(literal))
		list.WriteString(")")
	}
	return list.String()
}

// MachineryTables are the tables that a past-time read reads as they are now, each with its
// reason. Such a table holds no Dispatch content. It holds only the state of Dispatch's own
// machinery, which decides how a read runs. The relation inventory test of the shadows admits
// these tables with no shadow.
var MachineryTables = map[string]string{
	"embeddings_project_key_backfill": "It records how far the fill of embeddings.project_key went. " +
		"That progress selects the statement of a meaning search (api/search.go, meaningStatementFor). " +
		"It holds no Dispatch content.",
}

// CutReadTables are the tables that a past-time read reads with no shadow, under a predicate of
// its own that holds the read to the cut. Each value is that predicate. The relation inventory
// test of the shadows admits these tables with no shadow; each read of one of them at a cut
// must apply its predicate.
var CutReadTables = map[string]string{
	"document_block_texts": "Only the rows of the document's version at the cut, " +
		"for a version that document_indexed_versions holds.",
	"document_indexed_versions": "Only the rows of the document's version at the cut.",
	"document_search":           "Only the documents whose newest version at the cut is their present newest version.",
	"embeddings": "Only the windows of items that did not change after the cut, " +
		"embedded at or before the cut.",
	"post_body_history": "Only the body that the first comment.edited, comment.deleted, message.edited or message.deleted event " +
		"of the post after the cut replaced: the oldest row of the post whose edited_at is at or after that event's created_at, " +
		"for an event at or after the time schema_migrations records for 20261110224508.",
}
