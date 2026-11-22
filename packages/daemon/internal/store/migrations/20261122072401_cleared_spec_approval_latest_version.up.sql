-- 20261122072401_cleared_spec_approval_latest_version.up.sql — the newest version of the document
-- that the record of a spec approval a root's re-admission cleared knows of
-- (record.ClearedSpecApproval.LatestVersion). The record stands for the spec row the re-admission
-- deleted while the root holds no spec row (workflow's clearedSpecRow). That row ignores a changes
-- request on a version older than its latest (workflow's artifactIgnored), so the record needs
-- that version too. The re-admission writes it from the row's latest version (admit's
-- recordSpecApproval). A new version of that document raises it while no row stands, and a later
-- approval moves it (workflow's clearedSpecEvent). Each record the table already holds takes its
-- approved version, the lowest latest version a row can have with that approval. A late changes
-- request on a version above it then deletes that record, as the release before deleted it.
--
-- The release before names each column of cleared_spec_approvals it reads and writes
-- (record.Postgres.ClearedSpecApproval and PutClearedSpecApproval). It reads the records this
-- migration leaves. Its insert names no latest_version, which is not null and has no default, so
-- under it each re-admission of a root whose spec a person approved fails until this way back has
-- run.
--
-- There is no down file. This directory keeps none, so the way back is the one transaction that
-- closes this header, its last paragraph (migrations.WayBack), run with this release's daemon
-- stopped and before the release before starts, through psql '<postgres_dsn>' -X -v ON_ERROR_STOP=1.
-- Run it before the way back of 20261120210115_approval_route_records. That way back drops the
-- table, and with this migration's row still recorded, the roll-forward would create the table
-- again without the column.
--
-- The way back drops the column and deletes this migration's row, which makes the version the
-- store reports (store.SchemaVersionTx) that release's own again. At the roll-forward store.apply
-- finds no row of this version and runs this file again, and each record takes its approved
-- version as its latest version. What it costs is the newest version each record knew of: after
-- the roll-forward, a late changes request on a version between a record's approved version and
-- that newest version deletes the record, where the row would have ignored it.
--
-- begin;
-- alter table if exists cleared_spec_approvals drop column if exists latest_version;
-- delete from schema_version where version = 20261122072401;
-- commit;
alter table cleared_spec_approvals add column latest_version integer;
update cleared_spec_approvals set latest_version = approved_version;
alter table cleared_spec_approvals alter column latest_version set not null,
  add constraint cleared_spec_approvals_latest_version_check
    check (latest_version >= approved_version);
