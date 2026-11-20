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

// TestTheFillWritesANewEventWithinSixtySecondsOfTheHorizon holds the fill to R3's bound where
// nothing announces the moment the horizon moves: with the fill caught up, a transaction that
// holds a transaction id and writes no event stays open while three events commit, so the horizon
// stands below them, and then ends with no notification. The fill reads the horizon again on its
// own, 1 s and then up to 8 s apart, and the three events have their rows within 60 s of the
// moment Horizon.Await answers an id at or above them: under the fill's hold, and with no holder.
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
				fill.holder, fill.holdName = holder, fillHoldName
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
	hold, err := duty.TryHold(ctx, database.Pool, fillHoldName, holder, fillHoldOptions)
	if err != nil || hold == nil {
		t.Fatalf("take the fill's hold: %v, %v", hold, err)
	}
	t.Cleanup(func() { _ = hold.Release(context.Background()) })
	fill := testFiller(database, events.NewHorizon(database.Pool), io.Discard)
	fill.holder, fill.holdName, fill.maxEvents = holder, fillHoldName, 14
	var afterFirst cursorRow
	var rowsAfterFirst int
	fill.rest = func(ctx context.Context, _ time.Duration) error {
		if afterFirst.eventID != 0 {
			return nil
		}
		if err := hold.Lapse(ctx); err != nil {
			t.Fatalf("let the fill's hold lapse: %v", err)
		}
		taken, err := duty.TryHold(ctx, database.Pool, fillHoldName, uuid.New(), fillHoldOptions)
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

// fillHoldOptions are the fill hold's TTL, renewal and margin: those of the delivery intakes.
var fillHoldOptions = duty.HoldOptions{TTL: 15 * time.Second, Renew: 5 * time.Second, Margin: 2 * time.Second}

// takeFillHold takes the fill's hold for a new holder, released once the test ends, and answers
// the holder.
func takeFillHold(t *testing.T, database *store.Store) uuid.UUID {
	t.Helper()
	holder := uuid.New()
	hold, err := duty.TryHold(t.Context(), database.Pool, fillHoldName, holder, fillHoldOptions)
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

// The messages of the fill's lines the tests read.
const (
	batchMessage = "dispatch: the keyword fill wrote a batch"
	resetMessage = "dispatch: the event log was reset under the keyword fill's cursor; the fill emptied its table and starts again from the first event"
)

// logLine is what the tests read of one line a fill logged.
type logLine struct {
	Msg    string `json:"msg"`
	Events int    `json:"events"`
	First  int64  `json:"first_event"`
	Cursor int64  `json:"cursor"`
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
