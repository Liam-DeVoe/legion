package api

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// TestSavingAPathologicalBodyIsBounded holds the two defects that made saving a pathological body
// quadratic to the work each one does, which a busy machine cannot move. It held the save's time
// before, and correct code failed it on loaded runners (39 times against a bound of 32, and 72).
//
// Postgres's text-search parser rescans a whitespace-free run from every point where letters or
// digits meet an underscore, so a run of `a_` cost time quadratic in its length: 64 KiB of it saved
// in 46 s. search_text (migration 0056) breaks a run after its sixteenth underscore-joined segment.
// The parser's work reaches no count a test can read; its input does. The row's server runs on
// connections that find a recording to_tsvector ahead of pg_catalog's (recordParserInput), so each
// save is read as the parser received it, whatever path handed it the text: every save must hand
// the parser some text, and no run of more than searchTextSegments segments. With search_text
// answering its input unchanged, the 1 KiB document of `a_` hands it a run of 507. A path that
// calls pg_catalog.to_tsvector by its qualified name is not recorded. A run joined again after
// search_text, as the ask-block removal of the documents trigger can join one, is held by the row
// of the change that moves that removal ahead of search_text, not by this one: no document here
// holds an ask block.
//
// pmdoc's Update wrote each new block with its own insert, and ygo walks a fragment from its first
// child to the index each insert or delete names, so replacing a quote of n headings with a quote
// of n rules took n²/2 steps: 3.9 to 4.3 s of CPU at 21,844 blocks, the most one write's element
// limit allows of it, against 0.68 to 0.69 s with one insert for the run (2026-11-21). Update reads
// and changes a fragment's children only through one struct, and the row reads each call it makes
// (pmdoc.RecordChildEditsForTest): every read of a fragment's children and every positional
// delete and insert, in the order ygo receives them. Both writes of the replacement must make
// exactly the calls seedEdits and replacementEdits name: a constant number of them, whatever the
// number of blocks. A write that deletes or inserts the run one block at a time, or reads the
// quote's children once for each block, makes a call for each block.
//
// The largest save of each ladder is the most a caller can send: a mebibyte of document,
// contracts.DispatchBlockUnits characters of comment, and 21,844 blocks in the replaced quote. Each
// document's run is a code block's text, which the document stores and search indexes whole:
// written as paragraph text, the larger runs make more elements than one write may
// (TestUploadRefusesADocumentTooLargeToStore). The `a_b*` and `)_` documents leave no long run
// whatever search_text does, so for them the row holds only that each save is answered 201 and
// reaches the parser.
//
// What no count here holds: a change inside ygo; a change in pmdoc that reaches the fragment around
// that struct, or makes more than one ygo call inside one of its methods; a parser call by the
// qualified name; and a save that costs more CPU without making another call or handing the parser
// a longer run, in Go or in Postgres. The count that load cannot move for that last is the
// instructions the save executes, and this box gives a test no hardware counter
// (kernel.perf_event_paranoid is 4).
//
// Settlement is held off, so no settlement writes a document while a save is read; the test
// server's shutdown settles each document still loaded, after every count is read. No test in this
// package calls t.Parallel, so no other write runs beside the one read.
func TestSavingAPathologicalBodyIsBounded(t *testing.T) {
	recorded, parsed := recordParserInput(t, storetest.Open(t))
	handler, _, _ := newTestServer(t, testServerOptions{settle: time.Hour, store: recorded})
	issue := createArtifactIssue(t, handler)
	// save runs one save and holds what it handed the parser (parserInput).
	save := func(t *testing.T, what string, write func()) {
		t.Helper()
		input := parsed(write)
		t.Logf("%s handed the parser %d texts, %s", what, input.calls, input)
		switch {
		case input.calls == 0:
			t.Fatalf("%s handed the parser no text this row could read: no search function called to_tsvector by its bare name", what)
		case input.longest > searchTextSegments:
			t.Fatalf("%s handed the parser a run of %d underscore-joined segments, want at most %d: the text-search parser rescans a run to its end from every segment",
				what, input.longest, searchTextSegments)
		}
	}
	documents := 0
	for _, test := range []struct {
		shape string
		sizes []int
	}{
		{"a_", []int{1 << 10, 64 << 10, 1 << 20}},
		{"a_b*", []int{2 << 10, 128 << 10, 1 << 20}},
		{")_", []int{256, 16 << 10, 256 << 10}},
	} {
		t.Run("document "+test.shape, func(t *testing.T) {
			for _, size := range test.sizes {
				const fence = "```\n"
				body := fence + strings.Repeat(test.shape, (size-2*len(fence)-1)/len(test.shape)) + "\n" + fence
				documents++
				fields := map[string]string{"name": fmt.Sprintf("pathological-%d.md", documents)}
				save(t, fmt.Sprintf("a document of %s of `%s`", sizeText(size), test.shape), func() {
					response := multipartRequest(t, handler, "/api/v1/issues/"+issue.Key+"/artifacts", fields, "body.md", "text/markdown", []byte(body), "alice")
					if response.Code != http.StatusCreated {
						t.Fatalf("upload %s of %q: status=%d body=%.200s", sizeText(size), test.shape, response.Code, response.Body.String())
					}
				})
			}
		})
	}
	// A comment's whole body is one search_text run, as a document's is, so the same shape is held
	// at the longest block a caller may send, and at a thirty-second and a quarter of it.
	t.Run("comment a_", func(t *testing.T) {
		const shape = "a_"
		for _, size := range []int{contracts.DispatchBlockUnits / 32, contracts.DispatchBlockUnits / 4, contracts.DispatchBlockUnits} {
			body := strings.Repeat(shape, size/len(shape))
			save(t, fmt.Sprintf("a comment of %s of `%s`", sizeText(size), shape), func() {
				response := dispatchRequest(t, handler, http.MethodPost, "/api/v1/issues/"+issue.Key+"/comments", map[string]any{
					"body": body,
				}, "alice")
				if response.Code != http.StatusCreated {
					t.Fatalf("comment of %s of %q: status=%d body=%.200s", sizeText(size), shape, response.Code, response.Body.String())
				}
			})
		}
	})
	// A quote of two rules and then many headings, written into a new document and then rewritten
	// as a quote of the same two rules and as many rules again: the first write inserts the quote
	// and its children (seedEdits), the second deletes the run of headings after the two kept rules
	// and inserts its rules in one call each (replacementEdits).
	t.Run("replacing many blocks", func(t *testing.T) {
		const kept, from, to = "> ***\n> ***\n", "> #\n", "> ***\n"
		owner := "/api/v1/issues/" + issue.Key + "/artifacts"
		for _, blocks := range []int{682, 5_461, 21_842} {
			documents++
			name := fmt.Sprintf("many-blocks-%d.md", documents)
			for _, write := range []struct {
				what      string
				body      string
				fields    func() map[string]string
				wantEdits []pmdoc.ChildEdit
			}{
				{
					what:      fmt.Sprintf("writing a quote of 2 rules and %d headings into a new document", blocks),
					body:      kept + strings.Repeat(from, blocks),
					fields:    func() map[string]string { return map[string]string{"name": name} },
					wantEdits: seedEdits(blocks),
				},
				{
					what: fmt.Sprintf("replacing the %d headings after its 2 kept rules with as many rules", blocks),
					body: kept + strings.Repeat(to, blocks),
					fields: func() map[string]string {
						return map[string]string{"name": name, "precondition": encodeUploadBase(t, uploadBaseNamed(t, handler, owner, name))}
					},
					wantEdits: replacementEdits(blocks),
				},
			} {
				fields := write.fields()
				var edits []pmdoc.ChildEdit
				save(t, write.what, func() {
					edits = recordChildEdits(func() {
						response := multipartRequest(t, handler, owner, fields, "body.md", "text/markdown", []byte(write.body), "alice")
						if response.Code != http.StatusCreated {
							t.Fatalf("%s: status=%d body=%.200s", write.what, response.Code, response.Body.String())
						}
					})
				})
				t.Logf("%s made %s", write.what, childEditsText(edits))
				if !slices.Equal(edits, write.wantEdits) {
					t.Fatalf("%s made %s, want %s: each call walks the fragment it names from its first child",
						write.what, childEditsText(edits), childEditsText(write.wantEdits))
				}
			}
		}
	})
}

// seedEdits is every call writing a quote of 2 rules and blocks headings into a new document makes
// to a fragment's children: it reads the document's, none; inserts the quote's 2 + blocks children
// into the quote it builds, which no document holds yet; and inserts the quote, the tail of the
// document. An empty heading and a rule have no children to insert.
func seedEdits(blocks int) []pmdoc.ChildEdit {
	return []pmdoc.ChildEdit{
		{Kind: pmdoc.ChildRead, Count: 0},
		{Kind: pmdoc.ChildInsert, Index: 0, Count: 2 + blocks},
		{Kind: pmdoc.ChildInsert, Index: 0, Count: 1},
	}
}

// replacementEdits is every call rewriting that quote's blocks headings as rules makes: it reads
// the document's children, the quote; reads the quote's 2 + blocks children and each kept rule's,
// none, to weigh it as the document's first child and again as its last, being both; reads them a
// third time to keep it; then deletes the run of headings at index 2 and inserts as many rules
// there. Twelve calls, whatever blocks is.
func replacementEdits(blocks int) []pmdoc.ChildEdit {
	quote := []pmdoc.ChildEdit{
		{Kind: pmdoc.ChildRead, Count: 2 + blocks},
		{Kind: pmdoc.ChildRead, Count: 0},
		{Kind: pmdoc.ChildRead, Count: 0},
	}
	edits := []pmdoc.ChildEdit{{Kind: pmdoc.ChildRead, Count: 1}}
	for range 3 {
		edits = append(edits, quote...)
	}
	return append(edits,
		pmdoc.ChildEdit{Kind: pmdoc.ChildDelete, Index: 2, Count: blocks},
		pmdoc.ChildEdit{Kind: pmdoc.ChildInsert, Index: 2, Count: blocks})
}

// recordChildEdits runs write and answers every call it made to a fragment's children, reads and
// positional deletes and inserts, in the order ygo received them (pmdoc.RecordChildEditsForTest),
// restoring the recorder it found however write ends.
func recordChildEdits(write func()) []pmdoc.ChildEdit {
	var mu sync.Mutex
	var edits []pmdoc.ChildEdit
	restore := pmdoc.RecordChildEditsForTest(func(edit pmdoc.ChildEdit) {
		mu.Lock()
		defer mu.Unlock()
		edits = append(edits, edit)
	})
	defer restore()
	write()
	mu.Lock()
	defer mu.Unlock()
	return slices.Clone(edits)
}

// childEditsText writes edits as a failure names them: every call when there are at most 16, else
// the count and the first three and the last.
func childEditsText(edits []pmdoc.ChildEdit) string {
	if len(edits) <= 16 {
		return fmt.Sprintf("%d calls, %v", len(edits), edits)
	}
	return fmt.Sprintf("%d calls, %v … %v", len(edits), edits[:3], edits[len(edits)-1])
}

// searchTextSegments is the most underscore-joined segments search_text (migration 0056) leaves
// in one run before it breaks the run with a space, which bounds the parser's rescans of it.
const searchTextSegments = 16

// parserInput is what Postgres's text-search parser was handed during one save, as
// recordParserInput reads it: how many texts, and the most underscore-joined segments in one run of
// any of them, read only where some run had more than searchTextSegments (0 otherwise). A segment
// is letters, digits, `.` and `-` ending in `_`, the run search_text breaks.
type parserInput struct{ calls, longest int64 }

func (p parserInput) String() string {
	if p.longest == 0 {
		return fmt.Sprintf("no run of more than %d segments", searchTextSegments)
	}
	return fmt.Sprintf("a run of %d segments", p.longest)
}

// recordParserInput answers a store on database whose connections find a to_tsvector of a schema
// of their own ahead of pg_catalog's, for both its forms, and a function that runs a save and
// answers what the parser was handed while it ran (parserInput). Each search function calls
// to_tsvector by its bare name, so on those connections the text it hands the parser passes the
// recorder first, whichever trigger or function handed it on. The recorder keeps its count and its
// longest run in two sequences, which no rollback undoes, so a call whose subtransaction rolls back,
// as search_vector's does for a vector too long, is counted too. The store's pool is its own,
// closed when t ends, before database is dropped.
func recordParserInput(t *testing.T, database *store.Store) (*store.Store, func(save func()) parserInput) {
	t.Helper()
	ctx := context.Background()
	for _, statement := range []string{
		`create schema parser_input`,
		`create sequence parser_input.calls minvalue 0`,
		`create sequence parser_input.longest_run minvalue 0`,
		fmt.Sprintf(`create function parser_input.record(body text) returns void language plpgsql volatile strict as $$
		 declare
		   longest bigint;
		 begin
		   perform nextval('parser_input.calls');
		   if body ~ '(?:[A-Za-z0-9.-]+_){%d}' then
		     select coalesce(max(length(run[1]) - length(replace(run[1], '_', ''))), 0) into longest
		       from regexp_matches(body, '((?:[A-Za-z0-9.-]+_)+)', 'g') as run;
		     perform setval('parser_input.longest_run', greatest(longest, (select last_value from parser_input.longest_run)));
		   end if;
		 end $$`, searchTextSegments+1),
		`create function parser_input.to_tsvector(config regconfig, body text) returns tsvector
		 language plpgsql volatile strict as $$
		 begin
		   perform parser_input.record(body);
		   return pg_catalog.to_tsvector(config, body);
		 end $$`,
		`create function parser_input.to_tsvector(body text) returns tsvector
		 language plpgsql volatile strict as $$
		 begin
		   perform parser_input.record(body);
		   return pg_catalog.to_tsvector(body);
		 end $$`,
	} {
		if _, err := database.Pool.Exec(ctx, statement); err != nil {
			t.Fatalf("record the parser's input: %s: %v", statement, err)
		}
	}
	config := database.Pool.Config()
	config.ConnConfig.RuntimeParams["search_path"] = "parser_input, pg_catalog, public"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("open the recorded pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return &store.Store{Pool: store.NewPool(pool)}, func(save func()) parserInput {
		if _, err := database.Pool.Exec(ctx, `select setval('parser_input.calls', 0, true), setval('parser_input.longest_run', 0, true)`); err != nil {
			t.Fatalf("reset the parser's recorder: %v", err)
		}
		save()
		var input parserInput
		if err := database.Pool.QueryRow(ctx, `select (select last_value from parser_input.calls), (select last_value from parser_input.longest_run)`).Scan(&input.calls, &input.longest); err != nil {
			t.Fatalf("read the parser's recorder: %v", err)
		}
		return input
	}
}

// sizeText writes a size in bytes as a failure names it.
func sizeText(size int) string {
	switch {
	case size >= 1<<20 && size%(1<<20) == 0:
		return fmt.Sprintf("%d MiB", size>>20)
	case size >= 1<<10:
		return fmt.Sprintf("%d KiB", size>>10)
	}
	return fmt.Sprintf("%d B", size)
}

// Ordinary text searches as it did before search_text: a title, a document, a comment, an ask
// with its answer and a message are each found by a word they hold, with the same snippet marks,
// and a word joined to others by underscores is still found by itself.
func TestSearchFindsOrdinaryAndUnderscoredTextAfterSearchText(t *testing.T) {
	handler := newTestHandler(t)
	corpus := seedSearchCorpus(t, handler)
	for _, test := range []struct{ query, kind, id string }{
		{"astrolabe", "document", corpus.primaryArtifactID},
		{"sextant", "comment", corpus.commentID},
		{"alidade", "ask", corpus.askID},
		{"compass calibration", "message", corpus.messageID},
		{"navigation instruments", "issue", corpus.issueKey},
	} {
		results := searchResponse(t, handler, "q="+url.QueryEscape(test.query)).Results
		found := false
		for _, result := range results {
			if result.Kind == test.kind && result.ID == test.id {
				found = true
			}
		}
		if !found {
			t.Errorf("search %q: no %s %s among %d results", test.query, test.kind, test.id, len(results))
		}
	}
	message := dispatchRequest(t, handler, http.MethodPost, "/api/v1/issues/"+corpus.issueKey+"/messages", map[string]any{
		"body": "set LEGION_E2E_MODEL_GATEWAY_URL and the pod_safety_overlay_name_for_each_role_in_the_bundle_of_prompts_the_daemon_snapshots_at_boot_time first",
	}, "alice")
	if message.Code != http.StatusCreated {
		t.Fatalf("create underscored message: status=%d body=%s", message.Code, message.Body.String())
	}
	for _, query := range []string{"legion_e2e_model_gateway_url", "gateway", "snapshots", "boot"} {
		results := searchResponse(t, handler, "q="+url.QueryEscape(query)).Results
		found := false
		for _, result := range results {
			if result.Kind == "message" && strings.Contains(result.Snippet, "<mark>") {
				found = true
			}
		}
		if !found {
			t.Errorf("search %q: the underscored message was not found with a marked snippet", query)
		}
	}
}

// A document whose markdown makes more elements than one write may - 1 MiB of `)_`, 262,144
// italic spans, held a gigabyte while it was saved (LEGION-481) - is refused as too large to store,
// naming the limit, on a new document (SeedText) and on a replacement (ReplaceText), never a 500.
func TestUploadRefusesADocumentTooLargeToStore(t *testing.T) {
	handler := newTestHandler(t)
	issue := createArtifactIssue(t, handler)
	body := strings.Repeat(")_", (1<<20)/2)
	// A new document (SeedText): refused, and nothing is created.
	response := multipartRequest(t, handler, "/api/v1/issues/"+issue.Key+"/artifacts", map[string]string{"name": "too-large.md"}, "body.md", "text/markdown", []byte(body), "alice")
	if !refusedTooLarge(t, response) || !strings.Contains(response.Body.String(), "more than 65536 elements") {
		t.Fatalf("seed of a document too large to store: status=%d body=%.400s, want 413 CAP_EXCEEDED naming the 65536-element limit, as the document service words it", response.Code, response.Body.String())
	}
	if listing := dispatchRequest(t, handler, http.MethodGet, "/api/v1/issues/"+issue.Key+"/artifacts", nil, "alice"); strings.Contains(listing.Body.String(), "too-large") {
		t.Fatalf("the refused seed left an artifact behind: %.300s", listing.Body.String())
	}
	// A replacement of a stored document (ReplaceText): refused the same way, and the document keeps
	// its version.
	if first := multipartRequest(t, handler, "/api/v1/issues/"+issue.Key+"/artifacts", map[string]string{"name": "notes.md"}, "body.md", "text/markdown", []byte("# Notes\n"), "alice"); first.Code != http.StatusCreated {
		t.Fatalf("seed notes.md: status=%d body=%.300s", first.Code, first.Body.String())
	}
	response = multipartRequest(t, handler, "/api/v1/issues/"+issue.Key+"/artifacts", map[string]string{"name": "notes.md", "precondition": encodeUploadBase(t, uploadBaseNamed(t, handler, "/api/v1/issues/"+issue.Key+"/artifacts", "notes.md"))}, "body.md", "text/markdown", []byte(body), "alice")
	if !refusedTooLarge(t, response) || !strings.Contains(response.Body.String(), "more than 65536 elements") {
		t.Fatalf("replacement by a document too large to store: status=%d body=%.400s, want 413 CAP_EXCEEDED naming the 65536-element limit, as the document service words it", response.Code, response.Body.String())
	}
	text := dispatchRequest(t, handler, http.MethodGet, "/api/v1/issues/"+issue.Key+"/artifacts/notes-md/text", nil, "alice")
	if text.Code != http.StatusOK || !strings.Contains(text.Body.String(), `"markdown":"# Notes\n"`) {
		t.Fatalf("notes.md after the refused replacement: status=%d body=%.300s, want its first version", text.Code, text.Body.String())
	}
}

// Every route that writes a caller's markdown refuses markdown past the element limit as 413
// CAP_EXCEEDED naming the limit, and writes nothing: a spec at issue creation, an issue's and a
// project's document upload as JSON, and an edit's insert and replace.
func TestEveryMarkdownWriteRefusesMarkdownPastTheElementLimit(t *testing.T) {
	handler := newTestHandler(t)
	issue := createInteractionIssue(t, handler, "TEST", "Element limit", "Before.\n")
	if response := dispatchRequest(t, handler, http.MethodPost, "/api/v1/projects", map[string]string{"key": "DOCS", "name": "Docs"}, "alice"); response.Code != http.StatusCreated {
		t.Fatalf("create project: status=%d body=%s", response.Code, response.Body.String())
	}
	heavy := strings.Repeat(")_", 200_000)
	for _, write := range []struct {
		name, path string
		body       any
	}{
		{"a spec at issue creation", "/api/v1/issues", map[string]string{"project": "TEST", "title": "Too heavy", "spec": heavy}},
		{"an issue's document upload", "/api/v1/issues/" + issue.Key + "/artifacts", map[string]string{"name": "heavy.md", "content": heavy}},
		{"a project's document upload", "/api/v1/projects/DOCS/artifacts", map[string]string{"name": "heavy.md", "content": heavy}},
		{"an edit's insert", "/api/v1/artifacts/" + issue.PrimaryArtifactID + "/edits", map[string]any{
			"ops": []map[string]string{{"op": "insert", "after": "end", "markdown": heavy}},
		}},
		{"an edit's replace", "/api/v1/artifacts/" + issue.PrimaryArtifactID + "/edits", map[string]any{
			"ops": []map[string]string{{"op": "replace", "find": "Before.", "with": heavy}},
		}},
	} {
		response := dispatchRequest(t, handler, http.MethodPost, write.path, write.body, "alice")
		if response.Code != http.StatusRequestEntityTooLarge || !strings.Contains(response.Body.String(), `"code":"CAP_EXCEEDED"`) || !strings.Contains(response.Body.String(), "more than 65536 elements") {
			t.Errorf("%s past the element limit: status=%d body=%.300s, want 413 CAP_EXCEEDED naming the limit", write.name, response.Code, response.Body.String())
		}
	}
	if listed := dispatchRequest(t, handler, http.MethodGet, "/api/v1/issues?project=TEST", nil, "alice"); strings.Contains(listed.Body.String(), "Too heavy") {
		t.Errorf("the refused issue was created: %.300s", listed.Body.String())
	}
	for _, path := range []string{"/api/v1/issues/" + issue.Key + "/artifacts", "/api/v1/projects/DOCS/artifacts"} {
		if listed := dispatchRequest(t, handler, http.MethodGet, path, nil, "alice"); strings.Contains(listed.Body.String(), "heavy") {
			t.Errorf("a refused upload left a document behind at %s: %.300s", path, listed.Body.String())
		}
	}
	if text := documentMarkdown(t, handler, issue.PrimaryArtifactID); text != "Before.\n" {
		t.Errorf("the spec after the refused edits = %q, want it unchanged", text)
	}
}
