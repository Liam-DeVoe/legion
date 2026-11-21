package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/recordkey"
	"github.com/sjawhar/envoy/internal/session"
)

// replayRow is one comment_fallback_copies row as the merge base stored it, for one comment of
// project: its destination, a record key in the bytes that build wrote, and the key it is held
// for, a session (session_id) or a mention target (target).
type replayRow struct {
	project, issue, destination string
	session, target             string
	event                       int64
}

// seedReplayRow writes row's project and issue, a comment on the issue and its comment.created
// event, published, and the row itself as an earlier build wrote it, and answers the event's id.
func seedReplayRow(t *testing.T, rig *roleRig, row replayRow) int64 {
	t.Helper()
	ctx := context.Background()
	if _, err := rig.database.Pool.Exec(ctx, `
		insert into projects (key, name, default_assignee, owner) values ($1, $1, 'alice', 'alice') on conflict do nothing;
	`, row.project); err != nil {
		t.Fatalf("create project %s: %v", row.project, err)
	}
	if _, err := rig.database.Pool.Exec(ctx, `
		insert into issues (key, project_key, number, title, created_by, rank)
		values ($1, $2, 1, $1, '{"kind":"user","id":"alice"}', 'U')
	`, row.issue, row.project); err != nil {
		t.Fatalf("create issue %s: %v", row.issue, err)
	}
	alice := model.Actor{Kind: "user", ID: "alice"}
	commentID := seedComment(t, rig.database, row.issue, alice, "For whoever this reaches.", nil)
	event := appendEvent(t, rig.database, rig.broker, model.Event{
		IssueKey: new(row.issue), Type: "comment.created", Actor: alice,
		Payload: model.CommentEventPayload{Comment: model.Comment{ID: commentID, IssueKey: new(row.issue), Body: "For whoever this reaches."}},
	})
	var session, target *string
	if row.session != "" {
		session = new(row.session)
	} else {
		target = new(row.target)
	}
	if _, err := rig.database.Pool.Exec(ctx, `
		update events set published_at = now() where id = $1;
	`, event.ID); err != nil {
		t.Fatalf("publish %s's comment event: %v", row.issue, err)
	}
	if _, err := rig.database.Pool.Exec(ctx, `
		insert into comment_fallback_copies (comment_id, destination, event_id, session_id, target)
		values ($1, $2, $3, $4, $5)
	`, commentID, row.destination, event.ID, session, target); err != nil {
		t.Fatalf("store %s's fallback row at %s: %v", row.issue, row.destination, err)
	}
	return event.ID
}

// decodedWithoutLegacy is a stored role key read as contracts.CanonicalRole reads a role once
// #t-c-14 deletes its legacy branch: the new form alone, <project>.<role>. A stored key names the
// role alone, so it names no role read this way.
func decodedWithoutLegacy(key string) (project, role string, ok bool) {
	named, found := strings.CutPrefix(key, contracts.RoleTopicPrefix)
	if !found {
		return "", "", false
	}
	return strings.Cut(named, ".")
}

// TestAFallbackCopyReplaysAStoredKeyOnTheNewSubject (decideFallbackCopy, recordkey.Parse,
// fallbackRoleForward), over a test hub and Dispatch's test database: three comment_fallback_copies
// rows as the merge base stored them are each replayed on the subject that names their project.
//
//   - notifications.agent.S, for a comment of PRIV, is published on notifications.agent.PRIV.S and
//     received by a session consumer of S whose filter holds the new form alone, as every filter is
//     after #t-c-14.
//   - notifications.role.legion-infra-infra-12-architect, for a comment of INFRA, resolves (INFRA,
//     legion-infra-infra-12-architect) and is forwarded to its holder on
//     notifications.agent.INFRA.<holder>.
//   - notifications.role.reviewer, for a comment of PUB, with no holder, resolves (PUB, reviewer)
//     and is kept on notifications.envoy.kept.role.PUB.reviewer.
//
// Each row is settled published. No row reads a legacy branch of contracts.
//
// Controls, each on its own: the copy sent on the stored key as its topic (main's
// `item.Topic = held.destination`), which S's new-form consumer does not receive, and the check
// fails naming notifications.agent.S; and the stored role keys decoded as contracts.CanonicalRole
// decodes once its legacy branch is removed, under which neither names a role, so the Legion row
// resolves no holder and the check fails naming the key.
func TestAFallbackCopyReplaysAStoredKeyOnTheNewSubject(t *testing.T) {
	rig := newRoleRig(t)
	ctx := context.Background()
	const (
		sessionS = "S"
		legion   = "legion-infra-infra-12-architect"
		holder   = "ses_infra_holder"
	)
	js, err := rig.conn.JetStream()
	if err != nil {
		t.Fatalf("open JetStream: %v", err)
	}
	newForm := contracts.AgentSubject("PRIV", sessionS)
	consumer, err := js.SubscribeSync(newForm, natsgo.BindStream(bus.Stream), natsgo.DeliverNew())
	if err != nil {
		t.Fatalf("open S's consumer on %s: %v", newForm, err)
	}
	t.Cleanup(func() { _ = consumer.Unsubscribe() })
	forwards := make(chan contracts.Envelope, 4)
	sub, err := rig.conn.Subscribe(contracts.AgentSubject("INFRA", holder), func(msg *natsgo.Msg) {
		var item contracts.Envelope
		if json.Unmarshal(msg.Data, &item) == nil {
			forwards <- item
		}
	})
	if err != nil {
		t.Fatalf("subscribe the holder: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	if err := rig.conn.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	rig.claim(t, legion, holder, session.SessionEntry{SelfSubscribed: true})

	rows := []replayRow{
		{project: "PRIV", issue: "PRIV-1", destination: recordkey.Session(sessionS), session: sessionS},
		{project: "INFRA", issue: "INFRA-1", destination: recordkey.Role(legion), target: "role:" + legion},
		{project: "PUB", issue: "PUB-1", destination: recordkey.Role("reviewer"), target: "role:reviewer"},
	}
	for index := range rows {
		rows[index].event = seedReplayRow(t, rig, rows[index])
	}
	if _, err := rig.database.Pool.Exec(ctx, `update projects set visibility = 'private' where key = 'PRIV'`); err != nil {
		t.Fatalf("make PRIV private: %v", err)
	}
	deps := rig.deps()
	publishFallbackCopies(ctx, deps, unfenced{}, holding(t, deps).instance)

	eventID := func(row replayRow) string { return fmt.Sprintf("dispatch-%d", row.event) }
	received, err := consumer.NextMsg(10 * time.Second)
	if err != nil {
		t.Fatalf("S's consumer of %s received nothing of %s's row (%v), want the copy replayed on the new subject", newForm, rows[0].destination, err)
	}
	var copied contracts.Envelope
	if err := json.Unmarshal(received.Data, &copied); err != nil || copied.EventID != eventID(rows[0]) || received.Subject != newForm {
		t.Fatalf("S's consumer received %s on %s (%v), want %s's copy on %s", copied.EventID, received.Subject, err, rows[0].issue, newForm)
	}
	select {
	case forward := <-forwards:
		if forward.EventID != eventID(rows[1]) || forward.Topic != contracts.RoleSubject("INFRA", legion) {
			t.Fatalf("the holder was forwarded %s on topic %s, want %s's copy on %s", forward.EventID, forward.Topic, rows[1].issue, contracts.RoleSubject("INFRA", legion))
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("the holder of %s took no forward on %s, want %s's row forwarded there", legion, contracts.AgentSubject("INFRA", holder), rows[1].destination)
	}
	kept, err := rig.client.NextStoredMessage(ctx, bus.KeptRoleSubject("PUB.reviewer"), 1)
	if err != nil {
		t.Fatalf("read the kept lane of (PUB, reviewer): %v, want %s's row kept there", err, rows[2].destination)
	}
	var keptItem contracts.Envelope
	if err := json.Unmarshal(kept.Data, &keptItem); err != nil || keptItem.EventID != eventID(rows[2]) {
		t.Fatalf("the kept lane of (PUB, reviewer) holds %s (%v), want %s's copy", keptItem.EventID, err, rows[2].issue)
	}
	for _, row := range rows {
		var settled, published bool
		if err := rig.database.Pool.QueryRow(ctx, `
			select settled_at is not null, published from comment_fallback_copies where event_id = $1 and destination = $2
		`, row.event, row.destination).Scan(&settled, &published); err != nil || !settled || !published {
			t.Errorf("%s's row at %s is settled %v and published %v (%v), want both", row.issue, row.destination, settled, published, err)
		}
	}

	// Control: the copy sent on the stored key as its topic reaches no new-form consumer.
	stored := copied
	stored.Topic = rows[0].destination
	if err := rig.client.Publish(stored); err != nil {
		t.Fatalf("control: publish the copy on the stored key %s: %v", rows[0].destination, err)
	}
	if msg, err := consumer.NextMsg(2 * time.Second); !errors.Is(err, natsgo.ErrTimeout) {
		t.Fatalf("control: S's new-form consumer received %v (%v) from a copy on %s, want nothing, the check failing naming it", msg, err, rows[0].destination)
	}

	// Control: decoded without the legacy branch, no stored role key names a role.
	for _, row := range rows[1:] {
		if project, role, ok := decodedWithoutLegacy(row.destination); ok {
			t.Fatalf("control: %s decodes without the legacy branch as (%s, %s), want no role, the check failing naming the key", row.destination, project, role)
		}
	}
}
