-- 20261120171308_pull_request_reads_owed_link_backfill.down.sql: the by-hand way back of the
-- backfill of links' owed reads. It deletes each owed read that the backfill wrote and that no
-- later owe changed: its owed_at is still the time of the migration's transaction, which the
-- runner recorded as applied_at of version 20261120171308 in the same transaction, and its
-- linked_revision is still its revision. A later link write gives the row a new owed_at, and any
-- other owe a new revision, so neither is deleted; a read that GitHub answered has deleted its row
-- already. Run it before the version's row is deleted from schema_migrations, if that row is
-- deleted at all: with no such row it deletes nothing.
delete from pull_request_reads_owed o
where o.linked_revision = o.revision
	and o.owed_at = (select m.applied_at from schema_migrations m where m.version = 20261120171308);
