package textversions

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/dispatch/events"
	"github.com/sjawhar/envoy/internal/dispatch/itemblocks"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// TestACutCommentBodyIsRebuiltFromItsFirstRevision writes a comment longer than an event carries,
// three paragraphs and a list, as the create route writes one: its row and its block index in
// its own transaction, with its comment.created event, whose body is cut. An edit then inserts a
// block before the first, so the comment's row no longer holds the body the event cut. The
// comment.created version takes its body whole from revision 1 of the comment's blocks, in their
// order, and the row written with insertVersions holds the vector of the body the comment was
// written with.
func TestACutCommentBodyIsRebuiltFromItsFirstRevision(t *testing.T) {
	ctx := t.Context()
	database := storetest.Open(t)
	seedTheIssue(t, database)
	broker := events.NewBroker()

	first := longCommentBody()
	if units := itemblocks.Units16(first); units <= contracts.DispatchEventBodyMax {
		t.Fatalf("the comment's body is %d UTF-16 units, which an event carries whole; it must be over %d",
			units, contracts.DispatchEventBodyMax)
	}
	comment := writeComment(t, database, broker, first, true)
	edited := "A new opening paragraph, written before the others once the rotation was planned.\n\n" + first
	edit := editComment(t, database, broker, comment.id, edited)

	var logs bytes.Buffer
	got := decideAndWrite(t, database, &logs, readLoggedEvents(t, database, comment.event, edit))

	created := startedBy(t, got, comment.event)
	cut, truncated := model.EventBody(first)
	if !truncated || !created.BodyTruncated || created.Text == nil || *created.Text != cut {
		t.Fatalf("the comment.created event does not carry the cut body it should test (truncated %v, the version's body_truncated %v)",
			truncated, created.BodyTruncated)
	}
	blocks := itemblocks.Split(first)
	texts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		texts = append(texts, block.Text)
	}
	if len(texts) != 4 {
		t.Fatalf("the comment's body splits into %d blocks, want its three paragraphs and its list", len(texts))
	}
	want := strings.Join(texts, "\n\n")
	if created.BodyFrom != bodyFromBlocks {
		t.Fatalf("the comment.created version's body_from is %q, want %q", created.BodyFrom, bodyFromBlocks)
	}
	if created.Body != want {
		t.Fatalf("the comment.created version's body is %d bytes starting %q, want revision 1's %d blocks in their order, %d bytes starting %q",
			len(created.Body), abridged(created.Body, 80), len(texts), len(want), abridged(want, 80))
	}
	if after := startedBy(t, got, edit); after.BodyFrom != bodyFromRow || after.Body != edited {
		t.Fatalf("the comment.edited version holds %d bytes from %q, want the edited body's %d from %q",
			len(after.Body), after.BodyFrom, len(edited), bodyFromRow)
	}
	if got.Cut != 0 || logs.Len() != 0 {
		t.Fatalf("the batch kept %d cut bodies and logged %q, want none", got.Cut, logs.String())
	}

	var body, bodyFrom string
	var equal bool
	if err := database.Pool.QueryRow(ctx, `
		select body, body_from, search = search_vector('', search_text($3))
		  from search_text_versions where kind = 'comment' and id = $1 and event_id = $2
	`, comment.id, comment.event, first).Scan(&body, &bodyFrom, &equal); err != nil {
		t.Fatalf("read the comment.created row: %v", err)
	}
	if body != want || bodyFrom != bodyFromBlocks {
		t.Fatalf("the comment.created row holds %d bytes from %q, want revision 1's %d bytes from %q",
			len(body), bodyFrom, len(want), bodyFromBlocks)
	}
	if !equal {
		t.Fatal("the comment.created row's vector is not search_vector('', search_text(<the body the comment was written with>))")
	}
}

// TestACutBodyNoSourceHoldsKeepsItsEventsText holds the last source of a cut body: where neither
// the comment's present row nor revision 1 of its blocks holds the body an event cut, the
// version keeps the event's text, says so in body_from, counts in the batch's cut count and is
// named, with its event, in a warning.
func TestACutBodyNoSourceHoldsKeepsItsEventsText(t *testing.T) {
	database := storetest.Open(t)
	seedTheIssue(t, database)
	broker := events.NewBroker()
	long := longCommentBody()

	t.Run("revision 1's blocks were made in another transaction than the comment", func(t *testing.T) {
		// A comment an older build wrote, whose blocks the reconciliation indexed later in a
		// transaction of its own; an edit then leaves its row holding another body.
		comment := writeComment(t, database, broker, long, false)
		indexComment(t, database, comment.id, long)
		edit := editComment(t, database, broker, comment.id, "The plan moved to the issue's spec.\n\n"+long)

		var logs bytes.Buffer
		got := decideAndWrite(t, database, &logs, readLoggedEvents(t, database, comment.event, edit))
		requireKeptEventText(t, database, got, &logs, comment.id, comment.event, long)
	})

	t.Run("a cut comment.edited version followed by a second edit", func(t *testing.T) {
		comment := writeComment(t, database, broker, "Rotate the staging key first.", true)
		firstEdit := editComment(t, database, broker, comment.id, long)
		secondEdit := editComment(t, database, broker, comment.id, "Rotated; the long plan is in the spec now.")

		var logs bytes.Buffer
		got := decideAndWrite(t, database, &logs, readLoggedEvents(t, database, comment.event, firstEdit, secondEdit))
		if len(got.Versions) != 3 {
			t.Fatalf("the comment's three events start %d versions, want 3", len(got.Versions))
		}
		requireKeptEventText(t, database, got, &logs, comment.id, firstEdit, long)
	})
}

// TestACutEditThatKeepsTheBodyStartsNoVersion writes a comment longer than an event carries, and
// a batch writes its comment.created version, whole from the comment's row. In a later batch, so
// the comment's version is in the table and not in the batch, a comment.edited whose event cuts
// the body writes the same body again. Its whole body, read from the row, equals the comment's
// latest version's, so it starts no version, and the table keeps the one row.
func TestACutEditThatKeepsTheBodyStartsNoVersion(t *testing.T) {
	database := storetest.Open(t)
	seedTheIssue(t, database)
	broker := events.NewBroker()
	long := longCommentBody()
	comment := writeComment(t, database, broker, long, true)
	var logs bytes.Buffer
	first := decideAndWrite(t, database, &logs, readLoggedEvents(t, database, comment.event))
	if created := startedBy(t, first, comment.event); created.BodyFrom != bodyFromRow || created.Body != long {
		t.Fatalf("the comment.created version holds %d bytes from %q, want the comment's %d from %q",
			len(created.Body), created.BodyFrom, len(long), bodyFromRow)
	}

	edit := editComment(t, database, broker, comment.id, long)
	edited := readLoggedEvents(t, database, edit)
	if _, truncated := model.EventBody(long); !truncated {
		t.Fatal("the body is one an event carries whole, which tests no cut")
	}
	got := decideAndWrite(t, database, &logs, edited)
	if len(got.Versions) != 0 {
		t.Fatalf("the cut comment.edited that keeps the body starts %d versions (the first from %q), want none",
			len(got.Versions), got.Versions[0].BodyFrom)
	}
	var rows int
	if err := database.Pool.QueryRow(t.Context(),
		`select count(*) from search_text_versions where kind = 'comment' and id = $1`, comment.id).Scan(&rows); err != nil {
		t.Fatalf("count the comment's rows: %v", err)
	}
	if rows != 1 {
		t.Fatalf("the comment has %d rows, want the one of its comment.created", rows)
	}
}

// requireKeptEventText holds the version event starts of comment id, whose body was body when
// the event was written, to the event's cut text: body_from cut, as decided and as written, one
// cut body in the batch, and one warning, naming the comment and the event.
func requireKeptEventText(t *testing.T, database *store.Store, got decided, logs *bytes.Buffer, id string, event int64, body string) {
	t.Helper()
	cut, truncated := model.EventBody(body)
	if !truncated {
		t.Fatal("the body is one an event carries whole, which tests no cut")
	}
	v := startedBy(t, got, event)
	if !v.BodyTruncated || v.BodyFrom != bodyFromCut || v.Body != cut {
		t.Fatalf("the version of event %d holds %d bytes from %q (body_truncated %v), want its event's cut %d bytes from %q",
			event, len(v.Body), v.BodyFrom, v.BodyTruncated, len(cut), bodyFromCut)
	}
	if got.Cut != 1 {
		t.Fatalf("the batch counts %d cut bodies, want 1", got.Cut)
	}
	var stored, bodyFrom string
	if err := database.Pool.QueryRow(t.Context(), `
		select body, body_from from search_text_versions where kind = 'comment' and id = $1 and event_id = $2
	`, id, event).Scan(&stored, &bodyFrom); err != nil {
		t.Fatalf("read the row of event %d: %v", event, err)
	}
	if stored != cut || bodyFrom != bodyFromCut {
		t.Fatalf("the row of event %d holds %d bytes from %q, want its event's cut %d bytes from %q",
			event, len(stored), bodyFrom, len(cut), bodyFromCut)
	}
	warnings := loggedWarnings(t, logs)
	if len(warnings) != 1 {
		t.Fatalf("the batch logged %d warnings, want the one of the cut body: %q", len(warnings), logs.String())
	}
	if warnings[0]["kind"] != "comment" || warnings[0]["id"] != id || warnings[0]["event_id"] != fmt.Sprint(event) {
		t.Fatalf("the warning names %v %v at event %v, want comment %s at event %d: %q",
			warnings[0]["kind"], warnings[0]["id"], warnings[0]["event_id"], id, event, logs.String())
	}
}

// theIssue is the issue every comment of these tests is written on.
const theIssue = "ACME-1"

// seedTheIssue writes project ACME and its issue ACME-1.
func seedTheIssue(t *testing.T, database *store.Store) {
	t.Helper()
	for _, statement := range []string{
		`insert into projects (key, name, default_assignee) values ('ACME', 'Acme', 'alice')`,
		`insert into issues (key, project_key, number, title, created_by, rank)
		 values ('ACME-1', 'ACME', 1, 'Rotate the signing keys', '{"kind":"user","id":"alice"}', 'A')`,
	} {
		if _, err := database.Pool.Exec(t.Context(), statement); err != nil {
			t.Fatalf("seed the issue: %v", err)
		}
	}
}

// longCommentBody is a body over contracts.DispatchEventBodyMax UTF-16 units: three paragraphs of
// about 10,500 units each, which together fit in an event, and a list after them, which does not,
// so its event carries the three paragraphs alone. Each block is under
// contracts.DispatchBlockUnits, so the create route would take it.
func longCommentBody() string {
	paragraph := func(topic string) string {
		var b strings.Builder
		for number := 1; b.Len() < 10_500; number++ {
			fmt.Fprintf(&b, "The %s rotation step %d moves one signing key to its successor. ", topic, number)
		}
		return strings.TrimSpace(b.String())
	}
	var list strings.Builder
	for number := 1; number <= 60; number++ {
		if number > 1 {
			list.WriteString("\n")
		}
		fmt.Fprintf(&list, "- delta check %d confirms the verifier reads the new key", number)
	}
	return strings.Join([]string{paragraph("alpha"), paragraph("bravo"), paragraph("charlie"), list.String()}, "\n\n")
}

// writtenComment is a comment writeComment wrote and the id of its comment.created event.
type writtenComment struct {
	id    string
	event int64
}

// writeComment writes a comment of body on theIssue with its comment.created event, whose body
// the event carries as model.EventBody cuts it, in one transaction, as the create route does;
// with indexed, its block index too, in that same transaction (refs.IndexItem), as every route
// that writes a comment does, and without, none, as a build before the block index wrote one.
func writeComment(t *testing.T, database *store.Store, broker *events.Broker, body string, indexed bool) writtenComment {
	t.Helper()
	ctx := t.Context()
	tx, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the comment's transaction: %v", err)
	}
	defer tx.Rollback(ctx)
	var comment writtenComment
	var createdAt time.Time
	if err := tx.QueryRow(ctx, `
		insert into comments (issue_key, author, body) values ($1, '{"kind":"user","id":"alice"}', $2)
		returning id::text, created_at
	`, theIssue, body).Scan(&comment.id, &createdAt); err != nil {
		t.Fatalf("insert the comment: %v", err)
	}
	if indexed {
		if _, err := refs.IndexItem(ctx, tx, refs.ItemComment, comment.id, body, createdAt, nil, nil); err != nil {
			t.Fatalf("index the comment's blocks: %v", err)
		}
	}
	comment.event = appendCommentEvent(t, tx, broker, "comment.created", comment.id, body, createdAt)
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit the comment: %v", err)
	}
	return comment
}

// indexComment indexes the blocks of comment id, whose body is body, in a transaction of its own,
// as the block index's reconciliation indexes a comment a build without the index wrote.
func indexComment(t *testing.T, database *store.Store, id, body string) {
	t.Helper()
	ctx := t.Context()
	tx, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the index's transaction: %v", err)
	}
	defer tx.Rollback(ctx)
	var createdAt time.Time
	if err := tx.QueryRow(ctx, `select created_at from comments where id = $1::uuid`, id).Scan(&createdAt); err != nil {
		t.Fatalf("read the comment: %v", err)
	}
	if _, err := refs.IndexItem(ctx, tx, refs.ItemComment, id, body, createdAt, nil, nil); err != nil {
		t.Fatalf("index the comment's blocks: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit the index: %v", err)
	}
}

// editComment writes body as comment id's new body, its edited_at its transaction's now(), its
// block index and its comment.edited event, in one transaction as the edit route does, and
// answers the event's id.
func editComment(t *testing.T, database *store.Store, broker *events.Broker, id, body string) int64 {
	t.Helper()
	ctx := t.Context()
	tx, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the edit's transaction: %v", err)
	}
	defer tx.Rollback(ctx)
	var editedAt time.Time
	if err := tx.QueryRow(ctx, `
		update comments set body = $2, edited_at = now() where id = $1::uuid returning edited_at
	`, id, body).Scan(&editedAt); err != nil {
		t.Fatalf("edit the comment: %v", err)
	}
	if _, err := refs.IndexItem(ctx, tx, refs.ItemComment, id, body, editedAt, nil, nil); err != nil {
		t.Fatalf("index the edited comment's blocks: %v", err)
	}
	event := appendCommentEvent(t, tx, broker, "comment.edited", id, body, editedAt)
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit the edit: %v", err)
	}
	return event
}

// appendCommentEvent appends the event of type of comment id on theIssue, whose body is body,
// through the broker, and answers its id.
func appendCommentEvent(t *testing.T, tx pgx.Tx, broker *events.Broker, eventType, id, body string, createdAt time.Time) int64 {
	t.Helper()
	issue := theIssue
	actor := model.Actor{Kind: "user", ID: "alice"}
	payload := model.CommentEventPayload{Comment: model.Comment{
		ID: id, IssueKey: &issue, Author: actor, CreatedAt: createdAt, Mentions: []model.Mention{}, Deliveries: []model.CommentDelivery{},
	}}
	payload.Body, payload.BodyTruncated = model.EventBody(body)
	appended, err := broker.Append(t.Context(), tx, model.Event{IssueKey: &issue, Type: eventType, Actor: actor, Payload: payload})
	if err != nil {
		t.Fatalf("append %s: %v", eventType, err)
	}
	return appended.ID
}

// readLoggedEvents reads the events ids from the log as the fill reads it: the columns of each
// row the rules read, its project as the log names it, in id order.
func readLoggedEvents(t *testing.T, database *store.Store, ids ...int64) []event {
	t.Helper()
	rows, err := database.Pool.Query(t.Context(), `
		select e.id, e.type, e.issue_key, coalesce(e.project_key, i.project_key, ar.project_key, ''), e.seq, e.created_at, e.payload
		  from events e
		  left join issues i on i.key = e.issue_key
		  left join artifacts ar on ar.id = e.artifact_id
		 where e.id = any($1) order by e.id
	`, ids)
	if err != nil {
		t.Fatalf("read the events: %v", err)
	}
	defer rows.Close()
	var read []event
	for rows.Next() {
		var e event
		var payload []byte
		if err := rows.Scan(&e.ID, &e.Type, &e.IssueKey, &e.Project, &e.Seq, &e.CreatedAt, &payload); err != nil {
			t.Fatalf("read an event: %v", err)
		}
		e.Payload = payload
		read = append(read, e)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the events: %v", err)
	}
	if len(read) != len(ids) {
		t.Fatalf("read %d of the %d events", len(read), len(ids))
	}
	return read
}

// decideAndWrite decides the versions of a batch of events and writes them, in one transaction,
// as the fill's batch does, with the warnings logged to logs.
func decideAndWrite(t *testing.T, database *store.Store, logs *bytes.Buffer, batch []event) decided {
	t.Helper()
	ctx := t.Context()
	tx, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the batch: %v", err)
	}
	defer tx.Rollback(ctx)
	got, _, err := decideVersions(ctx, tx, slog.New(slog.NewJSONHandler(logs, nil)), batch)
	if err != nil {
		t.Fatalf("decide the versions: %v", err)
	}
	if err := insertVersions(ctx, tx, got.Versions); err != nil {
		t.Fatalf("write the versions: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit the batch: %v", err)
	}
	return got
}

// startedBy is the version event started in got.
func startedBy(t *testing.T, got decided, event int64) version {
	t.Helper()
	for _, v := range got.Versions {
		if v.EventID == event {
			return v
		}
	}
	t.Fatalf("event %d starts no version; the batch decided %d", event, len(got.Versions))
	return version{}
}

// loggedWarnings is each WARN record of logs, a JSON handler's output, with every attribute as
// its text.
func loggedWarnings(t *testing.T, logs *bytes.Buffer) []map[string]string {
	t.Helper()
	var warnings []map[string]string
	lines := bufio.NewScanner(bytes.NewReader(logs.Bytes()))
	lines.Buffer(nil, 1<<20)
	for lines.Scan() {
		var record map[string]any
		decoder := json.NewDecoder(bytes.NewReader(lines.Bytes()))
		decoder.UseNumber()
		if err := decoder.Decode(&record); err != nil {
			t.Fatalf("read a log record: %v: %q", err, lines.Text())
		}
		if record["level"] != "WARN" {
			continue
		}
		attributes := make(map[string]string, len(record))
		for key, value := range record {
			attributes[key] = fmt.Sprint(value)
		}
		warnings = append(warnings, attributes)
	}
	if err := lines.Err(); err != nil {
		t.Fatalf("read the log: %v", err)
	}
	return warnings
}
