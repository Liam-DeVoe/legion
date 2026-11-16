package refs

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// Every read of the graph answers only the nodes the acting person may see. A node carries that
// in `graph_nodes.visibility`: `project` is every node Dispatch writes today, which everyone who
// may read the project reads, and `author` reserves the node for whoever wrote it. A node
// reserved for its author is absent from the list of its kind, from the edges of every other
// node, from the backlink counts, from search, and from a read of its own `dispatch://` address,
// which answers exactly what an address nobody holds answers.
//
// The test is bound into the SQL of each read and never applied in Go, so a count and the list
// behind it cannot disagree: a row the count includes is a row the list shows.
//
// nodeRegistry (below) is the registry: one entry per node kind, carrying that kind's
// predicate beside the loader that reads its nodes, and every read binds its predicate from
// there rather than writing a test of its own. The two live in one value so a kind cannot be
// registered with a loader and no predicate: such an entry does not build, and one written as
// a literal past the constructors is refused at init, where the kind is named (the three lines
// nodeKind states).
// TestEveryNodeKindAnswersAVisibilityPredicate holds the registry's keys to the node kinds the
// store resolves, in both directions, and holds every read's own SQL to predicates this file
// rendered.

// nodeLoader loads the nodes of one kind whose ids are ids into nodes, for the person or
// session who the read acts for: a loader binds its kind's visibility predicate into its own
// SQL, so a node reserved for another author is one the read never answers. Artifact ids are
// uuids on the from side of an edge and ref_keys on the to side; byRefKey says which the ids
// are.
type nodeLoader func(ctx context.Context, q Queryer, who model.Actor, kind string, byRefKey bool, ids []string, nodes map[[2]string]resolvedNode) error

// nodeKind is one node kind's entry in the registry below: the loader that reads the kind's
// nodes, and the predicate that decides which of them the acting person may see
// (nodeVisibility). The two are one value because a kind that resolves with no predicate is a
// read that answers a node its author reserved.
//
// Three lines keep a kind from reaching a read unguarded, and each one sees what the others
// cannot:
//
//   - The constructors below state the predicate: resolvedKind and unresolvedKind take it as an
//     argument, so a kind registered with a loader and no predicate is a call with an argument
//     missing and the package does not build, and personAuthoredKind and credentialAuthoredKind
//     each make the one predicate their name says. This is the line a kind added the way every
//     kind here was added meets.
//   - init refuses an entry no constructor made: a hand-written literal such as
//     {load: loadIssueNodes} compiles, and its predicate is the zero value nobody chose
//     rather than none at all, so the kind would run on a rule no one wrote. The process does
//     not start instead, naming the kind. A compiler cannot tell that literal from a stated
//     predicate; this can.
//   - TestEveryNodeKindAnswersAVisibilityPredicate holds the registry's keys to the node kinds
//     the store itself resolves. A kind the store resolves that the registry never heard of is
//     in no literal and in no call, so neither of the other two lines can see it.
type nodeKind struct {
	// load reads the kind's nodes. It is nil for a kind no read of the graph resolves
	// (unresolvedKind), and such a kind is no key of nodeKinds.
	load nodeLoader
	// visibility is the kind's rule: which of its nodes a read answers (VisibleKind).
	visibility nodeVisibility
	// authorOnly says every read of the kind binds its predicate, so the kind may hold a node
	// reserved for its author: init hands every kind flagged so to the store, which refuses such
	// a node of any other kind (graphstore.AdmitAuthorOnly). It is set by personAuthoredKind and
	// credentialAuthoredKind alone, the constructors of the kinds dispatch://INFRA-1950 adds that
	// take `author` - interaction, lens, lens_block, risk and constraint - each called in the
	// commit that registers its kind; a lens_kind is always project-visible and takes neither.
	authorOnly bool
	// stated is true of an entry one of the constructors made, which is every entry whose
	// predicate someone stated: the zero value of nodeVisibility is a predicate in its own
	// right, the rule most kinds take, so the entry carries whether it was chosen rather than
	// defaulted. init refuses the registry over an entry whose stated is false.
	stated bool
}

// resolvedKind registers a node kind the graph addresses: the loader that reads its nodes, and
// the predicate every read of the kind binds. The kind takes no node reserved for its author.
func resolvedKind(load nodeLoader, visibility nodeVisibility) nodeKind {
	return nodeKind{load: load, visibility: visibility, stated: true}
}

// unresolvedKind registers a node kind the store holds rows of that no read of the graph
// resolves into a node. It answers a predicate like every other kind and carries no loader.
func unresolvedKind(visibility nodeVisibility) nodeKind {
	return nodeKind{visibility: visibility, stated: true}
}

// personAuthoredKind registers a node kind that may hold a node reserved for its author and whose
// predicate compares the person a credential proves (comparesPerson): the loader that reads its
// nodes, every one of which binds that predicate. A lens, a block of a lens's finding
// (lens_block), a risk and a constraint are registered by it, so a person's own personal token
// reads what that person's cookie wrote.
func personAuthoredKind(load nodeLoader) nodeKind {
	return nodeKind{load: load, visibility: nodeVisibility{compares: comparesPerson}, authorOnly: true, stated: true}
}

// credentialAuthoredKind registers a node kind that may hold a node reserved for its author and
// whose predicate compares the whole acting credential (comparesCredential): the loader that
// reads its nodes, every one of which binds that predicate. An interaction is registered by it,
// since a run is read by the credential that made it, as the going stream reads its run
// (model.Actor.SameCredential), and a run whose author is nobody by nobody.
func credentialAuthoredKind(load nodeLoader) nodeKind {
	return nodeKind{load: load, visibility: nodeVisibility{compares: comparesCredential}, authorOnly: true, stated: true}
}

// nodeRegistry is every node kind this build knows: how each one resolves, and the visibility
// predicate every read of it binds. These are node kinds, the ends of an edge, not edge kinds:
// a node kind a newer build adds to the graph is addressable, listed and counted only once it
// has an entry here. The keys are the kinds graph_edges stores; an ask's, a comment's or a
// message's block is stored as ask_block, comment_block or message_block and resolves to a
// node of kind block, as a document's block does.
//
// It is filled in init rather than in its own declaration because the loaders it holds render,
// through VisibleKind, the predicates this same registry answers: as the initializer of the
// variable, that reference is an initialization cycle the compiler refuses, while at init it
// is what it is at a read - a loader that reads the registry long after it is built.
var nodeRegistry map[string]nodeKind

// nodeKinds is every node kind nodeRegistry resolves, sorted, as the reads that filter
// graph_edges in SQL bind it. BacklinkCounts binds it on its mention arm alone, the one arm
// whose from_kind is a column; every other arm it reads fixes from_kind as a literal this list
// holds, so the filter would drop nothing there. A kind the registry carries no loader for is
// the address of no node a read of the graph answers, so it is none of these.
var nodeKinds []string

// personComparedKinds is every node kind of nodeRegistry whose predicate compares the person a
// credential proves (comparesPerson), sorted: what a predicate over a node whose kind is a column
// reads to pick the comparison of each node's own kind (authorIsOfRow).
var personComparedKinds []string

func init() {
	nodeRegistry = map[string]nodeKind{
		// The kinds the projection tables hold, whose nodes the graph addresses by the id
		// their own loader reads.
		"issue":         resolvedKind(loadIssueNodes, nodeVisibility{}),
		"artifact":      resolvedKind(loadArtifactNodes, nodeVisibility{fromAddress: artifactStoreAddress}),
		"ask":           resolvedKind(loadAskNodes, nodeVisibility{}),
		"comment":       resolvedKind(loadCommentNodes, nodeVisibility{}),
		"message":       resolvedKind(loadMessageNodes, nodeVisibility{}),
		"component":     resolvedKind(loadComponentNodes, nodeVisibility{}),
		"block":         resolvedKind(loadBlockNodes, nodeVisibility{}),
		"ask_block":     resolvedKind(loadAskBlockNodes, nodeVisibility{}),
		"comment_block": resolvedKind(loadCommentBlockNodes, nodeVisibility{}),
		"message_block": resolvedKind(loadMessageBlockNodes, nodeVisibility{}),
		// The kinds the node and tuple store holds itself (graphstore.KindPerson and its three
		// siblings, loadStoreNodes). None of them carries a `dispatch://` address, so the
		// address rule does not reach them; the four other rules do.
		graphstore.KindPerson:  resolvedKind(loadStoreNodes, nodeVisibility{}),
		graphstore.KindSession: resolvedKind(loadStoreNodes, nodeVisibility{}),
		graphstore.KindRole:    resolvedKind(loadStoreNodes, nodeVisibility{}),
		graphstore.KindPage:    resolvedKind(loadStoreNodes, nodeVisibility{}),
		// The kinds of dispatch://INFRA-1950 the store holds alone, each registered in the commit
		// that first writes a node of it (migration 20261116123354 writes both). A lens kind is a
		// node of the catalogue, always project-visible, so its entry takes no node reserved for its
		// author; a constraint may be reserved for its author, and its predicate compares the person
		// a credential proves.
		graphstore.KindLensKind:   resolvedKind(loadLensKindNodes, nodeVisibility{}),
		graphstore.KindConstraint: personAuthoredKind(loadConstraintNodes),
		// The block schema's own nodes, which a type is (migration 20261109051232). Nothing
		// writes one outside a migration, every one of them is project-visible and no read of
		// the graph resolves one, but they are nodes of the store like any other, so the
		// registry answers for them rather than leaving the one read that lists them
		// (pmdoc/schemanodes, through graphstore.NodesOfKind) with no predicate to bind the
		// day a type is reserved for whoever drafted it.
		"supertag":  unresolvedKind(nodeVisibility{}),
		"attribute": unresolvedKind(nodeVisibility{}),
	}
	for _, kind := range slices.Sorted(maps.Keys(nodeRegistry)) {
		if !nodeRegistry[kind].stated {
			// Nothing of this build runs rather than one read of this kind answering under a
			// rule nobody wrote: the entry was written as a literal instead of through one of
			// the constructors, and took the zero predicate by default.
			panic("refs: the node kind " + kind + " is registered with no visibility predicate: " +
				"every entry of nodeRegistry is made by one of its constructors, each of which states one")
		}
	}
	deriveRegistryLists()
	// The store takes a node reserved for its author of the kinds flagged here and of no other,
	// so this is the one place that decides which kinds those are (graphstore.AdmitAuthorOnly).
	graphstore.AdmitAuthorOnly(authorOnlyKinds()...)
}

// deriveRegistryLists sets the lists read off nodeRegistry once it is built: the kinds it
// resolves (nodeKinds) and the kinds whose predicate compares the person (personComparedKinds).
func deriveRegistryLists() {
	nodeKinds = resolvedNodeKinds()
	var persons []string
	for _, kind := range VisibilityKinds() {
		if nodeRegistry[kind].visibility.compares == comparesPerson {
			persons = append(persons, kind)
		}
	}
	personComparedKinds = persons
}

// authorOnlyKinds is every kind of nodeRegistry flagged as one that may hold a node reserved for
// its author (nodeKind.authorOnly), sorted.
func authorOnlyKinds() []string {
	var kinds []string
	for _, kind := range VisibilityKinds() {
		if nodeRegistry[kind].authorOnly {
			kinds = append(kinds, kind)
		}
	}
	return kinds
}

// resolvedNodeKinds is the registry's keys that carry a loader, sorted.
func resolvedNodeKinds() []string {
	kinds := make([]string, 0, len(nodeRegistry))
	for kind, entry := range nodeRegistry {
		if entry.load == nil {
			continue
		}
		kinds = append(kinds, kind)
	}
	slices.Sort(kinds)
	return kinds
}

// comparison is what a kind's predicate compares the author of a node reserved for that author
// with (nodeVisibility.compares).
type comparison uint8

const (
	// comparesCredential compares the whole acting credential with the one that wrote the node
	// (graphstore.AuthorIsSQL): every kind dispatch://INFRA-1149 registers, and an interaction.
	comparesCredential comparison = iota
	// comparesPerson compares the person the acting credential proves with the person the node's
	// author names - a user's login or a token's owner - and the whole credential where that
	// author names none (graphstore.AuthorPersonIsSQL): a lens, a block of a lens's finding, a
	// risk and a constraint.
	comparesPerson
)

// authorIs is the comparison c states of the author a node's `made_by` column records and the
// acting credential bound at param.
func (c comparison) authorIs(column, param string) string {
	if c == comparesPerson {
		return graphstore.AuthorPersonIsSQL(column, param)
	}
	return graphstore.AuthorIsSQL(column, param)
}

// nodeVisibility is one node kind's visibility rule, the half of its registry entry that says
// who may see its nodes.
type nodeVisibility struct {
	// fromAddress rewrites the id the from side of an edge carries into the id `graph_nodes`
	// keys the node by, and is nil for every kind that spells the two the same. Only an
	// artifact differs: an edge names it by uuid where it writes one and by ref_key where it
	// is written at, and the store holds it under the ref_key its `dispatch://` address
	// spells.
	fromAddress func(expr string) string
	// compares is what the predicate compares the author of a node reserved for that author
	// with: the whole acting credential, the zero value and the comparison of every kind
	// dispatch://INFRA-1149 registers, or the person that credential proves, which
	// personAuthoredKind states.
	compares comparison
}

// artifactStoreAddress is the ref_key of the artifact whose uuid expr names.
func artifactStoreAddress(expr string) string {
	return `(select store_artifact.ref_key from artifacts store_artifact where store_artifact.id::text = ` + expr + `)`
}

// VisibilityMarker opens every predicate the registry renders and names the kinds that
// predicate covers. It is what proves a read binds the registry rather than writing its own
// test: a statement whose `graph_nodes` reads are all marked is a statement whose visibility
// rules all came from here (TestEveryNodeKindAnswersAVisibilityPredicate).
const VisibilityMarker = "/* visibility predicate: "

// VisibilityKinds is every node kind the registry holds a predicate for, sorted.
func VisibilityKinds() []string {
	return slices.Sorted(maps.Keys(nodeRegistry))
}

// ResolvesNodeKind is whether a read of the graph answers nodes of kind at all: the registry
// holds a loader for it (nodeKinds). A kind it does not resolve is the address of no node.
func ResolvesNodeKind(kind string) bool {
	return slices.Contains(nodeKinds, kind)
}

// VisibilityArg is the one argument every rendered predicate binds: the acting credential, as
// graphstore's one author rule compares it, with the person that credential proves beside it,
// which the kinds that compare the person read (graphstore.ActingCredential). A read binds it
// once and names its placeholder in each predicate it renders, whatever kinds those are.
func VisibilityArg(who model.Actor) any {
	return graphstore.ActingCredential(who)
}

// VisibleKind is the predicate of one node kind: the SQL that is true of a node of that kind
// the acting person may see. address is the SQL naming the node's id as `graph_nodes` keys it,
// a column of the read's own query, and param the placeholder the acting credential is bound at
// (VisibilityArg). A node reserved for its author is compared with the acting credential as the
// kind's registry entry states (nodeVisibility.compares).
//
// A node the store holds no row for is visible: `graph_nodes` is the store's own list and the
// projections are still the ones most reads walk, so a node no write has recorded there is as
// project-visible as every node Dispatch wrote before this store existed.
//
// A kind the registry does not hold answers false, so a read that named one lists nothing
// rather than everything: a node kind a newer build adds is addressable, listed and counted
// only once it has an entry here, exactly as that entry's loader decides whether the kind
// resolves at all.
// TestEveryNodeKindAnswersAVisibilityPredicate is what keeps that branch unreachable.
func VisibleKind(kind, address, param string) string {
	entry, held := nodeRegistry[kind]
	if !held {
		return "false"
	}
	return VisibilityMarker + kind + ` */` +
		visibleWhere(`'`+kind+`'`, address, entry.visibility.compares.authorIs(`visible_node.made_by`, param))
}

// StoredNodeMarker opens the one read of `visibility` that is no test of it: the column a loader
// selects to answer a node's own stored state and visibility (storedNode). It decides nothing
// about which nodes a read answers, which the kind's predicate has already decided, so the census
// of the registry (TestEveryNodeKindAnswersAVisibilityPredicate) tells it from a predicate by
// this marker, and still holds every other `visibility` a statement reads to a predicate the
// registry rendered.
const StoredNodeMarker = "/* the node's stored state and visibility */"

// storedNode is the column every loader of kind selects for the node whose id address names, as
// `graph_nodes` keys it - the address that kind's predicate reads (VisibleKind): the state and
// the visibility the store holds for the node, as a text array of two, or null where the store
// holds no row of it (setStored). It reads `visibility` once, which StoredNodeMarker accounts
// for.
//
// It is a column and not a test, so it runs only for a row the read's own filters kept: one more
// probe of the `graph_nodes` primary key for each node a read answers, beside the predicate's own
// probe of the same row. That is a choice. Folding the two into one read of the row would move
// every loader's predicate from VisibleKind's scalar sub-plan to a join over the row it holds
// (VisibleKindRow): a rewrite of each loader's plan to save one index probe per answered node,
// 8,001 of them on a read that answers every block of an 8,001-block document.
func storedNode(kind, address string) string {
	return StoredNodeOf(`'`+kind+`'`, address)
}

// StoredNodeOf is storedNode for a node whose kind is the SQL kindExpr rather than a literal, for
// a read outside this package that answers each node's stored state and visibility beside rows
// of several kinds (api's person page): the same column, opened by the same marker. Its answer
// is read by StoredStateAndVisibility.
func StoredNodeOf(kindExpr, address string) string {
	return StoredNodeMarker + `(select array[stored_node.state, stored_node.visibility] from graph_nodes stored_node
		where stored_node.kind = ` + kindExpr + ` and stored_node.id = ` + address + `)`
}

// StoredStateAndVisibility is the state and the visibility a storedNode column read for a node,
// and live and project where the store holds no row of it, as VisibleKind reads such a node:
// `graph_nodes` is the store's own list, and a node no write has recorded there is as live and as
// project-visible as every node Dispatch wrote before the store existed. A row is two values; ok
// is false for anything else.
func StoredStateAndVisibility(stored []string) (state, visibility string, ok bool) {
	switch len(stored) {
	case 0:
		return graphstore.StateLive, graphstore.VisibilityProject, true
	case 2:
		return stored[0], stored[1], true
	}
	return "", "", false
}

// setStored gives node the state and visibility storedNode read for it
// (StoredStateAndVisibility), and fails the read, naming the node, for a column that is not one.
func setStored(node *model.GraphNode, stored []string) error {
	state, visibility, ok := StoredStateAndVisibility(stored)
	if !ok {
		return fmt.Errorf("read the stored state and visibility of %s %s: %d values, want 2", node.Kind, node.ID, len(stored))
	}
	node.State, node.Visibility = state, visibility
	return nil
}

// VisibleEnd is the predicate over an end of an edge, whose node kind is a column rather than a
// literal: one read of `graph_nodes` keyed on that column and on the id beside it, with the
// address rewrites the registry declares applied per kind, and the comparison of the kind of the
// row it reads (authorIsOfRow). byRefKey says the ids are the ones the to side of an edge
// carries, which are already the ids the store keys a node by; the from side carries an
// artifact's uuid instead, and that kind's rewrite translates it.
//
// A kind the registry does not hold passes: such an end resolves to no node and the read drops
// its edge anyway (resolveNodes), and refusing here would refuse the whole read for one row a
// newer build wrote.
func VisibleEnd(kindExpr, idExpr string, byRefKey bool, param string) string {
	address := idExpr
	if !byRefKey {
		var rewrites strings.Builder
		for _, kind := range VisibilityKinds() {
			rewrite := nodeRegistry[kind].visibility.fromAddress
			if rewrite == nil {
				continue
			}
			rewrites.WriteString(` when '` + kind + `' then ` + rewrite(idExpr))
		}
		if rewrites.Len() > 0 {
			address = `case ` + kindExpr + rewrites.String() + ` else ` + idExpr + ` end`
		}
	}
	return VisibilityMarker + `every kind */` + visibleWhere(kindExpr, address, authorIsOfRow(`visible_node`, param))
}

// VisibleNode is the predicate of a node whose kind is a column of the read's own query rather
// than a literal, named as the store keys it - the far end of a tuple, say: kindExpr and idExpr
// are its kind and its id as `graph_nodes` holds them. It is the predicate of the node's own kind,
// with that kind's comparison (authorIsOfRow), and false for a node whose kind the registry does
// not hold, as VisibleKind refuses one, so a node of a kind a newer build adds counts for nothing
// here until its entry does. A block's kind is read off its id (graphstore.AddressKind): a finding
// of a lens is a lens_block, a block of a document a block.
//
// A node the store holds no row for is visible, as VisibleKind reads one.
func VisibleNode(kindExpr, idExpr, param string) string {
	return VisibilityMarker + `the row's own kind */(` + graphstore.AddressKindSQL(kindExpr, idExpr) + ` in (` +
		quotedKinds(VisibilityKinds()) + `) and ` + visibleWhere(kindExpr, idExpr, authorIsOfRow(`visible_node`, param)) + `)`
}

// VisibleRow is the predicate over a `graph_nodes` row a read already holds, for the one read
// that walks the store's own table (loadStoreNodes): the same test, with the comparison of the
// row's own kind (authorIsOfRow) and no second read of the table to make it.
func VisibleRow(alias, param string) string {
	return VisibilityMarker + `the row's own kind */` + visibleHeldRow(alias, authorIsOfRow(alias, param))
}

// VisibleKindRow is one node kind's predicate over a `graph_nodes` row a read already holds:
// the test VisibleKind makes, over the row a read's own join or `exists` has already found, so
// a read that must read the node's row anyway makes both tests out of one read of it - the
// count's tuple arms, which answer no edge of a node whose state is gone, are that read
// (backlink_counts.go's liveVisibleNode). A kind the registry does not hold answers false,
// exactly as VisibleKind refuses it, so a read over a kind with no predicate counts nothing
// rather than everything.
//
// The rule VisibleKind states for a node the store holds no row for - that it is visible - is
// the one difference, and it is the caller's to keep: a test over a row is a test of a row that
// is there, and a read binds this only where it answers nothing for a node that has none.
func VisibleKindRow(kind, alias, param string) string {
	entry, held := nodeRegistry[kind]
	if !held {
		return "false"
	}
	return VisibilityMarker + kind + ` */` + visibleHeldRow(alias, entry.visibility.compares.authorIs(alias+`.made_by`, param))
}

// visibleHeldRow is the body both of those render: the row itself says whether the acting
// person may see the node, with no read of the table to find it. authorIs is the comparison of
// the row's author with the acting credential that its kind states.
func visibleHeldRow(alias, authorIs string) string {
	return `(` + alias + `.visibility <> '` + graphstore.VisibilityAuthor + `' or ` + authorIs + `)`
}

// visibleWhere is the body every rendered predicate shares: the store's row for the node, if it
// holds one, says whether the acting person may see it. authorIs is the comparison of that row's
// author, visible_node.made_by, with the acting credential that the node's kind states.
//
// It is a scalar subquery and not an `exists`, so the planner keeps it a sub-plan over the
// `graph_nodes` primary key rather than flattening it into an anti-join that reads the whole
// table: every read that binds this is keyed on an index of its own, and a count that gained a
// sequential scan of the store would be paid on every list.
func visibleWhere(kindExpr, address, authorIs string) string {
	return `coalesce((select visible_node.visibility <> '` + graphstore.VisibilityAuthor + `' or ` + authorIs + `
		from graph_nodes visible_node where visible_node.kind = ` + kindExpr + ` and visible_node.id = ` + address + `), true)`
}

// authorIsOfRow is the comparison of the author of the `graph_nodes` row alias names with the
// acting credential bound at param, by the row's own kind: the person for a kind whose entry
// compares the person (personComparedKinds), with a block's kind read off its id
// (graphstore.AddressKindSQL), and the whole credential for every other kind. While no kind
// compares the person, it is the whole credential's comparison alone, with no test of the kind.
func authorIsOfRow(alias, param string) string {
	credential := graphstore.AuthorIsSQL(alias+`.made_by`, param)
	if len(personComparedKinds) == 0 {
		return credential
	}
	return `(case when ` + graphstore.AddressKindSQL(alias+`.kind`, alias+`.id`) + ` in (` + quotedKinds(personComparedKinds) +
		`) then ` + graphstore.AuthorPersonIsSQL(alias+`.made_by`, param) + ` else ` + credential + ` end)`
}

// quotedKinds is kinds as the SQL literals of an `in` list. Every kind is a constant of this
// build, never a value a request carried.
func quotedKinds(kinds []string) string {
	quoted := make([]string, len(kinds))
	for index, kind := range kinds {
		quoted[index] = `'` + kind + `'`
	}
	return strings.Join(quoted, ", ")
}
