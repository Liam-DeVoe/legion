package refs

// mainEdgesInIssue is readEdgeRows' in read of an issue by its key with no Kinds and no
// ExcludeKinds, Query{Direction: "in", Kind: "issue"}: the Query GET /api/v1/references?to=<issue>
// sends with no ?kind=, which the issue page's Referenced by panel and `dispatch read` read. Its
// one arm is the first arm of every in read, with heldByABlock's probe and no arm over blocks, so
// it is also the text of the in read of a component, a session, a block, an ask's part and a
// comment's or a message's block, each with its own kind bound as $1. It binds $1 the kind
// "issue" and $2 the issue's key. It was captured as graph_test.go's constants were, from
// graph.go as b459fa923705 holds it, by a throwaway test that compared readEdgeRows' text for each
// of those kinds with it, letter for letter. It is the oracle of c-graph-edges' probe case.
const mainEdgesInIssue = `select kind, other_kind, other_id, created_at, source_seq, at_block from (select g.kind, g.from_kind, g.from_id, g.created_at, g.source_seq, null::text
			from graph_edges g where g.to_kind = $1 and g.to_id = $2 and not (g.kind = 'mentions' and ((g.from_kind = 'artifact' and exists (
		select 1 from block_refs b where b.to_kind = g.to_kind and b.to_id = g.to_id
		and b.artifact_id::text = g.from_id)) or (g.from_kind = 'comment' and exists (
		select 1 from comment_block_refs b where b.to_kind = g.to_kind and b.to_id = g.to_id
		and b.comment_id::text = g.from_id)) or (g.from_kind = 'message' and exists (
		select 1 from message_block_refs b where b.to_kind = g.to_kind and b.to_id = g.to_id
		and b.message_id::text = g.from_id)) or (g.from_kind = 'ask' and exists (
		select 1 from ask_block_refs b where b.to_kind = g.to_kind and b.to_id = g.to_id
		and b.ask_id::text = g.from_id)))))
		as e (kind, other_kind, other_id, created_at, source_seq, at_block) where true order by created_at desc, kind, other_kind, other_id, at_block`
