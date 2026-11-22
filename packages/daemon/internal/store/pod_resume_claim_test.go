package store

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/pods"
)

// A role's open resume is the resume of the claim its wake brought back (pod_resumes.claim), so
// a read of the resume for another claim of the same role finds none, and a close for that other
// claim leaves it open. Two claims can hold one role: the agent of key a, `…-implementer`, works
// the tester's step, beside the tester's own claim of an earlier version of the plan. A paused
// cancel of a release reads each released claim's resume (stopReleasedForPause), and a read by
// role alone would mark the tester's own claim owed the launch the wake took for key a's agent. A
// resume the release before wrote names no claim, and is still each claim of its role's.
func TestAnOpenResumeIsTheClaimItsWakeBroughtBack(t *testing.T) {
	ctx := context.Background()
	st := migratedStore(t)
	keyed, err := claim.NewToken("legion", rootIssue, claim.RoleImplementer)
	if err != nil {
		t.Fatal(err)
	}
	own, err := claim.NewToken("legion", rootIssue, claim.RoleTester)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Truncate(time.Millisecond)
	if taken, err := st.TakePodWake(ctx, "legion", rootIssue, keyed, claim.RoleTester, pods.WakeDelivery, at, 0); err != nil || !taken {
		t.Fatalf("take key a's wake on the tester's step = %t, %v; want it taken", taken, err)
	}
	if _, _, found, err := st.OpenPodResume(ctx, "legion", rootIssue, claim.RoleTester, keyed); err != nil || !found {
		t.Fatalf("key a's open resume = %t, %v; want the one its wake took", found, err)
	}
	if _, _, found, err := st.OpenPodResume(ctx, "legion", rootIssue, claim.RoleTester, own); err != nil || found {
		t.Fatalf("the tester's own claim's open resume = %t, %v; want none: the wake brought key a's agent back", found, err)
	}
	if err := st.Tx(ctx, func(tx pgx.Tx) error {
		closed, err := CloseRoleOpenResumeTx(ctx, tx, "legion", rootIssue, claim.RoleTester, own, at)
		if err == nil && closed {
			t.Errorf("a close for the tester's own claim closed key a's resume")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, found, err := st.OpenPodResume(ctx, "legion", rootIssue, claim.RoleTester, keyed); err != nil || !found {
		t.Fatalf("key a's open resume after the other claim's close = %t, %v; want it still open", found, err)
	}

	if _, err := st.Pool().Exec(ctx, `update pod_resumes set claim = '' where issue = $1`, rootIssue); err != nil {
		t.Fatal(err)
	}
	if _, _, found, err := st.OpenPodResume(ctx, "legion", rootIssue, claim.RoleTester, own); err != nil || !found {
		t.Fatalf("a resume the release before wrote, read for the tester's own claim = %t, %v; want it found: it names no claim", found, err)
	}
}

// The wake pacing's let-out of a launch moves the stuck clock of that claim's own resume and of no
// other claim's, so another claim of the same role is not hidden from the stuck rule by a launch
// that is not its own. The agent of key a, `…-implementer`, works the tester's step beside the
// tester's own claim; key a's wake took the tester's open resume. A let-out of the tester's own
// claim finds no resume of its own and moves nothing; key a's let-out moves its clock. A resume
// the release before wrote names no claim, so any claim of its role's let-out moves it.
func TestALetOutMovesTheStuckClockOfItsOwnClaimsResumeAlone(t *testing.T) {
	ctx := context.Background()
	st := migratedStore(t)
	keyed, err := claim.NewToken("legion", rootIssue, claim.RoleImplementer)
	if err != nil {
		t.Fatal(err)
	}
	own, err := claim.NewToken("legion", rootIssue, claim.RoleTester)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Truncate(time.Millisecond)
	if taken, err := st.TakePodWake(ctx, "legion", rootIssue, keyed, claim.RoleTester, pods.WakeDelivery, at, 0); err != nil || !taken {
		t.Fatalf("take key a's wake on the tester's step = %t, %v; want it taken", taken, err)
	}
	clock := func() time.Time {
		t.Helper()
		var clockAt time.Time
		if err := st.Pool().QueryRow(ctx, `select clock_at from pod_resumes where issue = $1 and role = 'tester'
			and first_turn_at is null and closed_at is null`, rootIssue).Scan(&clockAt); err != nil {
			t.Fatalf("read the tester's open resume: %v", err)
		}
		return clockAt.UTC()
	}

	if set, err := st.SetResumeClock(ctx, "legion", rootIssue, claim.RoleTester, own, at.Add(5*time.Minute)); err != nil || set {
		t.Errorf("the tester's own claim's let-out = %t, %v; want no resume of its own found", set, err)
	}
	if got := clock(); !got.Equal(at) {
		t.Errorf("key a's clock after the tester's own claim's let-out = %s, want %s: that launch is not key a's", got, at)
	}
	if set, err := st.SetResumeClock(ctx, "legion", rootIssue, claim.RoleTester, keyed, at.Add(5*time.Minute)); err != nil || !set {
		t.Errorf("key a's let-out = %t, %v; want its resume found", set, err)
	}
	if got := clock(); !got.Equal(at.Add(5 * time.Minute)) {
		t.Errorf("key a's clock after its own let-out = %s, want %s", got, at.Add(5*time.Minute))
	}

	if _, err := st.Pool().Exec(ctx, `update pod_resumes set claim = '' where issue = $1`, rootIssue); err != nil {
		t.Fatal(err)
	}
	if set, err := st.SetResumeClock(ctx, "legion", rootIssue, claim.RoleTester, own, at.Add(10*time.Minute)); err != nil || !set {
		t.Errorf("the tester's own claim's let-out over a resume that names no claim = %t, %v; want it found", set, err)
	}
	if got := clock(); !got.Equal(at.Add(10 * time.Minute)) {
		t.Errorf("the clock of a resume that names no claim = %s, want %s", got, at.Add(10*time.Minute))
	}
}
