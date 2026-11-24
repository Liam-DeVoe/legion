package daemon

import (
	"context"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// An ask the owner was told lives in the session it was told in: a fresh session holds nothing of
// it. On `implementer:owner reviewer` at the reviewer's step, the merge forward's ask reaches the
// owner in its session, and its delivery records that session. The owner's claim then comes back
// at its next launch: in a fresh session, the catch-up of that launch names the open ask, with its
// head, and no second message of the ask is queued; in the session it was told in, resumed, the
// catch-up does not name it, since that session still holds the ask.
func TestAnOwnersFreshSessionIsToldTheMergeForwardStillAskedOfIt(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		session string
		names   bool
	}{
		{name: "a fresh session", session: "ses_fresh", names: true},
		{name: "control: the session it was told in, resumed", session: ownerHoldSession},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			r := newOwnerHoldRig(t, phase.Reviewing, record.TaskPlace{Kind: record.PlaceStep, Position: 2}, "implementer:owner", "reviewer")
			r.started(t, "step", 1, true)
			r.relaunched(t, ownerHoldLaunch, ownerHoldSession)
			r.askedOf(t, r.owner, phase.Reviewing)
			if err := r.told(t, mergeForwardAsk(r.owner, phase.Reviewing)); err != nil {
				t.Fatalf("the ask to the owner in its session = %v", err)
			}
			if got := r.published(); len(got) != 1 {
				t.Fatalf("the ask published %v, want one: this case proves nothing", got)
			}

			r.relaunched(t, ownerHoldLaunch+1, tc.session)
			r.ready(t, "ready-2", ownerHoldLaunch+1, false)
			var catchUps []record.Notice
			for _, notice := range queuedNoticesOf(t, r.pool, "LEGION-1") {
				if notice.Kind == "catch-up" {
					catchUps = append(catchUps, notice)
				}
			}
			if len(catchUps) != 1 {
				t.Fatalf("the launch queued the catch-ups %+v, want one", catchUps)
			}
			reason := catchUps[0].Reason
			if names := strings.Contains(reason, "a merge forward the daemon asked of you") && strings.Contains(reason, mergeForwardHead); names != tc.names {
				t.Fatalf("the catch-up of the launch in %s says %q; want it to name the open ask at head %s: %t", tc.session, reason, mergeForwardHead, tc.names)
			}
			var asks int
			if err := r.pool.QueryRow(ctx, "select count(*) from outbox where kind = $1", string(record.OutboxKindMergeForward)).Scan(&asks); err != nil || asks != 0 {
				t.Fatalf("the launch queued %d merge-forward messages (%v), want none: the catch-up is the one telling", asks, err)
			}
		})
	}
}
