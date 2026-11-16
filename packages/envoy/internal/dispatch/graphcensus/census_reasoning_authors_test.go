package graphcensus

import (
	"context"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// The census holds the author of each reasoning node to a node of the store (D12a of
// dispatch://INFRA-1950's plan): the person and session row sides take, beside the authors the
// projected tables name, the author graphstore.AuthorOf names of the made_by of each lens, finding,
// risk and constraint that holds an asserted authored_by tuple (reasoningAuthored), so an author
// who wrote no comment is no difference, and the session a body named on the shared bearer is on
// neither side.
//
// Each node is written as the reasoning write (api's writeReasoningNodes) writes it: the node, its
// part_of, and, where the credential proved an author, the author's node and facts through
// graphstore.AuthorFacts in the same graphstore.Write. alice writes a pre-mortem on a cookie; a
// session a broker-issued token proved carol's writes a risk under it, which makes the session's
// node and carol's; dave's personal token writes an author-only finding of it, made by the token
// itself; and the shared bearer writes a constraint whose made_by names the session its body
// claimed, with no authored_by. No comment, delivery or ask names any of them, so before this
// census read the authors of these kinds, each of the four author nodes was a node with no row.
// The census reads zero differences, three people and one session on the row side, and the
// claimed session nowhere.
func TestTheCensusHoldsEachReasoningNodesAuthorToANode(t *testing.T) {
	ctx := context.Background()
	database := storetest.Open(t)
	at := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	owner := func(login string) *string { return &login }

	const (
		lens       = "00000000-0000-4000-8000-0000000001a1"
		risk       = "00000000-0000-4000-8000-0000000001a2"
		constraint = "00000000-0000-4000-8000-0000000001a3"
		proved     = "session-proved-carols"
		claimed    = "session-claimed-on-the-bearer"
	)
	finding := graphstore.KindLens + "/" + lens + "#b-first"
	for _, written := range []struct {
		kind, id, visibility string
		madeBy               model.Actor
		proves               bool
		tuples               []graphstore.Tuple
	}{
		{kind: graphstore.KindLens, id: lens, visibility: graphstore.VisibilityProject,
			madeBy: model.Actor{Kind: "user", ID: "alice"}, proves: true,
			tuples: []graphstore.Tuple{
				{Kind: graphstore.TuplePartOf, ToKind: graphstore.KindIssue, ToID: "CORE-1"},
				{Kind: graphstore.TupleHasKind, ToKind: graphstore.KindLensKind, ToID: graphstore.LensKindPreMortem},
			}},
		{kind: graphstore.KindRisk, id: risk, visibility: graphstore.VisibilityProject,
			madeBy: model.Actor{Kind: "session", ID: proved, Owner: owner("carol"), Act: &model.ActorAct{Session: proved}}, proves: true,
			tuples: []graphstore.Tuple{{Kind: graphstore.TuplePartOf, ToKind: graphstore.KindLens, ToID: lens}}},
		{kind: refs.BlockNodeKind, id: finding, visibility: graphstore.VisibilityAuthor,
			madeBy: model.Actor{Kind: "session", Owner: owner("dave")}, proves: true,
			tuples: []graphstore.Tuple{{Kind: graphstore.TuplePartOf, ToKind: graphstore.KindLens, ToID: lens}}},
		{kind: graphstore.KindConstraint, id: constraint, visibility: graphstore.VisibilityProject,
			madeBy: model.Actor{Kind: "session", ID: claimed}, proves: false,
			tuples: []graphstore.Tuple{{Kind: graphstore.TuplePartOf, ToKind: graphstore.KindIssue, ToID: "CORE-1"}}},
	} {
		nodes := []graphstore.Node{{
			Kind: written.kind, ID: written.id, Supertag: written.kind,
			Visibility: written.visibility, State: graphstore.StateLive, Project: "CORE",
			MadeAt: at, MadeBy: written.madeBy, SourceEvent: graphstore.NoEvent,
		}}
		tuples := make([]graphstore.Tuple, 0, len(written.tuples))
		for _, tuple := range written.tuples {
			tuple.FromKind, tuple.FromID = written.kind, written.id
			tuple.At, tuple.Who, tuple.SourceEvent, tuple.Asserted = at, written.madeBy, graphstore.NoEvent, true
			tuples = append(tuples, tuple)
		}
		if written.proves {
			authors, facts, named := graphstore.AuthorFacts(written.kind, written.id, written.madeBy, at, graphstore.NoEvent)
			if !named {
				t.Fatalf("%s/%s's writer %+v names nobody", written.kind, written.id, written.madeBy)
			}
			for _, author := range authors {
				author.Restates = true
				nodes = append(nodes, author)
			}
			tuples = append(tuples, facts...)
		}
		tx, err := database.Pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin the write of %s/%s: %v", written.kind, written.id, err)
		}
		if err := graphstore.Write(ctx, tx, nodes, tuples); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("write %s/%s: %v", written.kind, written.id, err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit the write of %s/%s: %v", written.kind, written.id, err)
		}
	}
	if _, held, err := graphstore.ReadNode(ctx, database.Pool, graphstore.KindSession, claimed); err != nil || held {
		t.Fatalf("the shared bearer's write made a node of the session its body claimed (held %t, %v)", held, err)
	}

	report := storeCensusOf(t, database)
	for _, kind := range report.Kinds {
		t.Logf("kind=%s rows=%d nodes=%d row differences=%d node differences=%d",
			kind.Kind, kind.Rows, kind.Nodes, kind.RowDifferences, kind.NodeDifferences)
		for _, difference := range append(append([]Difference{}, kind.ReportedRows...), kind.ReportedNodes...) {
			t.Errorf("the census reads a difference of kind %s at %s: %s", kind.Kind, difference.At, difference.Cause)
		}
	}
	if report.Differences() != 0 {
		t.Errorf("the census read %d difference(s), want none", report.Differences())
	}
	if people := censusKind(t, report, graphstore.KindPerson); people.Rows != 3 || people.Nodes != 3 {
		t.Errorf("the census checked %d people against %d person nodes, want alice, carol and dave on both sides",
			people.Rows, people.Nodes)
	}
	if sessions := censusKind(t, report, graphstore.KindSession); sessions.Rows != 1 || sessions.Nodes != 1 {
		t.Errorf("the census checked %d sessions against %d session nodes, want the proved session alone on both sides",
			sessions.Rows, sessions.Nodes)
	}
}
