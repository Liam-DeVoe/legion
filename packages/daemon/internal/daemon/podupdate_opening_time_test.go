package daemon

// When a pod round opens, as pod_update_rounds_per_minute weighs it: each sweep opens rounds on at
// most that many issues, and opens none 50 seconds or less after this daemon's last opening. The
// walk over the issues can wait before it reaches the issue it opens, on a write it makes to the
// store for an issue before that one, so each opening is dated when the sweep makes it
// (recordPodUpdateOpening) and the gap is read at the opening (podUpdateSweep.holds), never at the
// top of the sweep.
//
// The wait these tests hold the walk in is the one write every roll makes: the ask of a round a
// sweep before opened, which the walk enqueues as it reaches that round's issue (askRound). The
// limit rig is podupdate_limits_test.go's.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/api"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// openSlowRound opens slow's round at a sweep of its own, before the test sets a limit, with
// every other issue named undrifted at that sweep and drifted again after it. The round is this
// daemon's last opening, dated at the clock this leaves; its claims record it and no ask of it is
// queued yet, so the next sweep's walk enqueues that ask as it reaches slow (askRound), which is
// the write sweepHeldInAWrite holds.
func (r *fleetRig) openSlowRound(slow string, others ...string) {
	r.t.Helper()
	for _, issue := range others {
		r.drifts(issue)
	}
	r.tick()
	r.awaitRounds("the sweep that opened "+slow+"'s round", slow)
	for _, issue := range others {
		r.drifts(issue, limitDrift)
	}
}

// sweepHeldInAWrite is one sweep whose first write to the store for slow — the ask of slow's open
// round (openSlowRound) — takes hold, as a write the store is slow to take does: the clock moves
// by hold before the row is written. Every issue the walk reaches after slow is decided hold after
// every issue before it.
func (r *fleetRig) sweepHeldInAWrite(slow string, hold time.Duration) {
	r.t.Helper()
	enqueue := r.s.enqueue
	defer func() { r.s.enqueue = enqueue }()
	held := false
	r.s.enqueue = func(ctx context.Context, issue string, payloads ...record.OutboxPayload) error {
		if issue == slow && !held {
			held = true
			r.clock.advance(hold)
		}
		return enqueue(ctx, issue, payloads...)
	}
	began := r.clock.Now()
	r.tick()
	if got := r.clock.Now().Sub(began); got != hold {
		r.t.Fatalf("the sweep's write for %s held it for %s, want %s", slow, got, hold)
	}
}

// An opening is dated from the moment the sweep makes it, not from the top of the sweep. The walk
// can wait before it reaches the issue it opens, on a write to the store for an issue before it
// (sweepHeldInAWrite). Dated from the top of such a sweep, the opening it makes after the wait
// reads as older than it is, and the sweep after it opens another round less than 50 seconds after
// the real opening.
func TestPodUpdateRoundsPerMinuteDatesAnOpeningWhenTheSweepMakesIt(t *testing.T) {
	slow, opened, next := "ACME-1", "ACME-2", "ACME-3"
	rig := newLimitRig(t, slow, opened, next)
	rig.openSlowRound(slow, opened, next)
	rig.clock.advance(time.Minute)
	rig.s.cfg.PodUpdateRoundsPerMinute = new(1)

	rig.sweepHeldInAWrite(slow, 70*time.Second)
	rig.wantPodUpdateRecord("the sweep that waited on the write", api.PodUpdateRounds{
		Swept: true, RoundsPerMinute: new(1),
		InRound:                  []string{slow, opened},
		WaitingOnRoundsPerMinute: []string{next},
	})
	rig.awaitRounds("the opening that the sweep made after the write", slow, opened)

	// Fifty seconds after the real opening, the next sweep opens nothing. Dated from the top of
	// the sweep that waited, the opening would read as two minutes old.
	rig.clock.advance(50 * time.Second)
	rig.tick()
	rig.wantPodUpdateRecord("a sweep fifty seconds after the real opening", api.PodUpdateRounds{
		Swept: true, RoundsPerMinute: new(1),
		InRound:                  []string{slow, opened},
		WaitingOnRoundsPerMinute: []string{next},
	})

	// Half a second later the gap has passed, and the next issue opens.
	rig.clock.advance(500 * time.Millisecond)
	rig.tick()
	rig.wantPodUpdateRecord("a sweep fifty and a half seconds after the real opening", api.PodUpdateRounds{
		Swept: true, RoundsPerMinute: new(1),
		InRound: []string{slow, opened, next},
	})
	rig.awaitRounds("the sweep after the gap", slow, opened, next)
}

// A sweep whose budget is two rounds can open one, wait in its walk, and open the second after the
// wait. The daemon's last opening is then the second, and the next sweep measures its gap from it:
// dated from the first, or from the top of the sweep, it would read as older than it is, and the
// next sweep would open two more rounds less than 50 seconds after it.
func TestPodUpdateRoundsPerMinuteDatesTheLastOpeningOfASweepThatWaitedBetweenTwo(t *testing.T) {
	first, slow, second, third, fourth := "ACME-1", "ACME-2", "ACME-3", "ACME-4", "ACME-5"
	rig := newLimitRig(t, first, slow, second, third, fourth)
	rig.openSlowRound(slow, first, second, third, fourth)
	rig.clock.advance(time.Minute)
	rig.s.cfg.PodUpdateRoundsPerMinute = new(2)

	rig.sweepHeldInAWrite(slow, 70*time.Second)
	rig.wantPodUpdateRecord("the sweep that opened a round, waited on the write and opened another", api.PodUpdateRounds{
		Swept: true, RoundsPerMinute: new(2),
		InRound:                  []string{first, slow, second},
		WaitingOnRoundsPerMinute: []string{third, fourth},
	})
	rig.awaitRounds("the two openings of the sweep that waited", first, slow, second)

	// Fifty seconds after the second opening, the next sweep opens nothing.
	rig.clock.advance(50 * time.Second)
	rig.tick()
	rig.wantPodUpdateRecord("a sweep fifty seconds after the second opening", api.PodUpdateRounds{
		Swept: true, RoundsPerMinute: new(2),
		InRound:                  []string{first, slow, second},
		WaitingOnRoundsPerMinute: []string{third, fourth},
	})

	// Half a second later the gap has passed, and the next two issues open.
	rig.clock.advance(500 * time.Millisecond)
	rig.tick()
	rig.wantPodUpdateRecord("a sweep fifty and a half seconds after the second opening", api.PodUpdateRounds{
		Swept: true, RoundsPerMinute: new(2),
		InRound: []string{first, slow, second, third, fourth},
	})
	rig.awaitRounds("the sweep after the gap", first, slow, second, third, fourth)
}

// An opening the walk makes before a wait is dated when it was made, not after the wait. Dated at
// the end of the walk, an opening followed by a slow write would read as younger than it is, and
// the sweep that follows straight after the wait, 70 seconds after the real opening, would open
// nothing.
func TestPodUpdateRoundsPerMinuteDatesAnOpeningBeforeTheWaitThatFollowsIt(t *testing.T) {
	opened, slow, next := "ACME-1", "ACME-2", "ACME-3"
	rig := newLimitRig(t, opened, slow, next)
	rig.openSlowRound(slow, opened, next)
	rig.clock.advance(time.Minute)
	rig.s.cfg.PodUpdateRoundsPerMinute = new(1)

	rig.sweepHeldInAWrite(slow, 70*time.Second)
	rig.wantPodUpdateRecord("the sweep that opened a round and then waited on the write", api.PodUpdateRounds{
		Swept: true, RoundsPerMinute: new(1),
		InRound:                  []string{opened, slow},
		WaitingOnRoundsPerMinute: []string{next},
	})
	rig.awaitRounds("the opening before the write", opened, slow)

	rig.tick()
	rig.wantPodUpdateRecord("a sweep seventy seconds after the real opening", api.PodUpdateRounds{
		Swept: true, RoundsPerMinute: new(1),
		InRound: []string{opened, slow, next},
	})
	rig.awaitRounds("the sweep seventy seconds after the real opening", opened, slow, next)
}

// R3: "The sweep opens rounds before it waits on any pod replacement, so the openings follow the
// sweep's one-minute tick." In a roll each sweep's walk writes the ask of the round the sweep
// before it opened, whose issue sorts before the one it opens next, and here each of those writes
// takes 15 seconds. Ticks a minute apart then make openings a minute apart, and every tick opens
// its round. Read once at the top of the sweep, the gap would be measured 15 seconds short of the
// real one, and every second tick would open nothing.
func TestPodUpdateRoundsPerMinuteOpeningsFollowTheTickWhenEachWalkWaitsBeforeItsOpening(t *testing.T) {
	keys := []string{"ACME-0", "ACME-1", "ACME-2", "ACME-3", "ACME-4"}
	rig := newLimitRig(t, keys...)
	rig.openSlowRound(keys[0], keys[1:]...)
	rig.clock.advance(time.Minute)
	rig.s.cfg.PodUpdateRoundsPerMinute = new(1)

	top := rig.clock.Now()
	var last time.Time
	for i := 1; i < len(keys); i++ {
		// The regular tick: the tops of the sweeps are a minute apart.
		rig.clock.advance(top.Add(time.Duration(i-1) * time.Minute).Sub(rig.clock.Now()))
		rig.sweepHeldInAWrite(keys[i-1], 15*time.Second)
		rig.awaitRounds(fmt.Sprintf("tick %d, whose walk waited 15 seconds before its opening", i), keys[:i+1]...)
		opened := rig.claimOf(keys[i], claim.RoleArchitect).UpdateOpenedAt
		if opened == nil {
			t.Fatalf("tick %d opened %s, whose claim records no opening", i, keys[i])
		}
		if !last.IsZero() && opened.Sub(last) != time.Minute {
			t.Errorf("tick %d opened %s %s after the opening before it, want a minute", i, keys[i], opened.Sub(last))
		}
		last = *opened
	}
}

// The gap is read when the walk reaches the issue it would open, and it holds there as it holds
// at the top of a sweep: a sweep that begins 35 seconds after the last opening and whose walk
// reaches the next issue 50 seconds after it opens nothing, and the sweep half a second later
// opens that issue.
func TestPodUpdateRoundsPerMinuteHoldsAnOpeningTheWalkReachesFiftySecondsAfterTheLast(t *testing.T) {
	slow, opened, next := "ACME-1", "ACME-2", "ACME-3"
	rig := newLimitRig(t, slow, opened, next)
	rig.openSlowRound(slow, opened, next)
	rig.clock.advance(35 * time.Second)
	rig.s.cfg.PodUpdateRoundsPerMinute = new(1)

	rig.sweepHeldInAWrite(slow, 15*time.Second)
	rig.wantPodUpdateRecord("a sweep whose walk reached the next issue fifty seconds after the last opening", api.PodUpdateRounds{
		Swept: true, RoundsPerMinute: new(1),
		InRound:                  []string{slow},
		WaitingOnRoundsPerMinute: []string{opened, next},
	})

	rig.clock.advance(500 * time.Millisecond)
	rig.tick()
	rig.wantPodUpdateRecord("a sweep fifty and a half seconds after the last opening", api.PodUpdateRounds{
		Swept: true, RoundsPerMinute: new(1),
		InRound:                  []string{slow, opened},
		WaitingOnRoundsPerMinute: []string{next},
	})
	rig.awaitRounds("the sweep after the gap", slow, opened)
}
