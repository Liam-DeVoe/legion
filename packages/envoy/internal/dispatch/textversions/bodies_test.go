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
	"github.com/sjawhar/envoy/internal/dispatch/textversions/textversionstest"
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
	textversionstest.SeedIssue(t, database)
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
	_, truncated := model.EventBody(first)
	if !truncated || !created.BodyTruncated {
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
// named, with its event, in a warning, once: a batch whose bound leaves the event to the next
// batch does not warn of it.
func TestACutBodyNoSourceHoldsKeepsItsEventsText(t *testing.T) {
	database := storetest.Open(t)
	textversionstest.SeedIssue(t, database)
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

		// A batch bound to 1 byte decides the comment.created alone and leaves the cut edit to
		// the next batch, which is the one that warns of it.
		batch := readLoggedEvents(t, database, comment.event, firstEdit, secondEdit)
		var logs bytes.Buffer
		tx, err := database.Pool.Begin(t.Context())
		if err != nil {
			t.Fatalf("begin the bounded batch: %v", err)
		}
		_, bounded, err := decideVersions(t.Context(), tx, slog.New(slog.NewJSONHandler(&logs, nil)), batch, 1)
		_ = tx.Rollback(t.Context())
		if err != nil || bounded != 1 {
			t.Fatalf("the batch bound to 1 byte decided %d of the comment's events (%v), want its comment.created alone", bounded, err)
		}
		got := decideAndWrite(t, database, &logs, batch)
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
	textversionstest.SeedIssue(t, database)
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

// TestAReversedPairOfCutEditsKeepsEachEditsText writes a comment and then two edits of it whose
// bodies are longer than an event carries, in transactions that interleave as a reversed pair:
// A begins, B begins, B writes the comment's row and appends its comment.edited and commits, and
// A then writes the row and appends its comment.edited and commits. B's event has the lower id
// and the later created_at, and the row holds A's body, with A's start as its edited_at, which is
// before B's created_at. The row holds an event's text only where no later event of the comment
// changes it, and A's edit does, so B's version keeps its event's cut text, since no source holds
// B's whole body any more; A's version, which no later event changes, takes A's whole body from
// the row.
func TestAReversedPairOfCutEditsKeepsEachEditsText(t *testing.T) {
	ctx := t.Context()
	database := storetest.Open(t)
	textversionstest.SeedIssue(t, database)
	broker := events.NewBroker()
	long := longCommentBody()
	aBody := "Edit A moves the rotation to the spring.\n\n" + long
	bBody := "Edit B moves the rotation to the autumn.\n\n" + long
	comment := writeComment(t, database, broker, "Rotate the staging key first.", true)

	a, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin transaction A: %v", err)
	}
	defer a.Rollback(ctx)
	b, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin transaction B: %v", err)
	}
	defer b.Rollback(ctx)
	bEvent := writeEdit(t, b, broker, comment.id, bBody)
	if err := b.Commit(ctx); err != nil {
		t.Fatalf("commit transaction B: %v", err)
	}
	aEvent := writeEdit(t, a, broker, comment.id, aBody)
	if err := a.Commit(ctx); err != nil {
		t.Fatalf("commit transaction A: %v", err)
	}
	var aAt, bAt, editedAt time.Time
	if err := database.Pool.QueryRow(ctx, `
		select (select created_at from events where id = $1), (select created_at from events where id = $2),
		       (select edited_at from comments where id = $3::uuid)
	`, aEvent, bEvent, comment.id).Scan(&aAt, &bAt, &editedAt); err != nil {
		t.Fatalf("read the times of the pair: %v", err)
	}
	if bEvent > aEvent || !bAt.After(aAt) || !editedAt.Equal(aAt) {
		t.Fatalf("the pair is not reversed: B's event %d at %v, A's event %d at %v, the row edited at %v; want B's id lower, B's time later and the row edited at A's",
			bEvent, bAt, aEvent, aAt, editedAt)
	}

	var logs bytes.Buffer
	got := decideAndWrite(t, database, &logs, readLoggedEvents(t, database, comment.event, bEvent, aEvent))
	aCut, _ := model.EventBody(aBody)
	bCut, _ := model.EventBody(bBody)
	holds := func(v version) string {
		switch v.Body {
		case aBody:
			return "A's whole body"
		case bBody:
			return "B's whole body"
		case aCut:
			return "A's cut text"
		case bCut:
			return "B's cut text"
		}
		return fmt.Sprintf("%d other bytes", len(v.Body))
	}
	versions := make([]string, 0, len(got.Versions))
	for _, v := range got.Versions {
		versions = append(versions, fmt.Sprintf("event %d: %s from %q", v.EventID, holds(v), v.BodyFrom))
	}
	if len(got.Versions) != 3 {
		t.Fatalf("the comment's comment.created (event %d), B's edit (event %d) and A's edit (event %d) start %d versions, want 3: %v",
			comment.event, bEvent, aEvent, len(got.Versions), versions)
	}
	requireKeptEventText(t, database, got, &logs, comment.id, bEvent, bBody)
	if v := startedBy(t, got, aEvent); v.BodyFrom != bodyFromRow || v.Body != aBody {
		t.Fatalf("A's version holds %s from %q, want A's whole body from %q", holds(v), v.BodyFrom, bodyFromRow)
	}
	var stored, bodyFrom string
	if err := database.Pool.QueryRow(ctx, `
		select body, body_from from search_text_versions where kind = 'comment' and id = $1 and event_id = $2
	`, comment.id, aEvent).Scan(&stored, &bodyFrom); err != nil {
		t.Fatalf("read the row of A's edit: %v", err)
	}
	if stored != aBody || bodyFrom != bodyFromRow {
		t.Fatalf("the row of A's edit holds %d bytes from %q, want A's whole %d bytes from %q", len(stored), bodyFrom, len(aBody), bodyFromRow)
	}
}

// TestAnIssueCreatedAfterItsFirstDocumentVersionIsWarnedOfOnce writes issue ACME-1 with version
// 1 of its primary document in a transaction of its own, before the one that appends its
// issue.created, so version 1 was not made at the event's created_at; and between the two, issue
// ACME-2 as the create route writes one, with version 1 in the transaction of its issue.created,
// so ACME-1's issue.created is second in the batch. A batch bound to 1 byte decides ACME-2's
// event alone and logs no warning; the whole batch then logs exactly one, which names ACME-1, its
// event and when its version 1 was made, and none of ACME-2.
func TestAnIssueCreatedAfterItsFirstDocumentVersionIsWarnedOfOnce(t *testing.T) {
	ctx := t.Context()
	database := storetest.Open(t)
	textversionstest.SeedProject(t, database)
	broker := events.NewBroker()

	first, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin ACME-1's transaction: %v", err)
	}
	defer first.Rollback(ctx)
	early := textversionstest.InsertIssue(t, first, 1, "Rotate the signing keys", "# Rotation\n\nMove each signing key to its successor.")
	if err := first.Commit(ctx); err != nil {
		t.Fatalf("commit ACME-1's transaction: %v", err)
	}
	create, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin ACME-2's create: %v", err)
	}
	defer create.Rollback(ctx)
	routed := textversionstest.InsertIssue(t, create, 2, "Audit the verifiers", "# Audit\n\nRead each verifier's key.")
	routedEvent := textversionstest.AppendEvents(t, create, broker, routed.Event("issue.created", routed.Title))[0]
	if err := create.Commit(ctx); err != nil {
		t.Fatalf("commit ACME-2's create: %v", err)
	}
	lateEvent := textversionstest.CommitEvents(t, database, broker, early.Event("issue.created", early.Title))[0]
	batch := readLoggedEvents(t, database, routedEvent, lateEvent)
	var made time.Time
	if err := database.Pool.QueryRow(ctx, `select created_at from artifact_versions where artifact_id = $1::uuid and number = 1`,
		early.Artifact).Scan(&made); err != nil {
		t.Fatalf("read ACME-1's first document version: %v", err)
	}
	if made.Equal(batch[1].CreatedAt) {
		t.Fatal("ACME-1's first document version was made at its issue.created's created_at, which tests no warning")
	}

	var logs bytes.Buffer
	bounded, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the bounded batch: %v", err)
	}
	_, decidedEvents, err := decideVersions(ctx, bounded, slog.New(slog.NewJSONHandler(&logs, nil)), batch, 1)
	_ = bounded.Rollback(ctx)
	if err != nil || decidedEvents != 1 {
		t.Fatalf("the batch bound to 1 byte decided %d of the two issue.created events (%v), want ACME-2's alone", decidedEvents, err)
	}
	if warnings := loggedWarnings(t, &logs); len(warnings) != 0 {
		t.Fatalf("the batch bound to 1 byte, which leaves ACME-1's issue.created to the next batch, logged %d warnings, want none: %q",
			len(warnings), logs.String())
	}
	if got := decideAndWrite(t, database, &logs, batch); len(got.Versions) != 2 {
		t.Fatalf("the two issue.created events start %d versions, want 2", len(got.Versions))
	}
	warnings := loggedWarnings(t, &logs)
	if len(warnings) != 1 {
		t.Fatalf("the batches logged %d warnings, want the one of ACME-1's first document version: %q", len(warnings), logs.String())
	}
	warned := warnings[0]
	at, err := time.Parse(time.RFC3339Nano, warned["version_created_at"])
	if !strings.Contains(warned["msg"], "did not write version 1 of its primary document") || warned["issue"] != early.Key ||
		warned["event_id"] != fmt.Sprint(lateEvent) || err != nil || !at.Equal(made) {
		t.Fatalf("the warning says %q of issue %s at event %s, version 1 made at %s; want the warning of %s's first document version at event %d, made at %s",
			warned["msg"], warned["issue"], warned["event_id"], warned["version_created_at"], early.Key, lateEvent, made.Format(time.RFC3339Nano))
	}
}

// TestAnIssueEventBeforeAnyVersionOfItsIssueIsWarnedOfOnce holds the two warnings of an issue
// event that meets its issue with no version, in the table or earlier in the batch, which only a
// log that lacks the issue's issue.created holds (seedIssue): of an issue whose rows are there,
// which its version reads, and of an issue whose rows are not, which starts no version. Each case
// is a batch whose first event, the issue.created of an issue written as the create route writes
// one, warns of nothing, and whose second is the issue.updated warned of: a batch bound to 1 byte
// decides the first event alone and logs no warning, and the whole batch then logs exactly one,
// which names the issue and the event. The cases are two batches rather than one of three events,
// since a bound that ends a batch at its second event never decides its third.
func TestAnIssueEventBeforeAnyVersionOfItsIssueIsWarnedOfOnce(t *testing.T) {
	ctx := t.Context()
	database := storetest.Open(t)
	textversionstest.SeedProject(t, database)
	broker := events.NewBroker()

	t.Run("an issue whose rows a reset of the log kept", func(t *testing.T) {
		// ACME-1's rows, with version 1 of its primary document, are written with no
		// issue.created, as a reset of the log that kept them leaves them. A retitle is then
		// written as the update route writes one, and version 2 of the document after it, so the
		// document's version at the retitle's time is not its latest.
		rows, _ := beginTransaction(t, database)
		defer rows.Rollback(ctx)
		issue := textversionstest.InsertIssue(t, rows, 1, "Rotate the signing keys", "# Rotation\n\nMove each signing key to its successor.")
		if err := rows.Commit(ctx); err != nil {
			t.Fatalf("commit %s's rows: %v", issue.Key, err)
		}
		created := createIssue(t, database, broker, 2, "Audit the verifiers", "# Audit\n\nRead each verifier's key.")
		retitle, _ := beginTransaction(t, database)
		defer retitle.Rollback(ctx)
		title := issue.Title + ", then audit the verifiers"
		if _, err := retitle.Exec(ctx, `update issues set title = $2 where key = $1`, issue.Key, title); err != nil {
			t.Fatalf("retitle %s: %v", issue.Key, err)
		}
		retitled := textversionstest.AppendEvents(t, retitle, broker, issue.Event("issue.updated", title))[0]
		if err := retitle.Commit(ctx); err != nil {
			t.Fatalf("commit %s's retitle: %v", issue.Key, err)
		}
		later, _ := beginTransaction(t, database)
		defer later.Rollback(ctx)
		textversionstest.AppendEvents(t, later, broker, issue.InsertVersion(t, later, 2, "# Rotation\n\nMove each signing key, then audit each verifier."))
		if err := later.Commit(ctx); err != nil {
			t.Fatalf("commit version 2 of %s's document: %v", issue.Key, err)
		}

		got, warned := requireWarnedOnceByItsBatch(t, database, readLoggedEvents(t, database, created, retitled),
			"found no earlier version of the issue in the log", issue.Key, retitled)
		if len(got.Versions) != 2 {
			t.Fatalf("the batch's two events start %d versions, want 2", len(got.Versions))
		}
		// The retitle's version holds the title its event carries, which the issue's row holds
		// too, and the version of the primary document of the issue's rows made by the event's
		// time; the warning names where its body came from.
		want := specVersion{artifact: issue.Artifact, number: 1}
		v := startedBy(t, got, retitled)
		if spec, _ := v.spec(); v.Body != title || v.BodyFrom != bodyFromEvent || spec != want {
			t.Fatalf("the retitle's version holds %q from %q and names version %d of document %q; want %q from %q and version %d of %q, the primary document's at the event's time",
				v.Body, v.BodyFrom, spec.number, spec.artifact, title, bodyFromEvent, want.number, want.artifact)
		}
		if warned["body_from"] != bodyFromEvent {
			t.Fatalf("the warning names body_from %q, want the version's %q", warned["body_from"], bodyFromEvent)
		}
	})

	t.Run("an issue the database does not hold", func(t *testing.T) {
		// An event's issue_key references issues, so an event whose own columns name an issue
		// names one the database holds: an issue event of an issue with no row names it in its
		// payload alone, here in an event of the project. ACME-4 is never written.
		created := createIssue(t, database, broker, 3, "Retire the old keys", "# Retirement\n\nRetire each key the rotation replaced.")
		project, missing := textversionstest.Project, textversionstest.Project+"-4"
		retitled := textversionstest.CommitEvents(t, database, broker, model.Event{ProjectKey: &project, Type: "issue.updated",
			Actor: model.Actor{Kind: "user", ID: "alice"}, Payload: map[string]any{"key": missing, "title": "Audit the verifiers again"}})[0]

		got, _ := requireWarnedOnceByItsBatch(t, database, readLoggedEvents(t, database, created, retitled),
			"names an issue the log never created and the database does not hold", missing, retitled)
		if len(got.Versions) != 1 {
			t.Fatalf("the batch's two events start %d versions, want the issue.created's alone", len(got.Versions))
		}
		startedBy(t, got, created)
	})
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

// requireWarnedOnceByItsBatch holds the warning of event, the second of batch, to the batch that
// decides event: batch bound to 1 byte decides its first event alone and logs no warning, and the
// whole batch, decided and written (decideAndWrite), then logs exactly one, whose message holds
// message and which names issue and event. It answers what the whole batch decided and the
// warning, with every attribute as its text.
func requireWarnedOnceByItsBatch(t *testing.T, database *store.Store, batch []event, message, issue string, event int64) (decided, map[string]string) {
	t.Helper()
	ctx := t.Context()
	var logs bytes.Buffer
	bounded, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the bounded batch: %v", err)
	}
	_, decidedEvents, err := decideVersions(ctx, bounded, slog.New(slog.NewJSONHandler(&logs, nil)), batch, 1)
	_ = bounded.Rollback(ctx)
	if err != nil || decidedEvents != 1 {
		t.Fatalf("the batch bound to 1 byte decided %d of its %d events (%v), want the first alone", decidedEvents, len(batch), err)
	}
	if warnings := loggedWarnings(t, &logs); len(warnings) != 0 {
		t.Fatalf("the batch bound to 1 byte, which leaves event %d to the next batch, logged %d warnings, want none: %q",
			event, len(warnings), logs.String())
	}
	got := decideAndWrite(t, database, &logs, batch)
	warnings := loggedWarnings(t, &logs)
	if len(warnings) != 1 {
		t.Fatalf("the batches logged %d warnings, want the one of event %d: %q", len(warnings), event, logs.String())
	}
	warned := warnings[0]
	if !strings.Contains(warned["msg"], message) || warned["issue"] != issue || warned["event_id"] != fmt.Sprint(event) {
		t.Fatalf("the warning says %q of issue %s at event %s; want one that says %q of issue %s at event %d",
			warned["msg"], warned["issue"], warned["event_id"], message, issue, event)
	}
	return got, warned
}

// createIssue writes issue ACME-n of textversionstest.Project with title and its primary document
// of markdown, and its issue.created event, in one transaction, as the create route writes one,
// so version 1 of the document is made at the event's created_at, and answers the event's id.
func createIssue(t *testing.T, database *store.Store, broker *events.Broker, n int, title, markdown string) int64 {
	t.Helper()
	ctx := t.Context()
	tx, _ := beginTransaction(t, database)
	defer tx.Rollback(ctx)
	issue := textversionstest.InsertIssue(t, tx, n, title, markdown)
	event := textversionstest.AppendEvents(t, tx, broker, issue.Event("issue.created", title))[0]
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit the create of %s: %v", issue.Key, err)
	}
	return event
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

// writeComment writes a comment of body on textversionstest.Issue with its comment.created event,
// whose body the event carries as model.EventBody cuts it, in one transaction, as the create route
// does; with indexed, its block index too, in that same transaction (refs.IndexItem), as every
// route that writes a comment does, and without, none, as a build before the block index wrote
// one.
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
	`, textversionstest.Issue, body).Scan(&comment.id, &createdAt); err != nil {
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

// editComment writes body as comment id's new body in a transaction of its own, as the edit route
// does (writeEdit), and answers the id of its comment.edited event.
func editComment(t *testing.T, database *store.Store, broker *events.Broker, id, body string) int64 {
	t.Helper()
	ctx := t.Context()
	tx, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the edit's transaction: %v", err)
	}
	defer tx.Rollback(ctx)
	event := writeEdit(t, tx, broker, id, body)
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit the edit: %v", err)
	}
	return event
}

// writeEdit writes body as comment id's new body in tx, as the edit route does: its edited_at
// tx's now(), its block index and its comment.edited event, whose id it answers.
func writeEdit(t *testing.T, tx pgx.Tx, broker *events.Broker, id, body string) int64 {
	t.Helper()
	ctx := t.Context()
	var editedAt time.Time
	if err := tx.QueryRow(ctx, `
		update comments set body = $2, edited_at = now() where id = $1::uuid returning edited_at
	`, id, body).Scan(&editedAt); err != nil {
		t.Fatalf("edit the comment: %v", err)
	}
	if _, err := refs.IndexItem(ctx, tx, refs.ItemComment, id, body, editedAt, nil, nil); err != nil {
		t.Fatalf("index the edited comment's blocks: %v", err)
	}
	return appendCommentEvent(t, tx, broker, "comment.edited", id, body, editedAt)
}

// appendCommentEvent appends the event of type of comment id on textversionstest.Issue, whose body
// is body, through the broker, and answers its id.
func appendCommentEvent(t *testing.T, tx pgx.Tx, broker *events.Broker, eventType, id, body string, createdAt time.Time) int64 {
	t.Helper()
	issue := textversionstest.Issue
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
// row the rules read, its project as the log names it, and an ask's body (askBodySQL), in id
// order.
func readLoggedEvents(t *testing.T, database *store.Store, ids ...int64) []event {
	t.Helper()
	rows, err := database.Pool.Query(t.Context(), `
		select e.id, e.type, e.issue_key, e.artifact_id::text, coalesce(e.project_key, i.project_key, ar.project_key, ''), e.seq,
		       e.created_at, e.payload,
		       case when e.type = any($2::text[]) then `+askBodySQL+` end
		  from events e
		  left join issues i on i.key = e.issue_key
		  left join artifacts ar on ar.id = e.artifact_id
		 where e.id = any($1) order by e.id
	`, ids, askTypes)
	if err != nil {
		t.Fatalf("read the events: %v", err)
	}
	defer rows.Close()
	var read []event
	for rows.Next() {
		var e event
		var payload []byte
		if err := rows.Scan(&e.ID, &e.Type, &e.IssueKey, &e.ArtifactID, &e.Project, &e.Seq, &e.CreatedAt, &payload, &e.AskBody); err != nil {
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
	got, _, err := decideVersions(ctx, tx, slog.New(slog.NewJSONHandler(logs, nil)), batch, batchBytes)
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
