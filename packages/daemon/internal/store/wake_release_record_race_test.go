package store

import (
	"context"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/pods"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/supervise"
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
	if taken, err := records.TakePodWake(ctx, tx, rootIssue, claim.RoleImplementer, pods.WakeAgentMessage, at.Add(-time.Second)); err != nil || !taken {
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
	if taken, err := records.TakePodWake(ctx, tx, rootIssue, claim.RoleImplementer, pods.WakeAgentMessage, at); err != nil || !taken {
		t.Fatalf("take the implementer's wake = %t, %v; want it taken", taken, err)
	}

	cancelCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, _, err := store.CancelIssueRelease(cancelCtx, "legion", rootIssue, resumeRow, record.OutOfWorkflow)
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
