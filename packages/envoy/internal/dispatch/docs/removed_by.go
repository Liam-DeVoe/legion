package docs

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/graphcensus"
	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
	"github.com/sjawhar/envoy/internal/dispatch/store"
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
// held for the event that records the version the write leaves (removalsOf).
type heldRemoval struct {
	person string
	actor  model.Actor
	at     time.Time
}

// answerDroppedAnsweredAsks answers the answered asks of holder (documentAsksOf, roomAsksOf) whose
// block a write loses (lost; after is the tree the write leaves). A write that is not a person's
// own is refused for them (refuseDroppedAnsweredAsks). A person's own write is taken, and holds each
// as that person's removal on the transaction's ledger, under the document holding the ask's block
// (refs.AskBlockDocumentSQL), which the version the write leaves of that document states on the
// ask's node (removalsOf). standing narrows the removals, and the refusal, to the asks whose block
// the block index holds as an ask block, for a write that replaces the document whole and so has
// no tree of its own to say what the document held before it: an ask whose block an earlier write
// took out - a person's removal, or an agent's drop the restore still owes - is lost to no write of
// this one.
func answerDroppedAnsweredAsks(ctx context.Context, q Queryer, holder askHolder, after *pmdoc.Node, lost func(blockID string) bool, by dropper, standing bool) error {
	if !by.person {
		return refuseDroppedAnsweredAsks(ctx, q, holder, after, lost, standing)
	}
	ledger := ledgerFrom(ctx)
	if by.remover == "" || !graphstore.AddressFits(by.remover) || ledger == nil {
		return nil
	}
	rows, err := q.Query(ctx, `
		select k.id::text, k.block_id, `+refs.AskBlockDocumentSQL("k")+`::text from asks k
		where `+holder.predicate("k", "$1::uuid")+` and k.state = 'answered' and k.block_id is not null
		  and (not $2 or exists (
			select 1 from document_blocks d
			where d.artifact_id = k.block_artifact_id and d.block_id = k.block_id and d.type = 'ask'))
		order by k.block_id
	`, holder.id, standing)
	if err != nil {
		return fmt.Errorf("read the answered asks a person's write removes: %w", err)
	}
	asks, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) ([3]string, error) {
		var ask [3]string
		err := row.Scan(&ask[0], &ask[1], &ask[2])
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
		document := ask[2]
		if ledger.removals == nil {
			ledger.removals = map[string]map[string]heldRemoval{}
		}
		if ledger.removals[document] == nil {
			ledger.removals[document] = map[string]heldRemoval{}
		}
		ledger.removals[document][ask[0]] = heldRemoval{person: by.remover, actor: by.actor.Recorded(), at: at}
	}
	return nil
}

// holdSettledRemovals holds, for the version a settlement writes of artifactID from settled, the
// removal of each answered ask whose block the document's block index held and settled does not
// hold as an ask block, as the person the version's authors name (versionPerson). No writer but a
// person's own editor takes an answered ask's block out of a document in the room: every other
// write comes through a route that refuses it (refuseDroppedAnsweredAsks) and writes a version of
// its own. So the browser's deletion is that person's removal, as the same deletion over HTTP is
// at once (answerDroppedAnsweredAsks), and it stays taken: the version's index pass states it on
// the ask's node (removalsOf). An answer's own version names its answerer too, since the answer
// writes the ask's state into its block in her name (KeepAskBlockInStep), so a deletion settled
// into that version is hers as well. A version whose authors name no person holds none.
func holdSettledRemovals(ctx context.Context, q Queryer, artifactID string, settled *pmdoc.Node, authors []model.Actor) error {
	person, author := versionPerson(authors, "")
	if person == "" {
		return nil
	}
	held := openAskBlockIDs(settled)
	return answerDroppedAnsweredAsks(ctx, q, documentAsksOf(artifactID), settled, func(blockID string) bool {
		_, kept := held[blockID]
		return !kept
	}, dropper{person: true, remover: person, actor: author}, true)
}

// versionPerson is the person a version's authors name, "" where they name none, and the author
// that names them: the first author that is a person (kind user, a browser's person, or an agent
// acting for one through a route that takes the person scope) or an agent whose recorded act
// holds the person scope (a broker-issued token its person scoped), named as that author's person
// (personOf), other than excluded, the answerer in the version that first holds her answer
// (answerersVersion).
func versionPerson(authors []model.Actor, excluded string) (string, model.Actor) {
	for _, author := range authors {
		if author.Kind != "user" && (author.Act == nil || !slices.Contains(author.Act.Scopes, model.ScopePerson)) {
			continue
		}
		if login := personOf(author); login != "" && login != excluded && graphstore.AddressFits(login) {
			return login, author
		}
	}
	return "", model.Actor{}
}

// ReturnDeclined answers whether a person took the block of the ask of work, a return of the main
// shape (graphcensus.RestoreReturns), out of the spec (specRemover): the test the decisions step's
// restore of an earlier release read before it wrote an ask back, which the store census's
// dangling counter still reads its returning set by (graphcensus.ReturnDeclined), so that set
// holds only the asks that restore would write back. It reads the ask's row and the spec work
// names through q and writes nothing, so the census runs it in its read-only snapshot.
func ReturnDeclined(ctx context.Context, q graphstore.Queryer, work graphcensus.RestoreWork) (bool, error) {
	var specID string
	ask, err := ScanAsk(q.QueryRow(ctx, `
		select `+AskColumns+`, spec.id::text from asks a
		join artifacts spec on spec.ref_key = $2
		where a.id = $1::uuid`, work.Ask, work.Spec), &specID)
	if err != nil {
		return false, fmt.Errorf("read the ask %s the restore returns to %s: %w", work.Ask, work.Spec, err)
	}
	person, err := specRemover(ctx, q, work.Issue, specID, ask, work.BlockID)
	if err != nil {
		return false, err
	}
	return person != "", nil
}

// specRemover is the person who took ask's block, blockID, out of the spec specID of issue, read
// through q, "" where no person did: the version that left it out is the lowest above the newest
// whose stored tree holds an ask block with that id, read newest first through the stored-version
// reader the approval carry reads (approvedVersion), a version it cannot read holding none, and
// the person is the one that version's authors name (versionPerson). No version of the spec that
// held the block, a newest version that holds it, and a version whose authors name no person and
// no agent acting with the person scope name none. A version a person's own write wrote is the
// person's whatever other authors it lists, and one a session wrote with no act of a person is no
// person's.
//
// The ask's answerer is no remover in the one version that first holds her answer: the version
// written after the ask's answered_at whose previous version was written before it. The release
// before wrote an answer into its block in the live document in the answerer's name, so the first
// version after the answer credits her whatever else it holds, an agent's drop of the block among
// it (answerersVersion). In every later version she counts as any person does.
func specRemover(ctx context.Context, q Queryer, issue, specID string, ask model.Ask, blockID string) (string, error) {
	rows, err := q.Query(ctx, `select number from artifact_versions where artifact_id = $1::uuid order by number desc`, specID)
	if err != nil {
		return "", fmt.Errorf("read the versions of the spec of %s: %w", issue, err)
	}
	numbers, err := pgx.CollectRows(rows, pgx.RowTo[int])
	if err != nil {
		return "", fmt.Errorf("read the versions of the spec of %s: %w", issue, err)
	}
	removing, holder := 0, 0
	for _, number := range numbers {
		tree, _, readable, err := approvedVersion(ctx, q, specID, number)
		if err != nil {
			return "", err
		}
		if readable && askBlockIn(tree, blockID) != nil {
			holder = number
			break
		}
		removing = number
	}
	if holder == 0 || removing == 0 {
		return "", nil
	}
	var authors []model.Actor
	var at, previousAt time.Time
	if err := q.QueryRow(ctx, `
		select v.authors, v.created_at, previous.created_at
		from artifact_versions v, artifact_versions previous
		where v.artifact_id = $1::uuid and v.number = $2 and previous.artifact_id = $1::uuid and previous.number = $3
	`, specID, removing, holder).Scan(&authors, &at, &previousAt); err != nil {
		return "", fmt.Errorf("read the authors of version %d of the spec of %s: %w", removing, issue, err)
	}
	person, _ := versionPerson(authors, answerersVersion(ask, at, previousAt))
	return person, nil
}

// answerersVersion is the login of ask's answerer where a version written at at, whose previous
// version was written at previousAt, is the one that first holds the answer - written after the
// answer's time, its previous version before it - and "" otherwise (specRemover).
func answerersVersion(ask model.Ask, at, previousAt time.Time) string {
	if ask.Answer == nil || !at.After(ask.Answer.At) || !previousAt.Before(ask.Answer.At) {
		return ""
	}
	return model.CanonicalLogin(ask.Answer.User)
}

// askBlockIn is the ask block tree holds under id, nil where it holds none.
func askBlockIn(tree *pmdoc.Node, id string) *pmdoc.Node {
	var held *pmdoc.Node
	pmdoc.Walk(tree, func(node *pmdoc.Node) bool {
		if blockID, _ := node.Attrs[pmdoc.BlockIDAttr].(string); node.Type == "ask" && blockID == id {
			held = node
		}
		return held == nil
	})
	return held
}

// removalsOf is what a version of artifactID states of the removals a person's own write of this
// transaction held for that document (answerDroppedAnsweredAsks): the person's node, and one
// asserted removed_by from each ask's node to it, planned for the event that records the version
// (graphstore.PlanUncaused) and written nowhere here. The version's index pass plans it into its
// own stated write (refs.PlanBlocks, refs.PlanDecisionNodes, beside), so that write's node write
// writes the person's node beside the block nodes, under the one mark, and the caller's
// graphstore.QueueCaused appends the removed_by tuples, with what the index stated, under the
// version's event; a version no index pass plans queues it on its own (queueRemovals). The
// removals are taken off the ledger, so a second version of the same transaction states none of
// them again. It is the zero write where the transaction holds no removal of the document.
func removalsOf(ctx context.Context, artifactID string) graphstore.Uncaused {
	ledger := ledgerFrom(ctx)
	if ledger == nil || len(ledger.removals[artifactID]) == 0 {
		return graphstore.Uncaused{}
	}
	held := ledger.removals[artifactID]
	delete(ledger.removals, artifactID)
	var nodes []graphstore.Node
	var tuples []graphstore.Tuple
	people := map[string]bool{}
	for _, ask := range slices.Sorted(maps.Keys(held)) {
		removal := held[ask]
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
	return graphstore.PlanUncaused(nodes, tuples)
}

// queueRemovals is removalsOf for a version no index pass plans - one with no rendering
// (indexDocument) - which carries the removals on its own: their node write, the person's node, is
// queued here through write, after the version's insert, and the write planned is answered for
// the caller's event as an index pass's would be, which appends the removed_by tuples under it
// (graphstore.QueueCaused).
func queueRemovals(ctx context.Context, tx pgx.Tx, artifactID string) (graphstore.Uncaused, error) {
	removals := removalsOf(ctx, artifactID)
	if len(removals.Nodes) == 0 && len(removals.Tuples) == 0 {
		return graphstore.Uncaused{}, nil
	}
	if err := write(ctx, tx, func(b *store.WriteBatch) error {
		graphstore.QueueNodes(b, removals)
		return nil
	}); err != nil {
		return graphstore.Uncaused{}, fmt.Errorf("state the removals of the answered asks of %s: %w", artifactID, err)
	}
	return removals, nil
}
