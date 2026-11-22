package tmux

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/appauth"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/ghconfig"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
	"github.com/sjawhar/legion/daemon/internal/runtime"
)

// Under tmux a pane's gh files are its claim's own directory, which nothing stands between: the
// write of the architect's pane for the implementer's App on request leaves its hosts.yml holding
// the implement App's token, and the pane holds it at once (SlotHoldsCredential answers nil); the
// write for the architect's own App puts the review App's back.
func TestASlotSaysWhenItsFilesHoldTheCredentialUnderTmux(t *testing.T) {
	ctx := context.Background()
	f := newFakeServerRuntime(t, staticCredential, nil)
	spec := f.spec(t, claim.RoleArchitect)
	if _, err := f.Spawn(ctx, spec); err != nil {
		t.Fatalf("Spawn of the architect: %v", err)
	}
	dir := ghconfig.Dir(runtime.GHConfigDir(f.stateDir, spec.Claim), "acme")
	for _, tc := range []struct {
		role claim.Role
		want string
	}{
		{claim.RoleImplementer, staticCredentialToken(claim.RoleImplementer)},
		{claim.RoleArchitect, staticCredentialToken(claim.RoleArchitect)},
	} {
		if err := f.WriteSlotCredential(ctx, spec.Claim, tc.role); err != nil {
			t.Fatalf("WriteSlotCredential(the architect, %s) = %v", tc.role, err)
		}
		if got := readHostsToken(t, dir); got != tc.want {
			t.Fatalf("the architect's pane holds %q after the write for %s, want %q", got, tc.role, tc.want)
		}
		if err := f.SlotHoldsCredential(ctx, spec.Claim, tc.role); err != nil {
			t.Fatalf("SlotHoldsCredential(the architect, %s) = %v, want nil", tc.role, err)
		}
	}
}

// The App a pane is written for on request is the App of the step's role on the pane's own issue:
// LEGION-43's plan defines coder of the type implementation and prober of the type testing, and
// WriteSlotCredential of LEGION-43's architect for coder writes the implement App's lease and for
// prober the review App's.
func TestASlotCredentialIsTheAppOfTheStepsRoleOnItsIssueUnderTmux(t *testing.T) {
	ctx := context.Background()
	widgets := ghrepo.MustParse("acme/widgets")
	architect, err := claim.NewToken("omp", "LEGION-43", claim.RoleArchitect)
	if err != nil {
		t.Fatal(err)
	}
	apps := appauth.RoleApps{Plan: planOf43}
	r := &Runtime{
		stateDir:             t.TempDir(),
		gitHubCredential:     appauth.GitHubCredential(appLeaseTokens{}, servesOneOf(widgets), ghrepo.Renames{}, nil, nil, apps, slog.Default()),
		roleApps:             apps,
		controllerCredential: staticControllerCredential,
		claimRepository:      acmeWidgets,
		log:                  slog.New(slog.NewTextHandler(io.Discard, nil)),
		tracked:              map[string]*trackedProcess{},
		writeBundle:          ghconfig.WriteBundle,
	}
	r.track(runtime.Locator{Runtime: runtime.RuntimeTmux, Claim: architect, Incarnation: "4242:1",
		Tmux: &runtime.TmuxLocator{Window: "@1", Pane: "%1"}}, "LEGION-43", widgets, "acme", claim.RoleArchitect)
	dir := ghconfig.Dir(runtime.GHConfigDir(r.stateDir, architect), "acme")
	for _, tc := range []struct {
		role claim.Role
		want string
	}{
		{"coder", "ghs_implement_acme"},
		{"prober", "ghs_review_acme"},
		{"coder", "ghs_implement_acme"},
	} {
		if err := r.WriteSlotCredential(ctx, architect, tc.role); err != nil {
			t.Fatalf("WriteSlotCredential(the architect of LEGION-43, %s) = %v", tc.role, err)
		}
		if got := readHostsToken(t, dir); got != tc.want {
			t.Fatalf("the architect's pane holds %q after the write for %s, want %q", got, tc.role, tc.want)
		}
	}
}
