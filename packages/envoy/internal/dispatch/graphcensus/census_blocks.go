package graphcensus

import (
	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
)

// blockProjections are the census's kinds of the document blocks (projections): a block against
// its document_blocks row, and the links out of a block against its block_refs rows.
var blockProjections = []projection{{
	name:     "block",
	nodeKind: refs.BlockNodeKind,
	table:    "document_blocks",
	note: "one node per row, blocks past the address bound and the blocks of a lens's finding aside; the digest is" +
		" the row's document and the block its parent_block_id names, or the document where it names none, against" +
		" the in_document and part_of tuples the node states now, a block with no part_of telling itself from one" +
		" the document holds directly",
	rows: `
		select 'block ' || d.node_id, d.node_kind, d.node_address,
		       md5(d.node_id || '|' || ` + statedLinks(
		`'`+refs.TupleInDocument+`>artifact:' || a.ref_key`,
		`'`+graphstore.TuplePartOf+`>' || case when d.parent_block_id is null then 'artifact:' || a.ref_key
		      else '`+refs.BlockNodeKind+`:' || a.ref_key || '#' || d.parent_block_id end`,
	) + `)
		from document_blocks d
		join artifacts a on a.id = d.artifact_id
		where octet_length(d.node_id) <= ` + addressBound,
	nodes: `
		select n.id, md5(n.id || '|' || ` + currentLinks(refs.BlockNodeKind,
		`t.kind in ('`+refs.TupleInDocument+`', '`+graphstore.TuplePartOf+`')`) + `)
		from graph_nodes n where n.kind = '` + refs.BlockNodeKind + `' and not ` + lensBlock("n"),
	pastBound: `
		select count(*) from document_blocks d where octet_length(d.node_id) > ` + addressBound,
}, {
	name:     "block_ref",
	nodeKind: refs.BlockNodeKind,
	table:    "block_refs",
	note: "a row is one link out of a block and its node is that block's, so several rows share one node:" +
		" the node side is every block node not gone, a lens's finding aside, that states a link now of a kind" +
		" refs.IndexBlocks writes into block_refs (a mention, or a pointer of a block[] attribute the store's" +
		" type nodes declare, at any state), whether a row names it or not, and the digest is every link the" +
		" rows of that block name against every such link its node states now",
	rows: `
		select 'block_ref ' || r.from_node || ' ' || r.kind || ' -> ' || r.to_kind || ' ' || r.to_id,
		       r.node_kind, r.node_address, md5(r.from_node || '|' || named.links)
		from block_refs r
		join (
			select b.from_node,
			       string_agg(b.kind || '>' || b.to_kind || ':' || b.to_id, ','
			                  order by b.kind || '>' || b.to_kind || ':' || b.to_id) as links
			from block_refs b
			where octet_length(b.from_node) <= ` + addressBound + ` and octet_length(b.to_id) <= ` + addressBound + `
			group by b.from_node
		) named on named.from_node = r.from_node
		where octet_length(r.from_node) <= ` + addressBound + ` and octet_length(r.to_id) <= ` + addressBound,
	// Every block node is read, and not only those a row names: a mention or a pointer the store
	// states out of a block no block_refs row names is a link graph_edges answers, and a node
	// side that began at the rows could never find it. A block whose only links are its
	// placement, or tuples no block_refs row projects, states no link here and is no node of this
	// kind; the block kind holds it.
	nodes: `
		select held.id, md5(held.id || '|' || held.links)
		from (
			select n.id, ` + currentLinks(refs.BlockNodeKind, `t.kind in (`+refs.BlockRefKindsSQL+`)`) + ` as links
			from graph_nodes n where n.kind = '` + refs.BlockNodeKind + `' and not ` + lensBlock("n") + `
		) held
		where held.links <> ''`,
	pastBound: `
		select count(*) from block_refs r
		where octet_length(r.from_node) > ` + addressBound + ` or octet_length(r.to_id) > ` + addressBound,
}}

// blockBackfills are the backfill's passes over the document blocks (backfillPasses): the blocks,
// then the links out of them, since a link's node is its block's and the pointer pass stamps a
// link only where its block's node stands (blockNodeStands).
var blockBackfills = []backfillPass{oneKind(backfillBlocks), oneKind(backfillBlockRefs)}
