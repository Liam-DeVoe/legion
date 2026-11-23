package replay

import (
	"io"
	"maps"
	"slices"
	"strings"
)

// The kinds of number a forecast across a recorded change of the rule compares (spec R5): rates per
// hour, averages over time and shares. The two windows differ in length and in work, so a total,
// which grows with the window, is never compared.
const (
	QuantityRate    = "rate per hour"
	QuantityAverage = "average over time"
	QuantityShare   = "share"
)

// LoopsAll is the name of the total of all send-back loops, every cause of Causes together, in a
// forecast: the one place a cause the workflow change redefines is scored (plan D3).
const LoopsAll = "loops[all]"

// quantity is one number a forecast compares: its name, its kind, R5's gap of the measure it is
// read from, the send-back causes it counts (for a loop's), and how it is read from a scorecard.
type quantity struct {
	name   string
	kind   string
	gap    Gap
	causes []Cause
	of     func(Scorecard) float64
}

// forecastQuantities is every number a forecast across the change compares, read the same way from
// each of cards: the issues finished and the slots given out per hour; the two readings of started
// work without a slot, the issues past their review with a slot and without, the issues in flight
// and the slots held with a worker in a turn and with none, each as an average over time; the share
// of priority taken; the rounds and hours per hour of all send-back loops together and of each
// cause both releases define the same way apart (ForecastGroups); and the count and hours per hour
// of the waits on another issue by each word that ended one in any of cards.
func forecastQuantities(cards ...Scorecard) []quantity {
	perHour := func(card Scorecard, total float64) float64 {
		hours := card.To.Sub(card.From).Hours()
		if hours <= 0 {
			return 0
		}
		return total / hours
	}
	rate := func(name string, gap Gap, causes []Cause, total func(Scorecard) float64) quantity {
		return quantity{name: name + " per hour", kind: QuantityRate, gap: gap, causes: causes,
			of: func(card Scorecard) float64 { return perHour(card, total(card)) }}
	}
	average := func(name string, gap Gap, of func(Scorecard) float64) quantity {
		return quantity{name: name, kind: QuantityAverage, gap: gap, of: of}
	}
	loops := func(causes []Cause, of func(LoopHours) float64) func(Scorecard) float64 {
		return func(card Scorecard) float64 {
			total := 0.0
			for _, cause := range causes {
				total += of(card.Loops[string(cause)])
			}
			return total
		}
	}
	rounds := func(l LoopHours) float64 { return float64(l.Rounds) }
	hours := func(l LoopHours) float64 { return l.Hours }
	// loopRates is the rounds and the hours per hour of the send-back loops of causes, named name.
	loopRates := func(name string, causes []Cause) []quantity {
		return []quantity{rate(name+".rounds", gapThirty, causes, loops(causes, rounds)), rate(name+".hours", gapThirty, causes, loops(causes, hours))}
	}
	quantities := []quantity{
		rate("done", gapTenth, nil, func(card Scorecard) float64 { return float64(card.Done) }),
		rate("slotsGiven", gapTenth, nil, func(card Scorecard) float64 {
			total := 0
			for _, given := range card.SlotsGivenByDay {
				total += given
			}
			return float64(total)
		}),
		average("waitOnlyForSlot.average", gapThirty, func(card Scorecard) float64 { return card.WaitOnlyForSlot.Average }),
		average("startedWithoutSlot.average", gapThirty, func(card Scorecard) float64 { return card.StartedWithoutSlot.Average }),
		average("pastReviewHolding.average", gapThirty, func(card Scorecard) float64 { return card.PastReviewHolding.Average }),
		average("pastReviewNotHolding.average", gapThirty, func(card Scorecard) float64 { return card.PastReviewNotHolding.Average }),
		average("inFlight.average", gapThirty, func(card Scorecard) float64 { return card.InFlight.Average }),
		average("slotHoursInTurn per hour", gapThirty, func(card Scorecard) float64 { return perHour(card, card.SlotHoursInTurn) }),
		average("slotHoursNoTurn per hour", gapThirty, func(card Scorecard) float64 { return perHour(card, card.SlotHoursNoTurn) }),
		{name: "priorityTaken", kind: QuantityShare, gap: gapPriority, of: func(card Scorecard) float64 { return card.PriorityTaken }},
	}
	quantities = append(quantities, loopRates(LoopsAll, Causes)...)
	for _, cause := range ForecastGroups[GroupSame] {
		quantities = append(quantities, loopRates("loops["+string(cause)+"]", []Cause{cause})...)
	}
	words := map[string]struct{}{}
	for _, card := range cards {
		for word := range card.Waits {
			words[word] = struct{}{}
		}
	}
	for _, word := range slices.Sorted(maps.Keys(words)) {
		quantities = append(quantities,
			rate("waits["+word+"].count", gapThirty, nil, func(card Scorecard) float64 { return float64(card.Waits[word].Count) }),
			rate("waits["+word+"].hours", gapThirty, nil, func(card Scorecard) float64 { return card.Waits[word].Hours }))
	}
	return quantities
}

// ForecastCheck is one number of a forecast across a recorded change of the rule: its name and
// kind, the send-back causes it counts, the forecast's value (the mean over the seeds, with the
// lowest and the highest seed's), the value in the record of the window after the change, twice
// R5's gap, and whether the forecast stands within it.
type ForecastCheck struct {
	Quantity string  `json:"quantity"`
	Kind     string  `json:"kind"`
	Causes   []Cause `json:"causes,omitempty"`
	Forecast float64 `json:"forecast"`
	Low      float64 `json:"low"`
	High     float64 `json:"high"`
	After    float64 `json:"after"`
	Gap      Gap     `json:"gap"`
	Pass     bool    `json:"pass"`
}

// ForecastResult is a forecast across a recorded change of the rule (spec R5): the window before the
// change replayed under the rule after it, on one branch with its seeds, against the record of the
// window after the change.
type ForecastResult struct {
	Before string          `json:"before"`
	After  string          `json:"after"`
	Rule   string          `json:"rule"`
	Branch BranchAt        `json:"branch"`
	Seeds  []int64         `json:"seeds"`
	Checks []ForecastCheck `json:"checks"`
	// FromCommit and ToCommit are the commits the daemons of the window before and of the window
	// after ran, whose workflow changes between them the forecast carries too (plan D1).
	FromCommit string `json:"fromCommit"`
	ToCommit   string `json:"toCommit"`
}

// Pass says whether every number of the forecast stands within twice its gap.
func (f ForecastResult) Pass() bool {
	return !slices.ContainsFunc(f.Checks, func(c ForecastCheck) bool { return !c.Pass })
}

// ForecastChecks compares forecasts, the replays of the window before a recorded change of the rule
// under the rule after it, one scorecard a seed, with after, the record of the window after the
// change (spec R5): on rates per hour, averages over time and shares, never on totals, each within
// twice R5's gap of after's value. A cause the change redefines is scored only inside the total of
// all send-back loops, and each cause both releases define the same way apart (plan D3). Both
// sides are read from line timelines, as R5's validation reads them.
func ForecastChecks(after Scorecard, forecasts []Scorecard) []ForecastCheck {
	quantities := forecastQuantities(append([]Scorecard{after}, forecasts...)...)
	checks := make([]ForecastCheck, 0, len(quantities))
	for _, q := range quantities {
		values := make([]float64, len(forecasts))
		for i, card := range forecasts {
			values[i] = q.of(card)
		}
		mean, low, high := spread(values)
		check := ForecastCheck{Quantity: q.name, Kind: q.kind, Causes: q.causes, Forecast: mean, Low: low, High: high,
			After: q.of(after), Gap: q.gap.Twice()}
		check.Pass = check.Gap.Within(check.After, check.Forecast)
		checks = append(checks, check)
	}
	return checks
}

// Write prints f as Markdown, its headings at depth: what was forecast from what, the commits the
// forecast carries beside the rule (plan D1), each cause of each group of the send-back causes and
// how it is scored (plan D3), every number with its forecast, its value after the change, the
// difference, the allowance and the test, and whether the forecast passes.
func (f ForecastResult) Write(w io.Writer, depth int) error {
	p := &printer{w: w}
	p.line("%s %s under `%s`, against the record of %s", strings.Repeat("#", depth), f.Before, f.Rule, f.After)
	p.line("")
	p.line("The window before the change, %s, is replayed under the rule after it, `%s`, on the branch `%s` with the seeds %s, and compared with the record of the window after the change, %s.",
		f.Before, f.Rule, f.Branch.Label(), seedList(f.Seeds), f.After)
	if f.FromCommit != f.ToCommit {
		p.line("The forecast also carries the workflow commits between `%s` and `%s`, the commits the two windows' daemons ran, so a gap may come from those commits as well as from the rule (plan D1).",
			f.FromCommit, f.ToCommit)
	} else {
		p.line("The two windows' daemons ran the one commit `%s`, so the forecast carries no workflow commit beside the rule (plan D1).", f.FromCommit)
	}
	p.line("It compares rates per hour, averages over time and shares, never totals, since the windows differ in length and in work, and each passes within twice R5's gap of its value after the change.")
	p.line("The causes the workflow change redefines (%s): %s; each is scored only inside the total of all send-back loops, `%s`.",
		GroupRedefined, codeList(ForecastGroups[GroupRedefined]), LoopsAll)
	p.line("The causes both releases define the same way (%s): %s; each is scored apart, and inside the total too.",
		GroupSame, codeList(ForecastGroups[GroupSame]))
	p.line("")
	p.line("| quantity | kind | causes | forecast (lowest, highest seed) | after the change | difference | allowed | test |")
	p.line("| --- | --- | --- | --- | --- | --- | --- | --- |")
	for _, c := range f.Checks {
		test := passWord(c.Pass)
		p.line("| %s | %s | %s | %s | %s | %s | %s (%s) | %s |", c.Quantity, c.Kind, codeList(c.Causes), spreadCell(c.Forecast, c.Low, c.High, len(f.Seeds)),
			formatNumber(c.After), formatNumber(c.Forecast-c.After), formatNumber(c.Gap.Allowed(c.After)), c.Gap, test)
	}
	p.line("")
	if f.Pass() {
		p.line("The forecast passes: every number stands within twice its gap.")
	} else {
		var failed []string
		for _, c := range f.Checks {
			if !c.Pass {
				failed = append(failed, c.Quantity)
			}
		}
		p.line("The forecast fails on %s: the tool fails its check against a recorded change of the rule, and is fixed and checked again on a third window before it compares rules (R5).",
			strings.Join(failed, ", "))
	}
	return p.err
}
