package main

// edge_frame_verdict_test.go is the table test of edgeFrameVerdict (edge_frames.go), the one rule
// the relay and the relay socket both judge a frame by, with a row for each of its branches and a
// passing row beside each withholding one, so no branch can be deleted without a row failing. It
// calls the rule directly, with no NATS server, so it covers the branches the live frame-rule test
// (edge_frame_rule_test.go) cannot store a frame to reach: a GitHub relay header whose envelope
// names a topic that is no GitHub event's, the wildcard own subject, and the role mark a forged
// claim or a kept or taken copy carries (F1, F2).

import (
	"encoding/json"
	"testing"

	"github.com/sjawhar/envoy/internal/contracts"
)

func TestEdgeFrameVerdict(t *testing.T) {
	const session = "ses_v"
	own := contracts.LegacyAgentSubject(session)
	ownNew := contracts.AgentSubject("PRIV", session)
	otherAgent := contracts.LegacyAgentSubject("ses_other")
	previous := contracts.LegacyAgentSubject("ses_prev")
	const privRole, pubRole = "legion-priv-priv-1-tester", "legion-pub-pub-1-tester"
	const ownIssue = "notifications.dispatch.issue.PRIV.PRIV-1.comment.created"
	const otherIssue = "notifications.dispatch.issue.PUB.PUB-1.comment.created"
	const github = "notifications.github.acme.pub.pr.1"

	frame := func(topic, role string) []byte {
		fields := map[string]string{}
		if topic != "" {
			fields["topic"] = topic
		}
		if role != "" {
			fields["role"] = role
		}
		data, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}

	projects := []string{"PRIV"}
	for _, tc := range []struct {
		name         string
		subject      string
		topic        string
		role         string
		wantWithhold bool
		wantReason   string
	}{
		{"a send on the session's own subject passes", own, own, "", false, ""},
		{"a send on the session's own new-form subject passes", ownNew, ownNew, "", false, ""},
		{"a forged other-project role-marked send on the own subject is withheld", own, own, pubRole, true, withheldProjectNotHeld},
		{"the session's own project's role-marked send kept on the previous holder's subject passes", own, previous, privRole, false, ""},
		{"the session's own project's role-lane forward passes", own, contracts.RoleTopicPrefix + privRole, "", false, ""},
		{"an other-project role-lane forward is withheld", own, contracts.RoleTopicPrefix + pubRole, "", true, withheldProjectNotHeld},
		{"a frame on another session's agent subject is withheld", otherAgent, otherAgent, "", true, withheldProjectNotHeld},
		{"a frame on the own subject with no envelope topic is withheld", own, "", "", true, withheldProjectNotHeld},
		{"a non-role forward of a GitHub event on the own subject is withheld", own, github, "", true, withheldProjectNotHeld},
		{"a non-role forward of another agent subject on the own subject is withheld", own, otherAgent, "", true, withheldProjectNotHeld},
		{"the wildcard own subject is no one's and is withheld", contracts.AgentTopicPrefix + "*." + session, contracts.AgentTopicPrefix + "*." + session, "", true, withheldProjectNotHeld},
		{"a GitHub header whose envelope names a topic that is no GitHub event's is withheld", github, ownIssue, "", true, withheldProjectNotHeld},
		{"a GitHub header with no reader stands answer_unknown", github, "", "", true, withheldAnswerUnknown},
		{"a topic of the token's project passes", ownIssue, ownIssue, "", false, ""},
		{"a topic of a project the token does not list is withheld", otherIssue, otherIssue, "", true, withheldProjectNotHeld},
		{"a relay header of the token's project whose envelope names another project is withheld", ownIssue, otherIssue, "", true, withheldProjectNotHeld},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withhold, reason := edgeFrameVerdict(nil, session, projects, tc.subject, frame(tc.topic, tc.role))
			if withhold != tc.wantWithhold || reason != tc.wantReason {
				t.Fatalf("edgeFrameVerdict(subject=%q, topic=%q, role=%q) = (%t, %q), want (%t, %q)",
					tc.subject, tc.topic, tc.role, withhold, reason, tc.wantWithhold, tc.wantReason)
			}
		})
	}
}
