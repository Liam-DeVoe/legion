package refs

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/asks"
	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
	"github.com/sjawhar/envoy/internal/dispatch/text"
)

// The block types the typed-node pass reads: a decision and a reading, whose blocks are nodes of
// their own type's supertag (dispatch://INFRA-1150 R1 and R2), and an ask, whose block's shaped
// hangs on the ask's node and whose answer is the decision a reading in it reads.
const (
	decisionType = "decision"
	readingType  = "reading"
	askType      = "ask"
)

// DecisionSupertag and ReadingSupertag are the supertags of a decision's and a reading's block
// node, their types' own, where every other block's node carries BlockSupertag
// (TypedFacts.supertag).
const (
	DecisionSupertag = decisionType
	ReadingSupertag  = readingType
)

// The ties a decision's node holds and the values it carries, each under its attribute's store
// name (schemanodes.StoreName), beside graphstore.TupleDecidedBy: shaped points at each block of
// the issue the decision shaped, from_ask at the ask it settles, and uncertainty,
// uncertainty_reason and replaced are values no table projects. An ask block's shaped is the
// same kind, stated on the ask's node.
const (
	TupleShaped            = "shaped"
	TupleFromAsk           = "from_ask"
	ValueUncertainty       = "uncertainty"
	ValueUncertaintyReason = "uncertainty_reason"
	ValueReplaced          = "replaced"
)

// typedKinds are the kinds of tie the typed-node pass states, whose block_refs rows it reconciles
// on its own above the deferral bound (IndexDecisionNodes).
var typedKinds = []string{graphstore.TupleDecidedBy, TupleFromAsk, TupleShaped}

// decisionValues are the values a decision's node holds, in the order decisionFact holds them.
var decisionValues = [...]string{ValueUncertainty, ValueUncertaintyReason, ValueReplaced}

// TypedFacts is what the typed-node pass reads of one version of a document (DecisionFacts): its
// decision, reading and ask blocks, read off the tree the version was rendered from, and what the
// store and the asks rows hold of them. IndexBlocks takes it for the supertag of each node it
// writes, the part_of of each reading and the ties of each decision and ask block, and
// IndexDecisionNodes writes the typed nodes, their values and their authors from it, so the two
// passes read one version once and agree on what they write. The zero value is a version that
// holds no such block or whose tree did not read back, of which neither pass states a typed fact.
type TypedFacts struct {
	// read is whether the version holds a decision, a reading or an ask block and its tree was
	// read: the zero facts write every decision and reading as its type's node and no tie.
	read     bool
	document indexedDocument
	// nodes and fits are the version's blocks as IndexBlocks reads them (uniqueBlocks) and
	// whether each one's address fits the store's key, which the pass places and pairs above the
	// deferral bound.
	nodes []model.ArtifactBlock
	fits  []bool
	// supertags is each block of the version that is a node of its own type's supertag, by id:
	// every decision, and every reading but one standing in an ask a person has not answered,
	// which settlement flags (docs' flagReadingsWithoutAnswers) and which is a node of the
	// supertag block until the ask holds an answer. typed is the same blocks in document order.
	supertags map[string]string
	typed     []string
	decisions []decisionFact
	readings  []string
	// askParents is each reading standing in an answered ask whose row names its node, with that
	// ask's id: the reading's part_of is the ask's node (reconcileBlockParents).
	askParents map[string]string
	// askBlocks is every ask block of the version, by id, with the id of the ask whose row names
	// its node, "" for a block no such row holds yet: the shaped of a block with none waits for
	// the row settlement opens.
	askBlocks map[string]string
	// shaped and ties are the decisions' and the ask blocks' shaped values and the decisions'
	// ties their writer states (a from_ask), as the pointers the block index pairs
	// (pointerPairs); foreign is each of them naming another issue than the document's, of which
	// no pass states a tie.
	shaped  []Pointer
	ties    []Pointer
	foreign map[tieKey]struct{}
	// serverDecided is whether the schema marks a decision's decided_by `server`, the server's to
	// derive. decidedBy is then each decision's author: the far end of the newest decided_by tuple
	// its node holds, or for a node holding none, which is the node's first write as a decision,
	// the person or session the version's writer proved (graphstore.AuthorOf). firstDecided says
	// the second.
	serverDecided bool
	decidedBy     map[string]Target
	firstDecided  map[string]bool
	// held is what the store holds now of each typed node, by address and then tuple kind: the
	// newest tuple of each of its values, its decided_by and its authored_by.
	held map[string]map[string]heldTie
}

// decisionFact is one decision block of the version: its id, and the values its attributes hold
// in decisionValues' order, nil where the attribute is absent - replaced is held only while it is
// true.
type decisionFact struct {
	id     string
	values [len(decisionValues)]*string
}

// tieKey is one tie a typed block holds: the block's id, the tie's kind and its target.
type tieKey struct {
	block, kind string
	target      Target
}

// heldTie is the newest tuple of one kind a node holds: its far end, and whether it asserts.
type heldTie struct {
	toKind, toID string
	asserted     bool
}

// DecisionFacts reads version `version` of document artifactID for the typed-node pass, in the
// caller's transaction, which holds the document's owner row: blocks are the version's rendered
// blocks, tree the tree they were rendered from, and pointers the version's `block[]` values
// (pmdoc.BlockPointers), of which it keeps the decisions' and the ask blocks' shaped. A version
// holding no decision, reading or ask block, and one with no tree, reads nothing and answers the
// zero facts.
//
// It reads the version's writer and its document's issue (indexedVersionRecord), the ask row of
// each ask block - the ask's id, whether the row names the ask's node, and whether that node
// holds an answer (its newest answer_state value, which asks.Settle appends) - and, of each
// decision and reading node, the newest tuple of each value it holds and of its decided_by and
// its authored_by.
//
// Whose a decision's tie is, the schema says (pmdoc.BlockTypeAttributes): a relation attribute
// its writer writes - from_ask, of kind `ask` - is read off the tree, and decided_by, an `actor`
// the schema marks `server`, is the server's and never a value from the tree, which a browser
// can put anything in. Its author is read off its node, because markdown never carries it (the
// attribute is never rendered, `markdown: false`): the stored tuple is the one record of who
// decided, and the credential the version proved names the author only at the node's first
// write, so a later write by another writer, the browser's included, moves nothing of it.
func DecisionFacts(ctx context.Context, tx pgx.Tx, artifactID string, version int, blocks []model.ArtifactBlock, tree *pmdoc.Node, pointers []Pointer) (TypedFacts, error) {
	if tree == nil || !HoldsTypedBlocks(blocks) {
		return TypedFacts{}, nil
	}
	document, err := indexedVersionRecord(ctx, tx, artifactID, version)
	if err != nil {
		return TypedFacts{}, err
	}
	nodes, _, _ := uniqueBlocks(blocks)
	fits, _ := blockFits(document.refKey, nodes)
	facts := TypedFacts{
		read: true, document: document, nodes: nodes, fits: fits,
		supertags: map[string]string{}, askBlocks: map[string]string{},
	}
	types := make(map[string]string, len(nodes))
	for _, node := range nodes {
		types[node.ID] = node.Type
	}
	decisionAttributes, _ := pmdoc.BlockTypeAttributes(decisionType)
	facts.serverDecided = decisionAttributes.Get(graphstore.TupleDecidedBy).Server
	// The readings of each holder, in document order, before the ask rows say which of them
	// stand in an answered ask.
	type heldReading struct{ id, holder, holderType string }
	var readings []heldReading
	seen := map[string]struct{}{}
	pmdoc.Walk(tree, func(node *pmdoc.Node) bool {
		if node.Type != decisionType && node.Type != askType {
			return true
		}
		id := typedBlockID(node)
		if _, repeated := seen[id]; repeated || id == "" || types[id] != node.Type {
			return true
		}
		seen[id] = struct{}{}
		if node.Type == decisionType {
			facts.decisions = append(facts.decisions, decisionOf(id, node))
			facts.ties = append(facts.ties, writtenTies(id, node, decisionAttributes)...)
		} else {
			facts.askBlocks[id] = ""
		}
		_, admitted := pmdoc.TypedContent(node)
		for _, child := range admitted {
			reading := typedBlockID(child)
			if _, repeated := seen[reading]; child.Type != readingType || repeated || reading == "" || types[reading] != readingType {
				continue
			}
			seen[reading] = struct{}{}
			readings = append(readings, heldReading{id: reading, holder: id, holderType: node.Type})
		}
		return true
	})
	for _, pointer := range pointers {
		if pointer.Kind != TupleShaped || (pointer.BlockType != decisionType && pointer.BlockType != askType) {
			continue
		}
		facts.shaped = append(facts.shaped, pointer)
	}
	asked, err := readAskBlocks(ctx, tx, artifactID, facts.askBlocks)
	if err != nil {
		return TypedFacts{}, err
	}
	for id, ask := range asked {
		if ask.named {
			facts.askBlocks[id] = ask.id
		}
	}
	for _, decision := range facts.decisions {
		facts.supertags[decision.id] = decisionType
	}
	for _, reading := range readings {
		if reading.holderType == askType {
			ask, opened := asked[reading.holder]
			if opened && !ask.answered {
				continue
			}
			if opened && ask.named {
				if facts.askParents == nil {
					facts.askParents = map[string]string{}
				}
				facts.askParents[reading.id] = ask.id
			}
		}
		facts.supertags[reading.id] = readingType
		facts.readings = append(facts.readings, reading.id)
	}
	for _, node := range nodes {
		if _, typed := facts.supertags[node.ID]; typed {
			facts.typed = append(facts.typed, node.ID)
		}
	}
	facts.foreign = foreignTies(document.issue, facts.shaped, facts.ties)
	if err := facts.readHeld(ctx, tx); err != nil {
		return TypedFacts{}, err
	}
	return facts, nil
}

// HoldsTypedBlocks reports whether blocks hold a block the typed-node pass reads: a decision, a
// reading or an ask.
func HoldsTypedBlocks(blocks []model.ArtifactBlock) bool {
	for _, block := range blocks {
		switch block.Type {
		case decisionType, readingType, askType:
			return true
		}
	}
	return false
}

// typedBlockID is a typed block's id as the block index keys it: a U+0000 reads as U+FFFD, as the
// rendering writes it (pmdoc.BlockPointers).
func typedBlockID(node *pmdoc.Node) string {
	id, _ := node.Attrs[pmdoc.BlockIDAttr].(string)
	return strings.ReplaceAll(id, "\x00", "\uFFFD")
}

// decisionOf is decision block node as decisionFact holds it.
func decisionOf(id string, node *pmdoc.Node) decisionFact {
	fact := decisionFact{id: id}
	for index, name := range decisionValues {
		switch value := node.Attrs[name].(type) {
		case string:
			if value != "" {
				fact.values[index] = &value
			}
		case bool:
			if value {
				held := "true"
				fact.values[index] = &held
			}
		}
	}
	return fact
}

// writtenTies is the ties a decision block node's writer states, read off its attributes: the
// value of each relation attribute its type declares and does not mark `server`, as a pointer of
// the attribute's kind - an `ask` attribute's ask (a from_ask), and an `actor` attribute's
// person. A `server` attribute's value is the server's to derive and is never read off the tree
// (decidedBy). A tree inside the schema holds only an ask of an issue written whole (pmdoc's
// validateAskReference).
func writtenTies(id string, node *pmdoc.Node, attributes pmdoc.BlockAttributes) []Pointer {
	var ties []Pointer
	for _, attribute := range attributes {
		written, _ := node.Attrs[attribute.Name].(string)
		if attribute.Server || written == "" {
			continue
		}
		switch attribute.Kind {
		case pmdoc.BlockAttributeAsk:
			if ref, ok := text.ParseRef(written, ""); ok && ref.Kind == askType {
				ties = append(ties, Pointer{Block: id, BlockType: decisionType, Kind: attribute.Name, Target: ref})
			}
		case pmdoc.BlockAttributeActor:
			ties = append(ties, Pointer{Block: id, BlockType: decisionType, Kind: attribute.Name,
				Target: text.Ref{Kind: graphstore.KindPerson, ID: model.CanonicalLogin(written)}})
		}
	}
	return ties
}

// foreignTies is each of ties naming another issue than issue, the document's, by the issue its
// reference spells (pmdoc.ReferencedIssue), none for a document no issue holds: a write that
// takes markdown refuses such a tie (pmdoc.CheckIssueTies), and a browser's, which settlement
// never refuses, is stored with no tie stated, so the store holds no relation the editor and the
// server both forbid.
func foreignTies(issue string, ties ...[]Pointer) map[tieKey]struct{} {
	if issue == "" {
		return nil
	}
	var foreign map[tieKey]struct{}
	for _, held := range ties {
		for _, tie := range held {
			named := tie.Target.IssueKey
			if named == "" {
				named = tie.Target.Project
			}
			if named == "" || named == issue {
				continue
			}
			target, _ := TargetOf(tie.Target)
			if foreign == nil {
				foreign = map[tieKey]struct{}{}
			}
			foreign[tieKey{block: tie.Block, kind: tie.Kind, target: target}] = struct{}{}
		}
	}
	return foreign
}

// askBlockRow is what an ask block's row says of its ask: the ask's id, whether the row names the
// ask's node, and whether that node holds an answer.
type askBlockRow struct {
	id              string
	named, answered bool
}

// readAskBlocks reads the row of each of blocks, ask blocks of document artifactID, by block id,
// in one statement: a block no row holds is absent. An ask holds an answer where its node's
// newest answer_state value is answered, the value asks.Settle appends; a node holding none, an
// ask a process of the release before wrote, reads as open (docs' askAnswers).
func readAskBlocks(ctx context.Context, tx pgx.Tx, artifactID string, blocks map[string]string) (map[string]askBlockRow, error) {
	if len(blocks) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(blocks))
	for id := range blocks {
		ids = append(ids, id)
	}
	rows, err := tx.Query(ctx, `
		select k.block_id, k.id::text, k.node_kind is not null and k.node_address is not null,
		       coalesce(answer.to_id = $3, false)
		from asks k
		left join lateral (
			select t.to_id, t.asserted from graph_tuples t
			where t.from_kind = $4 and t.from_id = k.id::text and t.kind = $5 and t.to_kind = $6
			order by t.seq desc
			limit 1
		) answer on answer.asserted
		where k.block_artifact_id = $1 and k.block_id = any($2::text[])
	`, artifactID, ids, asks.StateAnswered, graphstore.KindAsk, graphstore.TupleAnswerState, graphstore.KindValue)
	if err != nil {
		return nil, fmt.Errorf("read the asks of the ask blocks of %s: %w", artifactID, err)
	}
	defer rows.Close()
	found := make(map[string]askBlockRow, len(ids))
	for rows.Next() {
		var block string
		var row askBlockRow
		if err := rows.Scan(&block, &row.id, &row.named, &row.answered); err != nil {
			return nil, fmt.Errorf("scan the ask of an ask block of %s: %w", artifactID, err)
		}
		found[block] = row
	}
	return found, rows.Err()
}

// readHeld reads what the store holds of each typed node that fits its key (held), in one
// statement, and decides each decision's author from it (decidedBy).
func (f *TypedFacts) readHeld(ctx context.Context, tx pgx.Tx) error {
	refKey := f.document.refKey
	addresses := make([]string, 0, len(f.typed))
	for _, id := range f.typed {
		if blockIDFits(refKey, id) {
			addresses = append(addresses, blockNodeAddress(refKey, id))
		}
	}
	f.held = make(map[string]map[string]heldTie, len(addresses))
	if len(addresses) > 0 {
		kinds := append([]string{graphstore.TupleDecidedBy, graphstore.TupleAuthoredBy}, decisionValues[:]...)
		rows, err := tx.Query(ctx, `
			select distinct on (t.from_id, t.kind) t.from_id, t.kind, t.to_kind, t.to_id, t.asserted
			from graph_tuples t
			where t.from_kind = $1 and t.from_id = any($2::text[]) and t.kind = any($3::text[])
			order by t.from_id, t.kind, t.seq desc
		`, BlockNodeKind, addresses, kinds)
		if err != nil {
			return fmt.Errorf("read what the store holds of the decisions and readings of %s: %w", refKey, err)
		}
		defer rows.Close()
		for rows.Next() {
			var address, kind string
			var tie heldTie
			if err := rows.Scan(&address, &kind, &tie.toKind, &tie.toID, &tie.asserted); err != nil {
				return fmt.Errorf("scan what the store holds of a decision or a reading of %s: %w", refKey, err)
			}
			if f.held[address] == nil {
				f.held[address] = map[string]heldTie{}
			}
			f.held[address][kind] = tie
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("read what the store holds of the decisions and readings of %s: %w", refKey, err)
		}
	}
	if !f.serverDecided {
		return nil
	}
	writerKind, writerID, proved := graphstore.AuthorOf(f.document.writer)
	proved = proved && graphstore.AddressFits(writerID)
	for _, decision := range f.decisions {
		if !blockIDFits(refKey, decision.id) {
			continue
		}
		if held, stated := f.held[blockNodeAddress(refKey, decision.id)][graphstore.TupleDecidedBy]; stated {
			f.setDecidedBy(decision.id, Target{Kind: held.toKind, ID: held.toID}, false)
		} else if proved {
			f.setDecidedBy(decision.id, Target{Kind: writerKind, ID: writerID}, true)
		}
	}
	return nil
}

func (f *TypedFacts) setDecidedBy(id string, author Target, first bool) {
	if f.decidedBy == nil {
		f.decidedBy, f.firstDecided = map[string]Target{}, map[string]bool{}
	}
	f.decidedBy[id], f.firstDecided[id] = author, first
}

// supertag is the supertag of the node of block id, of type typ: its own type's for a decision
// and for a reading the facts hold to be one, and BlockSupertag for every other block. Facts that
// read nothing tag every decision and reading by its type.
func (f TypedFacts) supertag(id, typ string) string {
	if f.read {
		if supertag, typed := f.supertags[id]; typed {
			return supertag
		}
		return BlockSupertag
	}
	switch typ {
	case decisionType, readingType:
		return typ
	}
	return BlockSupertag
}

// withTies is pointers with each decision's written ties beside them (a from_ask), which the
// block index pairs as it pairs a pointer: a from_ask value is written in its decision's opening
// line, so the mention the extraction finds there is the tie's and no mention of its own
// (pointerPairs).
func (f TypedFacts) withTies(pointers []Pointer) []Pointer {
	if len(f.ties) == 0 {
		return pointers
	}
	return slices.Concat(pointers, f.ties)
}

// pairs is a version's pairs (blockPairs) as replaceBlockRefs reconciles them: a tie naming
// another issue left out (foreign), an ask block's shaped marked with its ask's node or, where no
// row names that node yet, left out, and each decision's decided_by added, its far end the author
// decidedBy names. The typed-node pass hands the same function the pairs of its own ties, so the
// rows it writes above the deferral bound are the rows the block index writes for one version.
func (f TypedFacts) pairs(pairs []blockPair) []blockPair {
	if !f.read {
		return pairs
	}
	kept := make([]blockPair, 0, len(pairs)+len(f.decidedBy))
	for _, pair := range pairs {
		if _, foreign := f.foreign[tieKey{block: pair.blockID, kind: pair.kind, target: pair.target}]; foreign {
			continue
		}
		if pair.kind == TupleShaped {
			if ask, onAsk := f.askBlocks[pair.blockID]; onAsk {
				if ask == "" {
					continue
				}
				pair.nodeKind, pair.nodeAddress = graphstore.KindAsk, ask
			}
		}
		kept = append(kept, pair)
	}
	for _, decision := range f.decisions {
		if author, decided := f.decidedBy[decision.id]; decided {
			kept = append(kept, blockPair{blockID: decision.id, kind: graphstore.TupleDecidedBy, target: author})
		}
	}
	return kept
}

// typedPairs is the pairs of the facts' own ties: each decision's and ask block's shaped and each
// decision's from_ask, paired as the block index pairs a pointer (pointerPairs), and its
// decided_by (pairs).
func (f TypedFacts) typedPairs() []blockPair {
	paired, _ := pointerPairs(f.nodes, slices.Concat(f.shaped, f.ties))
	return f.pairs(paired)
}

// typedScope is the rows of block_refs the facts' own ties are: those of typedKinds held by the
// version's decisions and ask blocks.
func (f TypedFacts) typedScope() *pairScope {
	blocks := make([]string, 0, len(f.decisions)+len(f.askBlocks))
	for _, decision := range f.decisions {
		blocks = append(blocks, decision.id)
	}
	for id := range f.askBlocks {
		blocks = append(blocks, id)
	}
	slices.Sort(blocks)
	return &pairScope{kinds: typedKinds, blocks: blocks}
}

// IndexDecisionNodes is the typed-node pass over version `version` of document artifactID,
// written at versionAt, in the transaction that accepts the write, which holds the document's
// owner row: facts are what DecisionFacts read of the version. It answers the targets whose
// counts the ties it reconciled moved, and what it states in the node and tuple store, held for
// the event that records the version (graphstore.Uncaused), which the caller appends with the
// block index's.
//
// For each decision and reading of the version it writes the block's node with its type's
// supertag, the same node refs.IndexBlocks writes (blockNode), so whichever of the two writes it
// second moves nothing. Of a decision it appends uncertainty, uncertainty_reason and replaced as
// values where they moved, and retracts one the decision no longer holds, so a decision whose
// author recorded neither level nor reason holds neither. At a decision's first write it writes
// the node of the person or session the version's writer proved (graphstore.AuthorFacts), the
// far end of its decided_by; the tie itself is its block_refs row's, stated by the reconcile that
// writes the row (replaceBlockRefs) from the author DecisionFacts read, so a later write by
// another writer, which reads the stored tuple, moves nothing of it. Of a reading it appends
// authored_by once, at the first write that finds it holding none, through graphstore.AuthorFacts,
// from the credential and never from the body.
//
// indexed says refs.IndexBlocks indexed this version in the same transaction: that pass then
// stated each typed node's part_of and reconciled every block_refs row of the document with the
// facts' ties among them. Above the deferral bound (docs.IndexDocumentReferences) no block index
// runs in the request, and this pass does both for its own blocks alone: it states the part_of
// of each decision and reading where the store places it elsewhere (reconcileBlockParents), and
// reconciles the rows of its own kinds held by its own blocks (typedScope), deleting no other row
// of the document. The block-index reconciliation indexes the document later with the same
// facts, and its pairs hold these same ties, so it moves none of them
// (TestTheReconcilerMovesNoRowTheTypedPassWrote).
func IndexDecisionNodes(ctx context.Context, tx pgx.Tx, artifactID string, version int, versionAt time.Time, facts TypedFacts, indexed bool) ([]Target, graphstore.Uncaused, error) {
	if !facts.read {
		return nil, graphstore.Uncaused{}, nil
	}
	document := facts.document
	refKey, writer := document.refKey, document.writer
	nodes := make([]graphstore.Node, 0, len(facts.typed)+2)
	for _, id := range facts.typed {
		if blockIDFits(refKey, id) {
			nodes = append(nodes, blockNode(blockNodeAddress(refKey, id), document, versionAt, graphstore.StateLive, facts.supertags[id]))
		}
	}
	var tuples []graphstore.Tuple
	// The writer's node and the aliases the server proved of it are stated once however many
	// nodes name it, since one write of the store takes each node once.
	authorWritten := false
	author := func(address string) []graphstore.Tuple {
		authorNodes, stated, named := graphstore.AuthorFacts(BlockNodeKind, address, writer, versionAt, graphstore.NoEvent)
		if !named {
			return nil
		}
		if !authorWritten {
			nodes = append(nodes, authorNodes...)
			tuples = append(tuples, stated[1:]...)
			authorWritten = true
		}
		return stated[:1]
	}
	for _, decision := range facts.decisions {
		if !blockIDFits(refKey, decision.id) {
			continue
		}
		address := blockNodeAddress(refKey, decision.id)
		if facts.firstDecided[decision.id] {
			author(address)
		}
		held := facts.held[address]
		for index, kind := range decisionValues {
			stored, value := held[kind], decision.values[index]
			switch {
			case value != nil && (!stored.asserted || stored.toID != *value):
				tuples = append(tuples, valueTuple(address, kind, *value, versionAt, writer, true))
			case value == nil && stored.asserted:
				tuples = append(tuples, valueTuple(address, kind, stored.toID, versionAt, writer, false))
			}
		}
	}
	for _, reading := range facts.readings {
		if !blockIDFits(refKey, reading) {
			continue
		}
		address := blockNodeAddress(refKey, reading)
		if _, stated := facts.held[address][graphstore.TupleAuthoredBy]; !stated {
			tuples = append(tuples, author(address)...)
		}
	}
	write, err := graphstore.WriteUncaused(ctx, tx, nodes, tuples)
	if err != nil {
		return nil, graphstore.Uncaused{}, fmt.Errorf("write the decisions and readings of version %d of %s: %w", version, artifactID, err)
	}
	if indexed {
		return nil, write, nil
	}
	only := make(map[string]struct{}, len(facts.typed))
	for _, id := range facts.typed {
		only[id] = struct{}{}
	}
	parents, err := reconcileBlockParents(ctx, tx, artifactID, refKey, facts.nodes, facts.fits, facts.askParents, only, versionAt, writer)
	if err != nil {
		return nil, graphstore.Uncaused{}, err
	}
	write.Tuples = append(write.Tuples, parents...)
	moved, links, err := replaceBlockRefs(ctx, tx, artifactID, refKey, version, versionAt, writer, false, facts.typedPairs(), facts.typedScope())
	if err != nil {
		return nil, graphstore.Uncaused{}, err
	}
	write.Tuples = append(write.Tuples, links...)
	return moved, write, nil
}

// IndexAskTies states the shaped ties of block, an ask block of version `version` of document
// artifactID, in the transaction that opened the block's ask, which holds the document's owner
// row: facts are what DecisionFacts read of the version once that ask's row and node exist. An
// ask block's shaped hangs on the ask's node (TypedFacts.pairs), so a block whose ask no row held
// when its version was indexed had none of its ties written then; the settlement that opens the
// ask writes them here, beside the ask's row, when it writes no version of its own to index
// (docs' writeOpenedAskTies). It reconciles the block's shaped rows alone, from the same pairs the
// block index hands replaceBlockRefs, so the document's next index moves none of them, and
// answers the targets whose counts those rows moved, which the ask.opened that records the ask
// names (CountedChanges), and what it states, held for that event.
func IndexAskTies(ctx context.Context, tx pgx.Tx, artifactID string, version int, at time.Time, facts TypedFacts, block string) ([]Target, graphstore.Uncaused, error) {
	if _, onAsk := facts.askBlocks[block]; !facts.read || !onAsk {
		return nil, graphstore.Uncaused{}, nil
	}
	var pairs []blockPair
	for _, pair := range facts.typedPairs() {
		if pair.blockID == block && pair.kind == TupleShaped {
			pairs = append(pairs, pair)
		}
	}
	moved, tuples, err := replaceBlockRefs(ctx, tx, artifactID, facts.document.refKey, version, at, facts.document.writer, false, pairs,
		&pairScope{kinds: []string{TupleShaped}, blocks: []string{block}})
	if err != nil {
		return nil, graphstore.Uncaused{}, err
	}
	return moved, graphstore.Uncaused{Tuples: tuples}, nil
}

// valueTuple is one value of a decision's node: asserted where the decision holds it, and a
// retraction of the value the node held where the decision holds none.
func valueTuple(address, kind, value string, at time.Time, writer model.Actor, asserted bool) graphstore.Tuple {
	return graphstore.Tuple{
		FromKind: BlockNodeKind, FromID: address, Kind: kind,
		ToKind: graphstore.KindValue, ToID: value,
		At: at, Who: writer, Asserted: asserted,
	}
}
