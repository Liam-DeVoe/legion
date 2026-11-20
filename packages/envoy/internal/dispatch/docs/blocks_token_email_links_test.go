package docs

import (
	"context"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// A person types or pastes an address the renderer cannot write as the email rule reads it, so the
// room holds the rule's tree until its settlement. The blocks route reads the document's blocks
// (Blocks, through TextWithBlocks), each with its token, which an edit passes as a block
// precondition. While that settlement is owed, the block token the route answers names the block
// the document stores: an edit guarded by it, inserting after that block, is applied. The tester
// found at 74f47f2b that Blocks answers the token of the block as the room holds it (TextWithBlocks
// takes no settlement before it answers, where TextWithToken and TextWithTokenAndBlocks do), the
// edit repairs the room before it checks its precondition, and the edit is refused as "block
// changed" with no write between the read and the edit (INFRA-4224 tester round 10, on real
// servers and a browser, through GET /api/v1/artifacts/{id}/blocks and the issue slug route).
func TestABlocksReadBeforeTheSettlementAnswersBlockTokensAnEditCanUse(t *testing.T) {
	writer := model.Actor{Kind: "session", ID: "session-0123456789abcdef"}
	for _, shape := range unwritableAddresses {
		t.Run(shape, func(t *testing.T) {
			service, artifactID, _, _ := roomTypedByBrowser(t, "First.\n\n"+shape+"\n")
			markdown, blocks, err := service.TextWithBlocks(context.Background(), artifactID)
			if err != nil {
				t.Fatalf("read the document's blocks: %v", err)
			}
			var guard model.EditBlockPrecondition
			for _, block := range blocks {
				if strings.Contains(markdown[block.From:block.To], laterAddress) {
					guard = model.EditBlockPrecondition{ID: block.ID, Token: block.Token}
				}
			}
			if guard.ID == "" {
				t.Fatalf("no block of %q holds %s: %+v", markdown, laterAddress, blocks)
			}
			edit := []model.EditOp{{Op: "insert", After: "block:" + guard.ID, Markdown: "A later paragraph.\n"}}
			if _, err := joinedApplyOps(service, artifactID, edit, writer, &model.EditPrecondition{Blocks: []model.EditBlockPrecondition{guard}}); err != nil {
				t.Errorf("an edit guarded by the block token the blocks read answered was refused: %v", err)
			}
		})
	}
}
