package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/sjawhar/envoy/internal/dispatch/docs"
)

// The tests of a database fingerprint's wait (takeFingerprint, backgroundWrites). Each holds one
// background writer an ordinary request leaves owed past its answer, takes a fingerprint that
// releases the writer as its wait begins, lets the writer finish, and holds a second fingerprint
// equal to the first. A fingerprint that took no wait reads while the writer is still held, so
// each fails without the wait, every run. Each first checks that the writer was owed when the
// fingerprint began and fails UNKNOWN otherwise, so a pass never comes from a writer that had
// already finished.

// heldRefresh takes project's term-statistics refresh key on served before a test's create, with
// a job that runs the real refresh only once released: the create's related-item check then joins
// it (ensureSearchTermStats) rather than starting one, so the test decides when the refresh the
// create would have started commits. done receives once it has.
func heldRefresh(t *testing.T, served *server, project string) (release func(), done <-chan singleflight.Result) {
	t.Helper()
	gate := make(chan struct{})
	release = sync.OnceFunc(func() { close(gate) })
	t.Cleanup(release)
	done = served.termStats.DoChan(project, func() (any, error) {
		<-gate
		result, err := refreshSearchTermStats(context.Background(), served.deps.Store.Pool, project)
		return result.Items, err
	})
	return release, done
}

func TestAFingerprintWaitsOutATermStatisticsRefreshAnIssueCreateJoined(t *testing.T) {
	handler, _, _, served := newRegisteredTestServer(t, testServerOptions{settle: time.Hour})
	release, refreshed := heldRefresh(t, served, "TEST")
	createInteractionIssue(t, handler, "TEST", "A refresh held past the answer", "A spec")
	if !detachedRunning(&served.detached) {
		t.Fatal("UNKNOWN: the create's related-item check left no detached work running (it joined no refresh), so this run shows nothing")
	}
	before := takeFingerprint(t, served, release)
	release()
	select {
	case result := <-refreshed:
		if result.Err != nil {
			t.Fatalf("the held refresh failed: %v", result.Err)
		}
	case <-time.After(backgroundBound):
		t.Fatal("the held refresh did not end")
	}
	assertUnchanged(t, "no request (the term-statistics refresh an issue create joined)", before, databaseFingerprint(t, served))
}

func TestAFingerprintWaitsOutAMentionSendACommentLeftRunning(t *testing.T) {
	listener, state := newMentionListener(t)
	handler, database, _, served := newRegisteredTestServer(t, testServerOptions{envoyURL: listener.URL, settle: time.Hour})
	release := state.hold("s1")
	t.Cleanup(release)
	issue := createInteractionIssue(t, handler, "TEST", "A mention held past the answer", "A spec")
	comment := decodeMentionedComment(t, postMentionedComment(t, handler, issue.Key, map[string]any{
		"body": "Can this ship?", "mentions": []map[string]any{{"target": "session:s1"}},
	}))
	state.awaitSend(t)
	if mentionSendsHeld(served.mentions) == 0 {
		t.Fatal("UNKNOWN: the mention sender held no send when the fingerprint began, so this run shows nothing")
	}
	before := takeFingerprint(t, served, release)
	release()
	settledCommentDeliveries(t, database, comment.ID)
	assertUnchanged(t, "no request (the mention send a comment left running)", before, databaseFingerprint(t, served))
}

func TestAFingerprintWaitsOutADocumentSettlementAWriteArmed(t *testing.T) {
	handler, database, _, served := newRegisteredTestServer(t, testServerOptions{settle: time.Hour})
	issue := createInteractionIssue(t, handler, "TEST", "A settlement armed past the answer", "A spec")
	documents := served.deps.Docs.(*docs.Service)
	spec := issue.PrimaryArtifactID
	if !documentOwesSettlement(t, database, spec) {
		t.Fatal("UNKNOWN: the spec owed no settlement when the fingerprint began, so this run shows nothing")
	}
	// The create armed the spec's settlement an hour out; the fingerprint's wait begins by running
	// it now, which a wait must then see through to its end.
	before := takeFingerprint(t, served, func() { documents.SettleNow(spec) })
	if documentOwesSettlement(t, database, spec) {
		// Only a fingerprint that read before the settlement ran comes here: run it, so the second
		// fingerprint shows what it wrote.
		documents.SettleNow(spec)
		ctx, cancel := backgroundContext(t)
		defer cancel()
		awaitCondition(t, ctx, "the spec's settlement", func() bool { return !documentOwesSettlement(t, database, spec) })
	}
	assertUnchanged(t, "no request (the document settlement an issue create armed)", before, databaseFingerprint(t, served))
}

func TestAFingerprintWaitsOutARouteRosterFetchACancelledReadLeftRunning(t *testing.T) {
	listener, state := newMentionListener(t)
	handler, _, _, served := newRegisteredTestServer(t, testServerOptions{envoyURL: listener.URL, settle: time.Hour})
	// The fetch's own bound outlasts the hold, so it stores the listener's answer, not a silence.
	served.rosters.timeout = backgroundBound
	release := state.holdLookups("/v1/sessions")
	t.Cleanup(release)
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
	before := takeFingerprint(t, served, release)
	release()
	awaitCondition(t, ctx, "the roster fetch", func() bool { return !rosterFetching(served) })
	assertUnchanged(t, "no request (the route roster fetch a cancelled read left running)", before, databaseFingerprint(t, served))
}

// rosterFetching is whether served's route roster fetch runs: it stores the listener's answer
// before it lets the fetch go (fetchRouteRoster).
func rosterFetching(served *server) bool {
	served.rosters.mu.Lock()
	defer served.rosters.mu.Unlock()
	return served.rosters.fetch != nil
}
