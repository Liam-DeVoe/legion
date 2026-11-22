-- 20261122060000_spec_sources.up.sql
--
-- The repositories whose approved spec a project holds (D5 of the work-units plan): one row per
-- project and repository, `owner/name` as a repository mapping stores it, with the branch whose
-- files The Spec of that repository is read from and whether the source is on. An issue holds a
-- change request when its repository is an enabled spec source of its project (D1), so the
-- approval of its spec writes the approved spec of that repository (internal/dispatch/specstore).
-- A project names several, as it names several architecture sources.
create table spec_sources (
  project_key text not null references projects (key) on delete cascade,
  repo text not null,
  branch text not null,
  enabled boolean not null default true,
  created_by jsonb not null,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  primary key (project_key, repo)
);
