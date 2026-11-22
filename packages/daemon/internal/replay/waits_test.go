package replay

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/workflow"
)

// waitingRowsWindow is a window from 09:00 to 15:00 recorded under main, in a deployment that gates
// the spec, at the builder's cap of 4 and its hourly ticks, of two roots whose starts wait for
// hours on their backoff. ACME-1 is promoted at 09:00, its spec written at 09:05, approved at 09:10
// and its design registered at 09:15; it plans to 09:30 and implements to 14:30, and while it
// implements its architect writes a second version at 09:40, which a person approves at 09:50, so
// the news-only start of its implementer waits behind the implementer's task until 14:30. ACME-2 is
// promoted at 09:01 and works the same first version 15 minutes in each phase to retro at 10:31;
// its architect writes a second version at 10:40, which closes the gate, ACME-2 moves to merging at
// 10:46, and its merger's start waits for the gate until a person approves the version at 13:00.
// The merger works to 13:15, when ACME-2 merges.
func waitingRowsWindow(t *testing.T) Window {
	b := newWindowBuilder(t, clock(9, 0), clock(15, 0))
	b.gated(record.DesignStepSpec)
	b.boot(clock(9, 0))
	b.file(clock(9, 0), "ACME-1", filing{Title: "Export the widget list", Status: "todo", Rank: "a0", Labels: handedToLegion})
	b.rootPromoted(clock(9, 0), "ACME-1")
	b.file(clock(9, 1), "ACME-2", filing{Title: "Count the widgets per shelf", Status: "todo", Rank: "a1", Labels: handedToLegion})
	b.rootPromoted(clock(9, 1), "ACME-2")
	b.specWritten(clock(9, 5), "ACME-1")
	b.specWritten(clock(9, 6), "ACME-2")
	b.approves(clock(9, 10), "ACME-1", "")
	b.approves(clock(9, 11), "ACME-2", "")
	b.rootDesigned(clock(9, 15), "ACME-1")
	b.rootDesigned(clock(9, 16), "ACME-2")
	b.segmentEnds(clock(9, 30), "ACME-1", phase.Planning, phase.Implementing, workflow.TriggerPlannerCompleted)
	b.segmentEnds(clock(9, 31), "ACME-2", phase.Planning, phase.Implementing, workflow.TriggerPlannerCompleted)
	b.specRevised(clock(9, 40), "ACME-1", 2)
	b.info(clock(9, 40), "workflow: design gate closed", "issue", "ACME-1", "artifact", b.artifact("ACME-1"), "event", "artifact.version",
		"version", 2)
	b.segmentEnds(clock(9, 46), "ACME-2", phase.Implementing, phase.Testing, workflow.TriggerImplementationReady)
	b.approvesVersion(clock(9, 50), "ACME-1", 2)
	b.info(clock(9, 50), "workflow: design gate opened", "issue", "ACME-1", "artifact", b.artifact("ACME-1"), "event", "artifact.approved",
		"version", 2)
	b.segmentEnds(clock(10, 1), "ACME-2", phase.Testing, phase.Reviewing, workflow.TriggerTesterPassed)
	b.segmentEnds(clock(10, 16), "ACME-2", phase.Reviewing, phase.Accepting, workflow.TriggerReviewApproved)
	b.segmentEnds(clock(10, 31), "ACME-2", phase.Accepting, phase.Retro, workflow.TriggerSignOff)
	b.specRevised(clock(10, 40), "ACME-2", 2)
	b.info(clock(10, 40), "workflow: design gate closed", "issue", "ACME-2", "artifact", b.artifact("ACME-2"), "event", "artifact.version",
		"version", 2)
	b.turnEnds(clock(10, 46), "ACME-2", claim.RoleImplementer)
	b.moved(clock(10, 46), "ACME-2", phase.Retro, phase.Merging, workflow.TriggerRetroCompleted)
	b.approvesVersion(clock(13, 0), "ACME-2", 2)
	b.info(clock(13, 0), "workflow: design gate opened", "issue", "ACME-2", "artifact", b.artifact("ACME-2"), "event", "artifact.approved",
		"version", 2)
	b.turnStarts(clock(13, 0), "ACME-2", claim.RoleMerger)
	b.segmentEnds(clock(13, 15), "ACME-2", phase.Merging, phase.Done, workflow.TriggerPullRequestMerged)
	b.leaves(clock(13, 15), "ACME-2", record.DoneStatus)
	b.info(clock(11, 30), "pause: the project is paused", "pause", 1, "by", "operator", "by_session", "",
		"reason", "the operator moves the database to a larger disk", "stops", 0, "claims", []string{}, "told_controller", true)
	b.info(clock(12, 0), "pause: the project is resumed", "pause", 1, "by", "operator", "by_session", "",
		"paused_by", "operator", "paused_by_session", "", "held", 0, "told_controller", true)
	b.segmentEnds(clock(14, 30), "ACME-1", phase.Implementing, phase.Testing, workflow.TriggerImplementationReady)
	return b.build()
}

// The stub skips each try of a row given back to wait on its backoff that no change of what the
// try reads can end (skipWaits, scopeTriggers), and the skip changes no output of a replay. The
// test replays each window twice at once, with the skip and without it (RunConfig.noSkip), and
// compares the two replays' log, both scorecards and run.json but for what it measures of the run
// itself (hostFields), byte for byte: every slot move and phase move of the log, and
// Divergence.Retried, which counts each try skipped as a try. The windows are every fixture window,
// replayed under its last part's rule, manyPairsWindow, whose news-only starts wait behind their
// tasks, and waitingRowsWindow, where ACME-1's news-only start waits four hours and forty minutes,
// half an hour of it under a pause that holds every start, and ACME-2's merger's start waits two
// hours and a quarter for the gate. Without the skip the stub runs every try of those two, over
// three hundred; with it, those after each change in their trees or of the project and the few
// before each next item, which the window's hourly ticks and its pause bound to some dozens. It logs
// each window's tries counted and run with and without the skip.
func TestTheSkipOfAWaitingRowChangesNoOutput(t *testing.T) {
	dsn := replayDSN(t)
	entries, err := os.ReadDir(filepath.Join("testdata", "windows"))
	if err != nil {
		t.Fatal(err)
	}
	windows := map[string]Window{"many-pairs": manyPairsWindow(t), "waiting-rows": waitingRowsWindow(t)}
	for _, entry := range entries {
		if entry.IsDir() {
			windows[entry.Name()] = fixtureWindow(t, entry.Name())
		}
	}
	for _, name := range slices.Sorted(maps.Keys(windows)) {
		t.Run(name, func(t *testing.T) {
			window := windows[name]
			rule := replayedRule(t, window.Parts[len(window.Parts)-1].Rule)
			var outs [2]string
			var results [2]RunResult
			var errs [2]error
			var runs sync.WaitGroup
			for i := range outs {
				outs[i] = t.TempDir()
				runs.Go(func() {
					results[i], errs[i] = Run(context.Background(), RunConfig{Window: window, Rule: rule, Branch: MainBranch, Seed: 1,
						Slowdown: 1, PGDSN: dsn, Out: outs[i], noSkip: i == 1})
				})
			}
			runs.Wait()
			for i, err := range errs {
				if err != nil {
					t.Fatalf("replay %d of %s: %v", i+1, name, err)
				}
			}
			for _, file := range []string{logFile, scorecardFile, lineScorecardFile, runFile} {
				skipped, every := runOutput(t, outs[0], file), runOutput(t, outs[1], file)
				if line, a, b, differ := firstDifference(skipped, every); differ {
					t.Errorf("the replays of %s with and without the skip differ in %s first at line %d:\n  with:    %s\n  without: %s",
						name, file, line, a, b)
				}
			}
			withSkip, without := results[0], results[1]
			t.Logf("%s: %d tries counted; %d run without the skip, %d with it", name, without.Divergence.Retried, without.triesRun, withSkip.triesRun)
			if without.triesRun != without.Divergence.Retried {
				t.Errorf("the replay without the skip ran %d tries and counted %d, want them equal", without.triesRun, without.Divergence.Retried)
			}
			if name != "waiting-rows" {
				return
			}
			if without.triesRun < 300 {
				t.Errorf("the replay without the skip ran %d tries of rows given back, want at least 300", without.triesRun)
			}
			if withSkip.triesRun > 60 {
				t.Errorf("the replay with the skip ran %d tries of the %d it counted, want at most 60", withSkip.triesRun, withSkip.Divergence.Retried)
			}
		})
	}
}
