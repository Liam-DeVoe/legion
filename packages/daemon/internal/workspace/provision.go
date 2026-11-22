package workspace

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
	"github.com/sjawhar/legion/daemon/internal/rolefile"
	"golang.org/x/sys/unix"
)

// Provision ports provisionIssueWorkspace (packages/workspace/src/workspace.ts). It updates an
// existing working copy, obtains the shared clone through a temporary sibling, protects unreachable
// worker commits before every fetch, resolves a bookmark before adding, and leaves pane credentials
// on the clone.
// The clone and the fetch reach the repository through request.Source: a pod's feed with no
// credential, or GitHub with the one-shot credential. An issue with no branch starts at
// request.DefaultBranch, which the request must name. Every command on the shared clone runs under
// request.StoreLock, the `jj workspace add` that registers the issue's workspace with no files
// among them. Only the checkout (`jj sparse reset`), which writes the issue's own working copy and
// nothing of the clone's, runs with the lock let go, holding one of request.CheckoutSlot's places
// instead.
func Provision(ctx context.Context, run Runner, request Request) (Workspace, error) {
	workspace, err := Location(request.StateDir, request.Repo, request.Issue)
	if err != nil {
		return Workspace{}, err
	}
	if _, err := guardWorkspace(workspace.Dir, workspace.Clone, false); err != nil {
		return Workspace{}, err
	}
	if request.CredentialHelper == "" {
		return Workspace{}, fmt.Errorf("workspace credential helper is required")
	}
	if request.Log == nil {
		return Workspace{}, errors.New("workspace request names no log")
	}
	if request.DefaultBranch == "" {
		return Workspace{}, errors.New("workspace default branch is required")
	}
	workspace.DefaultBranch = request.DefaultBranch
	if request.Source == nil {
		return Workspace{}, errors.New("workspace request names no way to the repository: FromFeed or FromGitHub")
	}
	if err := request.Source.check(request.Repo); err != nil {
		return Workspace{}, err
	}

	store := &storeLock{locker: request.StoreLock}
	store.lock()
	defer store.unlock()
	exists, err := pathExists(workspace.Dir)
	if err != nil {
		return Workspace{}, err
	}
	if exists {
		if _, err := RunCheckedIn(ctx, run, workspace, []string{"jj", "workspace", "update-stale"}); err != nil {
			return Workspace{}, err
		}
	}
	source, err := request.Source.open(request.Repo, workspace.Bookmark, request.DefaultBranch)
	if err != nil {
		return Workspace{}, err
	}
	defer func() {
		_ = source.remove()
	}()
	if err := ensureRepoClone(ctx, run, WallClock{}, workspace.Clone, GitHubURL(request.Repo), request.DefaultBranch, source); err != nil {
		return Workspace{}, err
	}
	if err := ensureFetchConfiguration(ctx, run, workspace.Clone, source); err != nil {
		return Workspace{}, err
	}
	if err := source.remove(); err != nil {
		return Workspace{}, err
	}

	if exists {
		if err := restoreGitWorktree(ctx, run, workspace, request.Log); err != nil {
			return Workspace{}, err
		}
	} else if err := createWorkspace(ctx, run, workspace, request.Log); err != nil {
		return Workspace{}, err
	}
	checkout := !exists
	if exists {
		// A workspace whose checkout never finished — registered with no files, then the checkout
		// failed or the daemon stopped — is checked out now, or its pane would start in an empty
		// working copy.
		whole, err := CheckedOut(ctx, run, workspace)
		if err != nil {
			return Workspace{}, err
		}
		checkout = !whole
	}
	// Every workspace provisioning touches has its git worktree entry locked, one added before
	// provisioning locked any included, so a bare `git worktree prune` that cannot see it skips it.
	if err := lockGitWorktree(workspace.Clone, workspace.Dir); err != nil {
		return Workspace{}, err
	}
	if err := configureRepositoryCredential(ctx, run, workspace.Clone, request.CredentialHelper); err != nil {
		return Workspace{}, err
	}
	// Every provision runs these, so a clone made before a setting existed takes it at its next
	// provision.
	if err := configureCloneReads(ctx, run, workspace.Clone); err != nil {
		return Workspace{}, err
	}
	if err := configureCloneDefaultRepository(ctx, run, workspace.Clone); err != nil {
		return Workspace{}, err
	}
	if err := removeRepositoryIdentity(ctx, run, workspace.Clone); err != nil {
		return Workspace{}, err
	}
	// Keeps every future `codegraph init`/`index` in this workspace — the host warm-up's, and a
	// worker's own `codegraph` tool call inside a pod, which this function also provisions
	// (cmd/legion workspace-init) — from ever getting its `.codegraph/` tracked. CodeGraph's own
	// generated `.codegraph/.gitignore` is `*` then `!.gitignore`, so that one file stays visible
	// to git; without this, jj's default auto-track snapshots it into the workspace's own change
	// (confirmed empirically: `jj status` under an empty HOME showed `.codegraph/.gitignore`
	// newly added after a bare `codegraph init`). `.git/info/exclude` is local to this shared
	// clone, read by every workspace of it (git worktrees share one `info/exclude` through their
	// common git directory) and by jj (confirmed the same way: with the line added first, the
	// same `codegraph init` left `jj status` clean), so this needs no global git configuration
	// anywhere a workspace of this clone is used.
	if err := excludeCodegraphDirectory(workspace.Clone); err != nil {
		return Workspace{}, err
	}
	// The checkout writes the issue's working copy, every file of the tree, into the workspace the
	// registration made with no files: past the shared clone's own clone, whose size-following
	// steps share one CloneTimeout of their own (ensureRepoClone, above), it is the one step whose
	// time follows the repository's size, and it writes nothing of the shared clone's, so it runs
	// with the store lock let go, beside other issues' checkouts, holding one of the places the
	// daemon bounds checkouts by.
	//
	// It is also the one command past those steps that no change of this repository's makes fit
	// CommandTimeout's five minutes: one write of the largest repository a project then worked
	// in took nine minutes in an issue pod (CheckoutTimeout), and the runner killing it there
	// failed the whole provision and spent one of the claim's three launches. So this command
	// alone takes CheckoutTimeout (request.checkoutBound), and every provisioning command but it
	// and those steps keeps the runner's slow-command budget.
	store.unlock()
	if checkout {
		release, err := request.checkoutPlace(ctx)
		if err != nil {
			return Workspace{}, fmt.Errorf("check out workspace %s: wait for a checkout's place: %w", workspace.Dir, err)
		}
		_, err = runCheckedInWithin(ctx, run, workspace, request.checkoutBound(), []string{"jj", "sparse", "reset"})
		release()
		if err != nil {
			return Workspace{}, fmt.Errorf("check out workspace %s: %w", workspace.Dir, err)
		}
	}
	return workspace, nil
}

// CheckedOut is whether workspace's checkout finished: it holds at least one sparse pattern — the
// whole tree (`.`) after Provision's checkout, or whatever narrower set an agent chose since, which
// a launch must not undo. Provision registers a workspace with no pattern and no file (`jj
// workspace add --sparse-patterns empty`) and checks it out after (`jj sparse reset`), so a
// workspace whose checkout failed or was cut short — a jj error, a full disk, a daemon stopped
// mid-checkout — holds no pattern until a provision checks it out. A shared clone made from a
// pod's feed permanently holds none either, its patterns cleared when it was made so it carries no
// working tree (feedClone's `jj sparse set --clear`), and that is no unfinished checkout. So
// CheckedOut refuses two directories rather than answering for them: the shared clone itself, and
// any directory under the clone root that clone sits in (CloneRoot). A clone's empty pattern set
// would answer false, the caller would read that as an unfinished checkout, and its `jj sparse
// reset` would write the whole tree into the clone. The read never snapshots the working copy
// (--ignore-working-copy): a pane may be writing it, and a snapshot would run the working-copy
// filters, fsmonitor and signing programs a tree agent can configure. A directory with no `.jj` is
// refused, as every jj command in a workspace is (guardWorkspace).
func CheckedOut(ctx context.Context, run Runner, workspace Workspace) (bool, error) {
	// A clone sits at <clone root>/<owner>/<name> (Location), so the root it sits in is its
	// grandparent, and the clone itself is under that root. The clone's own case is therefore
	// first, for the refusal that names what the directory is. Both come before jj runs at all.
	if workspace.Clone != "" {
		cloneRoot := filepath.Dir(filepath.Dir(workspace.Clone))
		switch {
		case workspace.Dir == workspace.Clone:
			return false, fmt.Errorf("CheckedOut cannot answer for %s: that directory is the shared clone itself, not a workspace of one."+whyACloneHasNoAnswer, workspace.Dir)
		case underDirectory(cloneRoot, workspace.Dir):
			return false, fmt.Errorf("CheckedOut cannot answer for %s: that directory is under the clone root %s, which holds a shared clone of each repository."+whyACloneHasNoAnswer, workspace.Dir, cloneRoot)
		}
	}
	listed, err := RunCheckedIn(ctx, run, workspace, []string{"jj", "sparse", "list", "--ignore-working-copy", "--color=never"})
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(listed.Stdout) != "", nil
}

// whyACloneHasNoAnswer is the tail of both of CheckedOut's refusals: the reason a clone's pattern
// set answers nothing, which is the same whichever clone directory was asked about.
const whyACloneHasNoAnswer = " A clone holds no sparse pattern by design, because it carries no working tree (feedClone's `jj sparse set --clear`). An answer of false here reads as an unfinished checkout, and the caller then writes the whole tree into the clone"

// underDirectory is whether dir is root or lies below it, compared one path segment at a time
// (filepath.Rel, then filepath.IsLocal, as cmd/legion's sessions.go and runtime/sandbox's
// manifest.go compare a path with its root). A string prefix would call /a/bc a child of /a/b.
func underDirectory(root, dir string) bool {
	relative, err := filepath.Rel(root, dir)
	if err != nil {
		return false
	}
	return filepath.IsLocal(relative)
}

// excludeLimit bounds how much of the shared clone's .git/info/exclude excludeCodegraphDirectory
// reads: an exclude file a person or a tool wrote can pass gitdirLimit's 4 KiB, and one past a
// MiB is no list of patterns anyone keeps, so it is refused rather than read whole into memory.
const excludeLimit = 1 << 20

// excludeCodegraphDirectory appends ".codegraph/" to cloneDir's ".git/info/exclude" unless a
// line already matches it exactly, so repeated provisioning of the same clone writes it once. A
// missing .git or info directory is made at 0700, and a missing exclude file at 0600.
//
// The file is on the issue's volume, which every role of the pod writes, and the call runs under
// whatever Provision's caller holds: the tmux daemon's outbox, with the repository's store lock
// held, which every later provision and removal of the repository waits on. So it is opened once,
// read and appended to through that one descriptor, and opened as rolefile.OpenRegular opens a file
// a role can write: a symlink there is refused rather than followed, which would grow whatever file
// the link names, and a fifo or a device is refused rather than waited on, in its open or its read.
// A directory, or a file larger than excludeLimit, is refused too, each naming the file. The clone
// and the two directories between it and the file are opened the same way, without following a
// link (rolefile.OpenDirectory), each under the descriptor of the one above it, so a link a role
// leaves at .git or at info is refused as one at the file is, and nothing below the clone is
// reached by a path a role could swap between two opens.
func excludeCodegraphDirectory(cloneDir string) error {
	excludePath := filepath.Join(cloneDir, ".git", "info", "exclude")
	dir, err := rolefile.OpenDirectory(unix.AT_FDCWD, cloneDir, cloneDir)
	if err != nil {
		return fmt.Errorf("read %s: %w", excludePath, err)
	}
	path := cloneDir
	for _, name := range []string{".git", "info"} {
		path = filepath.Join(path, name)
		next, err := rolefile.OpenDirectory(dir, name, path)
		if errors.Is(err, fs.ErrNotExist) {
			if err := unix.Mkdirat(dir, name, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
				_ = unix.Close(dir)
				return fmt.Errorf("create %s: %w", path, err)
			}
			next, err = rolefile.OpenDirectory(dir, name, path)
		}
		_ = unix.Close(dir)
		if err != nil {
			return fmt.Errorf("read %s: %w", excludePath, err)
		}
		dir = next
	}
	defer unix.Close(dir)
	file, err := rolefile.OpenRegular(dir, "exclude", excludePath, unix.O_RDWR|unix.O_CREAT|unix.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("read %s: %w", excludePath, err)
	}
	defer file.Close()
	existing, err := rolefile.ReadBounded(file, excludePath, excludeLimit)
	if err != nil {
		return fmt.Errorf("read %s: %w", excludePath, err)
	}
	for _, line := range strings.Split(string(existing), "\n") {
		if line == ".codegraph/" {
			return nil
		}
	}
	content := ".codegraph/\n"
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		content = "\n" + content
	}
	if _, err := file.WriteString(content); err != nil {
		return fmt.Errorf("write %s: %w", excludePath, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("write %s: %w", excludePath, err)
	}
	return nil
}

// Bookmark is the jj bookmark an issue's workspace is on: its branch (claim.IssueBranch).
func Bookmark(issue string) string { return claim.IssueBranch(issue) }

// layoutDepth is how many directories below the state directory a shared clone and a workspace
// each sit (Location): repos/<host>/<owner>/<name>, and workspaces/<owner>/<name>/<issue>.
const layoutDepth = 4

// CloneRoot is the directory under stateDir that holds every repository's shared clone, each at
// <owner>/<name> below it (Location).
func CloneRoot(stateDir string) string {
	return filepath.Join(stateDir, "repos", "github.com")
}

// Location is the deterministic workspace location Provision creates for one issue, and the shared
// clone it is a jj workspace of. The repository arrives parsed (ghrepo.Parse); a `.` or `..`
// issue is refused: joined under the repository's workspaces directory it names that directory,
// or its owner's, and Remove deletes a workspace directory whole.
func Location(stateDir string, repository ghrepo.Repository, issue string) (Workspace, error) {
	if repository.IsZero() {
		return Workspace{}, errors.New("workspace repository is required")
	}
	if stateDir == "" {
		return Workspace{}, fmt.Errorf("workspace state directory is required")
	}
	if issue == "." || issue == ".." {
		return Workspace{}, fmt.Errorf("workspace issue %q is a %q segment; want an issue key", issue, issue)
	}
	if issue == "" || issue != filepath.Base(issue) {
		return Workspace{}, fmt.Errorf("workspace issue must be a single path component")
	}
	return Workspace{
		Dir:      filepath.Join(stateDir, "workspaces", repository.Owner(), repository.Name(), strings.ToLower(issue)),
		Bookmark: Bookmark(issue),
		Clone:    filepath.Join(CloneRoot(stateDir), repository.Owner(), repository.Name()),
		Repo:     repository,
	}, nil
}

// ReviewRoot is the directory under stateDir that holds every reviewer's clone, each at
// <owner>/<name>/<issue> below it (ReviewLocation): under tmux the daemon's state directory, and
// in a pod the issue's volume, where every role but the reviewer has it covered
// (internal/launcher's role-init).
func ReviewRoot(stateDir string) string { return filepath.Join(stateDir, "reviews") }

// ReviewLocation is the reviewer's own clone of issue's repository (ProvisionReviewClone),
// <state>/reviews/<owner>/<name>/<issue>: beside the shared clone and the issue's workspace, and
// under neither, so no other role's work reaches it. It refuses what Location refuses.
func ReviewLocation(stateDir string, repository ghrepo.Repository, issue string) (string, error) {
	located, err := Location(stateDir, repository, issue)
	if err != nil {
		return "", err
	}
	return filepath.Join(ReviewRoot(stateDir), repository.Owner(), repository.Name(), filepath.Base(located.Dir)), nil
}

// IssueWorkspace is the issue's own workspace (Location) when dir is a reviewer's clone
// (ReviewLocation) of that issue, read out of dir's path alone as locationOf reads a workspace's,
// and dir itself otherwise: the reviewer's LEGION_WORKSPACE is its clone, and what works the
// issue's workspace whichever role's variable it reads — a pod launcher's lock sweep and restore,
// a shim's release bundle — works the workspace this answers.
func IssueWorkspace(dir string) string {
	stateDir := stateDirOf(dir)
	owner, name := filepath.Base(filepath.Dir(filepath.Dir(dir))), filepath.Base(filepath.Dir(dir))
	repository, err := ghrepo.Parse("workspace repository", owner+"/"+name)
	if err != nil {
		return dir
	}
	if review, err := ReviewLocation(stateDir, repository, filepath.Base(dir)); err != nil || review != dir {
		return dir
	}
	located, err := Location(stateDir, repository, filepath.Base(dir))
	if err != nil {
		return dir
	}
	return located.Dir
}

// stateDirOf is the state directory of a path layoutDepth below it: a workspace, a shared clone
// or a reviewer's clone.
func stateDirOf(dir string) string {
	for range layoutDepth {
		dir = filepath.Dir(dir)
	}
	return dir
}

// ReviewRequestFor is the request a pod reviewer's role-init makes or refreshes its own clone with
// (internal/launcher): dir, its LEGION_WORKSPACE, must be the ReviewLocation of issue's workspace in
// a state directory's layout, and that workspace's shared clone is what it is cloned from. Any
// other dir is refused, naming it.
func ReviewRequestFor(dir, issue, defaultBranch string) (ReviewRequest, error) {
	shared := IssueWorkspace(dir)
	located, err := locationOf(shared)
	if err != nil || shared == dir {
		return ReviewRequest{}, fmt.Errorf("%s is no reviewer's clone of an issue's workspace in a state directory's layout", dir)
	}
	if want, err := ReviewLocation(stateDirOf(shared), located.Repo, issue); err != nil || want != dir {
		return ReviewRequest{}, fmt.Errorf("%s is not the reviewer's clone of %s", dir, issue)
	}
	return ReviewRequest{Clone: located.Clone, Dir: dir, Repo: located.Repo, Issue: issue, DefaultBranch: defaultBranch}, nil
}

// ReviewRootOf is the directory every reviewer's clone of the state directory sits under
// (ReviewRoot), for the issue's workspace dir in that directory's layout: what a pod's role-init
// covers in every role's namespace but the reviewer's.
func ReviewRootOf(dir string) (string, error) {
	if _, err := locationOf(dir); err != nil {
		return "", err
	}
	return ReviewRoot(stateDirOf(dir)), nil
}

// ReviewRequest is where ProvisionReviewClone makes a reviewer's clone and what from. Clone is the
// shared clone the review clone takes its objects from, and its record of origin's branches; Dir
// is the review clone (ReviewLocation). The head is Issue's branch (Bookmark) as the shared clone
// last saw it at origin, else DefaultBranch's, for an issue whose branch origin no longer holds.
// StoreLock and CheckoutSlot are Request's, for the same steps: the lock is held while the shared
// clone is read, and the checkout takes a place. A pod's reviewer passes neither: its role-init is
// the one provision of its pod's review clone, and a git clone or fetch reads a shared clone that
// another role's git writes at that moment as git reads any repository it shares. Log, when set,
// is told each stale lock a refresh removes.
type ReviewRequest struct {
	Clone, Dir           string
	Repo                 ghrepo.Repository
	Issue, DefaultBranch string
	StoreLock            sync.Locker
	CheckoutSlot         func(ctx context.Context) (release func(), err error)
	Log                  func(string)
}

// reviewProvisionEnv is what the git of ProvisionReviewClone's clone, fetch, head and checkout
// reads besides the runner's pins: no global or system configuration, the fsmonitor off and no
// hook, and the file transport alone, the shared clone being a local path.
var reviewProvisionEnv = append(ReviewGitEnv(ReviewGitConfig()), "GIT_ALLOW_PROTOCOL=file")

// reviewStagingSuffix ends the name of the temporary sibling a review clone is made in.
const reviewStagingSuffix = ".review-"

// ProvisionReviewClone makes the reviewer's own clone at request.Dir, or brings the one there to
// the head. A new one is a `git clone --no-checkout --no-hardlinks` of the shared clone, which
// copies its object files rather than linking them, so no role's write of the shared clone's
// object files reaches the review clone, then origin's
// branches as the shared clone records them (its refs/remotes/origin, which the clone alone does
// not copy), origin set to GitHub with the shared clone's credential helper and gh's default
// repository, then a checkout of the head, detached, and jj colocated with it, since the shim's
// adoption of the working copy and every role's prompt run jj in the workspace. It is made under a
// temporary sibling and renamed into place only once whole, so a clone that stands at Dir is a
// finished one, and one that stands there without its .jj is no clone of this function's, and is
// removed and made again; a temporary sibling an earlier attempt left when it was killed is
// removed before the next is made.
//
// A clone already there is brought to the head as a fresh one would stand, at every provision, so
// a reviewer never starts on the head its clone was first made at: the git lock files a reviewer's
// git killed mid-write left in it are removed, since its only writer is the reviewer, which is not
// running while its next launch provisions; origin's branches are fetched again from the shared
// clone's objects, by its path and with no network; and `jj new <head>` puts the working copy on
// the head, leaving what the reviewer wrote there before in a change of its own.
//
// No other role's run writes it: a clone copies neither the shared clone's configuration nor its
// hooks, and no normal git operation rewrites an object file another clone links to. Its clone,
// fetch, head and checkout read no global or system configuration and run no fsmonitor or hook
// (reviewProvisionEnv), so nothing planted in the shared clone or the machine's configuration
// runs here; the reviewer's own git is held the same way (ReviewGitEnv, InstallReviewGit). It goes
// with the issue's workspace (RemoveReviewClone).
func ProvisionReviewClone(ctx context.Context, run Runner, request ReviewRequest) error {
	switch {
	case !filepath.IsAbs(request.Clone) || !filepath.IsAbs(request.Dir):
		return fmt.Errorf("review clone %q of %q: each must be an absolute path", request.Dir, request.Clone)
	case underDirectory(request.Clone, request.Dir) || underDirectory(request.Dir, request.Clone):
		return fmt.Errorf("review clone %s and the shared clone %s: neither may lie in the other", request.Dir, request.Clone)
	case request.Repo.IsZero():
		return errors.New("review clone repository is required")
	case request.Issue == "":
		return errors.New("review clone issue is required")
	}
	if err := refuseSymlinkedLayout(request.Dir, request.Dir); err != nil {
		return err
	}
	complete := false
	if exists, err := pathExists(request.Dir); err != nil {
		return err
	} else if exists {
		if complete, err = pathExists(filepath.Join(request.Dir, ".jj")); err != nil {
			return err
		}
		if !complete {
			if err := rolefile.RemoveTree(request.Dir); err != nil {
				return fmt.Errorf("remove incomplete review clone %s: %w", request.Dir, err)
			}
		}
	}
	if complete {
		return refreshReviewClone(ctx, run, request)
	}
	parent := filepath.Dir(request.Dir)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("make the review clone's parent: %w", err)
	}
	abandoned, err := filepath.Glob(filepath.Join(parent, filepath.Base(request.Dir)+reviewStagingSuffix+"*"))
	if err != nil {
		return err
	}
	for _, dir := range abandoned {
		if err := rolefile.RemoveTree(dir); err != nil {
			return fmt.Errorf("remove an abandoned review clone %s: %w", dir, err)
		}
	}
	temporary, err := os.MkdirTemp(parent, filepath.Base(request.Dir)+reviewStagingSuffix)
	if err != nil {
		return fmt.Errorf("make the review clone's temporary directory: %w", err)
	}
	defer func() { _ = rolefile.RemoveTree(temporary) }()
	store := &storeLock{locker: request.StoreLock}
	store.lock()
	clone := []string{"git", "clone", "--quiet", "--no-checkout", "--no-hardlinks", request.Clone, temporary}
	result, err := runCommandTimeout(ctx, run, clone, reviewProvisionEnv, parent, CloneTimeout)
	if _, err = checkedResult(clone, result, err); err == nil {
		err = fetchForReview(ctx, run, temporary, "origin")
	}
	store.unlock()
	if err != nil {
		return err
	}
	head, err := reviewHead(ctx, run, request, temporary)
	if err != nil {
		return err
	}
	gitDir := "--git-dir=" + filepath.Join(temporary, ".git")
	if _, err := RunChecked(ctx, run, []string{"git", gitDir, "remote", "set-url", "origin", GitHubURL(request.Repo)}, reviewProvisionEnv, temporary); err != nil {
		return err
	}
	if err := configureRepositoryCredential(ctx, run, temporary, GitHubCredentialHelper); err != nil {
		return err
	}
	if err := configureCloneDefaultRepository(ctx, run, temporary); err != nil {
		return err
	}
	err = inCheckoutPlace(ctx, request, func(bound time.Duration) error {
		for _, step := range []struct {
			dir  string
			argv []string
			env  []string
		}{
			{temporary, []string{"git", "checkout", "--quiet", "--detach", head}, reviewProvisionEnv},
			// jj opens no repository here, so it runs in the parent (cloneSteps.dir says why).
			{parent, []string{"jj", "git", "init", "--colocate", temporary}, nil},
		} {
			result, err := runCommandTimeout(ctx, run, step.argv, step.env, step.dir, bound)
			if _, err := checkedResult(step.argv, result, err); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := requireReviewHead(ctx, run, temporary, head); err != nil {
		return err
	}
	if err := os.Rename(temporary, request.Dir); err != nil {
		if complete, statErr := pathExists(filepath.Join(request.Dir, ".jj")); cloneCollision(err) && statErr == nil && complete {
			return nil
		}
		return fmt.Errorf("rename the review clone into place: %w", err)
	}
	return nil
}

// refreshReviewClone brings the review clone at request.Dir to the head (ProvisionReviewClone):
// the stale locks under its git directory removed, origin's branches fetched again from the shared
// clone by its path, under its store lock, then `jj new <head>` in the clone, in a checkout place.
func refreshReviewClone(ctx context.Context, run Runner, request ReviewRequest) error {
	log := request.Log
	if log == nil {
		log = func(string) {}
	}
	why := "a review clone's only writer is its reviewer, which is not running while its launch provisions"
	if err := removeLocksUnder(filepath.Join(request.Dir, ".git"), time.Now(), log, why); err != nil {
		return err
	}
	store := &storeLock{locker: request.StoreLock}
	store.lock()
	err := fetchForReview(ctx, run, request.Dir, request.Clone)
	store.unlock()
	if err != nil {
		return err
	}
	head, err := reviewHead(ctx, run, request, request.Dir)
	if err != nil {
		return err
	}
	err = inCheckoutPlace(ctx, request, func(bound time.Duration) error {
		argv := []string{"jj", "new", head, "--color=never", "-R", request.Dir}
		result, err := runCommandTimeout(ctx, run, argv, nil, request.Dir, bound)
		_, err = checkedResult(argv, result, err)
		return err
	})
	if err != nil {
		return err
	}
	return requireReviewHead(ctx, run, request.Dir, head)
}

// requireReviewHead refuses the review clone at dir unless the commit its git HEAD names is head,
// the one the provision brought it to: a reviewer reads the change through that HEAD, so a clone
// left at any other commit is refused, naming both, rather than handed to the reviewer.
func requireReviewHead(ctx context.Context, run Runner, dir, head string) error {
	result, err := RunChecked(ctx, run, []string{"git", "--git-dir=" + filepath.Join(dir, ".git"), "rev-parse", "HEAD"}, reviewProvisionEnv, dir)
	if err != nil {
		return err
	}
	if at := strings.TrimSpace(result.Stdout); at != head {
		return fmt.Errorf("review clone %s stands on %s, not on %s, the head under review", dir, at, head)
	}
	return nil
}

// inCheckoutPlace runs checkout, a step that writes the review clone's whole working copy, holding
// one of request's checkout places and given the checkout's bound, as Provision's own checkout is.
func inCheckoutPlace(ctx context.Context, request ReviewRequest, checkout func(bound time.Duration) error) error {
	slot := Request{CheckoutSlot: request.CheckoutSlot}
	release, err := slot.checkoutPlace(ctx)
	if err != nil {
		return fmt.Errorf("check out review clone %s: wait for a checkout's place: %w", request.Dir, err)
	}
	defer release()
	if err := checkout(slot.checkoutBound()); err != nil {
		return fmt.Errorf("check out review clone %s: %w", request.Dir, err)
	}
	return nil
}

// fetchForReview fetches origin's branches as the shared clone records them (its
// refs/remotes/origin) from from — the clone's own origin while it still names the shared clone,
// else the shared clone's path — into the review clone at dir, pruning the ones the shared clone
// no longer has.
func fetchForReview(ctx context.Context, run Runner, dir, from string) error {
	_, err := RunChecked(ctx, run, []string{"git", "--git-dir=" + filepath.Join(dir, ".git"), "fetch", "--quiet", "--no-tags",
		"--prune", from, "+refs/remotes/origin/*:refs/remotes/origin/*"}, reviewProvisionEnv, dir)
	return err
}

// reviewHead is the head the review clone at dir is to stand on: Issue's branch at origin, else
// DefaultBranch's, as its last fetch (fetchForReview) recorded them.
func reviewHead(ctx context.Context, run Runner, request ReviewRequest, dir string) (string, error) {
	for _, branch := range []string{Bookmark(request.Issue), request.DefaultBranch} {
		if branch == "" {
			continue
		}
		argv := []string{"git", "--git-dir=" + filepath.Join(dir, ".git"), "rev-parse", "--verify", "--quiet",
			"refs/remotes/origin/" + branch + "^{commit}"}
		found, err := runCommand(ctx, run, argv, reviewProvisionEnv, dir)
		if err := answered(argv, found, err); err != nil {
			return "", err
		}
		if found.ExitCode == 0 {
			return strings.TrimSpace(found.Stdout), nil
		}
	}
	return "", fmt.Errorf("review clone of %s: the shared clone %s records neither %s nor %q at origin", request.Repo, request.Clone, Bookmark(request.Issue), request.DefaultBranch)
}

// RemoveReviewClone removes the reviewer's clone at dir, which its issue's workspace removal does
// with the workspace. It renames the clone aside first (removingSuffix, as Remove does), so a kill
// in the slower delete after leaves no half a clone at dir, which ProvisionReviewClone would
// otherwise take for a whole one; an aside an earlier removal left is cleared first.
func RemoveReviewClone(dir string) error {
	aside := removingPath(dir)
	if err := rolefile.RemoveTree(aside); err != nil {
		return fmt.Errorf("clear a stale removal of review clone %s: %w", dir, err)
	}
	if exists, err := pathExists(dir); err != nil {
		return err
	} else if exists {
		if err := os.Rename(dir, aside); err != nil {
			return fmt.Errorf("rename review clone %s aside before removing it: %w", dir, err)
		}
	}
	return rolefile.RemoveTree(aside)
}

// locationOf is the workspace Location puts at dir, read out of dir's path alone: the owner and
// the name are the two directories above dir's own, the issue is dir's own base name, and the
// state directory is layoutDepth above it. A dir Location does not put exactly there — one too
// shallow for the layout, one whose owner and name are no repository, one whose issue Location
// would case differently — is in no layout this inverts, and is refused rather than guessed at.
//
// It reads no file and is the one inverse of Location the daemon keeps: located asks it whether
// the pair it was handed is a workspace Location names, and WorkspaceOf (gitworktree.go) asks it
// for the clone of the directory a process stands in. A caller's own refusals — the derivation's
// absolute path among them — stay with that caller.
func locationOf(dir string) (Workspace, error) {
	stateDir := dir
	for range layoutDepth {
		stateDir = filepath.Dir(stateDir)
	}
	owner, name := filepath.Base(filepath.Dir(filepath.Dir(dir))), filepath.Base(filepath.Dir(dir))
	repository, err := ghrepo.Parse("workspace repository", owner+"/"+name)
	if err != nil {
		return Workspace{}, err
	}
	want, err := Location(stateDir, repository, filepath.Base(dir))
	if err != nil {
		return Workspace{}, err
	}
	if want.Dir != dir {
		return Workspace{}, fmt.Errorf("the workspace of %s under %s lies at %s", repository, stateDir, want.Dir)
	}
	return want, nil
}

// located is whether workspace's directory and clone are the pair Location derives for the issue
// its directory names: <state>/workspaces/<owner>/<repo>/<issue> beside
// <state>/repos/github.com/<owner>/<repo>. The directory is a string like any other input, so
// locationOf is asked what Location makes of it, and it answers only for a directory that is
// already the one Location would name; the clone it answers is what is left to compare.
func located(workspace Workspace) bool {
	want, err := locationOf(workspace.Dir)
	return err == nil && want.Clone == workspace.Clone
}
