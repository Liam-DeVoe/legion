package leakguard

import (
	"os"
	"path/filepath"
	"slices"
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
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	commit := func(message string) string {
		t.Helper()
		fixture.Run(repo, "git", "add", "-A")
		fixture.Run(repo, "git", "commit", "--quiet", "-m", message)
		return fixture.Run(repo, "git", "rev-parse", "HEAD")
	}
	write("README.md", "one\n")
	base := commit("the commit the destination holds")
	fixture.Run(repo, "git", "checkout", "--quiet", "-b", "side")
	write("side.md", "a line of the side branch\n")
	commit("the side commit")
	fixture.Run(repo, "git", "checkout", "--quiet", "main")
	write("main.md", "a line of the main branch\n")
	commit("the main commit")
	// The merge itself is clean; what the resolution adds is this file, which stands in neither
	// parent and is the whole of what the merge commit publishes of its own.
	fixture.Run(repo, "git", "merge", "--quiet", "--no-ff", "--no-commit", "side")
	write("resolution.md", "the runner answers at "+theHost+"\n")
	head := commit("the merge")
	if parents := fixture.Run(repo, "git", "rev-list", "--parents", "-n", "1", head); len(strings.Fields(parents)) != 3 {
		t.Fatalf("the fixture's head is %q, want a commit of two parents", parents)
	}
	destinationAt(t, fixture, repo, theURL, base+":refs/heads/main")
	guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public)
	err := guard.Push(bound(t), knivesPush(repo, base, head, "origin", theURL))
	want := []string{"resolution.md: " + theClass}
	if refused := refusal(t, err); !slices.Equal(refused.Lines, want) {
		t.Errorf("the report is %q, want %q", refused.Lines, want)
	}
}

// A merge's text file above blobBound refuses the push as a setup failure that names the file and
// its size, whether it is the merge's own or a parent's at a path the merge changes: a file passed
// over, or read in part, would pass its lines unread. Of a binary file above the bound the guard
// holds no more than its first 8000 bytes, and it adds no line; a text file at the bound is read. The
// bound here is 64 bytes.
func TestAMergesFileAboveTheBoundRefusesThePush(t *testing.T) {
	kept := blobBound
	blobBound = 64
	t.Cleanup(func() { blobBound = kept })
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
			repo, base, head := pushOf(t, nil, side, tc.added, nil)
			guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public)
			err := guard.Push(bound(t), knivesPush(repo, base, head, "origin", theURL))
			switch {
			case tc.failure != nil:
				setupFailure(t, err)
				if want := tc.failure(head); err.Error() != want {
					t.Errorf("the failure is\n  %s\nwant\n  %s", err, want)
				}
			case tc.want != nil:
				if refused := refusal(t, err); !slices.Equal(refused.Lines, tc.want) {
					t.Errorf("the report is %q, want %q", refused.Lines, tc.want)
				}
			case err != nil:
				t.Fatalf("a merge whose one named line is in a binary file: %v", err)
			}
		})
	}
}

// An octopus merge is read from its files, as every merge is: git's combined diff misreads a
// merge of 33 parents or more (mergeFiles' doc). The merges here are octopusRepo's, and each
// one's shared file holds every parent's line. One parent's line names a host: the destination
// holds every parent, so that line is text it already has, and the merge is the whole of what
// each push adds.
func TestAnOctopusMergeIsReadFromItsFiles(t *testing.T) {
	repo, base, every, octopus := octopusRepo(t, "the runner of parent 17 answers at "+theHost)

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
			err := guard.Push(bound(t), knivesPush(repo, base, tc.head, "origin", theURL))
			if tc.want == nil {
				if err != nil {
					t.Fatalf("a merge that adds nothing of its own: %v", err)
				}
				return
			}
			if refused := refusal(t, err); !slices.Equal(refused.Lines, tc.want) {
				t.Errorf("the report is %q, want %q", refused.Lines, tc.want)
			}
		})
	}
}
