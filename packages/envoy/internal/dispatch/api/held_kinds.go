package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/dispatch/itemblocks"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
	"github.com/sjawhar/envoy/internal/dispatch/reviewer"
)

// held_kinds.go is each kind of write the reviewer holds, as the one interface the hold, the edit,
// the post, the rewrite and the order of one writer's texts read it through (heldRequest): a new
// ask (askCreate), a new issue (issueCreate), a comment (commentCreate), a message
// (messageCreate), a write kept whole (heldReplay) and a decision block's hold
// (decisionBlockHold). A new kind is one type that implements it.

// heldRequest is a held write's request, decoded: what a held text stores and its post runs.
type heldRequest interface {
	// answer is the answer the held write gets, in its route's own shape, built from what the hold
	// stored under the reserved id and from dry, its dry run, never nil (heldAnswerOf).
	answer(kind reviewer.Kind, id string, actor model.Actor, at time.Time, dry *heldDryRun, trees bool) any
	// advice is the advice the held write's answer carries, given advice, the write advice its dry
	// run found (nil for none), and parent, the held text it waits on (heldAdvice); nil for none.
	advice(s *server, r *http.Request, actor model.Actor, advice *writeAdvice, dry *heldDryRun, parent *heldParent) any
	// dryRun runs the write as actor's under id on its create path with held, a dry run's post,
	// which ends the path before its commit, and answers what the run found (dryRunHeld).
	dryRun(s *server, ctx context.Context, actor model.Actor, id string, held *heldPost) (heldDryRun, error)
	// post runs the write's create path for the held row, held marking it posted in the path's
	// own transaction (runHeldPost).
	post(s *server, ctx context.Context, row heldRow, held *heldPost) error
	// edited is the request with input's wording in place of its own, checked as the kind's create
	// checks it (editedRequest).
	edited(input heldEditInput) (heldRequest, error)
	// rewritten is the write of kind with rewrite's wording in place of its own, false for a kind
	// whose wording no rewrite replaces (withRewrite).
	rewritten(kind reviewer.Kind, rewrite reviewer.Text) (reviewedWrite, bool)
	// place is where the text posts, for the order of one writer's held texts there
	// (heldBeforeOf): the issue or the document of an ask or a comment, the session or the role a
	// message goes to, else its issue. It is "" for a write whose order something else keeps: a
	// new issue, by the parent it names; a write kept whole, by the delivery a reply answers or the
	// held text its path names (heldReplyPlaceOf, awaitHeldTarget); a decision block, by its place
	// in its document.
	place() string
}

// placeOf is the place of a text on o (heldRequest.place): its issue, else its document.
func placeOf(o owner) string {
	switch {
	case o.IssueKey != nil:
		return "issue " + *o.IssueKey
	case o.ArtifactID != nil:
		return "document " + *o.ArtifactID
	}
	return ""
}

// refuseHeldEdit is an edit's refusal of a field its kind does not hold, or of an edit that
// changes nothing: 400 HELD_EDIT_INPUT.
func refuseHeldEdit(format string, args ...any) error {
	return errorf(http.StatusBadRequest, "HELD_EDIT_INPUT", format, args...)
}

// dryRunEnded is a create path's dry run as it ended: dry where it passed every check
// (errHeldDryRun), and its refusal where it did not. A path that committed broke the dry run.
func dryRunEnded(dry heldDryRun, err error) (heldDryRun, error) {
	if errors.Is(err, errHeldDryRun) {
		return dry, nil
	}
	if err == nil {
		return heldDryRun{}, errors.New("a held write's dry run committed")
	}
	return heldDryRun{}, err
}

// dryRunAdvice is the write advice a comment's or a message's dry run found, as its held answer
// merges it: none where the dry run found none, rather than a nil *writeAdvice inside an any.
func dryRunAdvice(advice *writeAdvice) any {
	if advice != nil {
		return advice
	}
	return nil
}

// The new ask.

func (c askCreate) answer(_ reviewer.Kind, id string, actor model.Actor, at time.Time, dry *heldDryRun, trees bool) any {
	ask := model.Ask{
		ID: id, IssueKey: c.Owner.IssueKey, ArtifactID: c.Owner.ArtifactID, Author: actor,
		Kind: c.Kind, Question: c.Question, Options: c.Options, Multiple: c.Multiple,
		Urgency: c.Urgency, State: "open", CreatedAt: at,
	}
	if wrote := dry.ask; wrote != nil {
		ask.Anchor, ask.AnchorArtifact, ask.WaitingOnPerson = heldAnchor(wrote.Anchor), wrote.AnchorArtifact, wrote.WaitingOnPerson
	}
	return newShapedAsk(ask, trees)
}

// advice is the ask's write advice with its related-item check, run here, before the id is
// reserved, as the write's own path runs it after its commit. An ask on a held issue searches
// that issue's project, which no issue row holds yet.
func (c askCreate) advice(s *server, r *http.Request, actor model.Actor, advice *writeAdvice, _ *heldDryRun, parent *heldParent) any {
	ctx := r.Context()
	input := relatedInput{
		text: c.Question, title: c.Question, source: suggestionSource{kind: "ask", actor: actor},
		question: true,
	}
	if c.Owner.IssueKey != nil {
		input.source.issueKey = *c.Owner.IssueKey
	} else if c.Owner.ArtifactID != nil {
		input.source.artifactID = *c.Owner.ArtifactID
	}
	if parent != nil {
		input.project = parent.project
	}
	input.trees = s.readDisplayTrees(ctx, wantsDisplayTrees(r))
	suggestions := s.relatedCheck(ctx, input)
	input.trees.resolve()
	return suggestedAdvice(advice, suggestions)
}

func (c askCreate) dryRun(s *server, ctx context.Context, actor model.Actor, id string, held *heldPost) (heldDryRun, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return heldDryRun{}, err
	}
	defer tx.Rollback(ctx)
	written, err := s.writeAsk(ctx, tx, c, actor, id, held)
	return dryRunEnded(heldDryRun{advice: written.advice, ask: &written.ask}, err)
}

func (c askCreate) post(s *server, ctx context.Context, row heldRow, held *heldPost) error {
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	written, err := s.writeAsk(ctx, tx, c, row.actor, row.id, held)
	if err != nil {
		return err
	}
	s.announceAsk(ctx, written)
	return nil
}

// edited takes the ask's question and its options.
func (c askCreate) edited(input heldEditInput) (heldRequest, error) {
	if input.Title != nil || input.Spec != nil || input.Body != nil {
		return nil, refuseHeldEdit("an ask's edit takes its question and its options")
	}
	if input.Question == nil && input.Options == nil {
		return nil, refuseHeldEdit("an ask's edit takes its question, its options or both")
	}
	if input.Question != nil {
		if err := validateAskQuestion(*input.Question); err != nil {
			return nil, err
		}
		c.Question = *input.Question
	}
	if input.Options != nil {
		options := askOptionsOf(*input.Options)
		if err := validateAskOptions(options); err != nil {
			return nil, err
		}
		c.Options = options
	}
	if err := validateAskWording(c.Question, c.Options); err != nil {
		return nil, err
	}
	return c, nil
}

// rewritten takes the rewrite's question and options. The options keep no id: a stored ask's
// option ids are the index's to mint (reviewer.OptionWords).
func (c askCreate) rewritten(_ reviewer.Kind, rewrite reviewer.Text) (reviewedWrite, bool) {
	c.Question = rewrite.Body
	if rewrite.Options != nil {
		c.Options = reviewer.OptionWords(rewrite.Options)
	}
	return c.reviewed(), true
}

func (c askCreate) place() string { return placeOf(c.Owner) }

// The new issue.

func (c issueCreate) answer(_ reviewer.Kind, id string, actor model.Actor, at time.Time, dry *heldDryRun, _ bool) any {
	number, _ := strconv.Atoi(strings.TrimPrefix(id, c.Project+"-"))
	labels := c.Labels
	if labels == nil {
		labels = []string{}
	}
	issue := model.Issue{
		Key: id, Project: c.Project, Number: number, Title: c.Title, Status: model.IssueStatusTriage,
		Priority: c.Priority, Labels: labels, Assignee: c.Assignee, BlockedBy: c.Blockers,
		CreatedBy: &actor, CreatedAt: at, UpdatedAt: at,
	}
	if issue.BlockedBy == nil {
		issue.BlockedBy = []string{}
	}
	if c.Parent != "" {
		issue.Parent = &c.Parent
	}
	heldIssueWrites(&issue, c, dry.issue)
	return issue
}

// advice is the issue's write advice with its related-item check, run here, before the id is
// reserved, over the spec as the dry run's seed stored it (as sent where no dry run seeded it),
// and what the near-copy gate told its create (issueCreate.gate), the note of a check not made
// included.
func (c issueCreate) advice(s *server, r *http.Request, actor model.Actor, advice *writeAdvice, dry *heldDryRun, _ *heldParent) any {
	ctx := r.Context()
	markdown := ""
	switch {
	case dry != nil:
		markdown = dry.markdown
	case c.Spec != nil && strings.TrimSpace(*c.Spec) != "":
		markdown = *c.Spec
	}
	trees := s.readDisplayTrees(ctx, wantsDisplayTrees(r))
	suggestions := s.relatedCheck(ctx, relatedInput{
		project: c.Project,
		text:    c.Title + "\n" + markdown,
		title:   c.Title,
		source:  suggestionSource{kind: "issue", actor: actor},
		trees:   trees,
	})
	trees.resolve()
	c.gate.tell(suggestions)
	return suggestedAdvice(advice, suggestions)
}

func (c issueCreate) dryRun(s *server, ctx context.Context, actor model.Actor, _ string, held *heldPost) (heldDryRun, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return heldDryRun{}, err
	}
	defer tx.Rollback(ctx)
	written, err := s.writeIssue(ctx, tx, c, actor, 0, held)
	return dryRunEnded(heldDryRun{advice: written.advice, markdown: written.markdown, issue: &written.issue}, err)
}

// post files the issue under the number its hold reserved, after the near-copy gate's second
// read (recheckNearCopy), and tells its writer of a near copy found then (tellNearCopy).
func (c issueCreate) post(s *server, ctx context.Context, row heldRow, held *heldPost) error {
	number, err := strconv.Atoi(strings.TrimPrefix(row.id, c.Project+"-"))
	if err != nil || number < 1 {
		return errorf(http.StatusUnprocessableEntity, "HELD_REQUEST", "held issue %s names no number of project %s", row.id, c.Project)
	}
	create, copies := s.recheckNearCopy(ctx, c)
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
	s.tellNearCopy(ctx, row, copies)
	return nil
}

// edited takes the issue's title and its spec.
func (c issueCreate) edited(input heldEditInput) (heldRequest, error) {
	if input.Question != nil || input.Options != nil || input.Body != nil {
		return nil, refuseHeldEdit("an issue's edit takes its title and its spec")
	}
	if input.Title == nil && input.Spec == nil {
		return nil, refuseHeldEdit("an issue's edit takes its title, its spec or both")
	}
	if input.Title != nil {
		title := strings.TrimSpace(*input.Title)
		if title == "" {
			return nil, errorf(http.StatusBadRequest, "INVALID_ISSUE", "title is required")
		}
		if length := itemblocks.Units16(title); length > contracts.IssueTitleMax {
			return nil, capExceededError("title", length, contracts.IssueTitleMax)
		}
		c.Title = title
	}
	if input.Spec != nil {
		spec := *input.Spec
		c.Spec = &spec
	}
	return c, nil
}

// rewritten takes the rewrite's title and spec.
func (c issueCreate) rewritten(_ reviewer.Kind, rewrite reviewer.Text) (reviewedWrite, bool) {
	c.Title = rewrite.Title
	spec := rewrite.Body
	c.Spec = &spec
	return c.reviewed(), true
}

func (c issueCreate) place() string { return "" }

// The comment.

func (c commentCreate) answer(_ reviewer.Kind, id string, actor model.Actor, at time.Time, dry *heldDryRun, trees bool) any {
	comment := model.Comment{
		ID: id, IssueKey: c.Owner.IssueKey, ArtifactID: c.Owner.ArtifactID, Author: actor,
		Body: c.Input.Body, ReplyTo: c.Input.ReplyTo, AskID: c.Input.AskID, CreatedAt: at,
		Mentions: []model.Mention{}, Deliveries: []model.CommentDelivery{},
	}
	if c.Input.Suggestion != nil {
		comment.Suggestion = &model.Suggestion{ReplaceWith: c.Input.Suggestion.ReplaceWith}
	}
	if wrote := dry.comment; wrote != nil {
		heldCommentWrites(&comment, wrote)
	}
	return commentWriteResponse{shapedComment: newShapedComment(comment, trees), AskWaitingOn: dry.askWaitingOn}
}

// advice is the comment's write advice, which its dry run found.
func (c commentCreate) advice(_ *server, _ *http.Request, _ model.Actor, advice *writeAdvice, _ *heldDryRun, _ *heldParent) any {
	return dryRunAdvice(advice)
}

func (c commentCreate) dryRun(s *server, ctx context.Context, actor model.Actor, id string, held *heldPost) (heldDryRun, error) {
	written, err := s.writeComment(ctx, c, actor, id, held)
	return dryRunEnded(heldDryRun{advice: written.advice, comment: &written.comment, askWaitingOn: written.waitingOn}, err)
}

func (c commentCreate) post(s *server, ctx context.Context, row heldRow, held *heldPost) error {
	_, err := s.writeComment(ctx, c, row.actor, row.id, held)
	return err
}

// edited takes the comment's body.
func (c commentCreate) edited(input heldEditInput) (heldRequest, error) {
	if input.Question != nil || input.Options != nil || input.Title != nil || input.Spec != nil || input.Body == nil {
		return nil, refuseHeldEdit("a comment's edit takes its body")
	}
	body := pmdoc.LineFeeds(*input.Body)
	if c.Input.Suggestion == nil && strings.TrimSpace(body) == "" {
		return nil, errorf(http.StatusBadRequest, "INVALID_COMMENT", "comment body is required")
	}
	if err := blockBoundError(itemblocks.Check("body", body)); err != nil {
		return nil, err
	}
	c.Input.Body = body
	return c, nil
}

// rewritten takes the rewrite's body.
func (c commentCreate) rewritten(_ reviewer.Kind, rewrite reviewer.Text) (reviewedWrite, bool) {
	c.Input.Body = rewrite.Body
	return c.reviewed(), true
}

func (c commentCreate) place() string { return placeOf(c.Owner) }

// The message.

func (c messageCreate) answer(_ reviewer.Kind, id string, actor model.Actor, at time.Time, dry *heldDryRun, trees bool) any {
	message := model.Message{
		ID: id, IssueKey: c.IssueKey, Author: actor, Body: c.Input.Body, Target: c.Input.Target,
		InReplyTo: c.Input.InReplyTo, CreatedAt: at, Deliveries: []model.MessageDelivery{},
	}
	if wrote := dry.message; wrote != nil {
		heldMessageWrites(&message, wrote)
	}
	return newShapedMessage(message, trees)
}

// advice is the message's write advice, which its dry run found.
func (c messageCreate) advice(_ *server, _ *http.Request, _ model.Actor, advice *writeAdvice, _ *heldDryRun, _ *heldParent) any {
	return dryRunAdvice(advice)
}

func (c messageCreate) dryRun(s *server, ctx context.Context, actor model.Actor, id string, held *heldPost) (heldDryRun, error) {
	message, advice, err := s.writeMessage(ctx, c, actor, id, held)
	return dryRunEnded(heldDryRun{advice: advice, message: &message}, err)
}

func (c messageCreate) post(s *server, ctx context.Context, row heldRow, held *heldPost) error {
	_, _, err := s.writeMessage(ctx, c, row.actor, row.id, held)
	return err
}

// edited takes the message's body, checked as a new message's is.
func (c messageCreate) edited(input heldEditInput) (heldRequest, error) {
	if input.Question != nil || input.Options != nil || input.Title != nil || input.Spec != nil || input.Body == nil {
		return nil, refuseHeldEdit("a message's edit takes its body")
	}
	c.Input.Body = *input.Body
	delivery, err := validateCreateMessage(&c.Input)
	if err != nil {
		return nil, err
	}
	c.Delivery = delivery
	return c, nil
}

// rewritten takes the rewrite's body.
func (c messageCreate) rewritten(_ reviewer.Kind, rewrite reviewer.Text) (reviewedWrite, bool) {
	c.Input.Body = rewrite.Body
	return c.reviewed(), true
}

func (c messageCreate) place() string {
	if target := deref(c.Input.Target); target != "" {
		return "to " + target
	}
	return "issue " + deref(c.IssueKey)
}

// The write kept whole: a reply, or a write that names a held text.

func (c heldReplay) answer(kind reviewer.Kind, id string, actor model.Actor, at time.Time, dry *heldDryRun, trees bool) any {
	return replayAnswerOf(kind, c, id, actor, at, dry, trees)
}

// advice is none: the route's own answer carries what it carries.
func (c heldReplay) advice(*server, *http.Request, model.Actor, *writeAdvice, *heldDryRun, *heldParent) any {
	return nil
}

// dryRun runs the route through the server's own mux (runReplay). It passes where the route ends
// the dry run, which keeps the reply it wrote (heldPost.keep); any other answer of the route is
// the reply's own, in answered: a refusal, or a reply to an attempt already answered, which writes
// nothing and answers the stored reply as it does with the reviewer off (the reply routes'
// finishRepeat), so nothing is held for it.
func (c heldReplay) dryRun(s *server, ctx context.Context, _ model.Actor, _ string, held *heldPost) (heldDryRun, error) {
	// The reply's route reads what its hold learned of the writer's role (roleHolderReplyOf).
	held.roleHolder = c.RoleHolder
	answer, err := s.runReplay(ctx, c, held)
	switch {
	case err != nil:
		return heldDryRun{}, err
	case answer.isDryRunPass():
		var dry heldDryRun
		switch wrote := held.wrote.(type) {
		case commentWritten:
			dry.comment, dry.askWaitingOn = &wrote.comment, wrote.waitingOn
		case model.Message:
			dry.message = &wrote
		}
		return dry, nil
	}
	return heldDryRun{answered: answer}, nil
}

func (c heldReplay) post(s *server, ctx context.Context, row heldRow, held *heldPost) error {
	held.roleHolder = c.RoleHolder
	return s.postReplay(ctx, row, c, held)
}

// edited takes a held reply's body; a write kept whole that holds no text takes no edit.
func (c heldReplay) edited(input heldEditInput) (heldRequest, error) {
	if c.Text.Body == "" || input.Question != nil || input.Options != nil || input.Title != nil || input.Spec != nil || input.Body == nil {
		return nil, refuseHeldEdit("a held reply's edit takes its body")
	}
	body := pmdoc.LineFeeds(*input.Body)
	if strings.TrimSpace(body) == "" {
		return nil, refuseHeldEdit("a reply's body is required")
	}
	if err := blockBoundError(itemblocks.Check("body", body)); err != nil {
		return nil, err
	}
	edited, err := c.withBody(body)
	if err != nil {
		return nil, err
	}
	return edited, nil
}

// rewritten takes the rewrite's body into the kept request's body.
func (c heldReplay) rewritten(kind reviewer.Kind, rewrite reviewer.Text) (reviewedWrite, bool) {
	edited, err := c.withBody(rewrite.Body)
	if err != nil {
		return reviewedWrite{}, false
	}
	return edited.reviewed(kind), true
}

func (c heldReplay) place() string { return "" }

// The decision block's hold: the block is in its document already, so its hold answers nothing,
// runs no dry run, takes no edit and no rewrite of its request; its post ends the hold
// (releaseDecisionBlock).

func (c decisionBlockHold) answer(_ reviewer.Kind, id string, _ model.Actor, _ time.Time, _ *heldDryRun, _ bool) any {
	return map[string]string{"id": id}
}

func (c decisionBlockHold) advice(*server, *http.Request, model.Actor, *writeAdvice, *heldDryRun, *heldParent) any {
	return nil
}

func (c decisionBlockHold) dryRun(*server, context.Context, model.Actor, string, *heldPost) (heldDryRun, error) {
	// The request is no part of the text: an error a handler logs carries nothing of an actor.
	return heldDryRun{}, errors.New("a held write of a kind this server has no path for cannot run")
}

func (c decisionBlockHold) post(s *server, ctx context.Context, row heldRow, held *heldPost) error {
	return s.releaseDecisionBlock(ctx, row, held)
}

func (c decisionBlockHold) edited(heldEditInput) (heldRequest, error) {
	return nil, refuseHeldEdit("this held text takes no edit")
}

func (c decisionBlockHold) rewritten(reviewer.Kind, reviewer.Text) (reviewedWrite, bool) {
	return reviewedWrite{}, false
}

func (c decisionBlockHold) place() string { return "" }
