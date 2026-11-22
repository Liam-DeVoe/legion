package sandbox

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/appauth"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/ghconfig"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
	"github.com/sjawhar/legion/daemon/internal/runtime"
)

// refusedProjectRepos is the repositories the project serves in the tests below: the repository
// every test spec works, under the owner its roles write to, and one repository of a second
// owner, which a bundle holds that owner's own directory for.
func refusedProjectRepos() []ghrepo.Repository {
	return []ghrepo.Repository{ghrepo.MustParse("sjawhar/legion-smoke"), ghrepo.MustParse("acme/widgets")}
}

// refusingOwnerTokens answers every lease of owner with the refusal GitHub gives a request its
// installation cannot answer (422, appauth.ErrNarrowingRefused) while refuse holds, and an
// ordinary lease for every other owner and once refuse is cleared. lease, when set, is the one
// lease that refuses — a token set's name, or "read" for the owner's read lease, which a
// controller's bundle holds — every other minting as readily as ever: GitHub answers each set's
// permissions for themselves, so it may refuse one set's narrowing where it grants another's.
type refusingOwnerTokens struct {
	refuse *atomic.Bool
	owner  string
	lease  string
}

func (t refusingOwnerTokens) Token(_ context.Context, set appauth.TokenSet, owner string) (appauth.Lease, error) {
	return t.mint(string(set), owner, "write")
}

func (t refusingOwnerTokens) ReadToken(_ context.Context, owner string) (appauth.Lease, error) {
	return t.mint("read", owner, "read")
}

func (t refusingOwnerTokens) VerdictToken(ctx context.Context, owner string) (appauth.Lease, error) {
	return t.Token(ctx, appauth.SetVerdict, owner)
}

func (t refusingOwnerTokens) mint(lease, owner, kind string) (appauth.Lease, error) {
	if t.refuse != nil && t.refuse.Load() && strings.EqualFold(owner, t.owner) && (t.lease == "" || t.lease == lease) {
		return appauth.Lease{}, fmt.Errorf("the %s lease on %s: %w", lease, owner, appauth.ErrNarrowingRefused)
	}
	return appauth.Lease{Token: "ghs_" + lease + "_" + strings.ToLower(owner) + "_" + kind, ExpiresAt: staticCredentialExpiry}, nil
}

// eachLeasesTokens answers each lease from the tokens it holds for that lease — a token set's
// name, or "read" for the owner's read lease — so the installation may refuse one set's lease of
// an owner while another's mints. A lease it holds no entry for mints as readily as ever.
type eachLeasesTokens map[string]refusingOwnerTokens

func (e eachLeasesTokens) Token(ctx context.Context, set appauth.TokenSet, owner string) (appauth.Lease, error) {
	return e[string(set)].Token(ctx, set, owner)
}

func (e eachLeasesTokens) ReadToken(ctx context.Context, owner string) (appauth.Lease, error) {
	return e["read"].ReadToken(ctx, owner)
}

func (e eachLeasesTokens) VerdictToken(ctx context.Context, owner string) (appauth.Lease, error) {
	return e.Token(ctx, appauth.SetVerdict, owner)
}

// withRefusedOwner gives the rig the daemon's three gh answers over tokens that refuse every
// lease of owner while refuse holds, whichever set asks and the read lease too
// (withProjectTokens).
func withRefusedOwner(owner string, refuse *atomic.Bool) rigOption {
	return withProjectTokens(refusingOwnerTokens{refuse: refuse, owner: owner})
}

// withProjectTokens gives the rig the daemon's own three gh answers (appauth.GitHubCredential,
// appauth.ControllerCredential and appauth.GitHubDirs) over refusedProjectRepos and tokens, the
// three sharing the two records their bundles write — the owners the App was read installed on
// and what the mints found GitHub refuses — as internal/daemon's sandboxRuntime builds them for
// a project. The App is installed on every owner the project serves a repository of, so what a
// bundle holds here is what the mints answer for.
func withProjectTokens(tokens appauth.Tokens) rigOption {
	return withOptions(func(o *Options) {
		installed, left := appauth.NewInstalled(), appauth.NewLeftOut()
		apps := sourceOf(tokens, installedOnServed{repos: refusedProjectRepos})
		served := servesOneOf(refusedProjectRepos)
		o.GitHubCredential = appauth.GitHubCredential(apps, served, ghrepo.Renames{}, installed, left, o.Log)
		o.ControllerCredential = appauth.ControllerCredential(apps, served, ghrepo.Renames{}, installed, left, o.Log)
		o.GitHubDirs = func(repo ghrepo.Repository, sets []appauth.TokenSet) []string {
			return appauth.GitHubDirs(served, ghrepo.Renames{}, installed, left, sets, repo)
		}
	})
}

// ghDriftReasons is why loc's pod has drifted that name role's gh volume: what an update round
// over that pod would be opened for (PodDrift, driftedInPod, movedGhDirs).
func ghDriftReasons(t *testing.T, g *rig, loc runtime.Locator, role claim.Role) []string {
	t.Helper()
	name := roleVolume(ghVolume, role)
	var named []string
	for _, reason := range driftOf(t, g, g.r, loc) {
		if strings.Contains(reason, name) {
			named = append(named, reason)
		}
	}
	return named
}

// ghVolumeDrift is why loc's pod, of a kind no update round covers, has drifted that name
// role's gh volume: what its next launch replaces the pod for (movedInPod over driftedInPod,
// movedGhDirs). PodDrift answers nothing for the project controller's pod, so this is the
// reading that reaches it.
func ghVolumeDrift(g *rig, loc runtime.Locator, role claim.Role) []string {
	name := roleVolume(ghVolume, role)
	var named []string
	for _, moved := range g.r.driftedInPod(g.pod(loc.Sandbox.Name), role) {
		if strings.Contains(moved.String(), name) {
			named = append(named, moved.String())
		}
	}
	return named
}

// An owner whose every repository GitHub refuses a lease for is left out of every bundle, so no
// pod projects a directory of that name — and the drift check must not ask for one. Were it
// asked for, the daemon would read the pod as drifted at every sweep, open an update round, task
// every worker in the pod with the steps of its update message (supervise.UpdateTask), replace the
// pod, and mint the pod that replaces it a bundle lacking that directory again: a pod replaced for
// ever over an owner no mint can produce (INFRA-513). The record the mints write and the
// directory set reads is what keeps the two answers one (appauth.LeftOut). Once GitHub answers for
// that owner again, a mint clears the record and the pod is replaced once, which is right.
func TestAnOwnerEveryLeaseIsRefusedForIsNoDriftForThePodWithoutIt(t *testing.T) {
	var refuse atomic.Bool
	refuse.Store(true)
	g := newRig(t, nil, withRefusedOwner("acme", &refuse))
	loc := g.spawn(workerSpec(t))

	if held := ghItems(t, g.pod(loc.Sandbox.Name), claim.RoleTester); !slices.Equal(held, ghItemsFor("sjawhar")) {
		t.Fatalf("the tester's gh volume projects %+v, want sjawhar's two files alone: acme answered for no repository", held)
	}
	for _, evaluation := range []string{"first", "second"} {
		if named := ghDriftReasons(t, g, loc, claim.RoleTester); len(named) != 0 {
			t.Fatalf("the %s evaluation reads %q as drift, want none: no bundle built now holds acme either", evaluation, named)
		}
	}

	refuse.Store(false)
	g.r.refreshGitHubCredentials(g.ctx)
	if named := ghDriftReasons(t, g, loc, claim.RoleTester); len(named) != 1 {
		t.Errorf("PodDrift names %q once acme answers again, want exactly one reason naming its gh volume", named)
	}
}

// Each of the six roles of an issue pod holds its own token set's leases, and GitHub answers each
// set's lease for itself: the installation may refuse one set's narrowing for an owner while
// every other set mints there. The pod then projects the directories every role's bundle holds
// and starts, since a projection item naming a key a role's Secret lacks would stop the pod from
// starting; a role whose bundle holds more keeps those keys in its own Secret, unprojected, and
// the issue's next pod projects them once every role's bundle holds them.
func TestAPodProjectsTheGhDirectoriesEveryRolesBundleHolds(t *testing.T) {
	var refuse atomic.Bool
	refuse.Store(true)
	tokens := refusingOwnerTokens{refuse: &refuse, owner: "acme", lease: string(appauth.SetReviewer)}
	g := newRig(t, nil, withOptions(func(o *Options) {
		o.GitHubCredential = appauth.GitHubCredential(sourceOf(tokens, installedOnServed{repos: refusedProjectRepos}),
			servesOneOf(refusedProjectRepos), ghrepo.Renames{}, nil, nil, o.Log)
	}))

	loc := g.spawn(workerSpec(t))
	pod := g.pod(loc.Sandbox.Name)
	for _, role := range claim.Roles {
		if held := ghItems(t, pod, role); !slices.Equal(held, ghItemsFor("sjawhar")) {
			t.Errorf("%s's gh volume projects %+v, want sjawhar's two files alone: the reviewer's set was refused every acme lease", role, held)
		}
		secret := g.secret(roleSecretName(loc.Sandbox.Name, role))
		_, holdsAcme := secret.Data[GitHubHostsKey("acme")]
		if want := appauth.SetFor(role) != appauth.SetReviewer; holdsAcme != want {
			t.Errorf("%s's Secret holds acme's gh files %v, want %v: each role's Secret holds its own bundle", role, holdsAcme, want)
		}
	}
}

// A launch whose roles share no directory at all still fails: the pod's GH_CONFIG_DIR would name
// a directory no role's gh volume projects, and every role would start unable to reach GitHub.
func TestALaunchWhoseRolesShareNoGhDirectoryFails(t *testing.T) {
	g := newRig(t, nil, withOptions(func(o *Options) {
		o.GitHubCredential = func(ctx context.Context, role claim.Role, repo ghrepo.Repository) (ghconfig.Bundle, error) {
			bundle, err := staticCredential(ctx, role, repo)
			if err != nil {
				return bundle, err
			}
			// Each set's mints answer for themselves, so the bundles share nothing: the
			// reviewer's holds acme's directory alone, which is then that role's own default,
			// and every other role's the issue owner's alone.
			if appauth.SetFor(role) == appauth.SetReviewer {
				return ghconfig.Bundle{Default: "acme", Dirs: map[string]ghconfig.Rendered{"acme": bundle.Dirs["acme"]}}, nil
			}
			delete(bundle.Dirs, "acme")
			return bundle, nil
		}
	}))

	_, err := g.r.Spawn(g.ctx, workerSpec(t))
	if err == nil || !strings.Contains(err.Error(), "do not hold the pod's default owner") {
		t.Fatalf("the launch = %v, want a failure naming the directories the roles share and the default they do not hold", err)
	}
}

// The record of what a mint found GitHub refuses is read for the sets a pod runs and no others
// (appauth.GitHubDirs over the sets of the pod's kind's roles, podTokenSets). The project
// controller's pod holds the controller's read leases alone: an owner those reach is one its pod
// must hold a directory for, however an issue role's set is answered there, and a controller
// launched before that owner was served holds no token for it until its pod is replaced. An
// issue pod is the other case: it projects what every one of its roles' bundles holds, so an
// owner any of its sets is refused is in no projection of it and its pod is no pod to replace.
// Were the record read for every set at once, the implementer's refusal would hide the
// controller's drift for as long as it stood — and in a project with no live issue pod nothing
// mints for that set to clear it.
func TestAnOwnerOnlyAnIssueRolesSetIsRefusedForIsDriftForTheControllersPod(t *testing.T) {
	var implementer, read atomic.Bool
	implementer.Store(true)
	read.Store(true)
	g := newRig(t, nil, withProjectTokens(eachLeasesTokens{
		string(appauth.SetImplementer): {refuse: &implementer, owner: "acme"},
		"read":                         {refuse: &read, owner: "acme"},
	}))
	issue := g.spawn(workerSpec(t))
	controller := g.spawn(controllerSpec(t))
	for _, pod := range []struct {
		what string
		loc  runtime.Locator
		role claim.Role
	}{{"issue", issue, claim.RoleTester}, {"controller", controller, claim.RoleController}} {
		if held := ghItems(t, g.pod(pod.loc.Sandbox.Name), pod.role); !slices.Equal(held, ghItemsFor("sjawhar")) {
			t.Fatalf("the %s pod's gh volume projects %+v, want sjawhar's two files alone: neither the implementer's set nor the read lease was answered for acme", pod.what, held)
		}
	}

	// GitHub answers the read lease for acme, the implementer's set refused there still: a
	// controller's bundle built now holds acme's directory, where no issue pod's projection can.
	read.Store(false)
	g.r.refreshGitHubCredentials(g.ctx)

	named := ghVolumeDrift(g, controller, claim.RoleController)
	if len(named) != 1 || !strings.Contains(named[0], "acme") {
		t.Errorf("the controller's pod holds %q, want one reason naming acme: the read lease, which is the controller's own, reaches it", named)
	}
	if named := ghDriftReasons(t, g, issue, claim.RoleTester); len(named) != 0 {
		t.Errorf("the issue pod reads %q as drift, want none: the implementer's set is refused acme, so no pod of every workflow role projects acme", named)
	}
}
