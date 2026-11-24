package projection

// Acceptance probe for dispatch://INFRA-5599 (scratch, not for merge): is the seed's Stranded list,
// which the populated state serves as `strandedClaims` and reads each issue's `why` from, the list
// the daemon's rule (workflow.Stranded, which internal/daemon's source.State serves) writes for the
// same record?

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/workflow"
)

// acceptStrandedStore is the seed's store, answering RoleRuns with no queued start and no claim
// row for any role: the seed queues no outbox start for any claim.
type acceptStrandedStore struct{ projectionStore }

func (acceptStrandedStore) RoleRuns(context.Context, pgx.Tx, []record.RoleRunKey) (map[claim.Token]record.RoleRun, error) {
	return map[claim.Token]record.RoleRun{}, nil
}

func TestAcceptTheSeedsStrandedListIsWhatTheRuleWrites(t *testing.T) {
	seed := populatedSeed(t)
	store, ok := seed.inputs.Store.(projectionStore)
	if !ok {
		t.Fatalf("the seed's store is a %T, not a projectionStore", seed.inputs.Store)
	}
	written, err := workflow.Stranded(context.Background(), nil, acceptStrandedStore{store}, seed.inputs.Project,
		seed.inputs.Steps, seed.inputs.Claims, nil, seed.inputs.Now)
	if err != nil {
		t.Fatalf("workflow.Stranded: %v", err)
	}
	rule := map[claim.Token]bool{}
	for _, c := range written {
		rule[c.Token] = true
		t.Logf("the rule writes %s %s at %s", c.Issue, c.Role, c.State)
	}
	if len(written) == 0 {
		t.Logf("the rule writes no stranded claim for the seed")
	}
	seeded := map[claim.Token]bool{}
	for _, c := range seed.inputs.Stranded {
		seeded[c.Token] = true
		if !rule[c.Token] {
			t.Errorf("the seed lists %s %s at %s as stranded, and workflow.Stranded does not write it for the seed's record", c.Issue, c.Role, c.State)
		}
	}
	for _, c := range written {
		if !seeded[c.Token] {
			t.Errorf("workflow.Stranded writes %s %s at %s for the seed's record, and the seed does not list it", c.Issue, c.Role, c.State)
		}
	}
}
