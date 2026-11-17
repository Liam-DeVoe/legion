package docs

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
)

// TextWithAnswers is the document an agent reads through the text routes (GET
// /api/v1/artifacts/{id}/text and its issue and project slug variants, and so `dispatch read` and
// `doc-read`): its canonical markdown and its whole-document token, with its blocks beside them
// when withBlocks, as TextWithToken and TextWithTokenAndBlocks answer them, but with each ask block
// carrying its ask's answer state and answer, rendered on read from the ask (askAnswersShown). A
// person's answer is held once, on the ask's node (dispatch://INFRA-1150 R3), and the stored bytes
// hold none of it, so an agent reads what was decided where it reads the question. The markdown
// and the blocks come from one rendering, so the blocks' byte ranges index the markdown answered;
// the tokens are the document's own, since no token weighs a server-owned attribute. A document
// holding no ask block is answered as those two read it.
func (s *Service) TextWithAnswers(ctx context.Context, artifactID string, withBlocks bool) (string, string, []model.ArtifactBlock, error) {
	var markdown, token string
	var blocks []model.ArtifactBlock
	var err error
	if withBlocks {
		markdown, token, blocks, err = s.TextWithTokenAndBlocks(ctx, artifactID)
	} else {
		markdown, token, err = s.TextWithToken(ctx, artifactID)
	}
	if err != nil || !strings.Contains(markdown, "ask{") {
		return markdown, token, blocks, err
	}
	tree, _, err := s.loadTree(ctx, artifactID)
	if err != nil || tree == nil {
		return markdown, token, blocks, err
	}
	shown, err := s.askAnswersShown(ctx, artifactID, tree)
	if err != nil || shown == tree {
		return markdown, token, blocks, err
	}
	rendered, err := renderDocumentTree(shown)
	if err != nil {
		return "", "", nil, err
	}
	if rendered.tokenErr != nil {
		return "", "", nil, rendered.tokenErr
	}
	if !withBlocks {
		return rendered.markdown, rendered.token, nil, nil
	}
	return rendered.markdown, rendered.token, rendered.blocks, nil
}

// askAnswersShown is a copy of tree, a tree of document artifactID, in which each ask block an ask
// of the document indexes, and each copy of an ask of another document of its owner
// (readCopiedAskSources), carries that ask's answer state and answer as the release before wrote
// them into the block (rollWindowAskAttributes): its state, who answered, when, what they chose
// and wrote, and a copy's source and the source's document. The rows are read in a transaction
// of the read's own that writes nothing and locks no row; the asks row is the projection of its
// node, written from the node's tuples in the transaction that appends them (asks.Settle). A tree
// none of whose blocks changes is answered as it is.
func (s *Service) askAnswersShown(ctx context.Context, artifactID string, tree *pmdoc.Node) (*pmdoc.Node, error) {
	nodes, readable := askNodes(tree)
	if !readable || len(nodes) == 0 {
		return tree, nil
	}
	var shown *pmdoc.Node
	err := pgx.BeginTxFunc(ctx, s.store.Pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			select `+AskColumns+`
			from asks a where a.block_artifact_id = $1 and a.block_id is not null
		`, artifactID)
		if err != nil {
			return fmt.Errorf("read the asks of the document's blocks: %w", err)
		}
		indexed := map[string]model.Ask{}
		for rows.Next() {
			ask, err := ScanAsk(rows)
			if err != nil {
				rows.Close()
				return fmt.Errorf("read an ask of the document's blocks: %w", err)
			}
			if ask.BlockID != nil {
				indexed[*ask.BlockID] = ask
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("read the asks of the document's blocks: %w", err)
		}
		var unindexed []askBlock
		var ids []string
		for id, node := range nodes {
			if _, held := indexed[id]; held {
				continue
			}
			if block, err := parseAskBlock(node); err == nil {
				unindexed, ids = append(unindexed, block), append(ids, id)
			}
		}
		var sources map[string]copiedSource
		if len(unindexed) > 0 {
			owner, err := documentOwner(ctx, tx, artifactID)
			if err != nil {
				return err
			}
			if sources, err = readCopiedAskSources(ctx, tx, artifactID, owner, unindexed, ids); err != nil {
				return err
			}
		}
		shown = withAskBlocks(tree, func(node *pmdoc.Node) (*pmdoc.Node, bool) {
			id, _ := node.Attrs[pmdoc.BlockIDAttr].(string)
			if nodes[id] != node {
				return node, false
			}
			write := func(*pmdoc.Node) bool { return false }
			if ask, held := indexed[id]; held {
				write = rollWindowAskAttributes(true, ask, "")
			} else if source, copied := sources[id]; copied {
				write = rollWindowAskAttributes(true, source.ask, source.document)
			}
			answered := attributesOf(node)
			if !write(answered) {
				return node, false
			}
			copied := *node
			copied.Attrs = answered.Attrs
			return &copied, true
		})
		return nil
	})
	return shown, err
}

// withAskBlocks is a copy of tree in which each ask block is the node show answers for it, where
// show says it changed one: each container on the way to such a block is a fresh node, and every
// other node is the tree's own. A tree none of whose ask blocks show changes is answered as it is.
func withAskBlocks(tree *pmdoc.Node, show func(*pmdoc.Node) (*pmdoc.Node, bool)) *pmdoc.Node {
	next, _ := withAskBlocksChanged(tree, show)
	return next
}

func withAskBlocksChanged(tree *pmdoc.Node, show func(*pmdoc.Node) (*pmdoc.Node, bool)) (*pmdoc.Node, bool) {
	node, changed := tree, false
	if tree.Type == "ask" {
		node, changed = show(tree)
	}
	if tree.Type == "text" || len(tree.Children) == 0 {
		return node, changed
	}
	var children []*pmdoc.Node
	for index, child := range tree.Children {
		next, took := withAskBlocksChanged(child, show)
		if !took {
			continue
		}
		if children == nil {
			children = append([]*pmdoc.Node(nil), tree.Children...)
		}
		children[index] = next
	}
	if children == nil {
		return node, changed
	}
	if !changed {
		copied := *tree
		node = &copied
	}
	node.Children = children
	return node, true
}
