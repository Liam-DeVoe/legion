package legionfacts

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/events"
	"github.com/sjawhar/envoy/internal/dispatch/legion/legiontest"
	"github.com/sjawhar/envoy/internal/dispatch/legion/metrics"
)

// testBuild is the build every test's loop runs under: one string for one process, as a build
// is (Deps.Build), and never the empty string, which the loop refuses (run). A check that needs
// a second build writes that build into the project's record (build_whole_read_test.go's
// otherBuild) or runs a second loop under it (roll_test.go), and the check of the build a
// released binary carries starts two stamped binaries rather than one process
// (cmd/dispatch's TestASecondReleaseReadsTheProjectWholeAndRecordsItsOwnCommit).
const testBuild = "legion-envoy-v0.0.0-test-build"

// consumer is one test's loop: the seeded history, the Deps a pass reads through, and the clock
// each pass runs at, which the test moves rather than the wall clock. A pass at a moment the test
// chose is what lets a check say which rows a change moved, and that the clock moving alone moves
// none.
type consumer struct {
	history
	deps  Deps
	sched schedule
	at    time.Time
}

// newConsumer is a loop over a seeded history of trees trees, whose process started at the
// history's own moment, the moment its first pass runs at.
func newConsumer(t *testing.T, trees int) *consumer {
	t.Helper()
	seeded := seed(t, trees)
	run := &consumer{history: seeded, at: seeded.now}
	run.deps = Deps{Pool: seeded.database.Pool, Build: testBuild, Horizon: events.NewHorizon(seeded.database.Pool)}
	run.sched = schedule{page: feedPage, now: func() time.Time { return run.at }, started: run.at}
	return run
}

// restart is the loop's process starting again at the consumer's clock, under the same build:
// what a second process of one release is, which judges every record of a whole read against its
// own start (legionProjects).
func (c *consumer) restart() {
	c.deps.Horizon = events.NewHorizon(c.database.Pool)
	c.sched.started = c.at
}

// pass makes one pass as of the consumer's clock and answers the keys whose stored rows it
// changed, sorted. The pass awaits the event log's safe horizon itself (pass), so it reads every
// event written before it, as the server's pass reads the event whose commit woke it.
func (c *consumer) pass(t *testing.T) []string {
	t.Helper()
	changed, err := c.attempt(t)
	if err != nil {
		t.Fatalf("the Legion facts pass failed: %v", err)
	}
	return changed
}

// attempt is pass, with the failure left to the caller: what a check of a pass that fails reads.
func (c *consumer) attempt(t *testing.T) ([]string, error) {
	t.Helper()
	changed, err := pass(t.Context(), c.deps, c.sched)
	if err != nil {
		return nil, err
	}
	slices.Sort(changed)
	return changed, nil
}

// exec runs one statement of a test's own setup.
func (c *consumer) exec(t *testing.T, what, statement string, args ...any) {
	t.Helper()
	if _, err := c.database.Pool.Exec(t.Context(), statement, args...); err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

// writeEvent writes one event of the issue at the moment given, as a Legion role's session
// wrote it: the actor carries the session title the phase-work rule reads (legiontest's own
// session actor), so the event is work of that issue.
func (c *consumer) writeEvent(t *testing.T, issue, role, kind, payload string, at time.Time) {
	t.Helper()
	actor := fmt.Sprintf(`{"kind":"session","id":"s-%s-%s","origin":{"session_title":%q}}`,
		role, issue, "Legion "+role+" · "+issue)
	c.exec(t, "write an event of "+issue, `
		insert into events (issue_key, seq, type, actor, payload, notify, created_at)
		values ($1, (select coalesce(max(seq), 0) + 1 from events where issue_key = $1),
		        $2, $3::jsonb, $4::jsonb, false, $5)
	`, issue, kind, actor, payload, at)
}

// rows is every stored row of the history's project, by issue key, with every column (readWhole).
func (c *consumer) rows(t *testing.T) map[string]Row {
	t.Helper()
	stored, err := readWhole(t.Context(), c.database.Pool, c.fixture.Project)
	if err != nil {
		t.Fatalf("read the stored facts: %v", err)
	}
	return stored
}

// cursor is where one cursor stands and the moment the pass that wrote it computed as of.
type cursor struct {
	position  int64
	updatedAt time.Time
}

// cursors is the three cursor rows, by name, and no entry for a cursor with no row.
func (c *consumer) cursors(t *testing.T) map[string]cursor {
	t.Helper()
	rows, err := c.database.Pool.Query(t.Context(), `select name, position, updated_at from issue_facts_cursor`)
	if err != nil {
		t.Fatalf("read the cursors: %v", err)
	}
	defer rows.Close()
	cursors := map[string]cursor{}
	for rows.Next() {
		var name string
		var at cursor
		if err := rows.Scan(&name, &at.position, &at.updatedAt); err != nil {
			t.Fatalf("read the cursors: %v", err)
		}
		cursors[name] = at
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the cursors: %v", err)
	}
	return cursors
}

// head is where a feed itself stands, which is where a pass that read all of it leaves its
// cursor.
func (c *consumer) head(t *testing.T, statement string) int64 {
	t.Helper()
	var position int64
	if err := c.database.Pool.QueryRow(t.Context(), statement).Scan(&position); err != nil {
		t.Fatalf("read a feed's head (%s): %v", statement, err)
	}
	return position
}

// feedHeads is where the three feeds stand, by the cursor each is read by.
func (c *consumer) feedHeads(t *testing.T) map[string]int64 {
	t.Helper()
	return map[string]int64{
		eventsCursor:       c.head(t, `select coalesce(max(id), 0) from events`),
		observationsCursor: c.head(t, `select coalesce(max(id), 0) from legion_observations`),
		changesCursor:      c.head(t, `select coalesce(max(seq), 0) from delivery_changes`),
	}
}

// statedSet is the set the pass about to run states for the project: the rows its changes touch
// through the links as they stand (rowsTouchedBy) and the rows of the tree a moved issue left
// (leftTrees), which is rowsToCompute, read here from the same database at the same moment.
func statedSet(t *testing.T, run *consumer, keys []string, moved map[string]bool) []string {
	t.Helper()
	above, err := metrics.TreeLinks(t.Context(), run.database.Pool, run.fixture.Project)
	if err != nil {
		t.Fatalf("read the links above the project's issues: %v", err)
	}
	stated, err := rowsToCompute(t.Context(), run.database.Pool, run.fixture.Project, keys, moved, above)
	if err != nil {
		t.Fatalf("read the set the pass states: %v", err)
	}
	return stated
}

// outside is the keys of got that want does not hold.
func outside(got, want []string) []string {
	left := []string{}
	for _, key := range got {
		if !slices.Contains(want, key) {
			left = append(left, key)
		}
	}
	return left
}

// TestOnePassWritesEveryIssueWithPhaseWorkAndAdvancesEveryCursor: one pass over a seeded project
// writes a row for every issue the rig says carries Legion phase work, and leaves each of the
// three cursors at the head of its own feed.
func TestOnePassWritesEveryIssueWithPhaseWorkAndAdvancesEveryCursor(t *testing.T) {
	run := newConsumer(t, 4)
	// A merged pull request of one issue, whose insert the delivery trigger logs as a change,
	// so the third feed has a row for the pass to read and its cursor a position to move to.
	run.exec(t, "seed a merged pull request", `
		insert into delivery_pull_requests (repo, number, title, url, author, created_at, merged_at, issue_key)
		values ('cybermonklabs/legion', 1, 'A pull request', 'https://example.test/pull/1', 'alice', $1, $2, $3)
	`, run.at.Add(-2*time.Hour), run.at.Add(-time.Hour), run.fixture.Keys[1])

	changed := run.pass(t)
	stored := run.rows(t)

	for _, key := range run.fixture.PhaseWork {
		if _, has := stored[key]; !has {
			t.Errorf("%s carries Legion phase work and the pass stored no row for it", key)
		}
	}
	for _, key := range run.fixture.NoPhaseWork {
		if row, has := stored[key]; has {
			t.Errorf("%s has the stored row %s, and no Legion role worked it", key, show(row))
		}
	}
	if len(stored) != len(run.fixture.PhaseWork) {
		t.Errorf("the pass stored %d rows, want the %d issues with Legion phase work", len(stored), len(run.fixture.PhaseWork))
	}
	// A cursor stands at its feed's head once the pass has read it, with one exception the facts
	// feed makes for itself: the pass's own write appends an issue_facts change row for every row
	// it wrote, after it read that feed, so the delivery_changes head runs ahead of the cursor by
	// exactly those rows. Nothing is lost, and the second pass below is the proof: it reads them,
	// keeps none, writes nothing and brings every cursor to its head.
	heads, cursors := run.feedHeads(t), run.cursors(t)
	for _, name := range cursorNames {
		at, has := cursors[name]
		switch {
		case !has:
			t.Errorf("the %s cursor has no row after a pass", name)
		case at.position > heads[name]:
			t.Errorf("the %s cursor stands at %d, past its feed's head %d", name, at.position, heads[name])
		case at.position < heads[name] && name != changesCursor:
			t.Errorf("the %s cursor stands at %d, want its feed's head %d", name, at.position, heads[name])
		case at.position == 0:
			t.Errorf("the %s cursor stands at 0, so the pass read none of that feed", name)
		case !at.updatedAt.Equal(run.at):
			t.Errorf("the %s cursor is stamped %s, want the moment the pass computed as of, %s",
				name, at.updatedAt.UTC(), run.at.UTC())
		}
	}
	if rewritten := run.pass(t); len(rewritten) != 0 {
		t.Errorf("a second pass with nothing moved rewrote %d rows, want none: %v", len(rewritten), rewritten)
	}
	settled, afterwards := run.feedHeads(t), run.cursors(t)
	for _, name := range cursorNames {
		if afterwards[name].position != settled[name] {
			t.Errorf("after a second pass the %s cursor stands at %d, want its feed's head %d",
				name, afterwards[name].position, settled[name])
		}
	}
	t.Logf("one pass: %d issues seeded, %d with Legion phase work, %d rows stored, %d rows changed; cursors events=%d observations=%d delivery_changes=%d",
		len(run.fixture.Keys), len(run.fixture.PhaseWork), len(stored), len(changed),
		cursors[eventsCursor].position, cursors[observationsCursor].position, cursors[changesCursor].position)
}

// TestAnEventChangesTheIssuesRowAndItsAncestorsAndNoRowOutsideThatSet: an event on one issue
// moves that issue's row and its ancestors' rows, because an issue's hours carry the time it
// waited while a child worked, and moves no row outside that set. The clock moves ten minutes
// between the two passes, and that moves no row: an open issue's row is closed at the newest input
// its facts read (metrics.ProjectFactsClosed), so the change's own rows are the only rows that
// can move.
//
// The event is a role session's first write on the follow-up a person filed, which no Legion
// role had worked: the issue carries Legion phase work from that event on, so the pass gives it
// the row a reader takes for "Legion worked this issue".
func TestAnEventChangesTheIssuesRowAndItsAncestorsAndNoRowOutsideThatSet(t *testing.T) {
	run := newConsumer(t, 3)
	run.pass(t)
	if _, had := run.rows(t)[run.fixture.NoPhaseWork[0]]; had {
		t.Fatalf("%s has a stored row before any role worked it", run.fixture.NoPhaseWork[0])
	}

	child := run.fixture.NoPhaseWork[0]
	root := run.fixture.Roots[0]
	run.writeEvent(t, child, "implementer", "comment.created", `{"status":"in_progress"}`, run.at.Add(-10*time.Hour))

	want := statedSet(t, run, []string{child}, nil)
	run.at = run.at.Add(10 * time.Minute)
	changed := run.pass(t)
	strayed := outside(changed, want)

	if !slices.Contains(changed, child) {
		t.Errorf("the event is on %s and the pass changed %v, which does not hold it", child, changed)
	}
	if !slices.Contains(changed, root) {
		t.Errorf("%s is the root above the event's issue and the pass left its row alone; it changed %v", root, changed)
	}
	if len(strayed) > 0 {
		t.Errorf("the pass changed %d rows outside the set the change touches: %v", len(strayed), strayed)
	}
	if !slices.Equal(want, []string{root, child}) && !slices.Equal(want, []string{child, root}) {
		t.Errorf("the set the change touches is %v, want the issue %s and its root %s alone", want, child, root)
	}
	t.Logf("an event on %s: the set it touches is %v (%d keys), %d rows changed (%v), %d of them outside that set",
		child, want, len(want), len(changed), changed, len(strayed))
}

// TestAMovedParentLinkChangesTheMovedIssuesSubtree: an issue whose parent link moved changes its
// own row, its new ancestors' rows and the rows of its whole subtree, because the root and the
// tree depth of every issue below it moved with the link.
func TestAMovedParentLinkChangesTheMovedIssuesSubtree(t *testing.T) {
	run := newConsumer(t, 3)
	run.pass(t)
	before := run.rows(t)

	moved, under := run.fixture.Roots[1], run.fixture.Roots[0]
	run.exec(t, "move a root under another", `update issues set parent_key = $1 where key = $2`, under, moved)
	run.writeEvent(t, moved, "architect", "issue.updated", `{"changed":["parent"]}`, run.at.Add(-time.Hour))

	parents, err := metrics.TreeLinks(t.Context(), run.database.Pool, run.fixture.Project)
	if err != nil {
		t.Fatalf("read the links above the project's issues: %v", err)
	}
	want := statedSet(t, run, []string{moved}, map[string]bool{moved: true})
	changed := run.pass(t)
	strayed := outside(changed, want)
	after := run.rows(t)

	subtree := []string{}
	for key, parent := range parents {
		if parent == moved {
			subtree = append(subtree, key)
		}
	}
	slices.Sort(subtree)
	for _, key := range subtree {
		was, is, stored := before[key], after[key], false
		if _, stored = after[key]; !stored {
			continue
		}
		if was.RootKey == is.RootKey {
			t.Errorf("%s stands under the moved %s and its root is still %s", key, moved, is.RootKey)
		}
		if is.RootKey != under {
			t.Errorf("%s stands under the moved %s and its root is %s, want %s", key, moved, is.RootKey, under)
		}
		if !slices.Contains(changed, key) {
			t.Errorf("%s is in the moved subtree and the pass left its row alone", key)
		}
	}
	if len(strayed) > 0 {
		t.Errorf("the pass changed %d rows outside the set the move touches: %v", len(strayed), strayed)
	}
	t.Logf("%s moved under %s: the set it touches is %d keys, its subtree is %d (%v), %d rows changed, %d of them outside that set",
		moved, under, len(want), len(subtree), subtree, len(changed), len(strayed))
}

// TestAChildMovedBetweenClosedRootsChangesBothRootsRows: an issue moved from one tree to
// another takes its hours with it, so the root it left stops counting the time it waited while
// that issue worked and the root it joined starts. Both roots are closed, and no pass computes a
// row its inputs did not name, so a pass that stated the new tree alone would leave the old
// root's row wrong for good.
func TestAChildMovedBetweenClosedRootsChangesBothRootsRows(t *testing.T) {
	run := newConsumer(t, 3)
	run.pass(t)
	before := run.rows(t)

	from, to := run.fixture.Roots[1], run.fixture.Roots[0]
	child := run.fixture.Keys[7]
	for _, key := range []string{from, to, child} {
		if before[key].ClosedAt == nil {
			t.Fatalf("%s is open, and this check needs the two roots and the child closed", key)
		}
	}
	if before[child].RootKey != from {
		t.Fatalf("%s stands under %s before the move, want %s", child, before[child].RootKey, from)
	}

	run.exec(t, "move a child to another tree", `update issues set parent_key = $1 where key = $2`, to, child)
	run.writeEvent(t, child, "architect", "issue.updated", `{"changed":["parent"]}`, run.at.Add(-time.Hour))

	want := statedSet(t, run, []string{child}, map[string]bool{child: true})
	changed := run.pass(t)
	strayed := outside(changed, want)
	after := run.rows(t)

	if after[child].RootKey != to {
		t.Errorf("%s moved to %s and its stored root is %s", child, to, after[child].RootKey)
	}
	if !slices.Contains(changed, to) {
		t.Errorf("%s is the root %s moved to and the pass left its row alone; it changed %v", to, child, changed)
	}
	if !slices.Contains(changed, from) {
		t.Errorf("%s is the root %s left and the pass left its row alone; it changed %v", from, child, changed)
	}
	if !slices.Contains(want, from) {
		t.Errorf("the set the pass stated is %v, which does not hold the root %s left", want, from)
	}
	if len(strayed) > 0 {
		t.Errorf("the pass changed %d rows outside the set it stated: %v", len(strayed), strayed)
	}
	t.Logf("%s moved from %s to %s, both closed: the pass stated %v (%d keys), %d rows changed (%v), %d of them outside that set; the child's stored root went %s to %s, the root it left holds %v and the root it joined %v",
		child, from, to, want, len(want), len(changed), changed, len(strayed),
		before[child].RootKey, after[child].RootKey, after[from].HoursByPhase, after[to].HoursByPhase)
}

// TestAPassWithNoInputMovingWritesNoRowOpenOrClosed: with no input moving, passes ten minutes,
// two hours and three days after the first write no row, open or closed, and log no change of the
// facts, because a row is closed at the newest input its facts read and no clock moves it
// (metrics.ProjectFactsClosed). The open issue's facts still grow: a read ten minutes on answers
// its work hours grown by ten minutes at the rate its row stores, and its last moment of work at
// the read's clock, while a closed issue reads as stored.
func TestAPassWithNoInputMovingWritesNoRowOpenOrClosed(t *testing.T) {
	run := newConsumer(t, 3)
	run.pass(t)
	before := run.rows(t)
	logged := run.factsChanges(t)

	open := run.fixture.Open[0]
	closed := ""
	for _, key := range run.fixture.PhaseWork {
		if !slices.Contains(run.fixture.Open, key) && !slices.Contains(run.fixture.Roots, key) &&
			!slices.Contains(run.fixture.NoMeasuredWork, key) {
			closed = key
			break
		}
	}
	if closed == "" {
		t.Fatal("the rig wrote no closed issue with measured work, so this check has nothing to hold still")
	}
	if rate := before[open].Growth["work_hours"]; rate != 1 {
		t.Fatalf("%s is open and its row stores a work-hours growth of %g, want 1: this check reads the growth of an issue at work", open, rate)
	}
	if len(before[closed].Growth) != 0 {
		t.Errorf("%s is closed and its row stores the growth %v, want none", closed, before[closed].Growth)
	}

	started := run.at
	for _, later := range []time.Duration{10 * time.Minute, 2 * time.Hour, 72 * time.Hour} {
		run.at = started.Add(later)
		if changed := run.pass(t); len(changed) != 0 {
			t.Errorf("a pass %s on with no input moving changed %d rows: %v", later, len(changed), changed)
		}
	}
	after := run.rows(t)
	moved := 0
	for key, was := range before {
		if difference := rowDifference(after[key], was); difference != "" {
			moved++
			t.Errorf("%s moved over passes with no input moving: %s", key, difference)
		}
	}
	if more := run.factsChanges(t) - logged; more != 0 {
		t.Errorf("passes with no input moving logged %d issue_facts changes, want none", more)
	}

	readAt, tenOn := started, started.Add(10*time.Minute)
	first, err := Read(t.Context(), run.database.Pool, run.fixture.Project, readAt)
	if err != nil {
		t.Fatalf("read the facts at %s: %v", readAt, err)
	}
	second, err := Read(t.Context(), run.database.Pool, run.fixture.Project, tenOn)
	if err != nil {
		t.Fatalf("read the facts at %s: %v", tenOn, err)
	}
	grew := second[open].WorkHours - first[open].WorkHours
	if math.Abs(grew-tenOn.Sub(readAt).Hours()) > 1e-3 {
		t.Errorf("%s is open and its work hours read %g then %g ten minutes on, a growth of %g, want %g",
			open, first[open].WorkHours, second[open].WorkHours, grew, tenOn.Sub(readAt).Hours())
	}
	if last := second[open].WorkLastAt; last == nil || !last.Equal(tenOn) {
		t.Errorf("%s is open and at work, and its last moment of work reads %v at %s, want the read's clock", open, utc(last), tenOn.UTC())
	}
	if !sameRow(second[closed], first[closed]) {
		t.Errorf("%s is closed and its row read ten minutes on moved:\n\tgot  %s\n\twant %s", closed, show(second[closed]), show(first[closed]))
	}
	t.Logf("three passes with no input moving, %s to %s on: %d rows of %d moved and %d changes logged; %s (open) reads %g work hours then %g ten minutes on, %s (closed) reads as stored",
		10*time.Minute, 72*time.Hour, moved, len(after), run.factsChanges(t)-logged, open, first[open].WorkHours, second[open].WorkHours, closed)
}

// factsChanges is how many changes of kind issue_facts the change log and its outbox hold, which
// a write of a stored row records (migration 20261115155340).
func (c *consumer) factsChanges(t *testing.T) int {
	t.Helper()
	var count int
	if err := c.database.Pool.QueryRow(t.Context(), `
		select (select count(*) from delivery_changes where kind = 'issue_facts')
		     + (select count(*) from delivery_changes_pending where kind = 'issue_facts')
	`).Scan(&count); err != nil {
		t.Fatalf("count the issue_facts changes: %v", err)
	}
	return count
}

// TestABackfillOverAnEmptiedTableRestoresEveryRow: a project whose last whole computation
// answered rows and which holds none now has lost them to something this loop did not do, and
// no narrow pass can name a row that is not there, so the pass reads the project whole
// (legionProjects, over the mark that whole read wrote). An emptied table is that case, and the
// rows it restores are the rows it held.
func TestABackfillOverAnEmptiedTableRestoresEveryRow(t *testing.T) {
	run := newConsumer(t, 3)
	run.pass(t)
	before := run.rows(t)
	if len(before) == 0 {
		t.Fatal("the first pass stored no row, so this check has nothing to restore")
	}

	run.exec(t, "empty the facts", `delete from issue_facts`)
	changed := run.pass(t)
	after := run.rows(t)

	if len(after) != len(before) {
		t.Errorf("the backfill restored %d rows, want the %d it held", len(after), len(before))
	}
	for key, was := range before {
		is, has := after[key]
		if !has {
			t.Errorf("the backfill restored no row for %s", key)
			continue
		}
		if !sameRow(is, was) {
			t.Errorf("the backfill restored %s as\n\tgot  %s\n\twant %s", key, show(is), show(was))
		}
	}
	t.Logf("a backfill over an emptied table: %d rows held, %d restored, %d of them written by the backfill",
		len(before), len(after), len(changed))
}

// TestAPassThatFailsPartWayLeavesItsCursorsWhereTheyWere: a cursor moves in the transaction that
// wrote the rows it covers, so a write that fails leaves every cursor where the last committed
// pass left it, and the next pass reads the same rows again.
func TestAPassThatFailsPartWayLeavesItsCursorsWhereTheyWere(t *testing.T) {
	run := newConsumer(t, 2)
	run.pass(t)
	before := run.cursors(t)

	// A trigger that refuses every write of a row: the pass computes its rows, the write of the
	// first of them fails, and the cursors it would have moved are in that transaction.
	run.exec(t, "refuse every write of a row", `
		create function refuse_issue_facts() returns trigger language plpgsql as $$
		begin raise exception 'this write is refused'; end $$;
		create trigger refuse_issue_facts before insert on issue_facts
		for each row execute function refuse_issue_facts()
	`)
	run.writeEvent(t, run.fixture.Open[0], "implementer", "comment.created", `{}`, run.at.Add(-time.Hour))
	run.at = run.at.Add(10 * time.Minute)

	changed, err := run.attempt(t)
	if err == nil {
		t.Fatalf("the pass over a table that refuses every write answered no error, and changed %v", changed)
	}
	after := run.cursors(t)

	if len(after) != len(before) {
		t.Errorf("the failed pass left %d cursor rows, want the %d the pass before it wrote", len(after), len(before))
	}
	for name, was := range before {
		is := after[name]
		if is.position != was.position {
			t.Errorf("the failed pass moved the %s cursor from %d to %d", name, was.position, is.position)
		}
		if !is.updatedAt.Equal(was.updatedAt) {
			t.Errorf("the failed pass stamped the %s cursor %s, from %s", name, is.updatedAt.UTC(), was.updatedAt.UTC())
		}
	}
	t.Logf("a pass that failed part way: %d cursors held at events=%d observations=%d delivery_changes=%d, stamped %s; the error was: %v",
		len(after), after[eventsCursor].position, after[observationsCursor].position,
		after[changesCursor].position, after[eventsCursor].updatedAt.UTC(), err)
}

// TestTheSetOfRowsOneChangeTouches: the set a change touches is the issues named, the issues
// above them and the issues below them. It is the set the pass computes before it writes, and
// no row outside it is written.
//
// The issues below are in it whatever the change was (rowsTouchedBy).
func TestTheSetOfRowsOneChangeTouches(t *testing.T) {
	// One tree: ROOT over MIDDLE over LEAF and SIBLING, with OTHER a root of its own, and a
	// pair whose parent links point at each other, which the walk must not follow for ever.
	parents := map[string]string{
		"ROOT": "", "MIDDLE": "ROOT", "LEAF": "MIDDLE", "SIBLING": "MIDDLE",
		"OTHER": "", "LOOP-1": "LOOP-2", "LOOP-2": "LOOP-1",
	}
	for _, check := range []struct {
		what string
		keys []string
		want []string
	}{
		{"an issue and the issues above it", []string{"LEAF"}, []string{"LEAF", "MIDDLE", "ROOT"}},
		{"a root, with the issues below it", []string{"ROOT"}, []string{"LEAF", "MIDDLE", "ROOT", "SIBLING"}},
		{"two issues of one tree", []string{"LEAF", "SIBLING"}, []string{"LEAF", "MIDDLE", "ROOT", "SIBLING"}},
		{"an issue with issues above it and below it", []string{"MIDDLE"},
			[]string{"LEAF", "MIDDLE", "ROOT", "SIBLING"}},
		{"a leaf, which has no issue below it", []string{"SIBLING"},
			[]string{"MIDDLE", "ROOT", "SIBLING"}},
		{"an issue of another tree", []string{"OTHER"}, []string{"OTHER"}},
		{"a parent chain that loops", []string{"LOOP-1"}, []string{"LOOP-1", "LOOP-2"}},
	} {
		got := rowsTouchedBy(check.keys, parents)
		if !slices.Equal(got, check.want) {
			t.Errorf("%s: %v touches %v, want %v", check.what, check.keys, got, check.want)
		}
		t.Logf("%s: %v touches %d rows (%s)", check.what, check.keys, len(got), strings.Join(got, " "))
	}
}

// TestCursorAgeAnswersTheOldestCursor: the board's header reads how fresh the served facts are
// from the cursors, and a database no pass has committed on has no cursor row at all.
func TestCursorAgeAnswersTheOldestCursor(t *testing.T) {
	run := newConsumer(t, 1)

	age, has, err := CursorAge(t.Context(), run.database.Pool, run.at)
	if err != nil {
		t.Fatalf("read the cursors' age: %v", err)
	}
	if has {
		t.Errorf("a database no pass has committed on answers an age of %s", age)
	}

	run.pass(t)
	age, has, err = CursorAge(t.Context(), run.database.Pool, run.at.Add(5*time.Minute))
	if err != nil {
		t.Fatalf("read the cursors' age: %v", err)
	}
	if !has {
		t.Fatal("a database one pass has committed on answers no age")
	}
	if age != 5*time.Minute {
		t.Errorf("five minutes after the pass the facts are %s old, want 5m0s", age)
	}
	t.Logf("the cursors' age: none before a pass, %s five minutes after one", age)
}

// TestAPassHoldsTheFreshnessRequirement: a stored row stands no more than Freshness behind the
// event that changed it, at the rig's own population and at four times it. No pass runs on a
// timer: a commit wakes the loop, whose next pass starts at most PassGap after the one before it,
// and a process that finds the lease held tries again PassGap later and up to a tenth again. A
// pass therefore has Freshness less one gap and one retry, 39 s, to run in.
//
// Each size takes two lengths and two reads: one whole read of the project (ComputeProject and
// WriteProject), which a new build makes once per project; one pass that reads one event of one
// open issue, which is what a wake runs; and the read of the rows another build wrote
// (otherBuildsRows), which every first page of the build a project's record names makes, timed
// with none to find, as a settled deployment holds, and with every row of the project written by
// another build, as a roll's first pass can find them.
//
// INFRA holds about 252 issues today, which is the rig's own size, so the second size covers
// INFRA at twice its size and leaves room above that: a reader sees how the costs grow with the
// population rather than one number at one size.
func TestAPassHoldsTheFreshnessRequirement(t *testing.T) {
	budget := Freshness - PassGap - (PassGap + PassGap/10)
	for _, size := range []struct {
		what  string
		trees int
	}{
		{"the rig's own population", legiontest.DefaultTrees},
		{"four times the rig's population", 4 * legiontest.DefaultTrees},
	} {
		t.Run(size.what, func(t *testing.T) {
			run := newConsumer(t, size.trees)
			project := run.fixture.Project

			started := time.Now()
			rows, _, err := ComputeProject(t.Context(), run.database.Pool, project, run.at)
			if err != nil {
				t.Fatalf("compute the project's facts: %v", err)
			}
			written, err := WriteProject(t.Context(), run.database.Pool, ProjectWrite{Project: project, Rows: rows, Whole: true, Build: testBuild})
			if err != nil {
				t.Fatalf("write the facts: %v", err)
			}
			whole := time.Since(started)

			// The pass that reads the three feeds through to their heads and records the whole
			// read, so the pass after the event reads that event alone.
			run.pass(t)
			open := run.fixture.Open[0]
			run.writeEvent(t, open, "implementer", "issue.updated", `{"status":"in_progress"}`, run.at.Add(-time.Minute))
			started = time.Now()
			changed := run.pass(t)
			woken := time.Since(started)
			if !slices.Contains(changed, open) {
				t.Errorf("the pass after an event on %s changed %v, which does not hold it", open, changed)
			}

			settled, settledRows := timeOtherBuildsRows(t, run)
			run.exec(t, "stamp every row with another build", `update issue_facts set written_build = $1 where project_key = $2`, otherBuild, project)
			rolled, rolledRows := timeOtherBuildsRows(t, run)

			for _, length := range []struct {
				what string
				took time.Duration
			}{{"one whole read", whole}, {"one pass after an event", woken}} {
				if length.took >= budget {
					t.Errorf("at %d issues %s takes %s, above the %s a pass has to run in",
						len(run.fixture.Keys), length.what, length.took.Round(time.Millisecond), budget)
				}
			}
			if stored := len(run.rows(t)); rolledRows != stored {
				t.Errorf("with every row of %s written by another build the read answered %d rows, want the %d stored", project, rolledRows, stored)
			}
			t.Logf("over %d issues, %d of them with Legion phase work: one whole read took %s and wrote %d rows; one pass after an event took %s and changed %d rows; against the %s a pass has",
				len(run.fixture.Keys), len(run.fixture.PhaseWork), whole.Round(time.Millisecond), len(written),
				woken.Round(time.Millisecond), len(changed), budget)
			t.Logf("the read of the rows another build wrote took %s finding %d rows, and %s finding %d with every row of the project written by another build",
				settled.Round(10*time.Microsecond), settledRows, rolled.Round(10*time.Microsecond), rolledRows)
		})
	}
}

// timeOtherBuildsRows times otherBuildsRows under the consumer's build, best of five, and answers
// how many rows of the history's project it found.
func timeOtherBuildsRows(t *testing.T, run *consumer) (time.Duration, int) {
	t.Helper()
	best, found := time.Duration(0), 0
	for attempt := range 5 {
		started := time.Now()
		other, err := otherBuildsRows(t.Context(), run.database.Pool, run.deps.Build)
		took := time.Since(started)
		if err != nil {
			t.Fatalf("read the rows another build wrote: %v", err)
		}
		if attempt == 0 || took < best {
			best = took
		}
		found = len(other[run.fixture.Project])
	}
	return best, found
}
