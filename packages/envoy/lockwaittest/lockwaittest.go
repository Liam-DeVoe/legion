// Package lockwaittest spells the read a Go test of this repository makes to learn that a
// PostgreSQL backend waits on a lock: Waiting, a condition on a row of pg_stat_activity, and
// WaitingOn, the same for one kind of lock. docs/testing.md, "A test reads a lock wait from
// pg_locks", states the rule; this documentation is its detail.
//
// # Why pg_locks
//
// A backend waits on a lock for as long as pg_locks holds an ungranted request of its pid. The
// request is queued when the backend asks and stays until the lock is granted or the wait is
// cancelled. pg_stat_activity's wait_event_type is not that fact. It reads Lock only while the
// backend sleeps on its latch, and a waiting backend leaves that sleep each time it is woken, by
// its deadlock check at deadlock_timeout or by a signal, so a read at that moment sees no wait.
// pg_blocking_pids reads the same queue as pg_locks.
//
// What was measured: a probe sampled one waiting backend's wait event and its pg_locks request
// together, beside the whole of internal/daemon under -race, with deadlock_timeout at its default
// of 1 s. In 2 of 10 waits it read no wait event at 1.007, 1.009 and 1.011 s into the wait, with
// the request still queued. TestReadyzIsFalseUntilTheDaemonServes read the wait event once and
// counted 0 of the daemon's backends waiting while its one connection waited (CI run
// 45206462019); how far into its wait that read fell was not measured.
//
// Any wait event reads nothing while a signal wakes its backend, a lock's or not: an idle holder's
// Client/ClientRead too. A test that decides on one sample of a wait event reads it again, within
// a bound, until it holds.
//
// # The check
//
// Envoy's internal/textcheck (lock_wait_reads_test.go) holds the three Go modules to this: Envoy,
// Envoy's infra and the daemon, each walked alone. It fails on a root that holds no go.mod, and on
// a module in which it read no Go file. It reads every Go string literal and names:
//
//   - this package's predicate written out anywhere but in this file: the select of the backends
//     pg_locks holds an ungranted request of, with or without one locktype condition and no
//     other, followed by the parenthesis that closes it or by the end of the literal, in whatever
//     statement holds it (pid in, pid not in, exists, a with query);
//   - a wait event compared with a lock by its exact value: wait_event_type with Lock, or
//     wait_event with a lock type such as advisory; by =, <> or !=, or by like, ilike or their
//     negations, in either order, or tested against a list (in, not in) that holds the value; the
//     column qualified (a.wait_event_type) or not, alone, inside one call (coalesce, lower) or
//     inside one pair of parentheses, and cast (::text); the value as a string or as an escape
//     string (E'Lock').
//
// It does not see:
//
//   - the predicate written another way: its conditions in the other order, with a table alias,
//     or as the start of a longer statement;
//   - a wait event matched by a pattern rather than its exact value, such as like 'Lock%';
//   - the column inside two calls or two pairs of parentheses, such as
//     lower(trim(wait_event_type));
//   - a list in which a parenthesis comes before the lock value, such as in (lower('x'), 'Lock');
//   - a comparison made in Go, of a wait_event_type the test scanned;
//   - a statement built from pieces: a value passed as a bind parameter, a column and a value in
//     two literals joined by +, or a format verb;
//   - another operator, such as = any(...), or a literal in a testdata directory;
//   - a test that decides on one sample of a wait event other than a lock's.
//
// A read of one lock from pg_locks, by relation or by advisory key, a read of one backend's request
// by its pid, and a read of the waiters behind one backend (pg_blocking_pids) do not write the
// predicate out, and the check leaves them alone.
package lockwaittest

// Waiting is the condition, on a row of pg_stat_activity, that its backend waits on a lock:
// pg_locks holds an ungranted request of its pid.
const Waiting = "pid in (select pid from pg_locks where not granted)"

// WaitingOn is Waiting for a lock of one kind. locktype is a value of pg_locks' locktype column,
// such as advisory or transactionid.
func WaitingOn(locktype string) string {
	return "pid in (select pid from pg_locks where not granted and locktype = '" + locktype + "')"
}
