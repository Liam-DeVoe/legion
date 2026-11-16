package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
	"github.com/sjawhar/envoy/internal/dispatch/text"
)

// personNodeTitleRunes is how much of a row's text the page shows: its first line, cut here so
// the answer carries a title and not a whole comment.
const personNodeTitleRunes = 240

// personNode is one thing on a person's page: a node they are tied to, which of the two ties it
// is, and where a reader opens it.
type personNode struct {
	// Kind is the node's kind, as the graph addresses it: the kind the store keys it by, but
	// lens_block for a finding of a lens, which the store keys as a block (graphstore.AddressKind).
	Kind string `json:"kind"`
	// ID is the node's id, as the store keys it.
	ID string `json:"id"`
	// Relations are the tuple kinds that put this node on the page, sorted: authored_by,
	// delivered_to, or both where the person wrote a thing that was delivered back to a
	// session of theirs.
	Relations []string `json:"relations"`
	// Title is the first line of the node's text, cut to personNodeTitleRunes.
	Title string `json:"title"`
	// CreatedAt is when the node was written, which is the order the page reads in.
	CreatedAt time.Time `json:"created_at"`
	// Href is the dashboard path the row opens, as askItemRefExpression answers one for an ask:
	// a comment's own place in its issue or its document, and for a lens, a finding, a risk or a
	// constraint, which the dashboard opens no page of, the page of the issue it stands under.
	Href string `json:"href"`
	// State is the node state the store holds for the node (graphstore.NodeStates), live for a
	// node it holds no row of, as every read of a node answers it. A node that is gone is on no
	// page. Every row of this build carries it, and a server of the release before answers the
	// page without it during a roll, so the document marks it optional (`required:"false"`) and a
	// reader takes a row that carries none as live.
	State string `json:"state" enum:"live,gone,proposed,settled,overtaken" required:"false"`
	// Visibility is the visibility the store holds for the node, project for a node it holds no
	// row of: a node reserved for its author is on its author's page alone, which reads it as
	// author. It is optional in the document as State is, and a row that carries none is
	// project.
	Visibility string `json:"visibility" enum:"project,author" required:"false"`
}

// personNodes is one page of a person's nodes, newest first.
type personNodes struct {
	Person string       `json:"person"`
	Rows   []personNode `json:"rows"`
	Total  int          `json:"total"`
	Limit  int          `json:"limit"`
	Offset int          `json:"offset"`
}

// personNodesHeld is what both statements of the page read first: the nodes one person holds
// that the reader may see. $1 is the canonical login and $2 the acting credential the visibility
// predicate is bound at (refs.VisibilityArg).
//
// Both arms read the tuples whose far end is the person's node or a session of theirs, which is
// the inbound read graph_tuples_inbound serves, and each keeps the newest tuple per far end
// while that newest one asserts - the current-tuple rule graphstore.CurrentIn and
// graphstore.CurrentOut read a node by, spliced here as graphstore.CurrentTupleRead.
//
//   - authored: the authored_by tuples into the person. A thing a session wrote stays that
//     session's, so it is on no person's page: a session is nobody's until a credential proves
//     it, and proving it says who received a delivery, not who wrote a comment.
//   - received: the delivered_to tuples into each session the server itself proved this
//     person's (the alias_of tuples into them, graphstore.TupleAliasOf). A delivery ends at a
//     session or a role and never at a person, so the alias is the one way a delivery reaches
//     a person at all.
//
// Each tied node is then held to the visibility predicate of its own kind, whatever kind that
// is (refs.VisibleNode): a node reserved for its author counts for its author alone, a node of a
// kind the registry does not hold for nobody, and a node that is gone for nobody either. Beside
// each node rides the state and the visibility the store holds for it (refs.StoredNodeOf), which
// its row answers.
var personNodesHeld = `
	with authored as (
		select current.from_kind, current.from_id, '` + graphstore.TupleAuthoredBy + `'::text as relation
		from (
			` + graphstore.CurrentTupleRead(`t.from_kind, t.from_id`, `t.from_kind, t.from_id, t.asserted`,
	`t.to_kind = '`+graphstore.KindPerson+`' and t.to_id = $1
			  and t.kind = '`+graphstore.TupleAuthoredBy+`'`) + `
		) current
		where current.asserted
	), aliases as (
		select current.session
		from (
			` + graphstore.CurrentTupleRead(`t.from_id`, `t.from_id as session, t.asserted`,
	`t.to_kind = '`+graphstore.KindPerson+`' and t.to_id = $1
			  and t.kind = '`+graphstore.TupleAliasOf+`' and t.from_kind = '`+graphstore.KindSession+`'`) + `
		) current
		where current.asserted
	), received as (
		select current.from_kind, current.from_id, '` + graphstore.TupleDeliveredTo + `'::text as relation
		from (
			` + graphstore.CurrentTupleRead(`t.from_kind, t.from_id, t.to_id`, `t.from_kind, t.from_id, t.asserted`,
	`t.to_kind = '`+graphstore.KindSession+`' and t.to_id in (select session from aliases)
			  and t.kind = '`+graphstore.TupleDeliveredTo+`'`) + `
		) current
		where current.asserted
	), related as (
		select from_kind, from_id, array_agg(distinct relation order by relation) as relations
		from (select * from authored union all select * from received) ties
		group by from_kind, from_id
	), seen as (
		select r.from_kind, r.from_id, r.relations, ` + refs.StoredNodeOf("r.from_kind", "r.from_id") + ` as stored
		from related r
		where ` + refs.VisibleNode("r.from_kind", "r.from_id", "$2") + `
	), held as (
		select from_kind, from_id, relations, stored
		from seen
		where stored is null or stored[1] <> '` + graphstore.StateGone + `'
	)`

// personPageShownQuery is whether the page of the person $1 names answers anyone but that
// person: the store holds the person, not gone and visible to the reader, and the person holds
// at least one node the reader may see (personNodesHeld).
var personPageShownQuery = personNodesHeld + `
	select exists (
		select 1 from graph_nodes
		where kind = '` + graphstore.KindPerson + `' and id = $1 and state <> '` + graphstore.StateGone + `'
		  and ` + refs.VisibleRow("graph_nodes", "$2") + `
	) and exists (select 1 from held)`

// personNodesQuery pages the nodes one person holds that the reader may see (personNodesHeld),
// $3 and $4 being the page.
//
// The items arm resolves each held node into the row a reader opens. A node of a kind this build
// cannot open is skipped rather than refused, exactly as refs.resolveNodes skips an end a newer
// build wrote: today the page opens comments, and the lenses, findings, risks and constraints of
// dispatch://INFRA-1950, and a kind joins here when this build learns to open it, its predicate
// already bound by the arm above. A lens, a finding, a risk or a constraint is a row of the store
// alone: its title is its text (refs.StandingTextSQL), its time its row's made_at, and it opens the
// page of the issue it stands under (refs.StandingIssueSQL), so one that stands under no issue - a
// constraint of the lens catalogue - is skipped as a comment with no place is.
var personNodesQuery = personNodesHeld + `, items as (
		select h.from_kind as kind, c.id::text as id, h.relations, c.body as text, c.created_at, h.stored,
		       case
		         when i.key is not null then '/issues/' || i.key || '/comments/' || c.id::text
		         else '/projects/' || a.project_key || '/documents/' || a.slug || '?comment=' || c.id::text
		       end as href
		from held h
		join comments c on h.from_kind = 'comment' and h.from_id = c.id::text
		left join issues i on i.key = c.issue_key
		left join artifacts a on a.id = c.artifact_id
		where (i.key is not null or a.id is not null)
		union all
		select standing.kind, standing.id, standing.relations, standing.text, standing.made_at, standing.stored,
		       '/issues/' || standing.issue_key
		from (
			select ` + graphstore.AddressKindSQL("h.from_kind", "h.from_id") + ` as kind, h.from_id as id, h.relations,
			       ` + refs.StandingTextSQL("n") + ` as text, n.made_at, h.stored,
			       ` + refs.StandingIssueSQL("n.kind", "n.id") + ` as issue_key
			from held h
			join graph_nodes n on n.kind = h.from_kind and n.id = h.from_id
			where ` + graphstore.AddressKindSQL("h.from_kind", "h.from_id") + ` in ('` + strings.Join(refs.StandingKinds(), `', '`) + `')
		) standing
		where standing.issue_key is not null
	)
	select count(*) over () as total, kind, id, relations, text, created_at, href, stored
	from items
	order by created_at desc, kind, id desc
	limit $3 offset $4`

// listPersonNodes answers GET /api/v1/people/{login}/nodes: what one person wrote and what
// reached them, newest first.
//
// It is a named read of the graph and no traversal: the route takes the person and a page, and
// takes no kind, no filter and no edge to walk, so no caller chooses what the store is asked.
// The whole route belongs to people - routeScopes gives it model.ScopePerson, and gateRouteScopes
// refuses a credential the deployment configured with 403 OAUTH_SCOPE_REQUIRED before this
// handler runs - and every row it answers passes the visibility predicate of its own kind.
//
// The page answers 200 only where the person holds at least one node the reader may see, through
// each node's own kind's predicate, or where the reader is that person (personPageShownQuery).
// Any other login answers 404, with the body an address nobody holds answers, byte for byte: the
// read says nothing about whether that person exists, has signed in or wrote anything, and a
// person whose only node is reserved for their author has no page anybody else can open. The
// caller's own login is the one exception, answered 200 with what they may see, none at all
// where they hold nothing: a person who has written nothing and been reached by nothing opens
// their own page from the signed-in login, and telling them they are who they signed in as tells
// them nothing new.
func (s *server) listPersonNodes(w http.ResponseWriter, r *http.Request) {
	caller, ok := s.actingPerson(w, r)
	if !ok {
		return
	}
	limit, offset, err := parseMyAnswersPage(r)
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	login := model.CanonicalLogin(r.PathValue("login"))
	// The acting credential is marshalled once and bound by both reads: it is one value for
	// the request, and the two statements name the same reader.
	seen := refs.VisibilityArg(caller)
	var shown bool
	if err := s.deps.Store.Pool.QueryRow(r.Context(), personPageShownQuery, login, seen).Scan(&shown); err != nil {
		s.writeHandlerError(w, err)
		return
	}
	if !shown {
		if login != caller.ID {
			writeError(w, "NOT_FOUND", http.StatusNotFound, "not found")
			return
		}
		WriteJSON(w, http.StatusOK, personNodes{Person: login, Rows: []personNode{}, Limit: limit, Offset: offset})
		return
	}
	rows, err := s.deps.Store.Pool.Query(r.Context(), personNodesQuery, login, seen, limit, offset)
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	defer rows.Close()
	response := personNodes{Person: login, Rows: []personNode{}, Limit: limit, Offset: offset}
	for rows.Next() {
		var row personNode
		var body string
		var stored []string
		if err := rows.Scan(&response.Total, &row.Kind, &row.ID, &row.Relations, &body, &row.CreatedAt, &row.Href, &stored); err != nil {
			s.writeHandlerError(w, err)
			return
		}
		var read bool
		if row.State, row.Visibility, read = refs.StoredStateAndVisibility(stored); !read {
			s.writeHandlerError(w, fmt.Errorf("read the stored state and visibility of %s %s: %d values, want 2", row.Kind, row.ID, len(stored)))
			return
		}
		first, _, _ := strings.Cut(body, "\n")
		row.Title = text.HeadRunes(first, personNodeTitleRunes)
		row.CreatedAt = row.CreatedAt.UTC()
		response.Rows = append(response.Rows, row)
	}
	if err := rows.Err(); err != nil {
		s.writeHandlerError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, response)
}
