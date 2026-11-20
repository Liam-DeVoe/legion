package launcher

import (
	"context"
	"sync"
	"time"

	"github.com/sjawhar/legion/daemon/internal/ompsessions"
)

// witnessInterval is how often a store witness looks at the store, from the child's start to its
// exit; a test lengthens it.
var witnessInterval = time.Second

// storeWitness is the launcher's own look at the session database a child dials itself where no
// proxy of the generation's sees the child's connections: a URL file's store. It connects as the
// child does when the child starts, and again once every witnessInterval until the child exits,
// whatever the store answered, and keeps what its latest look met: the store's own fault
// (ompsessions.StoreFault), or nothing for a store that answered, a refusal of its own included.
//
// A child that exits with an error while that latest look met a fault exited on the store
// (childExitFault): the fault overlaps the exit, since no look found the store answering again
// before it. A fault that a later look found over is not the exit's: the store answered in between,
// so a child that fails after that fails on its own and is charged, however recently the store
// failed. What no look sees is a store that fails between two looks, at the child's own open, and
// answers again before the launcher's ask after the exit. Where the child's own report says that
// open's connection failed before the server answered it, the report decides (childReport); where
// the server answered it, with its own state (57P03, 53300), neither the report nor a look can say
// so, and that exit reads as the child's own (docs/kubernetes.md). The cost is one connection to
// the store each witnessInterval for as long as the child runs.
type storeWitness struct {
	store  sessionDial
	stop   chan struct{}
	done   chan struct{}
	cancel context.CancelFunc

	mu     sync.Mutex
	latest error
}

// watchStore starts a witness of store: nil for a child that dials no store itself, or whose
// connections the generation's proxy sees (sessionproxy.Proxy.Fault).
func watchStore(store *sessionDial, proxied bool) *storeWitness {
	if store == nil || proxied {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	w := &storeWitness{store: *store, stop: make(chan struct{}), done: make(chan struct{}), cancel: cancel}
	go w.run(ctx)
	return w
}

func (w *storeWitness) run(ctx context.Context) {
	defer close(w.done)
	next := time.NewTimer(0)
	defer next.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-next.C:
		}
		select {
		case <-w.stop:
			// A stop and a look due at once: the stop wins, so no look starts after the exit.
			return
		default:
		}
		conn, err := w.store.connect(ctx)
		closeSessions(conn)
		if ctx.Err() != nil {
			// finish cut this look short: it says nothing of the store.
			return
		}
		if !ompsessions.StoreFault(err) {
			err = nil
		}
		w.mu.Lock()
		w.latest = err
		w.mu.Unlock()
		next.Reset(witnessInterval)
	}
}

// finish ends the witness at the child's exit and answers the store fault its latest look met, nil
// for a store that latest look found answering, or for no look at all. It starts no look after, and
// waits within bound for one already under way, as the launcher's own ask after the exit is
// bounded, before it abandons that look. A nil witness answers nil.
func (w *storeWitness) finish(bound time.Duration) error {
	if w == nil {
		return nil
	}
	close(w.stop)
	timer := time.NewTimer(max(bound, 0))
	select {
	case <-w.done:
	case <-timer.C:
	}
	timer.Stop()
	w.cancel()
	<-w.done
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.latest
}
