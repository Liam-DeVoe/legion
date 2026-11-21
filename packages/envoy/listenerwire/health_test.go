package listenerwire_test

import (
	"encoding/json"
	"testing"

	"github.com/sjawhar/envoy/listenerwire"
)

// GET /healthz's answer was a map, whose keys encoding/json writes sorted, and Health declares its
// fields, the embedded groups' among them, in that order: an answer that carries every field writes
// the same bytes as the map of those fields, and a group set at its zero values still writes each
// of its keys. The issuer fields are on every answer, the starting one too, null when not set.
func TestTheHealthAnswerWritesItsKeysInTheOrderTheMapDid(t *testing.T) {
	failing, completedAt, age, read, unread := int64(0), "2026-11-19T00:00:00Z", 0.5, true, false
	full := listenerwire.Health{
		ActiveInstance:   "pod-a",
		CICache:          "not_applicable",
		Error:            "durable consumer unavailable: refused",
		Google:           &unread,
		Lease:            "unavailable: missing NATS grant for KV bucket envoy_listener",
		DurableLag:       &listenerwire.DurableLag{},
		OIDC:             &read,
		Receive:          "preparing the durable",
		ReceiveFailingMS: &failing,
		Role:             listenerwire.RoleStandby,
		RoleClaimRepair:  &listenerwire.RoleClaimRepair{CompletedAt: &completedAt, AgeSeconds: &age, BoundSeconds: 120},
		SessionCache:     &listenerwire.SessionCache{},
		Status:           listenerwire.HealthDegraded,
	}
	got, err := json.Marshal(full)
	if err != nil {
		t.Fatalf("encode the answer: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(got, &fields); err != nil {
		t.Fatalf("decode the answer %s: %v", got, err)
	}
	if len(fields) != 17 {
		t.Fatalf("the answer %s carries %d keys, want all 17", got, len(fields))
	}
	want, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("encode the map: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("the answer is\n%s\nwhere the map of its fields is\n%s", got, want)
	}
	if starting, _ := json.Marshal(listenerwire.Health{Status: listenerwire.HealthStarting}); string(starting) != `{"google":null,"oidc":null,"status":"starting"}` {
		t.Fatalf("the starting answer is %s, want {\"google\":null,\"oidc\":null,\"status\":\"starting\"}", starting)
	}
}
