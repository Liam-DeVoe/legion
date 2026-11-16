package graphcensus

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
	"github.com/sjawhar/envoy/internal/pgmigrate"
)

// reportedDifferences is how many differences of one direction of one kind the report prints by
// name. The count beside them is every one there is, so a store that drifted by a million rows
// prints a million in its count and twenty rows to start from.
const reportedDifferences = 20

// projection is one kind of the store held against what projects it.
//
// rows is a select of (row_label, node_kind, node_address, digest): one row per projection row,
// naming the node that row says it projects and digesting the fields the row and its node
// share. nodes is a select of (id, digest): one row per node of the kind, digesting the same
// fields as the store holds them, of which the census compares those the store holds present
// (presentNodes). The census holds the two against each other in both directions, so a row with
// no node and a node with no row are each a difference.
//
// A kind with no projection table takes the set of actors the projected tables name as its rows
// (people and agents below), which is the side that exists for it; a role takes the roles the
// event log ever named as well (addressedRoles).
type projection struct {
	// name is the kind as the report names it, nodeKind the kind of graph_nodes row it
	// projects and table the relation its rows come from, for the report.
	name     string
	nodeKind string
	table    string
	// note says what this kind's two sides are where that is not the plain one row per node,
	// so the report says what it checked rather than only what it found.
	note string
	rows string
	// nodes is the nodes of the kind. It names no state: presentNodes wraps it.
	nodes string
	// addressed says rows reads the roles addressedRoles names as $1, which the census reads once
	// for the run (readAddressedRoles) rather than once in every statement that reads the rows.
	addressed bool
	// pastBound counts the addresses this kind's row side names that are past the store's bound
	// (graphstore.MaxAddressBytes), which the writers left unprojected on purpose: it is a full
	// `select count(*)`, reads $1 where addressed, and is empty for a kind no address of which can
	// pass the bound. The census reports the count as a class of its own and excludes those
	// addresses from both comparisons, so a row no store entry answers by design is no difference.
	pastBound string
	// mayBeEmpty says a deployment can hold no row of this kind - no run of the command bar, no
	// lens - so a census that checked zero rows of it proves nothing of it and refuses nothing for
	// it either (Unpopulated leaves it out). Every kind of dispatch://INFRA-1950 is such a kind.
	mayBeEmpty bool
}

// KindReport is one kind's census: how many rows and how many present nodes it read, and what
// the two said about each other.
type KindReport struct {
	Kind, NodeKind, Table, Note string
	Rows, Nodes                 int64
	// RowDifferences and NodeDifferences are how many there are in each direction, and the
	// Reported slices the first reportedDifferences of them, each as its cause and its row.
	RowDifferences, NodeDifferences int64
	ReportedRows, ReportedNodes     []Difference
	// PastBound is how many addresses this kind's row side named that are past the store's bound,
	// each a class of its own and no difference (projection.pastBound).
	PastBound int64
	// MayBeEmpty is the kind's projection.mayBeEmpty: zero rows of it refuse nothing.
	MayBeEmpty bool
}

// Difference is one disagreement: why it is one, and the row or node it was found at.
type Difference struct{ Cause, At string }

// StoreReport is what the store census found. It is a report and not an error: a census that
// reads a difference has done its job, and the deployment reading it decides.
type StoreReport struct {
	Kinds []KindReport
	// Projects is the project census (ProjectCensus): for each kind whose nodes carry a project
	// or parties, its rows with no node, with a node naming neither, and with a node naming
	// another than the row's. Every count of it refuses the census. ProjectIncomplete names the
	// first of those rows (ProjectComplete), as many as the census names differences of a kind.
	Projects          []ProjectKindReport
	ProjectIncomplete []string
	// Absent is true where the database holds no store at all: graph_nodes is created by the
	// first migration of this branch (20261109013400), so a database it has not reached has no
	// kind to read, and the census reads none.
	Absent bool
	// Sizing is what the sizing pass found, or nil when it was not asked for.
	Sizing *SizingReport
}

// ProjectFindings is how many rows the project census found incomplete over every kind.
func (r *StoreReport) ProjectFindings() int64 {
	var found int64
	for _, kind := range r.Projects {
		found += kind.Findings()
	}
	return found
}

// Rows is how many projection rows the census checked over every kind.
func (r *StoreReport) Rows() int64 {
	var rows int64
	for _, kind := range r.Kinds {
		rows += kind.Rows
	}
	return rows
}

// Differences is how many disagreements the census read over every kind and both directions.
func (r *StoreReport) Differences() int64 {
	var found int64
	for _, kind := range r.Kinds {
		found += kind.RowDifferences + kind.NodeDifferences
	}
	return found
}

// Unpopulated is every kind the census checked no row of, in the order it read them, but a kind a
// deployment may hold none of (KindReport.MayBeEmpty).
func (r *StoreReport) Unpopulated() []string {
	var empty []string
	for _, kind := range r.Kinds {
		if kind.Rows == 0 && !kind.MayBeEmpty {
			empty = append(empty, kind.Kind)
		}
	}
	return empty
}

// Refused reports whether the census refuses. It passes only where it checked more than zero
// rows of every kind it covers and found no difference in either direction (R19). A total over
// the kinds is no such proof: two empty sides of one kind agree, and one kind's rows say nothing
// about another's, so a census whose comments carried every row would pass while it proved
// nothing of the blocks, the roles or the sessions. A kind a deployment may hold none of - a run
// and its frames, and the visibility and state of a run, a lens, a finding, a risk, a constraint
// and a lens kind - refuses nothing on zero rows alone (projection.mayBeEmpty); a driver over a
// corpus that holds them still reads the count.
//
// A database the store does not exist in yet (Absent) has nothing to census. The sizing is taken
// on just such a database (R4 reads before the first migration), so there the sizing alone
// decides, and a census asked for without it refuses, since it read nothing.
func (r *StoreReport) Refused() bool {
	if r.Absent {
		return r.Sizing == nil || r.Sizing.Refused()
	}
	if len(r.Unpopulated()) > 0 || r.Differences() > 0 || r.ProjectFindings() > 0 {
		return true
	}
	return r.Sizing != nil && r.Sizing.Refused()
}

// The kinds the census holds the store to. The first five are the kinds a table projects, each
// with that table; the four after them are the people and the agents, which no table projects; and
// the last six are the visibility and the state of each node of the kinds dispatch://INFRA-1950
// adds, held to the store's own record of them (baseAttributeCensus).
//
// Each of the first five holds a row to the tuples that answer it as well as to its node row.
// A row that names its node is answered from the tuples by graph_edges, refs.Edges and
// refs.BacklinkCounts (migration 20261109215947), so a link whose tuple is missing or retracted
// is gone from every read while the node row still stands, and a census of the node rows alone
// reads zero differences over a store that has lost edges. A row's digest therefore carries
// every link the row states (statedLinks), and its node's digest carries every link the store
// states now under the same tuple kinds (currentLinks): the two agree exactly when the store
// states each link the row names and no other.
//
// An address past the store's bound (graphstore.MaxAddressBytes) is deliberately no part of the
// store: a block whose address is longer carries no node, a role name of 1,400 bytes is no node
// and no page, a comment anchored to a document whose ref_key is longer states no such tuple, and
// a session named past it is no node either. Each row side leaves those addresses out and counts
// them as a class of its own (pastBound), so the census reports no difference the writers made on
// purpose, in both directions.
//
// The two block kinds hold a block to what refs.IndexBlocks projects of it and to nothing else. A
// block of a lens's finding (lensBlock) is a block node no document holds, so no table projects
// it and neither block kind reads it. And a tuple another writer states out of a block - a
// requirement's constrained_by, a changed_by_run, a finding's authored_by - is no link a
// block_refs row names, so the block_ref kind reads only the kinds that writer projects into
// block_refs (refs.BlockRefKindsSQL, read from the store inside the census's own snapshot) and
// the block kind only a block's placement.
//
// A run of the command bar is an interaction node, which going_requests projects, and each of
// its frames a frame value of that node, which a going_events row projects
// (dispatch://INFRA-1950/spec#b-r-interaction): the interaction kind holds a row to its node and
// its authored_by, and the frame kind every frame a run's rows hold to every frame value its node
// holds. A run whose row a process of the release before deleted is a node the backfill states
// gone (backfillGoneRuns), which no row may project and which presentNodes reads past, so it is no
// difference either way.
var projections = []projection{{
	name:     "comment",
	nodeKind: "comment",
	table:    "comments",
	note: "one node per row; the digest is the row's created_at and author, the artifacts its anchor and its" +
		" artifact_id name and the author graphstore.AuthorOf reads off it, against the node's made_at and" +
		" made_by and the anchored_to, owned_by and authored_by tuples it states now",
	rows: `
		select 'comment ' || c.id::text, c.node_kind, c.node_address,
		       md5(c.created_at::text || '|' || c.author::text || '|' || ` + statedLinks(
		`'`+graphstore.TupleAnchoredTo+`>artifact:' || anchor.ref_key`,
		`'`+graphstore.TupleOwnedBy+`>artifact:' || owner.ref_key`,
		authoredLink("c.author"),
	) + `)
		from comments c` + commentStoreJoins + `
		where ` + commentInTheStore,
	nodes: `
		select n.id, md5(n.made_at::text || '|' || n.made_by::text || '|' || ` + currentLinks(graphstore.KindComment,
		kindIn("t.kind", commentLinkKinds)) + `)
		from graph_nodes n where n.kind = 'comment'`,
	pastBound: `
		select count(*) from comments c
		left join artifacts anchor on anchor.id = (c.anchor->>'artifact_id')::uuid
		left join artifacts owner on owner.id = c.artifact_id
		where octet_length(coalesce(anchor.ref_key, '')) > ` + addressBound + `
		   or octet_length(coalesce(owner.ref_key, '')) > ` + addressBound + `
		   or (c.author->>'kind' = '` + graphstore.KindSession + `'
		       and octet_length(coalesce(c.author->>'id', '')) > ` + addressBound + `)`,
}, {
	name:     "block",
	nodeKind: refs.BlockNodeKind,
	table:    "document_blocks",
	note: "one node per row, blocks past the address bound and the blocks of a lens's finding aside; the digest is" +
		" the row's document and the block its parent_block_id names, or the document where it names none, against" +
		" the in_document and part_of tuples the node states now, a block with no part_of telling itself from one" +
		" the document holds directly",
	rows: `
		select 'block ' || d.node_id, d.node_kind, d.node_address,
		       md5(d.node_id || '|' || ` + statedLinks(
		`'`+refs.TupleInDocument+`>artifact:' || a.ref_key`,
		`'`+graphstore.TuplePartOf+`>' || case when d.parent_block_id is null then 'artifact:' || a.ref_key
		      else '`+refs.BlockNodeKind+`:' || a.ref_key || '#' || d.parent_block_id end`,
	) + `)
		from document_blocks d
		join artifacts a on a.id = d.artifact_id
		where octet_length(d.node_id) <= ` + addressBound,
	nodes: `
		select n.id, md5(n.id || '|' || ` + currentLinks(refs.BlockNodeKind, kindIn("t.kind", blockLinkKinds)) + `)
		from graph_nodes n where n.kind = '` + refs.BlockNodeKind + `' and not ` + lensBlock("n"),
	pastBound: `
		select count(*) from document_blocks d where octet_length(d.node_id) > ` + addressBound,
}, {
	name:     "block_ref",
	nodeKind: refs.BlockNodeKind,
	table:    "block_refs",
	note: "a row is one link out of a block and its node is that block's, so several rows share one node:" +
		" the node side is every block node not gone, a lens's finding aside, that states a link now of a kind" +
		" refs.IndexBlocks writes into block_refs (a mention, or a pointer of a block[] attribute the store's" +
		" type nodes declare, at any state), whether a row names it or not, and the digest is every link the" +
		" rows of that block name against every such link its node states now",
	rows: `
		select 'block_ref ' || r.from_node || ' ' || r.kind || ' -> ' || r.to_kind || ' ' || r.to_id,
		       r.node_kind, r.node_address, md5(r.from_node || '|' || named.links)
		from block_refs r
		join (
			select b.from_node,
			       string_agg(b.kind || '>' || b.to_kind || ':' || b.to_id, ','
			                  order by b.kind || '>' || b.to_kind || ':' || b.to_id) as links
			from block_refs b
			where octet_length(b.from_node) <= ` + addressBound + ` and octet_length(b.to_id) <= ` + addressBound + `
			group by b.from_node
		) named on named.from_node = r.from_node
		where octet_length(r.from_node) <= ` + addressBound + ` and octet_length(r.to_id) <= ` + addressBound,
	// Every block node is read, and not only those a row names: a mention or a pointer the store
	// states out of a block no block_refs row names is a link graph_edges answers, and a node
	// side that began at the rows could never find it. A block whose only links are its
	// placement, or tuples no block_refs row projects, states no link here and is no node of this
	// kind; the block kind holds it.
	nodes: `
		select held.id, md5(held.id || '|' || held.links)
		from (
			select n.id, ` + currentLinks(refs.BlockNodeKind, `t.kind in (`+refs.BlockRefKindsSQL+`)`) + ` as links
			from graph_nodes n where n.kind = '` + refs.BlockNodeKind + `' and not ` + lensBlock("n") + `
		) held
		where held.links <> ''`,
	pastBound: `
		select count(*) from block_refs r
		where octet_length(r.from_node) > ` + addressBound + ` or octet_length(r.to_id) > ` + addressBound,
}, {
	name:     graphstore.KindInteraction,
	nodeKind: graphstore.KindInteraction,
	table:    "going_requests",
	note: "one node per row; the digest is the row's author, project, words, created_at and ends_at and the author" +
		" graphstore.AuthorOf reads off it, against the node's made_by, project, words, made_at and ends_at and the" +
		" authored_by tuple it states now; a run whose row is gone is a gone node, no difference",
	rows: `
		select 'interaction ' || r.id::text, '` + graphstore.KindInteraction + `', r.id::text,
		       md5(r.author::text || '|' || r.project || '|' || r.words || '|' || r.created_at::text
		           || '|' || r.ends_at::text || '|' || ` + statedLinks(authoredLink("r.author")) + `)
		from going_requests r`,
	nodes: `
		select n.id, md5(n.made_by::text || '|' || coalesce(n.project_key, '')
		                 || '|' || coalesce(n.attributes->>'` + graphstore.AttributeWords + `', '')
		                 || '|' || n.made_at::text
		                 || '|' || coalesce((n.attributes->>'` + graphstore.AttributeEndsAt + `')::timestamptz::text, '')
		                 || '|' || ` + currentLinks(graphstore.KindInteraction, kindIn("t.kind", runLinkKinds)) + `)
		from graph_nodes n where n.kind = '` + graphstore.KindInteraction + `'`,
	mayBeEmpty: true,
}, {
	name:     "frame",
	nodeKind: graphstore.KindInteraction,
	table:    "going_events",
	note: "a row is one frame of a run and its node is the run's interaction, so several rows share one node: the" +
		" node side is every interaction not gone that holds a frame value, and the digest is every frame the" +
		" rows of that run hold, each its sequence, name and data as graphstore.FrameValueSQL writes it, against" +
		" every frame value its node holds",
	rows: `
		select 'frame ' || e.request_id::text || ' ' || e.sequence, '` + graphstore.KindInteraction + `',
		       e.request_id::text, md5(run.frames)
		from going_events e
		join (
			select f.request_id,
			       string_agg(` + graphstore.FrameValueSQL("f.sequence", "f.name", "f.data") + `, E'\n' order by f.sequence) as frames
			from going_events f
			group by f.request_id
		) run on run.request_id = e.request_id`,
	// A frame value is never retracted, and two writes of one frame state the same text, which
	// the distinct reads once; two values at one sequence are two frames, and the digest says so.
	nodes: `
		select held.id, md5(held.frames)
		from (
			select n.id, (
				select string_agg(framed.value, E'\n' order by framed.sequence)
				from (
					select distinct t.to_id as value, (t.to_id::jsonb->>'sequence')::integer as sequence
					from graph_tuples t
					where t.from_kind = '` + graphstore.KindInteraction + `' and t.from_id = n.id
					  and t.kind = '` + graphstore.AttributeFrame + `' and t.to_kind = '` + graphstore.KindValue + `'
					  and t.asserted
				) framed
			) as frames
			from graph_nodes n where n.kind = '` + graphstore.KindInteraction + `'
		) held
		where held.frames is not null`,
	mayBeEmpty: true,
}, {
	name:     graphstore.KindPerson,
	nodeKind: graphstore.KindPerson,
	table:    "comments.author, going_requests.author, graph_nodes.made_by",
	note: "no table projects a person, so the row side is the people the projected tables name as authors," +
		" the owner of each session the server proved that wrote one, the author of each run, and the author of" +
		" each lens, finding, risk and constraint, and of each run whose row is gone, that states its authored_by",
	rows: `
		select 'person ' || p.login, '` + graphstore.KindPerson + `', p.login, ''
		from (` + recordedPeople + `) p`,
	nodes: `
		select n.id, '' from graph_nodes n where n.kind = '` + graphstore.KindPerson + `'`,
}, {
	name:     graphstore.KindSession,
	nodeKind: graphstore.KindSession,
	table: "comments.author, going_requests.author, comment_deliveries, message_deliveries, comment_fallback_copies," +
		" asks.author, ask_followers, events.payload, graph_nodes.made_by",
	note: "no table projects a session, so the row side is the sessions those tables name as authors, as reached," +
		" as askers and as followers, every session an ask.follower_added event named, the author of each run," +
		" and the author of each lens, finding, risk and constraint, and of each run whose row is gone, that" +
		" states its authored_by",
	rows: `
		select 'session ' || s.id, '` + graphstore.KindSession + `', s.id, ''
		from (` + recordedSessions + `) s where octet_length(s.id) <= ` + addressBound,
	nodes: `
		select n.id, '' from graph_nodes n where n.kind = '` + graphstore.KindSession + `'`,
	pastBound: `
		select count(*) from (` + recordedSessions + `) s where octet_length(s.id) > ` + addressBound,
}, {
	name:     graphstore.KindRole,
	nodeKind: graphstore.KindRole,
	table:    "comment_deliveries.target, messages.target, issues.route, events.payload",
	note: "no table projects a role, so the row side is the roles those tables address now and every role" +
		" an issue event's route, a message event's target or a new comment's mention ever named",
	rows: `
		select 'role ' || r.name, '` + graphstore.KindRole + `', r.name, ''
		from unnest($1::text[]) r (name) where octet_length(r.name) <= ` + addressBound,
	addressed: true,
	nodes: `
		select n.id, '' from graph_nodes n where n.kind = '` + graphstore.KindRole + `'`,
	pastBound: `
		select count(*) from unnest($1::text[]) r (name) where octet_length(r.name) > ` + addressBound,
}, {
	name:     graphstore.KindPage,
	nodeKind: graphstore.KindPage,
	table:    "comment_deliveries.target, messages.target, issues.route, events.payload",
	note: "a role's page, whose id is its role's: the row side is the pages of the roles those tables address" +
		" now and the event log ever named, and a session's own page is no part of this census",
	rows: `
		select 'page ' || '` + graphstore.KindRole + `/' || r.name, '` + graphstore.KindPage + `',
		       '` + graphstore.KindRole + `/' || r.name, ''
		from unnest($1::text[]) r (name)
		where octet_length('` + graphstore.KindRole + `/' || r.name) <= ` + addressBound,
	addressed: true,
	nodes: `
		select n.id, '' from graph_nodes n
		where n.kind = '` + graphstore.KindPage + `' and n.id like '` + graphstore.KindRole + `/%'`,
	pastBound: `
		select count(*) from unnest($1::text[]) r (name)
		where octet_length('` + graphstore.KindRole + `/' || r.name) > ` + addressBound,
}, baseAttributeCensus(graphstore.KindInteraction, graphstore.KindInteraction, nil, graphstore.VisibilityAuthor),
	baseAttributeCensus(graphstore.KindLens, graphstore.KindLens, nil, ""),
	baseAttributeCensus("finding", refs.BlockNodeKind, lensBlockID, ""),
	baseAttributeCensus(graphstore.KindRisk, graphstore.KindRisk, nil, ""),
	baseAttributeCensus(graphstore.KindConstraint, graphstore.KindConstraint, nil, ""),
	baseAttributeCensus(graphstore.KindLensKind, graphstore.KindLensKind, nil, graphstore.VisibilityProject),
}

// addressBound is the store's address bound (graphstore.MaxAddressBytes) as a statement spells
// it. It is written into the text rather than bound as a parameter because each side is spliced
// into a statement of its own and one shared constant keeps the two sides reading the same bound.
var addressBound = strconv.Itoa(graphstore.MaxAddressBytes)

// lensBlockPrefix is how a block node's id names a lens: a block of a lens's finding is addressed
// under its lens, dispatch://lens/<uuid>, so its id is `lens/<uuid>#<id>`.
const lensBlockPrefix = "lens/"

// lensBlock is true of the block node alias whose id names a lens: a block of a lens's finding
// (dispatch://INFRA-1950), which no document holds, so no document_blocks row projects it and no
// block_refs row names it. A document's block is `<ref_key>#<id>` (refs.IndexBlocks), and a
// ref_key begins with an issue's or a project's key, which is upper case, or with `agent/`
// (graphstore.ArtifactRefKey), so no document's block reads as one.
func lensBlock(alias string) string { return lensBlockID(alias + ".id") }

// lensBlockID is lensBlock over the SQL expression id, a block node's id however a statement
// reaches it: a node's own column, or the from_id of a tuple out of it.
func lensBlockID(id string) string {
	return `(` + id + ` like '` + lensBlockPrefix + `%')`
}

// The tuple kinds each projection row states of its node, which the census digests against the
// tuples of those kinds the node states now (currentLinks): a comment's anchor, its owner and its
// author; a block's document and the block or document holding it; and a run's author. A
// block_refs row states the kinds refs.BlockRefKindsSQL reads out of the store. A tuple of any
// other kind out of such a node is no part of any row.
var (
	commentLinkKinds = []string{graphstore.TupleAnchoredTo, graphstore.TupleOwnedBy, graphstore.TupleAuthoredBy}
	blockLinkKinds   = []string{refs.TupleInDocument, graphstore.TuplePartOf}
	runLinkKinds     = []string{graphstore.TupleAuthoredBy}
)

// kindIn is true of the tuple kind column whose value is one of kinds.
func kindIn(column string, kinds []string) string {
	return column + ` in ('` + strings.Join(kinds, `', '`) + `')`
}

// DigestedTuple is true of a tuple t (an alias of graph_tuples) of a kind a projection row of the
// node it goes out of states, so of a kind this census digests against that row: a comment's
// anchored_to, owned_by and authored_by, a document block's in_document and part_of and every kind
// block_refs projects, and a run's authored_by. It is the one definition the census's projections
// read their link kinds from (commentLinkKinds, blockLinkKinds, runLinkKinds) and the erase reads
// its blockers by (grapherase's readBlockers): a tuple of any other kind into a node - a
// requirement's constrained_by to a constraint, a changed_by_run to a run - is no row's to state,
// so erasing the node it points at, and the tuple with it, leaves no row stating a link the store
// no longer holds.
func DigestedTuple(t string) string {
	return `((` + t + `.from_kind = '` + graphstore.KindComment + `' and ` + kindIn(t+".kind", commentLinkKinds) + `)
		or (` + t + `.from_kind = '` + refs.BlockNodeKind + `' and not ` + lensBlockID(t+".from_id") + `
		    and (` + kindIn(t+".kind", blockLinkKinds) + ` or ` + t + `.kind in (` + refs.BlockRefKindsSQL + `)))
		or (` + t + `.from_kind = '` + graphstore.KindInteraction + `' and ` + kindIn(t+".kind", runLinkKinds) + `))`
}

// baseAttributeCensus is the census of the visibility and the state each node of one kind
// dispatch://INFRA-1950 adds carries, name the kind as the report names it and nodeKind the kind
// the store keys its nodes by, narrowed by narrow over a node id where it is not nil (a finding is
// a block node whose id names its lens). A node's row is the projection of its own base-attribute
// tuples (dispatch://INFRA-1149's R8), and no table projects these kinds, so the store's own
// record stands on the row side: for each node that states either attribute, the newest tuple of
// each, read as graphstore.CurrentValue reads one, against the node row's visibility and state
// columns. A column with no tuple behind it is a node no row projects, a tuple whose node has no
// row is a row naming a node the store does not hold, and a column that differs from its newest
// tuple is a row and a node that disagree; a node whose newest state is gone is held to a row at
// gone as the other kinds are (presentNodes, rowDifferences). The digest names each attribute and
// carries its value's md5, never a value of any other attribute.
//
// fixed is the one visibility R1 gives every node of the kind - author for a run, project for a
// lens kind - or empty for a kind whose nodes take either. The row side states it kept, and the
// node side reads whether the node's column keeps it, so a node at any other visibility is a
// difference even where its tuple says the same.
func baseAttributeCensus(name, nodeKind string, narrow func(id string) string, fixed string) projection {
	narrowed := func(id string) string {
		if narrow == nil {
			return ""
		}
		return " and " + narrow(id)
	}
	newest := func(attribute string) string {
		return `(select case when v.asserted then v.to_id end from graph_tuples v
				where v.from_kind = '` + nodeKind + `' and v.from_id = keyed.id and v.kind = '` + attribute + `'
				  and v.to_kind = '` + graphstore.KindValue + `'
				order by v.seq desc limit 1)`
	}
	digest := func(visibility, state string) string {
		return `'` + graphstore.AttributeVisibility + `:' || coalesce(md5(` + visibility + `), 'none')
		       || ' ` + graphstore.AttributeState + `:' || coalesce(md5(` + state + `), 'none')`
	}
	rowFixed, nodeFixed, note := "", "", ""
	if fixed != "" {
		rowFixed = ` || ' visibility_fixed:kept'`
		nodeFixed = ` || ' visibility_fixed:' || case when n.visibility = '` + fixed + `' then 'kept' else 'broken' end`
		note = ", and a node at any visibility but " + fixed + ", which R1 fixes for the kind, is a difference"
	}
	return projection{
		name:     name + "_visibility_state",
		nodeKind: nodeKind,
		table:    "graph_tuples",
		note: "no table projects a " + name + ", so the row side is the store's own record of each node's visibility and" +
			" state, the newest tuple of each, against the graph_nodes columns; a column with no tuple, a tuple with no" +
			" node row and a column that differs from its newest tuple are each a difference" + note,
		rows: `
		select '` + name + ` ' || held.id, '` + nodeKind + `', held.id, ` + digest("held.visibility", "held.state") + rowFixed + `
		from (
			select keyed.id, ` + newest(graphstore.AttributeVisibility) + ` as visibility,
			       ` + newest(graphstore.AttributeState) + ` as state
			from (
				select distinct t.from_id as id from graph_tuples t
				where t.from_kind = '` + nodeKind + `' and t.to_kind = '` + graphstore.KindValue + `'
				  and t.kind in ('` + graphstore.AttributeVisibility + `', '` + graphstore.AttributeState + `')` + narrowed("t.from_id") + `
			) keyed
		) held
		where held.state is distinct from '` + graphstore.StateGone + `'`,
		nodes: `
		select n.id, ` + digest("n.visibility", "n.state") + nodeFixed + `
		from graph_nodes n where n.kind = '` + nodeKind + `'` + narrowed("n.id"),
		mayBeEmpty: true,
	}
}

// statedLinks is the links a projection row states, as one value its digest can carry: links are
// SQL expressions over the row, each answering one link written kind>to_kind:to_id, or null where
// the row states no such link. They are joined in order as currentLinks joins the store's, and
// a row that states none reads the empty string.
func statedLinks(links ...string) string {
	return `(select coalesce(string_agg(stated.link, ',' order by stated.link), '')
		from (values (` + strings.Join(links, `), (`) + `)) stated (link))`
}

// currentLinks is every link the node n of kind fromKind states now under the tuple kinds the
// predicate kinds selects (written over t.kind), read as a statement reads a current relation:
// the newest tuple per (kind, far end), kept while that newest one asserts
// (graphstore.CurrentTupleRead). Each is written kind>to_kind:to_id, as statedLinks writes a
// row's, and they are joined in order, so a node that states none reads the empty string. A link
// whose newest tuple is a retraction is absent here exactly as a tuple never written is. The read
// walks graph_tuples_current, whose columns are the ones the distinct on and the order by name.
//
// A block's part_of is one of these links, so a block that states no parent at all and a block
// the document holds directly read differently: the first has no part_of link and the second
// has the one to its document, which is the link its row's null parent_block_id states.
func currentLinks(fromKind, kinds string) string {
	return `(select coalesce(string_agg(held.link, ',' order by held.link), '')
	 from (
		` + graphstore.CurrentTupleRead(`t.kind, t.to_kind, t.to_id`,
		`t.kind || '>' || t.to_kind || ':' || t.to_id as link, t.asserted`,
		`t.from_kind = '`+fromKind+`' and t.from_id = n.id
		  and t.to_kind <> '`+graphstore.KindValue+`' and `+kinds) + `
	 ) held where held.asserted)`
}

// authorNode is the far end of the authored_by tuple a row whose author is the jsonb actor states,
// written kind:id: the node graphstore.AuthorOf reads off the actor the row stored, a login
// canonicalised as recordedPeople canonicalises one, and null for an actor that names nobody,
// whose row states no authored_by at all.
func authorNode(actor string) string {
	return `case
			when ` + actor + `->>'kind' = 'user' and coalesce(` + actor + `->>'id', '') <> ''
				then '` + graphstore.KindPerson + `:' || user_threads_login(` + actor + `->>'id')
			when ` + actor + `->>'kind' = 'session' and coalesce(` + actor + `->>'id', '') <> ''
				then '` + graphstore.KindSession + `:' || (` + actor + `->>'id')
			when coalesce(` + actor + `->>'owner', '') <> ''
				then '` + graphstore.KindPerson + `:' || user_threads_login(` + actor + `->>'owner')
		end`
}

// authoredLink is the authored_by link a row whose author is the jsonb actor states, as
// statedLinks takes one: kind>to_kind:to_id, null for an actor naming nobody and for a session
// past the store's bound, of whom the writer states nothing (graphstore.AuthorFacts).
func authoredLink(actor string) string {
	return `case when ` + actor + `->>'kind' <> '` + graphstore.KindSession + `'
		           or octet_length(coalesce(` + actor + `->>'id', '')) <= ` + addressBound + `
		      then '` + graphstore.TupleAuthoredBy + `>' || ` + authorNode(actor) + ` end`
}

// commentStoreJoins are the two artifacts a comment's relations name, joined so that the bound
// below can read their addresses: the document its anchor names and the artifact that owns it.
// It follows `from comments c`.
const commentStoreJoins = `
		left join artifacts anchor on anchor.id = (c.anchor->>'artifact_id')::uuid
		left join artifacts owner on owner.id = c.artifact_id`

// commentInTheStore is true of a comments row the store holds a node for: one whose anchor's
// and owner's addresses both fit the store's bound (commentStoreJoins names the two). A comment
// with a longer one is left whole out of the store - no node, no tuple and no stamp, so that
// the old comments arm answers both of its edges (api's commentStored) - and the backfill
// leaves it alone for the same reason. So no such row names an author the store is held to a
// node for, and every side that reads the people and the sessions comments name binds this.
var commentInTheStore = `(anchor.ref_key is null or octet_length(anchor.ref_key) <= ` + addressBound + `)
		  and (owner.ref_key is null or octet_length(owner.ref_key) <= ` + addressBound + `)`

// recordedPeople are the people the projected tables name as the author of a row, as
// graphstore.AuthorOf reads an actor: the login an actor of kind user carries, or the owner a
// personal token's actor carries, each canonicalised the way user_threads_login canonicalises a
// login (migration 0095, which is model.CanonicalLogin in SQL). Beside them are the owners of the
// sessions the server proved that wrote a comment: the comment's author is the session, whose act
// names that same session and whose owner is the person it belongs to, and the write that ties
// the two with an alias_of tuple makes the owner's node with it (graphstore.AuthorFacts), except
// for a session past the store's bound, of which nothing is written.
//
// Only the comments the store holds name anybody here (commentInTheStore): a comment anchored
// to or owned by a document whose address is past the bound is left whole out of the store, so
// it states no authored_by and makes no author node, and a person whose only comment is one of
// those is no node of the store and no difference of this census.
//
// The authors of the reasoning nodes (reasoningAuthored) are read off their made_by by the same
// two rules, since their writer states each author through graphstore.AuthorFacts as the comment
// path does, and so are the authors of the runs going_requests holds and of the runs whose row
// is gone (goneRunsAuthored), whose writer (graphstore.InteractionFacts) states its author the
// same way.
var recordedPeople = `
	select distinct user_threads_login(author.login) as login
	from (
		select ` + authorLogin("c.author") + ` as login
		from comments c` + commentStoreJoins + `
		where ` + commentInTheStore + `
		union all
		select c.author->>'owner'
		from comments c` + commentStoreJoins + `
		where ` + provedSessionOwner("c.author") + `
		  and ` + commentInTheStore + `
		union all
		select ` + authorLogin("r.author") + `
		from going_requests r
		union all
		select r.author->>'owner'
		from going_requests r
		where ` + provedSessionOwner("r.author") + `
		union all
		select ` + authorLogin("r.author") + `
		from (` + storeAuthored + `) r
		union all
		select r.author->>'owner'
		from (` + storeAuthored + `) r
		where ` + provedSessionOwner("r.author") + `
	) author
	where author.login is not null and user_threads_login(author.login) <> ''`

// authorLogin is the login of the person graphstore.AuthorOf names of the actor a jsonb column
// holds, uncanonicalised, and null for an actor it names a session of or nobody.
func authorLogin(actor string) string {
	return `case
			when ` + actor + `->>'kind' = 'user' and coalesce(` + actor + `->>'id', '') <> '' then ` + actor + `->>'id'
			when ` + actor + `->>'kind' = 'session' and coalesce(` + actor + `->>'id', '') <> '' then null
			when coalesce(` + actor + `->>'owner', '') <> '' then ` + actor + `->>'owner'
		end`
}

// provedSessionOwner is true of a session author the server proved its owner's (graphstore's
// provedOwner): the act names that same session, within the store's bound. Its owner is a person
// the write that states the session's authorship makes a node of.
func provedSessionOwner(actor string) string {
	return actor + `->>'kind' = 'session' and coalesce(` + actor + `->>'id', '') <> ''
		  and ` + actor + `->'act'->>'session' = ` + actor + `->>'id'
		  and octet_length(` + actor + `->>'id') <= ` + addressBound
}

// reasoningAuthored are the nodes of dispatch://INFRA-1950's reasoning kinds - a lens, a finding
// of one (a block whose id names its lens, lensBlock), a risk and a constraint - that hold an
// asserted authored_by tuple, each with the actor its made_by records (author) and its made_at
// (at). Their one writer (api's writeReasoningNodes) states that tuple, and the author's own node
// beside it, only where the credential proved an author, so the author graphstore.AuthorOf names
// of the made_by is a person or a session the store owes a node; a node written on the shared
// bearer states none, and the session its body claimed is on neither side.
var reasoningAuthored = authoredNodes(`(n.kind in ('` + graphstore.KindLens + `', '` + graphstore.KindRisk + `', '` +
	graphstore.KindConstraint + `') or (n.kind = '` + refs.BlockNodeKind + `' and ` + lensBlock("n") + `))`)

// goneRunsAuthored are the runs whose row is gone - a process of the release before deleted it an
// hour old during a roll, and the backfill stated its interaction gone (backfillGoneRuns) - that
// hold an asserted authored_by tuple, in the shape of reasoningAuthored. The row that named the
// author went with the run, so the store's own record of the writer stands on the row side, as it
// does for a reasoning node: an author's node is history and stays, and without this a person
// whose one write was such a run would read as a node no row projects.
var goneRunsAuthored = authoredNodes(`n.kind = '` + graphstore.KindInteraction + `' and n.state = '` + graphstore.StateGone + `'`)

// storeAuthored is every node whose own record names an author the row sides of person and
// session take: reasoningAuthored and goneRunsAuthored.
var storeAuthored = reasoningAuthored + `
		union all` + goneRunsAuthored

// authoredNodes is the nodes the predicate which (over graph_nodes n) keeps that hold an asserted
// authored_by tuple, each with its made_by (author) and its made_at (at).
func authoredNodes(which string) string {
	return `
		select n.made_by as author, n.made_at as at
		from graph_nodes n
		where ` + which + `
		  and exists (
			select 1 from (
				` + graphstore.CurrentTupleRead(`t.to_kind, t.to_id`, `t.asserted`,
		`t.from_kind = n.kind and t.from_id = n.id and t.kind = '`+graphstore.TupleAuthoredBy+`'`) + `
			) held where held.asserted)`
}

// recordedSessions are the sessions a row of the store names, each once, with the first event
// that named it (null for a session only a row names) and the earliest time a row or an event
// names it. They are every write R14 records a session from: a comment a session wrote
// (graphstore.AuthorOf); every session a delivery reached - a comment mention's and a targeted
// message's attempt alike, whether the outbox's record states it (outbox.deliveredGraph) or the
// attempt's own settle does (graphstore.Reached), and a fallback copy the outbox published to a
// session's own topic (outbox.fallbackDelivered); an ask a session opened, every session that
// follows an ask now, and every session an ask.follower_added event ever named (asks.Follow), so a
// follower added and removed before the store existed is held to a node as well. A node is
// history and never goes, so a session that once followed still owes it.
//
// R14 is exact about which attempt is a delivery: an attempt the listener took is one, and a
// pending or failed one is not. So this binds the two states that mean it
// (model.DeliveryTakenStatesSQL), the test the writer binds too (graphstore.Reached): an
// attempt still pending has reached nobody yet and a failed one never will, and counting
// either here would hold the store to a node no writer is meant to have written. A queued
// attempt later answered is read by the same test before and after, so the flip to sent owes
// no node the settle did not already write.
//
// Only the comments the store holds name a session here (commentInTheStore), for the reason
// recordedPeople binds the same: a comment left out of the store records no author at all. The
// author of a run going_requests holds is a session here where the row's author is one, and the
// author of a reasoning node, or of a run whose row is gone, where its made_by is one
// (storeAuthored).
//
// It is one definition read by two: the census's session row side and the backfill's session
// pass (backfillSessions), so the sessions the backfill gives a node are exactly the sessions the
// census holds to one, as addressedRoles is for roles.
var recordedSessions = `
	select id, min(event) as event, min(at) as at from (
		select c.author->>'id' as id, null::bigint as event, c.created_at as at
		from comments c` + commentStoreJoins + `
		where c.author->>'kind' = 'session' and coalesce(c.author->>'id', '') <> ''
		  and ` + commentInTheStore + `
		union all
		select r.author->>'id', null, r.created_at from going_requests r
		where r.author->>'kind' = 'session' and coalesce(r.author->>'id', '') <> ''
		union all
		select r.author->>'id', null, r.at from (` + storeAuthored + `) r
		where r.author->>'kind' = 'session' and coalesce(r.author->>'id', '') <> ''
		union all
		select d.session_id, null, d.created_at from comment_deliveries d
		where coalesce(d.session_id, '') <> '' and d.state in ` + model.DeliveryTakenStatesSQL + `
		union all
		select d.session_id, null, d.created_at from message_deliveries d
		where coalesce(d.session_id, '') <> '' and d.state in ` + model.DeliveryTakenStatesSQL + `
		union all
		select substring(f.destination from ` + strconv.Itoa(len(contracts.AgentTopicPrefix)+1) + `), null, f.created_at
		from comment_fallback_copies f
		where f.published and f.destination like '` + contracts.AgentTopicPrefix + `%'
		union all
		select a.author->>'id', null, a.created_at from asks a
		where a.author->>'kind' = 'session' and coalesce(a.author->>'id', '') <> ''
		union all
		select f.session_id, null, f.since from ask_followers f
		union all
		select e.payload->>'session_id', e.id, e.created_at from events e
		where e.type = 'ask.follower_added'
	) sessions
	where id is not null and id <> ''
	group by id`

// addressedRoles are the roles a row of the store addresses now or an event of the log has ever
// addressed (R19), each once, with the first event that named it, null for a role only a row
// names. The rows are a comment mention's target, a message's target and an issue's route as they
// stand now. The events are the route every issue event recorded the issue under, the target
// every message event recorded, and the target of each mention a new comment's event named. A
// node is history and never goes, so an issue routed to one role and then re-routed to another
// still owes the first its node and its page: issues.route names only the second, and the
// issue's own events name both.
//
// It is one definition read by two: the census's role and page row side (readAddressedRoles)
// and the backfill's role pass (backfillRoles), so the roles the backfill gives a node are
// exactly the roles the census holds to one. The publisher writes a role's node and its page from
// those same fields of those same events (outbox.addressedRoles), whether or not it routed
// anything to the role. Every role a row names now was named by the event its write appended, so
// the two sides agree once the outbox has read the log; a role named by an event the outbox has
// not read yet is a row with no node until it does.
//
// It reads every event of six types, which no index narrows, so a census reads it once and binds
// the names into every statement that reads the role and page rows.
const addressedRoles = `
	select name, min(event) as event from (
		select substring(d.target from 6) as name, null::bigint as event
		from comment_deliveries d where d.target like 'role:%'
		union all
		select substring(m.target from 6), null from messages m where m.target like 'role:%'
		union all
		select substring(i.route from 6), null from issues i where i.route like 'role:%'
		union all
		select substring(addressed.target from 6), e.id
		from events e
		cross join lateral (
			select e.payload->>'route' where e.type in ('issue.created', 'issue.updated', 'issue.closed')
			union all
			select e.payload->>'target' where e.type in ('message.created', 'message.answered')
			union all
			select mention->>'target'
			from jsonb_array_elements(case when e.type = 'comment.created'
			                                and jsonb_typeof(e.payload->'mentions') = 'array'
			                               then e.payload->'mentions' end) mention
		) addressed (target)
		where e.type in ('issue.created', 'issue.updated', 'issue.closed',
		                 'message.created', 'message.answered', 'comment.created')
		  and addressed.target like 'role:%'
	) roles
	where name <> ''
	group by name`

// readAddressedRoles reads the role row side once for a run of the census: the name of every
// role addressedRoles names, in one statement, so the event log is scanned once however many
// statements then read the role and page rows. It runs inside the census's snapshot, so every
// statement after it reads the names that snapshot holds.
func readAddressedRoles(ctx context.Context, tx pgx.Tx) ([]string, error) {
	rows, err := tx.Query(ctx, `select addressed.name from (`+addressedRoles+`) addressed`)
	if err != nil {
		return nil, fmt.Errorf("read the roles the store addresses: %w", err)
	}
	defer rows.Close()
	names := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("read a role the store addresses: %w", err)
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// StoreCensus reads the node and tuple store against the tables that project it, in one
// read-only repeatable-read transaction on a connection of its own. It writes nothing: the
// transaction is read only, and every statement it runs is a select.
//
// A difference is reported by its cause and by the row it was found at, in both directions: a
// projection row naming no node, naming a node the store does not hold or naming one it holds
// gone; a row whose shared fields disagree with its node's; and a node the store holds present
// that no row projects, named with its state.
//
// Two writes can state one fact twice by design and nothing here reads wrong for it: a tuple at
// the source_event sentinel is outside both repeat keys, and the backfill writes every fact it
// cannot date from a row's own event at that sentinel, so two runs at once that both read a row
// before either names its node each append a tuple stating the same fact. The current read takes
// the newest asserting tuple per far end, which is that same fact, and this census counts nodes
// and projection rows rather than tuples.
//
// sizing adds the growth pass (SizingReport), which reads the same snapshot. The growth pass
// reads no table of the store, so it runs on a database this branch's migrations have not
// reached, where the census reads no kind and says the store is absent.
func StoreCensus(ctx context.Context, databaseURL string, sizing bool) (*StoreReport, error) {
	return storeCensus(ctx, databaseURL, sizing, nil)
}

// storeCensus is StoreCensus with afterKind, which runs once each kind has been read and before
// anything after it is, on the census's own context, and whose error ends the census. Only a
// test passes one: it is how a test commits a write between two of the census's reads and asks
// whether the reads after it still answer the snapshot the first one took. Nil runs nothing.
func storeCensus(ctx context.Context, databaseURL string, sizing bool, afterKind func(ctx context.Context, kind string) error) (*StoreReport, error) {
	conn, err := connect(ctx, databaseURL, applicationName)
	if err != nil {
		return nil, err
	}
	defer conn.Close(ctx)
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, fmt.Errorf("open the census snapshot: %w", err)
	}
	defer tx.Rollback(ctx)
	report := &StoreReport{}
	// The store is looked up before any kind is read, so a database the store does not exist in
	// answers that rather than failing at the first statement that names graph_nodes.
	if err := tx.QueryRow(ctx, `select to_regclass('graph_nodes') is null`).Scan(&report.Absent); err != nil {
		return nil, fmt.Errorf("look up the store: %w", err)
	}
	if !report.Absent {
		if report.Kinds, err = censusOfKinds(ctx, tx, afterKind); err != nil {
			return nil, err
		}
		if report.Projects, err = ProjectCensus(ctx, tx); err != nil {
			return nil, err
		}
		if report.ProjectFindings() > 0 {
			incomplete, err := ProjectComplete(ctx, tx)
			if err != nil {
				return nil, err
			}
			report.ProjectIncomplete = incomplete[:min(len(incomplete), reportedDifferences)]
		}
	}
	if sizing {
		sized, err := readSizing(ctx, tx)
		if err != nil {
			return nil, err
		}
		report.Sizing = sized
	}
	return report, nil
}

// censusOfKinds reads every kind of projections in order, running afterKind after each.
func censusOfKinds(ctx context.Context, tx pgx.Tx, afterKind func(ctx context.Context, kind string) error) ([]KindReport, error) {
	var kinds []KindReport
	// The roles are read before the first kind that binds them and kept for every kind after it.
	var addressed []string
	for _, held := range projections {
		var args []any
		if held.addressed {
			if addressed == nil {
				var err error
				if addressed, err = readAddressedRoles(ctx, tx); err != nil {
					return nil, err
				}
			}
			args = []any{addressed}
		}
		kind, err := censusOfKind(ctx, tx, held, args)
		if err != nil {
			return nil, err
		}
		kinds = append(kinds, kind)
		if afterKind != nil {
			if err := afterKind(ctx, held.name); err != nil {
				return nil, err
			}
		}
	}
	return kinds, nil
}

// censusOfKind reads one kind: how many rows and present nodes there are, and the differences in
// each direction with the first reportedDifferences of them. args are what the kind's rows bind,
// none but for a kind that reads the addressed roles.
//
// The four counts are four scalar subqueries of one statement rather than four round trips, so
// a census costs one read a kind here and not four. Each subquery is the statement it was on its
// own, so each is planned and streamed exactly as before.
func censusOfKind(ctx context.Context, tx pgx.Tx, held projection, args []any) (KindReport, error) {
	kind := KindReport{Kind: held.name, NodeKind: held.nodeKind, Table: held.table, Note: held.note, MayBeEmpty: held.mayBeEmpty}
	// pastBound is a scalar `select count(*)`, so it is read directly rather than wrapped in a
	// count of its own row; a kind no address of which can pass the bound reads zero.
	pastBound := held.pastBound
	if pastBound == "" {
		pastBound = "select 0"
	}
	if err := tx.QueryRow(ctx, `select
		(select count(*) from (`+held.rows+`) rows (row_label, node_kind, node_address, digest)),
		(select count(*) from (`+presentNodes(held)+`) nodes (id, digest, state)),
		(select count(*) from (`+rowDifferences(held)+`) rows_found),
		(select count(*) from (`+nodeDifferences(held)+`) nodes_found),
		(`+pastBound+`)`, args...,
	).Scan(&kind.Rows, &kind.Nodes, &kind.RowDifferences, &kind.NodeDifferences, &kind.PastBound); err != nil {
		return KindReport{}, fmt.Errorf("census of %s: %w", held.name, err)
	}
	var err error
	if kind.RowDifferences > 0 {
		kind.ReportedRows, err = readDifferences(ctx, tx, held.name, rowDifferences(held), args)
		if err != nil {
			return KindReport{}, err
		}
	}
	if kind.NodeDifferences > 0 {
		kind.ReportedNodes, err = readDifferences(ctx, tx, held.name, nodeDifferences(held), args)
		if err != nil {
			return KindReport{}, err
		}
	}
	return kind, nil
}

// presentNodes is the kind's node side bound to the nodes the store holds present, which is every
// node not at state gone (R12 of dispatch://INFRA-1950): a node at proposed, settled or overtaken
// is a node a row projects as one at live is, so a move between those states is no lost node. It
// answers each node's state beside its digest, which the report names a node with. A node the
// store holds gone is no node a row may project, and the row side holds it to that separately.
func presentNodes(held projection) string {
	return `select node.id, node.digest, present.state from (` + held.nodes + `) node (id, digest)
		join graph_nodes present on present.kind = '` + held.nodeKind + `' and present.id = node.id
		                        and present.state <> '` + graphstore.StateGone + `'`
}

// rowDifferences reads the row side against the store: every row that names no node, names one
// the store does not hold, names one it holds gone, or disagrees with its node's digest. The
// gone test is its own cause rather than a missing node, because a row projecting a node the
// store has ended is a different defect from a row projecting nothing, and it is the one test
// of a node's state here: a row may project a node at any other state.
func rowDifferences(held projection) string {
	return `
		select case
			when row.node_kind is null or row.node_address is null then 'the row names no node'
			when node.kind is null then 'the row names a node the store does not hold'
			when node.state = '` + graphstore.StateGone + `'
				then 'the row names a node the store holds ' || node.state
			else 'the row and its node disagree: the row digests ' || row.digest
			     || ' and the node ' || coalesce(present.digest, '(none)')
		end,
		row.row_label
		from (` + held.rows + `) row (row_label, node_kind, node_address, digest)
		left join graph_nodes node on node.kind = row.node_kind and node.id = row.node_address
		left join (` + presentNodes(held) + `) present (id, digest, state) on present.id = row.node_address
		where row.node_kind is null or row.node_address is null or node.kind is null
		   or node.state = '` + graphstore.StateGone + `'
		   or row.digest is distinct from present.digest
		order by 2, 1`
}

// nodeDifferences reads the store against the row side: every node of the kind the store holds
// present that no row projects, named with the state it is at, so a proposed node with no row
// reads as a proposed one and a live one as `a live node no row projects`.
func nodeDifferences(held projection) string {
	return `
		select case when node.state ~ '^[aeiou]' then 'an ' else 'a ' end || node.state
		       || ' node no row projects',
		       '` + held.nodeKind + ` ' || node.id
		from (` + presentNodes(held) + `) node (id, digest, state)
		where not exists (
			select 1 from (` + held.rows + `) row (row_label, node_kind, node_address, digest)
			where row.node_kind = '` + held.nodeKind + `' and row.node_address = node.id)
		order by 2`
}

func readDifferences(ctx context.Context, tx pgx.Tx, kind, sql string, args []any) ([]Difference, error) {
	rows, err := tx.Query(ctx, sql+` limit `+strconv.Itoa(reportedDifferences), args...)
	if err != nil {
		return nil, fmt.Errorf("read the differences of %s: %w", kind, err)
	}
	defer rows.Close()
	var found []Difference
	for rows.Next() {
		var difference Difference
		if err := rows.Scan(&difference.Cause, &difference.At); err != nil {
			return nil, fmt.Errorf("read a difference of %s: %w", kind, err)
		}
		found = append(found, difference)
	}
	return found, rows.Err()
}

// Write prints the report, one `census: store ` line per fact, every difference under its kind,
// and the sizing after it, ending with the verdict. It prints counts, digests, kinds and the
// addresses of rows and nodes; no body, no actor and no value of an attribute leaves the
// database.
func (r *StoreReport) Write(w io.Writer) {
	if r.Absent {
		fmt.Fprintln(w, "census: store absent (the database holds no graph_nodes: this branch's migrations have not run, so no kind was read)")
	}
	for _, kind := range r.Kinds {
		fmt.Fprintf(w, "census: store kind=%s nodes=%s rows=%s table=%s rows_without_a_node=%s nodes_without_a_row=%s addresses_past_bound=%s\n",
			kind.Kind, pgmigrate.Thousands(kind.Nodes), pgmigrate.Thousands(kind.Rows), kind.Table,
			pgmigrate.Thousands(kind.RowDifferences), pgmigrate.Thousands(kind.NodeDifferences),
			pgmigrate.Thousands(kind.PastBound))
		fmt.Fprintf(w, "census: store kind=%s checks %s\n", kind.Kind, kind.Note)
		switch {
		case kind.Rows == 0 && kind.MayBeEmpty:
			fmt.Fprintf(w, "census: store kind=%s checked no row, so the census proves nothing of it; a deployment may hold none, so it refuses nothing\n", kind.Kind)
		case kind.Rows == 0:
			fmt.Fprintf(w, "census: store kind=%s checked no row, so the census proves nothing of it\n", kind.Kind)
		}
		writeDifferences(w, kind.Kind, kind.RowDifferences, kind.ReportedRows)
		writeDifferences(w, kind.Kind, kind.NodeDifferences, kind.ReportedNodes)
	}
	writeProjectCensus(w, r.Projects)
	for _, row := range r.ProjectIncomplete {
		fmt.Fprintf(w, "census: project incomplete %s\n", row)
	}
	if !r.Absent {
		fmt.Fprintf(w, "census: store rows_checked=%s differences=%s\n",
			pgmigrate.Thousands(r.Rows()), pgmigrate.Thousands(r.Differences()))
	}
	if r.Sizing != nil {
		r.Sizing.Write(w)
	}
	empty := r.Unpopulated()
	switch {
	case r.Absent && r.Sizing == nil:
		fmt.Fprintln(w, "census: store REFUSED (the database holds no store, so the census read nothing)")
	case r.Absent && r.Sizing.Refused():
		fmt.Fprintf(w, "census: store absent, sizing REFUSED (%d reason(s))\n", len(r.Sizing.Refusals))
	case r.Absent:
		fmt.Fprintln(w, "census: store absent, sizing ok")
	case r.Differences() > 0:
		fmt.Fprintf(w, "census: store REFUSED (%s difference(s))\n", pgmigrate.Thousands(r.Differences()))
	case r.ProjectFindings() > 0:
		fmt.Fprintf(w, "census: store REFUSED (%s row(s) whose node names no project or the wrong one)\n",
			pgmigrate.Thousands(r.ProjectFindings()))
	case len(empty) > 0:
		fmt.Fprintf(w, "census: store REFUSED (kind(s) %s checked no row, so the census proves nothing of them)\n",
			strings.Join(empty, ", "))
	case r.Sizing != nil && r.Sizing.Refused():
		fmt.Fprintf(w, "census: store ok, sizing REFUSED (%d reason(s))\n", len(r.Sizing.Refusals))
	default:
		fmt.Fprintln(w, "census: store ok")
	}
}

func writeDifferences(w io.Writer, kind string, total int64, found []Difference) {
	for _, difference := range found {
		fmt.Fprintf(w, "census: store kind=%s difference at %s: %s\n", kind, difference.At, difference.Cause)
	}
	if total > int64(len(found)) {
		fmt.Fprintf(w, "census: store kind=%s and %s more difference(s) not printed\n",
			kind, pgmigrate.Thousands(total-int64(len(found))))
	}
}
