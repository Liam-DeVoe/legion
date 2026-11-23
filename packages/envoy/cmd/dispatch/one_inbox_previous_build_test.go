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
}
