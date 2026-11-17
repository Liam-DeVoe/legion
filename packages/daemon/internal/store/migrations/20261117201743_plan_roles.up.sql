-- 20261117201743_plan_roles.up.sql — the roles an owner's plan defines (record's plan.go).
--
-- plan_roles holds each role a plan defines with a role block, for an issue at a generation: its
-- name (the block's id), the version of the plan that wrote it, its type, its model ('' for the
-- default model) and its prompt, the block's body. Each version given writes its roles over the
-- earlier version's (record.Postgres.PutPlanRoles), so the rows of a generation are the roles of
-- the version in force, and the carry of a plan to a new generation copies them
-- (CarryPlan). The delete of a version deletes the roles it wrote, by the cascade from
-- issue_plans. A launch reads a role from here (PlanRoleOf) rather than from Dispatch, so it waits
-- on no read of the plan document. A role's name is lower case and at most 32 characters, the name
-- a step of a task can write.
--
-- The release before this one boots without the table: it names it in no statement, and it
-- refuses a plan that defines a role, so no plan it takes writes a row here. A plan in force with
-- roles before a rollback keeps its rows while that release runs, and a carry it makes to a new
-- generation copies no role, so after the roll forward such a generation's steps name roles the
-- store does not hold, and its owner gives the plan again.
--
-- There is no down file. This directory keeps none, so the way back is the transaction below, run
-- with the daemon stopped, before the earlier image is pinned. A rollback that also goes past the
-- issue plans' tables runs it first, since this table's foreign key stands on issue_plans:
--
--   psql '<postgres_dsn>' -X -v ON_ERROR_STOP=1 <<'SQL'
--   begin;
--   drop table if exists plan_roles;
--   delete from schema_version where version = 20261117201743;
--   commit;
--   SQL
create table plan_roles (
  issue text not null,
  generation bigint not null,
  role text not null constraint plan_roles_role_check check (role ~ '^[a-z][a-z0-9-]{0,31}$'),
  plan_version integer not null,
  type text not null,
  model text not null default '',
  prompt text not null,
  primary key (issue, generation, role),
  foreign key (issue, generation, plan_version) references issue_plans (issue, generation, version) on delete cascade
);
