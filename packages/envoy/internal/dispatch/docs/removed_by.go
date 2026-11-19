package docs

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
)

// dropper is who makes a write that may take an ask block out of its document: whether the write
// is a person's own (writtenByPerson), and so may take an answered ask's block out, and, for such
// a write, the person whose removal it records on each answered ask it takes out
// (graphstore.TupleRemovedBy), "" where it records none - the erase's, an operator's write no
// person made. actor is the write's own actor, which the removal's tuple names as its writer.
type dropper struct {
	person  bool
	remover string
	actor   model.Actor
}

// dropperOf is who makes actor's write in ctx: a person's own write (writtenByPerson) records its
// removals as the person it is made for (personOf).
func dropperOf(ctx context.Context, actor model.Actor) dropper {
	if !writtenByPerson(ctx, actor) {
		return dropper{actor: actor}
	}
	return dropper{person: true, remover: personOf(actor), actor: actor}
}

// personOf is the person a person's own write is made for, as an answer's decided_by names the
// person who answered: the browser's person (a user actor's login), or the person a credential
// holding the person scope belongs to (the owner a personal token's or a broker-issued token's
// actor names). It is "" where actor names neither.
func personOf(actor model.Actor) string {
	if actor.Kind == "user" {
		return model.CanonicalLogin(actor.ID)
	}
	if actor.Owner != nil {
		return model.CanonicalLogin(*actor.Owner)
	}
	return ""
}

// heldRemoval is one answered ask a person's own write took the block of out of its document,
// held for the event that records the version the write leaves (withRemovals).
type heldRemoval struct {
	person string
	actor  model.Actor
	at     time.Time
}

// answerDroppedAnsweredAsks answers the answered asks of document artifactID whose block a write
// loses (lost; after is the tree the write leaves). A write that is not a person's own is refused
// for them (refuseDroppedAnsweredAsks). A person's own write is taken, and holds each as that
// person's removal on the transaction's ledger, which the version the write leaves states on the
// ask's node (withRemovals). standing narrows the removals to the asks whose block the document's
// block index holds as an ask block, for a write that replaces the document whole and so has no
// tree of its own to say what the document held before it.
func answerDroppedAnsweredAsks(ctx context.Context, q Queryer, artifactID string, after *pmdoc.Node, lost func(blockID string) bool, by dropper, standing bool) error {
	if !by.person {
		return refuseDroppedAnsweredAsks(ctx, q, artifactID, after, lost)
	}
	ledger := ledgerFrom(ctx)
	if by.remover == "" || !graphstore.AddressFits(by.remover) || ledger == nil {
		return nil
	}
	rows, err := q.Query(ctx, `
		select k.id::text, k.block_id from asks k
		where k.block_artifact_id = $1 and k.state = 'answered' and k.block_id is not null
		  and (not $2 or exists (
			select 1 from document_blocks d
			where d.artifact_id = k.block_artifact_id and d.block_id = k.block_id and d.type = 'ask'))
		order by k.block_id
	`, artifactID, standing)
	if err != nil {
		return fmt.Errorf("read the answered asks a person's write removes: %w", err)
	}
	asks, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) ([2]string, error) {
		var ask [2]string
		err := row.Scan(&ask[0], &ask[1])
		return ask, err
	})
	if err != nil {
		return fmt.Errorf("read the answered asks a person's write removes: %w", err)
	}
	at := time.Now().UTC()
	for _, ask := range asks {
		if !lost(ask[1]) {
			continue
		}
		if ledger.removals == nil {
			ledger.removals = map[string]map[string]heldRemoval{}
		}
		if ledger.removals[artifactID] == nil {
			ledger.removals[artifactID] = map[string]heldRemoval{}
		}
		ledger.removals[artifactID][ask[0]] = heldRemoval{person: by.remover, actor: by.actor.Recorded(), at: at}
	}
	return nil
}

// withRemovals is blocks, what a version of artifactID's index stated, with the removals a
// person's own write of this transaction held for that document (answerDroppedAnsweredAsks): the
// person's node, written now, and one asserted removed_by from each ask's node to it, both
// appended under the event that records the version, which the caller hands blocks to
// (graphstore.Caused). The removals are taken off the ledger, so a second version of the same
// transaction states none of them again.
func withRemovals(ctx context.Context, tx pgx.Tx, artifactID string, blocks graphstore.Uncaused) (graphstore.Uncaused, error) {
	ledger := ledgerFrom(ctx)
	if ledger == nil || len(ledger.removals[artifactID]) == 0 {
		return blocks, nil
	}
	held := ledger.removals[artifactID]
	delete(ledger.removals, artifactID)
	var nodes []graphstore.Node
	var tuples []graphstore.Tuple
	people := map[string]bool{}
	for ask, removal := range held {
		if !people[removal.person] {
			people[removal.person] = true
			nodes = append(nodes, graphstore.PersonNode(removal.person, removal.actor, removal.at, graphstore.NoEvent))
		}
		tuples = append(tuples, graphstore.Tuple{
			FromKind: graphstore.KindAsk, FromID: ask, Kind: graphstore.TupleRemovedBy,
			ToKind: graphstore.KindPerson, ToID: removal.person,
			At: removal.at, Who: removal.actor, SourceEvent: graphstore.NoEvent, Asserted: true,
		})
	}
	written, err := graphstore.WriteUncaused(ctx, tx, nodes, tuples)
	if err != nil {
		return graphstore.Uncaused{}, fmt.Errorf("state the removals of the answered asks of %s: %w", artifactID, err)
	}
	blocks.Nodes = append(blocks.Nodes, written.Nodes...)
	blocks.Tuples = append(blocks.Tuples, written.Tuples...)
	return blocks, nil
}
