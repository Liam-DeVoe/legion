//go:build basebuild

// The basebuild tag, and how a test of it is run: store_census_test.go. This file's tests read the
// base release at $TMPDIR/base-dispatch (baseRelease), built from BASELINE_SHA, and are run as
//
//	go test -tags basebuild -timeout 30m ./cmd/dispatch -run '^(TestAReassignmentThePreviousBuildWritesMovesTheAsksDeliveryAndParticipation|TestAPersonMentionThePreviousBuildWritesReachesThatPerson)$' -count=1 -v

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/cmdtest"
	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
)

// rollCatchUpWait is how long a test of this file waits, once the base release has stopped and the
// backfill has run, for this head's block-index tick (docs.BlockIndexReconcileInterval, 30 s) to
// catch up what the base release wrote: three ticks.
const rollCatchUpWait = 100 * time.Second

// startRoll starts the base release alone on a database of the run's own, waits until it has
// announced its build, then starts this head beside it, as a rolling deploy has the two serve one
// database. It answers both processes and a connection to that database.
func startRoll(t *testing.T, ctx context.Context) (previous, current *dispatchProcess, database *pgx.Conn, head, databaseURL string) {
	t.Helper()
	databaseURL = censusDatabase(t)
	base := baseRelease(t)
	head = cmdtest.BuildStamped(t, "envoy-dispatch", headBuildStamp)

	previous = startDispatchProcessWith(t, databaseURL, dispatchOptions{Binary: base, SigningKey: previousBuildSigningKey})
	previous.waitHealthy(t)
	database, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect to the run's database: %v", err)
	}
	t.Cleanup(func() { database.Close(ctx) })
	var baseCommit string
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if err := database.QueryRow(ctx, `select coalesce((select commit from server_build_announcements order by seq desc limit 1), '')`).Scan(&baseCommit); err != nil {
			t.Fatalf("read the base release's announcement: %v", err)
		}
		if baseCommit != "" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if baseCommit == "" || baseCommit == headBuildStamp {
		t.Fatalf("the base release announced the build %q, want its own stamp, which is not this head's %q", baseCommit, headBuildStamp)
	}
	current = startDispatchProcessWith(t, databaseURL, dispatchOptions{Binary: head, SigningKey: previousBuildSigningKey})
	current.waitHealthy(t)
	return previous, current, database, head, databaseURL
}

// endRoll stops the base release as a deploy does, then runs this head's backfill
// (`envoy-dispatch backfill-graph-store`), so that both of this build's ways of giving a row the
// base release wrote its tuples - the backfill and the block-index tick's catch-up - have run or
// can run before the test reads the store.
func endRoll(t *testing.T, previous *dispatchProcess, head, databaseURL string) {
	t.Helper()
	previous.Terminate(t)
	previous.WaitExit(t, "SIGTERM ended the roll")
	if code, out := runDispatchCommand(t, head, databaseURL, "backfill-graph-store"); code != 0 {
		t.Fatalf("backfill-graph-store exited %d:\n%s", code, out)
	}
}

// heldTuple is whether the newest statement of the tuple (fromKind/fromID -kind-> toKind/toID)
// asserts it.
func heldTuple(t *testing.T, ctx context.Context, database *pgx.Conn, fromKind, fromID, kind, toKind, toID string) bool {
	t.Helper()
	var held bool
	if err := database.QueryRow(ctx, `
		select coalesce((
			select asserted from graph_tuples
			where from_kind = $1 and from_id = $2 and kind = $3 and to_kind = $4 and to_id = $5
			order by seq desc limit 1), false)
	`, fromKind, fromID, kind, toKind, toID).Scan(&held); err != nil {
		t.Fatalf("read %s/%s %s %s/%s: %v", fromKind, fromID, kind, toKind, toID, err)
	}
	return held
}

// openAgentAsk opens an ask on issue as an agent's session, so no person wrote it, and answers its
// id.
func openAgentAsk(t *testing.T, server *dispatchProcess, issue, question string) string {
	t.Helper()
	opened := asAgentSession(t, server, http.MethodPost, "/api/v1/issues/"+issue+"/asks", map[string]any{
		"question": question,
		"options":  []map[string]string{{"label": "Yes"}, {"label": "No"}},
	}, http.StatusCreated)
	var ask struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(opened.body, &ask); err != nil || ask.ID == "" {
		t.Fatalf("read the ask opened on %s: %v: %s", issue, err, opened.body)
	}
	return ask.ID
}

// R2: "Three writes move the person an ask waits on: patchIssue, putAskAskedOf and
// putProjectDefaultAssignee. For each ask it moves, the write appends a delivered_to tuple and a
// participates_in tuple to the new person. It appends a retraction of each for the old person ...
// So the list follows the assignment." R9: the rows a release before this one writes take their
// tuples once, from the backfill or the block-index tick's catch-up.
//
// An issue's assignee that the base release moves while it serves beside this head is such a
// write, and the ask the issue holds then waits on the new person. Once the roll ends and the
// backfill has run, the ask's delivery and participation follow the move, as they do when this
// head moves an assignee itself: the new person holds the delivered_to and the participates_in,
// and the old one, who took no other part in the ask, holds neither. The test first moves a
// second issue through this head, which is the expected result the base release's move is held
// to.
//
// R2 names two more writes that move the person an ask waits on, and each the base release writes
// beside this head is held to the same result: a hand-on of the ask to bob (putAskAskedOf) and a
// move of its project's default assignee to bob (putProjectDefaultAssignee), each first written
// through this head as its expected result. After either, bob holds the ask's delivered_to and its
// participates_in, and alice, whom it waited on before and who took no other part in it, holds
// neither.
func TestAReassignmentThePreviousBuildWritesMovesTheAsksDeliveryAndParticipation(t *testing.T) {
	ctx := context.Background()
	previous, current, database, head, databaseURL := startRoll(t, ctx)
	// bob is a person who has signed in, as an assignee must be (allowedLogin): the row sign-in
	// records (store/people.go).
	if _, err := database.Exec(ctx, `insert into people (email) values ('bob') on conflict (email) do nothing`); err != nil {
		t.Fatalf("record bob's sign-in: %v", err)
	}

	current.call(t, http.MethodPost, "/api/v1/projects", `{"key":"MOVE","name":"A move during a roll"}`, http.StatusCreated)
	moved := map[string]string{}
	for _, by := range []string{"head", "base"} {
		issue, _ := createCensusIssue(t, current, "MOVE", "An ask the "+by+" moves", "# Spec\n\nThe spec.\n")
		current.call(t, http.MethodPatch, "/api/v1/issues/"+issue, `{"assignee":"alice"}`, http.StatusOK)
		ask := openAgentAsk(t, current, issue, "Move it, "+by+"?")
		if !heldTuple(t, ctx, database, "ask", ask, graphstore.TupleDeliveredTo, graphstore.KindPerson, "alice") ||
			!heldTuple(t, ctx, database, graphstore.KindPerson, "alice", graphstore.TupleParticipatesIn, "ask", ask) {
			t.Fatalf("this head's ask %s on %s, assigned to alice, holds no delivered_to to alice or no participates_in from her", ask, issue)
		}
		moved[by] = ask
		server := current
		if by == "base" {
			server = previous
		}
		if answer := server.send(t, http.MethodPatch, "/api/v1/issues/"+issue, `{"assignee":"bob"}`, asUser(dispatchTestLogin)); answer.status != http.StatusOK {
			t.Fatalf("the %s moves %s to bob: %s", by, issue, answer)
		}
	}
	handed := map[string]string{}
	for _, by := range []string{"head", "base"} {
		issue, _ := createCensusIssue(t, current, "MOVE", "An ask the "+by+" hands on", "# Spec\n\nThe spec.\n")
		current.call(t, http.MethodPatch, "/api/v1/issues/"+issue, `{"assignee":"alice"}`, http.StatusOK)
		ask := openAgentAsk(t, current, issue, "Hand it on, "+by+"?")
		if !heldTuple(t, ctx, database, "ask", ask, graphstore.TupleDeliveredTo, graphstore.KindPerson, "alice") ||
			!heldTuple(t, ctx, database, graphstore.KindPerson, "alice", graphstore.TupleParticipatesIn, "ask", ask) {
			t.Fatalf("this head's ask %s on %s, assigned to alice, holds no delivered_to to alice or no participates_in from her", ask, issue)
		}
		handed[by] = ask
		server := current
		if by == "base" {
			server = previous
		}
		if answer := server.send(t, http.MethodPut, "/api/v1/asks/"+ask+"/asked-of", `{"asked_of":"bob"}`, asUser(dispatchTestLogin)); answer.status != http.StatusOK {
			t.Fatalf("the %s hands %s on to bob: %s", by, ask, answer)
		}
	}
	defaulted := map[string]string{}
	for by, project := range map[string]string{"head": "DFLTHEAD", "base": "DFLTBASE"} {
		current.call(t, http.MethodPost, "/api/v1/projects", `{"key":"`+project+`","name":"A default the `+by+` moves","default_assignee":"alice"}`, http.StatusCreated)
		issue, _ := createCensusIssue(t, current, project, "An ask on an issue nobody holds", "# Spec\n\nThe spec.\n")
		current.call(t, http.MethodPatch, "/api/v1/issues/"+issue, `{"assignee":null}`, http.StatusOK)
		ask := openAgentAsk(t, current, issue, "Whose is it, "+by+"?")
		if !heldTuple(t, ctx, database, "ask", ask, graphstore.TupleDeliveredTo, graphstore.KindPerson, "alice") ||
			!heldTuple(t, ctx, database, graphstore.KindPerson, "alice", graphstore.TupleParticipatesIn, "ask", ask) {
			t.Fatalf("this head's ask %s on %s, whose project's default is alice, holds no delivered_to to alice or no participates_in from her", ask, issue)
		}
		defaulted[by] = ask
		server := current
		if by == "base" {
			server = previous
		}
		if answer := server.send(t, http.MethodPut, "/api/v1/projects/"+project+"/default-assignee", `{"assignee":"bob"}`, asUser(dispatchTestLogin)); answer.status != http.StatusOK {
			t.Fatalf("the %s moves %s's default to bob: %s", by, project, answer)
		}
	}
	endRoll(t, previous, head, databaseURL)

	deadline := time.Now().Add(rollCatchUpWait)
	for time.Now().Before(deadline) &&
		!heldTuple(t, ctx, database, "ask", moved["base"], graphstore.TupleDeliveredTo, graphstore.KindPerson, "bob") {
		time.Sleep(time.Second)
	}
	for _, by := range []string{"head", "base"} {
		ask := moved[by]
		if !heldTuple(t, ctx, database, "ask", ask, graphstore.TupleDeliveredTo, graphstore.KindPerson, "bob") {
			t.Errorf("the ask %s whose issue the %s moved to bob holds no delivered_to to bob %s after the roll ended and the backfill ran", ask, by, rollCatchUpWait)
		}
		if !heldTuple(t, ctx, database, graphstore.KindPerson, "bob", graphstore.TupleParticipatesIn, "ask", ask) {
			t.Errorf("bob holds no participates_in into the ask %s whose issue the %s moved to him", ask, by)
		}
		if heldTuple(t, ctx, database, "ask", ask, graphstore.TupleDeliveredTo, graphstore.KindPerson, "alice") {
			t.Errorf("the ask %s whose issue the %s moved from alice to bob still holds its delivered_to to alice", ask, by)
		}
		if heldTuple(t, ctx, database, graphstore.KindPerson, "alice", graphstore.TupleParticipatesIn, "ask", ask) {
			t.Errorf("alice, who took no other part in the ask %s, still participates in it after the %s moved its issue to bob", ask, by)
		}
	}
	deadline = time.Now().Add(rollCatchUpWait)
	for time.Now().Before(deadline) &&
		(!heldTuple(t, ctx, database, "ask", handed["base"], graphstore.TupleDeliveredTo, graphstore.KindPerson, "bob") ||
			!heldTuple(t, ctx, database, "ask", defaulted["base"], graphstore.TupleDeliveredTo, graphstore.KindPerson, "bob")) {
		time.Sleep(time.Second)
	}
	for _, by := range []string{"head", "base"} {
		ask := handed[by]
		if !heldTuple(t, ctx, database, "ask", ask, graphstore.TupleDeliveredTo, graphstore.KindPerson, "bob") {
			t.Errorf("the ask %s the %s handed on to bob holds no delivered_to to bob %s after the roll ended and the backfill ran", ask, by, rollCatchUpWait)
		}
		if !heldTuple(t, ctx, database, graphstore.KindPerson, "bob", graphstore.TupleParticipatesIn, "ask", ask) {
			t.Errorf("bob holds no participates_in into the ask %s the %s handed on to him", ask, by)
		}
		if heldTuple(t, ctx, database, "ask", ask, graphstore.TupleDeliveredTo, graphstore.KindPerson, "alice") {
			t.Errorf("the ask %s the %s handed on from alice to bob still holds its delivered_to to alice", ask, by)
		}
		if heldTuple(t, ctx, database, graphstore.KindPerson, "alice", graphstore.TupleParticipatesIn, "ask", ask) {
			t.Errorf("alice, who took no other part in the ask %s, still participates in it after the %s handed it on to bob", ask, by)
		}
	}
	for _, by := range []string{"head", "base"} {
		ask := defaulted[by]
		if !heldTuple(t, ctx, database, "ask", ask, graphstore.TupleDeliveredTo, graphstore.KindPerson, "bob") {
			t.Errorf("the ask %s whose project's default the %s moved to bob holds no delivered_to to bob %s after the roll ended and the backfill ran", ask, by, rollCatchUpWait)
		}
		if !heldTuple(t, ctx, database, graphstore.KindPerson, "bob", graphstore.TupleParticipatesIn, "ask", ask) {
			t.Errorf("bob holds no participates_in into the ask %s whose project's default the %s moved to him", ask, by)
		}
		if heldTuple(t, ctx, database, "ask", ask, graphstore.TupleDeliveredTo, graphstore.KindPerson, "alice") {
			t.Errorf("the ask %s whose project's default the %s moved from alice to bob still holds its delivered_to to alice", ask, by)
		}
		if heldTuple(t, ctx, database, graphstore.KindPerson, "alice", graphstore.TupleParticipatesIn, "ask", ask) {
			t.Errorf("alice, who took no other part in the ask %s, still participates in it after the %s moved its project's default to bob", ask, by)
		}
	}
}

// R5: "A mention is a tuple ... The mentions tuple runs from the comment, issue message or
// document block that holds it to the identity it points at." R2: delivery reaches "Every person a
// mention of the write points at". R4: participation "holds every thread they were mentioned or
// addressed in without writing in it". R9: the rows a release before this one writes take their
// tuples once, from the backfill or the block-index tick's catch-up.
//
// The base release reads `dispatch://person/<login>` in a comment's body as a person, as this head
// does (text.Ref, key "person"). A comment it writes naming a person while it serves beside this
// head names that person once the roll ends and the backfill has run: the comment holds the
// mentions tuple to the person, the person the delivered_to from it, and the person takes part in
// it - as the same comment written through this head holds at once, which the test writes first.
//
// The comment is one of the three things R5's mentions tuple runs from, and the base release
// writes the other two beside this head too: an issue message naming the person, which once the
// roll ends holds the mentions tuple, the delivered_to and the person's participation as the
// comment does, and a document whose block names the person, which holds the mentions tuple out
// of that block - a document's block delivers nothing and keeps nobody's participation, in this
// head as in the base release. Each is first written through this head, which is the expected
// result the base release's write is held to. The document writes the person's address as a
// link's target: an upload renders a bare one with its email part linked (`person-[2@...]
// (mailto:...)`), in either release, which then names nobody.
func TestAPersonMentionThePreviousBuildWritesReachesThatPerson(t *testing.T) {
	ctx := context.Background()
	previous, current, database, head, databaseURL := startRoll(t, ctx)

	const named = "person-2@example.com"
	current.call(t, http.MethodPost, "/api/v1/projects", `{"key":"MENT","name":"A mention during a roll"}`, http.StatusCreated)
	issue, _ := createCensusIssue(t, current, "MENT", "A comment that names a person", "# Spec\n\nThe spec.\n")
	written := map[string]string{}
	for _, by := range []string{"head", "base"} {
		server := current
		if by == "base" {
			server = previous
		}
		answer := server.send(t, http.MethodPost, "/api/v1/issues/"+issue+"/comments",
			`{"body":"Over to dispatch://person/`+named+` for the copy, from the `+by+`."}`, asUser("bob"))
		var comment struct {
			ID string `json:"id"`
		}
		if answer.status != http.StatusCreated || json.Unmarshal(answer.body, &comment) != nil || comment.ID == "" {
			t.Fatalf("bob's comment naming %s through the %s: %s", named, by, answer)
		}
		written[by] = comment.ID
	}
	messaged := map[string]string{}
	documents := map[string]string{}
	for _, by := range []string{"head", "base"} {
		server := current
		if by == "base" {
			server = previous
		}
		answer := server.send(t, http.MethodPost, "/api/v1/issues/"+issue+"/messages",
			`{"body":"Over to dispatch://person/`+named+` for the message, from the `+by+`."}`, asUser("bob"))
		var message struct {
			ID string `json:"id"`
		}
		if answer.status != http.StatusCreated || json.Unmarshal(answer.body, &message) != nil || message.ID == "" {
			t.Fatalf("bob's issue message naming %s through the %s: %s", named, by, answer)
		}
		messaged[by] = message.ID
		documents[by] = censusProjectDocument(t, server, "MENT", "notes-from-the-"+by+".md",
			"# Notes\n\nOver to [the copy owner](dispatch://person/"+named+") for the notes, from the "+by+".\n")
	}
	endRoll(t, previous, head, databaseURL)

	deadline := time.Now().Add(rollCatchUpWait)
	for time.Now().Before(deadline) &&
		!heldTuple(t, ctx, database, "comment", written["base"], graphstore.TupleMentions, graphstore.KindPerson, named) {
		time.Sleep(time.Second)
	}
	for _, by := range []string{"head", "base"} {
		comment := written[by]
		if !heldTuple(t, ctx, database, "comment", comment, graphstore.TupleMentions, graphstore.KindPerson, named) {
			t.Errorf("the comment %s the %s wrote naming dispatch://person/%s holds no mentions tuple to that person after the roll ended and the backfill ran", comment, by, named)
		}
		if !heldTuple(t, ctx, database, "comment", comment, graphstore.TupleDeliveredTo, graphstore.KindPerson, named) {
			t.Errorf("the comment %s the %s wrote naming %s holds no delivered_to to that person", comment, by, named)
		}
		if !heldTuple(t, ctx, database, graphstore.KindPerson, named, graphstore.TupleParticipatesIn, "comment", comment) {
			t.Errorf("%s, whom the comment %s the %s wrote names, takes no part in it", named, comment, by)
		}
	}
	deadline = time.Now().Add(rollCatchUpWait)
	for time.Now().Before(deadline) &&
		(!heldTuple(t, ctx, database, "message", messaged["base"], graphstore.TupleMentions, graphstore.KindPerson, named) ||
			!heldTuple(t, ctx, database, graphstore.KindBlock, blockNaming(t, ctx, database, documents["base"], named), graphstore.TupleMentions, graphstore.KindPerson, named)) {
		time.Sleep(time.Second)
	}
	for _, by := range []string{"head", "base"} {
		message := messaged[by]
		if !heldTuple(t, ctx, database, "message", message, graphstore.TupleMentions, graphstore.KindPerson, named) {
			t.Errorf("the issue message %s the %s wrote naming dispatch://person/%s holds no mentions tuple to that person after the roll ended and the backfill ran", message, by, named)
		}
		if !heldTuple(t, ctx, database, "message", message, graphstore.TupleDeliveredTo, graphstore.KindPerson, named) {
			t.Errorf("the issue message %s the %s wrote naming %s holds no delivered_to to that person", message, by, named)
		}
		if !heldTuple(t, ctx, database, graphstore.KindPerson, named, graphstore.TupleParticipatesIn, "message", message) {
			t.Errorf("%s, whom the issue message %s the %s wrote names, takes no part in it", named, message, by)
		}
		block := blockNaming(t, ctx, database, documents[by], named)
		if block == "" {
			t.Errorf("the document %s the %s wrote naming dispatch://person/%s holds no block whose text names that person after the roll ended and the backfill ran", documents[by], by, named)
			logDocumentBlocks(t, ctx, database, documents[by])
			continue
		}
		if !heldTuple(t, ctx, database, graphstore.KindBlock, block, graphstore.TupleMentions, graphstore.KindPerson, named) {
			t.Errorf("the block %s of the document %s the %s wrote naming dispatch://person/%s holds no mentions tuple to that person after the roll ended and the backfill ran", block, documents[by], by, named)
			logDocumentBlocks(t, ctx, database, documents[by])
		}
	}
}

// blockNaming is the node address (refs' block address, <ref_key>#<block id>) of the block of the
// document documentID whose stored text names login, and "" where the document holds none yet.
func blockNaming(t *testing.T, ctx context.Context, database *pgx.Conn, documentID, login string) string {
	t.Helper()
	var block string
	if err := database.QueryRow(ctx, `
		select coalesce((
			select a.ref_key || '#' || b.block_id from document_blocks b join artifacts a on a.id = b.artifact_id
			where b.artifact_id = $1::uuid and strpos(b.excerpt, $2) > 0
			order by b.block_id limit 1), '')
	`, documentID, login).Scan(&block); err != nil {
		t.Fatalf("read the block of document %s that names %s: %v", documentID, login, err)
	}
	return block
}

// logDocumentBlocks logs what the store holds of the document documentID's block index: its row
// of document_block_versions, its artifact's kind, each of its document_blocks rows with the node
// it names and the head of its excerpt, and every tuple out of a block of the document, so a test
// that finds no block naming a person, or no mention out of one, shows what it read.
func logDocumentBlocks(t *testing.T, ctx context.Context, database *pgx.Conn, documentID string) {
	t.Helper()
	var held string
	if err := database.QueryRow(ctx, `
		select coalesce((select a.kind || ' ' || a.ref_key from artifacts a where a.id = $1::uuid), 'no artifact')
		    || ' | versions: ' || coalesce((select string_agg(coalesce(v.version::text, 'null') || ' store_version ' || coalesce(v.store_version::text, 'null')
		                                                      || ' owed_at ' || coalesce(v.store_owed_at::text, 'none'), '; ')
		                                    from document_block_versions v where v.artifact_id = $1::uuid), 'no row')
		    || ' | blocks: ' || coalesce((select string_agg(b.block_id || ' ' || b.type || ' node ' || coalesce(b.node_kind || ':' || b.node_address, 'none')
		                                                    || ' ' || left(b.excerpt, 100), '; ' order by b.block_id)
		                                  from document_blocks b where b.artifact_id = $1::uuid), 'none')
		    || ' | tuples: ' || coalesce((select string_agg(t.from_id || ' -' || t.kind || '-> ' || t.to_kind || ':' || t.to_id || ' @' || t.source_event
		                                                    || case when t.asserted then '' else ' retracted' end, '; ' order by t.seq)
		                                  from artifacts a join graph_tuples t on t.from_kind = 'block' and left(t.from_id, length(a.ref_key) + 1) = a.ref_key || '#'
		                                  where a.id = $1::uuid), 'none')
		    || ' | markdown versions: ' || (select count(*) from artifact_versions v where v.artifact_id = $1::uuid)::text
	`, documentID).Scan(&held); err != nil {
		t.Logf("read the block index of document %s: %v", documentID, err)
		return
	}
	t.Logf("document %s: %s", documentID, held)
}
