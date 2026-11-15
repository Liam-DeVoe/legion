package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/events"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/reviewer"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// held_texts.go keeps the texts the reviewer holds (store migration 20261115084745_held_texts):
// the hold, which reserves the text's id and answers the write at once; the decisions the review,
// the writer and the sweep make; and the post, which runs the kind's own create path with the
// stored request, the recorded actor and the reserved id, in one transaction that also marks the
// held row posted. A held text that a later write names as its parent or as the text it answers
// has that write held beside it, posted right after it in the order the writes came.

// The states of a held text (held_texts.state).
const (
	heldReviewing      = "reviewing"
	heldAwaitingWriter = "awaiting_writer"
	heldPosting        = "posting"
	heldPosted         = "posted"
	heldFailed         = "failed"
)

// heldNotReviewed is the outcome of a held text the reviewer does not read before it posts: a
// write that waits on a held text with its own kind's setting not hold.
const heldNotReviewed = "not_reviewed"

// heldRequestKind is the kind of a write kept whole beside a held text it names, which is no text
// of its own (awaitHeldTarget).
const heldRequestKind = "request"

// heldRow is one row of held_texts.
type heldRow struct {
	id, kind, route                 string
	request                         json.RawMessage
	original                        reviewer.Text
	actor                           model.Actor
	writer, reader, project         string
	issueKey                        string
	state                           string
	revision                        int
	reviewed                        bool
	findings                        []reviewer.Finding
	rewrite                         *reviewer.Text
	notApplied                      string
	outcome, reason, detail, answer string
	deadline                        *time.Time
	dependsOn                       string
	writtenAt                       time.Time
	postedAt                        *time.Time
	// personTyped is a write a person typed in a session, held beside the text it waits on: its
	// post marks its events person_typed (Decision 26).
	personTyped bool
}

// heldColumns are the columns scanHeld reads, in its order.
const heldColumns = `id, kind, route, request, original, actor, coalesce(writer, ''), coalesce(reader, ''),
	coalesce(project, ''), coalesce(issue_key, ''), state, revision, reviewed, findings, rewrite,
	coalesce(not_applied, ''), coalesce(outcome, ''), coalesce(reason, ''), coalesce(detail, ''),
	coalesce(writer_answer, ''), deadline, coalesce(depends_on, ''), written_at, posted_at, person_typed`

func scanHeld(row pgx.Row) (heldRow, error) {
	var held heldRow
	var original, actor, findings, rewrite []byte
	if err := row.Scan(&held.id, &held.kind, &held.route, &held.request, &original, &actor, &held.writer, &held.reader,
		&held.project, &held.issueKey, &held.state, &held.revision, &held.reviewed, &findings, &rewrite,
		&held.notApplied, &held.outcome, &held.reason, &held.detail, &held.answer, &held.deadline, &held.dependsOn,
		&held.writtenAt, &held.postedAt, &held.personTyped); err != nil {
		return heldRow{}, err
	}
	if err := json.Unmarshal(original, &held.original); err != nil {
		return heldRow{}, fmt.Errorf("read held text %s's original: %w", held.id, err)
	}
	if err := json.Unmarshal(actor, &held.actor); err != nil {
		return heldRow{}, fmt.Errorf("read held text %s's actor: %w", held.id, err)
	}
	if err := json.Unmarshal(findings, &held.findings); err != nil {
		return heldRow{}, fmt.Errorf("read held text %s's findings: %w", held.id, err)
	}
	if len(rewrite) > 0 {
		held.rewrite = &reviewer.Text{}
		if err := json.Unmarshal(rewrite, held.rewrite); err != nil {
			return heldRow{}, fmt.Errorf("read held text %s's rewrite: %w", held.id, err)
		}
	}
	return held, nil
}

// loadHeld reads the held text id; a held text that does not exist is 404 HELD_NOT_FOUND.
func (s *server) loadHeld(ctx context.Context, q queryer, id string, lock bool) (heldRow, error) {
	query := `select ` + heldColumns + ` from held_texts where id = $1`
	if lock {
		query += ` for update`
	}
	row, err := scanHeld(q.QueryRow(ctx, query, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return heldRow{}, errorf(http.StatusNotFound, "HELD_NOT_FOUND", "no held text has the id %s", id)
	}
	return row, err
}

// loadHeldRows reads the held texts where says, on the pool.
func (s *server) loadHeldRows(ctx context.Context, where string, args ...any) ([]heldRow, error) {
	rows, err := s.deps.Store.Pool.Query(ctx, `select `+heldColumns+` from held_texts where `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var held []heldRow
	for rows.Next() {
		row, err := scanHeld(rows)
		if err != nil {
			return nil, err
		}
		held = append(held, row)
	}
	return held, rows.Err()
}

// write is the held text's request, decoded by its kind: the write as it posts now, the writer's
// edits applied. A write kept whole (heldReplay) is decoded as one, whatever its kind.
func (h heldRow) write() (reviewedWrite, error) {
	if isReplay(h.request) {
		var replay heldReplay
		if err := json.Unmarshal(h.request, &replay); err != nil {
			return reviewedWrite{}, err
		}
		return replay.reviewed(reviewer.Kind(h.kind)), nil
	}
	switch reviewer.Kind(h.kind) {
	case reviewer.KindDecisionBlock:
		var hold decisionBlockHold
		if err := json.Unmarshal(h.request, &hold); err != nil {
			return reviewedWrite{}, err
		}
		return reviewedWrite{
			kind: reviewer.KindDecisionBlock, request: hold, text: h.original,
			subject: reviewer.Subject{AskID: hold.AskID, IssueKey: h.issueKey}, holdable: true,
		}, nil
	case reviewer.KindAsk:
		var create askCreate
		if err := json.Unmarshal(h.request, &create); err != nil {
			return reviewedWrite{}, err
		}
		return create.reviewed(), nil
	case reviewer.KindIssue:
		var create issueCreate
		if err := json.Unmarshal(h.request, &create); err != nil {
			return reviewedWrite{}, err
		}
		return create.reviewed(), nil
	case reviewer.KindComment:
		var create commentCreate
		if err := json.Unmarshal(h.request, &create); err != nil {
			return reviewedWrite{}, err
		}
		return create.reviewed(), nil
	case reviewer.KindMessage:
		var create messageCreate
		if err := json.Unmarshal(h.request, &create); err != nil {
			return reviewedWrite{}, err
		}
		return create.reviewed(), nil
	}
	return reviewedWrite{}, fmt.Errorf("held text %s is of kind %q, which no create path writes", h.id, h.kind)
}

// reviewed is the ask as the reviewer reads it: its question, options and choice, on its issue
// or in its document, at the quote its anchor names.
func (c askCreate) reviewed() reviewedWrite {
	subject := reviewer.Subject{IssueKey: deref(c.Owner.IssueKey), DocumentID: deref(c.Owner.ArtifactID)}
	if c.Anchor != nil {
		if _, err := uuid.Parse(c.Anchor.Artifact); err == nil {
			subject.DocumentID = c.Anchor.Artifact
		}
		subject.Quote = deref(c.Anchor.Quote)
	}
	return reviewedWrite{
		kind: reviewer.KindAsk, request: c, subject: subject, names: []string{deref(c.Owner.IssueKey)}, holdable: true,
		text: reviewer.Text{Kind: reviewer.KindAsk, Body: c.Question, Options: c.Options, Multiple: c.Multiple},
	}
}

// reviewed is the comment as the reviewer reads it: its body, in the thread it answers, on its
// issue or its document.
func (c commentCreate) reviewed() reviewedWrite {
	subject := reviewer.Subject{
		IssueKey: deref(c.Owner.IssueKey), DocumentID: deref(c.Owner.ArtifactID),
		ReplyTo: deref(c.Input.ReplyTo), AskID: deref(c.Input.AskID),
	}
	if c.Input.Anchor != nil {
		if _, err := uuid.Parse(c.Input.Anchor.Artifact); err == nil {
			subject.DocumentID = c.Input.Anchor.Artifact
		}
		subject.Quote = deref(c.Input.Anchor.Quote)
	}
	return reviewedWrite{
		kind: reviewer.KindComment, request: c, subject: subject, holdable: true,
		names: []string{deref(c.Owner.IssueKey), deref(c.Input.ReplyTo), deref(c.Input.AskID)},
		text:  reviewer.Text{Kind: reviewer.KindComment, Body: c.Input.Body},
	}
}

// reviewed is the message as the reviewer reads it: its body, in the conversation it answers, on
// its issue.
func (c messageCreate) reviewed() reviewedWrite {
	return reviewedWrite{
		kind: reviewer.KindMessage, request: c, holdable: true,
		subject: reviewer.Subject{IssueKey: deref(c.IssueKey), InReplyTo: deref(c.Input.InReplyTo)},
		names:   []string{deref(c.IssueKey), deref(c.Input.InReplyTo)},
		text:    reviewer.Text{Kind: reviewer.KindMessage, Body: c.Input.Body},
	}
}

// reviewed is the new issue as the reviewer reads it: its title and its spec, under its parent.
// An issue imported from GitHub is not held: its title is GitHub's.
func (c issueCreate) reviewed() reviewedWrite {
	return reviewedWrite{
		kind: reviewer.KindIssue, request: c, project: c.Project, holdable: c.External == "",
		subject: reviewer.Subject{IssueKey: c.Parent}, names: []string{c.Parent},
		text: reviewer.Text{Kind: reviewer.KindIssue, Title: c.Title, Body: deref(c.Spec)},
	}
}

// heldParent is the held text a write names, which it waits on.
type heldParent struct {
	id, state, reader, project string
}

// heldParentOf is the held text, not yet posted, among the ids a write names; nil where it names
// none. Where it names more than one, the latest held is the one it waits on.
func (s *server) heldParentOf(ctx context.Context, names []string) (*heldParent, error) {
	named := slices.DeleteFunc(slices.Clone(names), func(name string) bool { return strings.TrimSpace(name) == "" })
	if len(named) == 0 {
		return nil, nil
	}
	var parent heldParent
	err := s.deps.Store.Pool.QueryRow(ctx, `
		select id, state, coalesce(reader, ''), coalesce(project, '')
		from held_texts
		where id = any($1) and state not in ('posted', 'failed') and kind <> 'decision_block'
		order by seq desc limit 1`, named).Scan(&parent.id, &parent.state, &parent.reader, &parent.project)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the held texts a write names: %w", err)
	}
	return &parent, nil
}

// heldWriteOf is what reviewWrite decided of a write it holds.
type heldWriteOf struct {
	route, reader, project string
	parent                 *heldParent
	// reviewed is whether the reviewer reads the text before it posts.
	reviewed bool
	// event is the writing_check_events row of the write, its outcome left to set.
	event store.WritingCheckEvent
	// personTyped is a write a person typed in a session (Decision 26), which waits on parent and
	// is never reviewed.
	personTyped bool
}

// holdWrite stores write as a held text and answers the write: its id reserved (an issue's key
// from its project's sequence, else a uuid), its request, its text as written, its actor, writer,
// reader and project, and the held text it waits on. A text the reviewer reads is recorded held
// and its review started; one it does not waits, decided, for its parent to post. A text that
// waits on no held text first runs its whole write in a transaction that is rolled back (dryRunHeld),
// so a write its own path refuses is refused now, as with the reviewer off, and never fails at
// its post.
func (s *server) holdWrite(w http.ResponseWriter, r *http.Request, actor model.Actor, write reviewedWrite, of heldWriteOf) {
	ctx := r.Context()
	request, err := json.Marshal(write.request)
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	original, err := json.Marshal(write.text)
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	author, err := json.Marshal(actor)
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	id := uuid.NewString()
	if of.parent == nil {
		if refused, err := s.dryRunHeld(write, actor, id); err != nil {
			s.writeHandlerError(w, err)
			return
		} else if refused != nil {
			for key, values := range refused.header {
				w.Header()[key] = values
			}
			w.WriteHeader(refused.status)
			_, _ = w.Write(refused.body.Bytes())
			return
		}
	}
	tx, err := s.begin(ctx)
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	defer tx.Rollback(ctx)
	if write.kind == reviewer.KindIssue {
		create := write.request.(issueCreate)
		number, err := reserveIssueNumber(ctx, tx, create.Project)
		if err != nil {
			s.writeHandlerError(w, err)
			return
		}
		id = fmt.Sprintf("%s-%d", create.Project, number)
	}
	state, outcome := heldReviewing, ""
	if !of.reviewed {
		state, outcome = heldPosting, heldNotReviewed
	}
	dependsOn := ""
	if of.parent != nil {
		dependsOn = of.parent.id
	}
	var writtenAt time.Time
	if err := tx.QueryRow(ctx, `
		insert into held_texts (id, kind, route, request, original, actor, writer, reader, project, issue_key,
			state, reviewed, outcome, depends_on, person_typed)
		values ($1, $2, $3, $4, $5, $6, nullif($7, ''), nullif($8, ''), nullif($9, ''), nullif($10, ''),
			$11, $12, nullif($13, ''), nullif($14, ''), $15)
		returning written_at`,
		id, string(write.kind), of.route, request, original, author, writerOf(actor), of.reader, of.project,
		write.subject.IssueKey, state, of.reviewed, outcome, dependsOn, of.personTyped).Scan(&writtenAt); err != nil {
		s.writeHandlerError(w, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		s.writeHandlerError(w, err)
		return
	}
	if of.reviewed {
		event := of.event
		event.Outcome = reviewHeld
		s.recordWritingCheck(ctx, event)
		s.startReview(id)
	} else {
		// Its parent may have posted while this write was held beside it.
		s.detached.goDetached(s.postReady)
	}
	answer := heldState{State: state, WaitsOn: dependsOn}
	WriteJSON(w, http.StatusCreated, heldAnswerOf(write, id, actor, writtenAt, answer, wantsDisplayTrees(r)))
}

// reserveIssueNumber takes the next number of project's sequence, as an issue create takes it,
// for an issue the reviewer holds: its key exists at once, and its post files it under that key.
func reserveIssueNumber(ctx context.Context, tx pgx.Tx, project string) (int, error) {
	var number int
	err := tx.QueryRow(ctx, `
		update projects set next_number = next_number + 1 where key = $1 returning next_number - 1
	`, project).Scan(&number)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, errorf(http.StatusNotFound, "PROJECT_NOT_FOUND", "project %s not found", project)
	}
	return number, err
}

// heldState is the `held` object a held write's answer carries beside the write's own shape.
type heldState struct {
	// State is the held text's state: reviewing, awaiting_writer, posting, posted or failed.
	State string `json:"state"`
	// Deadline is when the writer's time to act ends, null while none runs.
	Deadline *time.Time `json:"deadline"`
	// WaitsOn is the held text this one posts after, "" for none.
	WaitsOn string `json:"waits_on,omitempty"`
}

// heldAnswer is a write's answer with the `held` object merged into it, as advice is merged
// (advisedResponse): the write's own shape, built from the held row, and what holds it.
type heldAnswer struct {
	payload any
	held    heldState
}

func (answer heldAnswer) MarshalJSON() ([]byte, error) {
	payload, err := json.Marshal(answer.payload)
	if err != nil {
		return nil, err
	}
	if len(payload) < 2 || payload[0] != '{' || payload[len(payload)-1] != '}' {
		return nil, errors.New("a held write's answer must be a JSON object")
	}
	held, err := json.Marshal(answer.held)
	if err != nil {
		return nil, err
	}
	separator := []byte(`"held":`)
	if len(payload) > 2 {
		separator = []byte(`,"held":`)
	}
	return slices.Concat(payload[:len(payload)-1], separator, held, []byte("}")), nil
}

// heldAnswerOf is the answer a held write gets: today's response shape for its kind, built from
// what the hold stored under the reserved id, with the `held` object.
func heldAnswerOf(write reviewedWrite, id string, actor model.Actor, at time.Time, held heldState, trees bool) heldAnswer {
	switch create := write.request.(type) {
	case askCreate:
		ask := model.Ask{
			ID: id, IssueKey: create.Owner.IssueKey, ArtifactID: create.Owner.ArtifactID, Author: actor,
			Kind: create.Kind, Question: create.Question, Options: create.Options, Multiple: create.Multiple,
			Urgency: create.Urgency, State: "open", CreatedAt: at,
		}
		return heldAnswer{payload: newShapedAsk(ask, trees), held: held}
	case commentCreate:
		comment := model.Comment{
			ID: id, IssueKey: create.Owner.IssueKey, ArtifactID: create.Owner.ArtifactID, Author: actor,
			Body: create.Input.Body, ReplyTo: create.Input.ReplyTo, AskID: create.Input.AskID, CreatedAt: at,
			Mentions: []model.Mention{}, Deliveries: []model.CommentDelivery{},
		}
		if create.Input.Suggestion != nil {
			comment.Suggestion = &model.Suggestion{ReplaceWith: create.Input.Suggestion.ReplaceWith}
		}
		return heldAnswer{payload: commentWriteResponse{shapedComment: newShapedComment(comment, trees)}, held: held}
	case messageCreate:
		message := model.Message{
			ID: id, IssueKey: create.IssueKey, Author: actor, Body: create.Input.Body, Target: create.Input.Target,
			InReplyTo: create.Input.InReplyTo, CreatedAt: at, Deliveries: []model.MessageDelivery{},
		}
		return heldAnswer{payload: newShapedMessage(message, trees), held: held}
	case issueCreate:
		number, _ := strconv.Atoi(strings.TrimPrefix(id, create.Project+"-"))
		labels := create.Labels
		if labels == nil {
			labels = []string{}
		}
		issue := model.Issue{
			Key: id, Project: create.Project, Number: number, Title: create.Title, Status: "open",
			Priority: create.Priority, Labels: labels, Assignee: create.Assignee, BlockedBy: create.Blockers,
			CreatedBy: &actor, CreatedAt: at, UpdatedAt: at,
		}
		if issue.BlockedBy == nil {
			issue.BlockedBy = []string{}
		}
		if create.Parent != "" {
			issue.Parent = &create.Parent
		}
		return heldAnswer{payload: issue, held: held}
	case heldReplay:
		return heldAnswer{payload: replayAnswerOf(write.kind, create, id, actor, at, trees), held: held}
	}
	return heldAnswer{payload: map[string]string{"id": id}, held: held}
}

// dryRunHeld runs write as actor's under id in a transaction it rolls back before the commit
// (heldPost's dry run), so every check the kind's own path makes refuses it now. A kind's create
// path answers its refusal as an error; a reply kept whole answers it as its route answered it,
// in refused. Both are nil for a write that passed.
func (s *server) dryRunHeld(write reviewedWrite, actor model.Actor, id string) (*replayAnswer, error) {
	ctx := s.postContext()
	held := &heldPost{id: id, dryRun: true}
	var err error
	switch create := write.request.(type) {
	case askCreate:
		tx, beginErr := s.begin(ctx)
		if beginErr != nil {
			return nil, beginErr
		}
		defer tx.Rollback(ctx)
		_, err = s.writeAsk(ctx, tx, create, actor, id, held)
	case issueCreate:
		tx, beginErr := s.begin(ctx)
		if beginErr != nil {
			return nil, beginErr
		}
		defer tx.Rollback(ctx)
		_, err = s.writeIssue(ctx, tx, create, actor, 0, held)
	case commentCreate:
		_, err = s.writeComment(ctx, create, actor, id, held)
	case messageCreate:
		_, _, err = s.writeMessage(ctx, create, actor, id, held)
	case heldReplay:
		answer, err := s.runReplay(ctx, create, held)
		switch {
		case err != nil:
			return nil, err
		case answer.isDryRunPass():
			return nil, nil
		case answer.status >= 200 && answer.status < 300:
			// The route answered with no new text: a reply to an attempt already answered, which
			// its post answers the same way.
			return nil, nil
		}
		return answer, nil
	default:
		return nil, fmt.Errorf("a held write of %T has no path to run", write.request)
	}
	if errors.Is(err, errHeldDryRun) {
		return nil, nil
	}
	if err == nil {
		return nil, errors.New("a held write's dry run committed")
	}
	return nil, err
}

// errHeldNotPosting is a post that found its held text no longer to be posted: another process
// posted it first, or it failed.
var errHeldNotPosting = errors.New("the held text is not waiting to post")

// heldPost is a held text's post inside the kind's create path, which takes its held row first in
// the create's transaction and marks it posted in the same transaction. A nil *heldPost is the
// write's own path, which holds nothing. A dry run (dryRun) takes no row, and ends the create's
// transaction before its commit with errHeldDryRun.
type heldPost struct {
	id     string
	dryRun bool
}

// lock takes the held row for the post: it must still be waiting to post.
func (h *heldPost) lock(ctx context.Context, tx pgx.Tx) error {
	if h == nil || h.dryRun {
		return nil
	}
	var state string
	err := tx.QueryRow(ctx, `select state from held_texts where id = $1 for update`, h.id).Scan(&state)
	if err != nil {
		return fmt.Errorf("take held text %s for its post: %w", h.id, err)
	}
	if state != heldPosting {
		return errHeldNotPosting
	}
	return nil
}

// finish marks the held row posted, in the create's transaction: its post time is the
// transaction's, which the posted row carries as its creation time. A dry run ends here, before
// the commit.
func (h *heldPost) finish(ctx context.Context, tx pgx.Tx) error {
	if h == nil {
		return nil
	}
	if h.dryRun {
		return errHeldDryRun
	}
	_, err := tx.Exec(ctx, `
		update held_texts set state = 'posted', posted_at = now(), claimed_at = null, updated_at = now()
		where id = $1`, h.id)
	return err
}

// claimHeldReview takes the held text id for a review: it must be in review, with no review
// holding it or one held past staleClaim.
func (s *server) claimHeldReview(ctx context.Context, id string) (heldRow, bool, error) {
	row, err := scanHeld(s.deps.Store.Pool.QueryRow(ctx, `
		update held_texts set claimed_at = now()
		where id = $1 and state = 'reviewing'
			and (claimed_at is null or claimed_at < now() - $2::interval)
		returning `+heldColumns, id, intervalOf(s.deps.Reviewer.staleClaim())))
	if errors.Is(err, pgx.ErrNoRows) {
		return heldRow{}, false, nil
	}
	return row, err == nil, err
}

// heldDecision is what posts of a held text: the outcome, and the detail and the writer's reason
// the events row records.
type heldDecision struct {
	outcome, detail, reason string
}

// decideHeld decides what of the held text row posts and posts it: the row is set posting with
// the outcome where it is still in review or awaiting its writer at the wording the decision was
// made of, the outcome is recorded, and the text and every text whose turn comes after it post.
// A decision block's rewrite the block cannot hold is decided as the block written
// (applicableDecision).
func (s *server) decideHeld(ctx context.Context, row heldRow, decision heldDecision) {
	decision = s.applicableDecision(ctx, row, decision)
	tag, err := s.deps.Store.Pool.Exec(ctx, `
		update held_texts
		set state = 'posting', outcome = $3, detail = nullif($4, ''), reason = nullif($5, ''),
			deadline = null, claimed_at = null, updated_at = now()
		where id = $1 and revision = $2 and state in ('reviewing', 'awaiting_writer')`,
		row.id, row.revision, decision.outcome, decision.detail, decision.reason)
	if err != nil {
		slog.Warn("dispatch: decide a held text", "text", row.id, "outcome", decision.outcome, "error", err)
		return
	}
	if tag.RowsAffected() == 0 {
		return
	}
	detail := decision.detail
	if decision.reason != "" {
		detail = decision.reason
	}
	var rules []string
	if decision.outcome != reviewPassed && decision.outcome != reviewPostedUnreviewed {
		rules = findingRuleSets(row.findings)
	}
	s.recordHeld(ctx, row, decision.outcome, rules, detail)
	s.postReady()
}

// postReady posts every held text that is decided and whose turn has come: the text it waits on,
// if any, has posted, and every text that waits on that one and came before it has posted or
// failed. Each posts in the order the writes came. It runs on the post context (postContext),
// whatever set it off, so no request's values reach a post.
func (s *server) postReady() {
	if s.deps.Store == nil {
		return
	}
	ctx := s.postContext()
	for range 20 {
		rows, err := s.deps.Store.Pool.Query(ctx, `
			select h.id from held_texts h
			left join held_texts p on p.id = h.depends_on
			where h.state = 'posting'
				and (h.claimed_at is null or h.claimed_at < now() - $1::interval)
				and (h.depends_on is null or p.state = 'posted')
				and not exists (
					select 1 from held_texts e
					where e.depends_on = h.depends_on and e.seq < h.seq and e.state not in ('posted', 'failed'))
			order by h.seq limit 50`, intervalOf(s.deps.Reviewer.staleClaim()))
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("dispatch: read the held texts ready to post", "error", err)
			}
			return
		}
		var ids []string
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil {
				ids = append(ids, id)
			}
		}
		rows.Close()
		if len(ids) == 0 {
			return
		}
		posted := 0
		for _, id := range ids {
			if s.postHeld(ctx, id) {
				posted++
			}
		}
		if posted == 0 {
			return
		}
	}
}

// postHeld posts the held text id, which is decided: it takes the row for the post, runs the
// kind's create path with the stored request (the reviewer's rewrite in its wording where the
// outcome is rewrite_posted), the recorded actor and the reserved id, and reports whether it
// posted. A post the create path refuses fails the held text with the refusal; any other failure
// leaves it for the sweep to post again.
func (s *server) postHeld(ctx context.Context, id string) bool {
	row, err := scanHeld(s.deps.Store.Pool.QueryRow(ctx, `
		update held_texts set claimed_at = now()
		where id = $1 and state = 'posting' and (claimed_at is null or claimed_at < now() - $2::interval)
		returning `+heldColumns, id, intervalOf(s.deps.Reviewer.staleClaim())))
	if errors.Is(err, pgx.ErrNoRows) {
		return false
	}
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("dispatch: take a held text for its post", "text", id, "error", err)
		}
		return false
	}
	err = s.runHeldPost(ctx, row)
	var refused *apiError
	switch {
	case err == nil:
		s.afterHeldPost(ctx, row)
		return true
	case errors.Is(err, errHeldNotPosting):
		return false
	case errors.As(err, &refused) && refused.status < http.StatusInternalServerError:
		if _, failErr := s.deps.Store.Pool.Exec(ctx, `
			update held_texts set state = 'failed', detail = $2, claimed_at = null, updated_at = now()
			where id = $1 and state = 'posting'`, row.id, "the post was refused: "+refused.message); failErr != nil {
			slog.Warn("dispatch: fail a held text", "text", row.id, "error", failErr)
		}
		slog.Warn("dispatch: a held text's post was refused", "text", row.id, "code", refused.code, "error", refused.message)
		return false
	default:
		if ctx.Err() == nil {
			slog.Warn("dispatch: post a held text", "text", row.id, "error", err)
		}
		if _, releaseErr := s.deps.Store.Pool.Exec(context.WithoutCancel(ctx), `
			update held_texts set claimed_at = null where id = $1 and state = 'posting'`, row.id); releaseErr != nil {
			slog.Warn("dispatch: release a held text's post", "text", row.id, "error", releaseErr)
		}
		return false
	}
}

// runHeldPost runs the held text's create path. A write a person typed posts with its events
// marked theirs (events.WithPersonTyped).
func (s *server) runHeldPost(ctx context.Context, row heldRow) error {
	write, err := row.write()
	if err != nil {
		return errorf(http.StatusUnprocessableEntity, "HELD_REQUEST", "the held text's request could not be read: %v", err)
	}
	if row.outcome == reviewRewritePosted && row.rewrite != nil {
		write = withRewrite(write, *row.rewrite)
	}
	if row.personTyped {
		ctx = events.WithPersonTyped(ctx)
	}
	held := &heldPost{id: row.id}
	switch create := write.request.(type) {
	case askCreate:
		tx, err := s.begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		written, err := s.writeAsk(ctx, tx, create, row.actor, row.id, held)
		if err != nil {
			return err
		}
		s.announceAsk(ctx, written)
	case issueCreate:
		number, err := strconv.Atoi(strings.TrimPrefix(row.id, create.Project+"-"))
		if err != nil || number < 1 {
			return errorf(http.StatusUnprocessableEntity, "HELD_REQUEST", "held issue %s names no number of project %s", row.id, create.Project)
		}
		tx, err := s.begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		written, err := s.writeIssue(ctx, tx, create, row.actor, number, held)
		if errors.Is(err, errExternalIssueImported) {
			return errorf(http.StatusConflict, "EXTERNAL_IMPORTED", "the GitHub issue is imported already")
		}
		if err != nil {
			return err
		}
		s.announceIssue(ctx, written)
	case commentCreate:
		if _, err := s.writeComment(ctx, create, row.actor, row.id, held); err != nil {
			return err
		}
	case messageCreate:
		if _, _, err := s.writeMessage(ctx, create, row.actor, row.id, held); err != nil {
			return err
		}
	case heldReplay:
		return s.postReplay(ctx, row, create, held)
	case decisionBlockHold:
		return s.releaseDecisionBlock(ctx, row, held)
	}
	return nil
}

// afterHeldPost is what follows a held text's post: a text the reviewer did not read before it
// posted is reviewed after the post where its kind's setting says so.
func (s *server) afterHeldPost(ctx context.Context, row heldRow) {
	if row.reviewed || row.reader == "" || row.kind == heldRequestKind || s.deps.Reviewer.setting(reviewer.Kind(row.kind)) == ReviewOff {
		return
	}
	if checked, _ := writingCheckReads(row.actor); !checked {
		return
	}
	write, err := row.write()
	if err != nil {
		return
	}
	method, route, _ := strings.Cut(row.route, " ")
	subject := write.subject
	subject.Text, subject.Writer, subject.Reader = write.text, row.writer, row.reader
	event := store.WritingCheckEvent{
		Route: route, Method: method, ActorKind: row.actor.Kind, ActorID: row.actor.ID, Reader: row.reader, Project: row.project,
	}
	if row.actor.Act != nil {
		event.ActSession, event.TokenID = row.actor.Act.Session, row.actor.Act.TokenID
	}
	s.reviewPosted(ctx, &postedReview{kind: write.kind, text: write.text, subject: subject, event: event}, row.id)
}

// withRewrite is write with the wording of rewrite in place of its own: an ask's question and
// options, an issue's title and spec, a comment's or a message's body. The options keep no id: a
// stored ask's option ids are the index's to mint.
func withRewrite(write reviewedWrite, rewrite reviewer.Text) reviewedWrite {
	switch create := write.request.(type) {
	case askCreate:
		create.Question = rewrite.Body
		if rewrite.Options != nil {
			options := make([]model.AskOption, len(rewrite.Options))
			for i, option := range rewrite.Options {
				options[i] = model.AskOption{Label: option.Label, Description: option.Description}
			}
			create.Options = options
		}
		return create.reviewed()
	case issueCreate:
		create.Title = rewrite.Title
		spec := rewrite.Body
		create.Spec = &spec
		return create.reviewed()
	case commentCreate:
		create.Input.Body = rewrite.Body
		return create.reviewed()
	case messageCreate:
		create.Input.Body = rewrite.Body
		return create.reviewed()
	case heldReplay:
		if edited, err := create.withBody(rewrite.Body); err == nil {
			return edited.reviewed(write.kind)
		}
	}
	return write
}

// failOrphanedHeld fails each held text that waits on one that failed, which can never post.
func (s *server) failOrphanedHeld(ctx context.Context) error {
	for range 10 {
		tag, err := s.deps.Store.Pool.Exec(ctx, `
			update held_texts h
			set state = 'failed', detail = 'the held text it waits on, ' || p.id || ', failed to post',
				claimed_at = null, updated_at = now()
			from held_texts p
			where p.id = h.depends_on and p.state = 'failed' and h.state not in ('posted', 'failed')`)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
	}
	return nil
}
