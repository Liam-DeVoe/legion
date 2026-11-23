package graphcensus

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// The tests of the writes a build before this one makes that change no row this release marks
// (backfill_inbox_events.go) and of the people the text of a comment or an issue message that
// build wrote names (writeCommentPointers, writeMessages). Each writes rows and events as that
// build writes them - its events on a connection with no marker writer setting, which the trigger
// of migration 20261122100123 queues - and reads that the store ends holding what this release's
// writer of the same write states (the spec's R2, R4, R5 and R9), that the queue is empty, and that
// the census of user_threads, which holds every participation to a row or to the arm or the
// mention that explains it, reads no difference.

// writeAsTheReleaseBefore commits rows and the event they answer as writes.write commits them, in
// a transaction whose connection does not carry this build's marker writer setting
// (store.StoreMarkerWriterSetting), as a build of the release before this one writes.
func writeAsTheReleaseBefore(t *testing.T, writes addressingWrites, rows func(ctx context.Context, tx pgx.Tx) (model.Event, error)) model.Event {
	t.Helper()
	return writes.write(t, func(ctx context.Context, tx pgx.Tx) (model.Event, error) {
		if _, err := tx.Exec(ctx, `select set_config($1, 'off', true)`, store.StoreMarkerWriterSetting); err != nil {
			return model.Event{}, err
		}
		return rows(ctx, tx)
	})
}

// queuedInboxEvents is the events events_inbox_owed holds, in id order.
func queuedInboxEvents(t *testing.T, database *store.Store) []int64 {
	t.Helper()
	rows, err := database.Pool.Query(context.Background(), `select event_id from events_inbox_owed order by event_id`)
	if err != nil {
		t.Fatalf("read the queue of an older build's events: %v", err)
	}
	queued, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		t.Fatalf("read the queue of an older build's events: %v", err)
	}
	return queued
}

// newestOf is whether the newest tuple of the relation (fromKind/fromID -kind-> toKind/toID)
// asserts it, and the event it was stated under; false and the sentinel where the store holds none.
func newestOf(t *testing.T, database *store.Store, fromKind, fromID, kind, toKind, toID string) (bool, int64) {
	t.Helper()
	var asserted bool
	var event int64
	err := database.Pool.QueryRow(context.Background(), `
		select asserted, source_event from graph_tuples
		where from_kind = $1 and from_id = $2 and kind = $3 and to_kind = $4 and to_id = $5
		order by seq desc limit 1
	`, fromKind, fromID, kind, toKind, toID).Scan(&asserted, &event)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, graphstore.NoEvent
	}
	if err != nil {
		t.Fatalf("read %s/%s %s %s/%s: %v", fromKind, fromID, kind, toKind, toID, err)
	}
	return asserted, event
}

// requireStands fails unless the newest tuple of the relation asserts it under event.
func requireStands(t *testing.T, database *store.Store, what string, event int64, fromKind, fromID, kind, toKind, toID string) {
	t.Helper()
	if asserted, under := newestOf(t, database, fromKind, fromID, kind, toKind, toID); !asserted || under != event {
		t.Errorf("%s: the newest %s/%s -%s-> %s/%s asserts %t under event %d, want it asserted under %d",
			what, fromKind, fromID, kind, toKind, toID, asserted, under, event)
	}
}

// requireEnded fails unless no tuple of the relation stands.
func requireEnded(t *testing.T, database *store.Store, what, fromKind, fromID, kind, toKind, toID string) {
	t.Helper()
	if asserted, under := newestOf(t, database, fromKind, fromID, kind, toKind, toID); asserted {
		t.Errorf("%s: %s/%s -%s-> %s/%s still stands, under event %d", what, fromKind, fromID, kind, toKind, toID, under)
	}
}

// requireTheThreadsAgree fails unless the census of user_threads and of its version read no
// difference in either direction.
func requireTheThreadsAgree(t *testing.T, database *store.Store, what string) {
	t.Helper()
	report := storeCensusOf(t, database)
	for _, name := range []string{"user_threads", "user_threads_version"} {
		kind := censusKind(t, report, name)
		for _, difference := range append(append([]Difference{}, kind.ReportedRows...), kind.ReportedNodes...) {
			t.Errorf("%s: the census of %s reads a difference at %s: %s", what, name, difference.At, difference.Cause)
		}
		if kind.RowDifferences+kind.NodeDifferences != 0 {
			t.Errorf("%s: the census of %s reads %d row and %d node difference(s), want none", what, name, kind.RowDifferences, kind.NodeDifferences)
		}
	}
}

// catchUpTheTick runs the block-index tick's catch-up (CatchUpOwedRows) until it reads no full
// batch.
func catchUpTheTick(t *testing.T, database *store.Store) {
	t.Helper()
	for round := 1; ; round++ {
		caught, err := CatchUpOwedRows(context.Background(), database)
		if err != nil {
			t.Fatalf("catch the inbox's rows up: %v", err)
		}
		if !caught.More {
			return
		}
		if round == 100 {
			t.Fatalf("the catch-up still reads a full batch after %d rounds", round)
		}
	}
}

// R2: an issue's reopen restores the asks its close withdrew, and each restored ask "reaches
// whoever it waits on as it is restored" (inboxwrite.Restored): this release's reopen moves its
// delivered_to and its participates_in from whom it waited on before the close to whom it waits on
// now. R9: what a build before this one writes takes its tuples once, from the backfill or the
// block-index tick's catch-up.
//
// alice asks on CENS-1, which bob holds, and the backfill states the ask reaching bob. A build of
// the release before then closes the issue, handing it to dave as it closes it - its close
// withdraws the ask and moves nothing - and reopens it, which restores the ask with an ask.opened.
// The trigger queues that event, which makes the tick owe its catch-up; the catch-up moves the ask
// to dave under the restore's event and by its actor, carol: dave holds the delivered_to and takes
// part, and bob, who took no other part in the ask, holds neither. The queue empties and the census
// of user_threads reads no difference. Its control is the queue's pass taken out of the passes
// (inboxPasses): the ask stays bob's.
func TestTheTickMovesAnAskAnOlderBuildsReopenRestoresToWhoeverItWaitsOn(t *testing.T) {
	writes := newAddressingWrites(t)
	writes.createIssue(t, "CENS-1", 1, "U", alice)
	ctx := context.Background()
	if _, err := writes.database.Pool.Exec(ctx, `update issues set assignee = 'bob' where key = 'CENS-1'`); err != nil {
		t.Fatalf("give CENS-1 to bob: %v", err)
	}
	asked := writeAsAnOlderBuild(t, writes, "ask.opened", alice, `
		insert into asks (issue_key, author, question) values ('CENS-1', $1, 'Ready?')
		returning id::text, created_at`, actorJSON(t, alice))
	backfillInboxOnce(t, writes.database)
	if asserted, _ := newestOf(t, writes.database, graphstore.KindAsk, asked.id, graphstore.TupleDeliveredTo, graphstore.KindPerson, "bob"); !asserted {
		t.Fatalf("after the backfill alice's ask holds no delivered_to to bob, who holds its issue: this test proves nothing")
	}

	writeAsTheReleaseBefore(t, writes, func(ctx context.Context, tx pgx.Tx) (model.Event, error) {
		if _, err := tx.Exec(ctx, `update issues set assignee = 'dave', status = 'done', closed_at = now() where key = 'CENS-1'`); err != nil {
			return model.Event{}, err
		}
		_, err := tx.Exec(ctx, `update asks set state = 'resolved', resolution = '{"kind": "withdrawn"}' where id = $1::uuid`, asked.id)
		return model.Event{IssueKey: new("CENS-1"), Type: "issue.closed", Actor: carol, Payload: model.IssueEventPayload{
			Issue: model.Issue{Key: "CENS-1", Status: "done"}, Changed: []string{"status", "assignee", "closed_at"},
		}}, err
	})
	after := newestEventID(t, writes.database)
	reopened := writeAsTheReleaseBefore(t, writes, func(ctx context.Context, tx pgx.Tx) (model.Event, error) {
		if _, err := tx.Exec(ctx, `update issues set status = 'todo', closed_at = null where key = 'CENS-1'`); err != nil {
			return model.Event{}, err
		}
		_, err := tx.Exec(ctx, `update asks set state = 'open', resolution = null where id = $1::uuid`, asked.id)
		return model.Event{IssueKey: new("CENS-1"), Type: "ask.opened", Actor: carol,
			Payload: model.AskEventPayload{Ask: model.Ask{ID: asked.id, IssueKey: new("CENS-1"), Author: alice, CreatedAt: asked.at}}}, err
	})
	if queued := queuedInboxEvents(t, writes.database); len(queued) != 1 || queued[0] != reopened.ID {
		t.Fatalf("the queue holds %v after the older build's close and reopen, want the restore's ask.opened %d alone", queued, reopened.ID)
	}
	if !eventsOwe(t, writes.database, after, reopened.ID) {
		t.Errorf("the older build's restore of an ask owes the tick's catch-up nothing, want it owed")
	}

	catchUpTheTick(t, writes.database)
	requireStands(t, writes.database, "the restore's move", reopened.ID,
		graphstore.KindAsk, asked.id, graphstore.TupleDeliveredTo, graphstore.KindPerson, "dave")
	requireStands(t, writes.database, "the restore's move", reopened.ID,
		graphstore.KindPerson, "dave", graphstore.TupleParticipatesIn, graphstore.KindAsk, asked.id)
	requireEnded(t, writes.database, "the restore's move", graphstore.KindAsk, asked.id, graphstore.TupleDeliveredTo, graphstore.KindPerson, "bob")
	requireEnded(t, writes.database, "the restore's move", graphstore.KindPerson, "bob", graphstore.TupleParticipatesIn, graphstore.KindAsk, asked.id)
	if queued := queuedInboxEvents(t, writes.database); len(queued) != 0 {
		t.Errorf("the queue holds %v after the tick's catch-up, want none", queued)
	}
	if eventsOwe(t, writes.database, after, reopened.ID) {
		t.Errorf("the restore still owes the tick's catch-up once the queue is empty, want nothing owed")
	}
	requireTheThreadsAgree(t, writes.database, "after the tick's catch-up of the restore")
	requireASecondRunAppendsNothing(t, writes.database)
}

// R2: "the list follows the assignment" - each move of an ask states its arm under its own event,
// made by its own writer, and a person who moves an ask onto themselves takes part in it and draws
// no delivery (inboxwrite.OwnMove). A build of the release before moves alice's ask twice before
// any pass reads either move: dave gives CENS-1 to carol, then bob gives it to dave. The ask waits
// on dave, moved there by bob, so this release's writer would have stated dave's delivered_to and
// participates_in under bob's move. The pass reads who each ask waits on as it runs, after both
// moves, so it states the ask's arm by the later move alone (laterMoveOf): dave holds the
// delivered_to under bob's event, and bob, whom the ask waited on before, holds neither tuple. Its
// control is the later moves left out of the test (laterMoveOf answering the stored marker alone):
// the first move, dave's, states dave's participation as his own move onto himself, with no
// delivery, and the second appends nothing.
func TestTheLastOfAnOlderBuildsMovesOfAnAskStatesItsArm(t *testing.T) {
	writes := newAddressingWrites(t)
	writes.createIssue(t, "CENS-1", 1, "U", alice)
	ctx := context.Background()
	if _, err := writes.database.Pool.Exec(ctx, `update issues set assignee = 'bob' where key = 'CENS-1'`); err != nil {
		t.Fatalf("give CENS-1 to bob: %v", err)
	}
	asked := writeAsAnOlderBuild(t, writes, "ask.opened", alice, `
		insert into asks (issue_key, author, question) values ('CENS-1', $1, 'Ready?')
		returning id::text, created_at`, actorJSON(t, alice))
	backfillInboxOnce(t, writes.database)

	reassign := func(to string, by model.Actor) model.Event {
		return writeAsTheReleaseBefore(t, writes, func(ctx context.Context, tx pgx.Tx) (model.Event, error) {
			_, err := tx.Exec(ctx, `update issues set assignee = $1 where key = 'CENS-1'`, to)
			return model.Event{IssueKey: new("CENS-1"), Type: "issue.updated", Actor: by, Payload: model.IssueEventPayload{
				Issue: model.Issue{Key: "CENS-1", Status: "triage"}, Changed: []string{"assignee"},
			}}, err
		})
	}
	first := reassign("carol", dave)
	second := reassign("dave", bob)
	if queued := queuedInboxEvents(t, writes.database); len(queued) != 2 || queued[0] != first.ID || queued[1] != second.ID {
		t.Fatalf("the queue holds %v after the older build's two changes of CENS-1's assignee, want %d and %d", queued, first.ID, second.ID)
	}

	backfillInboxOnce(t, writes.database)
	requireStands(t, writes.database, "the later move", second.ID,
		graphstore.KindAsk, asked.id, graphstore.TupleDeliveredTo, graphstore.KindPerson, "dave")
	requireStands(t, writes.database, "the later move", second.ID,
		graphstore.KindPerson, "dave", graphstore.TupleParticipatesIn, graphstore.KindAsk, asked.id)
	requireEnded(t, writes.database, "the later move", graphstore.KindAsk, asked.id, graphstore.TupleDeliveredTo, graphstore.KindPerson, "bob")
	requireEnded(t, writes.database, "the later move", graphstore.KindPerson, "bob", graphstore.TupleParticipatesIn, graphstore.KindAsk, asked.id)
	if queued := queuedInboxEvents(t, writes.database); len(queued) != 0 {
		t.Errorf("the queue holds %v after the backfill, want none", queued)
	}
	requireTheThreadsAgree(t, writes.database, "after the backfill of the two moves")
	requireASecondRunAppendsNothing(t, writes.database)
}

// R5: a mention "runs from the comment, issue message or document block that holds it to the
// identity it points at", and R4: participation "holds every thread they were mentioned or
// addressed in without writing in it". This release's edit of a comment states the mentions tuple
// to each identity its new text names and each newly named person's participates_in in the
// comment's thread, under the edit's event, and delivers nothing (api's editComment).
//
// alice's comment is stated by the backfill. A build of the release before then edits it to name
// dave and an agent's session; that build states no mention of either and stores no row of dave's,
// and the comment's marker marks its pointers, which no edit moves, so only the edit's event, which
// the trigger queues, tells the backfill. The backfill states the comment's mentions tuples to dave
// and to the session and dave's participation in the comment's thread under the edit's event, no
// delivered_to to either, and no participation of alice's from the edit, who wrote it. A second run
// appends nothing. Its control is the queue's edits left out (writeQueuedInboxEvents): the comment
// names nobody.
func TestTheBackfillStatesThePeopleAnOlderBuildsEditOfACommentNames(t *testing.T) {
	writes := newAddressingWrites(t)
	writes.createIssue(t, "CENS-1", 1, "U", alice)
	ctx := context.Background()
	thread := writeAsAnOlderBuild(t, writes, "comment.created", alice, `
		insert into comments (issue_key, author, body) values ('CENS-1', $1, 'Is the copy final?')
		returning id::text, created_at`, actorJSON(t, alice))
	backfillInboxOnce(t, writes.database)

	const body = "Is the copy final? Over to dispatch://person/" + daveAddress + " for it, with dispatch://agent/" + mentionedSession + "."
	edited := writeAsTheReleaseBefore(t, writes, func(ctx context.Context, tx pgx.Tx) (model.Event, error) {
		_, err := tx.Exec(ctx, `update comments set body = $2, edited_at = now() where id = $1::uuid`, thread.id, body)
		return model.Event{IssueKey: new("CENS-1"), Type: "comment.edited", Actor: alice,
			Payload: map[string]any{"id": thread.id, "body": body}}, err
	})
	if queued := queuedInboxEvents(t, writes.database); len(queued) != 1 || queued[0] != edited.ID {
		t.Fatalf("the queue holds %v after the older build's edit, want its comment.edited %d alone", queued, edited.ID)
	}

	backfillInboxOnce(t, writes.database)
	for _, named := range []struct{ kind, id string }{{graphstore.KindPerson, daveAddress}, {graphstore.KindSession, mentionedSession}} {
		requireStands(t, writes.database, "the edit's mention", edited.ID,
			graphstore.KindComment, thread.id, graphstore.TupleMentions, named.kind, named.id)
		if asserted, under := newestOf(t, writes.database, graphstore.KindComment, thread.id, graphstore.TupleDeliveredTo, named.kind, named.id); asserted {
			t.Errorf("the edit delivered comment %s to %s %s under event %d, want no delivery: an edit delivers nothing", thread.id, named.kind, named.id, under)
		}
	}
	requireStands(t, writes.database, "the edit's mention", edited.ID,
		graphstore.KindPerson, daveAddress, graphstore.TupleParticipatesIn, graphstore.KindComment, thread.id)
	var aliceUnderEdit int
	if err := writes.database.Pool.QueryRow(ctx, `
		select count(*) from graph_tuples
		where from_kind = $1 and from_id = 'alice' and kind = $2 and source_event = $3
	`, graphstore.KindPerson, graphstore.TupleParticipatesIn, edited.ID).Scan(&aliceUnderEdit); err != nil {
		t.Fatalf("read alice's participation under the edit: %v", err)
	}
	if aliceUnderEdit != 0 {
		t.Errorf("the edit stated %d participates_in of alice, who wrote it, want none", aliceUnderEdit)
	}
	if queued := queuedInboxEvents(t, writes.database); len(queued) != 0 {
		t.Errorf("the queue holds %v after the backfill, want none", queued)
	}
	requireTheThreadsAgree(t, writes.database, "after the backfill of the edit")
	requireASecondRunAppendsNothing(t, writes.database)
}

// R5, R2 and R4 of a comment and an issue message a build before this one wrote before this
// release's schema reached the database, or before any backfill ran: such a build states no
// mention of an identity and stores no row of a person's (refs.TargetOf), so the item's text is
// the one record of whom it names, and the backfill reads it once, beside what each item owed its
// thread (writeCommentPointers, writeMessages), as this release's write of the same text states it.
//
// alice opens a thread, and bob replies naming dave; bob writes an issue message naming dave, an
// agent's session and a role; and carol writes a comment that names erin only in code, which names
// nobody. A person's address is an email address (text's personLoginPattern). The backfill states,
// under each item's own event: the mentions tuple to dave out of bob's reply, and to dave, the
// session and the role out of his message; the reply's delivered_to to dave, whom no row of the
// thread reached, and to alice, whom the old trigger reached; the message's delivered_to to dave;
// dave's participation in the thread's root and in the message; no delivered_to to the session or
// the role, which a mention reaches nothing of by itself in this release; and nothing to erin. A
// second run appends nothing, and the census of user_threads reads no difference: dave holds no
// row of either thread, and his participation is the mention's.
func TestTheBackfillStatesThePeopleAnOlderBuildsCommentsAndIssueMessagesName(t *testing.T) {
	writes := newAddressingWrites(t)
	writes.createIssue(t, "CENS-1", 1, "U", alice)
	thread := writeAsAnOlderBuild(t, writes, "comment.created", alice, `
		insert into comments (issue_key, author, body) values ('CENS-1', $1, 'Is the copy final?')
		returning id::text, created_at`, actorJSON(t, alice))
	reply := writeAsAnOlderBuild(t, writes, "comment.created", bob, `
		insert into comments (issue_key, author, body, reply_to)
		values ('CENS-1', $1, 'Nearly: over to dispatch://person/`+daveAddress+` to check.', $2)
		returning id::text, created_at`, actorJSON(t, bob), thread.id)
	message := writeAsAnOlderBuild(t, writes, "message.created", bob, `
		insert into messages (issue_key, author, body)
		values ('CENS-1', $1, 'For dispatch://person/`+daveAddress+` to read, with dispatch://agent/`+mentionedSession+` and dispatch://role/reviewer.')
		returning id::text, created_at`, actorJSON(t, bob))
	coded := writeAsAnOlderBuild(t, writes, "comment.created", carol, "insert into comments (issue_key, author, body) values ('CENS-1', $1, 'Spelled `dispatch://person/erin@example.com`.') returning id::text, created_at",
		actorJSON(t, carol))

	backfillInboxOnce(t, writes.database)
	requireStands(t, writes.database, "bob's reply", reply.event,
		graphstore.KindComment, reply.id, graphstore.TupleMentions, graphstore.KindPerson, daveAddress)
	for _, reached := range []string{daveAddress, "alice"} {
		requireStands(t, writes.database, "bob's reply", reply.event,
			graphstore.KindComment, reply.id, graphstore.TupleDeliveredTo, graphstore.KindPerson, reached)
	}
	requireStands(t, writes.database, "bob's reply", reply.event,
		graphstore.KindPerson, daveAddress, graphstore.TupleParticipatesIn, graphstore.KindComment, thread.id)
	requireStands(t, writes.database, "bob's message", message.event,
		graphstore.KindMessage, message.id, graphstore.TupleMentions, graphstore.KindPerson, daveAddress)
	requireStands(t, writes.database, "bob's message", message.event,
		graphstore.KindMessage, message.id, graphstore.TupleDeliveredTo, graphstore.KindPerson, daveAddress)
	requireStands(t, writes.database, "bob's message", message.event,
		graphstore.KindPerson, daveAddress, graphstore.TupleParticipatesIn, graphstore.KindMessage, message.id)
	for _, identity := range []struct{ kind, id string }{{graphstore.KindSession, mentionedSession}, {graphstore.KindRole, "reviewer"}} {
		requireStands(t, writes.database, "bob's message", message.event,
			graphstore.KindMessage, message.id, graphstore.TupleMentions, identity.kind, identity.id)
		if asserted, under := newestOf(t, writes.database, graphstore.KindMessage, message.id, graphstore.TupleDeliveredTo, identity.kind, identity.id); asserted {
			t.Errorf("bob's message %s, which names %s %s, delivered to it under event %d, want no delivery a mention makes", message.id, identity.kind, identity.id, under)
		}
	}
	if asserted, _ := newestOf(t, writes.database, graphstore.KindComment, coded.id, graphstore.TupleMentions, graphstore.KindPerson, "erin@example.com"); asserted {
		t.Errorf("carol's comment %s, which spells erin's address only in code, mentions erin", coded.id)
	}
	requireTheThreadsAgree(t, writes.database, "after the backfill of the items naming people")
	requireASecondRunAppendsNothing(t, writes.database)
}

// mentionedSession is the agent's session the texts of these tests name, and daveAddress the
// address of the person they name.
const (
	mentionedSession = "019a1c0b-3333-7000-8000-000000000042"
	daveAddress      = "dave@example.com"
)
