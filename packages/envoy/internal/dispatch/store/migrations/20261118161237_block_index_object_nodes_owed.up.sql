-- 20261118161237_block_index_object_nodes_owed
--
-- A build's block-index event cursor (20261118115645) gains the time since which a tick of that
-- build owes the object pass: the nodes of the issues, documents, asks and messages a build before
-- this one wrote with no node, and the erase of the nodes it orphaned (docs'
-- writeObjectNodesAnotherBuildWrote). The tick that reads the event log for a roll sets it, in the
-- transaction that moves the cursor, and the tick whose pass finishes with nothing left clears it,
-- so a pass that fails or stops at its batch bound is run again by the next tick of any process of
-- the build, whichever holds the block-index lease then. Null is no debt.
alter table block_index_event_cursors add column object_nodes_owed_at timestamptz;
