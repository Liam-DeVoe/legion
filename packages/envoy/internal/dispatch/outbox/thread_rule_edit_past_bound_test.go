package outbox

import (
	"context"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/envoy"
	"github.com/sjawhar/envoy/internal/dispatch/events"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// R6: "a person's reply in a thread, and the person's edit of such a reply, reach the thread's
// sessions and no session that only a document credits ... A session that the reply names, whose
// mention failed, still gets the copy". The person's reply in B's thread (B live) names C, which
// the issue's primary document credits, and C's mention is held by its sender past
// unnamedForwardBound (its lane is held: it writes the claim again and again,
// agedUnnamedAttempts). The person edits the reply, as the API writes the edit (mentions []). Past
// the bound the edit's wait for that mention ends and C is kept (editCreditCopyOf), so the edit is
// published to C at once. Then the mention lands: it delivered the reply to C, which is a session
// only a document credits whose mention did not fail, so R6 gives it no copy of the edit - yet it
// has one. Without the thread rule (the control: B gone) C gets the edit as a credit.
func TestAnEditPastTheBoundReachesNoCreditWhoseMentionThenLands(t *testing.T) {
	for _, b := range []string{envoy.StateLive, envoy.StateGone} {
		t.Run("B "+b, func(t *testing.T) {
			ctx := context.Background()
			database := storetest.Open(t)
			broker := events.NewBroker()
			issue := seedThreadRuleIssue(t, database)
			root := seedThreadComment(t, database, "T-1", "", issue.b, nil)
			reply := seedThreadComment(t, database, "T-1", "", issue.person, &root)
			seedMentionDelivery(t, database, reply, issue.c.ID, "pending")
			agedUnnamedAttempts(t, database, reply, unnamedForwardBound+30*time.Second)
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
				Registry: newFakeRegistry(map[string]registryAnswer{issue.b.ID: {state: b}})}
			o := holding(t, deps)
			if _, err := scanBatch(ctx, deps, unfenced{}, o.instance); err != nil {
				t.Fatalf("scan batch: %v", err)
			}
			// The sender's lane reaches the send at last, and it lands.
			if _, err := database.Pool.Exec(ctx, `update comment_deliveries set state = 'sent' where comment_id = $1`, reply); err != nil {
				t.Fatalf("land the mention of %s: %v", issue.c.ID, err)
			}
			settleLater(t, database, deps, o.instance)
			if got := copiesOf(publisher.all(), created, issue.c.ID); len(got) != 0 {
				t.Fatalf("C got %d copies of the reply from the outbox, want none: its mention delivered it", len(got))
			}
			if b == envoy.StateLive {
				if got := copiesOf(publisher.all(), edited, issue.c.ID); len(got) != 0 {
					t.Fatalf("C, which only the primary document credits and whose mention of the reply was delivered, got the person's edit %d times: past unnamedForwardBound the edit's wait ended and kept C while that mention was still owed", len(got))
				}
			}
		})
	}
}

// R6: "A session that the reply names, whose mention failed, still gets the copy that the author
// route sends under the key of that mention", read for the person's edit as the round-5 test
// TestAnEditMadeWhileTheReplysMentionOfACreditIsBeingSentReachesItOnceThatMentionFails reads it:
// the edit follows the reply to a named credit whose mention failed. The person's reply in B's
// thread (B live) names @role:R, whose listener lookup went unanswered and whose claim the outbox
// cannot read; the sender holds the attempt past unnamedForwardBound, so A's copy of the reply is
// a credit's row (creditHeldAsARow). The person edits the reply (as the API writes it), and the
// edit, read past the bound, leaves A out. The sender then pins the attempt to A, and it fails: A
// gets the reply from its row, and never the edit.
func TestAnEditOfAReplyWhoseLateRoleMentionOfACreditFailsReachesIt(t *testing.T) {
	ctx := context.Background()
	database := storetest.Open(t)
	broker := events.NewBroker()
	issue := seedThreadRuleIssue(t, database)
	target := "role:legion-reviewer-t1"
	root := seedThreadComment(t, database, "T-1", "", issue.b, nil)
	reply := seedThreadComment(t, database, "T-1", "", issue.person, &root)
	seedDelivery(t, database, reply, target, "", "pending")
	agedUnnamedAttempts(t, database, reply, unnamedForwardBound+30*time.Second)
	created := appendEvent(t, database, broker, model.Event{
		IssueKey: new("T-1"), Type: "comment.created", Actor: issue.person,
		Payload: model.CommentEventPayload{Comment: model.Comment{
			ID: reply, IssueKey: new("T-1"), Author: issue.person, ReplyTo: &root,
			Mentions: []model.Mention{{Target: target, Delivery: "steer"}},
		}},
	})
	edited := appendEvent(t, database, broker, model.Event{
		IssueKey: new("T-1"), Type: "comment.edited", Actor: issue.person,
		Payload: model.CommentEventPayload{Comment: model.Comment{
			ID: reply, IssueKey: new("T-1"), Author: issue.person, ReplyTo: &root, Body: "Edited.",
			Mentions: []model.Mention{},
		}},
	})
	publisher := &recordingPublisher{}
	deps := Deps{
		Store: database, Publisher: publisher, Broker: events.NewBroker(),
		Registry:   newFakeRegistry(map[string]registryAnswer{issue.b.ID: {state: envoy.StateLive}}),
		RoleClaims: unreadableRoleClaims{}, Sessions: heldRoles{},
	}
	o := holding(t, deps)
	if _, err := scanBatch(ctx, deps, unfenced{}, o.instance); err != nil {
		t.Fatalf("scan batch: %v", err)
	}
	if _, err := database.Pool.Exec(ctx, `
		update comment_deliveries set session_id = $3, state = 'failed', error = 'envoy listener: delivery failed'
		where comment_id = $1 and target = $2
	`, reply, target, issue.a.ID); err != nil {
		t.Fatalf("fail the mention pinned to %s: %v", issue.a.ID, err)
	}
	settleLater(t, database, deps, o.instance)
	if len(copiesOf(publisher.all(), created, issue.a.ID)) != 1 {
		t.Fatalf("A, named by the reply, whose mention failed, got %d copies of the reply, want 1", len(copiesOf(publisher.all(), created, issue.a.ID)))
	}
	if len(copiesOf(publisher.all(), edited, issue.a.ID)) == 0 {
		t.Fatalf("A, named by the reply, whose mention failed, got the reply and not the person's edit of it (the edit reached %v): it holds the reply's first words",
			agentTopics(readEventRow(t, database, edited.ID).destinations))
	}
}
