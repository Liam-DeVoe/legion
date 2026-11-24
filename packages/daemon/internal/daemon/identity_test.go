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

	"github.com/sjawhar/legion/daemon/internal/appauth"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/fixtureenv"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// A pane commits as its role's App, minted for the owner of the repository its issue works: the
// launch carries the six git identity variables every worker pane gets, nothing else of the Apps,
// and a launch whose identity cannot be resolved does not happen.
func TestSpawnSpecCarriesTheRoleAppIdentity(t *testing.T) {
	// Not parallel: it unsets environment variables of the whole process (Unset).
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
	// The six below are the whole of Env only for a daemon holding none of the passthrough
	// variables a launch is told beside them (stallLimitVariables, refusalCooldownVariables):
	// this test's daemon holds none.
	fixtureenv.Unset(t, slices.Concat(stallLimitVariables, refusalCooldownVariables)...)
	bot := runtime.GitIdentity{Name: "legion-implementer[bot]", Email: "7+legion-implementer[bot]@users.noreply.github.com"}
	var asked []string
	s := specs{stateDir: stateDir, project: "s1", appOwners: noAppOwners,
		repository: func(context.Context, string) (ghrepo.Repository, string, error) {
			return ghrepo.MustParse("zeta/tools"), "main", nil
		},
		identity: func(_ context.Context, role claim.Role, repository ghrepo.Repository) (runtime.GitIdentity, error) {
			if role != claim.RoleImplementer {
				t.Errorf("identity asked for %s, want the claim's role", role)
			}
			asked = append(asked, repository.String())
			return bot, nil
		}}
	spec, err := s.SpawnSpec(context.Background(), c)
	if err != nil || spec.Repository != ghrepo.MustParse("zeta/tools") {
		t.Fatalf("SpawnSpec = %q, %v; want zeta/tools, the runtime's to locate the workspace from", spec.Repository, err)
	}
	if want := []string{"zeta/tools"}; !slices.Equal(asked, want) {
		t.Errorf("identity asked for %q, want the recorded repository alone, %q: the owner its lease is minted for is identity's own to decide", asked, want)
	}
	identity := map[string]string{
		"JJ_USER": bot.Name, "JJ_EMAIL": bot.Email,
		"GIT_AUTHOR_NAME": bot.Name, "GIT_AUTHOR_EMAIL": bot.Email,
		"GIT_COMMITTER_NAME": bot.Name, "GIT_COMMITTER_EMAIL": bot.Email,
	}
	if !maps.Equal(spec.Env, identity) {
		t.Errorf("the launch's Env = %v, want the six git identity variables alone", spec.Env)
	}

	s.identity = func(context.Context, claim.Role, ghrepo.Repository) (runtime.GitIdentity, error) {
		return runtime.GitIdentity{}, errors.New("github_app_not_installed")
	}
	if _, err := s.SpawnSpec(context.Background(), c); err == nil || !strings.Contains(err.Error(), "github_app_not_installed") {
		t.Fatalf("SpawnSpec with no identity = %v, want the lease failure", err)
	}
}

// askedTokens is roleTokens that counts the write leases it is asked for.
type askedTokens struct {
	roleTokens
	asked int
}

func (t *askedTokens) Token(ctx context.Context, set appauth.TokenSet, owner string) (appauth.Lease, error) {
	t.asked++
	return t.roleTokens.Token(ctx, set, owner)
}

// A claim on a role that has no token set fails its start with appauth's refusal, which names the
// role, and nothing is minted for it: its launch is told no owner its App reaches (plan.appOwners),
// since its container holds no bundle, and its git identity (workflowRuntime.identity) is minted
// with the role's set, which SetFor answers that role none of. The start fails rather than the
// daemon.
func TestAClaimWhoseRoleHasNoSetFailsItsStart(t *testing.T) {
	stateDir := t.TempDir()
	role := claim.Role("deployer")
	token := claim.Token("legion-s1-s1-1-" + string(role))
	c := supervise.Claim{Token: token, Issue: "S1-1", Tree: "S1-1", Role: role}
	if err := os.MkdirAll(filepath.Join(stateDir, "prompts"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rolePromptPath(stateDir, token), []byte("deploy\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tools := ghrepo.MustParse("zeta/tools")
	p := plan{repositories: renamedRepositories([]string{tools.String()}, ghrepo.Renames{}), installed: appauth.NewInstalled(), left: appauth.NewLeftOut()}
	p.installed.RecordProof([]string{tools.Owner()})
	tokens := &askedTokens{}
	w := &workflowRuntime{tokens: tokens, repositories: p.repositories}
	s := specs{stateDir: stateDir, project: "s1", appOwners: p.appOwners("tmux"), identity: w.identity,
		repository: func(context.Context, string) (ghrepo.Repository, string, error) { return tools, "main", nil }}

	_, err := s.SpawnSpec(context.Background(), c)
	if !errors.Is(err, appauth.ErrNoTokenSet) || !strings.Contains(err.Error(), `"deployer"`) {
		t.Fatalf("SpawnSpec of a claim on the role deployer = %v, want appauth.ErrNoTokenSet naming the role", err)
	}
	if tokens.asked != 0 {
		t.Errorf("the start asked for %d write leases, want none: the role has no set to mint one with", tokens.asked)
	}
}
