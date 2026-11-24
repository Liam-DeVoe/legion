package evals

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/delivery"
	"github.com/sjawhar/envoy/internal/dispatch/delivery/measures"
)

// The refresher's part of the stored figures (figures.go): the change log's read that folds again
// the figures of the days whose rows a write left unfolded, and the figures walk, which builds a
// project's figures whole once and then each new end day.
//
// Every build that holds this code writes the figures, in the transaction of each write of day
// rows, and keeps them as the day rows are kept (eval_days.built_by, migration 20261117100006): a
// read serves a stored figure whichever build folded it (eval_range_figures.built_by). A project's
// figures are first folded whole by its figures walk (eval_days_walks, family `figures`), one walk
// whichever build's process runs it, and are built once that first walk is done
// (eval_figures_built), from which a read of an offered range folds the stored figures
// (figuresThrough); the walk then folds each end day a new UTC day adds. A release, a lost cursor
// of the figures or of the refresher refold every built figure aside, in a rebuild (rebuild.go),
// and stage only the figures that move: so a release whose folds agree with the build before it
// writes nothing, and one whose folds differ lands what they move in its flip, which logs an
// eval_days change of each eval whose figures it moved, so the views read them. The stored figures
// stay in use through a deploy, a roll of two builds and a rollback alike.
//
// While two builds whose folds differ roll, a figure a page's write folds is the fold of the
// build whose process made the write, as each day row is, until the release's rebuild flips: it
// folds again, before its flip, every end day a statement wrote figures of, and the ranges of every
// day a statement wrote rows of, while it ran.
//
// A write that leaves its fold to the next pass is logged like any other: a register write, which
// moves an eval's fold key or listing and folds nothing, logs its eval_versions change, and a write
// of day rows by a build that holds the day rows and not the figures (an earlier build of the
// change that brought them; the release before both holds neither) logs its eval_days change, by
// eval_days' trigger (migration 20261117100014). Every build that writes figures reads every such
// change from the figures' own cursor in the change log (figureChanges, eval_days_cursor's
// `figures` row), which no build without the figures reads, so a change such a build's refresher
// read off the refresher's own cursor is still folded. A read sees figures older than the rows
// from that write's commit until the next pass of a build with the figures, which the change's
// own notification wakes and which starts as soon as the pass running then ends, and the pass's
// own length.

// figuresCursor names the figures' own cursor in eval_days_cursor (figureChanges).
const figuresCursor = "figures"

// figuresPass is the stored figures' part of a pass, after the feeds' pages and the walks of the
// day rows: the read of the change log (figureChanges), then, where no walk of the day rows in the
// open has days left, a batch of each figures walk (walkFigures), which waits for those walks (a
// walk begun again) so each range is folded once, from the rows as they leave them, rather than
// once more for each batch of days they move under it. It answers whether a figures walk has end
// days left.
func (r *refresh) figuresPass(ctx context.Context, walksPending bool, page int) (bool, error) {
	if err := r.figureChanges(ctx, page); err != nil {
		return false, err
	}
	if walksPending {
		return false, nil
	}
	return r.walkFigures(ctx)
}

// everyDay is the span an eval_versions change names: every day of the eval, since a new version
// of its definition or of one of its series can move its fold key.
var everyDay = daySpan{first: time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC), last: time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)}

// addSpan widens spans' span of evalID to hold first to last.
func addSpan(spans map[string]daySpan, evalID string, first, last time.Time) {
	span, held := spans[evalID]
	if !held || first.Before(span.first) {
		span.first = first
	}
	if !held || last.After(span.last) {
		span.last = last
	}
	spans[evalID] = span
}

// figureChanges is the figures' read of the change log, page after page until one comes back
// short: every eval_days change after the figures' cursor, whichever build's write logged it, and
// every eval_versions change, each by project and eval. For each project it folds again, in a
// transaction of its own, the figures of every day the changes span whose rows moved since they
// were folded (refoldFigures): none where a build's write folded them in its own transaction, which
// costs the two short reads of the moved-day test; the days of a build that writes no figures; and
// every day of an eval a register write or the seed gave another fold key. It then moves the
// cursor, so a pass that fails reads the same changes again. The cursor is the change log's as the
// refresher's own is (delivery.ChangeCursor): it starts at the log's head, the walks folding every
// figure before it; and one that may have passed over changes it never read (delivery.CursorLost)
// moves to the log's head and begins a rebuild of the built projects' figures (rebuild.go), which
// folds every one again aside and flips those that moved: the day rows' own cursor lost nothing.
func (r *refresh) figureChanges(ctx context.Context, limit int) error {
	pool := r.store.database.Pool
	for {
		checked := r.clock()
		var cursor delivery.ChangeCursor
		err := pool.QueryRow(ctx, `select position, at, updated_at from eval_days_cursor where name = $1`, figuresCursor).Scan(
			&cursor.Seq, &cursor.At, &cursor.ReadAt)
		if errors.Is(err, pgx.ErrNoRows) {
			head, err := delivery.ChangesHead(ctx, pool)
			if err != nil {
				return fmt.Errorf("read the head of the delivery changes: %w", err)
			}
			head.ReadAt = checked
			return r.moveFigureCursor(ctx, head, false)
		}
		if err != nil {
			return fmt.Errorf("read the stored figures' change cursor: %w", err)
		}
		lost, err := delivery.CursorLost(ctx, pool, cursor, checked)
		if err != nil {
			return fmt.Errorf("read the delivery change at %d: %w", cursor.Seq, err)
		}
		if lost {
			head, err := delivery.ChangesHead(ctx, pool)
			if err != nil {
				return fmt.Errorf("read the head of the delivery changes: %w", err)
			}
			head.ReadAt = checked
			slog.Warn("dispatch: the stored figures' change cursor may have passed changes it never read; a rebuild of the built figures begins",
				"cursor", cursor.Seq, "head", head.Seq)
			return r.moveFigureCursor(ctx, head, true)
		}
		changes, err := delivery.ChangesSince(ctx, pool, cursor.Seq, limit)
		if err != nil {
			return fmt.Errorf("read the delivery changes after %d: %w", cursor.Seq, err)
		}
		spans := map[string]map[string]daySpan{}
		for _, change := range changes {
			first, last := everyDay.first, everyDay.last
			switch {
			case !r.known[change.Repo]:
				continue
			case change.Kind == delivery.ChangeKindEvalDays && change.TOld != nil && change.TNew != nil:
				first, last = measures.Midnight(*change.TOld), measures.Midnight(*change.TNew)
			case change.Kind != delivery.ChangeKindEvalVersions:
				continue
			}
			if spans[change.Repo] == nil {
				spans[change.Repo] = map[string]daySpan{}
			}
			addSpan(spans[change.Repo], change.Ref, first, last)
		}
		for _, project := range slices.Sorted(maps.Keys(spans)) {
			if err := r.refoldChanged(ctx, project, spans[project]); err != nil {
				return err
			}
		}
		if err := r.moveFigureCursor(ctx, cursor.Through(changes, limit, checked), false); err != nil {
			return err
		}
		if len(changes) < limit {
			return nil
		}
	}
}

// moveFigureCursor moves the figures' change cursor to cursor (its ReadAt stamping the row, as the
// refresher's change cursor is stamped, commitPage), and, where restart is set, begins a rebuild
// of the figures of every project whose figures are built (replaceRebuild), in one transaction: a
// change the lost cursor passed is read by no other pass, whichever build's process runs it. The
// walks that rebuild walks are read on the pool before the transaction begins (servedWalks), as
// commitPage reads a rebuild's, so the pass holds one connection at a time.
func (r *refresh) moveFigureCursor(ctx context.Context, cursor delivery.ChangeCursor, restart bool) error {
	var walks []walkKey
	if restart {
		served, err := r.servedWalks(ctx)
		if err != nil {
			return err
		}
		walks = slices.DeleteFunc(served, func(key walkKey) bool { return key.family != walkFigures })
	}
	tx, err := r.store.database.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin the stored figures' change cursor: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		insert into eval_days_cursor (name, position, at, updated_at) values ($1, $2, $3, $4)
		on conflict (name) do update set position = excluded.position, at = excluded.at, updated_at = excluded.updated_at
	`, figuresCursor, cursor.Seq, cursor.At, cursor.ReadAt); err != nil {
		return fmt.Errorf("move the stored figures' change cursor: %w", err)
	}
	if restart {
		if err := replaceRebuild(ctx, tx, rebuildCursor, r.build, walks); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit the stored figures' change cursor: %w", err)
	}
	return nil
}

// refoldChanged folds again project's figures of the days of spans (by eval id) whose rows moved,
// in one transaction, under the locks of its logged evals among them (lockLogged), which every
// readings write, zero fill and register write of one takes too, so none of them writes a row of
// theirs between the read of the rows and the write of their figures.
func (r *refresh) refoldChanged(ctx context.Context, project string, spans map[string]daySpan) error {
	tx, err := r.store.database.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin the stored figures of %s: %w", project, err)
	}
	defer tx.Rollback(ctx)
	entries, err := readEntries(ctx, tx, project, slices.Sorted(maps.Keys(spans)))
	if err != nil {
		return err
	}
	if err := lockLogged(ctx, tx, entries); err != nil {
		return err
	}
	scope, ok, err := readFigureScope(ctx, tx, r.build, project)
	if err != nil || !ok {
		return err
	}
	if err := refoldFigures(ctx, tx, scope, entriesByID(entries), spans); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit the stored figures of %s: %w", project, err)
	}
	return nil
}

// lockLogged takes the lock of each logged eval of entries (lockEvals): a computed eval's rows
// are the refresher's alone, whose passes the refresher's lease runs one at a time, and a logged
// eval's rows are its readings writes' and its zero fill's too, which each take its lock before
// they read or write a row. The fold that takes them has read its entries before, and holds no
// transaction id while it waits; a register write that commits between the two moves the eval's
// fold key, which a read compares, and logs the eval_versions change that folds it again.
func lockLogged(ctx context.Context, tx pgx.Tx, entries []Entry) error {
	var logged []string
	for _, entry := range entries {
		if !entry.Definition.Computed() {
			logged = append(logged, entry.Definition.EvalID)
		}
	}
	if len(logged) == 0 {
		return nil
	}
	return lockEvals(ctx, tx, logged)
}

// noteWalked records that the pass's walk of family wrote project's rows of day, whose figures it
// folds at the end of the walk (foldWalked), by widening the walk's span of days to hold it: a
// walk's day is never the zero time, which widen reads as no day.
func (r *refresh) noteWalked(family, project string, day time.Time) {
	key := walkKey{family: family, project: project}
	if r.walked == nil {
		r.walked = map[walkKey]daySpan{}
	}
	r.walked[key] = r.walked[key].widen(day)
}

// foldWalked folds the figures of the days the pass's walks wrote (noteWalked) whose rows moved,
// one transaction per project once the walks' batches are written: a walk writes each day in a
// transaction of its own, and a range that holds several of a batch's days would be folded once
// for each of them in their own transactions, up to walkBatch times a pass. Until it commits, a
// read sees the figures of the rows before the batch, for the length of one pass, and a view
// recomputed then answers them; so the fold's commit logs a change of each eval whose figures it
// moved (foldFiguresOfDays), and the view is recomputed once more, from the figures of the rows
// it read. A pass that fails first leaves the days' stamps unmoved, and the next pass's read of
// the change log folds them (figureChanges).
//
// An owner key's walked days (owners.go) are folded into the figures of each project whose read
// folds its rows (readersOf, one read of every owner key walked), with that project's own.
func (r *refresh) foldWalked(ctx context.Context) error {
	walked := r.walked
	r.walked = nil
	var owners []string
	for key := range walked {
		if isOwner(key.project) {
			owners = append(owners, key.project)
		}
	}
	slices.Sort(owners)
	readers, err := readersOf(ctx, r.store.database.Pool, slices.Compact(owners))
	if err != nil {
		return err
	}
	byProject := map[string]map[string]daySpan{}
	for key, span := range walked {
		slugs := legionDaySlugs
		if key.family == walkDelivery {
			slugs = deliveryDaySlugs
		}
		projects := []string{key.project}
		if isOwner(key.project) {
			projects = readers[key.project]
		}
		for _, project := range projects {
			if byProject[project] == nil {
				byProject[project] = map[string]daySpan{}
			}
			for _, slug := range slugs {
				addSpan(byProject[project], EvalID(project, slug), span.first, span.last)
			}
		}
	}
	for _, project := range slices.Sorted(maps.Keys(byProject)) {
		if err := r.store.foldFiguresOfDays(ctx, r.build, project, byProject[project]); err != nil {
			return err
		}
	}
	return nil
}

// foldFiguresOfDays folds again, in a transaction of its own, project's figures of the days of
// spans whose rows moved (refoldProject, foldFiguresInTx's fold). Its evals are computed ones,
// whose rows the refresher alone writes, so it takes no eval's lock.
//
// The transaction writes no day row, so eval_days' triggers log nothing of it, and no trigger
// sits on the figure tables: it logs an eval_days change itself of each eval whose figures it
// folded again, spanning their days (eval_days_change_log, migration 20261117100014), in the
// change log's outbox as a day write's are. A view recomputed between the day rows' commits and
// this one read those rows beside the figures of before them, and nothing else would mark it once
// the figures stand. The fold could not run in the batch's last day commit instead: that commit
// logs a change only where the day's own rows moved, and a view recomputed between two earlier
// commits of the batch would still answer figures the last commit then moved. A fold that moves
// no figure logs nothing.
func (s *Store) foldFiguresOfDays(ctx context.Context, build, project string, spans map[string]daySpan) error {
	if len(spans) == 0 {
		return nil
	}
	tx, err := s.database.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin the stored figures of %s: %w", project, err)
	}
	defer tx.Rollback(ctx)
	folded, ok, err := refoldProject(ctx, tx, build, project, spans)
	if err != nil || !ok {
		return err
	}
	for _, evalID := range slices.Sorted(maps.Keys(folded)) {
		if _, err := tx.Exec(ctx, `select eval_days_change_log($1, $2, $3::date, $4::date)`, project, evalID, folded[evalID].first, folded[evalID].last); err != nil {
			return fmt.Errorf("log the figures of %s folded again: %w", evalID, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit the stored figures of %s: %w", project, err)
	}
	if len(folded) > 0 {
		s.drainChanges(ctx)
	}
	return nil
}

// walkFigures builds what the pass's batch (gapBatch) has left of each project's figures walk,
// whichever build's process runs it: from the first end day it has not built, or, where it is to
// begin (no record, a project's first), from the day after the project's first listed day row, it
// folds up to the batch's end days left of the walk through FigureLead days after today
// (buildFigureEnds), and records where it stands in the same transaction, so the figures and the
// walk that says how far they are folded commit together. It stops between two projects for an
// owed wake (yields), each project's part being one transaction. The part that reaches the last end
// day records the project's figures built through it (eval_figures_built), which the first whole
// walk sets and every later day moves on; so once a walk has built the last end day, each later UTC
// day's first walking pass builds the end day that day adds, two days after the new today. A
// project with no listed day row stands past the last end day, and one whose first row is later
// than that stands at the day after it, built: no range before holds a row, and the write of its
// first rows folds them. A release folds built figures again aside, in its rebuild (rebuild.go),
// not here. It answers whether end days before the last are left, which asks for the pass that
// builds them.
func (r *refresh) walkFigures(ctx context.Context) (bool, error) {
	walks, err := r.store.readWalks(ctx)
	if err != nil {
		return false, err
	}
	last := r.today.AddDate(0, 0, FigureLead)
	pending := false
	for _, project := range r.projects {
		if r.yields(ctx) {
			return true, nil
		}
		key := walkKey{family: walkFigures, project: project}
		next := walks[key].next
		begun := next == nil
		if begun {
			start, err := r.store.figuresStart(ctx, project)
			if err != nil {
				return false, err
			}
			if start == nil {
				start = new(last.AddDate(0, 0, 1))
			}
			next = start
		}
		if next.After(last) {
			if begun {
				if err := r.recordFiguresBuilt(ctx, key, *next); err != nil {
					return false, err
				}
			}
			continue
		}
		end, ok := r.batched.through(key, r.batch, *next, last)
		if !ok {
			pending = true
			continue
		}
		if err := r.buildFigures(ctx, key, *next, end, end.Equal(last)); err != nil {
			return false, err
		}
		r.batched.spend(key, int(end.Sub(*next)/(24*time.Hour))+1)
		r.walkBuilt++
		pending = pending || end.Before(last)
	}
	return pending, nil
}

// recordFiguresBuilt records, in one transaction, that key's walk stands at next, past the last end
// day, and that its project's figures are built through the day before it: a project whose ranges
// before next hold no row.
func (r *refresh) recordFiguresBuilt(ctx context.Context, key walkKey, next time.Time) error {
	tx, err := r.store.database.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin the figures walk of %s: %w", key.project, err)
	}
	defer tx.Rollback(ctx)
	if err := r.recordWalkIn(ctx, tx, key, next); err != nil {
		return err
	}
	if err := markFiguresBuilt(ctx, tx, key.project, next.AddDate(0, 0, -1), r.build); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit the figures walk of %s: %w", key.project, err)
	}
	return nil
}

// buildFigures is one batch of key's figures walk: the figures of every range of the project that
// ends on a day from first through last, where the walk stands after them, and, where built (the
// batch reaches the last end day), the project's figures built through last, in one transaction.
func (r *refresh) buildFigures(ctx context.Context, key walkKey, first, last time.Time, built bool) error {
	tx, err := r.store.database.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin the figures walk of %s: %w", key.project, err)
	}
	defer tx.Rollback(ctx)
	entries, err := readEntries(ctx, tx, key.project, nil)
	if err != nil {
		return err
	}
	byID := entriesByID(entries)
	if err := lockLogged(ctx, tx, entries); err != nil {
		return err
	}
	scope := figureScope{build: r.build, project: key.project, through: last}
	if err := buildFigureEnds(ctx, tx, scope, byID, first, last); err != nil {
		return err
	}
	if err := r.recordWalkIn(ctx, tx, key, last.AddDate(0, 0, 1)); err != nil {
		return err
	}
	if built {
		if err := markFiguresBuilt(ctx, tx, key.project, last, r.build); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit the figures walk of %s through %s: %w", key.project, last.Format(time.DateOnly), err)
	}
	return nil
}

// markFiguresBuilt records in q that project's figures are built through through, by build where
// no build had built them before: eval_figures_built's row is made by the first whole walk and
// moves on with each later day, never back.
func markFiguresBuilt(ctx context.Context, q executor, project string, through time.Time, build string) error {
	if _, err := q.Exec(ctx, `
		insert into eval_figures_built (project_key, through, built_by, updated_at) values ($1, $2, $3, now())
		on conflict (project_key) do update set through = excluded.through, updated_at = excluded.updated_at
		where eval_figures_built.through < excluded.through
	`, project, through, build); err != nil {
		return fmt.Errorf("record the stored figures of %s built through %s: %w", project, through.Format(time.DateOnly), err)
	}
	return nil
}

// figuresStart is the first end day of project's figures walk: the day after its first listed day
// row, its own or one of an owner key it reads (eval_project_owners, owners.go), whose figures it
// folds, nil where it reads none.
func (s *Store) figuresStart(ctx context.Context, project string) (*time.Time, error) {
	var first *time.Time
	if err := s.database.Pool.QueryRow(ctx, `
		select min(d.day) from eval_project_owners o join eval_days d on d.project_key = o.owner
		where o.project_key = $1 and d.listed
	`, project).Scan(&first); err != nil {
		return nil, fmt.Errorf("read the first day row of %s: %w", project, err)
	}
	if first == nil {
		return nil, nil
	}
	start := measures.Midnight(*first).AddDate(0, 0, 1)
	return &start, nil
}

// figuresThrough reads in q, for each of projects whose stored figures are built, the last end day
// they are built through (eval_figures_built): a read of a range of FigureLengths ending on or
// before it may read the project's stored figures (figures.go), whichever build serves the read and
// whichever folded them, and any other reads its day rows. A project is absent until the first
// whole walk of its figures by any build is done (the first release that holds the stored figures,
// or a project made later), and stays once it is, through every later release, roll and rollback.
func figuresThrough(ctx context.Context, q delivery.Querier, projects []string) (map[string]time.Time, error) {
	rows, err := q.Query(ctx, `select project_key, through from eval_figures_built where project_key = any($1)`, projects)
	if err != nil {
		return nil, fmt.Errorf("read how far the stored figures of %v are built: %w", projects, err)
	}
	through := map[string]time.Time{}
	var project string
	var day time.Time
	if _, err := pgx.ForEachRow(rows, []any{&project, &day}, func() error {
		through[project] = measures.Midnight(day)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("read how far the stored figures of %v are built: %w", projects, err)
	}
	return through, nil
}
