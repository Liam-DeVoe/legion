package evals

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/delivery/measures"
)

// The computed day rows' write (dispatch://INFRA-2916, R12), shared by every family of computed
// evals: one project's rows of one UTC day and the items they counted (ProjectDay), written in one
// transaction (writeProjectDay) for the delivery and CI evals (writeDeliveryDay, delivery_days.go)
// and for the Legion evals (writeLegionDay, legion_days.go), and the statements that write
// the rows themselves (upsertDays, deleteStaleDays), which the unowned-p0 write (WriteUnownedP0Day)
// writes its row through too.

// Member is one item a project's day counted, as eval_day_members records it beside the day: its
// family (KindDelivery, KindCI or KindLegion), the deploy repository it was counted under ("" for
// an item no deploy repository moves; deploy_repos.go), its row key (PullRequestMember, RunMember,
// an issue's key), its repository and, for a pull request a deploy shipped, the deploy run's row
// key (ShippedBy), which is how the refresher finds the day of the run that shipped it once
// containment moves it to another run.
type Member struct {
	Family     string
	DeployRepo string
	RowKey     string
	Repo       string
	ShippedBy  string
}

// ProjectDay is one project's computed rows of one UTC day, as writeDeliveryDay and
// writeLegionDay write them: the day rows (their built_by is the write's, and their
// updated_at its moment where a value moved) and the items they counted; and Rewind, where set,
// the day the walk of the day's family stands at no later than once the write commits
// (refresh.writeDelivery's rule): a write outside a walk that moves a series' first input day
// earlier, or creates one, leaves every day after it to the walk, and one that finds a series'
// first input day has lost its input leaves that day to it too.
type ProjectDay struct {
	Project string
	// Day is the day's UTC midnight.
	Day     time.Time
	Rows    []DayRow
	Members []Member
	Rewind  *time.Time
}

// WithZeros is p with each of zeros (the explicit zero rows of ZeroDeliveryDay) that p holds no row
// of the same eval, deploy repository, series and repository of: the refresher's zero row of a
// series on a day the series has no input, which never stands in for a row the day built.
func (p ProjectDay) WithZeros(zeros []DayRow) ProjectDay {
	held := map[[4]string]bool{}
	for _, row := range p.Rows {
		held[[4]string{row.EvalID, row.DeployRepo, row.Series, row.Repo}] = true
	}
	p.Rows = slices.Clone(p.Rows)
	for _, zero := range zeros {
		if !held[[4]string{zero.EvalID, zero.DeployRepo, zero.Series, zero.Repo}] {
			p.Rows = append(p.Rows, zero)
		}
	}
	return p
}

// writeProjectDay is the write of one project's computed rows of one day: day's rows of the evals
// slugs names, of every deploy repository the day holds rows of, and its members of families,
// written as writeDeliveryDay says. The stale rows it deletes are those of slugs alone and the
// stale members it deletes those of families alone, so the delivery and the Legion writes of one
// day leave each other's rows and members as they are. A row of an eval keyed by the deploy
// repository names the repository it is computed as if it were it, and every other row none
// (deploy_repos.go). what names the rows in a refusal. Its change rows wait in the change log's
// outbox (migration 20261117100014), so the transaction takes no change log lock, and they are
// moved into the log once it has committed (drainChanges). Where figures is set, the same
// transaction folds again the stored figures of the day whose rows it moved (foldFiguresInTx); a
// walk's day leaves them to the end of its batch (refresh.foldWalked). Where day.Rewind is set,
// the same transaction moves walk, the walk of the day's family (the delivery walk, or the
// project's Legion walk), back to that day, every build's, where it stands later: so a pass that
// fails after the write still leaves those days to the walk, which builds them from their own
// input, as gap rows written in the write's transaction were kept before. A walk to begin (no
// next day) begins at the first input day, and one behind the day stays where it is. The walk's
// record is written after the rows and the figures, the order every writer of the three takes.
func (s *Store) writeProjectDay(ctx context.Context, what string, day ProjectDay, slugs, families []string, walk walkKey, builtBy string, figures bool) error {
	date := measures.Midnight(day.Day)
	evalIDs := make([]string, len(slugs))
	for i, slug := range slugs {
		evalIDs[i] = EvalID(day.Project, slug)
	}
	for _, row := range day.Rows {
		if keyed := readsDeployRepo(slugOf(day.Project, row.EvalID)); keyed != (row.DeployRepo != "") {
			return fmt.Errorf("evals: a %s day row of %s on %s is of %s and deploy repository %q, and that eval is keyed by the deploy repository %t",
				what, day.Project, date.Format(time.DateOnly), row.EvalID, row.DeployRepo, keyed)
		}
		if row.ProjectKey != day.Project || !row.Day.Equal(date) || !slices.Contains(evalIDs, row.EvalID) {
			return fmt.Errorf("evals: a %s day row of %s on %s is %s of %s on %s", what, day.Project, date.Format(time.DateOnly),
				row.EvalID, row.ProjectKey, row.Day.Format(time.DateOnly))
		}
	}
	for _, member := range day.Members {
		if !slices.Contains(families, member.Family) {
			return fmt.Errorf("evals: a %s day item of %s on %s is %s of family %s", what, day.Project, date.Format(time.DateOnly),
				member.RowKey, member.Family)
		}
	}
	tx, err := s.database.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin the day rows of %s on %s: %w", day.Project, date.Format(time.DateOnly), err)
	}
	defer tx.Rollback(ctx)
	if err := upsertDays(ctx, tx, day.Rows, builtBy); err != nil {
		return err
	}
	if err := deleteStaleDays(ctx, tx, day.Project, date, evalIDs, day.Rows); err != nil {
		return err
	}
	count := len(day.Members)
	memberFamilies, deployRepos, keys := make([]string, count), make([]string, count), make([]string, count)
	memberRepos, shippedBy := make([]string, count), make([]string, count)
	for i, member := range day.Members {
		memberFamilies[i], deployRepos[i], keys[i] = member.Family, member.DeployRepo, member.RowKey
		memberRepos[i], shippedBy[i] = member.Repo, member.ShippedBy
	}
	// An item this write takes from another day leaves that day's rows counting it until the day
	// is written again: one it moves here from the day it was counted on, and, for a pull request
	// it moves to another deploy run or stops counting, the day of the run that shipped it, which
	// that run's repository's rows count it on. Each such day is recorded owed (eval_days_owed,
	// migration 20261117100006) in this transaction, before the members that name it move, so a
	// pass that fails before it rewrites that day still finds it (owedDays) where no stored member
	// names it any more.
	if _, err := tx.Exec(ctx, leftDaysOwedStatement, day.Project, date, memberFamilies, deployRepos, keys, shippedBy, families, KindDelivery); err != nil {
		return fmt.Errorf("record the days %s's items left for %s: %w", day.Project, date.Format(time.DateOnly), err)
	}
	// A member whose day, repository and shipping run stand is not written again.
	if _, err := tx.Exec(ctx, `
		insert into eval_day_members (project_key, family, deploy_repo, row_key, day, repo, shipped_by)
		select $1, member.family, member.deploy_repo, member.row_key, $2, member.repo, member.shipped_by
		from unnest($3::text[], $4::text[], $5::text[], $6::text[], $7::text[]) as member(family, deploy_repo, row_key, repo, shipped_by)
		order by member.family, member.deploy_repo, member.row_key
		on conflict (project_key, family, deploy_repo, row_key) do update
		set day = excluded.day, repo = excluded.repo, shipped_by = excluded.shipped_by
		where (eval_day_members.day, eval_day_members.repo, eval_day_members.shipped_by)
			is distinct from (excluded.day, excluded.repo, excluded.shipped_by)
	`, day.Project, date, memberFamilies, deployRepos, keys, memberRepos, shippedBy); err != nil {
		return fmt.Errorf("record the items %s counted on %s: %w", day.Project, date.Format(time.DateOnly), err)
	}
	if _, err := tx.Exec(ctx, goneMembersDeleteStatement, day.Project, families, date, memberFamilies, deployRepos, keys); err != nil {
		return fmt.Errorf("delete the items %s no longer counts on %s: %w", day.Project, date.Format(time.DateOnly), err)
	}
	// This write is the rewrite a day owed of its families.
	if _, err := tx.Exec(ctx, `delete from eval_days_owed where project_key = $1 and family = any($2) and day = $3`, day.Project, families, date); err != nil {
		return fmt.Errorf("clear what %s owed %s: %w", day.Project, date.Format(time.DateOnly), err)
	}
	if figures {
		spans := make(map[string]daySpan, len(evalIDs))
		for _, evalID := range evalIDs {
			spans[evalID] = daySpan{first: date, last: date}
		}
		if err := foldFiguresInTx(ctx, tx, builtBy, day.Project, spans); err != nil {
			return err
		}
	}
	if day.Rewind != nil {
		if _, err := tx.Exec(ctx, `
			update eval_days_walks set next_day = $3, updated_at = now()
			where family = $1 and project_key = $2 and next_day > $3
		`, walk.family, walk.project, measures.Midnight(*day.Rewind)); err != nil {
			return fmt.Errorf("move the %s walk of %q back to %s: %w", walk.family, walk.project, day.Rewind.Format(time.DateOnly), err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit the day rows of %s on %s: %w", day.Project, date.Format(time.DateOnly), err)
	}
	s.drainChanges(ctx)
	return nil
}

// The statements of a day's write that find what the day no longer holds (leftDaysOwedStatement's
// and goneMembersDeleteStatement's members, deleteStaleDaysStatement's and
// deleteStaleSettingsDaysStatement's rows) each find it as the stored keys except the given ones
// (`gone`), materialized, so the set operation runs once whatever plan the statement is given,
// and joined back to the table by its primary key: a set operation reads each side once, and the
// join back probes the key once per row that left. Written as `not in (…)` over the given list,
// the filter ran the list again for each stored row once the list outgrew work_mem: one day's
// write of k20's load at INFRA's size held eval_days for 13 minutes, and a day of 80,000 items
// took more than 180 s. Written as `not exists`, the generic plan a statement pgx caches can
// settle on after its fifth run estimates the list at 10 rows, and can run it again for each
// stored row as a nested-loop anti-join. TestEachFilterOfWhatADayNoLongerHoldsReadsItsListOnce
// holds each to its plan.

// leftDaysOwedStatement records owed (eval_days_owed) each day other than the project's day $2
// that an item of the write leaves, as writeProjectDay says: $1 the project, $3 to $6 the day's
// items (family, deploy repository, row key, shipping run), $7 the write's families and $8
// KindDelivery. held is every item that moves: each given item stored anywhere, read by its
// primary key, and each item of $7 stored on the day that the day no longer holds (gone).
const leftDaysOwedStatement = `
	with given as (
		select * from unnest($3::text[], $4::text[], $5::text[], $6::text[]) as given(family, deploy_repo, row_key, shipped_by)
	), gone as materialized (
		select family, deploy_repo, row_key from eval_day_members
		where project_key = $1 and family = any($7) and day = $2
		except
		select family, deploy_repo, row_key from given
	), held as (
		select stored.family, stored.deploy_repo, stored.row_key, stored.day, stored.shipped_by, given.shipped_by as shipped_now
		from given join eval_day_members stored
			on stored.project_key = $1 and stored.family = given.family and stored.deploy_repo = given.deploy_repo
				and stored.row_key = given.row_key
		union all
		select stored.family, stored.deploy_repo, stored.row_key, stored.day, stored.shipped_by, null
		from gone join eval_day_members stored
			on stored.project_key = $1 and stored.family = gone.family and stored.deploy_repo = gone.deploy_repo
				and stored.row_key = gone.row_key
	), left_days as (
		select family, day from held where day <> $2
		union
		select run.family, run.day from held
		join eval_day_members run on run.project_key = $1 and run.family = $8 and run.deploy_repo = held.deploy_repo
			and run.row_key = held.shipped_by
		where held.shipped_by <> '' and held.shipped_by is distinct from held.shipped_now
	)
	insert into eval_days_owed (project_key, family, day)
	select $1, family, day from left_days where day <> $2
	on conflict do nothing
`

// goneMembersDeleteStatement deletes the items of the families $2 that project $1 records on day
// $3 and that the day no longer holds, the day's items being $4 to $6 (family, deploy repository,
// row key). The deleted row is held to the day again, so an item another write moved to another
// day while this one waited for its row lock stays where that write put it.
const goneMembersDeleteStatement = `
	with gone as materialized (
		select family, deploy_repo, row_key from eval_day_members
		where project_key = $1 and family = any($2) and day = $3
		except
		select * from unnest($4::text[], $5::text[], $6::text[]) as given(family, deploy_repo, row_key)
	)
	delete from eval_day_members stored
	using gone
	where stored.project_key = $1 and stored.family = gone.family and stored.deploy_repo = gone.deploy_repo
		and stored.row_key = gone.row_key and stored.day = $3
`

// upsertDays writes rows in key order, in one statement where they fit and otherwise in several
// (writeDayRows), each the computation's (writer `computation`) with built_by builtBy: a new row
// is inserted, and a stored one rewritten only where a value differs (built_by included, so a new
// build's rebuild marks every row it rebuilt). updated_at moves only with a value: the trigger of
// eval_days (migration 20261117100014) logs no change for a rewrite that moves built_by and
// updated_at alone, and the evals list answers the oldest updated_at of the rows it folds
// (api/evals.go's freshness), so a rebuild that moved it would leave a live evals view answering
// the old one while the read answered the new.
func upsertDays(ctx context.Context, tx pgx.Tx, rows []DayRow, builtBy string) error {
	return writeDayRows(ctx, tx, rows, builtBy, true, `
		on conflict (project_key, eval_id, writer, deploy_repo, series, repo, day) do update
		set listed = excluded.listed, n = excluded.n, sum = excluded.sum, num = excluded.num, den = excluded.den,
			max = excluded.max, samples = excluded.samples, last_value = excluded.last_value, last_at = excluded.last_at,
			last_recorded_at = excluded.last_recorded_at, last_reading = excluded.last_reading,
			counts = excluded.counts, via_repository = excluded.via_repository, built_by = excluded.built_by,
			updated_at = case
				when (eval_days.listed, eval_days.n, eval_days.sum, eval_days.num, eval_days.den, eval_days.max,
						eval_days.samples, eval_days.last_value, eval_days.last_at, eval_days.last_recorded_at,
						eval_days.last_reading, eval_days.counts, eval_days.via_repository)
					is distinct from (excluded.listed, excluded.n, excluded.sum, excluded.num, excluded.den, excluded.max,
						excluded.samples, excluded.last_value, excluded.last_at, excluded.last_recorded_at,
						excluded.last_reading, excluded.counts, excluded.via_repository)
				then now() else eval_days.updated_at end
		where (eval_days.listed, eval_days.n, eval_days.sum, eval_days.num, eval_days.den, eval_days.max,
				eval_days.samples, eval_days.last_value, eval_days.last_at, eval_days.last_recorded_at,
				eval_days.last_reading, eval_days.counts, eval_days.via_repository, eval_days.built_by)
			is distinct from (excluded.listed, excluded.n, excluded.sum, excluded.num, excluded.den, excluded.max,
				excluded.samples, excluded.last_value, excluded.last_at, excluded.last_recorded_at,
				excluded.last_reading, excluded.counts, excluded.via_repository, excluded.built_by)`)
}

// deleteStaleDays deletes each of the computation's stored rows of project's day of the evals
// evalIDs names, of every deploy repository, that held is not among: the rows the day no longer
// builds. A day holds exactly the rows its builders build from its input, beside the zero row of
// each all-repositories series it has no input of from that series' first input day on
// (refresh.writeDelivery), so a row whose input left (a repository whose last pull request moved
// away, a card the settings no longer make, a job no run reports any more) goes, as a whole walk
// over the same inputs and settings holds no such row; the all-repositories zero rows the day
// keeps are among held (ProjectDay.WithZeros). The eval_days trigger logs the delete (migration
// 20261117100014), so a view of the row's numbers wakes to it. The readings write's rows of those
// evals (an eval a register write made logged for a while) are its own, and stay as they are.
// held grows with the day, so it goes in statements of at most statementBytes of parameters
// (staleRuns), all in tx, each reading the stored rows of its own range of keys: the statements
// together delete what one would.
func deleteStaleDays(ctx context.Context, tx pgx.Tx, project string, day time.Time, evalIDs []string, held []DayRow) error {
	base := paramBytes(7, project, writerComputation) + 4 + 8
	for _, evalID := range evalIDs {
		base += 4 + len(evalID)
	}
	ordered := make([]*DayRow, len(held))
	for i := range held {
		ordered[i] = &held[i]
	}
	for _, run := range staleRuns(ordered, base, staleKeyBytes) {
		count := len(run.rows)
		evals, deployRepos, series, repos := make([]string, count), make([]string, count), make([]string, count), make([]string, count)
		for i, row := range run.rows {
			evals[i], deployRepos[i], series[i], repos[i] = row.EvalID, row.DeployRepo, row.Series, row.Repo
		}
		if _, err := tx.Exec(ctx, deleteStaleDaysStatement, project, day, evalIDs, writerComputation, evals, deployRepos, series, repos,
			staleKey(run.from), staleKey(run.until)); err != nil {
			return fmt.Errorf("delete the day rows %s no longer holds on %s: %w", project, day.Format(time.DateOnly), err)
		}
	}
	return nil
}

// deleteStaleDaysStatement is deleteStaleDays' statement: $1 the project, $2 the day, $3 the evals,
// $4 writerComputation, $5 to $8 the rows the day holds of the statement's range of keys (eval,
// deploy repository, series, repository), and $9 and $10 that range, from $9 up to $10 in the byte
// order of the keys (staleKey), null for no bound. gone is the stored rows' keys of the range
// except the given ones.
const deleteStaleDaysStatement = `
	with gone as materialized (
		select eval_id, deploy_repo, series, repo from eval_days
		where project_key = $1 and day = $2 and eval_id = any($3) and writer = $4
			and ($9::text[] is null
				or (eval_id collate "C", deploy_repo collate "C", series collate "C", repo collate "C") >= ($9[1], $9[2], $9[3], $9[4]))
			and ($10::text[] is null
				or (eval_id collate "C", deploy_repo collate "C", series collate "C", repo collate "C") < ($10[1], $10[2], $10[3], $10[4]))
		except
		select * from unnest($5::text[], $6::text[], $7::text[], $8::text[]) as given(eval_id, deploy_repo, series, repo)
	)
	delete from eval_days stored
	using gone
	where stored.project_key = $1 and stored.eval_id = gone.eval_id and stored.writer = $4
		and stored.deploy_repo = gone.deploy_repo and stored.series = gone.series and stored.repo = gone.repo
		and stored.day = $2
`

// staleKeyBytes is the most bytes row adds to the given keys of deleteStaleDays' statement, as pgx
// encodes them in binary: in each of its four arrays 4 bytes of length and the element. A bound of
// the statement's range (staleKey) takes as many.
func staleKeyBytes(row *DayRow) int {
	return 4*4 + len(row.EvalID) + len(row.DeployRepo) + len(row.Series) + len(row.Repo)
}

// staleKey is row's key as a bound of deleteStaleDays' range (eval, deploy repository, series,
// repository), nil for no bound.
func staleKey(row *DayRow) []string {
	if row == nil {
		return nil
	}
	return []string{row.EvalID, row.DeployRepo, row.Series, row.Repo}
}

// staleRun is one statement's part of a delete of what days no longer hold: its rows of the held
// ones, in the byte order of their keys, and the range of keys whose stored rows it reads, from
// its first row's key (from, nil for the first statement, which reads from the first key) up to
// the first row of the next statement's (until, nil for the last, which reads to the last).
type staleRun struct {
	rows        []*DayRow
	from, until *DayRow
}

// staleRuns is held, sorted in place in the byte order of their keys (compareDayRows), cut into
// the runs one statement each sends, as statementChunks cuts them: base the most bytes the
// statement's parameters take with no row and no bound, and size the most a row adds, which each
// of its two bounds takes too. Each run reads the stored rows of the keys from its first row's up
// to the next run's first row's, so the runs' ranges cover every key once, and a stored key
// outside held is in one run's range and among that run's rows nowhere. Where held is empty it is
// one run of no row over every key: the days no longer hold any row.
func staleRuns(held []*DayRow, base int, size func(*DayRow) int) []staleRun {
	widest := 0
	for _, row := range held {
		widest = max(widest, size(row))
	}
	slices.SortFunc(held, compareDayRows)
	chunks := slices.Collect(statementChunks(held, base+2*widest, func(row **DayRow) int { return size(*row) }))
	if len(chunks) == 0 {
		return []staleRun{{}}
	}
	runs := make([]staleRun, len(chunks))
	for i, chunk := range chunks {
		runs[i].rows = chunk
		if i > 0 {
			runs[i].from = chunk[0]
		}
		if i+1 < len(chunks) {
			runs[i].until = chunks[i+1][0]
		}
	}
	return runs
}

// writeDayRows inserts rows, each the computation's (writer `computation`) with built_by builtBy,
// and meets a row already stored as conflict says. It takes them in the byte order of their keys
// (project, eval, deploy repository, series, repository, day), the order `collate "C"` gives them
// and every statement that inserts or locks day rows takes them in (compareDayKeys), whatever the
// database's collation: dayRowStatements sends them in that order (compareDayRows, whose order
// TestEachWritesGoKeyOrderIsPostgresCollateCOrder holds to `collate "C"`'s), and the statement
// takes them in the order it is sent them (`with ordinality`), which Postgres knows its unnest
// answers and so sorts nothing for. It sends them in one statement where their parameters fit in
// statementBytes, and otherwise in a statement for each run of them in that order that fits
// (dayRowStatements), all in tx, so the statements together insert and lock the rows in the order
// one statement does. Every statement of a transaction widens the one eval_days change it logs of
// an eval (migration 20261117100014), so several statements log what one would. updates says that
// conflict updates the stored row, and so an upsert writes a row once: two rows of one key are
// then refused before any statement is sent, as one statement refuses them (ON CONFLICT DO UPDATE
// cannot affect a row a second time), where cut into two statements the second would rewrite the
// first. A conflict that does nothing keeps the first row of a key in the order sent and skips the
// rest, in one statement or in several alike.
func writeDayRows(ctx context.Context, tx pgx.Tx, rows []DayRow, builtBy string, updates bool, conflict string) error {
	statements, err := dayRowStatements(rows, builtBy, updates)
	if err != nil {
		return err
	}
	sql := `
		insert into eval_days (project_key, eval_id, writer, deploy_repo, series, repo, day, listed, n, sum, num, den, max, samples,
			last_value, last_at, last_recorded_at, last_reading, counts, via_repository, built_by)
		select project_key, eval_id, $18, deploy_repo, series, repo, day, listed, n, sum, num, den, max, samples::double precision[],
			last_value, last_at, last_recorded_at, nullif(last_reading, '')::uuid, counts::jsonb, via_repository, $17
		from unnest($1::text[], $2::text[], $21::text[], $3::text[], $4::text[], $5::date[], $6::bool[], $7::int[],
			$8::double precision[], $9::double precision[], $10::double precision[], $11::double precision[], $12::text[],
			$13::double precision[], $14::timestamptz[], $19::timestamptz[], $20::text[], $15::text[], $16::int[]) with ordinality
			as given(project_key, eval_id, deploy_repo, series, repo, day, listed, n, sum, num, den, max, samples,
				last_value, last_at, last_recorded_at, last_reading, counts, via_repository, sent)
		order by given.sent
		` + conflict
	for args := range statements {
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			return fmt.Errorf("write the day rows of %s: %w", rows[0].ProjectKey, err)
		}
	}
	return nil
}

// dayRowArrays is how many arrays writeDayRows' statement takes, one for each column of a row it
// is given, beside its two scalars (the build and writerComputation).
const dayRowArrays = 19

// dayRowStatements is the arguments of each statement writeDayRows sends of rows, recording
// builtBy: rows in the byte order of their keys (compareDayRows), cut into runs whose parameters
// take at most statementBytes as statementChunks cuts them, the statement's own parameters
// estimated by paramBytes and each row's by dayRowBytes, each statement's arrays one run. A row
// adds at least 157 bytes, so the bound cuts a statement long before statementRows rows; a row
// larger than the bound alone is sent whole, in a statement of its own. It encodes and checks
// every row before it answers, so a row whose counts do not encode as JSON is refused before any
// statement is sent, and so, where updates (the statement's conflict updates the stored row, and
// so an upsert writes a row once), are two rows of one key: those of one key on one UTC day are
// next to each other in that order, since a row between them by instant falls on that day.
func dayRowStatements(rows []DayRow, builtBy string, updates bool) (iter.Seq[[]any], error) {
	count := len(rows)
	// Their indexes are sorted rather than the rows, so the caller's rows keep their order.
	order := make([]int, count)
	for i := range order {
		order[i] = i
	}
	slices.SortFunc(order, func(a, b int) int { return compareDayRows(&rows[a], &rows[b]) })
	projects, evals, deployRepos, series, repos := make([]string, count), make([]string, count), make([]string, count), make([]string, count), make([]string, count)
	days, lastAts, lastRecordedAts := make([]time.Time, count), make([]*time.Time, count), make([]*time.Time, count)
	listed := make([]bool, count)
	ns, via := make([]int32, count), make([]int32, count)
	sums, nums, dens, maxes, lastValues := make([]*float64, count), make([]*float64, count), make([]*float64, count), make([]*float64, count), make([]*float64, count)
	samples, counts, lastReadings := make([]*string, count), make([]string, count), make([]string, count)
	sizes := make([]int, count)
	for i, index := range order {
		row := &rows[index]
		projects[i], evals[i], deployRepos[i], series[i], repos[i] = row.ProjectKey, row.EvalID, row.DeployRepo, row.Series, row.Repo
		days[i], listed[i] = measures.Midnight(row.Day), row.Listed
		if updates && i > 0 && projects[i] == projects[i-1] && evals[i] == evals[i-1] && deployRepos[i] == deployRepos[i-1] &&
			series[i] == series[i-1] && repos[i] == repos[i-1] && days[i].Equal(days[i-1]) {
			return nil, fmt.Errorf("write the day rows of %s: two rows of eval %s, writer %s, deploy repository %q, series %q and repository %q on %s, and an upsert writes a row once",
				row.ProjectKey, row.EvalID, writerComputation, row.DeployRepo, row.Series, row.Repo, days[i].Format(time.DateOnly))
		}
		ns[i], via[i] = int32(row.N), int32(row.ViaRepository)
		sums[i], nums[i], dens[i], maxes[i], lastValues[i], lastAts[i] = row.Sum, row.Num, row.Den, row.Max, row.LastValue, row.LastAt
		lastRecordedAts[i], lastReadings[i] = row.LastRecordedAt, row.LastReading
		samples[i] = floatArray(row.Samples)
		encoded, err := json.Marshal(NonNilCounts(row.Counts))
		if err != nil {
			return nil, fmt.Errorf("encode the counts of a day row of %s: %w", row.EvalID, err)
		}
		counts[i] = string(encoded)
		sizes[i] = dayRowBytes(row, samples[i], counts[i])
	}
	return func(yield func([]any) bool) {
		from := 0
		for run := range statementChunks(sizes, paramBytes(dayRowArrays, builtBy, writerComputation), func(size *int) int { return *size }) {
			to := from + len(run)
			if !yield([]any{projects[from:to], evals[from:to], series[from:to], repos[from:to], days[from:to], listed[from:to], ns[from:to],
				sums[from:to], nums[from:to], dens[from:to], maxes[from:to], samples[from:to], lastValues[from:to], lastAts[from:to],
				counts[from:to], via[from:to], builtBy, writerComputation, lastRecordedAts[from:to], lastReadings[from:to], deployRepos[from:to]}) {
				return
			}
			from = to
		}
	}, nil
}

// dayRowBytes is the most bytes row adds to the parameters of writeDayRows' statement, as pgx
// encodes them in binary, samples and counts being the literal of its samples (floatArray) and its
// counts' JSON: in each of the statement's arrays 4 bytes of length and the element, each string
// its bytes, the listing 1, and each of its ten numbers, dates and times at most 8 (a null none).
func dayRowBytes(row *DayRow, samples *string, counts string) int {
	bytes := dayRowArrays*4 + len(row.ProjectKey) + len(row.EvalID) + len(row.DeployRepo) + len(row.Series) + len(row.Repo) +
		len(counts) + len(row.LastReading) + 1 + 10*8
	if samples != nil {
		bytes += len(*samples)
	}
	return bytes
}

// compareDayRows orders day rows by the bytes of their keys, the order writeDayRows' statement
// takes them in as it is sent them: project, eval and deploy repository, then series, repository
// and day (compareDayKeys), each day by its instant, since no instant falls on a UTC date (the
// statement's day) before an earlier instant's.
func compareDayRows(a, b *DayRow) int {
	return cmp.Or(strings.Compare(a.ProjectKey, b.ProjectKey), strings.Compare(a.EvalID, b.EvalID), strings.Compare(a.DeployRepo, b.DeployRepo),
		compareDayKeys(dayKey{series: a.Series, repo: a.Repo, day: a.Day}, dayKey{series: b.Series, repo: b.Repo, day: b.Day}))
}

// floatArray is values as a Postgres array literal, each the shortest decimal that reads back as
// the same float, so a stored sample is the builder's to the last bit; nil for none.
func floatArray(values []float64) *string {
	if values == nil {
		return nil
	}
	var text strings.Builder
	text.WriteByte('{')
	for i, value := range values {
		if i > 0 {
			text.WriteByte(',')
		}
		text.WriteString(strconv.FormatFloat(value, 'g', -1, 64))
	}
	text.WriteByte('}')
	encoded := text.String()
	return &encoded
}

// NonNilCounts is a day row's counts as it is stored and answered: {} where it holds none, never
// null.
func NonNilCounts(counts map[string]float64) map[string]float64 {
	if counts == nil {
		return map[string]float64{}
	}
	return counts
}
