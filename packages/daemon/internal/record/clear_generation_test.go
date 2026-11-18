package record

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
)

// A clear of a generation is handed the forget it runs over the decisions kept on the issues it
// clears (KeptForget), because it deletes the gates that forget's rule reads: a decision a later
// approval superseded, asked after its gate is gone, would read as the person's word again. So the
// forget is required, and a clear handed none is refused before it writes anything, whichever of
// the two clears it is. It answers no emptied issue, so its caller queues no publication of the
// empty set of gates, and a caller that commits its transaction after the refusal finds the issue
// as it was: its handoff, its rounds and its entry's task, and its gate.
func TestAClearHandedNoForgetForTheKeptDecisionsIsRefusedAndClearsNothing(t *testing.T) {
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
					t.Fatalf("%s handed no forget for the kept decisions answered nil, want it refused: the clear deletes the gates "+
						"the forget's rule reads, so a clear that skips it leaves a superseded decision to read as the person's word", tc.name)
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

// A clear runs the forget it is handed once, over the decisions kept on the issues it clears,
// while those issues' gates still stand (Postgres.forgetKept): the clear of one issue hands over
// that issue's decisions, and the clear of a tree every decision kept on an issue of it. Neither
// hands over a decision kept on another tree, nor the tree's kept approval, which the forget
// deletes nothing of. The forget asks each decision of the gates of the issue it was made on, so
// a set handed over after the gates went would ask a gate that is gone.
func TestAClearHandsItsForgetTheDecisionsKeptOnTheIssuesItClears(t *testing.T) {
	ctx := context.Background()
	records := NewStore()
	at := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	const root, child, other = "LEGION-300", "LEGION-301", "LEGION-302"
	parent := root
	issues := []Issue{
		{Key: root, Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets", Tree: root, Project: "LEGION",
			Title: "Root", Phase: phase.Implementing, Generation: 2, Status: "in_progress", Rank: "U"},
		{Key: child, Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets", Tree: root, Project: "LEGION",
			Title: "Child", Parent: &parent, Phase: phase.Implementing, Generation: 2, Status: "in_progress", Rank: "V"},
		{Key: other, Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets", Tree: other, Project: "LEGION",
			Title: "Another tree", Phase: phase.Implementing, Generation: 2, Status: "in_progress", Rank: "W"},
	}
	kept := []ParkedDecision{
		{Issue: root, Ref: "dispatch://LEGION-300/spec", Decision: DecisionDesignChangesRequested, Actor: "user:alice", At: at, Seq: 8},
		{Issue: child, Ref: "dispatch://LEGION-301/spec", Decision: DecisionDesignChangesRequested, Actor: "user:bob", At: at.Add(time.Minute), Seq: 9},
		{Issue: other, Ref: "dispatch://LEGION-302/spec", Decision: DecisionDesignChangesRequested, Actor: "user:carol", At: at.Add(2 * time.Minute), Seq: 10},
	}
	for _, tc := range []struct {
		name  string
		clear func(tx pgx.Tx, forget KeptForget) ([]string, error)
		want  []string
	}{
		{name: "the clear of the child", clear: func(tx pgx.Tx, forget KeptForget) ([]string, error) {
			return records.ClearGeneration(ctx, tx, issues[1], forget)
		}, want: []string{kept[1].Ref}},
		{name: "the clear of the tree", clear: func(tx pgx.Tx, forget KeptForget) ([]string, error) {
			return records.ClearTreeGeneration(ctx, tx, root, forget)
		}, want: []string{kept[0].Ref, kept[1].Ref}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := migratedStore(t)
			approved := 2
			inTx(t, st, func(tx pgx.Tx) {
				for i, issue := range issues {
					must(t, records.PutIssue(ctx, tx, issue))
					must(t, records.PutGate(ctx, tx, DesignGate{Issue: issue.Key, ArtifactID: "artifact-" + issue.Key, LatestVersion: 3,
						ApprovedVersion: &approved}))
					must(t, records.AddParkedDecision(ctx, tx, kept[i]))
				}
				must(t, records.KeepReadmittedApproval(ctx, tx, root, KeptApproval{Step: DesignStepSpec, Approved: 2, Latest: 3}))
			})
			var runs int
			var tree string
			var handed KeptDecisions
			var ungated []string
			forget := func(ctx context.Context, tx pgx.Tx, owedTo string, owed KeptDecisions) error {
				runs++
				tree, handed = owedTo, owed
				for _, d := range owed.Decisions {
					gates, err := records.GatesOf(ctx, tx, d.Issue)
					if err != nil {
						return err
					}
					if len(gates) == 0 {
						ungated = append(ungated, d.Issue)
					}
				}
				return nil
			}
			inTx(t, st, func(tx pgx.Tx) {
				_, err := tc.clear(tx, forget)
				must(t, err)
			})
			if runs != 1 || tree != root {
				t.Fatalf("%s ran its forget %d times, last for tree %q, want once for %s, the tree the cleared issues stand in",
					tc.name, runs, tree, root)
			}
			var refs []string
			for _, d := range handed.Decisions {
				refs = append(refs, d.Ref)
			}
			slices.Sort(refs)
			if !slices.Equal(refs, tc.want) {
				t.Fatalf("%s handed its forget the decisions %v, want %v: the ones kept on the issues it clears, and no other",
					tc.name, refs, tc.want)
			}
			if handed.KeptApproval != (KeptApproval{}) {
				t.Fatalf("%s handed its forget the kept approval %#v, want none: the forget deletes no approval",
					tc.name, handed.KeptApproval)
			}
			if len(ungated) != 0 {
				t.Fatalf("%s ran its forget after the gates of %v went, want it run while they stand: the forget's rule reads them",
					tc.name, ungated)
			}
		})
	}
}
