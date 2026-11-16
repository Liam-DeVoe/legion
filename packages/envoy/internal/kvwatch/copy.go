package kvwatch

import (
	"iter"
	"maps"
)

// Copy is one cache's in-memory copy of its bucket: its entries, the revision fence that keeps a
// delayed result from undoing a newer one, the marks of the read under way and the epoch that read
// began in. It is the whole of the read protocol (Cache) and of the write sites, so a cache keeps
// none of that state itself: the hooks it hands its watcher call BeginRead, Delivered and
// CompleteRead, and each result of a KV call it makes reaches the copy through Put, Apply or
// Evict, which drop a result of an epoch the watcher no longer takes and mark the key of every
// other before they compare the fence. No write site can take a result without marking it, or mark
// one it drops.
//
// A Copy has no lock of its own: the cache calls every method under the mutex that guards
// whatever else it keeps per key (a session's last heartbeat, its expiry), and its hooks take that
// mutex too. The write sites read the watcher's epoch under it, which the lock order allows: the
// cache's mutex, then the watcher's (Cache).
type Copy[V any] struct {
	rows      Rows[V]
	entries   map[string]V
	revisions map[string]uint64
	marks     marks
	readEpoch uint64
	// ahead is the fence of aheadEpoch, an epoch whose read has not begun: the revisions of the
	// results the copy took through a handle of that epoch, the bucket deleted and created again
	// (Watcher.Handle). They number in the new bucket's stream, so they are fenced against each
	// other and never against revisions, the fence of the bucket that was deleted; that epoch's
	// read takes ahead as its fence (BeginRead).
	ahead      map[string]uint64
	aheadEpoch uint64
}

// Rows is how one copy turns a record into its entry, and what the cache that keeps it records
// beside its entries when one is stored or removed. The copy calls each under the cache's mutex.
type Rows[V any] struct {
	// Decode turns a put record into the entry the copy keeps for its key. keep false with a nil
	// error removes the key as a delete marker does: the cache keeps no entry for that record. An
	// error, a value it cannot decode, removes the key as well and is returned for the caller to
	// log.
	Decode func(entry Entry) (value V, keep bool, err error)
	// Stored, when not nil, follows every entry the copy stores.
	Stored func(key string, value V)
	// Removed, when not nil, is handed every entry the copy removes for the bucket's sake: by a
	// record or an Evict that passed the fence, and by a completed read that found no evidence of
	// it. Remove, the cache's own removal, does not call it.
	Removed func(key string, value V)
}

// Outcome is what became of a KV call's result handed to a copy (Put, Apply, Evict).
type Outcome uint8

const (
	// Dropped is a result through a handle of an earlier epoch (Watcher.Handle), from a bucket
	// since recreated. It reaches nothing and marks nothing: its revision would fence out the new
	// bucket's, which number from 1 again, and the new bucket's read delivers what it holds for the
	// key.
	Dropped Outcome = iota
	// Fenced is a result of the current epoch, whose key it marked for the read under way, at a
	// revision older than the one the copy holds for that key: the copy is left as it was.
	Fenced
	// Applied is a result of the current epoch that marked its key and that the copy took.
	Applied
)

// NewCopy returns an empty copy that takes its records through rows.
func NewCopy[V any](rows Rows[V]) Copy[V] {
	return Copy[V]{rows: rows, entries: map[string]V{}, revisions: map[string]uint64{}}
}

// Get answers the entry the copy holds for key.
func (c *Copy[V]) Get(key string) (V, bool) {
	value, ok := c.entries[key]
	return value, ok
}

// Len is the number of entries the copy holds.
func (c *Copy[V]) Len() int {
	return len(c.entries)
}

// All yields every entry the copy holds, in no order. The loop may Remove the entry it is handed.
func (c *Copy[V]) All() iter.Seq2[string, V] {
	return maps.All(c.entries)
}

// Remove takes key's entry out of the copy for a reason of the cache's own, such as an expiry the
// bucket never delivers; the cache records beside it what it records for that reason, so Remove
// does not call Rows.Removed. The fence keeps key's revision: no record removed it, so a delayed
// one older than that revision is still fenced out.
func (c *Copy[V]) Remove(key string) {
	delete(c.entries, key)
}

// BeginRead starts a read of the bucket (Cache.BeginRead): a fresh set of marks, and the read's
// epoch as the one the fence belongs to. A recreated bucket's read drops the fence of the bucket
// that was deleted, whose revisions say nothing of the new stream's sequence, and takes in its
// place the fence of what the copy took through a handle of its own epoch before it began
// (ahead): those are the new bucket's revisions. Every entry stays.
//
// The read's watch read the stream (Read.LastSeq) before it made its consumer, and the read begins
// only once that consumer is made: a record that landed in between is past the read's scan, which
// delivers it only after its end. So every key the copy took at a revision past LastSeq - a
// write-through, a read-through, a delete, or a record the watcher this read replaces delivered -
// is marked for the read here, and its completion keeps the entry and its fence. The read cannot
// vouch that the bucket no longer holds it; the records that follow its scan say. A result of the
// deleted bucket's epoch, a call made before the watch read the new stream, is in no fence the
// recreated read keeps: it could have reached either bucket, and the new bucket's read delivers
// what that bucket holds for the key.
func (c *Copy[V]) BeginRead(read Read) {
	c.marks.begin()
	if read.Recreated {
		if c.ahead != nil && c.aheadEpoch == read.Epoch {
			c.revisions = c.ahead
		} else {
			clear(c.revisions)
		}
		c.ahead, c.aheadEpoch = nil, 0
	}
	c.readEpoch = read.Epoch
	for key, revision := range c.revisions {
		if revision > read.LastSeq {
			c.marks.mark(key)
		}
	}
}

// Delivered takes one record the current read delivered (Cache.Apply). Its key is marked before
// the fence is compared, so a delivered value older than a local write still keeps the key
// through the sweep. The error is a value Rows.Decode could not decode, for the caller to log.
func (c *Copy[V]) Delivered(entry Entry) error {
	c.marks.mark(entry.Key)
	_, err := c.apply(c.readFence(), entry)
	return err
}

// CompleteRead removes every entry and fence entry the read found no evidence of, handing each
// removed entry to Rows.Removed, and ends the read's marks (Cache.CompleteRead): the bucket no
// longer holds those keys.
func (c *Copy[V]) CompleteRead() {
	sweep(&c.marks, c.entries, c.rows.Removed)
	sweep(&c.marks, c.revisions, nil)
	c.marks.end()
}

// Put takes value, written for key at revision through a handle of epoch (Watcher.Handle): a
// write-through, or a read-through the cache decoded itself.
func (c *Copy[V]) Put(w *Watcher, epoch uint64, key string, value V, revision uint64) Outcome {
	fence, ok := c.fence(w, epoch, key)
	if !ok {
		return Dropped
	}
	if revision < fence[key] {
		return Fenced
	}
	c.store(fence, key, value, revision)
	return Applied
}

// Apply takes entry, a record of the bucket read through a handle of epoch, as a delivered record
// is taken: a read-through, or a key's latest record read back after a delete. The error is a
// value Rows.Decode could not decode, for the caller to log; a record the fence drops is not
// decoded.
func (c *Copy[V]) Apply(w *Watcher, epoch uint64, entry Entry) (Outcome, error) {
	fence, ok := c.fence(w, epoch, entry.Key)
	if !ok {
		return Dropped, nil
	}
	return c.apply(fence, entry)
}

// Evict removes key at revision for a delete made through a handle of epoch whose own record the
// cache could not read back.
func (c *Copy[V]) Evict(w *Watcher, epoch uint64, key string, revision uint64) Outcome {
	fence, ok := c.fence(w, epoch, key)
	if !ok {
		return Dropped
	}
	if revision < fence[key] {
		return Fenced
	}
	c.evict(fence, key, revision)
	return Applied
}

// FenceFor is the revision the fence of epoch holds for key, for a delete about to be made through
// a handle of epoch: the current read's fence, or the one the copy keeps for an epoch whose read
// has not begun (ahead). Any other epoch's is 0.
func (c *Copy[V]) FenceFor(epoch uint64, key string) uint64 {
	switch epoch {
	case c.readEpoch:
		return c.revisions[key]
	case c.aheadEpoch:
		return c.ahead[key]
	}
	return 0
}

// fence reports whether a result through a handle of epoch may reach the copy (Watcher.takes),
// marks key for the read under way when it may, and returns the fence the result is compared
// against and recorded in: the current read's for a result of its epoch, and for a result of a
// later one - a bucket deleted and created again whose read has not begun - that epoch's own
// (ahead), made afresh when the epoch it held is another.
func (c *Copy[V]) fence(w *Watcher, epoch uint64, key string) (map[string]uint64, bool) {
	if !w.takes(epoch) {
		return nil, false
	}
	c.marks.mark(key)
	if epoch == c.readEpoch {
		return c.readFence(), true
	}
	if c.ahead == nil || c.aheadEpoch != epoch {
		c.ahead, c.aheadEpoch = map[string]uint64{}, epoch
	}
	return c.ahead, true
}

// readFence is the current read's fence, made on first use.
func (c *Copy[V]) readFence() map[string]uint64 {
	if c.revisions == nil {
		c.revisions = map[string]uint64{}
	}
	return c.revisions
}

// apply takes one record whose key is already marked, against fence: one the fence drops leaves
// the copy as it is, undecoded; a put the cache keeps is stored; and a delete or purge marker, a
// put the cache does not keep and one it cannot decode remove the key.
func (c *Copy[V]) apply(fence map[string]uint64, entry Entry) (Outcome, error) {
	if entry.Revision < fence[entry.Key] {
		return Fenced, nil
	}
	if entry.Op != OpPut {
		c.evict(fence, entry.Key, entry.Revision)
		return Applied, nil
	}
	value, keep, err := c.rows.Decode(entry)
	if err != nil || !keep {
		c.evict(fence, entry.Key, entry.Revision)
		return Applied, err
	}
	c.store(fence, entry.Key, value, entry.Revision)
	return Applied, nil
}

func (c *Copy[V]) store(fence map[string]uint64, key string, value V, revision uint64) {
	if c.entries == nil {
		c.entries = map[string]V{}
	}
	c.entries[key] = value
	fence[key] = revision
	if c.rows.Stored != nil {
		c.rows.Stored(key, value)
	}
}

func (c *Copy[V]) evict(fence map[string]uint64, key string, revision uint64) {
	if value, ok := c.entries[key]; ok {
		delete(c.entries, key)
		if c.rows.Removed != nil {
			c.rows.Removed(key, value)
		}
	}
	fence[key] = revision
}
