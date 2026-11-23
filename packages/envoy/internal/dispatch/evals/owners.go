package evals

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/delivery"
	"github.com/sjawhar/envoy/internal/dispatch/legionfacts"
)

// owners.go is where what a repository attributes is kept (dispatch://INFRA-2916, R15, plan D1 and
// the architect's rulings of 14:08Z and 18:38Z): a pull request that names no issue the issues
// table holds, a CI run of such a pull request or of none, and a deploy run where it ships such a
// pull request or ships none, with each job of it a deploy card follows, count for each project
// whose default repository is their repository, a selection made at read time. So their day rows,
// members and owed days are written once, whatever the defaults are, under the repository's owner
// key (OwnerKey) in eval_days, eval_day_members and eval_days_owed, where a project's rows stand
// under its key (migration 20261117100006): the refresher's delivery family treats an owner key as
// one more project, its walks, pages, spans, settings rewrites and rebuilds alike, and every read of
// a project folds its own rows with those of the owner keys of the repositories it publishes as its
// default (eval_project_owners), less the ones it shares.
//
// A deploy run that ships pull requests of projects through their issues counts for those projects
// in their own rows. Where it also ships a pull request that names no issue, its repository's row
// of it names those projects as its sharers, which skip it, so a project whose default repository
// is the run's repository counts the run once, through its issue (plan D1 (iii)). One owner key
// stands for each distinct sharers set, so sharers is part of the rows' key: two deploys of one
// day with different sharers are two rows.

// ownerPrefix begins every owner key, and no project key (projects_key_check,
// ^[A-Z][A-Z0-9]{1,9}$, migration 0001): an owner key never names a project.
const ownerPrefix = ":"

// OwnerKey is the owner key of the rows repo attributes and the projects of sharers count through
// their own issues already: ':' + repo + '|' + the sorted, distinct keys of sharers joined by ','.
// repo is the repository's canonical name, as every delivery table keeps it. It is the one place
// the key is made (and parseOwner the one place it is read), beside the change trigger's reading of
// it (eval_days_change_log, migration 20261117100014). No repository name holds '|' and no project
// key ',', so the key reads back as it was made.
func OwnerKey(repo string, sharers []string) string {
	sorted := slices.Compact(slices.Sorted(slices.Values(sharers)))
	return ownerPrefix + repo + "|" + strings.Join(sorted, ",")
}

// legionfactsDefault is the repository a Legion row of an issue whose own chain names none is
// stored under (legionfacts.DefaultRepo): a project's cut of a repository reads those rows too
// where that repository is the one it publishes as its default, so a default move moves the cut
// and rewrites no row (dispatch://INFRA-2916, plan D1 (vi)).
const legionfactsDefault = legionfacts.DefaultRepo

// isOwner reports whether key is an owner key (OwnerKey) rather than a project's.
func isOwner(key string) bool {
	return strings.HasPrefix(key, ownerPrefix)
}

// parseOwner is the repository and the sharers of the owner key key, false where key is none.
func parseOwner(key string) (string, []string, bool) {
	rest, ok := strings.CutPrefix(key, ownerPrefix)
	repo, joined, found := strings.Cut(rest, "|")
	if !ok || !found || repo == "" {
		return "", nil, false
	}
	var sharers []string
	if joined != "" {
		sharers = strings.Split(joined, ",")
	}
	return repo, sharers, true
}

// readOwners reads every owner key eval_days has held a row of (eval_day_owners), in key order:
// the owners the delivery walk writes the days of beside the projects.
func readOwners(ctx context.Context, q delivery.Querier) ([]string, error) {
	rows, err := q.Query(ctx, `select owner from eval_day_owners order by owner collate "C"`)
	if err != nil {
		return nil, fmt.Errorf("read the owner keys of the day rows: %w", err)
	}
	owners, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("read the owner keys of the day rows: %w", err)
	}
	return owners, nil
}

// recordOwners records in eval_day_owners, in tx, each owner key of keys it does not hold yet, as
// the eval_days change trigger records a key the first time a statement writes a row of it
// (migration 20261117100014): a rebuild stages its rows aside first (stageRows), and its fold of
// the stored figures reads the staged rows through eval_project_owners before the flip writes
// them, so a key whose first rows it stages is read by the projects that publish its repository.
// A key whose staged rows the rebuild abandons stands with no row, as one whose rows all went.
func recordOwners(ctx context.Context, tx pgx.Tx, keys []string) error {
	var owners, repos, sharers []string
	for _, key := range slices.Compact(slices.Sorted(slices.Values(keys))) {
		if repo, shared, ok := parseOwner(key); ok {
			owners, repos, sharers = append(owners, key), append(repos, repo), append(sharers, strings.Join(shared, ","))
		}
	}
	if len(owners) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `
		insert into eval_day_owners (owner, repo, sharers)
		select given.owner, given.repo, coalesce(string_to_array(nullif(given.sharers, ''), ','), '{}')
		from unnest($1::text[], $2::text[], $3::text[]) as given(owner, repo, sharers)
		order by given.owner collate "C"
		on conflict do nothing
	`, owners, repos, sharers); err != nil {
		return fmt.Errorf("record %d owner keys a rebuild stages rows of: %w", len(owners), err)
	}
	return nil
}

// readersOf reads in q the projects whose reads fold the rows of owner (eval_project_owners): the
// projects whose published default is its repository, less its sharers, in key order.
func readersOf(ctx context.Context, q delivery.Querier, owner string) ([]string, error) {
	rows, err := q.Query(ctx, `select project_key from eval_project_owners where owner = $1 order by project_key collate "C"`, owner)
	if err != nil {
		return nil, fmt.Errorf("read the projects that read %s: %w", owner, err)
	}
	projects, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("read the projects that read %s: %w", owner, err)
	}
	return projects, nil
}

// PublishedDefaultIn is the repository project publishes as its default, read in q
// (eval_project_repos, the one seam a read takes its repositories from), "" where it publishes
// none yet. A Legion row stored under legionfacts.DefaultRepo is a row of that repository, which
// an items read shows and filters by.
func PublishedDefaultIn(ctx context.Context, q delivery.Querier, project string) (string, error) {
	var repo string
	if err := q.QueryRow(ctx, `select coalesce(max(repo), '') from eval_project_repos where project_key = $1`, project).Scan(&repo); err != nil {
		return "", fmt.Errorf("read the published default repository of %s: %w", project, err)
	}
	return repo, nil
}

// asReader is the eval id evalID, of owner's rows, as project's read of them names it: the same
// slug under project.
func asReader(owner, project, evalID string) string {
	return EvalID(project, slugOf(owner, evalID))
}

// ownedDayColumns is dayColumns of a day row d read through eval_project_owners o: the row as the
// project o names reads it, under its own key and its eval id of the row's slug.
const ownedDayColumns = `o.project_key, o.project_key || substr(d.eval_id, length(o.owner) + 1), d.deploy_repo, d.series, d.repo,
	d.day, d.listed, d.n, d.sum, d.num, d.den, d.max, d.samples, d.last_value, d.last_at, d.last_recorded_at,
	coalesce(d.last_reading::text, ''), d.counts, d.via_repository, d.built_by, d.updated_at`

// addOwnedRows is days, ordered by day, then series and repository, of one eval and deploy
// repository, with the rows of one series, repository and day that several owners hold (a
// project's own and its default repository's, read under the project) added into one, as a fold
// of the day adds their parts (addDayRow): a project's day is one row of each series and cut.
func addOwnedRows(days []DayRow) []DayRow {
	merged := days[:0]
	for _, day := range days {
		if n := len(merged); n > 0 {
			last := &merged[n-1]
			if last.DeployRepo == day.DeployRepo && last.Series == day.Series && last.Repo == day.Repo && last.Day.Equal(day.Day) {
				addDayRow(last, day)
				continue
			}
		}
		merged = append(merged, day)
	}
	return merged
}

// addDayRow adds row's part into into's, as the folds add two parts of one day: counts, sums,
// numerators, denominators and the via-repository count summed, the larger maximum, the samples of
// both, the newer last value, and the older moment of a write (the freshness a read answers).
func addDayRow(into *DayRow, row DayRow) {
	add := func(a, b *float64) *float64 {
		switch {
		case a == nil:
			return b
		case b == nil:
			return a
		}
		return new(*a + *b)
	}
	into.N += row.N
	into.Sum, into.Num, into.Den = add(into.Sum, row.Sum), add(into.Num, row.Num), add(into.Den, row.Den)
	if row.Max != nil && (into.Max == nil || *row.Max > *into.Max) {
		into.Max = row.Max
	}
	into.Samples = append(slices.Clone(into.Samples), row.Samples...)
	if len(row.Counts) > 0 {
		counts := make(map[string]float64, len(into.Counts)+len(row.Counts))
		for name, value := range into.Counts {
			counts[name] += value
		}
		for name, value := range row.Counts {
			counts[name] += value
		}
		into.Counts = counts
	}
	into.ViaRepository += row.ViaRepository
	if row.LastAt != nil && (into.LastAt == nil || row.LastAt.After(*into.LastAt)) {
		into.LastValue, into.LastAt, into.LastRecordedAt, into.LastReading = row.LastValue, row.LastAt, row.LastRecordedAt, row.LastReading
	}
	if row.UpdatedAt.Before(into.UpdatedAt) {
		into.UpdatedAt = row.UpdatedAt
	}
}
