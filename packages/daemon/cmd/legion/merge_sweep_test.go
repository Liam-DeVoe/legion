package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/jjmerge"
)

// The rows of `legion merge`'s leftover removal (B16 of dispatch://INFRA-4149's behaviour table
// among them) and of its lock, which lets one run at a time hold a workspace. merge_test.go's top
// comment says how each row builds its history and runs the command.

// B16: what a killed run in this workspace left where git conflicts, and a carrier of another
// workspace's. This workspace's are the carriers k2 and k3 with their bookmarks, and X, jj's merge
// of the two, which carries the killed run's Legion-Merge-Run line from the moment jj made it.
// Each carrier has the carriers' own identity, as a run writes it. A run of B1 removes this
// workspace's and leaves the other's.
func TestLegionMergeRemovesOnlyThisWorkspacesLeftovers(t *testing.T) {
	r := newMergeRepo(t)
	r.minimalCase(2)
	r.jj("workspace", "add", filepath.Join(r.dir, "other"), "--name", "other")
	// A carrier holds the tree of the commit that of names. k2 and k3 hold two trees, so git
	// writes two commits for them, not one.
	carrier := func(workspace, role, of string) (id, bookmark string) {
		id = r.writeCarrier(r.git("rev-parse", r.id(of)+"^{tree}"), "a carrier a killed run in "+workspace+" left")
		bookmark = "legion-merge-" + workspace + "-0123456789abcdef-" + role
		r.git("update-ref", "refs/heads/"+bookmark, id)
		return id, bookmark
	}
	k2, k2Bookmark := carrier("default", "k2", "b1")
	k3, k3Bookmark := carrier("default", "k3", "b2")
	theirs, theirBookmark := carrier("other", "g", "b1")
	r.jj("git", "import")
	r.jj("new", "--no-edit", k2, k3, "-m", "jj's merge of git's versions a killed run left", "-m", "Legion-Merge-Run: legion-merge-default-0123456789abcdef-")
	x := r.id(fmt.Sprintf("children(%s) & children(%s)", k2, k3))
	code, stdout, stderr := r.merge("-R", r.ws, "-m", mergeMessage, mergeFirst, mergeSecond)
	if code != 0 {
		t.Fatalf("legion merge = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	r.wantMergeHolds(mergeFirst, "main", "f", "b\n")
	for _, id := range []string{k2, k3, x} {
		if got := r.template(id, "hidden"); got != "true" {
			t.Errorf("this workspace's leftover %s is still visible", id)
		}
	}
	if got := r.template(theirs, `hidden ++ " " ++ bookmarks`); got != "false "+theirBookmark {
		t.Errorf("the other workspace's carrier %s reads %q, want visible with its bookmark %s", theirs, got, theirBookmark)
	}
	bookmarks := r.read("bookmark", "list", "--all", "-T", `name ++ "\n"`)
	refs := r.git("for-each-ref", "--format=%(refname)")
	for _, mine := range []string{k2Bookmark, k3Bookmark} {
		if strings.Contains(bookmarks, mine) {
			t.Errorf("this workspace's leftover bookmark %s is still there:\n%s", mine, bookmarks)
		}
		if strings.Contains(refs, mine) {
			t.Errorf("git's refs are\n%s\nwant %s gone", refs, mine)
		}
	}
	if !strings.Contains(refs, theirBookmark) {
		t.Errorf("git's refs are\n%s\nwant %s kept", refs, theirBookmark)
	}
}

// While another run holds the workspace's lock, the command refuses with exit 3 and makes
// nothing: the leftover sweep at the start of a run would otherwise take the commits of the run
// that is still going.
func TestLegionMergeRefusesWhileAnotherRunHoldsTheWorkspace(t *testing.T) {
	r := newMergeRepo(t)
	r.minimalCase(2)
	r.setEnv()
	before := r.snapshot()
	held, err := os.OpenFile(filepath.Join(r.ws, ".jj", "legion-merge.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Close() })
	if err := syscall.Flock(int(held.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("hold the merge lock: %v", err)
	}
	code, stdout, stderr := r.merge("-R", r.ws, "-m", mergeMessage, mergeFirst, mergeSecond)
	want := "legion merge: another legion merge is running in the workspace " + r.ws + ". Wait for it to end, then run the merge again\n"
	if code != 3 || !strings.Contains(stderr, want) || !strings.HasSuffix(stderr, mergeRefusedNextStep) || stdout != "" {
		t.Errorf("legion merge with the lock held = %d, want 3 naming %q and ending with the next step\nstdout:\n%s\nstderr:\n%s", code, want, stdout, stderr)
	}
	r.wantNothingMade(before)
}

// killRunner is jjmerge.Exec until the first command step picks out; that command and every one
// after it fail without running, the undo's included, as for a run something killed (SIGKILL,
// the memory guard, a pane's SIGHUP) at that step.
type killRunner struct {
	step   func(args []string) bool
	killed bool
}

func (k *killRunner) Run(ctx context.Context, c jjmerge.Command) ([]byte, error) {
	if k.killed || k.step(c.Args) {
		k.killed = true
		return nil, &jjmerge.ExitError{Args: c.Args, Code: -1, Stderr: "killed"}
	}
	return jjmerge.Exec{}.Run(ctx, c)
}

// A run killed after its jj new made the merge commit leaves that merge holding jj's own merge
// of the parents, c where git's merge holds b, and, once its describe ran, the Legion-Merge
// trailer that says the command made it. The next run in the workspace removes what the killed
// run left, as it removes the carriers: the visible commits are then those before the killed
// run, the new merge and the working copy on it.
func TestLegionMergeRerunRemovesTheMergeAKilledRunLeft(t *testing.T) {
	for _, tc := range []struct {
		name string
		step func(args []string) bool
	}{
		{"killed at the describe", func(args []string) bool { return slices.Contains(args, "describe") }},
		{"killed at the restore", func(args []string) bool { return slices.Contains(args, "restore") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newMergeRepo(t)
			r.minimalCase(2)
			r.setEnv()
			before := r.snapshot()
			killer := &killRunner{step: tc.step}
			var stdout, stderr strings.Builder
			runMerge(context.Background(), killer, r.ws, mergeMessage, []string{mergeFirst, mergeSecond}, &stdout, &stderr)
			if !killer.killed {
				t.Fatalf("the run never reached the step it is killed at\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
			}
			code, out, errOut := r.merge("-R", r.ws, "-m", mergeMessage, mergeFirst, mergeSecond)
			if code != 0 {
				t.Fatalf("legion merge after the killed run = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
			}
			r.wantMergeHolds(mergeFirst, "main", "f", "b\n")
			want := slices.Sorted(slices.Values(append(slices.Clone(before.visible), r.id("@-"), r.id("@"))))
			if got := r.visible(); !slices.Equal(got, want) {
				var merges []string
				for _, id := range strings.Fields(r.read("log", "--no-graph", "-r", fmt.Sprintf("children(%s) & children(%s)", r.id(mergeFirst), r.id("main")), "-T", `commit_id ++ "\n"`)) {
					merges = append(merges, fmt.Sprintf("%s holds %q at f, description %q", id, r.show(id, "f"), r.template(id, "description")))
				}
				t.Errorf("the visible commits are\n%q\nwant those before the killed run, the merge and the working copy\n%q\nthe merges of the two parents:\n%s", got, want, strings.Join(merges, "\n"))
			}
		})
	}
}

// Another merge of the same two parents that appears while the run makes its own, as a second
// `legion merge` in the same workspace or a role's `jj new` of the two would make it. The run
// either makes its merge, holding git's merge, or exits 3 having made nothing: then no merge
// commit of its own stays, and the only new visible commit is the other merge. A merge of its
// own left by an exit 3 holds jj's merge of the parents, c where git's holds b, and the help's
// "The command made nothing" is then false.
func TestLegionMergeMakesNothingWhenAnotherMergeOfTheParentsAppears(t *testing.T) {
	r := newMergeRepo(t)
	r.minimalCase(2)
	r.setEnv()
	first, second := r.id(mergeFirst), r.id("main")
	before := r.snapshot()
	runner := &stepRunner{
		step: func(args []string, _ [][]string) bool {
			return slices.Contains(args, "new") && slices.Contains(args, "--message="+mergeMessage)
		},
		fail: func(ctx context.Context, c jjmerge.Command) ([]byte, error) {
			other := jjmerge.Command{Dir: c.Dir, Args: []string{"jj", "--ignore-working-copy", "new", "--no-edit", first, second, "-m", "another run's merge"}}
			if _, err := (jjmerge.Exec{}).Run(ctx, other); err != nil {
				return nil, err
			}
			return jjmerge.Exec{}.Run(ctx, c)
		},
	}
	var stdout, stderr strings.Builder
	code := runMerge(context.Background(), runner, r.ws, mergeMessage, []string{mergeFirst, mergeSecond}, &stdout, &stderr)
	if !runner.hit {
		t.Fatalf("the merge never reached its jj new; it ran %q", runner.ran)
	}
	other := r.id(fmt.Sprintf(`children(%s) & children(%s) & description(substring:"another run's merge")`, first, second))
	switch code {
	case 0:
		r.wantMergeHolds(mergeFirst, "main", "f", "b\n")
	case 3:
		want := slices.Sorted(slices.Values(append(slices.Clone(before.visible), other)))
		if got := r.visible(); !slices.Equal(got, want) {
			var merges []string
			for _, id := range strings.Fields(r.read("log", "--no-graph", "-r", fmt.Sprintf("children(%s) & children(%s)", first, second), "-T", `commit_id ++ "\n"`)) {
				merges = append(merges, fmt.Sprintf("%s holds %q at f, description %q", id, r.show(id, "f"), r.template(id, "description")))
			}
			t.Errorf("legion merge exited 3, and the visible commits are\n%q\nwant those before the command and the other merge\n%q\nthe merges of the two parents:\n%s\nstderr:\n%s", got, want, strings.Join(merges, "\n"), stderr.String())
		}
	default:
		t.Errorf("legion merge = %d, want 0 or 3\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
}

// A carrier a killed run left is a visible head, and a role can build on it: a commit of its own
// work on the carrier, git's merged tree, which is not the working copy. The next run keeps the
// carrier and names it on stderr, since the role's commit stands on it (jjmerge's keep): an
// abandon of the carrier would move the role's work onto other parents. The run then makes the
// merge, exit 0, and the role's commit stays visible with its file.
func TestLegionMergeKeepsARolesWorkOnALeftoverCarrier(t *testing.T) {
	r := newMergeRepo(t)
	r.minimalCase(2)
	carrier := r.writeCarrier(r.gitTree(mergeFirst, "main"), "a carrier a killed run in default left")
	r.git("update-ref", "refs/heads/legion-merge-default-0123456789abcdef-g", carrier)
	r.jj("git", "import")
	r.jj("new", carrier, "-m", "the role's work on git's tree")
	if err := os.WriteFile(filepath.Join(r.ws, "work"), []byte("the role's work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.jj("status")
	// By its commit id, which still names the commit once it is hidden.
	work := r.id("@")
	r.workOn("base")
	code, stdout, stderr := r.merge("-R", r.ws, "-m", mergeMessage, mergeFirst, mergeSecond)
	if code != 0 {
		t.Errorf("legion merge = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if got := r.template(work, "hidden"); got != "false" {
		t.Errorf("legion merge = %d, and the role's commit %s on the leftover carrier is hidden: its work was abandoned\nstdout:\n%s\nstderr:\n%s", code, work, stdout, stderr)
	} else if got := r.show(work, "work"); got != "the role's work\n" {
		t.Errorf("the role's commit %s holds %q at work, want its own file", work, got)
	}
	if want := "legion merge: kept commit " + carrier + ", which a legion merge made, because commit " + work + " stands on it.\n"; !strings.Contains(stderr, want) {
		t.Errorf("legion merge = %d, and stderr lacks %q:\n%s", code, want, stderr)
	}
}

// A merge a stopped run left, which still carries the run's Legion-Merge-Run line, that a
// bookmark names or that a tracked remote bookmark holds: a role put its bookmark on the merge,
// or pushed it, and a tracked remote bookmark leaves it mutable. An abandon of the merge deletes
// the bookmark, or hides a commit the remote holds, with exit 0 and nothing on stderr. The next
// run of B1 keeps the merge visible with its bookmarks, and names it on stderr.
func TestLegionMergeKeepsALeftoverMergeABookmarkNames(t *testing.T) {
	for _, tc := range []struct {
		name string
		// hold puts what holds the stopped merge on it, and answers why stderr says it stays.
		hold func(r *mergeRepo, stopped string) string
	}{
		{"a bookmark names it", func(r *mergeRepo, stopped string) string {
			r.jj("bookmark", "create", "legion/ACME-2", "-r", stopped)
			return "the bookmark legion/ACME-2 names it"
		}},
		{"a tracked remote bookmark holds it", func(r *mergeRepo, stopped string) string {
			r.jj("bookmark", "create", "legion/ACME-2", "-r", stopped)
			r.jj("git", "push", "--bookmark", "legion/ACME-2")
			r.jj("bookmark", "delete", "legion/ACME-2")
			if got := r.template(stopped, `self.contained_in("bookmarks()") ++ " " ++ self.contained_in("mutable()")`); got != "false true" {
				r.t.Fatalf("the pushed merge reads %q, want no local bookmark on it and mutable", got)
			}
			return "a remote bookmark is on it or on a commit above it"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newMergeRepo(t)
			r.minimalCase(2)
			first, second := r.id(mergeFirst), r.id("main")
			r.jj("new", "--no-edit", first, second, "-m", "a merge a stopped run left", "-m", "Legion-Merge-Run: legion-merge-default-0123456789abcdef-")
			stopped := r.id(fmt.Sprintf(`children(%s) & children(%s) & description(substring:"a merge a stopped run left")`, first, second))
			why := tc.hold(r, stopped)
			bookmarks := r.template(stopped, "bookmarks")
			code, stdout, stderr := r.merge("-R", r.ws, "-m", mergeMessage, mergeFirst, mergeSecond)
			if code != 0 {
				t.Fatalf("legion merge = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
			}
			r.wantMergeHolds(mergeFirst, "main", "f", "b\n")
			if got := r.template(stopped, `hidden ++ " " ++ bookmarks`); got != "false "+bookmarks {
				t.Errorf("the stopped run's merge %s reads %q, want it visible with %q\nstderr:\n%s", stopped, got, bookmarks, stderr)
			}
			if want := "legion merge: kept commit " + stopped + ", which a legion merge made, because " + why + ".\n"; !strings.Contains(stderr, want) {
				t.Errorf("stderr lacks %q:\n%s", want, stderr)
			}
		})
	}
}

// A role's commit on root() that a carrier's bookmark names has no parent in the repository, as a
// carrier has none, and only its author tells it from a carrier: the sweep takes for a carrier
// only a commit of the carriers' own author, legion-merge@localhost. The run leaves the role's
// commit visible with its bookmark, and names the bookmark on stderr. A role's commit on a
// carrier that a run wrote is TestLegionMergeKeepsARolesWorkOnALeftoverCarrier's.
func TestLegionMergeLeavesARolesCommitOnRootThatACarriersBookmarkNames(t *testing.T) {
	const bookmark = "legion-merge-default-0123456789abcdef-g"
	r := newMergeRepo(t)
	r.minimalCase(2)
	r.commit("work", []string{"root()"}, map[string]string{"work": "the role's work\n"})
	work := r.id("work")
	r.jj("bookmark", "delete", "work")
	r.jj("bookmark", "create", bookmark, "-r", work)
	r.workOn("base")
	code, stdout, stderr := r.merge("-R", r.ws, "-m", mergeMessage, mergeFirst, mergeSecond)
	if code != 0 {
		t.Fatalf("legion merge = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if got := r.template(work, `hidden ++ " " ++ bookmarks`); got != "false "+bookmark {
		t.Errorf("the role's commit %s reads %q, want it visible with its bookmark %s\nstderr:\n%s", work, got, bookmark, stderr)
	}
	if want := "legion merge: left the bookmark " + bookmark + " where it is, on commit " + work + ", which is not a commit a legion merge wrote.\n"; !strings.Contains(stderr, want) {
		t.Errorf("stderr lacks %q:\n%s", want, stderr)
	}
}

// A carrier a killed run left that a tag makes immutable: a person or a tool ran `git tag` on it,
// and jj's default immutable_heads() holds tags(). jj abandons no immutable commit, so the next
// run of B1 keeps the carrier visible with its bookmark, and names the bookmark on stderr. The
// carrier is the carriers' own author's and has no parent in the repository, so the reason says
// that a legion merge wrote it and that it is immutable, not that a legion merge did not write it.
func TestLegionMergeSaysAnImmutableLeftoverCarrierIsOneItWrote(t *testing.T) {
	const bookmark = "legion-merge-default-0123456789abcdef-g"
	r := newMergeRepo(t)
	r.minimalCase(2)
	carrier := r.writeCarrier(r.gitTree(mergeFirst, "main"), "a carrier a killed run in default left")
	r.git("update-ref", "refs/heads/"+bookmark, carrier)
	r.git("tag", "carrier-tag", carrier)
	r.jj("git", "import")
	if got := r.template(carrier, `self.contained_in("mutable()")`); got != "false" {
		t.Fatalf("the tagged carrier %s reads mutable() %q, want it immutable", carrier, got)
	}
	code, stdout, stderr := r.merge("-R", r.ws, "-m", mergeMessage, mergeFirst, mergeSecond)
	if code != 0 {
		t.Fatalf("legion merge = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	r.wantMergeHolds(mergeFirst, "main", "f", "b\n")
	if got := r.template(carrier, `hidden ++ " " ++ bookmarks`); got != "false "+bookmark {
		t.Errorf("the tagged carrier %s reads %q, want it visible with its bookmark %s\nstderr:\n%s", carrier, got, bookmark, stderr)
	}
	if want := "legion merge: left the bookmark " + bookmark + " where it is, on commit " + carrier + ", which a legion merge wrote but which is immutable, so the run does not change it.\n"; !strings.Contains(stderr, want) {
		t.Errorf("stderr lacks %q:\n%s", want, stderr)
	}
	if untrue := "on commit " + carrier + ", which is not a commit a legion merge wrote."; strings.Contains(stderr, untrue) {
		t.Errorf("stderr says %q of the carrier, which a legion merge wrote:\n%s", untrue, stderr)
	}
}

// A bookmark of a carrier's shape under this workspace's prefix, on a commit of the repository
// rather than on a carrier: a role's commit on base, or the first parent the run itself names. A
// carrier has no parent in the repository, so nothing a carrier's removal does reaches the
// repository's own commits (dispatch://INFRA-4149's plan, step 4). The run abandons neither
// commit: it refuses, or it leaves the commit visible. At 03a652db it refused such a bookmark and
// made nothing; a sweep that abandons whatever such a bookmark names loses the role's work, and
// takes the first parent and its bookmark before the run resolves it.
func TestLegionMergeAbandonsNoRepositoryCommitACarriersBookmarkNames(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target func(r *mergeRepo) string
	}{
		{"a role's commit on base", func(r *mergeRepo) string {
			r.commit("work", []string{"base"}, map[string]string{"work": "the role's work\n"})
			id := r.id("work")
			r.jj("bookmark", "delete", "work")
			return id
		}},
		{"the first parent", func(r *mergeRepo) string { return r.id(mergeFirst) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newMergeRepo(t)
			r.minimalCase(2)
			target := tc.target(r)
			r.jj("bookmark", "create", "legion-merge-default-0123456789abcdef-g", "-r", target)
			r.workOn("base")
			code, stdout, stderr := r.merge("-R", r.ws, "-m", mergeMessage, mergeFirst, mergeSecond)
			if got := r.template(target, "hidden"); got != "false" {
				t.Errorf("legion merge = %d, and the repository's commit %s, which the bookmark legion-merge-default-0123456789abcdef-g names, is hidden: the run abandoned it\nstdout:\n%s\nstderr:\n%s", code, target, stdout, stderr)
			}
		})
	}
}
