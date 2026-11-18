-- 20261110224508 adds six nullable columns with no default, creates one table with its two
-- indexes, and replaces one trigger: no row that exists before it can violate anything it adds,
-- no row is read or rewritten, and the locks its catalogue writes take on comments, on messages
-- and on artifact_versions are released with the migration's own transaction. Neither body
-- column loses its `not null`, so no stored body is refused either.
select 0;
