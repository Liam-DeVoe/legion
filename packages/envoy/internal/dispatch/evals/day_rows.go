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
// updated_at its moment where a value moved) and the items they counted.
type ProjectDay struct {
	Project string
	// Day is the day's UTC midnight.
	Day     time.Time
	Rows    []DayRow
	Members []Member
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

// writeProjectDay is the write of one project's computed rows of one day: day's rows of family's
// evals, of every deploy repository the day holds rows of, and its members of family's families,
// written as writeDeliveryDay says. The stale rows it deletes are those of family's evals alone and
// the stale members it deletes those of its families alone, so the delivery and the Legion writes
// of one day leave each other's rows and members as they are. A row of an eval keyed by the deploy
// repository names the repository it is computed as if it were it, and every other row none
// (deploy_repos.go). family.what names the rows in a refusal. Its change rows wait in the change
// log's outbox (migration 20261117100014), so the transaction takes no change log lock, and they
// are moved into the log once it has committed (drainChanges). Where figures is set, the same
// transaction folds again the stored figures of the day whose rows it moved (foldFiguresInTx); a
// walk's day leaves them to the end of its batch (refresh.foldWalked). It writes the rows, then
// the members, then the figures, the order every writer of the three takes (checkProjectDay,
// writeDayRowsIn, writeDayMembersIn), which a page that writes several days in its one commit
// takes over all of them (rebuild). Where a rebuild is building, the triggers of eval_days and
// eval_day_members queue the day for it to build again (migration 20261117100021).
func (s *Store) writeProjectDay(ctx context.Context, family dayFamily, day ProjectDay, builtBy string, figures bool) error {
	date := measures.Midnight(day.Day)
	if err := checkProjectDay(family.what, day, family.slugs, family.families); err != nil {
		return err
	}
	tx, err := s.database.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin the day rows of %s on %s: %w", day.Project, date.Format(time.DateOnly), err)
	}
	defer tx.Rollback(ctx)
	if err := writeDayRowsIn(ctx, tx, day, family.slugs, builtBy); err != nil {
		return err
	}
	if err := writeDayMembersIn(ctx, tx, day, family.families); err != nil {
		return err
	}
	if figures {
		spans := make(map[string]daySpan, len(family.slugs))
		for _, slug := range family.slugs {
			spans[EvalID(day.Project, slug)] = daySpan{first: date, last: date}
		}
		if err := foldFiguresInTx(ctx, tx, builtBy, day.Project, spans); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit the day rows of %s on %s: %w", day.Project, date.Format(time.DateOnly), err)
	}
	s.drainChanges(ctx)
	return nil
}

// checkProjectDay refuses day before anything of it is written: a row of another project, another
// day or an eval slugs does not name, a row whose deploy repository is set where its eval is not
// keyed by one or the other way round, and an item of a family families does not name.
func checkProjectDay(what string, day ProjectDay, slugs, families []string) error {
	date := measures.Midnight(day.Day)
	for _, row := range day.Rows {
		slug := slugOf(day.Project, row.EvalID)
		if keyed := readsDeployRepo(slug); keyed != (row.DeployRepo != "") {
			return fmt.Errorf("evals: a %s day row of %s on %s is of %s and deploy repository %q, and that eval is keyed by the deploy repository %t",
				what, day.Project, date.Format(time.DateOnly), row.EvalID, row.DeployRepo, keyed)
		}
		if row.ProjectKey != day.Project || !row.Day.Equal(date) || row.EvalID != EvalID(day.Project, slug) || !slices.Contains(slugs, slug) {
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
	return nil
}

// writeDayRowsIn writes, in tx, day's rows of the evals slugs names as writeProjectDay writes them:
// each upserted where a value moved (upsertDays), and the project's stored rows of that day of
// those evals that day does not hold deleted (deleteStaleDays).
func writeDayRowsIn(ctx context.Context, tx pgx.Tx, day ProjectDay, slugs []string, builtBy string) error {
	evalIDs := make([]string, len(slugs))
	for i, slug := range slugs {
		evalIDs[i] = EvalID(day.Project, slug)
	}
	if err := upsertDays(ctx, tx, day.Rows, builtBy); err != nil {
		return err
	}
	return deleteStaleDays(ctx, tx, day.Project, measures.Midnight(day.Day), evalIDs, day.Rows)
}

// memberRun is one statement's part of a day's members: its members, in the byte order of their
// keys (family, deploy repository, row key), and the range of keys whose stored members the
// statements that find what the day no longer holds read, from its first member's key (from, nil
// for the first run, which reads from the first key) up to the next run's first member's (until,
// nil for the last, which reads to the last).
type memberRun struct {
	members     []Member
	from, until []string
}

// memberRuns is members, in the byte order of their keys, cut into the runs one statement each
// takes, as staleRuns cuts the rows of a day: each run's parameters take at most statementBytes in
// any of the statements of a day's members (leftDays, the upsert and goneMembersDeleteStatement),
// with project and families as their other parameters. The runs' ranges cover every key once, so
// a stored member the day no longer holds is in one run's range and among that run's members
// nowhere, and the statements of the runs together find what one statement would. With no member
// it is one run of none over every key: the day holds no item any more.
func memberRuns(project string, members []Member, families []string) []memberRun {
	ordered := slices.Clone(members)
	slices.SortFunc(ordered, compareMembers)
	size := func(member *Member) int {
		return 5*4 + len(member.Family) + len(member.DeployRepo) + len(member.RowKey) + len(member.Repo) + len(member.ShippedBy)
	}
	base, widest := paramBytes(8, project, KindDelivery)+8, 0
	for _, family := range families {
		base += 4 + len(family)
	}
	for index := range ordered {
		widest = max(widest, size(&ordered[index]))
	}
	chunks := slices.Collect(statementChunks(ordered, base+2*widest, size))
	if len(chunks) == 0 {
		return []memberRun{{}}
	}
	key := func(member Member) []string { return []string{member.Family, member.DeployRepo, member.RowKey} }
	runs := make([]memberRun, len(chunks))
	for i, chunk := range chunks {
		runs[i].members = chunk
		if i > 0 {
			runs[i].from = key(chunk[0])
		}
		if i+1 < len(chunks) {
			runs[i].until = key(chunks[i+1][0])
		}
	}
	return runs
}

// compareMembers orders members by the bytes of their keys: family, deploy repository and row key.
func compareMembers(a, b Member) int {
	return cmp.Or(strings.Compare(a.Family, b.Family), strings.Compare(a.DeployRepo, b.DeployRepo), strings.Compare(a.RowKey, b.RowKey))
}

// leftDaysArgs is the arguments leftDaysOwedStatement and leftDaysStatement take of one run of
// project's day of members, under families.
func leftDaysArgs(project string, day time.Time, run memberRun, families []string) []any {
	count := len(run.members)
	memberFamilies, deployRepos, keys, shippedBy := make([]string, count), make([]string, count), make([]string, count), make([]string, count)
	for i, member := range run.members {
		memberFamilies[i], deployRepos[i], keys[i], shippedBy[i] = member.Family, member.DeployRepo, member.RowKey, member.ShippedBy
	}
	return []any{project, measures.Midnight(day), memberFamilies, deployRepos, keys, shippedBy, families, KindDelivery, run.from, run.until}
}

// writeDayMembersIn writes, in tx, after the day's rows, day's members of families as
// writeProjectDay writes them: the days its items leave recorded owed, the members upserted where
// their day, repository or shipping run moved, those of the day it no longer holds deleted, and
// what the day owed of families cleared. Each of the three takes the members a run at a time
// (memberRuns), so no statement's parameters pass statementBytes however many items the day
// counts: every run's left days are recorded before any member moves, then every run is upserted,
// then every run's range loses what the day no longer holds.
func writeDayMembersIn(ctx context.Context, tx pgx.Tx, day ProjectDay, families []string) error {
	date := measures.Midnight(day.Day)
	runs := memberRuns(day.Project, day.Members, families)
	// An item this write takes from another day leaves that day's rows counting it until the day
	// is written again: one it moves here from the day it was counted on, and, for a pull request
	// it moves to another deploy run or stops counting, the day of the run that shipped it, which
	// that run's repository's rows count it on. Each such day is recorded owed (eval_days_owed,
	// migration 20261117100006) in this transaction, before the members that name it move, so a
	// pass that fails before it rewrites that day still finds it (owedDays) where no stored member
	// names it any more.
	for _, run := range runs {
		if _, err := tx.Exec(ctx, leftDaysOwedStatement, leftDaysArgs(day.Project, date, run, families)...); err != nil {
			return fmt.Errorf("record the days %s's items left for %s: %w", day.Project, date.Format(time.DateOnly), err)
		}
	}
	type columns struct{ families, deployRepos, keys, repos, shippedBy []string }
	of := func(run memberRun) columns {
		count := len(run.members)
		c := columns{make([]string, count), make([]string, count), make([]string, count), make([]string, count), make([]string, count)}
		for i, member := range run.members {
			c.families[i], c.deployRepos[i], c.keys[i], c.repos[i], c.shippedBy[i] = member.Family, member.DeployRepo, member.RowKey, member.Repo, member.ShippedBy
		}
		return c
	}
	// A member whose day, repository and shipping run stand is not written again.
	for _, run := range runs {
		if len(run.members) == 0 {
			continue
		}
		c := of(run)
		if _, err := tx.Exec(ctx, `
			insert into eval_day_members (project_key, family, deploy_repo, row_key, day, repo, shipped_by)
			select $1, member.family, member.deploy_repo, member.row_key, $2, member.repo, member.shipped_by
			from unnest($3::text[], $4::text[], $5::text[], $6::text[], $7::text[]) as member(family, deploy_repo, row_key, repo, shipped_by)
			order by member.family collate "C", member.deploy_repo collate "C", member.row_key collate "C"
			on conflict (project_key, family, deploy_repo, row_key) do update
			set day = excluded.day, repo = excluded.repo, shipped_by = excluded.shipped_by
			where (eval_day_members.day, eval_day_members.repo, eval_day_members.shipped_by)
				is distinct from (excluded.day, excluded.repo, excluded.shipped_by)
		`, day.Project, date, c.families, c.deployRepos, c.keys, c.repos, c.shippedBy); err != nil {
			return fmt.Errorf("record the items %s counted on %s: %w", day.Project, date.Format(time.DateOnly), err)
		}
	}
	for _, run := range runs {
		c := of(run)
		if _, err := tx.Exec(ctx, goneMembersDeleteStatement, day.Project, families, date, c.families, c.deployRepos, c.keys, run.from, run.until); err != nil {
			return fmt.Errorf("delete the items %s no longer counts on %s: %w", day.Project, date.Format(time.DateOnly), err)
		}
	}
	// This write is the rewrite a day owed of its families.
	if _, err := tx.Exec(ctx, `delete from eval_days_owed where project_key = $1 and family = any($2) and day = $3`, day.Project, families, date); err != nil {
		return fmt.Errorf("clear what %s owed %s: %w", day.Project, date.Format(time.DateOnly), err)
	}
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

// leftDays is the days other than the project's day $2 that an item of a write of that day
// leaves, as writeProjectDay says, the common table left_days: $1 the project, $3 to $6 one run of
// the day's items (family, deploy repository, row key, shipping run; memberRuns), $7 the write's
// families, $8 KindDelivery, and $9 and $10 the run's range of keys, from $9 up to $10 in the
// byte order of the keys, null for no bound. held is every item that moves: each given item
// stored anywhere, read by its primary key, and each item of $7 stored on the day, within the
// run's range, that the day no longer holds (gone).
const leftDays = `
	with given as (
		select * from unnest($3::text[], $4::text[], $5::text[], $6::text[]) as given(family, deploy_repo, row_key, shipped_by)
	), gone as materialized (
		select family, deploy_repo, row_key from eval_day_members
		where project_key = $1 and family = any($7) and day = $2
			and ($9::text[] is null or (family collate "C", deploy_repo collate "C", row_key collate "C") >= ($9[1], $9[2], $9[3]))
			and ($10::text[] is null or (family collate "C", deploy_repo collate "C", row_key collate "C") < ($10[1], $10[2], $10[3]))
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
`

// leftDaysOwedStatement records owed (eval_days_owed) each day an item of the write leaves
// (leftDays).
const leftDaysOwedStatement = leftDays + `
	insert into eval_days_owed (project_key, family, day)
	select $1, family, day from left_days where day <> $2
	on conflict do nothing
`

// leftDaysStatement reads each day an item of a write of the project's day would leave (leftDays),
// as the stored members stand: the days a page that writes several days in one commit writes with
// them (refresh.nameLeft).
const leftDaysStatement = leftDays + `
	select distinct day from left_days where day <> $2
`

// goneMembersDeleteStatement deletes the items of the families $2 that project $1 records on day
// $3 and that the day no longer holds, one run of the day's items being $4 to $6 (family, deploy
// repository, row key; memberRuns) and $7 and $8 the run's range of keys, from $7 up to $8 in the
// byte order of the keys, null for no bound. The deleted row is held to the day again, so an item
// another write moved to another day while this one waited for its row lock stays where that
// write put it.
const goneMembersDeleteStatement = `
	with gone as materialized (
		select family, deploy_repo, row_key from eval_day_members
		where project_key = $1 and family = any($2) and day = $3
			and ($7::text[] is null or (family collate "C", deploy_repo collate "C", row_key collate "C") >= ($7[1], $7[2], $7[3]))
			and ($8::text[] is null or (family collate "C", deploy_repo collate "C", row_key collate "C") < ($8[1], $8[2], $8[3]))
		except
		select * from unnest($4::text[], $5::text[], $6::text[]) as given(family, deploy_repo, row_key)
	)
	delete from eval_day_members stored
	using gone
	where stored.project_key = $1 and stored.family = gone.family and stored.deploy_repo = gone.deploy_repo
		and stored.row_key = gone.row_key and stored.day = $3
`

// upsertDays writes rows in key order, in one statement where they fit and otherwise in several
// (writeDayRows), each the computation's (writer `computation`): a new row is inserted, and a
// stored one rewritten only where a value differs, recording builtBy as the build that wrote its
// values and the moment it did (updated_at). A row whose values stand is left as it is, its build
// and its moment with it, whichever build's process writes it: so a write that moves no value
// writes no row, a release among them, the eval_days trigger (migration 20261117100014) logs
// nothing, and the evals list's freshness, the oldest updated_at of the rows it folds
// (api/evals.go), stays the one a live evals view answers.
func upsertDays(ctx context.Context, tx pgx.Tx, rows []DayRow, builtBy string) error {
	return writeDayRows(ctx, tx, rows, builtBy, true, `
		on conflict (project_key, eval_id, writer, deploy_repo, series, repo, day) do update
		set listed = excluded.listed, n = excluded.n, sum = excluded.sum, num = excluded.num, den = excluded.den,
			max = excluded.max, samples = excluded.samples, last_value = excluded.last_value, last_at = excluded.last_at,
			last_recorded_at = excluded.last_recorded_at, last_reading = excluded.last_reading,
			counts = excluded.counts, via_repository = excluded.via_repository, built_by = excluded.built_by, updated_at = now()
		where (eval_days.listed, eval_days.n, eval_days.sum, eval_days.num, eval_days.den, eval_days.max,
				eval_days.samples, eval_days.last_value, eval_days.last_at, eval_days.last_recorded_at,
				eval_days.last_reading, eval_days.counts, eval_days.via_repository)
			is distinct from (excluded.listed, excluded.n, excluded.sum, excluded.num, excluded.den, excluded.max,
				excluded.samples, excluded.last_value, excluded.last_at, excluded.last_recorded_at,
				excluded.last_reading, excluded.counts, excluded.via_repository)`)
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
	return dayRowRuns(rows, builtBy, updates, paramBytes(dayRowArrays, builtBy, writerComputation))
}

// dayRowRuns is dayRowStatements with base the most bytes the statement's own parameters take, a
// statement that sends other scalars beside the arrays (stageRowsStatement) bounding its own.
func dayRowRuns(rows []DayRow, builtBy string, updates bool, base int) (iter.Seq[[]any], error) {
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
		for run := range statementChunks(sizes, base, func(size *int) int { return *size }) {
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
