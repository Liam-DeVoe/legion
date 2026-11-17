-- Two columns, each with a constant default, which Postgres records in the catalog and writes
-- into no stored row, and two views replaced, which read no row. It reads no stored row, and
-- refuses and rewrites none.
select 0;
