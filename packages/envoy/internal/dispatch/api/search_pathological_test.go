package api

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// Saving a pathological body costs work linear in it, at every size a caller may send. Two
// defects made a save quadratic, and the row holds each to the work it did, which a busy machine
// cannot move, where it held the save's time before and failed on loaded runners (39 times
// against a bound of 32, and 72 times, both on correct code).
//
// Postgres's text-search parser tries to read a host name wherever letters or digits meet an
// underscore, and scans on to the end of the whitespace-free run each time, so a run such as
// `a_a_a_…` cost time quadratic in its length: 64 KiB of `a_` saved in 46 s. search_text (migration
// 0056) breaks every run with a space after its sixteenth underscore-joined segment, which bounds
// the rescans, and each search trigger hands the parser search_text's answer. The parser's work
// never reaches a count this test can read; its input does. So the row holds the input: the
// triggers of the two tables these saves write read their text through search_text
// (assertSearchTriggersNormalise), and search_text leaves no run of more than searchTextSegments
// segments in the text of each save (searchRunSegments), counted before it is saved. With
// search_text answering its input unchanged, 64 KiB of `a_` reads as one run of 32,763 segments.
//
// pmdoc's Update wrote each new block at its own index, and ygo walks a fragment from its first
// child to the index it is given, so replacing a quote of n headings with a quote of n rules took
// n²/2 steps: 10.5 s of CPU at 21,844 blocks, the most one write's element limit allows of it,
// where it takes 1.5 s with one insert for the run. The row counts those steps
// (pmdoc.CountWalksForTest): a replacement may walk past no more children than its document holds
// blocks. With an insert for each block, 682 blocks walk past 232,221.
//
// The ladders keep their sizes, each save is answered 201, and the largest of each is the most a
// caller can send: a mebibyte of document, contracts.DispatchBlockUnits characters of comment, and
// 21,844 replaced blocks. Written as paragraph text, the larger document runs make more elements
// than one write may (TestUploadRefusesADocumentTooLargeToStore), so each run is a code block's
// text, which the document stores and search indexes whole; pmdoc's linear-time test holds the
// parser on the same runs. An issue title is capped at 1,000 characters (contracts.IssueTitleMax),
// and its index reads it through search_text too.
//
// What no count here holds: a change to the save that costs CPU and walks no more children and
// leaves search_text's runs as they are, in Postgres or in Go.
//
// Settlement is held off, so no settlement writes a document while a replacement's walks are
// counted, and the test server's shutdown, which would otherwise spend its drain budget settling
// mebibyte documents, finds no document loaded and leaves each to resume. No test in this package
// calls t.Parallel, so no other write runs beside the one counted.
func TestSavingAPathologicalBodyIsBounded(t *testing.T) {
	handler, database, _ := newTestServer(t, testServerOptions{settle: time.Hour})
	issue := createArtifactIssue(t, handler)
	assertSearchTriggersNormalise(t, database)
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
				assertSearchRunsBounded(t, database, fmt.Sprintf("a document of %s of `%s`", sizeText(size), test.shape), body)
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
	// Replacing a quote of many headings with a quote of as many rules, every block changing type,
	// is one run of replaced blocks: Update deletes the run with one call and inserts its
	// replacements with another, each walking to the run's first index.
	t.Run("replacing many blocks", func(t *testing.T) {
		const from, to = "> #\n", "> ***\n"
		owner := "/api/v1/issues/" + issue.Key + "/artifacts"
		for _, blocks := range []int{682, 5_461, 21_844} {
			documents++
			name := fmt.Sprintf("many-blocks-%d.md", documents)
			upload := func(body []byte, fields map[string]string) {
				response := multipartRequest(t, handler, owner, fields, "body.md", "text/markdown", body, "alice")
				if response.Code != http.StatusCreated {
					t.Fatalf("upload %d blocks of %q: status=%d body=%.200s", blocks, body[:len(from)], response.Code, response.Body.String())
				}
			}
			upload([]byte(strings.Repeat(from, blocks)), map[string]string{"name": name})
			fields := map[string]string{"name": name, "precondition": encodeUploadBase(t, uploadBaseNamed(t, handler, owner, name))}
			var walked atomic.Int64
			stop := pmdoc.CountWalksForTest(&walked)
			upload([]byte(strings.Repeat(to, blocks)), fields)
			stop()
			t.Logf("replacing a quote of %d headings with a quote of %d rules walked past %d children, at most %d", blocks, blocks, walked.Load(), blocks)
			if walked.Load() > int64(blocks) {
				t.Fatalf("replacing a quote of %d headings with a quote of %d rules walked past %d children, want at most the %d blocks it writes: the write walks to each block it inserts",
					blocks, blocks, walked.Load(), blocks)
			}
		}
	})
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

// assertSearchTriggersNormalise holds the triggers that index what this test saves to reading it
// through search_text: the one trigger function of each table that writes the row's search
// vector, read from the catalog as the database runs it, applies search_text to the column it
// indexes. A trigger that handed the parser the raw text would index the same lexemes, so no
// stored vector tells the two apart.
func assertSearchTriggersNormalise(t *testing.T, database *store.Store) {
	t.Helper()
	for table, column := range map[string]string{"artifact_versions": "markdown", "comments": "body"} {
		rows, err := database.Pool.Query(context.Background(), `
			select p.proname, p.prosrc
			from pg_trigger tr
			join pg_proc p on p.oid = tr.tgfoid
			where tr.tgrelid = $1::regclass and not tr.tgisinternal and p.prosrc like '%new.search :=%'`, table)
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
		if len(writers) != 1 {
			t.Fatalf("%s has %d trigger functions that write its search vector (%v), want one", table, len(writers), writers)
		}
		for name, source := range writers {
			if want := "search_text(new." + column + ")"; !strings.Contains(source, want) {
				t.Fatalf("%s's search trigger %s does not read %s through search_text (%s):\n%s", table, name, column, want, source)
			}
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
