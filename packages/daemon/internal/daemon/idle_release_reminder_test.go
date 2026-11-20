package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/workflow"
)

// idleRigOwesNothing is the one issue of each rig below: an issue in implementing whose
// implementer has taken its start task and reported, and which a case then gives a recorded
// completion or leaves owing its phase.
const idleRigOwesNothing = "owes-nothing"

// dispatch://INFRA-2746 R1, through the daemon a deployment runs (check
// c-idle-keeps-nothing-for-a-claim-that-owes-nothing, its daemon row): the release of a pod whose
// open phase's role has its completion recorded, while the issue stays in that phase, arms no
// backstop for that role, since that role owes nothing a wake could bring back. OSSV-732's
// reviewer is the shape: its completion stood while its issue stayed in reviewing, waiting on a
// code-owner review. The control, the same release with no completion recorded, arms the open
// phase's backstop as it always has.
func TestIdleOfAClaimThatOwesNothingArmsNoBackstopForItsRole(t *testing.T) {
	for _, c := range []struct {
		name      string
		completed bool
	}{
		{name: "its role's completion is recorded", completed: true},
		{name: "the control: its role owes its phase", completed: false},
	} {
		t.Run(c.name, func(t *testing.T) {
			rig := newIdleWakeRig(t, nil, idleRigOwesNothing)
			issue := rig.issue(idleRigOwesNothing)
			rig.awaitStartTask(issue)
			rig.awaitReported(rig.token(issue), "")
			if c.completed {
				rig.recordCompletion(issue)
			}

			rig.awaitReleased(issue)
			rig.awaitReleaseStepDone(issue)

			armed := rig.openBackstops(issue)
			if c.completed && armed != 0 {
				t.Errorf("the release of %s armed %d open backstops for its %s, want none: its completion stands, so it owes nothing a wake could bring back",
					issue.key, armed, issue.role)
			}
			if !c.completed && armed != 1 {
				t.Errorf("the release of %s armed %d open backstops for its %s, want 1: it owes its phase", issue.key, armed, issue.role)
			}
			rig.kube.requireServed(t)
		})
	}
}

// dispatch://INFRA-2746 R1's last sentence, through the daemon a deployment runs: a claim that
// owes nothing holds no reminder wake, so no reminder holds its pod. The idle release reads what
// the record holds of each claim's role through the reader the daemon hands the store when it
// attaches the release (attachIdleRelease, store.ReadIdleRecordsWith, record.FillIdleClaims), and
// the reminder hold reads record.PhaseOwed from it (workflow.IdleRelease). The workflow and store
// tests each bring their own half; this case fails if the daemon stops handing the store that
// reader.
//
// The claim's last report names a reminder wake a minute past due: the report of a timer that
// never got to report that it cleared it. With its role's completion recorded, the pod is
// released once quiet past the idle bound and no pass holds it for the reminder. The control,
// the same past-due wake with no completion recorded, is held as reminder_due pass after pass.
func TestIdleOfAClaimThatOwesNothingHoldsNoPodForAPastDueReminder(t *testing.T) {
	t.Run("its role's completion is recorded", func(t *testing.T) {
		rig := newIdleWakeRig(t, nil, idleRigOwesNothing)
		issue := rig.issue(idleRigOwesNothing)
		rig.awaitStartTask(issue)
		rig.awaitReported(rig.token(issue), "")
		rig.recordCompletion(issue)
		rig.reportPastDueReminder(issue)

		rig.awaitReleased(issue)

		records := rig.awaitReleaseRecords(issue)
		idleRigReleaseOf(t, records, issue)
		for _, hold := range records.Holds {
			if hold.Issue == issue.key && hold.Reason == workflow.IdleHoldReminder.String() {
				t.Errorf("the pod of %s was held for a reminder (%+v), want no such hold: its role's completion stands, so its claim owes nothing", issue.key, hold)
			}
		}
		rig.kube.requireServed(t)
	})
	t.Run("the control: its role owes its phase", func(t *testing.T) {
		rig := newIdleWakeRig(t, nil, idleRigOwesNothing)
		issue := rig.issue(idleRigOwesNothing)
		rig.awaitStartTask(issue)
		rig.awaitReported(rig.token(issue), "")
		rig.reportPastDueReminder(issue)

		ctx := context.Background()
		st, err := store.Open(ctx, rig.cfg.PostgresDSN)
		if err != nil {
			t.Fatalf("open the store to read the release records: %v", err)
		}
		defer st.Close()
		var quieted time.Time
		idleRigEventually(t, idleRigRecordWait, "the pod of "+issue.key+" held for a due reminder over passes of the idle tick", func() bool {
			if time.Since(quieted) > 2*time.Second {
				quieted = time.Now()
				rig.quiet()
			}
			read, err := st.PodReleaseRecords(ctx, rig.project, time.Time{})
			if err != nil {
				t.Fatalf("read the release records: %v", err)
			}
			for _, release := range read.Releases {
				if release.Issue == issue.key {
					t.Fatalf("a release of %s was recorded (%+v) while its claim owes its phase and its reminder is due", issue.key, release)
				}
			}
			for _, hold := range read.Holds {
				if hold.Issue == issue.key && hold.Reason == workflow.IdleHoldReminder.String() && hold.Passes >= idleRigHeldPasses {
					return true
				}
			}
			return false
		})
		if !rig.kube.pod(issue.pod) || rig.kube.sandbox(issue.pod) == nil {
			t.Fatalf("the pod of %s is gone while its claim owes its phase and its reminder is due", issue.key)
		}
		rig.kube.requireServed(t)
	})
}

// recordCompletion writes the completion of the issue's role on its phases row, the handoff
// record a completion names, as the workflow records it, and leaves the issue in its phase.
func (r *idleWakeRig) recordCompletion(issue idleRigIssue) {
	r.t.Helper()
	ctx := context.Background()
	if err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		rows, err := r.records.Phases(ctx, tx, issue.key)
		if err != nil {
			return err
		}
		row := record.PhaseRowOf(rows, issue.role)
		row.Issue, row.Role, row.Claim = issue.key, issue.role, r.token(issue)
		row.HandoffRecord, row.HandoffVersion = "tester-completion", 1
		return r.records.PutPhase(ctx, tx, row)
	}); err != nil {
		r.t.Fatalf("record the completion of %s's %s: %v", issue.key, issue.role, err)
	}
}

// reportPastDueReminder sets the reminder wake of the issue's claim's last report a minute in the
// past, which is what a session that armed a reminder and reported nothing since leaves the
// daemon holding.
func (r *idleWakeRig) reportPastDueReminder(issue idleRigIssue) {
	r.t.Helper()
	tag, err := r.pool.Exec(context.Background(),
		`update claims set activity_next_reminder_at = now() - interval '1 minute'
		 where token = $1 and activity_at is not null`, string(r.token(issue)))
	if err != nil {
		r.t.Fatalf("set the reminder wake of %s: %v", issue.key, err)
	}
	if tag.RowsAffected() != 1 {
		r.t.Fatalf("set the reminder wake of %s: %d claims, want 1", issue.key, tag.RowsAffected())
	}
}

// awaitReleaseStepDone waits until the outbox step that released the issue's pod has finished:
// it arms the open phase's backstop (armBackstops) after the pod is gone and before it forgets
// the release's mark (ClearIssueRelease), so once the mark is gone every backstop that release
// arms is in the table.
func (r *idleWakeRig) awaitReleaseStepDone(issue idleRigIssue) {
	r.t.Helper()
	idleRigEventually(r.t, idleRigRecordWait, "the release step of "+issue.key+" done", func() bool {
		var marks int
		if err := r.pool.QueryRow(context.Background(),
			`select count(*) from issue_releases where project = $1 and issue = $2`, r.project, issue.key).Scan(&marks); err != nil {
			r.t.Fatalf("read the release mark of %s: %v", issue.key, err)
		}
		return marks == 0
	})
}

// openBackstops is how many backstops of the issue's role stand armed and not fired.
func (r *idleWakeRig) openBackstops(issue idleRigIssue) int {
	r.t.Helper()
	var armed int
	if err := r.pool.QueryRow(context.Background(),
		`select count(*) from pod_wake_backstops where project = $1 and issue = $2 and role = $3 and not fired`,
		r.project, issue.key, string(issue.role)).Scan(&armed); err != nil {
		r.t.Fatalf("read the backstops of %s: %v", issue.key, err)
	}
	return armed
}
