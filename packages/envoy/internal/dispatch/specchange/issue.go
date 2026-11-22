package specchange

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/docs"
	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
)

// ErrNoRecord is a version of a document that holds no record of the store revision it was read
// at (version_renders): one written before its issue's documents were rendered from the tree, so
// no block of it can be read as it stood at that version.
var ErrNoRecord = errors.New("the version holds no record of the store revision it was read at")

// VersionCut is the cut a version of a document was read at: the read_seq and the time of its
// record (version_renders), which an approval of that version covers the digest of. A version with
// no record is ErrNoRecord.
func VersionCut(ctx context.Context, q refs.Queryer, artifactID string, version int) (refs.TreeCut, error) {
	var cut refs.TreeCut
	err := q.QueryRow(ctx, `select read_seq, at from version_renders where artifact_id = $1 and number = $2`,
		artifactID, version).Scan(&cut.ReadSeq, &cut.At)
	if errors.Is(err, pgx.ErrNoRows) {
		return refs.TreeCut{}, fmt.Errorf("version %d of the document %s: %w", version, artifactID, ErrNoRecord)
	}
	if err != nil {
		return refs.TreeCut{}, fmt.Errorf("read the record of version %d of the document %s: %w", version, artifactID, err)
	}
	return cut, nil
}

// FromIssue reads the change request of the issue issueKey from its tree, through the walk and the
// facts reader the approval digest is built from (docs.WalkIssueTree, refs.TreeFactsOf): the
// statements and components among the nodes the spec query takes at statements, the cut of the
// spec version an approval names, and the checks among the nodes the Verification query takes at
// checks, the cut of the Verification version it names. So a request holds exactly the nodes the
// digest its approval covers holds, read at the revision that digest was read at.
func FromIssue(ctx context.Context, q refs.Queryer, issueKey string, statements, checks refs.TreeCut) (Request, error) {
	var blocks []Block
	for _, read := range []struct {
		cut   refs.TreeCut
		query string
		take  func(typ string) bool
	}{
		{statements, graphstore.QuerySpec, func(typ string) bool { return IsSpecBlock(typ) && typ != KindCheck }},
		{checks, graphstore.QueryVerification, func(typ string) bool { return typ == KindCheck }},
	} {
		tree, err := docs.WalkIssueTree(ctx, q, issueKey, read.cut)
		if err != nil {
			return Request{}, fmt.Errorf("read the change request of %s: %w", issueKey, err)
		}
		taken, _ := tree.Takes(read.query)
		var targets []refs.Target
		for _, index := range taken {
			node := tree.Nodes[index]
			if node.Kind == refs.BlockNodeKind && read.take(node.Type) {
				targets = append(targets, refs.Target{Kind: node.Kind, ID: node.ID})
			}
		}
		facts, err := refs.TreeFactsOf(ctx, q, targets, tree.Cut)
		if err != nil {
			return Request{}, fmt.Errorf("read the change request of %s: %w", issueKey, err)
		}
		for _, target := range targets {
			blocks = append(blocks, Block{Address: target.ID, Text: facts[target].Values[graphstore.AttributeText]})
		}
	}
	return FromBlocks(issueKey, blocks), nil
}
