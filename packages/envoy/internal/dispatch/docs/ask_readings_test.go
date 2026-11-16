package docs

import (
	"context"
	"errors"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
)

// shipAskWithReading is seedIndexedShipAsk's ask closing with one reading, r1.
func shipAskWithReading(blockID string) string {
	return "::::ask{#" + blockID + " urgency=\"med\" multiple=\"false\" state=\"open\"}\nShould we ship?\n\n" +
		"- Ship: Release it\n- Hold: Wait for review\n\n" +
		":::reading{#r1}\nThe agent reads the answer as shipping this week.\n:::\n::::\n"
}

// A reading reads a decision, and a person's ask is one only once they answer it, so a reading
// inside an ask that holds no answer is refused on a write that takes markdown - a new version
// and a document's first text here; a rebuild from supplied markdown runs the same check - naming
// the reading and the ask and saying the ask has no answer yet; a browser's write, which is never
// refused, has the ask block flagged `invalid` by settlement with the same reason, and its row
// left as it was. Once the ask is answered, the same markdown is taken.
func TestAReadingInAnAskWithNoAnswerIsRefused(t *testing.T) {
	service, artifactID := seedIndexedShipAsk(t, "ship")
	ctx := context.Background()
	var askID, issueKey string
	if err := service.store.Pool.QueryRow(ctx, `
		select id::text, issue_key from asks where block_artifact_id = $1 and block_id = 'ship'
	`, artifactID).Scan(&askID, &issueKey); err != nil {
		t.Fatalf("read the seeded ask: %v", err)
	}
	want := `reading "r1" stands in the ask dispatch://` + issueKey + `/ask/` + askID +
		`, and that ask has no answer yet: a reading reads a decision, and an ask is one only once a person answers it, so write the reading after the answer`
	actor := model.Actor{Kind: "session", ID: "session-0123456789abcdef"}

	_, err := joinedReplaceText(service, artifactID, shipAskWithReading("ship"), actor)
	var invalid *ErrInvalidAskBlock
	if !errors.As(err, &invalid) || invalid.Error() != want {
		t.Fatalf("a new version holding a reading in the open ask = %v, want it refused with\n%s", err, want)
	}

	second := createIssueDocument(t, service.store, 2, "# Second")
	err = joinedWrite(service, func(ctx context.Context) error {
		_, err := service.SeedText(ctx, second, shipAskWithReading("fresh"), actor)
		return err
	})
	const fresh = `reading "r1" stands in the ask of ask block "fresh", which no ask row holds yet, and that ask has no answer yet: a reading reads a decision, and an ask is one only once a person answers it, so write the reading after the answer`
	if !errors.As(err, &invalid) || invalid.Error() != fresh {
		t.Fatalf("a first text holding a reading in a new ask = %v, want it refused with\n%s", err, fresh)
	}

	editLiveTree(t, service, artifactID, appendToAsk(&pmdoc.Node{
		Type:     "reading",
		Attrs:    pmdoc.Attrs{pmdoc.BlockIDAttr: "r1"},
		Children: []*pmdoc.Node{{Type: "paragraph", Children: []*pmdoc.Node{{Type: "text", Text: "The agent reads it so."}}}},
	}))
	settleCurrentGeneration(t, service, artifactID)
	assertAskMarkedInvalidAndKept(t, service, artifactID, "ship", want)

	answerBlockAs(t, service, artifactID, "ship", "alice", []string{"Ship"})
	if _, err := joinedReplaceText(service, artifactID, shipAskWithReading("ship"), actor); err != nil {
		t.Fatalf("a new version holding a reading in the answered ask = %v, want it taken", err)
	}
}
