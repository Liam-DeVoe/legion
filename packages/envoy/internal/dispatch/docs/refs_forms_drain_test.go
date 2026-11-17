package docs

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// formsOwedRows is every row of the forms queue as "kind id", with the enqueued_at each holds.
func formsOwedRows(t *testing.T, database *store.Store) map[string]time.Time {
	t.Helper()
	rows, err := database.Pool.Query(context.Background(), `select kind || ' ' || id::text, enqueued_at from refs_forms_owed`)
	if err != nil {
		t.Fatalf("read refs_forms_owed: %v", err)
	}
	defer rows.Close()
	owed := map[string]time.Time{}
	for rows.Next() {
		var row string
		var at time.Time
		if err := rows.Scan(&row, &at); err != nil {
			t.Fatalf("scan refs_forms_owed: %v", err)
		}
		owed[row] = at
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate refs_forms_owed: %v", err)
	}
	return owed
}

// reconcileRefsForms runs one pass of the forms drain.
func reconcileRefsForms(t *testing.T, service *Service) RefsFormsTick {
	t.Helper()
	tick, err := service.ReconcileRefsForms(context.Background())
	if err != nil {
		t.Fatalf("forms drain pass: %v", err)
	}
	return tick
}

// citesIssue reports whether the source holds an edge to the issue key.
func citesIssue(t *testing.T, database *store.Store, fromKind, fromID, key string) bool {
	t.Helper()
	return slices.ContainsFunc(refsFrom(t, database, fromKind, fromID), func(ref refRow) bool {
		return ref.ToKind == "issue" && ref.ToID == key
	})
}

// createIndexedNotes writes document notes of CORE-1 as a build that predates the forms writes and
// indexes it: the live text the server serves, version 1 holding that text, its settlement done
// and its block index recorded at version 1, with no edge of the tab address it writes, since that
// build's grammar reads none. Its version's own trigger owes it to the forms queue.
func createIndexedNotes(t *testing.T, database *store.Store, service *Service, markdown string) string {
	t.Helper()
	ctx := context.Background()
	var notes string
	if err := database.Pool.QueryRow(ctx, `
		insert into artifacts (issue_key, project_key, slug, name, kind, created_by)
		values ('CORE-1', 'CORE', 'notes', 'notes.md', 'doc', '{"kind":"user","id":"alice"}')
		returning id::text
	`).Scan(&notes); err != nil {
		t.Fatalf("create the notes: %v", err)
	}
	seedServiceText(t, service, notes, markdown)
	live, _, err := service.TextWithBlocks(ctx, notes)
	if err != nil {
		t.Fatalf("read the notes: %v", err)
	}
	if _, err := database.Pool.Exec(ctx, `
		insert into artifact_versions (artifact_id, number, markdown, created_at, authors)
		values ($1, 1, $2, $3, '[{"kind":"user","id":"alice"}]')
	`, notes, live, refsGrammarWritten); err != nil {
		t.Fatalf("write version 1 of the notes: %v", err)
	}
	settle(t, database, notes)
	if _, err := database.Pool.Exec(ctx, `update document_block_versions set version = 1 where artifact_id = $1`, notes); err != nil {
		t.Fatalf("index the version as the earlier release does: %v", err)
	}
	return notes
}

// Every text a build that predates an issue's tab addresses and a document's dashboard URLs
// stored is re-indexed by one pass of the forms drain, with no edit and no command run: a comment,
// an ask's question and an issue message through refs.ReindexOwed, and a document indexed by the
// drain itself at its latest version, its own edge and its block's mention both, each settled in
// the transaction that indexed it. The queue is empty after it.
func TestTheFormsDrainIndexesTextAnOlderBuildStored(t *testing.T) {
	database, service := blockIndexService(t)
	ctx := context.Background()
	comment := createRawComment(t, database, "The plan: dispatch://CORE-2/plan.")
	message := createRawMessage(t, database, "The verification tab: https://dispatch.example/issues/CORE-2/verification.")
	var ask string
	if err := database.Pool.QueryRow(ctx, `
		insert into asks (issue_key, author, question, created_at)
		values ('CORE-1', '{"kind":"session","id":"s1"}', 'Ship after dispatch://CORE-2/children?', $1)
		returning id::text
	`, refsGrammarWritten).Scan(&ask); err != nil {
		t.Fatalf("write the ask: %v", err)
	}
	notes := createIndexedNotes(t, database, service, "The plan: dispatch://CORE-2/plan.\n")
	if owed := formsOwedRows(t, database); len(owed) != 4 {
		t.Fatalf("the forms queue holds %v after writes an older build makes, want the four sources", owed)
	}

	tick := reconcileRefsForms(t, service)

	if tick.Texts != 3 || tick.Documents != 1 || tick.OwedTexts != 0 || tick.LiveAhead != 0 || tick.Unindexed != 0 {
		t.Fatalf("pass = %#v, want three texts and the document indexed and nothing left owed", tick)
	}
	if owed := formsOwedRows(t, database); len(owed) != 0 {
		t.Fatalf("the forms queue holds %v after the pass, want it empty", owed)
	}
	assertRef(t, database, "comment", comment, "issue", "CORE-2", refsGrammarWritten)
	assertRef(t, database, "message", message, "issue", "CORE-2", refsGrammarWritten)
	assertRef(t, database, "ask", ask, "issue", "CORE-2", refsGrammarWritten)
	assertRef(t, database, "artifact", notes, "issue", "CORE-2", refsGrammarWritten)
	holder := holderOf(t, service, notes, "dispatch://CORE-2/plan")
	if !slices.ContainsFunc(blockPairsOf(t, database, notes), func(pair blockPair) bool {
		return pair.BlockID == holder && pair.ToKind == "issue" && pair.ToID == "CORE-2"
	}) {
		t.Fatalf("the notes' blocks hold %#v, want block %s citing CORE-2", blockPairsOf(t, database, notes), holder)
	}
	if at := blockIndexAt(t, database, notes); at == nil || *at != 1 {
		t.Fatalf("the notes' block index is at %v after the drain indexed them, want version 1", at)
	}
}

// A document's row is settled only by this drain's own index of it. The drain owes the document a
// block index and indexes it in the same pass, and any build's block-index pass may take a
// document owed one in between: here another process takes it under its owner row, as an older
// build's pass does, and records it indexed at its version with no edge of the tab address, which
// that build's grammar does not read. The drain then finds the document no longer owed and leaves
// its row owed, with the enqueued_at it listed; the next pass indexes it and settles the row.
func TestTheFormsDrainLeavesADocumentAnotherProcessIndexedFirstOwed(t *testing.T) {
	database, service := blockIndexService(t)
	ctx := context.Background()
	notes := createIndexedNotes(t, database, service, "The plan: dispatch://CORE-2/plan.\n")
	listed := formsOwedRows(t, database)
	if len(listed) != 1 {
		t.Fatalf("the forms queue holds %v, want the notes alone", listed)
	}

	// The other process's block-index pass holds the document's owner row, the issue's, for the
	// length of its index, which the drain's index takes too.
	other, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the other process's index: %v", err)
	}
	defer other.Rollback(ctx)
	if _, err := other.Exec(ctx, `select 1 from issues where key = 'CORE-1' for no key update`); err != nil {
		t.Fatalf("take the notes' owner row as a block index does: %v", err)
	}
	passed := make(chan RefsFormsTick, 1)
	failed := make(chan error, 1)
	go func() {
		tick, err := service.ReconcileRefsForms(ctx)
		if err != nil {
			failed <- err
			return
		}
		passed <- tick
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var owedIndex bool
		var waiting int
		if err := database.Pool.QueryRow(ctx, `
			select (select version is null from document_block_versions where artifact_id = $1),
			       (select count(*) from pg_stat_activity where datname = current_database() and wait_event_type = 'Lock')
		`, notes).Scan(&owedIndex, &waiting); err != nil {
			t.Fatalf("read the drain's progress: %v", err)
		}
		if owedIndex && waiting > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the drain never owed the notes a block index and waited on their owner row (owed %v, waiting %d)", owedIndex, waiting)
		}
		time.Sleep(10 * time.Millisecond)
	}
	// What the other process's index records: the version indexed, and no edge of the tab address.
	if _, err := other.Exec(ctx, `update document_block_versions set version = 1 where artifact_id = $1`, notes); err != nil {
		t.Fatalf("record the notes indexed as the other process does: %v", err)
	}
	if err := other.Commit(ctx); err != nil {
		t.Fatalf("commit the other process's index: %v", err)
	}
	var tick RefsFormsTick
	select {
	case tick = <-passed:
	case err := <-failed:
		t.Fatalf("forms drain pass: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("the forms drain pass did not end once the owner row was free")
	}

	if tick.Documents != 0 || tick.Unindexed != 1 || tick.OwedTexts != 1 {
		t.Fatalf("pass = %#v, want the notes left owed, unindexed by this drain", tick)
	}
	owed := formsOwedRows(t, database)
	if len(owed) != 1 || !owed["artifact "+notes].Equal(listed["artifact "+notes]) {
		t.Fatalf("the forms queue holds %v after another process indexed the notes first, want their row as listed (%v)", owed, listed)
	}
	if citesIssue(t, database, "artifact", notes, "CORE-2") {
		t.Fatalf("the notes cite CORE-2 though no index of this build ran: %#v", refsFrom(t, database, "artifact", notes))
	}

	next := reconcileRefsForms(t, service)

	if next.Documents != 1 || next.OwedTexts != 0 {
		t.Fatalf("the next pass = %#v, want the notes indexed and settled", next)
	}
	assertRef(t, database, "artifact", notes, "issue", "CORE-2", refsGrammarWritten)
}

// A document whose live text is ahead of its latest version, and differs from it, is not indexed
// at that version, which would date the edit's blocks to it: its row stays owed, and the
// settlement that writes its next version indexes it (a version of this build) or owes it again
// (one of an older build).
func TestTheFormsDrainLeavesALiveAheadDocumentOwed(t *testing.T) {
	database, service := blockIndexService(t)
	notes := createOwedDocument(t, database, service, "CORE-1", "", "notes", "The plan: dispatch://CORE-2/plan.\n", "Old text.\n")
	if owed := formsOwedRows(t, database); len(owed) != 1 {
		t.Fatalf("the forms queue holds %v, want the notes alone", owed)
	}

	tick := reconcileRefsForms(t, service)

	if tick.LiveAhead != 1 || tick.Documents != 0 || tick.OwedTexts != 1 {
		t.Fatalf("pass = %#v, want the notes left owed as live ahead", tick)
	}
	if owed := formsOwedRows(t, database); len(owed) != 1 {
		t.Fatalf("the forms queue holds %v, want the notes still owed", owed)
	}
	if citesIssue(t, database, "artifact", notes, "CORE-2") {
		t.Fatalf("the notes cite what only their unsettled live text writes: %#v", refsFrom(t, database, "artifact", notes))
	}
}

// A document that is gone owes no text: its row, which an older build's removal leaves behind when
// its delete of the document's edges owes the forms queue, is settled.
func TestTheFormsDrainSettlesAGoneDocument(t *testing.T) {
	database, service := blockIndexService(t)
	ctx := context.Background()
	var gone string
	if err := database.Pool.QueryRow(ctx, `select gen_random_uuid()::text`).Scan(&gone); err != nil {
		t.Fatalf("draw a uuid: %v", err)
	}
	for _, statement := range []string{
		`insert into refs (from_kind, from_id, to_kind, to_id) values ('artifact', $1, 'issue', 'CORE-2')`,
		`delete from refs where from_kind = 'artifact' and from_id = $1`,
	} {
		if _, err := database.Pool.Exec(ctx, statement, gone); err != nil {
			t.Fatalf("delete a removed document's edges as an older build does: %v", err)
		}
	}
	if owed := formsOwedRows(t, database); len(owed) != 1 {
		t.Fatalf("the forms queue holds %v, want the gone document", owed)
	}

	tick := reconcileRefsForms(t, service)

	if tick.Gone != 1 || tick.OwedTexts != 0 {
		t.Fatalf("pass = %#v, want the gone document's row settled", tick)
	}
}

// The forms drain's own index of a document settles every queue the document is in, after its own
// delete from refs: a document this drain indexes once leaves nothing owed, even where its index
// deletes an edge an older build wrote that the text no longer holds.
func TestTheFormsDrainsIndexSettlesAfterItsOwnDelete(t *testing.T) {
	database, service := blockIndexService(t)
	notes := createIndexedNotes(t, database, service, "The plan: dispatch://CORE-2/plan.\n")
	if _, err := database.Pool.Exec(context.Background(), `
		insert into refs (from_kind, from_id, to_kind, to_id) values ('artifact', $1, 'issue', 'CORE-1')
	`, notes); err != nil {
		t.Fatalf("write an edge the text does not hold: %v", err)
	}

	tick := reconcileRefsForms(t, service)

	if tick.Documents != 1 || tick.OwedTexts != 0 {
		t.Fatalf("pass = %#v, want the notes indexed and nothing owed", tick)
	}
	if citesIssue(t, database, "artifact", notes, "CORE-1") {
		t.Fatalf("the notes keep an edge their text does not hold: %#v", refsFrom(t, database, "artifact", notes))
	}
	if owed := formsOwedRows(t, database); len(owed) != 0 {
		t.Fatalf("the forms queue holds %v after the drain's index deleted an edge, want it empty", owed)
	}
	var grammar int
	if err := database.Pool.QueryRow(context.Background(),
		`select count(*) from refs_grammar_owed where kind = 'artifact' and id = $1::uuid`, notes).Scan(&grammar); err != nil {
		t.Fatalf("count the whole grammar's row: %v", err)
	}
	if grammar != 0 {
		t.Fatalf("the whole grammar's queue still owes the notes after this build indexed them")
	}
}
