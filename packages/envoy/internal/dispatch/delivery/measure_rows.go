package delivery

import (
	"context"
	"fmt"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// The rows the delivery and CI measures count, read by one function for every reader that counts
// them: the delivery routes (api's readDeliveryRows and readPipelinePopulation) read their
// window's (ListPullRequestsInWindow, and ReadWindowRuns of each source repository), and the eval
// registry's day rows (the refresher's read of a day, evals'
// readDeliveryDay) one UTC day's (ListPullRequestsInWindow, and ReadApplyRows and ReadDeploys of
// each repository the rows are kept for as the deploy repository), so a route and a day row count
// the same rows by the same rules, and a deploy's shipped pull requests by the same rule
// (Deploys.ShippedBy). Every read of runs takes one source repository's rule (a DeliverySettings of
// DeployRules, dispatch://INFRA-978) and reads that repository's runs by it alone, so a reader of
// several repositories reads each under its own rule.
//
// The reads an answer makes from the evals' day rows (api's evals items) take q, the pool or a
// read-only transaction whose one snapshot the answer reads (evals.Store.BeginSnapshot). A read of
// several statements runs them side by side on the pool and one after the other in a transaction
// (ReadAll).

// ReadAll runs reads, the reads of one answer in q: on the pool side by side, each on a connection
// of its own (store.ReadSideBySide), and in a transaction one after the other, since its one
// connection runs one statement at a time and every read then reads its one snapshot. The eval
// registry's reads of a day's rows take it too (evals' readAttribution, ComputeAtRead).
func ReadAll(ctx context.Context, q Querier, reads ...func(ctx context.Context) error) error {
	if _, pooled := q.(*store.Pool); pooled {
		return store.ReadSideBySide(ctx, reads...)
	}
	for _, read := range reads {
		if err := read(ctx); err != nil {
			return err
		}
	}
	return nil
}

// MergedRows is the population pull requests merged in a window and the deploy runs that can have
// shipped them.
type MergedRows struct {
	// PullRequests is every population pull request merged in [from, to), as
	// ListPullRequestsInWindow lists them.
	PullRequests []DeliveryPullRequest
	// ApplyRuns is the deploy runs on main of the deploy repository that GitHub created at or
	// after from, oldest creation first, not cut at to: a pull request merged just before to may
	// first ship after it, so bounding the read to [from, to) would read it as waiting.
	// ReadMergedRows reads every such run (listRuns); ReadWindowRuns every one created up to the
	// bound past which no run ships a pull request merged before to first, with no bound for a
	// window ending at the present (windowApplyRunsCreated). A production job on another branch
	// ships no pull request. ApplyJobs is their jobs: the jobs a deploy is judged by
	// (Production.JudgedJobNames), which judge each run as its every job would, and with them,
	// for the timeline, each of the run's jobs that failed.
	ApplyRuns []DeliveryRun
	ApplyJobs map[int64][]DeliveryRunJob
}

// ReadMergedRows reads MergedRows for [from, to) in q: the pull requests, and every apply run
// followed by their production jobs, side by side on the pool (ReadAll).
func ReadMergedRows(ctx context.Context, q Querier, settings DeliverySettings, from, to time.Time) (MergedRows, error) {
	return readMergedRows(ctx, q, settings, from, to, nil)
}

// readMergedRows is ReadMergedRows, and with createdBefore set only the apply runs GitHub created
// before it.
func readMergedRows(ctx context.Context, q Querier, settings DeliverySettings, from, to time.Time, createdBefore *time.Time) (MergedRows, error) {
	var rows MergedRows
	err := ReadAll(ctx, q,
		func(ctx context.Context) (err error) {
			rows.PullRequests, err = ListPullRequestsInWindow(ctx, q, from, to)
			return err
		},
		func(ctx context.Context) error {
			applies, err := readApplyRows(ctx, q, settings, from, createdBefore)
			rows.ApplyRuns, rows.ApplyJobs = applies.Runs, applies.Jobs
			return err
		})
	if err != nil {
		return MergedRows{}, err
	}
	return rows, nil
}

// ApplyRows is MergedRows' apply runs and their jobs read once from a moment on (ReadApplyRows),
// for a reader of many windows that start at or after it, each of which takes its own part
// (Since): the refresher's walk over the days, whose every day's read would otherwise read every
// deploy run created from that day to now again.
type ApplyRows struct {
	from time.Time
	Runs []DeliveryRun
	Jobs map[int64][]DeliveryRunJob
}

// ReadApplyRows reads the apply runs and their production jobs (the jobs a deploy is judged by)
// of every window starting at or after from: MergedRows.ApplyRuns and ApplyJobs as ReadMergedRows
// reads them for a window from from.
func ReadApplyRows(ctx context.Context, q Querier, settings DeliverySettings, from time.Time) (ApplyRows, error) {
	return readApplyRows(ctx, q, settings, from, nil)
}

// readApplyRows is ReadApplyRows, with createdBefore as readMergedRows takes it.
func readApplyRows(ctx context.Context, q Querier, settings DeliverySettings, from time.Time, createdBefore *time.Time) (ApplyRows, error) {
	runs, err := listRuns(ctx, q, settings.DeployRepo, DeliveryRunKindDeploy, "main", from, createdBefore)
	if err != nil {
		return ApplyRows{}, err
	}
	runIDs := make([]int64, len(runs))
	for i, run := range runs {
		runIDs[i] = run.RunID
	}
	jobs, err := ListRunJobsForRuns(ctx, q, settings.DeployRepo, runIDs, settings.Production().JudgedJobNames())
	if err != nil {
		return ApplyRows{}, err
	}
	return ApplyRows{from: from, Runs: runs, Jobs: jobs}, nil
}

// ReadWindowRuns is a delivery route's read of one source repository's runs for [from, to), under
// that repository's rule (settings): MergedRows' apply runs and their jobs, with failedJobs each
// apply run's failed jobs beside its production jobs, which the timeline lists each deploy run with
// (its failed jobs and the job that failed first, FailedJobs and RootFailingJob, can be any job,
// and keep the failed ones alone), and with deploys the window's Deploys, as ReadDeploys reads
// them. The window's pull requests are every repository's (ListPullRequestsInWindow), which a route
// reading several source repositories reads once beside each repository's runs, so this reads
// none. The two lists of runs are nearly one, the runs of the window, so they are read in one
// statement (listWindowRuns) and the production jobs of both in one more, where reading each list
// and its jobs apart read the window's production jobs twice, three times the window's for the
// measures of a compare range, whose apply runs reached to the present (dispatch://INFRA-2916, R8).
// The apply runs stop at the bound past which no run ships first a pull request of the window or
// of Deploys.Earlier (windowApplyRunsCreated), so every pull request's deploy is the one
// ReadMergedRows' runs give it.
func ReadWindowRuns(ctx context.Context, pool *store.Pool, settings DeliverySettings, from, to time.Time, failedJobs, deploys bool) (RunsWithJobs, Deploys, error) {
	applies, started, err := readWindowRuns(ctx, pool, settings, from, to, failedJobs, deploys)
	if err != nil || !deploys {
		return applies, Deploys{}, err
	}
	window, err := deploysOf(ctx, pool, settings, from, started)
	if err != nil {
		return RunsWithJobs{}, Deploys{}, err
	}
	return applies, window, nil
}

// readWindowRuns reads ReadWindowRuns' runs with their jobs: the apply runs, and with deploys the
// deploys of the window, each with its production jobs, read for both lists in one statement, and
// with failedJobs the apply runs' failed jobs too, in another beside it.
func readWindowRuns(ctx context.Context, pool *store.Pool, settings DeliverySettings, from, to time.Time, failedJobs, deploys bool) (applies, started RunsWithJobs, err error) {
	production := settings.Production()
	listed, err := listWindowRuns(ctx, pool, settings.DeployRepo, production, from, to, deploys)
	if err != nil {
		return RunsWithJobs{}, RunsWithJobs{}, err
	}
	names := production.JudgedJobNames()
	var named, failed map[int64][]DeliveryRunJob
	reads := []func(ctx context.Context) error{func(ctx context.Context) (err error) {
		named, err = ListRunJobsForRuns(ctx, pool, settings.DeployRepo, listed.ids, names)
		return err
	}}
	if failedJobs {
		applyIDs := make([]int64, len(listed.apply))
		for i, run := range listed.apply {
			applyIDs[i] = run.RunID
		}
		reads = append(reads, func(ctx context.Context) (err error) {
			failed, err = listRunJobsFailedForRuns(ctx, pool, settings.DeployRepo, applyIDs, names)
			return err
		})
	}
	if err := store.ReadSideBySide(ctx, reads...); err != nil {
		return RunsWithJobs{}, RunsWithJobs{}, err
	}
	return RunsWithJobs{Runs: listed.apply, Jobs: namedAndFailedJobs(listed.apply, named, failed)},
		RunsWithJobs{Runs: listed.started, Jobs: namedAndFailedJobs(listed.started, named, nil)}, nil
}

// Since is the apply rows of a window from from, taken from a, read from from or earlier: the runs
// GitHub created at or after from, oldest creation first, and their jobs, the rows ReadApplyRows
// reads itself from from.
func (a ApplyRows) Since(from time.Time) (ApplyRows, error) {
	if from.Before(a.from) {
		return ApplyRows{}, fmt.Errorf("apply rows read from %s hold no run of a window from %s", a.from.Format(time.RFC3339), from.Format(time.RFC3339))
	}
	rows := ApplyRows{from: from, Runs: []DeliveryRun{}, Jobs: map[int64][]DeliveryRunJob{}}
	for _, run := range a.Runs {
		if run.Created().Before(from) {
			continue
		}
		rows.Runs = append(rows.Runs, run)
		if jobs, held := a.Jobs[run.RunID]; held {
			rows.Jobs[run.RunID] = jobs
		}
	}
	return rows, nil
}

// HistoryRows is what every day of the eval registry's delivery and CI day rows reads but its CI
// runs, read once over the whole history: every population pull request merged before a moment,
// in ListPullRequestsInWindow's order, and, for each repository asked for as the deploy
// repository, every deploy run on main of it, whatever its event, oldest start first and those
// started in one instant by run id (listRunsStartedIn's order), with its jobs of the names asked
// for, each run's in ListRunJobs' order. A day's deploys, its apply runs and its deploy cards'
// push runs are all among those runs, so a reader that builds every day at once (the day-row
// refresher's rewrite of a settings change, evals' rewriteSettings) gives each day its part, where
// a read of each day would read every deploy run created from that day on again. No limit bounds
// it, as one bounds a window's reads (MaxPullRequestsPerWindow, MaxRunsPerWindow): the whole
// history is what such a reader builds, and a limit would cut it short without a word.
type HistoryRows struct {
	PullRequests []DeliveryPullRequest
	// Deploys is each repository's deploy runs and their jobs, by the repository.
	Deploys map[string]RunsWithJobs
}

// ReadHistoryRows reads HistoryRows of deployRepos: the pull requests merged before to, and each
// repository's deploy runs with their jobs named names, every read side by side.
func ReadHistoryRows(ctx context.Context, pool *store.Pool, deployRepos []string, names []string, to time.Time) (HistoryRows, error) {
	rows := HistoryRows{Deploys: make(map[string]RunsWithJobs, len(deployRepos))}
	deploys := make([]RunsWithJobs, len(deployRepos))
	reads := []func(context.Context) error{func(ctx context.Context) (err error) {
		rows.PullRequests, err = listPullRequestsMergedBefore(ctx, pool, to)
		return err
	}}
	for i, repo := range deployRepos {
		reads = append(reads,
			func(ctx context.Context) (err error) {
				deploys[i].Runs, err = listDeployRunsOnMain(ctx, pool, repo)
				return err
			},
			func(ctx context.Context) (err error) {
				deploys[i].Jobs, err = listDeployRunJobsOnMain(ctx, pool, repo, names)
				return err
			})
	}
	if err := store.ReadSideBySide(ctx, reads...); err != nil {
		return HistoryRows{}, err
	}
	for i, repo := range deployRepos {
		rows.Deploys[repo] = deploys[i]
	}
	return rows, nil
}

// Deploys is the measures' deploys of a window: every deploy run on main of the deploy repository
// started in it, whatever its event, with the jobs it is judged by (Production.JudgedJobNames), the
// deploys among them, and the rows from before the window that decide which pull requests merged
// before it those deploys ship. Whether a run reached production is decided on these runs and
// their jobs, never on the creation-time listing containment reads, so every run a window counts
// is judged.
type Deploys struct {
	Started RunsWithJobs
	// Applies is the deploys among Started (ProductionApplies), in compareApplies' order.
	Applies []Apply
	// Earlier is the pull requests merged before the window that Applies can ship, and the deploy
	// runs on main GitHub created before the window, which can ship them first: MergedRows from
	// since (ReadDeploys says what it is) up to the window's start, its apply runs those created
	// in that span alone. Empty where no pull request merged before the window can ship with one
	// of Applies.
	Earlier MergedRows
}

// ReadDeploys reads Deploys for [from, to) in q. The earlier rows start at since: the latest
// creation among the deploys that succeeded before the first of Applies completed
// (ReadLatestShipBefore; the beginning of time where none did), since a pull request merged before
// it had shipped by then and so ships with none of Applies; or the creation of one of Applies,
// where one was created earlier still, so that run is among the apply runs. Nothing earlier is
// read where the window holds no deploy, or since is not before from.
func ReadDeploys(ctx context.Context, q Querier, settings DeliverySettings, from, to time.Time) (Deploys, error) {
	started, err := listRunsWithJobsStartedIn(ctx, q, settings.DeployRepo, DeliveryRunKindDeploy, "main", "", from, to, settings.Production().JudgedJobNames())
	if err != nil {
		return Deploys{}, err
	}
	return deploysOf(ctx, q, settings, from, started)
}

// deploysOf is the Deploys of a window from from whose deploy runs, with the jobs each is judged by,
// are started: the deploys among them, and the earlier rows ReadDeploys reads, in q.
func deploysOf(ctx context.Context, q Querier, settings DeliverySettings, from time.Time, started RunsWithJobs) (Deploys, error) {
	production := settings.Production()
	deploys := Deploys{Started: started, Applies: ProductionApplies(started.Runs, started.Jobs, production).Applies}
	if len(deploys.Applies) == 0 {
		return deploys, nil
	}
	since, err := ReadLatestShipBefore(ctx, q, settings.DeployRepo, production, deploys.Applies[0].CompletedAt)
	if err != nil {
		return Deploys{}, err
	}
	for _, apply := range deploys.Applies {
		if apply.CreatedAt.Before(since) {
			since = apply.CreatedAt
		}
	}
	if !since.Before(from) {
		return deploys, nil
	}
	if deploys.Earlier, err = readMergedRows(ctx, q, settings, since, from, &from); err != nil {
		return Deploys{}, err
	}
	return deploys, nil
}

// ShippedBy is, for each of d.Applies that ships a pull request, the pull requests it ships first
// (ContainingRun over the apply runs of window and of d.Earlier), whenever they merged: those of
// window (ReadMergedRows, or ListPullRequestsInWindow and ReadWindowRuns, of d's window) and those
// merged before it, of
// d.Earlier, each list in that order. A deploy is one with pull requests when it ships one,
// whenever the pull request merged (dispatch://INFRA-2916): the deploy-frequency measure's
// with_prs (measures.Run.ShippedPRs) is this rule on the delivery routes and on the eval day rows
// alike. The pointers are into window's and d.Earlier's lists.
func (d Deploys) ShippedBy(window MergedRows, settings DeliverySettings) map[int64][]*DeliveryPullRequest {
	production := settings.Production()
	// The two listings hold no run in common (Earlier's were created before the window, window's
	// in it or after), and a run's verdict reads its own jobs alone, so their deploys are the
	// deploys of the two together.
	applies := append(ProductionApplies(d.Earlier.ApplyRuns, d.Earlier.ApplyJobs, production).Applies,
		ProductionApplies(window.ApplyRuns, window.ApplyJobs, production).Applies...)
	deploys := make(map[int64]bool, len(d.Applies))
	for _, apply := range d.Applies {
		deploys[apply.RunID] = true
	}
	shipped := map[int64][]*DeliveryPullRequest{}
	for _, prs := range [][]DeliveryPullRequest{window.PullRequests, d.Earlier.PullRequests} {
		for i := range prs {
			if apply := ContainingRun(prs[i], settings.DeployRepo, applies); apply != nil && deploys[apply.RunID] {
				shipped[apply.RunID] = append(shipped[apply.RunID], &prs[i])
			}
		}
	}
	return shipped
}

// CIRows is the CI measures' runs of a window, each read by start over the window: the PR-checks
// workflow's pull_request runs with every job, which their job timings and failing jobs read
// whatever the names, and the deploy workflow's push runs on main with the jobs the deploy cards
// follow and no other.
type CIRows struct {
	Checks, Deploys RunsWithJobs
}

// ReadCIRows reads CIRows for [from, to) of the deploy repository in q, the two reads side by side
// on the pool (ReadAll); deployJobNames is every job name the deploy cards follow.
func ReadCIRows(ctx context.Context, q Querier, deployRepo string, from, to time.Time, deployJobNames []string) (CIRows, error) {
	var rows CIRows
	err := ReadAll(ctx, q,
		func(ctx context.Context) (err error) {
			rows.Checks, err = listRunsWithEveryJobStartedIn(ctx, q, deployRepo, DeliveryRunKindPRChecks, "", "pull_request", from, to)
			return err
		},
		func(ctx context.Context) (err error) {
			rows.Deploys, err = listRunsWithJobsStartedIn(ctx, q, deployRepo, DeliveryRunKindDeploy, "main", "push", from, to, deployJobNames)
			return err
		})
	if err != nil {
		return CIRows{}, err
	}
	return rows, nil
}

// prChecksCompletedInQuery is ReadPRChecksCompletedIn's read of the runs: the PR-checks runs of a
// window as listRunsStartedIn lists them (kind $2, event $3, started in [$4, $5)), of those the
// ones completed in [$6, $7], oldest start first and those started in one instant by run id. The
// completion is a range of delivery_runs_kind_completed_at (migration 20261123042253), (repo,
// kind, completed_at), so the read reads the runs of those hours alone, and checks the start and
// the event on them.
const prChecksCompletedInQuery = `
		select ` + RunColumns + `
		from delivery_runs
		where repo = $1 and kind = $2 and event = $3 and started_at >= $4 and started_at < $5
			and completed_at >= $6 and completed_at <= $7
		order by started_at, run_id
		limit $8`

// ReadPRChecksCompletedIn reads the PR-checks workflow's pull_request runs of the deploy repository
// in q that started in [from, to), the runs ReadCIRows reads of that window, and completed in
// [completedFrom, completedTo], both ends counted as measures.FailingJobs counts a completion, each
// with every job it ran: the runs of a window whose jobs the failing-jobs tally reads, read by their
// completion rather than as every run of the window (dispatch://INFRA-2918, D9), so the read of
// their jobs reads the jobs of a day's runs, never those of every run of an 84-day window.
// Bounded by MaxRunsPerWindow, as listRunsStartedIn is.
func ReadPRChecksCompletedIn(ctx context.Context, q Querier, deployRepo string, from, to, completedFrom, completedTo time.Time) (RunsWithJobs, error) {
	rows, err := q.Query(ctx, prChecksCompletedInQuery, deployRepo, string(DeliveryRunKindPRChecks), "pull_request",
		from, to, completedFrom, completedTo, MaxRunsPerWindow)
	if err != nil {
		return RunsWithJobs{}, err
	}
	runs, err := collectRuns(rows)
	if err != nil {
		return RunsWithJobs{}, err
	}
	runIDs := make([]int64, len(runs))
	for i, run := range runs {
		runIDs[i] = run.RunID
	}
	jobs, err := listEveryRunJobForRuns(ctx, q, deployRepo, runIDs)
	if err != nil {
		return RunsWithJobs{}, err
	}
	return RunsWithJobs{Runs: runs, Jobs: jobs}, nil
}
