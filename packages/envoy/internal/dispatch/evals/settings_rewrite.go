package evals

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/sync/errgroup"

	"github.com/sjawhar/envoy/internal/dispatch/delivery"
	"github.com/sjawhar/envoy/internal/dispatch/delivery/measures"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// settings_rewrite.go is the refresher's answer to a change of the delivery settings
// (dispatch://INFRA-2916, R14): one pass rewrites, in one transaction, every day of the delivery
// and CI evals the change moves, and begins no walk. The settings the stored delivery and CI rows
// stand built under are recorded beside them (eval_days_settings, migration 20261117100021), and a
// page compares them with the settings it reads before it writes a day (syncSettings): field by
// field, since a settings change in the log names no field, and since rows can stand built under
// other settings than the change before the page's cursor implies (a page whose pass read the
// settings after a later change committed, or the release before this one during a roll).
//
// A change that moves no eval writes nothing and begins no walk. One of a setting no day row reads
// (a workflow path, the population rule's authors and excluded repositories, the roll-up job), or
// one that leaves the production rule and every deploy card following the same names with the
// same step (the order of the production names, a staging job renamed between two names the
// production rule follows), moves no eval by settingsMoves, and the pass reads nothing. A
// production job name no run holds moves the rule's names but its verdict on no run, so the
// rewrite builds every day of the evals the rule moves and writes none of them: each of its
// writes sends a row, a member or a figure only where its value moved.

// settingsMoves is the delivery and CI evals (by slug) whose day rows a change of the delivery
// settings from prev to next can move, and the families of eval_day_members it can move, read from
// the code that builds the rows (readDeliveryDay, DeliveryDay.Rows and the measures they call):
//
//   - deploy_repo: every read of a day names it (the deploys, the apply runs, the CI runs), and
//     containment ships its pull requests alone, so every eval but the two of the pull requests
//     alone (pr-open-to-merge and rework-share, which read a pull request's opening, merge and
//     rework and no deploy), and both families.
//   - the production rule (production_job_names, production_apply_step_name): it decides which run
//     is a deploy (ProductionApplies), so containment (the three lead times to production, and the
//     delivery family, whose shipped_by is containment's and whose deploy runs count for the
//     projects of the pull requests they ship), deploy-frequency and deploy-run-success. The rule
//     reads its names as a set (Production.Job), so their order alone moves nothing.
//   - each deploy card (production-deploy, staging-e2e and staging-deploy, cardParts): the job
//     names it counts a run by and its apply step, and whether the settings make it at all. So the
//     production rule moves production-deploy, a staging job name its card, and a production job
//     name a staging card follows that card too (a name two cards follow counts for the first).
//     The push runs the cards read are every push run on main whatever its jobs, so a card moves
//     no member.
//   - deploy_workflow_path, pr_checks_workflow_path, population_authors, excluded_repos and
//     pr_checks_rollup_job_name: none. The first four decide what the intake and the reconcile
//     store, each store a change of its own, and no read of a day names them; the roll-up job is
//     read at read time (the Pipeline's failing jobs, pr-ci's items) and by no day row.
func settingsMoves(prev, next delivery.DeliverySettings) (slugs, families []string) {
	if prev.DeployRepo != next.DeployRepo {
		return settingsMoveAll()
	}
	moved, family := map[string]bool{}, map[string]bool{}
	if partOf(prev.Production()) != partOf(next.Production()) {
		for _, slug := range []string{SlugMergeToProduction, SlugFirstCommitToProduction, SlugOpenToProduction, SlugDeployFrequency, SlugDeployRunSuccess} {
			moved[slug] = true
		}
		family[KindDelivery] = true
	}
	before, after := cardParts(prev), cardParts(next)
	for card, slug := range cardSlugs {
		was, held := before[card]
		now, holds := after[card]
		if held != holds || was != now {
			moved[slug] = true
		}
	}
	return slices.Sorted(maps.Keys(moved)), slices.Sorted(maps.Keys(family))
}

// settingsMoveAll is every delivery and CI eval and member family a change of the delivery
// settings can move: what a new deploy repository moves, every eval but pr-open-to-merge and
// rework-share, which read a pull request alone, and both families.
func settingsMoveAll() (slugs, families []string) {
	for _, slug := range deliveryDaySlugs {
		if slug != SlugPROpenToMerge && slug != SlugReworkShare {
			slugs = append(slugs, slug)
		}
	}
	slices.Sort(slugs)
	return slugs, []string{KindCI, KindDelivery}
}

// rulePart is what a production rule, or a deploy card, counts a run by: the job names it
// follows, sorted and each once, joined by a NUL (no job name holds one), and its apply step.
type rulePart struct {
	names   string
	step    string
	stepped bool
}

// partOf is production's rulePart over names, every name of it where names is nil.
func partOf(production delivery.Production, names ...string) rulePart {
	if names == nil {
		names = production.JobNames
	}
	part := rulePart{names: strings.Join(slices.Compact(slices.Sorted(slices.Values(names))), "\x00")}
	if production.ApplyStep != nil {
		part.step, part.stepped = *production.ApplyStep, true
	}
	return part
}

// cardParts is each deploy card settings make (measures.DeployCards) by its id, with what it counts
// a run by: the names it follows that no card before it follows, which measures.DeployJobsDay
// gives it, and its apply step.
func cardParts(settings delivery.DeliverySettings) map[measures.DeployJobCardID]rulePart {
	parts := map[measures.DeployJobCardID]rulePart{}
	taken := map[string]bool{}
	for _, card := range measures.DeployCards(settings) {
		names := []string{}
		for _, name := range card.JobNames {
			if !taken[name] {
				names = append(names, name)
			}
		}
		for _, name := range card.JobNames {
			taken[name] = true
		}
		parts[card.CardID] = partOf(card.Production, names...)
	}
	return parts
}

// sameBuiltSettings reports whether a and b agree on every delivery setting a day row reads, the
// fields eval_days_settings records, each exactly as stored.
func sameBuiltSettings(a, b delivery.DeliverySettings) bool {
	return a.DeployRepo == b.DeployRepo && slices.Equal(a.ProductionJobNames, b.ProductionJobNames) &&
		sameName(a.ProductionApplyStepName, b.ProductionApplyStepName) && sameName(a.StagingE2EJobName, b.StagingE2EJobName) &&
		sameName(a.StagingApplyJobName, b.StagingApplyJobName)
}

// sameName reports whether two optional names are one.
func sameName(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// readBuiltUnder reads the delivery settings the stored delivery and CI rows stand built under
// (eval_days_settings), the fields a day row reads alone, and when they were recorded; nil where
// none are recorded.
func (s *Store) readBuiltUnder(ctx context.Context) (*delivery.DeliverySettings, time.Time, error) {
	var built delivery.DeliverySettings
	var at time.Time
	err := s.database.Pool.QueryRow(ctx, `
		select deploy_repo, production_job_names, production_apply_step_name, staging_e2e_job_name, staging_apply_job_name, updated_at
		from eval_days_settings where singleton
	`).Scan(&built.DeployRepo, &built.ProductionJobNames, &built.ProductionApplyStepName, &built.StagingE2EJobName, &built.StagingApplyJobName, &at)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, time.Time{}, nil
	case err != nil:
		return nil, time.Time{}, fmt.Errorf("read the delivery settings the day rows stand built under: %w", err)
	}
	return &built, at, nil
}

// recordBuiltUnder records, in q, settings as the delivery settings the stored delivery and CI rows
// stand built under (eval_days_settings), and answers the moment it records, q's transaction's.
func recordBuiltUnder(ctx context.Context, q pgx.Tx, settings delivery.DeliverySettings) (time.Time, error) {
	var at time.Time
	if err := q.QueryRow(ctx, `
		insert into eval_days_settings (deploy_repo, production_job_names, production_apply_step_name, staging_e2e_job_name, staging_apply_job_name)
		values ($1, $2, $3, $4, $5)
		on conflict (singleton) do update
		set deploy_repo = excluded.deploy_repo, production_job_names = excluded.production_job_names,
			production_apply_step_name = excluded.production_apply_step_name,
			staging_e2e_job_name = excluded.staging_e2e_job_name, staging_apply_job_name = excluded.staging_apply_job_name,
			updated_at = now()
		returning updated_at
	`, settings.DeployRepo, nonNil(settings.ProductionJobNames), settings.ProductionApplyStepName,
		settings.StagingE2EJobName, settings.StagingApplyJobName).Scan(&at); err != nil {
		return time.Time{}, fmt.Errorf("record the delivery settings the day rows stand built under: %w", err)
	}
	return at, nil
}

// writtenByAnotherBuild reports whether a build other than build moved a value of a row of
// projects that a delivery setting can move (settingsMoveAll) at since or after it: the row's
// built_by, which every write records, and its updated_at, which moves only with a value.
func (s *Store) writtenByAnotherBuild(ctx context.Context, projects []string, build string, since time.Time) (bool, error) {
	slugs, _ := settingsMoveAll()
	evalIDs := make([]string, 0, len(projects)*len(slugs))
	for _, project := range projects {
		for _, slug := range slugs {
			evalIDs = append(evalIDs, EvalID(project, slug))
		}
	}
	var written bool
	if err := s.database.Pool.QueryRow(ctx, `
		select exists (
			select 1 from eval_days
			where project_key = any($1) and eval_id = any($2) and writer = $3 and built_by <> $4 and updated_at >= $5)
	`, projects, evalIDs, writerComputation, build, since).Scan(&written); err != nil {
		return false, fmt.Errorf("read whether another build wrote the delivery rows since %s: %w", since.Format(time.RFC3339), err)
	}
	return written, nil
}

// syncSettings brings the stored delivery and CI rows to the delivery settings the pass reads
// (r.settings) before the page writes a day under them, on every page, so no row is written under
// settings the rows do not stand built under:
//
//   - where the recorded settings (eval_days_settings, read once a pass) agree with them on every
//     field a day row reads, and the page holds no change of the delivery_settings row, nothing;
//   - where they differ, it rewrites every day of the evals the difference moves (settingsMoves,
//     rewriteSettings), which records the settings in its own transaction, or, where the
//     difference moves none, has the page record them with its cursors (w.built);
//   - where they differ, or the page holds a change of the row, and another build moved a value of
//     a row a setting can move since the settings were recorded (writtenByAnotherBuild), it
//     rewrites every eval a setting can move (settingsMoveAll), as a new deploy repository does;
//   - where none are recorded (a database no pass of this release has brought to its settings, or
//     one a bench reset), the rows' settings are unknown, so it begins the delivery walk again,
//     which builds every day under the settings it reads, and has the page record them with the
//     walk it begins, in one transaction.
//
// The third is the roll's. The release before this one writes rows under the settings its own
// pass read, recording none, and begins every build's delivery walk again on each settings change
// its pages read; but a change can commit after that pass's drain and before its read of the
// settings, so the pass writes under settings S1 a change it never reads names, and a page of this
// release can read that change and a later one together whose settings S2 agree with the recorded
// S0. Comparing S0 with S2 finds nothing, and the rows written under S1 would stand; the rows
// themselves show another build wrote, so every eval a setting can move is rewritten. That costs a
// whole rewrite for a settings change in the roll, and for the first after it while rows the
// release before wrote stand, and nothing in steady state, where every row is this build's or older
// than the record.
func (r *refresh) syncSettings(ctx context.Context, w *pageWork) error {
	if r.settings == nil {
		return nil
	}
	if !r.builtRead {
		built, at, err := r.store.readBuiltUnder(ctx)
		if err != nil {
			return err
		}
		r.builtUnder, r.builtAt, r.builtRead = built, at, true
	}
	next := *r.settings
	if r.builtUnder == nil {
		w.restart[walkDelivery], w.built = true, &next
		r.builtUnder = &next
		return nil
	}
	same := sameBuiltSettings(*r.builtUnder, next)
	if same && !w.settingsRow {
		return nil
	}
	slugs, families := settingsMoves(*r.builtUnder, next)
	foreign, err := r.store.writtenByAnotherBuild(ctx, r.projects, r.build, r.builtAt)
	if err != nil {
		return err
	}
	if foreign {
		slugs, families = settingsMoveAll()
	}
	switch {
	case len(slugs) > 0:
		if err := r.rewriteSettings(ctx, slugs, families); err != nil {
			return err
		}
	case !same:
		w.built = &next
	}
	r.builtUnder = &next
	return nil
}

// rewriteWorkers is the most days rewriteSettings builds at once: the CPUs the process may use, up
// to this many.
const rewriteWorkers = 8

// rewriteChunk is the most day rows one statement of rewriteSettings' upsert sends: its arrays are
// its parameters, and a deploy repository's rewrite holds every row of most evals.
const rewriteChunk = 20000

// memberKey is a member's identity in eval_day_members, and memberAt where it is counted.
type memberKey struct {
	project, family, rowKey string
}

type memberAt struct {
	day             time.Time
	repo, shippedBy string
}

// readMembers reads every member of projects under families, by its key.
func (s *Store) readMembers(ctx context.Context, projects, families []string) (map[memberKey]memberAt, error) {
	held := map[memberKey]memberAt{}
	if len(families) == 0 || len(projects) == 0 {
		return held, nil
	}
	rows, err := s.database.Pool.Query(ctx, `
		select project_key, family, row_key, day, repo, shipped_by from eval_day_members
		where project_key = any($1) and family = any($2)
	`, projects, families)
	if err != nil {
		return nil, fmt.Errorf("read the members of %v: %w", families, err)
	}
	var key memberKey
	var at memberAt
	if _, err := pgx.ForEachRow(rows, []any{&key.project, &key.family, &key.rowKey, &at.day, &at.repo, &at.shippedBy}, func() error {
		at.day = measures.Midnight(at.day)
		held[key] = at
		return nil
	}); err != nil {
		return nil, fmt.Errorf("read the members of %v: %w", families, err)
	}
	return held, nil
}

// rewriteSettings rewrites, under the settings the pass reads (r.settings), every day of the evals
// slugs names and the members of families, from the delivery walk's first day through today, as
// the whole delivery walk under those settings writes them, in one pass that begins no walk:
//
//   - one read of the history, its parts side by side: every population pull request, and every
//     deploy run on main of the deploy repository with its jobs of the names the production rule
//     judges by and the cards follow (delivery.ReadHistoryRows), the walk's first input day
//     (deliveryInputStart), each project's first days (firstDays), repo_projects and the stored
//     members of families; then the projects of the issues the pull requests name;
//   - containment once over the history: the production rule's verdict on each run
//     (ProductionApplies, which judges a run by its own jobs alone, and by its jobs of the judged
//     names as by every job), each pull request's first shipping apply (delivery.ShippingIndex),
//     and each deploy's shipped set, the pull requests whose first shipping apply it is
//     (Deploys.ShippedBy's rule over every day at once);
//   - each day's DeliveryDay as readDeliveryDay builds it from those rows, its PR-checks runs read
//     by day where pr-ci moves (a new deploy repository, delivery.ReadCIRows, the walk's read), and
//     each project's rows of it (DeliveryDay.Rows), the days built side by side;
//   - in day order, each project's day with the zero rows of its series from their first input
//     days on, as the walk writes it (writeDelivery, zerosFrom and noteBuilt), and the days the
//     walk writes, those holding a row;
//   - one transaction: the rows of the evals slugs names upserted where a value moved
//     (upsertDays); those evals' stored rows of the days the walk writes that the days no longer
//     hold made zero rows (zeroStaleDays' rule, over every day at once); the members of families
//     whose day, repository or shipping run moved, or that are new, upserted, and those of the
//     days written that the days no longer hold deleted, and no other member sent; the stored
//     figures of every day whose rows moved folded again (foldFiguresInTx, whose quantiles read
//     each rank across the days' sorted samples, measures.SortedSamples); and the settings recorded
//     as the ones the rows stand built under. Its eval_days changes are one per eval, as every
//     transaction's are (migration 20261117100014), so an evals view hears of the whole change
//     once.
//
// It leaves eval_days_owed as it is: it writes the moved evals of a day alone, and a day owed is
// owed the rewrite of every eval of its families (writeOwed). It records each day it wrote in the
// pass's report, as the walk does, and the first days it moved (noteBuilt), but no day as done
// (refresh.done): a change of the same page that names a day writes that day whole after it.
func (r *refresh) rewriteSettings(ctx context.Context, slugs, families []string) error {
	next := *r.settings
	pool := r.store.database.Pool
	end := r.today.AddDate(0, 0, 1)
	cards := measures.DeployCards(next)
	cardNames := measures.DeployCardJobNames(cards)
	production := next.Production()
	checks := slices.Contains(slugs, SlugPRCI)

	var history delivery.HistoryRows
	var input *time.Time
	var repos repoProjects
	var stored map[memberKey]memberAt
	firsts := make(map[string]map[string]time.Time, len(r.projects))
	if err := store.ReadSideBySide(ctx,
		func(ctx context.Context) (err error) {
			history, err = delivery.ReadHistoryRows(ctx, pool, next.DeployRepo, slices.Concat(production.JudgedJobNames(), cardNames), end)
			return err
		},
		func(ctx context.Context) (err error) {
			input, err = r.store.deliveryInputStart(ctx, next.DeployRepo)
			return err
		},
		func(ctx context.Context) error {
			for _, project := range r.projects {
				first, err := r.store.firstDays(ctx, project, ZeroDeliveryDay(next, project, r.today).Rows)
				if err != nil {
					return err
				}
				firsts[project] = first
			}
			return nil
		},
		func(ctx context.Context) (err error) {
			repos, err = readRepoProjects(ctx, r.store)
			return err
		},
		func(ctx context.Context) (err error) {
			stored, err = r.store.readMembers(ctx, r.projects, families)
			return err
		},
	); err != nil {
		return fmt.Errorf("read the delivery history for a settings change: %w", err)
	}
	every := map[string]time.Time{}
	for project, first := range firsts {
		r.first[walkKey{family: walkDelivery, project: project}] = first
		// An eval id names its project, so no two projects' entries share a key.
		maps.Copy(every, first)
	}
	var days []time.Time
	if start := earliestOf(input, every); start != nil {
		for day := *start; !day.After(r.today); day = day.AddDate(0, 0, 1) {
			days = append(days, day)
		}
	}
	prs, runs, jobs := history.PullRequests, history.Deploys.Runs, history.Deploys.Jobs
	var issueKeys []string
	for _, pr := range prs {
		if pr.IssueKey != nil {
			issueKeys = append(issueKeys, *pr.IssueKey)
		}
	}
	issueProjects, err := readIssueProjects(ctx, r.store, issueKeys)
	if err != nil {
		return err
	}
	attribute := attribution{issues: issueProjects, repos: repos}

	// Containment once over the history.
	applies := delivery.ProductionApplies(runs, jobs, production).Applies
	reached := make(map[int64]bool, len(applies))
	for _, apply := range applies {
		reached[apply.RunID] = true
	}
	shipping := delivery.NewShippingIndex(applies)
	runByID := make(map[int64]delivery.DeliveryRun, len(runs))
	for _, run := range runs {
		runByID[run.RunID] = run
	}
	containing := make([]*delivery.Apply, len(prs))
	shipped := map[int64][]*delivery.DeliveryPullRequest{}
	prsByDay := map[time.Time][]int{}
	for i := range prs {
		if containing[i] = shipping.Containing(prs[i], next.DeployRepo); containing[i] != nil {
			shipped[containing[i].RunID] = append(shipped[containing[i].RunID], &prs[i])
		}
		if prs[i].MergedAt != nil {
			day := measures.Midnight(*prs[i].MergedAt)
			prsByDay[day] = append(prsByDay[day], i)
		}
	}
	deploysByDay, pushesByDay := map[time.Time][]delivery.DeliveryRun{}, map[time.Time][]delivery.DeliveryRun{}
	for _, run := range runs {
		day := measures.Midnight(run.StartedAt)
		deploysByDay[day] = append(deploysByDay[day], run)
		if run.Event != nil && *run.Event == "push" {
			pushesByDay[day] = append(pushesByDay[day], run)
		}
	}

	// Each day built side by side: its DeliveryDay and each project's rows of it.
	built := make([][]ProjectDay, len(days))
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(min(max(runtime.GOMAXPROCS(0), 1), rewriteWorkers))
	for index, day := range days {
		group.Go(func() error {
			read := DeliveryDay{Day: day, cards: cards}
			for _, i := range prsByDay[day] {
				// A partial row, whose opening is not known yet, as readDeliveryDay leaves it out.
				if prs[i].CreatedAt != nil {
					read.prs = append(read.prs, attribute.pullRequest(prs[i], containing[i], runByID))
				}
			}
			for _, run := range deploysByDay[day] {
				read.deploys = append(read.deploys, attribute.deploy(run, reached[run.RunID], shipped[run.RunID]))
			}
			read.pushes, read.pushJobs = attribute.ciRuns(pushesByDay[day]), jobs
			if checks {
				ci, err := delivery.ReadCIRows(store.ForConcurrentWorker(groupCtx), pool, next.DeployRepo, day, day.AddDate(0, 0, 1), cardNames)
				if err != nil {
					return fmt.Errorf("read the CI runs of %s: %w", day.Format(time.DateOnly), err)
				}
				read.checks, read.checkJobs = attribute.ciRuns(ci.Checks.Runs), ci.Checks.Jobs
			}
			rows := make([]ProjectDay, len(r.projects))
			for p, project := range r.projects {
				rows[p] = read.Rows(project)
			}
			built[index] = rows
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return err
	}

	// The days the walk writes, in day order, as it moves each eval's first input day.
	ids, rewritten := map[string]bool{}, map[string]bool{}
	for _, project := range r.projects {
		for _, slug := range slugs {
			ids[EvalID(project, slug)] = true
		}
	}
	var rows []DayRow
	members := map[memberKey]memberAt{}
	var pairProjects []string
	var pairDays []time.Time
	written := map[projectDay]bool{}
	for index, day := range days {
		for p, project := range r.projects {
			own := built[index][p]
			first := r.first[walkKey{family: walkDelivery, project: project}]
			full := own.WithZeros(zerosFrom(ZeroDeliveryDay(next, project, day).Rows, first, day))
			if len(full.Rows) == 0 {
				continue
			}
			pairProjects, pairDays = append(pairProjects, project), append(pairDays, day)
			written[projectDay{project: project, day: day}], rewritten[project] = true, true
			for _, row := range full.Rows {
				if ids[row.EvalID] {
					rows = append(rows, row)
				}
			}
			for _, member := range full.Members {
				if slices.Contains(families, member.Family) {
					members[memberKey{project: project, family: member.Family, rowKey: member.RowKey}] = memberAt{day: day, repo: member.Repo, shippedBy: member.ShippedBy}
				}
			}
			r.noteBuilt(walkDelivery, project, day, own.Rows)
			r.report.days[builtDay{family: walkDelivery, project: project, day: day}] = true
		}
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin the rewrite of a settings change: %w", err)
	}
	defer tx.Rollback(ctx)
	// Each statement planned for the arrays it is sent: a connection's cached statement can turn
	// generic after its fifth run, and the zero rows' statement took 10 ms in its first runs and
	// 450 ms in later ones of one process (k8's phase D, at 2 % of the stated rate).
	if _, err := tx.Exec(ctx, `set local plan_cache_mode = force_custom_plan`); err != nil {
		return fmt.Errorf("plan the rewrite of a settings change: %w", err)
	}
	for at := 0; at < len(rows); at += rewriteChunk {
		if err := upsertDays(ctx, tx, rows[at:min(at+rewriteChunk, len(rows))], r.build); err != nil {
			return err
		}
	}
	if err := zeroStaleSettingsDays(ctx, tx, pairProjects, pairDays, slices.Sorted(maps.Keys(ids)), rows, r.build); err != nil {
		return err
	}
	if err := writeMovedMembers(ctx, tx, stored, members, written); err != nil {
		return err
	}
	for _, project := range slices.Sorted(maps.Keys(rewritten)) {
		spans := make(map[string]daySpan, len(slugs))
		for _, slug := range slugs {
			spans[EvalID(project, slug)] = daySpan{first: days[0], last: r.today}
		}
		if err := foldFiguresInTx(ctx, tx, r.build, project, spans); err != nil {
			return err
		}
	}
	recorded, err := recordBuiltUnder(ctx, tx, next)
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit the rewrite of a settings change: %w", err)
	}
	r.builtAt = recorded
	r.store.drainChanges(ctx)
	return nil
}

// zeroStaleSettingsDays is zeroStaleDays over many days at once: each of the computation's stored
// rows of the evals evalIDs names, on each day of a project the rewrite writes (projects and days,
// pairwise), that held is not among, written as a zero row recording builtBy, and one already zero
// left as it is, as zeroStaleDays writes them.
func zeroStaleSettingsDays(ctx context.Context, tx pgx.Tx, projects []string, days []time.Time, evalIDs []string, held []DayRow, builtBy string) error {
	if len(projects) == 0 {
		return nil
	}
	count := len(held)
	heldProjects, heldEvals, heldSeries, heldRepos := make([]string, count), make([]string, count), make([]string, count), make([]string, count)
	heldDays := make([]time.Time, count)
	for i, row := range held {
		heldProjects[i], heldEvals[i], heldSeries[i], heldRepos[i], heldDays[i] = row.ProjectKey, row.EvalID, row.Series, row.Repo, measures.Midnight(row.Day)
	}
	if _, err := tx.Exec(ctx, `
		with written as materialized (
			select * from unnest($1::text[], $2::date[]) as written(project_key, day)
		), held as materialized (
			select * from unnest($3::text[], $4::text[], $5::text[], $6::text[], $7::date[]) as held(project_key, eval_id, series, repo, day)
		), stale as (
			select e.project_key, e.eval_id, e.writer, e.series, e.repo, e.day,
				case when e.sum is not null then 0 end as sum, case when e.num is not null then 0 end as num,
				case when e.den is not null then 0 end as den,
				case when e.samples is not null then '{}'::double precision[] end as samples,
				coalesce((select jsonb_object_agg(key, 0) from jsonb_object_keys(e.counts) as key), '{}'::jsonb) as counts
			from eval_days e
			join written on written.project_key = e.project_key and written.day = e.day
			where e.eval_id = any($8) and e.writer = $10
				and not exists (select 1 from held where held.project_key = e.project_key and held.eval_id = e.eval_id
					and held.series = e.series and held.repo = e.repo and held.day = e.day)
		)
		update eval_days
		set n = 0, sum = stale.sum, num = stale.num, den = stale.den, max = null, samples = stale.samples,
			last_value = null, last_at = null, last_recorded_at = null, last_reading = null, counts = stale.counts,
			via_repository = 0, built_by = $9,
			updated_at = case
				when (eval_days.n, eval_days.sum, eval_days.num, eval_days.den, eval_days.max, eval_days.samples,
						eval_days.last_value, eval_days.last_at, eval_days.last_recorded_at, eval_days.last_reading,
						eval_days.counts, eval_days.via_repository)
					is distinct from (0, stale.sum, stale.num, stale.den, null::double precision, stale.samples,
						null::double precision, null::timestamptz, null::timestamptz, null::uuid, stale.counts, 0)
				then now() else eval_days.updated_at end
		from stale
		where eval_days.project_key = stale.project_key and eval_days.eval_id = stale.eval_id and eval_days.writer = stale.writer
			and eval_days.series = stale.series and eval_days.repo = stale.repo and eval_days.day = stale.day
			and (eval_days.n, eval_days.sum, eval_days.num, eval_days.den, eval_days.max, eval_days.samples,
				eval_days.last_value, eval_days.last_at, eval_days.last_recorded_at, eval_days.last_reading,
				eval_days.counts, eval_days.via_repository, eval_days.built_by)
			is distinct from (0, stale.sum, stale.num, stale.den, null::double precision, stale.samples,
				null::double precision, null::timestamptz, null::timestamptz, null::uuid, stale.counts, 0, $9::text)
	`, projects, days, heldProjects, heldEvals, heldSeries, heldRepos, heldDays, evalIDs, builtBy, writerComputation); err != nil {
		return fmt.Errorf("write the day rows a settings change leaves no longer held as zero rows: %w", err)
	}
	return nil
}

// writeMovedMembers writes, in tx, the members built that moved from where stored holds them: each
// that stored holds on another day, under another repository or shipping run, or not at all,
// upserted; and each stored on a day written (by project) that built does not hold, deleted, on
// that day alone. A member whose row stands is sent to neither statement: a production rule's
// change moves the shipping run of some pull requests and the projects of some deploys, and leaves
// most of a history's members where they are.
func writeMovedMembers(ctx context.Context, tx pgx.Tx, stored, built map[memberKey]memberAt, written map[projectDay]bool) error {
	var moved, gone []memberKey
	for key, at := range built {
		if held, found := stored[key]; !found || !held.day.Equal(at.day) || held.repo != at.repo || held.shippedBy != at.shippedBy {
			moved = append(moved, key)
		}
	}
	for key, at := range stored {
		if _, kept := built[key]; !kept && written[projectDay{project: key.project, day: at.day}] {
			gone = append(gone, key)
		}
	}
	if len(moved) > 0 {
		projects, families, keys, repos, shippedBy := make([]string, len(moved)), make([]string, len(moved)), make([]string, len(moved)), make([]string, len(moved)), make([]string, len(moved))
		days := make([]time.Time, len(moved))
		for i, key := range moved {
			at := built[key]
			projects[i], families[i], keys[i], days[i], repos[i], shippedBy[i] = key.project, key.family, key.rowKey, at.day, at.repo, at.shippedBy
		}
		if _, err := tx.Exec(ctx, `
			insert into eval_day_members (project_key, family, row_key, day, repo, shipped_by)
			select * from unnest($1::text[], $2::text[], $3::text[], $4::date[], $5::text[], $6::text[])
				as member(project_key, family, row_key, day, repo, shipped_by)
			order by member.project_key, member.family, member.row_key
			on conflict (project_key, family, row_key) do update
			set day = excluded.day, repo = excluded.repo, shipped_by = excluded.shipped_by
			where (eval_day_members.day, eval_day_members.repo, eval_day_members.shipped_by)
				is distinct from (excluded.day, excluded.repo, excluded.shipped_by)
		`, projects, families, keys, days, repos, shippedBy); err != nil {
			return fmt.Errorf("record the %d members a settings change moved: %w", len(moved), err)
		}
	}
	if len(gone) > 0 {
		projects, families, keys := make([]string, len(gone)), make([]string, len(gone)), make([]string, len(gone))
		days := make([]time.Time, len(gone))
		for i, key := range gone {
			projects[i], families[i], keys[i], days[i] = key.project, key.family, key.rowKey, stored[key].day
		}
		if _, err := tx.Exec(ctx, `
			delete from eval_day_members m
			using unnest($1::text[], $2::text[], $3::text[], $4::date[]) as gone(project_key, family, row_key, day)
			where m.project_key = gone.project_key and m.family = gone.family and m.row_key = gone.row_key and m.day = gone.day
		`, projects, families, keys, days); err != nil {
			return fmt.Errorf("delete the %d members a settings change's days no longer hold: %w", len(gone), err)
		}
	}
	return nil
}
