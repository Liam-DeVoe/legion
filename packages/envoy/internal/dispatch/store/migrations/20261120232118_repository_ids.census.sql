-- 20261120232118 adds one nullable column to issues and one to repo_projects, each with no default,
-- so every existing row reads null without being rewritten, and builds an index over the issues
-- whose new column is set, which no row is yet; nothing refuses any row. It cannot name the
-- columns, which do not exist when the census is taken.
select 0;
