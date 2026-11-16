-- Three columns, each nullable or with a constant default, which Postgres records in the catalog
-- and writes into no stored row, an ordinary non-unique index, and the drop of an index no
-- statement reads any more. It reads no row to refuse it, and refuses and rewrites none.
select 0;
