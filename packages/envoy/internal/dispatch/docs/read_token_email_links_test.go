package docs

import (
	"context"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// A person types or pastes an address the renderer cannot write as the email rule reads it, so the
// room holds the rule's tree until its settlement. An agent then edits other text of the document
// before that settlement, and the edit route writes its snapshot, which is stored as the settlement
// would leave the tree (TestAVersionBeforeTheSettlementStoresTheBaseEmailLinksItsTreeCannotBeWrittenAs).
// A read of the document's text and token a moment later, once the edit's updates are durable,
// answers a token that an edit can use: a second edit passing it as its document precondition is
// applied, and it is the token the first edit answered. The tester found at 2d0afd0b that the read answers the snapshot's token
// (TextWithToken serves the latest version while its stamp is the room's head, storedText), the
// precondition is checked against the room's tree, and the second edit is refused as stale with no
// write between the read and the edit (INFRA-4224 tester round 9, on real servers and a browser).
func TestAReadBeforeTheSettlementAnswersATokenAnEditCanUse(t *testing.T) {
	writer := model.Actor{Kind: "session", ID: "session-0123456789abcdef"}
	for _, shape := range unwritableAddresses {
		t.Run(shape, func(t *testing.T) {
			service, artifactID, _, _ := roomTypedByBrowser(t, "First.\n\n"+shape+"\n")
			var outcome EditOutcome
			if err := joinedWrite(service, func(ctx context.Context) error {
				var err error
				if outcome, err = service.ApplyOps(ctx, artifactID, []model.EditOp{{Op: "replace", Find: "First.", With: "First, again."}}, writer, nil); err != nil {
					return err
				}
				_, err = service.SnapshotVersion(ctx, artifactID, writer)
				return err
			}); err != nil {
				t.Fatalf("the edit and its snapshot: %v", err)
			}
			// The read a moment later, as an agent makes it: the edit's updates are durable.
			waitForLandedAppends(t, service, artifactID)
			_, read, err := service.TextWithToken(context.Background(), artifactID)
			if err != nil {
				t.Fatalf("read the document's token: %v", err)
			}
			if read != outcome.Token {
				t.Errorf("the edit answered the token %s, and a read right after it answers %s", outcome.Token, read)
			}
			second := []model.EditOp{{Op: "insert", After: "end", Markdown: "A later paragraph.\n"}}
			if _, err := joinedApplyOps(service, artifactID, second, writer, &model.EditPrecondition{Document: read}); err != nil {
				t.Errorf("a second edit under the token the read answered was refused: %v", err)
			}
		})
	}
}
