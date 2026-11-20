package evals

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/delivery"
	"github.com/sjawhar/envoy/internal/dispatch/delivery/measures"
	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// The checks of the stored figures (figures.go): after each change, the stored rows are the
// fold of the day rows that stand, as a read of those rows folds them (truthFigures, which reads
// them through DayWindow and folds each range with FoldStatistics over its rows in day order,
// merging and sorting every quantile's samples), and the rows the change wrote (by the
// transaction that last wrote each, xmin) are only those its moved days reach (figureReach).

// figureRow is one stored figures row as the checks read it: the transaction that last wrote it
// (xmin, "" for a row truthFigures folds), the build that folded it, its fold key and figures, and
// its oldest updated_at (a range row's) or its stamp (a day row's).
type figureRow struct {
	xmin    string
	builtBy string
	foldKey int64
	figures []byte
	oldest  time.Time
	stamp   int64
}

// same reports whether two rows hold the same figures, whichever transaction and build wrote them.
func (f figureRow) same(other figureRow) bool {
	return f.foldKey == other.foldKey && slices.Equal(f.figures, other.figures) && f.oldest.Equal(other.oldest) && f.stamp == other.stamp
}

// rangeLine names a stored range row: `range <project> <repo> <days> <end> <eval>`.
func rangeLine(project, repo string, days int, end time.Time, evalID string) string {
	return fmt.Sprintf("range %s %q %d %s %s", project, repo, days, end.Format(time.DateOnly), evalID)
}

// dayFigureLine names a stored day row: `day <project> <repo> <day> <eval>`.
func dayFigureLine(project, repo string, day time.Time, evalID string) string {
	return fmt.Sprintf("day %s %q %s %s", project, repo, day.Format(time.DateOnly), evalID)
}

// storedFigures is every stored figures row, by rangeLine and dayFigureLine.
func (r *refresherRig) storedFigures(t *testing.T) map[string]figureRow {
	t.Helper()
	stored := map[string]figureRow{}
	rows, err := r.database.Pool.Query(t.Context(), `
		select xmin::text, built_by, project_key, repo, days, end_day, eval_id, fold_key, figures, oldest_updated_at from eval_range_figures`)
	if err != nil {
		t.Fatalf("read the stored range figures: %v", err)
	}
	var row figureRow
	var project, repo, evalID string
	var days int
	var at time.Time
	if _, err := pgx.ForEachRow(rows, []any{&row.xmin, &row.builtBy, &project, &repo, &days, &at, &evalID, &row.foldKey, &row.figures, &row.oldest}, func() error {
		kept := row
		kept.oldest = kept.oldest.UTC()
		stored[rangeLine(project, repo, days, at.UTC(), evalID)] = kept
		return nil
	}); err != nil {
		t.Fatalf("read the stored range figures: %v", err)
	}
	rows, err = r.database.Pool.Query(t.Context(), `
		select xmin::text, built_by, project_key, repo, day, eval_id, fold_key, figures, rows_stamp from eval_day_figures`)
	if err != nil {
		t.Fatalf("read the stored day figures: %v", err)
	}
	row = figureRow{}
	if _, err := pgx.ForEachRow(rows, []any{&row.xmin, &row.builtBy, &project, &repo, &at, &evalID, &row.foldKey, &row.figures, &row.stamp}, func() error {
		stored[dayFigureLine(project, repo, at.UTC(), evalID)] = row
		return nil
	}); err != nil {
		t.Fatalf("read the stored day figures: %v", err)
	}
	return stored
}

// figuresBuilt is the last end day every project whose figures are built is built through
// (FiguresThrough), the marker a read of the stored figures reads.
func (r *refresherRig) figuresBuilt(t *testing.T) map[string]time.Time {
	t.Helper()
	projects, err := r.registry.Projects(t.Context())
	if err != nil {
		t.Fatalf("read the projects: %v", err)
	}
	built, err := r.registry.FiguresThrough(t.Context(), projects)
	if err != nil {
		t.Fatalf("read how far the figures are built: %v", err)
	}
	return built
}

// figuresFolded is how far each project's figures are folded, by any build (readFigureScope):
// every range ending on or before it may be stored, and each write keeps them true.
func (r *refresherRig) figuresFolded(t *testing.T) map[string]time.Time {
	t.Helper()
	projects, err := r.registry.Projects(t.Context())
	if err != nil {
		t.Fatalf("read the projects: %v", err)
	}
	folded := map[string]time.Time{}
	for _, project := range projects {
		scope, ok, err := readFigureScope(t.Context(), r.database.Pool, r.build, project)
		if err != nil {
			t.Fatalf("read how far %s's figures are folded: %v", project, err)
		}
		if ok {
			folded[project] = scope.through
		}
	}
	return folded
}

// truthFigures is every figure the walks have folded, folded from the day rows that stand as a
// read of the rows folds them: for each project folded through an end day, each repository's
// listed rows of each eval read through DayWindow, in day order, and every range of FigureLengths
// ending on or before that day that holds one of them folded by FoldStatistics, and every day
// before it that holds one folded alone over its headline series. No list is sorted before the
// fold, so every quantile merges and sorts its samples.
func (r *refresherRig) truthFigures(t *testing.T) map[string]figureRow {
	t.Helper()
	ctx := t.Context()
	truth := map[string]figureRow{}
	for project, through := range r.figuresFolded(t) {
		page, err := r.registry.Current(ctx, []string{project}, "", 1000, 0)
		if err != nil {
			t.Fatalf("read the evals of %s: %v", project, err)
		}
		rows, err := r.database.Pool.Query(ctx, `select distinct repo from eval_days where project_key = $1 and listed`, project)
		if err != nil {
			t.Fatalf("read the repositories of %s: %v", project, err)
		}
		repos, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatalf("read the repositories of %s: %v", project, err)
		}
		for _, repo := range repos {
			var named []string
			if repo != "" {
				named = []string{repo}
			}
			read, err := r.registry.DayWindow(ctx, []string{project}, page.Items, named, Range{From: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), To: through}, nil)
			if err != nil {
				t.Fatalf("read the day rows of %s %q: %v", project, repo, err)
			}
			byEval := map[string][]WindowRow{}
			for _, row := range read {
				byEval[row.EvalID] = append(byEval[row.EvalID], row)
			}
			for _, entry := range page.Items {
				held := byEval[entry.Definition.EvalID]
				listed, headline := listedAndHeadline(entry)
				if len(held) == 0 {
					continue
				}
				slices.SortStableFunc(held, func(a, b WindowRow) int { return a.Part.Day.Compare(b.Part.Day) })
				key := foldKeyOf(entry)
				within := func(from, to time.Time) ([]WindowRow, []measures.DayPart) {
					var rows []WindowRow
					var parts []measures.DayPart
					for _, row := range held {
						if !row.Part.Day.Before(from) && row.Part.Day.Before(to) {
							rows, parts = append(rows, row), append(parts, row.Part)
						}
					}
					return rows, parts
				}
				for end := held[0].Part.Day.AddDate(0, 0, 1); !end.After(through); end = end.AddDate(0, 0, 1) {
					for _, n := range FigureLengths {
						span := rangeOf(n, end)
						rows, parts := within(span.From, span.To)
						if len(rows) == 0 {
							continue
						}
						via, oldest := map[string]int{}, rows[0].UpdatedAt
						for _, row := range rows {
							via[row.Part.Series] += row.Part.ViaRepository
							if row.UpdatedAt.Before(oldest) {
								oldest = row.UpdatedAt
							}
						}
						truth[rangeLine(project, repo, n, end, entry.Definition.EvalID)] = figureRow{
							foldKey: key, figures: encodeFigures(FoldStatistics(entry.Definition, listed, parts, span), via), oldest: oldest,
						}
					}
				}
				for day := held[0].Part.Day; day.Before(through); day = day.AddDate(0, 0, 1) {
					rows, parts := within(day, day.AddDate(0, 0, 1))
					if len(rows) == 0 {
						continue
					}
					folded := FoldStatistics(entry.Definition, headline, parts, Range{From: day, To: day.AddDate(0, 0, 1)})
					kept := map[string]Statistics{}
					stamped := []stampedRow{}
					for _, series := range headline {
						if holds(parts, series.Series) {
							kept[series.Series] = folded[series.Series]
						}
					}
					for _, row := range rows {
						stamped = append(stamped, stampedRow{series: row.Part.Series, updatedAt: row.UpdatedAt})
					}
					truth[dayFigureLine(project, repo, day, entry.Definition.EvalID)] = figureRow{
						foldKey: key, figures: encodeFigures(kept, nil), stamp: rowsStamp(stamped),
					}
				}
			}
		}
	}
	return truth
}

// expectTruth checks that stored holds exactly truth's rows, each with truth's figures.
func expectTruth(t *testing.T, what string, stored, truth map[string]figureRow) {
	t.Helper()
	var wrong []string
	for _, line := range slices.Sorted(maps.Keys(truth)) {
		if row, held := stored[line]; !held {
			wrong = append(wrong, "missing "+line)
		} else if !row.same(truth[line]) {
			wrong = append(wrong, "stale "+line)
		}
	}
	for _, line := range slices.Sorted(maps.Keys(stored)) {
		if _, held := truth[line]; !held {
			wrong = append(wrong, "extra "+line)
		}
	}
	if len(wrong) > 0 {
		t.Errorf("%s: %d of the %d stored figures rows are not the fold of the day rows that stand (%d expected), first ones:\n\t%s",
			what, len(wrong), len(stored), len(truth), strings.Join(wrong[:min(len(wrong), 12)], "\n\t"))
	}
}

// writtenFigures is every stored figures row a transaction wrote, made or deleted between before
// and after.
func writtenFigures(before, after map[string]figureRow) []string {
	var written []string
	for line, row := range after {
		if old, held := before[line]; !held || old.xmin != row.xmin {
			written = append(written, line)
		}
	}
	for line := range before {
		if _, held := after[line]; !held {
			written = append(written, line)
		}
	}
	slices.Sort(written)
	return written
}

// figureReach is what a change of the day rows from before to after may write of the stored
// figures, with through how far each project's figures are folded: for each listed row that
// appeared, went or had its updated_at moved, on day D of its eval and repository, every range of
// FigureLengths whose span holds D and that ends no later than through, and D's own day row; and
// those day rows alone, each of which a write of its moved rows must write, since its stamp moved.
func figureReach(before, after map[string]DayRow, through map[string]time.Time) (map[string]bool, []string) {
	reach := map[string]bool{}
	var days []string
	note := func(row DayRow) {
		limit, built := through[row.ProjectKey]
		if !row.Listed || !built || !row.Day.Before(limit) {
			return
		}
		for _, n := range FigureLengths {
			for end := row.Day.AddDate(0, 0, 1); !end.After(row.Day.AddDate(0, 0, n)) && !end.After(limit); end = end.AddDate(0, 0, 1) {
				reach[rangeLine(row.ProjectKey, row.Repo, n, end, row.EvalID)] = true
			}
		}
		line := dayFigureLine(row.ProjectKey, row.Repo, row.Day, row.EvalID)
		if !reach[line] {
			reach[line] = true
			days = append(days, line)
		}
	}
	for key, row := range after {
		if old, held := before[key]; !held || !old.UpdatedAt.Equal(row.UpdatedAt) || old.Listed != row.Listed {
			note(row)
			if held {
				note(old)
			}
		}
	}
	for key, row := range before {
		if _, held := after[key]; !held {
			note(row)
		}
	}
	slices.Sort(days)
	return reach, days
}

// figureState is the day rows and the stored figures at one moment.
type figureState struct {
	days    map[string]DayRow
	figures map[string]figureRow
}

func (r *refresherRig) figureState(t *testing.T) figureState {
	t.Helper()
	return figureState{days: r.dayRows(t), figures: r.storedFigures(t)}
}

// expectRewrites checks a change against the state before it, once the passes after it ran: the
// stored figures are the fold of the day rows that stand, every row written is one the day rows
// the change moved reach (figureReach), or one extra allows, each recording the pass's build as the
// build that folded it, and every moved day's own row was written; where moves is set, the change
// moved a listed day row and wrote a stored row; then answers the state after it. It logs how many
// rows the change wrote of how many its moved days reach.
func (r *refresherRig) expectRewrites(t *testing.T, what string, before figureState, moves bool, extra func(line string) bool) figureState {
	t.Helper()
	after := r.figureState(t)
	expectTruth(t, what, after.figures, r.truthFigures(t))
	reach, days := figureReach(before.days, after.days, r.figuresFolded(t))
	written := writtenFigures(before.figures, after.figures)
	if moves && (len(days) == 0 || len(written) == 0) {
		t.Errorf("%s moved %d listed day rows and wrote %d stored rows, and the check needs it to move one", what, len(days), len(written))
	}
	var outside []string
	for _, line := range written {
		if !reach[line] && (extra == nil || !extra(line)) {
			outside = append(outside, line)
		}
		if row, held := after.figures[line]; held && row.builtBy != r.build {
			t.Errorf("%s: the stored row of %s, written by the pass of %s, records %s as the build that folded it", what, line, r.build, row.builtBy)
		}
	}
	if len(outside) > 0 {
		t.Errorf("%s: %d stored figures rows were written that no moved day reaches, first ones:\n\t%s", what, len(outside), strings.Join(outside[:min(len(outside), 12)], "\n\t"))
	}
	for _, line := range days {
		if !slices.Contains(written, line) {
			t.Errorf("%s: the stored row of %s was not written, and its day rows moved", what, line)
		}
	}
	t.Logf("%s: %d moved days reach %d stored rows; the change wrote %d of them", what, len(days), len(reach), len(written))
	return after
}

// expectFoldedInTx checks that each day whose listed rows moved from before to after had its
// stored row written by a transaction that wrote one of those rows: the day rows' own, which
// folds the figures of the days it moves (figures.go), and not a later one. It answers after.
func (r *refresherRig) expectFoldedInTx(t *testing.T, what string, before, after figureState) figureState {
	t.Helper()
	rows, err := r.database.Pool.Query(t.Context(), `select xmin::text, project_key, repo, day, eval_id from eval_days where listed`)
	if err != nil {
		t.Fatalf("read the day rows' transactions: %v", err)
	}
	wrote := map[string][]string{}
	var xmin, project, repo, evalID string
	var day time.Time
	if _, err := pgx.ForEachRow(rows, []any{&xmin, &project, &repo, &day, &evalID}, func() error {
		line := dayFigureLine(project, repo, day.UTC(), evalID)
		wrote[line] = append(wrote[line], xmin)
		return nil
	}); err != nil {
		t.Fatalf("read the day rows' transactions: %v", err)
	}
	_, days := figureReach(before.days, after.days, r.figuresFolded(t))
	for _, line := range days {
		if stored, held := after.figures[line]; held && !slices.Contains(wrote[line], stored.xmin) {
			t.Errorf("%s: the stored row of %s was written by transaction %s, and its day rows by %v", what, line, stored.xmin, wrote[line])
		}
	}
	return after
}

// seedLoggedQuantile registers EVAL's review-latency, a logged eval whose one series folds its
// readings' minutes as a quantile, and logs readings of it on three days of the history, some of
// them of a repository: the logged fold, a quantile, and a repository's rows beside the
// project's own.
func (r *refresherRig) seedLoggedQuantile(t *testing.T) {
	t.Helper()
	if _, err := r.registry.Register(t.Context(), Registration{
		Project: r.project, Slug: "review-latency", Caller: alice, Kind: Set(KindExperiment),
		Series: []SeriesChange{{Series: "minutes", SeriesComputationChange: SeriesComputationChange{Agg: Set(AggQuantile)}}},
	}); err != nil {
		t.Fatalf("register review-latency: %v", err)
	}
	var rows []json.RawMessage
	for index, minutes := range []float64{12, 30, 30, 7.5, 61, 2, 45, 18} {
		fields := map[string]any{"series": "minutes", "row_kind": "review", "row_key": fmt.Sprint(index), "value": minutes,
			"measured_at": refresherToday.AddDate(0, 0, -3+index%3).Add(time.Duration(index) * time.Hour)}
		if index%2 == 0 {
			fields["attributes"] = map[string]any{"repo": "acme/web"}
		}
		rows = append(rows, row(t, fields))
	}
	if _, err := r.registry.WriteReadings(t.Context(), Batch{Project: r.project, Slug: "review-latency", Rows: rows, Caller: alice, BuiltBy: r.build}); err != nil {
		t.Fatalf("log review-latency's readings: %v", err)
	}
}

// expectBuilt checks that every project's figures are built through the last end day of today
// (FiguresThrough), so a read of an offered range folds the stored figures.
func (r *refresherRig) expectBuilt(t *testing.T, what string, today time.Time) {
	t.Helper()
	projects, err := r.registry.Projects(t.Context())
	if err != nil {
		t.Fatalf("read the projects: %v", err)
	}
	built := r.figuresBuilt(t)
	for _, project := range projects {
		if through, held := built[project]; !held || !through.Equal(today.AddDate(0, 0, figureLead)) {
			t.Errorf("%s: %s's figures stand built %t through %s, want through %s", what, project, held, through.Format(time.DateOnly),
				today.AddDate(0, 0, figureLead).Format(time.DateOnly))
		}
	}
}

// TestEachChangeRewritesExactlyTheFiguresItsMovedDaysReach: over the delivery, Legion and logged
// histories, once the first whole build has folded every figure, each change of an input is folded
// into the stored figures by the pass after it, in the day rows' own transaction where a write of
// its rows moved them, which writes only the rows its moved days reach and leaves the stored
// figures the fold of the rows that stand: a pull request merged a day earlier, a deploy run that
// is cancelled, a production job that fails, an issue's facts, a logged reading (in the readings
// write's own transaction, after which the pass writes nothing), a register write that gives the
// logged eval another fold, a settings change (the delivery walk of every day), and the daily tick
// (the new day's rows and the ranges of the new last end day). A new build whose folds agree then
// walks every figure and writes none, the stored figures built throughout; and one that meets
// figures another build folded otherwise rewrites exactly those.
func TestEachChangeRewritesExactlyTheFiguresItsMovedDaysReach(t *testing.T) {
	r := newRefresherRig(t)
	deliveries, _ := r.seedHistories(t)
	r.seedLoggedQuantile(t)
	r.settle(t)
	state := r.figureState(t)
	expectTruth(t, "the first whole build", state.figures, r.truthFigures(t))
	r.expectBuilt(t, "the first whole build", refresherToday)
	t.Logf("the first whole build stored %d figures rows", len(state.figures))

	// A pull request of another repository than the deploy repository's, merged a day earlier.
	var moved delivery.DeliveryPullRequest
	for _, pr := range deliveries.PullRequests {
		if pr.Repo != deliveries.DeployRepo && pr.MergedAt != nil && measures.Midnight(*pr.MergedAt).After(deliveries.From) && moved.Number == 0 {
			moved = pr
		}
	}
	if moved.Number == 0 {
		t.Fatal("the history holds no pull request of another repository merged after its first day")
	}
	r.exec(t, "merge a pull request a day earlier", `
		update delivery_pull_requests set merged_at = $3, created_at = least(created_at, $3::timestamptz - interval '1 hour') where repo = $1 and number = $2
	`, moved.Repo, moved.Number, moved.MergedAt.AddDate(0, 0, -1))
	r.pass(t)
	state = r.expectFoldedInTx(t, "a pull request merged a day earlier", state, r.expectRewrites(t, "a pull request merged a day earlier", state, true, nil))

	// A deploy run that succeeded is cancelled: deploy-run-success counts it among the cancelled
	// runs rather than the concluded ones.
	var run delivery.DeliveryRun
	for _, candidate := range deliveries.Runs {
		if candidate.Kind == delivery.DeliveryRunKindDeploy && candidate.Conclusion != nil && *candidate.Conclusion == delivery.DeliveryRunConclusionSuccess {
			run = candidate
		}
	}
	if run.RunID == 0 {
		t.Fatal("the history holds no deploy run that succeeded")
	}
	r.exec(t, "cancel a deploy run", `update delivery_runs set conclusion = 'cancelled' where repo = $1 and run_id = $2`, run.Repo, run.RunID)
	r.pass(t)
	state = r.expectFoldedInTx(t, "a deploy run that is cancelled", state, r.expectRewrites(t, "a deploy run that is cancelled", state, true, nil))

	// The production job of a deploy run that reached production fails: the run stops reaching
	// it, and the pull requests it shipped ship with a later one or wait.
	var job delivery.DeliveryRunJob
	for _, candidate := range deliveries.Runs {
		for _, one := range deliveries.Jobs[candidate.RunID] {
			if candidate.Kind == delivery.DeliveryRunKindDeploy && candidate.RunID != run.RunID && slices.Contains(deliveries.Settings.ProductionJobNames, one.Name) &&
				one.Conclusion != nil && *one.Conclusion == delivery.DeliveryJobConclusionSuccess {
				job, run = one, candidate
			}
		}
	}
	if job.Name == "" {
		t.Fatal("the history holds no production job that succeeded")
	}
	r.exec(t, "fail a production job", `update delivery_run_jobs set conclusion = 'failure' where repo = $1 and run_id = $2 and name = $3`, run.Repo, run.RunID, job.Name)
	r.pass(t)
	state = r.expectFoldedInTx(t, "a production job that fails", state, r.expectRewrites(t, "a production job that fails", state, true, nil))

	// An issue's facts: more work hours and a review round.
	r.exec(t, "move an issue's facts", `
		update issue_facts set work_hours = work_hours + 1.5, review_rounds = review_rounds + 1
		where project_key = 'LEGION' and issue_key = (
			select issue_key from issue_facts where project_key = 'LEGION' and closed_at is not null order by closed_at desc, issue_key limit 1)`)
	r.pass(t)
	state = r.expectFoldedInTx(t, "an issue's facts", state, r.expectRewrites(t, "an issue's facts", state, true, nil))

	// A logged reading, folded in the readings write's own transaction; the pass after it finds
	// the figures of its day fresh and writes none.
	r.write(t, "review-latency", alice, row(t, map[string]any{"series": "minutes", "row_kind": "review", "row_key": "late",
		"value": 90, "measured_at": refresherToday.AddDate(0, 0, -2).Add(5 * time.Hour), "attributes": map[string]any{"repo": "acme/web"}}))
	state = r.expectFoldedInTx(t, "a logged reading's write", state, r.expectRewrites(t, "a logged reading's write", state, true, nil))
	r.pass(t)
	if after := r.storedFigures(t); len(writtenFigures(state.figures, after)) != 0 {
		t.Errorf("the pass after a logged reading wrote the stored figures %v, which the reading's write folded", writtenFigures(state.figures, after))
	}

	// A register write that folds the logged eval by its largest value: its fold key moves, and
	// every one of its stored rows is folded again.
	evalID := EvalID(r.project, "review-latency")
	if _, err := r.registry.Register(t.Context(), Registration{Project: r.project, Slug: "review-latency", Caller: alice,
		Series: []SeriesChange{{Series: "minutes", SeriesComputationChange: SeriesComputationChange{Agg: Set(AggMax)}}}}); err != nil {
		t.Fatalf("fold review-latency by its largest value: %v", err)
	}
	r.pass(t)
	state = r.expectRewrites(t, "a register write of the logged eval's fold", state, false, func(line string) bool { return strings.HasSuffix(line, " "+evalID) })

	// A settings change: the staging e2e job is named one no run holds, and the delivery walk
	// begins again and writes every day.
	settings := deliveries.Settings
	settings.StagingE2EJobName = new("no-such-job")
	if _, err := delivery.PutSettings(t.Context(), r.database.Pool, settings, model.Actor{Kind: "system", ID: "figures-test"}); err != nil {
		t.Fatalf("rename the staging e2e job: %v", err)
	}
	r.settle(t)
	state = r.expectRewrites(t, "a settings change", state, true, nil)

	// The daily tick: a pass of the next UTC day writes its rows, and the ranges of the end day it
	// adds, two days after the new today, of every eval.
	added := refresherToday.AddDate(0, 0, 1+figureLead).Format(time.DateOnly)
	r.now = r.now.Add(24 * time.Hour)
	r.settle(t)
	state = r.expectRewrites(t, "the daily tick", state, true, func(line string) bool {
		return strings.HasPrefix(line, "range ") && strings.Contains(line, " "+added+" ")
	})
	r.expectBuilt(t, "after the daily tick", refresherToday.AddDate(0, 0, 1))

	// A new build whose folds agree with the build before it: its first pass still reads the
	// stored figures as built, and its whole walk of them writes none.
	first := r.build
	r.build = "another-build"
	r.pass(t)
	r.expectBuilt(t, "a new build's first pass", refresherToday.AddDate(0, 0, 1))
	r.settle(t)
	after := r.figureState(t)
	expectTruth(t, "a new build's whole walk", after.figures, r.truthFigures(t))
	if written := writtenFigures(state.figures, after.figures); len(written) != 0 {
		t.Errorf("the whole walk of a new build whose folds agree wrote %d stored rows, first %v", len(written), written[:min(len(written), 5)])
	}
	state = after

	// Figures another build folded otherwise (the stored rows of one eval, as a build of other
	// folds leaves them): the next build's whole walk rewrites exactly those.
	other := EvalID(deliveries.Project, SlugDeployFrequency)
	r.exec(t, "fold one eval's figures otherwise", `
		update eval_range_figures set figures = '\x00'::bytea, built_by = 'other-folds' where eval_id = $1`, other)
	state = r.figureState(t)
	r.build = "third-build"
	r.settle(t)
	after = r.figureState(t)
	expectTruth(t, "a build's walk over figures another build folded otherwise", after.figures, r.truthFigures(t))
	for _, line := range writtenFigures(state.figures, after.figures) {
		if !strings.HasPrefix(line, "range ") || !strings.HasSuffix(line, " "+other) {
			t.Errorf("a build's walk over %s's figures folded otherwise wrote %s too", other, line)
		}
	}
	for line, row := range after.figures {
		if strings.HasPrefix(line, "range ") && strings.HasSuffix(line, " "+other) && row.builtBy != r.build {
			t.Errorf("the walk left %s recorded as %s's fold", line, row.builtBy)
		}
	}
	t.Logf("after %s and %s, the third build's walk rewrote the %d stored rows another build had folded otherwise",
		first, "another-build", len(writtenFigures(state.figures, after.figures)))
}

// TestADayRowABuildWithNoFiguresWritesIsFoldedByTheNextPass: the release before the stored
// figures writes day rows and no figures, and its refresher's pass reads the refresher's own
// change cursor past the eval_days changes its writes log; the next pass of a build with the
// figures reads those changes from the figures' own cursor and folds the figures of the days they
// moved, and only those.
func TestADayRowABuildWithNoFiguresWritesIsFoldedByTheNextPass(t *testing.T) {
	r := newRefresherRig(t)
	deliveries, _ := r.seedHistories(t)
	r.settle(t)
	state := r.figureState(t)
	day := deliveries.To.AddDate(0, 0, -3)
	// The old release's write of one day row, as its refresher writes a value, and its pass's
	// read of the refresher's own cursor past the change the write logs.
	r.exec(t, "write a day row as a build with no figures does", `
		update eval_days set n = n + 1, sum = sum + 1, updated_at = now()
		where project_key = $1 and eval_id = $2 and series = $3 and repo = '' and day = $4`,
		deliveries.Project, EvalID(deliveries.Project, SlugDeployFrequency), measures.SeriesDeploys, day)
	r.exec(t, "move the change into the log", `select delivery_changes_drain(1000)`)
	r.exec(t, "read the refresher's own cursor past it", `
		update eval_days_cursor set position = head.seq, at = head.at
		from (select seq, at from delivery_changes order by seq desc limit 1) as head where name = $1`, changesCursor)
	stale := r.figureState(t)
	if reach, days := figureReach(state.days, stale.days, r.figuresFolded(t)); len(days) == 0 {
		t.Fatalf("the write moved no day row (%d figures reached)", len(reach))
	}
	truth := r.truthFigures(t)
	staleRows := 0
	for line, row := range truth {
		if stored, held := stale.figures[line]; !held || !stored.same(row) {
			staleRows++
		}
	}
	if staleRows == 0 {
		t.Fatal("the write left every stored figure the fold of the rows that stand, so nothing shows the next pass folds it")
	}
	t.Logf("the write of a build with no figures left %d stored rows stale", staleRows)
	r.pass(t)
	r.expectRewrites(t, "the next pass of a build with the figures", state, true, nil)
}

// TestTheMarkerHoldsTheReadOffUntilTheFirstWholeBuild: a read of the stored figures reads them only
// of a project whose figures are built (FiguresThrough). While the first pass's walks of the day
// rows have days left the figures walk waits, and no project is built; the figures walk then folds
// a batch of end days a pass, what it has folded is at every pass the fold of the rows that stand,
// and a project stands built only once its walk has folded every end day through today+2, which no
// project with rows does while its walk has batches left. A project made later is built at the next
// pass, holding no row. The marker then holds whichever build serves: a new build's first pass, a
// roll of the two, and a rollback to the first all read the figures as built.
func TestTheMarkerHoldsTheReadOffUntilTheFirstWholeBuild(t *testing.T) {
	r := newRefresherRig(t)
	r.batch = 2
	r.seedHistories(t)
	last := refresherToday.AddDate(0, 0, figureLead)
	heldOff := 0
	for passes := 1; passes <= 100; passes++ {
		_, next := r.pass(t)
		built := r.figuresBuilt(t)
		if passes == 1 && len(built) != 0 {
			t.Errorf("the first pass, whose walks of the day rows have days left, left figures built: %v", built)
		}
		expectTruth(t, fmt.Sprintf("pass %d", passes), r.storedFigures(t), r.truthFigures(t))
		walks, err := r.registry.readWalks(t.Context(), r.build)
		if err != nil {
			t.Fatalf("read the walks: %v", err)
		}
		for key, walk := range walks {
			if key.family != walkFigures || walk.next == nil || walk.next.After(last) {
				continue
			}
			if through, held := built[key.project]; held {
				t.Errorf("pass %d left %s's figures walk at %s with end days to fold, and its figures stand built through %s",
					passes, key.project, walk.next.Format(time.DateOnly), through.Format(time.DateOnly))
			}
			heldOff++
		}
		if !next.Equal(r.now) {
			break
		}
	}
	if heldOff == 0 {
		t.Error("no pass left a project's figures walk part of the way, so nothing shows a read held off while they are folded")
	}
	r.expectBuilt(t, "the first whole build", refresherToday)
	expectTruth(t, "the first whole build", r.storedFigures(t), r.truthFigures(t))

	r.exec(t, "make a project later", `insert into projects (key, name, default_assignee) values ('LATER', 'Later', 'alice')`)
	r.pass(t)
	r.expectBuilt(t, "a pass after a project is made", refresherToday)

	first := r.build
	r.build = "another-build"
	r.pass(t)
	r.expectBuilt(t, "a new build's first pass", refresherToday)
	r.build = first
	r.pass(t)
	r.expectBuilt(t, "a pass of the build before it, as in a roll or a rollback", refresherToday)
	r.build = "another-build"
	r.settle(t)
	r.build = first
	r.pass(t)
	r.expectBuilt(t, "a rollback once the new build's walk is done", refresherToday)
	expectTruth(t, "after the roll and the rollback", r.storedFigures(t), r.truthFigures(t))
}
