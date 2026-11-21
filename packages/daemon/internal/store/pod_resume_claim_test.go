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
