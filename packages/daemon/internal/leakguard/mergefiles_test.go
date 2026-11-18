package leakguard

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/fixturegit"
	"github.com/sjawhar/legion/daemon/internal/leakguard/leakguardtest"
)

// What a merge's own resolution writes is read. A merge's diff-tree against its first parent is
// empty unless the shape is asked for, so a line that stands in neither parent — the text a
// person wrote while resolving, which is text no other commit of the push carries — would reach
// the destination unread. The merge here is clean and its resolution adds a file of its own,
// which is the "evil merge" every rebase-free branch can hold.
func TestAMergesOwnResolutionIsRead(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	repo := filepath.Join(root, "repo")
	fixture := fixturegit.Isolated(t, home)
	fixture.Run(root, fixturegit.InitWorkTree(repo, "main")...)
	base := commitFiles(t, fixture, repo, map[string]string{"README.md": "one\n"}, "the commit the destination holds")
	fixture.Run(repo, "git", "checkout", "--quiet", "-b", "side")
	commitFiles(t, fixture, repo, map[string]string{"side.md": "a line of the side branch\n"}, "the side commit")
	fixture.Run(repo, "git", "checkout", "--quiet", "main")
	commitFiles(t, fixture, repo, map[string]string{"main.md": "a line of the main branch\n"}, "the main commit")
	// The merge itself is clean; what the resolution adds is this file, which stands in neither
	// parent and is the whole of what the merge commit publishes of its own.
	fixture.Run(repo, "git", "merge", "--quiet", "--no-ff", "--no-commit", "side")
	head := commitFiles(t, fixture, repo, map[string]string{"resolution.md": "the runner answers at " + theHost + "\n"}, "the merge")
	if parents := fixture.Run(repo, "git", "rev-list", "--parents", "-n", "1", head); len(strings.Fields(parents)) != 3 {
		t.Fatalf("the fixture's head is %q, want a commit of two parents", parents)
	}
	destination := destinationAt(t, fixture, repo, base+":refs/heads/main")
	guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public)
	err := guard.Push(bound(t), mainPush(repo, base, head, "origin", destination))
	wantRefused(t, err, []string{"resolution.md: " + theClass})
}

// A merge's text file above the bound refuses the push as a setup failure that names the file and
// its size, whether it is the merge's own or a parent's at a path the merge changes: a file passed
// over, or read in part, would pass its lines unread. Of a binary file above the bound the guard
// holds no more than its first 8000 bytes, and it adds no line; a text file at the bound is read. The
// bound here is 64 bytes.
func TestAMergesFileAboveTheBoundRefusesThePush(t *testing.T) {
	big := strings.Repeat("a line of ordinary prose\n", 4)
	line := "at " + theHost
	atBound := line + strings.Repeat(" ", 63-len(line)) + "\n"
	const failed = "LEAK CHECK SETUP ERROR: the push to origin: the push's commits could not be read: "
	for _, tc := range []struct {
		name        string
		side, added map[string]string
		// failure is the setup failure for the merge, "" where there is none.
		failure func(merge string) string
		// want is the report, and nil where the push passes or fails.
		want []string
	}{
		{
			name:  "the merge's own file",
			added: map[string]string{"big.md": big},
			failure: func(merge string) string {
				return failed + "merge " + short(merge) + "'s file big.md is 100 bytes, more than the 64 bytes a merge's file is read up to"
			},
		},
		{
			name:  "a parent's file at a path the merge changes",
			side:  map[string]string{"big.md": big},
			added: map[string]string{"big.md": "a line of the resolution\n"},
			failure: func(merge string) string {
				return failed + "a parent of merge " + short(merge) + "'s file big.md is 100 bytes, more than the 64 bytes a merge's file is read up to"
			},
		},
		{
			name:  "a binary file above the bound",
			added: map[string]string{"big.bin": "\x00" + big + "the runner answers at " + theHost + "\n"},
		},
		{
			name:  "a text file at the bound",
			added: map[string]string{"edge.md": atBound},
			want:  []string{"edge.md: " + theClass},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			side := tc.side
			if side == nil {
				side = map[string]string{}
			}
			repo, base, head, destination := pushOf(t, nil, side, tc.added, nil)
			guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public)
			guard.blobBound = 64
			err := guard.Push(bound(t), mainPush(repo, base, head, "origin", destination))
			if tc.failure != nil {
				setupFailure(t, err)
				if want := tc.failure(head); err.Error() != want {
					t.Errorf("the failure is\n  %s\nwant\n  %s", err, want)
				}
				return
			}
			wantRefused(t, err, tc.want)
		})
	}
}

// An octopus merge is read from its files, as every merge is: git's combined diff misreads a
// merge of 33 parents or more (mergeChanges' doc). The merges here are octopusRepo's, and each
// one's shared file holds every parent's line. One parent's line names a host: the destination
// holds every parent, so that line is text it already has, and the merge is the whole of what
// each push adds.
func TestAnOctopusMergeIsReadFromItsFiles(t *testing.T) {
	repo, base, every, destination, octopus := octopusRepo(t, "the runner of parent 17 answers at "+theHost)

	for _, tc := range []struct {
		name string
		head string
		// want is the report, and nil where the push passes.
		want []string
	}{
		{
			name: "the resolution adds a line to the file every parent changes",
			head: octopus(every+"the resolution answers at "+theHost+"\n", ""),
			want: []string{"shared.md: " + theClass},
		},
		{
			name: "the host is one parent's line and the merge adds none",
			head: octopus(every, ""),
		},
		{
			name: "the resolution adds a file",
			head: octopus(every, "the runner answers at "+theHost+"\n"),
			want: []string{"resolution.md: " + theClass},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public)
			err := guard.Push(bound(t), mainPush(repo, base, tc.head, "origin", destination))
			wantRefused(t, err, tc.want)
		})
	}
}
