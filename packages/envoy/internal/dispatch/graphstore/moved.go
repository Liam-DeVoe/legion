package graphstore

import (
	"context"
	"fmt"
)

// TupleMovedTo points from a block address that no longer stands to the block it moved to
// (dispatch://INFRA-1150 R8): a move of a block from one document to another is a delete and an
// insert, which changes the block's address, so the move appends one of these, from the old
// address to the new one, with the time of the move, and rewrites no stored pointer. The restore
// and the conversion of that issue write it; dispatch://INFRA-1253 moves blocks through it again.
// The pre-mortem conversion (api.ConvertPremortem) appends one from the address of the document it
// retires - kind artifact, the document's ref_key - to the lens it wrote in the document's place.
// No table projects it.
const TupleMovedTo = "moved_to"

// MovedToHops is how many moved_to tuples a reader follows from one address before it reads the
// address as resolving to no block. block_moved_to, the SQL function every resolver reads an
// address through (migrations 20261117002006 and 20261118165901), holds the same bound in its
// loop, and the census follows a far end no further (graphcensus' storeOnlyRead).
const MovedToHops = 8

// MovedTo answers the block address a document_blocks row holds that address resolves to, and
// true, in one order (T17 of this branch's plan): the far end of the newest moved_to
// that address states now, read as AsOfOut reads a relation, and so on from there while another
// current moved_to stands at the far end; then, where no tuple stands, the same block id in the
// room the reached address's document is edited in (its document_rooms row: the node
// `<tree ref_key>#<block id>` of an issue room); then the reached address itself. It answers false
// where neither of the last two is held, and where the walk stops early: a far end that is no
// block, a cycle, or a chain longer than MovedToHops.
//
// From a document's address, its ref_key, which holds no '#' where a block's address always
// holds one, it answers the id of the lens the newest moved_to that address states points at, and
// true, where that lens is not gone: the lens the pre-mortem conversion retired the document into
// (migration 20261117042519). It answers false for a document's address that states no such
// moved_to, and for one whose lens is gone.
//
// The rule is block_moved_to's, called here alone, so the reading of api's blockReferenceNode and
// the readings refs.Closure, refs.BlockLinks, refs.BlockTargets and the block index's pointer
// writer make inside their own statements (refs.ResolvedBlockSQL) are one rule.
func MovedTo(ctx context.Context, q Queryer, address string) (string, bool, error) {
	var reached *string
	if err := q.QueryRow(ctx, `select block_moved_to($1)`, address).Scan(&reached); err != nil {
		return "", false, fmt.Errorf("follow the moves of %s: %w", address, err)
	}
	if reached == nil {
		return "", false, nil
	}
	return *reached, true, nil
}
