package grapherase

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/graphcensus"
	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// seedRun records one run of the command bar asked by author, with two frames, and gives it its
// interaction node and their frame values as this build's frame path does for a run with no node
// (graphcensus.WriteRun). It answers the run's id.
func seedRun(t *testing.T, database *store.Store, author model.Actor) string {
	t.Helper()
	ctx := context.Background()
	recorded, err := json.Marshal(author.Recorded())
	if err != nil {
		t.Fatalf("encode the run's author: %v", err)
	}
	var run string
	if err := database.Pool.QueryRow(ctx, `
		insert into going_requests (author, project, words, ends_at)
		values ($1::jsonb, 'CORE', 'What does the spec say?', now() + interval '30 seconds')
		returning id::text
	`, recorded).Scan(&run); err != nil {
		t.Fatalf("record the run: %v", err)
	}
	if _, err := database.Pool.Exec(ctx, `
		insert into going_events (request_id, sequence, name, data)
		values ($1::uuid, 1, 'text', to_jsonb('It says '::text)), ($1::uuid, 2, 'end', '{}'::jsonb)
	`, run); err != nil {
		t.Fatalf("append the run's frames: %v", err)
	}
	tx, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the run's node: %v", err)
	}
	defer tx.Rollback(ctx)
	if held, err := graphcensus.WriteRun(ctx, tx, run); err != nil || !held {
		t.Fatalf("write the run's node: held %t, %v", held, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit the run's node: %v", err)
	}
	return run
}

// markChangedByRun writes the node kind:id again, as the store holds it, on a context carrying
// run, as a write made for the run writes a node it changed (graphstore.WithRun, Node.Changed): the
// store appends one changed_by_run tuple from the node to the run's interaction.
func markChangedByRun(t *testing.T, database *store.Store, kind, id, run string) {
	t.Helper()
	ctx := context.Background()
	tx, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the write for run %s: %v", run, err)
	}
	defer tx.Rollback(ctx)
	node, held, err := graphstore.ReadNode(ctx, tx, kind, id)
	if err != nil || !held {
		t.Fatalf("read %s:%s: held %t, %v", kind, id, held, err)
	}
	node.Changed, node.Restates = true, true
	if err := graphstore.Write(graphstore.WithRun(ctx, run), tx, []graphstore.Node{node}, nil); err != nil {
		t.Fatalf("write %s:%s for run %s: %v", kind, id, run, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit the write for run %s: %v", run, err)
	}
}

// runRows counts what the store and the run's two tables hold of run: its going_requests row, its
// going_events rows, its interaction node, and every tuple out of it or into it.
func runRows(t *testing.T, database *store.Store, run string) (requests, events, nodes, tuples int64) {
	t.Helper()
	if err := database.Pool.QueryRow(context.Background(), `
		select (select count(*) from going_requests where id = $1::uuid),
		       (select count(*) from going_events where request_id = $1::uuid),
		       (select count(*) from graph_nodes where kind = $2 and id = $3),
		       (select count(*) from graph_tuples where (from_kind = $2 and from_id = $3) or (to_kind = $2 and to_id = $3))
	`, run, graphstore.KindInteraction, run).Scan(&requests, &events, &nodes, &tuples); err != nil {
		t.Fatalf("count what the store holds of run %s: %v", run, err)
	}
	return requests, events, nodes, tuples
}

// TestTheEraseOfARunLeavesNoRowAndNoBackfillWritesItBack is `envoy-dispatch erase --node
// interaction:<uuid>`: a run's two tables hold no node_kind and node_address, so the erase holds
// an entry of its own for the run (runTables), and deletes its going_events rows and its
// going_requests row in its own transaction, beside the node and its tuples, or a backfill would
// write the run back from its rows. A comment changed by a write made for the run carries a
// changed_by_run tuple into it; no row of the comment states that kind
// (graphcensus.DigestedTuple), so it is no blocker and the erase deletes it with the node.
// Afterwards no row, no node and no tuple of the run is left, a backfill writes none of it back,
// and the census reads zero differences.
func TestTheEraseOfARunLeavesNoRowAndNoBackfillWritesItBack(t *testing.T) {
	database := storetest.Open(t)
	ctx := context.Background()
	seeded := seedAgreeingStore(t, database)
	run := seedRun(t, database, model.Actor{Kind: "user", ID: "alice"})
	markChangedByRun(t, database, commentNodeKind, seeded.comment, run)
	if changed, err := graphstore.CurrentIn(ctx, database.Pool, graphstore.KindInteraction, run,
		[]string{graphstore.TupleChangedByRun}); err != nil || len(changed) != 1 || changed[0].FromID != seeded.comment {
		t.Fatalf("the run is pointed at by %+v (%v), want the comment's one changed_by_run", changed, err)
	}
	if requests, events, nodes, _ := runRows(t, database, run); requests != 1 || events != 2 || nodes != 1 {
		t.Fatalf("the run holds %d going_requests rows, %d going_events rows and %d nodes before the erase, want 1, 2 and 1",
			requests, events, nodes)
	}

	listing := dryRun(t, database, graphstore.KindInteraction, run)
	t.Logf("the listing of the run's erase:\n%s", strings.Join(listing.Listing, "\n"))
	if len(listing.Blockers) != 0 {
		t.Fatalf("the erase of the run is blocked by %v, want no blocker: a changed_by_run is no row's to state", listing.Blockers)
	}
	for _, line := range [][]string{
		{"delete going_requests (id)=(" + run + ")"},
		{"delete going_events (request_id, sequence)=(" + run + ",1)"},
		{"delete going_events (request_id, sequence)=(" + run + ",2)"},
		{"comment:" + seeded.comment + " " + graphstore.TupleChangedByRun + " interaction:" + run},
	} {
		if !listed(listing, line...) {
			t.Errorf("the listing names no line holding %q", line)
		}
	}

	erasure, err := confirmedErase(t, database, graphstore.KindInteraction, run, time.Millisecond)
	if err != nil {
		t.Fatalf("erase the run: %v", err)
	}
	if !erasure.Applied {
		t.Fatalf("the confirmed erase of the run removed nothing: blockers %v, mismatch %t", erasure.Blockers, erasure.Mismatch())
	}
	if requests, events, nodes, tuples := runRows(t, database, run); requests+events+nodes+tuples != 0 {
		t.Fatalf("after the erase the run holds %d going_requests rows, %d going_events rows, %d nodes and %d tuples, want none",
			requests, events, nodes, tuples)
	}
	if _, held, err := graphstore.ReadNode(ctx, database.Pool, commentNodeKind, seeded.comment); err != nil || !held {
		t.Fatalf("the comment changed by the run reads held %t (%v) after the run's erase, want it standing", held, err)
	}

	if _, err := graphcensus.Backfill(ctx, database, io.Discard); err != nil {
		t.Fatalf("backfill after the erase: %v", err)
	}
	if requests, events, nodes, tuples := runRows(t, database, run); requests+events+nodes+tuples != 0 {
		t.Fatalf("the backfill after the erase wrote back %d going_requests rows, %d going_events rows, %d nodes and %d tuples of the run, want none",
			requests, events, nodes, tuples)
	}
	census, err := graphcensus.StoreCensus(ctx, database.Pool.Config().ConnString(), false)
	if err != nil {
		t.Fatalf("take the store census: %v", err)
	}
	for _, kind := range census.Kinds {
		for _, difference := range append(append([]graphcensus.Difference{}, kind.ReportedRows...), kind.ReportedNodes...) {
			t.Errorf("the census after the erase reads a difference of kind %s at %s: %s", kind.Kind, difference.At, difference.Cause)
		}
	}
}

// TestTheEraseOfANodeIsRefusedWhileANodeOfAStandingKindStandsUnderIt: a lens, a finding of one, a
// risk and a constraint stand where their current part_of says, with no projection row behind
// them, and every read finds the issue each stands under by walking that tuple up
// (refs.StandingIssueSQL), so an erase of the node one stands under would leave it standing under
// nothing. A finding and a risk stand under a pre-mortem lens, a second lens is invoked on the
// finding, whose part_of names it lens_block, the kind the graph addresses it by, and a third on
// the risk. The erase of the lens names the finding and the risk, the erase of the finding the
// lens on it, and the erase of the risk the lens on it, each with that reason, and removes
// nothing. The finding is a block node whose id names the lens: its listing takes no document
// from its id and lists the tuple into it under its lens_block spelling. Once the lens on the
// finding is moved to stand on the issue, the retraction and the assertion of its part_of into
// the finding stand in the log and block nothing; and once the lens on the risk is erased, the
// finding, the risk and the lens are erased in turn, and no tuple into the finding is left under
// either spelling.
func TestTheEraseOfANodeIsRefusedWhileANodeOfAStandingKindStandsUnderIt(t *testing.T) {
	database := storetest.Open(t)
	ctx := context.Background()
	seedAgreeingStore(t, database)
	const (
		lens   = "00000000-0000-4000-8000-0000000003a1"
		risk   = "00000000-0000-4000-8000-0000000003a2"
		onIt   = "00000000-0000-4000-8000-0000000003a3"
		onRisk = "00000000-0000-4000-8000-0000000003a4"
		block  = "first"
	)
	finding := graphstore.KindLens + "/" + lens + "#" + block
	alice := model.Actor{Kind: "user", ID: "alice"}
	at := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	node := func(kind, id, supertag string) graphstore.Node {
		return graphstore.Node{
			Kind: kind, ID: id, Supertag: supertag,
			Visibility: graphstore.VisibilityProject, State: graphstore.StateLive, Project: "CORE",
			MadeAt: at, MadeBy: alice, SourceEvent: graphstore.NoEvent,
		}
	}
	partOf := func(fromKind, fromID, toKind, toID string) graphstore.Tuple {
		return graphstore.Tuple{
			FromKind: fromKind, FromID: fromID, Kind: graphstore.TuplePartOf, ToKind: toKind, ToID: toID,
			At: at, Who: alice, SourceEvent: graphstore.NoEvent, Asserted: true,
		}
	}
	write := func(what string, nodes []graphstore.Node, tuples []graphstore.Tuple) {
		t.Helper()
		tx, err := database.Pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin %s: %v", what, err)
		}
		if err := graphstore.Write(ctx, tx, nodes, tuples); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("write %s: %v", what, err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit %s: %v", what, err)
		}
	}
	// The lens is written first, with its kind, since a risk stands only under a pre-mortem lens
	// and the store reads the lens's kind off the store when it holds the risk's part_of to it.
	write("the lens", []graphstore.Node{node(graphstore.KindLens, lens, graphstore.KindLens)}, []graphstore.Tuple{
		partOf(graphstore.KindLens, lens, graphstore.KindIssue, "CORE-1"),
		{
			FromKind: graphstore.KindLens, FromID: lens, Kind: graphstore.TupleHasKind,
			ToKind: graphstore.KindLensKind, ToID: graphstore.LensKindPreMortem,
			At: at, Who: alice, SourceEvent: graphstore.NoEvent, Asserted: true,
		},
	})
	write("its finding, its risk and a lens on each", []graphstore.Node{
		node(refs.BlockNodeKind, finding, refs.BlockSupertag),
		node(graphstore.KindLens, onIt, graphstore.KindLens),
		node(graphstore.KindLens, onRisk, graphstore.KindLens),
		node(graphstore.KindRisk, risk, graphstore.KindRisk),
	}, []graphstore.Tuple{
		partOf(refs.BlockNodeKind, finding, graphstore.KindLens, lens),
		partOf(graphstore.KindRisk, risk, graphstore.KindLens, lens),
		partOf(graphstore.KindLens, onIt, graphstore.KindLensBlock, finding),
		partOf(graphstore.KindLens, onRisk, graphstore.KindRisk, risk),
	})

	const standsHere = " states " + graphstore.TuplePartOf + " into this node, which is where it stands"
	for _, refused := range []struct {
		kind, id string
		under    []string
	}{
		{graphstore.KindLens, lens, []string{refs.BlockNodeKind + ":" + finding, graphstore.KindRisk + ":" + risk}},
		{refs.BlockNodeKind, finding, []string{graphstore.KindLens + ":" + onIt}},
		{graphstore.KindRisk, risk, []string{graphstore.KindLens + ":" + onRisk}},
	} {
		erasure, err := confirmedErase(t, database, refused.kind, refused.id, time.Millisecond)
		if err != nil {
			t.Fatalf("erase %s:%s: %v", refused.kind, refused.id, err)
		}
		t.Logf("the erase of %s:%s is blocked by %v", refused.kind, refused.id, erasure.Blockers)
		if erasure.Applied {
			t.Fatalf("the erase of %s:%s removed it while %v stood under it", refused.kind, refused.id, refused.under)
		}
		for _, under := range refused.under {
			named := false
			for _, blocker := range erasure.Blockers {
				named = named || strings.HasPrefix(blocker, under+standsHere)
			}
			if !named {
				t.Errorf("the erase of %s:%s names no blocker for %s standing under it among %v", refused.kind, refused.id, under, erasure.Blockers)
			}
		}
		if _, held, err := graphstore.ReadNode(ctx, database.Pool, refused.kind, refused.id); err != nil || !held {
			t.Fatalf("the refused erase left %s:%s held %t (%v), want it standing", refused.kind, refused.id, held, err)
		}
	}

	findingListing := dryRun(t, database, refs.BlockNodeKind, finding)
	if !listed(findingListing, "lens:"+onIt+" "+graphstore.TuplePartOf+" "+graphstore.KindLensBlock+":"+finding) {
		t.Errorf("the listing of the finding's erase names no tuple into it under its lens_block spelling:\n%s",
			strings.Join(findingListing.Listing, "\n"))
	}
	for _, line := range findingListing.Listing {
		if strings.Contains(line, "document_block_texts") || strings.Contains(line, "live documents whose history is replaced: 1") {
			t.Errorf("the listing of the finding's erase takes a document from its id: %s", line)
		}
	}

	// The lens on the finding moves to stand on the issue: its part_of into the finding is
	// retracted, and that history stays in the log, under the lens_block spelling, until the
	// finding's erase deletes it.
	moved := partOf(graphstore.KindLens, onIt, graphstore.KindLensBlock, finding)
	moved.Asserted = false
	write("the move of the lens on the finding", nil, []graphstore.Tuple{moved, partOf(graphstore.KindLens, onIt, graphstore.KindIssue, "CORE-1")})
	for _, erased := range []struct{ kind, id string }{
		{graphstore.KindLens, onRisk}, {refs.BlockNodeKind, finding}, {graphstore.KindRisk, risk}, {graphstore.KindLens, lens},
	} {
		erasure, err := confirmedErase(t, database, erased.kind, erased.id, time.Millisecond)
		if err != nil {
			t.Fatalf("erase %s:%s: %v", erased.kind, erased.id, err)
		}
		if !erasure.Applied {
			t.Fatalf("the erase of %s:%s removed nothing: blockers %v", erased.kind, erased.id, erasure.Blockers)
		}
		if _, held, err := graphstore.ReadNode(ctx, database.Pool, erased.kind, erased.id); err != nil || held {
			t.Fatalf("after its erase %s:%s reads held %t (%v), want it gone", erased.kind, erased.id, held, err)
		}
	}
	if _, held, err := graphstore.ReadNode(ctx, database.Pool, graphstore.KindLens, onIt); err != nil || !held {
		t.Errorf("the lens moved off the finding reads held %t (%v) after the finding's erase, want it standing on the issue", held, err)
	}
	var into int64
	if err := database.Pool.QueryRow(ctx, `
		select count(*) from graph_tuples where to_id = $1 and to_kind in ($2, $3)
	`, finding, refs.BlockNodeKind, graphstore.KindLensBlock).Scan(&into); err != nil {
		t.Fatalf("count the tuples into the erased finding: %v", err)
	}
	if into != 0 {
		t.Errorf("the erased finding leaves %d tuple(s) into it, want none under either spelling", into)
	}
}
