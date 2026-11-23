package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/pods"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// The cancel of a release that did not finish (CancelIssueRelease, release_cancel.go): what it
// resumes, what it deletes in its one transaction, and what it leaves. The release's mark, its
// rows' decisions and its record are in issue_release_test.go, and the wakes taken under the mark
// in wake_start_test.go.

// A cancel decided at a role stop is not carried out by that decision: the mark stands until
// the resumes of the claims the release suspended are queued, and the two happen in one
// transaction. A mark deleted at the decision left the row's retry reading a release that is
// over — nothing to resume, nothing to undo — with the roles the release had already suspended
// `released` in a pod that still stands, which is what a resume that fails, or a daemon that
// stops between the two, produces.
func TestACancelKeepsTheMarkUntilItsResumesAreQueued(t *testing.T) {
	const first, boundary, last = 18, 20, 22
	st := migratedStore(t)
	ctx := context.Background()
	openRunningTree(t, st)
	markRelease(t, st, first, boundary, last, 0, claim.RoleArchitect)
	releasedClaim(t, st, claim.RoleArchitect)
	// A notice the pod must answer, queued above the release's own rows: the cancel at the
	// boundary that comes of it is the one every other cancel is shaped like.
	if _, err := st.Pool().Exec(ctx, `insert into outbox (id,kind,issue,payload,attempts,next_at,last_error)
		values ($1,'notice',$2,'{"kind":"todo","reason":"r"}',0,now(),'')`, last+1, rootIssue); err != nil {
		t.Fatal(err)
	}

	decision, err := st.IssueReleaseStop(ctx, "legion", rootIssue, boundary)
	if err != nil || decision.Act != ReleaseCancel {
		t.Fatalf("the boundary = %s, %v; want it cancelled", decision.Act, err)
	}
	if !releaseMarked(t, st) {
		t.Fatal("the decision deleted the mark; the cancel it decided had not been carried out")
	}
	// The row is retried — its resume failed, or the daemon stopped — and reads the same cancel.
	retried, err := st.IssueReleaseStop(ctx, "legion", rootIssue, boundary)
	if err != nil || retried.Act != ReleaseCancel {
		t.Fatalf("the retried stop = %s, %v; want the cancel read again", retried.Act, err)
	}

	refused := errors.New("the resume could not be encoded")
	if _, err := st.CancelIssueRelease(ctx, "legion", rootIssue, func(claim.Role, uint64, DroppedWake) (IdleReleaseRow, error) {
		return IdleReleaseRow{}, refused
	}, record.OutOfWorkflow, ReleaseCancelPause{At: time.Now()}); !errors.Is(err, refused) {
		t.Fatalf("a cancel whose resume could not be built = %v, want %v", err, refused)
	}
	if !releaseMarked(t, st) {
		t.Fatal("a cancel that queued no resume still deleted the mark")
	}
	if queued := startRows(t, st); len(queued) != 0 {
		t.Fatalf("starts queued by the refused cancel = %v, want none", queued)
	}

	cancelled, err := st.CancelIssueRelease(ctx, "legion", rootIssue, resumeRow, record.OutOfWorkflow, ReleaseCancelPause{At: time.Now()})
	if err != nil {
		t.Fatalf("carry the cancel out: %v", err)
	}
	if resumed := cancelled.Resumed; len(resumed) != 1 || resumed[0] != claim.RoleArchitect {
		t.Fatalf("resumed = %v, want the one claim the release suspended", resumed)
	}
	if queued := startRows(t, st); len(queued) != 1 || queued[0] != string(claim.RoleArchitect) {
		t.Fatalf("starts queued by the cancel = %v, want the architect's", queued)
	}
	if releaseMarked(t, st) {
		t.Fatal("the cancel queued its resumes and left the mark standing")
	}
}

// The bundle a cancelled release saved goes in the cancel's own transaction, with the resumes and
// the mark. The pod that work came off is the pod the resumes bring back, with the work in its own
// workspace, so the row is a second copy of a live disk's work: left behind it would hold the
// issue's pod (workflow.IdleHoldBundleUnrestored) and wait for whatever pod the issue made next —
// an eviction's, an update round's, a park and unpark's — to restore it over everything those
// roles pushed in the meantime.
//
// One transaction is what makes that true of a cancel that fails too: a resume the store cannot
// queue leaves the mark standing and the bundle with it, so the row's retry runs the whole cancel
// again rather than going on with the bundle already gone or already kept.
func TestACancelDeletesTheBundleItsReleaseSavedInTheSameTransaction(t *testing.T) {
	const first, boundary, last = 18, 20, 22
	st := migratedStore(t)
	ctx := context.Background()
	openRunningTree(t, st)
	markRelease(t, st, first, boundary, last, 0, claim.RoleArchitect)
	releasedClaim(t, st, claim.RoleArchitect)
	var release int64
	if err := st.Pool().QueryRow(ctx, `select id from issue_releases where issue = $1`, rootIssue).Scan(&release); err != nil {
		t.Fatalf("read the id of the release of %s: %v", rootIssue, err)
	}
	saved := runtime.WorkspaceBundle{Bundle: []byte("PACK the work of the cancelled release"),
		Head: "f2f134541b30dfa5160a5d1ce5e17b76c967c661", At: time.Now().UTC(), PodUID: "pod-being-released"}
	if err := st.PutWorkspaceBundle(ctx, rootIssue, release, saved); err != nil {
		t.Fatalf("save the release's bundle: %v", err)
	}

	refused := errors.New("the resume could not be encoded")
	if _, err := st.CancelIssueRelease(ctx, "legion", rootIssue, func(claim.Role, uint64, DroppedWake) (IdleReleaseRow, error) {
		return IdleReleaseRow{}, refused
	}, record.OutOfWorkflow, ReleaseCancelPause{At: time.Now()}); !errors.Is(err, refused) {
		t.Fatalf("a cancel whose resume could not be built = %v, want %v", err, refused)
	}
	if _, held, err := st.WorkspaceBundleSize(ctx, rootIssue); err != nil || !held {
		t.Fatalf("after a cancel that failed the store holds the bundle = %t (%v), want it kept with the mark", held, err)
	}

	if _, err := st.CancelIssueRelease(ctx, "legion", rootIssue, resumeRow, record.OutOfWorkflow, ReleaseCancelPause{At: time.Now()}); err != nil {
		t.Fatalf("carry the cancel out: %v", err)
	}
	if _, held, err := st.WorkspaceBundleSize(ctx, rootIssue); err != nil || held {
		t.Fatalf("after the cancel the store holds the bundle = %t (%v), want it deleted with the mark", held, err)
	}
}

// A cancel resumes the claims its own release suspended and no others. A release that finishes
// leaves its claims `released` until a wake brings them back, so an issue being released again
// can hold claims an earlier release left: resuming those puts a role back into a pod that has
// no work for it, with no wake, no task and no record of why it is there.
func TestACancelResumesNoClaimAnEarlierReleaseSuspended(t *testing.T) {
	const first, boundary, last = 18, 20, 22
	st := migratedStore(t)
	ctx := context.Background()
	openRunningTree(t, st)
	// The implementer was released by an earlier release and never woken; this release was
	// marked over the architect alone, which is the one role its stop suspended.
	releasedClaim(t, st, claim.RoleImplementer)
	releasedClaim(t, st, claim.RoleArchitect)
	markRelease(t, st, first, boundary, last, 0, claim.RoleArchitect)

	cancelled, err := st.CancelIssueRelease(ctx, "legion", rootIssue, resumeRow, record.OutOfWorkflow, ReleaseCancelPause{At: time.Now()})
	if err != nil {
		t.Fatalf("cancel the release: %v", err)
	}
	if resumed := cancelled.Resumed; len(resumed) != 1 || resumed[0] != claim.RoleArchitect {
		t.Fatalf("resumed = %v, want the architect alone: the implementer belongs to the release before this one", resumed)
	}
	if queued := startRows(t, st); len(queued) != 1 || queued[0] != string(claim.RoleArchitect) {
		t.Fatalf("starts queued by the cancel = %v, want the architect's alone", queued)
	}
}

// A release cancelled because its issue is no longer the workflow's queues no resume at all, and
// clears its mark all the same: those claims are the close's to end, and the close is already
// running. A resume queued for them lands above the close's own rows and supersedes them — the
// close's suspension then finishes without acting, its Sandbox is never deleted, and the roles
// of an issue that has left run on in it with their launcher tokens and `gh` credentials.
//
// The sequence is the one production runs: the release's stops have suspended the pod's claims,
// the root is parked while the save runs so the linger's rows are queued above the release's,
// and the release's teardown is the next row of the lane.
func TestACancelOfAnIssueOutOfTheWorkflowQueuesNoResumeSoItsCloseActs(t *testing.T) {
	st := migratedStore(t)
	ctx := context.Background()
	openRunningTree(t, st)
	releasedClaim(t, st, claim.RoleImplementer)
	first, boundary, last := releaseThroughTheStore(t, st, rootIssue)
	if err := st.ReleaseSuspendedClaim(ctx, "legion", rootIssue, claim.RoleImplementer, boundary); err != nil {
		t.Fatalf("record the implementer's suspension: %v", err)
	}

	// The root is parked: its tree lingers, its phase is done, and its close queues its own rows
	// above the release's (workflow's beginLinger).
	if _, err := st.Pool().Exec(ctx, `update issues set phase = 'done', status = 'backlog',
		linger_until = now() + interval '1 hour' where key = $1`, rootIssue); err != nil {
		t.Fatalf("park the root: %v", err)
	}
	var closeRow int64
	if err := st.Pool().QueryRow(ctx, `insert into outbox (kind, issue, payload, attempts, next_at, last_error, lease_token)
		values ('issue_suspend', $1, '{"tree":"LEGION-208","generation":1,"treeGeneration":1}', 0, now(), '', '')
		returning id`, rootIssue).Scan(&closeRow); err != nil {
		t.Fatalf("queue the close's own row: %v", err)
	}

	release := IssueReleased{Issue: rootIssue, Tree: rootIssue, IssueGeneration: 1, TreeGeneration: 1, Row: last}
	teardown, err := st.IssueRelease(ctx, "legion", release, record.OutOfWorkflow)
	if err != nil || teardown.Act != ReleaseCancel || teardown.Cause != ReleaseCauseUnauthorized {
		t.Fatalf("the teardown = %s for %s, %v; want %s for %s", teardown.Act, teardown.Cause, err,
			ReleaseCancel, ReleaseCauseUnauthorized)
	}
	cancelled, err := st.CancelIssueRelease(ctx, "legion", rootIssue, resumeRow, record.OutOfWorkflow, ReleaseCancelPause{At: time.Now()})
	if err != nil {
		t.Fatalf("carry the cancel out: %v", err)
	}
	if len(cancelled.Resumed) != 0 {
		t.Fatalf("resumed = %v, want none: the claims of an issue that has left are its close's", cancelled.Resumed)
	}
	if queued := startRows(t, st); len(queued) != 0 {
		t.Fatalf("starts queued by the cancel = %v, want none", queued)
	}
	if releaseMarked(t, st) {
		t.Fatal("the cancel queued no resume and left its mark behind; every later decision would read a release that is over")
	}

	// The lane deleted the release's own rows as it ran them, so the only rows left below the
	// close's are its own.
	if _, err := st.Pool().Exec(ctx, `delete from outbox where issue = $1 and id between $2 and $3`,
		rootIssue, first, last); err != nil {
		t.Fatalf("finish the release's rows: %v", err)
	}
	act, err := st.IssueSuspension(ctx, "legion",
		IssueClose{Issue: rootIssue, Tree: rootIssue, IssueGeneration: 1, TreeGeneration: 1, Row: closeRow},
		record.OutOfWorkflow)
	if err != nil || !act {
		t.Fatalf("the close = %t, %v; want it to act: its Sandbox deletion is what takes the pod's role Secrets with it", act, err)
	}
}

// A release can still be cancelled after such a wake, for another cause, such as a bundle too
// large to carry; this test calls the cancel as the outbox does then. The pod never goes away, so
// the wake brings nothing back. The cancel deletes the wake's resume and the start the wake
// queued, and queues the role's own resume, which carries no task: the role starts once, in the
// pod it was in, told nothing about a release that did not happen, and no resume stands for that
// release. A wake taken before the mark is no wake of this release, and it stays.
func TestACancelAfterAWakeOfARoleTheReleaseRecordedStartsTheRoleOnceWithNoWake(t *testing.T) {
	st := migratedStore(t)
	ctx := context.Background()
	openRunningTree(t, st)
	rootClaim(t, st, claim.RoleImplementer, supervise.StateIdle, "")
	rootClaim(t, st, claim.RoleArchitect, supervise.StateIdle, "")
	at := time.Now().UTC()
	recordRelease(t, st, at.Add(-2*time.Hour))
	if taken, err := st.TakePodWake(ctx, "legion", rootIssue, releasedToken,
		pods.WakeDelivery, at.Add(-time.Hour), 0); err != nil || !taken {
		t.Fatalf("take the wake before the mark = %t, %v; want it taken", taken, err)
	}

	boundary, _, _ := releaseTwoRolesThroughTheStore(t, st)
	stoppedByTheRelease(t, st, claim.RoleImplementer, boundary)
	if taken, err := st.TakePodWake(ctx, "legion", rootIssue, releasedToken,
		pods.WakeAgentMessage, at, 0); err != nil || !taken {
		t.Fatalf("take the wake after the record = %t, %v; want it taken", taken, err)
	}
	queueWakeStart(t, st, claim.RoleImplementer, pods.WakeAgentMessage)

	cancelled, err := st.CancelIssueRelease(ctx, "legion", rootIssue, resumeRow, record.OutOfWorkflow, ReleaseCancelPause{At: time.Now()})
	if resumed := cancelled.Resumed; err != nil || len(resumed) != 1 || resumed[0] != claim.RoleImplementer {
		t.Fatalf("CancelIssueRelease = %v, %v; want the implementer's resume queued", resumed, err)
	}
	rows, err := st.Pool().Query(ctx, `select payload->>'role', coalesce(payload->>'task', '') from outbox
		where issue = $1 and kind = 'supervise' and payload->>'op' = 'start' order by id`, rootIssue)
	if err != nil {
		t.Fatalf("read the queued starts: %v", err)
	}
	var starts []string
	for rows.Next() {
		var role, task string
		if err := rows.Scan(&role, &task); err != nil {
			t.Fatalf("read a queued start: %v", err)
		}
		starts = append(starts, role+":"+task)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("read the queued starts: %v", err)
	}
	if len(starts) != 1 || starts[0] != string(claim.RoleImplementer)+":" {
		t.Fatalf("queued starts = %q, want the cancel's one start of the implementer, with no task: the wake's start goes with the wake", starts)
	}
	var kinds []string
	resumes, err := st.Pool().Query(ctx, `select kind from pod_resumes where issue = $1 order by id`, rootIssue)
	if err != nil {
		t.Fatalf("read the resumes: %v", err)
	}
	for resumes.Next() {
		var kind string
		if err := resumes.Scan(&kind); err != nil {
			t.Fatalf("read a resume: %v", err)
		}
		kinds = append(kinds, kind)
	}
	resumes.Close()
	if err := resumes.Err(); err != nil {
		t.Fatalf("read the resumes: %v", err)
	}
	if len(kinds) != 1 || kinds[0] != pods.WakeDelivery {
		t.Fatalf("resumes = %q, want the wake before the mark alone: the pod never went away, so the wake after the record brought nothing back", kinds)
	}
	if releaseMarked(t, st) {
		t.Fatal("the cancel left its mark behind")
	}
}
