package api

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// Saving a pathological body costs work linear in it (LEGION-465). Before the fix Postgres's
// search indexing read a run of `a_` or `q_` in quadratic time, and pmdoc parsed `a_b*` in nearly
// quadratic time. On one loaded machine, before the fix and after it at the same moment, 64 KiB of
// `a_` saved in 46 s and 0.3 s, 128 KiB of `a_b*` in 33 s and 1.3-2.1 s, an issue titled with
// 32 KiB of `q_` in 12 s and 0.06-0.17 s, and the duplicate check against a 16 KiB title took
// 18-21 s and 0.12-0.31 s. Times follow the runner's load, so no clock bound holds on every runner,
// and this test reads no clock: it counts work, and reads the structure that bounds it.
//
// Postgres's quadratic read was its text-search parser's (0056_search_text): wherever letters or
// digits meet an underscore it tries to read a host name to the end of the run, so a run costs the
// square of its length, all of it processor time, which no count of pages sees. On Postgres 18.6,
// `select to_tsvector('english', t) from (select repeat('a_', n) t offset 0) s` under EXPLAIN
// (ANALYZE, BUFFERS), the subquery keeping the text out of the planner's constant folding, read:
//
//	text     raw         through search_text   shared buffers, both
//	1 KiB    26 ms       8 ms                  22 hit, 0 read
//	8 KiB    2.2 s       75 ms                 22 hit, 0 read
//	16 KiB   8.1 s       96 ms                 22 hit, 0 read
//	32 KiB   36.3 s      178 ms                22 hit, 0 read
//
// So the fix is held by what search_text leaves and by what each search trigger stores. search_text
// leaves no run of more than searchTextRun underscore-joined segments in any body saved here
// (requireShortSearchRuns), which is what keeps the parser's rescans short. Every trigger that
// builds a search vector is saved, through a write a caller makes, a text that opens with a run
// the parser reads as one token (searchProbe), and the vector it stored must hold that run broken
// where search_text breaks it (requireShortVectorRuns): a trigger that hands the parser its text
// without search_text stores the run whole. That reads what the trigger did, so it fails a trigger
// that names search_text only in a comment, which a read of the function's source passes, and it
// reads no source text. Beside them each save's indexing statement is counted in the shared buffers
// it reads (indexingBuffers) and held to linear growth (growsLinearly), which bounds the statement's
// own reads of the text: its TOAST and its index entries. A trigger that runs after its statement
// does its work past the plan whose buffers EXPLAIN counts, so the two that do are held by what
// they store alone: artifact_versions_document_search copies the vector artifact_versions_search
// built, which the document rungs count, and artifact_versions_issues_search stores the issue's
// vector over its spec, which issues_search builds the same way before its statement, where the
// issue's row counts it.
//
// Written as paragraph text, the larger runs make more elements than one write may
// (TestUploadRefusesADocumentTooLargeToStore), so each run is a code block's text: the document
// stores it whole and search indexes it whole, and pmdoc's linear-time test holds the parser on
// the same runs. The `)_` ladder stops at 256 KiB, as before the element limit. `)_` saved in
// linear time before the fix too and is held to the same growth.
//
// A comment, a comment edit, a message and a broadcast hold up to contracts.DispatchBlockUnits
// characters in one block - 32,000 since INFRA-45, sixteen times the 2,000 that was too short
// for a ladder to tell the code before the fix from the code after it, whose removed timings
// measured a 2,000-character `a_` comment at 51 ms before the fix and 56 ms after. That bound is
// laddered here ("comment a_"), a one-block `a_` comment at it being the longest pathological
// text a caller can write outside a document, so a write that stops being bounded at the block
// bound fails this test rather than waiting for a reader to notice. An issue title is capped at
// 1,000 (contracts.IssueTitleMax), too short to ladder; the title's ladders, and the duplicate
// check's against a stored title, went with the title cap
// (TestIssueTitlesAreCappedOnCreateAndRetitle). Every one of these texts is indexed through
// search_text, which each trigger's row holds by the vector it stores.
//
// Replacing a document of many short blocks with as many others, every block changing type, also
// took work that grew with the square of its blocks (LEGION-465): pmdoc wrote each new block at its
// own index, and ygo walks the fragment to each index it is given. A quote of n headings replaced
// with a quote of n rules took 34 ms of server CPU at 682 blocks and 10.5 s at 21,844 before the
// fix and 27 ms and 1.5 s after it. That replacement is held in pmdoc, at 2,730 and 21,845 blocks,
// where Go's coverage counters count ygo's walks wherever pmdoc makes them
// (TestUpdateIsLinearInTheBlockCount, "replacing a quote of headings with a quote of rules"), and
// not here: the count pmdoc keeps of itself (UpdateWork) is taken where the fix put its walks, so a
// revert to inserts made one index at a time walks ygo past it.
//
// Settlement is held off. A document's settlement runs on a goroutine of its own after the save,
// beside the counts a rung reads, and the test server's shutdown, which would otherwise spend its
// drain budget settling 1 MiB documents, finds no document loaded and leaves each to resume.
func TestSavingAPathologicalBodyIsBounded(t *testing.T) {
	handler, database, _ := newTestServer(t, testServerOptions{settle: time.Hour})
	issue := createArtifactIssue(t, handler)
	documents := 0
	// Each search trigger is held by what it stores: its row saves, through a write a caller makes
	// to the trigger's table, a text that opens with searchProbe and runs on in `a_`, then reads the
	// vector the trigger built of it (requireShortVectorRuns) and the runs search_text leaves in the
	// text (requireShortSearchRuns). Where the trigger's text can be long enough to ladder, the row
	// ladders it as the rungs after them do, its indexing statement held to linear growth. An
	// issue's title is checked at its cap alone, and each trigger that runs after its statement at
	// one size, for the reason the header gives. The rows run first: see the check after them.
	createIssue := func(t *testing.T, title, spec string) string {
		t.Helper()
		input := map[string]any{"project": "TEST", "title": title, "force": true}
		if spec != "" {
			input["spec"] = spec
		}
		response := dispatchRequest(t, handler, http.MethodPost, "/api/v1/issues", input, "alice")
		if response.Code != http.StatusCreated {
			t.Fatalf("create the issue %q: status=%d body=%.200s", title, response.Code, response.Body.String())
		}
		return decodeBody[model.Issue](t, response).Key
	}
	retitle := func(t *testing.T, key, title string) {
		t.Helper()
		response := dispatchRequest(t, handler, http.MethodPatch, "/api/v1/issues/"+key, map[string]any{"title": title}, "alice")
		if response.Code != http.StatusOK {
			t.Fatalf("retitle %s: status=%d body=%.200s", key, response.Code, response.Body.String())
		}
	}
	uploadProbe := func(t *testing.T) string {
		t.Helper()
		documents++
		response := multipartRequest(t, handler, "/api/v1/issues/"+issue.Key+"/artifacts", map[string]string{
			"name": fmt.Sprintf("pathological-%d.md", documents),
		}, "body.md", "text/markdown", []byte(probedDocument(1<<10)), "alice")
		if response.Code != http.StatusCreated {
			t.Fatalf("upload a document holding the probe: status=%d body=%.200s", response.Code, response.Body.String())
		}
		return decodeBody[struct {
			Artifact model.Artifact `json:"artifact"`
		}](t, response).Artifact.ID
	}
	const (
		issueVector = `select search from issues where key = $1`
		issueSpec   = `select v.markdown from artifact_versions v join artifacts a on a.id = v.artifact_id
			where a.issue_key = $1 and a.is_primary order by v.number desc limit 1`
		documentText = `select markdown from artifact_versions where artifact_id = $1`
	)
	t.Run("trigger issues_search", func(t *testing.T) {
		// The title, at its cap, through a retitle: the one write whose vector issues_search alone
		// stores. The trigger stores the title's lexemes beside the vector.
		key := createIssue(t, "A title to probe", "")
		title := probedRun(contracts.IssueTitleMax)
		retitle(t, key, title)
		requireShortSearchRuns(t, database, "an issue title of "+sizeText(len(title)), `select title from issues where key = $1`, key)
		requireShortVectorRuns(t, database, "an issue retitled with the probe", issueVector, key)
		requireShortVectorRuns(t, database, "the title lexemes of an issue retitled with the probe", `select array_to_tsvector(title_lexemes) from issues where key = $1`, key)
		// The spec, which issues_search reads into the issue's vector too (issue_embedding_spec),
		// laddered as a document is: each rung's retitle stores the vector issues_search built
		// over it, and its indexing statement is the issue's own write of its title. The vector the
		// spec's first version stored (artifact_versions_issues_search) is read first, at every
		// rung: a spec indexed without search_text would take hours at the top rung.
		growsLinearly(t, "indexing an issue's spec of `a_`", "buffers", []int{1 << 10, 16 << 10, 256 << 10}, func(size int) uint64 {
			key := createIssue(t, "A spec of "+sizeText(size)+" to probe", probedDocument(size))
			requireShortSearchRuns(t, database, "an issue spec of "+sizeText(size), issueSpec, key)
			requireShortVectorRuns(t, database, "an issue whose first spec of "+sizeText(size)+" holds the probe", issueVector, key)
			retitle(t, key, "A spec of "+sizeText(size)+" to probe, indexed again")
			requireShortVectorRuns(t, database, "an issue retitled over a spec of "+sizeText(size)+" holding the probe", issueVector, key)
			return indexingBuffers(t, database, `update issues set title = title where key = $1`, key)
		})
	})
	t.Run("trigger artifact_versions_issues_search", func(t *testing.T) {
		// An issue's spec, its primary document's first version: once the version is in, the
		// trigger stores the issue's vector over it, and no later write of the issue does.
		size := 1 << 10
		key := createIssue(t, "A first spec to probe", probedDocument(size))
		requireShortSearchRuns(t, database, "an issue spec of "+sizeText(size), issueSpec, key)
		requireShortVectorRuns(t, database, "an issue whose first spec holds the probe", issueVector, key)
	})
	t.Run("trigger artifact_versions_search", func(t *testing.T) {
		id := uploadProbe(t)
		requireShortSearchRuns(t, database, "a document holding the probe", documentText, id)
		requireShortVectorRuns(t, database, "a document holding the probe", `select search from artifact_versions where artifact_id = $1`, id)
	})
	t.Run("trigger artifact_versions_document_search", func(t *testing.T) {
		id := uploadProbe(t)
		requireShortSearchRuns(t, database, "a document holding the probe", documentText, id)
		requireShortVectorRuns(t, database, "the document_search row of a document holding the probe", `select search from document_search where artifact_id = $1`, id)
	})
	t.Run("trigger comments_search", func(t *testing.T) {
		// The comment rung after the rows ladders a comment's indexing; this is the vector one
		// stores.
		size := contracts.DispatchBlockUnits / 32
		response := dispatchRequest(t, handler, http.MethodPost, "/api/v1/issues/"+issue.Key+"/comments", map[string]any{
			"body": probedRun(size),
		}, "alice")
		if response.Code != http.StatusCreated {
			t.Fatalf("comment holding the probe: status=%d body=%.200s", response.Code, response.Body.String())
		}
		comment := decodeBody[model.Comment](t, response)
		requireShortSearchRuns(t, database, "a comment of "+sizeText(size)+" holding the probe", `select body from comments where id = $1`, comment.ID)
		requireShortVectorRuns(t, database, "a comment of "+sizeText(size)+" holding the probe", `select search from comments where id = $1`, comment.ID)
	})
	t.Run("trigger asks_search", func(t *testing.T) {
		// An ask's question and its options share one bound, so the last rung leaves room for
		// the one option.
		const option = "Yes"
		sizes := []int{contracts.DispatchEventBodyMax / 32, contracts.DispatchEventBodyMax / 4, contracts.DispatchEventBodyMax - len(option)}
		growsLinearly(t, "indexing an ask of `a_`", "buffers", sizes, func(size int) uint64 {
			response := dispatchRequest(t, handler, http.MethodPost, "/api/v1/issues/"+issue.Key+"/asks", map[string]any{
				"question": probedRun(size), "options": []map[string]string{{"label": option}},
			}, "alice")
			if response.Code != http.StatusCreated {
				t.Fatalf("ask of %s holding the probe: status=%d body=%.200s", sizeText(size), response.Code, response.Body.String())
			}
			ask := decodeBody[model.Ask](t, response)
			requireShortSearchRuns(t, database, "an ask's question of "+sizeText(size)+" holding the probe", `select question from asks where id = $1`, ask.ID)
			requireShortVectorRuns(t, database, "an ask whose question of "+sizeText(size)+" holds the probe", `select search from asks where id = $1`, ask.ID)
			return indexingBuffers(t, database, `update asks set question = question where id = $1`, ask.ID)
		})
	})
	t.Run("trigger messages_search", func(t *testing.T) {
		sizes := []int{contracts.DispatchBlockUnits / 32, contracts.DispatchBlockUnits / 4, contracts.DispatchBlockUnits}
		growsLinearly(t, "indexing a message of `a_`", "buffers", sizes, func(size int) uint64 {
			response := dispatchRequest(t, handler, http.MethodPost, "/api/v1/issues/"+issue.Key+"/messages", map[string]any{
				"body": probedRun(size),
			}, "alice")
			if response.Code != http.StatusCreated {
				t.Fatalf("message of %s holding the probe: status=%d body=%.200s", sizeText(size), response.Code, response.Body.String())
			}
			message := decodeBody[model.Message](t, response)
			requireShortSearchRuns(t, database, "a message of "+sizeText(size)+" holding the probe", `select body from messages where id = $1`, message.ID)
			requireShortVectorRuns(t, database, "a message of "+sizeText(size)+" holding the probe", `select search from messages where id = $1`, message.ID)
			return indexingBuffers(t, database, `update messages set body = body where id = $1`, message.ID)
		})
	})

	// The rungs below ladder texts up to a mebibyte. A trigger that hands Postgres's parser its
	// text whole reads such a text in time that grows with the square of its length, hours at the
	// top rungs, so the run would end at the binary's deadline naming no row: once a trigger's row
	// above has failed, they do not run.
	if t.Failed() {
		return
	}

	for _, test := range []struct {
		shape string
		sizes []int
	}{
		{"a_", []int{1 << 10, 64 << 10, 1 << 20}},
		{"a_b*", []int{2 << 10, 128 << 10, 1 << 20}},
		{")_", []int{256, 16 << 10, 256 << 10}},
	} {
		t.Run("document "+test.shape, func(t *testing.T) {
			growsLinearly(t, "indexing a document of `"+test.shape+"`", "buffers", test.sizes, func(size int) uint64 {
				documents++
				const fence = "```\n"
				body := fence + strings.Repeat(test.shape, (size-2*len(fence)-1)/len(test.shape)) + "\n" + fence
				response := multipartRequest(t, handler, "/api/v1/issues/"+issue.Key+"/artifacts", map[string]string{
					"name": fmt.Sprintf("pathological-%d.md", documents),
				}, "body.md", "text/markdown", []byte(body), "alice")
				if response.Code != http.StatusCreated {
					t.Fatalf("upload %s of %q: status=%d body=%.200s", sizeText(size), test.shape, response.Code, response.Body.String())
				}
				saved := decodeBody[struct {
					Artifact model.Artifact `json:"artifact"`
				}](t, response).Artifact
				requireShortSearchRuns(t, database, sizeText(size)+" of `"+test.shape+"`", `select markdown from artifact_versions where artifact_id = $1`, saved.ID)
				return indexingBuffers(t, database, `update artifact_versions set markdown = markdown where artifact_id = $1`, saved.ID)
			})
		})
	}
	// A comment's whole body is one search_text run, as a document's is, so the same pathological
	// shape is counted at the longest block a caller may send, in three rungs derived from it: the
	// block bound is a thirty-second of the largest document rung, enough of a span for a ladder
	// since INFRA-45.
	t.Run("comment a_", func(t *testing.T) {
		const shape = "a_"
		sizes := []int{contracts.DispatchBlockUnits / 32, contracts.DispatchBlockUnits / 4, contracts.DispatchBlockUnits}
		growsLinearly(t, "indexing a comment of `"+shape+"`", "buffers", sizes, func(size int) uint64 {
			body := strings.Repeat(shape, size/len(shape))
			response := dispatchRequest(t, handler, http.MethodPost, "/api/v1/issues/"+issue.Key+"/comments", map[string]any{
				"body": body,
			}, "alice")
			if response.Code != http.StatusCreated {
				t.Fatalf("comment of %s of %q: status=%d body=%.200s", sizeText(size), shape, response.Code, response.Body.String())
			}
			comment := decodeBody[model.Comment](t, response)
			requireShortSearchRuns(t, database, "a comment of "+sizeText(size)+" of `"+shape+"`", `select body from comments where id = $1`, comment.ID)
			return indexingBuffers(t, database, `update comments set body = body where id = $1`, comment.ID)
		})
	})
}

// indexingBuffers is how many shared buffers PostgreSQL reads to index a stored text once more:
// statement writes the text back to itself, which fires its table's search trigger as the save's
// own write did, under EXPLAIN (ANALYZE, BUFFERS) in a transaction rolled back. Hit plus read is
// every page the statement asked for, whether the cache held it or not, so the count is the
// statement's work and not the cache's state.
func indexingBuffers(t *testing.T, database *store.Store, statement string, args ...any) uint64 {
	t.Helper()
	ctx := context.Background()
	tx, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the explain of %s: %v", statement, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var explained []struct {
		Plan struct {
			SharedHit  uint64 `json:"Shared Hit Blocks"`
			SharedRead uint64 `json:"Shared Read Blocks"`
		} `json:"Plan"`
	}
	if err := tx.QueryRow(ctx, "explain (analyze, buffers, format json) "+statement, args...).Scan(&explained); err != nil {
		t.Fatalf("explain %s: %v", statement, err)
	}
	if len(explained) != 1 {
		t.Fatalf("explain %s gave %d plans, want one", statement, len(explained))
	}
	return explained[0].Plan.SharedHit + explained[0].Plan.SharedRead
}

// searchTextRun is the most underscore-joined segments search_text leaves in one run: it breaks a
// run with a space after every sixteenth segment (0056_search_text), which bounds how far
// Postgres's parser rescans a run from each place in it.
const searchTextRun = 16

// requireShortSearchRuns fails t when search_text leaves a run of more than searchTextRun
// underscore-joined segments in the text stored selects, what being the text's name: that run is
// what the search triggers hand Postgres's parser. The run is read with search_text's own
// segment class.
func requireShortSearchRuns(t *testing.T, database *store.Store, what, stored string, args ...any) {
	t.Helper()
	var longest int
	if err := database.Pool.QueryRow(context.Background(), `
		select coalesce(max(length(run[1]) - length(replace(run[1], '_', '')) + 1), 0)::int
		  from (`+stored+`) s(body),
		       regexp_matches(search_text(s.body), '([A-Za-z0-9.-]+(?:_[A-Za-z0-9.-]+)*)', 'g') run
	`, args...).Scan(&longest); err != nil {
		t.Fatalf("read the longest run search_text leaves in %s: %v", what, err)
	}
	if longest > searchTextRun {
		t.Fatalf("search_text left a run of %d underscore-joined segments in %s, past the %d it breaks a run at (0056_search_text): Postgres's parser reads a run in time that grows with the square of its length",
			longest, what, searchTextRun)
	}
}

// searchProbe opens the text of each trigger's row: 64 underscore-joined segments of `a.b`, a run
// Postgres's parser reads whole as one token (a file name), so the vector a trigger stores of it
// shows how long a run the trigger handed the parser. Through search_text its longest lexeme is
// searchTextRun segments; handed the text itself, the parser makes one lexeme of all 64. A run of
// `a_` leaves no such mark: the parser reads its one-letter words apart at each underscore and the
// english configuration drops them as stop words, so its vector is the same whether search_text
// broke the run or not.
var searchProbe = strings.Repeat("a.b_", 64)

// probedRun is a text of at most size characters: searchProbe, a space, and a run of `a_` that
// fills the rest.
func probedRun(size int) string {
	return searchProbe + " " + strings.Repeat("a_", (size-len(searchProbe)-1)/2)
}

// probedDocument is a document of at most size bytes whose one code block holds probedRun: a code
// block's text is stored whole, as the document rungs' is.
func probedDocument(size int) string {
	const fence = "```\n"
	return fence + probedRun(size-2*len(fence)-1) + "\n" + fence
}

// requireShortVectorRuns fails t unless the vector stored selects - one a search trigger built of a
// text holding searchProbe, what being its name - holds a lexeme of the probe's underscore-joined
// run, and none of more than searchTextRun segments. Its longest lexeme is the longest run the
// trigger handed Postgres's parser, so a trigger that reads its text through search_text passes,
// and one that hands the parser the text itself fails, whatever its source says. A vector that
// holds none of the run shows nothing, so that fails too: the parser drops a token past 2,047
// characters, which a long enough run handed it whole would be.
func requireShortVectorRuns(t *testing.T, database *store.Store, what, stored string, args ...any) {
	t.Helper()
	var longest int
	if err := database.Pool.QueryRow(context.Background(), `
		select coalesce(max(length(u.lexeme) - length(replace(u.lexeme, '_', ''))), 0)::int
		  from (`+stored+`) s(vector), unnest(s.vector) u
	`, args...).Scan(&longest); err != nil {
		t.Fatalf("read the longest run in the vector stored for %s: %v", what, err)
	}
	switch {
	case longest == 0:
		t.Fatalf("the vector stored for %s holds no lexeme of the probe's underscore-joined run, so it shows nothing of what its trigger handed Postgres's parser", what)
	case longest > searchTextRun:
		t.Fatalf("the vector stored for %s holds a run of %d underscore-joined segments, past the %d search_text breaks a run at (0056_search_text): its trigger handed Postgres's parser text no search_text read first, so the `a_` run after the probe reached the parser whole too, which reads such a run in time that grows with the square of its length",
			what, longest, searchTextRun)
	}
}

// TestGrowsLinearlyRejectsQuadraticFinalRung proves the final rung's bound stays anchored to the
// first rung's count, even when the rung between fills the growth it may take: a final rung held
// to the rung before it would pass work that grows with the square of the input.
func TestGrowsLinearlyRejectsQuadraticFinalRung(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=^TestGrowsLinearlyRejectsQuadraticFinalRungHelper$")
	command.Env = append(os.Environ(), "GROWS_LINEARLY_HELPER=true")
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("quadratic final rung passed:\n%s", output)
	}
	if !strings.Contains(string(output), "at "+sizeText(quadraticFinalRung)) {
		t.Fatalf("quadratic final rung failed unexpectedly:\n%s", output)
	}
}

// quadraticFinalRung is the last size of the helper's ladder, the rung whose count grows
// quadratically and fails the bound.
const quadraticFinalRung = 64

func TestGrowsLinearlyRejectsQuadraticFinalRungHelper(t *testing.T) {
	if _, helper := os.LookupEnv("GROWS_LINEARLY_HELPER"); !helper {
		t.Skip("subprocess helper")
	}
	growsLinearly(t, "a quadratic final rung", "steps", []int{1, 8, quadraticFinalRung}, func(size int) uint64 {
		switch size {
		case 1:
			return 1
		case 8:
			return 32
		case quadraticFinalRung:
			return uint64(size * size / 4)
		default:
			t.Fatalf("unexpected size %d", size)
			return 0
		}
	})
}

// growthAllowance is the permitted variation in work per input byte from the first rung. Work
// linear in the input takes no more per input byte as it grows, and less while a fixed cost counts,
// so each later rung may take four times the first rung's count per byte. Quadratic growth exceeds
// that headroom across this ladder's input ratios.
const growthAllowance = 4

// growsLinearly counts a three-rung ladder's work and holds each later rung to the first. The
// counts are the work itself, which no load on the machine moves, so each size is counted once. A
// count of none at the first size fails, since a growth from nothing proves nothing.
func growsLinearly(t *testing.T, what, unit string, sizes []int, counted func(size int) uint64) {
	t.Helper()
	firstSize := sizes[0]
	first := counted(firstSize)
	if first == 0 {
		t.Fatalf("%s counted no %s at %s, so its growth says nothing: none of the work it does is counted", what, unit, sizeText(firstSize))
	}
	for _, size := range sizes[1:] {
		checkLinearGrowth(t, what, unit, firstSize, first, size, counted(size))
	}
}

func checkLinearGrowth(t *testing.T, what, unit string, firstSize int, first uint64, size int, count uint64) {
	t.Helper()
	growth := size / firstSize
	allowed := uint64(growthAllowance*growth) * first
	ratio := float64(count) / float64(first)
	if count > allowed {
		t.Fatalf("%s took %d %s at %s, %.0f times the %d it took at %s: the input grew %d times, and work linear in it may take %d at most (growthAllowance)",
			what, count, unit, sizeText(size), ratio, first, sizeText(firstSize), growth, allowed)
	}
	t.Logf("%s: %d %s at %s, %d at %s, %.1f times for an input %d times larger (at most %d)", what, first, unit, sizeText(firstSize), count, sizeText(size), ratio, growth, allowed)
}

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
