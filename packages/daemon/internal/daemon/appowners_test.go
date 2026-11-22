package daemon

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/appauth"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
)

// appOwnersSource is the GitHub App token source of plan.appOwners' test: the App installed on
// the owners installed names, and every lease of an owner refused names for a set refused as
// GitHub refuses a narrowing (appauth.ErrNarrowingRefused). That is one of the four answers a
// bundle leaves an owner's directory out over, the one it says as "permissions refused"
// (appauth's leaveOutReason). The read lease names no set, and is never refused.
type appOwnersSource struct {
	installed []string
	refused   map[appauth.TokenSet][]string
}

func (s appOwnersSource) Token(_ context.Context, set appauth.TokenSet, owner string) (appauth.Lease, error) {
	if slices.Contains(s.refused[set], owner) {
		return appauth.Lease{}, fmt.Errorf("%w (owner %s)", appauth.ErrNarrowingRefused, owner)
	}
	return appauth.Lease{Token: "ghs_" + string(set) + "_" + owner, ExpiresAt: time.Now().Add(time.Hour)}, nil
}

func (s appOwnersSource) ReadToken(_ context.Context, owner string) (appauth.Lease, error) {
	return appauth.Lease{Token: "ghs_read_" + owner, ExpiresAt: time.Now().Add(time.Hour)}, nil
}

func (s appOwnersSource) VerdictToken(ctx context.Context, owner string) (appauth.Lease, error) {
	return s.Token(ctx, appauth.SetVerdict, owner)
}

func (s appOwnersSource) InstalledOwners(context.Context) ([]string, error) {
	return s.installed, nil
}

// installedOn is a source whose App is installed on owners and refuses nothing.
func installedOn(owners ...string) appOwnersSource {
	return appOwnersSource{installed: owners}
}

// roleOwners is what plan.appOwners answers three roles under one runtime: the implementer and
// the reviewer, which hold two sets and share an issue pod under Kubernetes, and the project
// controller, which holds the controller's set in a pod or pane of its own.
type roleOwners struct{ implementer, reviewer, controller []string }

// A launch's addressing says of each owner it names that the GitHub App is installed on it and
// that the role's token reaches every repository of it (RepositoriesClause). So what the daemon
// answers a launch (plan.appOwners) is the installed owners the role's container holds a
// directory for, and not every installed owner.
//
// An owner whose lease GitHub refuses is still in the installations the bundle read, and the
// bundle holds no directory for it. The alias directory a resolving rename puts in the bundle is
// the other way round, a directory for an account the App is not installed on. Neither is
// named. The runtimes differ in what a container holds: a tmux pane holds its own role's bundle,
// while a Kubernetes issue pod projects only the directories every one of its roles' bundles
// holds, so an owner another role's set's mint left out is in no container of that pod, whichever
// set the role holds. The project controller's pod holds the controller's set alone and answers
// alike under both runtimes. The owners the App was read installed on come from the boot's proof
// of its key first (proveAppKeys, appauth.Installed.RecordProof) and then from the last bundle,
// whichever role it was built for; until either has read the App, there is no read to report and
// the answer is none.
func TestAppOwnersAreTheInstalledOwnersTheRolesContainerHoldsADirectoryFor(t *testing.T) {
	moved := ghrepo.MustRenames(ghrepo.MustRename("sjawhar/legion", "cybermonklabs/legion"))
	for _, tc := range []struct {
		name    string
		renames ghrepo.Renames
		// served are the names the App answered installed on (repositories.fill).
		served []string
		source appOwnersSource
		// proved is whether the boot's proof of the App's key recorded the owners source answers,
		// before any bundle is built.
		proved bool
		// built are the roles whose bundles are built for repo before the launches are asked.
		built []claim.Role
		repo  string
		tmux  roleOwners
		pod   roleOwners
	}{
		{
			name:   "every installed owner's lease is minted",
			served: []string{"acme/widgets"}, source: installedOn("acme", "globex"),
			built: []claim.Role{claim.RoleImplementer, claim.RoleReviewer}, repo: "acme/widgets",
			tmux: roleOwners{[]string{"acme", "globex"}, []string{"acme", "globex"}, []string{"acme", "globex"}},
			pod:  roleOwners{[]string{"acme", "globex"}, []string{"acme", "globex"}, []string{"acme", "globex"}},
		},
		{
			name:   "an installed owner whose lease the role's own set is refused is left out",
			served: []string{"acme/widgets"},
			source: appOwnersSource{
				installed: []string{"acme", "globex"},
				refused:   map[appauth.TokenSet][]string{appauth.SetImplementer: {"globex"}},
			},
			built: []claim.Role{claim.RoleImplementer, claim.RoleReviewer}, repo: "acme/widgets",
			tmux: roleOwners{[]string{"acme"}, []string{"acme", "globex"}, []string{"acme", "globex"}},
			// The issue pod holds no globex directory at all, so the reviewer, whose own set
			// mints globex, is not told it either; the controller's pod holds its own set alone.
			pod: roleOwners{[]string{"acme"}, []string{"acme"}, []string{"acme", "globex"}},
		},
		{
			name:   "another set of the pod's mint leaves an owner out",
			served: []string{"acme/widgets"},
			source: appOwnersSource{
				installed: []string{"acme", "globex"},
				refused:   map[appauth.TokenSet][]string{appauth.SetReviewer: {"globex"}},
			},
			built: []claim.Role{claim.RoleImplementer, claim.RoleReviewer}, repo: "acme/widgets",
			tmux: roleOwners{[]string{"acme", "globex"}, []string{"acme"}, []string{"acme", "globex"}},
			// The controller's pod holds the controller's set alone, whose mint left nothing out.
			pod: roleOwners{[]string{"acme"}, []string{"acme"}, []string{"acme", "globex"}},
		},
		{
			name:    "the alias directory of a resolving rename is no installed owner",
			renames: moved, served: []string{"cybermonklabs/legion"}, source: installedOn("cybermonklabs"),
			built: []claim.Role{claim.RoleImplementer, claim.RoleReviewer}, repo: "sjawhar/legion",
			tmux: roleOwners{[]string{"cybermonklabs"}, []string{"cybermonklabs"}, []string{"cybermonklabs"}},
			pod:  roleOwners{[]string{"cybermonklabs"}, []string{"cybermonklabs"}, []string{"cybermonklabs"}},
		},
		{
			name:   "one role's bundle reads the App's installations for every role",
			served: []string{"acme/widgets"}, source: installedOn("acme", "globex"),
			built: []claim.Role{claim.RoleImplementer}, repo: "acme/widgets",
			tmux: roleOwners{[]string{"acme", "globex"}, []string{"acme", "globex"}, []string{"acme", "globex"}},
			pod:  roleOwners{[]string{"acme", "globex"}, []string{"acme", "globex"}, []string{"acme", "globex"}},
		},
		{
			// No bundle and no proof has read the App, so no role has a read to report: the
			// answer is none for each, never the owners of a repository.
			name:   "no installations read yet",
			served: []string{"acme/widgets", "zeta/tools"}, source: installedOn("globex"),
		},
		{
			name:   "the boot's proof of the App's key read its installations",
			served: []string{"acme/widgets"}, source: installedOn("acme", "globex"), proved: true,
			tmux: roleOwners{[]string{"acme", "globex"}, []string{"acme", "globex"}, []string{"acme", "globex"}},
			pod:  roleOwners{[]string{"acme", "globex"}, []string{"acme", "globex"}, []string{"acme", "globex"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := plan{repositories: newRepositories("LEGION", tc.renames), installed: appauth.NewInstalled(), left: appauth.NewLeftOut()}
			for _, repo := range tc.served {
				p.repositories.fill(ghrepo.MustParse(repo))
			}
			if tc.proved {
				p.installed.RecordProof(tc.source.installed)
			}
			// Each bundle is built as a runtime builds it, over the plan's own two records, so
			// what a row reads is what a real mint recorded.
			credential := appauth.GitHubCredential(tc.source, p.repositories.servesRepository, p.repositories.renames, p.installed, p.left, quietLogger())
			for _, role := range tc.built {
				if _, err := credential(context.Background(), role, ghrepo.MustParse(tc.repo)); err != nil {
					t.Fatalf("build the %s's bundle for %s: %v", role, tc.repo, err)
				}
			}
			for runtimeName, want := range map[string]roleOwners{"tmux": tc.tmux, "kubernetes": tc.pod} {
				owners := p.appOwners(runtimeName)
				for role, want := range map[claim.Role][]string{
					claim.RoleImplementer: want.implementer, claim.RoleReviewer: want.reviewer, claim.RoleController: want.controller,
				} {
					if got := owners(role); !slices.Equal(got, want) {
						t.Errorf("under %s, appOwners(%s) = %q, want %q: the installed owners the %s's container holds a directory for",
							runtimeName, role, got, want, role)
					}
				}
				// A role with no token set holds no bundle at all (appauth.SetFor), so no
				// container of it holds a directory and it is told no owner.
				if got := owners("deployer"); got != nil {
					t.Errorf("under %s, appOwners(deployer) = %q, want none: the role has no token set", runtimeName, got)
				}
			}
		})
	}
}

// Under Kubernetes a role whose pod holds another role with no token set is named no owner: that
// pod is never built (sandbox.PodTokenSets), so no container of it holds a directory. The one way
// a pod holds such a role is a claim role with no set, so the test adds one to claim.Roles for
// its own life; it never runs in parallel. The same role in a tmux pane holds its own bundle and
// is named the owner its App reaches, which is the control.
func TestAppOwnersNamesNoOwnerOfAPodWithARoleOutsideTheSets(t *testing.T) {
	held := claim.Roles
	t.Cleanup(func() { claim.Roles = held })
	claim.Roles = append(slices.Clone(held), "deployer")
	p := plan{repositories: newRepositories("LEGION", ghrepo.Renames{}), installed: appauth.NewInstalled(), left: appauth.NewLeftOut()}
	p.repositories.fill(ghrepo.MustParse("acme/widgets"))
	p.installed.RecordProof([]string{"acme"})

	if got := p.appOwners("tmux")(claim.RoleImplementer); !slices.Equal(got, []string{"acme"}) {
		t.Fatalf("under tmux, appOwners(implementer) = %q, want acme: the pane holds its own bundle", got)
	}
	if got := p.appOwners("kubernetes")(claim.RoleImplementer); got != nil {
		t.Errorf("under kubernetes, appOwners(implementer) = %q, want none: its pod holds the deployer, which has no token set", got)
	}
}
