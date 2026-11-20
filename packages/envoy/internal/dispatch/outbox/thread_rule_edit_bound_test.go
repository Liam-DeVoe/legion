package outbox

import (
	"context"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/dispatch/envoy"
	"github.com/sjawhar/envoy/internal/dispatch/events"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// The wait of the copy of a reply's edit bound for a session the issue's primary document credits
// alone has the bound the reply's own credit wait has (mentionDeliveries.editCreditCopyOf,
// writtenPastTheBound, unnamedForwardBound). The person's reply in B's thread (B live) names C,
// and C's mention is past the lease the outbox waits a send out for, its sender alive and its
// lane held, so the attempt stays pending however old it grows (agedUnnamedAttempts). The reply's
// copy to C is a fallback row (destinations.leftTo). The person's edit, as the API writes it
// (mentions []), waits for that mention within the bound, and holds C's later events of the issue
// while it waits. Past the bound nothing else would end the wait: the edit is decided then, and C,
// which the reply names by a mention still owed, keeps it, so the edit is published.
func TestAnEditOfAReplyWaitsForTheMentionOfACreditNoLongerThanTheBound(t *testing.T) {
	for _, tc := range []struct {
		name  string
		age   time.Duration
		waits bool
	}{
		{name: "within the bound", age: model.DeliveryClaimLapse + model.DeliveryClaimLapse/2, waits: true},
		{name: "past the bound", age: unnamedForwardBound + 30*time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			database := storetest.Open(t)
			broker := events.NewBroker()
			issue := seedThreadRuleIssue(t, database)
			root := seedThreadComment(t, database, "T-1", "", issue.b, nil)
			reply := seedThreadComment(t, database, "T-1", "", issue.person, &root)
			seedMentionDelivery(t, database, reply, issue.c.ID, "pending")
			agedUnnamedAttempts(t, database, reply, tc.age)
			created := appendCommentEvent(t, database, broker, "comment.created", issue.person, model.Comment{ID: reply, ReplyTo: &root}, issue.c.ID)
			edited := appendEvent(t, database, broker, model.Event{
				IssueKey: new("T-1"), Type: "comment.edited", Actor: issue.person,
				Payload: model.CommentEventPayload{Comment: model.Comment{
					ID: reply, IssueKey: new("T-1"), Author: issue.person, ReplyTo: &root, Body: "Edited.",
					Mentions: []model.Mention{},
				}},
			})
			publisher := &recordingPublisher{}
			deps := Deps{Store: database, Publisher: publisher, Broker: events.NewBroker(),
				Registry: newFakeRegistry(map[string]registryAnswer{issue.b.ID: {state: envoy.StateLive}})}
			o := holding(t, deps)
			if _, err := scanBatch(ctx, deps, unfenced{}, o.instance); err != nil {
				t.Fatalf("scan batch: %v", err)
			}
			if publishedAt(t, database, created.ID) == nil {
				t.Fatalf("the reply was not published: its copy to C, whose mention is past its lease, is a fallback row")
			}
			if held := heldDestinations(t, database, reply); len(held) != 1 || held[0] != contracts.LegacyAgentSubject(issue.c.ID) {
				t.Fatalf("the reply's fallback rows are %v, want C's own topic alone", held)
			}
			published := publishedAt(t, database, edited.ID) != nil
			copied := len(copiesOf(publisher.all(), edited, issue.c.ID)) > 0
			switch {
			case tc.waits && (published || copied):
				t.Fatalf("the edit was published %v and reached C %v while C's mention, %s old, was still owed within the bound (%s): its copy to C waits for it",
					published, copied, tc.age, unnamedForwardBound)
			case !tc.waits && !published:
				t.Fatalf("the edit is still unpublished while C's mention, %s old, is past the bound (%s): its wait for that mention has no end, and it holds C's later events of the issue",
					tc.age, unnamedForwardBound)
			case !tc.waits && !copied:
				t.Fatalf("the edit reached %v and not C, whose mention by the reply is still owed past the bound: the reply reaches C by that mention or by its row",
					agentTopics(readEventRow(t, database, edited.ID).destinations))
			}
		})
	}
}
