-- The migration creates issue_head_nodes and issue_head_marks, two new tables, and an index of
-- the first, then a function and a trigger on each of graph_tuples and graph_nodes, which take a
-- lock and write the catalog: it writes no row, rewrites none, and a trigger runs only on later
-- writes, never on the rows already there. The census names no table only a pending migration
-- creates, so it counts nothing of the two new ones. The number of existing rows this migration
-- would refuse or rewrite is therefore 0.
select 0
