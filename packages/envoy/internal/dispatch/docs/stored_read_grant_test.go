package docs

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reearth/ygo/crdt"

	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
)

// loadDocumentHeld is the service's store, noting how many bytes of the pod's load concurrency
// were held as each of its LoadDocument reads of room returned.
type loadDocumentHeld struct {
	VersionedStore
	slotsHeld
	room  string
	slots *loadSlots
}

func (s *loadDocumentHeld) LoadDocument(ctx context.Context, room string) (LoadedDocument, error) {
	loaded, err := s.VersionedStore.LoadDocument(ctx, room)
	if room == s.room {
		s.record(s.slots.held.Load())
	}
	return loaded, err
}

// A read of the stored document (storedWithUpdates) keeps the grant its load took: the load hands
// the document back still holding it, the merge of what a room behind the stored head has not
// stored yet and the build of the tree run on it, and the read gives it back once the tree is
// built. A merge of a heavy share - a run of keystrokes a room took one transaction each, past
// unslottedApplyStructs structs - raises that grant by the share's own weight before it decodes
// any of it (heavyUpdateWeight), and the grant then holds both. Before, the load gave its grant
// back as it returned, so the document, the merge and the tree were built beside the pool's
// holders, whatever the share weighed.
func TestAStoredReadMergesAndBuildsItsTreeOnItsLoadsGrant(t *testing.T) {
	ctx := context.Background()
	service, artifactID := newTestService(t)
	service.settle = time.Hour
	state := proseState(t, 16<<10)
	storeHistory(t, service, artifactID, state)

	room := copyOf(t, state)
	stored := docMarkdown(room)
	storedClocks := room.StateVector()
	const typist crdt.ClientID = 0x7e57
	chainedRun(t, room, lastCharacter(t, room), typist, unslottedApplyStructs+100)
	share := crdt.EncodeStateAsUpdateV1(room, storedClocks)
	merge, heavy := heavyUpdateWeight(share)
	if !heavy {
		t.Fatalf("a share of %d bytes declaring %d structs is light, so nothing here is tested", len(share), declaredStructs(share))
	}

	slots := useLoadSlots(t, newLoadSlots(1))
	var taken slotsHeld
	slots.taken = taken.record
	loads := &loadDocumentHeld{VersionedStore: service.persistence, room: artifactID, slots: slots}
	service.persistence = loads

	for _, read := range []struct {
		name     string
		unstored []byte
		want     string
		// raised says whether the read raises its load's grant before the merge.
		raised bool
	}{
		{name: "the stored document alone (storedTree)", want: stored},
		{name: "the stored document with a heavy share merged in", unstored: share, want: docMarkdown(room), raised: true},
	} {
		before, loadsBefore := len(taken.all()), len(loads.all())
		tree, err := service.storedWithUpdates(ctx, artifactID, read.unstored)
		if err != nil {
			t.Fatalf("%s: %v", read.name, err)
		}
		if markdown, err := renderTree(tree); err != nil || markdown != read.want {
			t.Fatalf("%s rendered %d bytes (%v), want the %d the document holds", read.name, len(markdown), err, len(read.want))
		}
		grants := taken.all()[before:]
		returned := loads.all()[loadsBefore:]
		if len(grants) == 0 || grants[0] <= 0 {
			t.Fatalf("%s took grants leaving %v held, want its load's", read.name, grants)
		}
		load := grants[0]
		if !slices.Equal(returned, []int64{load}) {
			t.Fatalf("%s: the pool held %v as its load returned, want the %d its load took still held, for the merge and the tree to run on",
				read.name, returned, load)
		}
		want := []int64{load}
		if read.raised {
			want = append(want, min(load+merge, loadSlotBytes))
		}
		if !slices.Equal(grants, want) {
			t.Fatalf("%s took grants leaving %v held, want %v: the load's, and the share's %d bytes added to it before the merge", read.name, grants, want, merge)
		}
		if held := slots.held.Load(); held != 0 {
			t.Fatalf("%s still holds %d bytes once its tree is built", read.name, held)
		}
		t.Logf("%s: its load took %d bytes and still held them as it returned; grants left %v held", read.name, load, grants)
	}
}

// storedJudgement is a service whose document's history is one stored row of prose, which a load
// reads taking no grant of its own (foldRead), and a published write of that document whose loss
// check claims the stored text: its writer's runs, every block of them claimed by its one
// operation, which the stored document still holds. It answers the write and what a decode of the
// stored state costs the pool (storedCopyWeight).
func storedJudgement(t *testing.T) (*Service, *liveWrite, int64) {
	t.Helper()
	service, artifactID := newTestService(t)
	service.settle = time.Hour
	storeHistory(t, service, artifactID, proseState(t, 16<<10))
	loaded, err := service.persistence.Load(context.Background(), artifactID)
	if err != nil {
		t.Fatalf("read the stored state: %v", err)
	}
	written := copyOf(t, loaded.Update)
	clocks := written.StateVector()
	if len(clocks) != 1 {
		t.Fatalf("the stored state names %d clients, want the one that wrote it", len(clocks))
	}
	var writer crdt.ClientID
	for client := range clocks {
		writer = client
	}
	inserted, err := pmdoc.AuthoredTextRuns(written.GetXmlFragment(fragmentName), writer, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	blocks := map[string]struct{}{}
	for block := range inserted {
		blocks[block] = struct{}{}
	}
	loss := newLossCheck(writer, 0, []map[string]struct{}{blocks}, inserted)
	if loss == nil {
		t.Fatal("the stored text claims no block, so the judgement would read nothing")
	}
	return service, &liveWrite{artifactID: artifactID, loss: loss}, storedCopyWeight(loaded.Update)
}

// A published write's judgement on the stored document (lostAtStoredHead), where no room of this
// process holds it, decodes and walks that document under a grant of what its copy costs
// (storedCopyWeight): with the pool full, the judgement waits rather than decoding beside the
// holder, and once those bytes are given back within its bound it takes that weight, reaches its
// verdict and gives it back. Before, the decode took nothing, after the publish's transaction had
// given its own grant back.
func TestAPublishedWritesJudgementOnTheStoredDocumentWaitsForAGrantOfItsCopy(t *testing.T) {
	service, write, weight := storedJudgement(t)
	waiting := make(chan struct{})
	slots, _ := poolOfOneSlot(t, sync.OnceFunc(func() { close(waiting) }))
	holder := fillPoolBut(t, slots, 0)
	var taken slotsHeld
	slots.taken = taken.record

	judged := make(chan struct{})
	go func() {
		defer close(judged)
		service.recordPublishedLoss(write)
	}()
	select {
	case <-waiting:
	case <-judged:
		t.Fatal("the judgement reached its verdict while the only slot was held: it decoded the stored document beside the pool's holder")
	case <-time.After(30 * time.Second):
		t.Fatal("the judgement neither waited for a slot nor reached its verdict in 30 s")
	}
	select {
	case <-judged:
		t.Fatal("the judgement reached its verdict while the only slot was held")
	case <-time.After(200 * time.Millisecond):
	}
	holder.release()
	select {
	case <-judged:
	case <-time.After(30 * time.Second):
		t.Fatal("the judgement did not reach its verdict in 30 s once the slot was free")
	}
	if !write.lostVerdict || len(write.lost) != 0 {
		t.Fatalf("the judgement's verdict = lost %v (reached %t), want nothing lost: the stored document holds the write's text", write.lost, write.lostVerdict)
	}
	if got := taken.all(); !slices.Equal(got, []int64{weight}) {
		t.Fatalf("the judgement took grants leaving %v held, want one of %d, what the stored document's copy costs", got, weight)
	}
	if held := slots.held.Load(); held != 0 {
		t.Fatalf("the judgement still holds %d bytes once it reached its verdict", held)
	}
	t.Logf("the judgement waited for the pool, then took %d bytes for the stored document's copy", weight)
}

// A published write's judgement whose grant the pool does not give within publishedLossTimeout
// ends there: the wait is the read's error, no decode of the stored document runs, the write is
// left with no verdict, and its transaction's LostOps answers false, which the caller reports as
// undetermined (LEGION-269), as after a flush or a load that fails. A wait with no deadline would
// hold the write's writer slot, which a joined transaction of the server's own holding its first
// document's grant can wait for with no bound of its own.
func TestAPublishedWritesJudgementWhoseGrantOutwaitsItsBoundReachesNoVerdict(t *testing.T) {
	bound := publishedLossTimeout
	publishedLossTimeout = time.Second
	t.Cleanup(func() { publishedLossTimeout = bound })
	logs := captureLogs(t)
	service, write, _ := storedJudgement(t)
	ledger := &Ledger{live: map[string]*liveWrite{write.artifactID: write}}
	slots, waited := poolOfOneSlot(t, nil)
	holder := fillPoolBut(t, slots, 0)
	var taken slotsHeld
	slots.taken = taken.record

	judged := make(chan struct{})
	began := time.Now()
	go func() {
		defer close(judged)
		service.recordPublishedLoss(write)
	}()
	select {
	case <-judged:
	case <-time.After(10 * time.Second):
		holder.release()
		<-judged
		t.Fatalf("the judgement still waited for the pool 10 s into its %s bound, and ended only once the slot was given back (verdict reached %t)",
			publishedLossTimeout, write.lostVerdict)
	}
	ended := time.Since(began)
	if !waited.Load() {
		t.Fatal("the judgement ended without waiting for the pool, so nothing here is tested")
	}
	if got := taken.all(); len(got) != 0 {
		t.Fatalf("the judgement took grants leaving %v held while the pool was full: a decode ran", got)
	}
	if lost, known := ledger.LostOps(write.artifactID); known {
		t.Fatalf("LostOps = %v with a verdict, want none from a judgement that could not read the document within its bound", lost)
	}
	line := logs.Lines("wait for the decode's grant")
	if !strings.Contains(line, context.DeadlineExceeded.Error()) {
		t.Fatalf("the judgement logged %q, want its wait for the decode's grant ended by its bound", line)
	}
	if held := slots.held.Load(); held != loadSlotBytes {
		t.Fatalf("the pool holds %d bytes once the judgement ended, want the holder's %d alone", held, loadSlotBytes)
	}
	t.Logf("the judgement ended %s after it began, against its %s bound, with no verdict: %s", ended.Round(time.Millisecond), publishedLossTimeout, strings.TrimSpace(line))
}
