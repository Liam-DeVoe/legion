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

	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// TestSavingAPathologicalBodyIsBounded holds the two defects that made saving a pathological body
// quadratic to the work each one does, which a busy machine cannot move. It held the save's time
// before, and correct code failed it on loaded runners (39 times against a bound of 32, and 72).
//
// Postgres's text-search parser rescans a whitespace-free run from every point where letters or
// digits meet an underscore, so a run of `a_` cost time quadratic in its length: 64 KiB of it saved
// in 46 s. search_text (migration 0056) breaks a run after its sixteenth underscore-joined segment.
// The parser's work reaches no count a test can read; its input does. So the row holds that every
// trigger function writing the search vector of the two tables these saves write passes the
// saved text to the parser only through search_text (assertSearchTriggersNormalise), and that
// search_text leaves no run of more than searchTextSegments segments in each `a_` save
// (searchRunSegments). With search_text answering its input unchanged, 1 KiB of `a_` reads as one
// run of 507 segments. The documents trigger strips ask blocks from search_text's answer, which can
// join two runs into one again: that defect is older than this row, is fixed apart from it, and no
// count here reads it.
//
// pmdoc's Update wrote each new block with its own insert, and ygo walks a fragment from its first
// child to the index each insert or delete names, so replacing a quote of n headings with a quote
// of n rules took n²/2 steps: 3.9 to 4.3 s of CPU at 21,844 blocks, the most one write's element
// limit allows of it, against 0.68 to 0.69 s with one insert for the run (2026-11-21). Update makes
// every positional insert and delete through one interface, and the row reads each call it makes
// (pmdoc.RecordChildEditsForTest): a run of replaced blocks after two kept ones must be exactly
// one delete and one insert at index 2. A write that deletes the run one block at a time makes n
// deletes, and one that inserts it one block at a time n inserts.
//
// The largest save of each ladder is the most a caller can send: a mebibyte of document,
// contracts.DispatchBlockUnits characters of comment, and 21,844 blocks in the replaced quote. Each
// document's run is a code block's text, which the document stores and search indexes whole:
// written as paragraph text, the larger runs make more elements than one write may
// (TestUploadRefusesADocumentTooLargeToStore). The `a_b*` and `)_` documents check only that each
// save is answered 201: search_text finds no run in them to break, and pmdoc's growth tests hold
// their parse.
//
// What no count here holds: a change inside ygo; a change in pmdoc that reaches the fragment around
// that interface, or makes more than one ygo call inside one of its methods; and a save that costs
// more CPU without making another call or leaving a longer search_text run, in Go or in Postgres.
// The count that load cannot move for that last is the instructions the save executes, and this box
// gives a test no hardware counter (kernel.perf_event_paranoid is 4).
//
// Settlement is held off, so no settlement writes a document while a replacement's calls are
// recorded; the test server's shutdown settles each document still loaded, after every count is
// read. No test in this package calls t.Parallel, so no other write runs beside the one recorded.
func TestSavingAPathologicalBodyIsBounded(t *testing.T) {
	handler, database, _ := newTestServer(t, testServerOptions{settle: time.Hour})
	issue := createArtifactIssue(t, handler)
	assertSearchTriggersNormalise(t, database)
	documents := 0
	for _, test := range []struct {
		shape string
		sizes []int
		runs  bool
	}{
		{"a_", []int{1 << 10, 64 << 10, 1 << 20}, true},
		{"a_b*", []int{2 << 10, 128 << 10, 1 << 20}, false},
		{")_", []int{256, 16 << 10, 256 << 10}, false},
	} {
		t.Run("document "+test.shape, func(t *testing.T) {
			for _, size := range test.sizes {
				const fence = "```\n"
				body := fence + strings.Repeat(test.shape, (size-2*len(fence)-1)/len(test.shape)) + "\n" + fence
				if test.runs {
					assertSearchRunsBounded(t, database, fmt.Sprintf("a document of %s of `%s`", sizeText(size), test.shape), body)
				}
				documents++
				response := multipartRequest(t, handler, "/api/v1/issues/"+issue.Key+"/artifacts", map[string]string{
					"name": fmt.Sprintf("pathological-%d.md", documents),
				}, "body.md", "text/markdown", []byte(body), "alice")
				if response.Code != http.StatusCreated {
					t.Fatalf("upload %s of %q: status=%d body=%.200s", sizeText(size), test.shape, response.Code, response.Body.String())
				}
			}
		})
	}
	// A comment's whole body is one search_text run, as a document's is, so the same shape is held
	// at the longest block a caller may send, and at a thirty-second and a quarter of it.
	t.Run("comment a_", func(t *testing.T) {
		const shape = "a_"
		for _, size := range []int{contracts.DispatchBlockUnits / 32, contracts.DispatchBlockUnits / 4, contracts.DispatchBlockUnits} {
			body := strings.Repeat(shape, size/len(shape))
			assertSearchRunsBounded(t, database, fmt.Sprintf("a comment of %s of `%s`", sizeText(size), shape), body)
			response := dispatchRequest(t, handler, http.MethodPost, "/api/v1/issues/"+issue.Key+"/comments", map[string]any{
				"body": body,
			}, "alice")
			if response.Code != http.StatusCreated {
				t.Fatalf("comment of %s of %q: status=%d body=%.200s", sizeText(size), shape, response.Code, response.Body.String())
			}
		}
	})
	// A quote of two rules and then many headings, rewritten as a quote of the same two rules and
	// as many rules again, is one run of replaced blocks after two kept ones: Update makes exactly
	// one delete of the run and one insert of its replacements, both at the run's first index, 2.
	t.Run("replacing many blocks", func(t *testing.T) {
		const kept, from, to = "> ***\n> ***\n", "> #\n", "> ***\n"
		owner := "/api/v1/issues/" + issue.Key + "/artifacts"
		for _, blocks := range []int{682, 5_461, 21_842} {
			documents++
			name := fmt.Sprintf("many-blocks-%d.md", documents)
			upload := func(body []byte, fields map[string]string) {
				response := multipartRequest(t, handler, owner, fields, "body.md", "text/markdown", body, "alice")
				if response.Code != http.StatusCreated {
					t.Fatalf("upload %d blocks after the kept rules: status=%d body=%.200s", blocks, response.Code, response.Body.String())
				}
			}
			upload([]byte(kept+strings.Repeat(from, blocks)), map[string]string{"name": name})
			fields := map[string]string{"name": name, "precondition": encodeUploadBase(t, uploadBaseNamed(t, handler, owner, name))}
			edits := recordChildEdits(func() { upload([]byte(kept+strings.Repeat(to, blocks)), fields) })
			want := []pmdoc.ChildEdit{{Index: 2, Count: blocks}, {Insert: true, Index: 2, Count: blocks}}
			t.Logf("replacing %d headings after 2 kept rules with as many rules made %s", blocks, childEditsText(edits))
			if !slices.Equal(edits, want) {
				t.Fatalf("replacing %d headings after 2 kept rules with as many rules made %s, want %v: each call walks the quote from its first child",
					blocks, childEditsText(edits), want)
			}
		}
	})
}

// recordChildEdits runs write and answers every positional call it made to a fragment's children,
// in order (pmdoc.RecordChildEditsForTest), restoring the recorder it found however write ends.
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

// childEditsText writes edits as a failure names them: every call when there are few, else the
// count and the first three and the last.
func childEditsText(edits []pmdoc.ChildEdit) string {
	if len(edits) <= 6 {
		return fmt.Sprintf("%d positional calls, %v", len(edits), edits)
	}
	return fmt.Sprintf("%d positional calls, %v … %v", len(edits), edits[:3], edits[len(edits)-1])
}

// searchTextSegments is the most underscore-joined segments search_text (migration 0056) leaves
// in one run before it breaks the run with a space, which bounds the parser's rescans of it.
const searchTextSegments = 16

// searchRunSegments is the most underscore-joined segments in one run of text as search_text
// leaves it, by the database's own search_text and the pattern it breaks runs at: a segment is
// letters, digits, `.` and `-` ending in `_`.
func searchRunSegments(t *testing.T, database *store.Store, text string) int {
	t.Helper()
	var segments int
	if err := database.Pool.QueryRow(context.Background(), `
		select coalesce(max(length(run[1]) - length(replace(run[1], '_', ''))), 0)
		from regexp_matches(search_text($1), '((?:[A-Za-z0-9.-]+_)+)', 'g') as run`, text).Scan(&segments); err != nil {
		t.Fatalf("read the runs search_text leaves: %v", err)
	}
	return segments
}

// assertSearchRunsBounded holds what's text, before it is saved, to search_text's bound on a run.
func assertSearchRunsBounded(t *testing.T, database *store.Store, what, text string) {
	t.Helper()
	if segments := searchRunSegments(t, database, text); segments > searchTextSegments {
		t.Fatalf("search_text leaves a run of %d underscore-joined segments in %s, want at most %d: the text-search parser rescans a run to its end from every segment",
			segments, what, searchTextSegments)
	}
}

// assertSearchTriggersNormalise holds the triggers that index what this test saves to passing the
// saved text to the parser only through search_text: in every trigger function of each table that
// writes the row's search vector, read from the catalog as the database runs it, each read of the
// column it indexes is the whole argument of search_text (searchTextReads). A trigger that also
// handed the parser the raw text would index the same lexemes, so no stored vector tells the two
// apart.
func assertSearchTriggersNormalise(t *testing.T, database *store.Store) {
	t.Helper()
	for table, column := range map[string]string{"artifact_versions": "markdown", "comments": "body"} {
		rows, err := database.Pool.Query(context.Background(), `
			select p.proname, p.prosrc
			from pg_trigger tr
			join pg_proc p on p.oid = tr.tgfoid
			where tr.tgrelid = $1::regclass and not tr.tgisinternal and p.prosrc ~* 'new\s*\.\s*search\s*:?='`, table)
		if err != nil {
			t.Fatalf("read the search triggers of %s: %v", table, err)
		}
		writers := map[string]string{}
		for rows.Next() {
			var name, source string
			if err := rows.Scan(&name, &source); err != nil {
				t.Fatalf("read a search trigger of %s: %v", table, err)
			}
			writers[name] = source
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatalf("read the search triggers of %s: %v", table, err)
		}
		if len(writers) == 0 {
			t.Fatalf("no trigger function of %s writes its search vector: nothing was read", table)
		}
		for name, source := range writers {
			reads, normalised := searchTextReads(source, column)
			if reads == 0 || normalised != reads {
				t.Fatalf("%s's search trigger %s reads new.%s %d times, %d of them as search_text(new.%s), want every read and at least one:\n%s",
					table, name, column, reads, normalised, column, source)
			}
		}
	}
}

// searchTextReads counts the reads of new.<column> in a trigger function's source, and how many of
// them are the whole argument of search_text, as search_text(new.<column>). It reads the source
// lowercased, with its whitespace and double quotes taken out, so neither case, spacing nor a
// quoted identifier hides a read.
func searchTextReads(source, column string) (reads, normalised int) {
	compact := strings.ReplaceAll(strings.ToLower(strings.Join(strings.Fields(source), "")), `"`, "")
	read, wrapper := "new."+column, "search_text("
	identifier := func(at int) bool {
		if at < 0 || at >= len(compact) {
			return false
		}
		c := compact[at]
		return c == '_' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9'
	}
	for from := 0; ; {
		found := strings.Index(compact[from:], read)
		if found < 0 {
			return reads, normalised
		}
		at, end := from+found, from+found+len(read)
		from = end
		if identifier(at-1) || identifier(end) {
			continue
		}
		reads++
		if strings.HasSuffix(compact[:at], wrapper) && end < len(compact) && compact[end] == ')' {
			normalised++
		}
	}
}

// searchTextReads counts a read of the indexed column as normalised only when it is search_text's
// whole argument, whatever its case, spacing or quoting; a second, raw read beside a normalised one
// is counted as a read that is not.
func TestSearchTextReadsCountsEveryRawRead(t *testing.T) {
	for _, test := range []struct {
		source            string
		reads, normalised int
	}{
		{`new.search := search_vector('', search_text(new.body));`, 1, 1},
		{`new.search := search_vector('', search_text(new.body)) || to_tsvector('simple', new.body);`, 2, 1},
		{`NEW.search := search_vector('', SEARCH_TEXT( NEW . "body" ));`, 1, 1},
		{`new.search := to_tsvector('simple', new.body);`, 1, 0},
		{`new.search := search_vector('', search_text(new.body || ' ' || new.title));`, 1, 0},
		{`new.search := search_vector(new.bodytext, search_text(new.body));`, 1, 1},
		{`new.search := search_vector('', search_text(new.title));`, 0, 0},
	} {
		reads, normalised := searchTextReads(test.source, "body")
		if reads != test.reads || normalised != test.normalised {
			t.Errorf("searchTextReads(%q) = %d reads, %d normalised, want %d and %d", test.source, reads, normalised, test.reads, test.normalised)
		}
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
