package docs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gws "github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/reearth/ygo/crdt"
	"github.com/reearth/ygo/provider/websocket"

	"github.com/sjawhar/envoy/internal/dispatch/admission"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// The rows here are about a document socket whose admission went through and whose room's load did
// not: ygo answers such a join with its own 500 "room unavailable" (sjawhar/ygo v1.55.1-sami.2,
// provider/websocket/server.go:2088-2096), and the socket answers its own refusal instead when the
// load found a pool busy or the server ended the socket's request while the room loaded. The
// tester's row (TestASocketWhoseRoomLoadFindsTheRoomsPoolBusyIsRefused503) holds the status of the
// first case; these hold the rest of that answer, the joins that wait on a load another caller
// began, and the loads no socket's admission holds.

// A cold socket whose room's load finds the rooms pool busy is refused as the socket's other
// refusals are, 503 with Retry-After and a WARN line naming the room, the pool and the busy pool's
// error, and within one of that pool's acquire bounds of the moment the pool filled: the load reads
// the store once, the head its preload is checked against and the document's pending settlement in
// one statement (takePreload), and a failure of that read is the load's, where a busy pool once ran
// its bound out twice - once for the head and once more for the whole history read again on the
// same pool - before ygo answered 500. The load renders nothing either: the admission's rendering
// is the only one a cold socket makes, refused or not.
func TestARoomLoadThatFindsThePoolBusyRefusesItsSocketWithinOneBound(t *testing.T) {
	logs := captureWarnings(t)
	database := storetest.Open(t)
	service, server := newTestServiceInstance(t, database)
	const bound = 2 * time.Second
	service.store.Pool.SetAcquireBoundForTest(store.RoomsPool, bound)
	artifactID := createDocuments(t, database, 1)[0]
	holds := make(chan heldRoomsPool, 1)
	t.Cleanup(func() { releaseHeldRoomsPool(holds) })
	var renders atomic.Int32
	service.admissionRendered = func(room string) {
		if room != artifactID || renders.Add(1) != 1 {
			return
		}
		holds <- takeTheRoomsPool(t, service)
	}

	answer := <-answerSocketAsync(server.URL, artifactID)
	if answer.err != nil {
		t.Fatal(answer.err)
	}
	var held heldRoomsPool
	select {
	case held = <-holds:
		defer held.release()
	default:
		t.Fatal("the socket's admission never rendered the document, so its room's load met no busy pool")
	}
	took := answer.at.Sub(held.at)
	t.Logf("the socket's handshake was answered %d %q %s after the rooms pool filled", answer.status, answer.body, took.Round(time.Millisecond))
	if answer.status != http.StatusServiceUnavailable {
		t.Fatalf("a socket whose room's load found the rooms pool busy was answered %d %q, want 503", answer.status, answer.body)
	}
	if answer.retryAfter != admission.RetryAfter {
		t.Errorf("the refused socket's Retry-After = %q, want %q, as every other 503 of the server carries", answer.retryAfter, admission.RetryAfter)
	}
	if limit := bound + bound/2; took >= limit {
		t.Errorf("the refusal came %s after the rooms pool filled, want within one acquire bound (%s, so under %s): the load read the busy pool more than once", took.Round(time.Millisecond), bound, limit)
	}
	if got := renders.Load(); got != 1 {
		t.Errorf("the cold socket rendered its document %d times, want once: the admission's rendering", got)
	}
	line := logLineWith(logs.String(), "a document socket was refused")
	if line == "" {
		t.Fatalf("the refused socket logged no WARN line naming why; the log was:\n%s", logs.String())
	}
	for _, want := range []string{"level=WARN", "room=" + artifactID, "pool=" + store.RoomsPool, store.ErrPoolBusy.Error()} {
		if !strings.Contains(line, want) {
			t.Errorf("the refused socket's line %q does not carry %q", line, want)
		}
	}
}

// A cold socket's room load takes one rooms-pool connection: the admission read and decoded the
// document and the issue already, and the load's one read of the store is the head its preload is
// checked against with the document's pending-settlement row beside it (takePreload), which the
// load's hook takes rather than reading the row a second time (onLoadDocument). Each connection
// the load took was one more wait on a pool of four under load, and one more bound a busy pool
// could run out before the socket was answered.
func TestAnAdmittedRoomLoadTakesOneRoomsPoolConnection(t *testing.T) {
	database := storetest.Open(t)
	service, server := newTestServiceInstance(t, database)
	artifactID := createDocuments(t, database, 1)[0]
	var renders atomic.Int32
	var atRender atomic.Uint64
	service.admissionRendered = func(room string) {
		if room != artifactID {
			return
		}
		renders.Add(1)
		atRender.CompareAndSwap(0, roomsPoolAcquires(service))
	}

	admitSocket(t, server.URL, artifactID, "a cold open")
	if got := renders.Load(); got != 1 {
		t.Fatalf("the cold socket rendered its document %d times, want once: its room's load took the admission's preload", got)
	}
	if took := roomsPoolAcquires(service) - atRender.Load(); took != 1 {
		t.Errorf("the cold socket's room load took %d rooms-pool connections after its admission read the document, want one", took)
	}
}

// A socket whose join waits on a load another caller began - a settlement's warm-up, which no
// admission holds - and that load finds the rooms pool busy, is refused 503 as a socket whose own
// load found it so: ygo hands every caller waiting on a load that load's error, and answers the
// socket's join 500 with it, so the socket reads the load's failure where the load recorded it,
// on the room's record the socket's admission holds (noteLoadFailed). The warm-up itself fails as
// it always did, with the busy pool's error.
func TestASocketWaitingOnALoadThatFindsThePoolBusyIsRefused503(t *testing.T) {
	database := storetest.Open(t)
	service, server := newTestServiceInstance(t, database)
	const bound = 2 * time.Second
	service.store.Pool.SetAcquireBoundForTest(store.RoomsPool, bound)
	artifactID := createDocuments(t, database, 1)[0]
	holds := make(chan heldRoomsPool, 1)
	t.Cleanup(func() { releaseHeldRoomsPool(holds) })
	warmed := make(chan error, 1)
	loadBegun := waitForRoomLoad(service, artifactID, roomWait(t))
	var once atomic.Bool
	service.admissionRendered = func(room string) {
		if room != artifactID || once.Swap(true) {
			return
		}
		// The pool fills once the admission has read the document, and the warm-up's load begins
		// before the socket offers what it read, so the socket's join finds that load under way and
		// waits on it rather than loading the room itself.
		holds <- takeTheRoomsPool(t, service)
		go func() { warmed <- warmUpAsASettlementDoes(service, artifactID) }()
		if err := loadBegun(); err != nil {
			t.Error(err)
		}
	}

	answer := <-answerSocketAsync(server.URL, artifactID)
	if answer.err != nil {
		t.Fatal(answer.err)
	}
	if answer.status != http.StatusServiceUnavailable {
		t.Errorf("a socket waiting on a load that found the rooms pool busy was answered %d %q, want 503", answer.status, answer.body)
	}
	if answer.retryAfter != admission.RetryAfter {
		t.Errorf("the refused socket's Retry-After = %q, want %q", answer.retryAfter, admission.RetryAfter)
	}
	select {
	case err := <-warmed:
		if !store.IsPoolBusy(err) {
			t.Errorf("the settlement's warm-up load ended %v, want the busy pool's error, as a load no admission holds always failed", err)
		}
	case <-time.After(roomWait(t)):
		t.Fatal("the settlement's warm-up load never ended")
	}
}

// A socket whose request the server ends while its join waits on the room's load - the heartbeat's
// re-resolution of the caller failed, which under load is a busy pool, or a re-check revoked the
// socket's admission - is refused 503 with that cause logged, as its admission refuses a request the
// server ended under it. ygo answers the join 500 with the context's error; the browser is still
// waiting, and nothing of the room failed.
func TestASocketWhoseRequestTheServerEndsWhileItsRoomLoadsIsRefused503(t *testing.T) {
	logs := captureWarnings(t)
	database := storetest.Open(t)
	service, _ := newTestServiceInstance(t, database)
	// Long enough that the warm-up's load still waits for a connection when the request ends.
	service.store.Pool.SetAcquireBoundForTest(store.RoomsPool, time.Minute)
	artifactID := createDocuments(t, database, 1)[0]
	var endRequest atomic.Pointer[context.CancelCauseFunc]
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithCancelCause(r.Context())
		defer cancel(nil)
		endRequest.Store(&cancel)
		service.ServeHTTP(w, r.WithContext(ctx))
	}))
	t.Cleanup(server.Close)
	holds := make(chan heldRoomsPool, 1)
	t.Cleanup(func() { releaseHeldRoomsPool(holds) })
	loadBegun := waitForRoomLoad(service, artifactID, roomWait(t))
	var once atomic.Bool
	service.admissionRendered = func(room string) {
		if room != artifactID || once.Swap(true) {
			return
		}
		holds <- takeTheRoomsPool(t, service)
		go func() { _ = warmUpAsASettlementDoes(service, artifactID) }()
		if err := loadBegun(); err != nil {
			t.Error(err)
		}
	}

	answers := answerSocketAsync(server.URL, artifactID)
	// ygo authorizes the socket before it joins the room, so a socket registered on the room's
	// record is one whose join is waiting on the warm-up's load.
	deadline := time.Now().Add(roomWait(t))
	for registeredConnections(service) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the socket was never authorized, so its join never waited on the room's load")
		}
		time.Sleep(time.Millisecond)
	}
	cause := fmt.Errorf("re-resolve the socket's caller: %w", store.ErrPoolBusy)
	(*endRequest.Load())(cause)

	answer := <-answers
	if answer.err != nil {
		t.Fatal(answer.err)
	}
	if answer.status != http.StatusServiceUnavailable {
		t.Fatalf("a socket whose request the server ended while its room loaded was answered %d %q, want 503", answer.status, answer.body)
	}
	if answer.retryAfter != admission.RetryAfter {
		t.Errorf("the refused socket's Retry-After = %q, want %q", answer.retryAfter, admission.RetryAfter)
	}
	line := logLineWith(logs.String(), "a document socket was refused")
	if line == "" {
		t.Fatalf("the refused socket logged no WARN line naming why; the log was:\n%s", logs.String())
	}
	for _, want := range []string{"room=" + artifactID, "re-resolve the socket's caller"} {
		if !strings.Contains(line, want) {
			t.Errorf("the refused socket's line %q does not carry %q", line, want)
		}
	}
}

// A load no socket's admission holds - a settlement's warm-up, here, and a committed write's
// publish, which loads the room the same way - reads the rooms pool as it always did: under a pool
// that stays full it fails with the busy pool's error after one acquire bound, the bound of its read
// of the stored history, and leaves no room; once a connection comes free within that bound it
// loads. Neither the merged read of a preload's check nor the socket's refusal is any of its
// concern: it has no preload to check and no socket to answer.
func TestALoadNoAdmissionHoldsFailsAfterOneBoundOrLoadsOnceAConnectionFrees(t *testing.T) {
	database := storetest.Open(t)
	service, _ := newTestServiceInstance(t, database)
	const bound = time.Second
	service.store.Pool.SetAcquireBoundForTest(store.RoomsPool, bound)
	artifactID := createDocuments(t, database, 1)[0]

	release := holdTheRoomsPool(t, service)
	start := time.Now()
	err := warmUpAsASettlementDoes(service, artifactID)
	took := time.Since(start)
	release()
	t.Logf("the warm-up under a full rooms pool ended after %s: %v", took.Round(time.Millisecond), err)
	if !store.IsPoolBusy(err) {
		t.Fatalf("the warm-up under a full rooms pool ended %v, want the busy pool's error", err)
	}
	if took < bound || took >= 2*bound {
		t.Errorf("the warm-up under a full rooms pool failed after %s, want after one acquire bound (%s)", took.Round(time.Millisecond), bound)
	}
	if service.srv.GetDoc(artifactID) != nil {
		t.Fatal("the failed warm-up left its room resident")
	}

	rooms, err := service.store.Pool.Rooms()
	if err != nil {
		t.Fatalf("open the rooms pool: %v", err)
	}
	var held []*pgxpool.Conn
	for range rooms.Stat().MaxConns() {
		conn, err := rooms.Acquire(context.Background())
		if err != nil {
			t.Fatalf("hold a rooms connection: %v", err)
		}
		held = append(held, conn)
	}
	freed := time.AfterFunc(bound/2, held[0].Release)
	defer func() {
		freed.Stop()
		for _, conn := range held[1:] {
			conn.Release()
		}
	}()
	if err := warmUpAsASettlementDoes(service, artifactID); err != nil {
		t.Fatalf("the warm-up with a rooms connection freed within the bound: %v, want the room loaded", err)
	}
	if service.srv.GetDoc(artifactID) == nil {
		t.Fatal("the warm-up returned with no room resident")
	}
}

// heldRoomsPool is every connection of a service's rooms pool, taken by a hook the server runs, and
// the moment the last of them was taken.
type heldRoomsPool struct {
	conns []*pgxpool.Conn
	at    time.Time
}

func (held heldRoomsPool) release() {
	for _, conn := range held.conns {
		conn.Release()
	}
}

// releaseHeldRoomsPool gives back the connections a hook took and the test never received.
func releaseHeldRoomsPool(holds <-chan heldRoomsPool) {
	select {
	case held := <-holds:
		held.release()
	default:
	}
}

// takeTheRoomsPool takes every connection of service's rooms pool. It runs in a hook on the server's
// goroutine, where a failure can only be reported.
func takeTheRoomsPool(t *testing.T, service *Service) heldRoomsPool {
	rooms, err := service.store.Pool.Rooms()
	if err != nil {
		t.Errorf("open the rooms pool: %v", err)
		return heldRoomsPool{at: time.Now()}
	}
	var held heldRoomsPool
	for range rooms.Stat().MaxConns() {
		conn, err := rooms.Acquire(context.Background())
		if err != nil {
			t.Errorf("hold a rooms connection: %v", err)
			break
		}
		held.conns = append(held.conns, conn)
	}
	held.at = time.Now()
	return held
}

// roomsPoolAcquires is how many connections service's rooms pool has handed out since it opened,
// readable from a hook on the server's goroutine (roomsAcquires is the test goroutine's).
func roomsPoolAcquires(service *Service) uint64 {
	for _, pool := range service.store.Pool.Stats() {
		if pool.Name == store.RoomsPool {
			return pool.Acquire.Count
		}
	}
	return 0
}

// waitForRoomLoad returns a wait, for a hook on the server's goroutine, that returns once a load of
// room has begun - its record stands under the room's name (beginRoomLoad) - or within wait with
// why it did not.
func waitForRoomLoad(service *Service, room string, wait time.Duration) func() error {
	return func() error {
		deadline := time.Now().Add(wait)
		for {
			if _, loading := service.roomLoads.Load(room); loading {
				return nil
			}
			if time.Now().After(deadline) {
				return errors.New("the settlement's warm-up never began its load of the room")
			}
			time.Sleep(time.Millisecond)
		}
	}
}

// warmUpAsASettlementDoes loads room as a settlement's warm-up loads the room it is about to settle
// (settleRoom): an Apply that applies nothing, under the service's own mark and on the settlements
// pool, which no socket's admission holds.
func warmUpAsASettlementDoes(service *Service, room string) error {
	settlements, err := service.store.Pool.Settlements()
	if err != nil {
		return err
	}
	ctx := withOwnerVerified(withQueryPool(context.Background(), settlements))
	err = service.srv.Apply(ctx, room, func(*crdt.Doc, func(func(*crdt.Transaction))) {})
	if errors.Is(err, websocket.ErrNoChanges) {
		return nil
	}
	return err
}

// socketAnswer is how a document socket's handshake was answered, when, or why it had no answer.
type socketAnswer struct {
	status     int
	retryAfter string
	body       string
	at         time.Time
	err        error
}

// answerSocketAsync dials id's document socket as alice's browser does, on a goroutine of its own,
// and answers on the channel it returns with how the handshake was answered. A socket it upgraded
// is closed at once.
func answerSocketAsync(serverURL, id string) <-chan socketAnswer {
	answers := make(chan socketAnswer, 1)
	go func() {
		dialer := gws.Dialer{HandshakeTimeout: time.Minute}
		connection, response, err := dialer.Dial(socketURL(serverURL, id), http.Header{"X-Dispatch-User": []string{"alice"}})
		at := time.Now()
		if connection != nil {
			_ = connection.Close()
		}
		if response == nil {
			answers <- socketAnswer{at: at, err: fmt.Errorf("the socket's handshake had no answer: %w", err)}
			return
		}
		body, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		answers <- socketAnswer{
			status:     response.StatusCode,
			retryAfter: response.Header.Get("Retry-After"),
			body:       string(body),
			at:         at,
		}
	}()
	return answers
}

// logLineWith is the first line of logged that holds marker, or "" when none does.
func logLineWith(logged, marker string) string {
	for _, line := range strings.Split(logged, "\n") {
		if strings.Contains(line, marker) {
			return line
		}
	}
	return ""
}
