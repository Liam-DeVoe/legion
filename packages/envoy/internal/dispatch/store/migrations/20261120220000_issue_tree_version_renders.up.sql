-- 20261120220000_issue_tree_version_renders.up.sql — the versions rendered from the tree.
--
-- A version of a document is rendered from the tree's tuples once a pass of this release writes
-- that version's tree, and every version written before keeps the bytes it stores. version_renders
-- holds one row for each version rendered from the tree, written in the transaction that writes
-- the version's tree (docs.QueueVersionRender), so the row and that tree commit together. A
-- version with no row answers artifact_versions.markdown byte for byte (docs.RenderVersion); the
-- boundary is the row, so no tuple, however it is dated, moves a version that holds none.
--
--   record_sha256  the digest of the record of the document's query the version was written with
--                  (docs.IssueTree.QueryRecord), which artifact_version_text_sha256 answers for the
--                  version (20261120220100) and so an approval of it pins.
--   at             the time the version renders as of: the time of the cut its record was read at,
--                  no earlier than every tuple the writing transaction dated before it read the cut,
--                  the time of the pass that wrote the version's tree among them.
--   read_seq       the highest graph_tuples.seq the record read.
--
-- A row goes with its document: the foreign key cascades, as every row of a document's own table
-- does, so a removal of an untouched document leaves none behind. No key reaches artifact_versions,
-- which a key would make the pre-deploy census size.
--
-- Rolling deploy. The table is new and holds no row; the release before names it nowhere and
-- reads every version's stored markdown, which is the render of every version this table holds.
-- Rolling back past this migration is an image rollback alone; the table left behind is read by
-- nothing that release runs, and its digest function (20261120220100) answers the stored markdown's
-- digest again only once that migration is rolled back too.
create table version_renders (
  artifact_id uuid not null references artifacts (id) on delete cascade,
  number integer not null,
  record_sha256 text not null check (record_sha256 ~ '^[0-9a-f]{64}$'),
  at timestamptz not null,
  read_seq bigint not null,
  primary key (artifact_id, number)
);
