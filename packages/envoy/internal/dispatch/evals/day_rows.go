package evals

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/delivery/measures"
)

// The computed day rows' write (dispatch://INFRA-2916, R12), shared by every family of computed
// evals: one project's rows of one UTC day and the items they counted (ProjectDay), written in one
// transaction (writeProjectDay) for the delivery and CI evals (WriteProjectDay, delivery_days.go)
// and for the Legion evals (WriteLegionProjectDay, legion_days.go), and the statements that write
// the rows themselves (upsertDays, zeroStaleDays, insertMissingDays), which the unowned-p0 write
// (WriteUnownedP0Day) writes its row through too.

// Member is one item a project's day counted, as eval_day_members records it beside the day: its
// family (KindDelivery, KindCI or KindLegion), its row key (PullRequestMember, RunMember, an
// issue's key), its repository and, for a pull request a deploy shipped, the deploy run's row key
// (ShippedBy), which is how the refresher finds the day of the run that shipped it once
// containment moves it to another run.
type Member struct {
	Family    string
	RowKey    string
	Repo      string
	ShippedBy string
}

// ProjectDay is one project's computed rows of one UTC day, as WriteProjectDay and
// WriteLegionProjectDay write them: the day rows (their built_by is the write's, and their
// updated_at its moment where a value moved) and the items they counted; and Gaps, the zero rows
// of the later days the day leaves with no row where it moves an eval's first input day earlier
// (refresh.gapsBehind), which the write inserts where a day holds no row of their key, in its own
// transaction.
type ProjectDay struct {
	Project string
	// Day is the day's UTC midnight.
	Day     time.Time
	Rows    []DayRow
	Members []Member
	Gaps    []DayRow
}

// WithZeros is p with each of zeros (the explicit zero rows of ZeroDeliveryDay) that p holds no row
// of the same eval, series and repository of: the refresher's zero row of a series on a day the
// series has no input, which never stands in for a row the day built.
func (p ProjectDay) WithZeros(zeros []DayRow) ProjectDay {
	held := map[[3]string]bool{}
	for _, row := range p.Rows {
		held[[3]string{row.EvalID, row.Series, row.Repo}] = true
	}
	p.Rows = slices.Clone(p.Rows)
	for _, zero := range zeros {
		if !held[[3]string{zero.EvalID, zero.Series, zero.Repo}] {
			p.Rows = append(p.Rows, zero)
		}
	}
	return p
}

// writeProjectDay is the write of one project's computed rows of one day: day's rows of the evals
// slugs name, and its members of families, written as WriteProjectDay says. The stale rows it
// zeroes are those of slugs alone and the stale members it deletes those of families alone, so
// the delivery and the Legion writes of one day leave each other's rows and members as they are.
// what names the rows in a refusal. Its upsert's change rows wait in the change log's outbox
// (migration 20261115155340), so the transaction takes no change log lock, and they are moved into
// the log once it has committed (drainChanges).
func (s *Store) writeProjectDay(ctx context.Context, what string, day ProjectDay, slugs, families []string, builtBy string) error {
	date := measures.Midnight(day.Day)
	evalIDs := make([]string, len(slugs))
	for i, slug := range slugs {
		evalIDs[i] = EvalID(day.Project, slug)
	}
	for _, row := range day.Rows {
		if row.ProjectKey != day.Project || !row.Day.Equal(date) || !slices.Contains(evalIDs, row.EvalID) {
			return fmt.Errorf("evals: a %s day row of %s on %s is %s of %s on %s", what, day.Project, date.Format(time.DateOnly),
				row.EvalID, row.ProjectKey, row.Day.Format(time.DateOnly))
		}
	}
	for _, row := range day.Gaps {
		if row.ProjectKey != day.Project || !row.Day.After(date) || !slices.Contains(evalIDs, row.EvalID) {
			return fmt.Errorf("evals: a %s gap row of %s after %s is %s of %s on %s", what, day.Project, date.Format(time.DateOnly),
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
	if err := zeroStaleDays(ctx, tx, day.Project, date, evalIDs, day.Rows, builtBy); err != nil {
		return err
	}
	if err := insertMissingDays(ctx, tx, day.Gaps, builtBy); err != nil {
		return err
	}
	count := len(day.Members)
	memberFamilies, keys, memberRepos, shippedBy := make([]string, count), make([]string, count), make([]string, count), make([]string, count)
	for i, member := range day.Members {
		memberFamilies[i], keys[i], memberRepos[i], shippedBy[i] = member.Family, member.RowKey, member.Repo, member.ShippedBy
	}
	// An item this write takes from another day leaves that day's rows counting it until the day
	// is written again: one it moves here from the day it was counted on, and, for a pull request
	// it moves to another deploy run or stops counting, the day of the run that shipped it. Each
	// such day is recorded owed (eval_days_owed, migration 20261113082154) in this transaction,
	// before the members that name it move, so a pass that fails before it rewrites that day still
	// finds it (owedDays) where no stored member names it any more.
	if _, err := tx.Exec(ctx, `
		with given as (
			select * from unnest($3::text[], $4::text[], $5::text[]) as given(family, row_key, shipped_by)
		), held as (
			select stored.family, stored.row_key, stored.day, stored.shipped_by, given.shipped_by as shipped_now
			from given join eval_day_members stored
				on stored.project_key = $1 and stored.family = given.family and stored.row_key = given.row_key
			union all
			select family, row_key, day, shipped_by, null from eval_day_members stored
			where project_key = $1 and family = any($6) and day = $2
			  and (family, row_key) not in (select family, row_key from given)
		), left_days as (
			select family, day from held where day <> $2
			union
			select run.family, run.day from held
			join eval_day_members run on run.project_key = $1 and run.family = $7 and run.row_key = held.shipped_by
			where held.shipped_by <> '' and held.shipped_by is distinct from held.shipped_now
		)
		insert into eval_days_owed (project_key, family, day)
		select $1, family, day from left_days where day <> $2
		on conflict do nothing
	`, day.Project, date, memberFamilies, keys, shippedBy, families, KindDelivery); err != nil {
		return fmt.Errorf("record the days %s's items left for %s: %w", day.Project, date.Format(time.DateOnly), err)
	}
	if _, err := tx.Exec(ctx, `
		insert into eval_day_members (project_key, family, row_key, day, repo, shipped_by)
		select $1, member.family, member.row_key, $2, member.repo, member.shipped_by
		from unnest($3::text[], $4::text[], $5::text[], $6::text[]) as member(family, row_key, repo, shipped_by)
		order by member.family, member.row_key
		on conflict (project_key, family, row_key) do update
		set day = excluded.day, repo = excluded.repo, shipped_by = excluded.shipped_by
		where (eval_day_members.day, eval_day_members.repo, eval_day_members.shipped_by)
			is distinct from (excluded.day, excluded.repo, excluded.shipped_by)
	`, day.Project, date, memberFamilies, keys, memberRepos, shippedBy); err != nil {
		return fmt.Errorf("record the items %s counted on %s: %w", day.Project, date.Format(time.DateOnly), err)
	}
	if _, err := tx.Exec(ctx, `
		delete from eval_day_members
		where project_key = $1 and family = any($2) and day = $3
		  and (family, row_key) not in (select * from unnest($4::text[], $5::text[]))
	`, day.Project, families, date, memberFamilies, keys); err != nil {
		return fmt.Errorf("delete the items %s no longer counts on %s: %w", day.Project, date.Format(time.DateOnly), err)
	}
	// This write is the rewrite a day owed of its families.
	if _, err := tx.Exec(ctx, `delete from eval_days_owed where project_key = $1 and family = any($2) and day = $3`, day.Project, families, date); err != nil {
		return fmt.Errorf("clear what %s owed %s: %w", day.Project, date.Format(time.DateOnly), err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit the day rows of %s on %s: %w", day.Project, date.Format(time.DateOnly), err)
	}
	s.drainChanges(ctx)
	return nil
}

// upsertDays writes rows in one statement, in key order, each the computation's (writer
// `computation`) with built_by builtBy: a new row is inserted, and a stored one rewritten only
// where a value differs (built_by included, so a new build's rebuild marks every row it rebuilt).
// updated_at moves only with a value: the trigger of eval_days (migration 20261113082202) logs no
// change for a rewrite that moves built_by and updated_at alone, and the evals list answers the
// oldest updated_at of the rows it folds (api/evals.go's freshness), so a rebuild that moved it
// would leave a live evals view answering the old one while the read answered the new.
func upsertDays(ctx context.Context, tx pgx.Tx, rows []DayRow, builtBy string) error {
	return writeDayRows(ctx, tx, rows, builtBy, `
		on conflict (project_key, eval_id, writer, series, repo, day) do update
		set listed = excluded.listed, n = excluded.n, sum = excluded.sum, num = excluded.num, den = excluded.den,
			max = excluded.max, samples = excluded.samples, last_value = excluded.last_value, last_at = excluded.last_at,
			counts = excluded.counts, via_repository = excluded.via_repository, built_by = excluded.built_by,
			updated_at = case
				when (eval_days.listed, eval_days.n, eval_days.sum, eval_days.num, eval_days.den, eval_days.max,
						eval_days.samples, eval_days.last_value, eval_days.last_at, eval_days.counts, eval_days.via_repository)
					is distinct from (excluded.listed, excluded.n, excluded.sum, excluded.num, excluded.den, excluded.max,
						excluded.samples, excluded.last_value, excluded.last_at, excluded.counts, excluded.via_repository)
				then now() else eval_days.updated_at end
		where (eval_days.listed, eval_days.n, eval_days.sum, eval_days.num, eval_days.den, eval_days.max,
				eval_days.samples, eval_days.last_value, eval_days.last_at, eval_days.counts,
				eval_days.via_repository, eval_days.built_by)
			is distinct from (excluded.listed, excluded.n, excluded.sum, excluded.num, excluded.den, excluded.max,
				excluded.samples, excluded.last_value, excluded.last_at, excluded.counts,
				excluded.via_repository, excluded.built_by)`)
}

// zeroStaleDays writes each of the computation's stored rows of project's day of the evals evalIDs
// names that held is not among as a zero row, in one statement, recording builtBy: n 0, a zero
// sum, numerator and denominator where the row holds one, an empty sample list where it holds
// one, no maximum and no last value, each named count 0 and via_repository 0, the zero row the day
// builders build for a day with no input. Such a row had input once (no write creates a row of a
// series and repository with none), so its series has a first input day and 0 is that day's
// value; deleting it would leave the day with no row, which reads as no data, and would log no
// eval_days change, so a view of the row's numbers would never wake to the change. A row already
// zero, built_by included, is left as it is and logs nothing; a row already zero under another
// build takes builtBy and keeps its updated_at, as upsertDays keeps it where no value moved.
// The readings write's rows of those evals (an eval a register write made logged for a while)
// are its own, and stay as they are.
func zeroStaleDays(ctx context.Context, tx pgx.Tx, project string, day time.Time, evalIDs []string, held []DayRow, builtBy string) error {
	evals, series, repos := make([]string, len(held)), make([]string, len(held)), make([]string, len(held))
	for i, row := range held {
		evals[i], series[i], repos[i] = row.EvalID, row.Series, row.Repo
	}
	if _, err := tx.Exec(ctx, `
		with stale as (
			select project_key, eval_id, writer, series, repo, day,
				case when sum is not null then 0 end as sum, case when num is not null then 0 end as num,
				case when den is not null then 0 end as den,
				case when samples is not null then '{}'::double precision[] end as samples,
				coalesce((select jsonb_object_agg(key, 0) from jsonb_object_keys(counts) as key), '{}'::jsonb) as counts
			from eval_days
			where project_key = $1 and day = $2 and eval_id = any($3) and writer = $8
				and (eval_id, series, repo) not in (select * from unnest($4::text[], $5::text[], $6::text[]))
		)
		update eval_days
		set n = 0, sum = stale.sum, num = stale.num, den = stale.den, max = null, samples = stale.samples,
			last_value = null, last_at = null, counts = stale.counts, via_repository = 0, built_by = $7,
			updated_at = case
				when (eval_days.n, eval_days.sum, eval_days.num, eval_days.den, eval_days.max, eval_days.samples,
						eval_days.last_value, eval_days.last_at, eval_days.counts, eval_days.via_repository)
					is distinct from (0, stale.sum, stale.num, stale.den, null::double precision, stale.samples,
						null::double precision, null::timestamptz, stale.counts, 0)
				then now() else eval_days.updated_at end
		from stale
		where eval_days.project_key = stale.project_key and eval_days.eval_id = stale.eval_id and eval_days.writer = stale.writer
			and eval_days.series = stale.series and eval_days.repo = stale.repo and eval_days.day = stale.day
			and (eval_days.n, eval_days.sum, eval_days.num, eval_days.den, eval_days.max, eval_days.samples,
				eval_days.last_value, eval_days.last_at, eval_days.counts, eval_days.via_repository, eval_days.built_by)
			is distinct from (0, stale.sum, stale.num, stale.den, null::double precision, stale.samples,
				null::double precision, null::timestamptz, stale.counts, 0, $7::text)
	`, project, day, evalIDs, evals, series, repos, builtBy, writerComputation); err != nil {
		return fmt.Errorf("write the day rows %s no longer holds on %s as zero rows: %w", project, day.Format(time.DateOnly), err)
	}
	return nil
}

// insertMissingDays writes each of rows that the table holds no row of the same key for, in one
// statement, each the computation's, and leaves every stored row as it is: the refresher's zero
// rows of the days a series had no row on, which never stand in for a row a write built.
func insertMissingDays(ctx context.Context, tx pgx.Tx, rows []DayRow, builtBy string) error {
	return writeDayRows(ctx, tx, rows, builtBy, `on conflict (project_key, eval_id, writer, series, repo, day) do nothing`)
}

// writeDayRows inserts rows in one statement, in key order, each the computation's (writer
// `computation`) with built_by builtBy, and meets a row already stored as conflict says.
func writeDayRows(ctx context.Context, tx pgx.Tx, rows []DayRow, builtBy, conflict string) error {
	if len(rows) == 0 {
		return nil
	}
	rows = slices.SortedFunc(slices.Values(rows), func(a, b DayRow) int {
		return cmp.Or(cmp.Compare(a.ProjectKey, b.ProjectKey), cmp.Compare(a.EvalID, b.EvalID), cmp.Compare(a.Series, b.Series),
			cmp.Compare(a.Repo, b.Repo), a.Day.Compare(b.Day))
	})
	count := len(rows)
	projects, evals, series, repos := make([]string, count), make([]string, count), make([]string, count), make([]string, count)
	days, lastAts := make([]time.Time, count), make([]*time.Time, count)
	listed := make([]bool, count)
	ns, via := make([]int32, count), make([]int32, count)
	sums, nums, dens, maxes, lastValues := make([]*float64, count), make([]*float64, count), make([]*float64, count), make([]*float64, count), make([]*float64, count)
	samples, counts := make([]*string, count), make([]string, count)
	for i, row := range rows {
		projects[i], evals[i], series[i], repos[i], days[i], listed[i] = row.ProjectKey, row.EvalID, row.Series, row.Repo, measures.Midnight(row.Day), row.Listed
		ns[i], via[i] = int32(row.N), int32(row.ViaRepository)
		sums[i], nums[i], dens[i], maxes[i], lastValues[i], lastAts[i] = row.Sum, row.Num, row.Den, row.Max, row.LastValue, row.LastAt
		samples[i] = floatArray(row.Samples)
		encoded, err := json.Marshal(NonNilCounts(row.Counts))
		if err != nil {
			return fmt.Errorf("encode the counts of a day row of %s: %w", row.EvalID, err)
		}
		counts[i] = string(encoded)
	}
	if _, err := tx.Exec(ctx, `
		insert into eval_days (project_key, eval_id, writer, series, repo, day, listed, n, sum, num, den, max, samples,
			last_value, last_at, counts, via_repository, built_by)
		select project_key, eval_id, $18, series, repo, day, listed, n, sum, num, den, max, samples::double precision[],
			last_value, last_at, counts::jsonb, via_repository, $17
		from unnest($1::text[], $2::text[], $3::text[], $4::text[], $5::date[], $6::bool[], $7::int[],
			$8::double precision[], $9::double precision[], $10::double precision[], $11::double precision[], $12::text[],
			$13::double precision[], $14::timestamptz[], $15::text[], $16::int[])
			as given(project_key, eval_id, series, repo, day, listed, n, sum, num, den, max, samples,
				last_value, last_at, counts, via_repository)
		`+conflict, projects, evals, series, repos, days, listed, ns, sums, nums, dens, maxes, samples, lastValues, lastAts, counts, via, builtBy, writerComputation); err != nil {
		return fmt.Errorf("write the day rows of %s: %w", rows[0].ProjectKey, err)
	}
	return nil
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
