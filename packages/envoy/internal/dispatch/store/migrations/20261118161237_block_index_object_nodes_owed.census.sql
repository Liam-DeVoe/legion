-- block_index_event_cursors gains one nullable column with no default, which is a catalog change:
-- no stored row is rewritten, and no constraint is added that a stored row could fail. The column
-- is null in every existing row. The number of existing rows this migration would refuse or
-- rewrite is therefore 0, whatever block_index_event_cursors holds.
select 0;
