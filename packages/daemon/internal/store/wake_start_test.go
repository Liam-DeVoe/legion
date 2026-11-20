package store

import (
	"context"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

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

// The class search of dispatch://INFRA-3589 at 45fc14a3, entry 40. A production check's look has
// the shape of a start that brings its role back (resumeStartRow), but it is fenced on its check
// and its look, and it finishes without acting when its look ended before it ran. A listener's
// wake that read it as the role coming back would take nothing (workflow's listenerWakeReleased),
// and the role would stay released when the look starts nothing. So StartQueued counts no look.
// A cancel's resume of the same role at the same generation is such a start, and it counts.
func TestAStartQueuedIsNoProductionChecksLook(t *testing.T) {
	st := migratedStore(t)
	openRunningTree(t, st)
	records := podRecords(t, st)
	queued := func() bool {
		t.Helper()
		var queued bool
		inTx(t, st, func(tx pgx.Tx) error {
			var err error
			queued, err = records.StartQueued(context.Background(), tx, rootIssue, claim.RoleImplementer, 1)
			return err
		})
		return queued
	}
	queueSupervise(t, st, record.SuperviseRequest{Op: "start", Tree: rootIssue, Role: claim.RoleImplementer,
		Generation: 1, Task: lookTask, CheckLook: 1, CheckOpenedAt: time.Now().UTC().Add(-time.Hour)})
	if queued() {
		t.Error("StartQueued = true with only a production check's look of the implementer queued, want false: the look can finish without acting, and the role would stay released")
	}
	queueSupervise(t, st, record.SuperviseRequest{Op: "start", Tree: rootIssue, Role: claim.RoleImplementer, Generation: 1})
	if !queued() {
		t.Error("StartQueued = false with a cancel's resume of the implementer queued, want true: that start brings the role back")
	}
}

// The class search of dispatch://INFRA-3589 at 45fc14a3, entry 41. A wake of a role of the mark's
// set was taken under the mark, and then an operator suspended that role's claim, which cancels
// the release (releaseOvertaken). The cancel resumes only the claims still suspended as
// `released`, so it neither drops nor holds that wake. The release took no pod, so the wake's
// resume is no pod's return: the cancel deletes it (dropTakenOverWakes). Left behind, it pairs
// with the release recorded before the wake, and the records count a pod return for that release.
// The wake's own start stays, for the operator's resume to run.
func TestACancelDeletesTheResumeOfAWakeWhoseClaimAnOperatorTookOver(t *testing.T) {
	st := migratedStore(t)
	ctx := context.Background()
	openRunningTree(t, st)
	rootClaim(t, st, claim.RoleImplementer, supervise.StateIdle, "")
	rootClaim(t, st, claim.RoleArchitect, supervise.StateIdle, "")
	at := time.Now().UTC()
	recordRelease(t, st, at.Add(-2*time.Hour))
	boundary, stop, _ := releaseTwoRolesThroughTheStore(t, st)
	stoppedByTheRelease(t, st, claim.RoleImplementer, boundary)
	stoppedByTheRelease(t, st, claim.RoleArchitect, stop)
	if taken, err := st.TakePodWake(ctx, "legion", rootIssue, releasedToken, pods.WakeChecks, at, 0); err != nil || !taken {
		t.Fatalf("take the implementer's wake = %t, %v; want it taken", taken, err)
	}
	queueWakeStart(t, st, claim.RoleImplementer, pods.WakeChecks)
	rootClaim(t, st, claim.RoleImplementer, supervise.StateSuspended, supervise.SuspendedByOperator)

	cancelled, err := st.CancelIssueRelease(ctx, "legion", rootIssue, resumeRow, record.OutOfWorkflow, ReleaseCancelPause{At: time.Now()})
	if err != nil || !slices.Equal(cancelled.Resumed, []claim.Role{claim.RoleArchitect}) {
		t.Fatalf("CancelIssueRelease = %+v, %v; want the architect alone resumed: the operator holds the implementer", cancelled, err)
	}
	if n := pauseCount(t, st, `select count(*)::int from pod_resumes where issue = $1 and role = 'implementer'`, rootIssue); n != 0 {
		t.Errorf("the implementer's resumes after the cancel = %d, want none: the release took no pod, so its wake brought none back", n)
	}
	want := []string{"implementer||the wake's task|" + pods.WakeChecks + "||", "architect|||||"}
	if got := startsOfTheRoot(t, st); !slices.Equal(got, want) {
		t.Errorf("queued starts after the cancel = %q, want %q: the wake's start stays for the operator's resume, and the architect gets the cancel's resume",
			got, want)
	}
}

// The kept start of TestACancelDeletesTheResumeOfAWakeWhoseClaimAnOperatorTookOver was written for
// a session a release stopped, and its task says that the release took the pod. The release took
// none, and the role reads that task when the operator resumes it (R3 of dispatch://INFRA-3589:
// the woken role's wake text is true). So the cancel rewrites that task in the same transaction,
// through the workflow's rule (ReleaseCancelPause.TakenOverWakeTask), told the kind of the wake's
// resume, whether the project runs or is paused. Only that task changes: the start keeps its wake
// kind and the rest of its payload, and the cancel's other starts keep theirs. The store does not
// import the workflow, so the rule here is a stand-in; the workflow's
// TestATakenOverWakeTaskIsTheTaskOfAWakeOfACancelledRelease holds its words.
func TestACancelRetellsTheKeptStartOfAWakeWhoseClaimAnOperatorTookOver(t *testing.T) {
	for _, paused := range []bool{false, true} {
		t.Run(map[bool]string{false: "the project runs", true: "the project is paused"}[paused], func(t *testing.T) {
			st := migratedStore(t)
			ctx := context.Background()
			openRunningTree(t, st)
			rootClaim(t, st, claim.RoleImplementer, supervise.StateIdle, "")
			rootClaim(t, st, claim.RoleArchitect, supervise.StateIdle, "")
			boundary, stop, _ := releaseTwoRolesThroughTheStore(t, st)
			stoppedByTheRelease(t, st, claim.RoleImplementer, boundary)
			stoppedByTheRelease(t, st, claim.RoleArchitect, stop)
			if taken, err := st.TakePodWake(ctx, "legion", rootIssue, releasedToken, pods.WakeRoleMessage, time.Now().UTC(), 0); err != nil || !taken {
				t.Fatalf("take the implementer's wake = %t, %v; want it taken", taken, err)
			}
			queueWakeStart(t, st, claim.RoleImplementer, pods.WakeRoleMessage)
			rootClaim(t, st, claim.RoleImplementer, supervise.StateSuspended, supervise.SuspendedByOperator)
			if paused {
				pauseLegion(t, st, time.Now().UTC())
			}
			retold := func(kind, task string) string { return "the cancelled release's " + kind + " wake: " + task }
			kept := func(kind, task string) string { return "the pause's " + kind + " wake: " + task }

			if _, err := st.CancelIssueRelease(ctx, "legion", rootIssue, resumeRow, record.OutOfWorkflow,
				ReleaseCancelPause{At: time.Now().UTC(), WakeTask: kept, TakenOverWakeTask: retold}); err != nil {
				t.Fatalf("CancelIssueRelease: %v", err)
			}
			want := []string{"implementer||the cancelled release's " + pods.WakeRoleMessage + " wake: the wake's task|" + pods.WakeRoleMessage + "||"}
			if !paused {
				want = append(want, "architect|||||")
			}
			if got := startsOfTheRoot(t, st); !slices.Equal(got, want) {
				t.Errorf("queued starts after the cancel = %q, want %q: the kept start of the role the operator took over is told in the words of a release that did not finish",
					got, want)
			}
		})
	}
}
