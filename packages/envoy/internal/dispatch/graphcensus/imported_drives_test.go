package graphcensus_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/api"
	"github.com/sjawhar/envoy/internal/dispatch/auth"
	"github.com/sjawhar/envoy/internal/dispatch/docs"
	"github.com/sjawhar/envoy/internal/dispatch/envoy"
	"github.com/sjawhar/envoy/internal/dispatch/events"
	"github.com/sjawhar/envoy/internal/dispatch/graphcensus"
	"github.com/sjawhar/envoy/internal/dispatch/grapherase"
	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/identity"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// This file is the package's external test because the sites it drives live in packages that
// import graphcensus - docs imports it for the block-index tick's comment write, api imports
// docs, and grapherase imports api - so the package's own tests cannot import them. It hands its
// drives to TestTheSizingCountsEveryTupleTheWritersAppend as the test binary starts.
func init() { graphcensus.ImportedDrives = importedDrives }

// writersServerURL is the public origin the writers' server serves under.
const writersServerURL = "https://dispatch.sizing.invalid"

// The sessions the stand-in listener knows, each live: the mention whose send it holds until the
// test lets it go, the mention whose send it refuses, and the message's target, whose send it
// takes at once.
const (
	heldSession    = "ses_mentioned"
	refusedSession = "ses_refused"
	messageSession = "ses_messaged"
)

// writersRig is the Dispatch API as cmd/dispatch builds it (api.NewDeps, api.Register) over a
// database of its own and the document service beside it (docs.New), with a stand-in Envoy
// listener, and the rows its drives leave for the drives after them. The document service settles
// nothing on a timer of its own (Settle is an hour): the one settlement a drive runs, it asks for
// (docs.Service.SettleNow).
type writersRig struct {
	database  *store.Store
	documents *docs.Service
	handler   http.Handler
	// arrived is signalled once the held mention's send reaches the listener, and release lets it
	// answer.
	arrived chan struct{}
	release func()
	// resume is one pass of the delivery sweep, which the maintenance loop runs once a minute.
	resume func(context.Context) error
	// The issue and its spec, the issue's notes document and the slug an anchor names it by, the
	// comment on the issue the erase takes, the comment whose held send the settle settles (and,
	// held, the one whose mention a Retry sends again), the ask the follower follows, the targeted
	// message the session's reply answers, and the untouched document the removal removes.
	issue, spec, notes, notesSlug, comment, mentioned, held, ask, message, removable string
}

func newWritersRig(t *testing.T) *writersRig {
	t.Helper()
	return newStoppableWritersRig(t, nil)
}

// newStoppableWritersRig is newWritersRig whose server's lifetime ends where the test asks, through
// the function it sets *stopping to, where stopping is not nil.
func newStoppableWritersRig(t *testing.T, stopping *func()) *writersRig {
	t.Helper()
	ctx := context.Background()
	rig := &writersRig{arrived: make(chan struct{}, 1)}
	released := make(chan struct{})
	var once sync.Once
	rig.release = func() { once.Do(func() { close(released) }) }
	record := func(session string) map[string]any {
		return map[string]any{
			"session_id": session, "title": session, "capabilities": []string{"btw", "steer"},
			"state": envoy.StateLive, "last_seen": time.Now().UnixMilli(),
		}
	}
	known := []string{heldSession, refusedSession, messageSession}
	listener := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions":
			var records []map[string]any
			for _, session := range known {
				records = append(records, record(session))
			}
			_ = json.NewEncoder(w).Encode(records)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/registry/"):
			session := strings.TrimPrefix(r.URL.Path, "/v1/registry/")
			for _, live := range known {
				if live == session {
					_ = json.NewEncoder(w).Encode(record(session))
					return
				}
			}
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"no live session"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/messages/send":
			var send struct {
				TargetSession string `json:"target_session"`
			}
			if err := json.NewDecoder(r.Body).Decode(&send); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			switch send.TargetSession {
			case refusedSession:
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":"the session's pane is gone"}`))
				return
			case heldSession:
				select {
				case rig.arrived <- struct{}{}:
				default:
				}
				select {
				case <-released:
				case <-r.Context().Done():
					return
				}
			}
			_, _ = fmt.Fprintf(w, `{"event_id":"envelope-%s","recipient":%q,"duplicate":false}`, send.TargetSession, send.TargetSession)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"no live session"}`))
		}
	}))
	t.Cleanup(listener.Close)

	rig.database = storetest.Open(t)
	people := store.NewPgPeopleStore(rig.database.Pool, auth.SingleSigningKey("signing-key"), nil)
	if err := people.Record(ctx, "alice"); err != nil {
		t.Fatalf("record alice: %v", err)
	}
	// One identity for the API and the document service, so a person's browser that connects to a
	// document's room is admitted by the same header the API reads (emptyAskQuestion).
	caller := identity.HeaderIdentity{Header: "X-Dispatch-User", People: people}
	broker := events.NewBroker()
	rig.documents = docs.New(docs.Deps{Store: rig.database, Events: broker, Identity: caller, ServerURL: writersServerURL, Settle: time.Hour})
	t.Cleanup(func() {
		if err := rig.documents.Shutdown(context.Background()); err != nil {
			t.Errorf("shut down the document service: %v", err)
		}
	})
	tokens, err := auth.ParseSharedAgentTokens("agent-token")
	if err != nil {
		t.Fatalf("parse the agent token: %v", err)
	}
	deps, err := api.NewDeps(api.DepsInput{
		Store:       rig.database,
		Identity:    caller,
		AgentTokens: tokens,
		ServerURL:   writersServerURL,
		EnvoyURL:    listener.URL,
		// The held send waits on the test longer than the client's own window would let it.
		EnvoyTimeout: time.Minute,
		// A refused send settles failed once the take-over window has passed, which a test
		// need not wait the production twelve seconds for.
		ListenerTakeoverWindow: 100 * time.Millisecond,
		Docs:                   rig.documents,
		Events:                 broker,
	})
	if err != nil {
		t.Fatalf("build the API's dependencies: %v", err)
	}
	mux := http.NewServeMux()
	if stopping == nil {
		// t.Context, the server's lifetime, ends before the cleanups run, so Wait returns once
		// the deliveries and handlers it started have ended, before the database closes under
		// them; the held send is let go first, so none of them waits on the test.
		background := api.Register(mux, t.Context(), deps)
		t.Cleanup(background.Wait)
		rig.resume = background.ResumeDeliveries
	} else {
		// A lifetime the test ends itself, and the shutdown it then runs: the cancel, and the
		// release of the held send and the wait for everything the server started, in that
		// order, at most once, the test's cleanup running it should a drive not.
		lifetime, cancel := context.WithCancel(t.Context())
		background := api.Register(mux, lifetime, deps)
		var stopped sync.Once
		*stopping = func() {
			stopped.Do(func() {
				cancel()
				rig.release()
				background.Wait()
			})
		}
		t.Cleanup(*stopping)
		rig.resume = background.ResumeDeliveries
	}
	t.Cleanup(rig.release)
	rig.handler = mux
	rig.request(t, http.MethodPost, "/api/v1/projects", map[string]any{"key": "SIZE", "name": "Sizing"}, "alice", http.StatusCreated)
	return rig
}

// request sends one request to the server: as the person login names, or, with no login, under
// the shared agent bearer, whose body names the session it acts as. It fails the test unless the
// answer is want, and answers the body.
func (r *writersRig) request(t *testing.T, method, target string, body any, login string, want int) []byte {
	t.Helper()
	return r.keyedRequest(t, method, target, body, login, "", want)
}

// keyedRequest is request under the Idempotency-Key key, none where key is empty.
func (r *writersRig) keyedRequest(t *testing.T, method, target string, body any, login, key string, want int) []byte {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode %s %s: %v", method, target, err)
	}
	request := httptest.NewRequest(method, target, bytes.NewReader(encoded))
	request.Header.Set("Content-Type", "application/json")
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	if login != "" {
		request.Header.Set("X-Dispatch-User", login)
	} else {
		request.Header.Set("Authorization", "Bearer agent-token")
	}
	response := httptest.NewRecorder()
	r.handler.ServeHTTP(response, request)
	if response.Code != want {
		t.Fatalf("%s %s: status=%d body=%s, want %d", method, target, response.Code, response.Body.String(), want)
	}
	return response.Body.Bytes()
}

// decoded decodes body into a T, failing the test where it cannot.
func decoded[T any](t *testing.T, body []byte) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return value
}

// sessionActor is a body's actor for the session id.
func sessionActor(id string) map[string]any { return map[string]any{"kind": "session", "id": id} }

// awaitTrue waits until query, a select of one boolean over args, answers true, and fails the test
// naming what once it has waited longer than any of the drives' work takes.
func (r *writersRig) awaitTrue(t *testing.T, what, query string, args ...any) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		var done bool
		if err := r.database.Pool.QueryRow(context.Background(), query, args...).Scan(&done); err != nil {
			t.Fatalf("read whether %s: %v", what, err)
		}
		if done {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not happen within 30s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// settledAttempts waits until no attempt of the comment is pending and one of them is in state.
func (r *writersRig) settledAttempts(t *testing.T, comment, state string) {
	t.Helper()
	r.awaitTrue(t, "comment "+comment+"'s attempt settling "+state, `
		select not exists (select 1 from comment_deliveries where comment_id = $1::uuid and state = 'pending')
		   and exists (select 1 from comment_deliveries where comment_id = $1::uuid and state = $2)
	`, comment, state)
}

// importedDrives are the drives of every site the register of the store's writers lists in api,
// docs, asks and grapherase, each the real site reached as its own lock-step driver reaches it:
// the API's through its HTTP routes, the settle of a delivery through the send the stand-in
// listener answers, the settlement and the block-index tick through the document service, and the
// erase through grapherase.Erase. They run in this order over one database, each needing what the
// ones before it wrote: an issue with its spec, an upload and an edit of a document, a comment on
// the issue and one anchored to the document mentioning a session whose send is held, that send's
// settle, a reply to a mention whose send was refused, a targeted message's delivery, an ask a
// session opens, a follow of it, the spec's approval request renewed on a retry and handed back, a
// settlement opening an ask a session wrote as a block, the tick indexing a version a build
// without the block index wrote, and the erase of the issue's comment, which appends nothing. The
// comment path's drives name the sites the path runs in turn (comment): the comment's node, its
// tuples under its event, and who wrote it.
func importedDrives(t *testing.T) []graphcensus.StoreDrive {
	t.Helper()
	r := newWritersRig(t)
	ctx := context.Background()
	// The direct message alice sends the message's session, which that session accepts, and the
	// keyed comment whose repeat puts its mention back on a lane, with its body and its key.
	var direct, replayed string
	replayedBody := map[string]any{
		"body": "Over to you, @" + messageSession + ".", "mentions": []map[string]any{{"target": "session:" + messageSession}},
	}
	const replayedKey = "sizing-replayed-comment"
	comment := func(name string, sites ...string) []string {
		return append([]string{name, "api/comment_create.go#writeNode", "api/comment_create.go#caused",
			"graphstore/people.go#QueueAuthored"}, sites...)
	}
	return append([]graphcensus.StoreDrive{{
		Name: "api/issue_create.go#createIssue", Database: r.database,
		Sites: []string{"api/issue_create.go#createIssue", "refs/blocks.go#Queue"},
		Drive: func(t *testing.T) {
			created := decoded[struct {
				Key     string `json:"key"`
				Primary string `json:"primary_artifact_id"`
			}](t, r.request(t, http.MethodPost, "/api/v1/issues", map[string]any{
				"project": "SIZE", "title": "What the writers write", "spec": "# Spec\n\nWhat the writers write.\n",
			}, "alice", http.StatusCreated))
			r.issue, r.spec = created.Key, created.Primary
		},
		Counts: merged(newBlockCounted, objectNodeCounted(graphstore.KindIssue), objectNodeCounted(graphstore.KindArtifact)),
	}, {
		Name: "api/artifacts.go#storeArtifact", Database: r.database,
		Sites: []string{"api/artifacts.go#storeArtifact", "api/artifacts.go#queueNewArtifact", "refs/blocks.go#Queue"},
		Drive: func(t *testing.T) {
			notes := decoded[struct {
				Artifact model.Artifact `json:"artifact"`
			}](t, r.request(t, http.MethodPost, "/api/v1/issues/"+r.issue+"/artifacts", map[string]any{
				"name": "notes.md", "content": "# Notes\n\nA paragraph the upload indexes.\n",
			}, "alice", http.StatusCreated)).Artifact
			r.notes, r.notesSlug = notes.ID, notes.Slug
		},
		Counts: merged(newBlockCounted, objectNodeCounted(graphstore.KindArtifact)),
	}, {
		Name: "api/artifacts.go#commitArtifactVersionEvent", Database: r.database,
		Sites: []string{"api/artifacts.go#commitArtifactVersionEvent", "refs/blocks.go#Queue"},
		Drive: func(t *testing.T) {
			r.request(t, http.MethodPost, "/api/v1/artifacts/"+r.notes+"/edits", map[string]any{
				"ops": []map[string]any{{"op": "insert", "after": "end", "markdown": "A paragraph the edit adds."}},
			}, "alice", http.StatusOK)
		},
		Counts: newBlockCounted,
	}, {
		Name: "api/comment_create.go#createCommentFor, on an issue", Database: r.database,
		Sites: comment("api/comment_create.go#createCommentFor"),
		Drive: func(t *testing.T) {
			r.comment = decoded[model.Comment](t, r.request(t, http.MethodPost, "/api/v1/issues/"+r.issue+"/comments",
				map[string]any{"body": "A remark on the issue."}, "alice", http.StatusCreated)).ID
		},
		// The comment, the first alice writes, and alice: her node is made by the write that first
		// records her.
		Counts: map[graphcensus.StoredKind][]string{
			{graphstore.KindComment, graphstore.AttributeVisibility}: {"comment"},
			{graphstore.KindComment, graphstore.AttributeState}:      {"comment"},
			{graphstore.KindComment, graphstore.AttributeProject}:    {"comment"},
			{graphstore.KindComment, graphstore.TupleAuthoredBy}:     {graphstore.TupleAuthoredBy},
			{graphstore.KindPerson, graphstore.AttributeVisibility}:  {graphstore.KindPerson},
			{graphstore.KindPerson, graphstore.AttributeState}:       {graphstore.KindPerson},
		},
	}, {
		// A comment anchored to the notes, whose mention resolves to the held session: the
		// listener holds that send, so what the comment's create appends is read before its settle
		// runs.
		Name: "api/comment_create.go#createCommentFor, anchored to a document, mentioning a session", Database: r.database,
		Sites: comment("api/comment_create.go#createCommentFor"),
		Drive: func(t *testing.T) {
			r.mentioned = decoded[model.Comment](t, r.request(t, http.MethodPost, "/api/v1/issues/"+r.issue+"/comments", map[string]any{
				"body": "Does this ship, @" + heldSession + "?", "mentions": []map[string]any{{"target": "session:" + heldSession}},
				"anchor": map[string]any{"artifact": r.notesSlug, "quote": "the upload indexes"},
			}, "alice", http.StatusCreated)).ID
			r.held = r.mentioned
			select {
			case <-r.arrived:
			case <-time.After(30 * time.Second):
				t.Fatalf("the mention of %s sent nothing to the listener within 30s", heldSession)
			}
		},
		Counts: map[graphcensus.StoredKind][]string{
			{graphstore.KindComment, graphstore.AttributeVisibility}: {"comment"},
			{graphstore.KindComment, graphstore.AttributeState}:      {"comment"},
			{graphstore.KindComment, graphstore.AttributeProject}:    {"comment"},
			{graphstore.KindComment, graphstore.TupleAuthoredBy}:     {graphstore.TupleAuthoredBy},
			{graphstore.KindComment, graphstore.TupleAnchoredTo}:     {graphstore.TupleAnchoredTo},
		},
	}, {
		Name: "api/comment_delivery.go#completeCommentDelivery", Database: r.database,
		Sites: []string{"api/comment_delivery.go#completeCommentDelivery", "graphstore/people.go#QueueReached",
			"graphstore/people.go#QueueSessionRecorded"},
		Drive: func(t *testing.T) {
			r.release()
			r.settledAttempts(t, r.mentioned, model.DeliveryStateSent)
		},
		Counts: sessionNodeCounted,
	}, {
		// The reply answers an attempt the listener refused, which the answer flips to sent: a
		// delivery its session is owed a node for, and the one write that states it delivered.
		Name: "api/comment_delivery.go#replyComment", Database: r.database,
		Sites: comment("api/comment_delivery.go#replyComment",
			"api/comment_delivery.go#queueAnsweredDelivery", "graphstore/people.go#QueueSessionRecorded"),
		Ready: func(t *testing.T) {
			refused := decoded[model.Comment](t, r.request(t, http.MethodPost, "/api/v1/issues/"+r.issue+"/comments", map[string]any{
				"body": "Can you take this, @" + refusedSession + "?", "mentions": []map[string]any{{"target": "session:" + refusedSession}},
			}, "alice", http.StatusCreated)).ID
			r.settledAttempts(t, refused, "failed")
			r.mentioned = refused
		},
		Drive: func(t *testing.T) {
			r.request(t, http.MethodPost, "/api/v1/comments/"+r.mentioned+"/reply", map[string]any{
				"actor": sessionActor(refusedSession), "attempt": 1, "body": "Yes, I take it.",
			}, "", http.StatusCreated)
		},
		// The reply, its author's node, the first write that records the session, and the
		// delivered_to of the attempt the reply makes a delivery, which the term delivery counts
		// once for each attempt the listener took or an answer flipped to sent.
		Counts: map[graphcensus.StoredKind][]string{
			{graphstore.KindComment, graphstore.AttributeVisibility}: {"comment"},
			{graphstore.KindComment, graphstore.AttributeState}:      {"comment"},
			{graphstore.KindComment, graphstore.AttributeProject}:    {"comment"},
			{graphstore.KindComment, graphstore.TupleAuthoredBy}:     {graphstore.TupleAuthoredBy},
			{graphstore.KindComment, graphstore.TupleDeliveredTo}:    {"delivery"},
			{graphstore.KindSession, graphstore.AttributeVisibility}: {graphstore.KindSession},
			{graphstore.KindSession, graphstore.AttributeState}:      {graphstore.KindSession},
		},
	}, {
		// A Retry of the refused mention: its second attempt, which the listener refuses too, so
		// it settles failed once the take-over window has passed.
		Name: "api/comment_delivery.go#openCommentDeliveryAttempt, refused again", Database: r.database,
		Sites: []string{"api/comment_delivery.go#openCommentDeliveryAttempt"},
		Drive: func(t *testing.T) {
			r.request(t, http.MethodPost, "/api/v1/comments/"+r.mentioned+"/deliveries",
				map[string]any{"delivery": "btw"}, "alice", http.StatusCreated)
			r.awaitTrue(t, "comment "+r.mentioned+"'s second attempt settling failed", `
				select exists (select 1 from comment_deliveries where comment_id = $1::uuid and attempt = 2 and state = 'failed')
			`, r.mentioned)
		},
		Counts: map[graphcensus.StoredKind][]string{},
	}, {
		// The session answers that second attempt with a reply naming the first block of the
		// comment it answers.
		Name: "api/comment_delivery.go#replyComment, naming a block", Database: r.database,
		Sites: comment("api/comment_delivery.go#replyComment", "api/comment_delivery.go#queueAnsweredDelivery",
			"refs/reply_blocks.go#Queue"),
		Drive: func(t *testing.T) {
			r.request(t, http.MethodPost, "/api/v1/comments/"+r.mentioned+"/reply", map[string]any{
				"actor": sessionActor(refusedSession), "attempt": 2, "body": "Taken, as that block asks.", "reply_to_block": "b1",
			}, "", http.StatusCreated)
		},
		Counts: map[graphcensus.StoredKind][]string{},
	}, {
		// A message the request stores on the issue, targeted at a session the listener takes the
		// send of at once: the message's node, in the issue's project, and the session's node the
		// settle of its attempt records.
		Name: "api/message_delivery.go#completeMessageDelivery", Database: r.database,
		Sites: []string{"api/message_delivery.go#completeMessageDelivery", "api/messages.go#createStoredMessage",
			"graphstore/people.go#QueueReached", "graphstore/people.go#QueueSessionRecorded"},
		Drive: func(t *testing.T) {
			r.message = decoded[model.Message](t, r.request(t, http.MethodPost, "/api/v1/issues/"+r.issue+"/messages", map[string]any{
				"body": "Over to you.", "target": "session:" + messageSession, "delivery": "steer",
			}, "alice", http.StatusCreated)).ID
			r.awaitTrue(t, "message "+r.message+"'s attempt settling sent", `
				select not exists (select 1 from message_deliveries where message_id = $1::uuid and state = 'pending')
				   and exists (select 1 from message_deliveries where message_id = $1::uuid and state = 'sent')
			`, r.message)
		},
		Counts: merged(sessionNodeCounted, objectNodeCounted(graphstore.KindMessage)),
	}, {
		// alice's direct message to the message's session from its Agents page, which the session
		// takes as her own turn within the minute it may: the attempt's accepted_at and
		// accepted_as.
		Name: "api/message_delivery.go#acceptDelivery", Database: r.database,
		Sites: []string{"api/message_delivery.go#acceptDelivery"},
		Ready: func(t *testing.T) {
			direct = decoded[model.Message](t, r.request(t, http.MethodPost, "/api/v1/agents/"+messageSession+"/messages",
				map[string]any{"body": "Carry on.", "delivery": "steer"}, "alice", http.StatusCreated)).ID
			r.awaitTrue(t, "direct message "+direct+"'s attempt settling sent", `
				select exists (select 1 from message_deliveries where message_id = $1::uuid and attempt = 1 and state = 'sent')
			`, direct)
		},
		Drive: func(t *testing.T) {
			r.request(t, http.MethodPost, "/api/v1/messages/"+direct+"/deliveries/1/accept",
				map[string]any{"actor": sessionActor(messageSession)}, "", http.StatusOK)
		},
		Counts: map[graphcensus.StoredKind][]string{},
	}, {
		// The session's reply to that message, a message of the same issue's project.
		Name: "api/message_delivery.go#insertSessionReply", Database: r.database,
		Sites: []string{"api/message_delivery.go#insertSessionReply", "api/message_delivery.go#replyMessage"},
		Drive: func(t *testing.T) {
			r.request(t, http.MethodPost, "/api/v1/messages/"+r.message+"/reply", map[string]any{
				"actor": sessionActor(messageSession), "attempt": 1, "body": "On it.",
			}, "", http.StatusCreated)
		},
		Counts: objectNodeCounted(graphstore.KindMessage),
	}, {
		// A broadcast to the message's session: the copy it stores for that session, a message on
		// no issue, between alice and the session, whose node names those parties.
		Name: "api/broadcasts.go#queueBroadcastCopy", Database: r.database,
		Sites: []string{"api/broadcasts.go#queueBroadcastCopy", "api/broadcasts.go#queue", "api/broadcasts.go#queueCause",
			"inboxwrite/record.go#Queue", "refs/item_blocks.go#Queue"},
		Drive: func(t *testing.T) {
			r.request(t, http.MethodPost, "/api/v1/broadcasts", map[string]any{
				"body": "To everyone.", "delivery": "btw", "session_ids": []string{messageSession},
			}, "alice", http.StatusCreated)
		},
		Counts: map[graphcensus.StoredKind][]string{
			{graphstore.KindMessage, graphstore.AttributeVisibility}: {graphstore.KindMessage},
			{graphstore.KindMessage, graphstore.AttributeState}:      {graphstore.KindMessage},
			{graphstore.KindMessage, graphstore.AttributeParties}:    {graphstore.KindMessage},
		},
	}, {
		// A session's ask on the issue: the ask's node, in the issue's project, who asked it, and
		// the asker's follow, planned in the create's read phase (asks.PlanFollowNewAsk).
		Name: "api/asks.go#createAskFor", Database: r.database,
		Sites: []string{"api/asks.go#createAskFor", "asks/followers.go#Queue", "asks/followers.go#queueFollow"},
		Drive: func(t *testing.T) {
			r.ask = decoded[model.Ask](t, r.request(t, http.MethodPost, "/api/v1/issues/"+r.issue+"/asks", map[string]any{
				"question": "Which store do the writers take?", "actor": sessionActor("ses_asker"),
			}, "", http.StatusCreated)).ID
		},
		Counts: merged(sessionNodeCounted, objectNodeCounted(graphstore.KindAsk), askAuthorCounted, followCounted),
	}, {
		// The removal of an untouched document of the issue, made just before by typing a title:
		// its node stated gone, which the term artifact_removed counts.
		Name: "api/artifact_removal.go#removeArtifact", Database: r.database,
		Sites: []string{"api/artifact_removal.go#removeArtifact"},
		Ready: func(t *testing.T) {
			r.removable = decoded[struct {
				Artifact model.Artifact `json:"artifact"`
			}](t, r.request(t, http.MethodPost, "/api/v1/issues/"+r.issue+"/artifacts",
				map[string]any{"name": "scratch.md"}, "alice", http.StatusCreated)).Artifact.ID
		},
		Drive: func(t *testing.T) {
			r.request(t, http.MethodDelete, "/api/v1/artifacts/"+r.removable, nil, "alice", http.StatusNoContent)
		},
		Counts: map[graphcensus.StoredKind][]string{{graphstore.KindArtifact, graphstore.AttributeState}: {"artifact_removed"}},
	}, {
		// The follower route's follow of that ask (asks.Follow), whose node it moves onto the
		// event it appends after it.
		Name: "api/server.go#appendCausingEvent", Database: r.database,
		Sites: []string{"api/server.go#appendCausingEvent", "asks/followers.go#Follow", "asks/followers.go#queueFollow"},
		Drive: func(t *testing.T) {
			r.request(t, http.MethodPut, "/api/v1/asks/"+r.ask+"/followers/ses_follower",
				map[string]any{"actor": sessionActor("ses_follower")}, "", http.StatusNoContent)
		},
		Counts: merged(sessionNodeCounted, followCounted),
	}, {
		// The spec's approval request, which a session opens: the request's node, in the spec's
		// project, and the requester's follow.
		Name: "api/reviews.go#openApprovalAsk", Database: r.database,
		Sites: []string{"api/reviews.go#openApprovalAsk", "asks/followers.go#Queue", "asks/followers.go#queueFollow"},
		Drive: func(t *testing.T) {
			r.request(t, http.MethodPost, "/api/v1/artifacts/"+r.spec+"/approval-requests",
				map[string]any{"actor": sessionActor("ses_requester")}, "", http.StatusCreated)
		},
		Counts: merged(sessionNodeCounted, objectNodeCounted(graphstore.KindAsk), askAuthorCounted, followCounted),
	}, {
		// That request renewed by another session while it waits on the person: a retry, which
		// appends no event.
		Name: "api/reviews.go#renewApprovalAsk, on a retry", Database: r.database,
		Sites: []string{"api/reviews.go#renewApprovalAsk", "asks/followers.go#Queue", "asks/followers.go#queueFollow"},
		Drive: func(t *testing.T) {
			r.request(t, http.MethodPost, "/api/v1/artifacts/"+r.spec+"/approval-requests",
				map[string]any{"actor": sessionActor("ses_retry")}, "", http.StatusOK)
		},
		Counts: merged(sessionNodeCounted, followCounted),
	}, {
		// A new version of the spec moves the request to its agent, and a session asking for the
		// approval again hands it back to the person.
		Name: "api/reviews.go#renewApprovalAsk, handing the request back", Database: r.database,
		Sites: []string{"api/reviews.go#renewApprovalAsk", "asks/followers.go#Queue", "asks/followers.go#queueFollow"},
		Ready: func(t *testing.T) {
			r.request(t, http.MethodPost, "/api/v1/issues/"+r.issue+"/artifacts", map[string]any{
				"name": "spec.md", "content": "# Spec\n\nWhat the writers write, read once more.\n",
			}, "alice", http.StatusCreated)
		},
		Drive: func(t *testing.T) {
			r.request(t, http.MethodPost, "/api/v1/artifacts/"+r.spec+"/approval-requests",
				map[string]any{"actor": sessionActor("ses_handback")}, "", http.StatusCreated)
		},
		Counts: merged(sessionNodeCounted, followCounted),
	}, {
		// A session writes an ask as a block of the notes through the edits route, which writes
		// the version holding it, and the settlement opens the ask, in the notes and part of them,
		// and follows it for that session.
		Name: "docs/settlement.go#appendEvents", Database: r.database,
		Sites: []string{"docs/settlement.go#appendEvents", "docs/ask_blocks.go#createAskBlock", "asks/followers.go#Queue",
			"asks/followers.go#queueFollow"},
		Ready: func(t *testing.T) {
			r.request(t, http.MethodPost, "/api/v1/artifacts/"+r.notes+"/edits", map[string]any{
				"actor": sessionActor("ses_editor"),
				"ops":   []map[string]any{{"op": "insert", "after": "end", "markdown": ":::ask{#sizing-ask}\nWhich store do the writers take?\n:::"}},
			}, "", http.StatusOK)
		},
		Drive: func(t *testing.T) {
			r.documents.SettleNow(r.notes)
			r.awaitTrue(t, "the settlement opening the notes' ask block", `
				select exists (select 1 from asks where block_artifact_id = $1::uuid and block_id = 'sizing-ask')
			`, r.notes)
		},
		Counts: merged(sessionNodeCounted, objectNodeCounted(graphstore.KindAsk), askAuthorCounted, followCounted,
			map[graphcensus.StoredKind][]string{
				{graphstore.KindAsk, graphstore.TupleAnchoredTo}: {"ask_opened"},
				{graphstore.KindAsk, graphstore.TuplePartOf}:     {"ask_opened"},
			}),
	}, {
		// A person's live edit of the notes, which a build without the block index then wrote as a
		// version of its own: the version's insert owes the document its index (migration 0122's
		// trigger), and the tick's pass indexes it. A tick first runs through whatever the drives
		// before left owed, so the one that is read indexes this document alone.
		Name: "docs/block_index.go#indexOwedDocument", Database: r.database,
		Sites: []string{"docs/block_index.go#indexOwedDocument", "refs/blocks.go#Queue"},
		Ready: func(t *testing.T) {
			if _, err := r.documents.ReconcileBlockIndex(ctx, docs.BlockIndexCursors{}, nil); err != nil {
				t.Fatalf("run the block-index tick over what the drives before left owed: %v", err)
			}
			tx, err := r.database.Pool.Begin(ctx)
			if err != nil {
				t.Fatalf("begin the live edit: %v", err)
			}
			defer tx.Rollback(ctx)
			joined, ledger := r.documents.Join(ctx, tx)
			defer ledger.Discard()
			if edit, err := r.documents.ApplyOps(joined, r.notes, []model.EditOp{{
				Op: "insert", After: "end", Markdown: "A paragraph a live edit adds.",
			}}, model.Actor{Kind: "user", ID: "alice"}, nil); err != nil || !edit.Changed {
				t.Fatalf("edit the notes live (changed %v): %v", edit.Changed, err)
			}
			if err := ledger.Commit(ctx); err != nil {
				t.Fatalf("commit the live edit: %v", err)
			}
			live, _, err := r.documents.TextWithBlocks(ctx, r.notes)
			if err != nil {
				t.Fatalf("read the notes' live text: %v", err)
			}
			if _, err := r.database.Pool.Exec(ctx, `
				insert into artifact_versions (artifact_id, number, markdown, authors)
				select $1::uuid, max(number) + 1, $2, '[{"kind":"user","id":"alice"}]' from artifact_versions where artifact_id = $1::uuid
			`, r.notes, live); err != nil {
				t.Fatalf("write the live edit as a version a build without the block index writes: %v", err)
			}
		},
		Drive: func(t *testing.T) {
			tick, err := r.documents.ReconcileBlockIndex(ctx, docs.BlockIndexCursors{}, nil)
			if err != nil {
				t.Fatalf("run the block-index tick: %v", err)
			}
			if tick.Documents != 1 {
				t.Fatalf("the tick indexed %d document(s) (%d live ahead), want the notes alone", tick.Documents, tick.LiveAhead)
			}
		},
		Counts: newBlockCounted,
	}, {
		// The edit of the issue's comment: its body's blocks indexed again, as a version of it.
		Name: "api/comment_actions.go#editComment", Database: r.database,
		Sites: []string{"api/comment_actions.go#editComment", "refs/item_blocks.go#Queue"},
		Drive: func(t *testing.T) {
			r.request(t, http.MethodPatch, "/api/v1/comments/"+r.comment,
				map[string]any{"body": "A remark on the issue, put plainly."}, "alice", http.StatusOK)
		},
		Counts: map[graphcensus.StoredKind][]string{},
	}, {
		// The asker's edit of the session's ask: its question's blocks indexed again.
		Name: "api/asks.go#editAsk", Database: r.database,
		Sites: []string{"api/asks.go#editAsk", "refs/item_blocks.go#Queue"},
		Drive: func(t *testing.T) {
			r.request(t, http.MethodPatch, "/api/v1/asks/"+r.ask, map[string]any{
				"question": "Which store do the writers write to?", "actor": sessionActor("ses_asker"),
			}, "", http.StatusOK)
		},
		Counts: map[graphcensus.StoredKind][]string{},
	}, {
		// alice snoozes the session's ask for an hour: her snoozed, from her node.
		Name: "api/ask_snooze.go#announceAskSnooze", Database: r.database,
		Sites: []string{"api/ask_snooze.go#announceAskSnooze"},
		Drive: func(t *testing.T) {
			r.request(t, http.MethodPut, "/api/v1/me/asks/"+r.ask+"/snooze", map[string]any{
				"snoozed_until": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			}, "alice", http.StatusOK)
		},
		Counts: map[graphcensus.StoredKind][]string{},
	}, {
		// alice reads the thread of her comment on the issue through now: her read_through.
		Name: "api/replies.go#putThreadRead", Database: r.database,
		Sites: []string{"api/replies.go#putThreadRead"},
		Drive: func(t *testing.T) {
			r.request(t, http.MethodPut, "/api/v1/me/threads/"+r.comment+"/read", map[string]any{
				"read_through": time.Now().UTC().Format(time.RFC3339Nano),
			}, "alice", http.StatusNoContent)
		},
		Counts: map[graphcensus.StoredKind][]string{},
	}, {
		// alice clears her conversation with the message's session: her cleared, from her node.
		Name: "api/state.go#putUserAgentState", Database: r.database,
		Sites: []string{"api/state.go#putUserAgentState"},
		Drive: func(t *testing.T) {
			r.request(t, http.MethodPut, "/api/v1/me/agents/"+messageSession+"/state", map[string]any{
				"cleared_before": time.Now().UTC().Format(time.RFC3339Nano),
			}, "alice", http.StatusOK)
		},
		Counts: map[graphcensus.StoredKind][]string{},
	}, {
		// alice marks her whole inbox read: the read_through of every thread a reply moved.
		Name: "api/inbox_read.go#commitInboxMarks", Database: r.database,
		Sites: []string{"api/inbox_read.go#commitInboxMarks"},
		Drive: func(t *testing.T) {
			r.request(t, http.MethodPost, "/api/v1/me/inbox/read", map[string]any{}, "alice", http.StatusOK)
		},
		Counts: map[graphcensus.StoredKind][]string{},
	}, {
		// A Retry of the mention the held send delivered: a second attempt, opened by the route
		// and sent at once now that the listener lets that session's sends through.
		Name: "api/comment_delivery.go#openCommentDeliveryAttempt", Database: r.database,
		Sites: []string{"api/comment_delivery.go#openCommentDeliveryAttempt"},
		Drive: func(t *testing.T) {
			r.request(t, http.MethodPost, "/api/v1/comments/"+r.held+"/deliveries",
				map[string]any{"delivery": "btw"}, "alice", http.StatusCreated)
			r.settledAttempts(t, r.held, model.DeliveryStateSent)
		},
		Counts: map[graphcensus.StoredKind][]string{},
	}, {
		// A keyed repeat of a comment whose process died after it committed and before its
		// mention's one attempt was sent: the repeat claims that attempt (claimQueuedMention) and
		// puts it back on the mention sender's lane, which sends it.
		Name: "api/comment_mention_sender.go#claimQueuedMention", Database: r.database,
		Sites: []string{"api/comment_mention_sender.go#claimQueuedMention"},
		Ready: func(t *testing.T) {
			replayed = decoded[model.Comment](t, r.keyedRequest(t, http.MethodPost, "/api/v1/issues/"+r.issue+"/comments", replayedBody,
				"alice", replayedKey, http.StatusCreated)).ID
			r.settledAttempts(t, replayed, model.DeliveryStateSent)
			if _, err := r.database.Pool.Exec(ctx, `
				update comment_deliveries set state = 'pending', claimed_at = null, envelope_id = null where comment_id = $1::uuid
			`, replayed); err != nil {
				t.Fatalf("leave the comment's attempt as a process that died before its send leaves it: %v", err)
			}
		},
		Drive: func(t *testing.T) {
			r.keyedRequest(t, http.MethodPost, "/api/v1/issues/"+r.issue+"/comments", replayedBody, "alice", replayedKey, http.StatusCreated)
			r.settledAttempts(t, replayed, model.DeliveryStateSent)
		},
		Counts: map[graphcensus.StoredKind][]string{},
	}, {
		// The delivery sweep's settle of the held mention's first attempt as superseded: a lane
		// that stood down from it left it pending and unclaimed while the Retry's attempt
		// delivered the comment in its place.
		Name: "api/comment_delivery.go#supersedeCommentDelivery", Database: r.database,
		Sites: []string{"api/comment_delivery.go#supersedeCommentDelivery"},
		Ready: func(t *testing.T) {
			if _, err := r.database.Pool.Exec(ctx, `
				update comment_deliveries set state = 'pending', claimed_at = null where comment_id = $1::uuid and attempt = 1
			`, r.held); err != nil {
				t.Fatalf("leave the held mention's first attempt as a lane that stood down leaves it: %v", err)
			}
		},
		Drive: func(t *testing.T) {
			if err := r.resume(ctx); err != nil {
				t.Fatalf("run a pass of the delivery sweep: %v", err)
			}
			r.settledAttempts(t, r.held, model.DeliveryStateFailed)
		},
		Counts: map[graphcensus.StoredKind][]string{},
	}, {
		// The delivery sweep's settle of an owed attempt whose comment no longer holds its
		// mention: the refused mention's second attempt, left pending and unclaimed past the
		// claim's lease, after its mention row was taken off the comment.
		Name: "api/delivery_sweep.go#settleUndeliverableMention", Database: r.database,
		Sites: []string{"api/delivery_sweep.go#settleUndeliverableMention"},
		Ready: func(t *testing.T) {
			if _, err := r.database.Pool.Exec(ctx, `
				update comment_deliveries set state = 'pending', claimed_at = null, created_at = now() - interval '2 minutes'
				where comment_id = $1::uuid and attempt = 2;
			`, r.mentioned); err != nil {
				t.Fatalf("leave the refused mention's second attempt owed: %v", err)
			}
			if _, err := r.database.Pool.Exec(ctx, `delete from comment_mentions where comment_id = $1::uuid`, r.mentioned); err != nil {
				t.Fatalf("take the mention off the comment: %v", err)
			}
		},
		Drive: func(t *testing.T) {
			if err := r.resume(ctx); err != nil {
				t.Fatalf("run a pass of the delivery sweep: %v", err)
			}
			r.awaitTrue(t, "the owed attempt settling failed", `
				select exists (select 1 from comment_deliveries where comment_id = $1::uuid and attempt = 2 and state = 'failed')
			`, r.mentioned)
		},
		Counts: map[graphcensus.StoredKind][]string{},
	}, {
		// A Retry of the targeted message: its second attempt, sent at once.
		Name: "api/message_delivery.go#insertMessageDeliveryAttempt", Database: r.database,
		Sites: []string{"api/message_delivery.go#insertMessageDeliveryAttempt"},
		Drive: func(t *testing.T) {
			r.request(t, http.MethodPost, "/api/v1/messages/"+r.message+"/deliveries",
				map[string]any{"delivery": "steer"}, "alice", http.StatusCreated)
			r.awaitTrue(t, "message "+r.message+"'s second attempt settling sent", `
				select exists (select 1 from message_deliveries where message_id = $1::uuid and attempt = 2 and state = 'sent')
			`, r.message)
		},
		Counts: map[graphcensus.StoredKind][]string{},
	}, {
		// The erase of the issue's comment, which deletes its tuples and appends none.
		Name: "grapherase/erase.go#apply", Database: r.database,
		Sites: []string{"grapherase/erase.go#apply"},
		Drive: func(t *testing.T) {
			url := r.database.Pool.Config().ConnString()
			listing, err := grapherase.Erase(ctx, url, "comment", r.comment, "", 10*time.Millisecond)
			if err != nil {
				t.Fatalf("list the erase of comment %s: %v", r.comment, err)
			}
			if len(listing.Blockers) > 0 {
				t.Fatalf("the erase of comment %s is blocked by %v", r.comment, listing.Blockers)
			}
			erased, err := grapherase.Erase(ctx, url, "comment", r.comment, listing.Digest, 10*time.Millisecond)
			if err != nil || !erased.Applied {
				t.Fatalf("erase comment %s (applied %v): %v", r.comment, erased != nil && erased.Applied, err)
			}
		},
		Counts: map[graphcensus.StoredKind][]string{},
	}}, askDrives(t, r)...)
}

// The terms that count what several drives here append: a block a version states afresh, with its
// two base attributes, its in_document and its part_of, which the term block counts; a session's
// node with its two base attributes, made by the first write that records the session, which the
// term session counts; who asked an ask, which the term ask_opened counts beside the ask's other
// facts its create states; and a session's follow of an ask, which the term ask_follower counts.
var (
	newBlockCounted = map[graphcensus.StoredKind][]string{
		{refs.BlockNodeKind, graphstore.AttributeVisibility}: {"block"},
		{refs.BlockNodeKind, graphstore.AttributeState}:      {"block"},
		{refs.BlockNodeKind, graphstore.AttributeProject}:    {"block"},
		{refs.BlockNodeKind, refs.TupleInDocument}:           {"block"},
		{refs.BlockNodeKind, graphstore.TuplePartOf}:         {"block"},
	}
	sessionNodeCounted = map[graphcensus.StoredKind][]string{
		{graphstore.KindSession, graphstore.AttributeVisibility}: {graphstore.KindSession},
		{graphstore.KindSession, graphstore.AttributeState}:      {graphstore.KindSession},
	}
	askAuthorCounted = map[graphcensus.StoredKind][]string{
		{graphstore.KindAsk, graphstore.TupleAuthoredBy}: {"ask_opened"},
	}
	followCounted = map[graphcensus.StoredKind][]string{
		{graphstore.KindAsk, graphstore.TupleFollowedBy}: {"ask_follower"},
	}
	// What a person's answer of an ask states on its node, which the term answer counts, and what
	// a resolution states, which the term ask_resolved counts.
	answerCounted = map[graphcensus.StoredKind][]string{
		{graphstore.KindAsk, graphstore.TupleAnswerState}: {"answer"},
		{graphstore.KindAsk, graphstore.TupleAnswer}:      {"answer"},
		{graphstore.KindAsk, graphstore.TupleSelected}:    {"answer"},
		{graphstore.KindAsk, graphstore.TupleAnsweredAt}:  {"answer"},
		{graphstore.KindAsk, graphstore.TupleAnsweredBy}:  {"answer"},
		{graphstore.KindAsk, graphstore.TupleDecidedBy}:   {"answer"},
	}
	resolutionCounted = map[graphcensus.StoredKind][]string{
		{graphstore.KindAsk, graphstore.TupleAnswerState}: {"ask_resolved"},
		{graphstore.KindAsk, graphstore.TupleResolution}:  {"ask_resolved"},
	}
)

// objectNodeCounted is what a create of an issue, a document, an ask or a message of a project
// appends of the object's node: its visibility, its state and its project, which the term of its
// kind counts.
func objectNodeCounted(kind string) map[graphcensus.StoredKind][]string {
	return map[graphcensus.StoredKind][]string{
		{kind, graphstore.AttributeVisibility}: {kind},
		{kind, graphstore.AttributeState}:      {kind},
		{kind, graphstore.AttributeProject}:    {kind},
	}
}

// merged is every kind each of counts holds, its terms joined where two hold one kind.
func merged(counts ...map[graphcensus.StoredKind][]string) map[graphcensus.StoredKind][]string {
	joined := map[graphcensus.StoredKind][]string{}
	for _, count := range counts {
		for kind, terms := range count {
			for _, term := range terms {
				if !slices.Contains(joined[kind], term) {
					joined[kind] = append(joined[kind], term)
				}
			}
		}
	}
	return joined
}
