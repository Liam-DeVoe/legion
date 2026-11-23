// Package lockwaittest spells the read a Go test of this repository makes to learn that a
// PostgreSQL backend waits on a lock: Waiting, a condition on a row of pg_stat_activity, and
// WaitingOn, the same for one kind of lock. Envoy's internal/textcheck fails on this predicate
// written out in another Go file of the three modules, in the forms docs/testing.md lists, and on
// a lock wait read from a wait event. Reads of one lock or of the waiters behind one backend are
// other reads, and the check leaves them alone.
//
// A backend waits on a lock for as long as pg_locks holds an ungranted request of its pid. The
// request is queued when the backend asks and stays until the lock is granted or the wait is
// cancelled. pg_stat_activity's wait_event_type is not that fact. It reads Lock only while the
// backend sleeps on its latch, and a waiting backend leaves that sleep each time it is woken, by
// its deadlock check at deadlock_timeout or by a signal, so a read at that moment sees no wait.
// pg_blocking_pids reads the same queue as pg_locks. docs/testing.md, "A test reads a lock wait
// from pg_locks", gives what was measured and the test that failed on the wait event.
package lockwaittest

// Waiting is the condition, on a row of pg_stat_activity, that its backend waits on a lock:
// pg_locks holds an ungranted request of its pid.
const Waiting = "pid in (select pid from pg_locks where not granted)"

// WaitingOn is Waiting for a lock of one kind. locktype is a value of pg_locks' locktype column,
// such as advisory or transactionid.
func WaitingOn(locktype string) string {
	return "pid in (select pid from pg_locks where not granted and locktype = '" + locktype + "')"
}
