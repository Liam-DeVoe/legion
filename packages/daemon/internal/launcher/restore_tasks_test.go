package launcher

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/shimwire"
	"github.com/sjawhar/legion/daemon/internal/testbin"
	"github.com/sjawhar/legion/daemon/internal/workspace"
)

// bundleTasks are the branches of the two tasks whose working copies sit beside the released
// issue's own: the first give of one slug, and a later give of another, whose part after the key
// holds a dot of its own and is read whole.
var bundleTasks = []string{"legion/" + bundleIssue + ".d", "legion/" + bundleIssue + ".b.2"}

// A released pod's save names the working copy of each task beside the issue's own, by the
// task's branch, at the commit that holds the edit its agent left there — an edit no jj command
// had snapshotted — and the bundle carries each of those commits. The issue's own working copy
// is the save's Head, as it always was, and is no task's.
func TestTheSaveNamesTheWorkingCopyOfEachTask(t *testing.T) {
	testbin.RequireJJFilters(t)
	f := newTaskBundleFixture(t)
	saved := f.bundleWorkspaceOne()

	if len(saved.WorkingCopies) != len(bundleTasks) {
		t.Fatalf("the save names the working copies %v, want one for each of %v and none for the issue's own", saved.WorkingCopies, bundleTasks)
	}
	heads := f.bundleHeads(saved)
	for _, branch := range bundleTasks {
		dir := f.taskDir(f.ws, branch)
		at := f.run(dir, "jj", "log", "-R", dir, "--ignore-working-copy", "-r", "@", "--no-graph", "-T", "commit_id")
		if saved.WorkingCopies[branch] != at {
			t.Fatalf("the save names the working copy of %s as %q, want %s", branch, saved.WorkingCopies[branch], at)
		}
		if got := f.run(dir, "git", "--git-dir="+f.gitDir(dir), "show", at+":"+taskFile(branch)); got+"\n" != taskEdit(branch) {
			t.Fatalf("the working copy of %s was saved on %s, which holds %q, not its agent's edit", branch, at, got)
		}
		if !slices.Contains(heads, at) {
			t.Fatalf("the bundle carries the heads %v, not the working copy of %s on %s", heads, branch, at)
		}
	}
}

// The pod that replaces the released one puts each task's working copy back beside the issue's
// own before any role starts: at the directory TaskDir names, a workspace of the pod's clone on
// the commit the save named, holding its agent's edit, its git worktree entry locked as
// provisioning locks one, and named in the restore's notice. The issue's own working copy is
// restored as it always was.
func TestTheRestorePutsEachTasksWorkingCopyBack(t *testing.T) {
	testbin.RequireJJFilters(t)
	f := newTaskBundleFixture(t)
	saved := f.bundleWorkspaceOne()

	ws := f.secondPod("ws2")
	notice := f.restoreInto(ws, saved, 1)
	f.requireRestored(ws, saved)
	f.requireTasksRestored(ws, saved, notice)
}

// newTaskBundleFixture is the bundle fixture whose released pod is laid out as provisioning lays
// one out (workspace.Location), which is where a task's working copy sits beside its issue's
// (workspace.TaskDir): the issue's own unpushed work (workOne), and a working copy of the pod's
// clone for each of bundleTasks, each holding an edit no jj command has snapshotted.
func newTaskBundleFixture(t *testing.T) *bundleFixture {
	t.Helper()
	f := newBundleFixture(t)
	f.ws = f.secondPod("released")
	f.workOne()
	for _, branch := range bundleTasks {
		dir := f.taskDir(f.ws, branch)
		f.run(f.ws, "jj", "workspace", "add", "-R", f.ws, "--name", filepath.Base(dir), "--revision", "trunk()", dir)
		f.write(filepath.Join(dir, taskFile(branch)), taskEdit(branch))
	}
	return f
}

// requireTasksRestored fails the test unless each task's working copy of saved is back beside
// the issue's working copy ws, on its saved commit, with its edit, its git worktree entry locked,
// and its branch named in the restore's notice.
func (f *bundleFixture) requireTasksRestored(ws string, saved shimwire.WorkspaceBundle, notice string) {
	f.t.Helper()
	worktrees := f.run(ws, "git", "--git-dir="+f.gitDir(ws), "worktree", "list", "--porcelain")
	for _, branch := range bundleTasks {
		dir := f.taskDir(ws, branch)
		if _, err := os.Stat(dir); err != nil {
			f.t.Fatalf("the restore put no working copy of %s back at %s (%v); its notice: %s", branch, dir, err, notice)
		}
		if head := f.run(dir, "jj", "log", "-R", dir, "--ignore-working-copy", "-r", "@", "--no-graph", "-T", "commit_id"); head != saved.WorkingCopies[branch] {
			f.t.Fatalf("the restored working copy of %s is on %s, want the saved %s", branch, head, saved.WorkingCopies[branch])
		}
		if body, err := os.ReadFile(filepath.Join(dir, taskFile(branch))); err != nil || string(body) != taskEdit(branch) {
			f.t.Fatalf("the restored working copy of %s reads %q (%v), want its agent's edit", branch, body, err)
		}
		if !lockedWorktree(worktrees, dir) {
			f.t.Fatalf("the git worktree entry of the restored working copy %s is not locked:\n%s", dir, worktrees)
		}
		if !strings.Contains(notice, branch) {
			f.t.Fatalf("the restore's notice names no working copy of %s: %s", branch, notice)
		}
	}
}

// lockedWorktree is whether `git worktree list --porcelain` lists dir's entry as locked.
func lockedWorktree(porcelain, dir string) bool {
	for _, block := range strings.Split(porcelain, "\n\n") {
		lines := strings.Split(block, "\n")
		if lines[0] == "worktree "+dir {
			return slices.ContainsFunc(lines, func(line string) bool { return line == "locked" || strings.HasPrefix(line, "locked ") })
		}
	}
	return false
}

// taskDir is the directory of the working copy of the task whose branch is branch, beside the
// issue's working copy ws.
func (f *bundleFixture) taskDir(ws, branch string) string {
	f.t.Helper()
	dir, err := workspace.TaskDir(ws, branch)
	if err != nil {
		f.t.Fatal(err)
	}
	return dir
}

// bundleHeads is the commit of each head saved's bundle carries, as git reads them.
func (f *bundleFixture) bundleHeads(saved shimwire.WorkspaceBundle) []string {
	f.t.Helper()
	path := filepath.Join(f.t.TempDir(), "saved.bundle")
	f.write(path, string(saved.Bundle))
	var heads []string
	for _, line := range strings.Split(f.run(f.ws, "git", "--git-dir="+f.gitDir(f.ws), "bundle", "list-heads", path), "\n") {
		commit, _, _ := strings.Cut(line, " ")
		heads = append(heads, commit)
	}
	return heads
}

// taskFile is the file a task's agent edits in its working copy, and taskEdit what it writes.
func taskFile(branch string) string { return strings.TrimPrefix(branch, "legion/") + ".txt" }

func taskEdit(branch string) string { return "the work of " + branch + "\n" }
