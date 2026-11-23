// check_states_pinned_test.go holds the settler's read of which commits are a pull request's own to
// the two revisions Dispatch stores for it: a candidate is a commit of the pull request where the
// stored head holds it and the stored base revision does not, which GitHub's comparison of those two
// commits answers whatever the pull request stands at when it is read. So however GitHub moves the
// pull request between two reads, away and back again, no answer decides a candidate otherwise than
// the commits between those two revisions say. The rig is check_states_test.go's.
package api

import (
	"fmt"
	"sync"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// The commits of the tests below: the base revision and the head Dispatch stores for #70, another
// base revision and another head GitHub moves #70 to for a while, and the commit a tester recorded
// a result at.
const (
	pinnedBase      = "ba5e000000000000000000000000000000000000"
	pinnedOtherBase = "ba5e111111111111111111111111111111111111"
	pinnedHead      = "4ead000000000000000000000000000000000000"
	pinnedOtherHead = "4ead111111111111111111111111111111111111"
	pinnedCandidate = "ca2d000000000000000000000000000000000000"
)

// TestAMembershipAnswerIsAboutTheStoredRevisionsHoweverGitHubMovesBetweenReads: the tester recorded
// k1 passing at a commit, and Dispatch stores #70 at a head onto a base revision, so the commit is
// #70's candidate. For a while GitHub's #70 stands elsewhere: onto another base revision, or at
// another head, whose commits name the candidate where the stored revisions do not, or leave it out
// where they name it. GitHub stands there for some of the reads a settle makes of #70, of its
// commits and of a comparison, and at the stored revisions for the others: the first read alone,
// the first three, the second and third, every read from the second on.
//
// Whatever the schedule, the settle decides the candidate only as the commits between the stored
// base revision and the stored head say, or leaves it undecided and keeps the obligation that
// asked for the read; once GitHub stands at the stored revisions for good, the next settles decide
// it as those commits say. The schedules are the red team's: a whole list read at another base
// revision that leaves out a commit of the stored one, or names one the stored one holds; a capped
// list read at another head that names the candidate; an empty list read where the base revision
// has moved past the head; a whole list read at a later head that names the stored head and a
// commit after it.
func TestAMembershipAnswerIsAboutTheStoredRevisionsHoweverGitHubMovesBetweenReads(t *testing.T) {
	capped := make([]string, 260)
	for index := range capped {
		capped[index] = fmt.Sprintf("%040x", 0xe0000+index)
	}
	capped[3], capped[259] = pinnedCandidate, pinnedOtherHead
	for _, story := range []struct {
		name string
		// member is whether the stored head holds the candidate and the stored base revision does
		// not: the answer every settle must agree with.
		member bool
		// home is GitHub's comparison of the stored base revision and head; awayBase, awayHead and
		// away are where GitHub's #70 stands for a while and the commits it lists there.
		home               []string
		awayBase, awayHead string
		away               []string
	}{
		{name: "a whole list at another base revision leaves out a commit of the stored one", member: true,
			home: []string{pinnedCandidate, pinnedHead}, awayBase: pinnedOtherBase, awayHead: pinnedHead, away: []string{pinnedHead}},
		{name: "a whole list at another base revision names a commit the stored one holds",
			home: []string{pinnedHead}, awayBase: pinnedOtherBase, awayHead: pinnedHead, away: []string{pinnedCandidate, pinnedHead}},
		{name: "a capped list at another head names the candidate",
			home: []string{pinnedHead}, awayBase: pinnedBase, awayHead: pinnedOtherHead, away: capped},
		{name: "an empty list where the base revision has moved past the head", member: true,
			home: []string{pinnedCandidate, pinnedHead}, awayBase: pinnedOtherBase, awayHead: pinnedHead, away: []string{}},
		{name: "a whole list at a later head names the stored head and a commit after it",
			home: []string{pinnedHead}, awayBase: pinnedBase, awayHead: pinnedOtherHead, away: []string{pinnedHead, pinnedCandidate, pinnedOtherHead}},
	} {
		for _, schedule := range []struct {
			name string
			away func(read int) bool
		}{
			{"away for the first read and back after it", func(read int) bool { return read == 1 }},
			{"away for the first three reads and back after them", func(read int) bool { return read <= 3 }},
			{"back for the first read, away for the next two, back after them", func(read int) bool { return read == 2 || read == 3 }},
			{"back for the first read and away from the second on", func(read int) bool { return read >= 2 }},
		} {
			t.Run(story.name+"/"+schedule.name, func(t *testing.T) {
				rig := newCheckStatesRig(t, checkStatesTwoChecks)
				eachHeadResult(t, rig.handler, rig.issue, "k1", "tester", "pass", pinnedCandidate)
				storeBaseRevision(t, rig, 70, pinnedHead, pinnedBase)
				if got := candidatesOf(t, rig); len(got) != 1 || got[pinnedCandidate] {
					t.Fatalf("#70 holds the former heads %v, want the candidate unverified: this test holds nothing", got)
				}
				rig.github.setRange(pinnedBase, pinnedHead, story.home...)
				rig.github.setRange(story.awayBase, story.awayHead, story.away...)
				rig.github.setPull(70, pinnedHead, "main", pinnedBase)
				var mu sync.Mutex
				reads := 0
				rig.github.onRead(func(string) {
					mu.Lock()
					reads++
					away := schedule.away(reads)
					mu.Unlock()
					if away {
						rig.github.setPull(70, story.awayHead, "main", story.awayBase)
					} else {
						rig.github.setPull(70, pinnedHead, "main", pinnedBase)
					}
				})
				settleOwed(t, rig)
				rig.github.onRead(nil)
				verified, held := candidatesOf(t, rig)[pinnedCandidate]
				switch {
				case story.member && !held:
					t.Fatalf("after GitHub's #70 stood elsewhere for some reads, the settle dropped the candidate, which the commits between the stored base revision and head name")
				case !story.member && held && verified:
					t.Fatalf("after GitHub's #70 stood elsewhere for some reads, the settle accepted the candidate, which the commits between the stored base revision and head do not name")
				case held && !verified && len(fenceMarksOf(t, rig, rig.issue)) == 0:
					t.Fatalf("the settle left the candidate undecided and kept no mark a test handoff waits for, want the obligation that asked for the read kept")
				}

				rig.github.setPull(70, pinnedHead, "main", pinnedBase)
				settleEveryMark(t, rig)
				verified, held = candidatesOf(t, rig)[pinnedCandidate]
				state := stateAt(t, rig, rig.issue, pinnedCandidate, "k1")
				if story.member && (!verified || state != model.PassStandsYes) {
					t.Fatalf("once GitHub stands at the stored revisions, the candidate is held=%v verified=%v and k1 is published %q there, want it a former head with k1 yes", held, verified, state)
				}
				if !story.member && (held || state != "") {
					t.Fatalf("once GitHub stands at the stored revisions, the candidate is held=%v verified=%v and k1 is published %q there, want it gone with nothing published", held, verified, state)
				}
			})
		}
	}
}
