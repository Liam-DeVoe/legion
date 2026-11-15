package main

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"time"
)

// pacedGenerator walks a Poisson schedule of rate per second from stream, the schedule
// openLoopPlan gives runEvents and runRequests, and paces each arrival as sent at send(at): a
// generator whose clock is send.
func pacedGenerator(rate float64, stream uint64, start, winStart, winEnd time.Time, send func(at time.Time) time.Time) *pacer {
	p := newPacer(start, winStart, winEnd)
	openLoopPlan(1, stream, ratePlan(rate, start, winStart, nil), winStart, winEnd, func(_ *rand.Rand, at time.Time, _ bool) bool {
		p.record(at, send(at))
		return true
	})
	return p
}

// pacingWindow is a step of a 10 s warm-up and a 60 s window.
func pacingWindow() (start, winStart, winEnd time.Time) {
	start = time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	return start, start.Add(10 * time.Second), start.Add(70 * time.Second)
}

// TestPacingSetsAsideAGeneratorThatSleepsThenCatchesUp: a generator the box stalls from second 20
// to second 35 of the window sends nothing in those seconds, then every arrival it owes at once,
// as openLoop does once sleepUntil returns for a time already past. The second it catches up in
// holds fifteen seconds of schedule, so the step is set aside, naming that second for the
// requests and for the event writes, and arrivals.json keeps every arrival: those the stall
// delayed are sent late, not lost.
func TestPacingSetsAsideAGeneratorThatSleepsThenCatchesUp(t *testing.T) {
	start, winStart, winEnd := pacingWindow()
	stallFrom, stallTo := winStart.Add(20*time.Second), winStart.Add(35*time.Second)
	send := func(at time.Time) time.Time {
		if !at.Before(stallFrom) && at.Before(stallTo) {
			return stallTo.Add(200 * time.Millisecond)
		}
		return at.Add(2 * time.Millisecond)
	}
	reqs := pacedGenerator(60, requestStream, start, winStart, winEnd, send)
	events := pacedGenerator(4, eventStream, start, winStart, winEnd, send)
	f := arrivalsOf([]*pacer{reqs}, []*pacer{events})
	if len(f.Warmup) != 10 || len(f.Window) != 60 {
		t.Fatalf("arrivals.json has %d warm-up and %d window seconds, want 10 and 60", len(f.Warmup), len(f.Window))
	}
	// Second 20 may hold an arrival of second 19 sent a moment late; from 21 on nothing is sent.
	for s := 21; s < 35; s++ {
		if got := f.Window[s]; got.RequestsSent != 0 || got.EventsSent != 0 || got.RequestsScheduled == 0 {
			t.Fatalf("stalled second %d: %+v, want arrivals scheduled and none sent", s, got)
		}
	}
	pacing, aside := judgePacing(f)
	if !allSent(pacing.Requests) || !allSent(pacing.Events) {
		t.Errorf("the window sent %+v requests and %+v events; the stall delays arrivals, it loses none", pacing.Requests, pacing.Events)
	}
	if aside == nil {
		t.Fatalf("a generator that caught up fifteen seconds at once is not set aside: %+v", pacing)
	}
	want := map[string]bool{"35 requests": false, "35 events": false}
	for _, s := range aside.Seconds {
		key := fmt.Sprintf("%d %s", s.Second, s.Kind)
		if _, ok := want[key]; !ok {
			t.Errorf("second %d (%s) broke the rule: %+v; only second 35 caught up", s.Second, s.Kind, s)
		}
		want[key] = true
	}
	for key, seen := range want {
		if !seen {
			t.Errorf("set_aside does not name second %s: %+v", key, aside.Seconds)
		}
	}
	if *pacing.Requests.MaxOverSecond != 35 || *pacing.Requests.MaxOver < 10 {
		t.Errorf("the requests' furthest second is %d at %.2fx, want 35 at 10x or more", *pacing.Requests.MaxOverSecond, *pacing.Requests.MaxOver)
	}
	for _, w := range []string{"more than 10% over its schedule in 2 seconds of the window", "the worst, second 35:"} {
		if !strings.Contains(aside.Why, w) {
			t.Errorf("set_aside.why %q lacks %q", aside.Why, w)
		}
	}
}

// TestPacingKeepsAGeneratorOnSchedule: a generator that sends each arrival a few ms after its
// time, some of them across a second's edge, is not set aside: at 1x (60 requests and 4 event
// writes a second) and at 10x (600 and 40). Its furthest second is within the tenth.
func TestPacingKeepsAGeneratorOnSchedule(t *testing.T) {
	start, winStart, winEnd := pacingWindow()
	late := func(at time.Time) time.Time { return at.Add(3 * time.Millisecond) }
	for _, rates := range [][2]float64{{60, 4}, {600, 40}} {
		f := arrivalsOf([]*pacer{pacedGenerator(rates[0], requestStream, start, winStart, winEnd, late)},
			[]*pacer{pacedGenerator(rates[1], eventStream, start, winStart, winEnd, late)})
		pacing, aside := judgePacing(f)
		if aside != nil {
			t.Errorf("%g req/s and %g events/s on schedule is set aside: %s", rates[0], rates[1], aside.Why)
		}
		if *pacing.Requests.MaxOver > 1+overScheduleLimit || *pacing.Events.MaxOver > 1+overScheduleLimit {
			t.Errorf("%g req/s on schedule: furthest seconds %.2fx and %.2fx, want within the tenth", rates[0], *pacing.Requests.MaxOver, *pacing.Events.MaxOver)
		}
		if !allSent(pacing.Requests) || pacing.Requests.SentAfterWindow > 2 {
			t.Errorf("%g req/s: %+v; want every arrival sent in the window, but the few late across its edges", rates[0], pacing.Requests)
		}
	}
}

// allSent says the window sent what it scheduled, give or take two arrivals sent a moment late
// across its edges: one of the warm-up's sent in its first second, one of its own after its end.
func allSent(k pacingKind) bool {
	d := k.Sent + k.SentAfterWindow - k.Scheduled
	return d >= -2 && d <= 2
}

// TestPacingDoesNotJudgeASecondWithTooSmallASchedule: a second scheduled fewer than
// minJudgedSchedule arrivals is judged against minJudgedSchedule: one or two arrivals across its
// edge (scheduled 1, sent 3; scheduled 4, sent 6) break nothing, while a burst into it (scheduled
// 2, sent 12) still does. At the minimum, one arrival over is exactly a tenth, which is not more.
func TestPacingDoesNotJudgeASecondWithTooSmallASchedule(t *testing.T) {
	for _, c := range []struct {
		scheduled, sent int
		breaks          bool
	}{
		{0, 1, false}, {1, 3, false}, {4, 6, false}, {0, 11, false},
		{2, 12, true}, {0, 30, true},
		{10, 11, false}, {10, 12, true}, {600, 660, false}, {600, 661, true}, {640, 3540, true},
	} {
		if _, breaks := (secondCount{scheduled: c.scheduled, sent: c.sent}).overSchedule(); breaks != c.breaks {
			t.Errorf("a second scheduled %d that sent %d breaks the rule: %t, want %t", c.scheduled, c.sent, breaks, c.breaks)
		}
	}
	// In a step: the event writes at 4 a second, every one sent across the next second's edge
	// from second 30 on, set nothing aside.
	f := arrivalsFile{Window: []arrivalSecond{
		{Second: 0, EventsScheduled: 3, EventsSent: 1, RequestsScheduled: 60, RequestsSent: 60},
		{Second: 1, EventsScheduled: 1, EventsSent: 4, RequestsScheduled: 58, RequestsSent: 59},
		{Second: 2, EventsScheduled: 0, EventsSent: 2, RequestsScheduled: 61, RequestsSent: 60},
	}}
	if _, aside := judgePacing(f); aside != nil {
		t.Errorf("seconds too small to judge set the step aside: %s", aside.Why)
	}
}

// TestPacerCountsWhereEachArrivalFalls: the warm-up's seconds count from the step's start, the
// window's from its start, an arrival sent at or after the window's end is late, and an arrival
// scheduled in the warm-up but sent in the window counts as sent in the window.
func TestPacerCountsWhereEachArrivalFalls(t *testing.T) {
	start, winStart, winEnd := pacingWindow()
	p := newPacer(start, winStart, winEnd)
	p.record(start.Add(1500*time.Millisecond), start.Add(1600*time.Millisecond))
	p.record(winStart.Add(-100*time.Millisecond), winStart.Add(100*time.Millisecond))
	p.record(winEnd.Add(-10*time.Millisecond), winEnd.Add(time.Second))
	if p.warmup[1] != (secondCount{1, 1}) || p.warmup[9] != (secondCount{1, 0}) || p.window[0] != (secondCount{0, 1}) {
		t.Errorf("warm-up second 1 %+v, 9 %+v, window second 0 %+v; want {1 1}, {1 0}, {0 1}", p.warmup[1], p.warmup[9], p.window[0])
	}
	if p.window[59] != (secondCount{1, 0}) || p.late != 1 {
		t.Errorf("window second 59 %+v, late %d; want {1 0} and 1", p.window[59], p.late)
	}
}

// TestSetAsideReachesTheTables: a set-aside step says so before the first table, carries the mark
// in every table's step cell, and the pacing table reads yes with its seconds; a step on schedule
// carries no mark, and a summary.json from before pacing reads - in the pacing cells.
func TestSetAsideReachesTheTables(t *testing.T) {
	over, second := 1.02, 17
	onTime := &pacingSummary{Requests: pacingKind{Scheduled: 10_800, Sent: 10_800, MaxOver: &over, MaxOverSecond: &second},
		Events: pacingKind{Scheduled: 720, Sent: 720, MaxOver: &over, MaxOverSecond: &second}}
	worst, at := 15.40, 35
	stalled := &pacingSummary{Requests: pacingKind{Scheduled: 10_800, Sent: 10_800, MaxOver: &worst, MaxOverSecond: &at},
		Events: pacingKind{Scheduled: 720, Sent: 720, MaxOver: &over, MaxOverSecond: &second}}
	_, aside := judgePacing(arrivalsFile{Window: []arrivalSecond{{Second: 35, RequestsScheduled: 60, RequestsSent: 924}}})
	steps := []stepData{
		{name: "1x", sum: summary{Config: summaryConfig{Step: "1x"}, Pacing: onTime}},
		{name: "10x", sum: summary{Config: summaryConfig{Step: "10x"}, Pacing: stalled, SetAside: aside}},
		{name: "old", sum: summary{Config: summaryConfig{Step: "old"}}},
	}
	md := renderTables(steps)
	head, _, _ := strings.Cut(md, "\n## ")
	if !strings.Contains(head, "**SET ASIDE: no pass and no failure.**") || !strings.Contains(head, "- **10x**: "+aside.Why) {
		t.Errorf("TABLES.md does not open with the set-aside step:\n%s", head)
	}
	if strings.Contains(head, "**1x**") {
		t.Errorf("TABLES.md names the step on schedule as set aside:\n%s", head)
	}
	for _, title := range []string{"Events, delivery and requests", "Generator pacing and accept backlog", "Resources"} {
		if rowCells(section(md, title), "10x "+setAsideMark) == nil {
			t.Errorf("section %q has no row marked %s:\n%s", title, setAsideMark, section(md, title))
		}
	}
	pace := section(md, "Generator pacing and accept backlog")
	for _, want := range []string{
		"| 10x (set aside) | 10,800 / 10,800 | 15.40x (s 35) | 720 / 720 | 1.02x (s 17) | yes: 1 second | - | - | - |",
		"| 1x | 10,800 / 10,800 | 1.02x (s 17) | 720 / 720 | 1.02x (s 17) | no | - | - | - |",
		"| old | - | - | - | - | - | - | - | - |",
	} {
		if !strings.Contains(pace, want+"\n") {
			t.Errorf("the pacing table lacks %q:\n%s", want, pace)
		}
	}
	if strings.Contains(md, "1x (set aside)") {
		t.Errorf("the step on schedule is marked set aside:\n%s", md)
	}
	checkRowWidths(t, md)
}
