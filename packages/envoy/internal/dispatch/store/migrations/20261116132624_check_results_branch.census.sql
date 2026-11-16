-- 20261116132624 adds a nullable column with no default to check_results and writes no row: every
-- stored row reads null there, which is a result a role recorded. Nothing is refused or rewritten.
select 0;
