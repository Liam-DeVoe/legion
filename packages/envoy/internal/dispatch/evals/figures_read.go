package evals

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/delivery"
	"github.com/sjawhar/envoy/internal/dispatch/delivery/measures"
)

// The read of the stored figures (figures.go): the eval list's read of an offered range answers
// each eval from its stored rows, by lookup, where the day rows' read (DayWindow) would fold every
// row of the window and the compare range again. k8 measured the first paint of 84 days with a
// compare range on the farm's slowest node: 95.6 ms of database time over the day rows, 12.2 ms
// over the stored rows, with the answers equal byte for byte.

// FiguresOffered reports whether the stored figures can answer a read of window and compare (nil
// for none) of repos: window and compare each whole UTC days of one of FigureLengths, and at most
// one repository, whose rows a stored row of that repository folds (none: the project's own). A
// read of another length, or of two repositories, whose fold merges several repositories' rows
// that no stored row holds, folds the day rows.
func FiguresOffered(repos []string, window Range, compare *Range) bool {
	if len(repos) > 1 || !figureRange(window) {
		return false
	}
	return compare == nil || figureRange(*compare)
}

// figureRange reports whether r is whole UTC days of one of FigureLengths.
func figureRange(r Range) bool {
	if !r.From.Equal(measures.Midnight(r.From)) || !r.To.Equal(measures.Midnight(r.To)) {
		return false
	}
	return slices.Contains(FigureLengths, rangeDays(r))
}

// rangeDays is how many whole days r spans.
func rangeDays(r Range) int {
	return int(r.To.Sub(r.From) / (24 * time.Hour))
}

// storedRange is a stored range row as the read reads it: its fold key, its figures and the oldest
// updated_at of the rows it folds.
type storedRange struct {
	key     int64
	figures []byte
	oldest  time.Time
}

// storedDay is a stored day row as the read reads it: its day, its fold key and its figures.
type storedDay struct {
	day     time.Time
	key     int64
	figures []byte
}

// StoredFigures is what a read of the stored figures read for one window, compare range and
// repository (ReadStoredFigures): how far each project's figures are built, each eval's stored row
// of the window and of the compare range, each eval's stored day rows of the window and of the
// compare range, and the evals whose stored rows the read found of more than one deploy repository
// (several), whose fold no stored row holds.
type StoredFigures struct {
	built             map[string]time.Time
	window, compare   map[string]storedRange
	days, compareDays map[string][]storedDay
	several           map[string]bool
}

// ReadStoredFigures is ReadStoredFiguresIn on the pool, each of its statements reading the rows
// that stand when it runs.
func (s *Store) ReadStoredFigures(ctx context.Context, projects []string, entries []Entry, deployRepos *DeployRepos, repo string, window Range, compare *Range) (StoredFigures, error) {
	return ReadStoredFiguresIn(ctx, s.database.Pool, projects, entries, deployRepos, repo, window, compare)
}

// ReadStoredFiguresIn reads in q the stored figures a read of window and compare (nil for none) of
// repo (the empty string for the projects' own rows) folds for entries, evals of projects, where
// the rows are recorded under deployRepos (DeployReposIn): of an eval keyed by the deploy
// repository the fold of its project's repositories' rows (DeployRepos.Of), and of every other the
// fold of the rows of none (DeployRepoOf). A stored row folds one deploy repository's rows, so an
// eval whose stored rows it finds of two deploy repositories is none the stored figures answer
// (SeriesValues, several), and its caller folds its day rows; one whose rows lie in one repository
// alone, as a project's do whose deploys and pull requests one source repository counts, is
// answered by lookup. It reads, one after the other: the range rows of the window and of the
// compare range, the `union all` of two ranges of eval_range_figures' primary key for each deploy
// repository; how far each project's figures are built (figuresThrough); and the day rows of the
// window and of the compare range of the evals with a headline series, the `union all` of one
// range of eval_day_figures' primary key for each project and range, each row tagged with the range
// it is of. A refresher pass rewrites the three in one transaction, so in one answer's snapshot
// (BeginSnapshot), as the eval list reads them, an eval's figures are the fold of the days read
// beside them; on the pool each statement reads the rows that stand when it runs. The read is
// FiguresOffered's to make.
func ReadStoredFiguresIn(ctx context.Context, q delivery.Querier, projects []string, entries []Entry, deployRepos *DeployRepos, repo string, window Range, compare *Range) (StoredFigures, error) {
	read := StoredFigures{
		window: map[string]storedRange{}, compare: map[string]storedRange{}, days: map[string][]storedDay{}, compareDays: map[string][]storedDay{},
		several: map[string]bool{},
	}
	evalIDs := make([]string, len(entries))
	var headlined []string
	for index, entry := range entries {
		evalIDs[index] = entry.Definition.EvalID
		if _, headline := listedAndHeadline(entry); len(headline) > 0 {
			headlined = append(headlined, entry.Definition.EvalID)
		}
	}
	seen := map[string]string{}
	note := func(evalID, deployRepo string) {
		if was, held := seen[evalID]; held && was != deployRepo {
			read.several[evalID] = true
		}
		seen[evalID] = deployRepo
	}
	pairProjects, pairRepos := deployRepos.pairs(projects)
	keyed := keyedEvals(entries)
	ranged := `select days, end_day, eval_id, deploy_repo, fold_key, figures, oldest_updated_at from eval_range_figures
		where project_key = any($1::text[]) and repo = $2 and eval_id = any($3::text[]) and days = $%d and end_day = $%d::date
			and ` + deployRepoCase("deploy_repo", "$4", "$5", "$6")
	query := fmt.Sprintf(ranged, 7, 8)
	args := []any{projects, repo, evalIDs, keyed, pairProjects, pairRepos, rangeDays(window), window.To}
	if compare != nil {
		query += "\n\t\tunion all\n\t\t" + fmt.Sprintf(ranged, 9, 10)
		args = append(args, rangeDays(*compare), compare.To)
	}
	ranges, err := q.Query(ctx, query, args...)
	if err != nil {
		return StoredFigures{}, fmt.Errorf("read the stored range figures of %v: %w", projects, err)
	}
	defer ranges.Close()
	for ranges.Next() {
		var days int32
		var end time.Time
		var evalID, deployRepo string
		var row storedRange
		if err := ranges.Scan(&days, &end, &evalID, &deployRepo, &row.key, &row.figures, &row.oldest); err != nil {
			return StoredFigures{}, fmt.Errorf("read the stored range figures of %v: %w", projects, err)
		}
		end, row.oldest = end.UTC(), row.oldest.UTC()
		note(evalID, deployRepo)
		if int(days) == rangeDays(window) && end.Equal(window.To) {
			read.window[evalID] = row
		}
		if compare != nil && int(days) == rangeDays(*compare) && end.Equal(compare.To) {
			read.compare[evalID] = row
		}
	}
	if err := ranges.Err(); err != nil {
		return StoredFigures{}, fmt.Errorf("read the stored range figures of %v: %w", projects, err)
	}
	if read.built, err = figuresThrough(ctx, q, projects); err != nil {
		return StoredFigures{}, err
	}
	if len(headlined) == 0 {
		return read, nil
	}
	dayRows := `select %s, eval_id, deploy_repo, day, fold_key, figures from eval_day_figures
		where project_key = any($1::text[]) and repo = $2 and day >= $%d::date and day < $%d::date and eval_id = any($3::text[])
			and ` + deployRepoCase("deploy_repo", "$4", "$5", "$6")
	query = fmt.Sprintf(dayRows, "true", 7, 8)
	args = []any{projects, repo, headlined, keyed, pairProjects, pairRepos, window.From, window.To}
	if compare != nil {
		query += "\n\t\tunion all\n\t\t" + fmt.Sprintf(dayRows, "false", 9, 10)
		args = append(args, compare.From, compare.To)
	}
	days, err := q.Query(ctx, query, args...)
	if err != nil {
		return StoredFigures{}, fmt.Errorf("read the stored day figures of %v: %w", projects, err)
	}
	defer days.Close()
	for days.Next() {
		var inWindow bool
		var evalID, deployRepo string
		var row storedDay
		if err := days.Scan(&inWindow, &evalID, &deployRepo, &row.day, &row.key, &row.figures); err != nil {
			return StoredFigures{}, fmt.Errorf("read the stored day figures of %v: %w", projects, err)
		}
		row.day = row.day.UTC()
		note(evalID, deployRepo)
		if inWindow {
			read.days[evalID] = append(read.days[evalID], row)
		} else {
			read.compareDays[evalID] = append(read.compareDays[evalID], row)
		}
	}
	if err := days.Err(); err != nil {
		return StoredFigures{}, fmt.Errorf("read the stored day figures of %v: %w", projects, err)
	}
	return read, nil
}

// SeriesValues is SeriesValuesOf over the stored figures: entry's listed series with their values
// over window and compare (nil for none), read against firsts, each series' first day, the answer
// SeriesValuesOf gives over the day rows, bit for bit, each statistic a lookup; and the oldest
// updated_at of the rows the stored rows it read fold, nil where it read none. A range of which the
// eval has no stored row holds none of its rows, and reads the fold of no parts, as the day rows'
// read folds none. ok is false where the stored figures cannot answer the eval, which the caller
// answers from its day rows: its project's figures are not built through the end of both ranges
// (FiguresThrough), its stored rows lie in more than one deploy repository (several), a
// stored row it would read was folded under another fold key than the eval's definition now has (a
// register write the refresher has not folded yet), or one does not decode.
func (f StoredFigures) SeriesValues(entry Entry, firsts map[string]time.Time, window Range, compare *Range) ([]SeriesValues, *time.Time, bool) {
	definition := entry.Definition
	through, built := f.built[definition.ProjectKey]
	if !built || through.Before(window.To) || compare != nil && through.Before(compare.To) {
		return nil, nil, false
	}
	if f.several[definition.EvalID] {
		return nil, nil, false
	}
	listed, _ := listedAndHeadline(entry)
	key := foldKeyOf(entry)
	var oldest *time.Time
	fold := func(rows map[string]storedRange, r Range) (map[string]Statistics, map[string]int, bool) {
		row, held := rows[definition.EvalID]
		if !held {
			return FoldStatistics(definition, listed, nil, r), nil, true
		}
		if row.key != key {
			return nil, nil, false
		}
		folded, via, err := decodeFigures(row.figures)
		if err != nil {
			return nil, nil, false
		}
		if oldest == nil || row.oldest.Before(*oldest) {
			oldest = &row.oldest
		}
		return folded, via, true
	}
	windowed, via, ok := fold(f.window, window)
	if !ok {
		return nil, nil, false
	}
	var compared map[string]Statistics
	if compare != nil {
		if compared, _, ok = fold(f.compare, *compare); !ok {
			return nil, nil, false
		}
	}
	daily, ok := storedDailyOf(f.days[definition.EvalID], key)
	if !ok {
		return nil, nil, false
	}
	var compareDaily []dayFold
	if compare != nil {
		if compareDaily, ok = storedDailyOf(f.compareDays[definition.EvalID], key); !ok {
			return nil, nil, false
		}
	}
	return valuesOf(listed, windowed, via, compared, daily, compareDaily, firsts, window, compare), oldest, true
}

// storedDailyOf is stored, an eval's stored day rows of one range, as the day folds they hold, in
// day order; false where one was folded under another fold key than key, or does not decode.
func storedDailyOf(stored []storedDay, key int64) ([]dayFold, bool) {
	sorted := slices.SortedFunc(slices.Values(stored), func(a, b storedDay) int { return a.day.Compare(b.day) })
	daily := make([]dayFold, len(sorted))
	for index, day := range sorted {
		if day.key != key {
			return nil, false
		}
		folded, _, err := decodeFigures(day.figures)
		if err != nil {
			return nil, false
		}
		daily[index] = dayFold{day: day.day, folded: folded}
	}
	return daily, true
}
