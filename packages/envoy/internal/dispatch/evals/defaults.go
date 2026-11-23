package evals

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/delivery"
)

// defaults.go publishes the projects' default repositories (eval_days_defaults, migration
// 20261117100006), the selection every read of a project's day rows makes among the owner keys'
// rows (owners.go): a person's write of a project's default repository
// (PUT /api/v1/projects/{key}/default-repository, projects.default_repo) moves no read and no view
// until the refresher's page that reads it publishes it, and that page's commit makes visible, with
// the new defaults, the stored figures of the projects it moves folded under them (the architect's
// rulings of 08:28Z (b) and 14:07Z). So a read in one snapshot answers the old defaults whole, their
// figures among them, or the new ones whole, and no read folds day rows because a figure is not
// folded yet. The move rewrites no day row and no member: every owner key's rows are written
// whatever the defaults are, its zero rows from its own first input day on.

// defaultsMove is what a page publishes of the projects' default repositories (syncDefaults):
// every project's default repository as it stands (live, by project), the projects whose published
// default it moves (moved, in key order), and the deploy repositories whose judging project, the
// delivery-on project whose deploy source reads the repository (DeployRepoEvals), it moves (judging,
// in key order): a source that names no repository reads its project's default.
type defaultsMove struct {
	live    map[string]string
	moved   []string
	judging []string
}

// syncDefaults reads the projects' default repositories and the published ones, and where they
// differ records in w what the page's commit publishes (publishDefaults). One statement, which
// reads every project's row: a few dozen at most.
func (r *refresh) syncDefaults(ctx context.Context, w *pageWork) error {
	pool := r.store.database.Pool
	live, published := map[string]string{}, map[string]string{}
	rows, err := pool.Query(ctx, `
		select 'live', key, default_repo from projects where default_repo is not null
		union all
		select 'published', project_key, repo from eval_days_defaults
	`)
	if err != nil {
		return fmt.Errorf("read the projects' default repositories: %w", err)
	}
	var side, project, repo string
	if _, err := pgx.ForEachRow(rows, []any{&side, &project, &repo}, func() error {
		if side == "live" {
			live[project] = repo
		} else {
			published[project] = repo
		}
		return nil
	}); err != nil {
		return fmt.Errorf("read the projects' default repositories: %w", err)
	}
	var moved []string
	for _, project := range slices.Sorted(maps.Keys(live)) {
		if was, held := published[project]; !held || was != live[project] {
			moved = append(moved, project)
		}
	}
	for _, project := range slices.Sorted(maps.Keys(published)) {
		if _, held := live[project]; !held {
			moved = append(moved, project)
		}
	}
	if len(moved) == 0 {
		return nil
	}
	slices.Sort(moved)
	settings, err := delivery.ListProjectSettings(ctx, pool)
	if err != nil {
		return err
	}
	w.defaults = &defaultsMove{live: live, moved: moved, judging: judgingMoves(settings, published, live)}
	return nil
}

// judgingMoves is the deploy repositories, in key order, whose judging project (DeployRepoEvals)
// moves from the settings under the published defaults (default repository by project) to those
// under the live ones: the delivery-on project whose deploy source reads the repository
// (delivery.DeployRules), a source that names no repository reading its project's default.
func judgingMoves(settings []delivery.ProjectSettings, published, live map[string]string) []string {
	judges := func(defaults map[string]string) map[string]string {
		projects := slices.Clone(settings)
		for i := range projects {
			projects[i].DefaultRepo = nil
			if repo, held := defaults[projects[i].Project]; held {
				projects[i].DefaultRepo = new(repo)
			}
		}
		judge := map[string]string{}
		for _, rule := range delivery.DeployRules(projects) {
			judge[ruleKey(rule.DeployRepo)] = rule.Project
		}
		return judge
	}
	was, now := judges(published), judges(live)
	var moved []string
	for repo, project := range now {
		if was[repo] != project {
			moved = append(moved, repo)
		}
	}
	for repo := range was {
		if _, held := now[repo]; !held {
			moved = append(moved, repo)
		}
	}
	slices.Sort(moved)
	return moved
}

// publishDefaults publishes, in tx, the page's commit, the default repositories move holds: every
// project's published default replaced by the one it stands at, then the stored figures of each
// project the move moves folded again over every day of its evals (foldFiguresInTx), so they fold
// the rows of the owner keys of its new default and none of its old one's, and then, for each
// moved project, one eval_days change of no eval (delivery.EvalDaysWrittenByDefaults), which marks
// its evals views, and, for each deploy repository whose judging project moved, one more
// (delivery.JudgingMoved), which marks the delivery views those evals judge. Every write of the
// commit lands together, so a read answers the old defaults and figures or the new ones.
func publishDefaults(ctx context.Context, tx pgx.Tx, build string, move *defaultsMove) error {
	projects := slices.Sorted(maps.Keys(move.live))
	repos := make([]string, len(projects))
	for i, project := range projects {
		repos[i] = move.live[project]
	}
	if _, err := tx.Exec(ctx, `delete from eval_days_defaults where not (project_key = any($1))`, projects); err != nil {
		return fmt.Errorf("publish the default repositories: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		insert into eval_days_defaults (project_key, repo)
		select given.project_key, given.repo from unnest($1::text[], $2::text[]) as given(project_key, repo)
		join projects p on p.key = given.project_key
		order by given.project_key collate "C"
		on conflict (project_key) do update set repo = excluded.repo where eval_days_defaults.repo is distinct from excluded.repo
	`, projects, repos); err != nil {
		return fmt.Errorf("publish the default repositories: %w", err)
	}
	for _, project := range move.moved {
		spans := make(map[string]daySpan, len(deliveryDaySlugs))
		for _, slug := range deliveryDaySlugs {
			spans[EvalID(project, slug)] = everyDay
		}
		if err := foldFiguresInTx(ctx, tx, build, project, spans); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `select delivery_change_log_pending($1, $2, $3, null, null, null)`,
			delivery.ChangeKindEvalDays, delivery.EvalDaysWrittenByDefaults, project); err != nil {
			return fmt.Errorf("log the default repository %s publishes: %w", project, err)
		}
	}
	for _, repo := range move.judging {
		if _, err := tx.Exec(ctx, `select delivery_change_log_pending($1, $2, $3, $4, null, null)`,
			delivery.ChangeKindEvalDays, delivery.EvalDaysWrittenByDefaults, repo, delivery.DefaultsJudgingRef); err != nil {
			return fmt.Errorf("log the project judging the delivery views of %s: %w", repo, err)
		}
	}
	return nil
}
