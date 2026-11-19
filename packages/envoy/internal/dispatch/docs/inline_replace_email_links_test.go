package docs

import (
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
)

// A replace whose `with` is one paragraph of text is written inline, into the textblock the match
// stands in (inlineReplacement), and the text it writes can be one of the shapes whose email rule
// reading has no spelling that reads back: the renderer writes the earlier address as an explicit
// link, and after an explicit link the rule links `smith@example.com`. A write takes the base's
// reading of such an address (R4 of INFRA-4224), so the room holds the base's link on
// `smith@example.com` and its rendering reads back as the room's tree, as it does after an insert
// or a replacement of blocks (TestAnEditOfAnAddressTheRuleCannotWriteStoresTheBaseEmailLinks).
// The tester found the inline path keeping the rule's tree in the room at 39144527: the room's
// text, `See [b-\_www.@example.com](mailto:b-_www.@example.com)./bob-smith@example.com here.`,
// reads back with a link on `smith@example.com` that the room does not hold.
func TestAnInlineReplaceOfAnAddressTheRuleCannotWriteStoresTheBaseEmailLinks(t *testing.T) {
	writer := model.Actor{Kind: "session", ID: "session-0123456789abcdef"}
	for _, shape := range []string{"b-_www.@example.com./bob-smith@example.com", "`c`&period;.@x_y.com/bob-smith@example.com"} {
		for _, row := range []struct{ name, seed string }{
			{name: "inside a paragraph", seed: "First.\n\nSee PLACEHOLDER here.\n"},
			{name: "as a whole paragraph", seed: "First.\n\nPLACEHOLDER\n"},
		} {
			t.Run(row.name+" of "+shape, func(t *testing.T) {
				service, artifactID := newTestService(t)
				service.settle = time.Hour // the test runs each settlement itself
				seedServiceText(t, service, artifactID, row.seed)
				settleCurrentGeneration(t, service, artifactID)
				if _, err := joinedApplyOps(service, artifactID, []model.EditOp{{Op: "replace", Find: "PLACEHOLDER", With: shape}}, writer, nil); err != nil {
					t.Fatalf("the replace was refused: %v", err)
				}
				room := liveTree(t, service, artifactID)
				requireBaseLink(t, "the edited room", room)
				rendering, err := pmdoc.Render(room)
				if err != nil {
					t.Fatalf("render the edited room: %v", err)
				}
				requireReadsBackAs(t, "the edited room's rendering", rendering, room)
				settleCurrentGeneration(t, service, artifactID)
				if err := service.roomFailure(artifactID); err != nil {
					t.Fatalf("the settlement failed the room: %v", err)
				}
				requireSettledAtLatestVersion(t, service, artifactID)
				requireReadsBackAs(t, "the latest version", latestVersionMarkdown(t, service, artifactID), liveTree(t, service, artifactID))
			})
		}
	}
}
