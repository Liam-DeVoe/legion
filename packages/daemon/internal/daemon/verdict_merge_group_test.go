package daemon

import (
	"log/slog"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/store"
)

// groupConcluded fails the test unless GitHub holds exactly one legion-verdict run on group, made
// by the one App and concluding want, and the store records that run as the daemon's own write.
func groupConcluded(t *testing.T, rig *verdictRig, group, want string) {
	t.Helper()
	runs := rig.github.legionRuns(group)
	if len(runs) != 1 || runs[0].Conclusion != want || runs[0].App != verdictAppSlug {
		t.Fatalf("legion-verdict runs on %s = %+v, want one of the one App concluding %s", group, runs, want)
	}
	if recorded := verdictWrites(t, rig.pool, group); len(recorded) != 1 || recorded[0].GitHubID != runs[0].ID ||
		recorded[0].Kind != store.VerdictWriteCheckRun || string(recorded[0].Conclusion) != want {
		t.Fatalf("the store records %+v on %s, want the one check run %d concluding %s", recorded, group, runs[0].ID, want)
	}
}

// A write queued on a merge group's commit (queueMergeGroupWrite) decides the pull request at the
// head it stands at now, of whichever kind it is, and writes legion-verdict on the group's commit:
// success where the verdict stands at that head, and failure where none does, which a head's own
// write leaves unwritten. A repository no verdict entry names queues nothing.
func TestLegionVerdictAQueuedMergeGroupWriteDecidesThePullRequestAtItsHead(t *testing.T) {
	widgets := ghrepo.MustParse("acme/widgets")
	t.Run("a Legion head whose verdict stands", func(t *testing.T) {
		rig := newVerdictRig(t, roleTokens{}, []config.VerdictRepository{widgetsEntry}, reviewingWithPass)
		rig.approvedAndWritten(t)
		rig.w.queueMergeGroupWrite(widgets, 86, "group-86", false)
		rig.written(t)
		groupConcluded(t, rig, "group-86", "success")
	})
	t.Run("a Legion head that moved past its verdict", func(t *testing.T) {
		rig := newVerdictRig(t, roleTokens{}, []config.VerdictRepository{widgetsEntry}, reviewingWithPass)
		rig.approvedAndWritten(t)
		rig.apply(t, intake.PullRequestSynchronized{Repo: "acme/widgets", Number: 86, Branch: "legion/CAPTURE-1", Before: "code", HeadSHA: "code-2"})
		rig.written(t)
		rig.w.queueMergeGroupWrite(widgets, 86, "group-86", false)
		rig.written(t)
		groupConcluded(t, rig, "group-86", "failure")
	})
	t.Run("a Legion head with no verdict", func(t *testing.T) {
		rig := newVerdictRig(t, roleTokens{}, []config.VerdictRepository{widgetsEntry}, reviewingWithPass)
		rig.w.queueMergeGroupWrite(widgets, 86, "group-86", false)
		rig.written(t)
		groupConcluded(t, rig, "group-86", "failure")
		if runs := rig.github.legionRuns("code"); len(runs) != 0 {
			t.Fatalf("legion-verdict runs on the pull request's head code = %+v, want none: the group's write writes on the group alone", runs)
		}
	})
	t.Run("an outside head a person approved", func(t *testing.T) {
		rig := newVerdictRig(t, roleTokens{}, []config.VerdictRepository{widgetsEntry}, nothingSeeded)
		rig.apply(t, outsideOpened)
		rig.review(t, outsideReview(1, "a-writer", "approved"))
		rig.written(t)
		rig.w.queueMergeGroupWrite(widgets, 90, "group-90", false)
		rig.written(t)
		groupConcluded(t, rig, "group-90", "success")
	})
	t.Run("a repository no entry names", func(t *testing.T) {
		writer := newVerdictWriter([]config.VerdictRepository{widgetsEntry})
		w := &workflowRuntime{verdicts: writer, repositories: servedAcme(), log: slog.New(slog.DiscardHandler)}
		w.queueMergeGroupWrite(ghrepo.MustParse("acme/gadgets"), 86, "group-86", false)
		if !writer.idle() {
			t.Fatal("a merge group's write on acme/gadgets, which no entry names, is queued; want none")
		}
		w.queueMergeGroupWrite(widgets, 86, "group-86", false)
		if writer.idle() {
			t.Fatal("a merge group's write on acme/widgets, which an entry names, is not queued")
		}
	})
}
