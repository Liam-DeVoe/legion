package main

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
)

// overScheduleLimit is the set-aside rule's threshold: a step is set aside when, in any second
// of its measured window, the generator sent more requests, or more event writes, than that
// second's schedule held by more than this share of it. It is a tenth, the architect's threshold
// (dispatch://INFRA-592, 2026-11-15), because a tenth lies far from both of the things it tells
// apart. A generator that keeps to its schedule sends in each second what the schedule put there,
// give or take the few arrivals sent a moment late across a second's edge. A generator the box
// stalled sends, once it runs again, every arrival it owes at once (openLoop: sleepUntil returns
// at once for a time already past), so the second after a stall holds the seconds it missed: in
// the failing 10x step /proc/net/snmp counted 700 to 3,540 new connections a second against a
// schedule of 640. Such a step did not deliver the load it claims, and its connection failures
// measure the test bed rather than Dispatch.
const overScheduleLimit = 0.10

// minJudgedSchedule is the smallest schedule a second is judged against. Below ten arrivals one
// arrival sent a moment late, across the edge into the next second, puts that second more than a
// tenth over its schedule (a second scheduled 4 that sends 5 is 25% over), so the ratio of so
// small a second measures where an arrival fell against the clock and not a burst; a second
// scheduled 0 or 1 has no ratio worth the name at all. So a second whose schedule holds fewer
// than ten is judged as if it held ten: one or two arrivals across its edge never break the rule,
// while a burst into it still does once it sends more than 11, which is the backlog of a stall
// and no edge effect. At ten, one arrival across the edge is a tenth over, which is not more than
// a tenth.
const minJudgedSchedule = 10

// The two kinds of arrival the rule judges, as summary.json and arrivals.json name them.
const (
	paceRequests = "requests"
	paceEvents   = "events"
)

// secondCount is one second's arrivals of one generator: those its schedule put in that second,
// and those it sent in it.
type secondCount struct {
	scheduled, sent int
}

// pacer counts one generator's arrivals per second of the step, by when the schedule put each one
// (scheduled) and by when the generator let it go (sent), the warm-up's seconds counted from the
// step's start and the window's from the window's start. An arrival sent at or after the window's
// end is counted in late. A pacer belongs to the one goroutine that walks its generator's
// schedule, and is read once that goroutine has returned, so it takes no lock.
type pacer struct {
	start, winStart, winEnd time.Time
	warmup, window          []secondCount
	late                    int
}

func newPacer(start, winStart, winEnd time.Time) *pacer {
	return &pacer{start: start, winStart: winStart, winEnd: winEnd,
		warmup: make([]secondCount, wholeSeconds(winStart.Sub(start))),
		window: make([]secondCount, wholeSeconds(winEnd.Sub(winStart)))}
}

// wholeSeconds is how many seconds, the last one perhaps partial, d spans.
func wholeSeconds(d time.Duration) int {
	if d <= 0 {
		return 0
	}
	return int((d + time.Second - 1) / time.Second)
}

// slot is the count of the second t falls in, nil outside [start, winEnd).
func (p *pacer) slot(t time.Time) *secondCount {
	var secs []secondCount
	var from time.Time
	switch {
	case t.Before(p.start) || !t.Before(p.winEnd):
		return nil
	case t.Before(p.winStart):
		secs, from = p.warmup, p.start
	default:
		secs, from = p.window, p.winStart
	}
	if i := int(t.Sub(from) / time.Second); i < len(secs) {
		return &secs[i]
	}
	return nil
}

// record counts one arrival the schedule put at scheduled and the generator sent at sent.
func (p *pacer) record(scheduled, sent time.Time) {
	if c := p.slot(scheduled); c != nil {
		c.scheduled++
	}
	if c := p.slot(sent); c != nil {
		c.sent++
	} else if !sent.Before(p.winEnd) {
		p.late++
	}
}

// arrivalSecond is one second of arrivals.json: the requests and event writes the schedule put
// in it and those the generators sent in it, every population and every generator of each kind
// summed.
type arrivalSecond struct {
	Second            int `json:"second"`
	RequestsScheduled int `json:"requests_scheduled"`
	RequestsSent      int `json:"requests_sent"`
	EventsScheduled   int `json:"events_scheduled"`
	EventsSent        int `json:"events_sent"`
}

// arrivalsFile is arrivals.json: the step's arrivals per second of the warm-up and of the window,
// and those sent once the window had ended.
type arrivalsFile struct {
	Warmup                  []arrivalSecond `json:"warmup"`
	Window                  []arrivalSecond `json:"window"`
	RequestsSentAfterWindow int             `json:"requests_sent_after_window"`
	EventsSentAfterWindow   int             `json:"events_sent_after_window"`
}

// arrivalsOf sums the pacers of each kind into arrivals.json. Every pacer of a step has the
// step's start and window, so their seconds line up.
func arrivalsOf(requests, events []*pacer) arrivalsFile {
	var f arrivalsFile
	add := func(pacers []*pacer, kind string) {
		for _, p := range pacers {
			f.Warmup = addSeconds(f.Warmup, p.warmup, kind)
			f.Window = addSeconds(f.Window, p.window, kind)
			if kind == paceRequests {
				f.RequestsSentAfterWindow += p.late
			} else {
				f.EventsSentAfterWindow += p.late
			}
		}
	}
	add(requests, paceRequests)
	add(events, paceEvents)
	return f
}

// addSeconds adds one pacer's seconds of kind into rows.
func addSeconds(rows []arrivalSecond, secs []secondCount, kind string) []arrivalSecond {
	for len(rows) < len(secs) {
		rows = append(rows, arrivalSecond{Second: len(rows)})
	}
	for i, c := range secs {
		if kind == paceRequests {
			rows[i].RequestsScheduled += c.scheduled
			rows[i].RequestsSent += c.sent
		} else {
			rows[i].EventsScheduled += c.scheduled
			rows[i].EventsSent += c.sent
		}
	}
	return rows
}

// of is the second's count of kind.
func (s arrivalSecond) of(kind string) secondCount {
	if kind == paceRequests {
		return secondCount{scheduled: s.RequestsScheduled, sent: s.RequestsSent}
	}
	return secondCount{scheduled: s.EventsScheduled, sent: s.EventsSent}
}

// overSchedule is how far a second ran over its schedule, sent / max(scheduled,
// minJudgedSchedule), and whether that breaks the set-aside rule: more than overScheduleLimit
// over.
func (c secondCount) overSchedule() (ratio float64, breaks bool) {
	base := max(c.scheduled, minJudgedSchedule)
	return float64(c.sent) / float64(base), float64(c.sent-base) > overScheduleLimit*float64(base)
}

// pacingSummary is summary.json's pacing: what the generators delivered against their schedule,
// per kind, and the rule's two constants.
type pacingSummary struct {
	OverScheduleLimit float64    `json:"over_schedule_limit"`
	MinJudgedSchedule int        `json:"min_judged_schedule"`
	Requests          pacingKind `json:"requests"`
	Events            pacingKind `json:"events"`
}

// pacingKind is one kind's arrivals: the window's scheduled and sent counts and those sent after
// it, and the second of the window, and of the warm-up, that ran furthest over its schedule
// (overSchedule's ratio). A kind that had nothing scheduled or sent in a span has no such second,
// and its two fields are null.
type pacingKind struct {
	Scheduled           int      `json:"scheduled"`
	Sent                int      `json:"sent"`
	SentAfterWindow     int      `json:"sent_after_window"`
	MaxOver             *float64 `json:"max_over"`
	MaxOverSecond       *int     `json:"max_over_second"`
	WarmupMaxOver       *float64 `json:"warmup_max_over"`
	WarmupMaxOverSecond *int     `json:"warmup_max_over_second"`
}

// setAsideSummary is summary.json's set_aside: why the step is set aside and every second of the
// window that broke the rule, in order. It is absent for a step that kept to its schedule.
type setAsideSummary struct {
	Why     string       `json:"why"`
	Seconds []overSecond `json:"seconds"`
}

// overSecond is one second of the window, counted from its start, in which one kind's sent
// arrivals broke the rule: its schedule, what was sent and how far over (overSchedule's ratio).
type overSecond struct {
	Second    int     `json:"second"`
	Kind      string  `json:"kind"`
	Scheduled int     `json:"scheduled"`
	Sent      int     `json:"sent"`
	Over      float64 `json:"over"`
}

// judgePacing reads arrivals.json's counts into summary.json's pacing, and set_aside when a
// second of the window broke the rule (nil otherwise). The warm-up is recorded and not judged:
// nothing in it is measured.
func judgePacing(f arrivalsFile) (pacingSummary, *setAsideSummary) {
	sum := pacingSummary{OverScheduleLimit: overScheduleLimit, MinJudgedSchedule: minJudgedSchedule}
	var over []overSecond
	for _, kind := range []string{paceRequests, paceEvents} {
		k := pacingKind{}
		k.WarmupMaxOver, k.WarmupMaxOverSecond, _ = maxOver(f.Warmup, kind)
		var breaks []overSecond
		k.MaxOver, k.MaxOverSecond, breaks = maxOver(f.Window, kind)
		over = append(over, breaks...)
		for _, s := range f.Window {
			c := s.of(kind)
			k.Scheduled += c.scheduled
			k.Sent += c.sent
		}
		if kind == paceRequests {
			k.SentAfterWindow = f.RequestsSentAfterWindow
			sum.Requests = k
		} else {
			k.SentAfterWindow = f.EventsSentAfterWindow
			sum.Events = k
		}
	}
	if len(over) == 0 {
		return sum, nil
	}
	// The seconds in order, a second's requests before its events ("requests" > "events").
	slices.SortStableFunc(over, func(a, b overSecond) int {
		return cmp.Or(cmp.Compare(a.Second, b.Second), strings.Compare(b.Kind, a.Kind))
	})
	worst := over[0]
	for _, s := range over[1:] {
		if s.Over > worst.Over {
			worst = s
		}
	}
	why := fmt.Sprintf("the generator sent more than %s over its schedule in %s of the window; the worst, second %d: %d %s sent against %d scheduled (%sx)",
		percent(overScheduleLimit), plural(len(over), "second"), worst.Second, worst.Sent, worst.Kind, worst.Scheduled, num(worst.Over, 2))
	return sum, &setAsideSummary{Why: why, Seconds: over}
}

// maxOver is the second of secs that ran furthest over kind's schedule, nil for a span with
// nothing of kind scheduled or sent, and every second of it that broke the rule.
func maxOver(secs []arrivalSecond, kind string) (*float64, *int, []overSecond) {
	var best *float64
	var at *int
	var breaks []overSecond
	for _, s := range secs {
		c := s.of(kind)
		if c.scheduled == 0 && c.sent == 0 {
			continue
		}
		ratio, broke := c.overSchedule()
		if best == nil || ratio > *best {
			best, at = &ratio, &s.Second
		}
		if broke {
			breaks = append(breaks, overSecond{Second: s.Second, Kind: kind, Scheduled: c.scheduled, Sent: c.sent, Over: ratio})
		}
	}
	return best, at, breaks
}

// percent is a share as a percentage, to a tenth of a percent: 0.1 is "10%".
func percent(share float64) string { return fmt.Sprintf("%g%%", math.Round(share*1000)/10) }

// sentCell is the window's "sent / scheduled".
func (k pacingKind) sentCell() string {
	return num(float64(k.Sent), 0) + " / " + num(float64(k.Scheduled), 0)
}

// overCell is a span's second furthest over its schedule, "1.04x (s 17)", or "-" for a span with
// nothing scheduled or sent.
func overCell(ratio *float64, second *int) string {
	if ratio == nil || second == nil {
		return "-"
	}
	return fmt.Sprintf("%sx (s %d)", num(*ratio, 2), *second)
}

// text is the kind's pacing as run.log writes it.
func (k pacingKind) text() string {
	return fmt.Sprintf("sent %s scheduled in the window, most over schedule %s (warm-up %s), %d sent after the window",
		k.sentCell(), overCell(k.MaxOver, k.MaxOverSecond), overCell(k.WarmupMaxOver, k.WarmupMaxOverSecond), k.SentAfterWindow)
}

// setAsideMark marks the step's name in every table of TABLES.md when the step is set aside.
const setAsideMark = "(set aside)"

// setAsideWord is what a set-aside step reads as where another step reads pass or fail: the
// curves' Pass table and curves.csv, and loadgen step-check's clause.
const setAsideWord = "set aside"

// setAsideNote is TABLES.md's first lines when a step is set aside: each such step, why, and what
// set aside means, so it is the first thing a reader of the tables sees.
func setAsideNote(md *markdown, steps []stepData) {
	var aside []string
	for _, s := range steps {
		if s.sum.SetAside != nil {
			aside = append(aside, fmt.Sprintf("- **%s**: %s.", s.name, mdEscape(s.sum.SetAside.Why)))
		}
	}
	if len(aside) == 0 {
		return
	}
	md.line("**SET ASIDE: no pass and no failure.** In a second of its window the generator of each step below sent more than %s over that second's schedule: the burst of a generator the box stalled, which then fires every arrival it owes at once. Such a step did not deliver the load it claims, so its rows below describe the test bed and not Dispatch (summary.json `set_aside`, arrivals.json). Run it again where the generator keeps its schedule.\n",
		percent(overScheduleLimit))
	md.line("%s\n", strings.Join(aside, "\n"))
}
