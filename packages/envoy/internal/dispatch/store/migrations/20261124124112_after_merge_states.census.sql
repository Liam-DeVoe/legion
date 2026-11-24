-- 20261124124112 adds four columns, two to pull_request_heads and two to check_results, each with
-- a constant default or none, so every stored row takes the default without being rewritten and
-- none is refused; builds two partial indexes, which read those tables' rows but change none; and
-- creates two new tables. It cannot name the columns, which do not exist when the census is taken.
select 0;
