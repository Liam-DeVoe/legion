package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"sync"
	"time"
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
