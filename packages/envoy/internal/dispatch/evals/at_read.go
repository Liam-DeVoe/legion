package evals

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/delivery"
	"github.com/sjawhar/envoy/internal/dispatch/delivery/measures"
)

// The read-time path of the delivery and CI evals (dispatch://INFRA-2918, D7a; R21): an eval's
// rows of each day of a read's window and compare range, built when the list is read from the
// stored single records of that day (its pull requests, its deploy runs, its CI runs and their
// jobs) by the day builders the refresher writes the eval's stored rows with (deliveryReadOf,
// DeliveryDay.Rows), under the rules the refresher keeps a series' first input day and its zero
// rows by (dropUnshipped, zerosFrom, ProjectDay.WithZeros), so the fold of them the stored rows are
// folded with (SeriesValuesOf) answers what the stored rows answer. A read takes it where it is
// narrowed by what no stored row is kept by: the delivery timeline's facets and its search, which
// select pull requests (Narrowing), so such a read builds the evals that count pull requests this
// way (CountsPullRequests) and states every other eval from its stored rows.
//
// unowned-p0 has no read-time path: its row is the count of open P0 issues with no owner at the
// moment the refresher wrote it (WriteUnownedP0Day), which no stored record of a past day holds.
// The Legion and the logged evals have none either: no filter of the Evals tab narrows them.

// atReadFamily is what a delivery day reads for an eval's rows: the records its builder builds
// them from.
type atReadFamily int

const (
	// atReadPullRequests is the day's pull requests, each shipped by its first shipping apply
	// among the deploy repository's apply runs.
	atReadPullRequests atReadFamily = iota + 1
	// atReadDeploys is the day's deploy runs on main, each with the pull requests it ships first.
	atReadDeploys
	// atReadChecks is the day's PR-checks runs, each with every job.
	atReadChecks
	// atReadPushes is the day's deploy push runs on main, with the jobs the deploy cards follow,
	// each counted for the projects of the deploy it is (atReadDeploys).
	atReadPushes
	// atReadFailures is the record of deploys and failures (production_events), whose builders
	// (measures.ChangeFailRateDay, RecoveryDay, ReworkRateDay) no delivery day runs: the refresher
	// writes no row of change-fail-rate, failed-deploy-recovery or deployment-rework-rate, and the
	// day builders this path runs build none either, so both read "no data".
	// TestANarrowedReadWithNoFacetEqualsTheStoredNumber fails the day the refresher writes a row
	// of one of them that this path does not build.
	atReadFailures
)

// atReadFamilies is the read-time path of each delivery and CI eval, by slug: every one of the
// catalogue but unowned-p0.
var atReadFamilies = map[string]atReadFamily{
	SlugMergeToProduction:       atReadPullRequests,
	SlugFirstCommitToProduction: atReadPullRequests,
	SlugOpenToProduction:        atReadPullRequests,
	SlugPROpenToMerge:           atReadPullRequests,
	SlugReworkShare:             atReadPullRequests,
	SlugDeployFrequency:         atReadDeploys,
	SlugDeployRunSuccess:        atReadDeploys,
	SlugPRCI:                    atReadChecks,
	SlugProductionDeploy:        atReadPushes,
	SlugStagingE2E:              atReadPushes,
	SlugStagingDeploy:           atReadPushes,
	SlugChangeFailRate:          atReadFailures,
	SlugFailedDeployRecovery:    atReadFailures,
	SlugDeploymentReworkRate:    atReadFailures,
}

// ComputedAtRead reports whether definition's eval has a read-time path (ComputeAtRead): a
// computed delivery or CI eval of the catalogue but unowned-p0.
func ComputedAtRead(definition Definition) bool {
	_, held := atReadFamilies[definition.Slug]
	return definition.Computed() && held
}

// CountsPullRequests reports whether definition's eval is a computed eval that counts pull
// requests: merge-to-production, first-commit-to-production, open-to-production, pr-open-to-merge
// and rework-share, the numbers the delivery timeline's facets and search narrow (api's
// measuresPRs), and the evals a Narrowing narrows.
func CountsPullRequests(definition Definition) bool {
	return definition.Computed() && atReadFamilies[definition.Slug] == atReadPullRequests
}

// AtRead is one read-time computation of evals' rows (ComputeAtRead).
type AtRead struct {
	// Entries is the evals built, each with a read-time path (ComputedAtRead); with Narrow set,
	// evals that count pull requests alone (CountsPullRequests).
	Entries []Entry
	// Firsts is each series' first day, by eval id and then by series, as FirstDaysIn reads it in
	// the same snapshot: the first input day a series' rows begin on, which decides which days
	// hold its zero rows and its lead times to production, as it decides the refresher's.
	Firsts map[string]map[string]time.Time
	// Repos is the repositories whose rows are kept, as DayWindowIn keeps them: none for each
	// project's own rows.
	Repos []string
	// Window and Compare are the ranges of whole UTC days built, Compare nil for none.
	Window  Range
	Compare *Range
	// Narrow, where set, narrows the pull requests the rows count (Narrowing); nil counts every
	// one.
	Narrow Narrowing
}

// NarrowInput is what a Narrowing selects from: every pull request merged on a day a read-time
// computation builds, in ListPullRequestsInWindow's order, with the delivery settings the rows are
// built under and the deploy repository's apply runs from the first such day on, with the jobs
// each is judged by, which ship them as the rows ship them.
type NarrowInput struct {
	Settings     delivery.DeliverySettings
	PullRequests []delivery.DeliveryPullRequest
	ApplyRuns    []delivery.DeliveryRun
	ApplyJobs    map[int64][]delivery.DeliveryRunJob
}

// Narrowing answers, reading what it selects by in q (the computation's snapshot), which of
// input's pull requests the rows count: keep[i] for input.PullRequests[i].
type Narrowing func(ctx context.Context, q delivery.Querier, input NarrowInput) (keep []bool, err error)

// ComputeAtRead builds, reading in q (one read's snapshot, BeginSnapshot), the rows of read's
// evals on each day of its window and its compare range, as DayWindowIn answers the stored rows
// of those days: for each day, the day's pull requests (one read of each span of consecutive days,
// split by the day each merged), and, where an eval needs them, the day's deploys and CI runs, each
// read as the refresher's read of a day reads them (readDeliveryDay), with the deploy repository's
// apply runs read once from the first day on (delivery.ApplyRows.Since), as the refresher's walk
// reads them; attributed to their projects by one read of what attributes every day's rows
// (readAttribution); built into each project's rows of the day by the refresher's day builders
// (deliveryReadOf, deliveryRead.Rows); less each lead time to production that counts no pull
// request before its first input day (dropUnshipped); and with the zero row of each series the day
// holds no input of from its first input day on, on each day the stored rows are whole through
// (eval_days_serving), as the refresher writes them (zerosFrom). Each row kept is of an eval read
// names, of the deploy repository its read reads (DeployRepoOf, under the delivery settings the
// rows are built under, BuiltSettingsIn), listed, and of one of read.Repos (each project's own row
// where it names none), answered once for each range whose days hold it.
//
// With read.Narrow set, the pull requests are narrowed to those it keeps before anything is built,
// and only the evals that count pull requests are built: a deploy counts what it ships whatever a
// filter keeps. A deployment with no delivery settings has no delivery or CI row, as the refresher
// writes none, and answers none.
func ComputeAtRead(ctx context.Context, q delivery.Querier, read AtRead) ([]WindowRow, error) {
	if len(read.Entries) == 0 {
		return nil, nil
	}
	families := map[atReadFamily]bool{}
	keyed := false
	for _, entry := range read.Entries {
		family, held := atReadFamilies[entry.Definition.Slug]
		switch {
		case !entry.Definition.Computed() || !held:
			return nil, fmt.Errorf("evals: %s has no read-time path", entry.Definition.EvalID)
		case read.Narrow != nil && family != atReadPullRequests:
			return nil, fmt.Errorf("evals: %s counts no pull request, and a narrowed read builds only the evals that do", entry.Definition.EvalID)
		}
		families[family] = true
		keyed = keyed || KeyedByDeployRepo(entry.Definition)
	}
	settings, err := BuiltSettingsIn(ctx, q)
	if err != nil || settings == nil {
		return nil, err
	}
	ranges := []Range{read.Window}
	if read.Compare != nil {
		ranges = append(ranges, *read.Compare)
	}
	days := atReadDays(ranges)
	if len(days) == 0 {
		return nil, nil
	}
	prs, err := atReadPullRequestsOf(ctx, q, days)
	if err != nil {
		return nil, err
	}

	var kept []keptRepo
	if keyed && settings.DeployRepo != "" {
		repos, err := readDeployRepos(ctx, q)
		if err != nil {
			return nil, err
		}
		if index := slices.IndexFunc(repos, func(repo keptRepo) bool { return repo.name == settings.DeployRepo }); index >= 0 {
			kept = repos[index : index+1]
		}
	}
	needDeploys := families[atReadDeploys] || families[atReadPushes]
	needCI := families[atReadChecks] || families[atReadPushes]
	// The apply runs ship the pull requests of the evals that read the deploy repository and say
	// which a deploy ships; a narrowing reads them too, since a pull request's deploy is among what
	// it selects by (the timeline's deployed facet).
	var applies delivery.ApplyRows
	if settings.DeployRepo != "" && (read.Narrow != nil || len(kept) > 0 && (families[atReadPullRequests] || needDeploys)) {
		if applies, err = delivery.ReadApplyRows(ctx, q, *settings, days[0]); err != nil {
			return nil, fmt.Errorf("read the deploy runs of %s from %s on: %w", settings.DeployRepo, days[0].Format(time.DateOnly), err)
		}
	}
	if read.Narrow != nil {
		if prs, err = narrowedPullRequests(ctx, q, read.Narrow, *settings, applies, days, prs); err != nil {
			return nil, err
		}
	}

	cards := measures.DeployCards(*settings)
	cardNames := measures.DeployCardJobNames(cards)
	reads := make([][]repoReads, len(days))
	var issueKeys []string
	var checks []delivery.DeliveryRun
	for i, day := range days {
		for _, pr := range prs[i] {
			if pr.IssueKey != nil {
				issueKeys = append(issueKeys, *pr.IssueKey)
			}
		}
		if len(kept) == 0 {
			continue
		}
		one := repoReads{settings: *settings, since: kept[0].since}
		if one.applies, err = applies.Since(day); err != nil {
			return nil, err
		}
		end := day.AddDate(0, 0, 1)
		if needDeploys {
			if one.deploys, err = delivery.ReadDeploys(ctx, q, *settings, day, end); err != nil {
				return nil, fmt.Errorf("read the deploys of %s on %s: %w", settings.DeployRepo, day.Format(time.DateOnly), err)
			}
			for _, pr := range one.deploys.Earlier.PullRequests {
				if pr.IssueKey != nil {
					issueKeys = append(issueKeys, *pr.IssueKey)
				}
			}
		}
		if needCI {
			if one.ci, err = delivery.ReadCIRows(ctx, q, settings.DeployRepo, day, end, cardNames); err != nil {
				return nil, fmt.Errorf("read the CI runs of %s on %s: %w", settings.DeployRepo, day.Format(time.DateOnly), err)
			}
			checks = append(checks, one.ci.Checks.Runs...)
		}
		reads[i] = []repoReads{one}
	}
	attribute, err := readAttribution(ctx, q, issueKeys, checks)
	if err != nil {
		return nil, err
	}
	next, err := deliveryServedIn(ctx, q)
	if err != nil {
		return nil, err
	}

	deployRepoOf := make(map[string]string, len(read.Entries))
	var projects []string
	firsts := map[string]map[evalKey]time.Time{}
	for _, entry := range read.Entries {
		definition := entry.Definition
		key := evalKey{eval: definition.EvalID, deployRepo: DeployRepoOf(definition, settings.DeployRepo)}
		deployRepoOf[key.eval] = key.deployRepo
		if firsts[definition.ProjectKey] == nil {
			projects = append(projects, definition.ProjectKey)
			firsts[definition.ProjectKey] = map[evalKey]time.Time{}
		}
		for _, day := range read.Firsts[key.eval] {
			if first, held := firsts[definition.ProjectKey][key]; !held || day.Before(first) {
				firsts[definition.ProjectKey][key] = day
			}
		}
	}
	repos := read.Repos
	if len(repos) == 0 {
		repos = []string{""}
	}
	rows := []WindowRow{}
	for i, day := range days {
		built := deliveryReadOf(day, cards, kept, prs[i], reads[i], attribute)
		for _, project := range projects {
			first := firsts[project]
			own := ProjectDay{Project: project, Day: day, Rows: dropUnshipped(built.Rows(project).Rows, first, day)}
			if next != nil && day.Before(*next) {
				own = own.WithZeros(zerosFrom(ZeroDeliveryDay(*settings, project, day, built.repos).Rows, first, day))
			}
			for _, row := range own.Rows {
				if deployRepo, held := deployRepoOf[row.EvalID]; !held || row.DeployRepo != deployRepo || !row.Listed || !slices.Contains(repos, row.Repo) {
					continue
				}
				for r, of := range ranges {
					if day.Before(of.From) || !day.Before(of.To) {
						continue
					}
					in := RangeWindow
					if r > 0 {
						in = RangeCompare
					}
					rows = append(rows, WindowRow{Range: in, EvalID: row.EvalID, Part: row.DayPart})
				}
			}
		}
	}
	return rows, nil
}

// atReadDays is each UTC day of ranges, ranges of whole UTC days, once, oldest first.
func atReadDays(ranges []Range) []time.Time {
	var days []time.Time
	for _, of := range ranges {
		for day := of.From; day.Before(of.To); day = day.AddDate(0, 0, 1) {
			days = append(days, day)
		}
	}
	slices.SortFunc(days, time.Time.Compare)
	return slices.CompactFunc(days, time.Time.Equal)
}

// atReadPullRequestsOf reads in q the pull requests merged on each of days (consecutive UTC days,
// oldest first), by day, each day's in ListPullRequestsInWindow's order: one read of each span of
// consecutive days, split by the day each merged, which answers what a read of each day answers.
func atReadPullRequestsOf(ctx context.Context, q delivery.Querier, days []time.Time) ([][]delivery.DeliveryPullRequest, error) {
	byDay := make([][]delivery.DeliveryPullRequest, len(days))
	for start := 0; start < len(days); {
		end := start + 1
		for end < len(days) && days[end].Equal(days[end-1].AddDate(0, 0, 1)) {
			end++
		}
		from, to := days[start], days[end-1].AddDate(0, 0, 1)
		read, err := delivery.ListPullRequestsInWindow(ctx, q, from, to)
		if err != nil {
			return nil, fmt.Errorf("read the pull requests merged from %s to %s: %w", from.Format(time.DateOnly), to.Format(time.DateOnly), err)
		}
		at := start
		for _, pr := range read {
			merged := measures.Midnight(*pr.MergedAt)
			for !days[at].Equal(merged) {
				at++
			}
			byDay[at] = append(byDay[at], pr)
		}
		start = end
	}
	return byDay, nil
}

// narrowedPullRequests is prs, each day's pull requests, with only those narrow keeps, which it is
// given in day order with the settings and the apply runs the rows ship them by (NarrowInput).
func narrowedPullRequests(ctx context.Context, q delivery.Querier, narrow Narrowing, settings delivery.DeliverySettings, applies delivery.ApplyRows,
	days []time.Time, prs [][]delivery.DeliveryPullRequest) ([][]delivery.DeliveryPullRequest, error) {
	input := NarrowInput{Settings: settings, PullRequests: slices.Concat(prs...), ApplyRuns: applies.Runs, ApplyJobs: applies.Jobs}
	keep, err := narrow(ctx, q, input)
	if err != nil {
		return nil, err
	}
	if len(keep) != len(input.PullRequests) {
		return nil, fmt.Errorf("evals: a narrowing kept %d answers of %d pull requests", len(keep), len(input.PullRequests))
	}
	narrowed := make([][]delivery.DeliveryPullRequest, len(days))
	at := 0
	for i := range days {
		for _, pr := range prs[i] {
			if keep[at] {
				narrowed[i] = append(narrowed[i], pr)
			}
			at++
		}
	}
	return narrowed, nil
}

// deliveryServedIn is, read in q, the first day the stored delivery and CI rows are not whole
// through (eval_days_serving's record of the delivery walk, servingNext's), nil where no walk has
// built a day of them: the days before it hold their series' zero rows.
func deliveryServedIn(ctx context.Context, q delivery.Querier) (*time.Time, error) {
	var next time.Time
	err := q.QueryRow(ctx, `select next_day from eval_days_serving where family = $1 and project_key = ''`, walkDelivery).Scan(&next)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("read what the delivery day rows are served through: %w", err)
	}
	return new(measures.Midnight(next)), nil
}
