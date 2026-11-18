// comment_delete.go is DELETE /api/v1/comments/{id}: the author withdraws its own comment. The
// row and its place in the thread stay; the body goes, and with it every copy a read serves as
// the comment's own text that does not follow the cleared column — the block index, each
// block's stored excerpt, the text each revision recorded of a block, the document margin mark,
// and the name of the document version an accepted suggestion made — each rewritten in the one
// transaction that clears it. The event log's payloads stay as written and are masked at each
// read (attachPostMarks), and a reply's own events keep the preview they froze. A pending
// suggestion's proof is in the document itself, so the same transaction rejects it before the
// clear.
package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/docs"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// deleteComment answers DELETE /api/v1/comments/{id}. Its prologue is the comment twin of the
// message delete's (lockedCommentForCorrection): the owner locked without the closed refusal,
// then the author and a post already deleted.
func (s *server) deleteComment(w http.ResponseWriter, r *http.Request) {
	if !requireUUIDPath(w, r, "comment") {
		return
	}
	var input actorInput
	if !s.decodeOptionalJSON(w, r, &input) {
		return
	}
	actor, ok := s.correctionActor(w, r, input.Actor)
	if !ok {
		return
	}
	tx, err := s.begin(r.Context())
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	documentCtx, ledger := s.deps.Docs.Join(r.Context(), tx)
	defer ledger.Discard()
	comment, ok := s.lockedCommentForCorrection(w, r, tx, actor, "delete")
	if !ok {
		return
	}
	// The owner lock above decided this comment is deleted, a closed issue's included, so its
	// anchor's document writes are comment marks (docs.WithCommentMarks): the margin projection
	// below, which a closed issue takes as it takes the delete. A comment carrying a pending
	// suggestion takes the context here, where the create withholds it from one: the reject
	// below settles that mark and the clear takes the suggestion off the row, so the record the
	// projection writes names no pending suggestion, and a new one is what a closed issue
	// refuses.
	documentCtx = docs.WithCommentMarks(documentCtx)
	artifactName, err := s.commentArtifactName(r.Context(), tx, comment)
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	// A pending suggestion's mark is proof in the document that the suggestion stands, and an
	// insert suggestion's text is in the document's own body (docs/reject.go, rejectedInsert).
	// The margin projection below rewrites the mark record and removes neither, and the clear
	// leaves an accept and a reject with nothing to move (409 POST_DELETED), so the delete
	// rejects the suggestion here, as the reject arm of commentAction does. It rejects it as the
	// comment's own anchor writes are made (docs.RejectDeletedCommentSuggestion), under the
	// owner lock this transaction holds: a closed issue still takes the delete of its comment,
	// so its document takes this reject too, where the accept and the reject routes are refused.
	// An orphaned mark is already out of the document: the delete records that on the anchor and
	// stands, where an accept and a reject answer 409.
	//
	// Everything up to Commit is the read phase (Docs.Join): this route and the document
	// operations queue their writes on the ledger, in the order made, and Commit sends them as
	// one batch.
	if comment.Suggestion != nil && comment.Suggestion.Accepted == nil && comment.Anchor != nil {
		if err := s.deps.Docs.RejectDeletedCommentSuggestion(documentCtx, comment.Anchor.ArtifactID, comment.Anchor.MarkID, actor); err != nil {
			if !errors.Is(err, docs.ErrAnchorOrphaned) {
				s.writeHandlerError(w, err)
				return
			}
			if err := recordOrphanedCommentAnchor(ledger, &comment); err != nil {
				s.writeHandlerError(w, err)
				return
			}
		}
	}
	// An accepted suggestion's proof is a version of the document, which the accept named
	// after the comment's body (acceptedVersionName); every read of the document's versions
	// serves that name, so the delete renames it here, beside the reject above, while the
	// comment still holds the anchor and the suggestion the clear below takes off it.
	if comment.Suggestion != nil && comment.Suggestion.Accepted != nil && *comment.Suggestion.Accepted && comment.Anchor != nil {
		if err := renameAcceptedVersion(r.Context(), tx, ledger, comment); err != nil {
			s.writeHandlerError(w, err)
			return
		}
	}
	// deleted_at is the transaction's now(), which the queued update writes, read here so the
	// projection, the event and the answer carry it before that update runs.
	deletedAt, err := ledger.Now(r.Context())
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	deletedAt = deletedAt.UTC()
	referenceChanges, clear, err := s.clearDeletedPostBody(
		ledger.ReadContext(r.Context()), tx, refs.ItemComment, comment.ID, comment.Body, actor, deletedAt)
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	if err := ledger.Queue(clear); err != nil {
		s.writeHandlerError(w, err)
		return
	}
	comment.Body = ""
	comment.Suggestion = nil
	comment.DeletedAt = &deletedAt
	comment.DeletedBy = &actor
	if err := s.projectThreadMark(documentCtx, tx, ledger, comment, actor); err != nil {
		s.writeHandlerError(w, err)
		return
	}
	// The answer is read here, in the read phase, with the attempts as they stand: the settle
	// below is queued, so the attempts it settles are set in the answer as it leaves them, and
	// each one's receipt is built from that.
	answer, err := s.hydrateWrittenComment(r.Context(), tx, comment)
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	var receipts []model.Event
	for index := range answer.Deliveries {
		attempt := &answer.Deliveries[index]
		if attempt.State != "pending" {
			continue
		}
		settleDeletedPostAttempt(&attempt.State, &attempt.Error)
		receipts = append(receipts, commentDeliveryReceipt(comment, comment.Author, *attempt))
	}
	if err := ledger.Queue(func(b *store.WriteBatch) error {
		queueDeletedPostSettlement(b, "comment_deliveries", "comment_id", comment.ID, len(receipts))
		return nil
	}); err != nil {
		s.writeHandlerError(w, err)
		return
	}
	events := make([]*model.Event, 0, len(receipts)+1)
	for _, receipt := range receipts {
		appended, err := s.queueEvent(ledger, receipt)
		if err != nil {
			s.writeHandlerError(w, err)
			return
		}
		events = append(events, appended)
	}
	// A copy of the comment the outbox held back for a mention it waited on is settled with
	// nothing published: the text that copy carries is withdrawn, and the comment.deleted event
	// below is what each subscriber is told instead (comment_fallback_copies, migration
	// 20261101180039).
	if err := queueExec(ledger, `
		update comment_fallback_copies set settled_at = now(), published = false
		where comment_id = $1 and settled_at is null
	`, comment.ID); err != nil {
		s.writeHandlerError(w, err)
		return
	}
	payload, logins, err := s.commentCorrectionPayload(r.Context(), tx, &comment, artifactName, referenceChanges)
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	event, err := s.queueLoggedEvent(ledger, ownerOf(comment.IssueKey, comment.ArtifactID).event(
		"comment.deleted",
		actor,
		payload,
	), logins, "comment", comment.ID)
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	events = append(events, event)
	if err := ledger.Commit(r.Context()); err != nil {
		s.writeHandlerError(w, err)
		return
	}
	s.publish(r.Context(), appendedEvents(events...)...)
	WriteJSON(w, http.StatusOK, answer)
}

// renameAcceptedVersion renames the document version comment's accepted suggestion made to the
// name an accept gives a suggestion with no body (acceptedVersionName), in the delete's own
// transaction, so no read of the document's versions serves any part of the deleted body as a
// version's name, and marks it renamed by this comment's delete in the same update
// (artifact_versions.renamed_by_delete, which answerEndedByDelete reads). The artifact.version
// event the accept appended keeps the name it carried: no delete rewrites a stored event, and
// only the deleted comment's own events are read without its body (attachPostMarks). The version
// is found in the delete's read phase, under the comment's owner lock, and its update is queued
// on ledger.
//
// The accept records the version it made by the comment's id (artifact_versions.accepted_from,
// queued in commentAction's transaction right after Docs.NamedVersion), so the version is the one
// of the anchor's document that link names, whatever other version carries its time. A suggestion
// accepted before the accept recorded that link has no version naming it, and only for such a
// comment is the version found by the time the accept wrote it. The accept writes the version
// (Docs.NamedVersion), the version's artifact.version event and the comment's suggestion.accepted
// event in one transaction, and the version's created_at and each event's are that transaction's
// now(), the time it began; nothing updates either afterwards. (The comment's own resolved_at is
// no such mark: a later resolve moves it and a reopen clears it.) The version is therefore the
// named version of the anchor's document whose created_at is the time of
// the comment's suggestion.accepted event, read within the comment's owner, as
// loadCommentCreatedEvent reads its comment.created event. Another named version of that document
// carries the same time only when a transaction that began in the same microsecond wrote it, and
// the accept's own transaction writes no other: a lookup that finds more than one version renames
// none, and one that finds none - an accept whose event or version is not where this reads them -
// renames none either, each logged with the comment and the document.
func renameAcceptedVersion(ctx context.Context, tx pgx.Tx, ledger *docs.Ledger, comment model.Comment) error {
	artifactID := comment.Anchor.ArtifactID
	rows, err := tx.Query(ctx, `
		select number from artifact_versions where artifact_id = $1 and accepted_from = $2
	`, artifactID, comment.ID)
	if err != nil {
		return err
	}
	numbers, err := pgx.CollectRows(rows, pgx.RowTo[int])
	if err != nil {
		return err
	}
	if len(numbers) == 0 {
		ownerColumn, ownerID := commentEventOwner(comment)
		rows, err = tx.Query(ctx, `
			select v.number from artifact_versions v
			where v.artifact_id = $3 and v.named and v.created_at in (
				select e.created_at from events e
				where e.`+ownerColumn+` = $1 and e.type = 'suggestion.accepted' and e.payload ->> 'id' = $2
			)
		`, ownerID, comment.ID, artifactID)
		if err != nil {
			return err
		}
		if numbers, err = pgx.CollectRows(rows, pgx.RowTo[int]); err != nil {
			return err
		}
	}
	if len(numbers) != 1 {
		slog.Warn("dispatch: a deleted comment's accepted suggestion names no single document version, so no version is renamed",
			"comment", comment.ID, "artifact", artifactID, "versions", numbers)
		return nil
	}
	return queueExec(ledger, `
		update artifact_versions set summary = $3, renamed_by_delete = $4 where artifact_id = $1 and number = $2
	`, artifactID, numbers[0], acceptedVersionName("", comment.Anchor.Quote, comment.Suggestion.ReplaceWith), comment.ID)
}
