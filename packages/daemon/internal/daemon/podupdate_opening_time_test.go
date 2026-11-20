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
	"slices"
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
// every issue before it. A sweep that makes no such write fails the test.
func (r *fleetRig) sweepHeldInAWrite(slow string, hold time.Duration) {
	r.t.Helper()
	began := r.clock.Now()
	r.sweepHoldingAWrite(slow, hold)
	if got := r.clock.Now().Sub(began); got != hold {
		r.t.Fatalf("the sweep's write for %s held it for %s, want %s", slow, got, hold)
	}
}

// sweepHoldingAWrite is sweepHeldInAWrite for a sweep that may make no write for slow, such as a
// tick of a roll whose last opening was asked at a tick before it: its first write for slow, if
// it makes one, takes hold and the return value says that it did.
func (r *fleetRig) sweepHoldingAWrite(slow string, hold time.Duration) bool {
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
	r.tick()
	return held
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

// R3: "The waiting issues open in the order of their keys as strings." The walk is in key order
// and the gap is read as the walk reaches each waiting issue, so a walk that waits between two
// waiting issues can reach the first within the gap and the second past it. Once the gap holds
// the first, the sweep opens neither, and the first opens at the next sweep, before the second.
// Here the first drifts after the roll has passed its key, and the wait between the two is the
// write a roll makes at every tick: the ask of the round the tick before opened, here 15 seconds.
func TestPodUpdateRoundsPerMinuteOpensNoLaterKeyOnceTheGapHoldsAnEarlierOne(t *testing.T) {
	early, first, second, third, fourth := "ACME-1", "ACME-2", "ACME-3", "ACME-4", "ACME-5"
	rig := newLimitRig(t, early, first, second, third, fourth)
	rig.drifts(early)
	rig.openSlowRound(first, second, third, fourth)
	rig.clock.advance(time.Minute)
	rig.s.cfg.PodUpdateRoundsPerMinute = new(1)
	top := rig.clock.Now()

	// Tick 1 writes first's ask, then opens second, 15 seconds after its top.
	rig.sweepHeldInAWrite(first, 15*time.Second)
	rig.wantPodUpdateRecord("tick 1", api.PodUpdateRounds{
		Swept: true, RoundsPerMinute: new(1),
		InRound:                  []string{first, second},
		WaitingOnRoundsPerMinute: []string{third, fourth},
	})
	rig.awaitRounds("tick 1", first, second)
	rig.drifts(early, limitDrift)

	// Tick 2, a minute after tick 1: the walk reaches early 45 seconds after second's opening,
	// writes second's ask, and reaches third 60 seconds after that opening. third waits behind
	// early.
	rig.clock.advance(top.Add(time.Minute).Sub(rig.clock.Now()))
	rig.sweepHeldInAWrite(second, 15*time.Second)
	rig.wantPodUpdateRecord("tick 2, whose walk waited between early and third", api.PodUpdateRounds{
		Swept: true, RoundsPerMinute: new(1),
		InRound:                  []string{first, second},
		WaitingOnRoundsPerMinute: []string{early, third, fourth},
	})

	// Tick 3 opens early, before every key after it.
	rig.clock.advance(top.Add(2 * time.Minute).Sub(rig.clock.Now()))
	rig.tick()
	rig.wantPodUpdateRecord("tick 3", api.PodUpdateRounds{
		Swept: true, RoundsPerMinute: new(1),
		InRound:                  []string{early, first, second},
		WaitingOnRoundsPerMinute: []string{third, fourth},
	})
	rig.awaitRounds("tick 3", early, first, second)
}

// The key-order rule also applies when the per-minute budget has more than one opening left: a
// gap that holds the earlier key spends the rest of the budget, rather than one opening.
func TestPodUpdateRoundsPerMinuteOpensNoLaterKeyWhenTheGapHoldsAnEarlierOneWithTwoBudget(t *testing.T) {
	early, slow, late, last := "ACME-1", "ACME-2", "ACME-3", "ACME-4"
	rig := newLimitRig(t, early, slow, late, last)
	rig.openSlowRound(slow, early, late, last)
	rig.clock.advance(35 * time.Second)
	rig.s.cfg.PodUpdateRoundsPerMinute = new(2)
	top := rig.clock.Now()

	// The gap first holds early; the walk then spends 20 seconds writing slow's ask before it
	// reaches late.
	rig.sweepHeldInAWrite(slow, 20*time.Second)
	rig.wantPodUpdateRecord("the two-budget sweep whose walk held the earlier key", api.PodUpdateRounds{
		Swept:                    true,
		RoundsPerMinute:          new(2),
		InRound:                  []string{slow},
		WaitingOnRoundsPerMinute: []string{early, late, last},
	})

	// The next sweep opens early before late, with the two openings the budget permits.
	rig.clock.advance(top.Add(time.Minute).Sub(rig.clock.Now()))
	rig.tick()
	rig.wantPodUpdateRecord("the next two-budget sweep", api.PodUpdateRounds{
		Swept:                    true,
		RoundsPerMinute:          new(2),
		InRound:                  []string{early, slow, late},
		WaitingOnRoundsPerMinute: []string{last},
	})
	rig.awaitRounds("the next two-budget sweep", early, slow, late)
}

// The roll of the cadence test above, with an issue whose key sorts before the roll drifting
// after tick 1. Every tick's walk reaches that issue first, and with each walk's write taking 15
// seconds the last opening is 45 seconds old when it does: were the issues after it opened past
// the gap, it would wait out the whole roll. It opens at the tick after the one the gap held it
// at, second of all the roll's openings, and the roll goes on in key order after it.
func TestPodUpdateRoundsPerMinuteAnIssueThatDriftsBehindTheRollOpensBeforeTheKeysAfterIt(t *testing.T) {
	keys := []string{"ACME-1", "ACME-2", "ACME-3", "ACME-4", "ACME-5", "ACME-6"}
	early := keys[0]
	rig := newLimitRig(t, keys...)
	rig.drifts(early)
	rig.openSlowRound(keys[1], keys[2:]...)
	rig.clock.advance(time.Minute)
	rig.s.cfg.PodUpdateRoundsPerMinute = new(1)

	top := rig.clock.Now()
	opened, last := []string{keys[1]}, keys[1]
	var order []string
	for tick := 1; tick <= 8 && len(opened) < len(keys); tick++ {
		// The regular tick: the tops of the sweeps are a minute apart, and each walk's write of
		// the ask of the round opened last takes 15 seconds.
		rig.clock.advance(top.Add(time.Duration(tick-1) * time.Minute).Sub(rig.clock.Now()))
		held := rig.sweepHoldingAWrite(last, 15*time.Second)
		if tick <= 2 && !held {
			t.Fatalf("the roll's tick %d made no held write for %s's ask", tick, last)
		}
		for _, issue := range rig.s.podUpdateRoundsView().InRound {
			if !slices.Contains(opened, issue) {
				opened, last = append(opened, issue), issue
				order = append(order, issue)
			}
		}
		rig.awaitRounds(fmt.Sprintf("tick %d", tick), slices.Clone(opened)...)
		if tick == 1 {
			rig.drifts(early, limitDrift)
		}
	}
	if want := []string{keys[2], early, keys[3], keys[4], keys[5]}; !slices.Equal(order, want) {
		t.Errorf("the roll opened %v in that order, want %v: %s, the first key of all, waiting from tick 2 on", order, want, early)
	}
}

// The tick after a slow sweep (the design's case: the queued sweep opens, and the regular tick
// comes less than a minute after it), here 49.5 seconds after the last opening, with a write of
// one second in the walk between two waiting issues. The gap holds the first, and the second
// waits behind it; the next sweep opens the first.
func TestPodUpdateRoundsPerMinuteOpensNoLaterKeyAfterAOneSecondWriteInTheTickAfterASlowSweep(t *testing.T) {
	early, slow, late := "ACME-1", "ACME-2", "ACME-3"
	rig := newLimitRig(t, early, slow, late)
	rig.openSlowRound(slow, early, late)
	rig.clock.advance(49*time.Second + 500*time.Millisecond)
	rig.s.cfg.PodUpdateRoundsPerMinute = new(1)

	rig.sweepHeldInAWrite(slow, time.Second)
	rig.wantPodUpdateRecord("the sweep whose walk waited a second between the two waiting issues", api.PodUpdateRounds{
		Swept: true, RoundsPerMinute: new(1),
		InRound:                  []string{slow},
		WaitingOnRoundsPerMinute: []string{early, late},
	})

	rig.tick()
	rig.wantPodUpdateRecord("the sweep after it, 50.5 seconds after the last opening", api.PodUpdateRounds{
		Swept: true, RoundsPerMinute: new(1),
		InRound:                  []string{early, slow},
		WaitingOnRoundsPerMinute: []string{late},
	})
	rig.awaitRounds("the sweep after it", early, slow)
}
