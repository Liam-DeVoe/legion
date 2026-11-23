package evals

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/delivery"
	"github.com/sjawhar/envoy/internal/dispatch/delivery/measures"
	"github.com/sjawhar/envoy/internal/dispatch/prodevents"
)

// failure_days.go is the day rows of the three failure measures, change-fail-rate,
// failed-deploy-recovery and deployment-rework-rate (dispatch://INFRA-978, D7, R9, R11 and R12):
// project rows of eval_days that no deploy repository keys, built from the record of deploys and
// failures (production_events).
//
// A project's failure measures are on where its delivery is on and it holds at least one failure
// source (failureProjectOf). Its rows begin on its failures_measured_since, and no row stands
// before that day. From it through today every day holds the all-repositories row of each of the
// three, a day with no deploy a zero row, so a window after the first day with no failure reads 0;
// and one row of each per repository the day's deploys, or the deploys of the failures restored
// that day, are in. A read names that day as each series' first day (failureFirstDays), so a window
// that starts before it reads "no data" and names it, whatever rows stand.
//
// A failure is a fact about its deploy. The project's deploys of a day are the deploy rows that
// finished on it that the attribution counts for it (recordProjects, deployProjects' rule): a
// GitHub deploy for the projects of the pull requests it ships through their issues, and, where it
// ships one that names no issue, for each project whose default repository is its repository and
// that is not among those (its owner key, owners.go), or, where it ships none, for the project
// whose source found it (the row's project, through its repository); a posted deploy for
// the project of its route alone, where that project holds an events source, whose posted deploys
// count for it there as they do in deploy-frequency. A deploy the GitHub adapter dropped (status
// `dropped`: a rule change no longer calls its run a deploy, and a failure blames it) counts in no
// numerator and no denominator. The failures counted for the project are the unrejected failure
// rows on those deploys whose signal maps to a source the project has on (signalSources), a
// person's flag under any source. Of each day then:
//
//   - change-fail-rate: the deploys with a counted failure over the deploys
//     (measures.ChangeFailRateDay);
//   - failed-deploy-recovery: the minutes from each counted failure's deploy to its restore, of the
//     failures restored that day, beside the count of the counted failures of the day's deploys not
//     restored yet (measures.RecoveryDay);
//   - deployment-rework-rate: the deploys named as restoring a counted failure over the deploys
//     (measures.ReworkRateDay).
//
// A part's via_repository counts the rows of it that reached the project through a repository: a
// GitHub deploy that ships no pull request, and a failure restored on such a deploy.
//
// The refresher keeps them (refresh.applyFailures, refresh.walkFailures). A change of the record
// rewrites, in every project whose measures are on, the days it names: the time its row counts on
// before and after (a deploy's finish, a failure's deploy's finish, a restore's time) and the finish
// day of the deploy that restored a failure it names. A change of a project's delivery settings, and
// a page that finds the repositories kept for the delivery rows moved, rewrite that project's rows
// whole, or delete them where its measures are off. The first walking pass of a process and of each
// UTC day writes every project's rows whole: so today gains its row, and what no change of the
// record names (a pull request whose issue moved to another project) reaches the rows within a day.
// Each write is a project's days in one transaction, as every computed day row is written: upserted
// where a value moved, the stale rows deleted, and the stored figures of the days folded again.

// failureDaySlugs is the evals a project's failure rows are of.
var failureDaySlugs = []string{SlugChangeFailRate, SlugFailedDeployRecovery, SlugDeploymentReworkRate}

// failureFamily names the failure rows in a pass's report (builtDay) and in a refusal.
const failureFamily = "failure"

// isFailureMeasure reports whether definition is one of the three failure measures: a computed
// eval of failureDaySlugs.
func isFailureMeasure(definition Definition) bool {
	return definition.Computed() && slices.Contains(failureDaySlugs, definition.Slug)
}

// signalSources is the failure source each signal of the record counts under
// (delivery.FailureSources). A person's flag (prodevents.SignalPerson) counts under any of them.
var signalSources = map[string]string{
	prodevents.SignalCheckFail:     delivery.FailureSourceCheckResults,
	prodevents.SignalRevert:        delivery.FailureSourceReverts,
	prodevents.SignalIncidentIssue: delivery.FailureSourceIncidentLabel,
	prodevents.SignalAPI:           delivery.FailureSourceEvents,
}

// failureProject is one project whose failure measures are on: its failure sources, the UTC
// midnight of its failures_measured_since, before which it holds no row, whether it holds an
// events deploy source, whose posted deploys count for it, and its default repository ("" for
// none), whose owner keys' deploys count for it as a read of its rows folds them (owners.go).
type failureProject struct {
	project     string
	sources     []string
	since       time.Time
	posted      bool
	defaultRepo string
}

// failureProjectOf is settings' failure project, false where its failure measures are off:
// delivery off, or no failure source.
func failureProjectOf(settings delivery.ProjectSettings) (failureProject, bool) {
	if !settings.Delivery || len(settings.FailureSources) == 0 || settings.FailuresMeasuredSince == nil {
		return failureProject{}, false
	}
	posted := slices.ContainsFunc(settings.Sources, func(source delivery.DeploySource) bool {
		return source.Kind == delivery.SourceKindEvents
	})
	project := failureProject{
		project: settings.Project, sources: settings.FailureSources, since: measures.Midnight(*settings.FailuresMeasuredSince), posted: posted,
	}
	if settings.DefaultRepo != nil {
		project.defaultRepo = *settings.DefaultRepo
	}
	return project, true
}

// reaches reports whether c, one of the projects or owner keys a deploy counts for
// (deployProjects), is p's: p itself, or an owner key of p's default repository whose sharers do
// not hold p (eval_project_owners' rule).
func (p failureProject) reaches(c counted) bool {
	if c.project == p.project {
		return true
	}
	repo, sharers, ok := parseOwner(c.project)
	return ok && p.defaultRepo != "" && strings.EqualFold(repo, p.defaultRepo) && !slices.Contains(sharers, p.project)
}

// counts reports whether a failure of signal counts for p: a person's flag under any source, and
// every other signal where p has its source on.
func (p failureProject) counts(signal *string) bool {
	if signal == nil {
		return false
	}
	if *signal == prodevents.SignalPerson {
		return true
	}
	source, known := signalSources[*signal]
	return known && slices.Contains(p.sources, source)
}

// failureDeploy is one deploy row a project's failure rows count, and whether it reached the
// project through its repository rather than an issue or a route (via_repository).
type failureDeploy struct {
	prodevents.Event
	via bool
}

// failureRecord is what one project's failure rows are built from (readFailureRecord): every
// deploy row that finished from the project's first day on that the attribution counts for it, in
// the order they finished (index by id), and the failures counted for it on them (counted, in the
// order they were detected), with, by deploy id, how many counted failures blame it (failed), how
// many of those are not restored (open) and whether it restored one (restores), and the counted
// failures restored on each UTC day, by their index in counted (restoredOn).
type failureRecord struct {
	project    failureProject
	deploys    []failureDeploy
	index      map[string]int
	counted    []prodevents.Event
	failed     map[string]int
	open       map[string]int
	restores   map[string]bool
	restoredOn map[time.Time][]int
}

// readFailureRecord reads, in q, project's failureRecord: the deploy rows of projects (every
// project, so a deploy of another project's repository that ships the project's pull request is
// read) that finished from its first day on (prodevents.DeploysFrom), the issues of the pull
// requests the GitHub ones ship (readShippedIssues), and the failure rows on the deploys that count
// for it (prodevents.FailuresOf).
func readFailureRecord(ctx context.Context, q delivery.Querier, project failureProject, projects []string) (failureRecord, error) {
	record := failureRecord{
		project: project, index: map[string]int{}, failed: map[string]int{}, open: map[string]int{},
		restores: map[string]bool{}, restoredOn: map[time.Time][]int{},
	}
	deploys, err := prodevents.DeploysFrom(ctx, q, projects, project.since)
	if err != nil {
		return failureRecord{}, err
	}
	attribute, issueOf, err := readShippedIssues(ctx, q, deploys)
	if err != nil {
		return failureRecord{}, err
	}
	for _, deploy := range deploys {
		if deploy.Source == prodevents.SourceAPI && !project.posted {
			continue
		}
		counts := attribute.recordProjects(deploy, issueOf)
		at := slices.IndexFunc(counts, project.reaches)
		if at < 0 {
			continue
		}
		record.index[deploy.ID] = len(record.deploys)
		record.deploys = append(record.deploys, failureDeploy{Event: deploy, via: counts[at].via})
	}
	failures, err := prodevents.FailuresOf(ctx, q, slices.Collect(maps.Keys(record.index)))
	if err != nil {
		return failureRecord{}, err
	}
	for _, failure := range failures {
		if failure.State != prodevents.StateCounted || !project.counts(failure.Signal) {
			continue
		}
		blamed := *failure.DeployID
		record.failed[blamed]++
		if failure.RestoredAt == nil {
			record.open[blamed]++
		} else {
			day := measures.Midnight(*failure.RestoredAt)
			record.restoredOn[day] = append(record.restoredOn[day], len(record.counted))
		}
		if by := failure.RestoredByDeploy; by != nil {
			if _, held := record.index[*by]; held {
				record.restores[*by] = true
			}
		}
		record.counted = append(record.counted, failure)
	}
	return record, nil
}

// recordProjects is what deploy, a deploy row of the record, counts for, by deployProjects' rule:
// a posted deploy its route's project alone, through no repository; a GitHub deploy the projects
// of the pull requests it ships through the issue its stored row names (issueOf, by row key), and
// its repository's owner key where one names no issue, or the row's project where it ships none; a
// dropped deploy nothing.
func (a attribution) recordProjects(deploy prodevents.Event, issueOf map[string]string) []counted {
	switch {
	case deploy.Status == prodevents.StatusDropped:
		return nil
	case deploy.Source == prodevents.SourceAPI:
		return []counted{{project: deploy.ProjectKey}}
	}
	var shipped []*delivery.DeliveryPullRequest
	for _, ref := range deploy.PullRequests {
		repo, number, ok := parsePullRequestRef(ref)
		if !ok {
			continue
		}
		pr := &delivery.DeliveryPullRequest{Repo: repo, Number: number}
		if issue, named := issueOf[PullRequestMember(repo, number)]; named {
			pr.IssueKey = &issue
		}
		shipped = append(shipped, pr)
	}
	return a.deployProjects(deploy.Repo, shipped, deploy.ProjectKey)
}

// readShippedIssues reads, in q, what attributes the pull requests the GitHub deploys of deploys
// ship (production_events.pull_requests, containment's): the issue each one's stored row names, by
// its row key (issueOf), and that issue's project (the attribution's issues), where the population
// holds a row of the pull request naming an issue the issues table holds. One statement, a probe of
// delivery_pull_requests' key for each pull request.
func readShippedIssues(ctx context.Context, q delivery.Querier, deploys []prodevents.Event) (attribution, map[string]string, error) {
	attribute, issueOf := attribution{issues: map[string]string{}, pulls: map[string]string{}}, map[string]string{}
	var members, repos []string
	var numbers []int32
	named := map[string]bool{}
	for _, deploy := range deploys {
		if deploy.Source == prodevents.SourceAPI || deploy.Status == prodevents.StatusDropped {
			continue
		}
		for _, ref := range deploy.PullRequests {
			repo, number, ok := parsePullRequestRef(ref)
			if !ok {
				continue
			}
			member := PullRequestMember(repo, number)
			if !named[member] {
				named[member] = true
				members, repos, numbers = append(members, member), append(repos, repo), append(numbers, int32(number))
			}
		}
	}
	if len(members) == 0 {
		return attribute, issueOf, nil
	}
	rows, err := q.Query(ctx, `
		select given.member, request.issue_key, issue.project_key
		from unnest($1::text[], $2::text[], $3::int[]) as given(member, repo, number)
		join delivery_pull_requests request on request.repo = given.repo and request.number = given.number
		join issues issue on issue.key = request.issue_key
	`, members, repos, numbers)
	if err != nil {
		return attribution{}, nil, fmt.Errorf("read the issues of the %d pull requests the deploys ship: %w", len(members), err)
	}
	var member, issue, project string
	if _, err := pgx.ForEachRow(rows, []any{&member, &issue, &project}, func() error {
		issueOf[member], attribute.issues[issue] = issue, project
		return nil
	}); err != nil {
		return attribution{}, nil, fmt.Errorf("read the issues of the %d pull requests the deploys ship: %w", len(members), err)
	}
	return attribute, issueOf, nil
}

// deploysFrom is the index in f.deploys of the first deploy that finished at or after at.
func (f failureRecord) deploysFrom(at time.Time) int {
	first, _ := slices.BinarySearchFunc(f.deploys, at, func(deploy failureDeploy, at time.Time) int {
		return deploy.FinishedAt.Compare(at)
	})
	return first
}

// day is the project's failure rows of day, a UTC midnight: none before its first day, and from it
// the all-repositories row of each of the three measures and one row of each per repository the
// day's deploys, or the deploys of the failures restored that day, are in (a posted deploy that
// names no repository is in no repository's rows).
func (f failureRecord) day(day time.Time) ProjectDay {
	built := ProjectDay{Project: f.project.project, Day: day}
	if day.Before(f.project.since) {
		return built
	}
	type cut struct {
		deploys, viaDeploys   []measures.FailureDeploy
		restored, viaRestored []measures.RestoredFailure
		open                  int
	}
	cuts := map[string]*cut{"": {}}
	cutsOf := func(repo string) []*cut {
		held := []*cut{cuts[""]}
		if repo != "" {
			if cuts[repo] == nil {
				cuts[repo] = &cut{}
			}
			held = append(held, cuts[repo])
		}
		return held
	}
	end := day.AddDate(0, 0, 1)
	for _, deploy := range f.deploys[f.deploysFrom(day):] {
		if !deploy.FinishedAt.Before(end) {
			break
		}
		one := measures.FailureDeploy{ID: deploy.ID, FinishedAt: *deploy.FinishedAt, Failures: f.failed[deploy.ID], Restores: f.restores[deploy.ID]}
		for _, c := range cutsOf(deploy.Repo) {
			c.deploys = append(c.deploys, one)
			if deploy.via {
				c.viaDeploys = append(c.viaDeploys, one)
			}
			c.open += f.open[deploy.ID]
		}
	}
	for _, at := range f.restoredOn[day] {
		failure := f.counted[at]
		deploy := f.deploys[f.index[*failure.DeployID]]
		one := measures.RestoredFailure{DeployFinishedAt: *deploy.FinishedAt, RestoredAt: *failure.RestoredAt}
		for _, c := range cutsOf(deploy.Repo) {
			c.restored = append(c.restored, one)
			if deploy.via {
				c.viaRestored = append(c.viaRestored, one)
			}
		}
	}
	for _, repo := range slices.Sorted(maps.Keys(cuts)) {
		c := cuts[repo]
		add := func(slug string, parts, viaParts []measures.DayPart) {
			via := make(map[string]int, len(viaParts))
			for _, part := range viaParts {
				via[part.Series] = part.N
			}
			for _, part := range parts {
				part.ViaRepository = via[part.Series]
				built.Rows = append(built.Rows, DayRow{
					ProjectKey: f.project.project, EvalID: EvalID(f.project.project, slug), Repo: repo, Listed: true, DayPart: part,
				})
			}
		}
		add(SlugChangeFailRate, measures.ChangeFailRateDay(day, c.deploys), measures.ChangeFailRateDay(day, c.viaDeploys))
		add(SlugFailedDeployRecovery, measures.RecoveryDay(day, c.restored, c.open), measures.RecoveryDay(day, c.viaRestored, 0))
		add(SlugDeploymentReworkRate, measures.ReworkRateDay(day, c.deploys), measures.ReworkRateDay(day, c.viaDeploys))
	}
	return built
}

// failureWrite is one write of a project's failure rows: its days, each whole, in day order, and
// whether they are every day its rows hold (whole), so that a stored row of any other day goes too.
type failureWrite struct {
	project string
	days    []ProjectDay
	whole   bool
}

// failureStaleDays is how many days one statement of deleteStaleFailureDays reads the stored rows
// of: a month of a project's rows, each day three all-repositories rows and three for each
// repository, well inside statementBytes.
const failureStaleDays = 31

// writeFailureDays writes write in one transaction, as writeProjectDay writes a day: every day's
// rows upserted where a value moved (upsertDays) and the project's stored rows of the three evals
// on those days that the days no longer hold deleted (deleteStaleFailureDays); for a whole write,
// every stored row of them on any other day too; and the stored figures of the days folded again
// (foldFiguresInTx), of every day for a whole write. Its change rows wait in the change log's
// outbox and are moved into the log once it has committed (drainChanges), so the evals view of the
// project wakes. A row of another project, another day or another eval is refused before anything
// is written.
func (s *Store) writeFailureDays(ctx context.Context, write failureWrite, builtBy string) error {
	if len(write.days) == 0 && !write.whole {
		return nil
	}
	evalIDs := make([]string, len(failureDaySlugs))
	for i, slug := range failureDaySlugs {
		evalIDs[i] = EvalID(write.project, slug)
	}
	var rows []DayRow
	spans := map[string]daySpan{}
	for _, day := range write.days {
		if err := checkProjectDay(failureFamily, day, failureDaySlugs, nil); err != nil {
			return err
		}
		rows = append(rows, day.Rows...)
		for _, id := range evalIDs {
			addSpan(spans, id, day.Day, day.Day)
		}
	}
	tx, err := s.database.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin the failure rows of %s: %w", write.project, err)
	}
	defer tx.Rollback(ctx)
	if len(rows) > 0 {
		if err := upsertDays(ctx, tx, rows, builtBy); err != nil {
			return err
		}
	}
	if err := deleteStaleFailureDays(ctx, tx, write.project, evalIDs, write.days); err != nil {
		return err
	}
	if write.whole {
		// Every day outside the days written: before the first day and after today, or every day
		// where the project's measures are off and no day is written. The figures of each day a
		// row goes from are folded again with the days written.
		outside := `delete from eval_days where project_key = $1 and eval_id = any($2) and writer = $3`
		args := []any{write.project, evalIDs, writerComputation}
		if len(write.days) > 0 {
			outside += ` and (day < $4::date or day > $5::date)`
			args = append(args, write.days[0].Day, write.days[len(write.days)-1].Day)
		}
		rows, err := tx.Query(ctx, outside+` returning eval_id, day`, args...)
		if err != nil {
			return fmt.Errorf("delete the failure rows %s holds outside its days: %w", write.project, err)
		}
		var evalID string
		var day time.Time
		if _, err := pgx.ForEachRow(rows, []any{&evalID, &day}, func() error {
			addSpan(spans, evalID, day, day)
			return nil
		}); err != nil {
			return fmt.Errorf("delete the failure rows %s holds outside its days: %w", write.project, err)
		}
	}
	if err := foldFiguresInTx(ctx, tx, builtBy, write.project, spans); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit the failure rows of %s: %w", write.project, err)
	}
	s.drainChanges(ctx)
	return nil
}

// deleteStaleFailureDays deletes, in tx, each of the computation's stored rows of project's evals
// evalIDs on the days of days that those days do not hold (deleteStaleDays' rule, over several days
// at once), failureStaleDays days a statement.
func deleteStaleFailureDays(ctx context.Context, tx pgx.Tx, project string, evalIDs []string, days []ProjectDay) error {
	for chunk := range slices.Chunk(days, failureStaleDays) {
		var dates, heldDays []time.Time
		var evals, deployRepos, series, repos []string
		for _, day := range chunk {
			dates = append(dates, day.Day)
			for _, row := range day.Rows {
				evals, deployRepos, series, repos = append(evals, row.EvalID), append(deployRepos, row.DeployRepo), append(series, row.Series), append(repos, row.Repo)
				heldDays = append(heldDays, row.Day)
			}
		}
		if _, err := tx.Exec(ctx, `
			with gone as materialized (
				select eval_id, deploy_repo, series, repo, day from eval_days
				where project_key = $1 and eval_id = any($2) and writer = $3 and day = any($4::date[])
				except
				select * from unnest($5::text[], $6::text[], $7::text[], $8::text[], $9::date[]) as held(eval_id, deploy_repo, series, repo, day)
			)
			delete from eval_days stored
			using gone
			where stored.project_key = $1 and stored.eval_id = gone.eval_id and stored.writer = $3
				and stored.deploy_repo = gone.deploy_repo and stored.series = gone.series and stored.repo = gone.repo
				and stored.day = gone.day
		`, project, evalIDs, writerComputation, dates, evals, deployRepos, series, repos, heldDays); err != nil {
			return fmt.Errorf("delete the failure rows %s no longer holds: %w", project, err)
		}
	}
	return nil
}

// writeFailures writes project's failure rows (writeFailureDays) as of the pass: every day from
// its first day through today where named is nil (a whole write, every row where its measures are
// off deleted, which the walk then leaves for the day: r.failuresWritten), and otherwise each day
// named holds and the finish day of each deploy that restored a counted failure among changed (the
// record's rows a page's changes name, by id), up to today. It writes nothing where named is set
// and the project's measures are off.
func (r *refresh) writeFailures(ctx context.Context, project string, named map[time.Time]bool, changed map[string]bool) (err error) {
	if named == nil {
		defer func() {
			if err == nil {
				r.failuresWritten[project] = r.today
			}
		}()
	}
	failures, on := r.failures[project]
	if !on {
		if named != nil {
			return nil
		}
		return r.store.writeFailureDays(ctx, failureWrite{project: project, whole: true}, r.build)
	}
	record, err := readFailureRecord(ctx, r.store.database.Pool, failures, r.projects)
	if err != nil {
		return err
	}
	var days []time.Time
	if named == nil {
		for day := failures.since; !day.After(r.today); day = day.AddDate(0, 0, 1) {
			days = append(days, day)
		}
	} else {
		named = maps.Clone(named)
		for _, failure := range record.counted {
			if by := failure.RestoredByDeploy; changed[failure.ID] && by != nil {
				if at, held := record.index[*by]; held {
					named[measures.Midnight(*record.deploys[at].FinishedAt)] = true
				}
			}
		}
		for day := range named {
			if !day.After(r.today) {
				days = append(days, day)
			}
		}
		slices.SortFunc(days, time.Time.Compare)
	}
	write := failureWrite{project: project, whole: named == nil}
	for _, day := range days {
		write.days = append(write.days, record.day(day))
		r.report.days[builtDay{family: failureFamily, project: project, day: day}] = true
	}
	return r.store.writeFailureDays(ctx, write, r.build)
}

// applyFailures writes the failure rows a page's changes move (the package comment's rule): of each
// project whose delivery settings changed, or whose default repository the page publishes moved
// (w.defaults, whose owner keys' deploys count for it), its rows whole; of every project, where the
// page begins a rebuild (a database or a change cursor whose changes are lost, or rules the rows
// stand built under that are not recorded), and of every project whose measures are on, where the
// repositories kept for the delivery rows moved (w.kept), the rows whole; and otherwise, of every
// project whose measures are on, the days the record's changes name (writeFailures).
func (r *refresh) applyFailures(ctx context.Context, w *pageWork) error {
	named, changed := map[time.Time]bool{}, map[string]bool{}
	for _, rows := range w.posted {
		for id, days := range rows {
			changed[id] = true
			for _, day := range days {
				named[day] = true
			}
		}
	}
	for _, project := range r.projects {
		_, on := r.failures[project]
		switch {
		case w.rebuild != "" || w.settingsOf[project] || w.kept != nil && on || w.defaults != nil && slices.Contains(w.defaults.moved, project):
			if err := r.writeFailures(ctx, project, nil, nil); err != nil {
				return err
			}
		case on && len(changed) > 0:
			if err := r.writeFailures(ctx, project, named, changed); err != nil {
				return err
			}
		}
	}
	return nil
}

// walkFailures writes each project's failure rows whole (writeFailures) once a UTC day: on the
// first walking pass of a process and of each UTC day, of each project no whole write of this
// process reached that day (r.failuresWritten, the loop's). Each project is a write of the walks,
// which stop between two of them for an owed wake (yields) and leave the rest to the next pass.
func (r *refresh) walkFailures(ctx context.Context) error {
	for _, project := range r.projects {
		if r.failuresWritten[project].Equal(r.today) {
			continue
		}
		if r.yields(ctx) {
			return nil
		}
		if err := r.writeFailures(ctx, project, nil, nil); err != nil {
			return err
		}
		r.walkBuilt++
	}
	return nil
}

// failureFirstDays sets, in firsts (FirstDaysIn's), the first day of each series of entries'
// failure measures (isFailureMeasure): the project's failures_measured_since where its measures
// are on (failuresSinceIn), and none otherwise, whatever rows stand (R9). So a range that starts
// before that day reads "no data" and names it, a day after today included, on which no row
// stands yet.
func failureFirstDays(ctx context.Context, q delivery.Querier, entries []Entry, firsts map[string]map[string]time.Time) error {
	var projects []string
	for _, entry := range entries {
		if isFailureMeasure(entry.Definition) && !slices.Contains(projects, entry.Definition.ProjectKey) {
			projects = append(projects, entry.Definition.ProjectKey)
		}
	}
	if len(projects) == 0 {
		return nil
	}
	since, err := failuresSinceIn(ctx, q, projects)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !isFailureMeasure(entry.Definition) {
			continue
		}
		id := entry.Definition.EvalID
		delete(firsts, id)
		day, on := since[entry.Definition.ProjectKey]
		if !on {
			continue
		}
		firsts[id] = make(map[string]time.Time, len(entry.Series))
		for _, series := range entry.Series {
			firsts[id][series.Series] = day
		}
	}
	return nil
}

// failuresSinceIn reads in q the first day of the failure rows of each of projects whose failure
// measures are on (failureProjectOf's rule: delivery on and a failure source), by project: the UTC
// midnight of its failures_measured_since, read as the settings reads read it.
func failuresSinceIn(ctx context.Context, q delivery.Querier, projects []string) (map[string]time.Time, error) {
	rows, err := q.Query(ctx, `
		select project_key, failures_measured_since::timestamptz from project_delivery_settings
		where project_key = any($1) and delivery and cardinality(failure_sources) > 0 and failures_measured_since is not null
	`, projects)
	if err != nil {
		return nil, fmt.Errorf("read the first days of the failure measures of %d projects: %w", len(projects), err)
	}
	since := map[string]time.Time{}
	var project string
	var day time.Time
	if _, err := pgx.ForEachRow(rows, []any{&project, &day}, func() error {
		since[project] = measures.Midnight(day.UTC())
		return nil
	}); err != nil {
		return nil, fmt.Errorf("read the first days of the failure measures of %d projects: %w", len(projects), err)
	}
	return since, nil
}

// DeploymentFailures is the stored day parts of the three failure measures a deployment-wide read
// folds (Store.DeploymentFailures): those of the one delivery-on project, with its first day.
type DeploymentFailures struct {
	parts measures.FailureParts
	since *time.Time
}

// Within is d's parts of days, a range of whole UTC days, as an eval read of that range folds the
// project's stored rows: none where the range starts before the project's first day or the project
// has none ("no data", FirstDaysIn's rule), and otherwise the parts of the range's days.
func (d DeploymentFailures) Within(days Range) measures.FailureParts {
	if d.since == nil || days.From.Before(*d.since) {
		return measures.FailureParts{}
	}
	in := func(parts []measures.DayPart) []measures.DayPart {
		var kept []measures.DayPart
		for _, part := range parts {
			if !part.Day.Before(days.From) && part.Day.Before(days.To) {
				kept = append(kept, part)
			}
		}
		return kept
	}
	return measures.FailureParts{ChangeFail: in(d.parts.ChangeFail), Recovery: in(d.parts.Recovery), Rework: in(d.parts.Rework)}
}

// DeploymentFailures reads, in one snapshot, the stored all-repositories parts of the three
// failure measures on the days of days, and the first day, of the one project that has delivery on
// (dispatch://INFRA-978, D9): a deployment-wide read answers the failure measures only where one
// project's rows are the deployment's, since a deploy that counts for two projects would count
// twice in a sum of theirs. With no such project, or several, or one whose measures are off, it
// holds none.
func (s *Store) DeploymentFailures(ctx context.Context, days Range) (DeploymentFailures, error) {
	tx, err := s.BeginSnapshot(ctx)
	if err != nil {
		return DeploymentFailures{}, fmt.Errorf("begin the read of the deployment's failure measures: %w", err)
	}
	defer tx.Rollback(ctx)
	settings, err := delivery.ListProjectSettings(ctx, tx)
	if err != nil {
		return DeploymentFailures{}, err
	}
	settings = slices.DeleteFunc(settings, func(one delivery.ProjectSettings) bool { return !one.Delivery })
	if len(settings) != 1 {
		return DeploymentFailures{}, nil
	}
	project, on := failureProjectOf(settings[0])
	if !on {
		return DeploymentFailures{}, nil
	}
	read := DeploymentFailures{since: &project.since}
	evalIDs := make([]string, len(failureDaySlugs))
	for i, slug := range failureDaySlugs {
		evalIDs[i] = EvalID(project.project, slug)
	}
	rows, err := tx.Query(ctx, `
		select `+dayColumns+` from eval_days
		where project_key = $1 and eval_id = any($2) and writer = $3 and deploy_repo = '' and repo = '' and listed
		  and day >= $4::date and day < $5::date
		order by day, eval_id, series
	`, project.project, evalIDs, writerComputation, days.From, days.To)
	if err != nil {
		return DeploymentFailures{}, fmt.Errorf("read the failure rows of %s: %w", project.project, err)
	}
	stored, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (DayRow, error) { return scanDay(row) })
	if err != nil {
		return DeploymentFailures{}, fmt.Errorf("read the failure rows of %s: %w", project.project, err)
	}
	for _, row := range stored {
		switch slugOf(project.project, row.EvalID) {
		case SlugChangeFailRate:
			read.parts.ChangeFail = append(read.parts.ChangeFail, row.DayPart)
		case SlugFailedDeployRecovery:
			read.parts.Recovery = append(read.parts.Recovery, row.DayPart)
		case SlugDeploymentReworkRate:
			read.parts.Rework = append(read.parts.Rework, row.DayPart)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return DeploymentFailures{}, fmt.Errorf("end the read of the deployment's failure measures: %w", err)
	}
	return read, nil
}

// The relations a row of the record has to a failure measure's numbers (FailureItem.Relation).
const (
	// RelationFailed is a deploy a counted failure blames: change-fail-rate's numerator.
	RelationFailed = "failed"
	// RelationRestoring is a deploy that restored a counted failure: deployment-rework-rate's
	// numerator.
	RelationRestoring = "restoring"
	// RelationDeployed is a deploy in the measure's denominator alone.
	RelationDeployed = "deployed"
	// RelationBlamed is the deploy a failure failed-deploy-recovery counts blames.
	RelationBlamed = "blamed"
	// RelationCounted is a counted failure on a deploy of the range (change-fail-rate).
	RelationCounted = "counted"
	// RelationRestored is a counted failure restored in the range (failed-deploy-recovery, timed),
	// or restored by a deploy of the range (deployment-rework-rate).
	RelationRestored = "restored"
	// RelationNotRestored is a counted failure of a deploy of the range that is not restored yet
	// (failed-deploy-recovery's not_restored).
	RelationNotRestored = "not_restored"
)

// FailureItem is one row of the record behind a failure measure's day rows of a range
// (FailureItemsIn): a deploy or a failure, its relation to the measure's numbers, the repository
// of the deploy it is counted through (the repository rows it is in), the moment it counts on, how
// many counted failures blame a deploy, and a restored failure's minutes to restore.
type FailureItem struct {
	Event    prodevents.Event
	Relation string
	Repo     string
	At       time.Time
	Failures int
	Minutes  *float64
}

// FailureItemsIn reads in q the rows of the record behind slug's day rows, one of the failure
// measures of project, on the days of days from the project's first day on, as its rows count them
// (failureRecord.day): none where its measures are off. Deploys first, in the order they finished,
// then failures:
//
//   - change-fail-rate: each deploy of the range, failed or deployed, and each counted failure
//     that blames one (counted), on its deploy's finish;
//   - failed-deploy-recovery: each counted failure restored in the range (restored, at its restore,
//     with its minutes) and each counted failure of a deploy of the range not restored yet
//     (not_restored, on its deploy's finish), and the deploys they blame (blamed);
//   - deployment-rework-rate: each deploy of the range, restoring or deployed, and each counted
//     failure a restoring one restored (restored, on that deploy's finish).
func FailureItemsIn(ctx context.Context, q delivery.Querier, project, slug string, days Range) ([]FailureItem, error) {
	settings, err := delivery.GetProjectSettings(ctx, q, project)
	if errors.Is(err, delivery.ErrNoSettings) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	failures, on := failureProjectOf(settings)
	if !on || !slices.Contains(failureDaySlugs, slug) {
		return nil, nil
	}
	projects, err := ProjectsIn(ctx, q)
	if err != nil {
		return nil, err
	}
	record, err := readFailureRecord(ctx, q, failures, projects)
	if err != nil {
		return nil, err
	}
	from := days.From
	if from.Before(failures.since) {
		from = failures.since
	}
	var deploys, counted []FailureItem
	blamed := map[string]bool{}
	blame := func(failure prodevents.Event) {
		deploy := record.deploys[record.index[*failure.DeployID]]
		if !blamed[deploy.ID] {
			blamed[deploy.ID] = true
			deploys = append(deploys, FailureItem{
				Event: deploy.Event, Relation: RelationBlamed, Repo: deploy.Repo, At: *deploy.FinishedAt, Failures: record.failed[deploy.ID],
			})
		}
	}
	inRange := func(at time.Time) bool { return !at.Before(from) && at.Before(days.To) }
	for _, deploy := range record.deploys[record.deploysFrom(from):] {
		if !deploy.FinishedAt.Before(days.To) {
			break
		}
		item := FailureItem{Event: deploy.Event, Relation: RelationDeployed, Repo: deploy.Repo, At: *deploy.FinishedAt, Failures: record.failed[deploy.ID]}
		switch slug {
		case SlugChangeFailRate:
			if item.Failures > 0 {
				item.Relation = RelationFailed
			}
			deploys = append(deploys, item)
		case SlugDeploymentReworkRate:
			if record.restores[deploy.ID] {
				item.Relation = RelationRestoring
			}
			deploys = append(deploys, item)
		}
	}
	for _, failure := range record.counted {
		deploy := record.deploys[record.index[*failure.DeployID]]
		switch slug {
		case SlugChangeFailRate:
			if inRange(*deploy.FinishedAt) {
				counted = append(counted, FailureItem{Event: failure, Relation: RelationCounted, Repo: deploy.Repo, At: *deploy.FinishedAt})
			}
		case SlugFailedDeployRecovery:
			switch {
			case failure.RestoredAt != nil && inRange(*failure.RestoredAt):
				one := measures.RestoredFailure{DeployFinishedAt: *deploy.FinishedAt, RestoredAt: *failure.RestoredAt}
				minutes := measures.RecoveryDay(measures.Midnight(*failure.RestoredAt), []measures.RestoredFailure{one}, 0)[0].Samples[0]
				counted = append(counted, FailureItem{Event: failure, Relation: RelationRestored, Repo: deploy.Repo, At: *failure.RestoredAt, Minutes: &minutes})
				blame(failure)
			case failure.RestoredAt == nil && inRange(*deploy.FinishedAt):
				counted = append(counted, FailureItem{Event: failure, Relation: RelationNotRestored, Repo: deploy.Repo, At: *deploy.FinishedAt})
				blame(failure)
			}
		case SlugDeploymentReworkRate:
			if by := failure.RestoredByDeploy; by != nil {
				if at, held := record.index[*by]; held && inRange(*record.deploys[at].FinishedAt) {
					restoring := record.deploys[at]
					counted = append(counted, FailureItem{Event: failure, Relation: RelationRestored, Repo: restoring.Repo, At: *restoring.FinishedAt})
				}
			}
		}
	}
	slices.SortStableFunc(deploys, func(a, b FailureItem) int { return a.At.Compare(b.At) })
	return slices.Concat(deploys, counted), nil
}
