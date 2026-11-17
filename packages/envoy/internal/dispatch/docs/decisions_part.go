package docs

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/asks"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
	"github.com/sjawhar/envoy/internal/dispatch/text"
)

// decisionsPartTitle is the title of a spec's `## Decisions` part, the part its decisions and its
// returned answered asks stand in (dispatch://INFRA-1150 R6, R9): a level-2 heading with this
// title, read without regard to case, and every top-level block after it up to the next heading
// of level 2 or above.
const decisionsPartTitle = "Decisions"

// decisionsPartLevel is the level of a `## Decisions` heading.
const decisionsPartLevel = 2

// headingLevel is a heading node's level, 0 for a node that is no heading.
func headingLevel(node *pmdoc.Node) int {
	if node.Type != "heading" {
		return 0
	}
	switch level := node.Attrs["level"].(type) {
	case int:
		return level
	case int64:
		return int(level)
	case float64:
		return int(level)
	}
	return 1
}

// decisionsPart is where tree's `## Decisions` part stands among its top-level children: the
// index of its heading and the index after its last block, or -1 and -1 where tree holds none. The
// first such heading is the part.
func decisionsPart(tree *pmdoc.Node) (int, int) {
	if tree == nil {
		return -1, -1
	}
	for index, child := range tree.Children {
		if headingLevel(child) != decisionsPartLevel ||
			!strings.EqualFold(strings.TrimSpace(pmdoc.TextContent(child)), decisionsPartTitle) {
			continue
		}
		end := index + 1
		for end < len(tree.Children) {
			if level := headingLevel(tree.Children[end]); level > 0 && level <= decisionsPartLevel {
				break
			}
			end++
		}
		return index, end
	}
	return -1, -1
}

// hasDecisionsPart reports whether tree holds a `## Decisions` part.
func hasDecisionsPart(tree *pmdoc.Node) bool {
	start, _ := decisionsPart(tree)
	return start >= 0
}

// withoutDecisionsPartBlocks is a copy of tree without each top-level block of its `## Decisions`
// part drop names, and without the part's heading where the part then holds nothing else, and
// whether it took anything out. tree itself, and every node it keeps, is left as it was.
func withoutDecisionsPartBlocks(tree *pmdoc.Node, drop func(*pmdoc.Node) bool) (*pmdoc.Node, bool) {
	start, end := decisionsPart(tree)
	if start < 0 {
		return tree, false
	}
	kept := make([]*pmdoc.Node, 0, len(tree.Children))
	kept = append(kept, tree.Children[:start]...)
	part := make([]*pmdoc.Node, 0, end-start-1)
	for _, child := range tree.Children[start+1 : end] {
		if !drop(child) {
			part = append(part, child)
		}
	}
	took := len(part) < end-start-1
	if len(part) > 0 {
		kept = append(kept, tree.Children[start])
		kept = append(kept, part...)
	} else {
		took = true
	}
	kept = append(kept, tree.Children[end:]...)
	if !took {
		return tree, false
	}
	copied := *tree
	copied.Children = kept
	return &copied, true
}

// withoutBlocks is a copy of tree without each block drop names, wherever it stands, and whether
// it took one out. Each container on the way to one is a fresh node; every other node is tree's
// own, which nothing here changes.
func withoutBlocks(tree *pmdoc.Node, drop func(*pmdoc.Node) bool) (*pmdoc.Node, bool) {
	if tree.Type == "text" || len(tree.Children) == 0 {
		return tree, false
	}
	var children []*pmdoc.Node
	took := false
	for index, child := range tree.Children {
		if drop(child) {
			if children == nil {
				children = slices.Clone(tree.Children[:index])
			}
			took = true
			continue
		}
		next, tookBelow := withoutBlocks(child, drop)
		if tookBelow && children == nil {
			children = slices.Clone(tree.Children[:index])
		}
		if children != nil {
			children = append(children, next)
		}
		took = took || tookBelow
	}
	if !took {
		return tree, false
	}
	copied := *tree
	copied.Children = children
	return &copied, true
}

// withoutAnsweredAskTies is a copy of tree with the `shaped` pointers taken off each ask block
// decided names, an ask holding an answer, and whether it took one off.
func withoutAnsweredAskTies(tree *pmdoc.Node, decided map[string]bool) (*pmdoc.Node, bool) {
	if len(decided) == 0 {
		return tree, false
	}
	return withAskBlocksChanged(tree, func(node *pmdoc.Node) (*pmdoc.Node, bool) {
		id, _ := node.Attrs[pmdoc.BlockIDAttr].(string)
		if _, holds := node.Attrs[shapedAttribute]; !holds || !decided[id] {
			return node, false
		}
		shown := *node
		shown.Attrs = maps.Clone(node.Attrs)
		delete(shown.Attrs, shapedAttribute)
		return &shown, true
	})
}

// shapedAttribute is the `block[]` attribute of a decision and of an ask that names each spec
// node it shaped (dispatch://INFRA-1150 R5).
const shapedAttribute = "shaped"

// decidedAskBlocks is each ask block of a version, as asksAgreeWithTheirNodes read it (shown),
// whose ask holds an answer, by block id: its own document's row, or the source of a copy, read
// answered (asks.StateAnswered).
func decidedAskBlocks(shown versionAsks) map[string]bool {
	decided := map[string]bool{}
	for id := range shown.nodes {
		if row, indexed := shown.rows[id]; indexed {
			decided[id] = row.State == asks.StateAnswered
			continue
		}
		if source, copied := shown.sources[id]; copied {
			decided[id] = source.ask.State == asks.StateAnswered
		}
	}
	return decided
}

// restoredAskBlocks is each answered ask block (decided) that stands in version's `## Decisions`
// part and that restoreCase takes out of both versions: one the approved version does not hold
// under its id, and one it holds under that id with the words the person approved - the question,
// the options, multiple and urgency (sameAskWording). A block whose words moved since is not
// among them, so the restore that returns it is a difference no case carries (R7).
func restoredAskBlocks(approved, version *pmdoc.Node, decided map[string]bool) map[string]bool {
	start, end := decisionsPart(version)
	if start < 0 {
		return nil
	}
	before, _ := askNodes(approved)
	restored := map[string]bool{}
	for _, child := range version.Children[start+1 : end] {
		id, _ := child.Attrs[pmdoc.BlockIDAttr].(string)
		if child.Type != "ask" || !decided[id] {
			continue
		}
		if held, approvedHeld := before[id]; approvedHeld && !sameAskWording(held, child) {
			continue
		}
		restored[id] = true
	}
	return restored
}

// sameAskWording reports whether two ask blocks ask the same thing, as settlement reads each
// (parseAskBlock): the same question, options, multiple and urgency.
func sameAskWording(a, b *pmdoc.Node) bool {
	first, err := parseAskBlock(a)
	if err != nil {
		return false
	}
	second, err := parseAskBlock(b)
	return err == nil && first.question == second.question && reflect.DeepEqual(first.options, second.options) &&
		first.multiple == second.multiple && first.urgency == second.urgency
}

// decisionsDocumentRefs is the ref_key of each decisions document of the issue document
// artifactID belongs to that stands (decisions_documents()), the documents a pointer line of its
// `## Decisions` part names; none for a project's document.
func decisionsDocumentRefs(ctx context.Context, tx pgx.Tx, artifactID string) (map[string]bool, error) {
	rows, err := tx.Query(ctx, `
		select d.ref_key from decisions_documents() d
		join artifacts a on a.issue_key = d.issue_key
		where a.id = $1
	`, artifactID)
	if err != nil {
		return nil, fmt.Errorf("read the decisions documents of the issue of %s: %w", artifactID, err)
	}
	refKeys, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("read the decisions documents of the issue of %s: %w", artifactID, err)
	}
	documents := make(map[string]bool, len(refKeys))
	for _, refKey := range refKeys {
		documents[refKey] = true
	}
	return documents, nil
}

// pointsAtDecisions reports whether node is a pointer line of a `## Decisions` part: a paragraph
// citing one of input's decisions documents (carryInput.decisions), as that document or one of
// its blocks.
func (input carryInput) pointsAtDecisions(node *pmdoc.Node) bool {
	return pointsAtDocuments(node, input.decisions, input.serverURL)
}

// pointsAtDocuments reports whether node is a paragraph citing one of documents, by ref_key, as
// the document or as one of its blocks, read from the paragraph's markdown as every reader of a
// citation reads it (text.Extract).
func pointsAtDocuments(node *pmdoc.Node, documents map[string]bool, serverURL string) bool {
	if node.Type != "paragraph" || len(documents) == 0 {
		return false
	}
	markdown, err := pmdoc.Render(&pmdoc.Node{Type: "doc", Children: []*pmdoc.Node{node}})
	if err != nil {
		return false
	}
	for _, ref := range text.Extract(markdown, serverURL) {
		switch ref.Kind {
		case "artifact":
			if documents[text.TargetID(ref)] {
				return true
			}
		case "block":
			if ref.Item == "" {
				document := ref
				document.Kind, document.Block, document.Pin = "artifact", "", 0
				if documents[text.TargetID(document)] {
					return true
				}
			}
		}
	}
	return false
}
