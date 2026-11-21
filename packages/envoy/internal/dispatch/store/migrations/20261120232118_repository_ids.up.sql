-- 20261120232118_repository_ids.up.sql — the GitHub id of the repository a name meant when a
-- person wrote it, and of the repository each pull request and run Delivery stores is of.
--
-- A repository's name is not its identity: GitHub renames a repository or moves it to another
-- owner and keeps its id, and answers the old name with the repository it moved to until another
-- repository takes that name. So Dispatch records the id beside each name a person writes, at the
-- write that names it, read from GitHub as Dispatch's App for that name then (api's
-- writtenRepository): an issue's own repository (issues.repo_id beside issues.repo) and a project's
-- default repository (repo_projects.repo_id on the mapping row that is_default marks). The GitHub
-- proxy decides whose a repository is by these ids (refs.RepositoryClause), and a project's owner
-- confirms the id of each name its issues hold with none before the project turns private
-- (refs.UnconfirmedRepositories, refs.ConfirmRepository).
--
-- Delivery records beside each pull request and run it stores the id GitHub's own answer about it
-- carries (delivery_pull_requests.repo_id, delivery_runs.repo_id): a webhook payload's repository,
-- a pull request's base repository, a run's repository. A pull request that names no issue, and a
-- run that ships none, is held to the same rule by that id.
--
-- Nothing here reads GitHub, and no row gets an id: a name stored before this migration meant a
-- repository nobody recorded, and a read of that name now can answer a repository that took the
-- name later. Such an issue or default holds its name with no id until a write names a repository
-- for it or its project's owner confirms it, and such a pull request or run until GitHub's next
-- answer about it is stored.
--
-- Adding a nullable column with no default writes the catalog alone, so each holds its table's
-- ACCESS EXCLUSIVE lock for milliseconds and rewrites no row. The index is over the rows whose id is
-- set, none of them yet, so building it reads issues once and writes an empty index; it holds the
-- table's SHARE lock, which waits for writers, for as long as that read. The release before this
-- one neither reads nor writes any of the columns, so it serves beside it unchanged: a name it
-- writes during the roll is stored with no id, as a name written before the migration is.
--
-- Its identifier is the UTC moment it was written, as `migrationid` requires of every migration
-- past the numbered series, which closed at 0161.
alter table issues add column repo_id bigint;
alter table repo_projects add column repo_id bigint;
alter table delivery_pull_requests add column repo_id bigint;
alter table delivery_runs add column repo_id bigint;
create index issues_repo_id on issues (repo_id) where repo_id is not null;
