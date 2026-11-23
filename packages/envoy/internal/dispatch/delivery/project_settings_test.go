package delivery

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

var settingsAuthor = model.Actor{Kind: "user", ID: "alice@example.com"}

// settingsStore is a store holding projects ACME (default acme/widgets), BETA (default acme/beta)
// and BARE (no default repository).
func settingsStore(t *testing.T) (*store.Pool, context.Context) {
	t.Helper()
	database := storetest.Open(t)
	ctx := t.Context()
	if _, err := database.Pool.Exec(ctx, `insert into projects (key, name, default_assignee, default_repo) values
		('ACME', 'Acme', 'alice', 'acme/widgets'), ('BETA', 'Beta', 'alice', 'acme/beta'), ('BARE', 'Bare', 'alice', null)`); err != nil {
		t.Fatalf("seed the projects: %v", err)
	}
	return database.Pool, ctx
}

func workflowSource(repo *string, workflow string) SourceInput {
	return SourceInput{
		Kind: SourceKindGitHubWorkflow, Repo: repo, DeployWorkflowPath: new(workflow),
		ProductionJobNames: []string{"production"}, PRChecksWorkflowPath: new(".github/workflows/ci.yaml"),
	}
}

func save(t *testing.T, ctx context.Context, pool *store.Pool, input ProjectSettingsInput) (ProjectSettings, error) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	saved, err := PutProjectSettings(ctx, tx, input, settingsAuthor)
	if err != nil {
		return ProjectSettings{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return saved, nil
}

func refusedWith(t *testing.T, err error, code string) *SettingsError {
	t.Helper()
	var refusal *SettingsError
	if !errors.As(err, &refusal) || refusal.Code != code {
		t.Fatalf("got %v, want a %s refusal", err, code)
	}
	return refusal
}

// Each project keeps its own settings and sources: ACME reads two repositories, one it names and
// its default through a source that names none, and BETA one; DeployRules gives each repository its
// own rule and owner, and a project with delivery off gives none.
func TestEachProjectKeepsItsOwnDeliverySettings(t *testing.T) {
	pool, ctx := settingsStore(t)
	since := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	acme, err := save(t, ctx, pool, ProjectSettingsInput{
		Project: "ACME", Delivery: true,
		Sources:           []SourceInput{workflowSource(new("acme/deploys"), "deploy.yaml"), workflowSource(nil, "release.yaml")},
		PopulationAuthors: []string{"alice"}, FailureSources: []string{FailureSourceReverts}, FailuresMeasuredSince: &since,
	})
	if err != nil {
		t.Fatalf("save ACME: %v", err)
	}
	if _, err := save(t, ctx, pool, ProjectSettingsInput{Project: "BETA", Delivery: true, Sources: []SourceInput{workflowSource(nil, "ship.yaml")}}); err != nil {
		t.Fatalf("save BETA: %v", err)
	}
	if _, err := save(t, ctx, pool, ProjectSettingsInput{Project: "BARE", Delivery: false}); err != nil {
		t.Fatalf("save BARE off: %v", err)
	}
	if len(acme.Sources) != 2 || acme.Sources[0].Repo == nil || *acme.Sources[0].Repo != "acme/deploys" || acme.Sources[1].Repo != nil {
		t.Fatalf("ACME's sources read back %+v, want acme/deploys then one naming none", acme.Sources)
	}
	if acme.FailuresMeasuredSince == nil || !acme.FailuresMeasuredSince.Equal(since) || !slices.Equal(acme.FailureSources, []string{FailureSourceReverts}) {
		t.Errorf("ACME's failure sources read back %v since %v", acme.FailureSources, acme.FailuresMeasuredSince)
	}
	all, err := ListProjectSettings(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, rule := range DeployRules(all) {
		got = append(got, rule.DeployRepo+" "+rule.Project+" "+rule.DeployWorkflowPath)
	}
	want := []string{"acme/beta BETA ship.yaml", "acme/deploys ACME deploy.yaml", "acme/widgets ACME release.yaml"}
	if !slices.Equal(got, want) {
		t.Errorf("DeployRules = %v, want %v", got, want)
	}
	if _, err := GetProjectSettings(ctx, pool, "NONE"); !errors.Is(err, ErrNoSettings) {
		t.Errorf("a project with no row answered %v, want ErrNoSettings", err)
	}
}

// A repository is the source of one delivery-on project at most, and of one source of it: a save
// naming another delivery-on project's repository, by name or through its default, is refused with
// that project, and so is a project naming one repository twice; with the other project off, the
// repository is free.
func TestASaveThatWouldShareADeployRepositoryIsRefused(t *testing.T) {
	pool, ctx := settingsStore(t)
	if _, err := save(t, ctx, pool, ProjectSettingsInput{Project: "ACME", Delivery: true, Sources: []SourceInput{workflowSource(nil, "d.yaml")}}); err != nil {
		t.Fatalf("save ACME: %v", err)
	}
	_, err := save(t, ctx, pool, ProjectSettingsInput{Project: "BETA", Delivery: true, Sources: []SourceInput{workflowSource(new("Acme/Widgets"), "d.yaml")}})
	if refusal := refusedWith(t, err, CodeDeliveryRepositoryShared); refusal.Project != "ACME" || refusal.Source == nil || *refusal.Source != 0 {
		t.Errorf("the refusal names project %q and source %v, want ACME and 0", refusal.Project, refusal.Source)
	}
	_, err = save(t, ctx, pool, ProjectSettingsInput{Project: "BETA", Delivery: true,
		Sources: []SourceInput{workflowSource(new("acme/beta"), "a.yaml"), workflowSource(nil, "b.yaml")}})
	refusedWith(t, err, CodeDeliverySettingsInput)
	if _, err := save(t, ctx, pool, ProjectSettingsInput{Project: "ACME", Delivery: false, Sources: []SourceInput{workflowSource(nil, "d.yaml")}}); err != nil {
		t.Fatalf("turn ACME off: %v", err)
	}
	if _, err := save(t, ctx, pool, ProjectSettingsInput{Project: "BETA", Delivery: true, Sources: []SourceInput{workflowSource(new("acme/widgets"), "d.yaml")}}); err != nil {
		t.Errorf("BETA naming acme/widgets with ACME off was refused: %v", err)
	}
}

// Delivery on with a github_workflow source that names no repository needs a default repository
// (R2); a source that names one does not, and delivery off needs neither.
func TestDeliveryOnNeedsARepositoryForEverySource(t *testing.T) {
	pool, ctx := settingsStore(t)
	_, err := save(t, ctx, pool, ProjectSettingsInput{Project: "BARE", Delivery: true, Sources: []SourceInput{workflowSource(nil, "d.yaml")}})
	refusedWith(t, err, CodeDeliveryNoDefaultRepository)
	if _, err := save(t, ctx, pool, ProjectSettingsInput{Project: "BARE", Delivery: false, Sources: []SourceInput{workflowSource(nil, "d.yaml")}}); err != nil {
		t.Errorf("BARE off with a source naming none was refused: %v", err)
	}
	if _, err := save(t, ctx, pool, ProjectSettingsInput{Project: "BARE", Delivery: true, Sources: []SourceInput{workflowSource(new("bare/deploys"), "d.yaml")}}); err != nil {
		t.Errorf("BARE on with a source naming its repository was refused: %v", err)
	}
	_, err = save(t, ctx, pool, ProjectSettingsInput{Project: "BARE", Delivery: true})
	refusedWith(t, err, CodeDeliverySettingsInput)
}

// A save keeps a source whose repository and rule are unchanged, with its id, its bookkeeping and
// its repository's reconcile progress, and clears the progress of a repository whose rule moved.
func TestASaveKeepsAnUnchangedSourceAndClearsAChangedOnesProgress(t *testing.T) {
	pool, ctx := settingsStore(t)
	first, err := save(t, ctx, pool, ProjectSettingsInput{Project: "ACME", Delivery: true,
		Sources: []SourceInput{workflowSource(new("acme/deploys"), "deploy.yaml"), workflowSource(nil, "release.yaml")}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `update project_deploy_sources set last_event_at = '2026-11-22T10:00:00Z' where project_key = 'ACME'`); err != nil {
		t.Fatal(err)
	}
	for _, repo := range []string{"acme/deploys", "acme/widgets"} {
		workflow := map[string]string{"acme/deploys": "deploy.yaml", "acme/widgets": "release.yaml"}[repo]
		if err := RecordReconcileProgress(ctx, pool, repoStep("runs/deploy", repo), repo+" "+workflow, time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC)); err != nil {
			t.Fatal(err)
		}
	}
	second, err := save(t, ctx, pool, ProjectSettingsInput{Project: "ACME", Delivery: true,
		Sources: []SourceInput{workflowSource(nil, "release-v2.yaml"), workflowSource(new("acme/deploys"), "deploy.yaml")}})
	if err != nil {
		t.Fatal(err)
	}
	if second.Sources[1].ID != first.Sources[0].ID || second.Sources[1].LastEventAt == nil {
		t.Errorf("the unchanged source is %+v, want the stored row %s with its bookkeeping", second.Sources[1], first.Sources[0].ID)
	}
	if second.Sources[0].ID == first.Sources[1].ID || second.Sources[0].LastEventAt != nil {
		t.Errorf("the changed source kept the old row %+v", second.Sources[0])
	}
	kept, err := ReconcileProgressThrough(ctx, pool, repoStep("runs/deploy", "acme/deploys"), "acme/deploys deploy.yaml")
	if err != nil || kept.IsZero() {
		t.Errorf("the unchanged repository's progress = %v (%v), want it kept", kept, err)
	}
	cleared, err := ReconcileProgressThrough(ctx, pool, repoStep("runs/deploy", "acme/widgets"), "acme/widgets release.yaml")
	if err != nil || !cleared.IsZero() {
		t.Errorf("the changed repository's progress = %v (%v), want it cleared", cleared, err)
	}
}

// A source is reconciled through the earliest of its repository's two run listings, and through
// nothing until both have a row: a reader waiting for the import does not read a repository whose
// pull-request checks have not been listed yet.
func TestReconciledThroughWaitsForEveryStepOfTheRepository(t *testing.T) {
	pool, ctx := settingsStore(t)
	rule := DeliverySettings{DeployRepo: "acme/deploys", DeployWorkflowPath: "deploy.yaml", PRChecksWorkflowPath: "ci.yaml"}
	if through, err := ReconciledThrough(ctx, pool, rule); err != nil || through != nil {
		t.Fatalf("with no progress: %v (%v), want nil", through, err)
	}
	deployed, checked := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC), time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)
	if err := RecordReconcileProgress(ctx, pool, repoStep(runsStep(DeliveryRunKindDeploy), rule.DeployRepo), "acme/deploys deploy.yaml", deployed); err != nil {
		t.Fatal(err)
	}
	if through, err := ReconciledThrough(ctx, pool, rule); err != nil || through != nil {
		t.Fatalf("with the deploy listing alone: %v (%v), want nil", through, err)
	}
	if err := RecordReconcileProgress(ctx, pool, repoStep(runsStep(DeliveryRunKindPRChecks), rule.DeployRepo), "acme/deploys ci.yaml", checked); err != nil {
		t.Fatal(err)
	}
	if through, err := ReconciledThrough(ctx, pool, rule); err != nil || through == nil || !through.Equal(checked) {
		t.Fatalf("with both listings: %v (%v), want the earlier, %v", through, err, checked)
	}
}

// A source whose pull-request checks run in its deploy workflow has one listing, which the
// reconcile records under the deploy step alone, so that one row is how far it is reconciled.
func TestReconciledThroughOfOneWorkflowWaitsForItsOneListing(t *testing.T) {
	pool, ctx := settingsStore(t)
	rule := DeliverySettings{DeployRepo: "acme/one", DeployWorkflowPath: "ci.yaml", PRChecksWorkflowPath: "ci.yaml"}
	listed := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	if err := RecordReconcileProgress(ctx, pool, repoStep(runsStep(DeliveryRunKindDeploy), rule.DeployRepo), "acme/one ci.yaml", listed); err != nil {
		t.Fatal(err)
	}
	if through, err := ReconciledThrough(ctx, pool, rule); err != nil || through == nil || !through.Equal(listed) {
		t.Fatalf("one workflow listed through %v: %v (%v)", listed, through, err)
	}
}
