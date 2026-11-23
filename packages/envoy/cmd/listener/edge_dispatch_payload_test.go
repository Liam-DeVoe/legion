package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// TestEdgeFrameVerdictPassesADispatchFrameOnlyOfAListedProject holds the edge's frame rule to the
// project of the Dispatch event a frame carries (dispatchFrameWithheld, dispatchFrameProject).
// Dispatch sends an issue's event to a session by the session's name - the owner route, the
// claimant, a primary document's author, an author route, a follower, the previous claimant, a
// mention (the API's send, and the outbox's copy to a session whose mention failed), a targeted
// message and a role forward - on the session's own agent subject or on the role's lane, under a
// topic that names the session or the lane and no project. Each kind is built here as Dispatch
// builds it (the outbox's envelope with its topic set to the destination, or the API's send
// through contracts.AgentSendEnvelope, with the event it carries), once with another project's
// event, which is withheld, and once with the token's project's, which passes. The mention and
// targeted-message frames carry an issue's event with an empty project, as Dispatch sends them, so
// their project is the one the issue key names. A direct message on no issue names no project and
// is withheld from an outside session, as a frame the edge cannot place in a listed project; the
// same message on the token's project's issue passes, and a listener that is no edge one hands it
// on. A payload with no project, one encoding/json does not read, an event whose project and
// issue key disagree, and a key of the payload that readers disagree on are withheld; a frame of
// another source passes by its topic whatever its payload says.
func TestEdgeFrameVerdictPassesADispatchFrameOnlyOfAListedProject(t *testing.T) {
	const session = "ses_v"
	own := contracts.LegacyAgentSubject(session)
	const privRole = "legion-priv-priv-1-tester"
	privLane := contracts.RoleTopicPrefix + privRole
	projects := []string{"PRIV"}
	notHeld := frameDrops(withheldProjectNotHeld)
	unread := frameDrops(withheldDispatchProjectUnread)
	person := model.Actor{Kind: "person", ID: "owner@example.com"}
	created := time.Date(2026, 11, 23, 20, 0, 0, 0, time.UTC)

	// issueEvent is an issue's event as the outbox reads it, its project the issue's
	// (readBatchSQL's coalesce of the event's, the issue's and the artifact's project key).
	issueEvent := func(project, typ string, payload map[string]any) model.Event {
		key := project + "-1"
		return model.Event{ID: 41, IssueKey: &key, Project: project, Seq: 7, Type: typ, Actor: person,
			Notify: true, CreatedAt: created, Payload: payload}
	}
	// documentEvent is a project document's event, which names its project and no issue.
	documentEvent := func(project, typ string, payload map[string]any) model.Event {
		artifact := "6c1f3a52-0000-4000-8000-000000000001"
		return model.Event{ID: 42, ArtifactID: &artifact, Project: project, Seq: 3, Type: typ, Actor: person,
			Notify: true, CreatedAt: created, Payload: payload}
	}
	// apiEvent is the event the API's mention and targeted-message frames carry: an issue's event
	// read with the events row's own project key, which an issue's event leaves empty
	// (loadCommentCreatedEvent, messageEvent), or no issue at all.
	apiEvent := func(issue, typ string, payload map[string]any) model.Event {
		event := model.Event{ID: 43, Seq: 9, Type: typ, Actor: person, Notify: true, CreatedAt: created, Payload: payload}
		if issue != "" {
			event.IssueKey = &issue
		}
		return event
	}
	encode := func(v any) string {
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	marshal := func(item contracts.Envelope) []byte {
		data, err := json.Marshal(item)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	// routed is the outbox's copy of event to one destination (publisher.go envelope, then
	// batch.go's routed.Topic = the destination's subject and its dedupe key where it has one).
	routed := func(event model.Event, destination, dedupeKey string) []byte {
		item := contracts.Envelope{
			EventID: "dispatch-41", Source: "dispatch", SourceEventID: "41", Topic: destination,
			DedupeKey: "dispatch-41", IssuedAt: created.UnixMilli(), PayloadSummary: event.Type,
			Payload: encode(struct {
				model.Event
				Quiet bool `json:"quiet"`
			}{Event: event}),
			TraceID: "dispatch-41",
		}
		if dedupeKey != "" {
			item.DedupeKey = dedupeKey
		}
		return marshal(item)
	}
	// sent is the API's send of a targeted frame to session (comment_delivery.go and
	// message_delivery.go, through the listener's send route: contracts.AgentSendEnvelope of the
	// message Dispatch's client builds, dispatchenvoy.SendInput.EnvelopeMessage), with role as its
	// role mark when the send is for a role.
	sent := func(event model.Event, delivery map[string]any, key, role string) []byte {
		frame := encode(struct {
			Event    model.Event    `json:"event"`
			Delivery map[string]any `json:"delivery"`
		}{event, delivery})
		item := contracts.AgentSendEnvelope(session, key, contracts.Message{Source: "dispatch", Text: "approved, go ahead", Payload: &frame})
		item.Role = role
		return marshal(item)
	}
	comment := func(project string) map[string]any {
		return map[string]any{"id": "c-1", "body": project + "'s comment", "author": person}
	}
	mention := map[string]any{"attempt": 1, "mode": "steer", "comment_id": "c-1", "target": "session:" + session}
	targeted := map[string]any{"attempt": 1, "mode": "steer"}
	message := func(project string) map[string]any {
		return map[string]any{"id": "m-1", "body": project + "'s message", "target": "session:" + session}
	}
	mentionKey := contracts.AgentSendDedupeKey(session, model.CommentMentionIdempotencyKey("c-1", "session:"+session, "steer"))

	type row struct {
		name string
		data []byte
		want frameVerdict
	}
	var rows []row
	for _, c := range []struct {
		project string
		want    frameVerdict
	}{{"PUB", notHeld}, {"PRIV", framePasses()}} {
		p := c.project
		rows = append(rows,
			row{"owner route: " + p + "'s issue event", routed(issueEvent(p, "comment.created", comment(p)), own, ""), c.want},
			row{"claimant: " + p + "'s claimed issue's event", routed(issueEvent(p, "issue.updated", map[string]any{"status": "in_progress"}), own, ""), c.want},
			row{"primary document author: " + p + "'s document's comment", routed(documentEvent(p, "comment.created",
				map[string]any{"id": "c-2", "body": "a note", "project_key": p, "artifact_slug": "spec"}), own, ""), c.want},
			row{"author route: a reply in " + p + "'s thread", routed(issueEvent(p, "comment.created",
				map[string]any{"id": "c-3", "body": "a reply", "reply_to": "c-1", "in_reply_to": "c-1"}), own, ""), c.want},
			row{"follower: " + p + "'s ask answered", routed(issueEvent(p, "ask.answered",
				map[string]any{"id": "ask-1", "answer": "yes"}), own, ""), c.want},
			row{"previous claimant: " + p + "'s issue claimed by another", routed(issueEvent(p, "issue.claimed",
				map[string]any{"previous_claim": map[string]any{"kind": "session", "id": session}}), own, ""), c.want},
			row{"mention, the outbox's copy: " + p + "'s comment", routed(issueEvent(p, "comment.created", comment(p)), own, mentionKey), c.want},
			row{"role forward: " + p + "'s issue event on the token's role's lane", routed(issueEvent(p, "comment.created", comment(p)), privLane,
				contracts.RoleForwardDedupeKey(contracts.Envelope{DedupeKey: "dispatch-41"}, session)), c.want},
			row{"mention, the API's send: " + p + "'s comment", sent(apiEvent(p+"-1", "comment.created", comment(p)), mention, mentionKey, ""), c.want},
			row{"targeted message: " + p + "'s issue's message", sent(apiEvent(p+"-1", "message.created", message(p)), targeted, "m-1:steer", ""), c.want},
			row{"targeted message for a role, role-marked: " + p + "'s issue's message", sent(apiEvent(p+"-1", "message.created", message(p)), targeted, "m-1:steer", privRole), c.want},
			row{"the session's own topic and the token's role mark: " + p + "'s issue event", func() []byte {
				var item contracts.Envelope
				if err := json.Unmarshal(routed(issueEvent(p, "comment.created", comment(p)), own, ""), &item); err != nil {
					t.Fatal(err)
				}
				item.Role = privRole
				return marshal(item)
			}(), c.want},
		)
	}
	ownWith := func(payload string) []byte {
		return marshal(contracts.Envelope{EventID: "dispatch-44", Source: "dispatch", Topic: own, DedupeKey: "dispatch-44", Payload: payload})
	}
	rows = append(rows,
		row{"decision: a direct message on no issue, to an outside session", sent(apiEvent("", "message.created", message("no")), targeted, "m-1:steer", ""), unread},
		row{"control: the same message on the token's project's issue", sent(apiEvent("PRIV-1", "message.created", message("PRIV")), targeted, "m-1:steer", ""), framePasses()},
		row{"a Dispatch payload that names no project and no issue", ownWith(`{"type":"comment.created","payload":{"body":"hi"}}`), unread},
		row{"a Dispatch frame with no payload", ownWith(""), unread},
		row{"a Dispatch payload encoding/json does not read: text after it", ownWith(`{"project":"PRIV","issue_key":"PRIV-1"} x`), unread},
		row{"a Dispatch payload encoding/json does not read: a misspelt literal", ownWith(`{"project":"PRIV","issue_key":"PRIV-1","notify":tru}`), unread},
		row{"a Dispatch payload that is no JSON", ownWith(`PUB's comment`), unread},
		row{"a Dispatch payload whose project and issue key disagree", ownWith(`{"project":"PRIV","issue_key":"PUB-1"}`), unread},
		row{"a Dispatch payload whose issue key is no issue key", ownWith(`{"issue_key":"pub"}`), unread},
		row{"a targeted Dispatch payload with no event", ownWith(`{"delivery":{"attempt":1}}`), unread},
		row{"a Dispatch payload with a Project key beside project", ownWith(`{"project":"PRIV","Project":"PUB"}`), frameDrops(withheldAmbiguousKey)},
		row{"a Dispatch payload with a __proto__ key", ownWith(`{"project":"PRIV","__proto__":{"project":"PUB"}}`), frameDrops(withheldPrototypeKey)},
		row{"a frame with a Source key beside source", []byte(`{"source":"agent","Source":"dispatch","topic":"` + own + `"}`), frameDrops(withheldAmbiguousKey)},
		row{"control: a session's own message whose payload names another project", marshal(contracts.Envelope{
			EventID: "e-1", Source: "agent", Topic: own, DedupeKey: "agent.ses_v.k", Payload: `{"project":"PUB","issue_key":"PUB-1"}`}), framePasses()},
		row{"control: the token's project's document event on the document's topic", func() []byte {
			event := documentEvent("PRIV", "artifact.updated", map[string]any{"version": 2})
			return routed(event, "notifications.dispatch.document.PRIV.spec.artifact.updated", "")
		}(), framePasses()},
	)
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			verdict := edgeFrameVerdict(nil, session, projects, own, tc.data)
			if verdict != tc.want {
				t.Fatalf("edgeFrameVerdict(subject=%q, %s) = {action %d, reason %q}, want {action %d, reason %q}",
					own, tc.data, verdict.action, verdict.reason, tc.want.action, tc.want.reason)
			}
		})
	}

	// A listener that is no edge one runs no frame rule (sessionRelays.edgeWithholds): the direct
	// message on no issue reaches its session there.
	t.Run("control: a direct message on no issue, at a listener that is no edge one", func(t *testing.T) {
		msg := bus.SessionMessage{Subject: own, Data: sent(apiEvent("", "message.created", message("no")), targeted, "m-1:steer", "")}
		if verdict := (&sessionRelays{}).edgeWithholds(session, msg); verdict != framePasses() {
			t.Fatalf("edgeWithholds at a listener that is no edge one = {action %d, reason %q}, want a pass", verdict.action, verdict.reason)
		}
	})
}
