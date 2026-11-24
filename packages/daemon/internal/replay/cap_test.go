package replay

import (
	"context"
	"testing"
	"time"
)

// A run's own cap (RunConfig.Cap) replaces the cap the record shows for the whole window: the
// question what the same window would have done with more slots. firstStartsWindow ran under a
// cap of 1, where ACME-2, handed to Legion at 09:01, waits for ACME-1's slot until ACME-1's
// accepting at 10:15. Replayed under main at the record's cap ACME-2's first slot is 10:15; at a
// cap of 2 the pass that records ACME-2 at 09:01 has a free slot and gives it, and run.json names
// the cap the run served under.
func TestARunsOwnCapReplacesTheRecordsCap(t *testing.T) {
	window := firstStartsWindow(t)
	for _, c := range []struct {
		cap   int
		first time.Time
	}{{0, clock(10, 15)}, {2, clock(9, 1)}} {
		out := t.TempDir()
		result, err := Run(context.Background(), RunConfig{Window: window, Rule: mainRule(t), Branch: MainBranch, Seed: 1, Slowdown: 1,
			PGDSN: replayDSN(t), Out: out, Cap: c.cap})
		if err != nil {
			t.Fatalf("replay the window at the cap %d: %v", c.cap, err)
		}
		if first, ok := result.Scorecard.FirstSlot["ACME-2"]; !ok || !first.Truncate(time.Minute).Equal(c.first) {
			t.Errorf("at the cap %d ACME-2's first slot is %s, want %s", c.cap, stamp(first), stamp(c.first))
		}
		var ran runRecord
		if err := readJSON(out+"/"+runFile, &ran); err != nil {
			t.Fatal(err)
		}
		if ran.Cap != c.cap {
			t.Errorf("run.json names the cap %d, want %d", ran.Cap, c.cap)
		}
	}
}
