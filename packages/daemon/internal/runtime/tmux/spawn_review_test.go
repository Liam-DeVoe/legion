package tmux

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
	"github.com/sjawhar/legion/daemon/internal/workspace"
)

// A reviewer's pane works in the reviewer's own clone of the issue (workspace.ReviewLocation),
// which the daemon made before the launch, and never in the issue's workspace, which the
// implementer and the tester write; a launch whose review clone is missing is refused rather
// than started in the shared one. Every other role's pane works in the issue's workspace.
func TestAReviewerPaneWorksInItsOwnClone(t *testing.T) {
	stateDir := t.TempDir()
	r := &Runtime{stateDir: stateDir}
	spec := testSpec()
	spec.Repository = ghrepo.MustParse("sjawhar/legion")
	shared := filepath.Join(stateDir, "workspaces", "sjawhar", "legion", "legion-43")
	if err := os.MkdirAll(shared, 0o700); err != nil {
		t.Fatal(err)
	}
	review, err := workspace.ReviewLocation(stateDir, spec.Repository, spec.Issue)
	if err != nil {
		t.Fatal(err)
	}

	spec.Role = claim.RoleReviewer
	if dir, err := r.workspaceDir(spec); err == nil || !strings.Contains(err.Error(), review) || !strings.Contains(err.Error(), "never provisioned") {
		t.Fatalf("a reviewer's workspaceDir before its clone was made = %q, %v; want a refusal naming %s", dir, err, review)
	}
	if err := os.MkdirAll(review, 0o700); err != nil {
		t.Fatal(err)
	}
	if dir, err := r.workspaceDir(spec); err != nil || dir != review {
		t.Fatalf("a reviewer's workspaceDir = %q, %v; want its own clone %s", dir, err, review)
	}

	for _, role := range []claim.Role{claim.RoleImplementer, claim.RoleTester} {
		spec.Role = role
		if dir, err := r.workspaceDir(spec); err != nil || dir != shared {
			t.Errorf("the %s's workspaceDir = %q, %v; want the issue's workspace %s", role, dir, err, shared)
		}
	}
}
