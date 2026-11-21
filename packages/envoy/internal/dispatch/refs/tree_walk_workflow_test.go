package refs_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/docs"
	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// TestTheWalkLeavesOutTheRecordsOfTheWorkflow: a run of a step, a plan and a handoff, each part_of
// an issue, record how the issue's work ran and are no part of its discussion, so the walk of the
// issue's tree (docs.WalkIssueTree) takes none of them and none of the blocks part_of each of
// them, while it takes a block part_of the issue itself. The nodes are seeded as tuples alone,
// through graphstore.Write, as a write that states a relation and no word of any node would.
func TestTheWalkLeavesOutTheRecordsOfTheWorkflow(t *testing.T) {
	database := storetest.Open(t)
	ctx := context.Background()
	const issue = "WALK-1"
	at := time.Now().UTC()
	partOf := func(child, parent refs.Target) graphstore.Tuple {
		return graphstore.Tuple{
			FromKind: child.Kind, FromID: child.ID, Kind: graphstore.TuplePartOf, ToKind: parent.Kind, ToID: parent.ID,
			At: at, Who: model.Actor{Kind: "user", ID: "alice"}, SourceEvent: graphstore.NoEvent, Asserted: true,
		}
	}
	root := refs.Target{Kind: graphstore.KindIssue, ID: issue}
	kept := refs.Target{Kind: refs.BlockNodeKind, ID: issue + "/spec#kept"}
	tuples := []graphstore.Tuple{partOf(kept, root)}
	var left []refs.Target
	for _, kind := range []string{"step", "plan", "handoff"} {
		record := refs.Target{Kind: kind, ID: kind + "-1"}
		under := refs.Target{Kind: refs.BlockNodeKind, ID: issue + "/spec#under-" + kind}
		tuples = append(tuples, partOf(record, root), partOf(under, record))
		left = append(left, record, under)
	}
	if err := pgx.BeginFunc(ctx, database.Pool, func(tx pgx.Tx) error { return graphstore.Write(ctx, tx, nil, tuples) }); err != nil {
		t.Fatalf("seed the part_of tuples of %s: %v", issue, err)
	}

	tree, err := docs.WalkIssueTree(ctx, database.Pool, issue, refs.TreeCut{})
	if err != nil {
		t.Fatalf("walk the tree of %s: %v", issue, err)
	}
	walked := map[refs.Target]bool{}
	for _, node := range tree.Nodes {
		walked[refs.Target{Kind: node.Kind, ID: node.ID}] = true
	}
	if !walked[kept] {
		t.Fatalf("the walk of %s takes %d nodes and not %s:%s, the block part_of the issue", issue, len(tree.Nodes), kept.Kind, kept.ID)
	}
	for _, node := range left {
		if walked[node] {
			t.Errorf("the walk of %s takes %s:%s, a record of the workflow or a node under one", issue, node.Kind, node.ID)
		}
	}
}
