package record

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
)

// A clear of a generation is handed the rule it asks of each decision kept on the issues it clears
// (KeptStands), because it deletes the gates that rule reads: a decision a later approval
// superseded, asked after its gate is gone, would read as the person's word again. So the rule is
// required, and a clear handed none is refused before it writes anything, whichever of the two
// clears it is. It answers no emptied issue, so its caller queues no publication of the empty set
// of gates, and a caller that commits its transaction after the refusal finds the issue as it was:
// its handoff, its rounds and its entry's task, and its gate.
func TestAClearHandedNoRuleForTheKeptDecisionsIsRefusedAndClearsNothing(t *testing.T) {
	ctx := context.Background()
	records := NewStore()
	const task = "Continue the change. Issue: LEGION-288. Phase: implementing."
	for _, tc := range []struct {
		name  string
		clear func(tx pgx.Tx, issue Issue) ([]string, error)
	}{
		{name: "the clear of one issue", clear: func(tx pgx.Tx, issue Issue) ([]string, error) {
			return records.ClearGeneration(ctx, tx, issue, nil)
		}},
		{name: "the clear of a tree", clear: func(tx pgx.Tx, issue Issue) ([]string, error) {
			return records.ClearTreeGeneration(ctx, tx, issue.Tree, nil)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := migratedStore(t)
			issue := issueFixture("LEGION-288")
			approved := 2
			inTx(t, st, func(tx pgx.Tx) {
				must(t, records.PutIssue(ctx, tx, issue))
				must(t, records.PutPhase(ctx, tx, PhaseRow{Issue: issue.Key, Role: claim.RoleImplementer, Claim: "implementer",
					HandoffCommit: "head", Rounds: 2, Entry: 3, EntryTask: task}))
				must(t, records.PutGate(ctx, tx, DesignGate{Issue: issue.Key, ArtifactID: "artifact-288", LatestVersion: 2, ApprovedVersion: &approved}))
			})
			inTx(t, st, func(tx pgx.Tx) {
				emptied, err := tc.clear(tx, issue)
				if err == nil {
					t.Fatalf("%s handed no rule for the kept decisions answered nil, want it refused: the clear deletes the gates "+
						"the rule reads, so a clear that skips it leaves a superseded decision to read as the person's word", tc.name)
				}
				if len(emptied) != 0 {
					t.Fatalf("%s was refused and answers %v emptied, want none: its caller would publish an empty set of gates the issue still holds",
						tc.name, emptied)
				}
			})
			inTx(t, st, func(tx pgx.Tx) {
				rows, err := records.Phases(ctx, tx, issue.Key)
				must(t, err)
				if len(rows) != 1 || rows[0].HandoffCommit != "head" || rows[0].Rounds != 2 || rows[0].EntryTask != task {
					t.Fatalf("phases after %s was refused = %#v, want the implementer's row as it was put: handoff head, two rounds, its entry's task",
						tc.name, rows)
				}
				gates, err := records.GatesOf(ctx, tx, issue.Key)
				must(t, err)
				if len(gates) != 1 {
					t.Fatalf("gates after %s was refused = %#v, want the one gate put", tc.name, gates)
				}
			})
		})
	}
}
