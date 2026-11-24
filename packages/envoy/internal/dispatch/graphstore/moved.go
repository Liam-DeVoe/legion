package graphstore

// TupleMovedTo points from a block address that no longer stands to the block it moved to: a
// move of a block from one document to another is a delete and an
// insert, which changes the block's address, so the move appends one of these, from the old
// address to the new one, with the time of the move, and rewrites no stored pointer. The restore
// and the conversion of that issue write it; a later change moves blocks through it again.
// The conversion also points a section's heading at an ask node, where the section cites an
// answered ask a person removed from its spec: the chain ends at that ask. No table projects it.
const TupleMovedTo = "moved_to"

// MovedToHops is how many moved_to tuples a reader follows from one address before it reads the
// address as resolving to no block. moved_to_reach, the SQL function every resolver reads a move
// through (migration 20261117002006), holds the same bound in its loop, and the census follows a
// far end no further (graphcensus' storeOnlyRead).
const MovedToHops = 8
