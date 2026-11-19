package tmux

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/ghconfig"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
	"github.com/sjawhar/legion/daemon/internal/runtime"
)

// A claim's pane launched as the implementer is stopped and let go, the claim is re-roled to the
// tester, and its relaunch writes the tester's bundle into the claim's gh directory, the one
// directory every incarnation of the claim reads, and watches the new pane as the tester. A
// refresh whose snapshot of the watch still held the let-go pane leaves that directory on the
// tester's App: it writes a pane's bundle under the launch lock the relaunch writes under, and
// only while the pane it rendered for is still watched. The relaunch is the launch's own three
// steps under that lock (refuseLiveIncarnation's untrack, writeGitHubCredential's bundle,
// openPane's track); no tmux server runs. The control is the relaunch made before the tick.
func TestARefreshOfALetGoPaneLeavesTheRelaunchsBundle(t *testing.T) {
	for _, tc := range []struct {
		name       string
		interleave bool
	}{
		{name: "control: the relaunch lands before the refresh takes its snapshot of the watch", interleave: false},
		{name: "the relaunch lands between the refresh's snapshot and its write of the claim's gh directory", interleave: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			keyed, err := claim.NewToken("omp", "LEGION-48", claim.RoleImplementer)
			if err != nil {
				t.Fatal(err)
			}
			repo := ghrepo.MustParse("acme/widgets")
			old := runtime.Locator{Runtime: runtime.RuntimeTmux, Claim: keyed, Incarnation: "4242:1",
				Tmux: &runtime.TmuxLocator{Window: "@1", Pane: "%1"}}
			next := runtime.Locator{Runtime: runtime.RuntimeTmux, Claim: keyed, Incarnation: "4242:2",
				Tmux: &runtime.TmuxLocator{Window: "@1", Pane: "%2"}}
			var r *Runtime
			refreshing, interleaved := false, false
			relaunch := func() {
				r.launchMu.Lock()
				defer r.launchMu.Unlock()
				r.untrack(old)
				bundle, err := staticCredential(ctx, claim.RoleTester, repo)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := ghconfig.WriteBundle(runtime.GHConfigDir(r.stateDir, keyed), bundle); err != nil {
					t.Fatal(err)
				}
				r.track(next, "LEGION-48", repo, "acme", claim.RoleTester)
			}
			r = &Runtime{
				stateDir: t.TempDir(),
				gitHubCredential: func(ctx context.Context, role claim.Role, repo ghrepo.Repository) (ghconfig.Bundle, error) {
					if refreshing && tc.interleave && !interleaved {
						interleaved = true
						relaunch()
					}
					return staticCredential(ctx, role, repo)
				},
				controllerCredential: staticControllerCredential,
				claimRepository:      acmeWidgets,
				log:                  slog.New(slog.NewTextHandler(io.Discard, nil)),
				tracked:              map[string]*trackedProcess{},
				writeBundle:          ghconfig.WriteBundle,
			}
			// The claim's first pane, launched as the implementer, with the implementer's bundle.
			first, err := staticCredential(ctx, claim.RoleImplementer, repo)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ghconfig.WriteBundle(runtime.GHConfigDir(r.stateDir, keyed), first); err != nil {
				t.Fatal(err)
			}
			r.track(old, "LEGION-48", repo, "acme", claim.RoleImplementer)
			if !tc.interleave {
				relaunch()
			}
			refreshing = true
			r.refreshGitHubCredentials(ctx)
			refreshing = false
			if tc.interleave && !interleaved {
				t.Fatal("the relaunch never landed inside the refresh: the case saw no window")
			}
			want := staticCredentialToken(claim.RoleTester)
			if got := readHostsToken(t, ghconfig.Dir(runtime.GHConfigDir(r.stateDir, keyed), "acme")); got != want {
				t.Fatalf("after the refresh the claim's gh directory, which the tester pane %s reads, holds %q, the App of the role the let-go pane %s was launched as; want the tester's %q",
					next.Incarnation, got, old.Incarnation, want)
			}
		})
	}
}
