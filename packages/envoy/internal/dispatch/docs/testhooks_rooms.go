package docs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"
)

// The test hooks let a test of several server processes (cmd/dispatch's multiprocess suite) see
// and steer what one process's rooms do: which rooms it holds, how often a room failed, compacted
// waiting for the document lock or was closed by the service, what another process's
// notifications carried to its rooms, when the settlement resumption armed a document's
// settlement, a room's persistence held back, and an agent's open write parked at a named point.
// A service built without Deps.TestHooks holds none of this: s.hooks is nil, every seam below is a
// nil check, and every exported hook answers ErrTestHooksOff.

var (
	// ErrTestHooksOff refuses a test hook on a service built without Deps.TestHooks.
	ErrTestHooksOff = errors.New("document test hooks are off")
	// ErrLiveWritePausePoint refuses a pause at a point a live write has no seam for.
	ErrLiveWritePausePoint = errors.New("unknown live write pause point")
	// ErrLiveWritePauseArmed refuses a pause armed for a document and point already armed.
	ErrLiveWritePauseArmed = errors.New("a live write pause is already armed for this document and point")
	// ErrLiveWritePauseNotArmed refuses a wait for, or a release of, a pause nobody armed.
	ErrLiveWritePauseNotArmed = errors.New("no live write pause is armed for this document and point")
)

// LiveWritePausePoint names where an open live write of a document parks for a test.
type LiveWritePausePoint string

const (
	// PauseAfterFirstAppend parks a write right after its transaction's first append to the
	// document, while that transaction holds the document's advisory lock (applyLive).
	PauseAfterFirstAppend LiveWritePausePoint = "after-first-append"
	// PauseAfterCommit parks a write once its transaction has committed, before its publish
	// applies it to the room (publishLiveWrite).
	PauseAfterCommit LiveWritePausePoint = "after-commit"
)

const (
	// MaxAwarenessClientsPerSocketForTest and MaxAwarenessClientsPerRoomForTest are the awareness
	// client bounds of a document socket (maxAwarenessClientsPerSocket, awareness_limit.go) and
	// of a room (maxAwarenessClientsPerRoom, awareness_relay.go), for the multiprocess suite: it
	// runs the built binaries, so it reads neither constant, and it reads what a room holds
	// through RoomCounters.AwarenessClients.
	MaxAwarenessClientsPerSocketForTest = maxAwarenessClientsPerSocket
	MaxAwarenessClientsPerRoomForTest   = maxAwarenessClientsPerRoom
)

// RoomCounters is what one process did with one document's rooms and owed settlement since the
// process started, and what its room of the document holds now (AwarenessClients).
type RoomCounters struct {
	// RoomFailures counts the times a room of the document failed (failRoomLocked).
	RoomFailures int64 `json:"room_failures"`
	// LockWaitingCompactions counts the compactions of a failed room, the one compaction that
	// waits for the document's lock rather than leaving a busy room for later
	// (servicePersistenceAdapter.Compact).
	LockWaitingCompactions int64 `json:"lock_waiting_compactions"`
	// RoomCloses counts the rooms of the document the service closed (roomServer.closeHeld): a
	// room whose stored history was replaced, a room of an issue that closed or reopened, a failed
	// room's eviction, a room live at shutdown. ygo's retirement of an idle room is not the
	// service's close and is not counted.
	RoomCloses int64 `json:"room_closes"`
	// NotifiedHead is the highest version of the document this process's room held every update
	// through (roomCursor.heldThrough) after a catch-up that another process's notification asked
	// for (roomCatchUps.headNotified, changed) brought the room to the stored head or found it
	// there - that catch-up itself, or its retry after a try that reached no verdict; 0 before any.
	// A catch-up the sweep after a LISTEN asked for raises SweptHead instead, and a room instance's
	// start (schedule), the retry of a catch-up neither asked for, and a catch-up asked again after
	// a replaced history the locked read did not confirm (closeReplaced) raise neither.
	NotifiedHead int64 `json:"notified_head"`
	// SweptHead is NotifiedHead for the sweep each LISTEN that stands runs (roomCatchUps.sweep):
	// what carries a change whose notification was sent while this process's listener had no
	// connection, and so reached nobody here.
	SweptHead int64 `json:"swept_head"`
	// ListenStands counts the LISTENs this process's listener has stood up, each followed by the
	// sweep (roomSync.connected): the first as room sync starts, and one more for each redial. It
	// is the process's count, the same whatever the document.
	ListenStands int64 `json:"listen_stands"`
	// NotifiedIssueCloses counts the notifications that the document's issue closed or reopened
	// which this process's issue-open lane applied to the document (issueOpenChanged), each of
	// which closes its room of the document when one is resident here (setRoomIssueOpen). The
	// recheck after a LISTEN (recheckResidentIssues) and the append gate's close of a room whose
	// append it refused (refuseClosedIssueAppend) are not counted.
	NotifiedIssueCloses int64 `json:"notified_issue_closes"`
	// RelayedAwareness is, per awareness client, what this process's rooms of the document took
	// of that client from another process's notification (awarenessRelay.received, touched): a
	// state each time a room added or updated the client, a removal each time it took the client
	// away. An entry a room dropped as stale changed nothing and is not counted, and nor is a
	// change a room made itself - a local peer's frame or departure, or an expiry
	// (awarenessExpiry), which removes a relayed client that no notification removed. It is empty,
	// never nil, in what RoomCounters answers.
	RelayedAwareness map[uint64]RelayedAwarenessChanges `json:"relayed_awareness"`
	// SettlementResumptions holds the time of each settlement-resumption pass of this process that
	// armed the document's owed settlement (resumeOwedSettlements), on the service's clock, oldest
	// first.
	SettlementResumptions []time.Time `json:"settlement_resumptions"`
	// AwarenessClients is how many client entries this process's room of the document holds now
	// - the clients present and the tombstones of the ones that left, whoever sent them - which
	// is what maxAwarenessClientsPerRoom bounds. It is 0 when no room of the document is
	// resident here. Unlike the counters above it is a gauge, read as the hook answers.
	AwarenessClients int `json:"awareness_clients"`
}

// RelayedAwarenessChanges is what a process's rooms of one document took of one awareness client
// from other processes' notifications (RoomCounters.RelayedAwareness).
type RelayedAwarenessChanges struct {
	// States counts the times a room added the client or took a later state of it.
	States int64 `json:"states"`
	// Removals counts the times a room took the client away.
	Removals int64 `json:"removals"`
}

// testHooks is the test hooks' state, on a service built with Deps.TestHooks.
type testHooks struct {
	mu       sync.Mutex
	counters map[string]*RoomCounters
	// listenStands is RoomCounters.ListenStands, the process's own count.
	listenStands int64
	// holds is, per document, the time until which its rooms' appends of browser updates wait.
	holds  map[string]time.Time
	pauses map[livePauseKey]*livePause
}

type livePauseKey struct {
	artifactID string
	point      LiveWritePausePoint
}

// livePause is one armed pause: the first write to reach its point parks there (parked closes)
// until the test releases it (released closes).
type livePause struct {
	taken    bool
	parked   chan struct{}
	released chan struct{}
}

func newTestHooks() *testHooks {
	return &testHooks{
		counters: make(map[string]*RoomCounters),
		holds:    make(map[string]time.Time),
		pauses:   make(map[livePauseKey]*livePause),
	}
}

// ResidentRooms lists the documents this process holds a room of now, sorted.
func (s *Service) ResidentRooms() []string {
	rooms := s.srv.Rooms()
	slices.Sort(rooms)
	return rooms
}

// RoomCounters reads what this process did with artifactID's rooms and owed settlement since it
// started, and how many awareness client entries its room of the document holds now. Its
// RelayedAwareness and SettlementResumptions are copies, empty rather than nil when none was
// counted.
func (s *Service) RoomCounters(artifactID string) (RoomCounters, error) {
	h := s.hooks
	if h == nil {
		return RoomCounters{}, ErrTestHooksOff
	}
	h.mu.Lock()
	var counted RoomCounters
	if counters := h.counters[artifactID]; counters != nil {
		counted = *counters
	}
	counted.ListenStands = h.listenStands
	relayed := make(map[uint64]RelayedAwarenessChanges, len(counted.RelayedAwareness))
	maps.Copy(relayed, counted.RelayedAwareness)
	counted.RelayedAwareness = relayed
	h.mu.Unlock()
	counted.SettlementResumptions = append([]time.Time{}, counted.SettlementResumptions...)
	clients, err := s.heldAwarenessClients(artifactID)
	if err != nil {
		return RoomCounters{}, err
	}
	counted.AwarenessClients = clients
	return counted, nil
}

// heldAwarenessClients is how many client entries this process's room of artifactID holds now,
// the tombstones of the clients that left included: the clients a full awareness update of the
// room encodes. A document with no room here holds none.
func (s *Service) heldAwarenessClients(artifactID string) (int, error) {
	aw, resident := s.srv.GetAwareness(artifactID)
	if !resident {
		return 0, nil
	}
	entries, err := decodeAwarenessEntries(aw.EncodeUpdate(nil))
	if err != nil {
		return 0, fmt.Errorf("read the awareness of the room of %s: %w", artifactID, err)
	}
	return len(entries), nil
}

// HoldRoomPersistence makes every append of a browser update from this process's rooms of
// artifactID that starts within duration from now wait until duration has passed. The room
// applies and broadcasts each such update meanwhile; only its store waits.
func (s *Service) HoldRoomPersistence(artifactID string, duration time.Duration) error {
	h := s.hooks
	if h == nil {
		return ErrTestHooksOff
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.holds[artifactID] = time.Now().Add(duration)
	return nil
}

// ArmLiveWritePause makes the next live write of artifactID to reach point park there until
// ReleaseLiveWritePause. AwaitLiveWriteParked says when it has.
func (s *Service) ArmLiveWritePause(artifactID string, point LiveWritePausePoint) error {
	h := s.hooks
	if h == nil {
		return ErrTestHooksOff
	}
	if point != PauseAfterFirstAppend && point != PauseAfterCommit {
		return ErrLiveWritePausePoint
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	key := livePauseKey{artifactID: artifactID, point: point}
	if h.pauses[key] != nil {
		return ErrLiveWritePauseArmed
	}
	h.pauses[key] = &livePause{parked: make(chan struct{}), released: make(chan struct{})}
	return nil
}

// AwaitLiveWriteParked waits until a write has parked at the pause armed for artifactID and
// point, or ctx ends.
func (s *Service) AwaitLiveWriteParked(ctx context.Context, artifactID string, point LiveWritePausePoint) error {
	h := s.hooks
	if h == nil {
		return ErrTestHooksOff
	}
	h.mu.Lock()
	pause := h.pauses[livePauseKey{artifactID: artifactID, point: point}]
	h.mu.Unlock()
	if pause == nil {
		return ErrLiveWritePauseNotArmed
	}
	select {
	case <-pause.parked:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ReleaseLiveWritePause disarms the pause armed for artifactID and point and lets the write
// parked there, if one is, go on. It reports whether a write had parked.
func (s *Service) ReleaseLiveWritePause(artifactID string, point LiveWritePausePoint) (bool, error) {
	h := s.hooks
	if h == nil {
		return false, ErrTestHooksOff
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	key := livePauseKey{artifactID: artifactID, point: point}
	pause := h.pauses[key]
	if pause == nil {
		return false, ErrLiveWritePauseNotArmed
	}
	delete(h.pauses, key)
	close(pause.released)
	return pause.taken, nil
}

// countRoomFailure counts a room of artifactID failing.
func (h *testHooks) countRoomFailure(artifactID string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.countersLocked(artifactID).RoomFailures++
}

// countLockWaitingCompaction counts a compaction of a room of artifactID that waits for the
// document's lock.
func (h *testHooks) countLockWaitingCompaction(artifactID string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.countersLocked(artifactID).LockWaitingCompactions++
}

// countRoomClose counts a close of a room of artifactID the service made.
func (h *testHooks) countRoomClose(artifactID string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.countersLocked(artifactID).RoomCloses++
}

// recordCaughtUpHead records that a catch-up of a room of artifactID that cause asked for left
// the room holding every update through head (RoomCounters.NotifiedHead, SweptHead).
func (h *testHooks) recordCaughtUpHead(artifactID string, cause catchUpCause, head int64) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	counters := h.countersLocked(artifactID)
	switch cause {
	case catchUpNotified:
		counters.NotifiedHead = max(counters.NotifiedHead, head)
	case catchUpSwept:
		counters.SweptHead = max(counters.SweptHead, head)
	}
}

// countListenStand counts a LISTEN this process's listener stood up (RoomCounters.ListenStands).
func (h *testHooks) countListenStand() {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.listenStands++
}

// countNotifiedIssueClose counts a notification that artifactID's issue closed or reopened which
// the issue-open lane applied to it (RoomCounters.NotifiedIssueCloses).
func (h *testHooks) countNotifiedIssueClose(artifactID string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.countersLocked(artifactID).NotifiedIssueCloses++
}

// countRelayedAwareness counts a change of client that another process's notification made to a
// room of artifactID: its removal, or else a state (RoomCounters.RelayedAwareness).
func (h *testHooks) countRelayedAwareness(artifactID string, client uint64, removed bool) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	counters := h.countersLocked(artifactID)
	if counters.RelayedAwareness == nil {
		counters.RelayedAwareness = make(map[uint64]RelayedAwarenessChanges)
	}
	changes := counters.RelayedAwareness[client]
	if removed {
		changes.Removals++
	} else {
		changes.States++
	}
	counters.RelayedAwareness[client] = changes
}

// countSettlementResumption records that a settlement-resumption pass armed the owed settlement
// of each of artifactIDs at at.
func (h *testHooks) countSettlementResumption(artifactIDs []string, at time.Time) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, artifactID := range artifactIDs {
		counters := h.countersLocked(artifactID)
		counters.SettlementResumptions = append(counters.SettlementResumptions, at)
	}
}

func (h *testHooks) countersLocked(artifactID string) *RoomCounters {
	counters := h.counters[artifactID]
	if counters == nil {
		counters = &RoomCounters{}
		h.counters[artifactID] = counters
	}
	return counters
}

// awaitPersistenceHold waits out a hold HoldRoomPersistence put on artifactID's appends, or
// until ctx ends.
func (h *testHooks) awaitPersistenceHold(ctx context.Context, artifactID string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	until := h.holds[artifactID]
	h.mu.Unlock()
	wait := time.Until(until)
	if wait <= 0 {
		return
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
}

// parkLiveWrite parks the calling write of artifactID at point when a pause is armed there and
// no write has taken it yet, until the test releases it.
func (h *testHooks) parkLiveWrite(artifactID string, point LiveWritePausePoint) {
	if h == nil {
		return
	}
	h.mu.Lock()
	pause := h.pauses[livePauseKey{artifactID: artifactID, point: point}]
	if pause == nil || pause.taken {
		h.mu.Unlock()
		return
	}
	pause.taken = true
	h.mu.Unlock()
	slog.Info("dispatch: test hook parked a live document write", "room", artifactID, "point", string(point))
	close(pause.parked)
	<-pause.released
	slog.Info("dispatch: test hook released a live document write", "room", artifactID, "point", string(point))
}
