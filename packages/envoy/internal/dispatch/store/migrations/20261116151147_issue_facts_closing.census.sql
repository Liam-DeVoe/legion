-- Four columns, each nullable or with a constant default, which Postgres records in the catalog
-- and writes into no stored row, three ordinary non-unique indexes, the drop of an index no
-- statement reads any more, and the replacement of one trigger function. It reads no row to refuse
-- it, and refuses and rewrites none.
select 0;
