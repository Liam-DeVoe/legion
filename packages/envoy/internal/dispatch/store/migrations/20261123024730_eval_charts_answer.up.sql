-- 20261123024730_eval_charts_answer.up.sql
--
-- An eval's declared charts and its answer (dispatch://INFRA-2918, R20; root dispatch://INFRA-979,
-- R38) are columns of its versioned definition, so a change of either writes the next version and
-- every version a reader once saw keeps its own (evals.Register copies both forward on a version
-- that does not set them):
--   charts   the list of chart declarations the Evals tab draws the eval's readings by, as a
--            register write gave them, each list accepted by the eval platform's validate_charts
--            rules before it is stored (evals/charts); '[]' for an eval that declares none.
--            eval_versions_charts_array holds it to a list.
--   answer   a short text that answers the eval's question, which a page draws beside its number;
--            '' for none.
-- The eval's detail read answers both; the list reads and the live evals view answer answer and
-- never charts, so no first paint carries a chart's declaration.
--
-- evals_current is stated again so its rows carry the two columns: the `*` of its query was
-- expanded when 20261117100005 created it. `create or replace view` keeps every column the view
-- had, in its place, and appends the two, so the release before this one, which names the columns
-- it reads, reads the view as before.
--
-- Both columns are added with a constant default, which Postgres writes into the catalog alone
-- and every existing version reads: no charts and no answer. The check is a statement of its own,
-- added not valid: inline, it would read every row of the table while the migration holds the
-- table's ACCESS EXCLUSIVE lock, and not valid still refuses every row written after it, where
-- every row before it holds '[]'. The release before this one names neither column in its insert,
-- so a version it writes, during the roll or after a rollback, holds the defaults: such a version
-- of an eval that declared charts or an answer declares none, until a register write sets them
-- again. The migration takes eval_versions' and evals_current's ACCESS EXCLUSIVE for its short
-- transaction, so a registry write and read wait behind it; it refuses and rewrites no row, and its
-- census answers 0.
alter table eval_versions
    add column charts jsonb not null default '[]'::jsonb,
    add column answer text not null default '';

alter table eval_versions
    add constraint eval_versions_charts_array check (jsonb_typeof(charts) = 'array') not valid;

create or replace view evals_current as
    select distinct on (project_key, eval_id) *
    from eval_versions
    order by project_key, eval_id, version desc;
