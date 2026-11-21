-- The migration creates version_renders, a new table, and nothing else: it writes no row and
-- rewrites none, and the only table it names beside its own is artifacts, which its foreign key
-- references and which takes no statement. The census names no table only a pending migration
-- creates, and version_renders is one, so it counts nothing. The number of existing rows this
-- migration would refuse or rewrite is therefore 0.
select 0
