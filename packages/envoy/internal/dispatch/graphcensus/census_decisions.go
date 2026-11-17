package graphcensus

import (
	"context"
	"strings"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// decisionProjections are the census's kinds of dispatch://INFRA-1150 (projections): the ties of
// decisions, readings and asks that block_refs, document_blocks, asks and ask_followers project.
var decisionProjections = []projection{
	tieProjection(refs.TupleShaped, refs.BlockNodeKind+", "+graphstore.KindAsk,
		"a decision's shaped, and an ask's, which hangs on the ask's node: the row side is every shaped row of"+
			" block_refs by the node it names, and the digest is every block those rows of one node name against"+
			" every shaped tuple that node states now", refs.BlockNodeKind, graphstore.KindAsk),
	tieProjection(refs.TupleFromAsk, refs.BlockNodeKind,
		"a decision's from_ask: the row side is every from_ask row of block_refs by the decision's node, and"+
			" the digest is the ask those rows name against the from_ask its node states now", refs.BlockNodeKind),
	tieProjection(graphstore.TupleDecidedBy, refs.BlockNodeKind,
		"a decision's decided_by: the row side is every decided_by row of block_refs by the decision's node,"+
			" and the digest is the person or session those rows name against the decided_by its node states now",
		refs.BlockNodeKind),
	{
		name:     graphstore.TupleFollowedBy,
		nodeKind: graphstore.KindAsk,
		table:    "ask_followers",
		note: "an ask's followers: the row side is every marked ask_followers row by the ask's node it names," +
			" and the digest is every session those rows name against every followed_by the node states now",
		rows: `
		select 'ask_follower ' || f.ask_id::text || ' ' || f.session_id, f.node_kind, f.node_address,
		       md5(f.node_address || '|' || named.links)
		from ask_followers f
		join (
			select b.node_address,
			       string_agg('` + graphstore.TupleFollowedBy + `>` + graphstore.KindSession + `:' || b.session_id, ','
			                  order by '` + graphstore.TupleFollowedBy + `>` + graphstore.KindSession + `:' || b.session_id) as links
			from ask_followers b
			where b.node_kind is not null and octet_length(b.session_id) <= ` + addressBound + `
			group by b.node_address
		) named on named.node_address = f.node_address
		where f.node_kind is not null and octet_length(f.session_id) <= ` + addressBound,
		nodes: linkedNodes(graphstore.KindAsk, `t.kind = '`+graphstore.TupleFollowedBy+`'`),
		pastBound: `
		select count(*) from ask_followers f where octet_length(f.session_id) > ` + addressBound,
		unmarked: `
		select count(*) from ask_followers f where f.node_kind is null and octet_length(f.session_id) <= ` + addressBound,
		emptyAgrees: true,
	}, {
		name:     "reading_parent",
		nodeKind: refs.BlockNodeKind,
		table:    "document_blocks.parent_block_id",
		note: "the parent of each reading: the row side is the document_blocks row of every reading's node, and" +
			" the digest is the block its parent_block_id names, or its document's top where it names none, against" +
			" the part_of the node states now, a part_of to an ask's node read as the ask's block in the node's own document",
		rows: `
		select 'reading ' || d.node_id, d.node_kind, d.node_address,
		       md5(d.node_id || '|' || ` + statedLinks(
			`'`+graphstore.TuplePartOf+`>' || case when d.parent_block_id is null then `+refs.DocumentTopSQL("a")+`
		      else '`+refs.BlockNodeKind+`:' || a.ref_key || '#' || d.parent_block_id end`) + `)
		from document_blocks d
		join artifacts a on a.id = d.artifact_id
		join graph_nodes reading on reading.kind = '` + refs.BlockNodeKind + `' and reading.id = d.node_id
		  and reading.supertag = '` + refs.ReadingSupertag + `'`,
		nodes: `
		select n.id, md5(n.id || '|' || ` + placedLinks(`t.kind = '`+graphstore.TuplePartOf+`'`) + `)
		from graph_nodes n where n.kind = '` + refs.BlockNodeKind + `' and n.supertag = '` + refs.ReadingSupertag + `'`,
		owed:        typedNodeOwed,
		emptyAgrees: true,
	},
	askProjection("ask_parent", "asks.block_artifact_id, asks.artifact_id",
		"an ask's placement: the row side is every marked asks row that names a document holding its block or"+
			" owning it, and the digest is those documents against the part_of and owned_by its node states now",
		`(document.id is not null or owner.id is not null)`,
		[]string{`'` + graphstore.TuplePartOf + `>artifact:' || document.ref_key`, `'` + graphstore.TupleOwnedBy + `>artifact:' || owner.ref_key`},
		`t.kind in ('`+graphstore.TuplePartOf+`', '`+graphstore.TupleOwnedBy+`')`),
	askProjection("ask_anchor", "asks.block_artifact_id, asks.anchor",
		"an ask's anchor: the row side is every marked asks row whose block's document or anchor names a"+
			" document, and the digest is that document against the anchored_to its node states now",
		`anchor.id is not null`,
		[]string{`'` + graphstore.TupleAnchoredTo + `>artifact:' || anchor.ref_key`},
		`t.kind = '`+graphstore.TupleAnchoredTo+`'`),
	{
		name:     "ask_answer",
		nodeKind: graphstore.KindAsk,
		table:    "asks.answer",
		note: "a person's answer: the row side is every marked asks row holding an answer, and the digest is its" +
			" words, its selection, its time and its person, character for character, against the answer, selected" +
			" and answered_at values and the answered_by and decided_by its node holds now",
		rows: `
		select 'ask ' || k.id::text, k.node_kind, k.node_address, md5(k.id::text || '|' || ` + statedLinks(
			`'`+graphstore.TupleAnswer+`>value:' || (k.answer->>'text')`,
			`'`+graphstore.TupleSelected+`>value:' || coalesce(case when jsonb_typeof(k.answer->'selected') = 'array'
			      then k.answer->'selected' end, '[]'::jsonb)::text`,
			`'`+graphstore.TupleAnsweredAt+`>value:' || (k.answer->>'at')`,
			`case when `+answerer+` <> '' and octet_length(`+answerer+`) <= `+addressBound+`
			      then '`+graphstore.TupleAnsweredBy+`>`+graphstore.KindPerson+`:' || `+answerer+` end`,
			`case when `+answerer+` <> '' and octet_length(`+answerer+`) <= `+addressBound+`
			      then '`+graphstore.TupleDecidedBy+`>`+graphstore.KindPerson+`:' || `+answerer+` end`) + `)
		from asks k
		where k.node_kind is not null and k.answer is not null`,
		// The values are read as a current value is, the newest tuple of each kind while it asserts,
		// and the two relations as a current relation is; selected is read as the JSON it holds,
		// as the row's is, so the labels are compared character for character whatever spacing
		// either encoding put between them. A value that is no JSON is compared as it is written,
		// and so differs.
		nodes: `
		select held.id, md5(held.id || '|' || held.facts)
		from (
			select n.id, (select coalesce(string_agg(answered.fact, ',' order by answered.fact), '') from (
				select v.kind || '>value:' || case when v.kind = '` + graphstore.TupleSelected + `' and pg_input_is_valid(v.to_id, 'jsonb')
				                                   then v.to_id::jsonb::text else v.to_id end
				from (
					select distinct on (t.kind) t.kind, t.to_id, t.asserted
					from graph_tuples t
					where t.from_kind = '` + graphstore.KindAsk + `' and t.from_id = n.id and t.to_kind = '` + graphstore.KindValue + `'
					  and t.kind in ('` + graphstore.TupleAnswer + `', '` + graphstore.TupleSelected + `', '` + graphstore.TupleAnsweredAt + `')
					order by t.kind, t.seq desc
				) v where v.asserted
				union all
				select r.kind || '>' || r.to_kind || ':' || r.to_id from (
					` + graphstore.CurrentTupleRead(`t.kind, t.to_kind, t.to_id`, `t.kind, t.to_kind, t.to_id, t.asserted`,
			`t.from_kind = '`+graphstore.KindAsk+`' and t.from_id = n.id and t.to_kind <> '`+graphstore.KindValue+`'
					  and t.kind in ('`+graphstore.TupleAnsweredBy+`', '`+graphstore.TupleDecidedBy+`')`) + `
				) r where r.asserted
			) answered (fact)) as facts
			from graph_nodes n where n.kind = '` + graphstore.KindAsk + `'
		) held
		where held.facts <> ''`,
		unmarked:    askUnmarked,
		pastBound:   askPastBound,
		emptyAgrees: true,
	},
}

// decisionBackfills are the backfill's passes over the asks and their followers
// (dispatch://INFRA-1150, backfillPasses), after the object pass wrote the node of every ask
// (backfillObjectNodes): the rest of the boot step's first phase (BackfillAsks), so a database the
// command fills reads as one the boot fills.
var decisionBackfills = []backfillPass{kindSoFar(backfillAsks), kindSoFar(backfillAskFollowers)}

// kindSoFar is the backfillPass of a pass over one kind that reports what it did beside its
// error when it fails: every batch of an asks or follower pass committed before a failure stays,
// and the report says how far it got, as BackfillAsks does.
func kindSoFar(write func(context.Context, *store.Store) (KindBackfill, error)) backfillPass {
	return func(ctx context.Context, database *store.Store) ([]KindBackfill, error) {
		done, err := write(ctx, database)
		return []KindBackfill{done}, err
	}
}

// linkedNodes is the node side of a kind whose links hang on nodes of kind fromKind: every node of
// that kind stating such a link now (currentLinks over kinds), digesting its id and those links. A
// node that states none is no node of the kind, as a row that names none is no row of it.
func linkedNodes(fromKind, kinds string) string {
	return `
		select held.id, md5(held.id || '|' || held.links)
		from (
			select n.id, ` + currentLinks(fromKind, kinds) + ` as links
			from graph_nodes n where n.kind = '` + fromKind + `'
		) held
		where held.links <> ''`
}

// placedLinks is currentLinks of a block node n under kinds, but a part_of whose far end is an
// ask's node is written as the block of n's own document that holds that ask: the block of the
// id the ask's marked row names (asks.block_id), which is the ask's own block in the document
// that row names and a copy's block in any other document of its owner, since a copy keeps its
// source's block id (docs' copiedAskSources). A reading in an answered ask hangs on the ask's
// node, while its row's parent_block_id keeps the id of the ask block holding it, which is that
// tuple's projection (refs' reconcileBlockParents), so both read as one link to that block. An
// ask no marked row names, or whose row names no block, keeps its own address, which no row
// states. A ref_key holds no '#', so n's document is the part of its id before the first one.
func placedLinks(kinds string) string {
	return `(select coalesce(string_agg(placed.link, ',' order by placed.link), '')
	 from (
		select case when held.kind = '` + graphstore.TuplePartOf + `' and held.to_kind = '` + graphstore.KindAsk + `'
		            then coalesce((
		                select held.kind || '>` + refs.BlockNodeKind + `:' || split_part(n.id, '#', 1) || '#' || k.block_id
		                from asks k
		                where k.node_kind = '` + graphstore.KindAsk + `' and k.node_address = held.to_id
		                  and k.block_id is not null), held.kind || '>' || held.to_kind || ':' || held.to_id)
		            else held.kind || '>' || held.to_kind || ':' || held.to_id end as link
		from (
			` + graphstore.CurrentTupleRead(`t.kind, t.to_kind, t.to_id`, `t.kind, t.to_kind, t.to_id, t.asserted`,
		`t.from_kind = '`+refs.BlockNodeKind+`' and t.from_id = n.id
		  and t.to_kind <> '`+graphstore.KindValue+`' and `+kinds) + `
		) held where held.asserted
	 ) placed)`
}

// typedNodeOwed is the owed predicate (projection.owed) of a kind whose node may be a decision's
// or a reading's. A document that still owes its block index (document_block_versions.version
// null) holds no row yet for a block its latest version added, while that version's typed-node
// pass already wrote the block's node: a decision or a reading written above the deferral bound
// (refs.IndexDecisionNodes), the one writer of a block node that writes no row beside it. The
// block-index reconciliation writes the row, so such a node is owed its row and no difference
// until then; a node of any other supertag in such a document is a difference still, since no
// writer leaves one without its row. A ref_key holds no '#', so a block node's document is the
// part of its id before the first one.
var typedNodeOwed = `exists (
		select 1 from graph_nodes typed
		join artifacts a on a.ref_key = split_part(typed.id, '#', 1)
		join document_block_versions owed on owed.artifact_id = a.id
		where typed.kind = '` + refs.BlockNodeKind + `' and typed.id = node.id
		  and typed.supertag in ('` + refs.DecisionSupertag + `', '` + refs.ReadingSupertag + `')
		  and owed.version is null)`

// tieProjection is the kind of one tie of dispatch://INFRA-1150 that block_refs projects - a
// decision's shaped, from_ask or decided_by, and an ask's shaped - held, as the block_ref kind
// holds a block's links, by the triple of holding node, tuple kind and far end: a row names the
// node that states its tie (node_kind, node_address: the decision's block node, or for an ask
// block's shaped the ask's node, refs.TypedFacts.pairs), and its digest is every far end the rows
// of that node name under kind, against every far end that node states now under kind
// (currentLinks). So a tuple that reaches the wrong node is a difference, and not a count that
// still matches. holders are the node kinds the tie hangs on. A row that names no node is a
// difference, as a block_ref row's is: every writer of these kinds names it, and a row past the
// store's bound is counted apart.
func tieProjection(kind, nodeKind, note string, holders ...string) projection {
	of := `kind = '` + kind + `'`
	inBound := func(alias string) string {
		return `octet_length(` + alias + `.from_node) <= ` + addressBound + ` and octet_length(` + alias + `.to_id) <= ` + addressBound
	}
	held := projection{
		name: kind, nodeKind: nodeKind, table: "block_refs", note: note, emptyAgrees: true,
		rows: `
		select 'block_ref ' || r.from_node || ' ' || r.kind || ' -> ' || r.to_kind || ' ' || r.to_id,
		       r.node_kind, r.node_address, md5(r.node_address || '|' || named.links)
		from block_refs r
		left join (
			select b.node_kind, b.node_address,
			       string_agg(b.kind || '>' || b.to_kind || ':' || b.to_id, ','
			                  order by b.kind || '>' || b.to_kind || ':' || b.to_id) as links
			from block_refs b
			where b.` + of + ` and b.node_kind is not null and ` + inBound("b") + `
			group by b.node_kind, b.node_address
		) named on named.node_kind = r.node_kind and named.node_address = r.node_address
		where r.` + of + ` and ` + inBound("r"),
		pastBound: `
		select count(*) from block_refs r
		where r.` + of + ` and not (` + inBound("r") + `)`,
	}
	if len(holders) == 1 {
		held.nodes = linkedNodes(holders[0], `t.`+of)
		return held
	}
	held.kinded = true
	arms := make([]string, len(holders))
	for index, holder := range holders {
		arms[index] = `select '` + holder + `', held.id, held.digest from (` + linkedNodes(holder, `t.`+of) + `) held (id, digest)`
	}
	held.nodes = strings.Join(arms, `
		union all
		`)
	return held
}

// askDocuments are the three documents an asks row k names, joined as the ask's writer reads
// them (graphstore.ReadAskPlaces) and the backfill's asks pass joins them (askRowsOwed): anchor,
// the document holding its block or else the one its anchor quotes; owner, the document an ask
// on no issue belongs to; document, the one holding its block. It follows `from asks k`.
const askDocuments = `
		left join artifacts anchor on anchor.id = coalesce(k.block_artifact_id, (k.anchor->>'artifact_id')::uuid)
		left join artifacts owner on owner.id = k.artifact_id
		left join artifacts document on document.id = k.block_artifact_id`

// askFits is true of an asks row none of whose documents' addresses is past the store's bound,
// the one kind of row the ask's writer marks (graphstore.AskPlaces.Fit) and the backfill owes
// (askRowsOwed). It reads askDocuments.
var askFits = `octet_length(coalesce(anchor.ref_key, '')) <= ` + addressBound + `
		  and octet_length(coalesce(owner.ref_key, '')) <= ` + addressBound + `
		  and octet_length(coalesce(document.ref_key, '')) <= ` + addressBound

// askPastBound counts the asks rows some document of which is past the store's bound: the
// writer states none of such an ask's relations and leaves its row unmarked on purpose, and the
// census counts them apart. askUnmarked counts every other asks row that names no node, which a
// process of the release before wrote and the decisions step's backfill owes: graph_edges' old
// arms answer it, and it is a class of its own and no difference (projection.unmarked).
var (
	askPastBound = `
		select count(*) from asks k` + askDocuments + `
		where not (` + askFits + `)`
	askUnmarked = `
		select count(*) from asks k` + askDocuments + `
		where k.node_kind is null and ` + askFits
)

// askProjection is a kind of the relations an ask's node holds that its asks row projects:
// links are the links a marked row states, as statedLinks takes them over askDocuments, member
// the predicate that makes a row one of the kind, and kinds the tuple kinds its node states them
// under. A marked row is held by its node, the ask's own, and an unmarked one is counted apart.
func askProjection(name, table, note, member string, links []string, kinds string) projection {
	return projection{
		name: name, nodeKind: graphstore.KindAsk, table: table, note: note, emptyAgrees: true,
		rows: `
		select 'ask ' || k.id::text, k.node_kind, k.node_address, md5(k.id::text || '|' || ` + statedLinks(links...) + `)
		from asks k` + askDocuments + `
		where k.node_kind is not null and ` + member,
		nodes:     linkedNodes(graphstore.KindAsk, kinds),
		pastBound: askPastBound,
		unmarked:  askUnmarked,
	}
}

// answerer is the person node an asks row's answer names, written as asks.AnswererOf names it:
// the canonical login of the answer's user (user_threads_login is model.CanonicalLogin in SQL),
// empty where it names nobody. It reads `asks k`.
const answerer = `coalesce(user_threads_login(k.answer->>'user'), '')`
