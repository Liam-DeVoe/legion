package main

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/sjawhar/envoy/internal/contracts"
)

// A session that registers an issue's topic in the old form, the key alone, is answered with the
// topic stored as it wrote it and a warning naming the new form, its project first, and its
// consumer delivers that issue's events whichever form they are published in: the rename's
// readers take both forms before any producer moves. Its own agent subject, which the plugin adds
// to every registration itself, is named in no warning, and is delivered in either form too.
func TestARegistrationInTheOldFormIsToldTheNewFormAndReceivesEither(t *testing.T) {
	const sessionID = "ses_old_form"
	rig := newSessionConsumerRig(t)
	legacy := "notifications.dispatch.issue.INFRA-12.>"
	current := contracts.DispatchIssueSubject("INFRA", "INFRA-12", ">")
	body, err := json.Marshal(map[string]any{
		"session_id":      sessionID,
		"self_subscribed": true,
		"topics":          []string{legacy, contracts.LegacyAgentSubject(sessionID)},
		"delivery":        map[string]any{"consumer": true, "epoch": "e"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var answer interestSubscribeAnswer
	if err := json.Unmarshal([]byte(rig.post(t, "/v1/interests/subscribe", string(body))), &answer); err != nil {
		t.Fatalf("decode the registration's answer: %v", err)
	}
	if want := []string{legacy + " is the old form of " + current + "; register " + current}; !slices.Equal(answer.Warnings, want) {
		t.Fatalf("warnings = %q, want %q", answer.Warnings, want)
	}
	if !slices.Contains(answer.Topics, legacy) {
		t.Fatalf("answered topics = %q, want %s stored as the caller wrote it", answer.Topics, legacy)
	}
	want := []string{
		"notifications.agent.*." + sessionID,
		contracts.LegacyAgentSubject(sessionID),
		legacy,
		current,
	}
	slices.Sort(want)
	if got := rig.filters(t, sessionID); !slices.Equal(got, want) {
		t.Fatalf("consumer filters = %q, want %q", got, want)
	}

	rig.store(t, "notifications.dispatch.issue.INFRA-12.comment.created")
	rig.store(t, contracts.DispatchIssueSubject("INFRA", "INFRA-12", "comment.created"))
	rig.store(t, contracts.AgentSubject("INFRA", sessionID))
	rig.store(t, contracts.DispatchIssueSubject("INFRA", "INFRA-13", "comment.created"))
	rig.awaitPending(t, sessionID, 3)
}

// A registration naming only the new form, or topics the rename leaves alone, is named in no
// warning: the warning is for a caller still on the old form.
func TestARegistrationInTheNewFormIsNamedInNoWarning(t *testing.T) {
	const sessionID = "ses_new_form"
	rig := newSessionConsumerRig(t)
	body, err := json.Marshal(map[string]any{
		"session_id":      sessionID,
		"self_subscribed": true,
		"topics":          []string{contracts.DispatchIssueSubject("INFRA", "INFRA-12", ">"), "notifications.team.deploy-squad"},
		"delivery":        map[string]any{"consumer": true, "epoch": "e"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var answer interestSubscribeAnswer
	if err := json.Unmarshal([]byte(rig.post(t, "/v1/interests/subscribe", string(body))), &answer); err != nil {
		t.Fatalf("decode the registration's answer: %v", err)
	}
	if len(answer.Warnings) != 0 {
		t.Fatalf("warnings = %q, want none", answer.Warnings)
	}
}

// A role claimed in either form claims the one role its name is: the old form, the name alone, is
// answered with a warning naming the new form, project first, and the new form claims the same
// role with no warning. The claim record is keyed by the name either way, so GET /v1/roles/<name>
// reads the holder the new form's claim gave it.
func TestARoleClaimedInEitherFormClaimsItsName(t *testing.T) {
	const sessionID = "ses_role_forms"
	const role = "legion-infra-infra-12-architect"
	rig := newSessionConsumerRig(t)
	rig.register(t, sessionID, nil)

	var old struct {
		Topics   []string `json:"topics"`
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(rig.post(t, "/v1/roles/set", `{"session_id":"`+sessionID+`","role":"`+role+`"}`)), &old); err != nil {
		t.Fatalf("decode the old form's claim: %v", err)
	}
	if want := []string{role + " is the old form of INFRA." + role + "; claim INFRA." + role}; !slices.Equal(old.Warnings, want) {
		t.Fatalf("warnings = %q, want %q", old.Warnings, want)
	}
	if !slices.Contains(old.Topics, contracts.RoleTopicPrefix+role) {
		t.Fatalf("the claimant's topics = %q, want its role's lane as before", old.Topics)
	}

	const other = "ses_role_new_form"
	rig.register(t, other, nil)
	var current struct {
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(rig.post(t, "/v1/roles/set", `{"session_id":"`+other+`","role":"INFRA.`+role+`"}`)), &current); err != nil {
		t.Fatalf("decode the new form's claim: %v", err)
	}
	if len(current.Warnings) != 0 {
		t.Fatalf("the new form's claim was warned %q", current.Warnings)
	}
	holder, err := rig.harness.registry.RoleHolder(role)
	if err != nil || holder != other {
		t.Fatalf("holder of %s = %q, %v; want %s, whose claim named the new form", role, holder, err, other)
	}
}
