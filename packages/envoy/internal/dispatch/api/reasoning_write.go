package api

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// reasoningNode is one node of the kinds dispatch://INFRA-1950 adds that a request writes under
// an issue: a lens (graphstore.KindLens), a finding of a lens - a node of kind and supertag block
// whose id names its lens, `lens/<uuid>#<block id>` (graphstore.KindLensBlock) - a risk
// (graphstore.KindRisk) or a constraint (graphstore.KindConstraint). A run's interaction is no
// node of this write: the going POST states it (api/going.go).
//
// visibility is what the request asked for, the empty string being project. parentKind and
// parentID are the node it stands under, which its part_of tuple names: a lens's node, a
// finding's or a risk's lens, a constraint's issue. attributes are what its row carries, and
// tuples every other fact the request states with it: a relation out of it or into it - a lens's
// has_kind and triggered_by, a risk's prevented_by, a requirement's constrained_by to a constraint
// - or a value of it (ToKind graphstore.KindValue). A tuple is named by its kind and its two ends
// alone: the write dates it, signs it as the node's writer and asserts it.
type reasoningNode struct {
	kind, id             string
	visibility           string
	parentKind, parentID string
	attributes           map[string]any
	tuples               []graphstore.Tuple
}

// reasoningWrite is one request's write of reasoning nodes, all under the issue issue names.
// proving is the credential that proved the caller, optionalActor's actor, and recorded the actor
// the write records, actorFrom's: the session the body names on a bearer that proves none, as a
// comment's author is. A caller that resolves no request - a command - passes the actor it acts
// as for both.
type reasoningWrite struct {
	issue    string
	proving  model.Actor
	recorded model.Actor
	at       time.Time
	nodes    []reasoningNode
}

// writeReasoningNodes is the one write path of the nodes and the tuples dispatch://INFRA-1950
// adds, in the caller's transaction: every route that writes a lens, a finding, a risk or a
// constraint, and the pre-mortem conversion, write through here. For each node it writes the
// node, its part_of tuple to its parent, the tuples the request states with it, and, where the
// proving credential proves an author (graphstore.ProvesAuthor), its authored_by tuple to the
// author graphstore.AuthorOf names of its made_by, with that author's own person or session node
// and its alias and recorded_as tuples (graphstore.AuthorFacts), as the comment path states who
// wrote a comment. A credential that proves no author - the deployment's shared bearer - states
// no author and no author node at all, though the node's made_by names the session its body
// claims, so a claimed session never becomes a node of the store.
//
// Each node's visibility is graphstore.VisibilityFor of what it asked for and the proving
// credential: `author` on a credential that proves no author is graphstore.ErrAuthorNamesNobody,
// 403 AUTHOR_NAMES_NOBODY, refused before the first statement, so nothing of the write is stored.
// An author-only node is made by the proving credential, so the credential its author reads it
// with matches its made_by; a project node is made by the recorded actor. Every node is live, in
// the project of the issue (the project graphstore.IssueNode states of the issue's own node), and
// between no parties.
//
// It takes the issue's row `for no key update` first, the owner row the lock order on
// api/inbox_read.go's markInboxReadThreads puts before every other, and reads the project off it;
// an issue that does not exist is pgx.ErrNoRows. Then it writes every node, the authors' among
// them, and every tuple in one graphstore.Write: the nodes in ascending (kind, id), the type rules
// of the pointer tuples checked before the first tuple is appended (graphstore's pointerRules,
// whose refusals are 400 INVALID_POINTER and 409 POINTER_ALREADY_ASSERTED), and every node and
// tuple at graphstore.NoEvent. No event is appended, because no write of these kinds sends a
// delivery (R1): a reader learns of one by reading it.
//
// That each far end a tuple names exists, is not gone and may be seen by the caller is the
// route's to check before it calls this, through the read registry's loader for that end's kind,
// so that a far end the caller may not see answers as one that does not exist. An end that
// stands under `{key}` - the node a lens is invoked on, a lens a risk or a finding stands under,
// a requirement or a check of the issue's documents - is found through FindNodeUnderIssue. An end
// that stands under no issue - a has_kind tuple's lens_kind, a constraint of the catalogue a
// lens_kind reaches - is no node FindNodeUnderIssue answers but 404 for, and is found through
// refs.Node with the caller's visibility instead.
//
// It answers the nodes the request wrote, in the order it named them, without their authors':
// the nodes this write made, which are the ones a write made for a run names as changed by it
// (graphstore.TupleChangedByRun). Each is marked changed (graphstore.Node's Changed) and no
// author's node is, so graphstore.Write, on a context holding a run, appends that run's
// changed_by_run out of each of them alone.
func writeReasoningNodes(ctx context.Context, tx pgx.Tx, write reasoningWrite) ([]graphstore.Node, error) {
	written := make([]graphstore.Node, len(write.nodes))
	for index, node := range write.nodes {
		if node.parentKind == "" || node.parentID == "" {
			return nil, fmt.Errorf("the %s %s names no node to stand under", node.kind, node.id)
		}
		visibility, err := graphstore.VisibilityFor(node.visibility, write.proving)
		if err != nil {
			return nil, err
		}
		madeBy := write.recorded
		if visibility == graphstore.VisibilityAuthor {
			madeBy = write.proving.Recorded()
		}
		written[index] = graphstore.Node{
			Kind: node.kind, ID: node.id, Supertag: node.kind,
			Visibility: visibility, State: graphstore.StateLive, Attributes: node.attributes,
			MadeAt: write.at, MadeBy: madeBy, SourceEvent: graphstore.NoEvent, Changed: true,
		}
	}
	var project string
	if err := tx.QueryRow(ctx, `select project_key from issues where key = $1`+ownerRowLock, write.issue).Scan(&project); err != nil {
		return nil, err
	}

	nodes := make([]graphstore.Node, 0, len(written)+2)
	var tuples []graphstore.Tuple
	// One write writes a node once and states a fact once, so an author several nodes of the
	// request name is made by the first of them, as are the alias facts of that author; each node
	// still states its own authored_by.
	authors := map[[2]string]struct{}{}
	aliases := map[[5]string]struct{}{}
	proves := graphstore.ProvesAuthor(write.proving)
	for index := range written {
		node := &written[index]
		node.Project = project
		nodes = append(nodes, *node)
		tuples = append(tuples, graphstore.Tuple{
			FromKind: node.Kind, FromID: node.ID, Kind: graphstore.TuplePartOf,
			ToKind: write.nodes[index].parentKind, ToID: write.nodes[index].parentID,
			At: write.at, Who: node.MadeBy, SourceEvent: graphstore.NoEvent, Asserted: true,
		})
		for _, stated := range write.nodes[index].tuples {
			stated.Seq, stated.LegacySourceSeq = 0, nil
			stated.At, stated.Who, stated.SourceEvent, stated.Asserted = write.at, node.MadeBy, graphstore.NoEvent, true
			tuples = append(tuples, stated)
		}
		if !proves {
			continue
		}
		authorNodes, facts, named := graphstore.AuthorFacts(node.Kind, node.ID, node.MadeBy, write.at, graphstore.NoEvent)
		if !named {
			continue
		}
		for _, author := range authorNodes {
			if _, made := authors[[2]string{author.Kind, author.ID}]; !made {
				authors[[2]string{author.Kind, author.ID}] = struct{}{}
				// The author's node is the person's or the session's, whose state and visibility
				// no node of theirs owns.
				author.Restates = true
				nodes = append(nodes, author)
			}
		}
		// The first fact is this node's authored_by; the rest are the author's aliases.
		tuples = append(tuples, facts[0])
		for _, alias := range facts[1:] {
			key := [5]string{alias.FromKind, alias.FromID, alias.Kind, alias.ToKind, alias.ToID}
			if _, stated := aliases[key]; !stated {
				aliases[key] = struct{}{}
				tuples = append(tuples, alias)
			}
		}
	}
	if err := graphstore.Write(ctx, tx, nodes, tuples); err != nil {
		return nil, err
	}
	return written, nil
}

// The three things a request may do to one pointer tuple out of a node (writeReasoningPointer).
const (
	pointerAppend   = "append"
	pointerRetract  = "retract"
	pointerRetarget = "retarget"
)

// reasoningPointer is one request's write of one pointer tuple out of a node that stands under
// the issue issue names: a lens's has_kind or triggered_by, a requirement's constrained_by, a
// risk's prevented_by. action is pointerAppend, pointerRetract or pointerRetarget. pointer is the
// tuple the write names, dated and signed by the write and at graphstore.NoEvent: the one it
// appends, the one it retracts, or, for a retarget, the one the node holds now, which moves to
// toKind and toID.
type reasoningPointer struct {
	issue        string
	action       string
	pointer      graphstore.Tuple
	toKind, toID string
}

// writeReasoningPointer is the one write path of a pointer tuple a request appends, retracts or
// retargets out of a node of dispatch://INFRA-1950's reasoning kinds, or out of a requirement
// block, in the caller's transaction. It takes the issue's row `for no key update` first, the
// owner row the lock order on api/inbox_read.go's markInboxReadThreads puts before every other,
// and then writes through the store, which holds the tuple to its type rule (graphstore's
// pointerRules) and takes the node rows that rule names after the owner row: an append and a
// retraction through graphstore.Write, a retarget through graphstore.Retarget. No event is
// appended, as none is for the nodes these tuples tie (writeReasoningNodes). The pointer is marked
// as changing the node it goes out of (graphstore.Tuple's Changes), so a write made for a run names
// that node, and not its far end, as changed by it.
//
// A retraction of a tuple the node does not hold now is a 404, as a retarget of one is
// (graphstore.ErrPointerNotHeld): there is nothing to retract. That both ends exist, are not
// gone and may be seen by the caller is the route's to check before it calls this.
func writeReasoningPointer(ctx context.Context, tx pgx.Tx, write reasoningPointer) error {
	var held bool
	if err := tx.QueryRow(ctx, `select true from issues where key = $1`+ownerRowLock, write.issue).Scan(&held); err != nil {
		return err
	}
	write.pointer.Changes = true
	switch write.action {
	case pointerRetarget:
		return graphstore.Retarget(ctx, tx, write.pointer, write.toKind, write.toID)
	case pointerRetract:
		current, err := graphstore.CurrentOut(ctx, tx, write.pointer.FromKind, write.pointer.FromID, []string{write.pointer.Kind})
		if err != nil {
			return err
		}
		if !slices.ContainsFunc(current, func(tuple graphstore.Tuple) bool {
			return tuple.ToKind == write.pointer.ToKind && tuple.ToID == write.pointer.ToID
		}) {
			return errorf(http.StatusNotFound, "NOT_FOUND", "the node holds no %s tuple at that node to retract", write.pointer.Kind)
		}
		retraction := write.pointer
		retraction.Asserted = false
		return graphstore.Write(ctx, tx, nil, []graphstore.Tuple{retraction})
	case pointerAppend:
		return graphstore.Write(ctx, tx, nil, []graphstore.Tuple{write.pointer})
	}
	return fmt.Errorf("no pointer write %q", write.action)
}

// nodeStateWrite is one request's write of one node's state: the node the store keys by kind and
// id, under the issue issue names, moved to state, signed by who and dated at. allowed is the
// route's rule over the node as the store holds it, read once the issue's row is held; held is
// false where the store holds no row of the node, which every read answers as live and project,
// and stored carries those then. A rule that answers a refusal stores nothing.
type nodeStateWrite struct {
	issue    string
	kind, id string
	state    string
	who      model.Actor
	at       time.Time
	allowed  func(stored graphstore.Node, held bool) error
}

// writeNodeState is the one write path of a node's state a request asks for - the state route and
// the settle (node_state.go) - in the caller's transaction. It takes the issue's row `for no key
// update` first, the owner row the lock order on api/inbox_read.go's markInboxReadThreads puts
// before every other, as writeReasoningPointer does, so two writes of one node's state queue there
// and the second reads what the first wrote. It reads the node's row, holds it to the route's rule,
// and writes the node again as the store holds it but for its state: a plain node write, not one
// that restates the node (graphstore.Node.Restates), dated and signed by the request and at
// graphstore.NoEvent, as every write of these routes is. So graphstore.Write moves the row's state
// and appends the one state tuple that says so, whose writer is who: the row and the log agree,
// and every read of the node answers the new state at once. made_at and made_by stay the row's
// own, the store's rule for a node written again, so the node's author is still the writer of its
// first write. A write of the state the node holds moves nothing and appends nothing. A write that
// moves it marks the node changed (graphstore.Node's Changed), so one made for a run names the node
// as changed by that run.
//
// It answers the node as the write leaves it and the state it held before. A node the store holds
// no row of is the rule's to refuse: nothing here writes a node that is not there.
func writeNodeState(ctx context.Context, tx pgx.Tx, write nodeStateWrite) (graphstore.Node, string, error) {
	var issueHeld bool
	if err := tx.QueryRow(ctx, `select true from issues where key = $1`+ownerRowLock, write.issue).Scan(&issueHeld); err != nil {
		return graphstore.Node{}, "", err
	}
	node, held, err := graphstore.ReadNode(ctx, tx, write.kind, write.id)
	if err != nil {
		return graphstore.Node{}, "", err
	}
	if !held {
		node = graphstore.Node{Kind: write.kind, ID: write.id, State: graphstore.StateLive, Visibility: graphstore.VisibilityProject}
	}
	if err := write.allowed(node, held); err != nil {
		return graphstore.Node{}, "", err
	}
	if !held {
		return graphstore.Node{}, "", fmt.Errorf("the rule over %s %s let a node the store holds no row of be written", write.kind, write.id)
	}
	previous := node.State
	if previous == write.state {
		return node, previous, nil
	}
	node.State, node.Restates, node.Changed = write.state, false, true
	node.MadeAt, node.MadeBy, node.SourceEvent = write.at, write.who, graphstore.NoEvent
	if err := graphstore.Write(ctx, tx, []graphstore.Node{node}, nil); err != nil {
		return graphstore.Node{}, "", err
	}
	return node, previous, nil
}
