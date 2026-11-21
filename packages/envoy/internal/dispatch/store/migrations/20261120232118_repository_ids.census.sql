-- 20261120232118 adds one nullable column, with no default, to each of issues, repo_projects,
-- delivery_pull_requests and delivery_runs, so every existing row reads null without being
-- rewritten, and builds an index over the issues whose new column is set, which no row is yet;
-- nothing refuses any row. It cannot name the columns, which do not exist when the census is taken.
select 0;
