// post_history.go is what a write of a comment's or an issue message's body does to the body it
// replaces: the one write that keeps it (postBodyHistoryStep), the clear both deletes make of it
// and of the sends still carrying it (clearDeletedPostBody, queueDeletedPostSettlement), and the
// two reads that serve the kept bodies, GET /api/v1/comments/{id}/history and GET
// /api/v1/messages/{id}/history. One table holds the kept bodies of both kinds, keyed by the one
// of comment_id and message_id its row carries (post_body_history, migration 20261110224508).
//
// Every write here is planned in the route's read phase and returned as a step of its write
// phase (docs.WriteStep), which the route queues where it makes the write: on its ledger for a
// comment, whose route joins a document ledger (Docs.Join), and on the one batch of a message's,
// which runs its write phase itself as the message create does (createStoredMessage).
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/dispatch/docs"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/text"
)

// postHistoryEntry is one body a write replaced, with the actor that replaced it and the time
// of that write. Body is empty on a deleted post, whose read gives no part of the body
// anywhere, this one included.
type postHistoryEntry struct {
	Body     string      `json:"body"`
	EditedBy model.Actor `json:"edited_by"`
	EditedAt time.Time   `json:"edited_at"`
}

// postHistoryRead is what the two history reads answer: the post's earlier bodies, newest
// first, and the deleted mark the post carries, as every other read of it carries it. Entries
// holds at most contracts.PostHistoryPageLimit rows; the page after is the same read under
// ?before= the oldest edited_at this one gave.
type postHistoryRead struct {
	DeletedAt *time.Time         `json:"deleted_at"`
	DeletedBy *model.Actor       `json:"deleted_by"`
	Entries   []postHistoryEntry `json:"entries"`
}

// postHistoryOwner is where a post of this kind is kept: the table the post itself is in and
// the post_body_history column naming it, in the order refs gives them, which owns the map of
// an item kind to its storage (refs.ItemKind.ItemTable): post_body_history keys a post by the
// same column every table of that post's blocks keys it by. Only a comment and a message keep a
// history, so an ask, which is an item kind of its own, is refused here rather than there. kind
// is the handler's own constant and never caller input, so a kind with no history is this
// server's own fault rather than a refusal a caller can provoke.
func postHistoryOwner(kind refs.ItemKind) (items, column string, err error) {
	if kind != refs.ItemComment && kind != refs.ItemMessage {
		return "", "", fmt.Errorf("read the body history of a %q: only a comment and a message keep one", string(kind))
	}
	return kind.ItemTable()
}

// postBodyHistoryStep is the write that keeps the body a write of comment or message itemID is
// about to replace, under the owner lock the caller already holds: one post_body_history row
// with the actor making the write, as a step of the caller's write phase. An edit makes it with
// the body it replaces and a delete with the body it clears; a create makes it never, so the
// history of a post nobody edited or deleted is empty. The row's edited_at is its column's
// clock_timestamp(), which the statement takes when the write phase runs it.
func postBodyHistoryStep(kind refs.ItemKind, itemID, body string, actor model.Actor) (docs.WriteStep, error) {
	_, column, err := postHistoryOwner(kind)
	if err != nil {
		return nil, err
	}
	editedBy, err := encodeJSON(actor)
	if err != nil {
		return nil, err
	}
	return func(b *store.WriteBatch) error {
		b.Queue(fmt.Sprintf("keep the replaced body of %s %s", kind, itemID), `
			insert into post_body_history (`+column+`, body, edited_by) values ($1, $2, $3)
		`, itemID, body, editedBy)
		return nil
	}, nil
}

// queuePostBodyHistory queues postBodyHistoryStep on ledger, for a route whose write phase is its
// ledger's (editComment).
func queuePostBodyHistory(ledger *docs.Ledger, kind refs.ItemKind, itemID, body string, actor model.Actor) error {
	step, err := postBodyHistoryStep(kind, itemID, body, actor)
	if err != nil {
		return err
	}
	return ledger.Queue(step)
}

// deletedPostDeliveryError is what a send of a deleted post still pending is settled with: the
// text that would have been sent is gone, so nothing can send it. It is written in the shape
// settleUndeliverableMention writes its two causes in (delivery_sweep.go).
const deletedPostDeliveryError = "not sent: the post was deleted"

// clearDeletedPostBody plans the clear a delete of either kind makes, under the owner lock the
// caller already holds, and returns it as one step of the caller's write phase with the
// reference changes the delete's event carries. The step writes, in order: the history row
// keeping the body about to go (postBodyHistoryStep); the one update that empties the body and
// stamps the delete on the row at deletedAt, the transaction's now() the caller read in its read
// phase; the index of the empty body that update leaves, planned here (planItemWrite), which
// takes every block out of the post's current revision and every reference out of its edges; and
// the copies of the body that do not follow the cleared column - each block's stored excerpt,
// the text each revision recorded of a block and the post's embedding windows - taken out of
// them (refs.TombstoneItem).
//
// A comment clears its suggestion with its body, since the suggestion's replacement text is the
// comment's text too, and a message carries no suggestion: the kind decides which, and the clear
// rides the one update, so the kind's search trigger rewrites the row's vector once from the
// cleared body and the deleted post stops matching a keyword search. The post's embedding
// windows go in this transaction too (refs.TombstoneItem): the cleared body splits into no
// block, so the write enqueues no window and deletes each window the post held, and no meaning
// search finds the post once the delete commits. No path of this build leaves a window of a
// deleted post behind; the mark is the backstop for a hit that reaches one all the same, which
// search stamps with the post's delete mark (stampSearchPostMarks, search.go).
//
// ctx is what the plan's reads run under: the context a comment's route derives from the ledger
// it queues on (docs.Ledger.ReadContext), as queueEditedItemReferences derives it, and the
// request's for a message, whose route queues on no ledger. Only an issue message is deleted, so
// both kinds cite what they mention and the index reconciles the post's own references.
func (s *server) clearDeletedPostBody(
	ctx context.Context,
	tx pgx.Tx,
	kind refs.ItemKind,
	itemID, previousBody string,
	actor model.Actor,
	deletedAt time.Time,
) (model.ReferenceChanges, docs.WriteStep, error) {
	items, _, err := postHistoryOwner(kind)
	if err != nil {
		return model.ReferenceChanges{}, nil, err
	}
	alsoClears := ""
	if kind == refs.ItemComment {
		alsoClears = ", suggestion = null"
	}
	history, err := postBodyHistoryStep(kind, itemID, previousBody, actor)
	if err != nil {
		return model.ReferenceChanges{}, nil, err
	}
	deletedBy, err := encodeJSON(actor)
	if err != nil {
		return model.ReferenceChanges{}, nil, err
	}
	// The update that clears the body is queued, so the index is planned from the empty body the
	// write leaves rather than read back, as an edit's is (queueEditedItemReferences).
	changes, index, err := s.planItemWrite(ctx, tx, kind, itemID, "", nil, deletedAt, true, nil,
		func(found []text.Ref) []refs.Target { return refs.EditedTargets(previousBody, "", found) })
	if err != nil {
		return model.ReferenceChanges{}, nil, err
	}
	return changes, func(b *store.WriteBatch) error {
		if err := history(b); err != nil {
			return err
		}
		b.Queue("", `
			update `+items+`
			set body = ''`+alsoClears+`, deleted_at = $3, deleted_by = $2
			where id = $1
		`, itemID, deletedBy, deletedAt)
		if err := index(b); err != nil {
			return err
		}
		return refs.TombstoneItem(b, kind, itemID)
	}, nil
}

// settleDeletedPostAttempt sets one send attempt of a deleted post still pending as the delete's
// settle leaves it (queueDeletedPostSettlement): failed, with deletedPostDeliveryError. state and
// failure are the attempt's own fields, a comment's and a message's alike.
func settleDeletedPostAttempt(state *string, failure **string) {
	settled := deletedPostDeliveryError
	*state, *failure = "failed", &settled
}

// queueDeletedPostSettlement queues the settle of every send of the deleted post still pending:
// failed with deletedPostDeliveryError. A delivery Dispatch already sent keeps what it carried;
// one still waiting would send the text after the delete, so it is settled here rather than left
// to the delivery sweep. claimed_at moves with the settle, as every other settle of an attempt
// moves it: it is how a sender still holding the old claim learns the row is no longer its own.
//
// The caller read the post's attempts in its read phase, under the post's owner lock, and builds
// the receipt each settle owes from the pending ones (settleDeletedPostAttempt), queued after this
// statement, since no statement's result is read in Go before the next is sent. pending is how
// many those were. Every settle of an attempt takes that owner's lock first
// (settleDeliveryAttempt, settleUndeliverableMention), so the statement finds them as the read
// did; were it to settle another count, its callback refuses, Commit returns that refusal and the
// receipts never land beside rows they do not describe.
//
// attempts is the table of the post's send attempts and owner the column naming the post in it,
// both the caller's own constants.
func queueDeletedPostSettlement(b *store.WriteBatch, attempts, owner, itemID string, pending int) {
	b.Queue("", `
		update `+attempts+` set state = 'failed', error = $2, claimed_at = now()
		where `+owner+` = $1 and state = 'pending'
	`, itemID, deletedPostDeliveryError).Exec(func(tag pgconn.CommandTag) error {
		if settled := tag.RowsAffected(); settled != int64(pending) {
			return fmt.Errorf("settle the pending sends of %s %s: the settle met %d pending attempts, the read under its owner lock %d",
				attempts, itemID, settled, pending)
		}
		return nil
	})
}

// getCommentHistory answers GET /api/v1/comments/{id}/history, read by whoever GET
// /api/v1/comments/{id} answers: any authenticated caller.
func (s *server) getCommentHistory(w http.ResponseWriter, r *http.Request) {
	if !s.requireAuthenticated(w, r) || !requireUUIDPath(w, r, "comment") {
		return
	}
	s.writePostHistory(w, r, refs.ItemComment)
}

// getMessageHistory answers GET /api/v1/messages/{id}/history under the read rule of GET
// /api/v1/messages/{id} (readableMessageThread), as the message's blocks read is. Only an issue
// message is edited or deleted, and that rule asks for a session on a thread with no issue
// alone, so it costs this read nothing.
func (s *server) getMessageHistory(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.readableMessageThread(w, r); !ok {
		return
	}
	s.writePostHistory(w, r, refs.ItemMessage)
}

func (s *server) writePostHistory(w http.ResponseWriter, r *http.Request, kind refs.ItemKind) {
	before, err := parseOptionalRFC3339(r.URL.Query(), "before", time.Time{})
	if err != nil {
		writeError(w, "INVALID_QUERY", http.StatusBadRequest, err.Error())
		return
	}
	read, err := s.postHistory(r.Context(), kind, r.PathValue("id"), before)
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, read)
}

// postHistory is one post's kept bodies, newest first, at most contracts.PostHistoryPageLimit of
// them and only those written before `before` when it is given. The order is the partial index's
// (post_body_history_comment, post_body_history_message), so the read sorts nothing, and
// edited_at is clock_timestamp(), so two writes of one transaction order by it with the
// identity breaking a tie within one reading.
//
// The page is contracts.PostHistoryPageLimit and not maxEventPage, though both are 200 today.
// maxEventPage bounds a page of an issue's or a document's event log; this page is a different
// read, and it is also the bound on which `dispatch read --history` decides a page is full and
// names the read of its older rows. Under one name a change to the event page would move this
// page with no client told, so this one is declared in packages/contracts, which both ends read.
//
// The post's deleted mark and its kept bodies are one statement, so both are read in one
// snapshot: as two statements a delete that committed between them served the bodies the
// first read found with no mark on them, which is the one answer this read must never give.
// The post itself drives the join, so a post that is not there answers no row at all, as the
// single read of it did (pgx.ErrNoRows, 404); the lateral is its page of kept bodies, read
// through the same partial index under the same order and limit, and a post that kept none
// answers one row with every history column null.
func (s *server) postHistory(ctx context.Context, kind refs.ItemKind, id string, before time.Time) (postHistoryRead, error) {
	items, column, err := postHistoryOwner(kind)
	if err != nil {
		return postHistoryRead{}, err
	}
	var cursor *time.Time
	if !before.IsZero() {
		cursor = &before
	}
	// A delete gives no part of the body on any read of the post, so the mark stands in place of
	// every body this list would otherwise serve and the read answers none of them.
	rows, err := s.deps.Store.Pool.Query(ctx, `
		select p.deleted_at, p.deleted_by,
		       case when p.deleted_at is null then h.body else '' end, h.edited_by, h.edited_at
		from `+items+` p
		left join lateral (
			select body, edited_by, edited_at
			from post_body_history
			where `+column+` = p.id and ($2::timestamptz is null or edited_at < $2)
			order by edited_at desc, id desc
			limit $3
		) h on true
		where p.id = $1
	`, id, cursor, contracts.PostHistoryPageLimit)
	if err != nil {
		return postHistoryRead{}, fmt.Errorf("read the body history of %s %s: %w", kind, id, err)
	}
	defer rows.Close()
	read := postHistoryRead{Entries: []postHistoryEntry{}}
	found := false
	for rows.Next() {
		var deletedAt, editedAt *time.Time
		var deletedBy, editedBy []byte
		var body *string
		if err := rows.Scan(&deletedAt, &deletedBy, &body, &editedBy, &editedAt); err != nil {
			return postHistoryRead{}, fmt.Errorf("scan a kept body of %s %s: %w", kind, id, err)
		}
		if !found {
			found = true
			if read.DeletedAt, read.DeletedBy, err = model.DeletedPostMark(deletedAt, deletedBy); err != nil {
				return postHistoryRead{}, err
			}
		}
		// The post kept no body this page holds: the left join's one row carries the mark alone.
		if editedAt == nil {
			continue
		}
		entry := postHistoryEntry{Body: *body, EditedAt: editedAt.UTC()}
		if err := json.Unmarshal(editedBy, &entry.EditedBy); err != nil {
			return postHistoryRead{}, fmt.Errorf("decode the actor of a kept body of %s %s: %w", kind, id, err)
		}
		read.Entries = append(read.Entries, entry)
	}
	if err := rows.Err(); err != nil {
		return postHistoryRead{}, fmt.Errorf("read the body history of %s %s: %w", kind, id, err)
	}
	if !found {
		return postHistoryRead{}, pgx.ErrNoRows
	}
	return read, nil
}
