package evals

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/delivery"
	"github.com/sjawhar/envoy/internal/dispatch/delivery/measures"
	"github.com/sjawhar/envoy/internal/dispatch/prodevents"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// The delivery and CI evals' day rows (dispatch://INFRA-2916, R12, R15, R16 and D1). One UTC day's
// delivery population is read once (readDeliveryDay), by the rules the delivery routes read their
// window by: delivery.ReadDeploys and ReadMergedRows' two reads (ListPullRequestsInWindow and
// ReadApplyRows), whose answer the routes' one read of a window, delivery.ReadWindowRows, gives
// too (TestAWindowsReadAnswersWhatItsTwoReadsAnswer holds it), and ReadCIRows, which the routes
// read with; each row of it is attributed to the projects it counts for; each project's rows are
// built into day parts by the measures package's day builders, the builders the delivery routes
// build their window's parts with (DeliveryDay.Rows); and the refresher writes the parts as that
// project's rows of the day (refresh.writeDelivery, through writeDeliveryDay), which the eval
// routes and the delivery routes fold through the same folds.
//
// A day holds the population pull requests merged that day (merge-to-production,
// first-commit-to-production, open-to-production, pr-open-to-merge and rework-share), the deploy
// runs on main started that day (deploy-frequency and deploy-run-success), the PR-checks
// workflow's pull_request runs started that day with every job (pr-ci, and its unlisted
// `job:<name>` series), and the deploy workflow's push runs on main started that day with the
// jobs the deploy cards follow (production-deploy, staging-e2e and staging-deploy, the last two
// where the settings name their jobs). A pull request's deploy is its first shipping apply
// (delivery.ContainingRun) among the deploy runs GitHub created from its merge on, the deploy it
// has on the delivery routes. A deploy's shipped set is every population pull request whose first
// shipping apply it is, whatever day that pull request merged (delivery.Deploys.ShippedBy): the
// deploy's own, which counts it as a deploy with pull requests (with_prs), as the delivery routes
// count it, and names the projects it counts for. Two kinds of row are not settled when their
// day is first built (measures.go names them): a pull request with no deploy yet and a deploy run
// still running; the day is built again when the run that settles them lands. change-fail-rate and
// failed-deploy-recovery get no rows: no failure source exists, so they read "no data".
//
// Attribution, decided in one place (attribution), by R15 of dispatch://INFRA-2916 and R12 of
// dispatch://INFRA-978: a pull request counts for the project of the issue it names
// (issues.project_key). A PR-checks run counts where its pull request counts, and so does each of
// its jobs. A deploy run counts for every project the pull requests it ships count for through an
// issue, once in each however many of a project's it ships, and so does each job of it a deploy
// card follows, its push run on main being that deploy. A deploy run that ships no pull request
// counts for the delivery-on project whose deploy source reads its repository, and for no other,
// whatever the default repositories say (INFRA-978 R12, which replaces R15's rule for such a
// deploy), and a posted deploy for the project of its route (postedDayAs). What no issue attributes
// otherwise counts for its repository's owner key (owners.go): a pull request that names no issue,
// or one no row of the issues table holds; a PR-checks run whose pull request is no population row,
// or names no such issue, or that names no pull request; a deploy run that ships such a pull
// request, whose owner key names as its sharers the projects that count it through an issue; and a
// push run on main that is no deploy run of the day. A project reads an owner key's rows for each
// repository it publishes as its default, so such a row counts for each project whose default
// repository is its repository, and for none where no project's is.
//
// A day row's via_repository is how many of the rows its n counts reached their owner through
// their repository rather than through an issue (its builder's n over those rows alone): every row
// of an owner key counts it, and so does a deploy run that ships no pull request, with its push
// run and that run's jobs, in its source's project's rows; no other row of a project does, so a
// project's read of its own rows and its default repository's adds up the rows it counts through a
// repository. It is never more than n, and the repositories' rows add up to the all-repositories
// row's as their n do.
//
// A project's rows of a day are, for each family of rows it holds that day (its pull requests,
// its deploy runs, its PR-checks runs, its deploy push runs), the all-repositories row (repo '')
// of every series of that family's evals, built from every row of the family counted for the
// project, and one row of each such series per repository the family's rows are in, built from
// that repository's rows alone: the pull request's repository, the run's. So every row counted is
// in one repository's rows, and the repositories' rows of a series partition its all-repositories
// row: their n, sum, num, den, counts and via_repository add up to its own, and their samples
// are its samples. A family a project holds no row of that day writes no row: the explicit zero
// rows of a series from its first input day on are the refresher's (ZeroDeliveryDay,
// ProjectDay.WithZeros).
//
// The evals that read the deploy repository are kept once for every source repository that holds
// a delivery run, each under its own source's rule, and once for each project's events repository
// (deploy_repos.go). So a day is read once for each such repository, its deploys, apply runs and
// CI runs, or its project's posted deploys, beside the day's pull requests, read once; each
// repository's read builds the rows of those evals, keyed by it, and the items they count
// (deliveryRead.days[1:]), and the pull requests alone build the rows of pr-open-to-merge and
// rework-share, which read no deploy, and the pull requests they count, under no deploy repository
// (deliveryRead.days[0]).

// The slugs of the delivery and CI evals (dispatch://INFRA-2916/artifact/plan-md, "The seeded
// catalogue"): an eval's id is EvalID(project, slug).
const (
	SlugDeployFrequency         = "deploy-frequency"
	SlugDeployRunSuccess        = "deploy-run-success"
	SlugMergeToProduction       = "merge-to-production"
	SlugFirstCommitToProduction = "first-commit-to-production"
	SlugOpenToProduction        = "open-to-production"
	SlugPROpenToMerge           = "pr-open-to-merge"
	SlugReworkShare             = "rework-share"
	SlugChangeFailRate          = "change-fail-rate"
	SlugFailedDeployRecovery    = "failed-deploy-recovery"
	SlugPRCI                    = "pr-ci"
	SlugStagingE2E              = "staging-e2e"
	SlugStagingDeploy           = "staging-deploy"
	SlugProductionDeploy        = "production-deploy"
	SlugUnownedP0               = "unowned-p0"
)

// SeriesUnownedP0 is unowned-p0's one series: the open P0 issues with no owner, folded by `last`.
const SeriesUnownedP0 = "issues"

// deliveryDaySlugs is every eval a delivery day's rows are of, and so every eval writeDeliveryDay
// writes the rows of that a day no longer holds as zero rows: neither change-fail-rate nor
// failed-deploy-recovery, which get no rows, nor unowned-p0, which WriteUnownedP0Day writes.
var deliveryDaySlugs = []string{
	SlugDeployFrequency, SlugDeployRunSuccess, SlugMergeToProduction, SlugFirstCommitToProduction,
	SlugOpenToProduction, SlugPROpenToMerge, SlugReworkShare, SlugPRCI, SlugStagingE2E,
	SlugStagingDeploy, SlugProductionDeploy,
}

// cardSlugs is the CI eval of each deploy card.
var cardSlugs = map[measures.DeployJobCardID]string{
	measures.CardProductionApply: SlugProductionDeploy,
	measures.CardStagingE2E:      SlugStagingE2E,
	measures.CardStagingApply:    SlugStagingDeploy,
}

// CardOf is the deploy card the CI eval slug times (production-deploy, staging-e2e and
// staging-deploy), found false for any other slug.
func CardOf(slug string) (measures.DeployJobCardID, bool) {
	for card, of := range cardSlugs {
		if of == slug {
			return card, true
		}
	}
	return "", false
}

// The families of eval_day_members a delivery day records its items under: the delivery evals'
// pull requests and deploy runs, and the CI evals' runs. Each is its evals' kind.
var deliveryDayFamilies = []string{KindDelivery, KindCI}

// PullRequestMember is a pull request's row key in eval_day_members: `pull_request:<repo>#<number>`.
func PullRequestMember(repo string, number int) string {
	return "pull_request:" + repo + "#" + strconv.Itoa(number)
}

// RunMember is a run's row key in eval_day_members: `run:<repo>/<run id>`.
func RunMember(repo string, runID int64) string {
	return "run:" + repo + "/" + strconv.FormatInt(runID, 10)
}

// counted is one project a row counts for, and whether it reached it through its repository
// rather than through an issue (via_repository).
type counted struct {
	project string
	via     bool
}

// dayPR is one pull request merged on the day, as the builders read it, with its repository, its
// row key, the project it counts for ("" for none) and the deploy run that ships it, nil while
// none does.
type dayPR struct {
	pr     measures.PR
	repo   string
	member string
	deploy *dayItem
	counted
}

// dayDeploy is one deploy run on main started on the day, as the builders read it, with its
// repository, its row key, the projects it counts for and the pull requests it ships.
type dayDeploy struct {
	run      measures.Run
	repo     string
	member   string
	projects []counted
	ships    []dayItem
}

// dayItem is an item of another day that a day's row is built from: the deploy run that ships a
// pull request merged on the day, or a pull request a deploy run started on the day ships, by its
// row key in eval_day_members and the UTC midnight of its own day (the run's start, the pull
// request's merge). The refresher rewrites that day too when the item's own row changes
// (DeliveryDay.linked).
type dayItem struct {
	member string
	day    time.Time
}

// dayCIRun is one CI run started on the day, with its row key and what it counts for: a PR-checks
// run where its pull request counts (attribution.check), a deploy push run where its deploy counts
// (attribution.pushRuns), each a project or its repository's owner key.
type dayCIRun struct {
	run      delivery.DeliveryRun
	member   string
	projects []counted
}

// DeliveryDay is one UTC day's delivery and CI rows as read with one repository as the deploy
// repository (deployRepo), under its own rule, each attributed to the projects it counts for: the
// rows of the evals that read the deploy repository, keyed by it (deploy_repos.go). With no deploy
// repository it is the day's pull requests alone, none shipped, whose rows are those of the evals
// no deploy repository moves. A project's events repository (posted) holds its posted deploys and
// the pull requests they ship alone (postedDayAs).
type DeliveryDay struct {
	// Day is the day's UTC midnight.
	Day        time.Time
	deployRepo string
	// since is the start of deployRepo's history (keptRepo.since): a day before it holds no row
	// and no item of the repository.
	since time.Time
	// posted is set for an events repository, whose rows are deploy-frequency's and
	// merge-to-production's alone (buildFamilies).
	posted  bool
	cards   []measures.DeployJobCard
	prs     []dayPR
	deploys []dayDeploy
	// checks are the PR-checks runs and checkJobs every job of each; pushes the deploy push runs
	// and pushJobs the jobs of each the cards follow.
	checks, pushes      []dayCIRun
	checkJobs, pushJobs map[int64][]delivery.DeliveryRunJob
}

// deliveryRead is one UTC day's delivery and CI rows (readDeliveryDay): the day's pull requests
// alone, with no deploy repository (days[0]), and the day as read with each kept repository as the
// deploy repository under its own rule (days[1:], in rules' order, rules[i].DeployRepo the
// repository). Its rows are theirs together: those of the evals no deploy repository moves once,
// and those of the evals that read it once for each repository. The settings rewrite's day holds
// the read of the repository it rewrites alone (rewriteSettings).
type deliveryRead struct {
	// Day is the day's UTC midnight.
	Day   time.Time
	rules []delivery.DeliverySettings
	days  []DeliveryDay
}

// repoReads is what a day reads of one repository as the deploy repository, under settings, its
// own rule, whose DeployRepo is that repository, whose history starts at since: its deploys, its
// apply runs and its CI runs; or, of an events repository (posted), the project's posted deploys
// that finished from the day on.
type repoReads struct {
	settings delivery.DeliverySettings
	since    time.Time
	deploys  delivery.Deploys
	applies  delivery.ApplyRows
	ci       delivery.CIRows
	posted   []prodevents.Event
}

// readDeliveryDay reads the delivery and CI rows of the UTC day day is on, with each of repos as
// the deploy repository under its own rule (keptRepo.rule), and attributes each to its projects.
// Its reads answer what the delivery routes' read of a window of that day answers with that
// repository as the deploy repository: the day's deploys and the rows before the day their shipped
// sets read (delivery.ReadDeploys), the pull requests merged in the day
// (delivery.ListPullRequestsInWindow) and every deploy run created from the day's start on
// (delivery.ReadApplyRows), which are delivery.ReadMergedRows' two reads and whose answer the
// routes' one read, delivery.ReadWindowRows, gives too (TestAWindowsReadAnswersWhatItsTwoReadsAnswer
// holds it); the day's CI runs (delivery.ReadCIRows); and what attributes them (readAttribution):
// the projects of the issues those pull requests name and of the pull requests the day's PR-checks
// runs are of. Of an events repository it reads the project's posted deploys that finished from the
// day on (postedDeploys). The pull requests are read once, and every other read once for each
// repository. Each deploy's shipped pull requests are delivery.Deploys.ShippedBy's, the rule the
// routes count with_prs by; the pull requests merged before the day are read for them alone. A
// repository's deploy runs created from the day's start on are taken from applyRows where it holds
// them (delivery.ApplyRows.Since): rows a walk read once from the first of its days, which every
// later day of the walk takes its part of rather than reading every run created from its own start
// to now again. The reads of the rows depend on none of each other, and are read side by side, and
// then the two reads of what attributes them, side by side too.
func (s *Store) readDeliveryDay(ctx context.Context, repos []keptRepo, day time.Time, applyRows map[string]*delivery.ApplyRows) (deliveryRead, error) {
	pool := s.database.Pool
	day = measures.Midnight(day)
	end := day.AddDate(0, 0, 1)

	var prs []delivery.DeliveryPullRequest
	reads := make([]repoReads, len(repos))
	parts := []func(context.Context) error{func(ctx context.Context) (err error) {
		if prs, err = delivery.ListPullRequestsInWindow(ctx, pool, day, end); err != nil {
			return fmt.Errorf("read the pull requests of %s: %w", day.Format(time.DateOnly), err)
		}
		return nil
	}}
	for i, kept := range repos {
		repo, one := kept.name, &reads[i]
		one.settings, one.since = kept.rule, kept.since
		one.settings.DeployRepo = repo
		if _, posted := eventsProject(repo); posted {
			parts = append(parts, func(ctx context.Context) (err error) {
				one.posted, err = postedDeploys(ctx, pool, one.settings.Project, day, postedEnd)
				return err
			})
			continue
		}
		cardNames := measures.DeployCardJobNames(measures.DeployCards(one.settings))
		parts = append(parts,
			func(ctx context.Context) (err error) {
				if one.deploys, err = delivery.ReadDeploys(ctx, pool, one.settings, day, end); err != nil {
					return fmt.Errorf("read the deploys of %s on %s: %w", repo, day.Format(time.DateOnly), err)
				}
				return nil
			},
			func(ctx context.Context) (err error) {
				if held := applyRows[repo]; held != nil {
					one.applies, err = held.Since(day)
				} else {
					one.applies, err = delivery.ReadApplyRows(ctx, pool, one.settings, day)
				}
				if err != nil {
					return fmt.Errorf("read the deploy runs of %s from %s on: %w", repo, day.Format(time.DateOnly), err)
				}
				return nil
			},
			func(ctx context.Context) (err error) {
				if one.ci, err = delivery.ReadCIRows(ctx, pool, repo, day, end, cardNames); err != nil {
					return fmt.Errorf("read the CI runs of %s on %s: %w", repo, day.Format(time.DateOnly), err)
				}
				return nil
			})
	}
	if err := store.ReadSideBySide(ctx, parts...); err != nil {
		return deliveryRead{}, err
	}
	held := [][]delivery.DeliveryPullRequest{prs}
	for _, one := range reads {
		held = append(held, one.deploys.Earlier.PullRequests)
	}
	var issueKeys []string
	for _, list := range held {
		for _, pr := range list {
			if pr.IssueKey != nil {
				issueKeys = append(issueKeys, *pr.IssueKey)
			}
		}
	}
	var checks []delivery.DeliveryRun
	for _, one := range reads {
		checks = append(checks, one.ci.Checks.Runs...)
	}
	attribute, err := readAttribution(ctx, s, issueKeys, checks)
	if err != nil {
		return deliveryRead{}, err
	}

	read := deliveryRead{Day: day, rules: keptRules(repos), days: make([]DeliveryDay, 0, len(repos)+1)}
	alone := DeliveryDay{Day: day}
	for _, pr := range prs {
		// A pull request whose opening is not known yet (a partial row) has no lead time to
		// count, as the delivery routes leave it out of theirs.
		if pr.CreatedAt != nil {
			alone.prs = append(alone.prs, attribute.pullRequest(pr, nil, nil))
		}
	}
	read.days = append(read.days, alone)
	for i, one := range reads {
		if _, posted := eventsProject(one.settings.DeployRepo); posted {
			read.days = append(read.days, postedDayAs(day, prs, one.posted, repos[i]))
			continue
		}
		read.days = append(read.days, attribute.dayAs(day, prs, one))
	}
	firstShipped(read.days[1:])
	return read, nil
}

// dayAs is the day's rows as read with one.settings.DeployRepo as the deploy repository, under its
// rule: prs, the pull requests merged that day, each shipped by its first shipping apply among that
// repository's apply runs (delivery.ContainingRun, under which a pull request of that repository
// alone ships); that repository's deploys, each with the pull requests it ships
// (delivery.Deploys.ShippedBy) and counted for the projects attribution.deploy names, the rule's
// project where it ships none; and its CI runs, the push runs by the cards of the rule; each
// attributed to its projects.
func (a attribution) dayAs(day time.Time, prs []delivery.DeliveryPullRequest, one repoReads) DeliveryDay {
	settings := one.settings
	read := DeliveryDay{Day: day, deployRepo: settings.DeployRepo, since: one.since, cards: measures.DeployCards(settings)}
	reached := map[int64]bool{}
	for _, apply := range one.deploys.Applies {
		reached[apply.RunID] = true
	}
	applies := delivery.ProductionApplies(one.applies.Runs, one.applies.Jobs, settings.Production()).Applies
	applyRuns := make(map[int64]delivery.DeliveryRun, len(one.applies.Runs))
	for _, run := range one.applies.Runs {
		applyRuns[run.RunID] = run
	}
	for _, pr := range prs {
		// A partial row, whose opening is not known yet, as readDeliveryDay leaves it out.
		if pr.CreatedAt != nil {
			read.prs = append(read.prs, a.pullRequest(pr, delivery.ContainingRun(pr, settings.DeployRepo, applies), applyRuns))
		}
	}
	shipped := one.deploys.ShippedBy(delivery.MergedRows{PullRequests: prs, ApplyRuns: one.applies.Runs, ApplyJobs: one.applies.Jobs}, settings)
	for _, run := range one.deploys.Started.Runs {
		read.deploys = append(read.deploys, a.deploy(run, reached[run.RunID], shipped[run.RunID], settings.Project))
	}
	read.checks, read.checkJobs = a.checkRuns(one.ci.Checks.Runs), one.ci.Checks.Jobs
	read.pushes, read.pushJobs = a.pushRuns(one.ci.Deploys.Runs, read.deploys), one.ci.Deploys.Jobs
	return read
}

// postedDayAs is the day's rows of repo, a project's events repository (EventsRepo), from posted,
// the project's posted deploys that finished from the day on (postedDeploys), and prs, the pull
// requests merged that day (dispatch://INFRA-978, R1 and R12): each posted deploy that finished on
// the day, a deploy that reached production, counted on its finished day for the project alone
// (deploy-frequency, with_prs where it names a pull request), and each pull request merged that day
// that a posted deploy names, shipped by the first to finish at or after its merge (ties by id),
// counted for the project alone, whose lead time is from its merge to that deploy's finish
// (merge-to-production). A posted deploy and a pull request reach the project through its route,
// not a repository: via_repository counts neither.
func postedDayAs(day time.Time, prs []delivery.DeliveryPullRequest, posted []prodevents.Event, repo keptRepo) DeliveryDay {
	end := day.AddDate(0, 0, 1)
	owner := counted{project: repo.rule.Project}
	read := DeliveryDay{Day: day, deployRepo: repo.name, since: repo.since, posted: true}
	succeeded := delivery.DeliveryRunConclusionSuccess
	for _, event := range posted {
		if event.FinishedAt.Before(day) || !event.FinishedAt.Before(end) {
			continue
		}
		named := 0
		for _, ref := range event.PullRequests {
			if _, _, ok := parsePullRequestRef(ref); ok {
				named++
			}
		}
		read.deploys = append(read.deploys, dayDeploy{
			run:  measures.Run{StartedAt: *event.FinishedAt, Conclusion: &succeeded, ReachedProduction: true, ShippedPRs: named},
			repo: event.Repo, member: PostedMember(event.ID), projects: []counted{owner},
		})
	}
	for _, pr := range prs {
		if pr.CreatedAt == nil || pr.MergedAt == nil {
			continue
		}
		var first *prodevents.Event
		for i := range posted {
			event := &posted[i]
			if event.FinishedAt.Before(*pr.MergedAt) || !namesPullRequest(*event, pr) {
				continue
			}
			if first == nil || event.FinishedAt.Before(*first.FinishedAt) || event.FinishedAt.Equal(*first.FinishedAt) && event.ID < first.ID {
				first = event
			}
		}
		if first == nil {
			continue
		}
		apply := delivery.Apply{CompletedAt: *first.FinishedAt, CreatedAt: *first.FinishedAt}
		read.prs = append(read.prs, dayPR{
			pr: measures.PROf(pr, &apply), repo: pr.Repo, member: PullRequestMember(pr.Repo, pr.Number),
			deploy:  &dayItem{member: PostedMember(first.ID), day: measures.Midnight(*first.FinishedAt)},
			counted: owner,
		})
	}
	return read
}

// namesPullRequest reports whether event, a posted deploy, names pr among the pull requests it
// ships (`owner/repo#n`, the repository's name compared without case).
func namesPullRequest(event prodevents.Event, pr delivery.DeliveryPullRequest) bool {
	return slices.ContainsFunc(event.PullRequests, func(ref string) bool {
		repo, number, ok := parsePullRequestRef(ref)
		return ok && strings.EqualFold(repo, pr.Repo) && number == pr.Number
	})
}

// firstShipped keeps each pull request's lead times to production in one deploy repository's
// rows of days, one day's reads of the deploy repositories: those of the first deploy to ship it,
// a GitHub deploy of its own repository or a posted deploy that names it, the GitHub one where
// both finish at once (and of two posted ones the events repository first by name). So a pull
// request shipped by both counts its merge-to-production once, from its merge to that deploy. A
// GitHub repository's day keeps a pull request another deploy ships first as one no deploy of it
// ships, and an events repository's day drops it.
func firstShipped(days []DeliveryDay) {
	type shipping struct {
		at     time.Time
		posted bool
		repo   string
	}
	before := func(a, b shipping) bool {
		switch {
		case !a.at.Equal(b.at):
			return a.at.Before(b.at)
		case a.posted != b.posted:
			return !a.posted
		}
		return a.repo < b.repo
	}
	first := map[string]shipping{}
	for _, day := range days {
		for _, pr := range day.prs {
			if pr.pr.DeployedAt == nil {
				continue
			}
			one := shipping{at: *pr.pr.DeployedAt, posted: day.posted, repo: day.deployRepo}
			if held, found := first[pr.member]; !found || before(one, held) {
				first[pr.member] = one
			}
		}
	}
	for i := range days {
		day := &days[i]
		kept := make([]dayPR, 0, len(day.prs))
		for _, pr := range day.prs {
			if pr.pr.DeployedAt == nil || first[pr.member].repo == day.deployRepo {
				kept = append(kept, pr)
				continue
			}
			if day.posted {
				continue
			}
			pr.pr.DeployedAt, pr.pr.DeployRunID, pr.deploy = nil, nil, nil
			kept = append(kept, pr)
		}
		day.prs = kept
	}
}

// Rows is project's rows of the day and the items they count: each of its reads' (DeliveryDay.Rows).
func (d deliveryRead) Rows(project string) ProjectDay {
	result := ProjectDay{Project: project, Day: d.Day}
	for _, day := range d.days {
		built := day.Rows(project)
		result.Rows = append(result.Rows, built.Rows...)
		result.Members = append(result.Members, built.Members...)
	}
	return result
}

// countedFor is every project any of the day's reads counts the item member for, by key.
func (d deliveryRead) countedFor(member string) []string {
	projects := map[string]bool{}
	for _, day := range d.days {
		for _, project := range day.countedFor(member) {
			projects[project] = true
		}
	}
	return slices.Sorted(maps.Keys(projects))
}

// owners is every owner key (OwnerKey) any of the day's reads counts an item for, in key order:
// the repositories' rows the day builds beside the projects'.
func (d deliveryRead) owners() []string {
	owners := map[string]bool{}
	add := func(counts []counted) {
		for _, c := range counts {
			if isOwner(c.project) {
				owners[c.project] = true
			}
		}
	}
	for _, day := range d.days {
		for _, pr := range day.prs {
			add([]counted{pr.counted})
		}
		for _, deploy := range day.deploys {
			add(deploy.projects)
		}
		for _, run := range slices.Concat(day.checks, day.pushes) {
			add(run.projects)
		}
	}
	return slices.Sorted(maps.Keys(owners))
}

// linked is the items of other days any of the day's reads builds the item member into, each
// once: each repository ships a pull request of its own alone, and its deploys are its own runs.
func (d deliveryRead) linked(member string) []dayItem {
	var items []dayItem
	seen := map[dayItem]bool{}
	for _, day := range d.days {
		for _, item := range day.linked(member) {
			if !seen[item] {
				seen[item] = true
				items = append(items, item)
			}
		}
	}
	return items
}

// attribution is what a day's rows are attributed to their projects by, and the one place the rule
// is decided (the Attribution paragraph above): the project of each issue a row names (issues),
// by its key, and the issue each PR-checks run's pull request names (pulls), by the pull request's
// row key, for each such pull request the population holds a row of that names an issue the
// issues table holds (readAttribution). readDeliveryDay attributes one day's rows by it, a span
// the rows of each day it builds (deliverySpanInput), and the refresher's rewrite of a settings
// change the rows of each day it builds (rewriteSettings).
type attribution struct {
	issues map[string]string
	pulls  map[string]string
}

// of is what pr counts for: its issue's project (issue), and, where it names no issue the issues
// table holds, its repository's owner key, through the repository.
func (a attribution) of(pr delivery.DeliveryPullRequest) counted {
	if counts := a.issue(pr.IssueKey); counts.project != "" {
		return counts
	}
	return counted{project: OwnerKey(pr.Repo, nil), via: true}
}

// issue is the project a row naming key counts for through that issue: the issue's project, and
// none ("") where key is nil or names no issue the issues table holds.
func (a attribution) issue(key *string) counted {
	if key != nil {
		if project, found := a.issues[*key]; found {
			return counted{project: project}
		}
	}
	return counted{}
}

// check is what a PR-checks run counts for: where its pull request counts (of), the project of the
// issue its population row names, or its repository's owner key where the run names no pull
// request, or one the population holds no row of or whose row names no issue the issues table
// holds.
func (a attribution) check(run delivery.DeliveryRun) []counted {
	if run.PRNumber != nil {
		if issue, named := a.pulls[PullRequestMember(run.Repo, *run.PRNumber)]; named {
			if counts := a.issue(&issue); counts.project != "" {
				return []counted{counts}
			}
		}
	}
	return []counted{{project: OwnerKey(run.Repo, nil), via: true}}
}

// pullRequest is pr, merged on the day, as the builders read it: shipped by apply (nil while none
// does), whose run, where runs holds it, is the deploy of another day that the day's rows link it
// to.
func (a attribution) pullRequest(pr delivery.DeliveryPullRequest, apply *delivery.Apply, runs map[int64]delivery.DeliveryRun) dayPR {
	entry := dayPR{
		pr: measures.PROf(pr, apply), repo: pr.Repo, member: PullRequestMember(pr.Repo, pr.Number),
		counted: a.of(pr),
	}
	if apply != nil {
		if run, found := runs[apply.RunID]; found {
			entry.deploy = &dayItem{member: RunMember(run.Repo, run.RunID), day: measures.Midnight(run.StartedAt)}
		}
	}
	return entry
}

// deploy is run, a deploy run on main started on the day, as the builders read it: whether it
// reached production, the pull requests it ships first (Deploys.ShippedBy's), and what it counts
// for: the projects its pull requests count for through an issue, once each, and, where it ships a
// pull request no issue attributes, its repository's owner key with those projects as its sharers
// (INFRA-2916 R15). A deploy run that ships no pull request counts for owner, the delivery-on
// project whose deploy source reads its repository (delivery.DeployRules), through that
// repository, and for no other project, whatever the default repositories say (INFRA-978 R12): a
// run that did not reach production ships none, and so counts for owner too; with no owner (no
// source reads the repository) it counts for the repository's owner key, as R15 has it. This is
// the one rule the delivery evals, the failure measures and their items read a deploy run's
// projects by.
func (a attribution) deploy(run delivery.DeliveryRun, reached bool, shipped []*delivery.DeliveryPullRequest, owner string) dayDeploy {
	deploy := dayDeploy{
		run: measures.Run{
			ID: run.RunID, StartedAt: run.StartedAt, Conclusion: run.Conclusion,
			ReachedProduction: reached, ShippedPRs: len(shipped),
		},
		repo: run.Repo, member: RunMember(run.Repo, run.RunID),
	}
	if len(shipped) == 0 && owner != "" {
		deploy.projects = []counted{{project: owner, via: true}}
		return deploy
	}
	for _, pr := range shipped {
		deploy.ships = append(deploy.ships, dayItem{member: PullRequestMember(pr.Repo, pr.Number), day: measures.Midnight(*pr.MergedAt)})
	}
	// Once in each project its pull requests count for through an issue; once more for its
	// repository where any of them, or none, counts through it.
	owned := len(shipped) == 0
	var sharers []string
	for _, pr := range shipped {
		counts := a.of(*pr)
		if isOwner(counts.project) {
			owned = true
			continue
		}
		if !slices.ContainsFunc(deploy.projects, func(c counted) bool { return c.project == counts.project }) {
			deploy.projects = append(deploy.projects, counts)
			sharers = append(sharers, counts.project)
		}
	}
	if owned {
		deploy.projects = append(deploy.projects, counted{project: OwnerKey(run.Repo, sharers), via: true})
	}
	return deploy
}

// checkRuns is runs, PR-checks runs started on the day, each attributed to the project its pull
// request counts for (check).
func (a attribution) checkRuns(runs []delivery.DeliveryRun) []dayCIRun {
	attributed := make([]dayCIRun, len(runs))
	for i, run := range runs {
		attributed[i] = dayCIRun{run: run, member: RunMember(run.Repo, run.RunID), projects: a.check(run)}
	}
	return attributed
}

// pushRuns is runs, the deploy workflow's push runs on main started on the day, each attributed
// to what the deploy it is counts for: deploys is every deploy run on main started on the day
// (deploy), which holds every push run on main among them, and a run it does not hold counts for
// its repository's owner key.
func (a attribution) pushRuns(runs []delivery.DeliveryRun, deploys []dayDeploy) []dayCIRun {
	projects := make(map[string][]counted, len(deploys))
	for _, deploy := range deploys {
		projects[deploy.member] = deploy.projects
	}
	attributed := make([]dayCIRun, len(runs))
	for i, run := range runs {
		member := RunMember(run.Repo, run.RunID)
		counts, held := projects[member]
		if !held {
			counts = []counted{{project: OwnerKey(run.Repo, nil), via: true}}
		}
		attributed[i] = dayCIRun{run: run, member: member, projects: counts}
	}
	return attributed
}

// readAttribution reads what attributes a day's rows (attribution): the project of each of keys,
// the issues the day's pull requests name, that an issue holds, and for each of checks, PR-checks
// runs, that names a pull request, the issue that pull request's row names and that issue's
// project, where the population holds a row of the pull request naming an issue the issues table
// holds. The two reads depend on none of each other, and are read side by side; neither is made
// where it has nothing to read.
func readAttribution(ctx context.Context, s *Store, keys []string, checks []delivery.DeliveryRun) (attribution, error) {
	attribute := attribution{issues: make(map[string]string, len(keys)), pulls: map[string]string{}}
	members, repos, numbers := []string{}, []string{}, []int32{}
	named := map[string]bool{}
	for _, run := range checks {
		if run.PRNumber == nil {
			continue
		}
		member := PullRequestMember(run.Repo, *run.PRNumber)
		if named[member] {
			continue
		}
		named[member] = true
		members, repos, numbers = append(members, member), append(repos, run.Repo), append(numbers, int32(*run.PRNumber))
	}
	pullProjects := map[string]string{}
	var reads []func(context.Context) error
	if len(keys) > 0 {
		reads = append(reads, func(ctx context.Context) error {
			rows, err := s.database.Pool.Query(ctx, `select key, project_key from issues where key = any($1)`, keys)
			if err != nil {
				return fmt.Errorf("read the projects of the pull requests' issues: %w", err)
			}
			var key, project string
			if _, err := pgx.ForEachRow(rows, []any{&key, &project}, func() error {
				attribute.issues[key] = project
				return nil
			}); err != nil {
				return fmt.Errorf("read the projects of the pull requests' issues: %w", err)
			}
			return nil
		})
	}
	if len(members) > 0 {
		reads = append(reads, func(ctx context.Context) error {
			rows, err := s.database.Pool.Query(ctx, `
				select given.member, request.issue_key, issue.project_key
				from unnest($1::text[], $2::text[], $3::int[]) as given(member, repo, number)
				join delivery_pull_requests request on request.repo = given.repo and request.number = given.number
				join issues issue on issue.key = request.issue_key
			`, members, repos, numbers)
			if err != nil {
				return fmt.Errorf("read the issues of the %d pull requests the PR-checks runs are of: %w", len(members), err)
			}
			var member, issue, project string
			if _, err := pgx.ForEachRow(rows, []any{&member, &issue, &project}, func() error {
				attribute.pulls[member], pullProjects[issue] = issue, project
				return nil
			}); err != nil {
				return fmt.Errorf("read the issues of the %d pull requests the PR-checks runs are of: %w", len(members), err)
			}
			return nil
		})
	}
	if err := store.ReadSideBySide(ctx, reads...); err != nil {
		return attribution{}, err
	}
	maps.Copy(attribute.issues, pullProjects)
	return attribute, nil
}

// countedFor is every project the day counts the item member for (a pull request, a deploy run or
// a CI run, by its row key in eval_day_members), by key: none where the day holds no such item.
func (d DeliveryDay) countedFor(member string) []string {
	projects := map[string]bool{}
	for _, pr := range d.prs {
		if pr.member == member {
			projects[pr.project] = true
		}
	}
	for _, deploy := range d.deploys {
		if deploy.member == member {
			for _, c := range deploy.projects {
				projects[c.project] = true
			}
		}
	}
	for _, run := range slices.Concat(d.checks, d.pushes) {
		if run.member == member {
			for _, c := range run.projects {
				projects[c.project] = true
			}
		}
	}
	delete(projects, "")
	return slices.Sorted(maps.Keys(projects))
}

// linked is the items of other days whose rows the day's item member is built into, each with its
// own day: for a pull request merged on the day, the deploy run that ships it, whose start day's
// rows count it among the pull requests that run ships and count the run for its projects; for a
// deploy run started on the day, every pull request it ships, whose merge day's rows hold the
// run's completion as their deploy time. A change of the item's own row moves those days' rows
// too, which is why the refresher rewrites them (refresher_work.go).
func (d DeliveryDay) linked(member string) []dayItem {
	var items []dayItem
	for _, pr := range d.prs {
		if pr.member == member && pr.deploy != nil {
			items = append(items, *pr.deploy)
		}
	}
	for _, deploy := range d.deploys {
		if deploy.member == member {
			items = append(items, deploy.ships...)
		}
	}
	return items
}

// familyRows is one project's rows of a day, or one repository's of them, by family.
type familyRows struct {
	prs     []dayPR
	deploys []dayDeploy
	// deployVia, checkVia and pushVia are, for each of deploys, checks and pushes, whether it
	// reached the project through its repository.
	deployVia         []bool
	checks, pushes    []delivery.DeliveryRun
	checkVia, pushVia []bool
}

func (f familyRows) empty() bool {
	return len(f.prs)+len(f.deploys)+len(f.checks)+len(f.pushes) == 0
}

// Rows is project's rows of the day, built by the measures package's day builders, and the items
// they count; no row and no item for a project no row of the day counts for. A read with a deploy
// repository builds the rows of the evals that read it alone, each and each item keyed by it; the
// read of the pull requests alone builds those of the evals no deploy repository moves, and its
// pull requests under none. A day before the start of the deploy repository's history (since)
// holds no row and no item of it.
func (d DeliveryDay) Rows(project string) ProjectDay {
	result := ProjectDay{Project: project, Day: d.Day}
	if d.deployRepo != "" && d.Day.Before(d.since) {
		return result
	}
	var all familyRows
	byRepo := map[string]*familyRows{}
	repoRows := func(repo string) *familyRows {
		if byRepo[repo] == nil {
			byRepo[repo] = &familyRows{}
		}
		return byRepo[repo]
	}
	members := map[Member]bool{}
	for _, pr := range d.prs {
		if pr.project != project {
			continue
		}
		all.prs = append(all.prs, pr)
		repoRows(pr.repo).prs = append(repoRows(pr.repo).prs, pr)
		member := Member{Family: KindDelivery, DeployRepo: d.deployRepo, RowKey: pr.member, Repo: pr.repo}
		if pr.deploy != nil {
			member.ShippedBy = pr.deploy.member
		}
		members[member] = true
	}
	for _, deploy := range d.deploys {
		index := slices.IndexFunc(deploy.projects, func(c counted) bool { return c.project == project })
		if index < 0 {
			continue
		}
		via := deploy.projects[index].via
		all.deploys, all.deployVia = append(all.deploys, deploy), append(all.deployVia, via)
		// A posted deploy that names no repository is in no repository's cut.
		if deploy.repo != "" {
			cut := repoRows(deploy.repo)
			cut.deploys, cut.deployVia = append(cut.deploys, deploy), append(cut.deployVia, via)
		}
		members[Member{Family: KindDelivery, DeployRepo: d.deployRepo, RowKey: deploy.member, Repo: deploy.repo}] = true
	}
	for _, family := range []struct {
		runs []dayCIRun
		into func(rows *familyRows, run delivery.DeliveryRun, via bool)
	}{
		{d.checks, func(rows *familyRows, run delivery.DeliveryRun, via bool) {
			rows.checks, rows.checkVia = append(rows.checks, run), append(rows.checkVia, via)
		}},
		{d.pushes, func(rows *familyRows, run delivery.DeliveryRun, via bool) {
			rows.pushes, rows.pushVia = append(rows.pushes, run), append(rows.pushVia, via)
		}},
	} {
		for _, run := range family.runs {
			index := slices.IndexFunc(run.projects, func(c counted) bool { return c.project == project })
			if index < 0 {
				continue
			}
			via := run.projects[index].via
			family.into(&all, run.run, via)
			family.into(repoRows(run.run.Repo), run.run, via)
			members[Member{Family: KindCI, DeployRepo: d.deployRepo, RowKey: run.member, Repo: run.run.Repo}] = true
		}
	}
	if all.empty() {
		return result
	}
	result.Rows = d.build(project, "", all)
	for _, repo := range slices.Sorted(maps.Keys(byRepo)) {
		result.Rows = append(result.Rows, d.build(project, repo, *byRepo[repo])...)
	}
	result.Members = slices.SortedFunc(maps.Keys(members), func(a, b Member) int {
		return cmp.Or(cmp.Compare(a.Family, b.Family), cmp.Compare(a.RowKey, b.RowKey))
	})
	return result
}

// ZeroDeliveryDay is project's all-repositories row of every delivery and CI series on day as the
// day builders build a day with no row (n 0, a zero sum, numerator and denominator, no sample, and
// zero counts): the explicit zero rows the refresher writes for a series from its first input day
// on, on a day with no input (ProjectDay.WithZeros). Those of the evals no deploy repository moves
// are one row each, and those of the evals that read it one row for each of rules, keyed by its
// DeployRepo (deploy_repos.go), under that rule, whose deploy cards decide which CI evals have one;
// an events repository's are deploy-frequency's and merge-to-production's alone (postedDayAs).
// pr-ci's job series have none: a job with no result that day has no part.
func ZeroDeliveryDay(project string, day time.Time, rules []delivery.DeliverySettings) ProjectDay {
	zero := ProjectDay{Project: project, Day: measures.Midnight(day)}
	zero.Rows = DeliveryDay{Day: zero.Day}.buildFamilies(project, "", familyRows{}, true)
	for _, rule := range rules {
		_, posted := eventsProject(rule.DeployRepo)
		d := DeliveryDay{Day: zero.Day, deployRepo: rule.DeployRepo, posted: posted, cards: measures.DeployCards(rule)}
		zero.Rows = append(zero.Rows, d.buildFamilies(project, "", familyRows{}, true)...)
	}
	return zero
}

// build is the rows of project's cut repo ("" for all repositories) from rows, the families rows
// holds alone.
func (d DeliveryDay) build(project, repo string, rows familyRows) []DayRow {
	return d.buildFamilies(project, repo, rows, false)
}

// buildFamilies is build, and with every set, every family's rows whether rows holds that family
// or not (ZeroDeliveryDay's). Each part's via_repository is the n the same builder counts over the
// family's rows that reached the project through their repository alone. A day read with a deploy
// repository builds the rows of the evals that read it, keyed by it, and the read of the pull
// requests alone those of the evals no deploy repository moves (readsDeployRepo): a builder whose
// evals the read does not keep is not run. An events repository's day (posted) builds
// deploy-frequency and merge-to-production alone: a posted deploy counts in those two
// (dispatch://INFRA-978, R1), and has no run whose success, jobs or checks another eval reads.
func (d DeliveryDay) buildFamilies(project, repo string, rows familyRows, every bool) []DayRow {
	var built []DayRow
	keyed := d.deployRepo != ""
	add := func(slug string, parts, viaParts []measures.DayPart) {
		via := make(map[string]int, len(viaParts))
		for _, part := range viaParts {
			via[part.Series] = part.N
		}
		for _, part := range parts {
			part.ViaRepository = via[part.Series]
			built = append(built, DayRow{
				ProjectKey: project, EvalID: EvalID(project, slug), DeployRepo: d.deployRepo, Repo: repo,
				Listed: !strings.HasPrefix(part.Series, measures.JobSeriesPrefix), DayPart: part,
			})
		}
	}
	if every || len(rows.prs) > 0 {
		var prs, viaPRs []measures.PR
		for _, pr := range rows.prs {
			prs = append(prs, pr.pr)
			if pr.via {
				viaPRs = append(viaPRs, pr.pr)
			}
		}
		for _, builder := range []struct {
			slug  string
			build func(time.Time, []measures.PR) []measures.DayPart
		}{
			{SlugMergeToProduction, measures.MergeToProductionDay},
			{SlugFirstCommitToProduction, measures.FirstCommitToProductionDay},
			{SlugOpenToProduction, measures.OpenToProductionDay},
			{SlugPROpenToMerge, measures.OpenToMergeDay},
			{SlugReworkShare, measures.ReworkShareDay},
		} {
			if readsDeployRepo(builder.slug) == keyed && (!d.posted || builder.slug == SlugMergeToProduction) {
				add(builder.slug, builder.build(d.Day, prs), builder.build(d.Day, viaPRs))
			}
		}
	}
	if keyed && (every || len(rows.deploys) > 0) {
		var runs, viaRuns []measures.Run
		for i, deploy := range rows.deploys {
			runs = append(runs, deploy.run)
			if rows.deployVia[i] {
				viaRuns = append(viaRuns, deploy.run)
			}
		}
		add(SlugDeployFrequency, measures.DeployFrequencyDay(d.Day, runs), measures.DeployFrequencyDay(d.Day, viaRuns))
		if !d.posted {
			add(SlugDeployRunSuccess, measures.DeployRunSuccessDay(d.Day, runs), measures.DeployRunSuccessDay(d.Day, viaRuns))
		}
	}
	if d.posted {
		return built
	}
	if keyed && (every || len(rows.checks) > 0) {
		parts := measures.PRChecksDay(d.Day, rows.checks, d.checkJobs)
		add(SlugPRCI, parts, measures.PRChecksDay(d.Day, viaOnly(rows.checks, rows.checkVia), d.checkJobs))
	}
	if keyed && (every || len(rows.pushes) > 0) {
		parts := measures.DeployJobsDay(d.Day, rows.pushes, d.pushJobs, d.cards)
		viaParts := measures.DeployJobsDay(d.Day, viaOnly(rows.pushes, rows.pushVia), d.pushJobs, d.cards)
		for _, card := range d.cards {
			add(cardSlugs[card.CardID], parts[card.CardID], viaParts[card.CardID])
		}
	}
	return built
}

// viaOnly is the runs of runs whose via is set, those that reached the project through their
// repository, in their order.
func viaOnly(runs []delivery.DeliveryRun, via []bool) []delivery.DeliveryRun {
	var kept []delivery.DeliveryRun
	for i, run := range runs {
		if via[i] {
			kept = append(kept, run)
		}
	}
	return kept
}

// writeDeliveryDay writes day as its project's delivery and CI rows of its day, in one
// transaction: each row with `insert … on conflict … do update … where (…) is distinct from (…)`,
// so a row whose every value is what it holds is left as it is (and logs no change, migration
// 20261117100014), recording builtBy, the running build, as the build that wrote it; then it
// deletes each of the project's rows of that day of the delivery and CI evals (deliveryDaySlugs)
// that day does not hold, such as a repository whose last row moved to another day or project
// (deleteStaleDays). It records day's members in eval_day_members on its day, and deletes the
// members of the project's delivery and CI families recorded on that day that day does not hold,
// so each item's row there says where it was last counted; and where figures is set, as the
// refresher's write of a day outside a walk sets it, it folds again the stored figures of the
// day where its rows moved, where builtBy writes them (figures.go). It moves no walk: a page
// whose changes move what other days hold writes those days with it, in its one commit
// (span.go). A row of another project, another day or another eval, and an item of another
// family, is refused before anything is written.
func (s *Store) writeDeliveryDay(ctx context.Context, day ProjectDay, builtBy string, figures bool) error {
	return s.writeProjectDay(ctx, deliveryFamily, day, builtBy, figures)
}

// WriteUnownedP0Day writes project's unowned-p0 row of the UTC day day is on: the count of its
// open P0 issues with no owner now (delivery.CountUnownedP0, the rule delivery.ListUnownedP0 lists
// by, read through the issues_open_p0 index) as the row's one value and its last_value, last_at
// the moment the count last moved, so the day's row is the count at its last refresh and the
// `last` fold of a window reads its newest day's. A write that counts what the row holds keeps
// the row's time, so it rewrites nothing (built_by aside) and logs no change: the refresher writes
// the row on every change that can move the count, most of which move nothing. builtBy is the
// running build. Its change row waits in the change log's outbox, so the transaction takes no
// change log lock, and is moved into the log once it has committed (drainChanges), as every
// writer of the day rows does; and its transaction folds again the stored figures of the day where
// its row moved and builtBy writes them (figures.go).
func (s *Store) WriteUnownedP0Day(ctx context.Context, project string, day time.Time, builtBy string) error {
	unowned, err := delivery.CountUnownedP0(ctx, s.database.Pool, project)
	if err != nil {
		return fmt.Errorf("count the unowned P0 issues of %s: %w", project, err)
	}
	tx, err := s.database.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin the unowned-p0 row of %s: %w", project, err)
	}
	defer tx.Rollback(ctx)
	evalID, date := EvalID(project, SlugUnownedP0), measures.Midnight(day)
	var now time.Time
	var stored *float64
	var storedAt *time.Time
	if err := tx.QueryRow(ctx, `
		select now(), stored.last_value, stored.last_at
		from (select 1) as one
		left join eval_days stored on stored.project_key = $1 and stored.eval_id = $2 and stored.writer = $5
			and stored.series = $3 and stored.repo = '' and stored.day = $4
	`, project, evalID, SeriesUnownedP0, date, writerComputation).Scan(&now, &stored, &storedAt); err != nil {
		return fmt.Errorf("read the unowned-p0 row of %s: %w", project, err)
	}
	count, at := float64(unowned), now.UTC()
	if stored != nil && *stored == count && storedAt != nil {
		at = storedAt.UTC()
	}
	row := DayRow{
		ProjectKey: project, EvalID: evalID, Listed: true, DayPart: measures.DayPart{
			Series: SeriesUnownedP0, Day: date, N: 1, Sum: &count, Max: &count, LastValue: &count, LastAt: &at,
		},
	}
	if err := upsertDays(ctx, tx, []DayRow{row}, builtBy); err != nil {
		return err
	}
	if err := foldFiguresInTx(ctx, tx, builtBy, project, map[string]daySpan{evalID: {first: date, last: date}}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit the unowned-p0 row of %s: %w", project, err)
	}
	s.drainChanges(ctx)
	return nil
}
