-- Three columns, each nullable or with a constant default, which Postgres records in the catalog
-- and writes into no stored row, and an ordinary non-unique index. It reads no row to refuse it,
-- and refuses and rewrites none.
select 0;
