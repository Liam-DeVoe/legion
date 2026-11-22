-- pgmigrate:non-transactional
-- 20261122010200_graph_erasures_block_document.up.sql
-- graph_erasures (20261109013400) holds a row for each node an erase (envoy-dispatch erase)
-- removed, so it grows with every erase, and its one index is its primary key. The record of a
-- related-item check (api's relatedCheckInsert) reads it under the owner lock of the check's issue
-- or document, twice: for an erase of the decision block the check is of, whose node is its
-- document's ref_key, '#' and the block's id, and, for an issue's own check, for an erase of any
-- block of the issue's spec. So each read reads the whole of this table while it holds that lock.
-- This index lets both probe it by the document a block's node names, the part of the node before
-- its first '#': the spec's ref_key, and the decision block's document's, beside the block's whole
-- node.
--
-- It holds the erasures of blocks alone, since both reads name `node_kind = 'block'`, and its one
-- key is the document. An index led by node_kind would serve a read that names the document too,
-- but it would also answer one that names it nowhere, by `node_kind = 'block'` alone, with every
-- block erasure; this one cannot be probed without the document. Its key is a document's ref_key,
-- which holds no '#' (0121), so it is the part of a block's node before the first one, and which
-- artifacts_ref_key_key's B-tree already holds, so no key is too large for this B-tree either.
--
-- It is built `concurrently`, outside the runner's transaction: a plain build would hold SHARE on
-- graph_erasures, which blocks every write, an erase's record among them, for the length of a read
-- of the table. A concurrent build takes SHARE UPDATE EXCLUSIVE, which no ordinary read or write
-- waits on, and the census exempts it from its table size limit on that ground (pgmigrate's
-- concurrentIndex). A failed build leaves an INVALID index of this name, which the runner drops
-- with DROP INDEX CONCURRENTLY and builds again on the next attempt. It is a performance index: no
-- constraint and no later migration depends on it.
create index concurrently if not exists graph_erasures_block_document
    on graph_erasures (split_part(node_id, '#', 1))
    where node_kind = 'block';
