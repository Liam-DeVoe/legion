package evals

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/delivery"
	"github.com/sjawhar/envoy/internal/dispatch/delivery/deliverytest"
	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// The checks of the refresher's answer to a change of the delivery settings (settings_rewrite.go):
// one pass rewrites what the change moves as the whole delivery walk would write it, and begins no
// walk; a change that moves no eval writes nothing; and the members write sends only the members
// that moved.

// settingsApplyStep is the apply step newSettingsRig records on the production jobs.
const settingsApplyStep = "terraform apply"

// newSettingsRig is a refresher over a database of its own loaded with deliverytest's history of
// five days and settled by its passes, each walking batch days. Every production job that succeeded
// records an apply step, settingsApplyStep, which succeeded but on the runs whose id ends in 3,
// where it was skipped; no setting names the step, so each job counts by its own conclusion.
func newSettingsRig(t *testing.T, batch int) (*refresherRig, deliverytest.Fixture) {
	t.Helper()
	r := newRefresherRig(t)
	r.batch = batch
	deliveries := deliverytest.Seed(t, r.database.Pool, deliverytest.Spec{Days: 5})
	r.exec(t, "record the production jobs' apply step", `
		update delivery_run_jobs
		set apply_step = $2, apply_step_conclusion = case when run_id % 10 = 3 then 'skipped' else 'success' end
		where repo = $1 and name = any($3) and conclusion = 'success'
	`, deliveries.DeployRepo, settingsApplyStep, deliveries.Settings.ProductionJobNames)
	r.settle(t)
	return r, deliveries
}

// putSettings saves settings as actor, as PUT /api/v1/settings/delivery does.
func (r *refresherRig) putSettings(t *testing.T, settings delivery.DeliverySettings, actor model.Actor) {
	t.Helper()
	if _, err := delivery.PutSettings(t.Context(), r.database.Pool, settings, actor); err != nil {
		t.Fatalf("save the delivery settings: %v", err)
	}
}

// isDeliveryEval reports whether evalID is a delivery or CI eval's, one a delivery day writes.
func isDeliveryEval(evalID string) bool {
	_, slug, _ := strings.Cut(evalID, "/")
	return slices.Contains(deliveryDaySlugs, slug)
}

// deliveryValues is every stored row of a delivery or CI eval, by rowKey, without the moment and
// the build of its last write: what two databases' rows compare by.
func (r *refresherRig) deliveryValues(t *testing.T) map[string]DayRow {
	t.Helper()
	values := map[string]DayRow{}
	for key, row := range r.dayRows(t) {
		if isDeliveryEval(row.EvalID) {
			row.UpdatedAt, row.BuiltBy = time.Time{}, ""
			values[key] = row
		}
	}
	return values
}

// memberLines is every eval_day_members row, `<project> <family> <row key>` mapped to
// `<day> <repo> <shipped by>`.
func (r *refresherRig) memberLines(t *testing.T) map[string]string {
	t.Helper()
	rows, err := r.database.Pool.Query(t.Context(), `
		select project_key || ' ' || family || ' ' || row_key, day::text || ' ' || repo || ' ' || shipped_by from eval_day_members`)
	if err != nil {
		t.Fatalf("read the members: %v", err)
	}
	members := map[string]string{}
	var key, at string
	if _, err := pgx.ForEachRow(rows, []any{&key, &at}, func() error {
		members[key] = at
		return nil
	}); err != nil {
		t.Fatalf("read the members: %v", err)
	}
	return members
}

// deliveryFigureValues is the fold key and figures of each stored figures row of a delivery or CI
// eval, by its line: what two databases' figures compare by, since the oldest updated_at and the
// stamp a row records are each database's own moments.
func deliveryFigureValues(stored map[string]figureRow) map[string]string {
	values := map[string]string{}
	for line, row := range stored {
		if isDeliveryEval(line[strings.LastIndex(line, " ")+1:]) {
			values[line] = fmt.Sprintf("%d %x", row.foldKey, row.figures)
		}
	}
	return values
}

// xmins is the transaction that last wrote each row query reads, by the row's name: the query's
// first column, its xmin the second.
func (r *refresherRig) xmins(t *testing.T, query string) map[string]string {
	t.Helper()
	rows, err := r.database.Pool.Query(t.Context(), query)
	if err != nil {
		t.Fatalf("read %q: %v", query, err)
	}
	written := map[string]string{}
	var name, xmin string
	if _, err := pgx.ForEachRow(rows, []any{&name, &xmin}, func() error {
		written[name] = xmin
		return nil
	}); err != nil {
		t.Fatalf("read %q: %v", query, err)
	}
	return written
}

// walkXmin is the transaction that last wrote the rig's build's record of the delivery walk, which
// a walk begun again rewrites.
func (r *refresherRig) walkXmin(t *testing.T) string {
	t.Helper()
	var xmin string
	if err := r.database.Pool.QueryRow(t.Context(), `
		select xmin::text from eval_days_walks where family = $1 and project_key = '' and built_by = $2
	`, walkDelivery, r.build).Scan(&xmin); err != nil {
		t.Fatalf("read the delivery walk's record: %v", err)
	}
	return xmin
}

// movedEvals is each eval with a row that appeared, went, or whose updated_at moved from before
// to after: a row whose values moved.
func movedEvals(before, after map[string]DayRow) []string {
	moved := map[string]bool{}
	for key, row := range after {
		if old, held := before[key]; !held || !old.UpdatedAt.Equal(row.UpdatedAt) {
			moved[row.EvalID] = true
		}
	}
	for key, row := range before {
		if _, kept := after[key]; !kept {
			moved[row.EvalID] = true
		}
	}
	return slices.Sorted(maps.Keys(moved))
}

// changedEvals is how many eval_days changes each eval logged after seq.
func (r *refresherRig) changedEvals(t *testing.T, seq int64) map[string]int {
	t.Helper()
	changed := map[string]int{}
	for _, line := range r.evalDayChanges(t, seq) {
		evalID, _, _ := strings.Cut(line, " ")
		changed[evalID]++
	}
	return changed
}

// sameValues answers, sorted, the keys a and b do not hold alike, each with show of both sides,
// the first ten of them.
func sameValues[V any](a, b map[string]V, show func(V) string) []string {
	keys := map[string]bool{}
	for key := range a {
		keys[key] = true
	}
	for key := range b {
		keys[key] = true
	}
	var differ []string
	for _, key := range slices.Sorted(maps.Keys(keys)) {
		one, inA := a[key]
		other, inB := b[key]
		if inA != inB || !reflect.DeepEqual(one, other) {
			left, right := "none", "none"
			if inA {
				left = show(one)
			}
			if inB {
				right = show(other)
			}
			differ = append(differ, fmt.Sprintf("%s: %s, want %s", key, left, right))
		}
	}
	return differ[:min(len(differ), 10)]
}

// expectSameAsWalk checks that r holds the delivery and CI day rows, the members and the stored
// figures walk holds, and that r's stored figures are the fold of its rows.
func (r *refresherRig) expectSameAsWalk(t *testing.T, what string, walk *refresherRig) {
	t.Helper()
	if differ := sameValues(r.deliveryValues(t), walk.deliveryValues(t), rowFacts); len(differ) > 0 {
		t.Errorf("%s: the day rows differ from the whole walk's, first ones:\n\t%s", what, strings.Join(differ, "\n\t"))
	}
	if differ := sameValues(r.memberLines(t), walk.memberLines(t), func(at string) string { return at }); len(differ) > 0 {
		t.Errorf("%s: the members differ from the whole walk's, first ones:\n\t%s", what, strings.Join(differ, "\n\t"))
	}
	stored := r.storedFigures(t)
	expectTruth(t, what, stored, r.truthFigures(t))
	if differ := sameValues(deliveryFigureValues(stored), deliveryFigureValues(walk.storedFigures(t)), func(figures string) string { return figures }); len(differ) > 0 {
		t.Errorf("%s: the stored figures differ from the whole walk's, first ones:\n\t%s", what, strings.Join(differ, "\n\t"))
	}
}

// TestASettingsChangeRewritesInOnePassWhatAWholeWalkWrites: two databases load one history and
// settle alike, the first walking two days a pass, so a walk begun again would leave days for later
// passes. Each change of a setting k8's phase D measured ("What each setting moves") is saved in
// both, in turn: a staging gate renamed to a job no run holds and then to one every run holds, and
// back; the staging deploy job renamed and back; the production job named as the staging gate's
// job, which moves the staging gate too (a name two cards follow counts for the first), and back;
// the apply step named, and cleared; and the deploy repository moved to one with pull requests and
// no run, and back. One pass of the first then rewrites what the change moves and begins no walk,
// while the second begins its delivery walk again from no record of the settings its rows stand
// built under, and walks every day. After each, the two hold the same delivery and CI day rows,
// the same members and the same stored figures, the first's the fold of its rows; every eval whose
// rows moved is one settingsMoves names; and each has exactly one eval_days change of the pass,
// and no other eval one, so an evals view hears of the change once.
func TestASettingsChangeRewritesInOnePassWhatAWholeWalkWrites(t *testing.T) {
	rewriting, deliveries := newSettingsRig(t, 2)
	walking, _ := newSettingsRig(t, walkBatch)
	compare := func(what string) {
		t.Helper()
		rewriting.expectSameAsWalk(t, what, walking)
	}
	compare("the first whole walk")

	original := deliveries.Settings
	api := deliveries.Repos[1]
	steps := []struct {
		name   string
		change func(*delivery.DeliverySettings)
	}{
		{"the staging gate renamed to a job no run holds", func(s *delivery.DeliverySettings) { s.StagingE2EJobName = new("staging-e2e / run-v2") }},
		{"the staging gate named as a job every run holds", func(s *delivery.DeliverySettings) { s.StagingE2EJobName = new("build / image") }},
		{"the staging gate named back", func(s *delivery.DeliverySettings) { s.StagingE2EJobName = original.StagingE2EJobName }},
		{"the staging deploy job renamed", func(s *delivery.DeliverySettings) { s.StagingApplyJobName = new("staging-release / apply-v2") }},
		{"the staging deploy job named back", func(s *delivery.DeliverySettings) { s.StagingApplyJobName = original.StagingApplyJobName }},
		{"the production job named as the staging gate's", func(s *delivery.DeliverySettings) {
			s.ProductionJobNames = []string{*original.StagingE2EJobName}
		}},
		{"the production job named back", func(s *delivery.DeliverySettings) { s.ProductionJobNames = original.ProductionJobNames }},
		{"the apply step named", func(s *delivery.DeliverySettings) { s.ProductionApplyStepName = new(settingsApplyStep) }},
		{"the apply step cleared", func(s *delivery.DeliverySettings) { s.ProductionApplyStepName = nil }},
		{"the deploy repository moved to one with no run", func(s *delivery.DeliverySettings) { s.DeployRepo = api }},
		{"the deploy repository moved back", func(s *delivery.DeliverySettings) { s.DeployRepo = original.DeployRepo }},
	}
	settings := original
	for _, step := range steps {
		prev := settings
		step.change(&settings)
		slugs, _ := settingsMoves(prev, settings)
		rewriting.putSettings(t, settings, alice)
		walking.putSettings(t, settings, alice)

		seq, walk, before := rewriting.latestSeq(t), rewriting.walkXmin(t), rewriting.dayRows(t)
		if _, next := rewriting.pass(t); next.Equal(rewriting.now) {
			t.Errorf("%s: the pass asked for another at once, which a walk with days left does", step.name)
		}
		if rewriting.walkXmin(t) != walk {
			t.Errorf("%s: the pass began the delivery walk again", step.name)
		}
		moved, changed := movedEvals(before, rewriting.dayRows(t)), rewriting.changedEvals(t, seq)
		if len(moved) == 0 {
			t.Errorf("%s: no eval's rows moved, and the change moves %v", step.name, slugs)
		}
		for _, evalID := range moved {
			if _, slug, _ := strings.Cut(evalID, "/"); !slices.Contains(slugs, slug) {
				t.Errorf("%s: %s's rows moved, and the change moves %v alone", step.name, evalID, slugs)
			}
		}
		if got := slices.Sorted(maps.Keys(changed)); !slices.Equal(got, moved) || slices.ContainsFunc(got, func(evalID string) bool { return changed[evalID] != 1 }) {
			t.Errorf("%s: the pass logged the eval_days changes %v, want one of each eval whose rows moved, %v", step.name, changed, moved)
		}

		walking.exec(t, "forget the settings the rows stand built under", `delete from eval_days_settings`)
		walking.settle(t)
		compare(step.name)
		t.Logf("%s: one pass rewrote %v, one eval_days change each", step.name, moved)
	}
}

// TestASettingsChangeThatMovesNoEvalWritesNothing: over a settled history, each change of a setting
// no day row reads (the two workflow paths, the population rule's authors and excluded
// repositories, the roll-up job), and a production job name no run holds, listed beside the one
// that is, logs its settings change, and the pass after it writes no day row, member or stored
// figure, logs no eval_days change, and begins no walk. The same settings saved again by another
// person log no change at all (migration 20261120111107), and the pass after them writes nothing
// either.
func TestASettingsChangeThatMovesNoEvalWritesNothing(t *testing.T) {
	r, deliveries := newSettingsRig(t, walkBatch)
	settings := deliveries.Settings
	for _, change := range []struct {
		name   string
		change func(*delivery.DeliverySettings)
		actor  model.Actor
		logs   bool
	}{
		{"the deploy workflow's path", func(s *delivery.DeliverySettings) { s.DeployWorkflowPath = ".github/workflows/deploy-next.yml" }, alice, true},
		{"the PR-checks workflow's path", func(s *delivery.DeliverySettings) { s.PRChecksWorkflowPath = ".github/workflows/pr-checks-next.yml" }, alice, true},
		{"one population author fewer", func(s *delivery.DeliverySettings) {
			s.PopulationAuthors = s.PopulationAuthors[:len(s.PopulationAuthors)-1]
		}, alice, true},
		{"one excluded repository more", func(s *delivery.DeliverySettings) { s.ExcludedRepos = []string{"acme/elsewhere"} }, alice, true},
		{"the roll-up job renamed", func(s *delivery.DeliverySettings) { s.PRChecksRollupJobName = new("checks-complete-v2") }, alice, true},
		{"a production job name no run holds", func(s *delivery.DeliverySettings) {
			s.ProductionJobNames = append(slices.Clone(s.ProductionJobNames), "production-release / apply-v2")
		}, alice, true},
		{"the same settings saved by another person", func(*delivery.DeliverySettings) {}, implementer, false},
	} {
		change.change(&settings)
		seq, walk := r.latestSeq(t), r.walkXmin(t)
		rows := r.xmins(t, `select project_key || ' ' || eval_id || ' ' || series || ' ' || repo || ' ' || day::text, xmin::text from eval_days`)
		members := r.xmins(t, `select project_key || ' ' || family || ' ' || row_key, xmin::text from eval_day_members`)
		figures := map[string]string{}
		for line, row := range r.storedFigures(t) {
			figures[line] = row.xmin
		}
		r.putSettings(t, settings, change.actor)
		changes, err := delivery.ChangesSince(t.Context(), r.database.Pool, seq, 100)
		if err != nil {
			t.Fatalf("read the changes after %d: %v", seq, err)
		}
		logged := slices.ContainsFunc(changes, func(c delivery.Change) bool { return c.Kind == delivery.ChangeKindSettings })
		if logged != change.logs {
			t.Errorf("%s: a settings change logged is %t, want %t (%+v)", change.name, logged, change.logs, changes)
		}

		r.pass(t)
		show := func(xmin string) string { return "written by " + xmin }
		if differ := sameValues(r.xmins(t, `select project_key || ' ' || eval_id || ' ' || series || ' ' || repo || ' ' || day::text, xmin::text from eval_days`), rows, show); len(differ) > 0 {
			t.Errorf("%s: the pass wrote day rows, first ones:\n\t%s", change.name, strings.Join(differ, "\n\t"))
		}
		if differ := sameValues(r.xmins(t, `select project_key || ' ' || family || ' ' || row_key, xmin::text from eval_day_members`), members, show); len(differ) > 0 {
			t.Errorf("%s: the pass wrote members, first ones:\n\t%s", change.name, strings.Join(differ, "\n\t"))
		}
		after := map[string]string{}
		for line, row := range r.storedFigures(t) {
			after[line] = row.xmin
		}
		if differ := sameValues(after, figures, show); len(differ) > 0 {
			t.Errorf("%s: the pass wrote stored figures, first ones:\n\t%s", change.name, strings.Join(differ, "\n\t"))
		}
		if changed := r.evalDayChanges(t, seq); len(changed) > 0 {
			t.Errorf("%s: the pass logged the eval_days changes %v, want none", change.name, changed)
		}
		if r.walkXmin(t) != walk {
			t.Errorf("%s: the pass began the delivery walk again", change.name)
		}
	}
}

// TestAProductionRuleChangeSendsOnlyTheMembersItMoves: over a settled history, the apply step
// named moves the deploys whose step was skipped out of production, so the pull requests they
// shipped ship with a later deploy and those deploys count for other projects; the rewrite sends
// eval_day_members (as a trigger of the test's own database records each row an insert or a
// delete is handed) exactly the members whose day, repository or shipping run moved, or that are
// new, and deletes exactly those the days no longer hold: none of the members that stand.
func TestAProductionRuleChangeSendsOnlyTheMembersItMoves(t *testing.T) {
	r, deliveries := newSettingsRig(t, walkBatch)
	if owed := r.count(t, `select count(*) from eval_days_owed`); owed != 0 {
		t.Fatalf("the settled history owes %d days a rewrite, which the pass would write with their members", owed)
	}
	for _, statement := range []string{
		`create table evals_test_members_sent (op text not null, member text not null)`,
		`create function evals_test_members_sent() returns trigger language plpgsql as $$
		begin
			if tg_op = 'DELETE' then
				insert into evals_test_members_sent values ('delete', old.project_key || ' ' || old.family || ' ' || old.row_key);
				return old;
			end if;
			insert into evals_test_members_sent values ('insert', new.project_key || ' ' || new.family || ' ' || new.row_key);
			return new;
		end $$`,
		`create trigger evals_test_members_sent before insert or delete on eval_day_members
			for each row execute function evals_test_members_sent()`,
	} {
		r.exec(t, "record the members the writes are handed", statement)
	}
	before := r.memberLines(t)
	settings := deliveries.Settings
	settings.ProductionApplyStepName = new(settingsApplyStep)
	r.putSettings(t, settings, alice)
	r.pass(t)
	after := r.memberLines(t)

	sent := map[string][]string{}
	rows, err := r.database.Pool.Query(t.Context(), `select op, member from evals_test_members_sent`)
	if err != nil {
		t.Fatalf("read the members sent: %v", err)
	}
	var op, member string
	if _, err := pgx.ForEachRow(rows, []any{&op, &member}, func() error {
		sent[op] = append(sent[op], member)
		return nil
	}); err != nil {
		t.Fatalf("read the members sent: %v", err)
	}
	var moved, gone []string
	standing := 0
	for key, at := range after {
		if was, held := before[key]; !held || was != at {
			moved = append(moved, key)
		} else {
			standing++
		}
	}
	for key := range before {
		if _, kept := after[key]; !kept {
			gone = append(gone, key)
		}
	}
	slices.Sort(moved)
	slices.Sort(gone)
	slices.Sort(sent["insert"])
	slices.Sort(sent["delete"])
	if !slices.Equal(sent["insert"], moved) {
		t.Errorf("the rewrite sent %d members to its upsert, want the %d that moved:\n\tsent %v\n\tmoved %v",
			len(sent["insert"]), len(moved), sent["insert"][:min(len(sent["insert"]), 10)], moved[:min(len(moved), 10)])
	}
	if !slices.Equal(sent["delete"], gone) {
		t.Errorf("the rewrite deleted the members %v, want those its days no longer hold, %v", sent["delete"], gone)
	}
	if len(moved) == 0 || standing <= len(moved) {
		t.Errorf("the apply step moved %d members and left %d standing, want some moved and more standing", len(moved), standing)
	}
	t.Logf("the apply step moved %d, deleted %d and left %d of %d members standing; the rewrite sent the moved and the deleted alone",
		len(moved), len(gone), standing, len(before))
}

// walkAsBuildBefore writes every day of the delivery walk under settings as the release before this
// one does during a roll: as another build, through the refresher's own write of a day, with no
// stored figure folded and no settings recorded.
func (r *refresherRig) walkAsBuildBefore(t *testing.T, settings delivery.DeliverySettings) {
	t.Helper()
	ctx := t.Context()
	before := r.registry.newRefresh("the-build-before", r.now, walkBatch)
	projects, err := r.registry.Projects(ctx)
	if err != nil {
		t.Fatalf("read the projects: %v", err)
	}
	before.projects, before.settings, before.walking = projects, &settings, true
	before.beginPage()
	start, err := before.deliveryStart(ctx)
	if err != nil || start == nil {
		t.Fatalf("read the delivery walk's first day: %v (%v)", start, err)
	}
	for day := *start; !day.After(before.today); day = day.AddDate(0, 0, 1) {
		if err := before.deliveryWalkDay(ctx, day); err != nil {
			t.Fatalf("write %s as the build before: %v", day.Format(time.DateOnly), err)
		}
	}
}

// TestRowsTheBuildBeforeWroteUnderAnInBetweenSettingAreRewritten: in a roll, the release before
// this one writes day rows under the settings its own pass read and records none. Two databases
// load one history and settle alike under S0; on each, a pass of the build before reads S1 (the
// production job named as the staging gate's), whose change it never reads, and walks every day
// under it; then S2 is saved, S0 again. The next pass of this release on the first reads both
// changes, and its record, S0, agrees with S2, so a comparison of settings finds nothing; another
// build moved rows since the record, though, so it rewrites every eval a setting can move. The
// first then holds the same day rows, members and stored figures as the second after a whole walk
// under S2 over the same rows (each row the build before made and S2 does not hold stays, as a
// zero row, as every walk keeps it).
func TestRowsTheBuildBeforeWroteUnderAnInBetweenSettingAreRewritten(t *testing.T) {
	rewriting, deliveries := newSettingsRig(t, walkBatch)
	walking, _ := newSettingsRig(t, walkBatch)
	original := deliveries.Settings
	between := original
	between.ProductionJobNames = []string{*original.StagingE2EJobName}
	underS0 := rewriting.deliveryValues(t)
	for _, r := range []*refresherRig{rewriting, walking} {
		r.putSettings(t, between, alice)
		r.walkAsBuildBefore(t, between)
	}
	if differ := sameValues(rewriting.deliveryValues(t), underS0, rowFacts); len(differ) == 0 {
		t.Fatal("the build before wrote no row under S1 that differs from S0's")
	}

	rewriting.putSettings(t, original, alice)
	walking.putSettings(t, original, alice)
	rewriting.pass(t)
	walking.exec(t, "forget the settings the rows stand built under", `delete from eval_days_settings`)
	walking.settle(t)
	rewriting.expectSameAsWalk(t, "S0, S1 written by the build before, then S2 = S0", walking)
}
