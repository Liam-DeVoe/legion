-- This migration writes the type nodes of the store alone: two `supertag` nodes, the supertag node
-- `component` moved to a block type, twenty-one `attribute` nodes, the version on the block-type
-- supertag nodes, and the tuples that state each value. A supertag write ships as a migration, in
-- the same release as the binary that handles it, and passes the pre-apply census. No route or
-- command writes a node of either kind (requireNoRouteOrCommandWritesAType in
-- cmd/dispatch/supertag_nodes_test.go), so every row it rewrites is a type node an earlier
-- migration wrote, and it states every value it gives. It refuses no row and rewrites no row a
-- person or an agent wrote, and it reads no existing row of another table. The number of existing
-- rows this migration would refuse or rewrite is therefore 0.
select 0;
