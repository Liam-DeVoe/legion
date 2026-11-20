package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
	"github.com/sjawhar/legion/daemon/internal/prompts"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/runtime/sandbox"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/workspace"
)

// Under a runtime that leaves workspaces to the daemon's host (tmux), a reviewer's launch makes
// sure of the issue's workspace and then of the reviewer's own clone (ensureReviewClone), and is
// told its git (workspace/reviewgit.go): the shim InstallReviewGit writes first on its PATH, no
// global or system configuration, and the pod's own git pairs followed by the fsmonitor off and
// the hooks at /dev/null, after the role's git identity. Another role's launch is told none of it
// and makes no review clone; a launch whose review clone cannot be made is refused, naming the
// claim; and under a runtime that provisions its own (a Sandbox pod), which refuses PATH and the
// GIT_CONFIG names in a spec, nothing of it is asked of the host or told the spec.
func TestAHostReviewerWorksInACloneOfItsOwnWithItsOwnGit(t *testing.T) {
	composer, err := prompts.New(t.TempDir())
	if err != nil {
		t.Fatalf("compose the shipped prompts: %v", err)
	}
	var ensured []string
	var refuse error
	repo := ghrepo.MustParse("acme/widgets")
	bot := runtime.GitIdentity{Name: "legion-review[bot]", Email: "9+legion-review[bot]@users.noreply.github.com"}
	state := t.TempDir()
	s := specs{
		stateDir: state, project: "s1", prompts: composer, appOwners: noAppOwners,
		repository: func(context.Context, string) (ghrepo.Repository, string, error) {
			return repo, "release/v7.1", nil
		},
		identity:       func(context.Context, claim.Role, ghrepo.Repository) (runtime.GitIdentity, error) { return bot, nil },
		hostWorkspaces: true,
		ensureWorkspace: func(_ context.Context, issue string, repo ghrepo.Repository, defaultBranch string) error {
			ensured = append(ensured, "workspace "+issue+" "+repo.String()+"@"+defaultBranch)
			return nil
		},
		ensureReviewClone: func(_ context.Context, issue string, repo ghrepo.Repository, defaultBranch string) error {
			ensured = append(ensured, "review clone "+issue+" "+repo.String()+"@"+defaultBranch)
			return refuse
		},
	}
	launch := func(role claim.Role) (runtime.SpawnSpec, error) {
		t.Helper()
		token, err := claim.NewToken("s1", "S1-2", role)
		if err != nil {
			t.Fatal(err)
		}
		return s.SpawnSpec(context.Background(), supervise.Claim{Token: token, Issue: "S1-2", Tree: "S1-1", Role: role})
	}

	spec, err := launch(claim.RoleReviewer)
	if err != nil {
		t.Fatalf("SpawnSpec of the reviewer: %v", err)
	}
	if want := []string{"workspace S1-2 acme/widgets@release/v7.1", "review clone S1-2 acme/widgets@release/v7.1"}; !slices.Equal(ensured, want) {
		t.Errorf("the reviewer's launch ensured %q, want %q", ensured, want)
	}
	bin := filepath.Join(workspace.ReviewGitDir(state), "bin")
	if first := strings.Split(spec.Env["PATH"], ":")[0]; first != bin {
		t.Errorf("the reviewer's PATH starts %q, want the shim's directory %s", first, bin)
	}
	if info, err := os.Stat(filepath.Join(bin, "git")); err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Errorf("the reviewer's git shim at %s is no executable file: %v", filepath.Join(bin, "git"), err)
	}
	pairs := slices.Concat(sandbox.GitConfigPairs(), [][2]string{{"core.fsmonitor", "false"}, {"core.hooksPath", "/dev/null"}})
	want := map[string]string{"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_COUNT": strconv.Itoa(len(pairs))}
	for i, pair := range pairs {
		want["GIT_CONFIG_KEY_"+strconv.Itoa(i)], want["GIT_CONFIG_VALUE_"+strconv.Itoa(i)] = pair[0], pair[1]
	}
	for name, value := range bot.Env() {
		want[name] = value
	}
	for name, value := range want {
		if spec.Env[name] != value {
			t.Errorf("the reviewer's %s = %q, want %q", name, spec.Env[name], value)
		}
	}
	if last := pairs[len(pairs)-1]; last != [2]string{"core.hooksPath", "/dev/null"} {
		t.Errorf("the reviewer's last git pair is %q, want its hooks at /dev/null, which git reads last and so takes", last)
	}

	ensured = nil
	spec, err = launch(claim.RoleImplementer)
	if err != nil {
		t.Fatalf("SpawnSpec of the implementer: %v", err)
	}
	if want := []string{"workspace S1-2 acme/widgets@release/v7.1"}; !slices.Equal(ensured, want) {
		t.Errorf("the implementer's launch ensured %q, want %q", ensured, want)
	}
	for name := range spec.Env {
		if name == "PATH" || strings.HasPrefix(name, "GIT_CONFIG_") {
			t.Errorf("the implementer is told %s, which is the reviewer's alone", name)
		}
	}

	refuse = errors.New("the shared clone records no branch")
	reviewer, err := claim.NewToken("s1", "S1-2", claim.RoleReviewer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := launch(claim.RoleReviewer); err == nil || !strings.Contains(err.Error(), "the review clone of "+string(reviewer)) || !strings.Contains(err.Error(), "records no branch") {
		t.Errorf("SpawnSpec with a review clone that failed = %v, want the refusal naming the claim", err)
	}

	ensured, refuse = nil, nil
	s.hostWorkspaces = false
	spec, err = launch(claim.RoleReviewer)
	if err != nil {
		t.Fatalf("SpawnSpec under a runtime that provisions its own: %v", err)
	}
	if len(ensured) != 0 {
		t.Errorf("a pod reviewer's launch ensured %q on the host, want nothing", ensured)
	}
	for name := range spec.Env {
		if name == "PATH" || strings.HasPrefix(name, "GIT_CONFIG_") {
			t.Errorf("a pod reviewer's spec carries %s, which the Sandbox runtime sets itself", name)
		}
	}
}
