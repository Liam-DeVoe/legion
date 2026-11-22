package workflow

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// The two starts that send a step's work again with no move of the task name the agent of the step
// where the issue stands (record.SuperviseRequest.Agent), as the entry that raised the number named
// it: the held phase's retry, here the operator's restart of the phase (restart), and the send of
// an owed entry (sendOwedEntry). The claim they reach starts that agent in a session of its own
// where its session is another agent's. In `implementer reviewer implementer` the task stands at
// step 3, whose agent is the agent of step 3 of task a, `step:3:a`, on the claim implementer-2. The
// fault: a start that names no agent, which takes whatever session the claim holds.
func TestARetryAndAnOwedEntryNameTheAgentOfTheStepWhereTheIssueStands(t *testing.T) {
	for _, tc := range []struct {
		name string
		send func(t *testing.T, pool *pgxpool.Pool, engine *Engine, key string, row record.PhaseRow)
		// resend is whether the start hands the entry its row names as a resend (restart).
		resend bool
	}{
		{name: "the retry of the phase", resend: true, send: func(t *testing.T, pool *pgxpool.Pool, engine *Engine, key string, _ record.PhaseRow) {
			applyRefusingNothing(t, pool, engine, intake.RestartPhase{Issue: key, Role: claim.RoleImplementer})
		}},
		{name: "the send of an owed entry", send: func(t *testing.T, pool *pgxpool.Pool, engine *Engine, key string, row record.PhaseRow) {
			issue := issueOf(t, pool, engine, key)
			applyInTx(t, pool, engine, func(ctx context.Context, tx pgx.Tx) error {
				return engine.SendOwedEntry(ctx, tx, issue, claim.RoleImplementer, row)
			})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := migratedPool(t)
			engine := textEngine()
			tickingClock(engine)
			root := stepRoot(t, pool, phase.Admitted)
			seedStepsPlan(t, pool, root.Key, root.Generation, "implementer", "reviewer", "implementer")
			move(t, pool, engine, root.Key, TriggerGateOpened, record.TaskPlace{}, record.PhaseRow{})
			completesAs(t, pool, engine, claim.RoleImplementer, agentToken(t, "implementer"), "implement", 1)
			completesAs(t, pool, engine, claim.RoleReviewer, agentToken(t, "reviewer"), "review", 1)
			move(t, pool, engine, root.Key, TriggerReviewApproved, record.TaskPlace{}, record.PhaseRow{})
			assertWalksAt(t, pool, engine, root.Key, phase.Implementing, stepPlace(3))
			row := roleRowNow(t, pool, engine, root.Key, claim.RoleImplementer)
			if row.StepPlace != 3 || row.Claim != agentToken(t, "implementer-2") {
				t.Fatalf("the implementer's row now is at place %d on the claim %q, want step 3's on %q", row.StepPlace, row.Claim, agentToken(t, "implementer-2"))
			}
			outboxRan(t, pool)

			tc.send(t, pool, engine, root.Key, row)

			starts := roleStartsOf(t, pool, root.Key, claim.RoleImplementer)
			if len(starts) != 1 {
				t.Fatalf("the implementer's starts = %+v, want the one the send wrote", starts)
			}
			if start := starts[0]; start.Agent != "step:3:a" || start.Entry != row.Entry || start.ResendEntry != tc.resend {
				t.Fatalf("the start = %+v, want it to name the agent step:3:a at entry %d (resend %t)", start, row.Entry, tc.resend)
			}
		})
	}
}
