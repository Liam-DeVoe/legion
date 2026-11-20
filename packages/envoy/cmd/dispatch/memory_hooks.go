package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/reearth/ygo/crdt"
	"github.com/reearth/ygo/persistence"

	"github.com/sjawhar/envoy/internal/dispatch/docs"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// The test hooks of the memory group's test (agent_memory_group_test.go): an agent that allocates
// at full speed (DISPATCH_TEST_AGENT_GROW_AFTER) beside a server that holds its heap near its peak
// (DISPATCH_TEST_SERVER_HEAP). The server takes each only under DISPATCH_TEST_HOOKS=1, and hands
// the first on to its agent; a deployment sets neither.

// heldChunkBytes is the size of each allocation the hooks make: a large object, which the runtime
// takes from the system untouched, so each of its pages is resident only once the hook writes it.
const heldChunkBytes = 4 << 20

// memoryPageSize is the size of the system's memory pages, the step at which a hook writes a chunk.
var memoryPageSize = os.Getpagesize()

// heldHeap is every chunk the hooks allocated, reachable until the process exits, so the collector
// frees none of it.
var heldHeap struct {
	sync.Mutex
	chunks [][]byte
}

// holdChunk allocates size bytes, writes one byte of each of their pages, so the kernel makes
// every page resident, and holds them.
func holdChunk(size int) {
	chunk := make([]byte, size)
	for at := 0; at < size; at += memoryPageSize {
		chunk[at] = 1
	}
	heldHeap.Lock()
	heldHeap.chunks = append(heldHeap.chunks, chunk)
	heldHeap.Unlock()
}

// holdServerHeap holds bytes on the server's heap, each page resident, until the process exits
// (DISPATCH_TEST_SERVER_HEAP).
func holdServerHeap(bytes int64) {
	for left := bytes; left > 0; left -= heldChunkBytes {
		holdChunk(int(min(left, heldChunkBytes)))
	}
	slog.Info("dispatch: holding a heap for a test", "bytes", bytes)
}

// growAgentHeap is the agent's growth (DISPATCH_TEST_AGENT_GROW_AFTER): once after has passed, one
// goroutine for each CPU the agent's runtime schedules on (GOMAXPROCS) allocates chunks as fast as
// it can, each page resident, and holds every one, until the server kills the process for its
// memory. It writes the goroutines' count to stderr, which the server logs.
func growAgentHeap(after time.Duration, stderr io.Writer) {
	procs := runtime.GOMAXPROCS(0)
	fmt.Fprintf(stderr, "agent: DISPATCH_TEST_AGENT_GROW_AFTER: allocating at full speed %v after the start, on %d goroutines (GOMAXPROCS=%d)\n", after, procs, procs)
	go func() {
		time.Sleep(after)
		for range procs {
			go func() {
				for {
					holdChunk(heldChunkBytes)
				}
			}()
		}
	}()
}

// The leaks a test's server plants for the negative control of the comparison of production's
// heap (memory_leaks_test.go): a copy of each closed room's document, held on ygo's heap after the
// room has gone (DISPATCH_TEST_KEEP_CLOSED_ROOMS), and 1 MB stored for each request
// (DISPATCH_TEST_KEEP_REQUEST_MEGABYTES). The server takes each only under DISPATCH_TEST_HOOKS=1;
// a deployment sets neither.

// plantLeaks wraps handler, the whole handler of the server's HTTP port, in the leaks boot turns
// on, the closed rooms' documents read from database; with neither on it is handler itself.
func plantLeaks(handler http.Handler, boot bootConfig, database *store.Store) http.Handler {
	if boot.KeepClosedRooms {
		handler = keepClosedRoomDocuments(handler, docs.NewPgVersioned(database).Load)
	}
	if boot.KeepRequestMegabytes {
		handler = keepRequestMegabytes(handler)
	}
	return handler
}

// roomStateLoad reads a room's stored state as a room's load reads it (docs.PgVersioned.Load).
type roomStateLoad func(ctx context.Context, room string) (persistence.LoadResult, error)

// closedRoomDocuments is every document keepClosedRoomDocuments kept, reachable until the process
// exits, so the collector frees none of them.
var closedRoomDocuments struct {
	sync.Mutex
	documents []*crdt.Doc
}

// keepClosedRoomDocuments keeps the document of each room a document socket (GET /ws/doc/{room})
// leaves. Once the socket's handler has returned, which it does when the socket closes, it decodes
// the room's stored state with ygo, as a room's load decodes it, into a document of its own
// (keepRoomDocument), and holds that document until the process exits. The room itself goes after
// its idle minute, as every room does, and dispatch_rooms' resident_bytes stops counting it, while
// its copy keeps as much of ygo's heap as the room's document held.
func keepClosedRoomDocuments(next http.Handler, load roomStateLoad) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
		if room, ok := strings.CutPrefix(r.URL.Path, "/ws/doc/"); ok && room != "" && r.Method == http.MethodGet {
			keepRoomDocument(room, load)
		}
	})
}

// keepRoomDocument decodes room's stored state into a new document and adds it to
// closedRoomDocuments. A state it cannot read or decode is logged, and nothing is kept.
func keepRoomDocument(room string, load roomStateLoad) {
	ctx, cancel := context.WithTimeout(store.WithTransactionTracking(context.Background()), time.Minute)
	defer cancel()
	stored, err := load(ctx, room)
	document := crdt.New()
	if err == nil && len(stored.Update) > 0 {
		err = crdt.ApplyUpdateV1(document, stored.Update, nil)
	}
	if err != nil {
		slog.Warn("dispatch: keep a closed room's document for a test", "room", room, "error", err)
		return
	}
	closedRoomDocuments.Lock()
	closedRoomDocuments.documents = append(closedRoomDocuments.documents, document)
	kept := len(closedRoomDocuments.documents)
	closedRoomDocuments.Unlock()
	slog.Info("dispatch: keeping a closed room's document for a test", "room", room, "kept", kept)
}

// requestMegabyte is the bytes keepRequestMegabytes stores for each request: 1 MB.
const requestMegabyte = 1_000_000

// requestMegabytes is the package-level map keepRequestMegabytes stores into, by each request's
// number. Nothing prunes it.
var requestMegabytes struct {
	sync.Mutex
	stored map[int][]byte
}

// keepRequestMegabytes stores 1 MB for each request the server's HTTP port serves
// (storeRequestMegabyte), and then serves the request.
func keepRequestMegabytes(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		storeRequestMegabyte()
		next.ServeHTTP(w, r)
	})
}

// storeRequestMegabyte allocates requestMegabyte bytes and stores them in requestMegabytes under
// the next number. A heap profile counts those bytes as this function's own (its flat heap).
func storeRequestMegabyte() {
	megabyte := make([]byte, requestMegabyte)
	requestMegabytes.Lock()
	defer requestMegabytes.Unlock()
	if requestMegabytes.stored == nil {
		requestMegabytes.stored = make(map[int][]byte)
	}
	requestMegabytes.stored[len(requestMegabytes.stored)] = megabyte
}
