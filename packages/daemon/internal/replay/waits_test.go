package replay

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/treelifecycle"
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

// lifecycleRefusedWindow is a window from 10:00 to 14:00 whose inputs reach back to the daemon's
// boot at 08:00, in a deployment that gates the spec, of a root whose architect's start its tree's
// lifecycle refuses for forty minutes. ACME-1 is handed to Legion and promoted before the reach:
// the daemon's write of in_progress at 07:31 is all the inputs show of it before its architect's
// turn from 08:05. ACME-2 is filed under it at 08:10, and at 08:20 the architect registers version
// 1 of the spec, not approved, and hands it to a person, on whom ACME-1 waits with its slot. At
// 10:20 the person approves the version, which answers the wait: the architect works to 10:35 and
// ACME-2 starts. At 11:00 the person closes ACME-1, whose tree lingers to 13:00.
//
// The start state opens no lifecycle of ACME-1's tree, whose slot no line of the inputs shows
// (seeder.lifecycles), so the architect's start the approval queues at 10:20 is refused by
// store.Store.AdmitClaim (treelifecycle.ErrLifecycleAbsent) and given back on its backoff, every
// try failing as the last, until ACME-1's close at 11:00 makes it the start of a lingering tree's
// root, which finishes.
func lifecycleRefusedWindow(t *testing.T) Window {
	b := newWindowBuilder(t, clock(10, 0), clock(14, 0))
	b.gated(record.DesignStepSpec)
	b.reaches(2 * time.Hour)
	b.boot(clock(8, 0))
	b.file(clock(7, 30), "ACME-1", filing{Title: "Export the widget list", Status: "todo", Rank: "a0", Labels: handedToLegion})
	b.daemonWrites(clock(7, 31), "ACME-1", "in_progress")
	b.turnStarts(clock(8, 5), "ACME-1", claim.RoleArchitect)
	b.file(clock(8, 10), "ACME-2", filing{Title: "Write the export's header row", Parent: "ACME-1", Tree: "ACME-1", Status: "todo", Rank: "a1"})
	b.specWritten(clock(8, 17), "ACME-1")
	b.asksApproval(clock(8, 18), "ACME-1", approvalAsk)
	b.turnEnds(clock(8, 20), "ACME-1", claim.RoleArchitect)
	b.registered(clock(8, 20), "ACME-1", false)
	since := b.seq("ACME-1")
	b.info(clock(8, 20), "workflow: a registration hands a design document to the person", "issue", "ACME-1",
		"step", string(record.DesignStepSpec), "version", 1, "since_seq", since)
	b.info(clock(8, 20), "workflow: root waits on a person and keeps its admission slot", "issue", "ACME-1", "tree", "ACME-1",
		"reason", "spec version 1 is awaiting a person's review", "since_seq", since)
	b.approves(clock(10, 20), "ACME-1", approvalAsk)
	b.info(clock(10, 20), "workflow: design gate opened", "issue", "ACME-1", "artifact", b.artifact("ACME-1"), "event", "approved", "version", 1)
	b.info(clock(10, 20), "workflow: a person's review or a newer spec version answered a root's wait", "issue", "ACME-1",
		"version", 1, "event", "approved", "ask", "", "waited_version", 1)
	b.turnStarts(clock(10, 20), "ACME-1", claim.RoleArchitect)
	b.childStarts(clock(10, 20), "ACME-2")
	b.turnEnds(clock(10, 35), "ACME-1", claim.RoleArchitect)
	b.edit(clock(11, 0), "ACME-1", setStatus(record.DoneStatus))
	b.leaves(clock(11, 0), "ACME-1", record.DoneStatus)
	b.lingerCloses(clock(13, 0), "ACME-1", 2)
	return b.build()
}

// The stub skips each try of a row given back to wait on its backoff that no change of what the
// try reads can end (skipWaits, scopeTriggers), and the skip changes no output of a replay. The
// test replays each window twice at once, with the skip and without it (RunConfig.noSkip), and
// compares the two replays' log, both scorecards and run.json but for what it measures of the run
// itself (hostFields), byte for byte: every slot move and phase move of the log, and
// Divergence.Retried, which counts each try skipped as a try. The windows are every fixture window,
// replayed under its last part's rule, manyPairsWindow, whose news-only starts wait behind their
// tasks, waitingRowsWindow, where ACME-1's news-only start waits four hours and forty minutes,
// half an hour of it under a pause that holds every start, and ACME-2's merger's start waits two
// hours and a quarter for the gate, and lifecycleRefusedWindow, where ACME-1's architect's start
// waits forty minutes for a lifecycle its tree does not have, until ACME-1's close. Without the
// skip the stub runs every try of the first two of waitingRowsWindow, over three hundred, and of
// lifecycleRefusedWindow's, one a minute at the backoff's cap; with it, those after each change in
// their trees or of the project and the few before each next item, which the windows' hourly ticks
// and waitingRowsWindow's pause bound to some dozens and a few. The replay without the skip of a
// window built for one kind of row given back gives back a row whose try failed as that kind fails
// (givenBack), read from the outbox at each instant its timeline settles. It logs each window's
// tries counted and run with and without the skip.
func TestTheSkipOfAWaitingRowChangesNoOutput(t *testing.T) {
	dsn := replayDSN(t)
	entries, err := os.ReadDir(filepath.Join("testdata", "windows"))
	if err != nil {
		t.Fatal(err)
	}
	windows := map[string]Window{"many-pairs": manyPairsWindow(t), "waiting-rows": waitingRowsWindow(t),
		"lifecycle-refused": lifecycleRefusedWindow(t)}
	// givenBack is, for a window built for one kind of row given back, what that kind's try writes
	// into the row's last_error (record.Postgres.RetryOutbox).
	givenBack := map[string]string{"lifecycle-refused": treelifecycle.ErrLifecycleAbsent.Error()}
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
			var gaveBack [2]bool
			var runs sync.WaitGroup
			for i := range outs {
				outs[i] = t.TempDir()
				runs.Go(func() {
					results[i], errs[i] = Run(context.Background(), RunConfig{Window: window, Rule: rule, Branch: MainBranch, Seed: 1,
						Slowdown: 1, PGDSN: dsn, Out: outs[i], noSkip: i == 1,
						atSettled: func(ctx context.Context, st *store.Store, _ time.Time) error {
							if givenBack[name] == "" || gaveBack[i] {
								return nil
							}
							return st.Pool().QueryRow(ctx, `select exists (select 1 from outbox where attempts > 0 and strpos(last_error, $1) > 0)`,
								givenBack[name]).Scan(&gaveBack[i])
						}})
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
			if want := givenBack[name]; want != "" && !gaveBack[1] {
				t.Errorf("the replay of %s without the skip gave back no row whose try failed with %q", name, want)
			}
			switch name {
			case "waiting-rows":
				if without.triesRun < 300 {
					t.Errorf("the replay without the skip ran %d tries of rows given back, want at least 300", without.triesRun)
				}
				if withSkip.triesRun > 60 {
					t.Errorf("the replay with the skip ran %d tries of the %d it counted, want at most 60", withSkip.triesRun, withSkip.Divergence.Retried)
				}
			case "lifecycle-refused":
				if without.triesRun < 30 {
					t.Errorf("the replay without the skip ran %d tries of rows given back, want at least 30", without.triesRun)
				}
				if withSkip.triesRun > 10 {
					t.Errorf("the replay with the skip ran %d tries of the %d it counted, want at most 10", withSkip.triesRun, withSkip.Divergence.Retried)
				}
			}
		})
	}
}
