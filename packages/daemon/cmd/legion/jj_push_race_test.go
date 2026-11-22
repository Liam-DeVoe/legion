package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// rolePushWritesDuringPush writes rolePushRaceFile into the working copy as the real push starts,
// once: a stand-in for another role's write into the issue's shared working copy that lands after
// the step's snapshot and before the real push.
const rolePushWritesDuringPush = `if [ -n "$is_push" ] && [ -z "$is_dry" ] && [ ! -e "$0.raced" ]; then : >"$0.raced"; printf 'raced\n' >"$PWD/` + rolePushRaceFile + `"; fi`

// rolePushRaceFile is the file rolePushWritesDuringPush writes.
const rolePushRaceFile = "raced.txt"

// A write that another process makes into the working copy after the step's snapshot and before
// the real push waits for the next command: over P, the other role's commit with the implementer's
// trailer, E on P under be and Y on E under by, with @ brought back to E and a waiting write, `-r
// mine()` sends be and by as the step's snapshot left them, each with the waiting write and
// without the raced one, P with the implementer's author, and leaves the raced file on disk and no
// change divergent. The push takes one snapshot, the step's.
func TestAWriteDuringThePushWaitsForTheNextCommand(t *testing.T) {
	t.Parallel()
	impl, rev := rolePushImplementer, rolePushReviewer
	rolePushEachForm(t, func(t *testing.T, form rolePushForm) {
		f := newRolePushFixture(t, form, rolePushWritesDuringPush)
		p, e, y, q := f.descendant()
		written := func(c rolePushChange) rolePushCommit {
			return rolePushCommit{change: c.change, author: impl, file: "waiting.txt", content: rolePushWaitingContent, lacks: rolePushRaceFile}
		}
		under := rolePushCommit{change: p.change, author: impl}
		want := rolePushWant{
			refs: []rolePushRef{
				{ref: "refs/heads/be", commits: []rolePushCommit{written(e), under}},
				{ref: "refs/heads/by", commits: []rolePushCommit{written(y), written(e), under}},
				{ref: "refs/heads/bq"},
			},
			rewrites:  []rolePushRewrite{{role: "implementer", author: impl, change: p.change, old: p.commit}},
			same:      map[string]rolePushIdentity{q.commit: rev},
			ids:       []rolePushIDs{{argument: "mine()", commits: 2, words: 1, bound: rolePushRevisionBound}},
			snapshots: rolePushTakesOneSnapshot,
		}
		f.pushAndCheck([]string{"git", "push", "-r", "mine()"}, want)
		f.onlyRefs(want)
		if _, err := os.Stat(filepath.Join(f.repo, rolePushRaceFile)); err != nil {
			t.Errorf("the raced write is not on disk after the push: %v", err)
		}
	})
}

// A new file over snapshot.max-new-file-size makes jj print its refusal to snapshot it once, as
// jj alone prints it: with a commit to rewrite under legion/K-1 and @ on it with a waiting write,
// the push through the form's jj prints "Refused to snapshot some files" once.
func TestALargeNewFilesWarningPrintsOnce(t *testing.T) {
	t.Parallel()
	impl, rev := rolePushImplementer, rolePushReviewer
	const refused = "Refused to snapshot some files"
	rolePushEachForm(t, func(t *testing.T, form rolePushForm) {
		state := func(f *rolePushFixture) {
			c := f.commitOn(rev, impl, "main", rolePushMessage("work", "implementer"))
			f.bookmark("legion/K-1", c.commit)
			f.jj(impl, "edit", c.commit)
			f.writeFile(filepath.Join(f.repo, "waiting.txt"), rolePushWaitingContent)
			f.writeFile(filepath.Join(f.repo, "large.bin"), strings.Repeat("x", 2<<20))
		}
		twin := newRolePushFixture(t, form, "")
		state(twin)
		_, _, aloneErr := twin.alone(impl, rolePushBookmark...)
		if n := strings.Count(aloneErr, refused); n != 1 {
			t.Fatalf("jj alone printed %q %d times, want once:\n%s", refused, n, aloneErr)
		}
		f := newRolePushFixture(t, form, rolePushRecords)
		state(f)
		code, _, stderr := f.push(impl, rolePushBookmark...)
		if code != 0 {
			t.Fatalf("the push exited %d:\n%s", code, stderr)
		}
		if n := strings.Count(stderr, refused); n != 1 {
			t.Errorf("the push printed %q %d times, want once, as jj alone prints it:\n%s", refused, n, stderr)
		}
	})
}
