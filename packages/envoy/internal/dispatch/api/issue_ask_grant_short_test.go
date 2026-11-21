package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/docs"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/testwait"
)

// This file tests what a transaction writing an issue's documents in turn answers where a later
// document's fork needs more of the pod's load concurrency than the grant its first document's
// fork took, and the pool has none of it free: a transaction holding a fork takes more of its
// grant only where the pool gives it at once (docs' loadSlots), so the later fork is refused busy,
// and the transaction rolls back and gives its grant back. The issue's ask-state write takes the
// weight of every document it writes at its first fork (docs.WriteIssueAskStates), so the later
// fork here needs more only because its document grew after that weighing, which each test makes
// happen once the first fork is read (docs.Service.AfterForkReadForTest), as another process's
// write landing then would. Before, the later fork's fold waited for the difference holding the
// first fork's grant, until the transaction's bound.

// laterDocumentGrowth is what each test appends to the later document once the transaction's
// first fork is read: about 68 KiB of prose, which weighs more (docs' copyWeight) than both
// documents weighed together before the transaction's first fork.
var laterDocumentGrowth = strings.Repeat(firstDocumentProse, 4)

// growLaterDocument arms documents' transactions so that the first one to read its fork of first
// then appends laterDocumentGrowth to later's stored history, as another process's write lands,
// and fills every byte of pool its holders do not hold: later's fork, read from the store, finds
// none of what it weighs past the transaction's grant free. done, run once the transaction under
// test has been answered, disarms the hook, gives the filled bytes back, and fails the test
// unless the growth and the fill both happened; the test's end gives the bytes back too.
func growLaterDocument(t *testing.T, documents *docs.Service, database *store.Store, pool *docs.LoadPoolForTest, first, later string) (done func()) {
	t.Helper()
	var once sync.Once
	var grew error
	var release func()
	documents.AfterForkReadForTest(func(room string) {
		if room != first {
			return
		}
		once.Do(func() {
			if grew = docs.AppendStoredChangeForTest(context.Background(), database, later, appendStoredBlocks(laterDocumentGrowth)); grew != nil {
				return
			}
			var filled bool
			if release, filled = pool.FillForTest(); !filled {
				grew = errors.New("the pool refused the bytes its holders do not hold")
			}
		})
	})
	end := func() {
		documents.AfterForkReadForTest(nil)
		if release != nil {
			release()
		}
	}
	t.Cleanup(end)
	return func() {
		t.Helper()
		end()
		switch {
		case grew != nil:
			t.Fatalf("grow the later document once the first fork was read: %v", grew)
		case release == nil:
			t.Fatal("no transaction read its fork of the first document, so the later one never grew and nothing here is tested")
		}
	}
}

// A close's sweep whose later document's fork needs more of the pool than the grant its first
// fork took, with none of it free, is refused busy at once rather than waiting for it: the sweep's
// failure is logged, naming the issue and the refusal, its transaction rolls back and gives its
// grant back, and none of it reaches the close's caller, whom the close answered once it had
// committed (sweepClosedIssueAsks returns nothing). The ask repair's next pass sweeps the issue,
// whose first document owes a settlement, and marks its ask block resolved, as the sweep would
// have. Before, the later fork's fold waited for the difference holding the first fork's grant
// until the sweep's own bound, docs.WriteRequestBound after its start, past this test's.
func TestASweepRefusedBusyAtALaterDocumentIsLoggedAndTheNextAskRepairFinishesIt(t *testing.T) {
	f := newHeldSecondDocument(t)
	logs := captureLogs(t)
	// Alice's keystroke on the second document is stored once its lock is let go, on the history
	// folded under her room, and the room closes for its reload: the sweep reads the second
	// document from the store, which the growth makes heavier.
	if err := f.holder.Commit(context.Background()); err != nil {
		t.Fatalf("let go of the second document's lock: %v", err)
	}
	reloading := `msg="dispatch: closed a document room whose browser's update was stored on a history replaced under it" room=` + f.second
	testwait.Eventually(t, "the second document's room to close for its reload", func() bool {
		return strings.Contains(logs.String(), reloading) && !slices.Contains(f.documents.ResidentRooms(), f.second)
	})
	grown := growLaterDocument(t, f.documents, f.database, f.pool, f.first, f.second)
	started, answered := f.sweepAsAnUnkeyedClose()
	select {
	case <-answered:
	case <-time.After(heldSecondDocumentWait):
		t.Fatalf("the close's sweep was not answered within %s of its start: its later document's fork waited for more of the pool holding the grant its first fork took", heldSecondDocumentWait)
	}
	t.Logf("the close's sweep answered %s after it began", time.Since(started).Round(time.Millisecond))
	grown()
	logged := logs.String()
	if !strings.Contains(logged, `msg="dispatch: sweep the ask blocks of a closed issue"`) || !strings.Contains(logged, "issue="+f.issue) ||
		!strings.Contains(logged, "needs more of it than is free at once") {
		t.Fatalf("the sweep logged:\n%s\nwant its refusal busy at its later document, naming %s", logged, f.issue)
	}
	if stored := storedUpdateCount(t, f.database, f.first); stored != f.firstUpdates {
		t.Fatalf("the first document holds %d stored updates, want the %d it held before: the refused sweep committed", stored, f.firstUpdates)
	}
	repair := repairNow(t, f.repairFixture)
	if repair.Swept != 1 || repair.Withdrawn != 0 || repair.Restored != 0 {
		t.Fatalf("the next repair pass = %+v, want the first document's ask block swept", repair)
	}
	requireLiveText(t, f.handler, f.first, unindexedAskResolved)
}

// An issue's close whose later document's fork needs more of the pool than the grant its first
// fork took, with none of it free, is refused busy at once: the close answers 503
// DOC_SERVICE_UNAVAILABLE, which its caller retries, and leaves every ask open, and the close sent
// again once the pool has room lands. Before, the later fork's fold waited for the difference
// holding the first fork's grant until the close's request reached docs.WriteRequestBound.
func TestAnIssueCloseRefusedBusyAtALaterDocumentAnswers503AndItsRetryLands(t *testing.T) {
	f := newCloseAsksFixtureSettlingAfter(t, time.Hour)
	documents := []string{f.spec, f.copy}
	sort.Strings(documents)
	first, later := documents[0], documents[1]
	if err := f.documents.Evict(context.Background(), later); err != nil {
		t.Fatalf("close the later document's room: %v", err)
	}
	if slices.Contains(f.documents.ResidentRooms(), later) {
		t.Fatal("the later document's room is still resident once it was closed")
	}
	pool, restore := docs.UseOneLoadSlotForTest(nil)
	t.Cleanup(restore)
	grown := growLaterDocument(t, f.documents, f.database, pool, first, later)
	started := time.Now()
	answered := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		answered <- dispatchRequest(t, f.handler, http.MethodPatch, "/api/v1/issues/"+f.issue, map[string]any{"status": "done"}, "alice")
	}()
	var refused *httptest.ResponseRecorder
	select {
	case refused = <-answered:
	case <-time.After(heldSecondDocumentWait):
		t.Fatalf("the close was not answered within %s of being sent: its later document's fork waited for more of the pool holding the grant its first fork took", heldSecondDocumentWait)
	}
	t.Logf("the close answered %s after it was sent", time.Since(started).Round(time.Millisecond))
	grown()
	if code := decodeBody[struct {
		Code string `json:"code"`
	}](t, refused).Code; refused.Code != http.StatusServiceUnavailable || code != codeDocServiceUnavailable {
		t.Fatalf("the close answered status=%d body=%s, want 503 %s", refused.Code, refused.Body.String(), codeDocServiceUnavailable)
	}
	for _, ask := range f.open() {
		if got := readBlockAsk(t, f.handler, ask); got.State != "open" {
			t.Fatalf("ask %s reads %s once the refused close answered, want it open", ask, got.State)
		}
	}
	closeIssue(t, f.handler, f.issue)
	for _, ask := range f.open() {
		if got := readBlockAsk(t, f.handler, ask); got.State != "resolved" {
			t.Fatalf("ask %s reads %s once the close's retry landed, want the close to have withdrawn it", ask, got.State)
		}
	}
}
