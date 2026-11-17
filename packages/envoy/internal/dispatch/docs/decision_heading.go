package docs

import (
	"context"
	"fmt"

	"github.com/sjawhar/envoy/internal/dispatch/decisionread"
	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
)

// HeadingDecision is a section of an issue's decisions document, read on its heading block for
// a Keep (dispatch://INFRA-1150 R11, ruling F3). Text is the section's markdown, from its heading
// to its last block. TextSHA256 is the sum of that text (decisionread.TextSum): the sum the
// release before's Decisions read served, and the sum the conversion compares a Keep with
// (issueRestore.sectionKeeps). ChangedVersion is the greatest version the block index recorded
// for one of the section's blocks, as that read gave it.
type HeadingDecision struct {
	Text           string
	TextSHA256     string
	ChangedVersion int
}

// DecisionHeading reads the section that the heading block blockID opens in document artifactID,
// through q, and reports whether there is one. The document must be a decisions document that
// stands (decisions_documents()). The section is cut by the decisions step's section reader
// (decisionSectionsOf) from the document as this server holds it. A retired document, another
// document, a block that opens no section, and a section under `## Replaced` each answer found
// false.
//
// kept for one release: heading-keep
func (s *Service) DecisionHeading(ctx context.Context, q graphstore.Queryer, artifactID, blockID string) (HeadingDecision, bool, error) {
	var standing bool
	if err := q.QueryRow(ctx, `select exists(select 1 from decisions_documents() where id = $1::uuid)`, artifactID).
		Scan(&standing); err != nil {
		return HeadingDecision{}, false, fmt.Errorf("read whether %s is a decisions document: %w", artifactID, err)
	}
	if !standing {
		return HeadingDecision{}, false, nil
	}
	tree, _, err := s.loadTree(ctx, artifactID)
	if err != nil || tree == nil {
		return HeadingDecision{}, false, err
	}
	var section *decisionSection
	for _, candidate := range decisionSectionsOf(tree) {
		if candidate.headingID() == blockID && !candidate.replaced {
			section = &candidate
			break
		}
	}
	if section == nil {
		return HeadingDecision{}, false, nil
	}
	markdown, offsets, err := renderTreeWithOffsets(tree)
	if err != nil {
		return HeadingDecision{}, false, err
	}
	ranges := make(map[string][2]int, len(offsets))
	for _, offset := range offsets {
		ranges[offset.ID] = [2]int{offset.From, offset.To}
	}
	changes, err := refs.BlockChanges(ctx, q, artifactID)
	if err != nil {
		return HeadingDecision{}, false, err
	}
	start, end := -1, -1
	decision := HeadingDecision{}
	for _, id := range section.blockIDs() {
		if change, found := changes[id]; found && change.Version > decision.ChangedVersion {
			decision.ChangedVersion = change.Version
		}
		span, found := ranges[id]
		if !found {
			continue
		}
		if start < 0 || span[0] < start {
			start = span[0]
		}
		if span[1] > end {
			end = span[1]
		}
	}
	if start < 0 || end > len(markdown) || start > end {
		return HeadingDecision{}, false, nil
	}
	decision.Text = markdown[start:end]
	decision.TextSHA256 = decisionread.TextSum(decision.Text)
	return decision, true, nil
}
