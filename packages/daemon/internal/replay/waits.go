package replay

// waits.go is the stub's skip of the tries of a row it gave back to wait on its backoff that no
// change of what the try reads can end (skipWaits), and the replay database's triggers that say
// when what a try reads changed (scopeTriggers).
//
// Every row the stub gives back (driver.retry) is a supervise start, and its try reads, before it
// gives the row back, only these, as worker.go's take, superviseRow and start read them:
//
//   - the store's claim of the row (record.Postgres.ClaimRow, outboxClaimable): an older
//     issue_branch row of its issue (outbox), a slot of its issue for a start marked NeedsSlot
//     (slots), an open pause of the project (project_pauses), and a live lease on another lifecycle
//     row of its issue, which the stub holds only while it runs a row, so none stands between two
//     drains;
//   - under a rule whose start may take a cover, waitHolds: the issue's slot (slots) and the claim's
//     given entry (claims);
//   - superviseRow's fences: the issue's record (issues) and, for a start with an entry and a
//     phase, its role's phases row (phases);
//   - start: the claim the stub holds, which it writes to the store as it writes its own copy
//     (claims, and its pending task in pending_task_deliveries), and the records of the issue and
//     its tree's root (issues: status, generation, phase, tree, linger). Then, by kind:
//   - the first start of a child's own architect, given back while its root lingers: the root's
//     record (issues) and that no claim holds the role (claims);
//   - a start queued for news alone, given back while its claim holds a task: the role's phases row
//     (phases), the claim (claims, pending_task_deliveries), and the news the claim speaks for
//     (creditNews): the root's gate rows (design_gates), the tree's members (issues), what each
//     holder took (design_news_told, counted with the credits deliveries hold, which the SQL
//     function design_news_told_in reads from pending_task_deliveries and claims), whose pull
//     requests merged (pull_requests), the architect claims of the tree (claims) and the change
//     sets the items name (design_changes);
//   - a merger's start, given back while its tree's design gate is closed (gateClosed:
//     design_gates) or its news is not recorded (mergerNewsUnrecorded: the news facts above and
//     design_changes);
//   - a start whose claim its tree's lifecycle refuses (store.Store.AdmitClaim: tree_lifecycles and
//     claims).
//
// No other row is ever given back: a hand-over's status write (handOverRow) writes or finishes, as
// every status write, gate seed, linger close and reconcile of news does. The skip's own write of a
// row's attempts and next_at, and every give-back, change the row alone (outbox), which no try
// reads but through the claim rules above.
//
// Each of those tables is in scopeTables, and a trigger on it bumps, at every row it writes, the
// version of the tree each row written belongs to, or, for a table no tree keys, the project's
// version. A row's try reads its own issue's tree, the trees of no other issue, and the project's
// tables, so its try is the same while the versions of its tree and of the project stand. An issue
// that moves to another tree bumps both. The issue's own version would add nothing: a start for
// news alone reads its whole tree.

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"time"

	"github.com/sjawhar/legion/daemon/internal/record"
)

// scopeTable is one table a try of a row given back reads (the list above), and how a row of it
// names its scope: tree, the row's own tree column (issues, tree_lifecycles); issue, its issue
// column's tree; claim, its tree column and its issue's tree; delivery, the tree and the issue's
// tree of the claim its claim_token names; branch, its issue's tree for an issue_branch row alone;
// project, the project's.
type scopeTable struct {
	table, shape string
}

// scopeTables is every table a try of a row given back reads (the list above).
var scopeTables = []scopeTable{
	{"issues", "tree"},
	{"tree_lifecycles", "tree"},
	{"phases", "issue"},
	{"slots", "issue"},
	{"design_gates", "issue"},
	{"design_news_told", "issue"},
	{"pull_requests", "issue"},
	{"claims", "claim"},
	{"pending_task_deliveries", "delivery"},
	{"outbox", "branch"},
	{"project_pauses", "project"},
	{"design_changes", "project"},
	{"design_history_unread", "project"},
}

// scopeTriggers is the statements that make, in a replay's database, the table of scope versions
// (replay_scope_versions: `tree:<root>` and `project`) and a trigger on each of scopeTables that
// bumps the versions of the scopes each row it inserts, updates or deletes belongs to, before and
// after the write.
func scopeTriggers() []string {
	statements := []string{
		`create table replay_scope_versions (scope text primary key, version bigint not null)`,
		`create function replay_scope_version_bump(bumped text) returns void language plpgsql as $$
		begin
			if bumped is null then
				return;
			end if;
			insert into replay_scope_versions as v (scope, version) values (bumped, 1)
				on conflict (scope) do update set version = v.version + 1;
		end $$`,
		`create function replay_tree_of(written text) returns text language sql stable as $$
			select coalesce((select tree from issues where key = $1), $1) $$`,
		`create function replay_scope_bump() returns trigger language plpgsql as $$
		declare
			written jsonb[] := array[]::jsonb[];
			r jsonb;
		begin
			if TG_OP <> 'INSERT' then
				written := written || to_jsonb(OLD);
			end if;
			if TG_OP <> 'DELETE' then
				written := written || to_jsonb(NEW);
			end if;
			foreach r in array written loop
				case TG_ARGV[0]
				when 'tree' then
					perform replay_scope_version_bump('tree:' || (r->>'tree'));
				when 'issue' then
					perform replay_scope_version_bump('tree:' || replay_tree_of(r->>'issue'));
				when 'claim' then
					perform replay_scope_version_bump('tree:' || (r->>'tree'));
					perform replay_scope_version_bump('tree:' || replay_tree_of(r->>'issue'));
				when 'delivery' then
					perform replay_scope_version_bump('tree:' || (select tree from claims where token = r->>'claim_token'));
					perform replay_scope_version_bump('tree:' || replay_tree_of((select issue from claims where token = r->>'claim_token')));
				when 'branch' then
					if r->>'kind' = 'issue_branch' then
						perform replay_scope_version_bump('tree:' || replay_tree_of(r->>'issue'));
					end if;
				else
					perform replay_scope_version_bump('project');
				end case;
			end loop;
			return null;
		end $$`,
	}
	for _, t := range scopeTables {
		statements = append(statements, fmt.Sprintf(`create trigger replay_scope after insert or update or delete on %s
			for each row execute function replay_scope_bump('%s')`, t.table, t.shape))
	}
	return statements
}

// skipMargin is how far before the next change the skip stops: a try the drain of a wake in that
// last millisecond would run is run. The moment a row is due is read with the slack (dueSlack), so
// an item at the change takes a row due up to a millisecond after it, and the tries of one instant
// move the simulated clock on by a microsecond for each read (simClock), never by a millisecond.
const skipMargin = time.Millisecond

// waitingRow is a row the stub gave back to wait on its backoff (retry): its issue's tree, the
// versions of that tree and of the project when its try read them, and the moment it is due again.
type waitingRow struct {
	tree                  string
	treeVersion, projects int64
	next                  time.Time
}

// scopesOf is the scopes a try of a row of issue read as the database stands: its tree and the
// versions of that tree and of the project (scopeTriggers). It reads no simulated clock.
func (d *driver) scopesOf(issue string) (waitingRow, error) {
	var w waitingRow
	err := d.pool.QueryRow(d.ctx, `select t.tree,
			coalesce((select version from replay_scope_versions where scope = 'tree:' || t.tree), 0),
			coalesce((select version from replay_scope_versions where scope = 'project'), 0)
		from (select coalesce((select tree from issues where key = $1), $1) as tree) t`, issue).Scan(&w.tree, &w.treeVersion, &w.projects)
	if err != nil {
		return waitingRow{}, fmt.Errorf("replay: read the scope versions of %s's tree: %w", issue, err)
	}
	return w, nil
}

// skipWaits skips the tries of the rows given back to wait that no change of what their tries read
// can end, as wake runs it before it queues the stub's next run. A try reads only the tables of
// scopeTables in its issue's tree and the project's (the list above), never a moment of its own: a
// try while those versions stand fails as the row's last did, and changes nothing but the row's own
// attempts, next_at and last_error, which no try of another row reads.
//
// Until the next item of the queue (an outbox wake among them, which may run a row that comes due
// at it), the next moment a row comes due that is not such a waiting row, the window's end, after
// which nothing runs, or, for the snapshot timeline, a moment an issue's rank changes in the
// record's trace (rankMoments), which a settle reads, the tries of the waiting rows come on the
// daemon's grid alone: at a wake at the earliest next_at, whose drain runs every row due by the
// slack, each given back at that instant plus its backoff (record.RetryBackoff of its attempts)
// with one attempt more. skipWaits plays that grid forward to the wakes before that moment, less
// skipMargin, writes each row's attempts and next_at as those tries leave them (last_error being
// the one its last try wrote), and counts each try skipped in Divergence.Retried. The wake it then
// queues, and every try from there, is the one a run without the skip makes. The daemon's backoff
// bounds no attempts, so a row given back is tried until something changes.
//
// It skips nothing of a row whose versions moved since its last try, or whose next_at is not on the
// millisecond (a row given back at an item's own instant, whose first wake reads it due by the
// slack), at a wake while the daemon is down (downAt: that wake is held to the next boot, and its
// drain with it), or while a boot has held acts still to run. It reads no simulated clock, so the
// clock's moments stay those of a run without the skip.
func (d *driver) skipWaits() error {
	if d.cfg.noSkip || d.heldLeft > 0 || len(d.waiting) == 0 {
		return nil
	}
	instant := d.clock.Instant()
	change := d.window.To
	if len(d.queue) > 0 && d.queue[0].at.Before(change) {
		change = d.queue[0].at
	}
	if i := sort.Search(len(d.rankMoments), func(i int) bool { return d.rankMoments[i].After(instant) }); i < len(d.rankMoments) &&
		d.rankMoments[i].Before(change) {
		change = d.rankMoments[i]
	}
	if !slices.ContainsFunc(slices.Collect(maps.Values(d.waiting)), func(w waitingRow) bool { return w.next.Before(change.Add(-skipMargin)) }) {
		return nil
	}
	scopes := []string{"project"}
	for _, w := range d.waiting {
		scopes = append(scopes, "tree:"+w.tree)
	}
	versions := map[string]int64{}
	rows, err := d.pool.Query(d.ctx, `select scope, version from replay_scope_versions where scope = any($1)`, scopes)
	if err != nil {
		return fmt.Errorf("replay: read the scope versions at %s: %w", stamp(instant), err)
	}
	for rows.Next() {
		var scope string
		var version int64
		if err := rows.Scan(&scope, &version); err != nil {
			rows.Close()
			return fmt.Errorf("replay: read the scope versions at %s: %w", stamp(instant), err)
		}
		versions[scope] = version
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("replay: read the scope versions at %s: %w", stamp(instant), err)
	}
	maps.DeleteFunc(d.waiting, func(_ int64, w waitingRow) bool {
		return w.treeVersion != versions["tree:"+w.tree] || w.projects != versions["project"]
	})
	type row struct {
		id       int64
		attempts int
		next     time.Time
		skipped  int
	}
	var waits []*row
	outbox, err := d.pool.Query(d.ctx, `select id, attempts, next_at from outbox where split_part(issue, '-', 1) = $1 and next_at > $2
		and (lease_until is null or lease_until <= $2) order by id`, d.window.Project, instant.Add(dueSlack))
	if err != nil {
		return fmt.Errorf("replay: read the outbox rows due after %s: %w", stamp(instant), err)
	}
	for outbox.Next() {
		r := &row{}
		if err := outbox.Scan(&r.id, &r.attempts, &r.next); err != nil {
			outbox.Close()
			return fmt.Errorf("replay: read the outbox rows due after %s: %w", stamp(instant), err)
		}
		r.next = r.next.UTC()
		if w, given := d.waiting[r.id]; given && w.next.Equal(r.next) && r.next.Equal(r.next.Truncate(time.Millisecond)) {
			waits = append(waits, r)
			continue
		}
		if at := r.next.Truncate(time.Millisecond); at.Before(change) {
			change = at
		}
	}
	outbox.Close()
	if err := outbox.Err(); err != nil {
		return fmt.Errorf("replay: read the outbox rows due after %s: %w", stamp(instant), err)
	}
	limit := change.Add(-skipMargin)
	skipped := 0
	for len(waits) > 0 {
		wake := slices.MinFunc(waits, func(x, y *row) int { return x.next.Compare(y.next) }).next
		if !wake.Before(limit) || d.downAt(wake) {
			break
		}
		for _, r := range waits {
			if !r.next.After(wake.Add(dueSlack)) {
				r.next, r.attempts, r.skipped = wake.Add(record.RetryBackoff(r.attempts)), r.attempts+1, r.skipped+1
				skipped++
			}
		}
	}
	for _, r := range waits {
		if r.skipped == 0 {
			continue
		}
		if _, err := d.pool.Exec(d.ctx, `update outbox set attempts = $2, next_at = $3 where id = $1`, r.id, r.attempts, r.next); err != nil {
			return fmt.Errorf("replay: move the outbox row %d along its backoff past the tries no change can end: %w", r.id, err)
		}
		w := d.waiting[r.id]
		w.next = r.next
		d.waiting[r.id] = w
	}
	d.counters.Retried += skipped
	return nil
}

// rankChanges is each moment inside t's window at which an issue's rank as its Dispatch events
// show it changes, in order: the moment of each issue event whose rank differs from that of the
// issue's event before it (ranksOf reads the rank of the newest event at or before a moment, and
// of the first before every one).
func rankChanges(t Trace) []time.Time {
	var moments []time.Time
	for _, iss := range t.Issues {
		previous := ""
		first := true
		for _, event := range iss.Dispatch {
			if event.Issue == nil {
				continue
			}
			if !first && event.Issue.Rank != previous && event.At.After(t.From) && event.At.Before(t.To) {
				moments = append(moments, event.At)
			}
			previous, first = event.Issue.Rank, false
		}
	}
	slices.SortFunc(moments, time.Time.Compare)
	return slices.CompactFunc(moments, time.Time.Equal)
}
