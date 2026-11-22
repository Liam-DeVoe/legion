package store_test

import (
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/cipher/store/storetest"
)

// TestProjectStandingKeepsTheLaterAnswer: an answer for a person and project is written unless the
// row holds one asked later, so an older answer written after a newer one leaves the newer. An
// answer that Dispatch holds no such project is kept for no person, and read for every person
// beside their own answer, the later of the two by when each was asked: so a project deleted after
// a person's answer reads as no project for them, and one made again reads as their later answer.
// Nothing is kept for a person and project never answered.
//
// Control: KeepProjectStanding writing every answer whatever the row holds (its where clause
// gone); the older visible answer as of t1 reads back, and the test fails naming that write.
func TestProjectStandingKeepsTheLaterAnswer(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	const ada, bob = "ada@example.com", "bob@example.com"
	if vis, visible, at, found, err := st.KeptProjectStanding(ctx, ada, "PRIV"); err != nil || found || vis != "" || visible || !at.IsZero() {
		t.Fatalf("KeptProjectStanding(ada, PRIV) with nothing kept = %q, %t, %v, %t, %v; want not found", vis, visible, at, found, err)
	}
	t1 := time.Date(2026, 11, 22, 0, 0, 1, 0, time.UTC)
	t2, t3, t4 := t1.Add(time.Second), t1.Add(2*time.Second), t1.Add(3*time.Second)
	type read struct {
		person, visibility string
		visible            bool
		at                 time.Time
	}
	for _, step := range []struct {
		person, visibility string
		visible            bool
		asked              time.Time
		want               []read
		write              string
	}{
		{ada, "private", false, t2, []read{{ada, "private", false, t2}}, "ada off PRIV's list as of t2"},
		{ada, "private", true, t1, []read{{ada, "private", false, t2}}, "ada on PRIV's list as of t1, older than the row's"},
		{bob, "private", true, t2, []read{{ada, "private", false, t2}, {bob, "private", true, t2}}, "bob on PRIV's list as of t2"},
		{bob, "", false, t3, []read{{ada, "", false, t3}, {bob, "", false, t3}}, "no such project as of t3, asked for bob"},
		{ada, "public", true, t4, []read{{ada, "public", true, t4}, {bob, "", false, t3}}, "PRIV made again public, ada's answer as of t4"},
	} {
		if err := st.KeepProjectStanding(ctx, step.person, "PRIV", step.visibility, step.visible, step.asked); err != nil {
			t.Fatalf("KeepProjectStanding for %s: %v", step.write, err)
		}
		for _, want := range step.want {
			vis, visible, at, found, err := st.KeptProjectStanding(ctx, want.person, "PRIV")
			if err != nil || !found || vis != want.visibility || visible != want.visible || !at.Equal(want.at) {
				t.Fatalf("KeptProjectStanding(%s, PRIV) after %s = %q visible %t as of %v (found %t, %v); want %q visible %t as of %v",
					want.person, step.write, vis, visible, at, found, err, want.visibility, want.visible, want.at)
			}
		}
	}
}
