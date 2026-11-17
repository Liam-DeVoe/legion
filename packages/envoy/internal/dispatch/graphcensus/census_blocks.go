package graphcensus

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// blockProjections are the census's kinds of the document blocks (projections): a block against
// its document_blocks row, the links out of a block against its block_refs rows, and the rank
// and the text every block holds (dispatch://INFRA-1253/spec#b-r-one-tree), each against the
// block's row.
var blockProjections = []projection{{
	name:     "block",
	nodeKind: refs.BlockNodeKind,
	table:    "document_blocks",
	note: "one node per row, blocks past the address bound and the blocks of a lens's finding aside; the digest is" +
		" the row's document and the block its parent_block_id names, or the document's top where it names none - the" +
		" issue for a block of an issue's spec, Verification or plan document, the document itself for any other -" +
		" against the in_document and part_of tuples the node states now, a block with no part_of telling itself from" +
		" one at its document's top and a part_of to an ask's node read as the ask's block in the node's own document",
	rows: `
		select 'block ' || d.node_id, d.node_kind, d.node_address,
		       md5(d.node_id || '|' || ` + statedLinks(
		`'`+refs.TupleInDocument+`>artifact:' || a.ref_key`,
		`'`+graphstore.TuplePartOf+`>' || case when d.parent_block_id is null then `+refs.DocumentTopSQL("a")+`
		      else '`+refs.BlockNodeKind+`:' || a.ref_key || '#' || d.parent_block_id end`,
	) + `)
		from document_blocks d
		join artifacts a on a.id = d.artifact_id
		where octet_length(d.node_id) <= ` + addressBound,
	nodes: `
		select n.id, md5(n.id || '|' || ` + placedLinks(kindIn("t.kind", blockLinkKinds)) + `)
		from graph_nodes n where n.kind = '` + refs.BlockNodeKind + `' and not ` + lensBlock("n"),
	pastBound: `
		select count(*) from document_blocks d where octet_length(d.node_id) > ` + addressBound,
	owed: typedNodeOwed,
}, {
	name:     "block_ref",
	nodeKind: refs.BlockNodeKind,
	table:    "block_refs",
	note: "a row is one link out of a block and its node is that block's, so several rows share one node:" +
		" the node side is every block node not gone, a lens's finding aside, that states a link now of a kind" +
		" refs.IndexBlocks writes into block_refs (a mention, or a pointer of a block[] attribute the store's" +
		" type nodes declare, at any state), whether a row names it or not, and the digest is every link the" +
		" rows of that block name against every such link its node states now; a row an ask's node holds, and" +
		" a decision's shaped, from_ask and decided_by, are no part of this kind",
	// The rows are the block_refs rows of those kinds alone (blockLinkKind). A row marked with an
	// ask's node is the ask's shaped, which hangs on that node and not on the block
	// (refs.TypedFacts.pairs), and a decision's shaped, from_ask and decided_by are ties held triple
	// by triple by kinds of their own (decisionProjections): none of them is a link of the block
	// this kind holds rows to.
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
			  and ` + blockLinkKind("b.kind") + ` and b.node_kind is distinct from '` + graphstore.KindAsk + `'
			group by b.from_node
		) named on named.from_node = r.from_node
		where octet_length(r.from_node) <= ` + addressBound + ` and octet_length(r.to_id) <= ` + addressBound + `
		  and ` + blockLinkKind("r.kind") + ` and r.node_kind is distinct from '` + graphstore.KindAsk + `'`,
	// Every block node is read, and not only those a row names: a mention or a pointer the store
	// states out of a block no block_refs row names is a link graph_edges answers, and a node
	// side that began at the rows could never find it. A block whose only links are its
	// placement, its shaped, its from_ask, its decided_by, or tuples no block_refs row projects -
	// its authored_by among them, which the block index states through graphstore.AuthorFacts -
	// states no link here and is no node of this kind; the block kind holds its placement.
	nodes: `
		select held.id, md5(held.id || '|' || held.links)
		from (
			select n.id, ` + currentLinks(refs.BlockNodeKind, blockLinkKind("t.kind")) + ` as links
			from graph_nodes n where n.kind = '` + refs.BlockNodeKind + `' and not ` + lensBlock("n") + `
		) held
		where held.links <> ''`,
	pastBound: `
		select count(*) from block_refs r
		where (octet_length(r.from_node) > ` + addressBound + ` or octet_length(r.to_id) > ` + addressBound + `)
		  and ` + blockLinkKind("r.kind") + ` and r.node_kind is distinct from '` + graphstore.KindAsk + `'`,
}, {
	name:     "block_rank",
	nodeKind: refs.BlockNodeKind,
	table:    "document_blocks",
	note: "the rank of every block of a document that owes no block index: the row side is each row that names its" +
		" node, and the digest is the band of the row's document (refs.DocumentBandSQL) against the first character of" +
		" the rank value its node holds now, so a block with no rank, or with one of another document's band, is a" +
		" difference; a block of a document still owed its index is on neither side, since the reconciliation" +
		" writes its rank, and a census of documents that all owe theirs reads none of this kind",
	rows: `
		select 'block_rank ' || d.node_id, d.node_kind, d.node_address, md5(d.node_id || '|' || ` + refs.DocumentBandSQL("a") + `)
		from document_blocks d
		join artifacts a on a.id = d.artifact_id
		where d.node_kind is not null and octet_length(d.node_id) <= ` + addressBound + ` and not ` + documentOwed("d.artifact_id"),
	nodes: `
		select n.id, md5(n.id || '|' || coalesce(left(` + currentValue(refs.AttributeRank) + `, 1), ''))
		from graph_nodes n
		where n.kind = '` + refs.BlockNodeKind + `' and not ` + lensBlock("n") + ` and not ` + blockDocumentOwed("n"),
	pastBound: `
		select count(*) from document_blocks d where octet_length(d.node_id) > ` + addressBound,
	emptyAgrees: true,
}, {
	name:     "block_text",
	nodeKind: refs.BlockNodeKind,
	table:    "document_block_texts",
	note: "the text of every block of a document that owes no block index: the row side is each document_blocks row" +
		" that names its node, and the digest is the sha256 of the newest text its document_block_texts rows hold, or" +
		" the row's text_sha256 where none holds one, against the sha256 of the text value its node holds now, so a" +
		" block with no text, or with another text than the one its rows hold, is a difference, and an erase that" +
		" rewrites both alike reads none; a block of a document still owed its index is on neither side, since the" +
		" reconciliation writes its text, and a census of documents that all owe theirs reads none of this kind",
	// The projection of a text value is the newest document_block_texts row of its block (R15 of
	// dispatch://INFRA-1253), and text_sha256 only for a block no row of that table records the
	// text of: the erase rewrites the quote of an erased block in both a holding block's rows and
	// its text values (grapherase's redactBlocks) and leaves text_sha256 as the version stated it.
	rows: `
		select 'block_text ' || d.node_id, d.node_kind, d.node_address,
		       md5(d.node_id || '|' || coalesce((
		           select encode(sha256(convert_to(t.text, 'UTF8')), 'hex') from document_block_texts t
		           where t.artifact_id = d.artifact_id and t.block_id = d.block_id and t.text is not null
		           order by t.version desc limit 1), d.text_sha256))
		from document_blocks d
		where d.node_kind is not null and octet_length(d.node_id) <= ` + addressBound + ` and not ` + documentOwed("d.artifact_id"),
	nodes: `
		select n.id, md5(n.id || '|' || coalesce(encode(sha256(convert_to(` + currentValue(graphstore.AttributeText) + `, 'UTF8')), 'hex'), ''))
		from graph_nodes n
		where n.kind = '` + refs.BlockNodeKind + `' and not ` + lensBlock("n") + ` and not ` + blockDocumentOwed("n"),
	pastBound: `
		select count(*) from document_blocks d where octet_length(d.node_id) > ` + addressBound,
	emptyAgrees: true,
}}

// blockLinkKind is true of the tuple or block_refs kind the SQL expression kind names while it is
// a link of a block the block_ref kind holds: a kind refs.IndexBlocks writes into block_refs
// (refs.BlockRefKindsSQL, read from the store inside the census's own snapshot), but the ties of a
// decision, which a kind of their own holds triple by triple (tieProjection): a decision's shaped
// is a pointer of its block, and held here as well it would read a correct row of a drifted
// decision as a difference, every row of that block sharing one digest.
func blockLinkKind(kind string) string {
	return `(` + kind + ` in (` + refs.BlockRefKindsSQL + `) and ` + kind + ` not in ('` + refs.TupleShaped + `', '` +
		refs.TupleFromAsk + `', '` + graphstore.TupleDecidedBy + `'))`
}

// documentOwed is true of the document whose id the SQL expression artifactID names while it owes
// its block index, by either mark the block-index reconciliation lists a document by
// (refs.OwedBlockIndexDebts, docs' owedDocuments): its version null, or this release's own mark,
// store_owed_at, which the backfill and its drain set (writeBlockBatch, drainBlockIndexes) and a
// release before this one neither reads nor clears. The reconciliation has yet to write the ranks
// and the texts of its blocks.
func documentOwed(artifactID string) string {
	return `exists (select 1 from document_block_versions owed where owed.artifact_id = ` + artifactID +
		` and (owed.version is null or owed.store_owed_at is not null))`
}

// blockDocumentOwed is documentOwed of the document of the block node alias, named by the part
// of its id before the first '#', since a ref_key holds none.
func blockDocumentOwed(alias string) string {
	return `exists (
			select 1 from artifacts document
			where document.ref_key = split_part(` + alias + `.id, '#', 1) and ` + documentOwed("document.id") + `)`
}

// blockBackfills are the backfill's passes over the document blocks (backfillPasses): the blocks,
// then the links out of them, since a link's node is its block's and the pointer pass stamps a
// link only where its block's node stands (blockNodeStands), then the one drain of every stored
// document's block index (drainBlockIndexes).
var blockBackfills = []backfillPass{oneKind(backfillBlocks), oneKind(backfillBlockRefs), oneKind(drainBlockIndexes)}

// blockDrainKind is the kind the backfill's report names the drain by.
const blockDrainKind = "block_index_drain"

// drainBlockIndexes marks every stored document owed its block index once, with this release's
// own mark, document_block_versions.store_owed_at (migration 20261116221539), as the block pass
// marks each document whose rows it names (writeBlockBatch), so the block-index reconciliation
// (docs.Service.RunBlockIndexReconciliation) indexes each again, under the fence it already takes,
// and gives every block an earlier build indexed what that build did not write: its rank, its text
// and its placement, and, for a top-level block of an issue's spec, Verification or plan document,
// its part_of to the issue (refs.IndexBlocks). That index writes no document_blocks row for a
// block whose text is unchanged, so no block's changed_version and no document_block_texts row
// moves for it.
//
// The mark is not the null version refs.RebuildAll sets. A pod of the release before this one,
// during a roll or after a rollback, reads a null version as its own debt: it indexes the
// document, writes its rows and states none of these values, and the document would be owed
// nothing again, while the drain, done, never marks it twice. That release neither reads nor
// clears store_owed_at, so this release's reconciliation, which lists a document holding it (docs'
// owedDocuments) and whose fence wins at the version the document already stands at while it
// holds it (refs' standsAtVersion), still reaches the document, and its pass clears the mark.
//
// issue_tree_block_drain records how far the drain has got, and the backfill runs at every boot,
// so it marks the documents once: a run that finds the drain done marks nothing, and a run stopped
// part way resumes after the last document its last committed batch marked. It reports the
// documents it marked as the kind's Owed, and the kind as complete once the drain is done.
func drainBlockIndexes(ctx context.Context, database *store.Store) (KindBackfill, error) {
	done := KindBackfill{Kind: blockDrainKind}
	if _, err := database.Pool.Exec(ctx, `
		insert into issue_tree_block_drain default values on conflict (singleton) do nothing
	`); err != nil {
		return done, fmt.Errorf("record the block index drain: %w", err)
	}
	for {
		marked, finished, err := drainBlockIndexBatch(ctx, database)
		done.Owed += marked
		if err != nil {
			return done, err
		}
		if finished {
			break
		}
	}
	done.Complete = true
	return done, nil
}

// drainBlockIndexBatch marks one batch of documents owed, in one transaction, and answers how many
// it marked and whether the drain is done. It takes the drain's row first, so two runs take turns,
// then the batch's owner rows and their document_block_versions rows in the store's order
// (lockBlockDocuments), the rows a version write holds while it writes, so a write in flight
// finishes before its document is marked and the mark is never lost under it. A document already
// holding the mark is left as it is; one whose version is null is marked too, since a pod of the
// release before may drain that version first.
//
// The cursor starts at the nil uuid, below every document's id, as the object pass's does
// (zeroUUID), so the batch's read binds one bound of the keyset and no test of whether there is
// one: under a generic plan that test would read from the first document every batch.
func drainBlockIndexBatch(ctx context.Context, database *store.Store) (int64, bool, error) {
	tx, err := database.Pool.Begin(ctx)
	if err != nil {
		return 0, false, fmt.Errorf("begin a block index drain batch: %w", err)
	}
	defer tx.Rollback(ctx)
	var after string
	var finished bool
	if err := tx.QueryRow(ctx, `
		select coalesce(after, $1::uuid)::text, done from issue_tree_block_drain for update
	`, zeroUUID).Scan(&after, &finished); err != nil {
		return 0, false, fmt.Errorf("read the block index drain: %w", err)
	}
	if finished {
		return 0, true, tx.Commit(ctx)
	}
	rows, err := tx.Query(ctx, `
		select b.artifact_id::text
		from document_block_versions b
		join artifacts a on a.id = b.artifact_id
		where a.kind = 'doc' and b.artifact_id > $1::uuid
		order by b.artifact_id
		limit $2
	`, after, backfillBatch)
	if err != nil {
		return 0, false, fmt.Errorf("list the documents a block index drain batch marks: %w", err)
	}
	documents, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, false, fmt.Errorf("list the documents a block index drain batch marks: %w", err)
	}
	var marked int64
	if len(documents) > 0 {
		if err := lockBlockDocuments(ctx, tx, documents); err != nil {
			return 0, false, err
		}
		tag, err := tx.Exec(ctx, `
			update document_block_versions set store_owed_at = now()
			where artifact_id = any($1::uuid[]) and store_owed_at is null
		`, documents)
		if err != nil {
			return 0, false, fmt.Errorf("mark a batch of documents owed this release's block index: %w", err)
		}
		marked = tag.RowsAffected()
		after = documents[len(documents)-1]
	}
	finished = len(documents) < backfillBatch
	if _, err := tx.Exec(ctx, `
		update issue_tree_block_drain set after = $1::uuid, done = $2
	`, after, finished); err != nil {
		return 0, false, fmt.Errorf("record how far the block index drain has got: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, false, fmt.Errorf("commit a block index drain batch: %w", err)
	}
	return marked, finished, nil
}
