package launcher

import (
	"context"
	"sync"
	"time"

	"github.com/sjawhar/legion/daemon/internal/ompsessions"
)

// witnessInterval is how often a store witness connects again while the store fails it.
const witnessInterval = time.Second

// storeWitness is the launcher's own record of the session database a child dials itself where no
// proxy of the generation's sees the child's connections: a URL file's store. The store the pinned
// Oh My Pi ends on is the one its first open meets; once open, it rides out a store that stops
// answering and dials again on its next write. So the witness connects as the child does when the
// child starts, and again once a second while the store fails it, until the store answers or the
// child exits, and keeps the latest failure that is the store's own (ompsessions.StoreFault).
//
// A child that exits with an error after such a failure exited on the store, even when the store
// answers again by the time the launcher asks it (childExitFault). The record holds one
// generation: a store that failed the witness while the child's own open got through, under a
// child that then failed on its own, reads as the store's once, and the next generation's witness,
// which starts with no record, charges that child.
type storeWitness struct {
	store  sessionDial
	stop   chan struct{}
	done   chan struct{}
	cancel context.CancelFunc

	mu    sync.Mutex
	fault error
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
	for {
		conn, err := w.store.connect(ctx)
		closeSessions(conn)
		if ctx.Err() != nil || !ompsessions.StoreFault(err) {
			return
		}
		w.mu.Lock()
		w.fault = err
		w.mu.Unlock()
		select {
		case <-w.stop:
			return
		case <-time.After(witnessInterval):
		}
	}
}

// finish ends the witness at the child's exit and answers the latest store fault it met, nil for
// none. It makes no connection after, and waits within bound for one already under way, as the
// launcher's own ask after the exit is bounded, before it abandons that connection. A nil witness
// answers nil.
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
	return w.fault
}
