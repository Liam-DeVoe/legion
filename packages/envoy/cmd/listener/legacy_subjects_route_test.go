package main

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"testing"

	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/store"
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

// legacyKinds is a store.LegacySubjectCounter that records the kind label of each count.
type legacyKinds struct {
	mu    sync.Mutex
	kinds []string
}

func (c *legacyKinds) Inc(labels ...[2]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, label := range labels {
		if label[0] == "kind" {
			c.kinds = append(c.kinds, label[1])
		}
	}
}

func (c *legacyKinds) counted() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.kinds)
}

// A session's stored row read into its consumer's filter counts each topic the filter carries in
// the old form (kind="interest"), on every such read: a write of the stored row's filter and a
// repair of the session's consumer each count the issue topic the session registered in the old
// form. The session's own agent subject, which the subscribe route writes into the row itself, and
// its role's lane, which the stream does not carry and so no filter holds, are not counted.
func TestAStoredOldFormTopicIsCountedEachTimeTheRowIsReadIntoTheFilter(t *testing.T) {
	const sessionID = "ses_stored_old_form"
	rig := newSessionConsumerRig(t)
	rig.register(t, sessionID, map[string]any{"topics": []string{"notifications.dispatch.issue.INFRA-12.>"}})
	rig.post(t, "/v1/roles/set", `{"session_id":"`+sessionID+`","role":"lead-reviewer"}`)
	stored, err := rig.deps.registry.Get(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("read the stored row: %v", err)
	}
	for _, topic := range []string{contracts.LegacyAgentSubject(sessionID), contracts.RoleTopicPrefix + "lead-reviewer"} {
		if !slices.Contains(stored.Topics, topic) {
			t.Fatalf("stored topics = %q, want %s among them, which this test shows is not counted", stored.Topics, topic)
		}
	}

	counter := &legacyKinds{}
	counting, err := store.Open(rig.harness.client.Conn, store.WithReplicas(1), store.WithLegacySubjectCounter(counter))
	if err != nil {
		t.Fatalf("open a counting registry: %v", err)
	}
	rig.deps.consumers.storedFilterWrite(context.Background(), counting, sessionID, "the test's write")
	if got := counter.counted(); !slices.Equal(got, []string{"interest"}) {
		t.Fatalf("a write of the stored row's filter counted %q, want one interest", got)
	}
	deps := *rig.deps
	deps.registry = counting
	if _, finding := rig.deps.consumers.registeredTarget(context.Background(), &deps, sessionID, 0); finding != registeredTargetBuilt {
		t.Fatalf("the repair's target = %v, want it built", finding)
	}
	if got := counter.counted(); !slices.Equal(got, []string{"interest", "interest"}) {
		t.Fatalf("after a repair read the row too, counted %q, want two interests", got)
	}
}
