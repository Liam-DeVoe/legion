package store

import (
	"context"
	"fmt"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/plan"
)

// ClaimAgentFromSteps is the agent the steps of issue give the claim token, in the identity a
// start names an agent by (plan.Agent.ID), "" where they give none: the agent that ran on the claim
// last, where its latest kept start (step_starts) names a place whose plan row still holds the
// claim's name, else the agent of the latest plan row of that name. It is the rule of the database
// function claim_agent_from_steps, which migration 20261122120917 defines and fills each claim of
// the release before by, so the outbox's start of a claim that records no agent and has run nothing,
// from a row that names no agent, records the agent that migration would have recorded. It is one
// read of q, a pool, a connection or a transaction (queryRower, as record.RowQuerier is): through
// a pool it takes no transaction.
func ClaimAgentFromSteps(ctx context.Context, q queryRower, issue string, token claim.Token) (plan.AgentID, error) {
	var agent string
	if err := q.QueryRow(ctx, `select coalesce(claim_agent_from_steps($1, $2), '')`, issue, string(token)).Scan(&agent); err != nil {
		return "", fmt.Errorf("read the agent the steps of %s give claim %s: %w", issue, token, err)
	}
	return plan.AgentID(agent), nil
}
