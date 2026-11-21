package workflow

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// The engine's move into held keeps when the hold began (record.Hold.Since), the time the slots
// reading dates a held holder's `stopped` by: the engine's own clock at the failure that held the
// phase. A second report of the same dead claim finds the issue held already and writes nothing,
// so the hold keeps the moment it began. A hold written by hand, as an earlier release's daemon
// wrote every hold, carries no time.
func TestTheMoveIntoHeldKeepsWhenTheHoldBegan(t *testing.T) {
	pool := migratedPool(t)
	ctx := context.Background()
	seedIssue(t, pool, record.Issue{Key: "LEGION-208", Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets",
		Tree: "LEGION-208", Project: "LEGION", Title: "root", Phase: phase.Implementing, Generation: 1, Status: "in_progress", Rank: "U"})
	seedIssue(t, pool, record.Issue{Key: "LEGION-209", Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets",
		Tree: "LEGION-209", Project: "LEGION", Title: "held by hand", Phase: phase.Held, Hold: &record.Hold{From: phase.Implementing},
		Generation: 1, Status: "in_progress", Rank: "V"})
	began := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	hold := func(eventID string) {
		t.Helper()
		if _, err := intake.ApplyFact(ctx, pool, "supervise", eventID,
			intake.ClaimFailed{Issue: "LEGION-208", Role: claim.RoleImplementer, Reason: budgetRanOut},
			testEngine(config.DesignGateRootIssues, nil), admissionStub{}); err != nil {
			t.Fatalf("ApplyFact the implementer's failed claim: %v", err)
		}
	}
	read := func(key string) *record.Issue {
		t.Helper()
		var issue *record.Issue
		if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			var err error
			issue, err = record.NewStore().Issue(ctx, tx, key)
			return err
		}); err != nil {
			t.Fatalf("read %s: %v", key, err)
		}
		return issue
	}

	hold("implementer-failed")
	held := read("LEGION-208")
	if held.Phase != phase.Held || held.Hold == nil || held.Hold.Since == nil || !held.Hold.Since.Equal(began) {
		t.Fatalf("the held issue reads %s with hold %+v, want held from %s with its hold begun at the engine's %s",
			held.Phase, held.Hold, phase.Implementing, began.Format(time.RFC3339))
	}
	hold("implementer-failed-again")
	if again := read("LEGION-208"); again.Hold == nil || again.Hold.Since == nil || !again.Hold.Since.Equal(began) {
		t.Fatalf("a second report of the dead claim left the hold %+v, want the moment it began kept", again.Hold)
	}
	if byHand := read("LEGION-209"); byHand.Hold == nil || byHand.Hold.Since != nil {
		t.Fatalf("the hold written by hand reads %+v, want it with no time", byHand.Hold)
	}
}
