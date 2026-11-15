package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/reviewer"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// held_replay.go keeps a write whole, with the caller that made it, and runs it again through the
// server's own route when its turn comes. Two kinds of write are kept so: an agent's reply to a
// delivery it got (POST /api/v1/messages/{id}/reply, POST /api/v1/comments/{id}/reply), which the
// reviewer holds as it holds a comment or a message, and which its post runs again under the id
// the hold reserved (heldPostFrom); and every other write that names a held text by its path (a
// document, a result, a handoff, a claim or an update of a held issue; an edit, an answer or a
// resolution of a held ask, comment or message), which is held beside that text and runs right
// after it posts (awaitHeldTarget, Decision 4 of INFRA-3231's plan).

// heldCaller is the caller of a kept write: the actor optionalActor resolved, with the credential
// it presented, and whether a person signed in at a browser made it. A replay presents it as the
// request's resolved caller (resolvedCallerKey), so no token is kept and none is read again.
type heldCaller struct {
	Actor      model.Actor       `json:"actor"`
	Credential *model.Credential `json:"credential,omitempty"`
	Human      bool              `json:"human,omitempty"`
}

// heldReplay is a write kept whole: its method, path, query, body and caller. Replay marks the
// stored request as one, apart from a kind's own decoded request. Text and Subject are the
// reviewer's text and where it stands, for a reply the reviewer reads; zero for any other write.
// PersonTyped is a write a person typed in a session (Dispatch-Person-Typed: 1), which runs again
// with that header.
type heldReplay struct {
	Replay      bool             `json:"replay"`
	Method      string           `json:"method"`
	Path        string           `json:"path"`
	Query       string           `json:"query,omitempty"`
	ContentType string           `json:"content_type,omitempty"`
	Body        []byte           `json:"body,omitempty"`
	Caller      heldCaller       `json:"caller"`
	Text        reviewer.Text    `json:"text"`
	Subject     reviewer.Subject `json:"subject"`
	PersonTyped bool             `json:"person_typed,omitempty"`
}

// isReplay reports whether a held text's request is a write kept whole.
func isReplay(request json.RawMessage) bool {
	var marker struct {
		Replay bool `json:"replay"`
	}
	return json.Unmarshal(request, &marker) == nil && marker.Replay
}

// withBody is replay with its JSON body's "body" field set to body: an edit or a rewrite of a
// held reply.
func (replay heldReplay) withBody(body string) (heldReplay, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(replay.Body, &fields); err != nil {
		return heldReplay{}, fmt.Errorf("read the held reply's body: %w", err)
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return heldReplay{}, err
	}
	fields["body"] = encoded
	if replay.Body, err = json.Marshal(fields); err != nil {
		return heldReplay{}, err
	}
	replay.Text.Body = body
	return replay, nil
}

// reviewed is the kept write as the reviewer reads it: a reply's text where it stands; any other
// write carries no text.
func (replay heldReplay) reviewed(kind reviewer.Kind) reviewedWrite {
	return reviewedWrite{kind: kind, request: replay, text: replay.Text, subject: replay.Subject, holdable: true}
}

// heldPostKey carries a held text's post, or its dry run, to the route a replay runs through.
type heldPostKey struct{}

// withHeldPost is ctx carrying held.
func withHeldPost(ctx context.Context, held *heldPost) context.Context {
	return context.WithValue(ctx, heldPostKey{}, held)
}

// heldPostFrom is the held text's post ctx carries, nil for a request that posts no held text.
func heldPostFrom(ctx context.Context) *heldPost {
	held, _ := ctx.Value(heldPostKey{}).(*heldPost)
	return held
}

// textIDFor is the id a write creates its text under: the id a held text's post or dry run
// reserved, else a fresh one.
func textIDFor(ctx context.Context) string {
	if held := heldPostFrom(ctx); held != nil {
		return held.id
	}
	return uuid.NewString()
}

// postContext is the context a held text's post and its dry run run on: the server's lifetime,
// with none of the values of the request that set it off (its Idempotency-Key above all, which
// the post must not bind its own recovery point to), and its own transaction tracking.
func (s *server) postContext() context.Context {
	lifetime := s.lifetime
	if lifetime == nil {
		lifetime = context.Background()
	}
	return store.WithTransactionTracking(lifetime)
}

// replayAnswer is what a replayed route answered.
type replayAnswer struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (a *replayAnswer) Header() http.Header { return a.header }
func (a *replayAnswer) WriteHeader(status int) {
	if a.status == 0 {
		a.status = status
	}
}
func (a *replayAnswer) Write(b []byte) (int, error) {
	if a.status == 0 {
		a.status = http.StatusOK
	}
	return a.body.Write(b)
}

// replayRefusal is a replayed route's answer as an error: its own status, code and message.
func (a *replayAnswer) refusal() *apiError {
	var body errorBody
	_ = json.Unmarshal(a.body.Bytes(), &body)
	return &apiError{status: a.status, code: cmpOr(body.Code, "HELD_REPLAY"), message: cmpOr(body.Error, strings.TrimSpace(a.body.String()))}
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// runReplay runs replay through the server's own routes as its caller, with held, the held text's
// post or its dry run, on the request's context. It answers the route's answer.
func (s *server) runReplay(ctx context.Context, replay heldReplay, held *heldPost) (*replayAnswer, error) {
	if s.mux == nil {
		return nil, errors.New("the server mounts no routes to run a kept write through")
	}
	target := replay.Path
	if replay.Query != "" {
		target += "?" + replay.Query
	}
	caller := replay.Caller.Actor
	caller.Credential = replay.Caller.Credential
	if caller.Credential == nil {
		caller.Credential = &model.Credential{}
	}
	ctx = context.WithValue(ctx, resolvedCallerKey{}, resolvedCaller{actor: caller, human: replay.Caller.Human})
	ctx = withHeldPost(ctx, held)
	request, err := http.NewRequestWithContext(ctx, replay.Method, target, bytes.NewReader(replay.Body))
	if err != nil {
		return nil, fmt.Errorf("build the kept write: %w", err)
	}
	if replay.ContentType != "" {
		request.Header.Set("Content-Type", replay.ContentType)
	}
	if replay.PersonTyped {
		request.Header.Set(contracts.PersonTypedHeader, "1")
	}
	if !replay.Caller.Human {
		// The caller is resolved already (resolvedCallerKey): this names a bearer caller to a
		// route that asks whether one is there, and is never read as a token.
		request.Header.Set("Authorization", "Bearer held-replay")
	}
	answer := &replayAnswer{header: http.Header{}}
	s.mux.ServeHTTP(answer, request)
	return answer, nil
}

// captureCaller is the caller r resolved, kept for a replay.
func (s *server) captureCaller(r *http.Request) (heldCaller, error) {
	actor, human, err := s.optionalActor(r)
	if err != nil {
		return heldCaller{}, err
	}
	credential := actor.Credential
	actor.Credential = nil
	return heldCaller{Actor: actor, Credential: credential, Human: human}, nil
}

// errHeldDryRun is how a held text's dry run ends: the kind's own create path ran in a
// transaction that took every check the write would, and is rolled back before its commit
// (heldPost.finish). A replayed route answers it as 202 HELD_DRY_RUN.
var errHeldDryRun = &apiError{status: http.StatusAccepted, code: "HELD_DRY_RUN", message: "the held text's dry run passed every check of its write"}

// isDryRunPass reports whether answer is a dry run that passed.
func (a *replayAnswer) isDryRunPass() bool {
	return a.status == errHeldDryRun.status && a.refusal().code == errHeldDryRun.code
}

// reviewReply is reviewWrite for an agent's reply to a delivery it got: input is the reply's
// decoded body, which holds body, the reply's text, and subject where it stands. A reply the
// reviewer holds is kept whole and posts by running the reply route again under the id the hold
// reserved; one it reviews after the post is reviewed once the handler committed it. A reply the
// post of a held one runs again is reviewed already and recorded already, and goes on.
func (s *server) reviewReply(w http.ResponseWriter, r *http.Request, actor model.Actor, input any, kind reviewer.Kind, body string, subject reviewer.Subject) (*postedReview, bool) {
	if heldPostFrom(r.Context()) != nil {
		return nil, true
	}
	rv := s.deps.Reviewer
	method, route := routeOf(r)
	setting := rv.setting(kind)
	if !rv.reviewsAny() || s.deps.Store == nil || setting == ReviewOff || generatedText(r, method+" "+route) || personTypedWrite(r) {
		return nil, s.writingCheck(w, r, actor, input)
	}
	if checked, _ := writingCheckReads(actor); !checked {
		return nil, s.writingCheck(w, r, actor, input)
	}
	ctx := r.Context()
	reader, err := s.writingCheckReader(ctx, r, method+" "+route, actor, input)
	if err != nil {
		slog.Warn("dispatch: decide the reader of a reviewed reply", "route", method+" "+route, "error", err)
		reader = ""
	}
	if reader == "" {
		return nil, s.writingCheck(w, r, actor, input)
	}
	text := reviewer.Text{Kind: kind, Body: body}
	subject.Text, subject.Writer, subject.Reader = text, writerOf(actor), reader
	project := s.projectOfReply(ctx, kind, r.PathValue("id"))
	event := store.WritingCheckEvent{
		Route: route, Method: method, ActorKind: actor.Kind, ActorID: actor.ID,
		TokenID: s.writingCheckTokenID(r, actor), Reader: reader, Project: project,
	}
	if actor.Act != nil {
		event.ActSession = actor.Act.Session
	}
	if setting != ReviewHold {
		return &postedReview{kind: kind, text: text, subject: subject, event: event}, true
	}
	caller, err := s.captureCaller(r)
	if err != nil {
		s.writeAuthenticationError(w, err)
		return nil, false
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		s.writeHandlerError(w, err)
		return nil, false
	}
	replay := heldReplay{
		Replay: true, Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, ContentType: "application/json",
		Body: encoded, Caller: caller, Text: text, Subject: subject,
	}
	write := replay.reviewed(kind)
	write.subject.IssueKey = subject.IssueKey
	s.holdWrite(w, r, actor, write, heldWriteOf{
		route: method + " " + route, reader: reader, project: project, reviewed: true, event: event,
	})
	return nil, false
}

// projectOfReply is the project of the message or the comment a reply answers, "" where it has
// none or the read fails.
func (s *server) projectOfReply(ctx context.Context, kind reviewer.Kind, id string) string {
	if _, err := uuid.Parse(id); err != nil {
		return ""
	}
	query := `select coalesce(i.project_key, '') from messages m left join issues i on i.key = m.issue_key where m.id = $1`
	if kind == reviewer.KindComment {
		query = `
			select coalesce(i.project_key, ar.project_key, '')
			from comments c
			left join artifacts ar on ar.id = c.artifact_id
			left join issues i on i.key = coalesce(c.issue_key, ar.issue_key)
			where c.id = $1`
	}
	var project string
	if err := s.deps.Store.Pool.QueryRow(ctx, query, id).Scan(&project); err != nil {
		return ""
	}
	return project
}

// heldTargets are the path values that name a text a later write may wait on, by the prefix of
// the patterns that carry them: an issue's key, and an ask's, a comment's or a message's id.
var heldTargets = []struct{ prefix, param string }{
	{"/api/v1/issues/{key}", "key"},
	{"/api/v1/asks/{id}", "id"},
	{"/api/v1/comments/{id}", "id"},
	{"/api/v1/messages/{id}", "id"},
}

// heldTargetParam is the path value of pattern that names a text a write may wait on, "" for a
// pattern that names none.
func heldTargetParam(pattern string) string {
	for _, target := range heldTargets {
		if pattern == target.prefix || strings.HasPrefix(pattern, target.prefix+"/") {
			return target.param
		}
	}
	return ""
}

// heldTargetSelf are the writes naming a held text by their path that awaitHeldTarget does not
// hold, each with why: the three create routes on an issue, which the reviewer's hook holds
// beside a held issue under an id of their own (reviewWrite), and the routes of a delivery,
// which a held text has never had.
var heldTargetSelf = map[string]string{
	"POST /api/v1/issues/{key}/asks":                         "reviewWrite holds a new ask on a held issue beside it, under an id of the ask's own",
	"POST /api/v1/issues/{key}/comments":                     "reviewWrite holds a new comment on a held issue beside it, under an id of the comment's own",
	"POST /api/v1/issues/{key}/messages":                     "reviewWrite holds a new message on a held issue beside it, under an id of the message's own",
	"POST /api/v1/messages/{id}/reply":                       "a reply to a delivered message: a held message was delivered to no session, and a held text's id here is the writer's answer to its findings (answerHeldFrame)",
	"POST /api/v1/comments/{id}/reply":                       "a reply to a delivered mention: a held comment was delivered to no session, so no attempt of it can be answered",
	"POST /api/v1/messages/{id}/deliveries/{attempt}/accept": "accepts a delivery attempt: a held message has none",
	"POST /api/v1/messages/{id}/deliveries":                  "sends a message again: a held message was never sent, and posts with its first attempt",
	"POST /api/v1/comments/{id}/deliveries":                  "sends a comment's mention again: a held comment was never sent, and posts with its first attempts",
}

// awaitHeldTarget holds route's write when the text its path names is a held text that has not
// posted: the write is kept whole with its caller (heldReplay), answered 202 with the `held`
// object, and runs through this route right after the held text posts, in the order the writes
// came. Any other write goes to handler. A route whose path names no such text, a read, and a
// route of heldTargetSelf are not wrapped.
func (s *server) awaitHeldTarget(route apiRoute, handler http.HandlerFunc) http.HandlerFunc {
	name := route.Method + " " + route.Pattern
	param, held := awaitsHeldTarget(route.Method, route.Pattern)
	if !held {
		return handler
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.deps.Reviewer.reviewsAny() || s.deps.Store == nil || heldPostFrom(r.Context()) != nil {
			handler(w, r)
			return
		}
		parent, err := s.heldParentOf(r.Context(), []string{r.PathValue(param)})
		if err != nil {
			s.writeHandlerError(w, err)
			return
		}
		if parent == nil {
			handler(w, r)
			return
		}
		s.holdRequest(w, r, name, parent)
	}
}

// awaitsHeldTarget reports whether awaitHeldTarget holds a write of method and pattern, with the
// path value that names the text it may wait on: a write whose path names an issue, an ask, a
// comment or a message, other than those heldTargetSelf names.
func awaitsHeldTarget(method, pattern string) (string, bool) {
	param := heldTargetParam(pattern)
	return param, method != http.MethodGet && param != "" && heldTargetSelf[method+" "+pattern] == ""
}

// init gives every write awaitHeldTarget holds its 202 answer in the route shapes, so the
// OpenAPI document names it where it can come.
func init() {
	for name, shape := range routeShapes {
		method, pattern, _ := strings.Cut(name, " ")
		if _, held := awaitsHeldTarget(method, pattern); held {
			shape.Also = append(slices.Clone(shape.Also), routeAnswer{Status: http.StatusAccepted, Body: heldDeferred{}})
			routeShapes[name] = shape
		}
	}
}

// heldDeferred is the answer of a write held beside a held text: the id it is kept under, and
// the `held` object, whose waits_on names the text it runs after.
type heldDeferred struct {
	ID   string    `json:"id"`
	Held heldState `json:"held"`
}

// heldRequestBodyMax bounds the body of a write kept whole: the route's own bound where it names
// one (routeBody), which the route then enforces again when the write runs.
const heldRequestBodyMax = 64 << 20

// holdRequest keeps r's write whole beside parent, a held text it names, and answers 202.
func (s *server) holdRequest(w http.ResponseWriter, r *http.Request, route string, parent *heldParent) {
	ctx := r.Context()
	caller, err := s.captureCaller(r)
	if err != nil {
		s.writeAuthenticationError(w, err)
		return
	}
	method, pattern, _ := strings.Cut(route, " ")
	limit, _ := routeBody(method, pattern)
	if limit <= 0 || limit > heldRequestBodyMax {
		limit = heldRequestBodyMax
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	if int64(len(body)) > limit {
		writeError(w, "BODY_TOO_LARGE", http.StatusRequestEntityTooLarge, "the request body is larger than this route takes")
		return
	}
	replay := heldReplay{
		Replay: true, Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery,
		ContentType: r.Header.Get("Content-Type"), Body: body, Caller: caller, PersonTyped: personTypedWrite(r),
	}
	request, err := json.Marshal(replay)
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	author, err := json.Marshal(caller.Actor)
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	id := uuid.NewString()
	if _, err := s.deps.Store.Pool.Exec(ctx, `
		insert into held_texts (id, kind, route, request, original, actor, writer, reader, project, state,
			reviewed, outcome, depends_on, person_typed)
		values ($1, 'request', $2, $3, '{}', $4, nullif($5, ''), nullif($6, ''), nullif($7, ''), 'posting',
			false, 'not_reviewed', $8, $9)`,
		id, route, request, author, writerOf(caller.Actor), parent.reader, parent.project, parent.id, replay.PersonTyped); err != nil {
		s.writeHandlerError(w, err)
		return
	}
	// Its parent may have posted while this write was kept beside it.
	s.detached.goDetached(s.postReady)
	WriteJSON(w, http.StatusAccepted, heldDeferred{ID: id, Held: heldState{State: heldPosting, WaitsOn: parent.id}})
}

// postReplay runs the kept write of row, the held text's post (held): a reply's route takes the
// held row in its own transaction and marks it posted there; any other write's row is marked
// posted once its route answered 2xx. A route's 4xx is the post's refusal.
func (s *server) postReplay(ctx context.Context, row heldRow, replay heldReplay, held *heldPost) error {
	answer, err := s.runReplay(ctx, replay, held)
	if err != nil {
		return err
	}
	switch {
	case answer.status >= 200 && answer.status < 300:
		_, err := s.deps.Store.Pool.Exec(ctx, `
			update held_texts set state = 'posted', posted_at = now(), claimed_at = null, updated_at = now()
			where id = $1 and state = 'posting'`, row.id)
		return err
	case answer.status >= 400 && answer.status < 500:
		return answer.refusal()
	}
	return fmt.Errorf("the kept write %s answered %d: %s", row.route, answer.status, strings.TrimSpace(answer.body.String()))
}

// replayAnswerOf is the answer a held reply's write gets: the reply route's own shape, built
// from what the hold stored under the reserved id.
func replayAnswerOf(kind reviewer.Kind, replay heldReplay, id string, actor model.Actor, at time.Time, trees bool) any {
	parent := replay.Subject.InReplyTo
	if kind == reviewer.KindComment {
		parent = replay.Subject.ReplyTo
		comment := model.Comment{
			ID: id, Author: actor, Body: replay.Text.Body, CreatedAt: at,
			Mentions: []model.Mention{}, Deliveries: []model.CommentDelivery{},
		}
		if parent != "" {
			comment.ReplyTo = &parent
		}
		if replay.Subject.IssueKey != "" {
			comment.IssueKey = &replay.Subject.IssueKey
		}
		return commentWriteResponse{shapedComment: newShapedComment(comment, trees)}
	}
	message := model.Message{ID: id, Author: actor, Body: replay.Text.Body, CreatedAt: at, Deliveries: []model.MessageDelivery{}}
	if parent != "" {
		message.InReplyTo = &parent
	}
	if replay.Subject.IssueKey != "" {
		message.IssueKey = &replay.Subject.IssueKey
	}
	return replyRead{shapedMessage: newShapedMessage(message, trees)}
}
