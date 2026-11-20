// Package plants is the reads of pg_locks and pg_stat_activity that the gates of cybermonklabs/legion
// pull request 4585 planted against its earlier text check, each spelled so that the check passed it
// though it reads every database's locks or backends, and the reads beside them that keep to their
// own. Two tests hold each one: lockread's, which runs it as if it were one of lockread's reads
// against a second database holding planted waiters and requires the run to name every plant that
// sees that database, and internal/textcheck's lock_reads_test.go, which plants each in a test file
// outside lockread and requires the token rule to refuse it. The statements are the gates' own, with
// the parameters they took given values (Args) and a statement that Go assembled written whole.
package plants

// Plant is one planted read: who planted it and its name there, what it does, its statement and
// the values of its parameters, and whether it keeps to its own database (a control the proof must
// pass) or reads another's (a read the proof must name).
type Plant struct {
	Name, What, SQL string
	Args            []any
	Keeps           bool
}

// All is every planted read, by the gate that planted it.
var All = []Plant{
	// The acceptance test of round 2 (its D1): a0 is the plain read, a1 to a10 other spellings.
	{Name: "accept-2 a0", What: "the plain read of a table's waiters", SQL: `select count(*) from pg_locks where relation = 'search_term_stats'::regclass and not granted`},
	{Name: "accept-2 a1", What: "a comma join to this database's pg_class", SQL: `select count(*) from pg_class c, pg_locks l where l.relation = c.oid and c.relname = 'search_term_stats' and not l.granted`},
	{Name: "accept-2 a2", What: "the view as a quoted identifier", SQL: `select count(*) from "pg_locks" where relation = 'search_term_stats'::regclass and not granted`},
	{Name: "accept-2 a3", What: "a comment between from and the view", SQL: `select count(*) from /* the lock view */ pg_locks where relation = 'search_term_stats'::regclass and not granted`},
	{Name: "accept-2 a4", What: "the statement strings.Join assembled", SQL: `select count(*) from pg_locks where relation = 'search_term_stats'::regclass and not granted`},
	{Name: "accept-2 a5", What: "the view's name in a variable", SQL: `select count(*) from pg_locks where relation = 'search_term_stats'::regclass and not granted`},
	{Name: "accept-2 a6", What: "every backend through the per-backend statistics functions", SQL: `select count(*) from pg_stat_get_backend_idset() s where pg_stat_get_backend_wait_event_type(s) = 'Lock'`},
	{Name: "accept-2 a7", What: "the database in the select list", SQL: `select pid, datname = current_database() as mine from pg_stat_activity where wait_event_type = 'Lock'`},
	{Name: "accept-2 a8", What: "every other database", SQL: `select count(*) from pg_stat_activity where datname <> current_database() and wait_event_type = 'Lock'`},
	{Name: "accept-2 a9", What: "the database compared with itself", SQL: `select count(*) from pg_locks l where not l.granted and l.database = l.database and l.relation = 'search_term_stats'::regclass`},
	{Name: "accept-2 a10", What: "fmt.Sprintf's database undone by an or", SQL: `select count(*) from pg_locks l where l.database = 0 or not l.granted`},

	// The acceptance test of round 3 (its D1): p0 the plain read, g1 to g6 the other gates' plants of
	// round 2, b1 to b7 its own, k1 and k2 reads that keep to their own.
	{Name: "accept-3 p0", What: "the plain read", SQL: `select count(*) from pg_locks where relation = 'search_term_stats'::regclass and not granted`},
	{Name: "accept-3 g1", What: "the database in the select list", SQL: `select current_database(), count(*) from pg_locks where relation = 'issues'::regclass and not granted`},
	{Name: "accept-3 g2", What: "every blocked backend of every database", SQL: `select count(*) from pg_stat_activity where cardinality(pg_blocking_pids(pid)) > 0`},
	{Name: "accept-3 g3", What: "the database in the select list", SQL: `select current_database(), count(*) from pg_locks where not granted`},
	{Name: "accept-3 g4", What: "the database compared with itself", SQL: `select count(*) from pg_locks where not granted and database = database`},
	{Name: "accept-3 g5", What: "a comma join", SQL: `select count(*) from pg_class c, pg_locks l where l.relation = c.oid and c.relname = 'issues' and not l.granted`},
	{Name: "accept-3 g6", What: "every backend's pid", SQL: `select count(*) from pg_locks where not granted and pid = any(select pid from pg_stat_activity)`},
	{Name: "accept-3 b1", What: "the scope comparison, then is not true", SQL: `select count(*) from pg_locks l where not l.granted and l.database = (select oid from pg_database where datname = current_database()) is not true`},
	{Name: "accept-3 b2", What: "the scope comparison, then notnull", SQL: `select count(*) from pg_stat_activity where wait_event_type = 'Lock' and datname = current_database() notnull`},
	{Name: "accept-3 b3", What: "the scope condition in a left join's on", SQL: `select count(*) from pg_locks l left join pg_stat_activity a on a.pid = l.pid and l.database = (select oid from pg_database where datname = current_database()) where not l.granted`},
	{Name: "accept-3 b4", What: "a subquery tied by its own pid", SQL: `select count(*) from pg_stat_activity a where a.datname = current_database() and exists (select 1 from pg_locks w where w.pid = w.pid and not w.granted and w.locktype = 'advisory')`},
	{Name: "accept-3 b5", What: "the scope comparison inside distinct on", SQL: `select count(*) from (select distinct on (datname = current_database()) pid from pg_stat_activity where wait_event_type = 'Lock') w`},
	{Name: "accept-3 b6", What: "pg_database's row joined by no column", SQL: `select count(*) from pg_locks l join pg_database d on d.datname = current_database() where not l.granted`},
	{Name: "accept-3 b7", What: "the scope comparison in parentheses, then is false", SQL: `select count(*) from pg_stat_activity where (datname = current_database()) is false and wait_event_type = 'Lock'`},
	{Name: "accept-3 k1", What: "pg_locks' database against this database's oid", SQL: `select count(*) from pg_locks l where not l.granted and l.database = (select oid from pg_database where datname = current_database())`, Keeps: true},
	{Name: "accept-3 k2", What: "one backend by its pid", SQL: `select count(*) from pg_stat_activity where pid = $1 and wait_event_type = 'Lock'`, Args: []any{0}, Keeps: true},

	// The review of round 3 (its finding 1), with the quality gate's three shapes of round 2.
	{Name: "review-3 r3Control", What: "the plain read", SQL: `select count(*) from pg_locks where not granted`},
	{Name: "review-3 r3LeftJoinOwnColumnInOn", What: "the scope in a left join's on", SQL: `select count(*) from pg_locks l left join pg_class c on c.oid = l.relation and l.database = (select oid from pg_database where datname = current_database()) where not l.granted`},
	{Name: "review-3 r3RightJoinOwnColumnInOn", What: "the scope in a right join's on", SQL: `select count(*) from pg_class c right join pg_locks l on l.relation = c.oid and l.database = (select oid from pg_database where datname = current_database()) where not l.granted`},
	{Name: "review-3 r3JoinPgDatabaseOn", What: "pg_database's row in a join's on", SQL: `select count(*) from pg_locks l join pg_database d on d.datname = current_database() where not l.granted`},
	{Name: "review-3 r3CommaPgDatabase", What: "pg_database's row in a comma join", SQL: `select count(*) from pg_locks l, pg_database d where d.datname = current_database() and not l.granted`},
	{Name: "review-3 r3IsNotTrue", What: "the scope comparison, then is not true", SQL: `select count(*) from pg_stat_activity where datname = current_database() is not true and wait_event_type = 'Lock'`},
	{Name: "review-3 r3ParenthesesIsNotTrue", What: "the scope comparison in parentheses, then is not true", SQL: `select count(*) from pg_stat_activity where (datname = current_database()) is not true and wait_event_type = 'Lock'`},
	{Name: "review-3 r3IsDistinctFromTrue", What: "the scope comparison, then is distinct from true", SQL: `select count(*) from pg_locks l where not l.granted and l.database = (select oid from pg_database where datname = current_database()) is distinct from true`},
	{Name: "review-3 r3CorrelatedWithItself", What: "a subquery tied by its own pid", SQL: `select count(*) from pg_stat_activity a where a.datname = current_database() and exists (select 1 from pg_locks l where l.pid = l.pid and not l.granted)`},
	{Name: "review-3 r3BlockingStepOfNothing", What: "a blocking step of no recursive query", SQL: `select count(*) from (select pid from pg_stat_activity where pid = $1 union select b.pid from pg_stat_activity a, pg_stat_activity b where b.pid = any(pg_blocking_pids(a.pid))) x`, Args: []any{0}},
	{Name: "review-3 r3OwnDatabaseKept", What: "pg_locks' database against this database's oid", SQL: `select count(*) from pg_locks l where not l.granted and l.database = (select oid from pg_database where datname = current_database())`, Keeps: true},
	{Name: "review-3 q3Projection", What: "the database in the select list", SQL: `select current_database(), count(*) from pg_locks where not granted`},
	{Name: "review-3 q3DatabaseSelfComparison", What: "the database compared with itself", SQL: `select count(*) from pg_locks where not granted and database = database`},
	{Name: "review-3 q3BlockersOfEveryBackend", What: "every blocked backend of every database", SQL: `select count(*) from pg_stat_activity where cardinality(pg_blocking_pids(pid)) > 0`},
	{Name: "review-3 r3DistinctOn", What: "the scope comparison inside distinct on", SQL: `select distinct on (datname = current_database()) pid from pg_stat_activity where wait_event_type = 'Lock'`},
	{Name: "review-3 r3ParenthesesEqualsFalse", What: "the scope comparison in parentheses, then = false", SQL: `select count(*) from pg_stat_activity where (datname = current_database()) = false and wait_event_type = 'Lock'`},

	// The deep audit of round 3 (its Medium and hardening 1): c0 to c3 round 2's plants, h1 to h14
	// other spellings, k1 and k2 reads that keep to their own.
	{Name: "deep-3 c0", What: "the plain read", SQL: `select count(*) from pg_locks where relation = 'issues'::regclass and not granted`},
	{Name: "deep-3 c1", What: "every blocked backend of every database", SQL: `select count(*) from pg_stat_activity where cardinality(pg_blocking_pids(pid)) > 0`},
	{Name: "deep-3 c2", What: "the database in the select list", SQL: `select current_database(), count(*) from pg_locks where relation = 'issues'::regclass and not granted`},
	{Name: "deep-3 c3", What: "pg_locks' database against this database's oid", SQL: `select count(*) from pg_locks l where not l.granted and l.database = (select oid from pg_database where datname = current_database())`, Keeps: true},
	{Name: "deep-3 h1", What: "pg_database's row in a join's on", SQL: `select count(*) from pg_locks l join pg_database d on d.datname = current_database() where not l.granted`},
	{Name: "deep-3 h2", What: "pg_database's row in a comma join", SQL: `select count(*) from pg_locks l, pg_database d where d.datname = current_database() and not l.granted`},
	{Name: "deep-3 h3", What: "the scope in a left join's on", SQL: `select count(*) from pg_locks l left join pg_stat_activity a on a.pid = l.pid and a.datname = current_database() where not l.granted`},
	{Name: "deep-3 h4", What: "the scope comparison, then is not true", SQL: `select count(*) from pg_stat_activity where datname = current_database() is not true and wait_event_type = 'Lock'`},
	{Name: "deep-3 h5", What: "an inner join read as a tie", SQL: `select (select count(*) from pg_locks w join pg_stat_activity a on a.pid = w.pid where not w.granted) from pg_stat_activity where pid = pg_backend_pid()`},
	{Name: "deep-3 h6", What: "a subquery tied by its own pid", SQL: `select (select count(*) from pg_locks i where i.pid = i.pid and not i.granted) from pg_stat_activity where pid = pg_backend_pid()`},
	{Name: "deep-3 h7", What: "the scope comparison inside distinct on", SQL: `select distinct on (datname = current_database()) pid from pg_stat_activity where wait_event_type = 'Lock'`},
	{Name: "deep-3 h8", What: "the scope comparison in the select list", SQL: `select pid, true and datname = current_database() from pg_stat_activity where wait_event_type = 'Lock'`},
	{Name: "deep-3 h9", What: "a blocking step beside a scoped branch", SQL: `select count(*) from (select pid from pg_stat_activity where datname = current_database() union select b.pid from pg_stat_activity b, pg_stat_activity w where w.pid = any(pg_blocking_pids(b.pid))) s`},
	{Name: "deep-3 h10", What: "the scope in a left join's on", SQL: `select count(*) from pg_locks l left join pg_database d on l.database = (select oid from pg_database where datname = current_database()) where not l.granted`},
	{Name: "deep-3 h11", What: "an unscoped scalar subquery", SQL: `select (select count(*) from pg_locks where not granted) from pg_stat_activity where pid = pg_backend_pid()`},
	{Name: "deep-3 h12", What: "an uncorrelated exists", SQL: `select count(*) from pg_stat_activity o where o.datname = current_database() and exists (select 1 from pg_locks w join pg_stat_activity a on a.pid = w.pid where not w.granted)`},
	{Name: "deep-3 h13", What: "the scope in an outer join's on", SQL: `select a.pid from pg_stat_activity a left join pg_locks l on l.pid = a.pid and a.datname = current_database() where a.wait_event_type = 'Lock'`},
	{Name: "deep-3 h14", What: "pg_database in an outer join", SQL: `select l.pid, d.datname from pg_locks l left join pg_database d on d.oid = l.database and d.datname = current_database() where not l.granted`},
	{Name: "deep-3 k1", What: "joined to pg_database by its oid", SQL: `select count(*) from pg_locks l join pg_database d on d.oid = l.database where d.datname = current_database() and not l.granted`, Keeps: true},
	{Name: "deep-3 k2", What: "one backend by its pid", SQL: `select mode from pg_locks where pid = $1 and not granted`, Args: []any{0}, Keeps: true},

	// The goal reader of round 3 (round 4's item 1).
	{Name: "goal-3 is false", What: "the scope comparison, then is false", SQL: `select count(*) from pg_stat_activity where datname = current_database() is false`},
}
