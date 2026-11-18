package workflow

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// In the plan `implementer tester:a reviewer:a` the tester's and the reviewer's steps are one agent,
// key a, named `tester`, so while step 3 runs the reviewer's phases row names the claim
// `…-tester`. What the engine tells the reviewer reaches that claim: the stuck message of a round
// GitHub will not merge from is a role message to it, and a role message the engine addresses to
// the reviewer is addressed to it. A reviewer's token derived from the role named `…-reviewer`, a
// claim that does not exist, so the message reached no session.
func TestAReviewStuckNoticeNamesTheReviewersAgent(t *testing.T) {
	ctx := context.Background()
	pool := migratedPool(t)
	engine := returnEngine()
	root := stepRoot(t, pool, phase.Reviewing)
	keyed := claim.Token(token(t, root.Key, claim.RoleTester))
	reviewer := record.PhaseRow{Issue: root.Key, Role: claim.RoleReviewer, Claim: keyed, StepTask: "a", StepPlace: 3, Entry: 2,
		Decision: &record.ReviewDecision{State: "approved", Head: "head-42"}}
	seedPhase(t, pool, reviewer)

	applyInTx(t, pool, func(ctx context.Context, tx pgx.Tx) error {
		addressed, err := engine.addressRoles(ctx, tx, root.Project, []roleRecipient{{issue: root.Key, role: claim.RoleReviewer, row: root.Key}}, nil)
		if err != nil {
			return err
		}
		if len(addressed) != 1 || addressed[0].token != keyed {
			t.Errorf("a role message to the reviewer is addressed to %+v, want %s", addressed, keyed)
		}
		return engine.tellStuckMergeState(ctx, tx, root, reviewer, round{outcome: roundStuck, cause: stuckMergeState, head: "head-42",
			state: "blocked", reason: "GitHub answers blocked for head head-42"}, "the reviewer")
	})

	var told string
	if err := pool.QueryRow(ctx, `select payload->>'role' from outbox where issue = $1 and kind = 'role_message'`, root.Key).Scan(&told); err != nil {
		t.Fatalf("read the stuck round's role message: %v", err)
	}
	if told != string(keyed) {
		t.Fatalf("the stuck round's role message went to %s, want the reviewer's agent %s", told, keyed)
	}
}
