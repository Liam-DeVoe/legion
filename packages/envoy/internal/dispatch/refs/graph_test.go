package refs

// The constants below are main's text, at commit b459fa923705, of each statement of the graph
// read that INFRA-4704 keys: refs.Edges' in read of a node, as readEdgeRows writes it for the
// Query each caller named sends, and the four loaders resolveNodes runs for the comments, asks,
// messages and blocks at the other end of an edge. Each was captured from that commit's code by
// a throwaway test that called the function through a Queryer recording the text it sent and the
// arguments it bound, and is held here letter for letter, as mainClosureQuery is: a text built
// from the package's own fragments would move with them, and main and the head would then agree
// however the head's statement changed. The ids a capture bound are not in any text: every
// statement binds them.

// mainEdgesInDocument is readEdgeRows' in read of a document by its ref_key, Query{Direction:
// "in", Kind: "artifact", WholeDocuments: false, ExcludeKinds: ["part_of"]}: the Query
// GET /api/v1/references?to=<document> sends with no ?kind=, which the issue page's Referenced
// by panel and `dispatch read` read. It binds $1 the kind "artifact", $2 the document's ref_key
// and $3 the excluded kinds.
const mainEdgesInDocument = `select kind, other_kind, other_id, created_at, source_seq, at_block from (select g.kind, g.from_kind, g.from_id, g.created_at, g.source_seq, null::text
			from graph_edges g where g.to_kind = $1 and g.to_id = $2 and not (g.kind = 'mentions' and ((g.from_kind = 'artifact' and exists (
		select 1 from block_refs b where b.to_kind = g.to_kind and b.to_id = g.to_id
		and b.artifact_id::text = g.from_id)) or (g.from_kind = 'comment' and exists (
		select 1 from comment_block_refs b where b.to_kind = g.to_kind and b.to_id = g.to_id
		and b.comment_id::text = g.from_id)) or (g.from_kind = 'message' and exists (
		select 1 from message_block_refs b where b.to_kind = g.to_kind and b.to_id = g.to_id
		and b.message_id::text = g.from_id)) or (g.from_kind = 'ask' and exists (
		select 1 from ask_block_refs b where b.to_kind = g.to_kind and b.to_id = g.to_id
		and b.ask_id::text = g.from_id))))
			union all
			select g.kind, g.from_kind, g.from_id, g.created_at, g.source_seq, g.to_id
			from graph_edges g where g.to_kind = 'block' and g.to_id in (select d.node_id from document_blocks d join artifacts a on a.id = d.artifact_id where a.ref_key = $2) and not (g.kind = 'mentions' and ((g.from_kind = 'artifact' and exists (
		select 1 from block_refs b where b.to_kind = g.to_kind and b.to_id = g.to_id
		and b.artifact_id::text = g.from_id)) or (g.from_kind = 'comment' and exists (
		select 1 from comment_block_refs b where b.to_kind = g.to_kind and b.to_id = g.to_id
		and b.comment_id::text = g.from_id)) or (g.from_kind = 'message' and exists (
		select 1 from message_block_refs b where b.to_kind = g.to_kind and b.to_id = g.to_id
		and b.message_id::text = g.from_id)) or (g.from_kind = 'ask' and exists (
		select 1 from ask_block_refs b where b.to_kind = g.to_kind and b.to_id = g.to_id
		and b.ask_id::text = g.from_id)))))
		as e (kind, other_kind, other_id, created_at, source_seq, at_block) where true and not (kind = any($3)) order by created_at desc, kind, other_kind, other_id, at_block`

// mainEdgesInWholeDocument is readEdgeRows' in read of a document by its ref_key as
// refs.ReferencedBy sends it, Query{Direction: "in", Kind: "artifact", WholeDocuments: true,
// Kinds: ["mentions"]}, for GET /api/v1/artifacts/{id}/references. It binds $1 the kind
// "artifact", $2 the document's ref_key and $3 the kinds.
const mainEdgesInWholeDocument = `select kind, other_kind, other_id, created_at, source_seq, at_block from (select g.kind, g.from_kind, g.from_id, g.created_at, g.source_seq, null::text
			from graph_edges g where g.to_kind = $1 and g.to_id = $2 and g.from_kind not in ('block', 'comment_block', 'message_block', 'ask_block')
			union all
			select g.kind, g.from_kind, g.from_id, g.created_at, g.source_seq, g.to_id
			from graph_edges g where g.to_kind = 'block' and g.to_id in (select d.node_id from document_blocks d join artifacts a on a.id = d.artifact_id where a.ref_key = $2) and g.from_kind not in ('block', 'comment_block', 'message_block', 'ask_block')
			union all
			select r.kind, 'ask', r.ask_id::text, max(r.created_at), max(r.source_seq), null::text
		from ask_block_refs r
		join ask_blocks b on b.ask_id = r.ask_id and b.block_id = r.block_id
		where b.position is not null
		and r.to_kind = 'artifact' and r.to_id = $2
		group by r.kind, r.ask_id
		union all
		select r.kind, 'ask', r.ask_id::text, max(r.created_at), max(r.source_seq), r.to_id
		from ask_block_refs r
		join ask_blocks b on b.ask_id = r.ask_id and b.block_id = r.block_id
		where b.position is not null
		and r.to_kind = 'block' and r.to_id in (select d.node_id from document_blocks d join artifacts a on a.id = d.artifact_id where a.ref_key = $2)
		group by r.kind, r.ask_id, r.to_id)
		as e (kind, other_kind, other_id, created_at, source_seq, at_block) where true and kind = any($3) order by created_at desc, kind, other_kind, other_id, at_block`

// mainEdgesInMessage is readEdgeRows' in read of a message by its uuid, Query{Direction: "in",
// Kind: "message", ExcludeKinds: ["part_of"]}: the Query GET /api/v1/references?to=<message>
// sends with no ?kind=, which the issue page's Referenced by panel and `dispatch read` read. It
// binds $1 the kind "message", $2 the message's id, $3 the same id, which the statement casts to
// the uuid the message's blocks are keyed by, and $4 the excluded kinds.
const mainEdgesInMessage = `select kind, other_kind, other_id, created_at, source_seq, at_block from (select g.kind, g.from_kind, g.from_id, g.created_at, g.source_seq, null::text
			from graph_edges g where g.to_kind = $1 and g.to_id = $2 and not (g.kind = 'mentions' and ((g.from_kind = 'artifact' and exists (
		select 1 from block_refs b where b.to_kind = g.to_kind and b.to_id = g.to_id
		and b.artifact_id::text = g.from_id)) or (g.from_kind = 'comment' and exists (
		select 1 from comment_block_refs b where b.to_kind = g.to_kind and b.to_id = g.to_id
		and b.comment_id::text = g.from_id)) or (g.from_kind = 'message' and exists (
		select 1 from message_block_refs b where b.to_kind = g.to_kind and b.to_id = g.to_id
		and b.message_id::text = g.from_id)) or (g.from_kind = 'ask' and exists (
		select 1 from ask_block_refs b where b.to_kind = g.to_kind and b.to_id = g.to_id
		and b.ask_id::text = g.from_id)))) and not (g.kind = 'replies_to' and g.from_kind = 'message' and g.from_id in (
		select r.id::text from messages r where r.replies_to_block in (select b.node_id from message_blocks b where b.message_id = $3::uuid and b.position is not null)))
			union all
			select g.kind, g.from_kind, g.from_id, g.created_at, g.source_seq, g.to_id
			from graph_edges g where g.to_kind = 'message_block' and g.to_id in (select b.node_id from message_blocks b where b.message_id = $3::uuid and b.position is not null) and not (g.kind = 'mentions' and ((g.from_kind = 'artifact' and exists (
		select 1 from block_refs b where b.to_kind = g.to_kind and b.to_id = g.to_id
		and b.artifact_id::text = g.from_id)) or (g.from_kind = 'comment' and exists (
		select 1 from comment_block_refs b where b.to_kind = g.to_kind and b.to_id = g.to_id
		and b.comment_id::text = g.from_id)) or (g.from_kind = 'message' and exists (
		select 1 from message_block_refs b where b.to_kind = g.to_kind and b.to_id = g.to_id
		and b.message_id::text = g.from_id)) or (g.from_kind = 'ask' and exists (
		select 1 from ask_block_refs b where b.to_kind = g.to_kind and b.to_id = g.to_id
		and b.ask_id::text = g.from_id)))))
		as e (kind, other_kind, other_id, created_at, source_seq, at_block) where true and not (kind = any($4)) order by created_at desc, kind, other_kind, other_id, at_block`

// mainEdgesInComment is readEdgeRows' in read of a comment by its uuid, Query{Direction: "in",
// Kind: "comment", ExcludeKinds: ["part_of"]}: the Query GET /api/v1/references?to=<comment>
// sends with no ?kind=, which the issue page's Referenced by panel and `dispatch read` read; with
// replies_to neither asked for nor excluded, it folds a reply's answers edge into its replies_to
// edge (foldsAnswers). It binds $1 the kind "comment", $2 the comment's id, $3 the same id, which
// the statement casts to the uuid the comment's blocks are keyed by, and $4 the excluded kinds.
const mainEdgesInComment = `select kind, other_kind, other_id, created_at, source_seq, at_block from (select g.kind, g.from_kind, g.from_id, g.created_at, g.source_seq, null::text
			from graph_edges g where g.to_kind = $1 and g.to_id = $2 and not (g.kind = 'mentions' and ((g.from_kind = 'artifact' and exists (
		select 1 from block_refs b where b.to_kind = g.to_kind and b.to_id = g.to_id
		and b.artifact_id::text = g.from_id)) or (g.from_kind = 'comment' and exists (
		select 1 from comment_block_refs b where b.to_kind = g.to_kind and b.to_id = g.to_id
		and b.comment_id::text = g.from_id)) or (g.from_kind = 'message' and exists (
		select 1 from message_block_refs b where b.to_kind = g.to_kind and b.to_id = g.to_id
		and b.message_id::text = g.from_id)) or (g.from_kind = 'ask' and exists (
		select 1 from ask_block_refs b where b.to_kind = g.to_kind and b.to_id = g.to_id
		and b.ask_id::text = g.from_id)))) and not (g.kind = 'replies_to' and g.from_kind = 'comment' and g.from_id in (
		select r.id::text from comments r where r.replies_to_block in (select b.node_id from comment_blocks b where b.comment_id = $3::uuid and b.position is not null)))
			union all
			select g.kind, g.from_kind, g.from_id, g.created_at, g.source_seq, g.to_id
			from graph_edges g where g.to_kind = 'comment_block' and g.to_id in (select b.node_id from comment_blocks b where b.comment_id = $3::uuid and b.position is not null) and not (g.kind = 'mentions' and ((g.from_kind = 'artifact' and exists (
		select 1 from block_refs b where b.to_kind = g.to_kind and b.to_id = g.to_id
		and b.artifact_id::text = g.from_id)) or (g.from_kind = 'comment' and exists (
		select 1 from comment_block_refs b where b.to_kind = g.to_kind and b.to_id = g.to_id
		and b.comment_id::text = g.from_id)) or (g.from_kind = 'message' and exists (
		select 1 from message_block_refs b where b.to_kind = g.to_kind and b.to_id = g.to_id
		and b.message_id::text = g.from_id)) or (g.from_kind = 'ask' and exists (
		select 1 from ask_block_refs b where b.to_kind = g.to_kind and b.to_id = g.to_id
		and b.ask_id::text = g.from_id)))))
		as e (kind, other_kind, other_id, created_at, source_seq, at_block) where true and not (kind = any($4)) and not (kind = 'answers' and exists (select 1 from comments c where c.id::text = other_id and (c.reply_to::text = $2 or c.replies_to_block in (select b.node_id from comment_blocks b where b.comment_id = $3::uuid and b.position is not null)))) order by created_at desc, kind, other_kind, other_id, at_block`

// mainEdgesInAsk is readEdgeRows' in read of an ask by its uuid with no Kinds and no
// ExcludeKinds, Query{Direction: "in", Kind: "ask"}: the read of a caller of refs.Edges that
// names no kind and excludes none. No route sends it for an ask, since GET /api/v1/references
// excludes AskBacklinkExclusions from an ask's in read whenever ?kind= is empty
// (mainEdgesInAskPanel). It binds $1 the kind "ask", $2 the ask's id and $3 the same id, which
// the statement casts to the uuid the ask's parts are keyed by.
const mainEdgesInAsk = `select kind, other_kind, other_id, created_at, source_seq, at_block from (select g.kind, g.from_kind, g.from_id, g.created_at, g.source_seq, null::text
			from graph_edges g where g.to_kind = $1 and g.to_id = $2 and not (g.kind = 'mentions' and ((g.from_kind = 'artifact' and exists (
		select 1 from block_refs b where b.to_kind = g.to_kind and b.to_id = g.to_id
		and b.artifact_id::text = g.from_id)) or (g.from_kind = 'comment' and exists (
		select 1 from comment_block_refs b where b.to_kind = g.to_kind and b.to_id = g.to_id
		and b.comment_id::text = g.from_id)) or (g.from_kind = 'message' and exists (
		select 1 from message_block_refs b where b.to_kind = g.to_kind and b.to_id = g.to_id
		and b.message_id::text = g.from_id)) or (g.from_kind = 'ask' and exists (
		select 1 from ask_block_refs b where b.to_kind = g.to_kind and b.to_id = g.to_id
		and b.ask_id::text = g.from_id)))) and not (g.kind = 'replies_to' and g.from_kind = 'comment' and g.from_id in (
		select r.id::text from comments r where r.replies_to_block in (select b.node_id from ask_blocks b where b.ask_id = $3::uuid and b.position is not null)))
			union all
			select g.kind, g.from_kind, g.from_id, g.created_at, g.source_seq, g.to_id
			from graph_edges g where g.to_kind = 'ask_block' and g.to_id in (select b.node_id from ask_blocks b where b.ask_id = $3::uuid and b.position is not null) and not (g.kind = 'mentions' and ((g.from_kind = 'artifact' and exists (
		select 1 from block_refs b where b.to_kind = g.to_kind and b.to_id = g.to_id
		and b.artifact_id::text = g.from_id)) or (g.from_kind = 'comment' and exists (
		select 1 from comment_block_refs b where b.to_kind = g.to_kind and b.to_id = g.to_id
		and b.comment_id::text = g.from_id)) or (g.from_kind = 'message' and exists (
		select 1 from message_block_refs b where b.to_kind = g.to_kind and b.to_id = g.to_id
		and b.message_id::text = g.from_id)) or (g.from_kind = 'ask' and exists (
		select 1 from ask_block_refs b where b.to_kind = g.to_kind and b.to_id = g.to_id
		and b.ask_id::text = g.from_id)))))
		as e (kind, other_kind, other_id, created_at, source_seq, at_block) where true order by created_at desc, kind, other_kind, other_id, at_block`

// mainEdgesInAskPanel is readEdgeRows' in read of an ask by its uuid, Query{Direction: "in", Kind:
// "ask", ExcludeKinds: AskBacklinkExclusions}: the Query GET /api/v1/references?to=<ask> sends
// with no ?kind=, which the ask's Referenced by panel on the issue page and `dispatch read --ask`
// read. Its text is mainEdgesInAsk's with the exclusion added. It binds $1 the kind "ask", $2 the
// ask's id, $3 the same id, which the statement casts to the uuid the ask's parts are keyed by,
// and $4 the excluded kinds, replies_to and part_of.
const mainEdgesInAskPanel = `select kind, other_kind, other_id, created_at, source_seq, at_block from (select g.kind, g.from_kind, g.from_id, g.created_at, g.source_seq, null::text
			from graph_edges g where g.to_kind = $1 and g.to_id = $2 and not (g.kind = 'mentions' and ((g.from_kind = 'artifact' and exists (
		select 1 from block_refs b where b.to_kind = g.to_kind and b.to_id = g.to_id
		and b.artifact_id::text = g.from_id)) or (g.from_kind = 'comment' and exists (
		select 1 from comment_block_refs b where b.to_kind = g.to_kind and b.to_id = g.to_id
		and b.comment_id::text = g.from_id)) or (g.from_kind = 'message' and exists (
		select 1 from message_block_refs b where b.to_kind = g.to_kind and b.to_id = g.to_id
		and b.message_id::text = g.from_id)) or (g.from_kind = 'ask' and exists (
		select 1 from ask_block_refs b where b.to_kind = g.to_kind and b.to_id = g.to_id
		and b.ask_id::text = g.from_id)))) and not (g.kind = 'replies_to' and g.from_kind = 'comment' and g.from_id in (
		select r.id::text from comments r where r.replies_to_block in (select b.node_id from ask_blocks b where b.ask_id = $3::uuid and b.position is not null)))
			union all
			select g.kind, g.from_kind, g.from_id, g.created_at, g.source_seq, g.to_id
			from graph_edges g where g.to_kind = 'ask_block' and g.to_id in (select b.node_id from ask_blocks b where b.ask_id = $3::uuid and b.position is not null) and not (g.kind = 'mentions' and ((g.from_kind = 'artifact' and exists (
		select 1 from block_refs b where b.to_kind = g.to_kind and b.to_id = g.to_id
		and b.artifact_id::text = g.from_id)) or (g.from_kind = 'comment' and exists (
		select 1 from comment_block_refs b where b.to_kind = g.to_kind and b.to_id = g.to_id
		and b.comment_id::text = g.from_id)) or (g.from_kind = 'message' and exists (
		select 1 from message_block_refs b where b.to_kind = g.to_kind and b.to_id = g.to_id
		and b.message_id::text = g.from_id)) or (g.from_kind = 'ask' and exists (
		select 1 from ask_block_refs b where b.to_kind = g.to_kind and b.to_id = g.to_id
		and b.ask_id::text = g.from_id)))))
		as e (kind, other_kind, other_id, created_at, source_seq, at_block) where true and not (kind = any($4)) order by created_at desc, kind, other_kind, other_id, at_block`

// mainLoadCommentNodes is loadCommentNodes' statement, which resolveNodes sends for every
// comment at the other end of an edge (refs.Edges, refs.Node, refs.TargetNames). It binds $1 the
// comments' ids, as text.
const mainLoadCommentNodes = `
		select c.id::text, c.issue_key, coalesce(i.project_key, a.project_key, ''), coalesce(a.slug, ''), c.body,
		       c.author, c.created_at, c.edited_at, c.reply_to::text, c.replies_to_block, c.ask_id::text, doc.id::text, doc.issue_key, coalesce(doc.project_key, ''), doc.session_id, doc.slug, doc.kind, doc.is_primary,
		       case when c.anchor->>'artifact_id' is not null then case when c.anchor->>'version' ~ '^[1-9][0-9]{0,8}$' then (c.anchor->>'version')::int end else c.written_version end
		from comments c
		left join issues i on i.key = c.issue_key
		left join artifacts a on a.id = c.artifact_id
		left join artifacts doc on doc.id = coalesce((c.anchor->>'artifact_id')::uuid, c.artifact_id)
		where c.id::text = any($1)`

// mainLoadAskNodes is loadAskNodes' statement, which resolveNodes sends for every ask at the
// other end of an edge (refs.Edges, refs.Node, refs.TargetNames). It binds $1 the asks' ids, as
// text.
const mainLoadAskNodes = `
		select k.id::text, k.issue_key, coalesce(i.project_key, a.project_key, ''), coalesce(a.slug, ''), k.question,
		       k.author, k.created_at, k.edited_at, edit.edited_by, doc.id::text, doc.issue_key, coalesce(doc.project_key, ''), doc.session_id, doc.slug, doc.kind, doc.is_primary,
		       case
		         when (case when pg_input_is_valid(k.anchor->>'artifact_id', 'uuid') then (k.anchor->>'artifact_id')::uuid end) is not null then case when k.anchor->>'version' ~ '^[1-9][0-9]{0,8}$' then (k.anchor->>'version')::int end
		         when k.block_artifact_id is not null then case when first.known then first.version end
		         else k.written_version
		       end
		from asks k
		left join issues i on i.key = k.issue_key
		left join artifacts a on a.id = k.artifact_id
		left join artifacts doc on doc.id = case
		  when (case when pg_input_is_valid(k.anchor->>'artifact_id', 'uuid') then (k.anchor->>'artifact_id')::uuid end) is not null then (case when pg_input_is_valid(k.anchor->>'artifact_id', 'uuid') then (k.anchor->>'artifact_id')::uuid end)
		  when k.block_artifact_id is not null then k.block_artifact_id
		  when k.kind = 'approval' then (k.approval->>'artifact_id')::uuid
		  else k.artifact_id
		end
		left join lateral (
			select e.payload->'edited_by' as edited_by from events e
			where e.type in ('ask.opened', 'ask.answered', 'ask.resolved', 'ask.edited') and e.type = 'ask.edited'
			  and e.payload->>'id' = k.id::text
			order by e.id desc limit 1
		) edit on k.edited_at is not null
		left join lateral (
		select t.version,
		       t.version = 1 or exists (
		         select 1 from document_indexed_versions i where i.artifact_id = t.artifact_id and i.version = t.version - 1
		       ) as known,
		       v.authors, v.created_at
		from document_block_texts t
		left join artifact_versions v on v.artifact_id = t.artifact_id and v.number = t.version
		where t.artifact_id = k.block_artifact_id and t.block_id = k.block_id and t.text is not null
		order by t.version limit 1
	) first on true
		where k.id::text = any($1)`

// mainLoadMessageNodes is loadMessageNodes' statement, which resolveNodes sends for every message
// at the other end of an edge (refs.Edges, refs.Node, refs.TargetNames). It binds $1 the messages'
// ids, as text.
const mainLoadMessageNodes = `
		select m.id::text, m.issue_key, coalesce(i.project_key, ''), m.body, m.author, m.created_at, m.in_reply_to::text, m.replies_to_block
		from messages m
		left join issues i on i.key = m.issue_key
		where m.id::text = any($1)`

// mainLoadBlockNodes is loadBlockNodes' statement, which resolveNodes sends for every block of a
// document at the other end of an edge, and for the blocks a document's read lists edges at
// (refs.Edges, refs.Node, refs.TargetNames). It binds $1 the blocks' node ids,
// '<document ref_key>#<block id>'.
const mainLoadBlockNodes = `
		select d.node_id, d.block_id, d.excerpt, d.changed_version, d.changed_at, changed.authors,
		       a.id::text, a.issue_key, coalesce(a.project_key, ''), a.session_id, a.slug, a.kind, a.is_primary, first.version, first.known, first.authors, first.created_at
		from document_blocks d join artifacts a on a.id = d.artifact_id
		left join artifact_versions changed on changed.artifact_id = d.artifact_id and changed.number = d.changed_version
		left join lateral (
		select t.version,
		       t.version = 1 or exists (
		         select 1 from document_indexed_versions i where i.artifact_id = t.artifact_id and i.version = t.version - 1
		       ) as known,
		       v.authors, v.created_at
		from document_block_texts t
		left join artifact_versions v on v.artifact_id = t.artifact_id and v.number = t.version
		where t.artifact_id = d.artifact_id and t.block_id = d.block_id and t.text is not null
		order by t.version limit 1
	) first on true
		where d.node_id = any($1)`
