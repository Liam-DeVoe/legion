package main

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/legionagents"
	"github.com/sjawhar/envoy/internal/logging"
)

// finishedLegionRole is the role token of a Legion phase worker whose issue has left the
// workflow, so no next holder of that role is coming.
const finishedLegionRole = "legion-infra-infra-205-reviewer"

// finishedRoleSession is the stopped reviewer the registry holds for finishedLegionRole.
const finishedRoleSession = "ses-old-reviewer"

// withFinishedHandoff is withAuditHandoff's opposite number: the registry holds one stopped agent
// of finishedLegionRole whose entry is finished, and the project answers for it.
func withFinishedHandoff(t *testing.T, d *listenerDeps) {
	t.Helper()
	agents, err := legionagents.Open(t.Context(), d.client.Conn, legionagents.WithReplicas(1))
	if err != nil {
		t.Fatalf("open the legion agent registry: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := agents.Put(ctx, legionagents.Entry{
		SessionID: finishedRoleSession, LegionProject: "infra", Issue: "INFRA-205", Role: "reviewer",
		RoleToken: finishedLegionRole, Title: "Legion reviewer · INFRA-205", Harness: "omp",
		StartedAt: "2026-10-26T03:00:00Z", Finished: true,
	}); err != nil {
		t.Fatalf("record the stopped reviewer of a finished issue: %v", err)
	}
	if _, err := agents.PutProject(ctx, "infra", legionagents.Project{
		Answering: true, UpdatedAt: time.Now().UTC().Format("2006-01-02T15:04:05Z"),
	}); err != nil {
		t.Fatalf("state that the project answers: %v", err)
	}
	wrapped := newLegionAgentRegistry(agents)
	d.legionAgents = wrapped
	d.handoff = &legionHandoff{client: d.client, registry: d.registry, sessions: d.sessions, agents: wrapped, logger: logging.New("finished-role")}
	d.delivery.handoff = d.handoff
}

// handedPublishAnswer is a publish answered with a hand-off: 200, nothing kept, the stopped agent
// the message was handed to as recipient, and what the answer names of the topic's old form.
type handedPublishAnswer struct {
	contracts.Envelope
	Kept       bool     `json:"kept"`
	KeptReason string   `json:"kept_reason"`
	Recipient  string   `json:"recipient"`
	Warnings   []string `json:"warnings"`
}

// A publish to a Legion role whose issue has left the workflow - closed, parked, or its tree
// retired - is handed to the agent's daemon and answered 200 with that agent as recipient, and
// nothing is kept: no next holder of the role is coming, so a copy kept on the role's lane would
// wait for nobody, while the daemon answers this one now from the stopped agent's conversation.
// The publish names the role in its old form, the name alone, and the hand-off's 200 names the
// new form in its warnings as a delivery's 200 and a keep's 202 do (store.Registry.ReadRole): the
// publisher is one the old form's refusal would stop, whoever answers it.
func TestAPublishToAFinishedLegionRoleIsHandedToItsDaemonAndNotKept(t *testing.T) {
	d, client := keptRoleDeps(t)
	withFinishedHandoff(t, d)

	recorder, _ := publishToRole(t, d, finishedLegionRole,
		`{"topic":"`+contracts.RoleTopicPrefix+finishedLegionRole+`","source":"agent","source_session":"ses-architect","message":"what did the review find?"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("publish to a finished Legion role: status %d, body %s; want 200 delivered", recorder.Code, recorder.Body.String())
	}
	var answer handedPublishAnswer
	if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
		t.Fatalf("decode the publish answer: %v", err)
	}
	if answer.Kept || answer.KeptReason != "" || answer.Recipient != finishedRoleSession {
		t.Fatalf("the answer is %+v, want it delivered to %s with nothing kept", answer, finishedRoleSession)
	}
	current := "INFRA." + finishedLegionRole
	if want := []string{finishedLegionRole + " is the old form of " + current + "; name " + current}; !slices.Equal(answer.Warnings, want) {
		t.Fatalf("the hand-off's warnings are %q, want %q", answer.Warnings, want)
	}
	assertNothingKept(t, client, finishedLegionRole)

	count, data := handoffs(t, client, finishedRoleSession)
	if count != 1 {
		t.Fatalf("the stream holds %d hand-offs for %s, want the one this publish made", count, finishedRoleSession)
	}
	wire := decodeHandoff(t, data)
	if wire.TargetSession != finishedRoleSession || wire.RoleToken != finishedLegionRole ||
		wire.OriginalTopic != contracts.RoleTopicPrefix+finishedLegionRole {
		t.Fatalf("the stored hand-off is %+v, want the publish's topic handed to the finished role's newest session", wire)
	}
}

// An entry the daemon wrote with no `finished`, the shape of an issue still in the workflow and of
// one written before the daemon stated any standing, is handed to its daemon at once, as it is once
// the daemon writes it finished: `finished` decides nothing of
// a role's hand-off, and a role with no live holder whose newest entry is not released, under a
// project that answers, is answered by that entry's daemon. Each publish is answered 200 with that
// session as recipient and nothing is kept.
//
// The control is the same entry written released, with no `finished`: the daemon resumes that
// session on the role's next wake, so the publish is kept for it, 202 kept unclaimed, and nothing
// more is handed off. Before D2 the first publish was kept too.
func TestARoleEntryIsHandedOffWhetherOrNotTheDaemonWroteItFinished(t *testing.T) {
	d, client := keptRoleDeps(t)
	withFinishedHandoff(t, d)
	agents := d.legionAgents.store()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	running := legionagents.Entry{
		SessionID: finishedRoleSession, LegionProject: "infra", Issue: "INFRA-205", Role: "reviewer",
		RoleToken: finishedLegionRole, Title: "Legion reviewer · INFRA-205", Harness: "omp",
		StartedAt: "2026-10-26T03:00:00Z",
	}
	if _, err := agents.Put(ctx, running); err != nil {
		t.Fatalf("write the entry again while its issue runs: %v", err)
	}
	handedOff := func(when, message string, count uint64) {
		t.Helper()
		recorder, _ := publishToRole(t, d, finishedLegionRole,
			`{"topic":"`+contracts.RoleTopicPrefix+finishedLegionRole+`","source":"agent","source_session":"ses-architect","message":"`+message+`"}`)
		var answer handedPublishAnswer
		if recorder.Code != http.StatusOK || json.Unmarshal(recorder.Body.Bytes(), &answer) != nil || answer.Kept || answer.Recipient != finishedRoleSession {
			t.Fatalf("publish %s: status %d, body %s; want 200 handed off to %s with nothing kept", when, recorder.Code, recorder.Body.String(), finishedRoleSession)
		}
		if got, _ := handoffs(t, client, finishedRoleSession); got != count {
			t.Fatalf("publish %s: the stream holds %d hand-offs for %s, want %d", when, got, finishedRoleSession, count)
		}
	}

	handedOff("while the role's issue runs", "the next round starts at abc123", 1)
	assertNothingKept(t, client, finishedLegionRole)

	finished := running
	finished.Finished = true
	if _, err := agents.Put(ctx, finished); err != nil {
		t.Fatalf("write the entry again once its issue finished: %v", err)
	}
	handedOff("once the entry is finished", "what did the review find?", 2)
	assertNothingKept(t, client, finishedLegionRole)

	released := running
	released.Released = true
	if _, err := agents.Put(ctx, released); err != nil {
		t.Fatalf("write the entry again released: %v", err)
	}
	recorder, answer := publishToRole(t, d, finishedLegionRole,
		`{"topic":"`+contracts.RoleTopicPrefix+finishedLegionRole+`","source":"agent","source_session":"ses-architect","message":"are you back?"}`)
	if recorder.Code != http.StatusAccepted || !answer.Kept || answer.KeptReason != "unclaimed" {
		t.Fatalf("control: publish once the entry is released: status %d, kept %t, reason %q, body %s; want 202 kept unclaimed for the resumed worker",
			recorder.Code, answer.Kept, answer.KeptReason, recorder.Body.String())
	}
	waitForKeptRoleMessage(t, client, finishedLegionRole, answer.EventID)
	if count, _ := handoffs(t, client, finishedRoleSession); count != 2 {
		t.Fatalf("control: the stream holds %d hand-offs for %s, want only the two before its release", count, finishedRoleSession)
	}
}
