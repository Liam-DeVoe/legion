package daemon

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/fixtureenv"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// A pane commits as its role's App, minted for the owner of the repository its issue works: the
// launch carries the six git identity variables every worker pane gets and the table of every
// role's identity (runtime.RoleIdentitiesVariable), asked once per App, the claim's own first,
// each for that same repository, nothing else of the Apps, and a launch whose own identity cannot
// be resolved does not happen.
func TestSpawnSpecCarriesTheRoleAppIdentity(t *testing.T) {
	stateDir := t.TempDir()
	token, err := claim.NewToken("s1", "S1-1", claim.RoleImplementer)
	if err != nil {
		t.Fatal(err)
	}
	c := supervise.Claim{Token: token, Issue: "S1-1", Tree: "S1-1", Role: claim.RoleImplementer}
	if err := os.MkdirAll(filepath.Join(stateDir, "prompts"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rolePromptPath(stateDir, token), []byte("implement\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The six below and the table are the whole of Env only for a daemon holding none of the
	// passthrough variables a launch is told beside them (stallLimitVariables,
	// refusalCooldownVariables): this test's daemon holds none.
	fixtureenv.Unset(t, slices.Concat(stallLimitVariables, refusalCooldownVariables)...)
	bot := runtime.GitIdentity{Name: "legion-implementer[bot]", Email: "7+legion-implementer[bot]@users.noreply.github.com"}
	var asked []string
	s := specs{stateDir: stateDir, project: "s1", appOwners: noAppOwners,
		repository: func(context.Context, string) (ghrepo.Repository, string, error) {
			return ghrepo.MustParse("zeta/tools"), "main", nil
		},
		identity: func(_ context.Context, role claim.Role, repository ghrepo.Repository) (runtime.GitIdentity, error) {
			asked = append(asked, string(role)+" on "+repository.String())
			return bot, nil
		}}
	spec, err := s.SpawnSpec(context.Background(), c)
	if err != nil || spec.Repository != ghrepo.MustParse("zeta/tools") {
		t.Fatalf("SpawnSpec = %q, %v; want zeta/tools, the runtime's to locate the workspace from", spec.Repository, err)
	}
	// The claim's own role first, as before the table, then the other App's first role of
	// claim.Roles, whose identity is that App's other roles' too.
	want := []string{"implementer on zeta/tools", "architect on zeta/tools"}
	if !slices.Equal(asked, want) {
		t.Errorf("identity asked for %q, want the claim's own role and then one role of the other App, each on the recorded repository alone, %q: the owner its lease is minted for is identity's own to decide", asked, want)
	}
	identity := map[string]string{
		"JJ_USER": bot.Name, "JJ_EMAIL": bot.Email,
		"GIT_AUTHOR_NAME": bot.Name, "GIT_AUTHOR_EMAIL": bot.Email,
		"GIT_COMMITTER_NAME": bot.Name, "GIT_COMMITTER_EMAIL": bot.Email,
		runtime.RoleIdentitiesVariable: runtime.RoleIdentities(map[claim.Role]runtime.GitIdentity{
			claim.RoleArchitect: bot, claim.RolePlanner: bot, claim.RoleImplementer: bot,
			claim.RoleTester: bot, claim.RoleReviewer: bot, claim.RoleMerger: bot,
		}),
	}
	if !maps.Equal(spec.Env, identity) {
		t.Errorf("the launch's Env = %v, want the six git identity variables and the role identities alone", spec.Env)
	}

	s.identity = func(context.Context, claim.Role, ghrepo.Repository) (runtime.GitIdentity, error) {
		return runtime.GitIdentity{}, errors.New("github_app_not_installed")
	}
	if _, err := s.SpawnSpec(context.Background(), c); err == nil || !strings.Contains(err.Error(), "github_app_not_installed") {
		t.Fatalf("SpawnSpec with no identity = %v, want the lease failure", err)
	}
}
