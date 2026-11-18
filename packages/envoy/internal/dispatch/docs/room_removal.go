package docs

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/reearth/ygo/crdt"
)

// RemovedFragment is a document's fragment of its issue room as the removal of the document reads
// it (ReadRemovedFragment), in the removal's transaction (api's removeArtifact). A document of an
// issue room has no history of its own: its text is its fragment of the room's history, which every
// other document of the room writes too, so its removal weighs the fragment rather than the room's
// updates, and takes the fragment out of the room (Leave).
type RemovedFragment struct {
	// Room is the issue room the document is edited in, the issue's tree artifact, whose layout the
	// removal changes: the removal announces it (AnnounceDocumentRooms) and closes it once it
	// commits, so the room's next load reads the room without the document.
	Room string
	// Unsettled is whether the document holds an edit no version of it holds: a pending author an
	// update of the room recorded under the document (pendingAuthorsIn), which only an update that
	// changed its fragment writes and only a version of the document clears, or a fragment that
	// reads otherwise than the document's latest version.
	Unsettled bool
	// IndexOwed is whether the room's block index is owed a version of one of its documents: a
	// write the request could not index, because another fragment of the room held an edit no
	// version holds yet (indexDocumentInRoom), whose pass the settlement or the block-index
	// reconciliation runs later (indexOwedRoom). Until it runs, what that write cites is in no
	// table, so a removal that reads what cites the document refuses rather than miss it.
	IndexOwed bool
	fragment  string
	// doc is the room's stored history at its head, read under the room's lock; nil for a room that
	// stores no update.
	doc *crdt.Doc
}

// ReadRemovedFragment reads, in tx, what the removal of the document artifactID weighs of its
// fragment of the issue room it is edited in (RemovedFragment), and answers nil for a document that
// is its own room, whose stored updates are its own. tx holds the document's issue row and then the
// room's lock (LockDocumentRoom), the order every writer of a room takes them in, so no append lands
// between this read and the removal's commit. The room is resolved as every room entry point
// resolves a document (roomOf, fragmentOf), and its history is read through tx at the head tx reads,
// as the erase reads it (ReadErasedLiveText): what the room has stored, the durable terms the removal
// judges a document in, never a room's unstored updates.
func ReadRemovedFragment(ctx context.Context, tx pgx.Tx, artifactID string) (*RemovedFragment, error) {
	room, err := roomOf(ctx, tx, artifactID)
	if err != nil {
		return nil, err
	}
	if room == artifactID {
		return nil, nil
	}
	removed := &RemovedFragment{Room: room, fragment: fragmentOf(room, artifactID)}
	var owed bool
	var latest string
	if err := tx.QueryRow(ctx, `
		select exists(select 1 from doc_pending_authors where artifact_id = $1::uuid),
		       coalesce((select markdown from artifact_versions where artifact_id = $1::uuid order by number desc limit 1), ''),
		       exists(select 1 from document_block_versions b
		              where (b.artifact_id = $2::uuid or b.artifact_id in (select d.artifact_id from document_rooms d where d.room = $2::uuid))
		                and (b.version is null or b.store_owed_at is not null))
	`, artifactID, room).Scan(&owed, &latest, &removed.IndexOwed); err != nil {
		return nil, fmt.Errorf("read the pending authors, the latest version and the room's owed index of %s: %w", artifactID, err)
	}
	head, err := NewPgVersioned(nil).head(ctx, tx, room)
	if err != nil {
		return nil, err
	}
	if head > 0 {
		if removed.doc, err = documentThrough(room, head, storedUpdates(ctx, tx, room, head), maxUpdateItems); err != nil {
			return nil, fmt.Errorf("load room %s of document %s: %w", room, artifactID, err)
		}
	}
	markdown := ""
	if removed.doc != nil {
		tree, _, err := treeOfFragments(removed.doc, []string{removed.fragment})
		switch {
		case errors.Is(err, ErrDocOutsideSchema):
			// A fragment outside the schema is no version the document holds.
			removed.Unsettled = true
			return removed, nil
		case err != nil:
			return nil, fmt.Errorf("read the fragment of %s in room %s: %w", artifactID, room, err)
		case tree != nil:
			if markdown, _, err = renderFragment(tree); err != nil {
				return nil, fmt.Errorf("render the fragment of %s in room %s: %w", artifactID, room, err)
			}
		}
	}
	removed.Unsettled = owed || markdown != latest
	return removed, nil
}

// Leave takes the document's fragment out of the room's stored state, in tx, the removal's
// transaction, which holds the room's lock: one update deleting every element the fragment holds,
// appended to the room (AppendUpdateTx) as a change of no content of any document the room still
// holds. With the document's document_rooms row gone in the same transaction, the room's layout no
// longer names the fragment, so no read, render or settlement of the room reaches it, and its
// content is no longer in the room's state either. A fragment that holds nothing appends nothing.
func (f *RemovedFragment) Leave(ctx context.Context, tx pgx.Tx) error {
	if f.doc == nil {
		return nil
	}
	// The fragment and its length are read before the transaction, which holds the lock both
	// reads take.
	fragment := f.doc.GetXmlFragment(f.fragment)
	length := fragment.Len()
	if length == 0 {
		return nil
	}
	before := f.doc.StateVector()
	f.doc.Transact(func(txn *crdt.Transaction) {
		fragment.Delete(txn, 0, length)
	})
	if _, err := NewPgVersioned(nil).AppendUpdateTx(ctx, tx, f.Room, crdt.EncodeStateAsUpdateV1(f.doc, before), false); err != nil {
		return fmt.Errorf("take the removed document's fragment out of room %s: %w", f.Room, err)
	}
	return nil
}
