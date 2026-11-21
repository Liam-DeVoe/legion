package docs

import (
	"context"
	"errors"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"weak"

	"github.com/reearth/ygo/crdt"
	"github.com/reearth/ygo/persistence"
	"github.com/reearth/ygo/provider/websocket"

	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// This file tests the pod's load concurrency's rule that no holder of a grant waits for more of
// the pool while it holds it (loadSlots, loadSlot.take): each holder that needs more takes it at
// once, gives its grant back and takes the total whole, does its work again under the whole
// grant, or refuses busy, and no holder is excepted - a transaction holds one grant for its forks
// and its reads (Ledger.forkSlot, withLoadSlot), and every take of it after its first fits at once
// or is refused busy. The pool serves its waiters in order (semaphore.Weighted), so most tests
// queue a taker for more of the pool than is free beside the holder under test and then have that
// holder need more: a holder that queued behind the taker for the difference would wait for it,
// while the taker waits for the bytes the holder holds, and the test fails at holdAndWaitBound
// naming that wait. With take put back to wait for the difference while it holds, every test here
// but the leak's and the up-front take's fails: at that bound, or at the state answer's own
// WriteWaitBound, which refuses it.

// holdAndWaitBound bounds each wait here for a holder that needs more of the pool. The work each
// does once it has its bytes is a fold or a decode of a few hundred kilobytes, or none, and takes
// milliseconds; a holder that queued for more behind a taker waiting for its bytes never finishes
// while the test holds the rest of the pool, so the test fails at this bound by name, the bound
// besideAFullPoolWait gives work a full pool holds up.
const holdAndWaitBound = 30 * time.Second

// poolWaits makes pool's waiting hook a signal: each taker that finds too little of the pool free
// and starts to wait sends on it. It is set before any taker can reach the pool.
func poolWaits(pool *loadSlots) <-chan struct{} {
	waits := make(chan struct{}, 64)
	pool.waiting = func() {
		select {
		case waits <- struct{}{}:
		default:
		}
	}
	return waits
}

// queueTaker starts a taker of bytes of the pool that holds nothing, on a goroutine of its own,
// and returns once it waits for them (poolWaits): bytes past what the pool has free beside the
// holder under test, and within what it has once that holder gives its own back. The taker gives
// them back as soon as it holds them, and served closes then.
func queueTaker(t *testing.T, waits <-chan struct{}, bytes int64) (served <-chan struct{}) {
	t.Helper()
	for len(waits) > 0 {
		<-waits
	}
	done := make(chan struct{})
	go func() {
		taker := newLoadSlot()
		if err := taker.take(context.Background(), bytes); err == nil {
			taker.release()
		}
		close(done)
	}()
	select {
	case <-waits:
	case <-done:
		t.Fatalf("the taker of %d bytes was served at once: the pool had them free beside the holder, so nothing here is tested", bytes)
	case <-time.After(holdAndWaitBound):
		t.Fatalf("the taker of %d bytes neither waited for the pool nor was served within %s", bytes, holdAndWaitBound)
	}
	return done
}

// answeredWithin is what answered sends within holdAndWaitBound, failing the test with what names
// the holder where nothing comes by then.
func answeredWithin(t *testing.T, answered <-chan error, what string) error {
	t.Helper()
	select {
	case err := <-answered:
		return err
	case <-time.After(holdAndWaitBound):
		t.Fatalf("%s did not finish within %s: it waited for more of the pool while it held its grant, which the pool could not give it while that grant stayed held", what, holdAndWaitBound)
		return nil
	}
}

// requireServed waits for the taker queueTaker started to be served.
func requireServed(t *testing.T, served <-chan struct{}) {
	t.Helper()
	select {
	case <-served:
	case <-time.After(holdAndWaitBound):
		t.Fatalf("the queued taker was not served within %s once the holder had finished", holdAndWaitBound)
	}
}

// pause is a goroutine's stop at a hook of the code under test, until the test lets it go on:
// reached closes as it stops there, and goOn lets it go on, once, and runs as the test ends in any
// case.
type pause struct {
	reached chan struct{}
	goOn    func()
	proceed chan struct{}
}

func newPause(t *testing.T) *pause {
	t.Helper()
	p := &pause{reached: make(chan struct{}), proceed: make(chan struct{})}
	p.goOn = sync.OnceFunc(func() { close(p.proceed) })
	t.Cleanup(p.goOn)
	return p
}

// stop is the hook's side: it reports the stop and waits to be let go on.
func (p *pause) stop() {
	close(p.reached)
	<-p.proceed
}

// await waits for the hook to stop, failing the test with what names it where it never does.
func (p *pause) await(t *testing.T, answered <-chan error, what string) {
	t.Helper()
	select {
	case <-p.reached:
	case err := <-answered:
		t.Fatalf("%s finished (%v) before it stopped where the test holds it", what, err)
	case <-time.After(holdAndWaitBound):
		t.Fatalf("%s never stopped where the test holds it within %s", what, holdAndWaitBound)
	}
}

// splitCharacters and splitInserts shape splitHistory for the two folds' test: rows weighed by
// their bytes, a state that declares an item more for each insert and so weighs more than they do,
// and less than twice as much.
const splitCharacters, splitInserts = 20_000, 400

// splitHistory is two rows of one text: characters characters typed in one update, and inserts
// characters typed into them in a second, each at a place of its own between them. A document
// applying both splits the first row's one item at every insert, so the state the fold encodes
// declares more structs than the rows declare between them, and weighs more than they do: the
// shape of an agent's rewrite, whose folded state CI found declaring a tenth more structs than its
// rows (browser_memory_test.go's agentRewrittenDocument, dispatch://INFRA-179).
func splitHistory(characters, inserts int) [][]byte {
	doc := crdt.New()
	text := doc.GetText("split")
	write := func(change func(txn *crdt.Transaction)) []byte {
		before := doc.StateVector()
		doc.Transact(change)
		return crdt.EncodeStateAsUpdateV1(doc, before)
	}
	typed := write(func(txn *crdt.Transaction) { text.Insert(txn, 0, strings.Repeat("x", characters), nil) })
	split := write(func(txn *crdt.Transaction) {
		for insert := inserts; insert > 0; insert-- {
			text.Insert(txn, insert*characters/(inserts+1), "y", nil)
		}
	})
	return [][]byte{typed, split}
}

// Two folds of one history whose state weighs more than its rows both finish, in a pool with room
// for one fold's read-back and not for two folds' rows: the first fold holds its rows' grant while
// the second queues for its own, which is more than is free, and the first fold's read-back then
// needs more than it holds. That is the cycle CI met between a settlement's room load and a
// write's cold fork of one rewritten document (job 136940412632, dispatch://INFRA-179): the
// read-back queued behind the second fold for bytes the pool had free, and the second fold waited
// for the first's, until the write's 45-second bound answered it 503. The read-back holds nothing
// it built by then - the fold's own document is dropped - so it gives its grant back and takes the
// state's weight whole (fold, holdUnbuiltLoad): the second fold takes the bytes given back, folds
// and reads back in turn, and each finishes. With take put back to wait for the difference while
// it holds, the first fold's read-back and the second fold wait on each other until this test's
// bound.
func TestTwoFoldsOfOneHistoryFinishWhereTheFirstsReadBackNeedsMoreThanItHolds(t *testing.T) {
	ctx := context.Background()
	database := storetest.Open(t)
	artifactID := createDocument(t, database, "before")
	rows := splitHistory(splitCharacters, splitInserts)
	var bytes, structs int64
	for version, row := range rows {
		if _, err := database.Pool.Exec(ctx, `insert into doc_updates (artifact_id, version, update, content_changed) values ($1, $2, $3, false)`,
			artifactID, version+1, row); err != nil {
			t.Fatalf("store row %d: %v", version+1, err)
		}
		bytes += int64(len(row))
		structs += int64(declaredStructs(row))
	}
	rowsWeight := copyWeight(bytes, structs)
	state, _, _, misread, err := fold(ctx, memoryUpdates(ctx, rows), maxUpdateItems)
	if err != nil || misread != nil {
		t.Fatalf("fold the history: %v, %v", err, misread)
	}
	stateWeight := storedCopyWeight(state)
	if stateWeight <= rowsWeight || stateWeight >= 2*rowsWeight {
		t.Fatalf("the history's rows weigh %d and the state they fold into %d, want the state heavier than the rows and lighter than two folds' rows, or nothing here is tested",
			rowsWeight, stateWeight)
	}

	versioned := NewPgVersioned(database)
	pool := useLoadSlots(t, newLoadSlots(1))
	waits := poolWaits(pool)
	// The pool has free what one fold's read-back takes: one fold's rows fit, a second fold's rows
	// do not fit beside them, and the read-back's difference does.
	filler := fillPoolBut(t, pool, stateWeight)
	held := newPause(t)
	var first atomic.Bool
	versioned.beforeFold = func(string) {
		if first.CompareAndSwap(false, true) {
			held.stop()
		}
	}
	loads := make(chan error, 2)
	load := func() {
		_, err := versioned.Load(ctx, artifactID)
		loads <- err
	}
	go load()
	held.await(t, loads, "the first fold")
	go load()
	select {
	case <-waits:
	case err := <-loads:
		t.Fatalf("the second fold finished (%v) while the first held its rows' grant, want it queued for its own", err)
	case <-time.After(holdAndWaitBound):
		t.Fatalf("the second fold neither queued for the pool nor finished within %s", holdAndWaitBound)
	}
	held.goOn()
	for range 2 {
		if err := answeredWithin(t, loads, "a fold of the history"); err != nil {
			t.Fatalf("a fold of the history: %v", err)
		}
	}
	if got := pool.held.Load(); got != filler.bytes {
		t.Fatalf("the pool holds %d bytes once both folds returned, want the test's %d alone", got, filler.bytes)
	}
	t.Logf("two folds of rows weighing %d into a state weighing %d finished in a pool with %d bytes free", rowsWeight, stateWeight, stateWeight)
}

// A cold write whose fork's read fails after its fold took bytes on the transaction's grant leaves
// none of them held. The fold takes them before any fork's cover does (foldRead, coldFork), so
// the write's own release of its share, which needs that cover, gave back nothing (giveFork), and
// the pool was short by those bytes until the process restarted. Here the write's request ends as
// its fold begins, so the fold, holding its grant, answers the request's end; the read that leaves
// no fork gives the grant back (forkLive, releaseUnforked), and the next write lands. Before, the
// pool still held what the fold took once the transaction had ended.
func TestAColdWriteThatFailsAfterItsFoldTookTheGrantLeavesNoneOfItHeld(t *testing.T) {
	ctx := context.Background()
	service, artifactID := newTestService(t)
	service.settle = time.Hour
	storeHistory(t, service, artifactID, grantTestState(t, 16<<10))
	if _, err := service.persistence.AppendUpdate(ctx, artifactID, emptyUpdate); err != nil {
		t.Fatal(err)
	}
	if service.srv.GetDoc(artifactID) != nil {
		t.Fatal("the room is resident, so the write would fork warm and fold nothing")
	}
	versioned, ok := service.persistence.(*PgVersioned)
	if !ok {
		t.Fatalf("the service reads through %T, which has no seam before its fold", service.persistence)
	}
	pool := useLoadSlots(t, newLoadSlots(1))
	var taken slotsHeld
	pool.taken = taken.record
	request, cancel := context.WithCancel(ctx)
	defer cancel()
	versioned.beforeFold = func(room string) {
		if room == artifactID {
			cancel()
		}
	}
	err := grantWrite(request, service, artifactID, grantWriteHeading, "Written")
	if err == nil {
		t.Fatal("the write went through though its request ended inside its fork's fold")
	}
	grants := taken.all()
	if len(grants) == 0 {
		t.Fatalf("the write failed (%v) having taken nothing of the pool: its fold took no grant, so nothing here is tested", err)
	}
	if held := pool.held.Load(); held != 0 {
		t.Fatalf("the pool holds %d bytes once the write failed (%v) and its transaction ended, want none: what its cold fork's fold took on the transaction's grant (grants left %v held) was given back by nothing",
			held, err, grants)
	}
	versioned.beforeFold = nil
	if err := grantWrite(ctx, service, artifactID, grantWriteHeading, "Written"); err != nil {
		t.Fatalf("the write after it: %v", err)
	}
	requireHeading(t, service, artifactID, false)
	if held := pool.held.Load(); held != 0 {
		t.Fatalf("the pool holds %d bytes once the next write landed, want none", held)
	}
	t.Logf("the write failed (%v) after its fold took %d bytes, and the pool held none of them after", err, grants[0])
}

// The decode of a log's runs after a fold that did not read back runs where nothing the fold built
// is held, on a grant its read holds alone (runsDocument, holdUnbuiltLoad), so where the runs
// weigh more than that grant holds and the pool does not give the difference at once, it gives
// the grant back and takes the runs' weight whole. Here a taker queued for more than is free
// beside the grant gets the bytes given back, and the decode then takes its own and finishes.
func TestARunsDecodeGivesItsReadsGrantBackRatherThanQueueForMoreWhileItHoldsIt(t *testing.T) {
	runs := [][]byte{proseState(t, 16<<10)}
	weight := storedCopyWeight(runs...)
	pool := useLoadSlots(t, newLoadSlots(1))
	waits := poolWaits(pool)
	filler := fillPoolBut(t, pool, weight)
	ctx, slot, done := withLoadSlot(context.Background())
	defer done()
	ctx = slot.ownedBy(ctx)
	// What the read's fold left its grant holding, lighter than the runs.
	if err := slot.take(ctx, weight/2); err != nil {
		t.Fatalf("take the fold's grant: %v", err)
	}
	served := queueTaker(t, waits, weight-weight/2+1)
	decoded := make(chan error, 1)
	go func() {
		_, err := runsDocument(ctx, runs, maxUpdateItems)
		decoded <- err
	}()
	if err := answeredWithin(t, decoded, "the runs' decode"); err != nil {
		t.Fatalf("decode the runs: %v", err)
	}
	requireServed(t, served)
	if slot.bytes != weight || pool.held.Load() != filler.bytes+weight {
		t.Fatalf("the read's grant holds %d bytes and the pool %d once the runs decoded, want the runs' %d and the test's %d beside them",
			slot.bytes, pool.held.Load(), weight, filler.bytes)
	}
}

// loadLeavingLess is a store whose Load takes took on the grant ctx carries, as a fold of rows
// lighter than the state they fold into leaves it, stops at then, and answers state.
type loadLeavingLess struct {
	VersionedStore
	state []byte
	took  int64
	then  func()
}

func (l *loadLeavingLess) Load(ctx context.Context, _ string) (persistence.LoadResult, error) {
	_, slot, _ := withLoadSlot(ctx)
	if err := slot.take(ctx, l.took); err != nil {
		return persistence.LoadResult{}, err
	}
	l.then()
	return persistence.LoadResult{Update: l.state, Version: 2}, nil
}

// A read of a stored history whose state weighs more than the grant its read left (the decode of a
// log's runs encoded anew, which can) takes the difference with nothing decoded yet: the read
// handed back bytes (loadDecodedHistory). On a grant it took up holding nothing, where the pool
// does not give the difference at once, it gives the grant back and takes the history's weight
// whole, and a taker queued for more than is free beside the grant gets the bytes given back.
func TestAReadHistoryHeavierThanItsReadsGrantGivesItBackRatherThanQueueForMoreWhileItHoldsIt(t *testing.T) {
	state := proseState(t, 16<<10)
	weight := storedCopyWeight(state)
	pool := useLoadSlots(t, newLoadSlots(1))
	waits := poolWaits(pool)
	filler := fillPoolBut(t, pool, weight)
	loaded := newPause(t)
	store := &loadLeavingLess{state: state, took: weight / 2, then: loaded.stop}
	read := make(chan error, 1)
	go func() {
		history, err := loadDecodedHistory(context.Background(), store, "room", maxUpdateItems, nil)
		if err == nil && history.decoded.doc == nil {
			err = errors.New("the history decoded into nothing")
		}
		read <- err
	}()
	loaded.await(t, read, "the history's read")
	served := queueTaker(t, waits, weight-weight/2+1)
	loaded.goOn()
	if err := answeredWithin(t, read, "the history's decode"); err != nil {
		t.Fatalf("read and decode the history: %v", err)
	}
	requireServed(t, served)
	if held := pool.held.Load(); held != filler.bytes {
		t.Fatalf("the pool holds %d bytes once the history's read returned, want the test's %d alone", held, filler.bytes)
	}
}

// A transaction's first fork's cover takes its bytes on a grant that holds at most what the fork's
// own read took - a cold read's fold - and nothing that read built (liveWrite.coverFork), so where
// the pool does not give the difference at once it gives the grant back and takes the fork's
// weight whole. A taker queued for more than is free beside the read's grant gets the bytes given
// back, and the cover then takes the fork's.
func TestAFirstForksCoverGivesItsReadsGrantBackRatherThanQueueForMoreWhileItHoldsIt(t *testing.T) {
	const weight = 1 << 20
	pool := useLoadSlots(t, newLoadSlots(1))
	waits := poolWaits(pool)
	filler := fillPoolBut(t, pool, weight)
	write := &liveWrite{slot: &forkSlot{slot: newLoadSlot()}}
	ctx := write.carriedBy(context.Background())
	// What the cold read's fold took on the transaction's grant, which no fork holds yet.
	if err := write.slot.slot.take(ctx, weight/2); err != nil {
		t.Fatalf("take the fold's grant: %v", err)
	}
	served := queueTaker(t, waits, weight-weight/2+1)
	covered := make(chan error, 1)
	go func() { covered <- write.coverFork(ctx, weight) }()
	if err := answeredWithin(t, covered, "the first fork's cover"); err != nil {
		t.Fatalf("cover the first fork: %v", err)
	}
	requireServed(t, served)
	if !write.holdsSlot || write.slot.forks != 1 || write.slot.slot.bytes != weight {
		t.Fatalf("the cover left the write holding a share %t of a grant of %d bytes held by %d forks, want a share of the fork's %d held by one",
			write.holdsSlot, write.slot.slot.bytes, write.slot.forks, int64(weight))
	}
	write.giveFork()
	if held := pool.held.Load(); held != filler.bytes {
		t.Fatalf("the pool holds %d bytes once the fork's share was given back, want the test's %d alone", held, filler.bytes)
	}
}

// joinedTransaction begins a transaction on service's store and joins it (Service.Join), as an
// API write's handler does, and answers the context its operations run with, its ledger, and what
// ends it: the ledger discarded and the transaction rolled back, once, which the test's end runs
// too. A committed transaction is ended the same way.
func joinedTransaction(t *testing.T, ctx context.Context, service *Service) (context.Context, *Ledger, func()) {
	t.Helper()
	tx, err := service.store.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin a transaction: %v", err)
	}
	joined, ledger := service.Join(ctx, tx)
	end := sync.OnceFunc(func() {
		ledger.Discard()
		_ = tx.Rollback(context.Background())
	})
	t.Cleanup(end)
	return joined, ledger, end
}

// requireGrantShort fails the test unless err, what answered, is the pool's refusal of a take it
// does not give at once while its holder holds some (errGrantShort), which the API answers 503
// DOC_SERVICE_UNAVAILABLE (ErrServiceUnavailable).
func requireGrantShort(t *testing.T, err error, what string) {
	t.Helper()
	if !errors.Is(err, errGrantShort) || !errors.Is(err, ErrServiceUnavailable) {
		t.Fatalf("%s was answered %v, want it refused busy: %v", what, err, errGrantShort)
	}
}

// residentGrantDocuments creates one document on each of the issues numbered, its stored history
// grantTestState's of the bytes given for it, and loads a room of each, so a write's fork of it
// is the room cloned and weighs what a copy of the room weighs (copyOfRoom). It answers the
// documents and their weights, in the order given.
func residentGrantDocuments(t *testing.T, service *Service, issues []int, bytes []int) ([]string, []int64) {
	t.Helper()
	documents, weights := make([]string, len(issues)), make([]int64, len(issues))
	for index, issue := range issues {
		documents[index] = createIssueDocument(t, service.store, issue, "Written whole by this test.")
		storeHistory(t, service, documents[index], grantTestState(t, bytes[index]))
		if err := loadRoomOf(service, documents[index]); err != nil {
			t.Fatalf("load room %s: %v", documents[index], err)
		}
		weights[index] = service.copyOfRoom(service.srv.GetDoc(documents[index]))
	}
	return documents, weights
}

// grantEdit is the write the tests below make of each document: its heading replaced.
var grantEdit = []model.EditOp{{Op: "replace", Find: grantWriteHeading, With: "Written"}}

// applyGrantEdit makes grantEdit to artifactID in the transaction joined is joined to, on a
// goroutine of its own, and answers what the write answers.
func applyGrantEdit(service *Service, joined context.Context, artifactID string) <-chan error {
	answered := make(chan error, 1)
	go func() {
		_, err := service.ApplyOps(joined, artifactID, grantEdit, grantWriter, nil)
		answered <- err
	}()
	return answered
}

// A transaction's later document takes what its fork weighs past the grant its first document's
// fork holds only where the pool gives it at once (loadSlot.take): refused busy while the pool
// lacks the difference, which leaves both documents as they were, and landed once the pool has it
// free. Before, the cover waited for the difference holding the first fork's grant
// (loadSlot.joinedBy), so the write here was answered by nothing while the pool lacked it.
func TestAJoinedTransactionsSecondForkTakesWhatItWeighsPastTheFirstsAtOnceOrIsRefusedBusy(t *testing.T) {
	ctx := context.Background()
	service, _ := newTestService(t)
	service.settle = time.Hour
	documents, weights := residentGrantDocuments(t, service, []int{31, 32}, []int{16 << 10, 64 << 10})
	first, second := documents[0], documents[1]
	firstWeight, secondWeight := weights[0], weights[1]
	if secondWeight <= firstWeight {
		t.Fatalf("the second room's copy weighs %d and the first's %d, want the second heavier, or nothing here is tested", secondWeight, firstWeight)
	}
	pool := useLoadSlots(t, newLoadSlots(1))
	joined, _, end := joinedTransaction(t, ctx, service)
	if err := <-applyGrantEdit(service, joined, first); err != nil {
		t.Fatalf("the transaction's write of the first document: %v", err)
	}
	if held := pool.held.Load(); held != firstWeight {
		t.Fatalf("the pool holds %d bytes once the first document's fork is built, want its %d", held, firstWeight)
	}
	filler := fillPoolBut(t, pool, secondWeight-firstWeight-1)
	err := answeredWithin(t, applyGrantEdit(service, joined, second), "the transaction's write of the second document")
	requireGrantShort(t, err, "the transaction's write of the second document, while the pool lacked one byte of what its fork weighs past the first's")
	if held := pool.held.Load(); held != filler.bytes+firstWeight {
		t.Fatalf("the pool holds %d bytes once the second document's fork was refused, want the first fork's %d beside the test's %d", held, firstWeight, filler.bytes)
	}
	end()
	requireHeading(t, service, first, true)
	requireHeading(t, service, second, true)
	if held := pool.held.Load(); held != filler.bytes {
		t.Fatalf("the pool holds %d bytes once the refused transaction ended, want the test's %d alone", held, filler.bytes)
	}
	filler.release()

	retried, ledger, endRetry := joinedTransaction(t, ctx, service)
	for _, document := range documents {
		if err := <-applyGrantEdit(service, retried, document); err != nil {
			t.Fatalf("the retry's write of %s, the pool free: %v", document, err)
		}
	}
	if held := pool.held.Load(); held != secondWeight {
		t.Fatalf("the pool holds %d bytes once the retry's second fork is built, want the grant raised to its %d", held, secondWeight)
	}
	if err := ledger.Commit(ctx); err != nil {
		t.Fatalf("commit the retry: %v", err)
	}
	endRetry()
	requireHeading(t, service, first, false)
	requireHeading(t, service, second, false)
	if held := pool.held.Load(); held != 0 {
		t.Fatalf("the pool holds %d bytes once the retry committed, want none", held)
	}
}

// Two transactions, each holding its first document's fork and each needing, for a second
// document's fork, more than the pool has free - bytes only the other's grant would free - both
// finish: the first is refused busy at once and rolls back, which frees what the second needs, so
// the second lands, and the first's retry lands after it. Before, a later fork waited for the
// difference holding its first fork's grant (loadSlot.joinedBy): the first queued for bytes the
// second held, the second queued behind it for bytes the first held, and neither was served
// until this test's bound.
func TestTwoJoinedTransactionsEachNeedingWhatTheOthersGrantHoldsBothFinish(t *testing.T) {
	ctx := context.Background()
	service, _ := newTestService(t)
	service.settle = time.Hour
	documents, weights := residentGrantDocuments(t, service, []int{33, 34, 35, 36}, []int{16 << 10, 64 << 10, 16 << 10, 64 << 10})
	// Transaction one writes documents 0 and 1, transaction two documents 2 and 3.
	needOne, needTwo := weights[1]-weights[0], weights[3]-weights[2]
	free := min(needOne, needTwo) - 1
	if needOne <= 0 || needTwo <= 0 || needTwo > free+weights[0] || needOne > free+weights[2] {
		t.Fatalf("the documents weigh %v, so the two transactions do not each need what only the other's grant frees, and nothing here is tested", weights)
	}
	pool := useLoadSlots(t, newLoadSlots(1))
	waits := poolWaits(pool)
	one, _, endOne := joinedTransaction(t, ctx, service)
	two, ledgerTwo, endTwo := joinedTransaction(t, ctx, service)
	for _, write := range []struct {
		joined   context.Context
		document string
	}{{one, documents[0]}, {two, documents[2]}} {
		if err := <-applyGrantEdit(service, write.joined, write.document); err != nil {
			t.Fatalf("a transaction's write of its first document: %v", err)
		}
	}
	filler := fillPoolBut(t, pool, free)
	for len(waits) > 0 {
		<-waits
	}
	answeredOne := applyGrantEdit(service, one, documents[1])
	var err error
	select {
	case err = <-answeredOne:
	case <-waits:
		// The first transaction queued for bytes the second's grant holds: the second's write of
		// its own second document then queues behind it for bytes the first's holds.
		answeredTwo := applyGrantEdit(service, two, documents[3])
		select {
		case err = <-answeredOne:
		case err = <-answeredTwo:
		case <-time.After(holdAndWaitBound):
			t.Fatalf("neither transaction's write of its second document was answered within %s: each queued for more of the pool holding its first fork's grant, for bytes only the other's grant would free", holdAndWaitBound)
		}
		t.Fatalf("the first transaction's write of its second document queued for more of the pool holding its first fork's grant, and a write was answered (%v) only after", err)
	case <-time.After(holdAndWaitBound):
		t.Fatalf("the first transaction's write of its second document neither waited for the pool nor was answered within %s", holdAndWaitBound)
	}
	requireGrantShort(t, err, "the first transaction's write of its second document, which needs what the second's grant holds")
	endOne()
	if held := pool.held.Load(); held != filler.bytes+weights[2] {
		t.Fatalf("the pool holds %d bytes once the first transaction ended, want the second's %d beside the test's %d", held, weights[2], filler.bytes)
	}
	if err := <-applyGrantEdit(service, two, documents[3]); err != nil {
		t.Fatalf("the second transaction's write of its second document, once the first gave its grant back: %v", err)
	}
	if err := ledgerTwo.Commit(ctx); err != nil {
		t.Fatalf("commit the second transaction: %v", err)
	}
	endTwo()
	requireHeading(t, service, documents[0], true)
	requireHeading(t, service, documents[1], true)
	requireHeading(t, service, documents[2], false)
	requireHeading(t, service, documents[3], false)

	retried, ledger, endRetry := joinedTransaction(t, ctx, service)
	for _, document := range documents[:2] {
		if err := <-applyGrantEdit(service, retried, document); err != nil {
			t.Fatalf("the first transaction's retry of %s: %v", document, err)
		}
	}
	if err := ledger.Commit(ctx); err != nil {
		t.Fatalf("commit the first transaction's retry: %v", err)
	}
	endRetry()
	requireHeading(t, service, documents[0], false)
	requireHeading(t, service, documents[1], false)
	filler.release()
	if held := pool.held.Load(); held != 0 {
		t.Fatalf("the pool holds %d bytes once both transactions committed, want none", held)
	}
}

// A transaction holding its first document's fork, beside a waiter queued for bytes that fork's
// grant holds, whose second document's fork weighs more past that grant than the pool has free,
// finishes: refused busy at once, since a take on a held grant never queues (loadSlot.take), and
// the waiter is served once the transaction gives its grant back. Before, the cover queued for the
// difference behind the waiter (loadSlot.joinedBy), which waited for the bytes the transaction
// held, until this test's bound.
func TestAJoinedTransactionBesideAWaiterForItsGrantFinishesAndTheWaiterGoesOn(t *testing.T) {
	ctx := context.Background()
	service, _ := newTestService(t)
	service.settle = time.Hour
	documents, weights := residentGrantDocuments(t, service, []int{37, 38}, []int{16 << 10, 64 << 10})
	need := weights[1] - weights[0]
	if need <= 0 {
		t.Fatalf("the documents weigh %v, want the second heavier, or nothing here is tested", weights)
	}
	pool := useLoadSlots(t, newLoadSlots(1))
	waits := poolWaits(pool)
	joined, _, end := joinedTransaction(t, ctx, service)
	if err := <-applyGrantEdit(service, joined, documents[0]); err != nil {
		t.Fatalf("the transaction's write of the first document: %v", err)
	}
	filler := fillPoolBut(t, pool, need-1)
	// The waiter needs more than is free beside the transaction's grant, and no more than is free
	// once that grant is given back.
	served := queueTaker(t, waits, need)
	err := answeredWithin(t, applyGrantEdit(service, joined, documents[1]), "the transaction's write of the second document, beside the waiter")
	requireGrantShort(t, err, "the transaction's write of the second document, beside a waiter for its grant's bytes")
	end()
	requireServed(t, served)
	requireHeading(t, service, documents[0], true)
	requireHeading(t, service, documents[1], true)
	filler.release()
	if held := pool.held.Load(); held != 0 {
		t.Fatalf("the pool holds %d bytes once the transaction ended and the waiter was served, want none", held)
	}
}

// An issue's ask-state write knows its documents before its first fork (WriteIssueAskStates), so
// its first fork takes what both documents' forks weigh, summed (Service.forksAhead,
// liveWrite.coverFork), and the second fork takes nothing more: the pool records one take, of
// both weights, for the whole transaction. Before, the first fork took its own weight and the
// second the difference past it, a second take that a pool short of it refuses busy.
func TestAnIssuesAskStateWriteTakesBothDocumentsWeightsAtItsFirstForkAndNeverAgain(t *testing.T) {
	ctx := context.Background()
	service, _ := newTestService(t)
	service.settle = time.Hour
	_, weights := residentGrantDocuments(t, service, []int{41, 41}, []int{16 << 10, 64 << 10})
	sum := weights[0] + weights[1]
	pool := useLoadSlots(t, newLoadSlots(1))
	var taken slotsHeld
	pool.taken = taken.record
	joined, _, end := joinedTransaction(t, ctx, service)
	if _, err := service.WriteIssueAskStates(joined, "DOC-41", nil, grantWriter, true); err != nil {
		t.Fatalf("write the issue's ask states: %v", err)
	}
	if takes := taken.all(); len(takes) != 1 || takes[0] != sum {
		t.Fatalf("the pool recorded takes leaving it holding %v for the transaction's two forks, want one take of both weights, %d and %d, summed: %d", takes, weights[0], weights[1], sum)
	}
	if held := pool.held.Load(); held != sum {
		t.Fatalf("the pool holds %d bytes once both forks are built, want their %d", held, sum)
	}
	end()
	if held := pool.held.Load(); held != 0 {
		t.Fatalf("the pool holds %d bytes once the transaction ended, want none", held)
	}
}

// A read of a stored document under a transaction holding a fork takes on the transaction's
// grant, which Join's context carries (withLoadSlot, transactionGrant): what it weighs past the
// fork's grant, at once, so the pool holds one grant, the transaction's, raised to the read's
// weight, and no second grant is taken. The pool here has free what the read weighs past the
// fork's grant and not the read's whole weight, so a read that took a grant of its own waited for
// it holding the fork's grant, as it did before, until this test's bound.
func TestAStoredReadUnderATransactionTakesOnTheTransactionsGrant(t *testing.T) {
	ctx := context.Background()
	service, _ := newTestService(t)
	service.settle = time.Hour
	documents, weights := residentGrantDocuments(t, service, []int{43}, []int{16 << 10})
	stored := createIssueDocument(t, service.store, 44, "Written whole by this test.")
	state := grantTestState(t, 64<<10)
	storeHistory(t, service, stored, state)
	if service.srv.GetDoc(stored) != nil {
		t.Fatal("a room holds the stored document, so its read would read the room")
	}
	storedWeight := storedCopyWeight(state)
	if storedWeight <= weights[0] {
		t.Fatalf("the stored document weighs %d and the fork %d, want the stored one heavier, or nothing here is tested", storedWeight, weights[0])
	}
	pool := useLoadSlots(t, newLoadSlots(1))
	waits := poolWaits(pool)
	joined, ledger, end := joinedTransaction(t, ctx, service)
	if err := <-applyGrantEdit(service, joined, documents[0]); err != nil {
		t.Fatalf("the transaction's write: %v", err)
	}
	filler := fillPoolBut(t, pool, storedWeight-weights[0])
	read := make(chan error, 1)
	var tree *pmdoc.Node
	go func() {
		var err error
		tree, err = service.storedTree(joined, stored)
		read <- err
	}()
	if err := answeredWithin(t, read, "the stored read under the transaction"); err != nil || tree == nil {
		t.Fatalf("the stored read under the transaction answered %v with a tree %t", err, tree != nil)
	}
	if len(waits) > 0 {
		t.Fatal("a taker waited for the pool during the read: the read took a grant of its own beside the transaction's")
	}
	if grant, held := ledger.forks.slot.bytes, pool.held.Load(); grant != storedWeight || held != filler.bytes+storedWeight {
		t.Fatalf("the transaction's grant holds %d bytes and the pool %d once the stored read returned, want the transaction's one grant raised to the read's %d beside the test's %d",
			grant, held, storedWeight, filler.bytes)
	}
	end()
	if held := pool.held.Load(); held != filler.bytes {
		t.Fatalf("the pool holds %d bytes once the transaction ended, want the test's %d alone", held, filler.bytes)
	}
}

// twoRowDocument creates a document on issue number whose stored history is two rows -
// grantTestState's of bytes and an empty update after it - so a read of it folds them in memory
// under a slot of the buffered folds (foldRead), and answers it with what those rows weigh, which
// the fold takes before it decodes them.
func twoRowDocument(t *testing.T, service *Service, number, bytes int) (string, int64) {
	t.Helper()
	document := createIssueDocument(t, service.store, number, "Written whole by this test.")
	state := grantTestState(t, bytes)
	storeHistory(t, service, document, state)
	if _, err := service.persistence.AppendUpdate(context.Background(), document, emptyUpdate); err != nil {
		t.Fatalf("append the second row of %s: %v", document, err)
	}
	if service.srv.GetDoc(document) != nil {
		t.Fatalf("a room holds %s, so its read would fold nothing", document)
	}
	return document, storedCopyWeight(state, emptyUpdate)
}

// A transaction holding its first document's fork whose cold read of a later document finds every
// slot of the buffered folds held, by a fold waiting for the bytes the transaction holds, is
// refused busy at once (foldRead, errFoldSlotsBusy), and the fold goes on once the transaction
// gives its grant back. Before, the read waited for the slot holding its grant, and the fold for
// the grant's bytes, until this test's bound.
func TestAJoinedTransactionsColdReadBesideHeldFoldSlotsIsRefusedBusyAndTheFoldGoesOn(t *testing.T) {
	ctx := context.Background()
	service, _ := newTestService(t)
	service.settle = time.Hour
	written, weights := residentGrantDocuments(t, service, []int{45}, []int{16 << 10})
	later, _ := twoRowDocument(t, service, 46, 16<<10)
	folding, foldingWeight := twoRowDocument(t, service, 47, 64<<10)
	versioned, ok := service.persistence.(*PgVersioned)
	if !ok {
		t.Fatalf("the service reads through %T, whose fold slots this test cannot hold", service.persistence)
	}
	versioned.foldSlots = make(chan struct{}, 1)
	pool := useLoadSlots(t, newLoadSlots(1))
	waits := poolWaits(pool)
	joined, _, end := joinedTransaction(t, ctx, service)
	if err := <-applyGrantEdit(service, joined, written[0]); err != nil {
		t.Fatalf("the transaction's write of its first document: %v", err)
	}
	// The other fold takes the one slot and then waits for its rows' weight, which the pool has
	// free only once the transaction gives its grant back.
	filler := fillPoolBut(t, pool, foldingWeight-1)
	if foldingWeight-1+weights[0] < foldingWeight {
		t.Fatal("the transaction's grant would not free what the other fold waits for, so nothing here is tested")
	}
	folded := make(chan error, 1)
	go func() {
		_, err := versioned.Load(ctx, folding)
		folded <- err
	}()
	select {
	case <-waits:
	case err := <-folded:
		t.Fatalf("the other fold finished (%v) while the pool lacked its rows' weight, want it waiting for that holding the fold slot", err)
	case <-time.After(holdAndWaitBound):
		t.Fatalf("the other fold never came to wait for the pool within %s", holdAndWaitBound)
	}
	err := answeredWithin(t, applyGrantEdit(service, joined, later), "the transaction's write of its later document, a cold read beside a held fold slot")
	if !errors.Is(err, errFoldSlotsBusy) || !errors.Is(err, ErrServiceUnavailable) {
		t.Fatalf("the transaction's cold read beside a held fold slot was answered %v, want it refused busy: %v", err, errFoldSlotsBusy)
	}
	end()
	if err := answeredWithin(t, folded, "the other fold, once the transaction gave its grant back"); err != nil {
		t.Fatalf("the other fold: %v", err)
	}
	requireHeading(t, service, written[0], true)
	filler.release()
	if held := pool.held.Load(); held != 0 {
		t.Fatalf("the pool holds %d bytes once the transaction ended and the fold finished, want none", held)
	}
}

// A transaction holding its first document's fork reads a later document no room holds here, while
// the pool is filled so that a room's load would queue behind a waiter that needs the bytes the
// transaction holds: it loads no room, reads the stored document on its own grant and finishes
// (docTree, transactionHolds), and the waiter goes on once the transaction ends. Before, it loaded
// the room (roomBudget.openServerRoom), whose load took a grant of its own on ygo's load goroutine
// and queued behind the waiter, while the transaction waited for the load, until this test's bound.
func TestAJoinedTransactionsReadOfADocumentNoRoomHoldsLoadsNoRoom(t *testing.T) {
	ctx := context.Background()
	service, _ := newTestService(t)
	service.settle = time.Hour
	written, weights := residentGrantDocuments(t, service, []int{48}, []int{16 << 10})
	stored := createIssueDocument(t, service.store, 49, "Written whole by this test.")
	state := grantTestState(t, 8<<10)
	storeHistory(t, service, stored, state)
	if service.srv.GetDoc(stored) != nil {
		t.Fatal("a room holds the stored document, so its read loads nothing")
	}
	if storedCopyWeight(state) > weights[0] {
		t.Fatalf("the stored document weighs %d, past the fork's %d, so its read would take more and nothing here is tested", storedCopyWeight(state), weights[0])
	}
	pool := useLoadSlots(t, newLoadSlots(1))
	waits := poolWaits(pool)
	joined, _, end := joinedTransaction(t, ctx, service)
	if err := <-applyGrantEdit(service, joined, written[0]); err != nil {
		t.Fatalf("the transaction's write: %v", err)
	}
	filler := fillPoolBut(t, pool, 0)
	served := queueTaker(t, waits, weights[0])
	read := make(chan error, 1)
	var tree *pmdoc.Node
	go func() {
		var err error
		tree, err = service.docTree(joined, stored)
		read <- err
	}()
	if err := answeredWithin(t, read, "the transaction's read of a document no room holds"); err != nil || tree == nil {
		t.Fatalf("the transaction's read of a document no room holds answered %v with a tree %t", err, tree != nil)
	}
	if slices.Contains(service.srv.Rooms(), stored) {
		t.Fatal("the transaction's read loaded a room of the stored document")
	}
	end()
	requireServed(t, served)
	filler.release()
	if held := pool.held.Load(); held != 0 {
		t.Fatalf("the pool holds %d bytes once the transaction ended and the waiter was served, want none", held)
	}
}

// A browser's first sync's answer weighs the room once it holds a grant and tops the grant up
// where the room grew while it waited (weighStateAnswer). Nothing of the answer is built yet, so
// where the pool does not give the difference at once the top-up gives the grant back and takes
// the new weight whole, and a taker queued for more than is free beside the first grant gets the
// bytes given back. Here the answer's info was read before the room's last thousand keystrokes.
func TestAStateAnswersTopUpGivesItsGrantBackRatherThanQueueForMoreWhileItHoldsIt(t *testing.T) {
	service, _ := newTestService(t)
	service.settle = time.Hour
	room := newDocumentCopy()
	text := room.GetText("t")
	for range 1_000 {
		room.Transact(func(txn *crdt.Transaction) { text.Insert(txn, 0, "x", nil) })
	}
	info := websocket.StateAnswerInfo{Room: "room", Doc: room}
	before := min(answerWeight(room, 0, 0), loadSlotBytes)
	after := min(answerWeight(room, 0, int64(info.StructsNow())), loadSlotBytes)
	if after <= before {
		t.Fatalf("the answer weighs %d before the room grew and %d after, want more after, or nothing here is tested", before, after)
	}
	pool := useLoadSlots(t, newLoadSlots(1))
	waits := poolWaits(pool)
	filler := fillPoolBut(t, pool, after)
	granted := newPause(t)
	var first atomic.Bool
	pool.taken = func(int64) {
		if first.CompareAndSwap(false, true) {
			granted.stop()
		}
	}
	answered := make(chan error, 1)
	var release func()
	go func() {
		var err error
		release, err = service.weighStateAnswer(info)
		answered <- err
	}()
	granted.await(t, answered, "the answer's first grant")
	served := queueTaker(t, waits, after-before+1)
	granted.goOn()
	if err := answeredWithin(t, answered, "the answer's top-up"); err != nil {
		t.Fatalf("the answer was refused: %v", err)
	}
	requireServed(t, served)
	if held := pool.held.Load(); held != filler.bytes+after {
		t.Fatalf("the pool holds %d bytes once the answer is admitted, want its %d beside the test's %d", held, after, filler.bytes)
	}
	release()
	if held := pool.held.Load(); held != filler.bytes {
		t.Fatalf("the pool holds %d bytes once the answer was sent, want the test's %d alone", held, filler.bytes)
	}
}

// loadDocumentThen is the service's store, stopping at then as its first LoadDocument read of room
// returns, with the document that read decoded still held under the read's grant, and counting
// its reads of room.
type loadDocumentThen struct {
	VersionedStore
	room  string
	reads atomic.Int32
	then  func()
}

func (s *loadDocumentThen) LoadDocument(ctx context.Context, room string) (LoadedDocument, error) {
	loaded, err := s.VersionedStore.LoadDocument(ctx, room)
	if room == s.room && s.reads.Add(1) == 1 {
		s.then()
	}
	return loaded, err
}

// A read of a room behind the stored head holds the stored document it loaded as it takes what the
// merge of the room's unstored updates and its apply decode on top of it (storedWithUpdates), so
// it does not wait for that: where the pool does not give it at once, the read drops the document,
// gives its grant back, takes the total whole and reads again under it (redoWhole). A taker queued
// for more than is free beside the load's grant gets the bytes given back, and the read's second
// pass then holds the total and merges the room's updates into the document it loads.
func TestAStoredReadBehindTheHeadReadsAgainUnderTheWholeGrantRatherThanQueueForMoreWhileItHoldsIt(t *testing.T) {
	ctx := context.Background()
	service, artifactID := newTestService(t)
	service.settle = time.Hour
	state := proseState(t, 16<<10)
	storeHistory(t, service, artifactID, state)
	room := copyOf(t, state)
	commit := func(edit func()) []byte {
		t.Helper()
		before := room.StateVector()
		edit()
		return crdt.EncodeStateAsUpdateV1(room, before)
	}
	light := commit(func() { chainedRun(t, room, lastCharacter(t, room), 0x5000, 1) })
	heavy := commit(func() { chainedRun(t, room, lastCharacter(t, room), 0x7e57, unslottedApplyStructs+100) })
	unstored := [][]byte{light, heavy}
	merge := mergeWeight(unstored)
	if merge == 0 {
		t.Fatal("the room's updates merge on no grant, so nothing here is tested")
	}
	merged, err := mergeUpdates(unstored)
	if err != nil {
		t.Fatal(err)
	}
	extra := merge
	if apply, heavy := heavyUpdateWeight(merged); heavy && apply > merge {
		extra = apply
	}

	pool := useLoadSlots(t, newLoadSlots(1))
	waits := poolWaits(pool)
	loaded := newPause(t)
	reads := &loadDocumentThen{VersionedStore: service.persistence, room: artifactID, then: loaded.stop}
	service.persistence = reads
	read := make(chan error, 1)
	var tree *pmdoc.Node
	go func() {
		var err error
		tree, err = service.storedWithUpdates(ctx, artifactID, unstored)
		read <- err
	}()
	loaded.await(t, read, "the stored read's load")
	load := pool.held.Load()
	if load <= 0 {
		t.Fatal("the stored read's load holds nothing of the pool, so nothing here is tested")
	}
	filler := fillPoolBut(t, pool, extra)
	served := queueTaker(t, waits, extra+1)
	loaded.goOn()
	if err := answeredWithin(t, read, "the stored read"); err != nil {
		t.Fatalf("read the stored document behind the room's updates: %v", err)
	}
	requireServed(t, served)
	if markdown, err := renderTree(tree); err != nil || markdown != docMarkdown(room) {
		t.Fatalf("the read rendered %d bytes (%v), want the %d the room holds", len(markdown), err, len(docMarkdown(room)))
	}
	if got := reads.reads.Load(); got != 2 {
		t.Fatalf("the stored document was loaded %d times, want twice: once before the merge's take was refused, and once more under the whole grant", got)
	}
	if held := pool.held.Load(); held != filler.bytes {
		t.Fatalf("the pool holds %d bytes once the read returned, want the test's %d alone", held, filler.bytes)
	}
	t.Logf("the read's load held %d bytes, its merge and apply %d more, and it read again under the whole grant", load, extra)
}

// The append gate's check of a history replaced under a room weighs the history's rows before its
// fold decodes any of them and takes their weight whole (storedHistoryHolds), so the fold takes
// nothing more row by row; and it holds the document that history makes as it takes what the run
// onto it costs, so it does not wait for that: where the pool does not give it at once, it drops
// the document, gives its grant back, takes the total whole and decodes the history again under it
// (redoWhole). A taker queued for more than is free beside the rows' grant gets the bytes given
// back, and the check then holds the total and judges the run.
func TestTheGatesCheckOfAReplacedHistoryDecodesAgainUnderTheWholeGrantRatherThanQueueForMoreWhileItHoldsIt(t *testing.T) {
	ctx := context.Background()
	database := storetest.Open(t)
	artifactID := createDocument(t, database, "before")
	rows, run := deletedHistory(deletedCharacters)
	var bytes, structs int64
	for version, row := range rows {
		if _, err := database.Pool.Exec(ctx, `insert into doc_updates (artifact_id, version, update, content_changed) values ($1, $2, $3, false)`,
			artifactID, version+1, row); err != nil {
			t.Fatalf("store row %d: %v", version+1, err)
		}
		bytes += int64(len(row))
		structs += int64(declaredStructs(row))
	}
	rowsWeight := copyWeight(bytes, structs)
	runWeight := copyWeight(int64(len(run)), int64(declaredStructs(run)))

	pool := useLoadSlots(t, newLoadSlots(1))
	waits := poolWaits(pool)
	granted := newPause(t)
	var first atomic.Bool
	var firstHeld atomic.Int64
	pool.taken = func(held int64) {
		if first.CompareAndSwap(false, true) {
			firstHeld.Store(held)
			granted.stop()
		}
	}
	checked := make(chan error, 1)
	var holds bool
	go func() {
		var err error
		holds, err = storedHistoryHolds(ctx, database.Pool, artifactID, int64(len(rows)), []BrowserUpdate{{Update: run, ContentChanged: true}}, maxUpdateItems)
		checked <- err
	}()
	granted.await(t, checked, "the gate's check")
	if got := firstHeld.Load(); got != rowsWeight {
		t.Fatalf("the gate's first grant left %d bytes held, want the %d the history's rows weigh, taken before its fold decodes any of them", got, rowsWeight)
	}
	filler := fillPoolBut(t, pool, runWeight)
	served := queueTaker(t, waits, runWeight+1)
	granted.goOn()
	if err := answeredWithin(t, checked, "the gate's check"); err != nil {
		t.Fatalf("the gate's check: %v", err)
	}
	if !holds {
		t.Fatal("the gate's check found the history does not hold the run, which builds on its clocks alone")
	}
	requireServed(t, served)
	if held := pool.held.Load(); held != filler.bytes {
		t.Fatalf("the pool holds %d bytes once the check returned, want the test's %d alone", held, filler.bytes)
	}
}

// A write's decode-cap count copies the room the write's fork was read from, on the write's own
// grant, which holds the fork and cannot be given back while the operation runs: where the copy
// weighs more than the grant holds and the pool does not give the difference at once, the count
// refuses the write busy, the 503 its caller retries, and takes nothing (leavesRoomPastDecodeCap).
// Before, it waited for the difference without a deadline while the write held its fork. The room
// here stands within a thousand structs of the cap and the write adds two thousand, so only the
// copy answers; the room is no room this service loaded, so its copy weighs a whole slot
// (copyOfRoom), and the pool lacks one byte of what that weighs past the fork's grant.
func TestAWritesDecodeCapCountIsRefusedBusyRatherThanQueueForMoreWhileItHoldsItsFork(t *testing.T) {
	skipAtTheBoundsUnderRace(t)
	const written = 2_000
	live := typedRoom(t, maxUpdateItems-written/2, 0)
	// The write's fork as its operations left it, here only what they added, so all its structs
	// are the transaction's (forkStructs zero, updatesAdd).
	fork := newDocumentCopy()
	text := fork.GetText("t")
	fork.Transact(func(txn *crdt.Transaction) {
		for range written {
			text.Insert(txn, 0, "w", nil)
		}
	})
	pool := useLoadSlots(t, newLoadSlots(1))
	write := &liveWrite{clientID: crdt.NewClientID(), forkedFrom: weak.Make(live), slot: &forkSlot{slot: newLoadSlot(), forks: 1}, holdsSlot: true}
	if _, decided := decodeCapWithoutCopy(live, serverWrite{adds: write.updatesAdd(fork), bounded: true}); decided {
		t.Fatal("the room's own total answers the decode cap without a copy, so nothing here is tested")
	}
	service := calibratedService()
	copied := service.copyOfRoom(live)
	ctx := write.carriedBy(context.Background())
	const forkGrant = 1 << 20
	if err := write.slot.slot.take(ctx, forkGrant); err != nil {
		t.Fatalf("take the fork's grant: %v", err)
	}
	filler := fillPoolBut(t, pool, copied-forkGrant-1)
	refused := make(chan error, 1)
	go func() { refused <- service.refuseUndecodable(ctx, write, fork, nil) }()
	err := answeredWithin(t, refused, "the write's decode-cap count")
	if !errors.Is(err, ErrServiceUnavailable) || !strings.Contains(err.Error(), "a write holding its fork waits for no more of the pool") {
		t.Fatalf("the write's decode-cap count answered %v, want it refused busy (ErrServiceUnavailable) naming the wait it will not make", err)
	}
	if held := write.slot.slot.bytes; held != forkGrant {
		t.Fatalf("the write's grant holds %d bytes once its count was refused, want the fork's %d as it was", held, int64(forkGrant))
	}
	if held := pool.held.Load(); held != filler.bytes+forkGrant {
		t.Fatalf("the pool holds %d bytes once the count was refused, want the fork's %d beside the test's %d", held, int64(forkGrant), filler.bytes)
	}
	write.slot.slot.release()
	runtime.KeepAlive(live)
	t.Logf("the count of a copy weighing %d beside a fork's grant of %d was refused: %v", copied, int64(forkGrant), err)
}
