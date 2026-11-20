package api

// readWrites is what one GET row of the table writes when it serves a request: whether it is a
// read, which a read-only token may call, or a GET that writes, which no read-only token may
// call. A read-only token is any credential that holds read: a token the secrets broker minted
// from a read entry or narrowed on request, and a personal token minted with read.
type readWrites int

const (
	// readWritesUnset is the zero value: an entry that decides nothing. It is never a class, so
	// an entry that leaves Writes unset is a row nobody has read yet.
	readWritesUnset readWrites = iota
	// readWritesNothing is a read. On the request's context, and in the work it detaches from
	// that context, the row writes nothing but four things: the insert into dpop_proofs that
	// spends the request's proof, the update of the personal token's last_used_at, the insert
	// into text_trees that a read with Dispatch-Trees: 1 makes, and the upsert of a stale
	// route_roster. A write the server makes on its own context for every reader alike, as the
	// event horizon's observer persists event_horizon for every reader of the event log, is not
	// the request's write: a row whose handler reads through such shared work is still a read,
	// and its WriteNote says so.
	readWritesNothing
	// readWritesState is a GET that writes: anything a read does not, on the request's context,
	// or on another context its handler itself starts the write on (a statistics refresh, say).
	// Its WriteNote names what it writes.
	readWritesState
)

// readEntry is the read class of one GET row of the table: what the row writes, and the note
// that says so where the class alone does not.
type readEntry struct {
	// Writes is the row's class: readWritesNothing or readWritesState, never readWritesUnset.
	Writes readWrites
	// WriteNote names what the row writes, and the statement or the function that writes it. A
	// readWritesState row always carries one. A readWritesNothing row carries one when its
	// handler makes the server write on its own context for every reader alike, or when its
	// class differs from what a first reading of its handler suggests, and then it says why.
	WriteNote string
}

// readClasses is the read class of every GET row of the table with its test hooks in, keyed
// "GET <pattern>" as routeScopes is: exactly one entry for each GET row and none for anything
// else. TestEveryGETRowHasOneReadEntry holds the table to it, so a GET row added without an
// entry fails that test by name.
var readClasses = map[string]readEntry{}
