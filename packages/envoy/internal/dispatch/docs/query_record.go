package docs

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
)

// An approval covers the digest of one query's record. A query is a filter of the
// issue's one tree: it walks the tree in the tree's own order, keeps the nodes it takes, and writes
// each in full into a record whose sha256 is the digest. One walk and one builder answer the head
// and every version: the head is read at the newest seq the tuple log holds, a version at the
// read_seq its record was read at (refs.TreeCut), and nothing else tells the two apart.

// The type names the query definitions read: the memo is a callout, and a result stands under a
// check.
const (
	memoType  = "callout"
	checkType = graphstore.BlockTypeCheck
)

// queryDefinitions is the one definition of each query whose record is built: which of the walk's
// nodes it takes, at their index in the walk. Each takes nodes of the types graphstore.QueryTakes
// lists for it (the takes value its query node carries), by the rule it adds. The spec takes the
// memo - the issue's first child in the tree's order, when that child is a callout; no other
// callout is the memo, and none enters the query - and the criteria and the requirements; on an
// issue that is a view (IssueTree.ViewOf) it takes the memo alone, so a requirement a client
// brought into a view through its room is no node of the view's spec and no part of the record an
// approval of it covers (R23). The verification takes the checks and the results under them. The
// plan takes the plan nodes. The decisions query takes asks, decisions and readings by a rule of
// its own (decisionsTakes), which the tree read's decisions view answers its nodes by (Takes); it
// holds no entry here until its record is built, so a record of it is refused, as a record of a
// query with no entry is.
var queryDefinitions = map[string]func(tree IssueTree, index int) bool{
	graphstore.QuerySpec: func(tree IssueTree, index int) bool {
		if tree.ViewOf != "" {
			return index == tree.Memo
		}
		typ := tree.Nodes[index].Type
		return slices.Contains(queryTakes[graphstore.QuerySpec], typ) && (typ != memoType || index == tree.Memo)
	},
	graphstore.QueryVerification: func(tree IssueTree, index int) bool {
		node := tree.Nodes[index]
		if !slices.Contains(queryTakes[graphstore.QueryVerification], node.Type) {
			return false
		}
		if node.Type != graphstore.SupertagResult {
			return true
		}
		parent, held := tree.index[node.Parent]
		return held && tree.Nodes[parent].Type == checkType
	},
	graphstore.QueryPlan: func(tree IssueTree, index int) bool {
		return slices.Contains(queryTakes[graphstore.QueryPlan], tree.Nodes[index].Type)
	},
}

// queryTakes is graphstore.QueryTakes of each query a definition holds, and of the decisions
// query, whose rule is decisionsTakes, read once.
var queryTakes = map[string][]string{
	graphstore.QuerySpec:         graphstore.QueryTakes(graphstore.QuerySpec),
	graphstore.QueryVerification: graphstore.QueryTakes(graphstore.QueryVerification),
	graphstore.QueryPlan:         graphstore.QueryTakes(graphstore.QueryPlan),
	graphstore.QueryDecisions:    graphstore.QueryTakes(graphstore.QueryDecisions),
}

// decisionsTakes is the decisions query's rule: of the types graphstore.QueryTakes lists for it,
// each ask by the ask's own node, never the block that shows it in a document; each decision; and
// each reading whose parent in the walk is a decision, the reading under an agent's decision.
func decisionsTakes(tree IssueTree, index int) bool {
	node := tree.Nodes[index]
	if !slices.Contains(queryTakes[graphstore.QueryDecisions], node.Type) {
		return false
	}
	switch node.Type {
	case graphstore.SupertagAsk:
		return node.Kind == graphstore.KindAsk
	case refs.ReadingSupertag:
		parent, held := tree.index[node.Parent]
		return held && tree.Nodes[parent].Type == refs.DecisionSupertag
	}
	return true
}

// TreeNode is one node of an issue's tree as a walk takes it: its kind and its id, as the store
// keys it; the node it stands under in the walk; its type (refs.TreeNodeType: its supertag, or for
// a block the typed block its text opens), which a query takes it by; its rank, and whether it
// holds one; when it was made, which orders it among its unranked siblings; and whether it stands
// in the walk's second part, under a node of an issue above or in such a node's subtree.
type TreeNode struct {
	Kind, ID string
	Parent   refs.Target
	Type     string
	Rank     string
	Ranked   bool
	MadeAt   time.Time
	Above    bool
}

// Address is the node's store address, `<kind>:<id>`, which no other node holds: the record names
// a node and each far end by it, and the walk compares two by it, byte for byte, where nothing else
// tells them apart.
func (node TreeNode) Address() string { return node.Kind + ":" + node.ID }

func (node TreeNode) target() refs.Target { return refs.Target{Kind: node.Kind, ID: node.ID} }

// IssueTree is the walk of one issue's tree at one cut: the issue's key, the cut, every node in
// the walk's order - the issue's own node first - the index of the memo among them, -1 where the
// issue's first child is no callout, and the issue it is a view of (ViewOf), "" where it is none.
type IssueTree struct {
	Issue  string
	Cut    refs.TreeCut
	Nodes  []TreeNode
	Memo   int
	ViewOf string
	index  map[refs.Target]int
}

// WalkIssueTree walks the tree of the issue issueKey at cut, the head's where cut is zero
// (refs.HeadCut), in two parts.
//
// The first part starts at the issue's node and takes only the issue's own nodes, each before its
// children. A node's children are the nodes whose part_of names it at the cut and, for the issue's
// own node, each ask, comment and message of the issue that the conversation test passes at the
// cut (refs.ConversationNodes) whose loader names no parent that passes it too. An item whose loader
// names such a parent hangs under it: a reply under its head, a comment in an ask's thread under
// the ask, a reply to a block under that block. A comment written on a document whose anchor lies
// inside a typed block hangs under that block's node - the anchor's own block where it is typed,
// else its nearest typed ancestor - read through the comment's anchored_in at the cut and, where
// that address is no node of the walk, through the moved_to tuples that carry it to one (the
// node in a converted issue's room); every other item hangs under the issue. Under a conversation
// item, its own blocks come first, in their position order. Then, under every node, the ranked
// children by increasing rank and the unranked ones - an ask, a comment, a message, a lens, a risk,
// a result - by when each was made, an item whose row has no node yet by its row's created_at,
// each tie by the lower address. A rank and an address compare byte for byte. The walk does not
// enter an issue under the issue, nor a node of another issue, so a parent's record holds no node
// of a child.
//
// The second part takes each node of the issue whose parent is a node of an issue above
// (refs.IssueAncestors, refs.TreePlacedAbove), by the parent's address, then rank, then its own
// address, each with its subtree walked as the first part walks it. The walk of the issue above
// passes over such a node.
//
// The walk binds no reader's visibility: it is what the record a person approves is built from,
// and what every gate compares a review's digest with, so it is the store as the server holds it,
// one answer for every reader. Were it a reader's, two readers of one version would read two
// digests, and a node hidden from the reader who asked would drop out of the record its approver
// read. A render of the tree leaves out what its reader may not see; the record does not.
func WalkIssueTree(ctx context.Context, q Queryer, issueKey string, cut refs.TreeCut) (IssueTree, error) {
	if cut == (refs.TreeCut{}) {
		head, err := refs.HeadCut(ctx, q)
		if err != nil {
			return IssueTree{}, err
		}
		cut = head
	}
	walk := treeWalk{
		issue:    refs.Target{Kind: graphstore.KindIssue, ID: issueKey},
		owner:    issueKey,
		cut:      cut,
		nodes:    map[refs.Target]TreeNode{},
		children: map[refs.Target][]refs.Target{},
		blocks:   map[refs.Target][]refs.Target{},
	}
	if err := walk.readConversation(ctx, q); err != nil {
		return IssueTree{}, err
	}
	frontier := append([]refs.Target{walk.issue}, walk.conversation...)
	if err := walk.readSubtrees(ctx, q, frontier, false); err != nil {
		return IssueTree{}, err
	}
	ancestors, err := refs.IssueAncestors(ctx, q, issueKey)
	if err != nil {
		return IssueTree{}, fmt.Errorf("read the issues above %s: %w", issueKey, err)
	}
	above, err := refs.TreePlacedAbove(ctx, q, issueKey, ancestors, cut)
	if err != nil {
		return IssueTree{}, err
	}
	var roots []refs.Target
	for _, child := range above {
		if _, seen := walk.nodes[child.Node]; seen {
			continue
		}
		walk.nodes[child.Node] = treeNodeOf(child, true)
		roots = append(roots, child.Node)
	}
	if err := walk.readSubtrees(ctx, q, roots, true); err != nil {
		return IssueTree{}, err
	}
	if err := walk.placeItems(ctx, q); err != nil {
		return IssueTree{}, err
	}

	tree := IssueTree{Issue: issueKey, Cut: cut, Memo: -1, index: map[refs.Target]int{}}
	// Whether the issue is a view is no fact of the tree: it is read from the issue's record as it
	// stands (ViewOf), as every reader of it reads it, whatever the cut.
	if tree.ViewOf, err = ViewOf(ctx, q, issueKey); err != nil {
		return IssueTree{}, err
	}
	walk.nodes[walk.issue] = TreeNode{Kind: walk.issue.Kind, ID: walk.issue.ID, Type: graphstore.SupertagIssue}
	walk.take(&tree, walk.issue)
	if first := walk.ordered(walk.issue); len(first) > 0 && walk.nodes[first[0]].Type == memoType {
		tree.Memo = 1
	}
	for _, root := range roots {
		walk.take(&tree, root)
	}
	return tree, nil
}

// walkDocumentTree is the walk of the tree of a document no issue holds - a project's document, a
// conversation's file - at cut, in the walk's order: the document's own node first and then its
// part_of subtree, walked as the first part of WalkIssueTree walks an issue's, every node of it
// one whose address begins with the owner its ref_key begins with, the project's key or the
// conversation's (graphstore.ArtifactRefKey). No conversation hangs under such a document, and no
// issue's walk has a second part there.
func walkDocumentTree(ctx context.Context, q Queryer, refKey string, cut refs.TreeCut) ([]TreeNode, error) {
	owner, _, _ := strings.Cut(refKey, "/")
	document := refs.Target{Kind: graphstore.KindArtifact, ID: refKey}
	walk := treeWalk{
		issue:    document,
		owner:    owner,
		cut:      cut,
		nodes:    map[refs.Target]TreeNode{},
		children: map[refs.Target][]refs.Target{},
		blocks:   map[refs.Target][]refs.Target{},
		hanging:  map[refs.Target][]refs.Target{},
	}
	if err := walk.readSubtrees(ctx, q, []refs.Target{document}, false); err != nil {
		return nil, err
	}
	tree := IssueTree{Cut: cut, Memo: -1, index: map[refs.Target]int{}}
	walk.nodes[document] = TreeNode{Kind: document.Kind, ID: document.ID, Type: graphstore.SupertagDocument}
	walk.take(&tree, document)
	return tree.Nodes, nil
}

// treeWalk is what a walk has read of the issue's tree: its root, the issue's node, and the owner
// every address of the issue's own nodes begins with, the issue's key (ownNode); every node it
// took, by its address; the part_of children of each node, ranked first, in the order the children
// read answers them; the conversation's items and their blocks, in the order the conversation read
// answers them, the items alone, the blocks of each item, and the parent each item's loader names;
// and the items that hang under each node.
type treeWalk struct {
	issue        refs.Target
	owner        string
	cut          refs.TreeCut
	nodes        map[refs.Target]TreeNode
	children     map[refs.Target][]refs.Target
	conversation []refs.Target
	items        []refs.Target
	blocks       map[refs.Target][]refs.Target
	loaded       map[refs.Target]*model.NodeParent
	hanging      map[refs.Target][]refs.Target
}

// readConversation reads the issue's conversation at the walk's cut (refs.ConversationNodes): each
// item and each of its blocks as a node of the walk, the blocks of each item in their order, and
// the parent each item's loader names. Every item, and every block of one, is a node part_of may
// place a node under, so each starts a subtree of its own.
//
// It reads as the server sees the store: no ask, comment or message is a node the store takes
// reserved for its author (graphstore.AdmitAuthorOnly), so no reader's predicate leaves one out,
// and the empty actor is the reader.
func (w *treeWalk) readConversation(ctx context.Context, q Queryer) error {
	conversation, err := refs.ConversationNodes(ctx, q, model.Actor{}, w.issue.ID, w.cut.At)
	if err != nil {
		return fmt.Errorf("read the conversation of %s: %w", w.issue.ID, err)
	}
	w.loaded = map[refs.Target]*model.NodeParent{}
	for _, node := range conversation {
		if kind, itemID, _, ok := refs.ParseItemBlockNode(node.ID); ok {
			block := refs.Target{Kind: kind.BlockKind(), ID: node.ID}
			item := refs.Target{Kind: string(kind), ID: itemID}
			w.blocks[item] = append(w.blocks[item], block)
			w.nodes[block] = TreeNode{Kind: block.Kind, ID: block.ID, Parent: item, Type: block.Kind}
			w.conversation = append(w.conversation, block)
			continue
		}
		item := refs.Target{Kind: node.Kind, ID: node.ID}
		made := time.Time{}
		if node.MadeAt != nil {
			made = *node.MadeAt
		}
		w.nodes[item] = TreeNode{Kind: item.Kind, ID: item.ID, Type: item.Kind, MadeAt: made}
		w.loaded[item] = node.Parent
		w.conversation = append(w.conversation, item)
		w.items = append(w.items, item)
	}
	rows, err := refs.TreeNodeRows(ctx, q, w.items)
	if err != nil {
		return err
	}
	for item, row := range rows {
		node := w.nodes[item]
		node.Type, node.MadeAt = refs.TreeNodeType(item.Kind, row.Supertag, ""), row.MadeAt
		w.nodes[item] = node
	}
	return nil
}

// readSubtrees reads, level by level, the part_of children of every node of frontier and of each
// child it takes, at the walk's cut, taking each node once. It passes over a node of another
// issue, and over an issue's node, and so over everything under either. above marks each node it
// takes as one of the walk's second part.
func (w *treeWalk) readSubtrees(ctx context.Context, q Queryer, frontier []refs.Target, above bool) error {
	for len(frontier) > 0 {
		children, err := refs.TreeChildren(ctx, q, frontier, w.cut)
		if err != nil {
			return err
		}
		frontier = frontier[:0:0]
		for _, child := range children {
			if _, seen := w.nodes[child.Node]; seen || !w.ownNode(child.Node) {
				continue
			}
			w.nodes[child.Node] = treeNodeOf(child, above)
			w.children[child.Parent] = append(w.children[child.Parent], child.Node)
			frontier = append(frontier, child.Node)
		}
	}
	return nil
}

// treeNodeOf is the node child names, as the walk takes it.
func treeNodeOf(child refs.TreeChild, above bool) TreeNode {
	return TreeNode{
		Kind: child.Node.Kind, ID: child.Node.ID, Parent: child.Parent, Type: child.Type,
		Rank: child.Rank, Ranked: child.Ranked, MadeAt: child.MadeAt, Above: above,
	}
}

// ownNode reports whether node is a node of the walk's issue as its address tells: an issue's node
// is never one, and a document's, a version's or a document block's is one where the walk's owner,
// the issue's key, is the first part of its address, the document's ref_key
// (graphstore.ArtifactRefKey). A finding of a lens and every node whose address names no issue - a
// lens, a risk, a result - is the issue's where the walk reaches it.
func (w *treeWalk) ownNode(node refs.Target) bool {
	switch node.Kind {
	case graphstore.KindIssue:
		return false
	case graphstore.KindArtifact, graphstore.KindVersion, refs.BlockNodeKind:
		if _, _, finding := graphstore.ParseFindingID(node.ID); finding {
			return true
		}
		owner, _, _ := strings.Cut(node.ID, "/")
		return owner == w.owner
	}
	return true
}

// placeItems hangs each conversation item under its parent (WalkIssueTree): the parent its loader
// names where that is a node of the conversation, for a comment written on a document the typed
// block its anchor lies inside, and else the issue.
func (w *treeWalk) placeItems(ctx context.Context, q Queryer) error {
	w.hanging = map[refs.Target][]refs.Target{}
	var anchored []refs.Target
	for _, item := range w.items {
		loaded, held := w.loaded[item]
		if !held {
			continue
		}
		if parent, conversation := w.conversationParent(loaded); conversation {
			w.hang(item, parent)
			continue
		}
		if item.Kind == graphstore.KindComment && loaded != nil && loaded.Kind == graphstore.KindArtifact {
			anchored = append(anchored, item)
			continue
		}
		w.hang(item, w.issue)
	}
	if len(anchored) == 0 {
		return nil
	}
	facts, err := refs.TreeFactsOf(ctx, q, anchored, w.cut)
	if err != nil {
		return err
	}
	addresses := map[refs.Target]string{}
	var unheld []string
	for _, comment := range anchored {
		for _, relation := range facts[comment].Relations {
			if relation.Kind != graphstore.TupleAnchoredIn || relation.To.Kind != refs.BlockNodeKind {
				continue
			}
			addresses[comment] = relation.To.ID
			if _, held := w.nodes[relation.To]; !held {
				unheld = append(unheld, relation.To.ID)
			}
		}
	}
	moved, err := refs.TreeMovedTo(ctx, q, unheld, w.cut)
	if err != nil {
		return err
	}
	for _, comment := range anchored {
		w.hang(comment, w.anchorParent(addresses[comment], moved))
	}
	return nil
}

// conversationParent is the node a loader's parent names, and whether it is a node of the walk's
// conversation, which only a node the conversation test passes is: an item, by its kind, or a current block of
// one, which a loader names as a block (`<kind>/<uuid>#<block id>`).
func (w *treeWalk) conversationParent(loaded *model.NodeParent) (refs.Target, bool) {
	if loaded == nil {
		return refs.Target{}, false
	}
	if kind, item, _, ok := refs.ParseItemBlockNode(loaded.ID); ok && loaded.Kind == refs.BlockNodeKind {
		block := refs.Target{Kind: kind.BlockKind(), ID: loaded.ID}
		return block, slices.Contains(w.blocks[refs.Target{Kind: string(kind), ID: item}], block)
	}
	parent := refs.Target{Kind: loaded.Kind, ID: loaded.ID}
	_, item := w.loaded[parent]
	return parent, item
}

// anchorParent is the node a comment anchored at the block address hangs under: the nearest typed
// block at or above the node the address names, or the node its moves reach (moved), and the
// issue where none is a node of the walk or none of them is typed.
func (w *treeWalk) anchorParent(address string, moved map[string][]string) refs.Target {
	if address == "" {
		return w.issue
	}
	start := refs.Target{Kind: refs.BlockNodeKind, ID: address}
	if _, held := w.nodes[start]; !held {
		start = refs.Target{}
		for _, reached := range moved[address] {
			if _, held := w.nodes[refs.Target{Kind: refs.BlockNodeKind, ID: reached}]; held {
				start = refs.Target{Kind: refs.BlockNodeKind, ID: reached}
				break
			}
		}
	}
	for at, steps := start, 0; at != (refs.Target{}) && at != w.issue && steps <= len(w.nodes); steps++ {
		node, held := w.nodes[at]
		if !held {
			break
		}
		if node.Kind == refs.BlockNodeKind && pmdoc.IsTypedBlock(node.Type) {
			return at
		}
		at = node.Parent
	}
	return w.issue
}

// hang hangs item under parent.
func (w *treeWalk) hang(item, parent refs.Target) {
	node := w.nodes[item]
	node.Parent = parent
	w.nodes[item] = node
	w.hanging[parent] = append(w.hanging[parent], item)
}

// ordered is the children of parent in the walk's order: a conversation item's own blocks first,
// in their position order; then its ranked children, in the order the children read answered them,
// by rank and then address; then every unranked child and every item that hangs under it, by when
// each was made and then by address.
func (w *treeWalk) ordered(parent refs.Target) []refs.Target {
	children := slices.Clone(w.blocks[parent])
	var unranked []refs.Target
	for _, child := range w.children[parent] {
		if w.nodes[child].Ranked {
			children = append(children, child)
			continue
		}
		unranked = append(unranked, child)
	}
	unranked = append(unranked, w.hanging[parent]...)
	slices.SortStableFunc(unranked, func(a, b refs.Target) int {
		return cmp.Or(w.nodes[a].MadeAt.Compare(w.nodes[b].MadeAt), strings.Compare(a.Kind+":"+a.ID, b.Kind+":"+b.ID))
	})
	return append(children, unranked...)
}

// take appends node and its subtree to tree in the walk's order, each node once.
func (w *treeWalk) take(tree *IssueTree, node refs.Target) {
	if _, taken := tree.index[node]; taken {
		return
	}
	tree.index[node] = len(tree.Nodes)
	tree.Nodes = append(tree.Nodes, w.nodes[node])
	for _, child := range w.ordered(node) {
		w.take(tree, child)
	}
}

// QueryRecord is the record of one query of an issue at one cut: the query, the nodes it takes in
// the walk's order, the record's bytes, its digest - the lower-case hex sha256 of the bytes - and
// the cut it was read at, whose ReadSeq is the highest graph_tuples.seq it read.
type QueryRecord struct {
	Query  string
	Nodes  []TreeNode
	Record []byte
	Digest string
	Cut    refs.TreeCut
}

// ReadQueryRecord is the record of query of the issue issueKey at cut, the head's where cut is
// zero: the walk of the issue's tree (WalkIssueTree) and the one builder of its record
// (IssueTree.QueryRecord).
func ReadQueryRecord(ctx context.Context, q Queryer, issueKey, query string, cut refs.TreeCut) (QueryRecord, error) {
	if _, defined := queryDefinitions[query]; !defined {
		return QueryRecord{}, fmt.Errorf("the query %q has no record", query)
	}
	tree, err := WalkIssueTree(ctx, q, issueKey, cut)
	if err != nil {
		return QueryRecord{}, err
	}
	return tree.QueryRecord(ctx, q, query)
}

// Takes is the index in the walk of each node the query takes, in the walk's order, by the one
// definition its record is built by (queryDefinitions), or for the decisions query, whose record is
// not built yet, by its rule (decisionsTakes), and false where neither names the query. The tree
// read's view of a query answers these nodes as the query's.
func (tree IssueTree) Takes(query string) ([]int, bool) {
	takes, defined := queryDefinitions[query]
	if !defined && query == graphstore.QueryDecisions {
		takes, defined = decisionsTakes, true
	}
	if !defined {
		return nil, false
	}
	var taken []int
	for index := range tree.Nodes {
		if takes(tree, index) {
			taken = append(taken, index)
		}
	}
	return taken, true
}

// recordLeftOut are the values a record writes no field of among the node's values. It leaves out
// a value only where another field of the record carries it: the rank, written in a field of its
// own, and the text, written as the body, since a value written twice would let two builders write
// two digests. Every other value - the node's visibility, its state and its placement among them -
// is among the values, so a change of any of them moves the digest.
var recordLeftOut = []string{refs.AttributeRank, graphstore.AttributeText}

// QueryRecord is the record of query over the walk: for each node the
// query takes, in the walk's order, seven fields - the node's address, its type, its parent's
// address, its rank, its values, its body text, and the relations out of it - each written as its
// byte length, one space, then its bytes, so no text inside a field can imitate a boundary. The
// values field holds, for each value in its attribute's name order (recordLeftOut aside), the
// attribute's name and the value, each a field; the relations field holds, for each relation by
// its kind and then its far end, the kind and the far end's address, each a field. The values and
// the relations are read at the walk's cut, as the server sees the store.
func (tree IssueTree) QueryRecord(ctx context.Context, q Queryer, query string) (QueryRecord, error) {
	takes, defined := queryDefinitions[query]
	if !defined {
		return QueryRecord{}, fmt.Errorf("the query %q has no record", query)
	}
	var taken []TreeNode
	var targets []refs.Target
	for index, node := range tree.Nodes {
		if takes(tree, index) {
			taken = append(taken, node)
			targets = append(targets, node.target())
		}
	}
	facts, err := refs.TreeFactsOf(ctx, q, targets, tree.Cut)
	if err != nil {
		return QueryRecord{}, fmt.Errorf("read the %s record of %s: %w", query, tree.Issue, err)
	}
	var record, scratch bytes.Buffer
	for _, node := range taken {
		held := facts[node.target()]
		writeRecordField(&record, node.Address())
		writeRecordField(&record, node.Type)
		writeRecordField(&record, node.Parent.Kind+":"+node.Parent.ID)
		writeRecordField(&record, node.Rank)
		scratch.Reset()
		names := make([]string, 0, len(held.Values))
		for name := range held.Values {
			if !slices.Contains(recordLeftOut, name) {
				names = append(names, name)
			}
		}
		slices.Sort(names)
		for _, name := range names {
			writeRecordField(&scratch, name)
			writeRecordField(&scratch, held.Values[name])
		}
		writeRecordField(&record, scratch.String())
		writeRecordField(&record, held.Values[graphstore.AttributeText])
		scratch.Reset()
		relations := slices.Clone(held.Relations)
		slices.SortFunc(relations, func(a, b refs.TreeRelation) int {
			return cmp.Or(strings.Compare(a.Kind, b.Kind), strings.Compare(a.To.Kind+":"+a.To.ID, b.To.Kind+":"+b.To.ID))
		})
		for _, relation := range relations {
			writeRecordField(&scratch, relation.Kind)
			writeRecordField(&scratch, relation.To.Kind+":"+relation.To.ID)
		}
		writeRecordField(&record, scratch.String())
	}
	sum := sha256.Sum256(record.Bytes())
	return QueryRecord{Query: query, Nodes: taken, Record: record.Bytes(), Digest: hex.EncodeToString(sum[:]), Cut: tree.Cut}, nil
}

// writeRecordField writes field to record as its byte length, one space, then its bytes.
func writeRecordField(record *bytes.Buffer, field string) {
	record.WriteString(strconv.Itoa(len(field)))
	record.WriteByte(' ')
	record.WriteString(field)
}
