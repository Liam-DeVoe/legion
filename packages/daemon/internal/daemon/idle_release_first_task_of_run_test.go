package daemon

import (
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// dispatch://INFRA-2746 R1 (spec v70) at the release of a pod, at a pause's stop and at a release
// a pause cancels (check c-idle-keeps-nothing-for-a-claim-that-owes-nothing, its sixth control):
// a released claim of the open phase's role that has confirmed no task of the issue's run, and
// holds the first task of that run and phase unconfirmed, its send failed before any turn, still
// gets a backstop. Nothing else sends that task again while its pod is gone, so the backstop is
// the one wake that delivers it. Two shapes reach it: a fresh claim, which serves run 0, and a
// live claim kept across a new run N+1, which still serves run N. The same claim whose phases row
// records its completion of the row's entry owes nothing a wake could bring back, and gets none.
//
// LEGION-208's implementer serves the run named served, the issue stands at run generation in
// implementing, its row stands on entry rowEntry, and the claim holds the task of that run, that
// phase and that entry. Each case runs through the three writers that arm a backstop: the
// release's last step (outbox.armBackstops), the stop transaction of a pause
// (projectPause.stopped) and the cancel of a release while the project is paused
// (outbox.cancelRelease, store.ReleaseCancelPause.GetsBackstop).
func TestIdleOfAClaimThatOwesNothingStillGetsABackstopForTheUndeliveredFirstTaskOfItsRun(t *testing.T) {
	for _, writer := range []struct {
		name string
		// arm runs the writer, and answers the pause whose stop it recorded, 0 for the release,
		// which records none.
		arm func(r *pauseRig, issue record.Issue, implementer supervise.Claim) int64
	}{
		{name: "the release", arm: func(r *pauseRig, issue record.Issue, implementer supervise.Claim) int64 {
			r.putClaim(implementer)
			runner := &outbox{pool: r.pool, records: r.pauses.records, trees: r.st, project: "legion", backstop: time.Hour,
				now: time.Now, log: quietLogger()}
			if err := runner.armBackstops(r.ctx, issue); err != nil {
				r.t.Fatalf("arm the backstops of %s's release: %v", pauseIssue, err)
			}
			return 0
		}},
		{name: "the stop transaction", arm: func(r *pauseRig, _ record.Issue, implementer supervise.Claim) int64 {
			held := r.pauseLegion()
			r.mustStop(implementer, supervise.PauseStop{Pause: held.ID})
			return held.ID
		}},
		{name: "a cancel of its release while paused", arm: func(r *pauseRig, issue record.Issue, implementer supervise.Claim) int64 {
			r.markReleased(implementer)
			if _, err := r.pool.Exec(r.ctx, `update issue_releases set generation = $2 where issue = $1`, pauseIssue,
				int64(issue.Generation)); err != nil {
				r.t.Fatalf("mark the release of %s at run %d: %v", pauseIssue, issue.Generation, err)
			}
			held := r.pauseLegion()
			r.cancelRelease()
			return held.ID
		}},
	} {
		for _, c := range []struct {
			name string
			// generation is the run LEGION-208 stands at, and served the run the claim serves,
			// which it has confirmed no task of.
			generation, served uint64
			rowEntry           int64
			completed          bool
			backstop           bool
			why                string
		}{
			{name: "the sixth control: a fresh claim serving run 0 holds the first task of run 1",
				generation: 1, served: 0, rowEntry: 1, backstop: true,
				why: "nothing sends its first task again, so the backstop is the one wake that delivers it"},
			{name: "the sixth control: a claim kept across run 2 still serving run 1 holds the first task of run 2",
				generation: 2, served: 1, rowEntry: 2, backstop: true,
				why: "nothing sends its first task again, so the backstop is the one wake that delivers it"},
			{name: "a fresh claim serving run 0 holds the first task of run 1, and its row records its completion",
				generation: 1, served: 0, rowEntry: 1, completed: true,
				why: "its completion of its row's entry stands, so it owes nothing a wake could bring back"},
			{name: "a claim kept across run 2 still serving run 1 holds the first task of run 2, and its row records its completion",
				generation: 2, served: 1, rowEntry: 2, completed: true,
				why: "its completion of its row's entry stands, so it owes nothing a wake could bring back"},
		} {
			t.Run(writer.name+"/"+c.name, func(t *testing.T) {
				r := newPauseRig(t, claim.RoleImplementer)
				if _, err := r.pool.Exec(r.ctx, `update issues set generation = $2 where key = $1`, pauseIssue,
					int64(c.generation)); err != nil {
					t.Fatalf("move %s to run %d: %v", pauseIssue, c.generation, err)
				}
				issue := record.Issue{Key: pauseIssue, Tree: pauseIssue, Phase: phase.Implementing, Generation: c.generation}
				implementer := r.claim(claim.RoleImplementer, supervise.StateSuspended)
				implementer.Generation, implementer.ServingGeneration = c.generation, c.served
				implementer.ServedEntry = 0
				if c.served != 0 {
					implementer.ServedEntry = c.rowEntry - 1
				}
				task := undeliveredTask(phase.Implementing, c.rowEntry)
				task.Generation = c.generation
				implementer.GivenEntry, implementer.Pending = c.rowEntry, task
				r.putPhaseRow(claim.RoleImplementer, c.rowEntry, c.completed)

				// A pause writer records the stop in every case, owing no launch, so a case that arms
				// none is a stop that read the backstop's rule and not one that never ran.
				if held := writer.arm(r, issue, implementer); held != 0 {
					if got := r.stops(held, claim.RoleImplementer); len(got) != 1 || got[0] != (pauseStopRecord{end: store.PauseStopStopped}) {
						t.Fatalf("the implementer's stops = %+v, want one stopped and owed no launch", got)
					}
				}

				armed := r.count(`select count(*) from pod_wake_backstops where issue = $1 and role = 'implementer' and not fired`, pauseIssue)
				want := 0
				if c.backstop {
					want = 1
				}
				if armed != want {
					t.Errorf("%s of %s's implementer armed %d open backstops, want %d: %s", writer.name, pauseIssue, armed, want, c.why)
				}
			})
		}
	}
}
