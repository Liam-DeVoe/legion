package metrics

import (
	"maps"
	"strings"
	"time"
)

// grow.go holds how an issue's facts grow after the moment they are closed at (ClosedFacts), and
// nothing else: the one computation a reader of a stored row makes (legionfacts' Row.At), which
// adds each fact's growth over the time from that moment to the reader's clock. Measuring the
// growth is the computation's (closed.go); this file is the half both sides share, the names a
// growth is keyed by and the arithmetic that applies it.

// Growth is how fast each of an issue's facts grows after the moment its facts are closed at, in
// hours an hour, by the fact's name: its IssueFacts field's name in snake case, and for an entry
// of a map the entry's key after a dot (`work_hours`, `phase_hours.implementing`,
// `bars.implementer.hours`, `bars.implementer.segments.idle`, `idle_by_role.reviewer`), the
// constants below, which the stored rows' growth column holds as its keys. The two moments of
// measured work are named `work_first_at` and `work_last_at`, and an entry of 1 for the last is a
// bar still open, whose end runs with the clock. A fact that stands still has no entry, so the
// growth of a closed issue is empty but for a slot it never gave up (SlotHours runs to the
// population's end, not the issue's).
//
// A rate is a whole number wherever the computation measured it: each hours fact is the measure
// of a set of spans or a sum of such measures, and a span still open grows one hour an hour.
type Growth map[string]float64

// The names Growth keys a fact by, the facts of a map by the prefix their key follows. The four
// exported are the ones a board entry carries a rate of (api's legionBoardGrowthOf): the work
// time, the slot hours, the last moment of measured work and each column's hours.
const (
	GrowthWorkHours      = "work_hours"
	GrowthSlotHours      = "slot_hours"
	growthParkedHours    = "parked_hours"
	growthQueuedHours    = "queued_hours"
	growthSlotWaitHours  = "slot_wait_hours"
	growthImplementing   = "implementing_hours_after_first_review"
	growthTesting        = "testing_hours_after_first_review"
	growthWorkFirstAt    = "work_first_at"
	GrowthWorkLastAt     = "work_last_at"
	GrowthPhasePrefix    = "phase_hours."
	growthBarPrefix      = "bars."
	growthBarHours       = "hours"
	growthSegmentsPrefix = "segments."
	growthIdlePrefix     = "idle_by_role."
)

// namedHours is one hours fact of an issue that is no entry of a map, with its name in a Growth.
type namedHours struct {
	name  string
	field *float64
}

// scalarHours is every hours fact of f that is no entry of a map: the one list of them that
// measuring a growth (hoursFacts) and applying one (addHours) both read.
func (f *IssueFacts) scalarHours() [7]namedHours {
	return [7]namedHours{
		{GrowthWorkHours, &f.WorkHours},
		{GrowthSlotHours, &f.SlotHours},
		{growthParkedHours, &f.ParkedHours},
		{growthQueuedHours, &f.QueuedHours},
		{growthSlotWaitHours, &f.SlotWaitHours},
		{growthImplementing, &f.ImplementingHoursAfterFirstReview},
		{growthTesting, &f.TestingHoursAfterFirstReview},
	}
}

// hoursFacts is every hours fact f holds, by its name in a Growth: the values a growth is
// measured between (growthBetween).
func (f IssueFacts) hoursFacts() map[string]float64 {
	facts := map[string]float64{}
	for _, scalar := range f.scalarHours() {
		facts[scalar.name] = *scalar.field
	}
	for id, hours := range f.PhaseHours {
		facts[GrowthPhasePrefix+id] = hours
	}
	for role, bar := range f.Bars {
		facts[growthBarPrefix+role+"."+growthBarHours] = bar.Hours
		for segment, hours := range bar.Segments.ByName() {
			facts[growthBarPrefix+role+"."+growthSegmentsPrefix+string(segment)] = hours
		}
	}
	for role, hours := range f.IdleByRole {
		facts[growthIdlePrefix+role] = hours
	}
	return facts
}

// workMoment is the moment of measured work name names in a Growth, and nil for a name that is
// none: the field itself, so a caller can move it.
func (f *IssueFacts) workMoment(name string) **time.Time {
	switch name {
	case growthWorkFirstAt:
		return &f.WorkFirstAt
	case GrowthWorkLastAt:
		return &f.WorkLastAt
	}
	return nil
}

// Grown is f elapsed after the moment it was closed at: each fact that grows grown by its rate
// times the hours elapsed, rounded as every number of the computation is, and a bar still open
// ending elapsed later. Every other fact - the counts, the tree, the moments that do not move -
// is f's own. A negative elapsed is a moment before the one f was closed at, which is how the
// computation closes facts it measured later at the moment of the issue's newest input
// (ProjectFactsClosed).
//
// It writes nothing of f: each map it grows is a copy. A map f does not hold (a reader that read
// no bars) stays absent. An hours entry by phase that grows from nothing is added and one that
// falls to nothing is left out, since PhaseHours holds no part without hours; a bar and a role
// hold an entry wherever the computation answered the map at all.
func (f IssueFacts) Grown(growth Growth, elapsed time.Duration) IssueFacts {
	if len(growth) == 0 || elapsed == 0 {
		return f
	}
	hours := elapsed.Hours()
	grown := f
	grown.PhaseHours = maps.Clone(f.PhaseHours)
	grown.Bars = maps.Clone(f.Bars)
	grown.IdleByRole = maps.Clone(f.IdleByRole)
	for name, rate := range growth {
		if at := grown.workMoment(name); at != nil {
			if *at != nil {
				moved := (*at).Add(time.Duration(rate * float64(elapsed)))
				*at = &moved
			}
			continue
		}
		grown.addHours(name, rate*hours)
	}
	return grown
}

// grownValue is a grown hours value rounded as the computation rounds, with no negative zero,
// which would encode as `-0` and read as a change of the stored row.
func grownValue(value float64) float64 {
	value = round(value)
	if value == 0 {
		return 0
	}
	return value
}

// addHours adds delta to the hours fact name names (Growth), rounded as the computation rounds
// (grownValue), in the maps Grown copied, and changes nothing for a name that is no fact or names
// an entry of a map f does not hold. A phase whose hours fall to nothing leaves PhaseHours.
func (f *IssueFacts) addHours(name string, delta float64) {
	for _, scalar := range f.scalarHours() {
		if scalar.name == name {
			*scalar.field = grownValue(*scalar.field + delta)
			return
		}
	}
	if id, phase := strings.CutPrefix(name, GrowthPhasePrefix); phase {
		if f.PhaseHours == nil {
			return
		}
		if value := grownValue(f.PhaseHours[id] + delta); value > 0 {
			f.PhaseHours[id] = value
		} else {
			delete(f.PhaseHours, id)
		}
		return
	}
	if role, idle := strings.CutPrefix(name, growthIdlePrefix); idle {
		if f.IdleByRole != nil {
			f.IdleByRole[role] = grownValue(f.IdleByRole[role] + delta)
		}
		return
	}
	rest, bar := strings.CutPrefix(name, growthBarPrefix)
	if !bar || f.Bars == nil {
		return
	}
	role, part, _ := strings.Cut(rest, ".")
	held := f.Bars[role]
	field := &held.Hours
	if segment, named := strings.CutPrefix(part, growthSegmentsPrefix); named {
		field = held.Segments.hoursOf(Segment(segment))
	} else if part != growthBarHours {
		field = nil
	}
	if field == nil {
		return
	}
	*field = grownValue(*field + delta)
	f.Bars[role] = held
}
