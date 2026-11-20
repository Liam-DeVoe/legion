package store

import (
	"context"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/pods"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// The reviewer's read-ahead of dispatch://INFRA-3589, finding 2. A root architect's entry start
// and a production check's look name no phase and wait for no slot, as a wake's own start does
// (resumeStartRow). Only the wake's start carries the wake's kind (record.SuperviseRequest.WakeKind,
// wakeStartRow). The reads of the wakes taken while a release stands read a wake's start by that
// kind: the teardown's pass-over, the cancel's delete and the two retells. So the entry start and
// the look are work the pod was given, and no wake's.

// entryTask and lookTask are the tasks of the two starts that are no wake's: the root architect's
// entry start and a production check's look.
const entryTask, lookTask = "the architect's entry task", "the check's look task"

// wakesBesideWork is a release of the root's pod whose two stops have recorded the implementer and
// the architect as `released`, and a wake of each taken since: a CI settlement's of the
// implementer and a role message's of the architect, each with the start the wake queued
// (queueWakeStart). With entry, the root architect's entry start is queued beside them, and with
// look, a production check's look of the implementer. It answers the row of the release's
// teardown.
func wakesBesideWork(t *testing.T, st *Store, entry, look bool) int64 {
	t.Helper()
	ctx := context.Background()
	openRunningTree(t, st)
	rootClaim(t, st, claim.RoleImplementer, supervise.StateIdle, "")
	rootClaim(t, st, claim.RoleArchitect, supervise.StateIdle, "")
	boundary, stop, last := releaseTwoRolesThroughTheStore(t, st)
	stoppedByTheRelease(t, st, claim.RoleImplementer, boundary)
	stoppedByTheRelease(t, st, claim.RoleArchitect, stop)
	at := time.Now().UTC()
	for _, wake := range []struct {
		role claim.Role
		kind string
	}{{claim.RoleArchitect, pods.WakeRoleMessage}, {claim.RoleImplementer, pods.WakeChecks}} {
		token, err := claim.NewToken("legion", rootIssue, wake.role)
		if err != nil {
			t.Fatal(err)
		}
		if taken, err := st.TakePodWake(ctx, "legion", rootIssue, token, wake.kind, at, 0); err != nil || !taken {
			t.Fatalf("take the %s wake of the %s = %t, %v; want it taken", wake.kind, wake.role, taken, err)
		}
		queueWakeStart(t, st, wake.role, wake.kind)
	}
	if entry {
		queueSupervise(t, st, record.SuperviseRequest{Op: "start", Tree: rootIssue, Role: claim.RoleArchitect,
			Generation: 1, Task: entryTask, Entry: 2})
	}
	if look {
		queueSupervise(t, st, record.SuperviseRequest{Op: "start", Tree: rootIssue, Role: claim.RoleImplementer,
			Generation: 1, Task: lookTask, CheckLook: 1, CheckOpenedAt: at.Add(-time.Hour)})
	}
	return last
}

// startsOfTheRoot is each supervise start of the root the outbox holds, in id order, spelled
// `role|phase|task|wakeKind|entry|checkLook`.
func startsOfTheRoot(t *testing.T, st *Store) []string {
	t.Helper()
	rows, err := st.Pool().Query(context.Background(), `select payload->>'role', coalesce(payload->>'phase', ''),
		coalesce(payload->>'task', ''), coalesce(payload->>'wakeKind', ''), coalesce(payload->>'entry', ''),
		coalesce(payload->>'checkLook', '') from outbox
		where issue = $1 and kind = 'supervise' and payload->>'op' = 'start' order by id`, rootIssue)
	if err != nil {
		t.Fatalf("read the queued starts of %s: %v", rootIssue, err)
	}
	defer rows.Close()
	var starts []string
	for rows.Next() {
		var role, named, task, kind, entry, look string
		if err := rows.Scan(&role, &named, &task, &kind, &entry, &look); err != nil {
			t.Fatalf("read the queued starts of %s: %v", rootIssue, err)
		}
		starts = append(starts, role+"|"+named+"|"+task+"|"+kind+"|"+entry+"|"+look)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the queued starts of %s: %v", rootIssue, err)
	}
	return starts
}

// The entry start and the look of the two starts that are no wake's, as startsOfTheRoot spells
// them: neither names a wake's kind.
const (
	entryStart = "architect||" + entryTask + "||2|"
	lookStart  = "implementer||" + lookTask + "|||1"
)

// The teardown passes over a wake's own start of a role the release recorded, since that start
// waits for the teardown and starts the role in the pod that comes back. A root architect's entry
// start and a production check's look of such a role are the pod being given work, as a phase's
// start is, and the teardown cancels the release for each.
func TestATeardownReadsAnEntryStartOrACheckLookBesideAWakeAsWorkThePodWasGiven(t *testing.T) {
	for _, tc := range []struct {
		name        string
		entry, look bool
		want        ReleaseAct
		cause       ReleaseCause
	}{
		{name: "the wakes' own starts alone", want: ReleaseActs},
		{name: "the root architect's entry start beside them", entry: true, want: ReleaseCancel, cause: ReleaseCauseOvertaken},
		{name: "a production check's look beside them", look: true, want: ReleaseCancel, cause: ReleaseCauseOvertaken},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := migratedStore(t)
			last := wakesBesideWork(t, st, tc.entry, tc.look)
			decision, err := st.IssueRelease(context.Background(), "legion", IssueReleased{Issue: rootIssue, Tree: rootIssue,
				IssueGeneration: 1, TreeGeneration: 1, Row: last}, record.OutOfWorkflow)
			if err != nil {
				t.Fatalf("IssueRelease: %v", err)
			}
			if decision.Act != tc.want || decision.Cause != tc.cause {
				t.Fatalf("the teardown = %s for %s, want %s for %s", decision.Act, decision.Cause, tc.want, tc.cause)
			}
		})
	}
}

// A cancel deletes each wake taken under the mark and the start that wake queued, and hands its
// builder the kind and the task of that wake (ReleaseWokenRow). The root architect's entry start
// and a production check's look are no wake's start: they stay as they were queued, and neither
// task reaches the builder as the wake's.
func TestACancelKeepsAnEntryStartAndACheckLookQueuedBesideAWake(t *testing.T) {
	st := migratedStore(t)
	wakesBesideWork(t, st, true, true)
	told := map[claim.Role]string{}
	woken := func(role claim.Role, generation uint64, kind, task string) (IdleReleaseRow, error) {
		told[role] = kind + "|" + task
		return resumeRow(role, generation)
	}

	cancelled, err := st.CancelIssueRelease(context.Background(), "legion", rootIssue, nil, record.OutOfWorkflow,
		ReleaseCancelPause{At: time.Now()}, woken)
	if err != nil || !slices.Equal(cancelled.Resumed, []claim.Role{claim.RoleArchitect, claim.RoleImplementer}) {
		t.Fatalf("CancelIssueRelease = %+v, %v; want the architect and the implementer resumed", cancelled, err)
	}
	want := []string{entryStart, lookStart, "architect|||||", "implementer|||||"}
	if got := startsOfTheRoot(t, st); !slices.Equal(got, want) {
		t.Errorf("queued starts after the cancel = %q, want %q: the wakes' own starts go, and the entry start and the look stay",
			got, want)
	}
	wantTold := map[claim.Role]string{
		claim.RoleArchitect:   pods.WakeRoleMessage + "|the wake's task",
		claim.RoleImplementer: pods.WakeChecks + "|the wake's task",
	}
	if !maps.Equal(told, wantTold) {
		t.Errorf("the builder was told %v, want %v: the wake's own task of each role", told, wantTold)
	}
}

// The release's record and a cancel made while the project is paused rewrite the task of each
// start a wake took under the mark (retellWakesUnderRelease, retellWakesUnderPause). The root
// architect's entry start and a production check's look are no wake's start, and keep their tasks.
// The store does not import the workflow, so each rule here is a stand-in, as in
// TestARecordedReleaseRetellsTheStartOfAHeldWakeTakenUnderItsMark.
func TestARetellRewritesNoEntryStartAndNoCheckLook(t *testing.T) {
	t.Run("the release's record", func(t *testing.T) {
		st := migratedStore(t)
		wakesBesideWork(t, st, true, true)
		retell := func(kind, task string) string { return "the release's " + kind + " wake: " + task }
		if _, err := st.RecordPodRelease(context.Background(), PodRelease{Project: "legion", Issue: rootIssue, Generation: 1,
			Roles: []string{"architect", "implementer"}, At: time.Now().UTC(), WakeTask: retell}); err != nil {
			t.Fatalf("record the release: %v", err)
		}
		want := []string{
			"architect||the release's " + pods.WakeRoleMessage + " wake: the wake's task|" + pods.WakeRoleMessage + "||",
			"implementer||the release's " + pods.WakeChecks + " wake: the wake's task|" + pods.WakeChecks + "||",
			entryStart, lookStart,
		}
		if got := startsOfTheRoot(t, st); !slices.Equal(got, want) {
			t.Errorf("queued starts after the record = %q, want %q: only the wakes' own starts are told again", got, want)
		}
	})
	t.Run("a cancel while the project is paused", func(t *testing.T) {
		st := migratedStore(t)
		wakesBesideWork(t, st, true, true)
		pause, _ := pauseLegion(t, st, time.Now().UTC())
		retell := func(kind, task string) string { return "the pause's " + kind + " wake: " + task }
		cancelled, err := st.CancelIssueRelease(context.Background(), "legion", rootIssue, resumeRow, record.OutOfWorkflow,
			ReleaseCancelPause{At: time.Now().UTC(), WakeTask: retell})
		if err != nil || cancelled.Pause != pause.ID || len(cancelled.Resumed) != 0 {
			t.Fatalf("cancel the release while paused = %+v, %v; want pause %d and no role resumed", cancelled, err, pause.ID)
		}
		want := []string{
			"architect||the pause's " + pods.WakeRoleMessage + " wake: the wake's task|" + pods.WakeRoleMessage + "||",
			"implementer||the pause's " + pods.WakeChecks + " wake: the wake's task|" + pods.WakeChecks + "||",
			entryStart, lookStart,
		}
		if got := startsOfTheRoot(t, st); !slices.Equal(got, want) {
			t.Errorf("queued starts after the cancel = %q, want %q: only the kept wakes' own starts are told again", got, want)
		}
	})
}
