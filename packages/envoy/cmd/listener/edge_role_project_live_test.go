//go:build natslive

package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/id"
)

// otherProjectRole is a Legion role that works for INFRA (contracts.RoleProject), not for the
// edge's project.
const otherProjectRole = "legion-infra-reviewer"

// roleEnvelopeOn is a role message on topic, as a publisher sends one.
func roleEnvelopeOn(topic, summary string) contracts.Envelope {
	eventID := id.New()
	return contracts.Envelope{
		EventID:        eventID,
		Source:         "envoy",
		SourceEventID:  eventID,
		Topic:          topic,
		DedupeKey:      "edge-role-project-" + eventID,
		IssuedAt:       contracts.NowMillis(),
		PayloadSummary: summary,
		Payload:        `{"text":"` + summary + `"}`,
		TraceID:        id.New(),
	}
}

// claimAtShared claims role for sessionID through the shared listener's /v1/roles/set.
func claimAtShared(t *testing.T, shared *listenerProcess, sessionID, role string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, "http://127.0.0.1:"+strconv.Itoa(shared.port)+"/v1/roles/set",
		strings.NewReader(`{"session_id":"`+sessionID+`","role":"`+role+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+listenerTestToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
	if err != nil {
		t.Fatalf("claim %s: %v", role, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("claim %s for %s: status %d", role, sessionID, response.StatusCode)
	}
}

// The edge's grant publishes role subjects only in the form that names its own projects
// (notifications.role.PRIV.>), so that a role message it publishes is one of its projects' and
// never another project's. A role message the edge's own user publishes on notifications.role.PRIV.
// naming a Legion role that works for INFRA must therefore reach no holder of INFRA's role: the
// shared listener that takes it from the role lane may not resolve it to INFRA's holder. Control:
// the same role message on INFRA's own lane, published by Dispatch's user, reaches INFRA's holder,
// which shows the observer sees a delivery to that holder; and the edge's publish on INFRA's own
// form of the lane is refused by the server.
func TestAnEdgeRoleMessageReachesNoHolderOfAnotherProjectsRole(t *testing.T) {
	rig := newEdgeLiveRig(t, edgeLiveGrant(t))
	shared := rig.startShared(t)
	shared.waitHealthy(t)
	shared.WaitForOutput(t, `"msg":"role lane subscribed"`)

	const holder = "ses_infra_holder"
	frames, err := rig.connect(t, "observer").SubscribeSync(bus.DeliverSubject(holder))
	if err != nil {
		t.Fatal(err)
	}
	if err := registerConsumerSession(shared.port, holder); err != nil {
		t.Fatal(err)
	}
	claimAtShared(t, shared, holder, otherProjectRole)

	// Control: INFRA's own lane reaches INFRA's holder.
	if err := rig.dispatch.Publish(roleEnvelopeOn(contracts.RoleTopicPrefix+otherProjectRole, "on INFRA's own lane")); err != nil {
		t.Fatalf("publish on INFRA's lane: %v", err)
	}
	if envelope, ok := nextRelayFrame(t, frames, 20*time.Second); !ok || envelope.PayloadSummary != "on INFRA's own lane" {
		t.Fatalf("control: the role message on INFRA's own lane did not reach its holder (got %q, %v):\n%s", envelope.PayloadSummary, ok, shared.Output.String())
	}

	edge := rig.connect(t, "edge")
	// Control: the server refuses the edge INFRA's own form of the lane.
	refusedTopic := contracts.RoleTopicPrefix + "INFRA." + otherProjectRole
	data, err := json.Marshal(roleEnvelopeOn(refusedTopic, "on INFRA's form, from the edge"))
	if err != nil {
		t.Fatal(err)
	}
	if err := edge.Publish(refusedTopic, data); err != nil {
		t.Fatal(err)
	}
	// The defect's row: the edge's own project's form, naming INFRA's role.
	topic := contracts.RoleTopicPrefix + edgeProject + "." + otherProjectRole
	data, err = json.Marshal(roleEnvelopeOn(topic, "on the edge's project form, naming INFRA's role"))
	if err != nil {
		t.Fatal(err)
	}
	if err := edge.Publish(topic, data); err != nil {
		t.Fatal(err)
	}
	if err := edge.Flush(); err != nil {
		t.Fatal(err)
	}
	if envelope, ok := nextRelayFrame(t, frames, 15*time.Second); ok {
		t.Fatalf("the edge's role message on %s reached %s, the holder of INFRA's role %s: %q on %s", topic, holder, otherProjectRole, envelope.PayloadSummary, envelope.Topic)
	}
}
