package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/docs"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// The tests of a database fingerprint's wait (takeFingerprint, backgroundWrites). Each holds one
// background writer an ordinary request leaves owed past its answer and takes a fingerprint whose
// wait lets the writer go heldWriterLag after it begins (heldWriter). The fingerprint's reads begin
// only once that writer has written, or the test fails naming it, so a wait that skips the branch
// that covers the writer fails its test every run, whatever the reads then see; a second
// fingerprint, equal to the first, shows the writer wrote before the first one read. Each first
// checks that the writer was owed when the fingerprint began and fails UNKNOWN otherwise, so a pass
// never comes from a writer that had already finished.

// heldRefresh takes project's term-statistics refresh key on served before a test's create, with
// a job that runs the real refresh only once released: the create's related-item check then joins
// it (ensureSearchTermStats) rather than starting one, so the test decides when the refresh the
// create would have started commits. failed answers the refresh's error once it has ended.
func heldRefresh(t *testing.T, served *server, project string) (held *heldWriter, failed func() error) {
	t.Helper()
	gate := make(chan struct{})
	open := sync.OnceFunc(func() { close(gate) })
	t.Cleanup(open)
	var refreshed atomic.Bool
	var refreshErr atomic.Pointer[error]
	served.termStats.DoChan(project, func() (any, error) {
		<-gate
		result, err := refreshSearchTermStats(context.Background(), served.deps.Store.Pool, project)
		refreshErr.Store(&err)
		refreshed.Store(true)
		return result.Items, err
	})
	held = &heldWriter{
		name:    "the term-statistics refresh of " + project + " an issue create joined",
		release: func() { time.AfterFunc(heldWriterLag, open) },
		ended:   refreshed.Load,
	}
	return held, func() error { return *refreshErr.Load() }
}

func TestAFingerprintWaitsOutATermStatisticsRefreshAnIssueCreateJoined(t *testing.T) {
	handler, _, _, served := newRegisteredTestServer(t, testServerOptions{settle: time.Hour})
	held, failed := heldRefresh(t, served, "TEST")
	createInteractionIssue(t, handler, "TEST", "A refresh held past the answer", "A spec")
	if !detachedRunning(&served.detached) {
		t.Fatal("UNKNOWN: the create's related-item check left no detached work running (it joined no refresh), so this run shows nothing")
	}
	before := takeFingerprint(t, served, held)
	if err := failed(); err != nil {
		t.Fatalf("the held refresh failed: %v", err)
	}
	assertUnchanged(t, "no request (the term-statistics refresh an issue create joined)", before, databaseFingerprint(t, served))
}

func TestAFingerprintWaitsOutAMentionSendACommentLeftRunning(t *testing.T) {
	listener, state := newMentionListener(t)
	handler, database, _, served := newRegisteredTestServer(t, testServerOptions{envoyURL: listener.URL, settle: time.Hour})
	open := state.hold("s1")
	t.Cleanup(open)
	issue := createInteractionIssue(t, handler, "TEST", "A mention held past the answer", "A spec")
	comment := decodeMentionedComment(t, postMentionedComment(t, handler, issue.Key, map[string]any{
		"body": "Can this ship?", "mentions": []map[string]any{{"target": "session:s1"}},
	}))
	state.awaitSend(t)
	if mentionSendsHeld(served.mentions) == 0 {
		t.Fatal("UNKNOWN: the mention sender held no send when the fingerprint began, so this run shows nothing")
	}
	before := takeFingerprint(t, served, &heldWriter{
		name:    "the mention send the comment left running",
		release: func() { time.AfterFunc(heldWriterLag, open) },
		ended:   func() bool { return !commentDeliveryPending(t, database, comment.ID) },
	})
	assertUnchanged(t, "no request (the mention send a comment left running)", before, databaseFingerprint(t, served))
}

// commentDeliveryPending is whether an attempt of comment's mentions is still pending, which its
// send's settle ends.
func commentDeliveryPending(t *testing.T, database *store.Store, comment string) bool {
	t.Helper()
	var pending bool
	if err := database.Pool.QueryRow(context.Background(),
		`select exists (select 1 from comment_deliveries where comment_id = $1 and state = 'pending')`, comment,
	).Scan(&pending); err != nil {
		t.Fatalf("read the pending attempts of comment %s: %v", comment, err)
	}
	return pending
}

func TestAFingerprintWaitsOutADocumentSettlementAWriteArmed(t *testing.T) {
	handler, database, _, served := newRegisteredTestServer(t, testServerOptions{settle: time.Hour})
	issue := createInteractionIssue(t, handler, "TEST", "A settlement armed past the answer", "A spec")
	documents := served.deps.Docs.(*docs.Service)
	spec := issue.PrimaryArtifactID
	// The create's other writers - its term-statistics refresh - end first, so no wait for them can
	// cover the settlement in the fingerprint below.
	awaitBackgroundWrites(t, served)
	if !documentOwesSettlement(t, database, spec) {
		t.Fatal("UNKNOWN: the spec owed no settlement when the fingerprint began, so this run shows nothing")
	}
	// The create armed the spec's settlement an hour out; the fingerprint's wait begins by arming
	// it heldWriterLag out, inside backgroundSoon, which a wait must then see through to its end.
	before := takeFingerprint(t, served, &heldWriter{
		name:    "the settlement of the spec the create armed",
		release: func() { documents.SettleAfter(spec, heldWriterLag) },
		ended:   func() bool { return !documentOwesSettlement(t, database, spec) },
	})
	assertUnchanged(t, "no request (the document settlement an issue create armed)", before, databaseFingerprint(t, served))
}

func TestAFingerprintWaitsOutARouteRosterFetchACancelledReadLeftRunning(t *testing.T) {
	listener, state := newMentionListener(t)
	handler, _, _, served := newRegisteredTestServer(t, testServerOptions{envoyURL: listener.URL, settle: time.Hour})
	// The fetch's own bound outlasts the hold, so it stores the listener's answer, not a silence.
	served.rosters.timeout = backgroundBound
	open := state.holdLookups("/v1/sessions")
	t.Cleanup(open)
	ctx, cancel := backgroundContext(t)
	defer cancel()
	reading, stopReading := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil).WithContext(reading)
	request.Header.Set("X-Dispatch-User", "alice")
	answered := make(chan struct{})
	go func() {
		defer close(answered)
		handler.ServeHTTP(httptest.NewRecorder(), request)
	}()
	awaitCondition(t, ctx, "the agents list's roster fetch to start", func() bool { return rosterFetching(served) })
	stopReading()
	select {
	case <-answered:
	case <-ctx.Done():
		t.Fatal("the agents list whose request ended did not answer")
	}
	if !rosterFetching(served) {
		t.Fatal("UNKNOWN: the roster fetch had ended when the fingerprint began, so this run shows nothing")
	}
	before := takeFingerprint(t, served, &heldWriter{
		name:    "the route roster fetch the cancelled read left running",
		release: func() { time.AfterFunc(heldWriterLag, open) },
		ended:   func() bool { return !rosterFetching(served) },
	})
	assertUnchanged(t, "no request (the route roster fetch a cancelled read left running)", before, databaseFingerprint(t, served))
}

// rosterFetching is whether served's route roster fetch runs: it stores the listener's answer
// before it lets the fetch go (fetchRouteRoster).
func rosterFetching(served *server) bool {
	served.rosters.mu.Lock()
	defer served.rosters.mu.Unlock()
	return served.rosters.fetch != nil
}

// A refresh of the mention sender's claims writes its statement without the sender's lock, so a
// send it named can leave held while the statement still writes that send's row (a send whose own
// claim statement failed ends that way). The wait counts the refresh's statement as well as the
// sends held (mentionSenderWriting).
func TestAFingerprintWaitsOutAMentionClaimRefreshWhoseSendLeftHeld(t *testing.T) {
	gate := make(chan struct{})
	open := sync.OnceFunc(func() { close(gate) })
	t.Cleanup(open)
	started := make(chan struct{})
	start := sync.OnceFunc(func() { close(started) })
	var wrote atomic.Bool
	refresh := func(context.Context, []mentionClaim) ([]mentionClaim, error) {
		start()
		<-gate
		wrote.Store(true)
		return nil, nil
	}
	read := func(context.Context, []mentionClaim) ([]mentionClaim, error) { return nil, nil }
	sender := newMentionSender(t.Context(), 10*time.Millisecond,
		func(context.Context, mentionSend) (mentionSend, bool) { return mentionSend{}, false },
		refresh, read, func(context.Context, []mentionClaim) error { return nil })
	t.Cleanup(sender.wait)
	key := mentionKey{comment: "comment", target: "session:s1", attempt: 1}
	sender.mu.Lock()
	sender.held[key] = mentionClaimState{claimed: time.Now()}
	sender.mu.Unlock()
	ctx, cancel := backgroundContext(t)
	defer cancel()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("UNKNOWN: the sender's claim keeper never refreshed the claim of the send it held, so this run shows nothing")
	}
	// The send ends without a claim of its own while the refresh that named it writes.
	sender.mu.Lock()
	delete(sender.held, key)
	sender.mu.Unlock()
	backgroundWrites(t, ctx, &server{mentions: sender}, func() { time.AfterFunc(heldWriterLag, open) })
	if !wrote.Load() {
		t.Fatal("the wait for the server's background writes returned while the claim refresh of a send that had left held still wrote: it did not wait for the refresh")
	}
}

// An events view starts the server's event stream relay, which observes the event horizon on its
// own goroutine and writes it to event_horizon at moments no wait can see: a fingerprint of that
// server is refused by name. Once the view's subscription has ended, the relay's generation has
// stopped, and the fingerprints agree.
func TestAFingerprintRefusesAServerWhoseEventStreamRelayRuns(t *testing.T) {
	rig := newLiveSocketRig(t, testServerOptions{settle: time.Hour}, nil)
	issue := createInteractionIssue(t, rig.handler, "TEST", "Events beside the relay", "A spec")
	client := rig.dial(t, asUser("alice"))
	subscribeEvents(t, client, "events", nil)
	renameIssue(t, rig.handler, issue.Key, "Renamed beside the relay")
	if refusal := backgroundRefusal(rig.served); !strings.Contains(refusal, "event stream relay runs") {
		t.Fatalf("with an events view open the fingerprint's refusal reads %q, want one naming the event stream relay", refusal)
	}
	client.send(t, LiveUnsubscribe{Type: "unsubscribe", Sub: "events"})
	ctx, cancel := backgroundContext(t)
	defer cancel()
	awaitCondition(t, ctx, "the relay's generation to stop with the view's subscription", func() bool { return backgroundRefusal(rig.served) == "" })
	assertUnchanged(t, "no request (an event stream relay that stopped)", databaseFingerprint(t, rig.served), databaseFingerprint(t, rig.served))
}

// An observation of the event horizon that no request waits for - the last read of an event
// stream relay generation that stopped while it observed - writes what it proves to event_horizon
// on its own goroutine once it ends. The test holds one on the event log's lock, from a goroutine
// of its own, and the fingerprint's wait must see it and its write through (events.Horizon.Busy).
func TestAFingerprintWaitsOutAnEventHorizonObservationNoRequestWaitedFor(t *testing.T) {
	handler, database, _, served := newRegisteredTestServer(t, testServerOptions{settle: time.Hour})
	createInteractionIssue(t, handler, "TEST", "An observation held past the answer", "A spec")
	awaitBackgroundWrites(t, served)
	<-served.bootMark
	ctx, cancel := backgroundContext(t)
	defer cancel()
	holder, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the transaction that holds the event log: %v", err)
	}
	open := sync.OnceFunc(func() { _ = holder.Rollback(context.Background()) })
	t.Cleanup(open)
	var pid int32
	if err := holder.QueryRow(ctx, `select pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatalf("read the holder's backend: %v", err)
	}
	if _, err := holder.Exec(ctx, `lock table events in access exclusive mode`); err != nil {
		t.Fatalf("hold the event log: %v", err)
	}
	horizon := served.horizon()
	observed := make(chan error, 1)
	go func() {
		_, err := horizon.Safe(context.Background())
		observed <- err
	}()
	awaitCondition(t, ctx, "the observation to wait on the held event log", func() bool {
		var waiting bool
		if err := database.Pool.QueryRow(ctx, `select exists (select 1 from pg_stat_activity where $1 = any(pg_blocking_pids(pid)))`, pid).Scan(&waiting); err != nil {
			t.Fatalf("read what waits on the holder: %v", err)
		}
		return waiting
	})
	if !horizon.Busy() {
		t.Fatal("UNKNOWN: the horizon had no observation in flight when the fingerprint began, so this run shows nothing")
	}
	before := takeFingerprint(t, served, &heldWriter{
		name:    "an observation of the event horizon no request waited for, and its write to event_horizon",
		release: func() { time.AfterFunc(heldWriterLag, open) },
		ended:   func() bool { return !horizon.Busy() },
	})
	if err := <-observed; err != nil {
		t.Fatalf("the held observation failed: %v", err)
	}
	assertUnchanged(t, "no request (an event horizon observation no request waited for)", before, databaseFingerprint(t, served))
}

// A document whose rows have aged past a compaction's tail - a test that ran that long after its
// second row, as one can under load - may be compacted at any moment, by its room's close or by
// the compaction queue, and the fingerprint's wait compacts it first, so the two fingerprints
// agree rather than fail naming that compaction.
func TestAFingerprintCompactsADocumentWhoseRowsACompactionMayFoldNow(t *testing.T) {
	handler, database, _, served := newRegisteredTestServer(t, testServerOptions{settle: time.Hour})
	issue := createInteractionIssue(t, handler, "TEST", "A document past the compaction tail", "A spec")
	spec := issue.PrimaryArtifactID
	if edited := dispatchRequest(t, handler, http.MethodPost, "/api/v1/artifacts/"+spec+"/edits", map[string]any{
		"ops": []map[string]string{{"op": "insert", "after": "end", "markdown": "A second row."}},
	}, "alice"); edited.Code != http.StatusOK {
		t.Fatalf("edit the spec: status=%d body=%s", edited.Code, edited.Body.String())
	}
	awaitBackgroundWrites(t, served)
	if _, err := database.Pool.Exec(context.Background(),
		`update doc_updates set created_at = created_at - interval '1 hour' where artifact_id = $1`, spec); err != nil {
		t.Fatalf("age the spec's rows: %v", err)
	}
	rows := storedUpdateRows(t, database, spec)
	if rows < 2 {
		t.Fatalf("UNKNOWN: the spec stores %d rows, so no compaction can fold any, and this run shows nothing", rows)
	}
	before := databaseFingerprint(t, served)
	if folded := storedUpdateRows(t, database, spec); folded >= rows {
		t.Fatalf("the spec stores %d rows after the fingerprint, as many as the %d before it: the wait did not compact the rows a compaction may fold", folded, rows)
	}
	assertUnchanged(t, "no request (a document whose rows a compaction may fold)", before, databaseFingerprint(t, served))
}

// storedUpdateRows is how many doc_updates rows document stores.
func storedUpdateRows(t *testing.T, database *store.Store, document string) int {
	t.Helper()
	var rows int
	if err := database.Pool.QueryRow(context.Background(), `select count(*) from doc_updates where artifact_id = $1`, document).Scan(&rows); err != nil {
		t.Fatalf("count the rows of document %s: %v", document, err)
	}
	return rows
}
