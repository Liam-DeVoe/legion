package main

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/session"
	"github.com/sjawhar/envoy/internal/testnats"
)

// A handover of a role's kept lane one of whose probes of the main client reads a false "down" - a
// read a loaded box keeps waiting past the probe's budget, made here by the seam - hands the lane
// over all the same, and logs nothing at ERROR. The cases are each read that can be down:
//   - sweep-head: the keep sweep's read of the lane head, the sweep's first probe, gives the role's
//     guard back, waits for the connection with nothing held (bus.Reconnected, which reads
//     nats.go connected at once) and reads the head again under the guard (sweptLaneHead);
//   - sweep-handover: the handover the sweep started, whose first probe is the sweep's second,
//     does the same and runs the handover again (handOverSweptLane);
//   - drain: drainKeptRole's own handover, as before.
//
// The controls are each of the two sweep reads giving up on the loss rather than waiting and
// making it again: the head read passes the lane over, and the handover reports the loss as a
// failed handover at ERROR; in both the copy is not forwarded, and the case fails at the test's
// deadline.
func TestAHandoverWhoseProbeReadsAFalseDownHandsTheLaneOver(t *testing.T) {
	for _, tc := range []struct {
		name   string
		downAt int64
	}{{"sweep-head", 1}, {"sweep-handover", 2}, {"drain", 1}} {
		t.Run(tc.name, func(t *testing.T) {
			harness := newListenerDeliveryHarness(t)
			role := "kept-false-down-" + tc.name
			holder := "ses_kept_false_down_" + strings.ReplaceAll(tc.name, "-", "_")
			cfg := harness.config
			forwards := make(chan string, 16)
			cfg.forwardRole = inlineForward(func(_ string, item contracts.Envelope, _ string, _ time.Duration) error {
				forwards <- item.DedupeKey
				return nil
			})
			item := listenerTestEnvelope(contracts.RoleTopicPrefix+role, "kept-false-down-00")
			if _, kept := keepRoleMessage(cfg, role, item); !kept {
				t.Fatalf("keep the role's copy: %s", harness.logs.String())
			}
			claimRoleWith(t, harness, holder, role, session.SessionEntry{MachineID: "test-machine", SelfSubscribed: true})
			var probes atomic.Int64
			restore := harness.client.ProbeReadsDownForTest(func() bool { return probes.Add(1) == tc.downAt })
			defer restore()
			ctx, cancel := context.WithTimeout(context.Background(), testnats.Remaining(t))
			defer cancel()
			if tc.name != "drain" {
				sweepOnce(ctx, cfg)
			} else if err := drainKeptRole(ctx, cfg, role, time.Now); err != nil {
				t.Fatalf("the handover answered %v", err)
			}
			select {
			case key := <-forwards:
				if !strings.HasSuffix(key, item.DedupeKey) {
					t.Fatalf("the handover forwarded %q, want the kept copy %q", key, item.DedupeKey)
				}
			case <-time.After(testnats.Remaining(t)):
				t.Fatalf("the %s case forwarded nothing by the test's deadline after probe read %d read a false down (%d probe reads); log: %s", tc.name, tc.downAt, probes.Load(), harness.logs.String())
			}
			if probes.Load() <= tc.downAt {
				t.Fatalf("the probe was read %d times, want a read made again after the false down at read %d", probes.Load(), tc.downAt)
			}
			if logs := harness.logs.String(); strings.Contains(logs, `"level":"ERROR"`) || strings.Contains(logs, "level=ERROR") {
				t.Fatalf("the %s case logged at ERROR on a false down: %s", tc.name, logs)
			}
		})
	}
}
