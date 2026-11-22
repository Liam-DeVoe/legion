package evals

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/delivery"
	"github.com/sjawhar/envoy/internal/dispatch/delivery/measures"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// refresher_work.go is what one refresher pass does with what its feeds read (refresher.go has the
// loop): which days each change and event touches, the writes of those days, and the walks.
//
// What each change touches, the oracle dispatch://INFRA-2916's k12 reads:
//
//   - pull_request: the pull request's merge day, in each project that day counts it for now, and
//     the day and project eval_day_members holds it under (where it was counted before), so a
//     pull request merged again or moved to another project rewrites both days; then the start
//     day of the deploy run that ships it now and of the one that shipped it when it was last
//     counted (eval_day_members.shipped_by), each of which counts it among the pull requests it
//     ships and counts for its projects (D1), in the run's projects now and before.
//   - run and run_job: the run's start day, its delivery rows (a deploy) and its CI rows (every
//     run on main of the deploy workflow and every PR-checks run), in each project that day counts
//     it for and the projects eval_day_members holds it under; then the merge day of each pull
//     request the run ships now (delivery/containment.go's rule, DeliveryDay.linked) and of each
//     it shipped when that pull request was last counted (shipped_by), whose rows hold the
//     completion of the run that ships it as their deploy time.
//   - settings: a change of the delivery settings rewrites, in one transaction, the days of the
//     delivery and CI evals it moves whose rows it can move, from their first input day on, and
//     begins no walk (settings_rewrite.go: settingsMoves names what each setting moves and
//     movedDeliveryDays the days it can move, and the pass finds the change by comparing the
//     settings it reads with those the rows stand built under, so one that moves no eval
//     rewrites nothing, a move of the deploy repository among them, whose evals are kept for
//     every repository that holds a run, deploy_repos.go); a change of repo_projects
//     (whose change names the repository) begins the delivery walk again, and every project's
//     Legion walk too, since the mapping decides the project a row counts for and the repository
//     an issue or a filing with none of its own resolves to (ReadLegionDay).
//   - issue_facts: the issue's Legion close day before the change and after it, which the change
//     names itself (t_old and t_new, migration 20261117100002) and no read is made to find: a
//     Legion day counts an issue on its close day alone, so an issue closed again on another day
//     rewrites its old day and its new one, and an open issue whose facts moved touches no day.
//     eval_day_members is not read for it, since the day it holds the issue under is the close
//     day before the change, which the change already names: the days rewritten are the days
//     the change marks, and no other.
//   - issue_filings: the filing's day before and after, in the filing's project.
//   - eval_readings: no day, since the readings write keeps a logged eval's day rows itself; the
//     eval's zero rows are filled up to today, which a reading measured before its eval's first
//     day leaves a gap in.
//   - session_group, eval_versions and eval_days: no day.
//
// The event log adds two more: an issue event that can move the count of open P0 issues with no
// owner (a change of an issue's priority, status or route, its creation or close, a claim or a
// release, on an issue that is a P0 or whose priority moved) rewrites the project's unowned-p0
// row of today; and one that moves an issue's repository or its parent rewrites the days of the
// filings of that issue and of every issue below it, whose repository resolves through it.
//
// A day a change names is written from its own input, and every day it does not name is the
// walk's (refresh.walkDays), so the stored rows are a function of the inputs and the settings
// alone, whichever order the inputs arrived in (dispatch://INFRA-2916, items 5 and 8):
//
//   - the write holds the rows the day builds and deletes the rows of the day it no longer builds
//     (deleteStaleDays), and writes the zero row of each series the day has no input of from the
//     series' first input day on only where the running build's walk has computed the day (a day
//     before its next day): on a day the walk has not computed yet, or while it has no record or
//     begins again, the day holds its rows with input alone, and reads "no data" where it has
//     none, until the walk computes it (walkNext);
//   - a write that moves a series' first input day earlier, or creates one, moves the walk of its
//     family back to the day after it, and one that finds the day was a series' first input day
//     and holds no input of it any more drops that zero row and moves the walk back to the day
//     itself, each in the write's own transaction and never later than the walk stands
//     (ProjectDay.Rewind): the walk then builds every day after the write's from their own input;
//   - a change in the repositories that hold a delivery run, a new one's first runs, moves the
//     delivery walk back to the earliest start that changed, in the page's commit (syncKept);
//   - the walk computes every day it reaches, a day a page of the same pass wrote included, and
//     every day holding rows of its family, whether or not it builds a row of it; each series'
//     first input day it builds by is the first it holds among the days before the walk's next
//     day, which the walk built, or the first it builds itself (refresh.firstOf).

// refresh is one pass's state: the build and the day it writes as, the projects and the delivery
// settings it read, the first input day of each project's series it has read (of each deploy
// repository's rows of it, evalKey), each project's LegionSince, what it has written, and the
// page's own reads and writes: the delivery days it has read, the days it has written and the
// projects whose unowned-p0 row of today it has written. now is the pass's moment, which every row
// is built as of; clock is the refresher's clock, which each page reads afresh for its change
// cursor (page).
type refresh struct {
	store      *Store
	build      string
	now, today time.Time
	clock      func() time.Time
	batch      int
	projects   []string
	known      map[string]bool
	// settings is nil where no delivery settings exist, which leaves the delivery and CI rows
	// unwritten: without them no production rule judges a run and no deploy card follows a job.
	settings *delivery.DeliverySettings
	// builtUnder is the delivery settings the stored delivery and CI rows stand built under
	// (eval_days_settings), read by the pass's first page that has settings (builtRead) and moved by
	// each of its pages that brings the rows to the settings it reads (syncSettings); nil where
	// none are recorded. keptUnder is the repositories the rows stand built for, read with it and
	// moved by each page that reads another set (syncKept).
	builtUnder *delivery.DeliverySettings
	keptUnder  []keptRepo
	builtRead  bool
	// first is the first input day of each project's series the store holds (storedFirst), read
	// once a pass and moved by each day a page writes (noteBuilt); walkFirst is the walk's own,
	// read of the days before walkFrom, where each walk's batch began, and moved by each day the
	// walk writes (firstOf).
	first     map[walkKey]map[evalKey]time.Time
	walkFirst map[walkKey]map[evalKey]time.Time
	walkFrom  map[walkKey]time.Time
	since     map[string]LegionSince
	// reads, done and unowned hold for one page (page sets them anew) and then the walks: a page
	// writes a day once, from one read of it, and a change to that day committed after the read
	// is on a later page, which must read and write the day again. Kept for the whole pass, they
	// would let that page skip the day while its cursor moved past the change, and the day would
	// stand stale until another change named it. The walk sets done anew too, since it computes
	// every day it reaches, a day a page wrote included. repos is the page's one read of
	// repo_projects, which every delivery day it reads attributes by, and which a page that reads
	// a change of a mapping (a settings change) reads after loading that change, since it starts
	// empty; and deployRepos is the page's one read of the repositories whose rows of the evals
	// that read the deploy repository are kept (readDeployRepos), made after the page has read its
	// changes, so a repository whose first run is among them has its rows written.
	reads       map[time.Time]deliveryRead
	done        map[builtDay]bool
	unowned     map[string]bool
	repos       *repoProjects
	deployRepos []keptRepo
	// walks is the page's one read of the running build's walk records (walkNext), begun the
	// families whose walks the page begins again, and rewound the day each walk of the page's
	// writes moved back to (ProjectDay.Rewind) or that its commit moves back to (syncKept): what
	// tells a day the walk has computed from one it has not.
	walks   map[walkKey]walkState
	begun   map[string]bool
	rewound map[walkKey]time.Time
	// walkApplies is the deploy runs of each kept repository the delivery walk's batch read once
	// from walkAppliesFrom on (deliveryWalkDay), for the pass's walk alone.
	walkApplies     map[string]*delivery.ApplyRows
	walkAppliesFrom time.Time
	// walkHeld is each project's day of each walk's batch that holds rows of the walk's family,
	// which the walk writes whether or not it builds a row of it (heldDays).
	walkHeld map[builtDay]bool
	// walking is set while the pass walks, whose days run in order from the first input day or
	// from days the walk built, so they move no walk back and write every zero row from each
	// series' first input day on, and whose days' stored figures are folded once the walks'
	// batches are written (foldWalked) rather than in each day's own transaction.
	walking bool
	// walked is the span of days the pass's walks wrote, by family and project (noteWalked).
	walked map[walkKey]daySpan
	// eventsFrom is where the event cursor stood when the pass's first page read it (paged is set
	// once one has), and eventsTo where the last page the pass committed left it (commitPage): the
	// span of the event log the pass read past, which owedFacts reads.
	eventsFrom, eventsTo int64
	paged                bool
	report               refreshed
}

// load reads the projects and the delivery settings and seeds the projects the pass owes a seed
// (the seed's rules, seed.go): every project on the first pass of this process, so the catalogue
// this build states reaches every project; on a later pass every project no seed has reached
// (UnseededProjects: one a command or a fixture wrote by SQL while the server ran); and on every
// pass each project whose seed failed before, and each whose seed was refused for its `evals-md`
// once that artifact is a document (seedRefusals). A project whose seed fails is logged and seeded
// again by the next pass; its day rows are written all the same, since a day row needs no
// definition. seeds is the loop's, which each pass reads and moves.
func (r *refresh) load(ctx context.Context, seeds *seeding) error {
	var err error
	if r.projects, err = r.store.Projects(ctx); err != nil {
		return err
	}
	r.known = map[string]bool{}
	for _, project := range r.projects {
		r.known[project] = true
	}
	owed := r.projects
	if seeds.everyProject {
		if owed, err = r.store.UnseededProjects(ctx); err != nil {
			return err
		}
	}
	if owed, err = seeds.refused.pending(ctx, r.store.database.Pool, slices.Concat(owed, seeds.owed())); err != nil {
		return err
	}
	for _, project := range owed {
		_, err := r.store.Ensure(ctx, project)
		seeds.note(project, err)
		r.report.seeded = append(r.report.seeded, project)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			slog.Warn("dispatch: the eval day-row refresher could not seed a project's evals; the next pass tries again",
				"project", project, "error", err)
		}
	}
	seeds.everyProject = true
	return r.loadSettings(ctx)
}

// loadSettings reads the delivery settings, nil where none exist yet.
func (r *refresh) loadSettings(ctx context.Context) error {
	settings, err := delivery.GetSettings(ctx, r.store.database.Pool)
	switch {
	case errors.Is(err, delivery.ErrNoSettings):
		r.settings = nil
	case err != nil:
		return fmt.Errorf("read the delivery settings: %w", err)
	default:
		r.settings = &settings
	}
	return nil
}

// pageWork is what one page of the feeds names: the pull requests and runs whose rows changed, by
// row key; the Legion days the facts and filing changes name, by project; the issues whose
// repository or parent moved; the projects whose count of unowned P0 issues may have moved; the
// logged evals whose readings changed, by project; the walks to begin again; the delivery
// settings to record with the page's cursors as the ones the delivery and CI rows stand built
// under; and the repositories to record as the ones they stand built for, with the day the
// delivery walk moves back to for them.
type pageWork struct {
	prs        map[string]pullRequestRef
	runs       map[string]runRef
	legion     map[string]map[time.Time]bool
	reparented map[string]bool
	unowned    map[string]bool
	logged     map[string]map[string]bool
	// restart names the families whose walks begin again: walkDelivery, walkLegion (every
	// project's) and walkLogged (every project's).
	restart  map[string]bool
	settings bool
	// built is the delivery settings commitPage records in eval_days_settings with the cursors and
	// the walks the page begins again (syncSettings), nil for none.
	built *delivery.DeliverySettings
	// kept is the repositories that hold a delivery run commitPage records in eval_days_settings,
	// nil where they are the ones recorded, and keptRewind the day it moves the delivery walk back
	// to for them, nil for none (syncKept).
	kept       []keptRepo
	keptRewind *time.Time
}

// pullRequestRef is a pull request's row by its key; runRef a run's.
type pullRequestRef struct {
	repo   string
	number int
}

type runRef struct {
	repo string
	id   int64
}

func newPageWork() *pageWork {
	return &pageWork{
		prs: map[string]pullRequestRef{}, runs: map[string]runRef{}, legion: map[string]map[time.Time]bool{},
		reparented: map[string]bool{}, unowned: map[string]bool{}, logged: map[string]map[string]bool{},
		restart: map[string]bool{},
	}
}

// restartAll begins every walk again: a database no pass has committed on, or a change cursor
// whose change left the log before it passed its retention, which lost changes no one can name.
func (w *pageWork) restartAll() {
	w.restart[walkDelivery], w.restart[walkLegion], w.restart[walkLogged] = true, true, true
}

// addChange records what change touches (refresher_work.go's list above). A ref that names no row
// (a pull request number or a run id that is no number, or a pull request number past the 32 bits
// delivery_pull_requests.number holds) names nothing.
func (w *pageWork) addChange(change delivery.Change) {
	switch change.Kind {
	case delivery.ChangeKindPullRequest:
		if parsed, err := strconv.ParseInt(change.Ref, 10, 32); err == nil {
			number := int(parsed)
			w.prs[PullRequestMember(change.Repo, number)] = pullRequestRef{repo: change.Repo, number: number}
		}
	case delivery.ChangeKindRun, delivery.ChangeKindRunJob:
		if id, err := strconv.ParseInt(change.Ref, 10, 64); err == nil {
			w.runs[RunMember(change.Repo, id)] = runRef{repo: change.Repo, id: id}
		}
	case delivery.ChangeKindSettings:
		// The page reads the settings again and rewrites what they moved (syncSettings). A
		// repository's mapping moves which project every row of the repository counts for, so it
		// begins the walks again.
		w.settings = true
		if change.Repo != "" {
			w.restart[walkDelivery], w.restart[walkLegion] = true, true
		}
	case delivery.ChangeKindIssueFacts, delivery.ChangeKindIssueFilings:
		// Each names its Legion days itself, in its project: a facts change the issue's close day
		// before and after it, a filing change the filing's day before and after it, nil where
		// there is none.
		for _, at := range []*time.Time{change.TOld, change.TNew} {
			if at != nil {
				if w.legion[change.Repo] == nil {
					w.legion[change.Repo] = map[time.Time]bool{}
				}
				w.legion[change.Repo][measures.Midnight(*at)] = true
			}
		}
	case delivery.ChangeKindEvalReadings:
		if w.logged[change.Repo] == nil {
			w.logged[change.Repo] = map[string]bool{}
		}
		w.logged[change.Repo][change.Ref] = true
	}
}

// addEvents records what a page of events touches.
func (w *pageWork) addEvents(page eventPage) {
	for _, project := range page.unowned {
		w.unowned[project] = true
	}
	for _, issue := range page.reparented {
		w.reparented[issue] = true
	}
}

// restarts is the walks w begins again, over projects.
func (w *pageWork) restarts(projects []string) []walkKey {
	var keys []walkKey
	if w.restart[walkDelivery] {
		keys = append(keys, walkKey{family: walkDelivery})
	}
	for _, family := range []string{walkLegion, walkLogged} {
		if w.restart[family] {
			for _, project := range projects {
				keys = append(keys, walkKey{family: family, project: project})
			}
		}
	}
	return keys
}

// page is one page of each feed: it reads where each cursor stands and a page of each feed after
// it, brings the delivery and CI rows to the delivery settings it reads (syncSettings), writes the
// days the page names, and moves the cursors in a transaction of its own, which also records the
// walks the page begins again. It answers whether a feed holds more rows. The pass's first page
// records where the event cursor stood before it (eventsFrom), where the pass's reading of the
// event log began (owedFacts).
//
// A database holding no cursor row has committed no pass: the cursors start at the feeds' heads,
// and every walk begins, which builds every day the feeds' history could name. A change cursor
// that may have passed over changes it never read (delivery.CursorLost, judged on the database's
// clock for the change and on the page's own reading of the refresher's clock against when a page
// last read the change log to its end) moves to the log's head, and every walk begins again,
// which covers every change the log ever held.
//
// The page reads the clock once, before its reads, and that one reading is what the lost-cursor
// check is judged at and what a read of the change log to its end stamps (delivery.ChangeCursor
// and its Through): a page shorter than limit, a move to the head, and the first read. A full page
// moves the change cursor and carries its ReadAt forward, so a pass that catches up a backlog of
// changes, judged page by page, finds the cursor lost once the retention has passed since the last
// read of the whole log, a prune having taken changes after it meanwhile. The rows are built as of
// the pass's moment (now), whatever the page's reading.
func (r *refresh) page(ctx context.Context, safe int64, limit int) (bool, error) {
	r.beginPage()
	checked := r.clock()
	cursors, committed, err := r.store.readRefresherCursors(ctx)
	if err != nil {
		return false, err
	}
	if !r.paged {
		r.paged, r.eventsFrom = true, cursors.events
	}
	work := newPageWork()
	var moved feedCursors
	full := false
	if !committed {
		head, err := delivery.ChangesHead(ctx, r.store.database.Pool)
		if err != nil {
			return false, fmt.Errorf("read the head of the delivery changes: %w", err)
		}
		head.ReadAt = checked
		moved = feedCursors{changes: head, events: safe}
		work.restartAll()
	} else {
		after := cursors.changes
		lost, err := delivery.CursorLost(ctx, r.store.database.Pool, after, checked)
		if err != nil {
			return false, fmt.Errorf("read the delivery change at %d: %w", after.Seq, err)
		}
		if lost {
			if moved.changes, err = delivery.ChangesHead(ctx, r.store.database.Pool); err != nil {
				return false, fmt.Errorf("read the head of the delivery changes: %w", err)
			}
			moved.changes.ReadAt = checked
			work.restartAll()
		} else {
			changes, err := delivery.ChangesSince(ctx, r.store.database.Pool, after.Seq, limit)
			if err != nil {
				return false, fmt.Errorf("read the delivery changes after %d: %w", after.Seq, err)
			}
			moved.changes = after.Through(changes, limit, checked)
			for _, change := range changes {
				work.addChange(change)
			}
			full = len(changes) == limit
		}
		events, err := r.store.readRefresherEvents(ctx, cursors.events, safe, limit)
		if err != nil {
			return false, err
		}
		moved.events = events.position
		full = full || events.full
		work.addEvents(events)
	}
	if work.settings {
		// The settings or a mapping moved: the first days this pass read before were read under
		// the old ones, so each is read again.
		if err := r.loadSettings(ctx); err != nil {
			return false, err
		}
		for _, project := range r.projects {
			delete(r.first, walkKey{family: walkDelivery, project: project})
		}
	}
	// The walks this page begins again have computed no day the page writes (walkNext);
	// syncSettings may begin the delivery walk again too, which the same map holds.
	r.begun = work.restart
	if err := r.syncSettings(ctx, work); err != nil {
		return false, err
	}
	if err := r.syncKept(ctx, work); err != nil {
		return false, err
	}
	if err := r.apply(ctx, work); err != nil {
		return false, err
	}
	if err := r.commitPage(ctx, moved, work.restarts(r.projects), work); err != nil {
		return false, err
	}
	return full, nil
}

// beginPage starts a page: its delivery reads, the days it wrote, the unowned-p0 rows it wrote,
// its reads of repo_projects, of the repositories whose rows are kept per deploy repository and
// of the running build's walks, and the walks it begins again or moves back, begin empty
// (refresh's rule on why they hold for one page).
func (r *refresh) beginPage() {
	r.reads, r.done, r.unowned, r.repos = map[time.Time]deliveryRead{}, map[builtDay]bool{}, map[string]bool{}, nil
	r.deployRepos = nil
	r.walks, r.begun, r.rewound = nil, nil, nil
}

// walkNext is the first day the running build's walk of family (project's, for the Legion walk)
// has not computed: it computed every day before it and builds every day from it on. It is nil
// where the walk has computed no day the page can rely on: no record of the running build's, a
// record to begin, or a walk this page begins again (begun), each of which builds every day from
// the first input day on. A day this page's writes moved the walk back to (rewound), or its commit
// will (syncKept), stands in for a later record. The records are read once a page.
func (r *refresh) walkNext(ctx context.Context, family, project string) (*time.Time, error) {
	if r.begun[family] {
		return nil, nil
	}
	if r.walks == nil {
		walks, err := r.store.readWalks(ctx, r.build)
		if err != nil {
			return nil, err
		}
		r.walks = walks
	}
	key := walkOf(family, project)
	next := r.walks[key].next
	if back, moved := r.rewound[key]; moved && next != nil && back.Before(*next) {
		next = &back
	}
	return next, nil
}

// walkOf is the walk that builds project's days of family: the one delivery walk of every
// project's delivery and CI rows, or project's Legion walk.
func walkOf(family, project string) walkKey {
	if family == walkDelivery {
		return walkKey{family: walkDelivery}
	}
	return walkKey{family: family, project: project}
}

// noteRewind records that the walk key stands no later than day once the page commits (walkNext).
func (r *refresh) noteRewind(key walkKey, day *time.Time) {
	if day == nil {
		return
	}
	if r.rewound == nil {
		r.rewound = map[walkKey]time.Time{}
	}
	if back, moved := r.rewound[key]; !moved || day.Before(back) {
		r.rewound[key] = *day
	}
}

// apply writes every day w names (writeDelivery and writeLegion say what each write writes), and
// then every day a write left owed (writeOwed).
func (r *refresh) apply(ctx context.Context, w *pageWork) error {
	if err := r.applyDelivery(ctx, w); err != nil {
		return err
	}
	if err := r.applyLegion(ctx, w); err != nil {
		return err
	}
	for _, project := range slices.Sorted(maps.Keys(w.unowned)) {
		if r.known[project] {
			if err := r.writeUnowned(ctx, project); err != nil {
				return err
			}
		}
	}
	for _, project := range slices.Sorted(maps.Keys(w.logged)) {
		if r.known[project] {
			if err := r.fillLogged(ctx, project, slices.Sorted(maps.Keys(w.logged[project]))); err != nil {
				return err
			}
		}
	}
	return r.writeOwed(ctx)
}

// owedRounds bounds how many times one page writes the owed days again: a write of an owed day may
// leave another owed, where the store moved since the page read its changes, which a later page's
// read of the same move names too.
const owedRounds = 3

// writeOwed writes every day of the pass's projects a write left owing a rewrite (owedDays): a day
// an item moved off, or the day of the deploy run a pull request moved off, which a pass that
// failed after the moving write and before the day's own left unwritten, or this page's own writes
// left so. Each is written whole, as a change naming it is, though the page wrote it already: the
// mark may be younger than that write.
func (r *refresh) writeOwed(ctx context.Context) error {
	for range owedRounds {
		owed, err := r.store.owedDays(ctx, r.projects)
		if err != nil || len(owed) == 0 {
			return err
		}
		for _, day := range owed {
			switch {
			case slices.Contains(deliveryDayFamilies, day.family) && r.settings != nil:
				delete(r.done, builtDay{family: walkDelivery, project: day.project, day: day.day})
				read, err := r.readDelivery(ctx, day.day, nil)
				if err != nil {
					return err
				}
				if err := r.writeDelivery(ctx, read, day.project, true); err != nil {
					return err
				}
			case slices.Contains(legionDayFamilies, day.family):
				delete(r.done, builtDay{family: walkLegion, project: day.project, day: day.day})
				if err := r.writeLegion(ctx, day.project, day.day, true); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// deliveryItem is a pull request or a run by its row key, and the UTC midnight of its day as its
// row stands now: the pull request's merge, the run's start; nil where it has none (a pull request
// not merged, or a row gone).
type deliveryItem struct {
	member string
	day    *time.Time
}

// applyDelivery writes the delivery and CI days of the pull requests and runs w names, then the
// days those items are linked to: the deploy run that ships a pull request now (DeliveryDay.linked)
// and the one that shipped it when it was last counted (eval_day_members.shipped_by), and the pull
// requests a run ships now and shipped before. A pull request a changed run ships now may have
// shipped with another run before, which counts it among its pull requests until its day is
// written, so the runs that shipped the linked pull requests are written last. No link beyond
// those is followed: the rows of the items behind them did not move.
func (r *refresh) applyDelivery(ctx context.Context, w *pageWork) error {
	if r.settings == nil || len(w.prs)+len(w.runs) == 0 {
		return nil
	}
	items, err := r.store.deliveryItemDays(ctx, w.prs, w.runs)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, item := range items {
		seen[item.member] = true
	}
	linked, former, err := r.rewriteDelivery(ctx, items, true)
	if err != nil {
		return err
	}
	next, err := r.followUp(ctx, linked, former, seen)
	if err != nil {
		return err
	}
	_, former, err = r.rewriteDelivery(ctx, next, false)
	if err != nil {
		return err
	}
	last, err := r.followUp(ctx, nil, former, seen)
	if err != nil {
		return err
	}
	_, _, err = r.rewriteDelivery(ctx, last, false)
	return err
}

// followUp is the items of linked and the deploy runs former names (by row key) that seen does
// not hold yet, each run with the day its row stands at now, and adds them to seen.
func (r *refresh) followUp(ctx context.Context, linked []deliveryItem, former []string, seen map[string]bool) ([]deliveryItem, error) {
	var next []deliveryItem
	for _, item := range linked {
		if !seen[item.member] {
			seen[item.member] = true
			next = append(next, item)
		}
	}
	runs := map[string]runRef{}
	for _, member := range former {
		if run, ok := parseRunMember(member); ok && !seen[member] {
			seen[member] = true
			runs[member] = run
		}
	}
	if len(runs) == 0 {
		return next, nil
	}
	days, err := r.store.deliveryItemDays(ctx, nil, runs)
	if err != nil {
		return nil, err
	}
	return append(next, days...), nil
}

// parseRunMember is the run a run's row key names (RunMember), false for a key that names none.
func parseRunMember(member string) (runRef, bool) {
	rest, ok := strings.CutPrefix(member, "run:")
	slash := strings.LastIndex(rest, "/")
	if !ok || slash < 0 {
		return runRef{}, false
	}
	id, err := strconv.ParseInt(rest[slash+1:], 10, 64)
	if err != nil {
		return runRef{}, false
	}
	return runRef{repo: rest[:slash], id: id}, true
}

// rewriteDelivery writes, for each item, its day in every project that day counts it for, and the
// day eval_day_members holds it under in that project, and answers the deploy runs that shipped a
// pull request among items when it was last counted. Where follow is set it also writes the merge
// day of every pull request a run among items shipped when that pull request was last counted,
// which the run's day no longer names once it stops shipping it, and answers the items of other
// days each item's day, and each of those pull requests' days, links it to now.
func (r *refresh) rewriteDelivery(ctx context.Context, items []deliveryItem, follow bool) ([]deliveryItem, []string, error) {
	if len(items) == 0 {
		return nil, nil, nil
	}
	members := make([]string, len(items))
	for i, item := range items {
		members[i] = item.member
	}
	kept, err := r.keptRepos(ctx)
	if err != nil {
		return nil, nil, err
	}
	held, err := r.store.heldMembers(ctx, r.projects, deliveryDayFamilies, append([]string{""}, deployRepoNames(kept)...), members)
	if err != nil {
		return nil, nil, err
	}
	var former []string
	for _, member := range held {
		if member.shippedBy != "" {
			former = append(former, member.shippedBy)
		}
	}
	var shipped []heldMember
	if follow {
		if shipped, err = r.store.shippedMembers(ctx, r.projects, members); err != nil {
			return nil, nil, err
		}
	}
	type job struct{ projects, members map[string]bool }
	jobs := map[time.Time]*job{}
	jobOf := func(day time.Time) *job {
		if jobs[day] == nil {
			jobs[day] = &job{projects: map[string]bool{}, members: map[string]bool{}}
		}
		return jobs[day]
	}
	for _, item := range items {
		if item.day != nil {
			jobOf(*item.day).members[item.member] = true
		}
	}
	for _, member := range held {
		jobOf(member.day).projects[member.project] = true
	}
	// A pull request a run shipped before is an item of its merge day too: the run that ships it
	// now, which the day's read links it to, counts it among its pull requests from this change on.
	for _, member := range shipped {
		job := jobOf(member.day)
		job.projects[member.project], job.members[member.member] = true, true
	}
	var linked []deliveryItem
	for _, day := range slices.SortedFunc(maps.Keys(jobs), time.Time.Compare) {
		read, err := r.readDelivery(ctx, day, nil)
		if err != nil {
			return nil, nil, err
		}
		projects := maps.Clone(jobs[day].projects)
		for member := range jobs[day].members {
			for _, project := range read.countedFor(member) {
				projects[project] = true
			}
			if follow {
				for _, item := range read.linked(member) {
					linked = append(linked, deliveryItem{member: item.member, day: &item.day})
				}
			}
		}
		for _, project := range slices.Sorted(maps.Keys(projects)) {
			if r.known[project] {
				if err := r.writeDelivery(ctx, read, project, true); err != nil {
					return nil, nil, err
				}
			}
		}
	}
	return linked, former, nil
}

// keptRepos is the repositories whose rows of the evals that read the deploy repository the pass
// keeps (readDeployRepos), read once a page (refresh.deployRepos).
func (r *refresh) keptRepos(ctx context.Context) ([]keptRepo, error) {
	if r.deployRepos == nil {
		repos, err := readDeployRepos(ctx, r.store.database.Pool)
		if err != nil {
			return nil, err
		}
		r.deployRepos = nonNil(repos)
	}
	return r.deployRepos, nil
}

// readDelivery is the delivery rows of day (readDeliveryDay) with each kept repository as the
// deploy repository (keptRepos), read once a page (refresh.reads), each repository's deploy runs
// taken from applies where it holds them and its repo_projects from the page's one read of them
// (refresh.repos), made by the first day the page reads.
func (r *refresh) readDelivery(ctx context.Context, day time.Time, applies map[string]*delivery.ApplyRows) (deliveryRead, error) {
	if read, ok := r.reads[day]; ok {
		return read, nil
	}
	if r.repos == nil {
		repos, err := readRepoProjects(ctx, r.store)
		if err != nil {
			return deliveryRead{}, err
		}
		r.repos = &repos
	}
	kept, err := r.keptRepos(ctx)
	if err != nil {
		return deliveryRead{}, err
	}
	read, err := r.store.readDeliveryDay(ctx, *r.settings, kept, day, applies, r.repos)
	if err != nil {
		return deliveryRead{}, err
	}
	r.reads[day] = read
	return read, nil
}

// writeDelivery writes project's delivery and CI rows of read's day, those of the evals that read
// the deploy repository once for each repository read held, as planWrite says: the rows the day
// builds, and the zero row of every series the day has no input of whose first input day is on or
// before it (zerosFrom) where the day is one the walk computes or has computed; once a page
// (refresh.done). A project the day counts nothing for and holds no series of before that day
// writes nothing unless explicit, which a project eval_day_members named is, or the walk finds
// the day holding rows of it (walkHeld): its write deletes what the day held.
func (r *refresh) writeDelivery(ctx context.Context, read deliveryRead, project string, explicit bool) error {
	key := builtDay{family: walkDelivery, project: project, day: read.Day}
	if r.done[key] {
		return nil
	}
	first, err := r.firstOf(ctx, walkDelivery, project)
	if err != nil {
		return err
	}
	built := read.Rows(project)
	plan, err := r.planWrite(ctx, walkDelivery, project, read.Day, built.Rows, first)
	if err != nil {
		return err
	}
	built.Rows = dropUnshipped(built.Rows, plan.first, read.Day)
	day := built
	if plan.zeros {
		day = built.WithZeros(zerosFrom(ZeroDeliveryDay(*r.settings, project, read.Day, read.repos).Rows, plan.first, read.Day))
	}
	if len(day.Rows) == 0 && !explicit && !r.walkHeld[key] {
		return nil
	}
	day.Rewind = plan.rewind
	if err := r.store.writeDeliveryDay(ctx, day, r.build, !r.walking); err != nil {
		return err
	}
	if r.walking {
		r.noteWalked(walkDelivery, project, read.Day)
	}
	r.noteBuilt(walkDelivery, project, read.Day, built.Rows)
	r.noteRewind(walkOf(walkDelivery, project), plan.rewind)
	r.done[key], r.report.days[key] = true, true
	return nil
}

// dayPlan is how a write of one project's day of a family writes the rows the day builds: the
// first input days it builds them by, whether it writes the zero rows of the series the day has
// no input of, and the day it moves the walk of the family back to (ProjectDay.Rewind), nil for
// none.
type dayPlan struct {
	first  map[evalKey]time.Time
	zeros  bool
	rewind *time.Time
}

// planWrite is how the write of project's day of family writes it, where built is the rows the
// day's reads build and first the series' first input days (firstOf). The walk writes its days in
// order, from the first input day or from the days it built, by the first input days of those
// days, with every zero row, and moves no walk back. A write outside a walk, of a day a change
// names:
//
//   - takes the store's first input days, but that of each series whose first input day is day
//     itself and which the day holds no input of any more (inputKeys): day is no first input day
//     of it, so the write keeps no row of it, its zero row or a lead time that counts no pull
//     request, and moves the walk back to day, which builds it and the days after it from their
//     own input, beginning the series where a whole walk begins it; the series has no first input
//     day for the rest of the pass;
//   - moves the walk back to the day after day where it moves a series' first input day earlier,
//     or creates one (input of a series the store holds no row of on or before day), and leaves
//     every day after it to the walk, which builds each from its own input;
//   - writes the zero rows only where the running build's walk has computed day, and still will
//     once the write commits (walkNext): a day it has not computed holds the rows with input
//     alone, and reads "no data" where it holds none, until the walk computes it, so no day that
//     holds input reads 0 before the walk has computed it.
func (r *refresh) planWrite(ctx context.Context, family, project string, day time.Time, built []DayRow, first map[evalKey]time.Time) (dayPlan, error) {
	if r.walking {
		return dayPlan{first: first, zeros: true}, nil
	}
	plan := dayPlan{first: first}
	input := inputKeys(built)
	for key, since := range first {
		if since.Equal(day) && !input[key] {
			// first is the pass's own (storedFirst), which holds no first input day of the series
			// any more: the store's next one is a day the walk builds again.
			delete(first, key)
			plan.rewind = &day
		}
	}
	for key := range input {
		if since, held := first[key]; plan.rewind == nil && (!held || day.Before(since)) {
			after := day.AddDate(0, 0, 1)
			plan.rewind = &after
		}
	}
	next, err := r.walkNext(ctx, family, project)
	if err != nil {
		return dayPlan{}, err
	}
	plan.zeros = next != nil && day.Before(*next) && (plan.rewind == nil || day.Before(*plan.rewind))
	return plan, nil
}

// applyLegion writes the Legion days w names: the days the facts and filing changes named, and the
// filing days and the close and held days of every issue below an issue whose repository or
// parent moved, whose repository rows resolve through it.
func (r *refresh) applyLegion(ctx context.Context, w *pageWork) error {
	days := map[string]map[time.Time]bool{}
	add := func(project string, day time.Time) {
		if days[project] == nil {
			days[project] = map[time.Time]bool{}
		}
		days[project][day] = true
	}
	if len(w.reparented) > 0 {
		moved := slices.Sorted(maps.Keys(w.reparented))
		filed, err := r.store.subtreeFilingDays(ctx, moved)
		if err != nil {
			return err
		}
		closed, err := r.store.subtreeCloseDays(ctx, moved)
		if err != nil {
			return err
		}
		for _, day := range slices.Concat(filed, closed) {
			add(day.project, day.day)
		}
	}
	for project, named := range w.legion {
		for day := range named {
			add(project, day)
		}
	}
	for _, project := range slices.Sorted(maps.Keys(days)) {
		if !r.known[project] {
			continue
		}
		for _, day := range slices.SortedFunc(maps.Keys(days[project]), time.Time.Compare) {
			if err := r.writeLegion(ctx, project, day, true); err != nil {
				return err
			}
		}
	}
	return nil
}

// writeLegion writes project's Legion rows of day as planWrite says: the rows the day builds, and
// the zero row of every series the day has no input of whose first input day is on or before it
// where the day is one the walk computes or has computed; once a page (refresh.done). A day with
// no input and no series begun before it writes nothing unless explicit, which a change naming the
// day is, or the walk finds the day holding rows of it (walkHeld): its write deletes what the day
// held.
func (r *refresh) writeLegion(ctx context.Context, project string, day time.Time, explicit bool) error {
	key := builtDay{family: walkLegion, project: project, day: day}
	if r.done[key] {
		return nil
	}
	read, err := r.store.ReadLegionDay(ctx, project, day)
	if err != nil {
		return err
	}
	since, err := r.sinceOf(ctx, project)
	if err != nil {
		return err
	}
	first, err := r.firstOf(ctx, walkLegion, project)
	if err != nil {
		return err
	}
	built := read.Rows(since)
	plan, err := r.planWrite(ctx, walkLegion, project, read.Day, built.Rows, first)
	if err != nil {
		return err
	}
	written := built
	if plan.zeros {
		written = built.WithZeros(zerosFrom(ZeroLegionDay(project, read.Day, legionDaySlugs...).Rows, plan.first, read.Day))
	}
	if len(written.Rows) == 0 && !explicit && !r.walkHeld[key] {
		return nil
	}
	written.Rewind = plan.rewind
	if err := r.store.writeLegionDay(ctx, written, r.build, !r.walking); err != nil {
		return err
	}
	if r.walking {
		r.noteWalked(walkLegion, project, read.Day)
	}
	r.noteBuilt(walkLegion, project, read.Day, built.Rows)
	r.noteRewind(walkOf(walkLegion, project), plan.rewind)
	r.done[key], r.report.days[key] = true, true
	return nil
}

// writeUnowned writes project's unowned-p0 row of today, once a page (refresh.unowned).
func (r *refresh) writeUnowned(ctx context.Context, project string) error {
	if r.unowned[project] {
		return nil
	}
	if err := r.store.WriteUnownedP0Day(ctx, project, r.today, r.build); err != nil {
		return err
	}
	r.unowned[project] = true
	r.report.days[builtDay{family: SlugUnownedP0, project: project, day: r.today}] = true
	return nil
}

// fillLogged writes the zero rows of project's logged evals ids names (every one where ids is nil)
// from each series' first day to today (fillLoggedZeros), recorded where it wrote any.
func (r *refresh) fillLogged(ctx context.Context, project string, ids []string) error {
	filled, err := r.fillLoggedZeros(ctx, project, ids)
	if err != nil {
		return err
	}
	if filled > 0 {
		r.report.days[builtDay{family: walkLogged, project: project, day: r.today}] = true
	}
	return nil
}

// sinceOf is project's LegionSince, read once a pass.
func (r *refresh) sinceOf(ctx context.Context, project string) (LegionSince, error) {
	if since, ok := r.since[project]; ok {
		return since, nil
	}
	since, err := r.store.ReadLegionSince(ctx, project)
	if err != nil {
		return LegionSince{}, err
	}
	r.since[project] = since
	return since, nil
}

// firstOf is the first input day of each eval of family that project holds a row of, by eval and
// deploy repository (evalKey), as a write of a day builds its rows by: the store's (storedFirst)
// outside a walk, and in it the walk's own, the first day of each eval's all-repositories rows of
// the days before the day the walk's batch began at (walkFrom), every one of which the walk built
// or a write outside it built from its input, read once a pass and moved by every day the walk
// writes (noteBuilt). So the walk begins a series at the first day it builds holding input of it,
// and a day before the walk's own first days holds no row of it, whatever rows a day the walk now
// builds held before: a whole walk over the same inputs holds the same.
func (r *refresh) firstOf(ctx context.Context, family, project string) (map[evalKey]time.Time, error) {
	if !r.walking {
		return r.storedFirst(ctx, family, project)
	}
	key := walkKey{family: family, project: project}
	if first, ok := r.walkFirst[key]; ok {
		return first, nil
	}
	var before *time.Time
	if from, ok := r.walkFrom[walkOf(family, project)]; ok {
		before = &from
	}
	first, err := r.readFirst(ctx, family, project, before)
	if err != nil {
		return nil, err
	}
	if r.walkFirst == nil {
		r.walkFirst = map[walkKey]map[evalKey]time.Time{}
	}
	r.walkFirst[key] = first
	return first, nil
}

// storedFirst is the first input day of each eval of family that project holds a row of, by eval
// and deploy repository (evalKey), read once a pass and moved by every day a page writes
// (noteBuilt): the first day of each eval's all-repositories rows of the series its zero day holds
// (firstDays), of each kept repository's rows of an eval that reads the deploy repository apart.
// It is where a walk begins (deliveryStart, legionStart), so the walk rewrites every day holding a
// row of its family.
func (r *refresh) storedFirst(ctx context.Context, family, project string) (map[evalKey]time.Time, error) {
	key := walkKey{family: family, project: project}
	if first, ok := r.first[key]; ok {
		return first, nil
	}
	first, err := r.readFirst(ctx, family, project, nil)
	if err != nil {
		return nil, err
	}
	r.first[key] = first
	return first, nil
}

// readFirst reads the first day of each of project's evals of family before before (every day,
// where it is nil), as storedFirst says.
func (r *refresh) readFirst(ctx context.Context, family, project string, before *time.Time) (map[evalKey]time.Time, error) {
	var zero []DayRow
	switch family {
	case walkDelivery:
		kept, err := r.keptRepos(ctx)
		if err != nil {
			return nil, err
		}
		zero = ZeroDeliveryDay(*r.settings, project, r.today, deployRepoNames(kept)).Rows
	case walkLegion:
		zero = ZeroLegionDay(project, r.today, legionDaySlugs...).Rows
	}
	return r.store.firstDays(ctx, project, zero, before)
}

// zerosFrom is each of zeros whose eval's first input day, of its deploy repository's rows, is on
// or before day: the zero rows a day with no input of a series writes, which no day before a
// series' first input day holds.
func zerosFrom(zeros []DayRow, first map[evalKey]time.Time, day time.Time) []DayRow {
	var kept []DayRow
	for _, zero := range zeros {
		if since, ok := first[keyOf(zero)]; ok && !day.Before(since) {
			kept = append(kept, zero)
		}
	}
	return kept
}

// noteBuilt moves the first input day of each eval built holds an all-repositories row of (of each
// deploy repository apart) to day, where day is before it or the eval held none: its first input
// day is now. A walk's day moves the walk's first days (firstOf), any other the pass's own.
func (r *refresh) noteBuilt(family, project string, day time.Time, built []DayRow) {
	firsts := r.first
	if r.walking {
		firsts = r.walkFirst
	}
	first := firsts[walkKey{family: family, project: project}]
	for _, row := range built {
		if row.Repo != "" {
			continue
		}
		if since, ok := first[keyOf(row)]; ok && !day.Before(since) {
			continue
		}
		first[keyOf(row)] = day
	}
}

// walk makes a batch of each walk: the delivery walk (where delivery settings exist), and each
// project's Legion walk, logged zero fill and unowned-p0 row of today, and then folds the stored
// figures of the days the batches wrote (foldWalked). It answers whether a walk has days left,
// which asks for the next pass at once. It computes every day it reaches, so the days the pass's
// pages wrote are written again where a batch reaches them (refresh.done begins empty).
func (r *refresh) walk(ctx context.Context) (bool, error) {
	walks, err := r.store.readWalks(ctx, r.build)
	if err != nil {
		return false, err
	}
	r.walking, r.done = true, map[builtDay]bool{}
	defer func() { r.walking = false }()
	pending := false
	if r.settings != nil {
		more, err := r.walkDays(ctx, walks, walkKey{family: walkDelivery}, r.deliveryStart, r.deliveryWalkDay)
		if err != nil {
			return false, err
		}
		pending = pending || more
	}
	for _, project := range r.projects {
		more, err := r.walkDays(ctx, walks, walkKey{family: walkLegion, project: project},
			func(ctx context.Context) (*time.Time, error) { return r.legionStart(ctx, project) },
			func(ctx context.Context, day time.Time) error { return r.writeLegion(ctx, project, day, false) })
		if err != nil {
			return false, err
		}
		pending = pending || more
		if err := r.walkDaily(ctx, walks, walkKey{family: walkLogged, project: project},
			func(ctx context.Context) error { return r.fillLogged(ctx, project, nil) }); err != nil {
			return false, err
		}
		if err := r.walkDaily(ctx, walks, walkKey{family: walkUnowned, project: project},
			func(ctx context.Context) error { return r.writeUnowned(ctx, project) }); err != nil {
			return false, err
		}
	}
	return pending, r.foldWalked(ctx)
}

// walkDays makes one batch of the walk key: from the first day it has not built, or, where it is
// to begin (no record of the running build's, which is how each build makes its own walk, or one
// begun again), from the first input day begin answers, it builds up to r.batch days through today
// with build, and records where it stands. A walk with nothing to walk (begin answers nil) stands
// at tomorrow. It answers whether days before today are left. The batch builds by the first input
// days of the days before its first (beginBatch), and writes each project's day of it that holds
// rows of the walk's family whether or not it builds a row of it (heldDays), so a day whose input
// left holds no row a whole walk would not hold.
func (r *refresh) walkDays(ctx context.Context, walks map[walkKey]walkState, key walkKey,
	begin func(context.Context) (*time.Time, error), build func(context.Context, time.Time) error) (bool, error) {
	state, found := walks[key]
	begun := !found || state.next == nil
	next := state.next
	if begun {
		start, err := begin(ctx)
		if err != nil {
			return false, err
		}
		if start == nil {
			return false, r.recordWalk(ctx, key, r.today.AddDate(0, 0, 1))
		}
		next = start
	}
	if next.After(r.today) {
		if begun {
			return false, r.recordWalk(ctx, key, *next)
		}
		return false, nil
	}
	last := next.AddDate(0, 0, max(r.batch, 1)-1)
	if last.After(r.today) {
		last = r.today
	}
	if err := r.beginBatch(ctx, key, *next, last); err != nil {
		return false, err
	}
	for day := *next; !day.After(last); day = day.AddDate(0, 0, 1) {
		if err := build(ctx, day); err != nil {
			return false, err
		}
	}
	if err := r.recordWalk(ctx, key, last.AddDate(0, 0, 1)); err != nil {
		return false, err
	}
	return last.Before(r.today), nil
}

// beginBatch begins a batch of the walk key from from through through: the walk's first input days
// are read anew of the days before from (firstOf), and the days of the batch that hold rows of its
// family (walkHeld) are read, once for the batch.
func (r *refresh) beginBatch(ctx context.Context, key walkKey, from, through time.Time) error {
	if r.walkFrom == nil {
		r.walkFrom, r.walkFirst, r.walkHeld = map[walkKey]time.Time{}, map[walkKey]map[evalKey]time.Time{}, map[builtDay]bool{}
	}
	r.walkFrom[key] = from
	projects, slugs := r.projects, deliveryDaySlugs
	if key.family == walkLegion {
		projects, slugs = []string{key.project}, legionDaySlugs
	}
	for _, project := range projects {
		delete(r.walkFirst, walkKey{family: key.family, project: project})
	}
	held, err := r.store.heldDays(ctx, key.family, projects, slugs, from, through)
	if err != nil {
		return err
	}
	maps.Copy(r.walkHeld, held)
	return nil
}

// deliveryStart is the first input day of the delivery walk, the UTC midnight of the earliest of
// the first merge of a pull request, the first start of a run of a kept repository (keptRepos) and
// the first day any project holds a delivery or CI row of, nil where none exists. Every project's
// first days are taken together, so the walk begins at a midnight however many projects there
// are, none included.
func (r *refresh) deliveryStart(ctx context.Context) (*time.Time, error) {
	kept, err := r.keptRepos(ctx)
	if err != nil {
		return nil, err
	}
	earliest, err := r.store.deliveryInputStart(ctx, deployRepoNames(kept))
	if err != nil {
		return nil, err
	}
	firsts := map[evalKey]time.Time{}
	for _, project := range r.projects {
		first, err := r.storedFirst(ctx, walkDelivery, project)
		if err != nil {
			return nil, err
		}
		// An eval id names its project, so no two projects' entries share a key.
		maps.Copy(firsts, first)
	}
	return earliestOf(earliest, firsts), nil
}

// legionStart is the first input day of project's Legion walk: the earliest of its first close of
// an issue with Legion phase work, its first filing and the first day it holds a Legion row of,
// nil where none exists.
func (r *refresh) legionStart(ctx context.Context, project string) (*time.Time, error) {
	earliest, err := r.store.legionInputStart(ctx, project)
	if err != nil {
		return nil, err
	}
	first, err := r.storedFirst(ctx, walkLegion, project)
	if err != nil {
		return nil, err
	}
	return earliestOf(earliest, first), nil
}

// earliestOf is the UTC midnight of the earliest of earliest and the days of first.
func earliestOf(earliest *time.Time, first map[evalKey]time.Time) *time.Time {
	for _, day := range first {
		if earliest == nil || day.Before(*earliest) {
			earliest = &day
		}
	}
	if earliest == nil {
		return nil
	}
	day := measures.Midnight(*earliest)
	return &day
}

// deliveryWalkDay writes one day of the delivery walk: every project's delivery and CI rows of it,
// of every kept repository. The deploy runs of each kept repository every day of a batch reads
// from its own start on are read once, at the batch's first day (delivery.ReadApplyRows), each
// repository's read side by side, and each day takes its part of them: a batch's days run in
// order, so the first day's read holds every later day's runs.
func (r *refresh) deliveryWalkDay(ctx context.Context, day time.Time) error {
	if r.walkApplies == nil || day.Before(r.walkAppliesFrom) {
		kept, err := r.keptRepos(ctx)
		if err != nil {
			return err
		}
		applies := make([]delivery.ApplyRows, len(kept))
		reads := make([]func(context.Context) error, len(kept))
		for i, repo := range kept {
			settings := *r.settings
			settings.DeployRepo = repo.name
			reads[i] = func(ctx context.Context) (err error) {
				if applies[i], err = delivery.ReadApplyRows(ctx, r.store.database.Pool, settings, day); err != nil {
					return fmt.Errorf("read the deploy runs of %s from %s on: %w", repo.name, day.Format(time.DateOnly), err)
				}
				return nil
			}
		}
		if err := store.ReadSideBySide(ctx, reads...); err != nil {
			return err
		}
		r.walkApplies = make(map[string]*delivery.ApplyRows, len(kept))
		for i, repo := range kept {
			r.walkApplies[repo.name] = &applies[i]
		}
		r.walkAppliesFrom = day
	}
	read, err := r.readDelivery(ctx, day, r.walkApplies)
	if err != nil {
		return err
	}
	for _, project := range r.projects {
		if err := r.writeDelivery(ctx, read, project, false); err != nil {
			return err
		}
	}
	return nil
}

// walkDaily makes the once-a-day write of the walk key: write runs on the first pass of each UTC
// day, and on the first pass of a walk begun again (no record of the running build's), and the
// walk then stands at tomorrow. The logged evals' zero fill is one (fillLogged through today), and
// the unowned-p0 row of today another (writeUnowned): its count reads issues alone, so it is
// written whether or not delivery settings exist, a project made since the last pass included.
func (r *refresh) walkDaily(ctx context.Context, walks map[walkKey]walkState, key walkKey, write func(context.Context) error) error {
	state, found := walks[key]
	if found && state.next != nil && state.next.After(r.today) {
		return nil
	}
	if err := write(ctx); err != nil {
		return err
	}
	return r.recordWalk(ctx, key, r.today.AddDate(0, 0, 1))
}
