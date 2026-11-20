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
// settings after a later change committed).
//
// A change that moves no eval writes nothing and begins no walk. One of a setting no day row reads
// (a workflow path, the population rule's authors and excluded repositories, the roll-up job), a
// move of the deploy repository (the evals that read it are kept for every repository that holds
// a delivery run, and a read chooses among them: deploy_repos.go), or one that leaves the
// production rule and every deploy card following the same names with the same step (the order of
// the production names, a staging job renamed between two names the production rule follows),
// moves no eval by settingsMoves, and the pass reads nothing. A production job name no run holds
// moves the rule's names but its verdict on no run, so the rewrite builds every day of the evals
// the rule moves and writes none of them: each of its writes sends a row, a member or a figure
// only where its value moved.

// settingsMoves is the delivery and CI evals (by slug) whose day rows a change of the delivery
// settings from prev to next can move, and the families of eval_day_members it can move, read from
// the code that builds the rows (readDeliveryDay, DeliveryDay.Rows and the measures they call).
// Each is a rule every repository's rows are built by alike, so it moves the rows of each
// repository that holds a run it reads, and of no other (rewriteSettings):
//
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
//   - deploy_repo: none. Every read of a day names it (the deploys, the apply runs, the CI runs),
//     and containment ships its pull requests alone, but the evals those reads build are kept for
//     every repository that holds a delivery run, each as if it were the deploy repository, and
//     the eval list reads those of the repository the settings name (DeployRepoOf).
//   - deploy_workflow_path, pr_checks_workflow_path, population_authors, excluded_repos and
//     pr_checks_rollup_job_name: none. The first four decide what the intake and the reconcile
//     store, each store a change of its own, and no read of a day names them; the roll-up job is
//     read at read time (the Pipeline's failing jobs, pr-ci's items) and by no day row.
func settingsMoves(prev, next delivery.DeliverySettings) (slugs, families []string) {
	moved, family := map[string]bool{}, map[string]bool{}
	if partOf(prev.ProductionJobNames, prev.ProductionApplyStepName) != partOf(next.ProductionJobNames, next.ProductionApplyStepName) {
		for _, slug := range slices.Concat(leadTimeSlugs, []string{SlugDeployFrequency, SlugDeployRunSuccess}) {
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

// rulePart is what a production rule, or a deploy card, counts a run by: the job names it
// follows, sorted and each once, joined by a NUL (no job name holds one), and its apply step.
type rulePart struct {
	names   string
	step    string
	stepped bool
}

// partOf is the rulePart of a rule that follows names and reads the apply step step (nil for
// none).
func partOf(names []string, step *string) rulePart {
	part := rulePart{names: strings.Join(slices.Compact(slices.Sorted(slices.Values(names))), "\x00")}
	if step != nil {
		part.step, part.stepped = *step, true
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
		var names []string
		for _, name := range card.JobNames {
			if !taken[name] {
				names = append(names, name)
			}
		}
		for _, name := range card.JobNames {
			taken[name] = true
		}
		parts[card.CardID] = partOf(names, card.Production.ApplyStep)
	}
	return parts
}

// sameBuiltSettings reports whether a and b agree on every delivery setting a day row reads, the
// fields eval_days_settings records, each exactly as stored. The deploy repository is none of
// them: the rows of the evals that read it are kept for every repository that holds a run.
func sameBuiltSettings(a, b delivery.DeliverySettings) bool {
	return slices.Equal(a.ProductionJobNames, b.ProductionJobNames) &&
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
// (eval_days_settings), the fields a day row reads alone; nil where none are recorded.
func (s *Store) readBuiltUnder(ctx context.Context) (*delivery.DeliverySettings, error) {
	var built delivery.DeliverySettings
	err := s.database.Pool.QueryRow(ctx, `
		select production_job_names, production_apply_step_name, staging_e2e_job_name, staging_apply_job_name
		from eval_days_settings where singleton
	`).Scan(&built.ProductionJobNames, &built.ProductionApplyStepName, &built.StagingE2EJobName, &built.StagingApplyJobName)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("read the delivery settings the day rows stand built under: %w", err)
	}
	return &built, nil
}

// recordBuiltUnder records, in q, settings as the delivery settings the stored delivery and CI rows
// stand built under (eval_days_settings).
func recordBuiltUnder(ctx context.Context, q pgx.Tx, settings delivery.DeliverySettings) error {
	if _, err := q.Exec(ctx, `
		insert into eval_days_settings (production_job_names, production_apply_step_name, staging_e2e_job_name, staging_apply_job_name)
		values ($1, $2, $3, $4)
		on conflict (singleton) do update
		set production_job_names = excluded.production_job_names,
			production_apply_step_name = excluded.production_apply_step_name,
			staging_e2e_job_name = excluded.staging_e2e_job_name, staging_apply_job_name = excluded.staging_apply_job_name,
			updated_at = now()
	`, nonNil(settings.ProductionJobNames), settings.ProductionApplyStepName,
		settings.StagingE2EJobName, settings.StagingApplyJobName); err != nil {
		return fmt.Errorf("record the delivery settings the day rows stand built under: %w", err)
	}
	return nil
}

// syncSettings brings the stored delivery and CI rows to the delivery settings the pass reads
// (r.settings) before the page writes a day under them, on every page, so no row is written under
// settings the rows do not stand built under:
//
//   - where the recorded settings (eval_days_settings, read once a pass) agree with them on every
//     field a day row reads, nothing;
//   - where they differ, it rewrites every day of the evals the difference moves (settingsMoves,
//     rewriteSettings), which records the settings in its own transaction, or, where the
//     difference moves none, has the page record them with its cursors (w.built);
//   - where none are recorded (a database no pass of this release has brought to its settings, or
//     one a bench reset), the rows' settings are unknown, so it begins the delivery walk again,
//     which builds every day under the settings it reads, and has the page record them with the
//     walk it begins, in one transaction.
//
// A move of the deploy repository alone agrees with the record, so it writes nothing at all, and
// is read at read time (DeployRepoOf).
func (r *refresh) syncSettings(ctx context.Context, w *pageWork) error {
	if r.settings == nil {
		return nil
	}
	if !r.builtRead {
		built, err := r.store.readBuiltUnder(ctx)
		if err != nil {
			return err
		}
		r.builtUnder, r.builtRead = built, true
	}
	next := *r.settings
	if r.builtUnder == nil {
		w.restart[walkDelivery], w.built = true, &next
		r.builtUnder = &next
		return nil
	}
	if sameBuiltSettings(*r.builtUnder, next) {
		return nil
	}
	if slugs, families := settingsMoves(*r.builtUnder, next); len(slugs) > 0 {
		if err := r.rewriteSettings(ctx, slugs, families); err != nil {
			return err
		}
	} else {
		w.built = &next
	}
	r.builtUnder = &next
	return nil
}

// rewriteWorkers is the most days rewriteSettings builds at once: the CPUs the process may use, up
// to this many.
const rewriteWorkers = 8

// rewriteDays is how many days rewriteSettings builds and writes at a time, in its one
// transaction: what it holds at once is a chunk's days, their rows and their samples, as many as
// a pass's batch of the delivery walk writes, rather than every day of the history.
const rewriteDays = walkBatch

// rewriteChunk is the most day rows one statement of rewriteSettings' upsert sends: its arrays are
// its parameters, and a chunk of days of a large project holds tens of thousands of rows.
const rewriteChunk = 20000

// memberKey is a member's identity in eval_day_members, and memberAt where it is counted.
type memberKey struct {
	project, family, deployRepo, rowKey string
}

type memberAt struct {
	day             time.Time
	repo, shippedBy string
}

// readMembers reads every member of projects under families counted under one of deployRepos, by
// its key.
func (s *Store) readMembers(ctx context.Context, projects, families, deployRepos []string) (map[memberKey]memberAt, error) {
	held := map[memberKey]memberAt{}
	if len(families) == 0 || len(projects) == 0 || len(deployRepos) == 0 {
		return held, nil
	}
	rows, err := s.database.Pool.Query(ctx, `
		select project_key, family, deploy_repo, row_key, day, repo, shipped_by from eval_day_members
		where project_key = any($1) and family = any($2) and deploy_repo = any($3)
	`, projects, families, deployRepos)
	if err != nil {
		return nil, fmt.Errorf("read the members of %v: %w", families, err)
	}
	var key memberKey
	var at memberAt
	if _, err := pgx.ForEachRow(rows, []any{&key.project, &key.family, &key.deployRepo, &key.rowKey, &at.day, &at.repo, &at.shippedBy}, func() error {
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
// the whole delivery walk under those settings writes them, in one pass that begins no walk. It
// rewrites them for each kept repository (keptRepos) that holds a deploy run, which the production
// rule and the deploy cards judge. A repository that holds none moves with no setting, and its
// rows and members are left as they are:
//
//   - one read of the history, its parts side by side: every population pull request, and every
//     deploy run on main of each repository rewritten with its jobs of the names the production
//     rule judges by and the cards follow (delivery.ReadHistoryRows), the walk's first input day
//     (deliveryInputStart, over every kept repository), each project's first days (firstDays),
//     repo_projects and the stored members of families of the repositories rewritten; then the
//     projects of the issues the pull requests name;
//   - containment once over each repository's history as the deploy repository (historyOf): the
//     production rule's verdict on each run (ProductionApplies, which judges a run by its own jobs
//     alone, and by its jobs of the judged names as by every job), each pull request's first
//     shipping apply (delivery.ShippingIndex), and each deploy's shipped set, the pull requests
//     whose first shipping apply it is (Deploys.ShippedBy's rule over every day at once);
//   - then, in one transaction, the days in day order a chunk at a time (rewriteDays): each day of
//     the chunk built side by side, each repository's DeliveryDay as readDeliveryDay builds it
//     from those rows and each project's rows of the day (deliveryRead.Rows); in day order, each
//     project's day with the zero rows of its series from their first input days on, as the walk
//     writes it (writeDelivery, zerosFrom and noteBuilt), and the days the walk writes, those
//     holding a row; and the chunk's rows of the evals slugs names upserted where a value moved
//     (upsertDays), and those evals' stored rows of the repositories rewritten, of the chunk's
//     days the walk writes, that the days no longer hold made zero rows (zeroStaleDays' rule,
//     over the chunk's days at once);
//   - after the last chunk, in the same transaction: the members of families of those repositories
//     whose day, repository or shipping run moved, or that are new, upserted, and those of the days
//     written that the days no longer hold deleted, and no other member sent; the stored figures of
//     every day whose rows moved folded again (foldFiguresInTx, whose quantiles read each rank
//     across the days' sorted samples, measures.SortedSamples); and the settings recorded as the
//     ones the rows stand built under. Its eval_days changes are one per eval, as every
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
	production := next.Production()
	kept, err := r.keptRepos(ctx)
	if err != nil {
		return err
	}
	var rewritten []keptRepo
	for _, repo := range kept {
		if repo.deploys {
			rewritten = append(rewritten, repo)
		}
	}
	names, every := deployRepoNames(rewritten), deployRepoNames(kept)

	var history delivery.HistoryRows
	var input *time.Time
	var repos repoProjects
	var stored map[memberKey]memberAt
	firsts := make(map[string]map[evalKey]time.Time, len(r.projects))
	if err := store.ReadSideBySide(ctx,
		func(ctx context.Context) (err error) {
			history, err = delivery.ReadHistoryRows(ctx, pool, names, slices.Concat(production.JudgedJobNames(), measures.DeployCardJobNames(cards)), end)
			return err
		},
		func(ctx context.Context) (err error) {
			input, err = r.store.deliveryInputStart(ctx, every)
			return err
		},
		func(ctx context.Context) error {
			for _, project := range r.projects {
				first, err := r.store.firstDays(ctx, project, ZeroDeliveryDay(next, project, r.today, every).Rows)
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
			stored, err = r.store.readMembers(ctx, r.projects, families, names)
			return err
		},
	); err != nil {
		return fmt.Errorf("read the delivery history for a settings change: %w", err)
	}
	earliest := map[evalKey]time.Time{}
	for project, first := range firsts {
		r.first[walkKey{family: walkDelivery, project: project}] = first
		// An eval id names its project, so no two projects' entries share a key.
		maps.Copy(earliest, first)
	}
	var days []time.Time
	if start := earliestOf(input, earliest); start != nil {
		for day := *start; !day.After(r.today); day = day.AddDate(0, 0, 1) {
			days = append(days, day)
		}
	}
	prs := history.PullRequests
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
	prsByDay := map[time.Time][]int{}
	for i := range prs {
		if prs[i].MergedAt != nil {
			day := measures.Midnight(*prs[i].MergedAt)
			prsByDay[day] = append(prsByDay[day], i)
		}
	}
	histories := make([]repoHistory, len(rewritten))
	for k, repo := range rewritten {
		histories[k] = historyOf(repo, history.Deploys[repo.name], prs, production)
	}
	// dayOf is each project's rows of day: each repository's DeliveryDay of it, as readDeliveryDay
	// builds it from the history's rows.
	dayOf := func(day time.Time) []ProjectDay {
		read := deliveryRead{Day: day, repos: names, days: make([]DeliveryDay, len(histories))}
		for k, h := range histories {
			one := DeliveryDay{Day: day, deployRepo: h.repo, since: h.since, cards: cards}
			for _, i := range prsByDay[day] {
				// A partial row, whose opening is not known yet, as readDeliveryDay leaves it out.
				if prs[i].CreatedAt != nil {
					one.prs = append(one.prs, attribute.pullRequest(prs[i], h.containing[i], h.runByID))
				}
			}
			for _, run := range h.deploysByDay[day] {
				one.deploys = append(one.deploys, attribute.deploy(run, h.reached[run.RunID], h.shipped[run.RunID]))
			}
			one.pushes, one.pushJobs = attribute.ciRuns(h.pushesByDay[day]), h.jobs
			read.days[k] = one
		}
		rows := make([]ProjectDay, len(r.projects))
		for p, project := range r.projects {
			rows[p] = read.Rows(project)
		}
		return rows
	}
	ids := map[string]bool{}
	for _, project := range r.projects {
		for _, slug := range slugs {
			ids[EvalID(project, slug)] = true
		}
	}
	evalIDs := slices.Sorted(maps.Keys(ids))

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
	members := map[memberKey]memberAt{}
	written := map[projectDay]bool{}
	folded := map[string]bool{}
	for chunk := range slices.Chunk(days, rewriteDays) {
		// The chunk's days built side by side.
		built := make([][]ProjectDay, len(chunk))
		group, groupCtx := errgroup.WithContext(ctx)
		group.SetLimit(min(runtime.GOMAXPROCS(0), rewriteWorkers))
		for index, day := range chunk {
			group.Go(func() error {
				if err := groupCtx.Err(); err != nil {
					return err
				}
				built[index] = dayOf(day)
				return nil
			})
		}
		if err := group.Wait(); err != nil {
			return err
		}

		// The chunk's days the walk writes, in day order, as it moves each eval's first input day.
		var rows []DayRow
		var pairProjects []string
		var pairDays []time.Time
		for index, day := range chunk {
			for p, project := range r.projects {
				own := built[index][p]
				first := r.first[walkKey{family: walkDelivery, project: project}]
				own.Rows = dropUnshipped(own.Rows, first, day)
				full := own.WithZeros(zerosFrom(ZeroDeliveryDay(next, project, day, names).Rows, first, day))
				if len(full.Rows) == 0 {
					continue
				}
				pairProjects, pairDays = append(pairProjects, project), append(pairDays, day)
				written[projectDay{project: project, day: day}], folded[project] = true, true
				for _, row := range full.Rows {
					if ids[row.EvalID] {
						rows = append(rows, row)
					}
				}
				for _, member := range full.Members {
					if slices.Contains(families, member.Family) {
						key := memberKey{project: project, family: member.Family, deployRepo: member.DeployRepo, rowKey: member.RowKey}
						members[key] = memberAt{day: day, repo: member.Repo, shippedBy: member.ShippedBy}
					}
				}
				r.noteBuilt(walkDelivery, project, day, own.Rows)
				r.report.days[builtDay{family: walkDelivery, project: project, day: day}] = true
			}
		}
		for part := range slices.Chunk(rows, rewriteChunk) {
			if err := upsertDays(ctx, tx, part, r.build); err != nil {
				return err
			}
		}
		if err := zeroStaleSettingsDays(ctx, tx, pairProjects, pairDays, evalIDs, names, rows, r.build); err != nil {
			return err
		}
	}
	if err := writeMovedMembers(ctx, tx, stored, members, written); err != nil {
		return err
	}
	for _, project := range slices.Sorted(maps.Keys(folded)) {
		spans := make(map[string]daySpan, len(slugs))
		for _, slug := range slugs {
			spans[EvalID(project, slug)] = daySpan{first: days[0], last: r.today}
		}
		if err := foldFiguresInTx(ctx, tx, r.build, project, spans); err != nil {
			return err
		}
	}
	if err := recordBuiltUnder(ctx, tx, next); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit the rewrite of a settings change: %w", err)
	}
	r.store.drainChanges(ctx)
	return nil
}

// repoHistory is containment over one repository's whole history as the deploy repository
// (historyOf): the start of its history, its deploy runs by id, those that reached production,
// each pull request's first shipping apply (by its index among the history's pull requests), the
// pull requests each apply ships first, and its deploy runs and push runs on main by the day they
// started, with their jobs.
type repoHistory struct {
	repo                      string
	since                     time.Time
	runByID                   map[int64]delivery.DeliveryRun
	reached                   map[int64]bool
	containing                []*delivery.Apply
	shipped                   map[int64][]*delivery.DeliveryPullRequest
	deploysByDay, pushesByDay map[time.Time][]delivery.DeliveryRun
	jobs                      map[int64][]delivery.DeliveryRunJob
}

// historyOf is containment over repo's deploy runs and their jobs, deploys, as the deploy
// repository, of prs, every population pull request, under production.
func historyOf(repo keptRepo, deploys delivery.RunsWithJobs, prs []delivery.DeliveryPullRequest, production delivery.Production) repoHistory {
	h := repoHistory{
		repo: repo.name, since: repo.since, runByID: make(map[int64]delivery.DeliveryRun, len(deploys.Runs)), reached: map[int64]bool{},
		containing: make([]*delivery.Apply, len(prs)), shipped: map[int64][]*delivery.DeliveryPullRequest{},
		deploysByDay: map[time.Time][]delivery.DeliveryRun{}, pushesByDay: map[time.Time][]delivery.DeliveryRun{}, jobs: deploys.Jobs,
	}
	applies := delivery.ProductionApplies(deploys.Runs, deploys.Jobs, production).Applies
	for _, apply := range applies {
		h.reached[apply.RunID] = true
	}
	shipping := delivery.NewShippingIndex(applies)
	for i := range prs {
		if h.containing[i] = shipping.Containing(prs[i], repo.name); h.containing[i] != nil {
			h.shipped[h.containing[i].RunID] = append(h.shipped[h.containing[i].RunID], &prs[i])
		}
	}
	for _, run := range deploys.Runs {
		h.runByID[run.RunID] = run
		day := measures.Midnight(run.StartedAt)
		h.deploysByDay[day] = append(h.deploysByDay[day], run)
		if run.Event != nil && *run.Event == "push" {
			h.pushesByDay[day] = append(h.pushesByDay[day], run)
		}
	}
	return h
}

// zeroStaleSettingsDays is zeroStaleDays over many days at once: each of the computation's stored
// rows of the evals evalIDs names and of the deploy repositories deployRepos names, on each day of
// a project the rewrite writes (projects and days, pairwise), that held is not among, written as a
// zero row recording builtBy, and one already zero left as it is, as zeroStaleDays writes them.
func zeroStaleSettingsDays(ctx context.Context, tx pgx.Tx, projects []string, days []time.Time, evalIDs, deployRepos []string, held []DayRow, builtBy string) error {
	if len(projects) == 0 || len(deployRepos) == 0 {
		return nil
	}
	count := len(held)
	heldProjects, heldEvals, heldDeployRepos := make([]string, count), make([]string, count), make([]string, count)
	heldSeries, heldRepos, heldDays := make([]string, count), make([]string, count), make([]time.Time, count)
	for i, row := range held {
		heldProjects[i], heldEvals[i], heldDeployRepos[i] = row.ProjectKey, row.EvalID, row.DeployRepo
		heldSeries[i], heldRepos[i], heldDays[i] = row.Series, row.Repo, measures.Midnight(row.Day)
	}
	if _, err := tx.Exec(ctx, `
		with written as materialized (
			select * from unnest($1::text[], $2::date[]) as written(project_key, day)
		), held as materialized (
			select * from unnest($3::text[], $4::text[], $11::text[], $5::text[], $6::text[], $7::date[])
				as held(project_key, eval_id, deploy_repo, series, repo, day)
		), stale as (
			select e.project_key, e.eval_id, e.writer, e.deploy_repo, e.series, e.repo, e.day,
				`+staleZeroColumns+`
			from eval_days e
			join written on written.project_key = e.project_key and written.day = e.day
			where e.eval_id = any($8) and e.writer = $10 and e.deploy_repo = any($12)
				and not exists (select 1 from held where held.project_key = e.project_key and held.eval_id = e.eval_id
					and held.deploy_repo = e.deploy_repo and held.series = e.series and held.repo = e.repo and held.day = e.day)
		)`+zeroStaleUpdate("$9"), projects, days, heldProjects, heldEvals, heldSeries, heldRepos, heldDays, evalIDs, builtBy, writerComputation,
		heldDeployRepos, deployRepos); err != nil {
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
		projects, families, deployRepos, keys := make([]string, len(moved)), make([]string, len(moved)), make([]string, len(moved)), make([]string, len(moved))
		repos, shippedBy, days := make([]string, len(moved)), make([]string, len(moved)), make([]time.Time, len(moved))
		for i, key := range moved {
			at := built[key]
			projects[i], families[i], deployRepos[i], keys[i] = key.project, key.family, key.deployRepo, key.rowKey
			days[i], repos[i], shippedBy[i] = at.day, at.repo, at.shippedBy
		}
		if _, err := tx.Exec(ctx, `
			insert into eval_day_members (project_key, family, deploy_repo, row_key, day, repo, shipped_by)
			select member.* from unnest($1::text[], $2::text[], $3::text[], $4::text[], $5::date[], $6::text[], $7::text[])
				as member(project_key, family, deploy_repo, row_key, day, repo, shipped_by)
			order by member.project_key, member.family, member.deploy_repo, member.row_key
			on conflict (project_key, family, deploy_repo, row_key) do update
			set day = excluded.day, repo = excluded.repo, shipped_by = excluded.shipped_by
			where (eval_day_members.day, eval_day_members.repo, eval_day_members.shipped_by)
				is distinct from (excluded.day, excluded.repo, excluded.shipped_by)
		`, projects, families, deployRepos, keys, days, repos, shippedBy); err != nil {
			return fmt.Errorf("record the %d members a settings change moved: %w", len(moved), err)
		}
	}
	if len(gone) > 0 {
		projects, families, deployRepos, keys := make([]string, len(gone)), make([]string, len(gone)), make([]string, len(gone)), make([]string, len(gone))
		days := make([]time.Time, len(gone))
		for i, key := range gone {
			projects[i], families[i], deployRepos[i], keys[i], days[i] = key.project, key.family, key.deployRepo, key.rowKey, stored[key].day
		}
		if _, err := tx.Exec(ctx, `
			delete from eval_day_members m
			using unnest($1::text[], $2::text[], $3::text[], $4::text[], $5::date[]) as gone(project_key, family, deploy_repo, row_key, day)
			where m.project_key = gone.project_key and m.family = gone.family and m.deploy_repo = gone.deploy_repo
				and m.row_key = gone.row_key and m.day = gone.day
		`, projects, families, deployRepos, keys, days); err != nil {
			return fmt.Errorf("delete the %d members a settings change's days no longer hold: %w", len(gone), err)
		}
	}
	return nil
}
