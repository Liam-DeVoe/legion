package replay

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/testpg/testdb"
)

// compareFrom is the start of every scorecard these tests make up: a window of compareHours hours.
var compareFrom = time.Date(2001, 3, 5, 9, 0, 0, 0, time.UTC)

// compareHours is the span of the windows these tests make up.
const compareHours = 6

// sampleCard is a scorecard over [compareFrom, compareFrom+hours) with a value in every measure of
// R4 and three issues with times: ACME-1 with a first slot at the start and a done two hours on,
// ACME-2 with a first slot half an hour on, ACME-3 with none.
func sampleCard(hours int) Scorecard {
	day := compareFrom.Format(time.DateOnly)
	return Scorecard{
		Source: "replay", From: compareFrom, To: compareFrom.Add(time.Duration(hours) * time.Hour),
		Done: 20, WorkLeft: 3,
		WaitOnlyForSlot:      Occupancy{Average: 2, Max: 4, Hours: 10},
		StartedWithoutSlot:   Occupancy{Average: 3, Max: 5, Hours: 15},
		PastReviewHolding:    Occupancy{Average: 1, Max: 2, Hours: 5},
		PastReviewNotHolding: Occupancy{Average: 1.5, Max: 3, Hours: 7.5},
		PriorityTaken:        0.2,
		Loops: map[string]LoopHours{string(CauseReviewChanges): {Rounds: 4, Hours: 10}, string(CauseConflict): {Rounds: 2, Hours: 10},
			string(CauseSendBack): {Rounds: 3, Hours: 6}, string(CauseTestFailed): {Rounds: 2, Hours: 4}},
		Waits:           map[string]WaitHours{string(WaitLinkRemoved): {Count: 2, Hours: 8}, string(WaitOpen): {Count: 1, Hours: 3}},
		SlotHoursInTurn: 10, SlotHoursNoTurn: 5,
		InFlight:              Occupancy{Average: 6, Max: 8, Hours: 30},
		SlotsGivenByDay:       map[string]int{day: 10},
		MedianSlotWaitMinutes: 10,
		FirstSlot:             map[string]time.Time{"ACME-1": compareFrom, "ACME-2": compareFrom.Add(30 * time.Minute)},
		DoneAt:                map[string]time.Time{"ACME-1": compareFrom.Add(2 * time.Hour)},
		Assumptions:           []int{},
		Scenario:              MainBranch.NeverStarted,
	}
}

// Each gap of R5 passes a replay at its bound and fails one past it (spec R5): 10% of the issues
// finished and of the slots given out on each day, 30% of the median wait for a slot, of the hours
// of started work that waits only for a slot and of every other measure, 0.05 of the share of
// priority taken, and 60 minutes at the median of each issue's first-slot error; a measure the
// record lacks stands at 0 there, so any replayed value of it fails; and the numbers R5 tests not —
// the issues the start state could not place and the first day apart — fail no branch.
func TestValidationGaps(t *testing.T) {
	day := compareFrom.Format(time.DateOnly)
	shiftFirstSlots := func(minutes float64) func(*Scorecard) {
		return func(card *Scorecard) {
			shifted := map[string]time.Time{}
			for key, at := range card.FirstSlot {
				shifted[key] = at.Add(time.Duration(minutes * float64(time.Minute)))
			}
			card.FirstSlot = shifted
		}
	}
	cases := []struct {
		name    string
		measure string
		bound   func(*Scorecard)
		past    func(*Scorecard)
	}{
		{"issues finished, 10%", "done", func(c *Scorecard) { c.Done = 22 }, func(c *Scorecard) { c.Done = 23 }},
		{"issues finished below, 10%", "done", func(c *Scorecard) { c.Done = 18 }, func(c *Scorecard) { c.Done = 17 }},
		{"slots given out on a day, 10%", "slotsGivenByDay[" + day + "]",
			func(c *Scorecard) { c.SlotsGivenByDay = map[string]int{day: 11} }, func(c *Scorecard) { c.SlotsGivenByDay = map[string]int{day: 12} }},
		{"median wait for a slot, 30%", "medianSlotWaitMinutes",
			func(c *Scorecard) { c.MedianSlotWaitMinutes = 13 }, func(c *Scorecard) { c.MedianSlotWaitMinutes = 13.01 }},
		{"hours waiting only for a slot, 30%", "waitOnlyForSlot.hours",
			func(c *Scorecard) { c.WaitOnlyForSlot.Hours = 13 }, func(c *Scorecard) { c.WaitOnlyForSlot.Hours = 13.01 }},
		{"priority taken, 0.05", "priorityTaken", func(c *Scorecard) { c.PriorityTaken = 0.25 }, func(c *Scorecard) { c.PriorityTaken = 0.2501 }},
		{"priority taken below, 0.05", "priorityTaken", func(c *Scorecard) { c.PriorityTaken = 0.15 }, func(c *Scorecard) { c.PriorityTaken = 0.1499 }},
		{"each issue's first slot, 60 minutes at the median", FirstSlotTest, shiftFirstSlots(60), shiftFirstSlots(61)},
		{"another measure, 30%", "slotHoursInTurn", func(c *Scorecard) { c.SlotHoursInTurn = 13 }, func(c *Scorecard) { c.SlotHoursInTurn = 13.01 }},
		{"a loop's hours below, 30%", "loops[conflict].hours",
			func(c *Scorecard) { c.Loops[string(CauseConflict)] = LoopHours{Rounds: 2, Hours: 7} },
			func(c *Scorecard) { c.Loops[string(CauseConflict)] = LoopHours{Rounds: 2, Hours: 6.99} }},
		{"a measure the record lacks", "loops[queue_drop].rounds",
			func(c *Scorecard) {}, func(c *Scorecard) { c.Loops[string(CauseQueueDrop)] = LoopHours{Rounds: 1} }},
	}
	clone := func(card Scorecard) Scorecard {
		card.Loops, card.Waits, card.SlotsGivenByDay = maps.Clone(card.Loops), maps.Clone(card.Waits), maps.Clone(card.SlotsGivenByDay)
		card.FirstSlot, card.DoneAt = maps.Clone(card.FirstSlot), maps.Clone(card.DoneAt)
		return card
	}
	recorded := sampleCard(compareHours)
	recorded.Source = "record"
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			atBound := clone(recorded)
			c.bound(&atBound)
			if got := ValidateBranch(recorded, MainBranchAt, []Scorecard{atBound}); !got.Pass() {
				t.Errorf("a replay at the bound of %s fails %v; it should pass", c.measure, got.Failed)
			}
			past := clone(recorded)
			c.past(&past)
			if got := ValidateBranch(recorded, MainBranchAt, []Scorecard{past}); !slices.Equal(got.Failed, []string{c.measure}) {
				t.Errorf("a replay past the bound of %s fails %v; it should fail %s alone", c.measure, got.Failed, c.measure)
			}
		})
	}
	t.Run("numbers no test of R5 reads", func(t *testing.T) {
		replay := clone(recorded)
		replay.Unplaced = 40
		first := sampleCard(compareHours)
		first.Done = 100
		replay.FirstDayApart = &first
		got := ValidateBranch(recorded, MainBranchAt, []Scorecard{replay})
		if !got.Pass() {
			t.Errorf("a replay that differs from the record only in its unplaced issues and its first day apart fails %v", got.Failed)
		}
		for _, check := range got.Measures {
			if (check.Measure == "unplaced" || strings.HasPrefix(check.Measure, firstDayPrefix)) && check.Tested {
				t.Errorf("the validation tests %s, which no gap of R5 names", check.Measure)
			}
		}
	})
}

// A validation prints, for each branch, every measure of R4 with its recorded value, its replayed
// value (the mean of the seeds, with the lowest and the highest seed's) and the gap, and each
// issue's error in its first slot and in its done with their median and 90th percentile (spec R5).
// The errors are worked by hand: over a six-hour window, ACME-1's first slot is 10 and 20 minutes
// late in the two seeds (15) and its done 20 early and 10 late (15); ACME-2's first slot is on
// time and an hour late (30); ACME-3 holds no first slot in the record, so the record's stands at
// the window's end, 360 minutes on, and the seeds' at 60 and at the end (150). The median of 15,
// 30 and 150 is 30 and the 90th percentile by the nearest rank is 150; the one done error is 15.
func TestValidationPrintsEveryMeasureAndIssueError(t *testing.T) {
	recorded := sampleCard(compareHours)
	recorded.Source = "record"
	seed := func(n int64, firsts map[string]float64, done float64, doneAt bool) Scorecard {
		card := sampleCard(compareHours)
		card.Seed, card.Done, card.PriorityTaken = n, 20+int(n), 0.2+0.01*float64(n)
		card.Loops = maps.Clone(card.Loops)
		card.Loops[string(CauseQueueDrop)] = LoopHours{Rounds: int(n), Hours: float64(n)}
		card.FirstSlot = map[string]time.Time{}
		for key, minutes := range firsts {
			card.FirstSlot[key] = compareFrom.Add(time.Duration(minutes) * time.Minute)
		}
		card.DoneAt = map[string]time.Time{}
		if doneAt {
			card.DoneAt["ACME-1"] = compareFrom.Add(time.Duration(done) * time.Minute)
		}
		return card
	}
	seeds := []Scorecard{
		seed(1, map[string]float64{"ACME-1": 10, "ACME-2": 30, "ACME-3": 60}, 100, true),
		seed(2, map[string]float64{"ACME-1": 20, "ACME-2": 90}, 130, true),
	}
	branch := ValidateBranch(recorded, MainBranchAt, seeds)
	if branch.FirstSlot != (ErrorSummary{Issues: 3, Median: 30, P90: 150}) || branch.Done != (ErrorSummary{Issues: 1, Median: 15, P90: 15}) {
		t.Errorf("the issues' errors sum up as first slot %+v and done %+v, want {3 30 150} and {1 15 15}", branch.FirstSlot, branch.Done)
	}
	var out bytes.Buffer
	v := Validation{Window: "acme-window", Rule: "main", Record: recorded, Branches: []BranchValidation{branch}}
	if err := v.Write(&out, 1, func(BranchAt) bool { return true }); err != nil {
		t.Fatal(err)
	}
	printed := out.String()
	recordedValues := valuesOf(recorded)
	perSeed := []map[string]float64{valuesOf(seeds[0]), valuesOf(seeds[1])}
	names := measureNames(recorded, seeds[0], seeds[1])
	if !slices.Contains(names, "loops[queue_drop].rounds") {
		t.Fatalf("the measures %v leave out the loop only the replay holds", names)
	}
	for _, name := range names {
		a, b := perSeed[0][name], perSeed[1][name]
		mean := (a + b) / 2
		row := fmt.Sprintf("| %s | %s | %s | %s |", name, formatNumber(recordedValues[name]), spreadCell(mean, min(a, b), max(a, b), 2),
			formatNumber(mean-recordedValues[name]))
		if !strings.Contains(printed, row) {
			t.Errorf("the validation prints no row %q for the measure %s", row, name)
		}
	}
	for _, row := range []string{"| ACME-1 | 15 | 15 |", "| ACME-2 | 30 | none |", "| ACME-3 | 150 | none |",
		"the first slot over 3 issues, median 30 and 90th percentile 150; the done over 1 issues, median 15 and 90th percentile 15"} {
		if !strings.Contains(printed, row) {
			t.Errorf("the validation prints no %q:\n%s", row, printed)
		}
	}
}

// A branch whose replay of the recorded rule fails a test of R5 gives no comparison, and a window
// whose main branch fails gives none at all; the validation names each such branch (spec R5).
func TestBranchThatFailsGivesNoComparison(t *testing.T) {
	recorded := sampleCard(compareHours)
	good := sampleCard(compareHours)
	bad := sampleCard(compareHours)
	bad.Done = 40
	wall, err := BranchNamed("main+timing=wall")
	if err != nil {
		t.Fatal(err)
	}
	failing := BranchAt{Branch: wall, Slowdown: 1}
	slower := BranchAt{Branch: MainBranch, Slowdown: 1.2}
	validation := func(main Scorecard) Validation {
		return Validation{Window: "acme-window", Rule: "main", Record: recorded, Branches: []BranchValidation{
			ValidateBranch(recorded, MainBranchAt, []Scorecard{main}),
			ValidateBranch(recorded, failing, []Scorecard{bad}),
			ValidateBranch(recorded, slower, []Scorecard{good}),
		}}
	}
	labels := func(branches []BranchAt) []string {
		var out []string
		for _, at := range branches {
			out = append(out, at.Label())
		}
		return out
	}
	t.Run("a failing branch", func(t *testing.T) {
		v := validation(good)
		if !v.MainPasses() || !v.Compares(slower) || v.Compares(failing) {
			t.Errorf("main passes %t, %s compares %t and %s compares %t; want true, true and false", v.MainPasses(), slower.Label(),
				v.Compares(slower), failing.Label(), v.Compares(failing))
		}
		if got := labels(v.NoComparison()); !slices.Equal(got, []string{failing.Label()}) {
			t.Errorf("the branches that give no comparison are %v, want [%s]", got, failing.Label())
		}
		var out bytes.Buffer
		if err := v.Write(&out, 1, nil); err != nil {
			t.Fatal(err)
		}
		for _, line := range []string{"The window's main branch (main@1) passes every test of R5.",
			"The branches that give no comparison: `main+timing=wall@1`."} {
			if !strings.Contains(out.String(), line) {
				t.Errorf("the validation prints no %q:\n%s", line, out.String())
			}
		}
	})
	t.Run("a failing main branch", func(t *testing.T) {
		v := validation(bad)
		if v.MainPasses() || v.Compares(slower) {
			t.Errorf("main passes %t and %s compares %t; a window whose main branch fails gives no comparison at all", v.MainPasses(), slower.Label(),
				v.Compares(slower))
		}
		if got, want := labels(v.NoComparison()), []string{MainBranchAt.Label(), failing.Label(), slower.Label()}; !slices.Equal(got, want) {
			t.Errorf("the branches that give no comparison are %v, want %v", got, want)
		}
		var out bytes.Buffer
		if err := v.Write(&out, 1, nil); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "So the window gives no comparison at all") {
			t.Errorf("the validation does not say the window gives no comparison at all:\n%s", out.String())
		}
	})
}

// A forecast across a recorded change compares rates per hour, averages over time and shares, never
// totals, each within twice its gap (spec R5): a window half as long with half the work forecasts
// the same rates, and each kind passes at twice its gap and fails past it.
func TestForecastScoresRatesNeverTotals(t *testing.T) {
	after := sampleCard(100)
	after.Done, after.SlotsGivenByDay = 100, map[string]int{"2001-03-05": 100, "2001-03-06": 100}
	after.Loops = map[string]LoopHours{string(CauseReviewChanges): {Rounds: 4, Hours: 10}, string(CauseConflict): {Rounds: 2, Hours: 10},
		string(CauseSendBack): {Rounds: 4, Hours: 6}, string(CauseTestFailed): {Rounds: 2, Hours: 4}}
	after.Waits = map[string]WaitHours{string(WaitLinkRemoved): {Count: 2, Hours: 8}, string(WaitOpen): {Count: 2, Hours: 4}}
	half := func() Scorecard {
		card := sampleCard(50)
		card.Done, card.SlotsGivenByDay = 50, map[string]int{"2001-03-05": 100}
		card.Loops = map[string]LoopHours{}
		for cause, loop := range after.Loops {
			card.Loops[cause] = LoopHours{Rounds: loop.Rounds / 2, Hours: loop.Hours / 2}
		}
		card.Waits = map[string]WaitHours{}
		for word, wait := range after.Waits {
			card.Waits[word] = WaitHours{Count: wait.Count / 2, Hours: wait.Hours / 2}
		}
		card.SlotHoursInTurn, card.SlotHoursNoTurn = after.SlotHoursInTurn/2, after.SlotHoursNoTurn/2
		return card
	}
	kinds := []string{QuantityRate, QuantityAverage, QuantityShare}
	checkOf := func(checks []ForecastCheck, name string) ForecastCheck {
		i := slices.IndexFunc(checks, func(c ForecastCheck) bool { return c.Quantity == name })
		if i < 0 {
			t.Fatalf("the forecast holds no %s", name)
		}
		return checks[i]
	}
	checks := ForecastChecks(after, []Scorecard{half()})
	for _, c := range checks {
		if !slices.Contains(kinds, c.Kind) {
			t.Errorf("the forecast scores %s as %q, which is no rate per hour, average over time or share", c.Quantity, c.Kind)
		}
		if !c.Pass {
			t.Errorf("the forecast fails %s (%v against %v), though the window half as long has half the work", c.Quantity, c.Forecast, c.After)
		}
	}
	for _, total := range []string{"done", "slotsGivenByDay[2001-03-05]", "waitOnlyForSlot.hours", "loops[review_changes].rounds", "slotHoursInTurn"} {
		if slices.ContainsFunc(checks, func(c ForecastCheck) bool { return c.Quantity == total }) {
			t.Errorf("the forecast scores the total %s", total)
		}
	}
	bounds := []struct {
		name       string
		bound      func(*Scorecard)
		past       func(*Scorecard)
		kind, want string
	}{
		{"done per hour", func(c *Scorecard) { c.Done = 60 }, func(c *Scorecard) { c.Done = 61 }, QuantityRate, "20%"},
		{"waitOnlyForSlot.average", func(c *Scorecard) { c.WaitOnlyForSlot.Average = 3.2 }, func(c *Scorecard) { c.WaitOnlyForSlot.Average = 3.21 },
			QuantityAverage, "60%"},
		{"priorityTaken", func(c *Scorecard) { c.PriorityTaken = 0.3 }, func(c *Scorecard) { c.PriorityTaken = 0.3001 }, QuantityShare, "0.1"},
	}
	for _, b := range bounds {
		atBound, past := half(), half()
		b.bound(&atBound)
		b.past(&past)
		check := checkOf(ForecastChecks(after, []Scorecard{atBound}), b.name)
		if !check.Pass || check.Kind != b.kind || check.Gap.String() != b.want {
			t.Errorf("%s at twice its gap: pass %t, kind %s, gap %s; want a pass, %s, %s", b.name, check.Pass, check.Kind, check.Gap, b.kind, b.want)
		}
		if check := checkOf(ForecastChecks(after, []Scorecard{past}), b.name); check.Pass {
			t.Errorf("%s past twice its gap passes (%v against %v)", b.name, check.Forecast, check.After)
		}
	}
}

// A forecast scores the causes the workflow change redefines only inside the total of all
// send-back loops and each cause both releases define the same way apart, and names each cause in
// each group (plan D3): a forecast whose loops are all red checks where the window after had
// conflicts and send-backs passes on the total, holds no number of a redefined cause, and fails a
// cause defined the same way that it misses.
func TestForecastGroupsTheRedefinedCauses(t *testing.T) {
	after := sampleCard(compareHours)
	after.Loops = map[string]LoopHours{string(CauseConflict): {Rounds: 10, Hours: 50}, string(CauseSendBack): {Rounds: 10, Hours: 50},
		string(CauseReviewChanges): {Rounds: 5, Hours: 20}, string(CauseTestFailed): {Rounds: 5, Hours: 10}}
	forecast := sampleCard(compareHours)
	forecast.Loops = map[string]LoopHours{string(CauseRedChecksOrConflict): {Rounds: 20, Hours: 100},
		string(CauseReviewChanges): {Rounds: 5, Hours: 20}, string(CauseTestFailed): {Rounds: 5, Hours: 10}}
	checks := ForecastChecks(after, []Scorecard{forecast})
	for _, c := range checks {
		if !c.Pass {
			t.Errorf("the forecast fails %s (%v against %v): a cause the change redefines is scored only inside the total", c.Quantity, c.Forecast, c.After)
		}
		for _, cause := range ForecastGroups[GroupRedefined] {
			if strings.HasPrefix(c.Quantity, "loops["+string(cause)+"]") {
				t.Errorf("the forecast scores %s apart, a cause the workflow change redefines", c.Quantity)
			}
		}
	}
	for _, name := range []string{LoopsAll + ".rounds per hour", LoopsAll + ".hours per hour"} {
		i := slices.IndexFunc(checks, func(c ForecastCheck) bool { return c.Quantity == name })
		if i < 0 || !slices.Equal(checks[i].Causes, Causes) {
			t.Errorf("the forecast holds no %s over every cause", name)
		}
	}
	for _, cause := range ForecastGroups[GroupSame] {
		for _, suffix := range []string{".rounds per hour", ".hours per hour"} {
			name := "loops[" + string(cause) + "]" + suffix
			if !slices.ContainsFunc(checks, func(c ForecastCheck) bool { return c.Quantity == name }) {
				t.Errorf("the forecast holds no %s: each cause both releases define the same way is scored apart", name)
			}
		}
	}
	missed := forecast
	missed.Loops = maps.Clone(forecast.Loops)
	missed.Loops[string(CauseReviewChanges)] = LoopHours{Rounds: 20, Hours: 20}
	missedChecks := ForecastChecks(after, []Scorecard{missed})
	if i := slices.IndexFunc(missedChecks, func(c ForecastCheck) bool { return c.Quantity == "loops[review_changes].rounds per hour" }); i < 0 || missedChecks[i].Pass {
		t.Errorf("a forecast four times the review rounds of the window after passes them")
	}
	var out bytes.Buffer
	result := ForecastResult{Before: "window-two", After: "window-one", Rule: "legion-v20.42.5", Branch: MainBranchAt, Seeds: []int64{1}, Checks: checks,
		FromCommit: "1111111111111111111111111111111111111111", ToCommit: "2222222222222222222222222222222222222222"}
	if err := result.Write(&out, 1); err != nil {
		t.Fatal(err)
	}
	for _, sentence := range []string{
		"The causes the workflow change redefines (redefined): `red_checks_or_conflict`, `conflict`, `send_back`; each is scored only inside the total of all send-back loops, `loops[all]`.",
		"The causes both releases define the same way (same): `review_changes`, `test_failed`, `queue_drop`, `other_backward`; each is scored apart",
		"The forecast also carries the workflow commits between `1111111111111111111111111111111111111111` and `2222222222222222222222222222222222222222`",
	} {
		if !strings.Contains(out.String(), sentence) {
			t.Errorf("the forecast prints no %q:\n%s", sentence, out.String())
		}
	}
}

// A comparison names a rule best only when, on every branch of both windows that passes R5, it is
// no worse than main by more than R5's gap on the issues finished and on each of the owner's three
// costs, and better on at least one (spec R7). Main's costs are 100 issues finished, 50 hours
// waiting only for a slot, 0.2 of priority taken and 400 hours of other delays.
func TestBestRuleDecision(t *testing.T) {
	main := Costs{Done: 100, WaitOnlyForSlot: 50, PriorityTaken: 0.2, OtherDelays: 400}
	with := func(change func(*Costs)) Costs {
		c := main
		change(&c)
		return c
	}
	branch := func(window string, at BranchAt, passes bool, costs map[string]Costs) BranchCosts {
		all := map[string]Costs{record.MainRule.Name: main}
		maps.Copy(all, costs)
		return BranchCosts{Window: window, Branch: at, Passes: passes, Costs: all}
	}
	slower := BranchAt{Branch: MainBranch, Slowdown: 1.6}
	both := func(passes [4]bool, costs map[string]Costs) []BranchCosts {
		return []BranchCosts{branch("one", MainBranchAt, passes[0], costs), branch("one", slower, passes[1], costs),
			branch("two", MainBranchAt, passes[2], costs), branch("two", slower, passes[3], costs)}
	}
	allPass := [4]bool{true, true, true, true}
	trade := func(d Decision, rule string) Trade {
		i := slices.IndexFunc(d.Trades, func(tr Trade) bool { return tr.Rule == rule })
		if i < 0 {
			t.Fatalf("the decision gives no trade of %s", rule)
		}
		return d.Trades[i]
	}
	t.Run("one rule best", func(t *testing.T) {
		d := Decide([]string{"main", "keep-past-review", "cover"}, both(allPass, map[string]Costs{
			"keep-past-review": with(func(c *Costs) { c.Done = 105 }),
			"cover":            with(func(c *Costs) { c.Done, c.PriorityTaken = 105, 0.26 }),
		}))
		if !slices.Equal(d.Best, []string{"keep-past-review"}) || d.Branches != 4 {
			t.Errorf("the decision names %v best over %d branches, want [keep-past-review] over 4", d.Best, d.Branches)
		}
		if got := trade(d, "cover").Measures[2]; got != (MeasureTrade{Measure: "priorityTaken", Worse: 4}) {
			t.Errorf("cover's trade on priority taken is %+v, want worse on all 4 branches", got)
		}
	})
	t.Run("none best, with each trade", func(t *testing.T) {
		d := Decide([]string{"keep-past-review", "returned-by-rank"}, both(allPass, map[string]Costs{
			"keep-past-review": with(func(c *Costs) { c.Done, c.WaitOnlyForSlot = 110, 70 }),
			"returned-by-rank": with(func(c *Costs) { c.Done = 85 }),
		}))
		if len(d.Best) != 0 {
			t.Errorf("the decision names %v best, though each rule is worse than main by more than R5's gap on one measure", d.Best)
		}
		want := map[string][]MeasureTrade{
			"keep-past-review": {{Measure: "done", Better: 4}, {Measure: "waitOnlyForSlot.hours", Worse: 4},
				{Measure: "priorityTaken", Level: 4}, {Measure: "otherDelays.hours", Level: 4}},
			"returned-by-rank": {{Measure: "done", Worse: 4}, {Measure: "waitOnlyForSlot.hours", Level: 4},
				{Measure: "priorityTaken", Level: 4}, {Measure: "otherDelays.hours", Level: 4}},
		}
		for rule, measures := range want {
			if got := trade(d, rule).Measures; !slices.Equal(got, measures) {
				t.Errorf("%s's trade is %+v, want %+v", rule, got, measures)
			}
		}
	})
	t.Run("better on one cost and worse on another", func(t *testing.T) {
		d := Decide([]string{"blockers-first", "cover"}, both(allPass, map[string]Costs{
			"blockers-first": with(func(c *Costs) { c.WaitOnlyForSlot, c.OtherDelays = 40, 520.5 }),
			"cover":          with(func(c *Costs) { c.WaitOnlyForSlot, c.OtherDelays = 40, 520 }),
		}))
		if !slices.Equal(d.Best, []string{"cover"}) {
			t.Errorf("the decision names %v best; blockers-first is worse than main past R5's gap on the other delays and cover is at it, so cover alone", d.Best)
		}
		if got := standingOf(with(func(c *Costs) { c.WaitOnlyForSlot, c.OtherDelays = 40, 520.5 }), main); got != StandingTrade {
			t.Errorf("a rule better on one cost and worse on another stands %s against main, want %s", got, StandingTrade)
		}
	})
	t.Run("a branch that failed R5 cannot name a rule", func(t *testing.T) {
		betterOnFailed := []BranchCosts{
			branch("one", MainBranchAt, true, map[string]Costs{"cover": main}),
			branch("one", slower, false, map[string]Costs{"cover": with(func(c *Costs) { c.Done = 120 })}),
		}
		if d := Decide([]string{"cover"}, betterOnFailed); len(d.Best) != 0 || d.Branches != 1 {
			t.Errorf("a rule better only on a branch that failed R5 is named best (%v over %d branches)", d.Best, d.Branches)
		}
		worseOnFailed := []BranchCosts{
			branch("one", MainBranchAt, true, map[string]Costs{"cover": with(func(c *Costs) { c.Done = 105 })}),
			branch("one", slower, false, map[string]Costs{"cover": with(func(c *Costs) { c.Done = 50 })}),
		}
		if d := Decide([]string{"cover"}, worseOnFailed); !slices.Equal(d.Best, []string{"cover"}) {
			t.Errorf("a rule better on every branch that passes R5 is not named best (%v): a branch that failed is read", d.Best)
		}
		if d := Decide([]string{"cover"}, both([4]bool{}, map[string]Costs{"cover": with(func(c *Costs) { c.Done = 120 })})); len(d.Best) != 0 || d.Branches != 0 {
			t.Errorf("with no branch that passes R5 the decision names %v best over %d branches", d.Best, d.Branches)
		}
	})
}

// The break-even search runs the main branch from 1.0 to 2.0 by 0.1, and a rule's break-even factor
// is the first at which its standing against main differs from its standing at 1.0, or none (spec
// R3, assumption 1).
func TestBreakEvenFactor(t *testing.T) {
	factors := BreakEvenSlowdowns()
	if want := []float64{1, 1.1, 1.2, 1.3, 1.4, 1.5, 1.6, 1.7, 1.8, 1.9, 2}; !slices.Equal(factors, want) {
		t.Fatalf("the break-even search runs %v, want %v", factors, want)
	}
	main := map[float64]Costs{}
	rule := map[float64]Costs{}
	for _, f := range factors {
		main[f] = Costs{Done: 100, WaitOnlyForSlot: 50, PriorityTaken: 0.2, OtherDelays: 400}
		done := 105.0
		if f >= 1.4 {
			done = 85
		}
		rule[f] = Costs{Done: done, WaitOnlyForSlot: 50, PriorityTaken: 0.2, OtherDelays: 400}
	}
	got := BreakEven(factors, rule, main)
	if !got.Changes || got.Factor != 1.4 || got.Standings[0].Standing != StandingBetter || standingAt(got, 1.4) != StandingWorse {
		t.Errorf("the search gives %+v; want the factor 1.4, better below it and worse from it", got)
	}
	skipped := maps.Clone(rule)
	delete(skipped, 1.4)
	if got := BreakEven(factors, skipped, main); !got.Changes || got.Factor != 1.5 || len(got.Standings) != len(factors)-1 {
		t.Errorf("with no run at 1.4 the search gives %+v; want the factor 1.5 over the ten factors that ran", got)
	}
	steady := map[float64]Costs{}
	for _, f := range factors {
		steady[f] = rule[1]
	}
	if got := BreakEven(factors, steady, main); got.Changes || len(got.Standings) != len(factors) {
		t.Errorf("a rule better at every factor gives %+v; no factor up to 2.0 changes its rank", got)
	}
}

// The document of a comparison holds every rule, window, branch and measure (spec R7; check k6): it
// is written from made-up runs of two windows under every rule the repository keeps, every branch
// with its seeds and the break-even search, and is read for each window's scorecard, validation
// and table of each branch, every rule in each table, every measure in each, what each rule with a
// grace keeps on each window, the forecast with the workflow commits it carries, which window
// measures conflicts, each rule's break-even factor, the decision and the assumptions. With
// LEGION_TEST_PG_DSN set it also replays two fixture windows whole and in parts and holds the two
// documents equal: a document written from one process's runs and one written from the runs of
// parts, each run in a process of its own, are the same.
func TestTheDocumentHasEveryRuleWindowBranchAndMeasure(t *testing.T) {
	dir := t.TempDir()
	to := compareFrom.Add(compareHours * time.Hour)
	recorded := func(name string, order int, rule, commit string) windowRecord {
		return windowRecord{Name: name, Order: order, Project: "ACME", From: compareFrom, To: to, Rule: rule, Scorecard: sampleCard(compareHours),
			Parts: []Part{{From: compareFrom, To: to, Release: "fixture", Commit: commit, Rule: rule}}}
	}
	before := recorded("window-two", 0, "legion-v19.57.3", "1111111111111111111111111111111111111111")
	before.Stretches = 4
	after := recorded("window-one", 1, "legion-v20.42.5", "2222222222222222222222222222222222222222")
	after.Stretches, after.Conflicted = 10, 3
	plan := fullPlan()
	var cards []Scorecard
	for _, w := range []windowRecord{before, after} {
		if err := writeJSON(filepath.Join(dir, w.Name, recordFile), w); err != nil {
			t.Fatal(err)
		}
		for r, rule := range record.SlotRules() {
			part := partRecord{Window: w.Name, Rule: rule.Name, RuleCommit: rule.Commit, Branches: []partRun{}, BreakEven: []partRun{}, Parallel: 1}
			write := func(at BranchAt) partRun {
				seeds := w.seeds(at.Branch, plan.perDraw)
				for _, seed := range seeds {
					snapshot := sampleCard(compareHours)
					snapshot.Rule, snapshot.Branch, snapshot.Seed, snapshot.Slowdown = rule.Name, at.Branch.Name, seed, at.Slowdown
					snapshot.Done += r
					snapshot.PriorityTaken += 0.001 * float64(seed)
					line := snapshot
					line.SlotHoursInTurn += 0.5
					cards = append(cards, snapshot)
					ran := runRecord{Rule: rule.Name, RuleCommit: rule.Commit, Branch: at.Branch, Seed: seed, Slowdown: at.Slowdown, Scenario: at.Branch.NeverStarted,
						Assumptions: []int{1, 3, 4}, Gaps: Gaps(snapshot, line)}
					runDirectory := runDir(dir, w.Name, rule.Name, at, seed)
					for name, value := range map[string]any{runFile: ran, scorecardFile: snapshot, lineScorecardFile: line} {
						if err := writeJSON(filepath.Join(runDirectory, name), value); err != nil {
							t.Fatal(err)
						}
					}
				}
				return partRun{Branch: at.Branch.Name, Slowdown: at.Slowdown, Seeds: seeds}
			}
			for _, at := range plan.branches {
				part.Branches = append(part.Branches, write(at))
			}
			for _, f := range plan.breakEven {
				part.BreakEven = append(part.BreakEven, write(BranchAt{Branch: MainBranch, Slowdown: f}))
			}
			if err := writeJSON(filepath.Join(dir, w.Name, rule.Name, partFile), part); err != nil {
				t.Fatal(err)
			}
		}
	}
	runs, err := ReadRuns(dir)
	if err != nil {
		t.Fatal(err)
	}
	document, err := runs.Comparison()
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, w := range []windowRecord{before, after} {
		want = append(want, "### "+w.Name+"\n", "### "+w.Name+" under `"+w.Rule+"`")
	}
	want = append(want, "## The rule named best (R7)", "## The scorecard of each window, read from its record", "## The validation of each window (R5)",
		"## What each grace keeps, read from each window's record",
		"## The forecast across the change (R5)", "replayed under the rule after it, `legion-v20.42.5`",
		"The forecast also carries the workflow commits between `1111111111111111111111111111111111111111` and `2222222222222222222222222222222222222222`",
		"window-two does not measure how a rule changes conflicts", "window-one measures how a rule changes conflicts",
		"## The gap between the snapshot and the line timelines", "## The break-even slowdown factor of each rule against main",
		"## The assumptions and scenarios each run used", "3. "+Assumptions[2].Text)
	for _, rule := range record.SlotRules() {
		if rule.Name != record.MainRule.Name {
			want = append(want, "| `"+rule.Name+"` | no factor up to 2 changes it")
		}
		want = append(want, "- `"+rule.Name+"`, `main@1.1`")
		if rule.WaitGrace > 0 {
			for _, w := range []windowRecord{before, after} {
				want = append(want, "| "+w.Name+" | `"+rule.Name+"` | "+rule.WaitGrace.String()+" | ")
			}
		}
	}
	for _, line := range want {
		if !strings.Contains(document, line) {
			t.Errorf("the document holds no %q", line)
		}
	}
	names := measureNames(cards...)
	for _, w := range []windowRecord{before, after} {
		for _, at := range Branches() {
			heading := "### " + w.Name + ", `" + at.Label() + "` ("
			start := strings.Index(document, heading)
			if start < 0 {
				t.Errorf("the document holds no table of %s on %s", w.Name, at.Label())
				continue
			}
			table := document[start:]
			if end := strings.Index(table[len(heading):], "\n### "); end >= 0 {
				table = table[:len(heading)+end]
			}
			if !strings.Contains(table, tableHead("measure", coded(namesOf(record.SlotRules())))) {
				t.Errorf("the table of %s on %s does not head a column for every rule:\n%s", w.Name, at.Label(), table)
			}
			for _, name := range names {
				if !strings.Contains(table, "\n| "+name+" |") {
					t.Errorf("the table of %s on %s holds no row of %s", w.Name, at.Label(), name)
				}
			}
		}
	}
	t.Run("whole or in parts", func(t *testing.T) {
		dsn := os.Getenv("LEGION_TEST_PG_DSN")
		if dsn == "" {
			t.Skip(testdb.SkipMessage)
		}
		windows := []string{filepath.Join(fixtureWindowsDir, "review-and-test-kept"), filepath.Join(fixtureWindowsDir, "conflict-from-fitted-rate")}
		small := &runPlan{branches: []BranchAt{MainBranchAt}, breakEven: []float64{1.1}, perDraw: 2}
		config := CompareConfig{Windows: windows, Rules: []record.SlotRule{record.MainRule}, PGDSN: dsn, Parallel: 2, plan: small}
		whole := config
		whole.Runs = t.TempDir()
		if err := RunCompare(context.Background(), whole); err != nil {
			t.Fatal(err)
		}
		wholeRuns, err := ReadRuns(whole.Runs)
		if err != nil {
			t.Fatal(err)
		}
		wholeDocument, err := wholeRuns.Comparison()
		if err != nil {
			t.Fatal(err)
		}
		var parts []string
		for _, window := range []string{"review-and-test-kept", "conflict-from-fitted-rate"} {
			for _, rule := range []string{"main", "legion-v20.42.5"} {
				part := config
				part.Runs, part.OnlyWindow, part.OnlyRule = t.TempDir(), window, rule
				if err := RunCompare(context.Background(), part); err != nil {
					t.Fatal(err)
				}
				parts = append(parts, part.Runs)
			}
		}
		partRuns, err := ReadRuns(parts...)
		if err != nil {
			t.Fatal(err)
		}
		partDocument, err := partRuns.Comparison()
		if err != nil {
			t.Fatal(err)
		}
		if partDocument != wholeDocument {
			wholeLines, partLines := strings.Split(wholeDocument, "\n"), strings.Split(partDocument, "\n")
			for i := range max(len(wholeLines), len(partLines)) {
				var a, b string
				if i < len(wholeLines) {
					a = wholeLines[i]
				}
				if i < len(partLines) {
					b = partLines[i]
				}
				if a != b {
					t.Fatalf("the document written in parts differs from the one written whole at line %d:\nwhole: %s\nparts: %s", i+1, a, b)
				}
			}
		}
		for _, line := range []string{"## The rule named best (R7)", "### review-and-test-kept, `main@1`", "### conflict-from-fitted-rate, `main@1`",
			"- `legion-v20.42.5`, `main@1.1`"} {
			if !strings.Contains(wholeDocument, line) {
				t.Errorf("the document of the fixture windows holds no %q:\n%s", line, wholeDocument)
			}
		}
	})
}

// namesOf is the name of each of rules.
func namesOf(rules []record.SlotRule) []string {
	names := make([]string, 0, len(rules))
	for _, rule := range rules {
		names = append(names, rule.Name)
	}
	return names
}
