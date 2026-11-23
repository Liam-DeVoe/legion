package evals

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"maps"
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
// and for the Legion evals (writeLegionDay, legion_days.go), or the days a page names, of every
// project, in one transaction (writeProjectDays), and the statements that write the rows
// themselves (upsertDays, deleteStaleDays), which the unowned-p0 write (WriteUnownedP0Day) writes
// its row through too.

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
// written as writeDeliveryDay says, in a transaction of its own (writeProjectDays of one day).
func (s *Store) writeProjectDay(ctx context.Context, family dayFamily, day ProjectDay, builtBy string, figures bool) error {
	return s.writeProjectDays(ctx, family, []ProjectDay{day}, builtBy, figures)
}

// writeProjectDays is the write of days, each one project's computed rows of one day, in one
// transaction: each day's rows of family's evals, of every deploy repository the day holds rows
// of, and its members of family's families, written as writeDeliveryDay says, the statements of
// every day at once, each cut at statementBytes (writeDayRowsIn, writeDayMembersIn). The stale
// rows it deletes are those of family's evals alone and the stale members it deletes those of its
// families alone, so the delivery and the Legion writes of one day leave each other's rows and
// members as they are. A row of an eval keyed by the deploy repository names the repository it is
// computed as if it were it, and every other row none (deploy_repos.go). family.what names the
// rows in a refusal. Its change rows wait in the change log's outbox (migration 20261117100014), so
// the transaction takes no change log lock, and they are moved into the log once it has committed,
// in one drain (drainChanges): every statement of a transaction widens the one change it logs of an
// eval, so the days a page writes log one change of each eval they moved, and a view of the rows is
// sent one delta of them. Where figures is set, the same transaction folds again, once for each
// project, the stored figures of the span of its days whose rows it moved (foldFiguresInTx); a
// walk's day leaves them to the end of its batch (refresh.foldWalked). It writes the rows, then the
// members, then the figures, the order every writer of the three takes (checkProjectDay,
// writeDayRowsIn, writeDayMembersIn), which a page whose days move other days takes in its one
// commit too (rebuild). Where a rebuild is building, the triggers of eval_days and
// eval_day_members queue the days for it to build again (migration 20261117100021).
func (s *Store) writeProjectDays(ctx context.Context, family dayFamily, days []ProjectDay, builtBy string, figures bool) error {
	if len(days) == 0 {
		return nil
	}
	for _, day := range days {
		if err := checkProjectDay(family.what, day, family.slugs, family.families); err != nil {
			return err
		}
	}
	named := daysNamed(days)
	tx, err := s.database.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin the day rows of %s: %w", named, err)
	}
	defer tx.Rollback(ctx)
	if err := writeDayRowsIn(ctx, tx, days, family.slugs, builtBy); err != nil {
		return err
	}
	if err := writeDayMembersIn(ctx, tx, days, family.families); err != nil {
		return err
	}
	if figures {
		written := map[string]daySpan{}
		for _, day := range days {
			written[day.Project] = written[day.Project].widen(measures.Midnight(day.Day))
		}
		for _, project := range slices.Sorted(maps.Keys(written)) {
			spans := make(map[string]daySpan, len(family.slugs))
			for _, slug := range family.slugs {
				spans[EvalID(project, slug)] = written[project]
			}
			if err := foldFiguresInTx(ctx, tx, builtBy, project, spans); err != nil {
				return err
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit the day rows of %s: %w", named, err)
	}
	s.drainChanges(ctx)
	return nil
}

// daysNamed names days in an error: the one day's project and date, or how many days of how many
// projects.
func daysNamed(days []ProjectDay) string {
	if len(days) == 1 {
		return days[0].Project + " on " + measures.Midnight(days[0].Day).Format(time.DateOnly)
	}
	projects := map[string]bool{}
	for _, day := range days {
		projects[day.Project] = true
	}
	return fmt.Sprintf("%d days of %d projects", len(days), len(projects))
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

// writeDayRowsIn writes, in tx, days' rows of the evals slugs names as writeProjectDay writes them:
// each upserted where a value moved (upsertDays), and each project's stored rows of each of its
// days of those evals that day does not hold deleted (deleteStaleDays), the statements of every
// day at once.
func writeDayRowsIn(ctx context.Context, tx pgx.Tx, days []ProjectDay, slugs []string, builtBy string) error {
	if len(days) == 0 {
		return nil
	}
	var rows []DayRow
	for _, day := range days {
		rows = append(rows, day.Rows...)
	}
	if err := upsertDays(ctx, tx, rows, builtBy); err != nil {
		return err
	}
	return deleteStaleDays(ctx, tx, days, slugs)
}

// writtenDays is the projects and the days of days, pairwise: the days a write's statements that
// find what the days no longer hold read the stored rows and members of.
func writtenDays(days []ProjectDay) (projects []string, dates []time.Time) {
	projects, dates = make([]string, len(days)), make([]time.Time, len(days))
	for i, day := range days {
		projects[i], dates[i] = day.Project, measures.Midnight(day.Day)
	}
	return projects, dates
}

// writtenBytes is the most bytes the projects and days of writtenDays add to a statement's
// parameters, as pgx encodes them in binary: in each of the two arrays 4 bytes of length and the
// element, a date at most 8.
func writtenBytes(projects []string) int {
	bytes := 0
	for _, project := range projects {
		bytes += 4 + len(project) + 4 + 8
	}
	return bytes
}

// dayMember is one item a project's day counted, with the project and the day, as a write of
// several days takes the items of all of them (memberRuns).
type dayMember struct {
	project string
	day     time.Time
	Member
}

// memberRun is one statement's part of days' members: its members, in the byte order of their
// keys (project, family, deploy repository, row key), and the range of keys whose stored members
// the statements that find what the days no longer hold read, from its first member's key (from,
// nil for the first run, which reads from the first key) up to the next run's first member's
// (until, nil for the last, which reads to the last).
type memberRun struct {
	members     []dayMember
	from, until []string
}

// memberRuns is the members of days, in the byte order of their keys, cut into the runs one
// statement each takes, as staleRuns cuts the rows of the days: each run's parameters take at most
// statementBytes in any of the statements of the days' members (leftDays, the upsert and
// goneMembersDeleteStatement), with the days written (writtenDays) and families as their other
// parameters. The runs' ranges cover every key once, so a stored member a day no longer holds is
// in one run's range and among that run's members of that day nowhere, and the statements of the
// runs together find what one statement would. With no member it is one run of none over every
// key: the days hold no item any more.
func memberRuns(days []ProjectDay, families []string) []memberRun {
	var ordered []dayMember
	for _, day := range days {
		date := measures.Midnight(day.Day)
		for _, member := range day.Members {
			ordered = append(ordered, dayMember{project: day.Project, day: date, Member: member})
		}
	}
	slices.SortFunc(ordered, compareDayMembers)
	size := func(member *dayMember) int {
		return 7*4 + len(member.project) + 8 + len(member.Family) + len(member.DeployRepo) + len(member.RowKey) + len(member.Repo) + len(member.ShippedBy)
	}
	projects, _ := writtenDays(days)
	base, widest := paramBytes(11, KindDelivery)+writtenBytes(projects), 0
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
	key := func(member dayMember) []string {
		return []string{member.project, member.Family, member.DeployRepo, member.RowKey}
	}
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

// compareDayMembers orders the members of several days by the bytes of their keys: project,
// family, deploy repository and row key.
func compareDayMembers(a, b dayMember) int {
	return cmp.Or(strings.Compare(a.project, b.project), compareMembers(a.Member, b.Member))
}

// countedTwice reports whether one project's two days of days count one item: a page's reads of
// two days of a project made either side of a write that moved the item from one to the other.
// Every member a statement upserts is one row of eval_day_members, which holds an item on one day.
func countedTwice(days []ProjectDay) bool {
	seen := map[[4]string]bool{}
	for _, day := range days {
		for _, member := range day.Members {
			key := [4]string{day.Project, member.Family, member.DeployRepo, member.RowKey}
			if seen[key] {
				return true
			}
			seen[key] = true
		}
	}
	return false
}

// leftDaysArgs is the arguments leftDaysOwedStatement and leftDaysStatement take of one run of the
// members of the days written (projects and dates, pairwise), under families.
func leftDaysArgs(projects []string, dates []time.Time, run memberRun, families []string) []any {
	count := len(run.members)
	memberProjects, memberDays := make([]string, count), make([]time.Time, count)
	memberFamilies, deployRepos, keys, shippedBy := make([]string, count), make([]string, count), make([]string, count), make([]string, count)
	for i, member := range run.members {
		memberProjects[i], memberDays[i] = member.project, member.day
		memberFamilies[i], deployRepos[i], keys[i], shippedBy[i] = member.Family, member.DeployRepo, member.RowKey, member.ShippedBy
	}
	return []any{projects, dates, memberProjects, memberDays, memberFamilies, deployRepos, keys, shippedBy, families, KindDelivery, run.from, run.until}
}

// leftDays reads each day an item of days would leave were they written (leftDaysStatement), as
// the stored members stand: the days a page writes with them (refresh.nameLeft), every project's
// in one statement a run of their members (memberRuns).
func (s *Store) leftDays(ctx context.Context, days []ProjectDay, families []string) ([]projectDay, error) {
	if len(days) == 0 {
		return nil, nil
	}
	projects, dates := writtenDays(days)
	var left []projectDay
	for _, run := range memberRuns(days, families) {
		rows, err := s.database.Pool.Query(ctx, leftDaysStatement, leftDaysArgs(projects, dates, run, families)...)
		if err != nil {
			return nil, fmt.Errorf("read the days the items of %s left: %w", daysNamed(days), err)
		}
		var one projectDay
		if _, err := pgx.ForEachRow(rows, []any{&one.project, &one.day}, func() error {
			left = append(left, projectDay{project: one.project, day: measures.Midnight(one.day)})
			return nil
		}); err != nil {
			return nil, fmt.Errorf("read the days the items of %s left: %w", daysNamed(days), err)
		}
	}
	slices.SortFunc(left, func(a, b projectDay) int {
		return cmp.Or(strings.Compare(a.project, b.project), a.day.Compare(b.day))
	})
	return slices.Compact(left), nil
}

// writeDayMembersIn writes, in tx, after the days' rows, days' members of families as
// writeProjectDay writes them: the days their items leave recorded owed, the members upserted
// where their day, repository or shipping run moved, those of each day it no longer holds deleted,
// and what the days owed of families cleared. Each of the three takes the members of every day a
// run at a time (memberRuns), so no statement's parameters pass statementBytes however many items
// the days count: every run's left days are recorded before any member moves, then every run is
// upserted, then every run's range loses what each day no longer holds. A day the write's other
// days took an item from is among them, and their write clears what it owed with theirs. Where two
// days of one project count one item (countedTwice), which one statement cannot upsert twice, the
// days are written one after another in tx, the later in the days' order taking the item, as
// writes of the days in transactions of their own leave it.
func writeDayMembersIn(ctx context.Context, tx pgx.Tx, days []ProjectDay, families []string) error {
	if len(days) == 0 {
		return nil
	}
	if len(days) > 1 && countedTwice(days) {
		for _, day := range days {
			if err := writeDayMembersIn(ctx, tx, []ProjectDay{day}, families); err != nil {
				return err
			}
		}
		return nil
	}
	named := daysNamed(days)
	projects, dates := writtenDays(days)
	runs := memberRuns(days, families)
	// An item this write takes from another day leaves that day's rows counting it until the day
	// is written again: one it moves here from the day it was counted on, and, for a pull request
	// it moves to another deploy run or stops counting, the day of the run that shipped it, which
	// that run's repository's rows count it on. Each such day is recorded owed (eval_days_owed,
	// migration 20261117100006) in this transaction, before the members that name it move, so a
	// pass that fails before it rewrites that day still finds it (owedDays) where no stored member
	// names it any more.
	for _, run := range runs {
		if _, err := tx.Exec(ctx, leftDaysOwedStatement, leftDaysArgs(projects, dates, run, families)...); err != nil {
			return fmt.Errorf("record the days the items of %s left: %w", named, err)
		}
	}
	type columns struct {
		projects, families, deployRepos, keys, repos, shippedBy []string
		days                                                    []time.Time
	}
	of := func(run memberRun) columns {
		count := len(run.members)
		c := columns{make([]string, count), make([]string, count), make([]string, count), make([]string, count), make([]string, count),
			make([]string, count), make([]time.Time, count)}
		for i, member := range run.members {
			c.projects[i], c.families[i], c.deployRepos[i], c.keys[i] = member.project, member.Family, member.DeployRepo, member.RowKey
			c.repos[i], c.shippedBy[i], c.days[i] = member.Repo, member.ShippedBy, member.day
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
			select member.* from unnest($1::text[], $2::text[], $3::text[], $4::text[], $5::date[], $6::text[], $7::text[])
				as member(project_key, family, deploy_repo, row_key, day, repo, shipped_by)
			order by member.project_key collate "C", member.family collate "C", member.deploy_repo collate "C", member.row_key collate "C"
			on conflict (project_key, family, deploy_repo, row_key) do update
			set day = excluded.day, repo = excluded.repo, shipped_by = excluded.shipped_by
			where (eval_day_members.day, eval_day_members.repo, eval_day_members.shipped_by)
				is distinct from (excluded.day, excluded.repo, excluded.shipped_by)
		`, c.projects, c.families, c.deployRepos, c.keys, c.days, c.repos, c.shippedBy); err != nil {
			return fmt.Errorf("record the items %s counted: %w", named, err)
		}
	}
	for _, run := range runs {
		c := of(run)
		if _, err := tx.Exec(ctx, goneMembersDeleteStatement, projects, dates, families, c.projects, c.families, c.deployRepos, c.keys, c.days,
			run.from, run.until); err != nil {
			return fmt.Errorf("delete the items %s no longer counts: %w", named, err)
		}
	}
	// This write is the rewrite the days owed of its families.
	if _, err := tx.Exec(ctx, `
		delete from eval_days_owed owed
		using unnest($1::text[], $2::date[]) as written(project_key, day)
		where owed.project_key = written.project_key and owed.family = any($3) and owed.day = written.day
	`, projects, dates, families); err != nil {
		return fmt.Errorf("clear what %s owed: %w", named, err)
	}
	return nil
}

// The statements of a write of days that find what the days no longer hold (leftDaysOwedStatement's
// and goneMembersDeleteStatement's members, deleteStaleDaysStatement's and
// deleteStaleSettingsDaysStatement's rows) each find it as the stored keys of the days written
// except the given ones (`gone`), materialized, so the set operation runs once whatever plan the
// statement is given, and joined back to the table by its primary key: a set operation reads each
// side once, and the join back probes the key once per row that left. Written as `not in (…)` over
// the given list, the filter ran the list again for each stored row once the list outgrew
// work_mem: one day's write of k20's load at INFRA's size held eval_days for 13 minutes, and a day
// of 80,000 items took more than 180 s. Written as `not exists`, the generic plan a statement pgx
// caches can settle on after its fifth run estimates the list at 10 rows, and can run it again for
// each stored row as a nested-loop anti-join. TestEachFilterOfWhatADayNoLongerHoldsReadsItsListOnce
// holds each to its plan. Each takes the days written as two lists, their projects and days
// pairwise (writtenDays), the days a page writes in one transaction (writeProjectDays), and one
// day for a write of one. Each reads the table by its keys from those lists as a lateral read of
// an index fenced from the planner's joins (`offset 0`, or `limit 1` for a read of one primary key):
// the project and the day are no constant of the statement any more, and joined as tables the
// planner merged the stored members with a run's items on their project and family alone, testing
// each of the 80,000 members of TestEachFilterOfWhatADayNoLongerHoldsReadsItsListOnce's day
// against each of the 2,700 items of a run (a minute a statement), and read eval_days whole for
// each day written.

// leftDays is the days an item of a write of days leaves, each other than the day of the write
// that leaves it (written_day), as writeProjectDay says, the common table left_days: $1 and $2 the
// days written (writtenDays), $3 to $8 one run of their items (project, day, family, deploy
// repository, row key, shipping run; memberRuns), $9 the write's families, $10 KindDelivery, and
// $11 and $12 the run's range of keys, from $11 up to $12 in the byte order of the keys (project,
// family, deploy repository, row key), null for no bound. held is every item that moves: each
// given item stored anywhere, read by its primary key, and each item of $9 stored on a day written,
// within the run's range, that the day no longer holds (gone), each with the day of the write that
// moves it.
const leftDays = `
	with written as materialized (
		select * from unnest($1::text[], $2::date[]) as written(project_key, day)
	), given as (
		select * from unnest($3::text[], $4::date[], $5::text[], $6::text[], $7::text[], $8::text[])
			as given(project_key, day, family, deploy_repo, row_key, shipped_by)
	), gone as materialized (
		select stored.project_key, stored.family, stored.deploy_repo, stored.row_key, stored.day
		from written cross join lateral (
			select stored.project_key, stored.family, stored.deploy_repo, stored.row_key, stored.day
			from eval_day_members stored
			where stored.project_key = written.project_key and stored.family = any($9) and stored.day = written.day
				and ($11::text[] is null or (stored.project_key collate "C", stored.family collate "C", stored.deploy_repo collate "C",
					stored.row_key collate "C") >= ($11[1], $11[2], $11[3], $11[4]))
				and ($12::text[] is null or (stored.project_key collate "C", stored.family collate "C", stored.deploy_repo collate "C",
					stored.row_key collate "C") < ($12[1], $12[2], $12[3], $12[4]))
			offset 0
		) as stored
		except
		select project_key, family, deploy_repo, row_key, day from given
	), held as (
		select given.project_key, given.family, given.deploy_repo, given.row_key, stored.day, stored.shipped_by,
			given.shipped_by as shipped_now, given.day as written_day
		from given cross join lateral (
			select stored.day, stored.shipped_by from eval_day_members stored
			where stored.project_key = given.project_key and stored.family = given.family and stored.deploy_repo = given.deploy_repo
				and stored.row_key = given.row_key
			limit 1
		) as stored
		union all
		select gone.project_key, gone.family, gone.deploy_repo, gone.row_key, stored.day, stored.shipped_by, null, gone.day
		from gone cross join lateral (
			select stored.day, stored.shipped_by from eval_day_members stored
			where stored.project_key = gone.project_key and stored.family = gone.family and stored.deploy_repo = gone.deploy_repo
				and stored.row_key = gone.row_key
			limit 1
		) as stored
	), left_days as (
		select project_key, family, day, written_day from held where day <> written_day
		union
		select held.project_key, run.family, run.day, held.written_day
		from held cross join lateral (
			select run.family, run.day from eval_day_members run
			where run.project_key = held.project_key and run.family = $10 and run.deploy_repo = held.deploy_repo
				and run.row_key = held.shipped_by
			limit 1
		) as run
		where held.shipped_by <> '' and held.shipped_by is distinct from held.shipped_now
	)
`

// leftDaysOwedStatement records owed (eval_days_owed) each day an item of the write leaves
// (leftDays). A day the write writes too is cleared again by its own part of the write
// (writeDayMembersIn).
const leftDaysOwedStatement = leftDays + `
	insert into eval_days_owed (project_key, family, day)
	select distinct project_key, family, day from left_days where day <> written_day
	on conflict do nothing
`

// leftDaysStatement reads each day an item of a write of the days would leave (leftDays), each
// project's by its key, as the stored members stand: the days a page writes with them
// (refresh.nameLeft).
const leftDaysStatement = leftDays + `
	select distinct project_key, day from left_days where day <> written_day
`

// goneMembersDeleteStatement deletes the items of the families $3 that a project records on one of
// its days written ($1 and $2, writtenDays) and that the day no longer holds, one run of the days'
// items being $4 to $8 (project, family, deploy repository, row key, day; memberRuns) and $9 and
// $10 the run's range of keys, from $9 up to $10 in the byte order of the keys (project, family,
// deploy repository, row key), null for no bound. The deleted row is held to its day again, so an
// item another write moved to another day while this one waited for its row lock stays where that
// write put it.
const goneMembersDeleteStatement = `
	with written as materialized (
		select * from unnest($1::text[], $2::date[]) as written(project_key, day)
	), gone as materialized (
		select stored.project_key, stored.family, stored.deploy_repo, stored.row_key, stored.day
		from written cross join lateral (
			select stored.project_key, stored.family, stored.deploy_repo, stored.row_key, stored.day
			from eval_day_members stored
			where stored.project_key = written.project_key and stored.family = any($3) and stored.day = written.day
				and ($9::text[] is null or (stored.project_key collate "C", stored.family collate "C", stored.deploy_repo collate "C",
					stored.row_key collate "C") >= ($9[1], $9[2], $9[3], $9[4]))
				and ($10::text[] is null or (stored.project_key collate "C", stored.family collate "C", stored.deploy_repo collate "C",
					stored.row_key collate "C") < ($10[1], $10[2], $10[3], $10[4]))
			offset 0
		) as stored
		except
		select * from unnest($4::text[], $5::text[], $6::text[], $7::text[], $8::date[]) as given(project_key, family, deploy_repo, row_key, day)
	)
	delete from eval_day_members stored
	using gone
	where stored.project_key = gone.project_key and stored.family = gone.family and stored.deploy_repo = gone.deploy_repo
		and stored.row_key = gone.row_key and stored.day = gone.day
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

// deleteStaleDays deletes each of the computation's stored rows of each project's day of days of
// the evals slugs names, of every deploy repository, that the day does not hold: the rows the day
// no longer builds. A day holds exactly the rows its builders build from its input, beside the
// zero row of each all-repositories series it has no input of from that series' first input day on
// (refresh.writeDelivery), so a row whose input left (a repository whose last pull request moved
// away, a card the settings no longer make, a job no run reports any more) goes, as a whole walk
// over the same inputs and settings holds no such row; the all-repositories zero rows the day
// keeps are among its rows (ProjectDay.WithZeros). The eval_days trigger logs the delete
// (migration 20261117100014), so a view of the row's numbers wakes to it. The readings write's rows
// of those evals (an eval a register write made logged for a while) are its own, and stay as they
// are. The rows held grow with the days, so they go in statements of at most statementBytes of
// parameters (staleRuns), all in tx, each with every day written and reading the stored rows of
// its own range of keys: the statements together delete what one would.
func deleteStaleDays(ctx context.Context, tx pgx.Tx, days []ProjectDay, slugs []string) error {
	projects, dates := writtenDays(days)
	var evalIDs []string
	base := paramBytes(12, writerComputation) + writtenBytes(projects)
	for _, project := range slices.Compact(slices.Sorted(slices.Values(projects))) {
		for _, slug := range slugs {
			evalID := EvalID(project, slug)
			evalIDs, base = append(evalIDs, evalID), base+4+len(evalID)
		}
	}
	var held []*DayRow
	for i := range days {
		for j := range days[i].Rows {
			held = append(held, &days[i].Rows[j])
		}
	}
	for _, run := range staleRuns(held, base, settingsKeyBytes) {
		count := len(run.rows)
		heldProjects, heldEvals, heldDeployRepos := make([]string, count), make([]string, count), make([]string, count)
		heldSeries, heldRepos, heldDays := make([]string, count), make([]string, count), make([]time.Time, count)
		for i, row := range run.rows {
			heldProjects[i], heldEvals[i], heldDeployRepos[i] = row.ProjectKey, row.EvalID, row.DeployRepo
			heldSeries[i], heldRepos[i], heldDays[i] = row.Series, row.Repo, measures.Midnight(row.Day)
		}
		if _, err := tx.Exec(ctx, deleteStaleDaysStatement, projects, dates, evalIDs, writerComputation, heldProjects, heldEvals, heldDeployRepos,
			heldSeries, heldRepos, heldDays, settingsKey(run.from), settingsKey(run.until)); err != nil {
			return fmt.Errorf("delete the day rows %s no longer holds: %w", daysNamed(days), err)
		}
	}
	return nil
}

// deleteStaleDaysStatement is deleteStaleDays' statement: $1 and $2 the days written
// (writtenDays), $3 the evals, $4 writerComputation, $5 to $10 the rows the days hold of the
// statement's range of keys (project, eval, deploy repository, series, repository, day), and $11
// and $12 that range, from $11 up to $12 in the byte order of the keys (settingsKey), null for no
// bound. gone is the stored rows' keys of the days written of the range except the given ones,
// each day's read by its project, evals and day from eval_days' primary key, as the write of one
// day reads them.
const deleteStaleDaysStatement = `
	with written as materialized (
		select * from unnest($1::text[], $2::date[]) as written(project_key, day)
	), gone as materialized (
		select e.project_key, e.eval_id, e.deploy_repo, e.series, e.repo, e.day
		from written cross join lateral (
			select e.project_key, e.eval_id, e.deploy_repo, e.series, e.repo, e.day
			from eval_days e
			where e.project_key = written.project_key and e.eval_id = any($3) and e.writer = $4 and e.day = written.day
				and ($11::text[] is null
					or (e.project_key collate "C", e.eval_id collate "C", e.deploy_repo collate "C", e.series collate "C", e.repo collate "C", e.day)
						>= ($11[1], $11[2], $11[3], $11[4], $11[5], $11[6]::date))
				and ($12::text[] is null
					or (e.project_key collate "C", e.eval_id collate "C", e.deploy_repo collate "C", e.series collate "C", e.repo collate "C", e.day)
						< ($12[1], $12[2], $12[3], $12[4], $12[5], $12[6]::date))
			offset 0
		) as e
		except
		select * from unnest($5::text[], $6::text[], $7::text[], $8::text[], $9::text[], $10::date[])
			as held(project_key, eval_id, deploy_repo, series, repo, day)
	)
	delete from eval_days e
	using gone
	where e.project_key = gone.project_key and e.eval_id = gone.eval_id and e.writer = $4
		and e.deploy_repo = gone.deploy_repo and e.series = gone.series and e.repo = gone.repo and e.day = gone.day
`

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
