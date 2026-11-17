-- 20261117082309_issue_branches.up.sql — the branch each issue is worked on, stored rather than
-- named after the issue.
--
-- issue_branches.branch is record.Issue.Branch: the GitHub branch the issue's roles push to, the
-- jj bookmark of its workspace, the branch the daemon creates for it (the outbox's issue_branch
-- row) and the head branch of its pull request. The daemon links a pull request to its issue by
-- looking its head branch up here (record.Store.IssueForBranch), never by reading a key out of the
-- name. Before this table every reader built the branch as legion/<KEY>, which published the
-- issue's Dispatch key in every branch and pull request of the repositories Legion works, and some
-- of those are public.
--
-- A row is written once and never changed or deleted. record.Store.PutIssue writes one as an issue
-- is first recorded, and POST /legion/v1/issues/branch (and its operator twin) writes one for an
-- issue not yet recorded, so a branch someone creates at that name before admission is the one the
-- issue takes when it is recorded. A new row's branch is legion/<16 lowercase hex>
-- (claim.NewIssueBranch). The key is no foreign key of issues: a row can precede its issue, and it
-- outlives a record that is dropped and made again, which then finds its old branch.
--
-- Every issue recorded before this migration gets legion/<KEY>, the branch its roles have pushed
-- to and its pull request is on, so an open branch keeps working with no reader that knows the
-- old shape. A pull request recorded on another branch than that is left as it is: the workflow
-- records a pull request only on its issue's branch, so none exists.
--
-- There is no down file; migrations here are forward-only. The way back is
--   drop table issue_branches;
--   delete from schema_version where version = 20261117082309;
-- run with the daemon stopped, before the earlier image is pinned; without the delete, a later roll
-- forward finds the version recorded, skips this file, and every issue read fails on the table it
-- names. That earlier image names every issue's branch legion/<KEY> again, so an issue recorded
-- under this release works on a branch it can no longer find: its pushed work stays on GitHub
-- under the stored name, which this table held.
create table issue_branches (
  issue text primary key,
  branch text not null unique,
  created_at timestamptz not null default now()
);

insert into issue_branches (issue, branch)
select key, 'legion/' || key from issues;
