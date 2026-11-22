package api

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/asks"
	"github.com/sjawhar/envoy/internal/dispatch/decisionread"
	"github.com/sjawhar/envoy/internal/dispatch/docs"
	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
)

// The tree read (GET /api/v1/issues/{key}/tree) answers one issue's tree as one reader may see
// it, in the tree's own order: the walk the record of every query is built from
// (docs.WalkIssueTree), with the reader's visibility applied to every node it reaches, the nodes
// no query of the tree returns left out - a document, a document version, and the prose of a role
// document (R2, R16) - and the members each node answers read from the nodes and the rows that
// hold them. A view answers one query's nodes from the same walk, with the fixed context that
// query carries.

// issueTreePageSize is the most nodes one page of the tree read answers. It stands above the
// largest issue the implementer measured through Dispatch's own API (the pull request body's R8
// table: 10,910 blocks, comments, asks and messages), so one request answers that issue whole; a
// larger tree answers a cursor at the next node of the walk.
const issueTreePageSize = 12000

// issueTreeViews are the views the tree read answers, each the query of that name.
var issueTreeViews = []string{graphstore.QuerySpec, graphstore.QueryVerification, graphstore.QueryPlan, graphstore.QueryDecisions}

// The roles a node plays in a tree read (model.IssueTreeNode.Role).
const (
	treeRoleTree    = "tree"
	treeRoleQuery   = "query"
	treeRoleContext = "context"
)

// issueTreeQuery is GET /api/v1/issues/{key}/tree's query: the view, the version of the view's
// query node to read it at, and the cursor of a page past the first.
type issueTreeQuery struct {
	// View is the query whose nodes the read answers: spec, verification, plan or decisions;
	// absent for the whole tree.
	View string `query:"view" enum:"spec,verification,plan,decisions"`
	// V is a version of the view's query node: the read answers the query as of the store
	// revision that version's record read. It needs view beside it. The decisions query node
	// records no version of its own yet, so a version of it is answered as any version whose
	// record the query node does not hold, 404 QUERY_VERSION_NOT_FOUND (issueTreeCut).
	V int `query:"v"`
	// Cursor is where a page past the first starts: the cursor the page before it answered.
	Cursor string `query:"cursor"`
}

// getIssueTree is GET /api/v1/issues/{key}/tree.
func (s *server) getIssueTree(w http.ResponseWriter, r *http.Request) {
	who, ok := s.actingReader(w, r)
	if !ok {
		return
	}
	query := r.URL.Query()
	view := query.Get("view")
	if query.Has("view") && !slices.Contains(issueTreeViews, view) {
		writeError(w, "INVALID_QUERY", http.StatusBadRequest, "view must be one of spec, verification, plan and decisions, or absent for the whole tree")
		return
	}
	version := 0
	if query.Has("v") {
		if view == "" {
			writeError(w, "INVALID_QUERY", http.StatusBadRequest, "v names a version of a view's query node: give view beside it")
			return
		}
		number, err := strconv.Atoi(query.Get("v"))
		if err != nil || number < 1 {
			writeError(w, "INVALID_QUERY", http.StatusBadRequest, "v must be a version number, 1 or above")
			return
		}
		version = number
	}
	key := r.PathValue("key")
	if err := s.requireIssueExists(r.Context(), key); err != nil {
		s.writeHandlerError(w, err)
		return
	}
	trees := s.readDisplayTrees(r.Context(), wantsDisplayTrees(r))
	answer, err := s.readIssueTree(r.Context(), who, key, view, version, query.Get("cursor"), trees)
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	trees.resolve()
	WriteJSON(w, http.StatusOK, answer)
}

// readIssueTree is the tree read of the issue key for who: the view's query and its context, or
// the whole tree where view is "", at version of the view's query node or at the head where it is
// 0, from cursor on. Every read of the store is made in one read-only transaction at repeatable
// read, so the answer is one state of the store; the documents' block lists, which the live rooms
// answer, are read after it (issueTreeBuilder.documentPositions). trees is the request's reader of
// the display trees, nil for a caller that asked for none.
func (s *server) readIssueTree(ctx context.Context, who model.Actor, key, view string, version int, cursor string, trees *displayTreeReader) (model.IssueTreeRead, error) {
	tx, err := s.deps.Store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return model.IssueTreeRead{}, fmt.Errorf("open the tree read of %s: %w", key, err)
	}
	defer tx.Rollback(ctx)
	builder := &issueTreeBuilder{ctx: ctx, q: tx, who: who, key: key, view: view}
	if err := builder.build(version); err != nil {
		return model.IssueTreeRead{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return model.IssueTreeRead{}, fmt.Errorf("close the tree read of %s: %w", key, err)
	}
	if err := builder.documentPositions(s); err != nil {
		return model.IssueTreeRead{}, err
	}
	return builder.answer(cursor, version, trees)
}

// treeEntry is what the tree read holds of one node while it builds its answer.
type treeEntry struct {
	// target is the node as the store keys it.
	target refs.Target
	// walk is the node's index in the walk, -1 for a node the walk does not take.
	walk int
	// order is where a node the walk does not take was added among the others.
	order int
	// parent is the node it stands under in the store: the walk's parent, or the node a part_of
	// of its own names for a node the walk does not take.
	parent refs.Target
	typ    string
	rank   string
	ranked bool
	madeAt time.Time
	// above is a node of the walk's second part, whose parent is a node of an issue above.
	above bool
	// issue is the issue the node belongs to where it is not the issue read.
	issue string
	// role is what the node is to the answer (treeRoleTree, treeRoleQuery, treeRoleContext).
	role string
}

// blockRow is what a block node's document_blocks row says of it: its block id, its type, the
// text sum and version of its words, and the document it is a block of, with that document's
// slug and band (refs.DocumentBandSQL).
type blockRow struct {
	blockID, typ, sum string
	version           int
	document, slug    string
	band              byte
}

// issueTreeBuilder builds one tree read.
type issueTreeBuilder struct {
	ctx  context.Context
	q    pgx.Tx
	who  model.Actor
	key  string
	view string
	cut  refs.TreeCut
	tree docs.IssueTree

	entries map[refs.Target]*treeEntry
	walked  []refs.Target
	extras  []refs.Target
	context []refs.Target

	facts  map[refs.Target]refs.TreeFacts
	loaded map[refs.Target]refs.ExcerptedNode
	rows   map[refs.Target]refs.TreeNodeRow
	blocks map[string]blockRow

	kept      map[refs.Target]bool
	effective map[refs.Target]refs.Target
	order     []refs.Target
	descend   []string
	ancestors []string

	// taken is each node of the walk the view's query takes (queryTakes), requirements each
	// requirement the verification view carries (verificationRequirements), and tied each node
	// of the walk or the others the spec and the verification views carry as context for a tie of
	// theirs (readTies), each read once by readContext for a view.
	taken        map[refs.Target]bool
	requirements map[refs.Target]bool
	tied         map[refs.Target]bool

	issues    map[string]*model.IssueTreeIssue
	texts     map[refs.Target]string
	asks      map[string]treeAsk
	results   map[string]treeResult
	decisions map[refs.Target]model.Decision
	plans     map[refs.Target]*model.IssueTreePlan
	positions map[refs.Target]int
	// owed is each document of the issue that owes its block index (queueOwedDocuments).
	owed []model.OwedDocument
}

// treeAsk is what an ask's row says that its node does not: its question, its state, and the rest
// of its wording, its options, urgency and multiple.
type treeAsk struct {
	question, state string
	wording         model.IssueTreeAsk
}

// treeResult is what a result's check_results row and its retraction hold for the tree read: who
// recorded it and when, and its retraction where one stands.
type treeResult struct {
	actor     model.Actor
	createdAt time.Time
	retracted *model.CheckResultRetracted
}

// root is the issue read's own node.
func (b *issueTreeBuilder) root() refs.Target {
	return refs.Target{Kind: graphstore.KindIssue, ID: b.key}
}

// build reads everything of the answer the store holds, in the read's transaction.
func (b *issueTreeBuilder) build(version int) error {
	cut, err := issueTreeCut(b.ctx, b.q, b.key, b.view, version)
	if err != nil {
		return err
	}
	b.cut = cut
	if b.tree, err = docs.WalkIssueTree(b.ctx, b.q, b.key, cut); err != nil {
		return err
	}
	b.entries = map[refs.Target]*treeEntry{}
	for index, node := range b.tree.Nodes {
		target := refs.Target{Kind: node.Kind, ID: node.ID}
		b.entries[target] = &treeEntry{
			target: target, walk: index, parent: node.Parent, typ: node.Type,
			rank: node.Rank, ranked: node.Ranked, madeAt: node.MadeAt, above: node.Above,
		}
		b.walked = append(b.walked, target)
	}
	if b.ancestors, err = refs.IssueAncestors(b.ctx, b.q, b.key); err != nil {
		return err
	}
	if b.descend, err = refs.IssueDescendants(b.ctx, b.q, b.who, b.key); err != nil {
		return err
	}
	if err := b.readOthers(); err != nil {
		return err
	}
	if err := b.readFacts(b.candidates()); err != nil {
		return err
	}
	if err := b.placeOthers(); err != nil {
		return err
	}
	b.keep()
	if err := b.readContext(); err != nil {
		return err
	}
	return b.readMembers()
}

// issueTreeCut is the cut the tree read reads the store at: the head's where version is 0, and
// where it names a version, the cut the record of that version of the view's query node was read
// at (version_renders: its read_seq and its time). A version the query node does not hold a
// record of - one it does not have, or one written before its versions were rendered from the
// tree - is 404 QUERY_VERSION_NOT_FOUND.
func issueTreeCut(ctx context.Context, q pgx.Tx, key, view string, version int) (refs.TreeCut, error) {
	if version == 0 {
		return refs.HeadCut(ctx, q)
	}
	var cut refs.TreeCut
	err := q.QueryRow(ctx, `
		select r.read_seq, r.at
		from artifacts a
		join graph_nodes n on n.kind = $4 and n.id = a.ref_key and n.supertag = $5 and n.attributes->>$6 = $2
		join version_renders r on r.artifact_id = a.id and r.number = $3
		where a.issue_key = $1`,
		key, view, version, graphstore.KindArtifact, graphstore.SupertagQuery, graphstore.AttributeQuery).Scan(&cut.ReadSeq, &cut.At)
	if errors.Is(err, pgx.ErrNoRows) {
		return refs.TreeCut{}, errorf(http.StatusNotFound, "QUERY_VERSION_NOT_FOUND",
			"the %s query of %s holds no version %d whose record names the store revision it read", view, key, version)
	}
	if err != nil {
		return refs.TreeCut{}, fmt.Errorf("read version %d of the %s query of %s: %w", version, view, key, err)
	}
	return cut, nil
}

// add adds a node the walk does not take to the answer's candidates, once.
func (b *issueTreeBuilder) add(target refs.Target, parent refs.Target, issue, role string) *treeEntry {
	if entry, held := b.entries[target]; held {
		return entry
	}
	entry := &treeEntry{target: target, walk: -1, order: len(b.extras), parent: parent, issue: issue, role: role}
	b.entries[target] = entry
	b.extras = append(b.extras, target)
	return entry
}

// treeIssueOf is the issue a node's address names - a document block's, `<KEY>/<slug>#<id>`, and an
// issue's own key - and "" for a node whose address names none.
func treeIssueOf(target refs.Target) string {
	switch target.Kind {
	case graphstore.KindIssue:
		return target.ID
	case refs.BlockNodeKind:
		if _, _, finding := graphstore.ParseFindingID(target.ID); finding {
			return ""
		}
		owner, _, cut := strings.Cut(target.ID, "/")
		if cut {
			return owner
		}
	}
	return ""
}

// readOthers adds the nodes of issues under the issue read that its answer holds: each child
// issue, placed where its part_of names a node of the issue and under the issue otherwise (R2);
// each node of another issue whose part_of names a node of the walk, a child's criterion under a
// comment of the issue among them; each check and plan node of an issue under the issue whose
// serves names one of the issue's requirements, with the results under each such check, reached
// through serves (R3, R23), which the spec and the decisions views carry none of; and in the plan
// view every plan node of the whole tree's ordered read (refs.PlanOrder). Each is read-only,
// marked with its issue.
func (b *issueTreeBuilder) readOthers() error {
	rows, err := b.q.Query(b.ctx, `select key from issues where parent_key = $1 order by key`, b.key)
	if err != nil {
		return fmt.Errorf("read the children of %s: %w", b.key, err)
	}
	children, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("read the children of %s: %w", b.key, err)
	}
	for _, child := range children {
		if slices.Contains(b.descend, child) {
			b.add(refs.Target{Kind: graphstore.KindIssue, ID: child}, b.root(), child, treeRoleTree)
		}
	}
	placed, err := refs.TreeChildren(b.ctx, b.q, b.walked, b.cut)
	if err != nil {
		return err
	}
	for _, child := range placed {
		if _, walked := b.entries[child.Node]; walked && b.entries[child.Node].walk >= 0 {
			continue
		}
		owner := treeIssueOf(child.Node)
		if owner == "" || owner == b.key || !slices.Contains(b.descend, owner) {
			continue
		}
		entry := b.add(child.Node, child.Parent, owner, treeRoleTree)
		entry.parent, entry.typ, entry.rank, entry.ranked, entry.madeAt = child.Parent, child.Type, child.Rank, child.Ranked, child.MadeAt
	}
	if b.view != graphstore.QuerySpec && b.view != graphstore.QueryDecisions {
		if err := b.readServing(); err != nil {
			return err
		}
	}
	if b.view == graphstore.QueryPlan {
		return b.readPlanOrder()
	}
	return nil
}

// readServing adds each check and plan node of an issue under the issue read whose current serves
// names one of the issue's requirements, read into each requirement over graph_tuples_as_of_in
// (servesInto), and each result under such a check, each marked with its issue.
func (b *issueTreeBuilder) readServing() error {
	var requirements []string
	for _, target := range b.walked {
		if entry := b.entries[target]; entry.typ == requirementBlockType && target.Kind == refs.BlockNodeKind {
			requirements = append(requirements, target.ID)
		}
	}
	serving, err := servesInto(b.ctx, b.q, requirements, b.cut)
	if err != nil {
		return err
	}
	var checks []refs.Target
	for _, requirement := range requirements {
		for _, from := range serving[requirement] {
			owner := treeIssueOf(from)
			if owner == "" || owner == b.key || !slices.Contains(b.descend, owner) {
				continue
			}
			if _, held := b.entries[from]; held {
				continue
			}
			b.add(from, refs.Target{}, owner, treeRoleTree)
			checks = append(checks, from)
		}
	}
	if len(checks) == 0 {
		return nil
	}
	results, err := refs.TreeChildren(b.ctx, b.q, checks, b.cut)
	if err != nil {
		return err
	}
	for _, child := range results {
		if child.Node.Kind != graphstore.KindResult {
			continue
		}
		entry := b.add(child.Node, child.Parent, b.entries[child.Parent].issue, treeRoleTree)
		entry.typ, entry.madeAt = graphstore.SupertagResult, child.MadeAt
	}
	return nil
}

// tuplesIntoStatement is, at the cut whose seq bound $2 is, each current tuple of kind $3 into the
// block nodes $1 names: of each relation the newest tuple at or below the bound, read into its far
// end through graph_tuples_as_of_in, kept while it asserts.
const tuplesIntoStatement = `
	select newest.to_id, newest.from_kind, newest.from_id
	from (
		select distinct on (t.to_id, t.from_kind, t.from_id) t.to_id, t.from_kind, t.from_id, t.asserted
		from graph_tuples t
		where t.to_kind = '` + refs.BlockNodeKind + `' and t.to_id = any($1) and t.kind = $3
		  and t.to_kind <> '` + graphstore.KindValue + `' and t.seq <= $2
		order by t.to_id, t.from_kind, t.from_id, t.seq desc
	) newest
	where newest.asserted
	order by newest.to_id, newest.from_kind, newest.from_id collate "C"`

// servesInto is each node whose current serves names one of the requirement blocks requirements,
// keyed by the requirement (tuplesInto): the coverage read walks serves from the requirement, so
// one check serving three requirements is one node under each of the three.
func servesInto(ctx context.Context, q queryer, requirements []string, cut refs.TreeCut) (map[string][]refs.Target, error) {
	return tuplesInto(ctx, q, servesTupleKind, requirements, cut)
}

// tuplesInto is each node whose current tuple of kind names one of the block nodes blocks, keyed
// by the block, read at cut's seq, the head's where it is a dated cut.
func tuplesInto(ctx context.Context, q queryer, kind string, blocks []string, cut refs.TreeCut) (map[string][]refs.Target, error) {
	into := map[string][]refs.Target{}
	if len(blocks) == 0 {
		return into, nil
	}
	seq := cut.ReadSeq
	if cut.Dated || seq == 0 {
		seq = math.MaxInt64
	}
	rows, err := q.Query(ctx, tuplesIntoStatement, blocks, seq, kind)
	if err != nil {
		return nil, fmt.Errorf("read the %s into %d blocks: %w", kind, len(blocks), err)
	}
	defer rows.Close()
	for rows.Next() {
		var block string
		var from refs.Target
		if err := rows.Scan(&block, &from.Kind, &from.ID); err != nil {
			return nil, fmt.Errorf("read the %s into a block: %w", kind, err)
		}
		into[block] = append(into[block], from)
	}
	return into, rows.Err()
}

// readPlanOrder reads the ordered read of the issue's whole tree (refs.PlanOrder) for the plan
// view: each node's position, needs and size, every plan node of an issue under the issue added
// beside the issue's own, and each issue of the tree answered as its two halves.
func (b *issueTreeBuilder) readPlanOrder() error {
	order, err := refs.PlanOrder(b.ctx, b.q, b.who, b.key)
	if err != nil {
		return err
	}
	b.plans = map[refs.Target]*model.IssueTreePlan{}
	cycles := map[refs.OrderNode]bool{}
	for _, cycle := range order.Cycles {
		for _, node := range cycle {
			cycles[node.OrderNode] = true
		}
	}
	place := func(node refs.OrderedNode, position *int) {
		plan := &model.IssueTreePlan{Position: position, Needs: []model.IssueTreeHalfEnd{}, Cycle: cycles[node.OrderNode]}
		for _, need := range node.Needs {
			plan.Needs = append(plan.Needs, model.IssueTreeHalfEnd{Kind: need.Kind, ID: need.ID, Half: need.Half})
		}
		if node.Size != "" {
			size := node.Size
			plan.Size = &size
		}
		if node.Kind == graphstore.KindIssue {
			target := refs.Target{Kind: graphstore.KindIssue, ID: node.ID}
			halves := b.plans[target]
			if halves == nil {
				halves = &model.IssueTreePlan{Needs: []model.IssueTreeHalfEnd{}}
				b.plans[target] = halves
			}
			if node.Half == refs.HalfDone {
				halves.Done = plan
			} else {
				halves.Start = plan
			}
			if node.ID != b.key {
				b.add(target, refs.Target{}, node.ID, treeRoleTree)
			}
			return
		}
		target := refs.Target{Kind: node.Kind, ID: node.ID}
		b.plans[target] = plan
		if node.Issue != b.key {
			b.add(target, refs.Target{}, node.Issue, treeRoleTree)
		}
	}
	for index, node := range order.Order {
		position := index
		place(node, &position)
	}
	for _, cycle := range order.Cycles {
		for _, node := range cycle {
			place(node, nil)
		}
	}
	for _, node := range order.Overtaken {
		place(node, nil)
	}
	return nil
}

// candidates is every node the answer may hold before the context: the walk's and the others'.
func (b *issueTreeBuilder) candidates() []refs.Target {
	return slices.Concat(b.walked, b.extras)
}

// readFacts reads what each of targets states at the read's cut (refs.TreeFactsOf), each node's
// row (refs.TreeNodeRows), each block's document_blocks row, and each node as the registry's
// loader of its kind answers it for the reader (refs.ExcerptedNodes), which is the one test of
// whether the reader may see it, beside what those maps already hold.
func (b *issueTreeBuilder) readFacts(targets []refs.Target) error {
	if b.facts == nil {
		b.facts, b.loaded, b.rows, b.blocks = map[refs.Target]refs.TreeFacts{}, map[refs.Target]refs.ExcerptedNode{}, map[refs.Target]refs.TreeNodeRow{}, map[string]blockRow{}
	}
	var unread []refs.Target
	for _, target := range targets {
		if _, held := b.rows[target]; !held {
			unread = append(unread, target)
		}
	}
	if len(unread) == 0 {
		return nil
	}
	facts, err := refs.TreeFactsOf(b.ctx, b.q, unread, b.cut)
	if err != nil {
		return err
	}
	for target, held := range facts {
		b.facts[target] = held
	}
	rows, err := refs.TreeNodeRows(b.ctx, b.q, unread)
	if err != nil {
		return err
	}
	loaderTargets := make([]refs.Target, 0, len(unread))
	for _, target := range unread {
		b.rows[target] = rows[target]
		if refs.ResolvesNodeKind(loaderKind(target)) {
			loaderTargets = append(loaderTargets, refs.Target{Kind: loaderKind(target), ID: target.ID})
		}
	}
	loaded, err := refs.ExcerptedNodes(b.ctx, b.q, b.who, true, loaderTargets)
	if err != nil {
		return err
	}
	for _, target := range unread {
		if node, held := loaded[refs.Target{Kind: loaderKind(target), ID: target.ID}]; held {
			b.loaded[target] = node
		}
	}
	return b.readBlockRows(unread)
}

// loaderKind is the kind the registry resolves the node target names by: a finding of a lens,
// which the store keys as a block, is a lens_block (graphstore.AddressKind).
func loaderKind(target refs.Target) string {
	return graphstore.AddressKind(target.Kind, target.ID)
}

// readBlockRows reads the document_blocks row of each block node of targets: its block id, type,
// text sum and changed version, and the document it is a block of (refs.BlockDocumentSQL) with that
// document's slug and band (refs.DocumentBandSQL).
func (b *issueTreeBuilder) readBlockRows(targets []refs.Target) error {
	var ids []string
	for _, target := range targets {
		if target.Kind == refs.BlockNodeKind {
			ids = append(ids, target.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := b.q.Query(b.ctx, `
		select d.node_id, d.block_id, d.type, d.text_sha256, d.changed_version, doc.id::text, doc.slug, `+refs.DocumentBandSQL("doc")+`
		from document_blocks d
		cross join lateral (select `+refs.BlockDocumentSQL("d")+` as id) owner
		join artifacts doc on doc.id = owner.id
		where d.node_id = any($1)`, ids)
	if err != nil {
		return fmt.Errorf("read the rows of the blocks of %s: %w", b.key, err)
	}
	defer rows.Close()
	for rows.Next() {
		var node, band string
		var row blockRow
		if err := rows.Scan(&node, &row.blockID, &row.typ, &row.sum, &row.version, &row.document, &row.slug, &band); err != nil {
			return fmt.Errorf("read the rows of the blocks of %s: %w", b.key, err)
		}
		if band != "" {
			row.band = band[0]
		}
		b.blocks[node] = row
	}
	return rows.Err()
}

// placeOthers gives each node the walk does not take what its facts and its row say: a child
// issue its placement, where its current part_of names a node of the walk, and every such node its
// type, its rank and when it was made, where nothing read so far gave them.
func (b *issueTreeBuilder) placeOthers() error {
	for _, target := range b.extras {
		entry := b.entries[target]
		held := b.facts[target]
		if target.Kind == graphstore.KindIssue && entry.role == treeRoleTree {
			for _, relation := range held.Relations {
				if relation.Kind != graphstore.TuplePartOf {
					continue
				}
				if parent, walked := b.entries[relation.To]; walked && parent.walk >= 0 {
					entry.parent = relation.To
				}
			}
		}
		if entry.typ == "" {
			opening, _, _ := strings.Cut(held.Values[graphstore.AttributeText], "\n")
			entry.typ = refs.TreeNodeType(target.Kind, b.rows[target].Supertag, opening)
		}
		if rank, ranked := held.Values[refs.AttributeRank]; ranked && !entry.ranked {
			entry.rank, entry.ranked = rank, true
		}
		if entry.madeAt.IsZero() {
			entry.madeAt = b.rows[target].MadeAt
		}
		if entry.parent == (refs.Target{}) && entry.role != treeRoleContext {
			for _, relation := range held.Relations {
				if relation.Kind == graphstore.TuplePartOf {
					entry.parent = relation.To
				}
			}
			if entry.parent == (refs.Target{}) && entry.issue != "" && target.Kind != graphstore.KindIssue {
				entry.parent = refs.Target{Kind: graphstore.KindIssue, ID: entry.issue}
			}
		}
	}
	return nil
}

// visible reports whether the reader may see the node: the registry's loader of its kind answered
// it (refs.ExcerptedNodes binds the kind's predicate), or its kind is one no read of the graph
// resolves - a result, a document version - whose nodes are project-visible.
func (b *issueTreeBuilder) visible(target refs.Target) bool {
	if !refs.ResolvesNodeKind(loaderKind(target)) {
		return true
	}
	_, held := b.loaded[target]
	return held
}

// excluded reports whether the node is one no query of the tree returns (R2, R16): a document,
// the issue's tree among them, and a document version, which no view draws; and a block of a role
// document - the spec, the Verification document, the plan - that is no typed block, its prose,
// which stays a node of the issue that its document's versions render.
func (b *issueTreeBuilder) excluded(target refs.Target) bool {
	switch target.Kind {
	case graphstore.KindArtifact, graphstore.KindVersion:
		return true
	case refs.BlockNodeKind:
		row, held := b.blocks[target.ID]
		if !held {
			return false
		}
		switch row.band {
		case refs.RankBandSpec, refs.RankBandVerification, refs.RankBandPlan:
			return !pmdoc.IsTypedBlock(b.entries[target].typ)
		}
	}
	return false
}

// keep decides which candidates the answer holds - each the reader may see and that no query of
// the tree leaves out - and under which node each stands: its parent where the answer holds it,
// else the nearest node above it the answer holds, the issue at worst, or for a node whose parent
// is a node of an issue above, that node. The read answers nothing of a node it leaves out, so the
// answer is the one it would be if that node did not exist.
func (b *issueTreeBuilder) keep() {
	b.kept = map[refs.Target]bool{}
	for target, entry := range b.entries {
		if entry.role == treeRoleContext {
			continue
		}
		b.kept[target] = target == b.root() || b.visible(target) && !b.excluded(target)
	}
	b.effective = map[refs.Target]refs.Target{}
	for target, kept := range b.kept {
		if !kept || target == b.root() {
			continue
		}
		parent := b.entries[target].parent
		for steps := 0; steps <= len(b.entries); steps++ {
			if parent == (refs.Target{}) || parent == b.root() || b.kept[parent] {
				break
			}
			above, held := b.entries[parent]
			if !held {
				break
			}
			if above.parent == (refs.Target{}) {
				parent = b.root()
				break
			}
			parent = above.parent
		}
		if parent == (refs.Target{}) {
			parent = b.root()
		}
		b.effective[target] = parent
	}
}

// readContext adds the context nodes the answer carries, and reads what they state and whether the
// reader may see them. Every read carries each decision's decided_by person or session. The read
// with no view carries each catalogue node a has_kind or constrained_by of a node it holds names, a
// lens_kind or a constraint the tree does not hold, one level out, and each interaction a
// changed_by_run of a node it holds names, through the reader's predicate. A view carries its
// fixed context (viewContext), and the spec and the verification views the ties of their nodes
// (readTies). It reads, once, what the view's query takes and the requirements the verification
// view carries, which inView answers the view's nodes by.
func (b *issueTreeBuilder) readContext() error {
	var wanted []refs.Target
	for _, target := range b.candidates() {
		if !b.kept[target] {
			continue
		}
		for _, relation := range b.facts[target].Relations {
			if _, held := b.entries[relation.To]; held {
				continue
			}
			switch {
			case relation.Kind == graphstore.TupleDecidedBy:
			case b.view != "":
				continue
			case relation.Kind == graphstore.TupleHasKind && relation.To.Kind == graphstore.KindLensKind,
				relation.Kind == graphstore.TupleConstrainedBy && relation.To.Kind == graphstore.KindConstraint,
				relation.Kind == graphstore.TupleChangedByRun && relation.To.Kind == graphstore.KindInteraction:
			default:
				continue
			}
			if !slices.Contains(wanted, relation.To) {
				wanted = append(wanted, relation.To)
			}
		}
	}
	if b.view != "" {
		b.taken = b.queryTakes(b.view)
		if b.view == graphstore.QueryVerification {
			b.requirements = b.verificationRequirements()
		}
		wanted = append(wanted, b.viewContext()...)
	}
	if err := b.addContext(wanted); err != nil {
		return err
	}
	if b.view == graphstore.QuerySpec || b.view == graphstore.QueryVerification {
		tied, err := b.readTies()
		if err != nil {
			return err
		}
		if err := b.addContext(tied); err != nil {
			return err
		}
	}
	return b.placeContext()
}

// addContext reads what each of wanted states and whether the reader may see it, and adds each
// one the reader may see, and that the answer holds as no node of its walk or of the others, as a
// context node, once.
func (b *issueTreeBuilder) addContext(wanted []refs.Target) error {
	if err := b.readFacts(wanted); err != nil {
		return err
	}
	for _, target := range wanted {
		if !b.visible(target) {
			continue
		}
		if entry, held := b.entries[target]; held {
			if entry.role != treeRoleContext {
				continue
			}
		} else {
			entry := &treeEntry{target: target, walk: -1, issue: treeIssueOf(target), role: treeRoleContext}
			if entry.issue == b.key {
				entry.issue = ""
			}
			b.entries[target] = entry
		}
		b.kept[target] = true
		if !slices.Contains(b.context, target) {
			b.context = append(b.context, target)
		}
	}
	return nil
}

// readTies is what the spec and the verification views carry as context for the ties of the nodes
// they answer - each node their query takes that the reader may see, and their fixed context: each
// constraint a constrained_by of one of them names, and each decision, and each ask a person
// answered, whose current shaped tuple names one of them, read into it at the read's cut
// (tuplesInto). A tie's node the walk or the read of the others took - a decision or an ask of the
// issue, a constraint written on it - is marked for inView to answer as context (tied), and the
// rest are answered for addContext to add. A node a tie brings in brings in no tie of its own.
func (b *issueTreeBuilder) readTies() ([]refs.Target, error) {
	var viewed []refs.Target
	for _, target := range b.candidates() {
		if _, in := b.inView(target); in && b.kept[target] {
			viewed = append(viewed, target)
		}
	}
	viewed = append(viewed, b.context...)
	var blocks []string
	var ties []refs.Target
	for _, target := range viewed {
		if target.Kind == refs.BlockNodeKind {
			blocks = append(blocks, target.ID)
		}
		for _, relation := range b.facts[target].Relations {
			if relation.Kind == graphstore.TupleConstrainedBy && relation.To.Kind == graphstore.KindConstraint && !slices.Contains(ties, relation.To) {
				ties = append(ties, relation.To)
			}
		}
	}
	shaping, err := tuplesInto(b.ctx, b.q, refs.TupleShaped, blocks, b.cut)
	if err != nil {
		return nil, err
	}
	var shapers []refs.Target
	for _, block := range blocks {
		for _, from := range shaping[block] {
			if !slices.Contains(shapers, from) {
				shapers = append(shapers, from)
			}
		}
	}
	if err := b.readFacts(shapers); err != nil {
		return nil, err
	}
	for _, shaper := range shapers {
		decision := shaper.Kind == refs.BlockNodeKind && b.rows[shaper].Supertag == refs.DecisionSupertag
		answered := shaper.Kind == graphstore.KindAsk && b.facts[shaper].Values[graphstore.TupleAnswerState] == asks.StateAnswered
		if decision || answered {
			ties = append(ties, shaper)
		}
	}
	b.tied = map[refs.Target]bool{}
	var wanted []refs.Target
	for _, tie := range ties {
		if entry, held := b.entries[tie]; held && entry.role != treeRoleContext {
			b.tied[tie] = true
			continue
		}
		wanted = append(wanted, tie)
	}
	return wanted, nil
}

// viewContext is the fixed context of the view beyond what its query takes: on a child issue's
// spec view, each requirement of an issue above that one of the child's checks serves and the
// child's own design blocks, the blocks of its tree's own fragment that are no typed block; on
// the verification view, every requirement node of the issue, served or not, so a requirement no
// check serves is in the answer, and each other requirement a check of the answer serves - one of
// an issue above on a child, one of an issue under the issue read on a parent. The walk holds the
// issue's own requirements and the design blocks, which ordered marks as context (inView); this
// adds the requirements no walk holds, each once.
func (b *issueTreeBuilder) viewContext() []refs.Target {
	var wanted []refs.Target
	switch b.view {
	case graphstore.QuerySpec:
		if len(b.ancestors) == 0 {
			return nil
		}
		for _, target := range b.walked {
			entry := b.entries[target]
			if entry.typ != graphstore.BlockTypeCheck {
				continue
			}
			for _, relation := range b.facts[target].Relations {
				if relation.Kind == servesTupleKind && slices.Contains(b.ancestors, treeIssueOf(relation.To)) && !slices.Contains(wanted, relation.To) {
					wanted = append(wanted, relation.To)
				}
			}
		}
	case graphstore.QueryVerification:
		for _, target := range b.candidates() {
			entry := b.entries[target]
			if entry.typ != graphstore.BlockTypeCheck || !b.kept[target] {
				continue
			}
			for _, relation := range b.facts[target].Relations {
				if relation.Kind == servesTupleKind && !slices.Contains(wanted, relation.To) {
					if held, walked := b.entries[relation.To]; walked && held.walk >= 0 {
						continue
					}
					wanted = append(wanted, relation.To)
				}
			}
		}
	}
	return wanted
}

// placeContext gives each context node its type, its rank and its time, and a context node of a
// view its own parent, which every node a view answers carries; a catalogue node, an interaction
// and a person stand under none.
func (b *issueTreeBuilder) placeContext() error {
	for _, target := range b.context {
		entry := b.entries[target]
		held := b.facts[target]
		if entry.typ == "" {
			opening, _, _ := strings.Cut(held.Values[graphstore.AttributeText], "\n")
			entry.typ = refs.TreeNodeType(target.Kind, b.rows[target].Supertag, opening)
		}
		if rank, ranked := held.Values[refs.AttributeRank]; ranked {
			entry.rank, entry.ranked = rank, true
		}
		entry.madeAt = b.rows[target].MadeAt
		if b.view != "" && entry.parent == (refs.Target{}) && target.Kind == refs.BlockNodeKind {
			for _, relation := range held.Relations {
				if relation.Kind == graphstore.TuplePartOf {
					entry.parent = relation.To
				}
			}
		}
		if entry.parent != (refs.Target{}) {
			b.effective[target] = entry.parent
		}
	}
	return nil
}

// servesTupleKind is the relation a check and a plan node serve a requirement by, and a
// requirement a criterion.
const servesTupleKind = "serves"

// requirementBlockType is the typed block a requirement is.
const requirementBlockType = "requirement"

// readMembers reads, for every node the answer holds, what its own members need from the rows
// that hold them: each issue's own members, the texts of the items and their blocks, the asks'
// questions, states and wording, the results' rows and retractions, and the decisions'
// members through their one builder (decisionread.Decisions); and, in the batch of the texts, the
// documents of the issue that owe their block index (queueOwedDocuments).
func (b *issueTreeBuilder) readMembers() error {
	answer := b.answered()
	b.issues = map[string]*model.IssueTreeIssue{}
	root, err := readIssueTreeIssue(b.ctx, b.q, b.key)
	if err != nil {
		return err
	}
	b.issues[b.key] = root
	for _, target := range answer {
		if target.Kind != graphstore.KindIssue || target.ID == b.key {
			continue
		}
		state, err := readIssueTreeIssue(b.ctx, b.q, target.ID)
		if err != nil {
			return err
		}
		b.issues[target.ID] = state
	}
	// A parent the answer does not hold - a node of an issue above that a node of the walk's
	// second part stands under, a node of an issue under the one read whose node the answer does
	// not hold - is named by what the store and the registry's loader answer of it.
	var outside []refs.Target
	for _, target := range answer {
		if parent, standing := b.effective[target]; standing && parent != (refs.Target{}) {
			if _, held := b.entries[parent]; !held && !slices.Contains(outside, parent) {
				outside = append(outside, parent)
			}
		}
	}
	if err := b.readFacts(outside); err != nil {
		return err
	}
	b.texts = map[refs.Target]string{}
	b.asks = map[string]treeAsk{}
	b.results = map[string]treeResult{}
	var comments, messages, asks, results []string
	var itemBlocks []refs.Target
	for _, target := range answer {
		switch target.Kind {
		case graphstore.KindComment:
			comments = append(comments, target.ID)
		case graphstore.KindMessage:
			messages = append(messages, target.ID)
		case graphstore.KindAsk:
			asks = append(asks, target.ID)
		case graphstore.KindResult:
			results = append(results, target.ID)
		case refs.ItemAsk.BlockKind(), refs.ItemComment.BlockKind(), refs.ItemMessage.BlockKind():
			itemBlocks = append(itemBlocks, target)
		}
	}
	batch := &pgx.Batch{}
	queueTexts := func(kind, statement string, ids []string) {
		if len(ids) == 0 {
			return
		}
		batch.Queue(statement, ids).Query(func(rows pgx.Rows) error {
			for rows.Next() {
				var id, body string
				if err := rows.Scan(&id, &body); err != nil {
					return err
				}
				b.texts[refs.Target{Kind: kind, ID: id}] = body
			}
			return rows.Err()
		})
	}
	queueTexts(graphstore.KindComment, `select id::text, body from comments where id = any($1::uuid[])`, comments)
	queueTexts(graphstore.KindMessage, `select id::text, body from messages where id = any($1::uuid[])`, messages)
	if len(asks) > 0 {
		batch.Queue(`select id::text, question, state, options, urgency, multiple from asks where id = any($1::uuid[])`, asks).Query(func(rows pgx.Rows) error {
			for rows.Next() {
				var id string
				var ask treeAsk
				var options []byte
				if err := rows.Scan(&id, &ask.question, &ask.state, &options, &ask.wording.Urgency, &ask.wording.Multiple); err != nil {
					return err
				}
				if err := json.Unmarshal(options, &ask.wording.Options); err != nil {
					return fmt.Errorf("decode the options of ask %s: %w", id, err)
				}
				b.asks[id] = ask
				b.texts[refs.Target{Kind: graphstore.KindAsk, ID: id}] = ask.question
			}
			return rows.Err()
		})
	}
	if len(results) > 0 {
		batch.Queue(`
			select r.id::text, r.actor, r.created_at, x.reason, x.actor, x.created_at
			from check_results r
			left join check_result_retractions x on x.result_id = r.id
			where r.id = any($1::uuid[])`, results).Query(func(rows pgx.Rows) error {
			for rows.Next() {
				var id string
				var actor, retractedBy []byte
				var result treeResult
				var reason *string
				var retractedAt *time.Time
				if err := rows.Scan(&id, &actor, &result.createdAt, &reason, &retractedBy, &retractedAt); err != nil {
					return err
				}
				if err := json.Unmarshal(actor, &result.actor); err != nil {
					return fmt.Errorf("decode the actor of result %s: %w", id, err)
				}
				if reason != nil {
					retraction := model.CheckResultRetracted{Reason: *reason, CreatedAt: *retractedAt}
					if err := json.Unmarshal(retractedBy, &retraction.Actor); err != nil {
						return fmt.Errorf("decode the actor of result %s's retraction: %w", id, err)
					}
					result.retracted = &retraction
				}
				b.results[id] = result
			}
			return rows.Err()
		})
	}
	queueOwedDocuments(batch, b.key, &b.owed)
	if err := b.q.SendBatch(b.ctx, batch).Close(); err != nil {
		return fmt.Errorf("read the texts of the tree of %s: %w", b.key, err)
	}
	if len(itemBlocks) > 0 {
		texts, err := refs.BlockTexts(b.ctx, b.q, b.who, itemBlocks)
		if err != nil {
			return err
		}
		for target, text := range texts {
			b.texts[target] = text
		}
	}
	return b.readDecisions(answer)
}

// readDecisions reads, through the one builder of a decision's members (decisionread.Decisions),
// each decision node the answer holds and each ask a person answered, which the decisions read
// answers as a person's decision.
func (b *issueTreeBuilder) readDecisions(answer []refs.Target) error {
	var nodes []decisionread.Node
	for _, target := range answer {
		switch {
		case target.Kind == refs.BlockNodeKind && b.entries[target].typ == refs.DecisionSupertag:
			nodes = append(nodes, decisionread.Node{Kind: refs.BlockNodeKind, ID: target.ID})
		case target.Kind == graphstore.KindAsk && b.asks[target.ID].state == "answered":
			nodes = append(nodes, decisionread.Node{Kind: graphstore.KindAsk, ID: target.ID})
		}
	}
	b.decisions = map[refs.Target]model.Decision{}
	if len(nodes) == 0 {
		return nil
	}
	decided, err := decisionread.Decisions(b.ctx, b.q, b.who, nodes)
	if err != nil {
		return err
	}
	for _, decision := range decided {
		b.decisions[refs.Target{Kind: decision.NodeKind, ID: decision.NodeID}] = decision
	}
	return nil
}

// readIssueTreeIssue is what the issue key answers of itself in a tree read (model.IssueTreeIssue):
// whether it is converted and its tree, whose view it is (docs.ViewOf), and while it has not
// converted the ids of its role documents, as the role predicates find them.
func readIssueTreeIssue(ctx context.Context, q pgx.Tx, key string) (*model.IssueTreeIssue, error) {
	state := &model.IssueTreeIssue{}
	tree, err := issueTreeOf(ctx, q, key)
	if err != nil {
		return nil, err
	}
	if tree != "" {
		state.Converted, state.Tree = true, &tree
	} else {
		documents := &model.IssueTreeDocuments{}
		if err := q.QueryRow(ctx, `
			select (select a.id::text from artifacts a where a.issue_key = $1 and a.is_primary),
			       (select a.id::text from artifacts a where a.issue_key = $1 and `+aliasedVerificationRole+`),
			       (select a.id::text from artifacts a where a.issue_key = $1 and `+aliasedPlanRole+`)`,
			key).Scan(&documents.Spec, &documents.Verification, &documents.Plan); err != nil {
			return nil, fmt.Errorf("read the role documents of %s: %w", key, err)
		}
		state.Documents = documents
	}
	ancestor, err := docs.ViewOf(ctx, q, key)
	if err != nil {
		return nil, err
	}
	if ancestor != "" {
		state.ViewOf = &ancestor
	}
	return state, nil
}

// answered is every node the answer holds, in its order, the context last (ordered).
func (b *issueTreeBuilder) answered() []refs.Target {
	if b.order == nil {
		b.order = b.ordered()
	}
	return slices.Concat(b.order, b.contextOrder())
}

// queryTakes is each node of the walk the query takes, by its definition (docs'
// IssueTree.Takes).
func (b *issueTreeBuilder) queryTakes(query string) map[refs.Target]bool {
	taken := map[refs.Target]bool{}
	indexes, _ := b.tree.Takes(query)
	for _, index := range indexes {
		node := b.tree.Nodes[index]
		taken[refs.Target{Kind: node.Kind, ID: node.ID}] = true
	}
	return taken
}

// inView reports whether the view answers the node a walk or a read of the others took: a node of
// its query (taken), or of the view's fixed context, each of an issue under the issue read included
// where the view carries one (readServing, readPlanOrder), on a child issue's spec view its design
// blocks, and on the verification view the requirements it carries (requirements); and on the
// spec and the verification views each node a tie of their nodes brings in (tied, readTies).
func (b *issueTreeBuilder) inView(target refs.Target) (string, bool) {
	entry := b.entries[target]
	if b.taken[target] {
		return treeRoleQuery, true
	}
	if b.tied[target] {
		return treeRoleContext, true
	}
	switch b.view {
	case graphstore.QuerySpec:
		if entry.walk >= 0 && target.Kind == refs.BlockNodeKind && !pmdoc.IsTypedBlock(entry.typ) &&
			b.blocks[target.ID].band == refs.RankBandTree && len(b.ancestors) > 0 {
			return treeRoleContext, true
		}
	case graphstore.QueryVerification:
		if entry.walk < 0 && (entry.typ == graphstore.BlockTypeCheck || target.Kind == graphstore.KindResult) || b.requirements[target] {
			return treeRoleContext, true
		}
	case graphstore.QueryPlan:
		if _, planned := b.plans[target]; planned && (entry.walk < 0 || target.Kind == graphstore.KindIssue) {
			return treeRoleContext, true
		}
	}
	return "", false
}

// verificationRequirements is each requirement the verification view carries as context among the
// nodes the walk and the read of the others took: every requirement node of the issue, each one
// its spec query takes, found as the spec view finds them (queryTakes), and each requirement a
// check the answer holds serves.
func (b *issueTreeBuilder) verificationRequirements() map[refs.Target]bool {
	carried := map[refs.Target]bool{}
	for target := range b.queryTakes(graphstore.QuerySpec) {
		if b.entries[target].typ == requirementBlockType {
			carried[target] = true
		}
	}
	for _, target := range b.candidates() {
		if b.entries[target].typ != graphstore.BlockTypeCheck || !b.kept[target] {
			continue
		}
		for _, relation := range b.facts[target].Relations {
			if relation.Kind == servesTupleKind {
				carried[relation.To] = true
			}
		}
	}
	return carried
}

// siblingClass is where a child stands among its siblings in the walk's order: a conversation
// item's own blocks first, then the children that hold a rank, then the rest.
func (b *issueTreeBuilder) siblingClass(target refs.Target) int {
	entry := b.entries[target]
	switch {
	case target.Kind == refs.ItemAsk.BlockKind() || target.Kind == refs.ItemComment.BlockKind() || target.Kind == refs.ItemMessage.BlockKind():
		return 0
	case entry.ranked:
		return 1
	}
	return 2
}

// compareSiblings orders two children of one parent as the walk does (docs' treeWalk.ordered): a
// ranked child by rank, an unranked one by when it was made, each tie by the lower address, every
// comparison byte for byte. Two blocks of one item keep the order the walk gave them.
func (b *issueTreeBuilder) compareSiblings(left, right refs.Target) int {
	leftClass, rightClass := b.siblingClass(left), b.siblingClass(right)
	if leftClass != rightClass {
		return leftClass - rightClass
	}
	l, r := b.entries[left], b.entries[right]
	switch leftClass {
	case 0:
		return l.walk - r.walk
	case 1:
		return cmp.Or(strings.Compare(l.rank, r.rank), strings.Compare(left.Kind+":"+left.ID, right.Kind+":"+right.ID))
	}
	return cmp.Or(l.madeAt.Compare(r.madeAt), strings.Compare(left.Kind+":"+left.ID, right.Kind+":"+right.ID))
}

// ordered is the answer's nodes but the context in the tree's order: the issue's node, each node
// before its children, a node's own children in the walk's order and after them each node lifted
// to it from under a node the answer leaves out, by its rank and then its id; then each node whose
// parent is a node the answer does not hold - the walk's second part, under a node of an issue
// above, and a node reached through serves whose issue's node the answer does not hold - each with
// its subtree, in the walk's order and then the order the others were read in. A view keeps the
// nodes it answers in that order, and the plan view orders them by their positions in the one
// order of the whole tree.
func (b *issueTreeBuilder) ordered() []refs.Target {
	direct := map[refs.Target][]refs.Target{}
	lifted := map[refs.Target][]refs.Target{}
	var outside []refs.Target
	for _, target := range b.candidates() {
		if !b.kept[target] || target == b.root() || b.entries[target].role == treeRoleContext {
			continue
		}
		parent := b.effective[target]
		switch {
		case parent != b.root() && !b.kept[parent]:
			outside = append(outside, target)
		case parent == b.entries[target].parent:
			direct[parent] = append(direct[parent], target)
		default:
			lifted[parent] = append(lifted[parent], target)
		}
	}
	for parent := range direct {
		slices.SortStableFunc(direct[parent], func(left, right refs.Target) int {
			l, r := b.entries[left], b.entries[right]
			if l.walk >= 0 && r.walk >= 0 {
				return l.walk - r.walk
			}
			return b.compareSiblings(left, right)
		})
	}
	for parent := range lifted {
		slices.SortStableFunc(lifted[parent], func(left, right refs.Target) int {
			l, r := b.entries[left], b.entries[right]
			if l.ranked != r.ranked {
				if l.ranked {
					return -1
				}
				return 1
			}
			return cmp.Or(strings.Compare(l.rank, r.rank), strings.Compare(left.Kind+":"+left.ID, right.Kind+":"+right.ID))
		})
	}
	var order []refs.Target
	taken := map[refs.Target]bool{}
	var take func(target refs.Target)
	take = func(target refs.Target) {
		if taken[target] {
			return
		}
		taken[target] = true
		order = append(order, target)
		for _, child := range direct[target] {
			take(child)
		}
		for _, child := range lifted[target] {
			take(child)
		}
	}
	take(b.root())
	for _, target := range outside {
		take(target)
	}
	if b.view == "" {
		return order
	}
	var viewed []refs.Target
	for _, target := range order {
		if role, in := b.inView(target); in {
			b.entries[target].role = role
			viewed = append(viewed, target)
		}
	}
	if b.view == graphstore.QueryPlan {
		if _, held := b.plans[b.root()]; held {
			b.entries[b.root()].role = treeRoleContext
			viewed = append([]refs.Target{b.root()}, slices.DeleteFunc(viewed, func(target refs.Target) bool { return target == b.root() })...)
		}
		slices.SortStableFunc(viewed, func(left, right refs.Target) int {
			return cmp.Compare(b.planPosition(left), b.planPosition(right))
		})
	}
	return viewed
}

// planPosition is where a node of the plan view stands in the one order of the whole tree: its
// position, an issue's its start half's, and past every placed node for one the order gives none.
func (b *issueTreeBuilder) planPosition(target refs.Target) int {
	plan := b.plans[target]
	switch {
	case plan == nil:
	case plan.Position != nil:
		return *plan.Position
	case plan.Start != nil && plan.Start.Position != nil:
		return *plan.Start.Position
	}
	return 1 << 30
}

// contextOrder is the context nodes in kind order and then id order, each once, byte for byte.
func (b *issueTreeBuilder) contextOrder() []refs.Target {
	var context []refs.Target
	for _, target := range b.context {
		if b.kept[target] && b.entries[target].role == treeRoleContext && !slices.Contains(b.order, target) {
			context = append(context, target)
		}
	}
	slices.SortFunc(context, func(left, right refs.Target) int {
		return cmp.Or(strings.Compare(left.Kind, right.Kind), strings.Compare(left.ID, right.ID))
	})
	return context
}

// documentPositions reads, for each typed block of a role document the answer holds, its position
// in its document as GET /api/v1/issues/{key}/artifacts/{slug}/blocks lists it, from that read's
// own call on each document once (docs.Service.Blocks). It runs after the read's transaction has
// closed, since a document's blocks are its live room's.
func (b *issueTreeBuilder) documentPositions(s *server) error {
	b.positions = map[refs.Target]int{}
	listed := map[string]map[string]int{}
	for _, target := range b.answered() {
		row, documented := b.roleBlock(target)
		if !documented {
			continue
		}
		positions, read := listed[row.document]
		if !read {
			blocks, err := s.deps.Docs.Blocks(b.ctx, row.document)
			if err != nil {
				return err
			}
			positions = make(map[string]int, len(blocks))
			for index, block := range blocks {
				if _, held := positions[block.ID]; !held {
					positions[block.ID] = index
				}
			}
			listed[row.document] = positions
		}
		if position, held := positions[row.blockID]; held {
			b.positions[target] = position
		}
	}
	return nil
}

// roleBlock is the document_blocks row of a typed block of a role document, and whether target is
// one.
func (b *issueTreeBuilder) roleBlock(target refs.Target) (blockRow, bool) {
	if target.Kind != refs.BlockNodeKind {
		return blockRow{}, false
	}
	row, held := b.blocks[target.ID]
	if !held || !pmdoc.IsTypedBlock(b.entries[target].typ) {
		return blockRow{}, false
	}
	switch row.band {
	case refs.RankBandSpec, refs.RankBandVerification, refs.RankBandPlan:
		return row, true
	}
	return blockRow{}, false
}

// answer is the read's answer from cursor on: the page of the tree's order that starts there, the
// context on the last page, and the tuples whose near end the page holds and whose far end the
// whole answer does.
func (b *issueTreeBuilder) answer(cursor string, version int, trees *displayTreeReader) (model.IssueTreeRead, error) {
	order := b.answered()
	context := order[len(b.order):]
	walk := order[:len(b.order)]
	start := 0
	if cursor != "" {
		address, err := base64.RawURLEncoding.DecodeString(cursor)
		kind, id, named := strings.Cut(string(address), ":")
		start = slices.Index(walk, refs.Target{Kind: kind, ID: id})
		if err != nil || !named || start < 0 {
			return model.IssueTreeRead{}, errorf(http.StatusBadRequest, "INVALID_QUERY", "cursor names no node of the tree of %s: read the first page again", b.key)
		}
	}
	end := min(start+issueTreePageSize, len(walk))
	page := slices.Clone(walk[start:end])
	issue := b.issues[b.key]
	read := model.IssueTreeRead{
		Issue: b.key, View: b.view, Converted: issue.Converted, Tree: issue.Tree, ViewOf: issue.ViewOf, Documents: issue.Documents,
		Nodes: []model.IssueTreeNode{}, Tuples: []model.IssueTreeTuple{}, OwedDocuments: b.owed,
	}
	if version > 0 {
		read.Version = &version
	}
	if end < len(walk) {
		next := base64.RawURLEncoding.EncodeToString([]byte(walk[end].Kind + ":" + walk[end].ID))
		read.Cursor = &next
	} else {
		page = append(page, context...)
	}
	all := map[refs.Target]bool{}
	for _, target := range order {
		all[target] = true
	}
	read.Nodes = make([]model.IssueTreeNode, len(page))
	for index, target := range page {
		read.Nodes[index] = b.node(target)
		for _, relation := range b.facts[target].Relations {
			if all[relation.To] {
				read.Tuples = append(read.Tuples, model.IssueTreeTuple{
					Kind: relation.Kind,
					From: model.IssueTreeEnd{Kind: target.Kind, ID: target.ID},
					To:   model.IssueTreeEnd{Kind: relation.To.Kind, ID: relation.To.ID},
				})
			}
		}
	}
	// The trees are registered once the answer's nodes stand where they will be encoded from.
	for index := range read.Nodes {
		node := &read.Nodes[index]
		if node.Decision != nil {
			trees.text(&node.Decision.TextTree, node.Decision.Text)
		}
		trees.text(&node.TextTree, node.Text)
	}
	return read, nil
}

// node is target as the answer carries it (model.IssueTreeNode).
func (b *issueTreeBuilder) node(target refs.Target) model.IssueTreeNode {
	entry := b.entries[target]
	held := b.facts[target]
	loaded := b.loaded[target]
	node := model.IssueTreeNode{
		Kind: target.Kind, ID: target.ID, Supertag: entry.typ, Issue: entry.issue, ReadOnly: entry.issue != "",
		Role: cmp.Or(entry.role, treeRoleTree), Author: []model.Actor{}, Attributes: map[string]string{},
		State: graphstore.StateLive, Visibility: graphstore.VisibilityProject,
	}
	if entry.ranked {
		rank := entry.rank
		node.Rank = &rank
	}
	for name, value := range held.Values {
		if name != refs.AttributeRank && name != graphstore.AttributeText {
			node.Attributes[name] = value
		}
	}
	if loaded.Node.Kind != "" {
		if loaded.Node.MadeBy != nil {
			node.Author = loaded.Node.MadeBy
		}
		node.MadeAt = loaded.Node.MadeAt
		node.State, node.Visibility = cmp.Or(loaded.Node.State, node.State), cmp.Or(loaded.Node.Visibility, node.Visibility)
	} else {
		node.State = cmp.Or(held.Values[graphstore.AttributeState], node.State)
		node.Visibility = cmp.Or(held.Values[graphstore.AttributeVisibility], node.Visibility)
		if !entry.madeAt.IsZero() {
			made := entry.madeAt
			node.MadeAt = &made
		}
	}
	if _, blockID, cut := strings.Cut(target.ID, "#"); cut && (target.Kind == refs.BlockNodeKind || strings.HasSuffix(target.Kind, "_block")) {
		node.BlockID = blockID
	}
	node.Text = b.text(target)
	if parent, standing := b.effective[target]; standing && target != b.root() {
		node.Parent = b.parentOf(parent)
	}
	if b.noConversation(target) {
		node.NoConversation = true
	}
	if row, documented := b.roleBlock(target); documented {
		if position, held := b.positions[target]; held {
			node.Document = &model.IssueTreeDocument{ArtifactID: row.document, Slug: row.slug, Position: position}
		}
	}
	if ask, held := b.asks[target.ID]; held && target.Kind == graphstore.KindAsk {
		wording := ask.wording
		node.Ask = &wording
	}
	if decision, decided := b.decisions[target]; decided {
		node.Decision = &decision
		node.Text = decision.Text
	}
	switch {
	case target.Kind == graphstore.KindResult:
		node.Result = b.result(target)
		if recorded, held := b.results[target.ID]; held {
			node.Author = []model.Actor{recorded.actor}
			made := recorded.createdAt
			node.MadeAt = &made
		}
	case entry.typ == graphstore.BlockTypeCheck:
		if row, held := b.blocks[target.ID]; held {
			node.Check = &model.IssueTreeCheck{TextSHA256: row.sum, ChangedVersion: row.version}
		}
	}
	if plan, planned := b.plans[target]; planned && b.view == graphstore.QueryPlan {
		node.Plan = plan
	}
	if target.Kind == graphstore.KindIssue {
		node.IssueTreeIssue = b.issues[target.ID]
		if target.ID == b.key {
			node.Issue = ""
		}
	}
	return node
}

// text is the node's words: a block's and a finding's text value at the read's cut, a comment's or
// a message's body, an ask's question, the text of an item's block, a result's observed text, an
// issue's title, and for any other node the words an edge at it shows.
func (b *issueTreeBuilder) text(target refs.Target) string {
	if text, held := b.texts[target]; held {
		return text
	}
	held := b.facts[target]
	switch target.Kind {
	case refs.BlockNodeKind:
		return held.Values[graphstore.AttributeText]
	case graphstore.KindResult:
		return held.Values[graphstore.AttributeObserved]
	}
	if excerpt := b.loaded[target].Excerpt; excerpt != nil {
		return excerpt.Text
	}
	return ""
}

// parentOf names the node parent as a node of the answer names its parent: its store kind and id,
// its supertag, the first line of its words, and its issue where it is not the issue read.
func (b *issueTreeBuilder) parentOf(parent refs.Target) *model.IssueTreeParent {
	named := &model.IssueTreeParent{Kind: parent.Kind, ID: parent.ID}
	if entry, held := b.entries[parent]; held {
		named.Supertag, named.FirstLine, named.Issue = entry.typ, treeFirstLine(b.text(parent)), entry.issue
		if parent.Kind == graphstore.KindIssue && parent.ID != b.key {
			named.Issue = parent.ID
		}
		return named
	}
	held := b.facts[parent]
	opening, _, _ := strings.Cut(held.Values[graphstore.AttributeText], "\n")
	named.Supertag = refs.TreeNodeType(parent.Kind, b.rows[parent].Supertag, opening)
	named.FirstLine = treeFirstLine(b.text(parent))
	switch loaded := b.loaded[parent].Node; {
	case loaded.IssueKey != nil && *loaded.IssueKey != b.key:
		named.Issue = *loaded.IssueKey
	case treeIssueOf(parent) != b.key:
		named.Issue = treeIssueOf(parent)
	}
	return named
}

// treeFirstLine is the first line of a node's words that says something: past a typed block's opening
// line, its first line that is not blank, its leading space and markdown marks trimmed.
func treeFirstLine(text string) string {
	lines := strings.Split(text, "\n")
	if len(lines) > 0 && strings.HasPrefix(strings.TrimSpace(lines[0]), ":::") {
		lines = lines[1:]
	}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || line == ":::" {
			continue
		}
		return strings.TrimSpace(strings.TrimLeft(line, "#>-*+ "))
	}
	return ""
}

// noConversation reports whether target is a node of the issue read that no conversation node -
// an ask, a comment or a message, or a block of one - stands above in the walk. A conversation
// node itself, a node of another issue, a node of the walk's second part, the issue's own node and
// a context node carry no such mark.
func (b *issueTreeBuilder) noConversation(target refs.Target) bool {
	entry := b.entries[target]
	if entry.walk < 0 || entry.above || target == b.root() || isConversationKind(target.Kind) {
		return false
	}
	at := entry.parent
	for steps := 0; steps <= len(b.entries); steps++ {
		if at == (refs.Target{}) || at == b.root() {
			return true
		}
		if isConversationKind(at.Kind) {
			return false
		}
		above, held := b.entries[at]
		if !held {
			return false
		}
		at = above.parent
	}
	return true
}

// isConversationKind is whether a node of kind is an ask, a comment or a message, or a block of one.
func isConversationKind(kind string) bool {
	switch kind {
	case graphstore.KindAsk, graphstore.KindComment, graphstore.KindMessage,
		refs.ItemAsk.BlockKind(), refs.ItemComment.BlockKind(), refs.ItemMessage.BlockKind():
		return true
	}
	return false
}

// result is what a result node answers as the results read answers it: its verdict, its check's
// version and text sum when it was recorded, each a value of the node, and its retraction from the
// check_result_retractions row that projects its retracted value.
func (b *issueTreeBuilder) result(target refs.Target) *model.IssueTreeResult {
	values := b.facts[target].Values
	version, _ := strconv.Atoi(values[graphstore.AttributeCheckVersion])
	return &model.IssueTreeResult{
		Verdict: values[graphstore.AttributeVerdict], CheckVersion: version, CheckSHA256: values[graphstore.AttributeCheckSHA256],
		Retracted: b.results[target.ID].retracted,
	}
}
