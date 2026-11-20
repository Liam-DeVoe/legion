package docs

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// backgroundWaitBound bounds one wait of these tests for the service's background writes: the
// writers they hold end within a second, and a wait that does not return by then has hung.
const backgroundWaitBound = time.Minute

// The wait answers, for a document with rows after its first, the moment its oldest such row
// leaves compactTail, as the compaction queue's own check reads it (checkCompaction), whether ygo
// holds the document's room or has closed it: no compaction under the tail rule can fold before.
func TestTheBackgroundWaitAnswersWhenTheCompactionCheckLetsADocumentFold(t *testing.T) {
	for _, resident := range []bool{true, false} {
		name := "a resident room"
		if !resident {
			name = "a room ygo closed"
		}
		t.Run(name, func(t *testing.T) {
			service, artifactID := newTestService(t)
			writeRoomRows(t, service, artifactID)
			// Every row a minute old, short of the tail, and the first older than the rest, as
			// when the rows after it came later.
			ageUpdatesThrough(t, service.store, artifactID, everyVersion, time.Minute)
			ageUpdatesThrough(t, service.store, artifactID, storedRows(t, service.store, artifactID)[0].version, 90*time.Second)
			ctx, cancel := context.WithTimeout(context.Background(), backgroundWaitBound)
			defer cancel()
			if !resident {
				if err := service.Evict(ctx, artifactID); err != nil {
					t.Fatalf("close the document's room: %v", err)
				}
				if service.srv.GetDoc(artifactID) != nil {
					t.Fatal("UNKNOWN: the document's room is still resident, so this run shows nothing")
				}
			}
			quiet, err := service.AwaitBackgroundWrites(ctx, time.Now())
			if err != nil {
				t.Fatalf("wait for the service's background writes: %v", err)
			}
			check, err := service.checkCompaction(ctx, artifactID)
			if err != nil {
				t.Fatalf("check the document's compaction: %v", err)
			}
			if check.later == 0 || check.aged {
				t.Fatalf("UNKNOWN: the check reads %+v, not rows after the first short of the tail, so this run shows nothing", check)
			}
			// The two reads take the database's clock moments apart; the rows themselves put the
			// moment 60 s out, and the first row 30 s nearer.
			want := time.Now().Add(check.untilAged)
			if gap := want.Sub(quiet.At).Abs(); gap > 5*time.Second || !strings.Contains(quiet.Writer, artifactID) {
				t.Fatalf("the wait answers %s (%s), want the compaction of %s at %s, when the check lets one fold",
					quiet.At.Format(time.RFC3339Nano), quiet.Writer, artifactID, want.Format(time.RFC3339Nano))
			}
		})
	}
}

// A document whose rows a compaction may fold already - its room's close, the queue's retry or a
// forced eviction may run one at any moment - the wait compacts itself, so none of them is left
// anything to fold until the next row comes of age, which is the moment it answers.
func TestTheBackgroundWaitCompactsADocumentACompactionMayFoldNow(t *testing.T) {
	service, artifactID := newTestService(t)
	foldPoint := writeRoomRows(t, service, artifactID)
	ageUpdatesThrough(t, service.store, artifactID, foldPoint, time.Hour)
	before := storedRows(t, service.store, artifactID)
	ctx, cancel := context.WithTimeout(context.Background(), backgroundWaitBound)
	defer cancel()
	if check, err := service.checkCompaction(ctx, artifactID); err != nil || !check.aged {
		t.Fatalf("UNKNOWN: the check reads %+v (%v), not rows a compaction may fold, so this run shows nothing", check, err)
	}
	quiet, err := service.AwaitBackgroundWrites(ctx, time.Now())
	if err != nil {
		t.Fatalf("wait for the service's background writes: %v", err)
	}
	if after := storedRows(t, service.store, artifactID); len(after) >= len(before) {
		t.Fatalf("the document stores %d rows after the wait, as many as the %d before it: the wait left the rows a compaction may fold", len(after), len(before))
	}
	if check, err := service.checkCompaction(ctx, artifactID); err != nil || check.aged {
		t.Fatalf("after the wait the check reads %+v (%v), want no row a compaction may fold", check, err)
	}
	if !quiet.Waited || !quiet.At.After(time.Now()) {
		t.Fatalf("the wait answers waited=%v at %s (%s), want it to have compacted and to answer the next row's coming of age",
			quiet.Waited, quiet.At.Format(time.RFC3339Nano), quiet.Writer)
	}
}

// A look for the service's background writes reads the registers a writer stands in before the
// counts it keeps while it runs, and takes a second look before the wait goes on, so a writer that
// passes from one to the other while a look reads is seen: one that starts and enters a count once
// the registers are read, and one that enters a register once they are read.
func TestTheBackgroundWaitSeesAWriterThatMovesBetweenItsReads(t *testing.T) {
	for _, test := range []struct {
		name  string
		start func(service *Service, room string) (end func())
	}{
		{"a settlement callback that drops its timer", func(service *Service, _ string) func() {
			timerID := service.nextSettleTimer.Add(1)
			service.registerSettleTimer(timerID, "between-reads", time.Now())
			service.settleCallbacks.Add(1)
			service.unregisterSettleTimer(timerID)
			return func() { service.settleCallbacks.Add(-1) }
		}},
		{"a forced eviction an append starts", func(service *Service, _ string) func() {
			service.evicting.Add(1)
			return func() { service.evicting.Add(-1) }
		}},
		{"a room update a socket leaves unstored", func(service *Service, room string) func() {
			state := service.lockState(room)
			state.pendingUpdates++
			service.unlockState(room, state)
			return func() {
				state := service.lockState(room)
				state.pendingUpdates--
				service.countsFellLocked(room, state)
				service.unlockState(room, state)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, artifactID := newTestService(t)
			const runs = 300 * time.Millisecond
			var once sync.Once
			ended := make(chan time.Time, 1)
			service.betweenBackgroundReads = func() {
				once.Do(func() {
					end := test.start(service, artifactID)
					time.AfterFunc(runs, func() {
						end()
						ended <- time.Now()
					})
				})
			}
			ctx, cancel := context.WithTimeout(context.Background(), backgroundWaitBound)
			defer cancel()
			quiet, err := service.AwaitBackgroundWrites(ctx, time.Now().Add(time.Second))
			returned := time.Now()
			if err != nil {
				t.Fatalf("wait for the service's background writes: %v", err)
			}
			select {
			case at := <-ended:
				if returned.Before(at) || !quiet.Waited {
					t.Fatalf("the wait returned at %s (waited=%v), before the writer ended at %s", returned.Format(time.RFC3339Nano), quiet.Waited, at.Format(time.RFC3339Nano))
				}
			default:
				t.Fatalf("the wait returned (waited=%v) while the writer that moved between its reads still ran", quiet.Waited)
			}
		})
	}
}

// A document socket writes on its own goroutines - its peer's updates and their parked bytes, its
// last browser's settlement and the retirement of the room that settlement loaded - so the wait
// waits for it to end, teardown included, and fails naming it while a test keeps it open.
func TestTheBackgroundWaitWaitsOutADocumentSocket(t *testing.T) {
	service, artifactID := newTestService(t)
	seedServiceText(t, service, artifactID, "before")
	httpServer := httptest.NewServer(http.HandlerFunc(service.ServeHTTP))
	t.Cleanup(httpServer.Close)
	connection, err := dialDocument(httpServer.URL, artifactID)
	if err != nil {
		t.Fatalf("open the document's socket: %v", err)
	}
	open, cancelOpen := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancelOpen()
	if _, err := service.AwaitBackgroundWrites(open, time.Now().Add(time.Second)); err == nil || !strings.Contains(err.Error(), "a document socket") {
		t.Fatalf("the wait beside an open document socket answered %v, want it to fail at its bound naming the socket", err)
	}
	leaveDocument(connection)
	ctx, cancel := context.WithTimeout(context.Background(), backgroundWaitBound)
	defer cancel()
	if _, err := service.AwaitBackgroundWrites(ctx, time.Now().Add(time.Second)); err != nil {
		t.Fatalf("wait for the service's background writes once the socket closed: %v", err)
	}
	if served := service.serving.Load(); served != 0 {
		t.Fatalf("the wait returned while %d document sockets were still served", served)
	}
}

// A look for the service's background writes takes the service's locks, and a holder that stalls
// keeps one past any bound: the wait fails at its own bound, naming the look, rather than waiting
// for the lock.
func TestTheBackgroundWaitFailsAtItsBoundWhenALockOutlastsIt(t *testing.T) {
	for _, test := range []struct {
		name string
		hold func(service *Service, room string) (release func())
	}{
		{"the settlement timers' lock", func(service *Service, _ string) func() {
			service.timerMu.Lock()
			return service.timerMu.Unlock
		}},
		{"a room's lock", func(service *Service, room string) func() {
			state := service.lockState(room)
			return func() { service.unlockState(room, state) }
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, artifactID := newTestService(t)
			seedServiceText(t, service, artifactID, "held")
			release := sync.OnceFunc(test.hold(service, artifactID))
			defer release()
			// The lock goes after heldFor whatever the wait does, so a wait that blocks on it ends.
			const heldFor = 3 * time.Second
			time.AfterFunc(heldFor, release)
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			began := time.Now()
			_, err := service.AwaitBackgroundWrites(ctx, time.Now().Add(time.Second))
			took := time.Since(began)
			if !errors.Is(err, context.DeadlineExceeded) || took >= heldFor {
				t.Fatalf("the wait with %s held answered %v after %s, want its bound's end before the lock's %s", test.name, err, took, heldFor)
			}
		})
	}
}
