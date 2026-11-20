package workspace

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/fixtureenv"
	"github.com/sjawhar/legion/daemon/internal/fixturegit"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
)

// reviewFixture is a shared clone standing on an issue's branch at origin, made as the issue's
// workspace provision makes one, and the review clone ProvisionReviewClone is asked to make
// beside it: origin, the commit a commit makes there on the branch, and the request.
type reviewFixture struct {
	setup   fixturegit.Runner
	git, jj string
	origin  string
	request ReviewRequest
	first   string
}

func newReviewFixture(t *testing.T) reviewFixture {
	t.Helper()
	f := reviewFixture{git: fixturegit.CompiledGit(t), jj: realTool(t, "jj")}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	for _, entry := range fixtureenv.Home(home) {
		key, value, _ := strings.Cut(entry, "=")
		t.Setenv(key, value)
	}
	t.Setenv("JJ_USER", "Legion test")
	t.Setenv("JJ_EMAIL", "legion-test@example.invalid")
	f.setup = fixturegit.Isolated(t, home).With("PATH=" + filepath.Dir(f.git) + ":/usr/bin:/bin")
	repo := ghrepo.MustParse("acme/widgets")
	const issue = "ACME-4"
	f.origin = filepath.Join(root, "origin")
	initOrigin := fixturegit.InitWorkTree(f.origin, "main")
	initOrigin[0] = f.git
	f.setup.Run(root, initOrigin...)
	f.commit(t, "base\n")
	f.setup.Run(f.origin, f.git, "checkout", "--quiet", "-b", Bookmark(issue))
	f.first = f.commit(t, "base\nfirst\n")
	state := filepath.Join(root, "state")
	located, err := Location(state, repo, issue)
	if err != nil {
		t.Fatal(err)
	}
	f.setup.Run(root, f.git, "clone", "--quiet", "--branch", Bookmark(issue), f.origin, located.Clone)
	review, err := ReviewLocation(state, repo, issue)
	if err != nil {
		t.Fatal(err)
	}
	f.request = ReviewRequest{Clone: located.Clone, Dir: review, Repo: repo, Issue: issue, DefaultBranch: "main"}
	return f
}

// commit commits body as notes.txt on origin's current branch and answers the commit.
func (f reviewFixture) commit(t *testing.T, body string) string {
	t.Helper()
	writeFiles(t, f.origin, map[string]string{"notes.txt": body})
	f.setup.Run(f.origin, f.git, "add", "--all")
	f.setup.Run(f.origin, f.git, "commit", "--quiet", "--message", body)
	return f.setup.Run(f.origin, f.git, "rev-parse", "HEAD")
}

// The review clone holds objects of its own: no file under its object store is a hard link to a
// file of the shared clone's, so no role that writes the shared clone's object files in place
// writes what the reviewer's git reads.
func TestAReviewCloneSharesNoObjectFileWithTheSharedClone(t *testing.T) {
	f := newReviewFixture(t)
	run := NewRunner(testTimeout, map[string]string{"git": f.git, "jj": f.jj})
	if err := ProvisionReviewClone(context.Background(), run, f.request); err != nil {
		t.Fatalf("ProvisionReviewClone: %v", err)
	}
	files := 0
	err := filepath.WalkDir(filepath.Join(f.request.Dir, ".git", "objects"), func(path string, entry fs.DirEntry, err error) error {
		if err != nil || !entry.Type().IsRegular() {
			return err
		}
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		files++
		if links := info.Sys().(*syscall.Stat_t).Nlink; links != 1 {
			t.Errorf("the review clone's object file %s has %d links, want 1: it is shared with another clone", path, links)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files == 0 {
		t.Fatalf("the review clone at %s holds no object file, so this row read nothing", f.request.Dir)
	}
}

// skipJJNew is a runner that answers every `jj new` as done without running it, as a refresh that
// failed to move the working copy and said nothing would: the clone stays where it was.
type skipJJNew struct{ Runner }

func (r skipJJNew) Run(ctx context.Context, command Command) (Result, error) {
	if len(command.Argv) > 1 && filepath.Base(command.Argv[0]) == "jj" && slices.Contains(command.Argv, "new") {
		return Result{}, nil
	}
	return r.Runner.Run(ctx, command)
}

// A review clone whose checked-out HEAD is not the head the provision brought it to is refused,
// naming both commits, rather than handed to a reviewer that would read another commit.
func TestAReviewCloneLeftAtAnotherCommitIsRefused(t *testing.T) {
	f := newReviewFixture(t)
	run := NewRunner(testTimeout, map[string]string{"git": f.git, "jj": f.jj})
	if err := ProvisionReviewClone(context.Background(), run, f.request); err != nil {
		t.Fatalf("the first provision: %v", err)
	}
	second := f.commit(t, "base\nfirst\nsecond\n")
	f.setup.Run(f.request.Clone, f.git, "fetch", "--quiet", "origin")
	err := ProvisionReviewClone(context.Background(), skipJJNew{run}, f.request)
	if err == nil || !strings.Contains(err.Error(), f.first) || !strings.Contains(err.Error(), second) {
		t.Fatalf("a provision that left the clone at %s rather than %s = %v, want a refusal naming both", f.first, second, err)
	}
}

// A review clone that is already there is brought to the head the shared clone records at every
// provision, from the shared clone's objects and with no network, as a fresh clone would stand: a
// reviewer started on it reviews the head of now, not the one its clone was first made at. What
// the reviewer wrote in it before stays in a change of its own, and the working copy holds the new
// head's tree; a lock a reviewer's git killed mid-write left in the clone does not stop it, and
// a temporary clone a killed attempt left beside it is gone with the next one. Once origin no
// longer holds the issue's branch and the shared clone has pruned it, the clone stands on the
// default branch instead, never on the branch's stale head; and with neither branch at origin,
// the provision is refused, naming both.
func TestAReviewCloneIsBroughtToTheCurrentHeadAtEachProvision(t *testing.T) {
	git := fixturegit.CompiledGit(t)
	jj := realTool(t, "jj")
	root := t.TempDir()
	home := filepath.Join(root, "home")
	for _, entry := range fixtureenv.Home(home) {
		key, value, _ := strings.Cut(entry, "=")
		t.Setenv(key, value)
	}
	t.Setenv("JJ_USER", "Legion test")
	t.Setenv("JJ_EMAIL", "legion-test@example.invalid")
	setup := fixturegit.Isolated(t, home).With("PATH=" + filepath.Dir(git) + ":/usr/bin:/bin")
	repo := ghrepo.MustParse("acme/widgets")
	const issue = "ACME-2"
	state := filepath.Join(root, "state")
	origin := filepath.Join(root, "origin")
	initOrigin := fixturegit.InitWorkTree(origin, "main")
	initOrigin[0] = git
	setup.Run(root, initOrigin...)
	commit := func(body, message string) string {
		t.Helper()
		writeFiles(t, origin, map[string]string{"notes.txt": body})
		setup.Run(origin, git, "add", "--all")
		setup.Run(origin, git, "commit", "--quiet", "--message", message)
		return setup.Run(origin, git, "rev-parse", "HEAD")
	}
	base := commit("base\n", "base")
	setup.Run(origin, git, "checkout", "--quiet", "-b", Bookmark(issue))
	first := commit("base\nfirst\n", "first head")
	located, err := Location(state, repo, issue)
	if err != nil {
		t.Fatal(err)
	}
	setup.Run(root, git, "clone", "--quiet", "--branch", Bookmark(issue), origin, located.Clone)
	review, err := ReviewLocation(state, repo, issue)
	if err != nil {
		t.Fatal(err)
	}
	abandoned := review + ".review-killed"
	writeFiles(t, abandoned, map[string]string{".git/HEAD": "ref: refs/heads/main\n"})
	run := NewRunner(testTimeout, map[string]string{"git": git, "jj": jj})
	request := ReviewRequest{Clone: located.Clone, Dir: review, Repo: repo, Issue: issue, DefaultBranch: "main"}
	if err := ProvisionReviewClone(context.Background(), run, request); err != nil {
		t.Fatalf("the first provision: %v", err)
	}
	if _, err := os.Lstat(abandoned); !os.IsNotExist(err) {
		t.Fatalf("the temporary clone a killed attempt left at %s is still there: %v", abandoned, err)
	}
	if at := setup.Run(review, git, "rev-parse", "HEAD"); at != first {
		t.Fatalf("the review clone stands on %s, want the first head %s", at, first)
	}

	// The branch moves at origin, the shared clone fetches it, the reviewer has written a file of
	// its own in its clone since, and its git was killed holding the lock of the branch's ref.
	second := commit("base\nfirst\nsecond\n", "second head")
	setup.Run(located.Clone, git, "fetch", "--quiet", "origin")
	writeFiles(t, review, map[string]string{
		"reviewer-scratch.txt": "notes\n",
		".git/refs/remotes/origin/" + Bookmark(issue) + ".lock": "",
	})
	if err := ProvisionReviewClone(context.Background(), run, request); err != nil {
		t.Fatalf("the second provision: %v", err)
	}
	if at := setup.Run(review, git, "rev-parse", "HEAD"); at != second {
		t.Fatalf("after the shared clone moved, the review clone stands on %s, want the second head %s", at, second)
	}
	if body, err := os.ReadFile(filepath.Join(review, "notes.txt")); err != nil || string(body) != "base\nfirst\nsecond\n" {
		t.Fatalf("the review clone's working copy holds notes.txt = %q (%v), want the second head's", body, err)
	}
	if empty := setup.Run(review, jj, "log", "-R", review, "--ignore-working-copy", "-r", "@", "--no-graph", "-T", "empty"); empty != "true" {
		t.Fatalf("the review clone's working copy is not an empty change on the head (empty=%s)", empty)
	}
	if kept := setup.Run(review, jj, "log", "-R", review, "--ignore-working-copy", "-r", `files("reviewer-scratch.txt") & ~@`, "--no-graph", "-T", `"kept"`); kept != "kept" {
		t.Fatalf("the reviewer's own file is in no change of its own after the move: %q", kept)
	}

	// The issue's branch goes from origin and from the shared clone's record of it.
	setup.Run(origin, git, "checkout", "--quiet", "main")
	setup.Run(origin, git, "branch", "--quiet", "--delete", "--force", Bookmark(issue))
	setup.Run(located.Clone, git, "fetch", "--quiet", "--prune", "origin")
	if err := ProvisionReviewClone(context.Background(), run, request); err != nil {
		t.Fatalf("the provision after the branch went: %v", err)
	}
	if at := setup.Run(review, git, "rev-parse", "HEAD"); at != base {
		t.Fatalf("after the issue's branch went, the review clone stands on %s, want the default branch's %s", at, base)
	}
	request.DefaultBranch = "release/v7"
	if err := ProvisionReviewClone(context.Background(), run, request); err == nil || !strings.Contains(err.Error(), Bookmark(issue)) || !strings.Contains(err.Error(), "release/v7") {
		t.Fatalf("a provision with neither branch at origin = %v, want a refusal naming both", err)
	}
}

// IssueWorkspace answers the issue's own workspace for a reviewer's clone, and any other directory
// as it is: the launcher's sweep and restore and the shim's bundle work the issue's workspace
// whichever role's LEGION_WORKSPACE they read.
func TestIssueWorkspaceIsTheWorkspaceOfAReviewClonesIssue(t *testing.T) {
	repo := ghrepo.MustParse("acme/widgets")
	located, err := Location("/legion", repo, "ACME-3")
	if err != nil {
		t.Fatal(err)
	}
	review, err := ReviewLocation("/legion", repo, "ACME-3")
	if err != nil {
		t.Fatal(err)
	}
	for dir, want := range map[string]string{
		review:                                located.Dir,
		located.Dir:                           located.Dir,
		"":                                    "",
		"/legion/reviews/acme/widgets":        "/legion/reviews/acme/widgets",
		"/elsewhere/reviews/acme/w/acme-3":    "/elsewhere/workspaces/acme/w/acme-3",
		"/legion/reviews/acme/widgets/ACME-3": "/legion/reviews/acme/widgets/ACME-3",
	} {
		if got := IssueWorkspace(dir); got != want {
			t.Errorf("IssueWorkspace(%q) = %q, want %q", dir, got, want)
		}
	}
}
