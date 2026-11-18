// What the role publish route answers a caller's repeat under the same idempotency_key: the
// stream keeps the first forward on the holder's agent subject and stores no second one, so the
// 200 is that stored forward, with duplicate and without the forward mark on its dedupe key, and
// never the new envelope the route built for the repeat.

package main

import (
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/id"
)

// TestARolePublishRepeatedUnderOneIdempotencyKeyAnswersTheStoredCopy is check
// c-role-publish-repeat-stored-copy. A session publishes one message to a role whose holder is
// live under idempotency_key K, then publishes it again under K. The holder's agent subject holds
// one forward, and the repeat's 200 carries duplicate and that forward's envelope, its dedupe key
// without roleForwardDedupePrefix, both where the repeat is forwarded again (the route's confirm
// record cleared, as another listener has none) and the stream answers it a duplicate, and where
// the route's confirm record answers it without a forward. A key that is no UUID (retry-1) is a
// repeat to the role route too, since the forward's Nats-Msg-Id is made of the key whatever its
// form (bus.ForwardMsgID).
//
// The control is the same repeat to a role nobody claims: its 202 kept answer is the copy the
// role's kept lane holds, which the route already answered before this change (roleSend.kept).
func TestARolePublishRepeatedUnderOneIdempotencyKeyAnswersTheStoredCopy(t *testing.T) {
	rig := newSenderLimitRig(t)
	const sender = "ses_role_repeat_sender"

	// stored is the forward the holder's agent subject holds, as the route answers it.
	stored := func(t *testing.T, holder string) contracts.Envelope {
		t.Helper()
		forward := heldCopy(t, rig.harness.client, contracts.LegacyAgentSubject(holder))
		if !strings.HasPrefix(forward.DedupeKey, roleForwardDedupePrefix) {
			t.Fatalf("the forward stored for %s has dedupe key %q, want it under %s", holder, forward.DedupeKey, roleForwardDedupePrefix)
		}
		forward.DedupeKey = strings.TrimPrefix(forward.DedupeKey, roleForwardDedupePrefix)
		return forward
	}

	for _, row := range []struct {
		name    string
		key     string
		forward bool
	}{
		{"case: a repeat forwarded again answers the stored forward with duplicate", uuid.NewString(), true},
		{"case: a repeat the route's confirm record answers gives the stored forward with duplicate", uuid.NewString(), false},
		{"case: a repeat under a key of another form, forwarded again, answers the stored forward with duplicate", "retry-1", true},
	} {
		t.Run(row.name, func(t *testing.T) {
			role := "role-repeat-" + id.New()
			holder := "ses_role_repeat_holder_" + id.New()
			claimRoleFor(t, rig.deps.registry, rig.deps.sessions, holder, role, nil)
			key := map[string]any{"idempotency_key": row.key}
			first := held(t, rig.publishFrom(t, sender, contracts.RoleTopicPrefix+role, key))
			first.requireHeld(t, "the first role publish", stored(t, holder), false)
			if row.forward {
				rig.deps.confirmed.Clear(publishDedupePrefix+row.key, holder)
			}
			repeat := held(t, rig.publishFrom(t, sender, contracts.RoleTopicPrefix+role, key))
			repeat.requireHeld(t, "the repeated role publish", stored(t, holder), true)
			if repeat.Holder != holder {
				t.Fatalf("the repeated role publish named holder %q, want %s; body %s", repeat.Holder, holder, repeat.raw)
			}
		})
	}

	t.Run("control: a repeat to a role nobody claims answers 202 with the kept copy", func(t *testing.T) {
		role := "role-repeat-unclaimed-" + id.New()
		key := map[string]any{"idempotency_key": uuid.NewString()}
		first := held(t, rig.publishFrom(t, sender, contracts.RoleTopicPrefix+role, key))
		first.requireStatus(t, "the first publish to the unclaimed role", http.StatusAccepted)
		repeat := held(t, rig.publishFrom(t, sender, contracts.RoleTopicPrefix+role, key))
		repeat.requireStatus(t, "the repeated publish to the unclaimed role", http.StatusAccepted)
		kept := heldCopy(t, rig.harness.client, bus.KeptRoleSubject(role))
		if repeat.envelope.EventID != first.envelope.EventID || repeat.envelope.EventID != kept.EventID {
			t.Fatalf("the repeat's 202 names event %s, want the kept copy's %s (the first answer named %s)",
				repeat.envelope.EventID, kept.EventID, first.envelope.EventID)
		}
	})
}
