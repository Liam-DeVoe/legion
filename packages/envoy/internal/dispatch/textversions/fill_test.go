package textversions

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/duty"
	"github.com/sjawhar/envoy/internal/dispatch/events"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// TestTheFillContinuesFromItsCursorAfterARestart writes 3,000 events of every kind a rule reads,
// and some no rule reads, and stops a fill as its second batch commits, as a process killed while
// it catches up stops. A fill in a second process, with a pool and a horizon of its own, starts at
// the cursor the first left: its first batch reads the event after the cursor first. At the end
// every event has exactly the rows it starts, none twice and none missing, and the cursor row says
// rules version 1 wrote them.
func TestTheFillContinuesFromItsCursorAfterARestart(t *testing.T) {
	database := storetest.Open(t)
	seedFillProject(t, database)
	broker := events.NewBroker()
	var want []written
	for n := 1; len(want) < 3000; n++ {
		want = append(want, writeIssue(t, database, broker, n)...)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var firstLogs bytes.Buffer
	first := testFiller(database, events.NewHorizon(database.Pool), &firstLogs)
	committed := 0
	first.rest = func(ctx context.Context, _ time.Duration) error {
		if committed++; committed == 2 {
			cancel()
			return ctx.Err()
		}
		return nil
	}
	if err := first.toHorizon(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("the first fill, stopped after its second batch, answered %v; want it cancelled", err)
	}
	left := readCursorRow(t, database)
	firstBatches := batchLines(t, &firstLogs)
	if len(firstBatches) != 2 || firstBatches[1].Cursor != left.eventID {
		t.Fatalf("the first fill logged batches %+v and left the cursor at %d; want two batches, the second ending at the cursor",
			firstBatches, left.eventID)
	}
	if last := want[len(want)-1].id; left.eventID >= last {
		t.Fatalf("the first fill's cursor is %d, at or past the last event %d: it stopped with nothing left to write", left.eventID, last)
	}

	second := storetest.SecondProcess(t, database)
	var secondLogs bytes.Buffer
	if err := testFiller(second, events.NewHorizon(second.Pool), &secondLogs).toHorizon(t.Context()); err != nil {
		t.Fatalf("the second process's fill: %v", err)
	}
	var next int64
	if err := database.Pool.QueryRow(t.Context(), `select min(id) from events where id > $1`, left.eventID).Scan(&next); err != nil {
		t.Fatalf("read the event after the cursor: %v", err)
	}
	secondBatches := batchLines(t, &secondLogs)
	if len(secondBatches) == 0 || secondBatches[0].First != next {
		t.Fatalf("the second process's fill logged batches %+v; want its first to start at event %d, the one after the cursor %d",
			secondBatches, next, left.eventID)
	}
	requireRowsOf(t, database, want)
	end := readCursorRow(t, database)
	if end.eventID != want[len(want)-1].id || end.rules != rulesVersion {
		t.Fatalf("the cursor row ends at event %d with rules %d; want event %d and rules %d",
			end.eventID, end.rules, want[len(want)-1].id, rulesVersion)
	}
}

// TestTheFillWritesANewEventWithinSixtySecondsOfTheHorizon holds the fill to 60 s from the moment
// the horizon passes an event to the event's rows, where nothing announces the moment the horizon
// moves: with the fill caught up, a transaction that holds a transaction id and writes no event
// stays open while three events commit, so the horizon stands below them, and then ends with no
// notification. The fill reads the horizon again on its own, 1 s and then up to 8 s apart, and
// the three events have their rows within 60 s of the moment Horizon.Await answers an id at or
// above them: under the fill's hold, and with no holder.
func TestTheFillWritesANewEventWithinSixtySecondsOfTheHorizon(t *testing.T) {
	for _, held := range []bool{true, false} {
		name := "under its hold"
		if !held {
			name = "with no holder"
		}
		t.Run(name, func(t *testing.T) {
			database := storetest.Open(t)
			seedTheIssue(t, database)
			broker := events.NewBroker()
			horizon := events.NewHorizon(database.Pool)
			fill := testFiller(database, horizon, io.Discard)
			fill.rest = sleep
			holder := uuid.Nil
			if held {
				holder = takeFillHold(t, database)
				fill.holder = holder
			}
			ctx := t.Context()
			commitComment(t, database, broker, theIssue)
			if err := fill.toHorizon(ctx); err != nil {
				t.Fatalf("catch the fill up: %v", err)
			}

			holding, err := database.Pool.Begin(ctx)
			if err != nil {
				t.Fatalf("begin the transaction that holds the horizon: %v", err)
			}
			defer holding.Rollback(context.Background())
			if _, err := holding.Exec(ctx, `select pg_current_xact_id()`); err != nil {
				t.Fatalf("take a transaction id: %v", err)
			}
			var ids []int64
			for range 3 {
				ids = append(ids, commitComment(t, database, broker, theIssue).id)
			}
			done := make(chan error, 1)
			go func() { done <- fill.toHorizon(ctx) }()
			// The fill reads the horizon below the three events and reads it again, at 1 s and then
			// 2 s, while the transaction holds it there.
			time.Sleep(3 * time.Second)
			if rows := rowsOfEvents(t, database, ids...); rows != 0 {
				t.Fatalf("the fill wrote %d rows of events the horizon had not passed", rows)
			}
			if err := holding.Rollback(ctx); err != nil {
				t.Fatalf("end the transaction that holds the horizon: %v", err)
			}
			safe, err := horizon.Await(ctx)
			if err != nil {
				t.Fatalf("await the horizon: %v", err)
			}
			if safe < ids[2] {
				t.Fatalf("Await answered %d, below the third event %d", safe, ids[2])
			}
			passed := time.Now()
			for rowsOfEvents(t, database, ids...) < len(ids) {
				if time.Since(passed) > time.Minute {
					t.Fatalf("the three events had %d rows a minute after the horizon passed them", rowsOfEvents(t, database, ids...))
				}
				time.Sleep(50 * time.Millisecond)
			}
			t.Logf("the rows came %s after the horizon passed the events", time.Since(passed).Round(time.Millisecond))
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("the fill: %v", err)
				}
			case <-time.After(time.Minute):
				t.Fatal("the fill did not return once the horizon passed every event it saw")
			}
			if got := readCursorRow(t, database); got.eventID != ids[2] || got.holder != holderText(holder) {
				t.Fatalf("the cursor row is at event %d naming holder %q; want event %d and holder %q",
					got.eventID, got.holder, ids[2], holderText(holder))
			}
		})
	}
}

// TestTheFillStartsAgainAfterTheEventLogIsReset resets the event log under a fill that wrote the
// rows of about 100 events, in the two forms that empty it: `truncate events restart identity`,
// after which the new log stands below the cursor, and the delete and sequence restart that
// e2e/seed.ts runs, here refilled past the cursor, so the row at the cursor's id is another
// event. The fill finds the reset, empties the table and its cursor and writes the new log from
// its first event: no row of the old log is left, and the new events have exactly their rows.
func TestTheFillStartsAgainAfterTheEventLogIsReset(t *testing.T) {
	for _, form := range []struct {
		name  string
		reset []string
		// issues is how many issues the new log holds the events of, 14 events each; pastCursor
		// is whether that reaches past the old log's cursor.
		issues     int
		pastCursor bool
	}{
		{name: "truncate restart identity", reset: []string{`truncate events restart identity cascade`}, issues: 2},
		{name: "delete and restart the sequence", reset: []string{`delete from events`, `alter sequence events_id_seq restart`}, issues: 9, pastCursor: true},
	} {
		t.Run(form.name, func(t *testing.T) {
			database := storetest.Open(t)
			seedFillProject(t, database)
			broker := events.NewBroker()
			var logs bytes.Buffer
			fill := testFiller(database, events.NewHorizon(database.Pool), &logs)
			ctx := t.Context()
			const oldIssues = 8
			var before []written
			for n := 1; n <= oldIssues; n++ {
				before = append(before, writeIssue(t, database, broker, n)...)
			}
			if err := fill.toHorizon(ctx); err != nil {
				t.Fatalf("fill the old log: %v", err)
			}
			requireRowsOf(t, database, before)
			left := readCursorRow(t, database)

			for _, statement := range form.reset {
				if _, err := database.Pool.Exec(ctx, statement); err != nil {
					t.Fatalf("reset the event log (%s): %v", statement, err)
				}
			}
			var after []written
			for n := oldIssues + 1; n <= oldIssues+form.issues; n++ {
				after = append(after, writeIssue(t, database, broker, n)...)
			}
			if past := after[len(after)-1].id > left.eventID; past != form.pastCursor {
				t.Fatalf("the new log ends at event %d against the old cursor %d; the test meant it past the cursor: %t",
					after[len(after)-1].id, left.eventID, form.pastCursor)
			}
			if err := fill.toHorizon(ctx); err != nil {
				t.Fatalf("fill the new log: %v", err)
			}

			var oldRows int
			oldKeys := make([]string, 0, oldIssues)
			for n := 1; n <= oldIssues; n++ {
				oldKeys = append(oldKeys, fmt.Sprintf("ACME-%d", n))
			}
			if err := database.Pool.QueryRow(ctx, `select count(*) from search_text_versions where issue_key = any($1)`, oldKeys).
				Scan(&oldRows); err != nil {
				t.Fatalf("count the old log's rows: %v", err)
			}
			if oldRows != 0 {
				t.Fatalf("%d rows of the old log's items are left", oldRows)
			}
			requireRowsOf(t, database, after)
			end := readCursorRow(t, database)
			var lastAt time.Time
			if err := database.Pool.QueryRow(ctx, `select created_at from events where id = $1`, after[len(after)-1].id).Scan(&lastAt); err != nil {
				t.Fatalf("read the new log's last event: %v", err)
			}
			if end.eventID != after[len(after)-1].id || end.eventAt == nil || !end.eventAt.Equal(lastAt) || end.reach == nil {
				t.Fatalf("the cursor row is at event %d of %v with reach %v; want event %d of %s and a reach",
					end.eventID, end.eventAt, end.reach, after[len(after)-1].id, lastAt)
			}
			// The batch after the reset reads the new log from its first event.
			var firstNew int64
			if err := database.Pool.QueryRow(ctx, `select min(id) from events`).Scan(&firstNew); err != nil {
				t.Fatalf("read the new log's first event: %v", err)
			}
			lines := logLines(t, &logs)
			reset := slices.IndexFunc(lines, func(line logLine) bool { return line.Msg == resetMessage })
			if reset < 0 {
				t.Fatalf("the fill logged no reset: %+v", lines)
			}
			next := slices.IndexFunc(lines[reset:], func(line logLine) bool { return line.Msg == batchMessage })
			if next < 0 || lines[reset+next].First != firstNew {
				t.Fatalf("after the reset the fill logged %+v; want a batch starting at the new log's first event %d", lines[reset:], firstNew)
			}
		})
	}
}

// TestAFillThatLostItsHoldWritesNothing has another holder take the fill's hold over between two
// of its batches: the hold lapses and the other process takes it. The next batch finds that
// duty_holds no longer names its holder and writes nothing: no row and no move of the cursor,
// which still names the first holder, and the fill answers duty.ErrHoldLost.
func TestAFillThatLostItsHoldWritesNothing(t *testing.T) {
	database := storetest.Open(t)
	seedFillProject(t, database)
	broker := events.NewBroker()
	var want []written
	for n := 1; n <= 3; n++ {
		want = append(want, writeIssue(t, database, broker, n)...)
	}
	ctx := t.Context()
	holder := uuid.New()
	hold, err := duty.TryHold(ctx, database.Pool, fillHoldName, holder, holdOptions)
	if err != nil || hold == nil {
		t.Fatalf("take the fill's hold: %v, %v", hold, err)
	}
	t.Cleanup(func() { _ = hold.Release(context.Background()) })
	fill := testFiller(database, events.NewHorizon(database.Pool), io.Discard)
	fill.holder, fill.maxEvents = holder, 14
	var afterFirst cursorRow
	var rowsAfterFirst int
	fill.rest = func(ctx context.Context, _ time.Duration) error {
		if afterFirst.eventID != 0 {
			return nil
		}
		if err := hold.Lapse(ctx); err != nil {
			t.Fatalf("let the fill's hold lapse: %v", err)
		}
		taken, err := duty.TryHold(ctx, database.Pool, fillHoldName, uuid.New(), holdOptions)
		if err != nil || taken == nil {
			t.Fatalf("another holder takes the lapsed hold: %v, %v", taken, err)
		}
		t.Cleanup(func() { _ = taken.Release(context.Background()) })
		afterFirst, rowsAfterFirst = readCursorRow(t, database), countRows(t, database)
		return nil
	}
	if err := fill.toHorizon(ctx); !errors.Is(err, duty.ErrHoldLost) {
		t.Fatalf("the fill whose hold another holder took answered %v; want duty.ErrHoldLost", err)
	}
	if afterFirst.eventID == 0 || afterFirst.eventID >= want[len(want)-1].id {
		t.Fatalf("the first batch left the cursor at %d; want a batch that leaves events before event %d", afterFirst.eventID, want[len(want)-1].id)
	}
	if got := readCursorRow(t, database); !got.equal(afterFirst) {
		t.Fatalf("after the hold was taken over the cursor row is %+v; want it as the first batch left it, %+v", got, afterFirst)
	}
	if got := countRows(t, database); got != rowsAfterFirst {
		t.Fatalf("the table holds %d rows; the first batch left %d", got, rowsAfterFirst)
	}
	if afterFirst.holder != holder.String() {
		t.Fatalf("the cursor row names holder %q; want the first holder %s", afterFirst.holder, holder)
	}
}

// TestAFillStopsAtAnEventItsRulesCannotRead writes, between two issues' events, a comment.created
// whose payload carries no body, which no rule can read. The fill writes the rows of every event
// before it and stops there, with an error that names it: the cursor stands at the event before,
// and no event after it has a row, so none is passed by.
func TestAFillStopsAtAnEventItsRulesCannotRead(t *testing.T) {
	database := storetest.Open(t)
	seedFillProject(t, database)
	broker := events.NewBroker()
	ctx := t.Context()
	before := writeIssue(t, database, broker, 1)
	tx, _ := beginTransaction(t, database)
	defer tx.Rollback(context.Background())
	key := fillProject + "-1"
	unreadable, err := broker.Append(ctx, tx, model.Event{IssueKey: &key, Type: "comment.created",
		Actor: model.Actor{Kind: "user", ID: "alice"}, Payload: map[string]any{"id": uuid.NewString(), "issue_key": key}})
	if err != nil {
		t.Fatalf("append the unreadable event: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit the unreadable event: %v", err)
	}
	after := writeIssue(t, database, broker, 2)

	err = FillToHorizon(ctx, database, events.NewHorizon(database.Pool))
	if !errors.Is(err, errUnreadablePayload) || !strings.Contains(err.Error(), fmt.Sprintf("event %d", unreadable.ID)) {
		t.Fatalf("the fill answered %v; want the rules' unreadable payload, naming event %d", err, unreadable.ID)
	}
	requireRowsOf(t, database, before)
	if got := readCursorRow(t, database).eventID; got != before[len(before)-1].id {
		t.Fatalf("the cursor is at event %d; want %d, the event before the unreadable one", got, before[len(before)-1].id)
	}
	if rows := rowsOfEvents(t, database, after[0].id); rows != 0 {
		t.Fatalf("an event after the unreadable one has %d rows", rows)
	}
}

// TestABatchCountsTheSpecMarkdownItsIssueRowsRead holds each batch, at the production bound of
// 2 MB (batchBytes), to the text its vectors read, in three logs the fill writes from its first
// event. What each batch reads the test measures itself, from the rows the batch wrote and the
// events between its first event and its cursor as its batch line names them (batchText): their
// payloads, the markdown of the primary document version each issue row names and the body of
// each post row whose event cut it. Each batch of more than one event reads at most 2 MB of it,
// each commits under its statement_timeout (a batch that runs past it fails the fill), and every
// event has its rows.
//
//   - 24 issues created with a primary document of about 1 MiB, each then retitled, which
//     carries that version forward, and 4 of them given a new version of about 1 MiB: each of
//     their 52 rows reads about 1 MiB of markdown. A count of their payloads alone, as the fill
//     counted at a5078553, puts the 48 issue events in one batch.
//   - A comment of 10 KB, carried whole, and 400 comment.edited events that leave it as it was,
//     so they start no row: their payloads still count, about 4 MB, so they take two batches at
//     least.
//   - 30 comments created with bodies of about 100 KB, which their events cut, with their
//     revision 1 indexed in their own transactions, each then edited to a few words, all before
//     the fill runs: each comment.created version reads revision 1's blocks, about 3 MB in all,
//     where the comment's row holds the few words; a count of the row's body puts them in one
//     batch.
func TestABatchCountsTheSpecMarkdownItsIssueRowsRead(t *testing.T) {
	t.Run("the markdown each issue row reads", func(t *testing.T) {
		database := storetest.Open(t)
		seedFillProject(t, database)
		broker := events.NewBroker()
		const issueCount, newVersions = 24, 4
		var want []written
		issues := make([]specIssue, 0, issueCount)
		for n := 1; n <= issueCount; n++ {
			issue, created := writeSpecIssue(t, database, broker, n)
			issues, want = append(issues, issue), append(want, created)
		}
		retitles := make([]model.Event, 0, len(issues))
		for _, issue := range issues {
			retitles = append(retitles, issue.event("issue.updated", issue.title+", then audit the verifiers"))
		}
		for _, id := range commitEvents(t, database, broker, retitles...) {
			want = append(want, written{id: id, versions: 1})
		}
		for _, issue := range issues[:newVersions] {
			want = append(want, writeSpecVersion(t, database, broker, issue, 2))
		}
		fillWithinTheBound(t, database, want)
	})

	t.Run("the payload of an event that starts no row", func(t *testing.T) {
		database := storetest.Open(t)
		seedTheIssue(t, database)
		broker := events.NewBroker()
		body := rotationParagraphs(1, 10_000)
		if _, truncated := model.EventBody(body); truncated {
			t.Fatal("the comment's body is one an event cuts; the case needs one its events carry whole")
		}
		comment := writeComment(t, database, broker, body, true)
		want := []written{{id: comment.event, versions: 1}}
		tx, startedAt := beginTransaction(t, database)
		defer tx.Rollback(context.Background())
		for range 400 {
			want = append(want, written{id: appendCommentEvent(t, tx, broker, "comment.edited", comment.id, body, startedAt)})
		}
		if err := tx.Commit(t.Context()); err != nil {
			t.Fatalf("commit the edits: %v", err)
		}
		if batches := fillWithinTheBound(t, database, want); len(batches) < 2 {
			t.Fatalf("the fill wrote the comment and its 400 edits in %d batch: their payloads, about 4 MB, take two at least", len(batches))
		}
	})

	t.Run("the revision 1 a cut comment reads", func(t *testing.T) {
		database := storetest.Open(t)
		seedTheIssue(t, database)
		broker := events.NewBroker()
		const comments = 30
		var want []written
		for n := 1; n <= comments; n++ {
			body := rotationParagraphs(n, 100_000)
			if _, truncated := model.EventBody(body); !truncated {
				t.Fatal("the comment's body is one an event carries whole; the case needs one its event cuts")
			}
			comment := writeComment(t, database, broker, body, true)
			edit := editComment(t, database, broker, comment.id, fmt.Sprintf("Rotated region %d; the plan is in the spec.", n))
			want = append(want, written{id: comment.event, versions: 1}, written{id: edit, versions: 1})
		}
		batches := fillWithinTheBound(t, database, want)
		var fromBlocks int
		if err := database.Pool.QueryRow(t.Context(),
			`select count(*) from search_text_versions where kind = 'comment' and body_from = $1`, bodyFromBlocks).Scan(&fromBlocks); err != nil {
			t.Fatalf("count the rows whose body is revision 1's blocks: %v", err)
		}
		if fromBlocks != comments {
			t.Fatalf("%d of the %d comment.created rows hold revision 1's blocks; the case tests the bodies the rules read from them", fromBlocks, comments)
		}
		if len(batches) < 2 {
			t.Fatalf("the fill wrote the %d comments and their edits in %d batch: their revision 1, about 3 MB, takes two at least", comments, len(batches))
		}
	})
}

// TestTheCursorKeepsTheLowestRulesVersionThatWroteUpToIt holds the cursor row's rules to the
// lowest rules version that wrote the rows up to the cursor. A fill that creates the cursor row
// writes this build's, 1. Set to 0, as a build of an older rules version leaves it (the column
// has no check), it stays 0 while this build's fill writes the next events and moves the cursor
// past them. After a reset of the event log the fill writes the cursor row again, with 1.
func TestTheCursorKeepsTheLowestRulesVersionThatWroteUpToIt(t *testing.T) {
	database := storetest.Open(t)
	seedFillProject(t, database)
	broker := events.NewBroker()
	ctx := t.Context()
	fill := testFiller(database, events.NewHorizon(database.Pool), io.Discard)
	first := writeIssue(t, database, broker, 1)
	if err := fill.toHorizon(ctx); err != nil {
		t.Fatalf("fill the first events: %v", err)
	}
	if created := readCursorRow(t, database); created.eventID != first[len(first)-1].id || created.rules != rulesVersion {
		t.Fatalf("the cursor row the fill created is at event %d with rules %d; want event %d and rules %d",
			created.eventID, created.rules, first[len(first)-1].id, rulesVersion)
	}

	if _, err := database.Pool.Exec(ctx, `update search_text_versions_fill set rules = 0`); err != nil {
		t.Fatalf("set the cursor row's rules to an older build's: %v", err)
	}
	later := writeIssue(t, database, broker, 2)
	if err := fill.toHorizon(ctx); err != nil {
		t.Fatalf("fill the later events: %v", err)
	}
	requireRowsOf(t, database, append(first, later...))
	if got := readCursorRow(t, database); got.eventID != later[len(later)-1].id || got.rules != 0 {
		t.Fatalf("after this build wrote past an older build's cursor the row is at event %d with rules %d; want event %d and rules 0, the older build's",
			got.eventID, got.rules, later[len(later)-1].id)
	}

	if _, err := database.Pool.Exec(ctx, `truncate events restart identity cascade`); err != nil {
		t.Fatalf("reset the event log: %v", err)
	}
	after := writeIssue(t, database, broker, 3)
	if err := fill.toHorizon(ctx); err != nil {
		t.Fatalf("fill the new log: %v", err)
	}
	requireRowsOf(t, database, after)
	if got := readCursorRow(t, database); got.eventID != after[len(after)-1].id || got.rules != rulesVersion {
		t.Fatalf("after a reset the cursor row is at event %d with rules %d; want event %d and rules %d",
			got.eventID, got.rules, after[len(after)-1].id, rulesVersion)
	}
}

// fillWithinTheBound fills database from its first event with the production bounds and holds
// what it wrote: the fill returns no error, so every batch committed under its statement_timeout;
// every event of want has its rows and no other event has one; and each batch of more than one
// event reads at most batchBytes (batchText). It answers the batches' lines, in order.
func fillWithinTheBound(t *testing.T, database *store.Store, want []written) []logLine {
	t.Helper()
	var logs bytes.Buffer
	fill := testFiller(database, events.NewHorizon(database.Pool), &logs)
	started := time.Now()
	if err := fill.toHorizon(t.Context()); err != nil {
		t.Fatalf("the fill, which a batch past its statement_timeout fails: %v", err)
	}
	batches := batchLines(t, &logs)
	t.Logf("the fill wrote %d events in %d batches in %s", len(want), len(batches), time.Since(started).Round(time.Millisecond))
	requireRowsOf(t, database, want)
	for _, b := range batches {
		if b.Events < 2 {
			continue
		}
		if read := batchText(t, database, b.First, b.Cursor); read > batchBytes {
			t.Errorf("the batch of the %d events %d to %d reads %d bytes of text into its vectors and its payloads; want at most %d",
				b.Events, b.First, b.Cursor, read, batchBytes)
		}
	}
	return batches
}

// batchTextQuery measures what the batch of the events $1 to $2 reads, from the events and the
// rows they started: the payload of each event of a type a rule reads ($3), the markdown of the
// primary document version each issue row names, and the body of each post row that does not
// hold its event's (body_from other than 'event'), which the rules read from another source.
const batchTextQuery = `
	select coalesce((select sum(octet_length(e.payload::text)) from events e
	                  where e.id between $1 and $2 and e.type = any($3)), 0)
	     + coalesce((select sum(case when v.kind = 'issue' then coalesce(octet_length(a.markdown), 0)
	                                 when v.body_from <> 'event' then octet_length(v.body)
	                                 else 0 end)
	                   from search_text_versions v
	                   left join artifact_versions a on a.artifact_id = v.spec_artifact_id and a.number = v.spec_version
	                  where v.event_id between $1 and $2), 0)`

// batchText is what the batch of the events first to last reads (batchTextQuery), measured from
// the rows it wrote, not from what the fill counted.
func batchText(t *testing.T, database *store.Store, first, last int64) int64 {
	t.Helper()
	var read int64
	if err := database.Pool.QueryRow(t.Context(), batchTextQuery, first, last, ruleTypes).Scan(&read); err != nil {
		t.Fatalf("measure the text of the batch of events %d to %d: %v", first, last, err)
	}
	return read
}

// specBytes is about the size of each primary document version specMarkdown makes: about 1 MiB,
// under pmdoc.MaxDocumentBytes, so two of them fit in a batch's 2 MB and three do not.
const specBytes = 1_000_000

// specMarkdown is version number of issue n's primary document: a heading and paragraphs of
// words, up to specBytes.
func specMarkdown(n, number int) string {
	const paragraph = "Each verifier of the region reads the signing key until the rotation moves it to its successor, and the audit confirms the move.\n\n"
	var b strings.Builder
	fmt.Fprintf(&b, "# Rotation %d, version %d\n\n", n, number)
	for b.Len()+len(paragraph) <= specBytes {
		b.WriteString(paragraph)
	}
	return b.String()
}

// rotationParagraphs is a post's body of about size bytes about region n: paragraphs of about
// 10 KB, each a block the create route takes (under contracts.DispatchBlockUnits).
func rotationParagraphs(n, size int) string {
	var paragraphs []string
	for total := 0; total < size; {
		var b strings.Builder
		for step := 1; b.Len() < 10_000 && total+b.Len() < size; step++ {
			fmt.Fprintf(&b, "Step %d of region %d moves one signing key to its successor. ", step, n)
		}
		paragraph := strings.TrimSpace(b.String())
		paragraphs = append(paragraphs, paragraph)
		total += len(paragraph) + 2
	}
	return strings.Join(paragraphs, "\n\n")
}

// specIssue is an issue writeSpecIssue wrote: its number, its key, its title and its primary
// document.
type specIssue struct {
	n                    int
	key, title, artifact string
}

// event is an issue.* event of type of the issue with title, as the routes write one.
func (issue specIssue) event(eventType, title string) model.Event {
	key := issue.key
	return model.Event{IssueKey: &key, Type: eventType, Actor: model.Actor{Kind: "user", ID: "alice"},
		Payload: map[string]any{"key": issue.key, "title": title, "primary_artifact_id": issue.artifact}}
}

// writeSpecIssue writes issue ACME-n, its primary document with version 1 of specMarkdown, and
// its issue.created event, in one transaction, as the create route writes them, and answers the
// issue and its event, which starts one version.
func writeSpecIssue(t *testing.T, database *store.Store, broker *events.Broker, n int) (specIssue, written) {
	t.Helper()
	ctx := t.Context()
	issue := specIssue{n: n, key: fmt.Sprintf("%s-%d", fillProject, n), title: fmt.Sprintf("Rotate the signing keys of region %d", n)}
	spec := specMarkdown(n, 1)
	if len(spec) >= pmdoc.MaxDocumentBytes {
		t.Fatalf("the spec is %d bytes, at or over pmdoc.MaxDocumentBytes, %d", len(spec), pmdoc.MaxDocumentBytes)
	}
	tx, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin issue %s: %v", issue.key, err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		insert into issues (key, project_key, number, title, created_by, rank)
		values ($1, $2, $3, $4, '{"kind":"user","id":"alice"}', 'A')
	`, issue.key, fillProject, n, issue.title); err != nil {
		t.Fatalf("insert issue %s: %v", issue.key, err)
	}
	if err := tx.QueryRow(ctx, `
		insert into artifacts (issue_key, project_key, slug, name, kind, is_primary, created_by)
		values ($1, $2, 'spec', 'spec.md', 'doc', true, '{"kind":"user","id":"alice"}')
		returning id::text
	`, issue.key, fillProject).Scan(&issue.artifact); err != nil {
		t.Fatalf("insert the primary document of %s: %v", issue.key, err)
	}
	if _, err := tx.Exec(ctx, `insert into artifact_versions (artifact_id, number, markdown, authors) values ($1::uuid, 1, $2, '[]')`,
		issue.artifact, spec); err != nil {
		t.Fatalf("insert version 1 of %s's primary document: %v", issue.key, err)
	}
	created, err := broker.Append(ctx, tx, issue.event("issue.created", issue.title))
	if err != nil {
		t.Fatalf("append the issue.created of %s: %v", issue.key, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit issue %s: %v", issue.key, err)
	}
	return issue, written{id: created.ID, versions: 1}
}

// writeSpecVersion writes version number of issue's primary document, of specMarkdown, and its
// artifact.version event, in one transaction, and answers the event, which starts one version.
func writeSpecVersion(t *testing.T, database *store.Store, broker *events.Broker, issue specIssue, number int) written {
	t.Helper()
	ctx := t.Context()
	spec := specMarkdown(issue.n, number)
	tx, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin version %d of %s's primary document: %v", number, issue.key, err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `insert into artifact_versions (artifact_id, number, markdown, authors) values ($1::uuid, $2, $3, '[]')`,
		issue.artifact, number, spec); err != nil {
		t.Fatalf("insert version %d of %s's primary document: %v", number, issue.key, err)
	}
	key := issue.key
	appended, err := broker.Append(ctx, tx, model.Event{IssueKey: &key, Type: "artifact.version", Actor: model.Actor{Kind: "user", ID: "alice"},
		Payload: map[string]any{"artifact_id": issue.artifact, "version": map[string]any{"number": number, "size": len(spec)}}})
	if err != nil {
		t.Fatalf("append the artifact.version of %s: %v", issue.key, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit version %d of %s's primary document: %v", number, issue.key, err)
	}
	return written{id: appended.ID, versions: 1}
}

// commitEvents appends events through the broker in one transaction, in order, and answers their
// ids.
func commitEvents(t *testing.T, database *store.Store, broker *events.Broker, appended ...model.Event) []int64 {
	t.Helper()
	ctx := t.Context()
	tx, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the events: %v", err)
	}
	defer tx.Rollback(ctx)
	ids := make([]int64, 0, len(appended))
	if err := store.RunWrite(ctx, tx, func(batch *store.WriteBatch) error {
		for _, e := range appended {
			if err := broker.QueueAppend(batch, e, func(written model.Event) { ids = append(ids, written.ID) }); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("append the events: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit the events: %v", err)
	}
	return ids
}

// takeFillHold takes the fill's hold for a new holder, released once the test ends, and answers
// the holder.
func takeFillHold(t *testing.T, database *store.Store) uuid.UUID {
	t.Helper()
	holder := uuid.New()
	hold, err := duty.TryHold(t.Context(), database.Pool, fillHoldName, holder, holdOptions)
	if err != nil || hold == nil {
		t.Fatalf("take the fill's hold: %v, %v", hold, err)
	}
	t.Cleanup(func() { _ = hold.Release(context.Background()) })
	return holder
}

// holderText is holder as the cursor row's holder column reads as text, "" for none.
func holderText(holder uuid.UUID) string {
	if holder == uuid.Nil {
		return ""
	}
	return holder.String()
}

// testFiller is a fill on database's shared pool up to horizon, logging to logs at debug level,
// that does not rest between batches, so a test of many batches is not twice their length.
func testFiller(database *store.Store, horizon *events.Horizon, logs io.Writer) *filler {
	fill := newFiller(database.Pool, horizon)
	fill.logger = slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	fill.rest = func(context.Context, time.Duration) error { return nil }
	return fill
}

// fillProject is the project the issues writeIssue writes are in.
const fillProject = "ACME"

// seedFillProject writes project ACME.
func seedFillProject(t *testing.T, database *store.Store) {
	t.Helper()
	if _, err := database.Pool.Exec(t.Context(),
		`insert into projects (key, name, default_assignee) values ($1, 'Acme', 'alice')`, fillProject); err != nil {
		t.Fatalf("seed the project: %v", err)
	}
}

// written is an event a test wrote and how many keyword versions it starts.
type written struct {
	id       int64
	versions int
}

// writeIssue writes issue ACME-n with its primary document at versions 1 and 2, and fourteen
// events of it in id order, in one transaction, the events appended through the broker as the
// routes append them: one of every type a rule reads, each changing the text it reads, so each
// starts one version, and two that start none, an issue.updated that keeps the title and an
// issue.claimed, a type no rule reads. It answers each event with the versions it starts.
func writeIssue(t *testing.T, database *store.Store, broker *events.Broker, n int) []written {
	t.Helper()
	ctx := t.Context()
	key := fmt.Sprintf("%s-%d", fillProject, n)
	title := fmt.Sprintf("Rotate the signing keys of region %d", n)
	tx, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin issue %s: %v", key, err)
	}
	defer tx.Rollback(ctx)
	var artifact string
	if _, err := tx.Exec(ctx, `
		insert into issues (key, project_key, number, title, created_by, rank)
		values ($1, $2, $3, $4, '{"kind":"user","id":"alice"}', 'A')
	`, key, fillProject, n, title); err != nil {
		t.Fatalf("insert issue %s: %v", key, err)
	}
	if err := tx.QueryRow(ctx, `
		insert into artifacts (issue_key, project_key, slug, name, kind, is_primary, created_by)
		values ($1, $2, 'spec', 'spec.md', 'doc', true, '{"kind":"user","id":"alice"}')
		returning id::text
	`, key, fillProject).Scan(&artifact); err != nil {
		t.Fatalf("insert the primary document of %s: %v", key, err)
	}
	second := fmt.Sprintf("# Rotation %d\n\nMove each signing key of region %d to its successor, then audit the verifiers.", n, n)
	if _, err := tx.Exec(ctx, `
		insert into artifact_versions (artifact_id, number, markdown, authors)
		values ($1::uuid, 1, $2, '[]'), ($1::uuid, 2, $3, '[]')
	`, artifact, fmt.Sprintf("# Rotation %d\n\nMove each signing key to its successor.", n), second); err != nil {
		t.Fatalf("insert the versions of %s's primary document: %v", key, err)
	}

	actor := model.Actor{Kind: "user", ID: "alice"}
	issueKey := key
	comment, message, ask := uuid.NewString(), uuid.NewString(), uuid.NewString()
	issueEvent := func(eventType, title string) model.Event {
		return model.Event{IssueKey: &issueKey, Type: eventType, Actor: actor,
			Payload: map[string]any{"key": key, "title": title, "primary_artifact_id": artifact}}
	}
	post := func(eventType, id, body string) model.Event {
		return model.Event{IssueKey: &issueKey, Type: eventType, Actor: actor,
			Payload: map[string]any{"id": id, "issue_key": key, "body": body}}
	}
	askEvent := func(eventType, question string, answer *model.AskAnswer) model.Event {
		return model.Event{IssueKey: &issueKey, Type: eventType, Actor: actor, Payload: model.AskEventPayload{Ask: model.Ask{
			ID: ask, IssueKey: &issueKey, Author: actor, Kind: "question", Question: question, State: "open",
			Options: []model.AskOption{{Label: "Rotate now"}, {Label: "Wait a week"}}, Answer: answer,
		}}}
	}
	note := "Rotate them before the audit."
	steps := []struct {
		event    model.Event
		versions int
	}{
		{issueEvent("issue.created", title), 1},
		{model.Event{IssueKey: &issueKey, Type: "artifact.version", Actor: actor,
			Payload: map[string]any{"artifact_id": artifact, "version": map[string]any{"number": 2, "size": len(second)}}}, 1},
		{issueEvent("issue.updated", title+", then audit"), 1},
		{issueEvent("issue.updated", title+", then audit"), 0},
		{model.Event{IssueKey: &issueKey, Type: "issue.claimed", Actor: actor, Payload: map[string]any{"key": key}}, 0},
		{issueEvent("issue.closed", title+", audited"), 1},
		{post("comment.created", comment, "The verifiers read the old key until Monday."), 1},
		{post("comment.answered", uuid.NewString(), "Monday works; the audit starts Tuesday."), 1},
		{post("comment.edited", comment, "The verifiers read the old key until Tuesday."), 1},
		{post("message.created", message, "Can the rotation wait for the freeze to end?"), 1},
		{post("message.answered", uuid.NewString(), "It can; the freeze ends Friday."), 1},
		{askEvent("ask.opened", "Rotate the keys now?", nil), 1},
		{askEvent("ask.edited", "Rotate the signing keys now?", nil), 1},
		{askEvent("ask.answered", "Rotate the signing keys now?",
			&model.AskAnswer{User: "alice", Selected: []string{"Rotate now"}, Text: &note, At: time.Now()}), 1},
	}
	var out []written
	if err := store.RunWrite(ctx, tx, func(batch *store.WriteBatch) error {
		for _, step := range steps {
			versions := step.versions
			if err := broker.QueueAppend(batch, step.event, func(appended model.Event) {
				out = append(out, written{id: appended.ID, versions: versions})
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("append the events of %s: %v", key, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit issue %s: %v", key, err)
	}
	return out
}

// committedEvent is an event a test committed and the start of the transaction that wrote it,
// which is its created_at.
type committedEvent struct {
	id        int64
	startedAt time.Time
}

// otherIssue is a second issue of theIssue's project (seedOtherIssue): an event of it takes no
// lock an open transaction holds on theIssue's events.
const otherIssue = "ACME-2"

// seedOtherIssue writes otherIssue, beside theIssue.
func seedOtherIssue(t *testing.T, database *store.Store) {
	t.Helper()
	if _, err := database.Pool.Exec(t.Context(), `
		insert into issues (key, project_key, number, title, created_by, rank)
		values ($1, 'ACME', 2, 'Audit the verifiers', '{"kind":"user","id":"alice"}', 'B')
	`, otherIssue); err != nil {
		t.Fatalf("seed issue %s: %v", otherIssue, err)
	}
}

// commitComment commits a comment.created event of a new comment on issue, in a transaction of
// its own, the event appended through the broker, and answers it. Two transactions that append an
// event of one issue take turns on that issue's lock, so a test that holds one open while another
// commits gives them two issues.
func commitComment(t *testing.T, database *store.Store, broker *events.Broker, issue string) committedEvent {
	t.Helper()
	tx, startedAt := beginTransaction(t, database)
	defer tx.Rollback(context.Background())
	actor := model.Actor{Kind: "user", ID: "alice"}
	appended, err := broker.Append(t.Context(), tx, model.Event{IssueKey: &issue, Type: "comment.created", Actor: actor,
		Payload: model.CommentEventPayload{Comment: model.Comment{
			ID: uuid.NewString(), IssueKey: &issue, Author: actor, Body: "The signing keys rotate on Monday.", CreatedAt: startedAt,
			Mentions: []model.Mention{}, Deliveries: []model.CommentDelivery{},
		}}})
	if err != nil {
		t.Fatalf("append a comment.created of %s: %v", issue, err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatalf("commit the comment's event: %v", err)
	}
	return committedEvent{id: appended.ID, startedAt: startedAt}
}

// beginTransaction begins a transaction on database and reads its start, which is the
// xact_start pg_stat_activity shows for it and the created_at of every event it writes.
func beginTransaction(t *testing.T, database *store.Store) (pgx.Tx, time.Time) {
	t.Helper()
	tx, err := database.Pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin a transaction: %v", err)
	}
	var startedAt time.Time
	if err := tx.QueryRow(t.Context(), `select now()`).Scan(&startedAt); err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatalf("read the transaction's start: %v", err)
	}
	return tx, startedAt
}

// requireRowsOf holds the keyword table to exactly the rows of want: each event as many rows as
// it starts, and no row of any other event.
func requireRowsOf(t *testing.T, database *store.Store, want []written) {
	t.Helper()
	rows, err := database.Pool.Query(t.Context(), `select event_id, count(*) from search_text_versions group by event_id`)
	if err != nil {
		t.Fatalf("count the rows of each event: %v", err)
	}
	got := map[int64]int{}
	for rows.Next() {
		var id int64
		var count int
		if err := rows.Scan(&id, &count); err != nil {
			t.Fatalf("count the rows of each event: %v", err)
		}
		got[id] = count
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("count the rows of each event: %v", err)
	}
	expected := map[int64]int{}
	for _, w := range want {
		if w.versions > 0 {
			expected[w.id] = w.versions
		}
	}
	ids := slices.Collect(maps.Keys(got))
	for id := range expected {
		if _, ok := got[id]; !ok {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	var wrong []string
	for _, id := range ids {
		if got[id] != expected[id] {
			wrong = append(wrong, fmt.Sprintf("event %d: %d rows, want %d", id, got[id], expected[id]))
		}
	}
	if len(wrong) > 0 {
		t.Fatalf("%d events have the wrong rows, the first: %v", len(wrong), wrong[:min(len(wrong), 10)])
	}
}

// rowsOfEvents counts the keyword table's rows of the events ids.
func rowsOfEvents(t *testing.T, database *store.Store, ids ...int64) int {
	t.Helper()
	var count int
	if err := database.Pool.QueryRow(t.Context(), `select count(*) from search_text_versions where event_id = any($1)`, ids).Scan(&count); err != nil {
		t.Fatalf("count the rows of events %v: %v", ids, err)
	}
	return count
}

// countRows counts the keyword table's rows.
func countRows(t *testing.T, database *store.Store) int {
	t.Helper()
	var count int
	if err := database.Pool.QueryRow(t.Context(), `select count(*) from search_text_versions`).Scan(&count); err != nil {
		t.Fatalf("count the keyword table's rows: %v", err)
	}
	return count
}

// cursorRow is the fill's cursor row, its holder as text ("" for none).
type cursorRow struct {
	eventID   int64
	eventAt   *time.Time
	reach     *time.Time
	holder    string
	rules     int
	updatedAt time.Time
}

// equal is whether r and other hold the same values.
func (r cursorRow) equal(other cursorRow) bool {
	sameTime := func(a, b *time.Time) bool {
		if a == nil || b == nil {
			return a == b
		}
		return a.Equal(*b)
	}
	return r.eventID == other.eventID && sameTime(r.eventAt, other.eventAt) && sameTime(r.reach, other.reach) &&
		r.holder == other.holder && r.rules == other.rules && r.updatedAt.Equal(other.updatedAt)
}

// readCursorRow reads the fill's cursor row, which must be there.
func readCursorRow(t *testing.T, database *store.Store) cursorRow {
	t.Helper()
	var row cursorRow
	if err := database.Pool.QueryRow(t.Context(), `
		select event_id, event_at, reach, coalesce(holder::text, ''), rules, updated_at
		  from search_text_versions_fill where id = 1
	`).Scan(&row.eventID, &row.eventAt, &row.reach, &row.holder, &row.rules, &row.updatedAt); err != nil {
		t.Fatalf("read the fill's cursor row: %v", err)
	}
	return row
}

// logLine is what the tests read of one line a fill logged.
type logLine struct {
	Msg    string `json:"msg"`
	First  int64  `json:"first_event"`
	Cursor int64  `json:"cursor"`
	Events int    `json:"events"`
}

// logLines reads every line of logs, a JSON handler's output, in order.
func logLines(t *testing.T, logs *bytes.Buffer) []logLine {
	t.Helper()
	var lines []logLine
	scanner := bufio.NewScanner(bytes.NewReader(logs.Bytes()))
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		var line logLine
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			t.Fatalf("read a logged line %q: %v", scanner.Text(), err)
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read the logged lines: %v", err)
	}
	return lines
}

// batchLines is the batch lines of logs, in order.
func batchLines(t *testing.T, logs *bytes.Buffer) []logLine {
	t.Helper()
	var batches []logLine
	for _, line := range logLines(t, logs) {
		if line.Msg == batchMessage {
			batches = append(batches, line)
		}
	}
	return batches
}
