-- 20261116014445_eval_set_by_person.up.sql
--
-- The record of which fields of an eval a person set (dispatch://INFRA-2916, R5). The seed fills
-- the catalogue's target, its inclusiveness and its judges, a series' min_n and an eval's method
-- on an eval it takes over: one of the catalogue's slugs a reading or a register write registered
-- before any seed of its project. It read a person's choice off the stored values, a version
-- whose target differs from the one before it, and a person who set a target to none while it was
-- already none moved no value: the register write stored no version (evals.sameDefinition), and
-- the next seed wrote the catalogue's target over that person's choice, which is a write accepted
-- and then ignored.
--   set_by_person   every field of the row a person's register write has named, as the body
--                   spells it, sorted and each once: on a version `target`, `target_inclusive`,
--                   `target_judges`, `owner_role`, `person`, `issue_key`, `state`, `replaced_by`,
--                   `links`, `doc_text` and `doc_block`; on a series `min_n`, the one field of a
--                   series a person sets on a computed eval. It is written whether or not the
--                   write moved the field's value, and every version after it carries it
--                   forward, whoever writes it, so the newest version alone answers whether a
--                   person has set a field and the seed needs no read of the versions before it
--                   (evals' takeCatalogueFields and seriesToWrite). The fields of the
--                   computation are not among them: the catalogue states those on a computed
--                   eval, which is the one kind of eval the seed writes over.
--
-- Both views are replaced because each was created as `select *`, which Postgres expands into the
-- column list the view keeps: a new column of the table reaches a reader of evals_current or
-- eval_series_current only once the view states it. `create or replace view` takes a query whose
-- columns are the view's with columns added at the end, which `select *` over the widened table
-- is, and it rewrites no row.
--
-- One column on each of two tables, each `not null` with a constant default, which Postgres
-- records in the catalog and writes into no stored row: the statement reads no row, refuses none
-- and rewrites none, and its census answers 0. It takes ACCESS EXCLUSIVE on eval_versions and
-- then on eval_series, the order every writer of the two takes them in (a register write and a
-- seed insert a version of the definition before a version of a series), so it closes no cycle
-- with one; each lock lasts as long as the catalog change, under the runner's five-second bound
-- on the wait.
--
-- The release before this one names the columns it reads and the columns it writes in both
-- tables, so it goes on working beside this one (cmd/dispatch/README.md, "Database migrations"):
-- a version it writes takes the default, an empty record, which is what that build already reads
-- an eval no person has set as. This release writes the column on every insert. Rolling back past
-- this migration is an image rollback alone.
alter table eval_versions add column set_by_person text[] not null default '{}';
alter table eval_series add column set_by_person text[] not null default '{}';

create or replace view evals_current as
    select distinct on (project_key, eval_id) *
    from eval_versions
    order by project_key, eval_id, version desc;

create or replace view eval_series_current as
    select distinct on (eval_id, series) *
    from eval_series
    order by eval_id, series, version desc;
