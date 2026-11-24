package daemon

import (
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/runtime/sandbox"
	"github.com/sjawhar/legion/daemon/internal/runtime/tmux"
	"github.com/sjawhar/legion/daemon/internal/workflow"
	"github.com/sjawhar/legion/daemon/internal/workspace"
)

// The engine names, in each task's working copy line, the directory the runtime the daemon runs
// starts the issue's role in: the pod's WorkspaceDir under Kubernetes, the pane's under tmux, for
// the repository the issue's record names (TestTheWorkspaceDirIsTheAgentContainersWorkingDirectory
// and TestTheWorkspaceDirIsThePanesStartDirectory hold each runtime's own process to that
// directory), and for the reviewer its own clone of the issue under that runtime's root
// (workspace.ReviewLocation), which its pane starts in and its pod's LEGION_WORKSPACE names
// (TestAPodReviewerIsToldItsOwnCloneAndGit), never the shared workspace. An issue whose recorded
// repository does not parse names the branch alone.
func TestEachTaskNamesTheDirectoryItsRuntimeStartsTheAgentIn(t *testing.T) {
	t.Parallel()
	issue := record.Issue{Key: "LEGION-208", Repo: "sjawhar/legion", Phase: phase.Implementing}
	repo := ghrepo.MustParse("sjawhar/legion")
	stateDir := t.TempDir()
	pod, err := sandbox.WorkspaceDir(repo, issue.Key)
	if err != nil {
		t.Fatal(err)
	}
	pane, err := tmux.WorkspaceDir(stateDir, repo, issue.Key)
	if err != nil {
		t.Fatal(err)
	}
	podReview, err := workspace.ReviewLocation(sandbox.TreeRoot, repo, issue.Key)
	if err != nil {
		t.Fatal(err)
	}
	paneReview, err := workspace.ReviewLocation(stateDir, repo, issue.Key)
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		cfg          config.Config
		want, review string
	}{
		"kubernetes": {config.Config{Runtime: config.Runtime{Name: "kubernetes"}, StateDir: stateDir}, pod, podReview},
		"tmux":       {config.Config{Runtime: config.Runtime{Name: "tmux"}, StateDir: stateDir}, pane, paneReview},
	} {
		t.Run(name, func(t *testing.T) {
			dir := engineConfig(tc.cfg, "", nil).WorkspaceDir
			for _, role := range []claim.Role{claim.RolePlanner, claim.RoleImplementer, claim.RoleTester, claim.RoleMerger} {
				if got := dir(issue, role); got != tc.want || got == "" {
					t.Fatalf("the engine's workspace of %s's %s = %q, want the runtime's %q", issue.Key, role, got, tc.want)
				}
			}
			if got := dir(issue, claim.RoleReviewer); got != tc.review || got == tc.want {
				t.Fatalf("the engine's workspace of %s's reviewer = %q, want its own clone %q and not the shared %q", issue.Key, got, tc.review, tc.want)
			}
			if got, want := workflow.WorkingCopy(issue, dir).String(), " Working copy: branch legion/LEGION-208, directory "+tc.want+"."; got != want {
				t.Fatalf("the working copy line = %q, want %q", got, want)
			}
			reviewing := issue
			reviewing.Phase = phase.Reviewing
			if got, want := workflow.WorkingCopy(reviewing, dir).String(), " Working copy: branch legion/LEGION-208, directory "+tc.review+"."; got != want {
				t.Fatalf("the reviewer's working copy line = %q, want %q, its own clone", got, want)
			}
			// At admitted RoleFor answers the architect, which works no clone of its own: the line
			// names the issue's workspace, never the reviewer's clone.
			admitted := issue
			admitted.Phase = phase.Admitted
			if got, want := workflow.WorkingCopy(admitted, dir).String(), " Working copy: branch legion/LEGION-208, directory "+tc.want+"."; got != want {
				t.Fatalf("the working copy line at admitted = %q, want %q, the issue's workspace", got, want)
			}
			unparsed := reviewing
			unparsed.Repo = "not a repository"
			if got, want := workflow.WorkingCopy(unparsed, dir).String(), " Working copy: branch legion/LEGION-208."; got != want {
				t.Fatalf("the working copy line of a repository that does not parse = %q, want %q", got, want)
			}
		})
	}
}
