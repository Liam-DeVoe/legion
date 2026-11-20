package daemon

// The bound a round's ask states (askRound), as the walk writes it. A project's pause moves a
// round's bound by the time it held the round's pod, and a hold that stands counts to the moment
// it is read (roundClock.roundBound, heldUntil). The walk can wait before it reaches an issue, on a
// write it makes for an issue before it (sweepHeldInAWrite), so the bound is read at the clock as
// the ask is written, never at the top of the sweep. The limit rig is podupdate_limits_test.go's,
// with no limit set.

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// Both rounds open at one sweep. A minute later a pause holds the second issue's pod, and the hold
// stands. A minute after that, the next sweep's walk first writes the ask of the first issue's
// round, which takes 20 seconds, and then writes the ask of the second: the bound it states counts
// the hold to that moment, 1m20s, not to the top of the sweep.
func TestPodUpdateAnAskStatesTheBoundAtTheMomentTheWalkWritesIt(t *testing.T) {
	asked, held := "ACME-1", "ACME-2"
	rig := newLimitRig(t, asked, held)
	rig.tick()
	rig.awaitRounds("the sweep that opened both rounds", asked, held)
	c := rig.claimOf(held, claim.RoleArchitect)
	if c.UpdateOpenedAt == nil {
		t.Fatalf("%s's claim records round %q with no opening", held, c.UpdateRound)
	}
	round, opened := c.UpdateRound, *c.UpdateOpenedAt

	rig.clock.advance(time.Minute)
	heldAt := rig.clock.Now()
	holdLegionPod(t, rig.sup.store, openLegionPause(t, rig.pool, heldAt), held, heldAt)

	rig.clock.advance(time.Minute)
	top := rig.clock.Now()
	rig.sweepHeldInAWrite(asked, 20*time.Second)
	written := top.Add(20 * time.Second)

	var texts []string
	for _, row := range rig.rows(record.OutboxKindUpdateAsk) {
		if row.Issue != held {
			continue
		}
		var ask record.UpdateAsk
		if err := json.Unmarshal(row.Payload, &ask); err != nil {
			t.Fatalf("decode the update ask %d: %v", row.ID, err)
		}
		texts = append(texts, ask.Text)
	}
	if len(texts) != 1 {
		t.Fatalf("the sweep wrote %d asks of %s's round, want 1", len(texts), held)
	}
	bound := opened.Add(podUpdateGrace + written.Sub(heldAt))
	if want := supervise.UpdateTask(round, limitDrift, bound); texts[0] != want {
		t.Errorf("the ask of %s's round states %q, want %q: the walk wrote it %s after the top of its sweep, with the pause's hold standing, so the bound is %s",
			held, texts[0], want, written.Sub(top), bound.Format(time.RFC3339))
	}
}
