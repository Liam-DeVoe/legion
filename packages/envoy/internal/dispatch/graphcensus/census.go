package graphcensus

import (
	"context"
	"fmt"
	"io"
	"slices"
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
	// kinded says nodes answers (kind, id, digest) rather than (id, digest): a kind whose tuples
	// hang on nodes of more than one kind, as shaped hangs on a decision's block node and on an
	// ask's node. nodeKind then names those kinds for the report alone.
	kinded bool
	// addressed says rows reads the roles addressedRoles names as $1, which the census reads once
	// for the run (readAddressedRoles) rather than once in every statement that reads the rows.
	addressed bool
	// pastBound counts the addresses this kind's row side names that are past the store's bound
	// (graphstore.MaxAddressBytes), which the writers left unprojected on purpose: it is a full
	// `select count(*)`, reads $1 where addressed, and is empty for a kind no address of which can
	// pass the bound. The census reports the count as a class of its own and excludes those
	// addresses from both comparisons, so a row no store entry answers by design is no difference.
	pastBound string
	// unmarked counts the rows of the kind's table that name no node and are no difference: an
	// asks or ask_followers row a process of the release before wrote, which graph_edges' old
	// arms answer until the decisions step's backfill or catch-up marks it (dispatch://INFRA-1150
	// R3). It is a full `select count(*)`; the row side reads marked rows alone, and the census
	// reports the count as a class of its own, which the handoff's list for the root names as the
	// test of the three old arms. Empty for a kind whose row names no node only by a defect.
	unmarked string
	// owed is a predicate over node.id, a present node of the kind, that holds where the node's row
	// is owed by a writer that has not run yet rather than missing: such a node is no node-side
	// difference. Empty for a kind whose every node has its row from the write that stated it.
	owed string
	// emptyAgrees says a kind that holds no row and no present node is no refusal: one of the kinds
	// of dispatch://INFRA-1150, a decision's, a reading's and an answered or followed ask's, which
	// a deployment holds none of until an agent writes a decision or a person answers an ask. A
	// kind of INFRA-1149's is one every store holds rows of, and checking none of it refuses.
	emptyAgrees bool
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
	// CountsUnmarked says the kind holds its table's rows that name no node as a class of their
	// own, and Unmarked is how many there are, none of them a difference (projection.unmarked).
	CountsUnmarked bool
	Unmarked       int64
	// EmptyAgrees says a kind that read no row and no present node is no refusal
	// (projection.emptyAgrees).
	EmptyAgrees bool
}

// Empty reports whether the kind read no row and no present node, and so compared nothing.
func (k KindReport) Empty() bool { return k.Rows == 0 && k.Nodes == 0 }

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
	// StoreOnly is, for each tuple kind no table projects (copied_from and moved_to), how many
	// the store states now and how many of those reach a far end that does not resolve
	// (storeOnlyKinds). An unresolved far end refuses the census; a count does not.
	StoreOnly []StoreOnlyReport
	// Sizing is what the sizing pass found, or nil when it was not asked for.
	Sizing *SizingReport
}

// StoreOnlyReport is one tuple kind no table projects: how many relations of it the store states
// now, and how many of those reach a far end that does not resolve.
type StoreOnlyReport struct {
	Kind               string
	Tuples, Unresolved int64
}

// Unresolved is how many far ends of the store-only kinds do not resolve, over every such kind.
func (r *StoreReport) Unresolved() int64 {
	var unresolved int64
	for _, kind := range r.StoreOnly {
		unresolved += kind.Unresolved
	}
	return unresolved
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

// Unpopulated is every kind the census checked no row of, in the order it read them, but a kind
// that may be empty (KindReport.EmptyAgrees) and is: no row and no present node.
func (r *StoreReport) Unpopulated() []string {
	var empty []string
	for _, kind := range r.Kinds {
		if kind.Rows == 0 && !(kind.EmptyAgrees && kind.Empty()) {
			empty = append(empty, kind.Kind)
		}
	}
	return empty
}

// Refused reports whether the census refuses. It passes only where it checked more than zero
// rows of every kind it covers and found no difference in either direction (R19), and where every
// far end of a kind no table projects resolves. A total over the kinds is no such proof: two
// empty sides of one kind agree, and one kind's rows say nothing about another's, so a census
// whose comments carried every row would pass while it proved nothing of the blocks, the roles or
// the sessions. A kind of dispatch://INFRA-1150 is the one exception: no row and no node of it is
// a store nothing of that kind was written to yet, and is no refusal (projection.emptyAgrees).
//
// A database the store does not exist in yet (Absent) has nothing to census. The sizing is taken
// on just such a database (R4 reads before the first migration), so there the sizing alone
// decides, and a census asked for without it refuses, since it read nothing.
func (r *StoreReport) Refused() bool {
	if r.Absent {
		return r.Sizing == nil || r.Sizing.Refused()
	}
	if len(r.Unpopulated()) > 0 || r.Differences() > 0 || r.ProjectFindings() > 0 || r.Unresolved() > 0 {
		return true
	}
	return r.Sizing != nil && r.Sizing.Refused()
}

// projections are the kinds the census holds the store to, in the order it reads and reports
// them. The first three are the kinds a table projects, each with that table: the comment, then
// the block and the block reference (blockProjections); the four after them are the people and
// the agents, which no table projects (actorProjections); and the eight after those are
// dispatch://INFRA-1150's, the ties of decisions, readings and asks that block_refs,
// document_blocks, asks and ask_followers project (decisionProjections). The comment kind is read
// first: TestTheStoreCensusReadsOneSnapshot commits a write after it and before the block kind.
//
// Each topic of the store keeps its kinds in a list in a file of its own, census_<topic>.go, and
// projections is those lists one after another, so a kind of one topic is registered in that
// topic's file alone. A topic whose list is empty projects no kind of its own.
//
// Each of the first three holds a row to the tuples that answer it as well as to its node row.
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
var projections = slices.Concat(
	commentProjections, blockProjections, actorProjections, decisionProjections,
	issueProjections, componentProjections, resultProjections, versionProjections, refProjections,
	approvalProjections, searchProjections, citationProjections, queryRoleProjections,
)

// backfillPasses are the backfill's passes (Backfill), in the order it runs them, each topic's
// taken from its list in census_<topic>.go as projections takes its kinds. The order is what the
// passes rest on. The blocks run before the links out of them (blockBackfills). The sessions run
// after the comments, so that the session pass writes only the sessions the comment pass could
// not give a node (backfillSessions). The project passes run after every pass that writes a
// comment's or a block's node, so that such a node takes its project from the pass that wrote it
// and the fill finds nothing of it to do (projectBackfills). The asks and their followers run
// after the project passes, whose object pass wrote the node of every ask (decisionBackfills).
// Every other topic's passes run after those, once every issue, document, ask and message holds
// its node.
var backfillPasses = slices.Concat(
	blockBackfills, commentBackfills, actorBackfills, projectBackfills, decisionBackfills,
	issueBackfills, componentBackfills, resultBackfills, versionBackfills, refBackfills,
	approvalBackfills, searchBackfills, citationBackfills, queryRoleBackfills,
)

// commentBackfills are the backfill's pass over the comments (backfillComments).
var commentBackfills = []backfillPass{oneKind(backfillComments)}

// actorBackfills are the backfill's passes over the roles and their pages and over the sessions,
// which no projection row names (backfillRoles, backfillSessions).
var actorBackfills = []backfillPass{oneKind(backfillRoles), oneKind(backfillSessions)}

// projectBackfills are the passes of the project of every node (dispatch://INFRA-3862): the node
// of each issue, document, ask and message written before its kind's writer wrote one, and the
// project of each comment and block node written before a node named its project
// (backfillObjectNodes, backfillScopes).
var projectBackfills = []backfillPass{backfillObjectNodes, backfillScopes}

// commentProjections are the census's kind of the comments, which the comments table projects.
var commentProjections = []projection{{
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
		`case when c.author->>'kind' <> '`+graphstore.KindSession+`'
		           or octet_length(coalesce(c.author->>'id', '')) <= `+addressBound+`
		      then '`+graphstore.TupleAuthoredBy+`>' || `+commentAuthor+` end`,
	) + `)
		from comments c` + commentStoreJoins + `
		where ` + commentInTheStore,
	nodes: `
		select n.id, md5(n.made_at::text || '|' || n.made_by::text || '|' || ` + currentLinks(graphstore.KindComment,
		`t.kind in ('`+graphstore.TupleAnchoredTo+`', '`+graphstore.TupleOwnedBy+`', '`+graphstore.TupleAuthoredBy+`')`) + `)
		from graph_nodes n where n.kind = 'comment'`,
	pastBound: `
		select count(*) from comments c
		left join artifacts anchor on anchor.id = (c.anchor->>'artifact_id')::uuid
		left join artifacts owner on owner.id = c.artifact_id
		where octet_length(coalesce(anchor.ref_key, '')) > ` + addressBound + `
		   or octet_length(coalesce(owner.ref_key, '')) > ` + addressBound + `
		   or (c.author->>'kind' = '` + graphstore.KindSession + `'
		       and octet_length(coalesce(c.author->>'id', '')) > ` + addressBound + `)`,
}}

// actorProjections are the census's kinds of the people, the sessions, the roles and the roles'
// pages, which no table projects: each row side is the actors the projected tables and the event
// log name.
var actorProjections = []projection{{
	name:     graphstore.KindPerson,
	nodeKind: graphstore.KindPerson,
	table:    "comments.author, asks.author, asks.answer, block_refs.decided_by, events.payload",
	note: "no table projects a person, so the row side is the people the projected tables name as authors," +
		" the owner of each session the server proved that wrote one, the person each ask's answer names and" +
		" each answer the store took ever named, and who wrote each decision and reading and their proved owners",
	rows: `
		select 'person ' || p.login, '` + graphstore.KindPerson + `', p.login, ''
		from (` + commentPeople + `) p`,
	nodes: `
		select n.id, '' from graph_nodes n where n.kind = '` + graphstore.KindPerson + `'`,
}, {
	name:     graphstore.KindSession,
	nodeKind: graphstore.KindSession,
	table:    "comments.author, comment_deliveries, message_deliveries, comment_fallback_copies, asks.author, ask_followers, block_refs.decided_by, events.payload",
	note: "no table projects a session, so the row side is the sessions those tables name as authors, as reached," +
		" as askers, as followers and as who wrote a decision or a reading, and every session an ask.opened" +
		" event named as the asker or an ask.follower_added event named",
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
}}

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
func lensBlock(alias string) string {
	return `(` + alias + `.id like '` + lensBlockPrefix + `%')`
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

// commentAuthor is the far end of the authored_by tuple a comment row states, written kind:id:
// the node graphstore.AuthorOf reads off the actor the row stored, a login canonicalised as
// commentPeople canonicalises one, and null for an actor that names nobody, whose comment states
// no authored_by at all.
const commentAuthor = `case
			when c.author->>'kind' = 'user' and coalesce(c.author->>'id', '') <> ''
				then '` + graphstore.KindPerson + `:' || user_threads_login(c.author->>'id')
			when c.author->>'kind' = 'session' and coalesce(c.author->>'id', '') <> ''
				then '` + graphstore.KindSession + `:' || (c.author->>'id')
			when coalesce(c.author->>'owner', '') <> ''
				then '` + graphstore.KindPerson + `:' || user_threads_login(c.author->>'owner')
		end`

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

// commentPeople are the people the projected tables name as the author of a row, as
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
// An ask whose row names its node names people too (dispatch://INFRA-1150): its author, read as a
// comment's is (graphstore.AskFacts states its authored_by), and the person whose answer it holds,
// whose node its answered_by and decided_by point at (asks.Settle). A node is history and never
// goes, as a session that once followed an ask still owes its node (recordedSessions), so a person
// whose answer a reopen took off the row is still the person that answer named: every person an
// ask's answered_by or decided_by ever named, asserted or retracted since, is read here off the
// store's own log, which holds that answer still. Only an answer the store took names anybody so,
// and an answer a build before the store wrote and took off again names nobody, since the
// backfill states a row's answer as it stands (graphcensus' asks pass). An erase of the ask takes
// those tuples with it, and the person then leaves by an erase of their own, as the author of an
// erased comment does.
//
// Who wrote each decision, reading and ask the store holds an authored_by or decided_by of names
// people too (storedAuthors), and so does the owner the server proved of each such writer that is a
// session (storedOwners).
var commentPeople = `
	select distinct user_threads_login(author.login) as login
	from (
		select case
			when c.author->>'kind' = 'user' and coalesce(c.author->>'id', '') <> '' then c.author->>'id'
			when c.author->>'kind' = 'session' and coalesce(c.author->>'id', '') <> '' then null
			when coalesce(c.author->>'owner', '') <> '' then c.author->>'owner'
		end as login
		from comments c` + commentStoreJoins + `
		where ` + commentInTheStore + `
		union all
		select c.author->>'owner'
		from comments c` + commentStoreJoins + `
		where c.author->>'kind' = 'session' and coalesce(c.author->>'id', '') <> ''
		  and c.author->'act'->>'session' = c.author->>'id'
		  and octet_length(c.author->>'id') <= ` + addressBound + `
		  and ` + commentInTheStore + `
		union all
		select case
			when k.author->>'kind' = 'user' and coalesce(k.author->>'id', '') <> '' then k.author->>'id'
			when k.author->>'kind' = 'session' and coalesce(k.author->>'id', '') <> '' then null
			when coalesce(k.author->>'owner', '') <> '' then k.author->>'owner'
		end
		from asks k where k.node_kind is not null
		union all
		select k.author->>'owner'
		from asks k
		where k.node_kind is not null and k.author->>'kind' = 'session' and coalesce(k.author->>'id', '') <> ''
		  and k.author->'act'->>'session' = k.author->>'id'
		  and octet_length(k.author->>'id') <= ` + addressBound + `
		union all
		select k.answer->>'user' from asks k where k.node_kind is not null and k.answer is not null
		union all
		select answered.id from graph_nodes answered
		where answered.kind = '` + graphstore.KindPerson + `' and exists (
			select 1 from graph_tuples t
			where t.to_kind = answered.kind and t.to_id = answered.id
			  and t.kind in ('` + graphstore.TupleAnsweredBy + `', '` + graphstore.TupleDecidedBy + `')
			  and t.from_kind = '` + graphstore.KindAsk + `')
		union all
		select stored.id from (` + storedAuthors + `) stored where stored.kind = '` + graphstore.KindPerson + `'
		union all
		select owner.login from (` + storedOwners + `) owner
	) author
	where author.login is not null and user_threads_login(author.login) <> ''`

// storedAuthors are who wrote the decisions and the readings of every document, and every ask the
// store names an author of, as (kind, id, at): each a person or a session, whose node the writer
// writes beside the tie that names it (refs.IndexDecisionNodes and graphstore.AskFacts, through
// graphstore.AuthorFacts). A decision's author is the far end of its decided_by, which its
// block_refs row projects, and that row is what is read for it. A reading's author is the far end
// of its authored_by, which no table projects (the tuple table of dispatch://INFRA-1150), so the
// row read for it is the tuple itself, the authored_by each live reading's node states now, as
// graph_edges' tuple arm would answer it. Either names a node the store must hold, so a writer
// that stated the tie and not the node is a difference here.
//
// A node is history and never goes: a decision or a reading deleted or retyped since takes its
// row and its current tie away and leaves its author's node standing, so every person and session
// a block's decided_by or authored_by ever named, asserted or retracted since, is read too, off
// the store's log through graph_tuples_inbound, as an ask's answerer is (commentPeople). So is
// every one an ask's authored_by ever named: an ask one of whose documents is past the store's
// bound keeps a row that names no node (graphstore.AskPlaces.Fit), which the ask author arms of
// commentPeople leave out, while its writer states the ask's node, its authored_by and its
// author's node all the same. An ask a build before the store wrote states no authored_by until
// the backfill marks its row, and one an erase took away states none any more, so neither names
// an author here.
var storedAuthors = `
	select r.to_kind as kind, r.to_id as id, r.created_at as at
	from block_refs r
	where r.kind = '` + graphstore.TupleDecidedBy + `'
	  and r.to_kind in ('` + graphstore.KindPerson + `', '` + graphstore.KindSession + `')
	union all
	select written.to_kind, written.to_id, written.at
	from graph_nodes reading
	cross join lateral (
		` + graphstore.CurrentTupleRead(`t.to_kind, t.to_id`, `t.to_kind, t.to_id, t.at, t.asserted`,
	`t.from_kind = '`+refs.BlockNodeKind+`' and t.from_id = reading.id and t.kind = '`+graphstore.TupleAuthoredBy+`'
		  and t.to_kind in ('`+graphstore.KindPerson+`', '`+graphstore.KindSession+`')`) + `
	) written
	where reading.kind = '` + refs.BlockNodeKind + `' and reading.supertag = '` + refs.ReadingSupertag + `'
	  and written.asserted
	union all
	select writer.kind, writer.id, writer.made_at
	from graph_nodes writer
	where writer.kind in ('` + graphstore.KindPerson + `', '` + graphstore.KindSession + `') and exists (
		select 1 from graph_tuples t
		where t.to_kind = writer.kind and t.to_id = writer.id
		  and t.kind in ('` + graphstore.TupleDecidedBy + `', '` + graphstore.TupleAuthoredBy + `')
		  and (t.from_kind = '` + refs.BlockNodeKind + `'
		       or t.from_kind = '` + graphstore.KindAsk + `' and t.kind = '` + graphstore.TupleAuthoredBy + `'))`

// storedOwners are the people the server proved own a session that wrote a decision, a reading or
// an ask (storedAuthors): the write that names such a session ties it to its owner with an
// alias_of tuple and makes the owner's node with it (graphstore.AuthorFacts). Neither block_refs
// nor any other table records the actor a decision's author was proved by, so the owner is read as
// the store holds that tie now, the far end of each alias_of the session states.
var storedOwners = `
	select owner.to_id as login
	from (select distinct stored.id from (` + storedAuthors + `) stored where stored.kind = '` + graphstore.KindSession + `') writer
	cross join lateral (
		` + graphstore.CurrentTupleRead(`t.to_kind, t.to_id`, `t.to_id, t.asserted`,
	`t.from_kind = '`+graphstore.KindSession+`' and t.from_id = writer.id and t.kind = '`+graphstore.TupleAliasOf+`'
		  and t.to_kind = '`+graphstore.KindPerson+`'`) + `
	) owner
	where owner.asserted`

// recordedSessions are the sessions a row of the store names, each once, with the first event
// that named it (null for a session only a row names) and the earliest time a row or an event
// names it. They are every write R14 records a session from: a comment a session wrote
// (graphstore.AuthorOf); every session a delivery reached - a comment mention's and a targeted
// message's attempt alike, whether the outbox's record states it (outbox.deliveredGraph) or the
// attempt's own settle does (graphstore.Reached), and a fallback copy the outbox published to a
// session's own topic (outbox.fallbackDelivered); an ask a session opened, every session that
// follows an ask now, and every session an ask.follower_added event ever named (asks.Follow), so a
// follower added and removed before the store existed is held to a node as well. A node is
// history and never goes, so a session that once followed still owes it. The same holds of a
// session that asked: every session an ask.opened event named as the ask's author is read too, so
// a session whose ask an erase took away, asks row, follower rows and node with it, still owes its
// node, which no erase removes (grapherase refuses a session) and the log keeps addressing; the
// erase redacts no ask event. And every session that wrote a decision, a reading or an ask the
// store names an author of (storedAuthors).
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
// commentPeople binds the same: a comment left out of the store records no author at all.
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
		select case when e.type = 'ask.follower_added' then e.payload->>'session_id'
		            when e.payload->'author'->>'kind' = 'session' then e.payload->'author'->>'id' end,
		       e.id, e.created_at
		from events e
		where e.type in ('ask.follower_added', 'ask.opened')
		union all
		select stored.id, null, stored.at from (` + storedAuthors + `) stored where stored.kind = '` + graphstore.KindSession + `'
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
		if report.StoreOnly, err = readStoreOnly(ctx, tx); err != nil {
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

// movedTo is the tuple kind a block address that no longer stands states to the block it moved
// to (dispatch://INFRA-1150 R8): the restore and the conversion write it, and the resolvers follow
// it. No table projects it, so the census counts it and resolves its far ends (readStoreOnly).
const movedTo = "moved_to"

// movedToHops is how many moved_to tuples a far end is followed through before it is read as not
// resolving, as the resolvers follow a chain (dispatch://INFRA-1150 R8: at most 8 hops).
const movedToHops = 8

// storeOnlyRead counts, in one read of graph_tuples, every relation of the two kinds no table
// projects that the store states now - the newest tuple of each (near end, far end), kept while
// it asserts - and those of them whose far end does not resolve: a copied_from (a copy's block to
// the ask it was copied from, graphstore.TupleCopiedFrom) resolves to a live ask node, and a
// moved_to to a block that stands, which is one a document_blocks row holds, reached at once or
// through at most movedToHops more moved_to tuples, a cycle reaching none. No index keys on a
// tuple's kind, so the read walks the table once for both kinds; it reads nothing of any other
// kind, so a store that holds none of them reads two zero counts.
var storeOnlyRead = `
	with recursive stated as (
		select held.from_kind, held.from_id, held.kind, held.to_kind, held.to_id from (
			` + graphstore.CurrentTupleRead(`t.from_kind, t.from_id, t.kind, t.to_kind, t.to_id`,
	`t.from_kind, t.from_id, t.kind, t.to_kind, t.to_id, t.asserted`,
	`t.kind in ('`+graphstore.TupleCopiedFrom+`', '`+movedTo+`')`) + `
		) held where held.asserted
	),
	followed (start, kind, address, hops, path) as (
		select far.to_id, far.to_kind, far.to_id, 0, array[far.to_id]
		from (select distinct s.to_kind, s.to_id from stated s where s.kind = '` + movedTo + `') far
		union all
		select f.start, m.to_kind, m.to_id, f.hops + 1, f.path || m.to_id
		from followed f
		join stated m on m.kind = '` + movedTo + `' and m.from_kind = f.kind and m.from_id = f.address
		where f.kind = '` + refs.BlockNodeKind + `' and f.hops < ` + strconv.Itoa(movedToHops) + ` and m.to_id <> all(f.path)
		  and not exists (select 1 from document_blocks d where d.node_id = f.address)
	),
	standing as (
		select distinct f.start from followed f
		where f.kind = '` + refs.BlockNodeKind + `' and exists (select 1 from document_blocks d where d.node_id = f.address)
	),
	resolved as (
		select s.kind, case s.kind
			when '` + graphstore.TupleCopiedFrom + `' then s.to_kind = '` + graphstore.KindAsk + `' and exists (
				select 1 from graph_nodes ask where ask.kind = '` + graphstore.KindAsk + `' and ask.id = s.to_id
				  and ask.state = '` + graphstore.StateLive + `')
			else s.to_kind = '` + refs.BlockNodeKind + `' and exists (select 1 from standing where standing.start = s.to_id)
		end as resolves
		from stated s
	)
	select listed.kind, count(r.kind), count(r.kind) filter (where not r.resolves)
	from (values (1, '` + graphstore.TupleCopiedFrom + `'), (2, '` + movedTo + `')) listed (position, kind)
	left join resolved r on r.kind = listed.kind
	group by listed.position, listed.kind
	order by listed.position`

// readStoreOnly reads the kinds no table projects in the census's snapshot (storeOnlyRead), one
// report for each whether or not the store holds any of it, copied_from first.
func readStoreOnly(ctx context.Context, tx pgx.Tx) ([]StoreOnlyReport, error) {
	rows, err := tx.Query(ctx, storeOnlyRead)
	if err != nil {
		return nil, fmt.Errorf("read the kinds no table projects: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (StoreOnlyReport, error) {
		var kind StoreOnlyReport
		err := row.Scan(&kind.Kind, &kind.Tuples, &kind.Unresolved)
		return kind, err
	})
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
// a census of seven kinds costs seven reads here and not twenty-eight. Each subquery is the
// statement it was on its own, so each is planned and streamed exactly as before.
func censusOfKind(ctx context.Context, tx pgx.Tx, held projection, args []any) (KindReport, error) {
	kind := KindReport{Kind: held.name, NodeKind: held.nodeKind, Table: held.table, Note: held.note,
		CountsUnmarked: held.unmarked != "", EmptyAgrees: held.emptyAgrees}
	// pastBound and unmarked are each a scalar `select count(*)`, so each is read directly rather
	// than wrapped in a count of its own row; a kind without the class reads zero.
	pastBound, unmarked := held.pastBound, held.unmarked
	if pastBound == "" {
		pastBound = "select 0"
	}
	if unmarked == "" {
		unmarked = "select 0"
	}
	if err := tx.QueryRow(ctx, `select
		(select count(*) from (`+held.rows+`) rows (row_label, node_kind, node_address, digest)),
		(select count(*) from (`+presentNodes(held)+`) nodes (kind, id, digest, state)),
		(select count(*) from (`+rowDifferences(held)+`) rows_found),
		(select count(*) from (`+nodeDifferences(held)+`) nodes_found),
		(`+pastBound+`),
		(`+unmarked+`)`, args...,
	).Scan(&kind.Rows, &kind.Nodes, &kind.RowDifferences, &kind.NodeDifferences, &kind.PastBound, &kind.Unmarked); err != nil {
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

// presentNodes is the kind's node side bound to the nodes the store holds present, as (kind, id,
// digest, state), which is every node not at state gone (R12 of dispatch://INFRA-1950): a node at
// proposed, settled or overtaken is a node a row projects as one at live is, so a move between
// those states is no lost node. It answers each node's state beside its digest, which the report
// names a node with. A node the store holds gone is no node a row may project, and the row side
// holds it to that separately. A kind whose nodes are of one kind answers (id, digest), and is
// given that kind here.
func presentNodes(held projection) string {
	nodes := held.nodes
	if !held.kinded {
		nodes = `select '` + held.nodeKind + `', single.id, single.digest from (` + held.nodes + `) single (id, digest)`
	}
	return `select node.kind, node.id, node.digest, present.state from (` + nodes + `) node (kind, id, digest)
		join graph_nodes present on present.kind = node.kind and present.id = node.id
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
		left join (` + presentNodes(held) + `) present (kind, id, digest, state)
			on present.kind = row.node_kind and present.id = row.node_address
		where row.node_kind is null or row.node_address is null or node.kind is null
		   or node.state = '` + graphstore.StateGone + `'
		   or row.digest is distinct from present.digest
		order by 2, 1`
}

// nodeDifferences reads the store against the row side: every node of the kind the store holds
// present that no row projects, named with the state it is at, so a proposed node with no row
// reads as a proposed one and a live one as `a live node no row projects`, but for a node whose
// row is owed by a writer that has not run yet (projection.owed).
func nodeDifferences(held projection) string {
	owed := ""
	if held.owed != "" {
		owed = `
		  and not ` + held.owed
	}
	return `
		select case when node.state ~ '^[aeiou]' then 'an ' else 'a ' end || node.state
		       || ' node no row projects',
		       node.kind || ' ' || node.id
		from (` + presentNodes(held) + `) node (kind, id, digest, state)
		where not exists (
			select 1 from (` + held.rows + `) row (row_label, node_kind, node_address, digest)
			where row.node_kind = node.kind and row.node_address = node.id)` + owed + `
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
// the store-only kinds and the sizing after them, ending with the verdict. It prints counts,
// digests, kinds and the addresses of rows and nodes; no body, no actor and no value of an
// attribute leaves the database.
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
		if kind.CountsUnmarked {
			fmt.Fprintf(w, "census: store kind=%s unmarked_rows=%s (rows a writer of the release before wrote, which name no node yet and graph_edges' old arms answer)\n",
				kind.Kind, pgmigrate.Thousands(kind.Unmarked))
		}
		switch {
		case kind.EmptyAgrees && kind.Empty():
			fmt.Fprintf(w, "census: store kind=%s holds no row and no node: nothing of it is written yet, which is no refusal\n", kind.Kind)
		case kind.Rows == 0:
			fmt.Fprintf(w, "census: store kind=%s checked no row, so the census proves nothing of it\n", kind.Kind)
		}
		writeDifferences(w, kind.Kind, kind.RowDifferences, kind.ReportedRows)
		writeDifferences(w, kind.Kind, kind.NodeDifferences, kind.ReportedNodes)
	}
	for _, kind := range r.StoreOnly {
		fmt.Fprintf(w, "census: store-only kind=%s tuples=%s unresolved=%s\n",
			kind.Kind, pgmigrate.Thousands(kind.Tuples), pgmigrate.Thousands(kind.Unresolved))
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
	case r.Unresolved() > 0:
		fmt.Fprintf(w, "census: store REFUSED (%s far end(s) of a kind no table projects do not resolve)\n",
			pgmigrate.Thousands(r.Unresolved()))
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
