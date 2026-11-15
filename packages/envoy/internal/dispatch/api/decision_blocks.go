package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/docs"
	"github.com/sjawhar/envoy/internal/dispatch/events"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
	"github.com/sjawhar/envoy/internal/dispatch/reviewer"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/texttrees"
)

// decision_blocks.go is the reviewer of a decision block an agent writes in a document
// (INFRA-3231's R5 and Decisions 3 and 23 of its plan). A decision block shows in its document
// when the document is saved, so its ask row exists from the settlement that opens it; the
// reviewer cannot hold it from the document, only from the people who would be asked to act on it.
// By DISPATCH_REVIEWER_DECISION_BLOCKS:
//
//   - hold: in the settlement's own transaction the ask gets held_until, which every Inbox read
//     and every open-ask count reads (askNotHeld), and a held_texts row of kind decision_block
//     under the ask's id carries its review, the writer's frame and window, and the sweep, as for
//     any held text. Its post ends the hold: the ask's held_until goes back to null and a quiet
//     ask.reviewed event tells an open page to refetch its Inbox and counts; where the outcome is
//     rewrite_posted, the reviewer's rewrite is applied first (rewriteDecisionBlock).
//   - after: the block is reviewed after its settlement commits, with the findings sent to the
//     writer and nothing held.
//
// A block a person typed in a session (doc_ask_block_authors.person_typed) is never held nor
// reviewed, and is recorded exempt_person_typed. An open block whose wording a later write moves is
// reviewed after that write with no hold, unless its hold has not ended, when the move is its
// writer's edit of the held text and is reviewed again before the block reaches an Inbox.

// decisionBlockRoute is the route a decision block's records name: the settlement that opened it,
// which no request of its own makes.
const decisionBlockRoute = "SETTLE decision block"

// askNotHeld is the condition every read of an Inbox or an open-ask count adds for the ask aliased
// a: no reviewer's hold of a decision block keeps it out (asks.held_until, migration
// 20261115104117).
const askNotHeld = `(a.held_until is null or a.held_until <= now())`

// decisionBlockHold is a held decision block's request: the ask whose block is held.
type decisionBlockHold struct {
	DecisionBlock bool   `json:"decision_block"`
	AskID         string `json:"ask_id"`
}

// decisionBlockReviewer is the server as the document service's AskBlockReviewer.
type decisionBlockReviewer struct{ s *server }

var _ docs.AskBlockReviewer = decisionBlockReviewer{}

// heldDecisionBlockBound is how long a hold keeps a decision block out of people's Inboxes when
// nothing ends it: the review's bound and the writer's window, with a minute more.
func (rv *Reviewer) heldDecisionBlockBound() time.Duration {
	return rv.config.Timeout + rv.config.WriterWindow + time.Minute
}

// decisionBlockReview is the reader and the project of the decision block ask, read in q, and
// whether the reviewer reads it at all: an agent session's block (the Legion daemon's left out)
// that a person waits on.
func (s *server) decisionBlockReview(ctx context.Context, q queryer, ask model.Ask, author model.Actor) (reader, project string, reads bool, err error) {
	if checked, _ := writingCheckReads(author); !checked {
		return "", "", false, nil
	}
	if reader, err = askWaitingOnPerson(ctx, q, ask.ID); err != nil {
		return "", "", false, err
	}
	if err := q.QueryRow(ctx, `
		select coalesce(i.project_key, ar.project_key, '')
		from asks a left join issues i on i.key = a.issue_key left join artifacts ar on ar.id = coalesce(a.artifact_id, a.block_artifact_id)
		where a.id = $1`, ask.ID).Scan(&project); err != nil {
		return "", "", false, fmt.Errorf("read the project of decision block %s: %w", ask.ID, err)
	}
	return reader, project, reader != "", nil
}

// decisionBlockEvent is the writing_check_events row of a decision block author wrote.
func decisionBlockEvent(author model.Actor, reader, project string) store.WritingCheckEvent {
	method, route, _ := strings.Cut(decisionBlockRoute, " ")
	event := store.WritingCheckEvent{
		Route: route, Method: method, ActorKind: author.Kind, ActorID: author.ID, Reader: reader, Project: project,
	}
	if author.Act != nil {
		event.ActSession, event.TokenID = author.Act.Session, author.Act.TokenID
	}
	return event
}

// AskBlockOpened holds or reviews the new decision block ask, by the setting, in the settlement's
// transaction tx. A block a person typed is recorded exempt_person_typed and opens as it is.
func (d decisionBlockReviewer) AskBlockOpened(ctx context.Context, tx pgx.Tx, ask model.Ask, author model.Actor, personTyped bool) (func(), error) {
	s := d.s
	rv := s.deps.Reviewer
	if personTyped {
		if checked, _ := writingCheckReads(author); !checked {
			return nil, nil
		}
		reader, project, _, err := s.decisionBlockReview(ctx, tx, ask, author)
		if err != nil {
			return nil, err
		}
		event := decisionBlockEvent(author, reader, project)
		event.Outcome = writingCheckPersonTyped
		return func() { s.recordWritingCheck(s.postContext(), event) }, nil
	}
	setting := rv.setting(reviewer.KindDecisionBlock)
	if setting == ReviewOff || s.deps.Store == nil {
		return nil, nil
	}
	reader, project, reads, err := s.decisionBlockReview(ctx, tx, ask, author)
	if err != nil || !reads {
		return nil, err
	}
	event := decisionBlockEvent(author, reader, project)
	if setting == ReviewAfter {
		kind, text, subject := askReviewSubject(ask)
		text.Kind = kind
		subject.Text, subject.Writer, subject.Reader = text, writerOf(author), reader
		review := &postedReview{kind: kind, text: text, subject: subject, event: event}
		return func() { s.reviewPosted(s.postContext(), review, ask.ID) }, nil
	}
	request, err := json.Marshal(decisionBlockHold{DecisionBlock: true, AskID: ask.ID})
	if err != nil {
		return nil, err
	}
	_, text, _ := askReviewSubject(ask)
	text.Kind = reviewer.KindDecisionBlock
	original, err := json.Marshal(text)
	if err != nil {
		return nil, err
	}
	actor, err := json.Marshal(author)
	if err != nil {
		return nil, err
	}
	issueKey := ""
	if ask.IssueKey != nil {
		issueKey = *ask.IssueKey
	}
	if _, err := tx.Exec(ctx, `update asks set held_until = now() + $2::interval where id = $1`,
		ask.ID, intervalOf(rv.heldDecisionBlockBound())); err != nil {
		return nil, fmt.Errorf("hold decision block %s: %w", ask.ID, err)
	}
	if _, err := tx.Exec(ctx, `
		insert into held_texts (id, kind, route, request, original, actor, writer, reader, project, issue_key, state, reviewed)
		values ($1, 'decision_block', $2, $3, $4, $5, nullif($6, ''), $7, nullif($8, ''), nullif($9, ''), 'reviewing', true)`,
		ask.ID, decisionBlockRoute, request, original, actor, writerOf(author), reader, project, issueKey); err != nil {
		return nil, fmt.Errorf("hold decision block %s: %w", ask.ID, err)
	}
	event.Outcome = reviewHeld
	return func() {
		s.recordWritingCheck(s.postContext(), event)
		s.startReview(ask.ID)
	}, nil
}

// AskBlockReworded is a write that moved the open decision block ask's wording from previous. A
// write a person typed in a session is theirs: it is reviewed by nobody and recorded
// exempt_person_typed (personTypedBlockEdit). While the block's hold has not ended any other move
// is its writer's edit of the held text, reviewed again before the block reaches an Inbox
// (heldDecisionBlockEdit); else it is reviewed after the write, by the setting, with no hold.
// Dispatch's own rewrite (the reviewer's actor) starts no review.
func (d decisionBlockReviewer) AskBlockReworded(ctx context.Context, tx pgx.Tx, ask model.Ask, previous model.AskEditPrevious, editor model.Actor, personTyped bool) (func(), error) {
	s := d.s
	if editor == reviewerActor || s.deps.Store == nil {
		return nil, nil
	}
	if personTyped {
		return s.personTypedBlockEdit(ctx, tx, ask, editor)
	}
	if after, edited, err := s.heldDecisionBlockEdit(ctx, tx, ask.ID); err != nil || edited {
		return after, err
	}
	if s.deps.Reviewer.setting(reviewer.KindDecisionBlock) == ReviewOff {
		return nil, nil
	}
	reader, project, reads, err := s.decisionBlockReview(ctx, tx, ask, editor)
	if err != nil || !reads {
		return nil, err
	}
	kind, text, subject := askReviewSubject(ask)
	text.Kind = kind
	subject.Text, subject.Writer, subject.Reader = text, writerOf(editor), reader
	review := &postedReview{kind: kind, text: text, subject: subject, event: decisionBlockEvent(editor, reader, project)}
	return func() { s.reviewPosted(s.postContext(), review, ask.ID) }, nil
}

// personTypedBlockEdit is a person's typed rewording of the decision block ask (Decision 26 of
// INFRA-3231's plan), in the settlement's transaction tx: no review reads it, it is recorded
// exempt_person_typed, and a hold of the block that has not ended ends now
// (endHeldDecisionBlockForPerson).
func (s *server) personTypedBlockEdit(ctx context.Context, tx pgx.Tx, ask model.Ask, editor model.Actor) (func(), error) {
	var record *store.WritingCheckEvent
	if checked, _ := writingCheckReads(editor); checked {
		reader, project, _, err := s.decisionBlockReview(ctx, tx, ask, editor)
		if err != nil {
			return nil, err
		}
		event := decisionBlockEvent(editor, reader, project)
		event.Outcome = writingCheckPersonTyped
		record = &event
	}
	ended, err := s.endHeldDecisionBlockForPerson(ctx, tx, ask.ID)
	if err != nil {
		return nil, err
	}
	return func() {
		if record != nil {
			s.recordWritingCheck(s.postContext(), *record)
		}
		ended()
	}, nil
}

// endHeldDecisionBlockForPerson ends, in q, the hold of the decision block id that has not ended,
// with the block as a person's typed edit left it (posted_as_written), so no reviewer's rewrite is
// written over their words. What it returns records that and posts the block once q commits; it
// does nothing for a block nobody holds.
func (s *server) endHeldDecisionBlockForPerson(ctx context.Context, q queryer, id string) (func(), error) {
	const detail = "a person typed an edit of the block in a session"
	held, err := scanHeld(q.QueryRow(ctx, `
		update held_texts
		set state = 'posting', outcome = 'posted_as_written', detail = $2, deadline = null, claimed_at = null,
			updated_at = now()
		where id = $1 and kind = 'decision_block' and state in ('reviewing', 'awaiting_writer')
		returning `+heldColumns, id, detail))
	if errors.Is(err, pgx.ErrNoRows) {
		return func() {}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("end the hold of decision block %s for a person's edit: %w", id, err)
	}
	return func() {
		s.recordHeld(s.postContext(), held, reviewPostedAsWritten, nil, detail)
		s.detached.goDetached(s.postReady)
	}, nil
}

// heldDecisionBlockEdit takes, in q, a move of the held decision block id's wording as its
// writer's edit: the review starts again on the new wording, and the frame's window with it. It
// reports whether id is a decision block whose hold has not ended, with what to run once q
// commits.
func (s *server) heldDecisionBlockEdit(ctx context.Context, q queryer, id string) (func(), bool, error) {
	row, err := scanHeld(q.QueryRow(ctx, `
		update held_texts
		set revision = revision + 1, state = 'reviewing', findings = '[]', rewrite = null, not_applied = null,
			deadline = null, claimed_at = null, updated_at = now()
		where id = $1 and kind = 'decision_block' and state in ('reviewing', 'awaiting_writer')
		returning `+heldColumns, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("take the edit of held decision block %s: %w", id, err)
	}
	return func() {
		s.recordHeld(s.postContext(), row, reviewEdited, nil, "")
		s.startReview(id)
	}, true, nil
}

// editedHeldDecisionBlock is heldDecisionBlockEdit for an edit already committed (PATCH
// /api/v1/asks/{id} writes the block and the row in one transaction, so no settlement rewords it):
// it reports whether ask is a decision block whose hold has not ended, whose review it restarted.
func (s *server) editedHeldDecisionBlock(ctx context.Context, ask model.Ask) bool {
	if ask.BlockID == nil || s.deps.Store == nil {
		return false
	}
	after, edited, err := s.heldDecisionBlockEdit(ctx, s.deps.Store.Pool, ask.ID)
	if err != nil {
		slog.Warn("dispatch: take an edit of a held decision block", "ask", ask.ID, "error", err)
		return false
	}
	if edited {
		after()
	}
	return edited
}

// decisionBlockSubject is the held decision block row's subject for its review: the ask as it now
// stands (its writer may have reworded it since the hold), its writer and its reader.
func (s *server) decisionBlockSubject(ctx context.Context, row heldRow) (reviewer.Subject, error) {
	subject, err := reviewer.AskSubject(ctx, s.deps.Store, row.id)
	if err != nil {
		return reviewer.Subject{}, err
	}
	subject.Writer, subject.Reader = row.writer, row.reader
	return subject, nil
}

// applicableDecision is decision about the held row, except that a decision to post the reviewer's
// rewrite of a decision block that the block cannot hold is a decision to post the block as
// written, saying why: the rewrite would drop a picture or a link the block's question holds,
// which the release would refuse (rewriteDecisionBlock). Deciding it here keeps the held row's
// outcome and its writing_check_events row truthful, which R8's counts read. Where the block
// cannot be read the decision stands, and the release still refuses what the block cannot hold.
func (s *server) applicableDecision(ctx context.Context, row heldRow, decision heldDecision) heldDecision {
	if row.kind != string(reviewer.KindDecisionBlock) || decision.outcome != reviewRewritePosted || row.rewrite == nil {
		return decision
	}
	refusal, err := s.decisionBlockRewriteRefusal(ctx, row.id, *row.rewrite)
	if err != nil {
		slog.Warn("dispatch: weigh the reviewer's rewrite of a decision block", "ask", row.id, "error", err)
		return decision
	}
	if refusal == "" {
		return decision
	}
	detail := "the reviewer's rewrite is not applied: " + refusal
	if decision.detail != "" {
		detail = decision.detail + "; " + detail
	}
	return heldDecision{outcome: reviewPostedAsWritten, detail: detail, reason: decision.reason}
}

// decisionBlockRewriteRefusal is why the decision block ask id cannot hold rewrite, "" where it
// can: a picture or a link the block's question holds, as its document's newest version holds it
// (docs.AskBlockQuestionMarkdown, which the review's record read), that the rewrite's question
// leaves out. It is the release's own write guard (docs.RefuseDroppedInlines), worded as
// rewriteRefusal gives it.
func (s *server) decisionBlockRewriteRefusal(ctx context.Context, id string, rewrite reviewer.Text) (string, error) {
	var blockID, markdown string
	if err := s.deps.Store.Pool.QueryRow(ctx, `
		select a.block_id, coalesce((select v.markdown from artifact_versions v
			where v.artifact_id = a.block_artifact_id order by v.number desc limit 1), '')
		from asks a where a.id = $1 and a.block_id is not null`, id).Scan(&blockID, &markdown); err != nil {
		return "", fmt.Errorf("read decision block %s: %w", id, err)
	}
	question, err := docs.AskBlockQuestionMarkdown(markdown, blockID)
	if err != nil {
		return "", err
	}
	err = docs.RefuseDroppedInlines(question, rewrite.Body)
	if refusal := rewriteRefusal(err); refusal != "" {
		return refusal, nil
	}
	return "", err
}

// releaseDecisionBlock is a held decision block's post: in one transaction it applies the
// reviewer's rewrite where the outcome is rewrite_posted and the ask is still open
// (rewriteDecisionBlock), ends the hold, appends the quiet ask.reviewed an open page refetches its
// Inbox and counts on, and marks the held row posted.
func (s *server) releaseDecisionBlock(ctx context.Context, row heldRow, held *heldPost) error {
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	documentCtx, ledger := s.deps.Docs.Join(ctx, tx)
	defer ledger.Discard()
	if err := held.lock(ctx, tx); err != nil {
		return err
	}
	unlocked, err := s.loadAsk(ctx, tx, row.id)
	if err != nil {
		return err
	}
	owner := ownerOf(unlocked.IssueKey, unlocked.ArtifactID)
	_, open, err := ownerStatus(documentCtx, tx, owner, ownerRowLock)
	if err != nil {
		return err
	}
	ask, err := s.lockAskForTransition(documentCtx, tx, unlocked.ID)
	if err != nil {
		return err
	}
	var published []model.Event
	var previous *model.AskEditPrevious
	if row.outcome == reviewRewritePosted && row.rewrite != nil && open && ask.State == "open" && ask.BlockID != nil {
		before := model.NewAskEditPrevious(ask)
		record := model.AskRewriteEdit{By: model.RewriteByReviewer, AskedBy: reviewerActor, Reason: findingsReason(row.findings)}
		rewritten, event, err := s.rewriteDecisionBlock(documentCtx, tx, ask, *row.rewrite, record)
		switch reason := rewriteRefusal(err); {
		case reason != "":
			// The block cannot hold the reviewer's rewrite, which wrote nothing: the block posts
			// as its writer wrote it rather than staying held for good. The decision weighed the
			// rewrite against the block first (applicableDecision), so only a block changed
			// since the decision reaches here.
			slog.Warn("dispatch: the reviewer's rewrite of a decision block is not applied", "ask", ask.ID, "reason", reason)
		case err != nil:
			return err
		default:
			ask, previous = rewritten, &before
			published = append(published, event)
		}
	}
	if _, err := tx.Exec(ctx, `update asks set held_until = null where id = $1`, ask.ID); err != nil {
		return err
	}
	payload, err := events.AskEventPayload(ctx, tx, ask, model.ReferenceChanges{})
	if err != nil {
		return err
	}
	event, err := s.appendEvent(documentCtx, tx, owner.event("ask.reviewed", reviewerActor, payload))
	if err != nil {
		return err
	}
	published = append(published, event)
	if err := held.finish(ctx, tx); err != nil {
		return err
	}
	if err := ledger.Commit(ctx); err != nil {
		return err
	}
	s.publish(ctx, published...)
	if previous != nil {
		s.storeDisplayTrees(ctx, ask.ID, texttrees.AskWrittenTexts(
			texttrees.AskWrittenTexts(nil, ask.Question, ask.Options), previous.Question, previous.Options)...)
	}
	return nil
}

// findingsReason is the reviewer's findings as the reason its rewrite records: each finding's
// words and why they break their rule.
func findingsReason(findings []reviewer.Finding) string {
	reasons := make([]string, 0, len(findings))
	for _, finding := range findings {
		reasons = append(reasons, fmt.Sprintf("%q: %s", finding.Quote, finding.Why))
	}
	return strings.Join(reasons, "; ")
}

// rewriteDecisionBlock is the one path that applies a rewrite to a decision block (Decision 23 of
// INFRA-3231's plan): rewrite's wording is written through the edit route's own path
// (writeAskEdit) into the ask's `:::ask` block, which the document's next version holds, and into
// its row, and the quiet ask.edited it appends holds the original wording (previous), who asked
// for the rewrite and why (record). Dispatch itself writes it (reviewerActor), so the document's
// version and the event name Dispatch, and its own rewrite starts no review
// (AskBlockReworded); record.AskedBy names who asked. The reviewer's rewrite of a held decision
// block (releaseDecisionBlock) calls it, and a person's Rewrite of a decision block calls it with
// record.By model.RewriteByPerson (ask_rewrite.go). The rewrite read the block's question as the
// document holds it, in markdown (reviewer.BuildRecord), so its question is written back as
// markdown, its pictures, links and marks with it, while the row takes the plain text the block
// parses back to; a rewrite that would drop a picture or a link the block holds is refused, as
// *docs.ErrAskBlockUnrepresentable, which rewriteRefusal gives as the reason it was not applied.
// ctx is the document context of tx's ledger (docs.API.Join), and the caller has locked the ask's
// owner and its row; it publishes the event once tx commits.
func (s *server) rewriteDecisionBlock(ctx context.Context, tx pgx.Tx, ask model.Ask, rewrite reviewer.Text, record model.AskRewriteEdit) (model.Ask, model.Event, error) {
	if ask.BlockID == nil {
		return model.Ask{}, model.Event{}, fmt.Errorf("ask %q is no decision block", ask.ID)
	}
	wording := rewriteWording(rewrite)
	wording.QuestionMarkdown = true
	rewritten, _, event, err := s.writeAskEdit(ctx, tx, ask, wording, reviewerActor, &record)
	return rewritten, event, err
}

// rewriteWording is a rewrite's wording as an edit of an ask writes it: its question, with the
// line feeds a block's text reaches the parser with (pmdoc.LineFeeds), its choice, and, where it
// names them, its options' labels and descriptions with no id, since a stored ask's option ids
// are the index's to mint.
func rewriteWording(rewrite reviewer.Text) askWording {
	question, multiple := pmdoc.LineFeeds(rewrite.Body), rewrite.Multiple
	wording := askWording{Question: &question, Multiple: &multiple}
	if rewrite.Options != nil {
		options := make([]model.AskOption, len(rewrite.Options))
		for i, option := range rewrite.Options {
			options[i] = model.AskOption{Label: pmdoc.LineFeeds(option.Label), Description: pmdoc.LineFeeds(option.Description)}
		}
		wording.Options = &options
	}
	return wording
}
