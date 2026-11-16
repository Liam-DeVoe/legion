package api

import (
	"context"
	"fmt"
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
// route's to check before it calls this, through the read registry's loader for that end's kind
// (FindNodeUnderIssue), so that a far end the caller may not see answers as one that does not
// exist.
//
// It answers the nodes the request wrote, in the order it named them, without their authors':
// the nodes this write made, which are the ones a write made for a run names as changed by it
// (graphstore.TupleChangedByRun).
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
			MadeAt: write.at, MadeBy: madeBy, SourceEvent: graphstore.NoEvent,
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
