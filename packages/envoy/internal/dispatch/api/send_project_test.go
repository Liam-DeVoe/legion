package api

import (
	"fmt"
	"testing"

	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// TestEverySendNamesItsProjectAndTheQueuedCopyTakesIt (listenerSendInput, messageProject,
// commentProject, queuedEnvelope): every Dispatch send names its project to the listener - a
// comment mention its comment's, a targeted message its issue's, and a message of no issue
// contracts.NoProject, named rather than left out - and the copy Dispatch queues for a session the
// listener does not know is built from that same project, so the listener's body project decides
// both copies' subject: notifications.agent.PUB.s1 for PUB-3's message, and the subject of no
// project for a message of none.
//
// Control, in the test: the same message's queued copy built from an input that names no project,
// as a send before this rule was, goes out on the subject of no project, and the check fails
// naming it.
func TestEverySendNamesItsProjectAndTheQueuedCopyTakesIt(t *testing.T) {
	issue := "PUB-3"
	rows := []struct {
		name, project, subject string
	}{
		{name: "PUB-3's message", project: messageProject(model.Message{ID: "m1", IssueKey: &issue}), subject: contracts.AgentSubject("PUB", "s1")},
		{name: "a message of no issue", project: messageProject(model.Message{ID: "m2"}), subject: contracts.LegacyAgentSubject("s1")},
		{name: "a PRIV comment's mention", project: commentProject(model.Event{Project: "PRIV"}), subject: contracts.AgentSubject("PRIV", "s1")},
		{name: "a comment whose event names no project", project: commentProject(model.Event{}), subject: contracts.LegacyAgentSubject("s1")},
	}
	misread := func(project string, want string) string {
		input := listenerSendInput("s1", "steer", project, "body", "m1:steer", nil, contracts.ExpectsReplyOptional, []byte(`{}`), false)
		if input.Project == "" {
			return fmt.Sprintf("the send names no project (want %q)", project)
		}
		if input.Project != project {
			return fmt.Sprintf("the send names %q, want %q", input.Project, project)
		}
		if got := queuedEnvelope(input).Topic; got != want {
			return fmt.Sprintf("the queued copy goes out on %q, want %q", got, want)
		}
		return ""
	}
	for _, row := range rows {
		if wrong := misread(row.project, row.subject); wrong != "" {
			t.Errorf("%s (project %q): %s", row.name, row.project, wrong)
		}
	}

	input := listenerSendInput("s1", "steer", "", "body", "m1:steer", nil, contracts.ExpectsReplyOptional, []byte(`{}`), false)
	if got := queuedEnvelope(input).Topic; got == contracts.AgentSubject("PUB", "s1") {
		t.Fatalf("control: a queued copy built from an input of no project goes out on %q, want the subject of no project, which PUB-3's row would name", got)
	}
}
