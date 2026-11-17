package kvwatch

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrRefilling is what Servable answers while the copy cannot be served: no read has completed
// yet, or the copy has not followed the bucket for longer than the stale bound.
var ErrRefilling = errors.New("kvwatch: the cache has no complete read of its bucket recent enough to serve")

// defaultStaleBound is how long a copy that stopped following its bucket is still served. It is
// under half the 5-minute record lifetime of the session bucket, so a copy past it would mostly
// list sessions that already ended; within it the reconnect hook and the listener's 30 s
// self-health rebuild normally complete a read.
const defaultStaleBound = 2 * time.Minute

// boundFor is the stale bound of a stream whose MaxAge is maxAge: staleBound, or half the MaxAge
// when that is shorter, so a copy is never served once most of its records would have expired.
func (w *Watcher) boundFor(maxAge time.Duration) time.Duration {
	if maxAge > 0 {
		return min(w.staleBound, maxAge/2)
	}
	return w.staleBound
}

// Servable reports whether the copy can be served: nil once a read has completed and the copy
// either follows its bucket or stopped following it no longer ago than the stale bound (2 minutes,
// or half the stream's MaxAge when that is shorter), and ErrRefilling otherwise. It reads the
// watcher's state only, never the cache, so a caller may ask it under the cache's mutex.
func (w *Watcher) Servable() error {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.servable()
}

// servable is Servable's reading, under mu. WaitComplete takes it together with the terminal error
// and the stop, in one reading, so a copy whose bucket Check found recreated is never answered
// servable while the view behind it refuses every read.
func (w *Watcher) servable() error {
	if w.completedOnce && (w.following || w.now().Sub(w.staleSince) <= w.bound) {
		return nil
	}
	return ErrRefilling
}

// ErrUnverified is what ReadErr wraps: for longer than the copy's stale bound, no read of the
// bucket has read its own consumer past a barrier (settling), so none completed.
var ErrUnverified = errors.New("kvwatch: no read could read its bucket past a barrier")

// ReadErr reports reads that have not read the bucket past a barrier for the stale bound (2
// minutes, or half the stream's MaxAge when that is shorter), measured from when the first of them
// began waiting: an unbroken run of reads whose wait failed (readAgainUnverified), or, before any
// has failed, the one still waiting. From then on the copy is refused (Servable) because those
// reads do not complete, and only a bucket that takes the barrier and a consumer that reaches it
// repair it. It names the watcher and its bucket, how long the reads have waited and the run's
// last error, and is nil before the bound and once a read completes. A cache that answers for two
// watchers (store.Registry) reports either one's under that one's own name. A watcher whose reads
// fail so keeps running, so Err stays nil; the listener's /healthz reports this instead. A wait is
// bounded by the barrier write's request timeout and then the idle timeout, both well inside the
// stale bound, so a run reaches it only across reads.
func (w *Watcher) ReadErr() error {
	w.mu.RLock()
	defer w.mu.RUnlock()
	since := w.unverifiedSince
	if w.unverifiedRuns == 0 {
		since = w.settleSince
	}
	if since.IsZero() {
		return nil
	}
	waiting := w.now().Sub(since)
	if waiting < w.bound {
		return nil
	}
	if w.unverifiedRuns == 0 {
		return fmt.Errorf("%s (KV bucket %s): %w for %s, past the %s stale bound: the read is still waiting",
			w.name, w.bucket, ErrUnverified, waiting.Round(time.Second), w.bound)
	}
	return fmt.Errorf("%s (KV bucket %s): %w for %s over %d reads, past the %s stale bound: %w",
		w.name, w.bucket, ErrUnverified, waiting.Round(time.Second), w.unverifiedRuns, w.bound, w.unverifiedErr)
}

// WaitComplete blocks until the copy can be served, the state Servable reports, and then returns
// nil: a view that answers no read until then (store.Registry.RolesHeldBy) gates its caller on
// this rather than on WaitReady, which a read whose scan timed out releases too. Being servable is
// not one-way -- a copy that stopped following its bucket is refused again once its stale episode
// outlasts the bound -- so this reads the state again after every wake-up and returns nil only on
// a reading that says servable; a read whose scan timed out is read again by the watcher itself
// until one does. It returns early, rather than waiting ctx out, when no read will complete: the
// current watcher's terminal error (Err), which only a Rewatch clears, and a stop, after which the
// watcher arms nothing. The error is read before servability, as RolesHeldBy reads it, so a
// watcher Check found on a recreated bucket never answers this wait nil while that view still
// refuses every read. Callers bound ctx, whose error it returns when ctx ends first: a bucket can
// be unreachable.
func (w *Watcher) WaitComplete(ctx context.Context) error {
	for {
		w.mu.RLock()
		err, stopped, servable, changed := w.err, w.stopped, w.servable(), w.completeChanged
		w.mu.RUnlock()
		switch {
		case err != nil:
			return err
		case servable == nil:
			return nil
		case stopped:
			return fmt.Errorf("%s watcher stopped before a read of its bucket completed", w.name)
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// wakeComplete wakes every WaitComplete waiter, which then reads the state the caller changed.
// The caller holds mu for writing, so a waiter that took the channel before this reading of the
// state is woken, and one that takes it after reads the new state with the channel that replaces
// the closed one.
func (w *Watcher) wakeComplete() {
	close(w.completeChanged)
	w.completeChanged = make(chan struct{})
}

// Following reports whether the copy follows its bucket: a read has completed since the copy last
// stopped following (a watch installing a new read, the current watcher's own end, a deleted or
// recreated bucket a read of its stream found, a recreate Check found, or a disconnect).
func (w *Watcher) Following() bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.following
}

// StaleEpisode reports, in one reading, when the copy's current stale episode began and whether
// the copy can be served (Servable). The episode begins at the first stop since the read that
// last completed (stopFollowing) and runs across any number of reads that complete nothing, a
// disconnect during a read's completion included, until a read restores following. The time is
// zero while the copy follows its bucket. It is measured on the watcher's clock (WithNow). A cache
// that judges its rows by their age reads them at that moment while the copy is served unfollowed,
// since it cannot see the records that renewed them then, and at its own now otherwise.
func (w *Watcher) StaleEpisode() (since time.Time, servable bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	servable = w.servable() == nil
	if w.following {
		return time.Time{}, servable
	}
	return w.staleSince, servable
}

// Disconnected says the connection the watcher reads through was lost. nats.go reconnects forever
// and keeps the watcher's subscription open through the outage, so the watcher does not end and
// nothing else would say its copy stopped following the bucket: the listener calls this from the
// bus's disconnect hook (bus.Client.AddDisconnectHook). The copy stops following, its stale episode
// begins unless one is under way, a read under way completes nothing, and the scheduled read is
// disarmed. The reconnect hook's Rewatch then reads the bucket again, and that read's completion
// follows it once more and arms the next scheduled read.
func (w *Watcher) Disconnected() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped {
		return
	}
	w.disconnects++
	w.stopFollowing()
	// The reconnect hook's Rewatch reads the bucket once the connection is back, and a scheduled
	// read fired during the outage would only fail, and be armed again, until it is.
	w.clearScheduledArm()
}

// NeedsRead reports whether the copy does not follow its bucket and no read is under way to make
// it: the current generation's scan has ended or never began, and no re-read (of a timed-out scan,
// or of a recreated bucket a read of its stream found) is armed. Such a copy is repaired by nothing
// but a Rewatch, for instance when the reconnect hook's Rewatch failed while the old watcher
// survived, so the listener's self-health check reads it as a watcher to rebuild. A stopped watcher
// needs nothing.
func (w *Watcher) NeedsRead() bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return !w.stopped && !w.following && !w.reading && w.rereadTimer == nil
}

// stopFollowing records that the copy no longer follows its bucket, under mu: the read under way,
// if any, can no longer restore following, and the stale episode begins unless one already has, so
// one episode runs from the first stop to the next completed read across any number of failed
// reads.
func (w *Watcher) stopFollowing() {
	w.following = false
	w.reading = false
	w.markStale()
}

// markStale begins the stale episode unless one is under way, under mu.
func (w *Watcher) markStale() {
	if w.staleSince.IsZero() {
		w.staleSince = w.now()
	}
}
