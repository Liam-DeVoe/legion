//go:build basebuild

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// dispatch://INFRA-1152's two measurements, taken before its first line of product code (spec
// R11, plan T1), over the corpus one_inbox_scale_seed_test.go seeds:
//
//   - the participant set (TestOneInboxParticipantSetOfTheCorpus): R11's three statements over the
//     corpus on this build's own schema, which is the base's until the first product change. The
//     stop rule is R4's: a thread of the corpus above oneInboxParticipantBound participants stops
//     the work, and fails this test.
//   - the write baseline (TestOneInboxWriteBaseline): every write path R11 names, timed against
//     the build of the commit BASELINE_SHA names, booted over the database c-p99 names. Each
//     reading is the round trip on loopback, because the base build's own clock is not this
//     process's to read, and 200 of them give a 99th percentile that is the 198th reading rather
//     than the slowest. It counts too the rows one comment in the widest thread writes, and fails
//     where its per-participant rows pass two for each participant (R4's append and update).
//
// Each prints its readings on lines of their own, under the prefixes measurement-one: and
// measurement-two:, each with the moment it was taken, which is what the pull request body
// carries. Both run when LEGION_SCALE_TEST is 1, as a14 does (inbox_dismissed_scale_test.go):
// seeding the corpus and taking 2,800 timed readings costs minutes, which no ordinary run pays.

// The participant set, in exactly R11's words: the participants user_threads holds per thread, and
// the count over the writers user_threads cannot hold, the sessions, over comments and over messages
// (a lower bound for messages, whose root can be further up than in_reply_to).
const (
	oneInboxThreadsStatement  = `select max(n), percentile_disc(0.999) within group (order by n), count(*) from (select thread_id, count(*) as n from user_threads group by thread_id) t`
	oneInboxCommentsStatement = `select max(n) from (select coalesce(ask_id, reply_to, id) as root, count(distinct author->>'id') as n from comments group by 1) t`
	oneInboxMessagesStatement = `select max(n) from (select coalesce(in_reply_to, id) as root, count(distinct author->>'id') as n from messages group by 1) t`
)

// The base build's database and worktree, as the Verification preamble names them for c-p99.
const (
	oneInboxDatabaseEnv = "ISSUE_P99_DATABASE_URL"
	oneInboxDatabase    = "dispatch_infra_1152_p99"
	oneInboxWorktree    = "base-p99"
)

// oneInboxSettle bounds the wait for the base build to drain what the corpus owes its background
// work: every comment the seed wrote is owed a block index and a grammar pass, which the base
// build's own reconciliation pays off a batch at a time. A timing taken while it still runs is a
// timing of that work too.
const oneInboxSettle = 30 * time.Minute

// oneInboxOwedWork is what the base build still owes the corpus, as its block index tick counts it
// (docs/block_index.go): bodies owed a block-grammar pass, sources owed a grammar pass, and
// comments, messages and asks owed a block index of their own or their windows.
const oneInboxOwedWork = `
	select (select count(*) from block_grammar_owed),
	       (select count(*) from refs_grammar_owed),
	       (select count(*) from comment_block_state where owed_at is not null or windows_owed_at is not null)
	         + (select count(*) from message_block_state where owed_at is not null or windows_owed_at is not null)
	         + (select count(*) from ask_block_state where owed_at is not null or windows_owed_at is not null)`

// TestOneInboxParticipantSetOfTheCorpus is measurement one: the corpus seeded on a database of the
// test's own through this build's handler, and R11's three statements read over it.
func TestOneInboxParticipantSetOfTheCorpus(t *testing.T) {
	if os.Getenv(dismissedScaleEnv) != "1" {
		t.Skipf("set %s=1 to take the participant set of the one-inbox corpus", dismissedScaleEnv)
	}
	handler, database, _ := newTestServer(t, testServerOptions{envoyURL: standInListener(t)})
	seedOneInboxCorpus(t, database, handlerCaller{handler: handler})

	ctx := context.Background()
	var widest, p999, threads int64
	if err := database.Pool.QueryRow(ctx, oneInboxThreadsStatement).Scan(&widest, &p999, &threads); err != nil {
		t.Fatalf("read the participants of each thread: %v", err)
	}
	t.Logf("measurement-one: user_threads max=%d p99.9=%d threads=%d taken_at=%s", widest, p999, threads, oneInboxNow())
	var commented int64
	if err := database.Pool.QueryRow(ctx, oneInboxCommentsStatement).Scan(&commented); err != nil {
		t.Fatalf("read the writers of each comment thread: %v", err)
	}
	t.Logf("measurement-one: comments max=%d taken_at=%s", commented, oneInboxNow())
	var messaged int64
	if err := database.Pool.QueryRow(ctx, oneInboxMessagesStatement).Scan(&messaged); err != nil {
		t.Fatalf("read the writers of each message thread: %v", err)
	}
	t.Logf("measurement-one: messages max=%d taken_at=%s", messaged, oneInboxNow())

	for _, read := range []struct {
		statement string
		widest    int64
	}{{"user_threads", widest}, {"comments", commented}, {"messages", messaged}} {
		if read.widest > oneInboxParticipantBound {
			t.Errorf("stop rule: a thread of the corpus holds %d participants by %s, above R4's bound of %d",
				read.widest, read.statement, oneInboxParticipantBound)
		}
	}
}

// TestOneInboxWriteBaseline is measurement two: the build of BASELINE_SHA, checked out at
// $TMPDIR/base-p99 and booted over $ISSUE_P99_DATABASE_URL, which it migrates; the corpus seeded
// into that database once the migration has run, through its pool and through the base build's
// API; and every write path R11 names timed against it, 200 readings each.
func TestOneInboxWriteBaseline(t *testing.T) {
	if os.Getenv(dismissedScaleEnv) != "1" {
		t.Skipf("set %s=1 to take the one-inbox write baseline", dismissedScaleEnv)
	}
	databaseURL := baselineDatabaseURL(t, oneInboxDatabaseEnv, oneInboxDatabase)
	binary, commit := buildBaseline(t, filepath.Join(os.TempDir(), oneInboxWorktree))
	server := bootBaseline(t, binary, databaseURL)
	ctx := context.Background()
	database, err := store.Open(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open the database %s names: %v", oneInboxDatabaseEnv, err)
	}
	t.Cleanup(database.Pool.Close)

	corpus := seedOneInboxCorpus(t, database, server)
	settleOneInboxBase(t, database)
	// Statistics, as a production table has them, and the visibility map a freshly loaded table
	// lacks: without them the planner reads this corpus as the empty tables it was migrated as.
	if _, err := database.Pool.Exec(ctx, "vacuum analyze"); err != nil {
		t.Fatalf("vacuum the corpus: %v", err)
	}

	t.Logf("measurement-two: base build %s, %d readings of each write path after one not counted, taken_at=%s",
		commit, oneInboxReadings, oneInboxNow())
	for _, path := range oneInboxWritePaths(server, database, corpus) {
		measureOneInboxPath(t, path)
	}
	countOneInboxCommentRows(t, server, database, corpus)
}

// handlerCaller sends the corpus's requests to this build's handler in the process: a person by
// the X-Dispatch-User header newTestServer trusts, and the agent bearer by its shared token.
type handlerCaller struct {
	handler http.Handler
}

func (c handlerCaller) call(t *testing.T, login, method, path string, body any) apiAnswer {
	t.Helper()
	started := time.Now()
	var response *httptest.ResponseRecorder
	if login == "" {
		response = bearerRequest(t, c.handler, method, path, body)
	} else {
		response = dispatchRequest(t, c.handler, method, path, body, login)
	}
	return apiAnswer{status: response.Code, body: response.Body.Bytes(), took: time.Since(started)}
}

// settleOneInboxBase waits for the base build to drain what the corpus owes its background work
// (oneInboxOwedWork), saying how much is left once a minute.
func settleOneInboxBase(t *testing.T, database *store.Store) {
	t.Helper()
	started := time.Now()
	said := started
	for {
		var grammar, texts, items int64
		if err := database.Pool.QueryRow(context.Background(), oneInboxOwedWork).Scan(&grammar, &texts, &items); err != nil {
			t.Fatalf("read what the base build owes the corpus: %v", err)
		}
		if grammar+texts+items == 0 {
			t.Logf("the base build drained what the corpus owed it in %s", time.Since(started).Round(time.Second))
			return
		}
		if time.Since(started) > oneInboxSettle {
			t.Fatalf("after %s the base build still owes %d block-grammar passes, %d grammar passes and %d item block indexes",
				oneInboxSettle, grammar, texts, items)
		}
		if time.Since(said) >= time.Minute {
			t.Logf("the base build still owes %d block-grammar passes, %d grammar passes and %d item block indexes",
				grammar, texts, items)
			said = time.Now()
		}
		time.Sleep(2 * time.Second)
	}
}

// oneInboxPath is one write path the baseline times: its name in the printed line, one reading of
// it, and what the line prints beside the times, read once every reading is in. A reading puts back
// off the clock whatever it moved that the next reading would otherwise find moved, or writes to a
// target of its own the seed made one of per reading, so every reading measures the same write.
type oneInboxPath struct {
	name   string
	run    func(t *testing.T, reading int) time.Duration
	beside func(t *testing.T) string
}

// oneInboxWritePaths are the write paths R11 names, each found in the route table by its handler,
// in the order they are measured: a comment and a mention (createComment), an issue message
// (createMessage), a broadcast (createBroadcast), a session's direct reply (replyMessage), an ask's
// answer (answerAsk), a dismissal (dismissInbox), Mark all read (markInboxRead), a thread read
// (putThreadRead), an agent's Clear and read (putUserAgentState), a snooze (putAskSnooze), and the
// three writes that move the person an ask waits on: patchIssue, putAskAskedOf and
// putProjectDefaultAssignee. The inbox writes are the heavy person's, over their own history.
func oneInboxWritePaths(server *baselineServer, database *store.Store, corpus *oneInboxCorpus) []oneInboxPath {
	issue := "/api/v1/issues/" + corpus.issue
	return []oneInboxPath{
		{name: "comment", run: func(t *testing.T, reading int) time.Duration {
			// Into the thread at the bound, by one of its participants, so the participant set the
			// write moves stays the bound's.
			return oneInboxExpect(t, "a comment at the bound", server.call(t, oneInboxCommenter, http.MethodPost, issue+"/comments",
				map[string]any{"body": fmt.Sprintf("Reading %d of a comment at the bound.", reading), "reply_to": corpus.widestRoot}),
				http.StatusCreated).took
		}},
		{name: "comment-mention", run: func(t *testing.T, reading int) time.Duration {
			return oneInboxExpect(t, "a comment mentioning the session", server.call(t, oneInboxSolo, http.MethodPost, issue+"/comments",
				map[string]any{
					"body": fmt.Sprintf("Reading %d of a mention.", reading), "reply_to": corpus.soloRoot,
					"mentions": []map[string]string{{"target": "session:" + oneInboxSession}},
				}), http.StatusCreated).took
		}},
		{name: "issue-message", run: func(t *testing.T, reading int) time.Duration {
			return oneInboxExpect(t, "an issue message", server.call(t, oneInboxMessagers[0], http.MethodPost, issue+"/messages",
				map[string]any{"body": fmt.Sprintf("Reading %d of an issue message.", reading), "in_reply_to": corpus.messageRoot}),
				http.StatusCreated).took
		}},
		{name: "broadcast", run: func(t *testing.T, reading int) time.Duration {
			return oneInboxExpect(t, "a broadcast", server.call(t, oneInboxConverser, http.MethodPost, "/api/v1/broadcasts",
				map[string]any{
					"body": fmt.Sprintf("Reading %d of a broadcast.", reading), "delivery": "btw",
					"session_ids": []string{oneInboxSession, oneInboxOtherSession},
				}), http.StatusCreated).took
		}},
		{name: "direct-reply", run: func(t *testing.T, reading int) time.Duration {
			return oneInboxExpect(t, "the session's direct reply", server.call(t, "", http.MethodPost,
				"/api/v1/messages/"+corpus.directRoots[reading]+"/reply", map[string]any{
					"actor": oneInboxSessionActor(), "attempt": 1, "body": fmt.Sprintf("Reading %d of a session's reply.", reading),
				}), http.StatusCreated).took
		}},
		{name: "ask-answer", run: func(t *testing.T, reading int) time.Duration {
			return oneInboxExpect(t, "an ask's answer", server.call(t, oneInboxAnswerer, http.MethodPost,
				"/api/v1/asks/"+corpus.answerAsks[reading]+"/answer", map[string]any{
					"text": fmt.Sprintf("Reading %d of an answer.", reading), "expected_edited_at": nil,
				}), http.StatusOK).took
		}},
		{name: "dismissal", run: func(t *testing.T, _ int) time.Duration {
			// A thread the heavy person has read, so the dismissal moves no read mark, put back in
			// their Inbox afterwards. The version it carries is read first, as a page carries it.
			rows := []string{inboxDismissReplyPrefix + corpus.heavyRead[0]}
			body := dismissBody(t, database, oneInboxHeavy, rows)
			answer := oneInboxExpect(t, "a dismissal", server.call(t, oneInboxHeavy, http.MethodPost, "/api/v1/me/inbox/dismiss", body),
				http.StatusOK)
			requireOneInboxCount(t, "the dismissal", answer, "dismissed", 1)
			restored := oneInboxExpect(t, "the dismissal's restore", server.call(t, oneInboxHeavy, http.MethodPost,
				"/api/v1/me/inbox/dismiss", map[string]any{"threads": rows, "restore": true}), http.StatusOK)
			requireOneInboxCount(t, "the dismissal's restore", restored, "dismissed", 1)
			return answer.took
		}},
		{name: "mark-all-read", run: func(t *testing.T, _ int) time.Duration {
			answer := oneInboxExpect(t, "Mark all read", server.call(t, oneInboxHeavy, http.MethodPost, "/api/v1/me/inbox/read",
				map[string]any{"read_through": oneInboxMoment(0), "kind": []string{"reply", "agent"}}), http.StatusOK)
			requireOneInboxCount(t, "Mark all read", answer, "marked", len(corpus.heavyUnread))
			unreadOneInbox(t, database, corpus.heavyUnread)
			return answer.took
		}},
		{name: "thread-read", run: func(t *testing.T, _ int) time.Duration {
			thread := corpus.heavyUnread[0]
			answer := oneInboxExpect(t, "a thread read", server.call(t, oneInboxHeavy, http.MethodPut, "/api/v1/me/threads/"+thread+"/read",
				map[string]any{"read_through": oneInboxMoment(0)}), http.StatusNoContent)
			unreadOneInbox(t, database, []string{thread})
			return answer.took
		}},
		{name: "agent-clear-and-read", run: func(t *testing.T, _ int) time.Duration {
			return oneInboxExpect(t, "an agent's Clear and read", server.call(t, oneInboxConverser, http.MethodPut,
				"/api/v1/me/agents/"+oneInboxSession+"/state", map[string]any{
					"cleared_before": oneInboxMoment(0), "read_through": oneInboxMoment(0),
				}), http.StatusOK).took
		}},
		{name: "snooze", run: func(t *testing.T, _ int) time.Duration {
			snooze := "/api/v1/me/asks/" + corpus.snoozeAsk + "/snooze"
			answer := oneInboxExpect(t, "a snooze", server.call(t, oneInboxHeavy, http.MethodPut, snooze,
				map[string]any{"snoozed_until": oneInboxMoment(24 * time.Hour)}), http.StatusOK)
			oneInboxExpect(t, "the snooze's undo", server.call(t, oneInboxHeavy, http.MethodDelete, snooze, nil), http.StatusNoContent)
			return answer.took
		}},
		{name: "patch-issue-assignee", run: func(t *testing.T, reading int) time.Duration {
			// The seed set the first assignee, so each reading hands the issue, and the one ask it
			// holds, to the other one.
			return oneInboxExpect(t, "an assignee change", server.call(t, oneInboxHeavy, http.MethodPatch, "/api/v1/issues/"+corpus.movingIssue,
				map[string]any{"assignee": oneInboxAssignees[(reading+1)%2]}), http.StatusOK).took
		}, beside: func(t *testing.T) string {
			return fmt.Sprintf("asks_moved=%d", oneInboxAsksWaitingOn(t, database, `a.id = $1 and a.asked_of is null`,
				corpus.movingAsk, oneInboxAssignees[(oneInboxReadings+1)%2]))
		}},
		{name: "ask-asked-of", run: func(t *testing.T, reading int) time.Duration {
			// The ask waits on its issue's assignee, so each reading hands it to one of two people who
			// are not, in turn.
			return oneInboxExpect(t, "an ask handed on", server.call(t, oneInboxHeavy, http.MethodPut, "/api/v1/asks/"+corpus.movingAsk+"/asked-of",
				map[string]any{"asked_of": oneInboxAskedOf[reading%2]}), http.StatusOK).took
		}, beside: func(t *testing.T) string {
			return fmt.Sprintf("asks_moved=%d", oneInboxAsksWaitingOn(t, database, `a.id = $1 and a.asked_of is not null`,
				corpus.movingAsk, oneInboxAskedOf[oneInboxReadings%2]))
		}},
		{name: "project-default-assignee", run: func(t *testing.T, reading int) time.Duration {
			return oneInboxExpect(t, "a project's default moved", server.call(t, oneInboxHeavy, http.MethodPut,
				"/api/v1/projects/"+corpus.moveProject+"/default-assignee",
				map[string]any{"assignee": oneInboxDefaults[(reading+1)%2]}), http.StatusOK).took
		}, beside: func(t *testing.T) string {
			moved := oneInboxAsksWaitingOn(t, database, `p.key = $1 and a.asked_of is null and i.assignee is null`,
				corpus.moveProject, oneInboxDefaults[(oneInboxReadings+1)%2])
			if moved != oneInboxDefaultAsks {
				t.Fatalf("the project's default moved %d asks, want the %d nobody holds", moved, oneInboxDefaultAsks)
			}
			return fmt.Sprintf("asks_moved=%d", moved)
		}},
	}
}

// measureOneInboxPath takes one reading of path that is not counted - the first run of a statement
// on a fresh connection pays for the pages every later run finds cached - then oneInboxReadings
// that are, and prints their p50, p95 and p99, each the nearest-rank reading so every number is a
// run that happened.
func measureOneInboxPath(t *testing.T, path oneInboxPath) {
	t.Helper()
	path.run(t, 0)
	taken := make([]time.Duration, 0, oneInboxReadings)
	for reading := 1; reading <= oneInboxReadings; reading++ {
		taken = append(taken, path.run(t, reading))
	}
	sorted := slices.Sorted(slices.Values(taken))
	beside := ""
	if path.beside != nil {
		beside = " " + path.beside(t)
	}
	t.Logf("measurement-two: %s p50=%.2fms p95=%.2fms p99=%.2fms n=%d%s taken_at=%s", path.name,
		msOf(percentileOfDismissedScale(sorted, 0.50)), msOf(percentileOfDismissedScale(sorted, 0.95)),
		msOf(percentileOfDismissedScale(sorted, 0.99)), len(sorted), beside, oneInboxNow())
}

// oneInboxAsksWaitingOn counts the open asks narrowed by where (over the ask a, its issue i and
// its project p, with $1 its argument) that wait on person, read through the one expression that
// decides who an ask waits on: a write that moved them names that person now.
func oneInboxAsksWaitingOn(t *testing.T, database *store.Store, where string, argument any, person string) int {
	t.Helper()
	var waiting int
	if err := database.Pool.QueryRow(context.Background(), `
		select count(*) from asks a`+askedOfPersonJoins+`
		where a.state = 'open' and `+where+` and `+askedOfPersonExpression+` = $2
	`, argument, person).Scan(&waiting); err != nil {
		t.Fatalf("count the asks waiting on %s: %v", person, err)
	}
	if waiting == 0 {
		t.Fatalf("no open ask waits on %s once the write that moves it to them has run", person)
	}
	return waiting
}

// unreadOneInbox puts the heavy person's read marks on threads back where the seed left them,
// before each thread's newest reply, so the next reading finds them unread again.
func unreadOneInbox(t *testing.T, database *store.Store, threads []string) {
	t.Helper()
	tag, err := database.Pool.Exec(context.Background(), `
		update user_threads set read_through = replied_at - interval '1 minute'
		where login = $1 and thread_id = any($2::uuid[])
	`, oneInboxHeavy, threads)
	if err != nil {
		t.Fatalf("put the read marks back: %v", err)
	}
	if tag.RowsAffected() != int64(len(threads)) {
		t.Fatalf("put back %d read marks, want %d", tag.RowsAffected(), len(threads))
	}
}

// requireOneInboxCount fails the test unless the write's answer counts want in field.
func requireOneInboxCount(t *testing.T, what string, answer apiAnswer, field string, want int) {
	t.Helper()
	counted := map[string]any{}
	if err := json.Unmarshal(answer.body, &counted); err != nil {
		t.Fatalf("decode %s's answer: %v: %s", what, err, answer.body)
	}
	if got, _ := counted[field].(float64); int(got) != want {
		t.Fatalf("%s answered %s=%v, want %d: %s", what, field, counted[field], want, answer.body)
	}
}

// oneInboxWritten is one table's rows that one operation of one transaction wrote, as the audit
// below recorded them.
type oneInboxWritten struct {
	relation, operation string
	rows                int64
}

// countOneInboxCommentRows writes one comment into the widest thread and counts the rows its
// transaction wrote: the per-participant ones, which are its user_threads rows, and the fixed ones,
// every other table's, each printed apart. It fails where the per-participant rows pass two for
// each participant of the bound, the append and the update R4 allows; that failure is the stop
// rule's.
//
// The rows are counted by an audit installed for this one write (installOneInboxAudit), because
// a row's xmin alone undercounts: the base build's background work rewrites rows the write left -
// the outbox its event, the block index its owed marks - moments after it commits.
func countOneInboxCommentRows(t *testing.T, server *baselineServer, database *store.Store, corpus *oneInboxCorpus) {
	t.Helper()
	ctx := context.Background()
	installOneInboxAudit(t, database)
	id := oneInboxID(t, "the comment whose rows are counted", server.call(t, oneInboxCommenter, http.MethodPost,
		"/api/v1/issues/"+corpus.issue+"/comments",
		map[string]any{"body": "The comment whose rows are counted.", "reply_to": corpus.widestRoot}), http.StatusCreated)
	var xid int64
	if err := database.Pool.QueryRow(ctx, `select xmin::text::bigint from comments where id = $1`, id).Scan(&xid); err != nil {
		t.Fatalf("read the transaction that wrote the comment: %v", err)
	}
	rows, err := database.Pool.Query(ctx, `
		select relation, operation, sum(rows)::bigint from one_inbox_written where xid = $1 group by 1, 2 order by 1, 2
	`, xid)
	if err != nil {
		t.Fatalf("read the rows the comment wrote: %v", err)
	}
	written, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (oneInboxWritten, error) {
		var part oneInboxWritten
		err := row.Scan(&part.relation, &part.operation, &part.rows)
		return part, err
	})
	if err != nil {
		t.Fatalf("read the rows the comment wrote: %v", err)
	}
	var perParticipant, fixed, comments int64
	var parts []string
	for _, part := range written {
		if part.relation == "user_threads" {
			perParticipant += part.rows
			continue
		}
		fixed += part.rows
		parts = append(parts, fmt.Sprintf("%s.%s=%d", part.relation, strings.ToLower(part.operation), part.rows))
		if part.relation == "comments" && part.operation == "INSERT" {
			comments += part.rows
		}
	}
	if comments != 1 {
		t.Fatalf("transaction %d inserted %d comments by the audit, want the one written: it is not the comment's", xid, comments)
	}
	var participants, carried int64
	if err := database.Pool.QueryRow(ctx, `
		select count(*), count(*) filter (where xmin::text::bigint = $2) from user_threads where thread_id = $1
	`, corpus.widestRoot, xid).Scan(&participants, &carried); err != nil {
		t.Fatalf("read the widest thread's rows: %v", err)
	}
	if carried != perParticipant {
		t.Fatalf("the comment wrote %d user_threads rows by the audit and %d of the widest thread's rows carry its transaction",
			perParticipant, carried)
	}
	t.Logf("measurement-two: rows of one comment in the thread of %d participants: per_participant=%d fixed=%d (%s) bound=%d taken_at=%s",
		participants, perParticipant, fixed, strings.Join(parts, " "), 2*oneInboxParticipantBound, oneInboxNow())
	if perParticipant > 2*oneInboxParticipantBound {
		t.Errorf("stop rule: one comment in the thread of %d participants wrote %d per-participant rows, above two for each of R4's %d",
			participants, perParticipant, oneInboxParticipantBound)
	}
}

// installOneInboxAudit records, for every statement that writes a table of the schema, the
// transaction it ran in, the table, the operation and how many rows it wrote, through a statement
// trigger reading the statement's transition table. It is installed over the base build's own
// database after the timings, so no reading pays for it, and removed when the test ends.
func installOneInboxAudit(t *testing.T, database *store.Store) {
	t.Helper()
	ctx := context.Background()
	exec := func(statement string) {
		t.Helper()
		if _, err := database.Pool.Exec(ctx, statement); err != nil {
			t.Fatalf("install the audit: %s: %v", statement, err)
		}
	}
	exec(`create table one_inbox_written (xid bigint not null, relation text not null, operation text not null, rows bigint not null)`)
	exec(`
		create function one_inbox_written() returns trigger language plpgsql as $$
		declare
			counted bigint;
		begin
			select count(*) into counted from written;
			if counted > 0 then
				insert into one_inbox_written
				values ((pg_current_xact_id()::text::numeric % 4294967296)::bigint, tg_table_name, tg_op, counted);
			end if;
			return null;
		end
		$$`)
	t.Cleanup(func() {
		for _, statement := range []string{`drop function one_inbox_written() cascade`, `drop table one_inbox_written`} {
			if _, err := database.Pool.Exec(context.Background(), statement); err != nil {
				t.Errorf("remove the audit: %s: %v", statement, err)
			}
		}
	})
	tables := oneInboxStrings(t, database, `
		select c.relname::text from pg_class c join pg_namespace n on n.oid = c.relnamespace
		where n.nspname = 'public' and c.relkind in ('r', 'p') and not c.relispartition and c.relname <> 'one_inbox_written'
		order by 1
	`)
	// One statement a table, each committed on its own, so the audit never holds a lock on one table
	// while it waits for another that the base build's own work holds.
	for _, table := range tables {
		for _, event := range []struct{ operation, transition string }{{"insert", "new"}, {"update", "new"}, {"delete", "old"}} {
			exec(fmt.Sprintf(`create trigger one_inbox_written_%s after %s on %s referencing %s table as written `+
				`for each statement execute function one_inbox_written()`,
				event.operation, event.operation, pgx.Identifier{table}.Sanitize(), event.transition))
		}
	}
}

// oneInboxMoment is now plus ahead, as a write's timestamp field takes it.
func oneInboxMoment(ahead time.Duration) string {
	return time.Now().Add(ahead).UTC().Format(time.RFC3339Nano)
}

// oneInboxNow is the moment a reading was taken, as `date -u +%FT%TZ` prints it.
func oneInboxNow() string {
	return time.Now().UTC().Format(time.RFC3339)
}
