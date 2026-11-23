package embedqueue

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/embed"
	"github.com/sjawhar/envoy/internal/dispatch/retry"
)

// A call the shared pace holds at its slowest, retry.MaxDelay (5 minutes, past claimWindow's 2),
// keeps its rows claimed through the whole wait: the claim is renewed during the wait, so it is
// never older than claimWindow, and another process's scan cannot take the rows mid-wait and embed
// them again. A canary that meets an outage drives the pace there (confirmSoloFailure). The row
// runs on a synctest fake clock, where the wait takes no real time and no Postgres statement may
// run, so each renewal is recorded (claimRenewal.extend) rather than written. Renewed only before
// the call, the claim was 5 minutes old when the call went out.
func TestAClaimIsRenewedThroughThePacesWaitAtItsSlowest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		inner := &fakeEmbedder{}
		paced := embed.NewRateLimitedEmbedder(inner)
		deps := Deps{Embedder: paced}
		ctx := context.Background()
		// The first call takes the pace's one token at once; then failing canaries back the pace
		// off to its slowest, and the next call waits that whole interval.
		if _, err := deps.embedDocuments(ctx, []string{"first"}, nil); err != nil {
			t.Fatalf("first call: %v", err)
		}
		for paced.Interval() < retry.MaxDelay {
			paced.BackOff()
		}
		renewal := newClaimRenewal(deps, []pendingRow{{kind: "issue", id: "pace", text: "second"}})
		claimed := []time.Time{time.Now()}
		renewal.extend = func(context.Context, []pendingRow) error {
			claimed = append(claimed, time.Now())
			return nil
		}
		started := time.Now()
		if _, err := deps.embedDocuments(ctx, []string{"second"}, renewal); err != nil {
			t.Fatalf("second call: %v", err)
		}
		called := time.Now()
		if waited := called.Sub(started); waited != retry.MaxDelay || len(inner.calls) != 2 {
			t.Fatalf("the second call went out after %v, call %d; want after the pace's slowest, %v, call 2", waited, len(inner.calls), retry.MaxDelay)
		}
		for i, until := range append(claimed[1:], called) {
			if age := until.Sub(claimed[i]); age >= claimWindow {
				t.Errorf("the claim made at +%v was %v old at +%v, not less than claimWindow (%v): another scan could take its rows; renewals at %v",
					claimed[i].Sub(started), age, until.Sub(started), claimWindow, offsets(claimed, started))
			}
		}
	})
}

// offsets is each of times as its distance from start.
func offsets(times []time.Time, start time.Time) []time.Duration {
	distances := make([]time.Duration, len(times))
	for i, at := range times {
		distances[i] = at.Sub(start)
	}
	return distances
}
