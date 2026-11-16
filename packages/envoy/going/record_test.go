package going_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/going"
)

// TestTheRecordReadsBack reads each entry the bake-off wrote back through DecisionRecord, by its
// model's name, with the thresholds the file holds, and holds the chosen entry to R7: one model is
// chosen, it was measured on at least 80 cases and 20 chart-worthy ones, it reaches 90% on the
// cases that name their action and 70% on the rest at a p95 under 500 ms, and its action
// threshold holds.
func TestTheRecordReadsBack(t *testing.T) {
	raw, err := os.ReadFile("decision_models.json")
	if err != nil {
		t.Fatal(err)
	}
	var file []struct {
		Model           string   `json:"model"`
		ActionThreshold *float64 `json:"action_threshold"`
		ChartThreshold  *float64 `json:"chart_threshold"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("decision_models.json: %v", err)
	}
	if len(file) == 0 {
		t.Fatal("the record holds no measured model")
	}
	chosen := 0
	for _, entry := range file {
		record, held := going.DecisionRecord(entry.Model)
		if !held {
			t.Errorf("DecisionRecord(%q) holds no entry, though the file has one", entry.Model)
			continue
		}
		if !sameThreshold(record.ActionThreshold, entry.ActionThreshold) || !sameThreshold(record.ChartThreshold, entry.ChartThreshold) {
			t.Errorf("%s reads back thresholds %v and %v, want the file's %v and %v", entry.Model,
				show(record.ActionThreshold), show(record.ChartThreshold), show(entry.ActionThreshold), show(entry.ChartThreshold))
		}
		for _, v := range []*float64{record.ActionThreshold, record.ChartThreshold} {
			if v != nil && (*v < 0 || *v > 1) {
				t.Errorf("%s holds the threshold %v, outside a confidence's 0 to 1", entry.Model, *v)
			}
		}
		if !record.Chosen {
			continue
		}
		chosen++
		switch {
		case record.ActionThreshold == nil:
			t.Errorf("the chosen %s has no action threshold", record.Model)
		case record.Cases < going.LeastCases || record.ChartWorthyCases < going.LeastChartCases:
			t.Errorf("the chosen %s was measured on %d cases, %d chart-worthy; R7 needs %d and %d",
				record.Model, record.Cases, record.ChartWorthyCases, going.LeastCases, going.LeastChartCases)
		case record.NamedAccuracy < going.NamedFloor || record.OtherAccuracy < going.OtherFloor || record.P95MS >= going.P95CeilingMS:
			t.Errorf("the chosen %s reaches %.3f named, %.3f on the rest at p95 %.0f ms; R7 needs %v, %v and under %v",
				record.Model, record.NamedAccuracy, record.OtherAccuracy, record.P95MS, going.NamedFloor, going.OtherFloor, going.P95CeilingMS)
		}
	}
	if chosen != 1 {
		t.Errorf("%d entries are chosen, want one", chosen)
	}
	if _, held := going.DecisionRecord("a-model-nobody-measured"); held {
		t.Error("an unmeasured model reads back an entry")
	}
}

// Each entry's repeats agree with its pooled numbers, each paired test is the mirror of the
// other model's, and the chosen entry is the one R7's rule names (ruleFault).
func TestTheRecordHoldsItsRepeatsAndTheRuleThatChose(t *testing.T) {
	records := committedRecords(t)
	entries := make(map[string]*going.Record, len(records))
	for i := range records {
		entries[records[i].Model] = &records[i]
	}
	for i := range records {
		record := &records[i]
		if record.Repeats < 1 || len(record.RepeatRights) != record.Repeats {
			t.Errorf("%s holds %d repeats and %d repeat counts, want at least one and one count for each", record.Model, record.Repeats, len(record.RepeatRights))
			continue
		}
		named, other := 0, 0
		totals := make([]int, 0, record.Repeats)
		for _, counted := range record.RepeatRights {
			if counted.Total != counted.Named+counted.Other || counted.Named > record.NamedCases || counted.Other > record.OtherCases {
				t.Errorf("%s holds the repeat count %+v of %d named and %d other cases", record.Model, counted, record.NamedCases, record.OtherCases)
			}
			named, other = named+counted.Named, other+counted.Other
			totals = append(totals, counted.Total)
		}
		if !sameShare(record.NamedAccuracy, named, record.NamedCases*record.Repeats) || !sameShare(record.OtherAccuracy, other, record.OtherCases*record.Repeats) {
			t.Errorf("%s pools %d named and %d other right choices over %d repeats, but holds the accuracies %v and %v",
				record.Model, named, other, record.Repeats, record.NamedAccuracy, record.OtherAccuracy)
		}
		if want := going.SpreadOf(totals); record.TotalSpread != want {
			t.Errorf("%s holds the spread %+v, want %+v from its repeats", record.Model, record.TotalSpread, want)
		}
		for _, c := range record.InconsistentCases {
			if c.Of != record.Repeats || c.Right < 1 || c.Right >= c.Of {
				t.Errorf("%s holds %+v as a case right in some repeats and wrong in others", record.Model, c)
			}
		}
		if len(record.PairedTests) != len(records)-1 {
			t.Errorf("%s holds %d paired tests, want one against each of the %d other models", record.Model, len(record.PairedTests), len(records)-1)
		}
		for _, test := range record.PairedTests {
			against, known := entries[test.Against]
			if !known {
				t.Errorf("%s holds a test against %q, which the record does not hold", record.Model, test.Against)
				continue
			}
			mirror, held := against.PairedAgainst(record.Model)
			switch {
			case !held:
				t.Errorf("%s holds a test against %q, which holds none against it", record.Model, test.Against)
			case test.Better != mirror.Worse || test.Worse != mirror.Better || test.PValue != mirror.PValue || test.Real != mirror.Real:
				t.Errorf("%s against %s reads %+v, but the mirror reads %+v", record.Model, test.Against, test, mirror)
			case test.Better-test.Worse != record.Right()-against.Right():
				t.Errorf("%s against %s: %d pairs against %d, but the right choices differ by %d", record.Model, test.Against, test.Better, test.Worse, record.Right()-against.Right())
			}
		}
	}
	if err := ruleFault(records); err != nil {
		t.Error(err)
	}
	rule := going.Choose(records)
	t.Logf("R7's rule on the committed record names %q by %q; level: %v", rule.Model, rule.By, rule.Level)
	for i := range records {
		t.Logf("%s: %d right choices of %d, display fit %t, meets the floors %t", records[i].Model, records[i].Right(), records[i].Cases*records[i].Repeats, records[i].DisplayFit(), records[i].MeetsFloors())
	}
}

// The committed record is byte for byte what the bake-off writes for its own entries
// (going.EncodeRecords): a run's record goes in as the tool wrote it, with no formatter or hand
// edit between them, so every number in it is the run's own.
func TestTheRecordIsWhatTheBakeOffWrites(t *testing.T) {
	raw, err := os.ReadFile("decision_models.json")
	if err != nil {
		t.Fatal(err)
	}
	var records []going.Record
	if err := json.Unmarshal(raw, &records); err != nil {
		t.Fatalf("decision_models.json: %v", err)
	}
	written, err := going.EncodeRecords(records)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(raw, written) {
		return
	}
	committed, wrote := strings.Split(string(raw), "\n"), strings.Split(string(written), "\n")
	for i := range max(len(committed), len(wrote)) {
		if lineAt(committed, i) != lineAt(wrote, i) {
			t.Fatalf("line %d of the committed record is %q, but the bake-off writes %q", i+1, lineAt(committed, i), lineAt(wrote, i))
		}
	}
}

// The rule ranks the candidates by their right choices pooled over every repeat. A display fit
// separates candidates level on that count and nothing else does: not a lower p95, and not the
// order the models were measured in, so a tie on both keys names no model.
func TestTheRuleRanksByRightChoicesThenADisplayFit(t *testing.T) {
	lowP95 := func(r going.Record) going.Record { r.P95MS = 90; return r }
	for _, tc := range []struct {
		name      string
		records   []going.Record
		model, by string
		level     []string
	}{
		{
			name:    "the most right choices over every repeat, though another led one repeat with a display fit and a lower p95",
			records: []going.Record{lowP95(candidate("model-b", true, 102, 100)), candidate("model-a", false, 100, 103)},
			model:   "model-a", by: going.ChosenByRightChoices, level: []string{"model-a"},
		},
		{
			name:    "a display fit never outranks one more right choice",
			records: []going.Record{candidate("model-a", true, 200), candidate("model-b", false, 201)},
			model:   "model-b", by: going.ChosenByRightChoices, level: []string{"model-b"},
		},
		{
			name:    "a display fit separates a level count",
			records: []going.Record{lowP95(candidate("model-a", false, 101, 101)), candidate("model-b", true, 100, 102)},
			model:   "model-b", by: going.ChosenByDisplayFit, level: []string{"model-b"},
		},
		{
			name:    "level with a display fit each: a lower p95 names none",
			records: []going.Record{lowP95(candidate("model-a", true, 202)), candidate("model-b", true, 202)},
			level:   []string{"model-a", "model-b"},
		},
		{
			name:    "level with no display fit either: none",
			records: []going.Record{candidate("model-a", false, 202), lowP95(candidate("model-b", false, 202))},
			level:   []string{"model-a", "model-b"},
		},
		{
			name:    "three level, two with a display fit: the fit drops the third and names none of the two",
			records: []going.Record{candidate("model-a", true, 202), lowP95(candidate("model-b", false, 202)), candidate("model-c", true, 202)},
			level:   []string{"model-a", "model-c"},
		},
		{
			name:    "the one candidate",
			records: []going.Record{candidate("model-a", false, 90)},
			model:   "model-a", by: going.ChosenByRightChoices, level: []string{"model-a"},
		},
		{
			name:    "no record",
			records: nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := going.Choose(tc.records)
			if got.Model != tc.model || got.By != tc.by || got.Reason != "" || !slices.Equal(got.Level, tc.level) {
				t.Errorf("Choose names %q by %q, level %v; want %q by %q, level %v", got.Model, got.By, got.Level, tc.model, tc.by, tc.level)
			}
		})
	}
}

// Only a model that meets R7's three floors is a candidate: one that misses any of them is never
// named, however many right choices it made.
func TestOnlyAModelThatMeetsTheFloorsIsACandidate(t *testing.T) {
	for name, miss := range map[string]func(*going.Record){
		"named under 90%":    func(r *going.Record) { r.NamedAccuracy = 0.899 },
		"rest under 70%":     func(r *going.Record) { r.OtherAccuracy = 0.699 },
		"p95 at the ceiling": func(r *going.Record) { r.P95MS = going.P95CeilingMS },
	} {
		t.Run(name, func(t *testing.T) {
			strong := candidate("model-strong", true, 117)
			miss(&strong)
			if got := going.Choose([]going.Record{strong, candidate("model-weak", false, 100)}); got.Model != "model-weak" || got.By != going.ChosenByRightChoices {
				t.Errorf("Choose names %q by %q, want model-weak, the one model meeting the floors", got.Model, got.By)
			}
			if got := going.Choose([]going.Record{strong}); got.Model != "" || len(got.Level) != 0 {
				t.Errorf("with no model meeting the floors, Choose names %q, level %v", got.Model, got.Level)
			}
		})
	}
}

// An action fit is no floor: a model that meets the floors without one is a candidate and ranks
// as any other. Where it ranks first, the rule names no model, not the next one with a fit, and
// says why: that model has no action fit, and R5's repair is owed before any model is named. A
// person may not name one either. A model without a fit that ranks below the first stops nothing.
func TestATopModelWithNoActionFitNamesNone(t *testing.T) {
	noFit := func(r going.Record) going.Record { r.ActionThreshold = nil; return r }
	for _, tc := range []struct {
		name         string
		records      []going.Record
		level, unfit []string
	}{
		{
			name:    "the most right choices, though the second has a fit",
			records: []going.Record{noFit(candidate("model-strong", true, 117)), candidate("model-weak", true, 100)},
			level:   []string{"model-strong"}, unfit: []string{"model-strong"},
		},
		{
			name:    "first by the display fit at a level count, though the other has an action fit",
			records: []going.Record{candidate("model-a", false, 101), noFit(candidate("model-b", true, 101))},
			level:   []string{"model-b"}, unfit: []string{"model-b"},
		},
		{
			name:    "level on both keys, one with no action fit",
			records: []going.Record{candidate("model-a", true, 101), noFit(candidate("model-b", true, 101))},
			level:   []string{"model-a", "model-b"}, unfit: []string{"model-b"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := going.Choose(tc.records)
			if got.Model != "" || got.By != "" || !slices.Equal(got.Level, tc.level) || !slices.Equal(got.Unfit, tc.unfit) {
				t.Errorf("Choose names %q by %q, level %v, unfit %v; want none, level %v, unfit %v", got.Model, got.By, got.Level, got.Unfit, tc.level, tc.unfit)
			}
			if !strings.Contains(got.Why, tc.unfit[0]) || !strings.Contains(got.Why, "action fit") || !strings.Contains(got.Why, "R5") {
				t.Errorf("Choose says %q, which does not name %s, its missing action fit and R5's repair", got.Why, tc.unfit[0])
			}
			for _, model := range tc.level {
				if broken, err := got.Break(model, "a reason"); err == nil {
					t.Errorf("a tie-break names %q by %q", broken.Model, broken.By)
				}
			}
		})
	}
	below := going.Choose([]going.Record{candidate("model-a", true, 102), noFit(candidate("model-b", true, 101))})
	if below.Model != "model-a" || below.By != going.ChosenByRightChoices || len(below.Unfit) != 0 {
		t.Errorf("with a model without a fit ranked second, Choose names %q by %q, unfit %v; want model-a by right choices", below.Model, below.By, below.Unfit)
	}
}

// The architect names a model only where the rule names none: one of the models level on right
// choices and on the display fit, with a reason.
func TestATieBreakIsTakenOnlyOnAFullTie(t *testing.T) {
	tie := going.Choose([]going.Record{candidate("model-a", true, 101), candidate("model-b", true, 101)})
	broken, err := tie.Break("model-b", "the cheaper of two level models")
	if err != nil || broken.Model != "model-b" || broken.By != going.ChosenByPerson || broken.Reason != "the cheaper of two level models" {
		t.Fatalf("a full tie broken for model-b reads %+v, %v", broken, err)
	}
	for name, refused := range map[string]func() (going.Choice, error){
		"a run the right choices decide": func() (going.Choice, error) {
			return going.Choose([]going.Record{candidate("model-a", true, 102), candidate("model-b", true, 101)}).Break("model-b", "a reason")
		},
		"a run the display fit decides": func() (going.Choice, error) {
			return going.Choose([]going.Record{candidate("model-a", true, 101), candidate("model-b", false, 101)}).Break("model-b", "a reason")
		},
		"a model outside the tie":     func() (going.Choice, error) { return tie.Break("model-c", "a reason") },
		"an empty reason":             func() (going.Choice, error) { return tie.Break("model-a", "") },
		"a reason of spaces":          func() (going.Choice, error) { return tie.Break("model-a", "  ") },
		"a tie already broken":        func() (going.Choice, error) { return broken.Break("model-a", "a reason") },
		"no model meeting the floors": func() (going.Choice, error) { return going.Choose(nil).Break("model-a", "a reason") },
	} {
		if got, err := refused(); err == nil {
			t.Errorf("%s: the tie-break is taken, naming %q by %q", name, got.Model, got.By)
		}
	}
}

// A record whose chosen model is not the one R7's rule names fails the check the committed
// record is held to, however it says the model was chosen; a person's choice passes only where
// the record itself shows the full tie on both keys and keeps a reason.
func TestARecordTheRuleDidNotNameFails(t *testing.T) {
	chose := func(records []going.Record, model, by, reason string) []going.Record {
		for i := range records {
			if records[i].Model == model {
				records[i].Chosen, records[i].ChosenBy, records[i].ChosenReason = true, by, reason
			}
		}
		return records
	}
	decided := func() []going.Record {
		return []going.Record{candidate("model-a", true, 103), candidate("model-b", true, 101)}
	}
	byFit := func() []going.Record {
		return []going.Record{candidate("model-a", true, 101), candidate("model-b", false, 101)}
	}
	level := func() []going.Record {
		return []going.Record{candidate("model-a", true, 101), candidate("model-b", true, 101)}
	}
	unfitFirst := func() []going.Record {
		first := candidate("model-a", true, 103)
		first.ActionThreshold = nil
		return []going.Record{first, candidate("model-b", true, 101)}
	}
	for _, tc := range []struct {
		name    string
		records []going.Record
		passes  bool
	}{
		{"the rule's model by the right choices", chose(decided(), "model-a", going.ChosenByRightChoices, ""), true},
		{"the rule's model by the display fit", chose(byFit(), "model-a", going.ChosenByDisplayFit, ""), true},
		{"a full tie the architect broke, with a reason", chose(level(), "model-b", going.ChosenByPerson, "the cheaper of two level models"), true},
		{"a hand-chosen model where the rule names another", chose(decided(), "model-b", going.ChosenByPerson, "it reads better"), false},
		{"a hand-chosen model where the display fit names another", chose(byFit(), "model-b", going.ChosenByPerson, "it reads better"), false},
		{"another model said to be the rule's", chose(decided(), "model-b", going.ChosenByRightChoices, ""), false},
		{"the rule's model said to be a person's", chose(decided(), "model-a", going.ChosenByPerson, "it reads better"), false},
		{"the rule's model by a key that did not name it", chose(decided(), "model-a", going.ChosenByDisplayFit, ""), false},
		{"the rule's model by a paired test's verdict", chose(decided(), "model-a", "noise", ""), false},
		{"the rule's model with a reason no person gave", chose(decided(), "model-a", going.ChosenByRightChoices, "a reason"), false},
		{"a full tie broken with no reason", chose(level(), "model-b", going.ChosenByPerson, " "), false},
		{"a full tie said to be the rule's", chose(level(), "model-b", going.ChosenByRightChoices, ""), false},
		{"no model chosen", decided(), false},
		{"two models chosen", chose(chose(decided(), "model-a", going.ChosenByRightChoices, ""), "model-b", going.ChosenByRightChoices, ""), false},
		{"the next model where the first has no action fit", chose(unfitFirst(), "model-b", going.ChosenByRightChoices, ""), false},
		{"a person's model where the first has no action fit", chose(unfitFirst(), "model-b", going.ChosenByPerson, "it can act"), false},
		{"the first, though it has no action fit", chose(unfitFirst(), "model-a", going.ChosenByRightChoices, ""), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ruleFault(tc.records); (err == nil) != tc.passes {
				t.Errorf("ruleFault reads %v; want it to pass: %t", err, tc.passes)
			}
		})
	}
}

// A record entry with every field set reads back through JSON as it was written: no two fields
// share a name, so the fields the server reads (the model, both thresholds, chosen) come back
// beside every field a repeated run adds.
func TestARecordEntryRoundTrips(t *testing.T) {
	action, chart := 0.54, 0.32
	want := going.Record{
		Model: "demo-model", Endpoint: "https://decide.example",
		ActionThreshold: &action, ActionShare: 0.98, ChartThreshold: &chart, ChartShare: 1,
		MeasuredAt: "2026-11-16T12:00:00Z", ReleaseDate: "2026-10-22T02:00:00+00:00",
		Cases: 117, NamedCases: 45, OtherCases: 72, ChartWorthyCases: 25, Unanswered: 1,
		NamedAccuracy: 0.97, OtherAccuracy: 0.99, P50MS: 108.8, P95MS: 178.2,
		DisplayRates:      []going.DisplayRate{{Label: going.LabelChartWorthy, Answered: going.DisplayChart, Count: 50, Of: 50, Rate: 1}},
		Repeats:           2,
		RepeatRights:      []going.RepeatRight{{Named: 44, Other: 71, Total: 115}, {Named: 45, Other: 71, Total: 116}},
		TotalSpread:       going.Spread{Mean: 115.5, Min: 115, Max: 116, SD: 0.7071},
		InconsistentCases: []going.CaseConsistency{{Words: "close DEMO-12", Right: 1, Of: 2}},
		PairedTests: []going.PairedTest{{
			Against: "other-model", Pairs: 234, Better: 1, Worse: 3, PValue: 0.625, MeanDifference: -1, Interval: &[2]float64{-13.7, 11.7},
		}},
		Chosen: true, ChosenBy: going.ChosenByPerson, ChosenReason: "the cheaper of two level models",
	}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got going.Record
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the entry reads back as\n%+v\nwant\n%+v", got, want)
	}
}

// committedRecords is the committed record's entries.
func committedRecords(t *testing.T) []going.Record {
	t.Helper()
	raw, err := os.ReadFile("decision_models.json")
	if err != nil {
		t.Fatal(err)
	}
	var records []going.Record
	if err := json.Unmarshal(raw, &records); err != nil {
		t.Fatalf("decision_models.json: %v", err)
	}
	return records
}

// ruleFault is what is wrong with a record's choice, nil where nothing is: one entry is chosen,
// and it is the model R7's rule names (going.Choose) by the key that named it, or, where the
// rule names none, the model a person named among the level ones with the reason kept
// (going.Choice.Break). An entry not chosen holds no way it was chosen and no reason.
func ruleFault(records []going.Record) error {
	var chosen *going.Record
	for i := range records {
		r := &records[i]
		switch {
		case r.Chosen && chosen != nil:
			return fmt.Errorf("%s and %s are both chosen", chosen.Model, r.Model)
		case r.Chosen:
			chosen = r
		case r.ChosenBy != "" || r.ChosenReason != "":
			return fmt.Errorf("%s is not chosen, yet holds chosen_by %q and chosen_reason %q", r.Model, r.ChosenBy, r.ChosenReason)
		}
	}
	if chosen == nil {
		return errors.New("no entry is chosen")
	}
	want := going.Choose(records)
	if chosen.ChosenBy == going.ChosenByPerson {
		broken, err := want.Break(chosen.Model, chosen.ChosenReason)
		if err != nil {
			return fmt.Errorf("a person chose %s, which the record does not bear out: %w", chosen.Model, err)
		}
		want = broken
	}
	if chosen.Model != want.Model || chosen.ChosenBy != want.By || chosen.ChosenReason != want.Reason {
		return fmt.Errorf("%s is chosen by %q with the reason %q, but R7's rule names %q by %q (level: %v)",
			chosen.Model, chosen.ChosenBy, chosen.ChosenReason, want.Model, want.By, want.Level)
	}
	return nil
}

// candidate is a record of a model that meets R7's floors with an action fit, measured on 117
// cases, with its right choices in each repeat and a display fit or none.
func candidate(model string, fit bool, rights ...int) going.Record {
	action, chart := 0.5, 0.1
	r := going.Record{Model: model, ActionThreshold: &action, Cases: 117, NamedAccuracy: 0.95, OtherAccuracy: 0.9, P95MS: 150, Repeats: len(rights)}
	for _, right := range rights {
		r.RepeatRights = append(r.RepeatRights, going.RepeatRight{Other: right, Total: right})
	}
	if fit {
		r.ChartThreshold = &chart
	}
	return r
}

func lineAt(lines []string, i int) string {
	if i < len(lines) {
		return lines[i]
	}
	return "(past the end)"
}

// sameShare is whether share is right of n, as the bake-off divides it.
func sameShare(share float64, right, n int) bool {
	return n > 0 && share == float64(right)/float64(n)
}

func sameThreshold(a, b *float64) bool {
	return (a == nil) == (b == nil) && (a == nil || *a == *b)
}

func show(v *float64) any {
	if v == nil {
		return "null"
	}
	return *v
}
