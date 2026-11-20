package store

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/pods"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/testwait"
)

// R3 and check l2's row (f) (dispatch://INFRA-3589): a wake taken after a release recorded its
// role as `released` and before the release ended names that release, so pod_resumes holds one
// resume, of that release. RecordPodRelease points at itself each such wake it can read, and a
// take reads the newest recorded release. Two transactions that run at once must not each miss
// the other: here the wake's take has read the releases and inserted its resume, and the
// release's record starts before the take commits. The record may wait for the take; the test
// commits the take two seconds after the record starts, or as soon as the record returns.
//
// The idle rig's held-stop row (internal/daemon, AWakeDuringAHeldStopBringsThePodBackOnce) met
// this order on a loaded pod at 90768ceb: the daemon logged "outbox: released the idle pod of an
// issue" at 05:48:13.283 and "wake: a released pod is coming back" at 05:48:13.316, and the row
// failed with the release recorded and its one resume naming release 0.
func TestAWakeTakenWhileItsReleaseIsRecordedNamesThatRelease(t *testing.T) {
	store := migratedStore(t)
	ctx := context.Background()
	openRunningTree(t, store)
	at := time.Now().UTC().Truncate(time.Millisecond)
	// The release is marked, and its stop has recorded the implementer as released.
	markRelease(t, store, 1, 1, 1, 0, claim.RoleImplementer)
	records := &PodRecords{store: store, project: "legion"}

	tx, err := store.Pool().Begin(ctx)
	if err != nil {
		t.Fatalf("begin the wake's transaction: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if taken, err := records.TakePodWake(ctx, tx, rootIssue, claim.RoleImplementer, pods.WakeAgentMessage, at.Add(-time.Second), 0); err != nil || !taken {
		t.Fatalf("take the implementer's wake = %t, %v; want it taken", taken, err)
	}

	recordCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	type recorded struct {
		id  int64
		err error
	}
	done := make(chan recorded, 1)
	go func() {
		id, err := store.RecordPodRelease(recordCtx, PodRelease{Project: "legion", Issue: rootIssue, Generation: 1,
			Roles: []string{"implementer"}, At: at})
		done <- recorded{id, err}
	}()
	var rec recorded
	waited := false
	select {
	case rec = <-done:
	case <-time.After(2 * time.Second):
		waited = true
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit the wake's transaction: %v", err)
	}
	if waited {
		rec = <-done
	}
	if rec.err != nil {
		t.Fatalf("record the release while the wake's transaction is open: %v", rec.err)
	}
	release := rec.id

	var resumes, named int64
	if err := store.Pool().QueryRow(ctx, `select count(*), count(*) filter (where release_id = $2)
		from pod_resumes where project = 'legion' and issue = $1`, rootIssue, release).Scan(&resumes, &named); err != nil {
		t.Fatalf("read the resumes: %v", err)
	}
	if resumes != 1 || named != 1 {
		var got int64
		_ = store.Pool().QueryRow(ctx, `select release_id from pod_resumes where project = 'legion' and issue = $1
			order by id limit 1`, rootIssue).Scan(&got)
		t.Errorf("pod_resumes holds %d resumes, %d of them naming release %d (the first names release %d); want one resume, of that release: the wake came after the release recorded the role and ends that release",
			resumes, named, release, got)
	}
}

// The same two transactions, where the release does not finish: a cancel for another cause (a
// bundle the release cannot save, say) deletes each wake taken under the mark and starts the role
// once (dropWakesUnderRelease), since the pod never went away. A take still open when the cancel
// runs must not leave its resume behind: that resume names no release that happened, and the
// wake's own start beside it would tell the role its pod was released.
func TestAWakeTakenWhileItsReleaseIsCancelledLeavesNoResume(t *testing.T) {
	store := migratedStore(t)
	ctx := context.Background()
	openRunningTree(t, store)
	at := time.Now().UTC().Truncate(time.Millisecond)
	// The release stops both roles of the pod and has recorded the implementer as released, as in
	// TestACancelAfterAWakeOfARoleTheReleaseRecordedStartsTheRoleOnceWithNoWake, where the take
	// commits before the cancel and the cancel drops it.
	rootClaim(t, store, claim.RoleImplementer, supervise.StateIdle, "")
	rootClaim(t, store, claim.RoleArchitect, supervise.StateIdle, "")
	boundary, _, _ := releaseTwoRolesThroughTheStore(t, store)
	stoppedByTheRelease(t, store, claim.RoleImplementer, boundary)
	records := &PodRecords{store: store, project: "legion"}

	tx, err := store.Pool().Begin(ctx)
	if err != nil {
		t.Fatalf("begin the wake's transaction: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if taken, err := records.TakePodWake(ctx, tx, rootIssue, claim.RoleImplementer, pods.WakeAgentMessage, at, 0); err != nil || !taken {
		t.Fatalf("take the implementer's wake = %t, %v; want it taken", taken, err)
	}

	cancelCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := store.CancelIssueRelease(cancelCtx, "legion", rootIssue, resumeRow, record.OutOfWorkflow, ReleaseCancelPause{At: time.Now()})
		done <- err
	}()
	var cancelErr error
	waited := false
	select {
	case cancelErr = <-done:
	case <-time.After(2 * time.Second):
		waited = true
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit the wake's transaction: %v", err)
	}
	if waited {
		cancelErr = <-done
	}
	if cancelErr != nil {
		t.Fatalf("cancel the release while the wake's transaction is open: %v", cancelErr)
	}

	var resumes int64
	if err := store.Pool().QueryRow(ctx, `select count(*) from pod_resumes where project = 'legion' and issue = $1`,
		rootIssue).Scan(&resumes); err != nil {
		t.Fatalf("read the resumes: %v", err)
	}
	if resumes != 0 {
		t.Errorf("pod_resumes holds %d resumes after the cancel, want none: the release was cancelled, so the pod never went away and no released pod came back", resumes)
	}
}

// Window 1 of round 9's class search (dispatch://INFRA-3589): the workflow's wake closes a
// resume whose pod went away (ClosePodGoneResume) and then takes its own (TakePodWake), in one
// transaction (workflow's wakeReleased). A release recorded between the close's read of the
// newest release and the take's lock on the mark is missed by the close and read by the take.
// The take then finds the old resume still open and takes nothing, so the wake is lost while the
// role's pod is gone. Here the record of a release of the pod no claim owns, which closes no
// resume, holds the mark while the wake runs. The close must wait for the record and read its
// release: the old resume is closed, and the wake takes its own.
func TestAWakeWhileANewerReleaseIsRecordedClosesTheOldResumeAndTakesItsOwn(t *testing.T) {
	store := migratedStore(t)
	ctx := context.Background()
	openRunningTree(t, store)
	at := time.Now().UTC().Truncate(time.Millisecond)
	// An idle release took the pod and recorded the implementer. A wake of the implementer came
	// after it, and its resume stands open: the pod it waits for never came back.
	idle, err := store.RecordPodRelease(ctx, PodRelease{Project: "legion", Issue: rootIssue, Generation: 1,
		Roles: []string{"implementer"}, At: at.Add(-2 * time.Hour)})
	if err != nil {
		t.Fatalf("record the idle release: %v", err)
	}
	if taken, err := store.TakePodWake(ctx, "legion", rootIssue, releasedToken, pods.WakeDelivery,
		at.Add(-time.Hour), 0); err != nil || !taken {
		t.Fatalf("take the implementer's first wake = %t, %v; want it taken", taken, err)
	}
	old, _, found, err := store.OpenPodResume(ctx, "legion", rootIssue, claim.RoleImplementer)
	if err != nil || !found {
		t.Fatalf("the implementer's open resume = %t, %v; want the one its first wake recorded", found, err)
	}
	// The second rule then releases the pod no claim can use. Its mark names no role, so it closes
	// no resume.
	marked, err := store.ReleaseIdleIssue(ctx, "legion", rootIssue, func(view pods.IdleView) (IdleReleaseMark, error) {
		return IdleReleaseMark{Generation: view.Generation, SuspendsNoRole: true, Rows: []IdleReleaseRow{{
			Kind: "issue_release", Payload: []byte(`{"tree":"LEGION-208","generation":1,"treeGeneration":1}`),
			Boundary: true}}}, nil
	})
	if err != nil || !marked {
		t.Fatalf("ReleaseIdleIssue = %t, %v; want the release of the pod no claim owns marked", marked, err)
	}

	// The teardown's record of that release has locked the mark and written the release's row, and
	// has not committed: the first two statements of RecordPodRelease. The rest of it changes
	// nothing here, since the mark names no role and the pod has no hold.
	rec, err := store.Pool().Begin(ctx)
	if err != nil {
		t.Fatalf("begin the record's transaction: %v", err)
	}
	defer func() { _ = rec.Rollback(ctx) }()
	if _, err := rec.Exec(ctx, `select 1 from issue_releases where project = 'legion' and issue = $1 for update`,
		rootIssue); err != nil {
		t.Fatalf("lock the release mark: %v", err)
	}
	var newer int64
	if err := rec.QueryRow(ctx, `insert into pod_releases (project, issue, generation, roles, at, bundle_bytes)
		values ('legion', $1, 1, '', $2, 0) returning id`, rootIssue, at.Add(-time.Minute)).Scan(&newer); err != nil {
		t.Fatalf("write the newer release: %v", err)
	}
	var holder int32
	if err := rec.QueryRow(ctx, `select pg_backend_pid()`).Scan(&holder); err != nil {
		t.Fatalf("read the record's backend: %v", err)
	}

	// The implementer's backstop fires while the record is open.
	records := podRecords(t, store)
	wakeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	type woken struct {
		closed int64
		taken  bool
		err    error
	}
	done := make(chan woken, 1)
	go func() {
		var w woken
		w.err = store.Tx(wakeCtx, func(tx pgx.Tx) error {
			var err error
			if w.closed, err = records.ClosePodGoneResume(wakeCtx, tx, rootIssue, claim.RoleImplementer); err != nil {
				return err
			}
			w.taken, err = records.TakePodWake(wakeCtx, tx, rootIssue, claim.RoleImplementer, pods.WakeBackstop, at, 0)
			return err
		})
		done <- w
	}()
	if !testwait.Holds(20*time.Second, 20*time.Millisecond, func() bool {
		var waiting bool
		err := store.Pool().QueryRow(ctx, `select exists (select 1 from pg_stat_activity
			where $1 = any(pg_blocking_pids(pid)))`, holder).Scan(&waiting)
		return err == nil && waiting
	}) {
		t.Fatal("the wake never waited for the record's lock on the release mark")
	}
	if err := rec.Commit(ctx); err != nil {
		t.Fatalf("commit the record: %v", err)
	}
	w := <-done
	if w.err != nil {
		t.Fatalf("the wake: %v", w.err)
	}

	if w.closed != newer || !w.taken {
		t.Errorf("the wake closed a resume under release %d and took its own wake = %t; want the old resume closed under release %d and the wake taken: that release was recorded before the take",
			w.closed, w.taken, newer)
	}
	var oldClosed bool
	if err := store.Pool().QueryRow(ctx, `select closed_at is not null from pod_resumes where id = $1`,
		old).Scan(&oldClosed); err != nil {
		t.Fatalf("read the old resume: %v", err)
	}
	if !oldClosed {
		t.Errorf("the old resume %d is still open; want it closed: its pod went away with release %d", old, newer)
	}
	// The wake's own resume ends the idle release, the newest that suspended a role
	// (LatestPodRelease), and was taken after the newer release.
	var open, id, release, seen int64
	if err := store.Pool().QueryRow(ctx, `select count(*), coalesce(max(id), 0), coalesce(max(release_id), 0),
		coalesce(max(seen_release), 0) from pod_resumes where project = 'legion' and issue = $1
		and role = 'implementer' and first_turn_at is null and closed_at is null`, rootIssue).Scan(&open, &id, &release, &seen); err != nil {
		t.Fatalf("read the open resumes: %v", err)
	}
	if open != 1 || id == old || release != idle || seen != newer {
		t.Errorf("open resumes of the implementer = %d (the newest is %d, of release %d, seen release %d); want one, the wake's own and not the old %d, of release %d and seen release %d",
			open, id, release, seen, old, idle, newer)
	}
}

// Window 2 of round 9's class search (dispatch://INFRA-3589): the outbox's delivery wake resumes
// its claim in process, with no `start` row to wait for the release's teardown. A release's stop
// suspends the claim as `released` first, and records the role on the mark after it, in a
// transaction of its own. A delivery wake that read the role on the mark found it not recorded
// in that gap, took the wake and resumed the role in the pod the teardown was about to take. So
// the delivery wake takes nothing while any mark of the issue stands, and once the teardown has
// cleared the mark it takes its wake against the release the teardown recorded.
func TestADeliveryWakeTakesNothingWhileAReleaseMarkStands(t *testing.T) {
	store := migratedStore(t)
	ctx := context.Background()
	openRunningTree(t, store)
	at := time.Now().UTC().Truncate(time.Millisecond)
	rootClaim(t, store, claim.RoleImplementer, supervise.StateIdle, "")
	rootClaim(t, store, claim.RoleArchitect, supervise.StateIdle, "")
	boundary, stop, _ := releaseTwoRolesThroughTheStore(t, store)
	// The implementer's stop has suspended its claim as `released`, and has not recorded the role
	// on the mark yet.
	releasedClaim(t, store, claim.RoleImplementer)
	resumes := func() int64 {
		t.Helper()
		var n int64
		if err := store.Pool().QueryRow(ctx, `select count(*) from pod_resumes where project = 'legion' and issue = $1`,
			rootIssue).Scan(&n); err != nil {
			t.Fatalf("count the resumes: %v", err)
		}
		return n
	}

	taken, inRelease, err := store.TakeDeliveryWake(ctx, "legion", rootIssue, releasedToken, at, 0)
	if err != nil || taken || !inRelease {
		t.Fatalf("the delivery wake before the stop records the role = taken %t, in a release %t, %v; want nothing taken and the release named: the teardown is about to take the pod",
			taken, inRelease, err)
	}
	if n := resumes(); n != 0 {
		t.Fatalf("pod_resumes holds %d resumes after the delivery wake in the release, want none", n)
	}

	// The stops record their roles. The mark still stands, and the wake still takes nothing.
	stoppedByTheRelease(t, store, claim.RoleImplementer, boundary)
	stoppedByTheRelease(t, store, claim.RoleArchitect, stop)
	taken, inRelease, err = store.TakeDeliveryWake(ctx, "legion", rootIssue, releasedToken, at, 0)
	if err != nil || taken || !inRelease {
		t.Fatalf("the delivery wake after the stops record their roles = taken %t, in a release %t, %v; want nothing taken and the release named",
			taken, inRelease, err)
	}
	if n := resumes(); n != 0 {
		t.Fatalf("pod_resumes holds %d resumes after the delivery wake in the release, want none", n)
	}

	// The teardown records the release and clears the mark. The next attempt takes the wake
	// against that release.
	release, err := store.RecordPodRelease(ctx, PodRelease{Project: "legion", Issue: rootIssue, Generation: 1,
		Roles: []string{"implementer", "architect"}, At: at.Add(time.Minute)})
	if err != nil {
		t.Fatalf("record the release: %v", err)
	}
	if err := store.ClearIssueRelease(ctx, "legion", rootIssue); err != nil {
		t.Fatalf("clear the release's mark: %v", err)
	}
	taken, inRelease, err = store.TakeDeliveryWake(ctx, "legion", rootIssue, releasedToken, at.Add(2*time.Minute), 0)
	if err != nil || !taken || inRelease {
		t.Fatalf("the delivery wake once the teardown finished = taken %t, in a release %t, %v; want the wake taken", taken, inRelease, err)
	}
	if n := resumes(); n != 1 {
		t.Fatalf("pod_resumes holds %d resumes after the wake, want the wake's one", n)
	}
	var resumed, seen int64
	var kind string
	if err := store.Pool().QueryRow(ctx, `select release_id, seen_release, kind from pod_resumes
		where project = 'legion' and issue = $1 and role = 'implementer'`, rootIssue).Scan(&resumed, &seen, &kind); err != nil {
		t.Fatalf("read the wake's resume: %v", err)
	}
	if resumed != release || seen != release || kind != pods.WakeDelivery {
		t.Errorf("the wake's resume is a %s wake of release %d, seen release %d; want a %s wake of release %d, seen release %d",
			kind, resumed, seen, pods.WakeDelivery, release, release)
	}
}

// A release's record of a wake that a project's pause holds (main's c7e1cbd5 in this pull
// request). A pause starts no release, and a release that saved before the pause runs to its end
// (IssueReleaseSave, decision 12 of dispatch://INFRA-3759). A wake of a role that release
// recorded can then come while the project is paused, and it is a held wake of the pause
// (PauseOfWake, decision 15), which pairs with no release
// (pod_resumes_held_wake_pairs_with_no_release). The release's record points that wake at itself
// as it points every wake taken under its mark, and keeps its release 0: its seen release becomes
// the release, and its time and its stuck clock become the release's time. A record that set the
// release on it would fail the check, and the teardown with it.
func TestARecordedReleaseKeepsAHeldWakeTakenUnderItsMarkPairedWithNoRelease(t *testing.T) {
	store := migratedStore(t)
	ctx := context.Background()
	openRunningTree(t, store)
	at := time.Now().UTC().Truncate(time.Millisecond)
	markRelease(t, store, 1, 1, 1, 0, claim.RoleImplementer)
	pause, _ := pauseLegion(t, store, at.Add(-time.Minute))
	records := podRecords(t, store)
	inTx(t, store, func(tx pgx.Tx) error {
		held, stopping, err := records.PauseOfWake(ctx, tx, rootIssue, releasedToken, 0)
		if err != nil || held != pause.ID || stopping != 0 {
			t.Fatalf("the pause of the implementer's wake = held %d, stopping %d, %v; want held by pause %d and stopped by none",
				held, stopping, err, pause.ID)
		}
		if taken, err := records.TakePodWake(ctx, tx, rootIssue, claim.RoleImplementer, pods.WakeChecks, at.Add(-30*time.Second), held); err != nil || !taken {
			t.Fatalf("take the held wake = %t, %v; want it taken", taken, err)
		}
		return nil
	})

	release, err := store.RecordPodRelease(ctx, PodRelease{Project: "legion", Issue: rootIssue, Generation: 1,
		Roles: []string{"implementer"}, At: at})
	if err != nil {
		t.Fatalf("record the release under the held wake: %v", err)
	}
	var resumed, seen, held int64
	var when, clock time.Time
	if err := store.Pool().QueryRow(ctx, `select release_id, seen_release, coalesce(pause_id, 0), at, clock_at from pod_resumes
		where project = 'legion' and issue = $1 and role = 'implementer'`, rootIssue).Scan(&resumed, &seen, &held, &when, &clock); err != nil {
		t.Fatalf("read the held wake's resume: %v", err)
	}
	if resumed != 0 || seen != release || held != pause.ID || !when.Equal(at) || !clock.Equal(at) {
		t.Errorf("the held wake's resume = release %d, seen release %d, pause %d, time %s, stuck clock %s; want release 0, seen release %d, pause %d and the release's time %s for both",
			resumed, seen, held, when.UTC().Format(time.RFC3339Nano), clock.UTC().Format(time.RFC3339Nano),
			release, pause.ID, at.Format(time.RFC3339Nano))
	}
}
