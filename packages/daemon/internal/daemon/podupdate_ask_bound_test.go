package daemon

// The bound a round's ask states (askRound), as the walk writes it. A project's pause moves a
// round's bound by the time it held the round's pod: a hold that stands counts to the moment it is
// read, and one that ended counts to its end (roundClock.roundBound, heldUntil). The walk can wait
// before it reaches an issue, on a write it makes for an issue before it (sweepHeldInAWrite), and a
// hold can stand or end meanwhile, so the ask reads the clock and then the holds of its pod as it
// is written, never the sweep's top. The limit rig is podupdate_limits_test.go's, with no limit
// set.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/testwait"
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
	rig.pauseHolds(held, heldAt)

	rig.clock.advance(time.Minute)
	top := rig.clock.Now()
	rig.sweepHeldInAWrite(asked, 20*time.Second)
	written := top.Add(20 * time.Second)

	texts := rig.askTexts(held)
	if len(texts) != 1 {
		t.Fatalf("the sweep wrote %d asks of %s's round, want 1", len(texts), held)
	}
	bound := opened.Add(podUpdateGrace + written.Sub(heldAt))
	if want := supervise.UpdateTask(round, limitDrift, bound); texts[0] != want {
		t.Errorf("the ask of %s's round states %q, want %q: the walk wrote it %s after the top of its sweep, with the pause's hold standing, so the bound is %s",
			held, texts[0], want, written.Sub(top), bound.Format(time.RFC3339))
	}
}

// The case above, but the pause's hold of the second issue's pod ends, as a role's ready ends it,
// 5 seconds into the walk's 20-second write for the first issue. The sweep read the hold at its
// top, when it stood. The round now ends at its opening, the grace and the 1m5s the hold kept the
// pod, and that is where the next sweep past it replaces the pod with its disk: the ask states
// that end. Counted over the hold the sweep read at its top, the hold would run to the moment the
// ask is written, and the ask would tell the roles an end 15 seconds after the pod and the work
// they had not pushed are gone.
func TestPodUpdateAnAskStatesNoEndLaterThanTheRoundsWhenAHoldEndsInTheWalk(t *testing.T) {
	asked, held := "ACME-1", "ACME-2"
	rig := newLimitRig(t, asked, held)
	rig.tick()
	rig.awaitRounds("the sweep that opened both rounds", asked, held)
	c := rig.claimOf(held, claim.RoleArchitect)
	if c.UpdateOpenedAt == nil {
		t.Fatalf("%s's claim records round %q with no opening", held, c.UpdateRound)
	}
	round, opened := c.UpdateRound, *c.UpdateOpenedAt
	pod := strings.TrimPrefix(round, supervise.PodRoundPrefix)

	rig.clock.advance(time.Minute)
	heldAt := rig.clock.Now()
	rig.pauseHolds(held, heldAt)

	rig.clock.advance(time.Minute)
	top := rig.clock.Now()
	ended := top.Add(5 * time.Second)
	if !rig.sweepWaitingInAWrite(asked, func() {
		rig.clock.advance(5 * time.Second)
		if done, err := rig.sup.store.EndPausePodHold(rig.ctx, rig.s.cfg.Project, held, store.PausePodHoldReady, ended); err != nil || !done {
			t.Errorf("end the pause's hold of %s's pod at %s = %t, %v; want it ended", held, ended.Format(time.RFC3339), done, err)
		}
		rig.clock.advance(15 * time.Second)
	}) {
		t.Fatalf("the sweep made no write for %s", asked)
	}

	texts := rig.askTexts(held)
	if len(texts) != 1 {
		t.Fatalf("the sweep wrote %d asks of %s's round, want 1", len(texts), held)
	}
	bound := opened.Add(podUpdateGrace + ended.Sub(heldAt))
	if want := supervise.UpdateTask(round, limitDrift, bound); texts[0] != want {
		t.Errorf("the ask of %s's round states %q, want %q: the pause's hold of its pod ended %s after the top of the sweep, before the ask, so the round ends at %s",
			held, texts[0], want, ended.Sub(top), bound.Format(time.RFC3339))
	}

	// The first sweep at or past that end replaces the pod: the end the ask stated is that sweep's.
	rig.clock.advance(bound.Sub(rig.clock.Now()))
	rig.tick()
	testwait.Eventually(t, "the round's pod to be replaced at the end its ask stated", func() bool {
		return len(rig.replacementsOf(pod)) > 0
	})
}

// pauseHolds opens a pause of the rig's own project at at, as an operator's pause does, and its
// hold of issue's pod (openProjectPause, holdProjectPod): the pair a daemon reads its holds by.
func (r *fleetRig) pauseHolds(issue string, at time.Time) {
	r.t.Helper()
	project := r.s.cfg.Project
	holdProjectPod(r.t, r.sup.store, openProjectPause(r.t, r.pool, project, at), project, issue, at)
}

// askTexts is the text of every update ask the outbox holds for issue, oldest first.
func (r *fleetRig) askTexts(issue string) []string {
	r.t.Helper()
	var texts []string
	for _, row := range r.rows(record.OutboxKindUpdateAsk) {
		if row.Issue != issue {
			continue
		}
		var ask record.UpdateAsk
		if err := json.Unmarshal(row.Payload, &ask); err != nil {
			r.t.Fatalf("decode the update ask %d: %v", row.ID, err)
		}
		texts = append(texts, ask.Text)
	}
	return texts
}
