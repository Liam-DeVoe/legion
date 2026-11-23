package daemon

import (
	"context"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/appauth"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
	"github.com/sjawhar/legion/daemon/internal/prompts"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// The tmux runtime holds the App each role of the deployment acts as (tmux.Options.RoleApps), the
// reader its gh bundle function reads, so one walk of its panes builds a role of the deployment's
// bundle once per repository, over every issue whose plan does not define its name
// (appauth.NewBundles), and builds a role of a plan once for each App it acts as.
func TestTheTmuxRuntimeHoldsTheAppOfEachRoleOfTheDeployment(t *testing.T) {
	cfg := config.Config{Runtime: config.Runtime{Name: "tmux"}, Roles: []config.Role{
		{Name: "checker", Type: "testing", GitHubApp: config.RoleAppImplement},
		{Name: "critic", Type: "review", GitHubApp: config.RoleAppReview},
	}}
	want := appauth.DeploymentApps{"checker": appauth.Implement, "critic": appauth.Review}
	if got := tmuxOptions(cfg, "test", nil, "", "", quietLogger()).RoleApps.Deployed; !maps.Equal(got, want) {
		t.Errorf("tmux RoleApps.Deployed = %v, want %v", got, want)
	}
}

// The addressing of a role an owner's plan defines names the owners the App of its type reaches,
// as it does for every role: the owners that App was read installed on and the role's container
// holds a directory for (plan.appOwners), for the App the launch's one resolution of the claim's
// role names (resolveClaimRole) for the claim's own issue. The role is its issue's alone, so the
// same name on another issue, whose plan defines no such role, resolves to no App and its
// addressing names no owner.
func TestTheAddressingOfARoleOfAPlanNamesTheOwnersItsAppReaches(t *testing.T) {
	p := plan{repositories: newRepositories("LEGION", ghrepo.Renames{}), installed: appauth.NewInstalled(), left: appauth.NewLeftOut()}
	p.repositories.fill(ghrepo.MustParse("acme/widgets"))
	source := bothApps("acme")
	for _, app := range appauth.Roles {
		p.installed.RecordProof(app, source.installed[app])
	}
	planRole := func(_ context.Context, issue string, role claim.Role) (record.PlanRole, bool, error) {
		if issue == "LEGION-43" && role == "coder" {
			return record.PlanRole{Issue: issue, Role: "coder", Type: "implementation", Prompt: "# Coder\n\nWrite the change."}, true, nil
		}
		return record.PlanRole{}, false, nil
	}
	ctx := context.Background()
	for _, issue := range []string{"LEGION-43", "LEGION-44"} {
		resolved, err := resolveClaimRole(ctx, planRole, nil, nil, issue, "coder")
		if err != nil {
			t.Fatalf("resolve coder on %s: %v", issue, err)
		}
		want := map[string][]string{"LEGION-43": {"acme"}, "LEGION-44": nil}[issue]
		for _, runtimeName := range []string{"tmux", "kubernetes"} {
			if got := p.appOwners(runtimeName, nil)(ctx, resolved.app, "coder"); !slices.Equal(got, want) {
				t.Errorf("under %s, appOwners of coder on %s (App %q) = %q, want %q: the role is LEGION-43's alone", runtimeName, issue, resolved.app, got, want)
			}
		}
	}

	composer, err := prompts.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := specs{stateDir: t.TempDir(), project: "legion", prompts: composer, planRole: planRole,
		appOwners: p.appOwners("tmux", nil),
		repository: func(context.Context, string) (ghrepo.Repository, string, error) {
			return ghrepo.MustParse("acme/widgets"), "main", nil
		}}
	token, err := claim.NewToken("legion", "LEGION-43", "coder")
	if err != nil {
		t.Fatal(err)
	}
	spec, err := s.SpawnSpec(ctx, supervise.Claim{Token: token, Issue: "LEGION-43", Tree: "LEGION-42", Role: "coder"})
	if err != nil {
		t.Fatalf("the coder's spawn spec: %v", err)
	}
	if want := RepositoriesClause([]string{"acme"}); !strings.Contains(spec.Prompt.Addressing, want) {
		t.Errorf("the coder's addressing = %q, want it to hold %q", spec.Prompt.Addressing, want)
	}
}
