// Package dedupe provides a time-windowed cache for suppressing duplicate
// envelope delivery. The cache is keyed by (DedupeKey, SessionID) so the same
// envelope can be delivered to different sessions independently.
package dedupe

import (
	"sync"
	"time"
)

// Key identifies a unique delivery: same envelope to same session.
type Key struct {
	DedupeKey string
	SessionID string
}

// Cache tracks recently delivered (dedupe_key, session_id) pairs to prevent
// duplicate delivery during NATS retry storms. Thread-safe via sync.RWMutex.
type Cache struct {
	mu      sync.RWMutex
	entries map[Key]entry
	// latest is, for each dedupe key, the newest delivery recorded with a stored copy
	// (RecordStored with a sequence), whichever session it went to (LatestStored).
	latest map[string]stored
	ttl    time.Duration
	clock  func() time.Time
	stop   chan struct{}
}

// entry is one recorded delivery: when it was recorded, and the stream sequence of the copy it
// stored where the recorder named one (RecordStored), 0 where it named none.
type entry struct {
	at       time.Time
	sequence uint64
}

// stored is the newest delivery of one dedupe key that stored a copy: the session it went to,
// when it was recorded, and the copy's stream sequence.
type stored struct {
	sessionID string
	at        time.Time
	sequence  uint64
}

// New creates a dedupe cache with the given TTL and starts a background
// cleanup goroutine that periodically removes expired entries.
func New(ttl time.Duration) *Cache {
	return NewWithClock(ttl, time.Now)
}

// NewWithClock creates a dedupe cache with an injectable clock for testing.
func NewWithClock(ttl time.Duration, clock func() time.Time) *Cache {
	c := &Cache{
		entries: make(map[Key]entry),
		latest:  make(map[string]stored),
		ttl:     ttl,
		clock:   clock,
		stop:    make(chan struct{}),
	}
	go c.cleanup()
	return c
}

// Seen returns true if the (dedupeKey, sessionID) pair was delivered within
// the TTL window. Does not modify state — call Record after successful delivery.
func (c *Cache) Seen(dedupeKey, sessionID string) bool {
	_, ok := c.Stored(dedupeKey, sessionID)
	return ok
}

// Stored reports whether the (dedupeKey, sessionID) pair was delivered within the TTL window, and
// the stream sequence of the copy that delivery stored where RecordStored named one, else 0.
func (c *Cache) Stored(dedupeKey, sessionID string) (uint64, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[Key{DedupeKey: dedupeKey, SessionID: sessionID}]
	if !ok || c.clock().Sub(e.at) > c.ttl {
		return 0, false
	}
	return e.sequence, true
}

// Record marks a (dedupeKey, sessionID) pair as delivered.
// Call only after successful delivery to preserve at-least-once semantics.
func (c *Cache) Record(dedupeKey, sessionID string) {
	c.RecordStored(dedupeKey, sessionID, 0)
}

// RecordStored marks a (dedupeKey, sessionID) pair as delivered by the copy the stream holds at
// sequence, which Stored hands back to a later reader of the pair, and LatestStored to a reader of
// the key alone, where sequence names a copy.
func (c *Cache) RecordStored(dedupeKey, sessionID string, sequence uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.clock()
	c.entries[Key{DedupeKey: dedupeKey, SessionID: sessionID}] = entry{at: now, sequence: sequence}
	if sequence != 0 {
		c.latest[dedupeKey] = stored{sessionID: sessionID, at: now, sequence: sequence}
	}
}

// LatestStored reports the session the newest delivery of dedupeKey recorded with a stored copy
// went to within the TTL window, whichever session that was, and the copy's stream sequence.
func (c *Cache) LatestStored(dedupeKey string) (sessionID string, sequence uint64, ok bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	s, ok := c.latest[dedupeKey]
	if !ok || c.clock().Sub(s.at) > c.ttl {
		return "", 0, false
	}
	return s.sessionID, s.sequence, true
}

// Clear removes a recorded delivery so a retry can run after a failed attempt.
func (c *Cache) Clear(dedupeKey, sessionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, Key{DedupeKey: dedupeKey, SessionID: sessionID})
	if c.latest[dedupeKey].sessionID == sessionID {
		delete(c.latest, dedupeKey)
	}
}

// Len returns the number of entries currently in the cache.
func (c *Cache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}

// Stop halts the background cleanup goroutine.
func (c *Cache) Stop() {
	close(c.stop)
}

func (c *Cache) cleanup() {
	ticker := time.NewTicker(c.ttl)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			c.mu.Lock()
			now := c.clock()
			for k, e := range c.entries {
				if now.Sub(e.at) > c.ttl {
					delete(c.entries, k)
				}
			}
			for k, s := range c.latest {
				if now.Sub(s.at) > c.ttl {
					delete(c.latest, k)
				}
			}
			c.mu.Unlock()
		case <-c.stop:
			return
		}
	}
}
